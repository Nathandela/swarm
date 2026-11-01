package logins

import "encoding/hex"

// CodexRefreshRef binds one native refresh to its exact saved authority, never selection.
type CodexRefreshRef struct {
	LoginID            string `json:"login_id"`
	Identity           string `json:"identity"`
	Owner              string `json:"owner"`
	CredentialRevision uint64 `json:"credential_revision"`
	// Generation hashes native access/id/refresh tokens, excluding formatting and last_refresh.
	Generation string `json:"generation"`
}

func (r CodexRefreshRef) Valid() bool {
	return validID(r.LoginID) && validDigest(r.Identity) && validDigest(r.Owner) && r.CredentialRevision != 0 && validDigest(r.Generation)
}

// RefreshRef observes nonsecret authority under the existing exclusive grant lease.
func (l *GrantLease) RefreshRef() (CodexRefreshRef, error) {
	if l == nil || l.store == nil || l.lock == nil {
		return CodexRefreshRef{}, ErrInUse
	}
	r, err := l.store.Snapshot()
	if err != nil {
		return CodexRefreshRef{}, err
	}
	login := find(&r, l.login.ID)
	if r.MigrationPending || !codexOwned(r, ProviderCodex) {
		return CodexRefreshRef{}, ErrPending
	}
	if login == nil || login.Provider != ProviderCodex || login.Identity != l.login.Identity || login.CredentialRevision != l.login.CredentialRevision {
		return CodexRefreshRef{}, ErrConflict
	}
	if login.AuthState != AuthReady {
		return CodexRefreshRef{}, ErrIneligible
	}
	if op, pending := r.Switches[ProviderCodex]; pending && op.Phase != "committed" && op.Phase != "resuming" {
		return CodexRefreshRef{}, ErrPending
	}
	generation, err := l.Generation()
	if err != nil {
		return CodexRefreshRef{}, err
	}
	return CodexRefreshRef{LoginID: login.ID, Identity: login.Identity, Owner: l.owner, CredentialRevision: login.CredentialRevision, Generation: hex.EncodeToString(generation[:])}, nil
}
