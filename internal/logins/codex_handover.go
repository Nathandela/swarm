package logins

import (
	"encoding/json"
	"slices"
	"strings"
	"time"
	"unicode"
)

const (
	MaxHandoverMembers   = 128
	MaxHandoverGrantRefs = 32
	maxHandoverBytes     = 256 << 10
)

// NativeMember is an exact native incarnation, never a display/status identity.
// The coordinator proves retirement against PID/start, not idle or disconnection.
type NativeMember struct {
	SessionID         string `json:"session_id"`
	NativeIncarnation string `json:"native_incarnation"`
	ConversationID    string `json:"conversation_id,omitempty"`
	NativePID         int    `json:"native_pid"`
	NativeStartTime   int64  `json:"native_start_time"`
}
type HandoverMember struct {
	Native                    NativeMember `json:"native"`
	Phase                     string       `json:"phase"`
	AdoptedID                 string       `json:"adopted_id,omitempty"`
	AdoptedEpoch              uint64       `json:"adopted_epoch,omitempty"`
	AdoptedCredentialRevision uint64       `json:"adopted_credential_revision,omitempty"`
}

// Handover retains one unresolved membership and authority set. ActiveID/Epoch
// describe the desired provider selection; only exact receipts prove adoption.
type Handover struct {
	MigrationSwitchID             string                `json:"migration_switch_id,omitempty"`
	Bootstrap                     []BootstrapObligation `json:"bootstrap,omitempty"`
	Version                       int                   `json:"version"`
	ID                            string                `json:"id"`
	Provider                      string                `json:"provider"`
	FromID                        string                `json:"from_id,omitempty"`
	ToID                          string                `json:"to_id"`
	Epoch                         uint64                `json:"epoch"`
	DestinationCredentialRevision uint64                `json:"destination_credential_revision"`
	Phase                         string                `json:"phase"`
	Members                       []HandoverMember      `json:"members"`
	HeldLoginIDs                  []string              `json:"held_login_ids"`
}
type HandoverReceipt struct {
	HandoverID                    string
	ToID                          string
	Epoch                         uint64
	DestinationCredentialRevision uint64
	Native                        NativeMember
	Phase                         string
}

func boundedNativeID(v string, empty bool) bool {
	if v == "" {
		return empty
	}
	if len(v) > 256 || strings.TrimSpace(v) != v {
		return false
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func validNativeMember(n NativeMember) bool {
	return boundedNativeID(n.SessionID, false) && boundedNativeID(n.NativeIncarnation, false) && boundedNativeID(n.ConversationID, true) && n.NativePID > 0 && n.NativeStartTime > 0
}
func adoptionPhase(members []HandoverMember) string {
	for _, m := range members {
		if m.Phase != "adopted" && m.Phase != "retired" {
			return "adopting"
		}
	}
	return "adopted"
}
func schemaForHandovers(r *Registry) {
	if r.SchemaVersion < 3 {
		r.SchemaVersion = 3
	}
	if r.Handovers == nil {
		r.Handovers = make(map[string]Handover)
	}
}
func validHandover(r Registry, h Handover) bool {
	if !validBootstrap(r, h) || !validID(h.ID) || h.Provider != ProviderCodex || !codexOwned(r, ProviderCodex) || h.Epoch == 0 || h.DestinationCredentialRevision == 0 || len(h.Members) > MaxHandoverMembers || len(h.HeldLoginIDs) == 0 || len(h.HeldLoginIDs) > MaxHandoverGrantRefs {
		return false
	}
	p := r.Providers[ProviderCodex]
	to := find(&r, h.ToID)
	if to == nil || to.Provider != ProviderCodex || to.CredentialRevision != h.DestinationCredentialRevision || p.ActiveID != h.ToID || p.Epoch != h.Epoch || p.DesiredCredentialRevision != h.DestinationCredentialRevision {
		return false
	}
	if h.FromID != "" {
		from := find(&r, h.FromID)
		if from == nil || from.Provider != ProviderCodex {
			return false
		}
	}
	held := make(map[string]bool, len(h.HeldLoginIDs))
	for _, id := range h.HeldLoginIDs {
		l := find(&r, id)
		if held[id] || l == nil || l.Provider != ProviderCodex {
			return false
		}
		held[id] = true
	}
	if !held[h.ToID] || h.FromID != "" && !held[h.FromID] {
		return false
	}
	seen := make(map[[2]string]bool, len(h.Members))
	for _, m := range h.Members {
		key := [2]string{m.Native.SessionID, m.Native.NativeIncarnation}
		if !validNativeMember(m.Native) || seen[key] {
			return false
		}
		seen[key] = true
		switch m.Phase {
		case "pending", "acknowledged", "adopted", "retired":
		default:
			return false
		}
		if m.AdoptedID == "" {
			if m.AdoptedEpoch != 0 || m.AdoptedCredentialRevision != 0 {
				return false
			}
		} else if !held[m.AdoptedID] || m.AdoptedEpoch == 0 || m.AdoptedEpoch > h.Epoch || m.AdoptedCredentialRevision == 0 {
			return false
		}
		if m.Phase == "adopted" && (m.AdoptedID != h.ToID || m.AdoptedEpoch != h.Epoch || m.AdoptedCredentialRevision != h.DestinationCredentialRevision) {
			return false
		}
	}
	if h.Phase != handoverPhase(h) {
		return false
	}
	encoded, e := json.Marshal(h)
	return e == nil && len(encoded) <= maxHandoverBytes
}
func (s *Store) handoverDestination(r *Registry, toID string) (*Login, error) {
	l := find(r, toID)
	if l == nil || l.Provider != ProviderCodex || !eligible(*l, time.Now()) {
		return nil, ErrIneligible
	}
	saved, e := s.readSavedRecord(*l)
	if e != nil {
		return nil, e
	}
	if _, e = s.readCodexGrant(*l, saved); e != nil {
		return nil, e
	}
	return l, nil
}
func (s *Store) BeginCodexHandover(expected uint64, toID string, members []NativeMember) (Registry, Handover, error) {
	return s.beginCodexHandover(expected, toID, members, false)
}

// BootstrapCodexHandover upgrades an unknown legacy baseline through an explicit
// new epoch and exact pending cohort; it never infers native adoption.
func (s *Store) BootstrapCodexHandover(expected uint64, members []NativeMember) (Registry, Handover, error) {
	return s.beginCodexHandover(expected, "", members, true)
}
func (s *Store) beginCodexHandover(expected uint64, toID string, members []NativeMember, bootstrap bool) (Registry, Handover, error) {
	var out Handover
	r, e := s.mutate(expected, func(r *Registry) error {
		if !codexOwned(*r, ProviderCodex) || r.MigrationPending {
			return ErrPending
		}
		if _, ok := r.Switches[ProviderCodex]; ok {
			return ErrPending
		}
		if _, ok := r.Handovers[ProviderCodex]; ok {
			return ErrPending
		}
		if active := r.Providers[ProviderCodex].ActiveID; active != "" {
			if e := s.noGrantRelogin(active); e != nil {
				return e
			}
		}
		if len(members) > MaxHandoverMembers {
			return ErrCapacity
		}
		if bootstrap {
			p := r.Providers[ProviderCodex]
			if p.ActiveID == "" || p.DesiredCredentialRevision != 0 {
				return ErrConflict
			}
			toID = p.ActiveID
		}
		destination, e := s.handoverDestination(r, toID)
		if e != nil {
			return e
		}
		p := r.Providers[ProviderCodex]
		if p.Epoch == ^uint64(0) {
			return ErrIneligible
		}
		if p.ActiveID == toID {
			if p.DesiredCredentialRevision == 0 && !bootstrap {
				return ErrPending
			}
			if p.DesiredCredentialRevision == destination.CredentialRevision {
				return ErrConflict
			}
		}
		id, e := newID()
		if e != nil {
			return e
		}
		out = Handover{Version: 1, ID: id, Provider: ProviderCodex, FromID: p.ActiveID, ToID: toID, Epoch: p.Epoch + 1, DestinationCredentialRevision: destination.CredentialRevision, Members: make([]HandoverMember, len(members))}
		for i, n := range members {
			out.Members[i] = HandoverMember{Native: n, Phase: "pending"}
		}
		if p.ActiveID != "" {
			out.HeldLoginIDs = append(out.HeldLoginIDs, p.ActiveID)
		}
		if !slices.Contains(out.HeldLoginIDs, toID) {
			out.HeldLoginIDs = append(out.HeldLoginIDs, toID)
		}
		out.Phase = adoptionPhase(out.Members)
		if e = bootstrapCapacity(out); e != nil {
			return e
		}
		schemaForHandovers(r)
		p.ActiveID = toID
		p.Epoch = out.Epoch
		p.DesiredCredentialRevision = destination.CredentialRevision
		r.Providers[ProviderCodex] = p
		if !validHandover(*r, out) {
			return ErrIneligible
		}
		r.Handovers[ProviderCodex] = out
		return nil
	})
	return r, out, e
}
func (s *Store) RetargetCodexHandover(expected uint64, id, toID string) (Registry, Handover, error) {
	var out Handover
	r, e := s.mutate(expected, func(r *Registry) error {
		h, ok := r.Handovers[ProviderCodex]
		if ok {
			if e := s.noGrantRelogin(h.ToID); e != nil {
				return e
			}
		}
		if !ok || h.ID != id {
			return ErrConflict
		}
		destination, e := s.handoverDestination(r, toID)
		if e != nil {
			return e
		}
		if h.ToID == toID && h.DestinationCredentialRevision == destination.CredentialRevision {
			return ErrConflict
		}
		if h.Epoch == ^uint64(0) {
			return ErrIneligible
		}
		if !slices.Contains(h.HeldLoginIDs, toID) {
			if len(h.HeldLoginIDs) >= MaxHandoverGrantRefs {
				return ErrCapacity
			}
			h.HeldLoginIDs = append(h.HeldLoginIDs, toID)
		}
		h.ToID = toID
		h.Epoch++
		h.DestinationCredentialRevision = destination.CredentialRevision
		for i := range h.Members {
			if h.Members[i].Phase != "retired" {
				h.Members[i].Phase = "pending"
			}
		}
		h.Phase = handoverPhase(h)
		if e = bootstrapCapacity(h); e != nil {
			return e
		}
		p := r.Providers[ProviderCodex]
		p.ActiveID = toID
		p.Epoch = h.Epoch
		p.DesiredCredentialRevision = destination.CredentialRevision
		r.Providers[ProviderCodex] = p
		r.Handovers[ProviderCodex] = h
		out = h
		return nil
	})
	return r, out, e
}
func (s *Store) RecordCodexReceipt(expected uint64, receipt HandoverReceipt) (Registry, Handover, error) {
	var out Handover
	r, e := s.mutate(expected, func(r *Registry) error {
		h, ok := r.Handovers[ProviderCodex]
		if ok {
			if e := s.noGrantRelogin(h.ToID); e != nil {
				return e
			}
		}
		if !ok || receipt.HandoverID != h.ID || receipt.ToID != h.ToID || receipt.Epoch != h.Epoch || receipt.DestinationCredentialRevision != h.DestinationCredentialRevision {
			return ErrConflict
		}
		if receipt.Phase != "acknowledged" && receipt.Phase != "adopted" && receipt.Phase != "retired" {
			return ErrIneligible
		}
		index := -1
		for i, m := range h.Members {
			if m.Native == receipt.Native {
				index = i
				break
			}
		}
		if index < 0 {
			return ErrConflict
		}
		member := &h.Members[index]
		if member.Phase == "retired" && receipt.Phase != "retired" {
			return ErrConflict
		}
		if receipt.Phase == "adopted" {
			member.AdoptedID = h.ToID
			member.AdoptedEpoch = h.Epoch
			member.AdoptedCredentialRevision = h.DestinationCredentialRevision
		}
		if !(member.Phase == "adopted" && receipt.Phase == "acknowledged") {
			member.Phase = receipt.Phase
		}
		h.Phase = handoverPhase(h)
		r.Handovers[ProviderCodex] = h
		out = h
		return nil
	})
	return r, out, e
}
func (s *Store) CompleteCodexHandover(expected uint64, id string) (Registry, error) {
	return s.mutate(expected, func(r *Registry) error {
		h, ok := r.Handovers[ProviderCodex]
		if ok {
			if e := s.noGrantRelogin(h.ToID); e != nil {
				return e
			}
		}
		if !ok || h.ID != id {
			return ErrConflict
		}
		if h.Phase != "adopted" {
			return ErrPending
		}
		delete(r.Handovers, ProviderCodex)
		return nil
	})
}

// Reserve the largest permitted receipt record before selecting a destination,
// including its map envelope; later acknowledgements must still fit the registry.
func reserveHandoverSpace(r Registry) error {
	if len(r.Handovers) == 0 {
		return nil
	}
	base := r
	base.Handovers = nil
	encoded, e := json.Marshal(base)
	if e != nil {
		return e
	}
	if len(encoded)+maxHandoverBytes+64 > maxRegistryBytes {
		return ErrCapacity
	}
	return nil
}
