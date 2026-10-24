package logins

import (
	"encoding/json"
	"slices"
	"strings"
)

// BootstrapChild binds a migration's persisted source/child lineage to an exact
// native incarnation. A retired binding promises PID/start is proven ended.
type BootstrapChild struct {
	SourceID string
	Native   NativeMember
	Phase    string
}
type BootstrapLaunch struct {
	ID                            string `json:"id"`
	ToID                          string `json:"to_id"`
	Epoch                         uint64 `json:"epoch"`
	DestinationCredentialRevision uint64 `json:"destination_credential_revision"`
}
type BootstrapObligation struct {
	SourceID         string           `json:"source_id"`
	ConversationID   string           `json:"conversation_id"`
	ChildID          string           `json:"child_id,omitempty"`
	ChildIncarnation string           `json:"child_incarnation,omitempty"`
	Phase            string           `json:"phase"`
	Launch           *BootstrapLaunch `json:"launch,omitempty"`
}

func bootstrapSettled(rows []BootstrapObligation) bool {
	for _, b := range rows {
		if b.Phase != "ready" && b.Phase != "failed" && b.Phase != "cancelled" {
			return false
		}
	}
	return true
}
func handoverPhase(h Handover) string {
	if !bootstrapSettled(h.Bootstrap) {
		return "adopting"
	}
	return adoptionPhase(h.Members)
}
func bootstrapCapacity(h Handover) error {
	if len(h.Bootstrap) > MaxHandoverMembers {
		return ErrCapacity
	}
	// Reserve the largest child tuple and claim before any launch can happen.
	future := h
	future.Epoch = ^uint64(0)
	future.DestinationCredentialRevision = ^uint64(0)
	future.Phase = "adopting"
	future.Members = slices.Clone(h.Members)
	future.Bootstrap = slices.Clone(h.Bootstrap)
	for i, b := range future.Bootstrap {
		if b.ChildID != "" || b.Phase == "ready" || b.Phase == "failed" || b.Phase == "cancelled" {
			continue
		}
		future.Members = append(future.Members, HandoverMember{Native: NativeMember{SessionID: strings.Repeat("<", 256), NativeIncarnation: strings.Repeat("<", 256), ConversationID: b.ConversationID, NativePID: int(^uint(0) >> 1), NativeStartTime: int64(^uint64(0) >> 1)}, Phase: "pending"})
		future.Bootstrap[i].ChildID = strings.Repeat("<", 256)
		future.Bootstrap[i].ChildIncarnation = strings.Repeat("<", 256)
		future.Bootstrap[i].Phase = "cancelled"
		if b.Launch == nil {
			future.Bootstrap[i].Launch = &BootstrapLaunch{ID: strings.Repeat("a", 32), ToID: h.ToID, Epoch: ^uint64(0), DestinationCredentialRevision: ^uint64(0)}
		}
	}
	if len(future.Members) > MaxHandoverMembers {
		return ErrCapacity
	}
	for i := range future.Members {
		future.Members[i].Phase = "acknowledged"
		future.Members[i].AdoptedID = h.ToID
		future.Members[i].AdoptedEpoch = ^uint64(0)
		future.Members[i].AdoptedCredentialRevision = ^uint64(0)
	}
	encoded, e := json.Marshal(future)
	if e != nil {
		return e
	}
	if len(encoded) > maxHandoverBytes {
		return ErrCapacity
	}
	return nil
}
func validBootstrap(r Registry, h Handover) bool {
	if h.Version == 1 {
		return len(h.Bootstrap) == 0 && h.MigrationSwitchID == ""
	}
	if h.Version != 2 || r.SchemaVersion < 4 || !validID(h.MigrationSwitchID) || len(h.Bootstrap) > MaxHandoverMembers {
		return false
	}
	seen := map[string]bool{}
	children := map[[2]string]bool{}
	launches := map[string]bool{}
	for _, b := range h.Bootstrap {
		if !boundedNativeID(b.SourceID, false) || !boundedNativeID(b.ConversationID, false) || seen[b.SourceID] {
			return false
		}
		seen[b.SourceID] = true
		switch b.Phase {
		case "pending", "ambiguous", "launching", "bound", "ready", "failed", "cancelled":
		default:
			return false
		}
		if b.Launch != nil {
			l := b.Launch
			if !validID(l.ID) || launches[l.ID] || !slices.Contains(h.HeldLoginIDs, l.ToID) || l.Epoch == 0 || l.Epoch > h.Epoch || l.DestinationCredentialRevision == 0 {
				return false
			}
			launches[l.ID] = true
		}
		if b.ChildID == "" {
			if b.ChildIncarnation != "" || b.Phase == "bound" || b.Phase == "ready" || b.Phase == "launching" && b.Launch == nil || b.Launch != nil && b.Phase != "launching" {
				return false
			}
			continue
		}
		if b.Phase == "pending" || b.Phase == "ambiguous" || b.Phase == "launching" {
			return false
		}
		key := [2]string{b.ChildID, b.ChildIncarnation}
		if children[key] {
			return false
		}
		children[key] = true
		found := false
		for _, m := range h.Members {
			if m.Native.SessionID == b.ChildID && m.Native.NativeIncarnation == b.ChildIncarnation && m.Native.ConversationID == b.ConversationID {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return bootstrapCapacity(h) == nil
}

// TransferCodexMigration atomically moves all original restart obligations into
// the single live handover. The caller fences admission and supplies ALL live
// external incarnations, not merely migration children. Store CAS cannot prove
// host inventory completeness or process lineage; the coordinator must do both.
func (s *Store) TransferCodexMigration(expected uint64, switchID, toID string, members []NativeMember, children []BootstrapChild) (Registry, Handover, error) {
	var out Handover
	r, e := s.mutate(expected, func(r *Registry) error {
		op, ok := r.Switches[ProviderCodex]
		if !ok || op.ID != switchID || !op.OwnershipMigration || (op.Phase != "committed" && op.Phase != "resuming") || !codexOwned(*r, ProviderCodex) {
			return ErrPending
		}
		if _, ok = r.Handovers[ProviderCodex]; ok || r.MigrationPending {
			return ErrPending
		}
		if e := s.noGrantRelogin(op.ToID); e != nil {
			return e
		}
		p := r.Providers[ProviderCodex]
		if p.ActiveID != op.ToID || p.Epoch != op.ToEpoch {
			return ErrConflict
		}
		destination, e := s.handoverDestination(r, toID)
		if e != nil {
			return e
		}
		// An unknown legacy baseline is rebuilt through this explicit fresh
		// selection, never treated as matching the current saved revision.
		if p.ActiveID == toID && p.DesiredCredentialRevision != 0 {
			if p.DesiredCredentialRevision == destination.CredentialRevision {
				// New migrations transfer before any legacy resume, then use the
				// bootstrap's durable per-source launch claim for every child.
				if op.Phase != "committed" {
					return ErrConflict
				}
			} else if destination.LastLoginJobID == "" {
				return ErrConflict
			}
		}
		if p.Epoch == ^uint64(0) {
			return ErrIneligible
		}
		if len(members) > MaxHandoverMembers || len(op.Sessions) > MaxHandoverMembers || len(children) > MaxHandoverMembers {
			return ErrCapacity
		}
		id, e := newID()
		if e != nil {
			return e
		}
		out = Handover{Version: 2, ID: id, Provider: ProviderCodex, FromID: p.ActiveID, ToID: toID, Epoch: p.Epoch + 1, DestinationCredentialRevision: destination.CredentialRevision, MigrationSwitchID: op.ID}
		for _, n := range members {
			out.Members = append(out.Members, HandoverMember{Native: n, Phase: "pending"})
		}
		bindings := map[string]BootstrapChild{}
		for _, c := range children {
			if _, ok := bindings[c.SourceID]; ok {
				return ErrConflict
			}
			bindings[c.SourceID] = c
		}
		for _, row := range op.Sessions {
			b := BootstrapObligation{SourceID: row.SourceID, ConversationID: row.ConversationID, ChildID: row.ChildID, Phase: "pending"}
			switch row.Phase {
			case "ready", "failed", "cancelled":
				b.Phase = row.Phase
			}
			c, recovered := bindings[row.SourceID]
			if row.ChildID != "" || recovered {
				if !recovered || row.ChildID != "" && c.Native.SessionID != row.ChildID || c.Native.ConversationID != row.ConversationID || !validNativeMember(c.Native) {
					return ErrConflict
				}
				b.ChildID = c.Native.SessionID
				b.ChildIncarnation = c.Native.NativeIncarnation
				if b.Phase == "pending" {
					b.Phase = "bound"
				}
				present := false
				for _, m := range out.Members {
					if m.Native == c.Native {
						present = true
					}
				}
				if c.Phase == "pending" {
					if !present {
						return ErrConflict
					}
				} else if c.Phase == "retired" {
					if present {
						return ErrConflict
					}
					out.Members = append(out.Members, HandoverMember{Native: c.Native, Phase: "retired"})
				} else {
					return ErrIneligible
				}
				delete(bindings, row.SourceID)
			} else if (op.Phase == "resuming" || row.Phase != "stopped") && b.Phase == "pending" {
				// Without per-source claims, only stopped rows in a never-resumed
				// journal prove a restart has not been attempted.
				b.Phase = "ambiguous"
			}
			out.Bootstrap = append(out.Bootstrap, b)
		}
		if len(bindings) != 0 {
			return ErrConflict
		}
		for _, held := range []string{op.FromID, op.ToID, toID} {
			if held != "" && !slices.Contains(out.HeldLoginIDs, held) {
				out.HeldLoginIDs = append(out.HeldLoginIDs, held)
			}
		}
		if e = bootstrapCapacity(out); e != nil {
			return e
		}
		out.Phase = handoverPhase(out)
		schemaForHandovers(r)
		r.SchemaVersion = 4
		p.ActiveID = toID
		p.Epoch = out.Epoch
		p.DesiredCredentialRevision = destination.CredentialRevision
		r.Providers[ProviderCodex] = p
		delete(r.Switches, ProviderCodex)
		r.Handovers[ProviderCodex] = out
		if !validHandover(*r, out) {
			return ErrIneligible
		}
		return nil
	})
	return r, out, e
}
func (s *Store) bootstrapMutation(expected uint64, id, source string, change func(*Handover, *BootstrapObligation) error) (Registry, Handover, error) {
	var out Handover
	r, e := s.mutate(expected, func(r *Registry) error {
		h, ok := r.Handovers[ProviderCodex]
		if !ok || h.ID != id || h.Version != 2 {
			return ErrConflict
		}
		if e := s.noGrantRelogin(h.ToID); e != nil {
			return e
		}
		index := -1
		for i, b := range h.Bootstrap {
			if b.SourceID == source {
				index = i
				break
			}
		}
		if index < 0 {
			return ErrConflict
		}
		if e := change(&h, &h.Bootstrap[index]); e != nil {
			return e
		}
		if e := bootstrapCapacity(h); e != nil {
			return e
		}
		h.Phase = handoverPhase(h)
		r.Handovers[ProviderCodex] = h
		out = h
		return nil
	})
	return r, out, e
}

// ClaimCodexBootstrapLaunch authorizes a launch only on its first successful
// transition. The same persisted nonce returns ErrPending, never permission to replay.
func (s *Store) ClaimCodexBootstrapLaunch(expected uint64, id, source, launchID string) (Registry, Handover, error) {
	return s.bootstrapMutation(expected, id, source, func(h *Handover, b *BootstrapObligation) error {
		if !validID(launchID) {
			return ErrIneligible
		}
		if b.Launch != nil {
			if b.Launch.ID == launchID {
				return ErrPending
			}
			return ErrConflict
		}
		if b.Phase == "ambiguous" {
			return ErrPending
		}
		if b.Phase != "pending" || b.ChildID != "" {
			return ErrConflict
		}
		for _, other := range h.Bootstrap {
			if other.Launch != nil && other.Launch.ID == launchID {
				return ErrConflict
			}
		}
		b.Launch = &BootstrapLaunch{ID: launchID, ToID: h.ToID, Epoch: h.Epoch, DestinationCredentialRevision: h.DestinationCredentialRevision}
		b.Phase = "launching"
		return nil
	})
}

// RecordCodexBootstrapChild requires exact persisted source/launch lineage.
// Birth under an old claim never proves adoption of the current desired account.
func (s *Store) RecordCodexBootstrapChild(expected uint64, id, source, launchID string, n NativeMember) (Registry, Handover, error) {
	return s.bootstrapMutation(expected, id, source, func(h *Handover, b *BootstrapObligation) error {
		if !validNativeMember(n) || n.ConversationID != b.ConversationID {
			return ErrConflict
		}
		if b.Launch != nil && b.Launch.ID != launchID || b.Launch == nil && launchID != "" {
			return ErrConflict
		}
		if b.ChildID != "" {
			if b.ChildID == n.SessionID && b.ChildIncarnation == n.NativeIncarnation {
				for _, m := range h.Members {
					if m.Native == n {
						return nil
					}
				}
			}
			return ErrConflict
		}
		recovered := b.Phase == "ambiguous" && b.Launch == nil
		if !recovered && (b.Phase != "launching" || b.Launch == nil) {
			return ErrConflict
		}
		present := false
		for _, m := range h.Members {
			if m.Native.SessionID == n.SessionID && m.Native.NativeIncarnation == n.NativeIncarnation {
				if !recovered || m.Native != n {
					return ErrConflict
				}
				present = true
			}
		}
		b.ChildID = n.SessionID
		b.ChildIncarnation = n.NativeIncarnation
		b.Phase = "bound"
		if !present {
			h.Members = append(h.Members, HandoverMember{Native: n, Phase: "pending"})
		}
		return nil
	})
}

// RecordCodexBootstrapOutcome settles exact continuation readiness or a final
// owner/lifecycle outcome. It cannot settle an ambiguous unbound launch.
func (s *Store) RecordCodexBootstrapOutcome(expected uint64, id, source, launchID, phase string) (Registry, Handover, error) {
	return s.bootstrapMutation(expected, id, source, func(_ *Handover, b *BootstrapObligation) error {
		if phase != "ready" && phase != "failed" && phase != "cancelled" {
			return ErrIneligible
		}
		if b.Launch != nil && b.Launch.ID != launchID || b.Launch == nil && launchID != "" {
			return ErrConflict
		}
		if b.Phase == "ready" || b.Phase == "failed" || b.Phase == "cancelled" {
			if b.Phase != phase {
				return ErrConflict
			}
			return nil
		}
		if b.ChildID == "" {
			if b.Launch != nil || b.Phase == "ambiguous" {
				return ErrPending
			}
			if phase != "cancelled" {
				return ErrConflict
			}
		}

		b.Phase = phase
		return nil
	})
}

// ResetCodexBootstrapLaunch is reserved for a causally received no-start result
// from the first direct launch call. Missing/failed/replayed launches are not proof.
func (s *Store) ResetCodexBootstrapLaunch(expected uint64, id, source, launchID string) (Registry, Handover, error) {
	return s.bootstrapMutation(expected, id, source, func(_ *Handover, b *BootstrapObligation) error {
		if b.Phase != "launching" || b.ChildID != "" || b.Launch == nil || b.Launch.ID != launchID {
			return ErrConflict
		}
		b.Launch = nil
		b.Phase = "pending"
		return nil
	})
}

// ValidateCodexBootstrapBirth binds token-free process birth to its durable claim.
// The claim's account tuple is historical; live authentication adopts current selection.
func (s *Store) ValidateCodexBootstrapBirth(launchID, source, conversation, child string) error {
	r, e := s.Snapshot()
	if e != nil {
		return e
	}
	h, ok := r.Handovers[ProviderCodex]
	if !ok || !validID(launchID) || child == "" {
		return ErrPending
	}
	for _, b := range h.Bootstrap {
		if b.SourceID == source && b.ConversationID == conversation && b.Launch != nil && b.Launch.ID == launchID && b.Phase == "launching" && b.ChildID == "" {
			return nil
		}
	}
	return ErrConflict
}
