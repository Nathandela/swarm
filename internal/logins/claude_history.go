package logins

import (
	"os"
	"path/filepath"
)

// OpenClaudeProfileProjects returns a held shared-history root, never a pathname
// through the profile alias. It proves read custody, not native input admission.
func (s *Store) OpenClaudeProfileProjects(rt Runtime) (*os.Root, error) {
	if !IsClaudeProfileRuntime(s.StateRoot(), rt) {
		return nil, ErrIneligible
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var projects *os.Root
	err := s.locked(func() error {
		r, err := s.load()
		if err != nil {
			return err
		}
		profile, known := r.ClaudeProfiles[rt.Identity]
		if r.SchemaVersion < ClaudeProfileCustodySchemaVersion || !known || !profile.Provisioned || rt.Epoch > r.Providers[ProviderClaude].Epoch {
			return ErrPending
		}
		if err := s.verifyClaudeAliases(filepath.Join("provider", "claude", "profiles", rt.Identity)); err != nil {
			return err
		}
		projects, err = openPrivateDir(s.root, "provider/claude/native/projects")
		return err
	})
	if err != nil && projects != nil {
		_ = projects.Close()
		projects = nil
	}
	return projects, err
}
