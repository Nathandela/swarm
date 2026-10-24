package logins

// PrepareCodexExternalBirth routes a token-free native process to the owned common
// home. Authentication and admission are established later by the private backend.
func (s *Store) PrepareCodexExternalBirth(rt Runtime, inherited []string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.safeAnchor(); e != nil {
		return nil, e
	}
	r, e := s.load()
	if e != nil {
		return nil, e
	}
	if r.MigrationPending || !codexOwned(r, ProviderCodex) || rt.Provider != ProviderCodex || !rt.ExternalAuth || rt.Home != s.Home(ProviderCodex) {
		return nil, ErrPending
	}
	if _, pending := r.Switches[ProviderCodex]; pending {
		return nil, ErrPending
	}
	return prepareEnvironment(rt, "", inherited)
}
