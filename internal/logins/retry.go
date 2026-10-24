package logins

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"time"
)

func eligible(l Login, now time.Time) bool {
	return l.AuthState == AuthReady && (!l.LimitKnown || (!l.ExhaustedUntil.IsZero() && !now.Before(l.ExhaustedUntil)))
}
func currentSwitch(r *Registry, op *Switch) bool {
	p := r.Providers[op.Provider]
	return p.ActiveID == op.ToID && p.Epoch == op.ToEpoch
}

func (s *Store) BeginSwitchRetry(id, nextID, reason string) error {
	return s.beginSwitchRetry(nil, id, nextID, reason)
}
func (s *Store) BeginSwitchRetryAtRevision(expected uint64, id, nextID, reason string) error {
	return s.beginSwitchRetry(&expected, id, nextID, reason)
}
func (s *Store) beginSwitchRetry(expected *uint64, id, nextID, reason string) error {
	return s.switchMutationAt(expected, id, func(r *Registry, op *Switch) error {
		if codexOwned(*r, op.Provider) || op.OwnershipMigration {
			return ErrPending
		}
		if !currentSwitch(r, op) {
			return ErrConflict
		}
		if op.Phase == "retry-stopping" {
			if op.NextID == nextID && nextID != op.ToID {
				return nil
			}
			return ErrPending
		}
		if op.Phase != "committed" && op.Phase != "resuming" {
			return ErrInUse
		}
		l := find(r, nextID)
		if l == nil || l.Provider != op.Provider || nextID == op.ToID || slices.Contains(op.AttemptedIDs, nextID) || !eligible(*l, time.Now()) {
			return ErrIneligible
		}
		if _, e := s.readRelogin(*l); e == nil {
			return ErrPending
		} else if !errors.Is(e, os.ErrNotExist) {
			return e
		}
		if _, e := s.readSaved(*l); e != nil {
			return e
		}
		op.NextID = nextID
		op.Phase = "retry-stopping"
		op.Reason = reason
		op.LastError = ""
		op.LoginJobID = ""
		return nil
	})
}

func (s *Store) BeginSwitchRelogin(id string, c Credential, reason string) error {
	return s.beginSwitchRelogin(nil, id, c, reason, "")
}
func (s *Store) BeginSwitchReloginAtRevision(expected uint64, id string, c Credential, reason string) error {
	return s.beginSwitchRelogin(&expected, id, c, reason, "")
}
func (s *Store) BeginSwitchReloginJobAtRevision(expected uint64, id string, c Credential, reason, jobID string) error {
	if !validID(jobID) {
		return ErrIneligible
	}
	return s.beginSwitchRelogin(&expected, id, c, reason, jobID)
}
func (s *Store) beginSwitchRelogin(expected *uint64, id string, c Credential, reason, jobID string) error {
	return s.switchMutationAt(expected, id, func(r *Registry, op *Switch) error {
		if codexOwned(*r, op.Provider) || op.OwnershipMigration {
			return ErrPending
		}
		if !currentSwitch(r, op) {
			return ErrConflict
		}
		l := find(r, op.ToID)
		if l == nil {
			return ErrIneligible
		}
		if jobID != "" && l.LastLoginJobID == jobID {
			return nil
		}
		if op.Phase == "retry-stopping" {
			if op.NextID != op.ToID {
				return ErrPending
			}
			if jobID != "" && op.LoginJobID == jobID {
				return nil
			}
			staged, e := s.readStaged("pending/"+op.ID+"-retry.json", *l)
			if e != nil {
				return e
			}
			normalized, identity, _, _, e := normalizeCredential(l.Provider, c)
			if e != nil {
				return e
			}
			if identity != l.Identity {
				return ErrIdentityChanged
			}
			a, _ := json.Marshal(staged.Credential)
			b, _ := json.Marshal(normalized)
			if string(a) != string(b) {
				return ErrPending
			}
			return nil
		}
		if op.Phase != "committed" && op.Phase != "resuming" {
			return ErrInUse
		}
		if e := s.stageCredential(op.ID+"-retry", *l, c, jobID); e != nil {
			return e
		}
		op.NextID = op.ToID
		op.LoginJobID = jobID
		op.Phase = "retry-stopping"
		op.Reason = reason
		op.LastError = ""
		return nil
	})
}

// RetargetSwitch is called only after lifecycle proves every current writer
// stopped. Original source/conversation obligations remain in this one journal.
func (s *Store) RetargetSwitch(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.locked(func() error {
		r, e := s.load()
		if e != nil {
			return e
		}
		if r.Revision == ^uint64(0) {
			return ErrIneligible
		}
		provider := ""
		var op Switch
		for p, current := range r.Switches {
			if current.ID == id {
				provider = p
				op = current
				break
			}
		}
		if codexOwned(r, op.Provider) || op.OwnershipMigration {
			return ErrPending
		}
		if provider == "" {
			return ErrConflict
		}
		if op.Phase != "retry-stopping" || op.NextID == "" {
			return ErrInUse
		}
		if !currentSwitch(&r, &op) {
			return ErrConflict
		}
		if op.ToEpoch == ^uint64(0) {
			return ErrIneligible
		}
		l := find(&r, op.NextID)
		if l == nil || l.Provider != op.Provider {
			return ErrIneligible
		}
		relogin := op.NextID == op.ToID
		if relogin {
			staged, e := s.readStaged("pending/"+id+"-retry.json", *l)
			if e != nil {
				return e
			}
			raw, e := json.Marshal(staged)
			if e != nil {
				return e
			}
			if _, e = durableReplace(s.root, "pending/"+id+".json", raw, s.writeOps); e != nil {
				return e
			}
			op.LoginJobID = staged.LoginJobID
		} else {
			if !eligible(*l, time.Now()) || slices.Contains(op.AttemptedIDs, l.ID) {
				return ErrIneligible
			}
			op.AttemptedIDs = append(op.AttemptedIDs, l.ID)
			op.LoginJobID = ""
		}
		op.FromID = op.ToID
		op.FromEpoch = op.ToEpoch
		op.ToID = op.NextID
		op.ToEpoch++
		op.NextID = ""
		op.Relogin = relogin
		op.Phase = "stopped"
		op.LastError = ""
		for i := range op.Sessions {
			row := &op.Sessions[i]
			if row.Phase == "cancelled" {
				continue
			}
			row.Phase = "stopped"
			row.ChildID = ""
			row.ChildStopClaimed = false
			row.LastError = ""
		}
		r.Switches[provider] = op
		r.Revision++
		if e = s.save(r); e != nil {
			return e
		}
		// Registry ownership moves before obsolete private grants are erased.
		if !relogin {
			if e = s.erasePending(id); e != nil {
				return e
			}
		}
		return s.eraseRetryPending(id)
	})
}

func (s *Store) stageCredential(name string, l Login, c Credential, jobID string) error {
	if l.Provider == ProviderClaude {
		if err := s.claudeAuthMutationAllowed(l.ID); err != nil {
			return err
		}
	}
	if jobID != "" && !validID(jobID) {
		return ErrIneligible
	}
	c, identity, _, _, e := normalizeCredential(l.Provider, c)
	if e != nil {
		return e
	}
	if identity != l.Identity {
		return ErrIdentityChanged
	}
	raw, e := json.Marshal(savedCredential{Identity: identity, Credential: c, LoginJobID: jobID})
	if e != nil {
		return e
	}
	_, e = durableReplace(s.root, "pending/"+name+".json", raw, s.writeOps)
	return e
}
