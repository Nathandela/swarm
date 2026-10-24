package logins

import "slices"

type grantReloginSelection struct {
	Kind                string `json:"kind"`
	ID                  string `json:"id"`
	FromEpoch           uint64 `json:"from_epoch"`
	FromDesiredRevision uint64 `json:"from_desired_revision"`
}

func validGrantSelection(x *grantReloginSelection) bool {
	return x != nil && (x.Kind == "handover" || x.Kind == "migration") && validID(x.ID) && x.FromEpoch > 0 && (x.Kind != "handover" || x.FromEpoch != ^uint64(0)) && x.FromDesiredRevision > 0
}
func reloginSelection(r Registry, l Login) (*grantReloginSelection, error) {
	p := r.Providers[ProviderCodex]
	if op, ok := r.Switches[ProviderCodex]; ok {
		if !op.OwnershipMigration || (op.Phase != "committed" && op.Phase != "resuming") || op.ToID != l.ID || p.ActiveID != l.ID || p.Epoch != op.ToEpoch || p.DesiredCredentialRevision == 0 {
			return nil, ErrPending
		}
		return &grantReloginSelection{Kind: "migration", ID: op.ID, FromEpoch: p.Epoch, FromDesiredRevision: p.DesiredCredentialRevision}, nil
	}
	if h, ok := r.Handovers[ProviderCodex]; ok && slices.Contains(h.HeldLoginIDs, l.ID) {
		if h.ToID != l.ID || p.ActiveID != l.ID {
			return nil, ErrPending
		}
		if h.Epoch == ^uint64(0) {
			return nil, ErrIneligible
		}
		return &grantReloginSelection{Kind: "handover", ID: h.ID, FromEpoch: h.Epoch, FromDesiredRevision: h.DestinationCredentialRevision}, nil
	}
	return nil, nil
}

// The intent binds selection before authority changes. A committed retry may
// only retire that same transition, never advance a cohort a second time.
func validateGrantSelection(r Registry, intent grantReloginIntent) error {
	x := intent.Selection
	if x == nil {
		return nil
	}
	if r.SchemaVersion < 4 || !validGrantSelection(x) {
		return ErrConflict
	}
	p := r.Providers[ProviderCodex]
	if p.ActiveID != intent.LoginID {
		return ErrConflict
	}
	if x.Kind == "migration" {
		op, ok := r.Switches[ProviderCodex]
		if !ok || op.ID != x.ID || !op.OwnershipMigration || (op.Phase != "committed" && op.Phase != "resuming") || op.ToID != intent.LoginID || p.Epoch != x.FromEpoch || p.DesiredCredentialRevision != x.FromDesiredRevision {
			return ErrConflict
		}
		return nil
	}
	h, ok := r.Handovers[ProviderCodex]
	if !ok || h.ID != x.ID || h.ToID != intent.LoginID {
		return ErrConflict
	}
	if h.Epoch == x.FromEpoch && h.DestinationCredentialRevision == x.FromDesiredRevision {
		return nil
	}
	l := find(&r, intent.LoginID)
	if l != nil && l.LastLoginJobID == intent.Target.LoginJobID && l.CredentialRevision == intent.Target.Revision && h.Epoch == x.FromEpoch+1 && h.DestinationCredentialRevision == intent.Target.Revision {
		return nil
	}
	return ErrConflict
}
func applyGrantSelection(r *Registry, intent grantReloginIntent) error {
	if e := validateGrantSelection(*r, intent); e != nil {
		return e
	}
	x := intent.Selection
	if x == nil || x.Kind == "migration" {
		return nil
	}
	h := r.Handovers[ProviderCodex]
	if h.Epoch == x.FromEpoch+1 {
		return nil
	}
	h.Epoch++
	h.DestinationCredentialRevision = intent.Target.Revision
	for i := range h.Members {
		if h.Members[i].Phase != "retired" {
			h.Members[i].Phase = "pending"
		}
	}
	h.Phase = handoverPhase(h)
	r.Handovers[ProviderCodex] = h
	p := r.Providers[ProviderCodex]
	p.Epoch = h.Epoch
	p.DesiredCredentialRevision = h.DestinationCredentialRevision
	r.Providers[ProviderCodex] = p
	return nil
}

func (s *Store) migrationGrantPending(r Registry, op Switch) error {
	if !op.OwnershipMigration || !codexOwned(r, ProviderCodex) {
		return nil
	}
	if e := s.noGrantRelogin(op.ToID); e != nil {
		return e
	}
	if op.Phase == "installed" {
		return nil
	}
	l := find(&r, op.ToID)
	if l == nil || r.Providers[ProviderCodex].DesiredCredentialRevision != l.CredentialRevision {
		return ErrPending
	}
	return nil
}
