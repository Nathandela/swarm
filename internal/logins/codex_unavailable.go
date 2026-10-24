package logins

import (
	"errors"
	"os"
	"path/filepath"
)

func emptyCredentialPart(raw []byte) bool { return len(raw) == 0 || string(raw) == "null" }
func credentiallessCodex(l Login, saved savedCredential) bool {
	return l.Provider == ProviderCodex && (l.AuthState == AuthNeedsLogin || l.AuthState == AuthPaused) && !l.Renewable && saved.AuthOwner == nil && emptyCredentialPart(saved.Credential.Auth) && emptyCredentialPart(saved.Credential.Identity)
}
func (s *Store) verifyNoCodexGrant(l Login) error {
	_, e := s.root.Lstat(filepath.Join("saved", l.ID, "auth.json"))
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	return ErrInvalidCredentials
}
