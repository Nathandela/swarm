package logins

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"time"
)

func validPhase(p string) bool {
	switch p {
	case "waiting", "stopping", "stopped", "captured", "installing", "installed", "committed", "resuming", "retry-stopping":
		return true
	}
	return false
}
func rowRank(p string) int {
	switch p {
	case "waiting":
		return 0
	case "claimed":
		return 1
	case "stopped":
		return 2
	case "launched":
		return 3
	case "ready", "failed", "cancelled":
		return 4
	}
	return -1
}
func terminalRow(p string) bool { return p == "ready" || p == "failed" || p == "cancelled" }
func (s *Store) BeginSwitch(expected uint64, provider, toID, reason string, sessions []Restart) (Switch, error) {
	return s.beginSwitch(expected, provider, toID, reason, sessions, nil, "")
}

// BeginRelogin stages a native-verified replacement grant separately from the
// outgoing snapshot. The normal stop/capture/install/restart transaction owns it.
func (s *Store) BeginRelogin(expected uint64, loginID string, credential Credential, reason string, sessions []Restart) (Switch, error) {
	return s.beginReloginJob(expected, loginID, credential, reason, sessions, "")
}
func (s *Store) BeginReloginJob(expected uint64, loginID string, credential Credential, reason string, sessions []Restart, jobID string) (Switch, error) {
	if !validID(jobID) {
		return Switch{}, ErrIneligible
	}
	return s.beginReloginJob(expected, loginID, credential, reason, sessions, jobID)
}
func (s *Store) beginReloginJob(expected uint64, loginID string, credential Credential, reason string, sessions []Restart, jobID string) (Switch, error) {
	r, e := s.Snapshot()
	if e != nil {
		return Switch{}, e
	}
	l := find(&r, loginID)
	if l == nil {
		return Switch{}, ErrIneligible
	}
	if jobID != "" {
		if l.LastLoginJobID == jobID {
			return Switch{}, nil
		}
		if op, ok := r.Switches[l.Provider]; ok && op.LoginJobID == jobID && (op.ToID == loginID || op.NextID == loginID) {
			return op, nil
		}
	}
	return s.beginSwitch(expected, l.Provider, loginID, reason, sessions, &credential, jobID)
}

func (s *Store) beginSwitch(expected uint64, provider, toID, reason string, sessions []Restart, incoming *Credential, jobID string) (Switch, error) {
	var out Switch
	_, e := s.mutate(expected, func(r *Registry) error {
		if _, ok := r.Switches[provider]; ok {
			return ErrPending
		}
		l := find(r, toID)
		if l == nil || l.Provider != provider || (incoming == nil && !eligible(*l, time.Now())) {
			return ErrIneligible
		}
		if _, e := s.readRelogin(*l); e == nil {
			return ErrPending
		} else if !errors.Is(e, os.ErrNotExist) {
			return e
		}
		if incoming == nil {
			if _, e := s.readSaved(*l); e != nil {
				return e
			}
		} else if _, e := s.readSavedRecord(*l); e != nil {
			return e
		}
		if e := s.validateAuthContext(provider); e != nil {
			return e
		}
		p := r.Providers[provider]
		if (incoming == nil && p.ActiveID == toID) || (incoming != nil && p.ActiveID != toID) {
			return ErrInUse
		}
		if p.Epoch == ^uint64(0) {
			return ErrIneligible
		}
		id, e := newID()
		if e != nil {
			return e
		}
		if e = waitingRows(sessions, true); e != nil {
			return e
		}
		attempted := []string{toID}
		out = Switch{ID: id, Provider: provider, FromID: p.ActiveID, ToID: toID, FromEpoch: p.Epoch, ToEpoch: p.Epoch + 1, Phase: "waiting", Reason: reason, Sessions: sessions, AttemptedIDs: attempted, LoginJobID: jobID}
		if incoming != nil {
			if e := s.stageCredential(id, *l, *incoming, jobID); e != nil {
				return e
			}
			out.Relogin = true
		}
		r.Switches[provider] = out
		return nil
	})
	return out, e
}

// BeginDeactivate returns a provider to ordinary native sign-in. The journal has
// no destination: it stops writers, saves the outgoing grant and clears the
// selection. Nothing is resumed, so rows need no exact conversation.
func (s *Store) BeginDeactivate(expected uint64, provider, reason string, sessions []Restart) (Switch, error) {
	var out Switch
	_, e := s.mutate(expected, func(r *Registry) error {
		if !validProvider(provider) {
			return ErrIneligible
		}
		if _, ok := r.Switches[provider]; ok {
			return ErrPending
		}
		p := r.Providers[provider]
		if p.ActiveID == "" || p.Epoch == ^uint64(0) {
			return ErrIneligible
		}
		id, e := newID()
		if e != nil {
			return e
		}
		if e = waitingRows(sessions, false); e != nil {
			return e
		}
		out = Switch{ID: id, Provider: provider, FromID: p.ActiveID, FromEpoch: p.Epoch, ToEpoch: p.Epoch + 1, Phase: "waiting", Reason: reason, Sessions: sessions, Deactivate: true}
		r.Switches[provider] = out
		return nil
	})
	return out, e
}
func waitingRows(sessions []Restart, conversation bool) error {
	seen := map[string]bool{}
	for i := range sessions {
		row := &sessions[i]
		if row.SourceID == "" || (conversation && row.ConversationID == "") || seen[row.SourceID] {
			return ErrIneligible
		}
		seen[row.SourceID] = true
		row.Phase = "waiting"
		row.ChildID = ""
		row.ChildStopClaimed = false
		row.LastError = ""
	}
	return nil
}
func (s *Store) switchMutation(id string, fn func(*Registry, *Switch) error) error {
	return s.switchMutationAt(nil, id, fn)
}
func (s *Store) switchMutationAt(expected *uint64, id string, fn func(*Registry, *Switch) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.locked(func() error {
		r, e := s.load()
		if e != nil {
			return e
		}
		if expected != nil && r.Revision != *expected {
			return ErrConflict
		}
		for p, op := range r.Switches {
			if op.ID != id {
				continue
			}
			if e = fn(&r, &op); e != nil {
				return e
			}
			if r.Revision == ^uint64(0) {
				return ErrIneligible
			}
			if op.Phase == "complete" {
				delete(r.Switches, p)
			} else {
				r.Switches[p] = op
			}
			r.Revision++
			if e = s.save(r); e != nil {
				return e
			}
			if op.Phase == "complete" {
				if e = s.erasePending(op.ID); e != nil {
					return e
				}
				return s.eraseRetryPending(op.ID)
			}
			return nil
		}
		return ErrConflict
	})
}
func (s *Store) SaveSwitch(in Switch) error {
	return s.switchMutation(in.ID, func(_ *Registry, op *Switch) error {
		if in.Provider != op.Provider || in.FromID != op.FromID || in.ToID != op.ToID || in.FromEpoch != op.FromEpoch || in.ToEpoch != op.ToEpoch || in.Relogin != op.Relogin || in.Deactivate != op.Deactivate || in.NextID != op.NextID || in.LoginJobID != op.LoginJobID || !reflect.DeepEqual(in.AttemptedIDs, op.AttemptedIDs) || len(in.Sessions) != len(op.Sessions) {
			return ErrConflict
		}
		transition := in.Phase == op.Phase || (op.Phase == "waiting" && in.Phase == "stopping") || (op.Phase == "stopping" && in.Phase == "stopped") || (op.Phase == "committed" && in.Phase == "resuming")
		if !transition {
			return ErrConflict
		}
		for i, old := range op.Sessions {
			row := in.Sessions[i]
			ownerCancel := row.Phase == "cancelled" && (old.Phase == "ready" || old.Phase == "failed") && old.ChildID == row.ChildID
			claimChild := op.Phase == "retry-stopping" && !old.ChildStopClaimed && row.ChildStopClaimed && old.ChildID != "" && row.Phase == old.Phase && row.LastError == old.LastError
			if (old.ChildStopClaimed && !row.ChildStopClaimed) || (!old.ChildStopClaimed && row.ChildStopClaimed && (op.Phase != "retry-stopping" || row.ChildID == "")) {
				return ErrConflict
			}
			if old.SourceID != row.SourceID || old.ConversationID != row.ConversationID || rowRank(row.Phase) < rowRank(old.Phase) || rowRank(row.Phase) < 0 || (old.ChildID != "" && old.ChildID != row.ChildID) || (terminalRow(old.Phase) && !reflect.DeepEqual(old, row) && !ownerCancel && !claimChild) {
				return ErrConflict
			}
			if in.Phase == "stopped" && row.Phase != "stopped" && !terminalRow(row.Phase) {
				return ErrInUse
			}
		}
		in.CancelRequested = in.CancelRequested || op.CancelRequested
		*op = in
		return nil
	})
}

// CaptureOutgoing may be replayed after a crash. The native execution store is
// read only after lifecycle has durably recorded every writer stopped.
func (s *Store) CaptureOutgoing(id string) error {
	return s.switchMutation(id, func(r *Registry, op *Switch) error {
		if op.Phase == "captured" {
			return nil
		}
		if op.Phase != "stopped" {
			return ErrInUse
		}
		if op.FromID != "" {
			l := find(r, op.FromID)
			if l == nil {
				return ErrIneligible
			}
			c, unavailable, e := s.captureCredential(*l)
			// Returning to native sign-in must not wait for a grant it cannot
			// save: the login needs sign-in instead. Storage faults still retry.
			if op.Deactivate && (errors.Is(e, ErrIdentityChanged) || errors.Is(e, ErrInvalidCredentials)) {
				unavailable, e = true, nil
			}
			if e != nil {
				return e
			}
			if unavailable {
				l.AuthState = AuthNeedsLogin
				op.Phase = "captured"
				return nil
			}
			c, _, renewable, expires, e := normalizeCredential(op.Provider, c)
			if e != nil {
				return e
			}
			if l.CredentialRevision == ^uint64(0) {
				return ErrIneligible
			}
			l.CredentialRevision++
			l.Renewable = renewable
			l.ExpiresAt = expires
			if e = s.writeSavedMetadata(*l, c); e != nil {
				return e
			}
		}
		op.Phase = "captured"
		return nil
	})
}

func (s *Store) InstallIncoming(id string) error {
	// Commit the intent separately before touching either native auth store.
	if e := s.switchMutation(id, func(_ *Registry, op *Switch) error {
		if op.Phase == "installed" || op.Phase == "installing" {
			return nil
		}
		if op.Phase != "captured" {
			return ErrInUse
		}
		op.Phase = "installing"
		return nil
	}); e != nil {
		return e
	}
	return s.switchMutation(id, func(r *Registry, op *Switch) error {
		if op.Phase == "installed" {
			return nil
		}
		if op.Phase != "installing" {
			return ErrInUse
		}
		if op.Deactivate {
			// Nothing is installed. Claude's grant stays in its saved home.
			if op.Provider == ProviderCodex {
				if e := s.scrubCodexGrant(); e != nil {
					return e
				}
			}
			op.Phase = "installed"
			return nil
		}
		l := find(r, op.ToID)
		if l == nil {
			return ErrIneligible
		}
		var c Credential
		var e error
		if op.Relogin {
			c, e = s.readPending(*op, *l)
		} else {
			c, e = s.readSaved(*l)
		}
		if e != nil {
			return e
		}
		if e = s.installCredential(op.Provider, c); e != nil {
			return e
		}
		if op.Relogin {
			_, _, renewable, expires, err := normalizeCredential(l.Provider, c)
			if err != nil {
				return err
			}
			if l.CredentialRevision == ^uint64(0) {
				return ErrIneligible
			}
			l.CredentialRevision++
			l.AuthState, l.Renewable, l.ExpiresAt = AuthReady, renewable, expires
			l.LimitKnown = false
			l.LimitNonRolling = false
			l.ExhaustedUntil = time.Time{}
			if e = s.writeSaved(*l, c); e != nil {
				return e
			}
		}
		if e = s.validateNative(op.Provider, l.Identity, l.ID); e != nil {
			return e
		}
		op.Phase = "installed"
		return nil
	})
}

// scrubCodexGrant leaves no usable token copy in the common Codex home once the
// provider returns to native sign-in; unrelated auth.json fields remain.
func (s *Store) scrubCodexGrant() error {
	name := "provider/codex/native/auth.json"
	current, e := s.readOptional(name)
	if e != nil || current == nil {
		return e
	}
	b, e := mergeFields(current, []byte("{}"), "auth_mode", "tokens", "last_refresh", "OPENAI_API_KEY")
	if e != nil {
		return e
	}
	_, e = durableReplace(s.root, name, b, s.writeOps)
	return e
}

func (s *Store) readPending(op Switch, l Login) (Credential, error) {
	staged, e := s.readStaged("pending/"+op.ID+".json", l)
	if e == nil && staged.LoginJobID != op.LoginJobID {
		return Credential{}, ErrConflict
	}
	return staged.Credential, e
}
func (s *Store) readStaged(name string, l Login) (savedCredential, error) {
	raw, e := readPrivate(s.root, name, maxCredentialBytes*2)
	if e != nil {
		return savedCredential{}, e
	}
	var saved savedCredential
	if validateJSON(raw) != nil || json.Unmarshal(raw, &saved) != nil || saved.Identity != l.Identity || (saved.LoginJobID != "" && !validID(saved.LoginJobID)) {
		return savedCredential{}, ErrInvalidCredentials
	}
	c, id, _, _, e := normalizeCredential(l.Provider, saved.Credential)
	if e != nil {
		return savedCredential{}, e
	}
	if id != l.Identity {
		return savedCredential{}, ErrIdentityChanged
	}
	saved.Credential = c
	return saved, nil
}
func (s *Store) erasePending(id string) error {
	return s.erasePendingKind(id, false)
}
func (s *Store) eraseRetryPending(id string) error { return s.erasePendingKind(id, true) }
func (s *Store) erasePendingKind(id string, retry bool) error {
	if !validID(id) {
		return ErrIneligible
	}
	suffix := ""
	if retry {
		suffix = "-retry"
	}
	name := "pending/" + id + suffix + ".json"
	if _, e := readPrivate(s.root, name, maxCredentialBytes*2); errors.Is(e, os.ErrNotExist) {
		return nil
	} else if e != nil {
		return e
	}
	if e := s.root.Remove(name); e != nil {
		return e
	}
	if e := syncRootDir(s.root, "pending"); e != nil {
		return ErrDurabilityUncertain
	}
	return nil
}
func (s *Store) VerifyInstalled(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, e := s.load()
	if e != nil {
		return e
	}
	for _, op := range r.Switches {
		if op.ID == id && (op.Phase == "installed" || op.Phase == "committed" || op.Phase == "resuming") {
			if op.Deactivate {
				return nil
			}
			l := find(&r, op.ToID)
			return s.validateNative(op.Provider, l.Identity, l.ID)
		}
	}
	return ErrPending
}
func (s *Store) CommitSwitch(id string) (Runtime, error) {
	var rt Runtime
	e := s.switchMutation(id, func(r *Registry, op *Switch) error {
		if op.Phase != "installed" && op.Phase != "committed" && op.Phase != "resuming" {
			return ErrInUse
		}
		// A deactivation commits its empty ToID: the selection is cleared and
		// there is no replacement runtime.
		l := find(r, op.ToID)
		if !op.Deactivate {
			if l == nil {
				return ErrIneligible
			}
			if e := s.validateNative(op.Provider, l.Identity, l.ID); e != nil {
				return e
			}
		}
		p := r.Providers[op.Provider]
		if p.Epoch != op.FromEpoch && (p.Epoch != op.ToEpoch || p.ActiveID != op.ToID) {
			return ErrConflict
		}
		r.Providers[op.Provider] = ProviderState{Provider: op.Provider, ActiveID: op.ToID, Epoch: op.ToEpoch}
		if op.Relogin && op.LoginJobID != "" {
			l.LastLoginJobID = op.LoginJobID
		}
		if op.Phase == "installed" {
			op.Phase = "committed"
		}
		if !op.Deactivate {
			rt = Runtime{Provider: op.Provider, Epoch: op.ToEpoch, Identity: l.Identity, Home: s.Home(op.Provider)}
		}
		return nil
	})
	return rt, e
}
func (s *Store) CompleteSwitch(id string) error {
	return s.switchMutation(id, func(r *Registry, op *Switch) error {
		if op.Phase != "committed" && op.Phase != "resuming" {
			return ErrInUse
		}
		for _, row := range op.Sessions {
			if !terminalRow(row.Phase) {
				return ErrInUse
			}
			if row.Phase != "cancelled" && !op.Deactivate {
				l := find(r, op.ToID)
				if l == nil || !eligible(*l, time.Now()) {
					return ErrIneligible
				}
			}
		}
		op.Phase = "complete"
		return nil
	})
}
func (s *Store) CancelSwitch(id string) error {
	return s.cancelSwitch(nil, id)
}
func (s *Store) CancelSwitchAtRevision(expected uint64, id string) error {
	return s.cancelSwitch(&expected, id)
}
func (s *Store) cancelSwitch(expected *uint64, id string) error {
	return s.switchMutationAt(expected, id, func(_ *Registry, op *Switch) error {
		if op.Phase != "waiting" && op.Phase != "stopping" {
			op.CancelRequested = true
			return nil
		}
		for _, row := range op.Sessions {
			if rowRank(row.Phase) > 0 {
				op.CancelRequested = true
				return nil
			}
		}
		op.Phase = "complete"
		return nil
	})
}
func (s *Store) RuntimeForSwitch(id, source string) (Runtime, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.safeAnchor(); e != nil {
		return Runtime{}, e
	}
	r, e := s.load()
	if e != nil {
		return Runtime{}, e
	}
	if r.MigrationPending {
		return Runtime{}, ErrPending
	}
	for _, op := range r.Switches {
		// A deactivation has no replacement runtime to restart into.
		if op.ID != id || op.Deactivate || (op.Phase != "committed" && op.Phase != "resuming") {
			continue
		}
		for _, row := range op.Sessions {
			if row.SourceID == source && (row.Phase == "stopped" || row.Phase == "launched") {
				return s.runtime(r, op.Provider)
			}
		}
	}
	return Runtime{}, ErrPending
}
func (s *Store) ValidateSwitchRestart(id, source string, rt Runtime) error {
	current, e := s.RuntimeForSwitch(id, source)
	if e != nil {
		return e
	}
	if current != rt {
		return ErrConflict
	}
	return nil
}

// Reconcile repairs a credential write completed before its registry commit.
// It never refreshes an active or parked login, nor clears a switch barrier.
func (s *Store) Reconcile() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.locked(func() error {
		r, e := s.load()
		if e != nil {
			return e
		}
		completed, e := s.reconcileRelogins(&r)
		if e != nil {
			return e
		}
		changed := len(completed) > 0
		dir, e := s.root.Open("saved")
		if e != nil {
			return e
		}
		entries, e := dir.ReadDir(-1)
		_ = dir.Close()
		if e != nil {
			return e
		}
		for _, entry := range entries {
			if validID(entry.Name()) && find(&r, entry.Name()) == nil {
				if e = s.eraseSaved(entry.Name()); e != nil {
					// Unreferenced unknown bytes are preserved for the owner; they
					// cannot prevent unrelated canonical logins from recovering.
					if errors.Is(e, errUnknownSavedData) || errors.Is(e, ErrUnsafePath) {
						continue
					}
					return e
				}
			}
		}
		pending, e := s.root.Open("pending")
		if e != nil {
			return e
		}
		pendingEntries, e := pending.ReadDir(-1)
		_ = pending.Close()
		if e != nil {
			return e
		}
		for _, entry := range pendingEntries {
			id := strings.TrimSuffix(entry.Name(), ".json")
			retry := strings.HasSuffix(id, "-retry")
			if retry {
				id = strings.TrimSuffix(id, "-retry")
			}
			suffix := ""
			if retry {
				suffix = "-retry"
			}
			if !validID(id) || entry.Name() != id+suffix+".json" {
				continue
			}
			owned := false
			for _, op := range r.Switches {
				if op.ID == id && ((!retry && op.Relogin) || (retry && op.Phase == "retry-stopping" && op.NextID == op.ToID)) {
					owned = true
					break
				}
			}
			if !owned {
				if e = s.erasePendingKind(id, retry); e != nil {
					return e
				}
			}
		}
		for i := range r.Logins {
			l := &r.Logins[i]
			raw, e := readPrivate(s.root, s.savedPath(*l), maxCredentialBytes*2)
			if errors.Is(e, os.ErrNotExist) {
				return ErrInvalidCredentials
			}
			if e != nil {
				return e
			}
			var c savedCredential
			if validateJSON(raw) != nil || json.Unmarshal(raw, &c) != nil {
				return ErrInvalidCredentials
			}
			if c.Revision == l.CredentialRevision {
				continue
			}
			if c.Revision != l.CredentialRevision+1 || c.Identity != l.Identity {
				return ErrConflict
			}
			if l.Provider == ProviderClaude {
				c.Credential.Auth, e = s.claudeAuth(l.ID)
				if e != nil {
					return e
				}
			}
			_, identity, renewable, expires, e := normalizeCredential(l.Provider, c.Credential)
			if e != nil {
				return e
			}
			if identity != l.Identity {
				return ErrIdentityChanged
			}
			l.CredentialRevision = c.Revision
			l.Renewable = renewable
			l.ExpiresAt = expires
			changed = true
		}
		if !changed {
			return nil
		}
		if r.Revision == ^uint64(0) {
			return ErrIneligible
		}
		r.Revision++
		if e = s.save(r); e != nil {
			return e
		}
		for _, id := range completed {
			if e = s.eraseRelogin(id); e != nil {
				return e
			}
		}
		return nil
	})
}
