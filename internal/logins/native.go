package logins

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// ReadNativeCredential collects only auth-owned fields after the enrollment
// worker has exited. It reads macOS Claude's real native Keychain authority.
func ReadNativeCredential(provider, home string) (Credential, error) {
	if !validProvider(provider) || checkAbsolute(home, true) != nil {
		return Credential{}, ErrUnsafePath
	}
	info, e := os.Lstat(home)
	if e != nil || !privateInfo(info, true) {
		return Credential{}, ErrUnsafePath
	}
	root, e := os.OpenRoot(home)
	if e != nil {
		return Credential{}, e
	}
	defer func() { _ = root.Close() }()
	opened, e := root.Lstat(".")
	if e != nil || !os.SameFile(info, opened) {
		return Credential{}, ErrUnsafePath
	}
	var c Credential
	if provider == ProviderCodex {
		if _, pendingErr := root.Lstat(grantReloginFile); pendingErr == nil {
			return Credential{}, ErrPending
		} else if !errors.Is(pendingErr, os.ErrNotExist) {
			return Credential{}, pendingErr
		}
		c.Auth, e = readPrivate(root, "auth.json", maxCredentialBytes)
	} else {
		c.Identity, e = readPrivate(root, ".claude.json", maxCredentialBytes)
		if e != nil {
			return Credential{}, e
		}
		if runtime.GOOS == "darwin" {
			c.Auth, e = (nativeKeychain{}).Read(home)
		} else {
			c.Auth, e = readPrivate(root, ".credentials.json", maxCredentialBytes)
		}
	}
	if e != nil {
		return Credential{}, e
	}
	c, _, _, _, e = normalizeCredential(provider, c)
	return c, e
}

func mergeFields(current, fields []byte, keys ...string) ([]byte, error) {
	m := map[string]json.RawMessage{}
	if len(current) > 0 && (validateJSON(current) != nil || json.Unmarshal(current, &m) != nil || m == nil) {
		return nil, ErrInvalidCredentials
	}
	var f map[string]json.RawMessage
	if json.Unmarshal(fields, &f) != nil {
		return nil, ErrInvalidCredentials
	}
	for _, key := range keys {
		delete(m, key)
		if v, ok := f[key]; ok {
			m[key] = v
		}
	}
	return json.Marshal(m)
}
func (s *Store) readOptional(name string) ([]byte, error) {
	b, e := readPrivate(s.root, name, maxCredentialBytes)
	if errors.Is(e, os.ErrNotExist) {
		return nil, nil
	}
	return b, e
}
func (s *Store) nativeCredential(provider, loginID string) (Credential, error) {
	if r, e := s.load(); e != nil {
		return Credential{}, e
	} else if codexOwned(r, provider) || claudeProfilesOwned(r, provider) {
		return Credential{}, ErrPending
	}
	base := filepath.Join("provider", provider, "native")
	if provider == ProviderCodex {
		b, e := readPrivate(s.root, filepath.Join(base, "auth.json"), maxCredentialBytes)
		return Credential{Auth: b}, e
	}
	identity, e := readPrivate(s.root, filepath.Join(base, ".claude.json"), maxCredentialBytes)
	if e != nil {
		return Credential{}, e
	}
	auth, e := s.claudeAuth(loginID)
	return Credential{Auth: auth, Identity: identity}, e
}
func (s *Store) installCredential(provider string, c Credential) error {
	if r, e := s.load(); e != nil {
		return e
	} else if codexOwned(r, provider) || claudeProfilesOwned(r, provider) {
		return ErrPending
	} else if provider == ProviderClaude {
		identity, err := ClaudeIdentity(c.Identity)
		if err != nil {
			return err
		}
		for _, l := range r.Logins {
			if l.Provider == ProviderClaude && l.Identity == identity {
				if err := s.claudeAuthMutationAllowed(l.ID); err != nil {
					return err
				}
			}
		}
	}
	base := filepath.Join("provider", provider, "native")
	if provider == ProviderCodex {
		if e := s.ensureCodexFileMode(); e != nil {
			return e
		}
		current, e := s.readOptional(filepath.Join(base, "auth.json"))
		if e != nil {
			return e
		}
		b, e := mergeFields(current, c.Auth, "auth_mode", "tokens", "last_refresh", "OPENAI_API_KEY")
		if e != nil {
			return e
		}
		_, e = durableReplace(s.root, filepath.Join(base, "auth.json"), b, s.writeOps)
		return e
	}
	// Claude always refreshes its canonical secure-storage home. Selection only
	// changes the common native identity after every outgoing writer has stopped.
	name := filepath.Join(base, ".claude.json")
	current, e := s.readOptional(name)
	if e != nil {
		return e
	}
	identity, e := mergeFields(current, c.Identity, "oauthAccount", "userID")
	if e != nil {
		return e
	}
	_, e = durableReplace(s.root, name, identity, s.writeOps)
	return e
}

func (s *Store) savedNativeHome(id string) string { return filepath.Join(s.path, "saved", id) }
func (s *Store) claudeAuth(id string) ([]byte, error) {
	if !validID(id) {
		return nil, ErrIneligible
	}
	if _, e := checkRelative(s.root, filepath.Join("saved", id), true); e != nil {
		return nil, e
	}
	if s.keychain != nil {
		return s.keychain.Read(s.savedNativeHome(id))
	}
	return readPrivate(s.root, filepath.Join("saved", id, ".credentials.json"), maxCredentialBytes)
}
func (s *Store) writeClaudeAuth(id string, auth []byte) error {
	if err := s.claudeAuthMutationAllowed(id); err != nil {
		return err
	}
	current, e := s.claudeAuth(id)
	if errors.Is(e, os.ErrNotExist) {
		current = nil
	} else if e != nil {
		return e
	}
	merged, e := mergeFields(current, auth, "claudeAiOauth")
	if e != nil {
		return e
	}
	if s.keychain != nil {
		return s.keychain.Write(s.savedNativeHome(id), merged)
	}
	_, e = durableReplace(s.root, filepath.Join("saved", id, ".credentials.json"), merged, s.writeOps)
	return e
}
func (s *Store) ensureCodexFileMode() error {
	name := "provider/codex/native/config.toml"
	b, e := s.readOptional(name)
	if e != nil {
		return e
	}
	m := map[string]any{}
	if len(b) > 0 && toml.Unmarshal(b, &m) != nil {
		return ErrIneligible
	}
	if v, ok := m["cli_auth_credentials_store"]; ok && v != "file" {
		return ErrIneligible
	}
	if codexCompeting(m) {
		return ErrIneligible
	}
	if m["cli_auth_credentials_store"] == "file" {
		return nil
	}
	m["cli_auth_credentials_store"] = "file"
	out, e := toml.Marshal(m)
	if e != nil {
		return e
	}
	_, e = durableReplace(s.root, name, out, s.writeOps)
	return e
}
func codexCompeting(m map[string]any) bool {
	return codexCompetingLevel(m, 0)
}
func codexCompetingLevel(m map[string]any, depth int) bool {
	if depth > 32 {
		return true
	}
	if mode, ok := m["cli_auth_credentials_store"]; ok && mode != "file" {
		return true
	}
	for _, k := range []string{"forced_login_method", "chatgpt_base_url", "chatgpt_auth_tokens_base_url", "openai_base_url", "forced_chatgpt_workspace_id"} {
		if v, ok := m[k]; ok && v != "" && v != "chatgpt" {
			return true
		}
	}
	if v, ok := m["model_provider"]; ok && v != "openai" && v != "" {
		return true
	}
	if providers, ok := m["model_providers"].(map[string]any); ok {
		if _, ok := providers["openai"]; ok {
			return true
		}
	}
	if profiles, ok := m["profiles"].(map[string]any); ok {
		for _, value := range profiles {
			if profile, ok := value.(map[string]any); ok && codexCompetingLevel(profile, depth+1) {
				return true
			}
		}
	}
	return false
}
func (s *Store) validateNative(provider, identity, loginID string) error {
	if provider == ProviderCodex {
		b, e := readPrivate(s.root, "provider/codex/native/config.toml", maxCredentialBytes)
		var m map[string]any
		if e != nil || toml.Unmarshal(b, &m) != nil || m["cli_auth_credentials_store"] != "file" || codexCompeting(m) {
			return ErrIneligible
		}
	}
	if provider == ProviderClaude {
		if e := s.validateClaudeAuthSettings(); e != nil {
			return e
		}
	}
	c, e := s.nativeCredential(provider, loginID)
	if e != nil {
		return e
	}
	_, id, _, _, e := normalizeCredential(provider, c)
	if e != nil {
		return e
	}
	if id != identity {
		return ErrIdentityChanged
	}
	return nil
}

func (s *Store) validateAuthContext(provider string) error {
	if provider == ProviderClaude {
		return s.validateClaudeAuthSettings()
	}
	if provider != ProviderCodex {
		return ErrIneligible
	}
	b, e := s.readOptional("provider/codex/native/config.toml")
	if e != nil {
		return e
	}
	m := map[string]any{}
	if len(b) > 0 && toml.Unmarshal(b, &m) != nil {
		return ErrIneligible
	}
	if mode, ok := m["cli_auth_credentials_store"]; ok && mode != "file" {
		return ErrIneligible
	}
	if codexCompeting(m) {
		return ErrIneligible
	}
	return nil
}

func (s *Store) validateClaudeAuthSettings() error {
	for _, name := range []string{".claude.json", "settings.json", "settings.local.json"} {
		raw, e := s.readOptional(filepath.Join("provider/claude/native", name))
		if e != nil {
			return e
		}
		if len(raw) == 0 {
			continue
		}
		if e = ValidateAuthSettings(ProviderClaude, raw); e != nil {
			return e
		}
	}
	return nil
}

func (s *Store) Runtime(provider string) (Runtime, error) {
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
	if _, ok := r.Switches[provider]; ok {
		return Runtime{}, ErrPending
	}
	return s.runtime(r, provider)
}
func (s *Store) runtime(r Registry, provider string) (Runtime, error) {
	if claudeProfilesOwned(r, provider) {
		return Runtime{}, ErrPending
	}
	p := r.Providers[provider]
	l := find(&r, p.ActiveID)
	if l == nil || l.AuthState != AuthReady {
		return Runtime{}, ErrIneligible
	}
	if codexOwned(r, provider) && (p.DesiredCredentialRevision == 0 || p.DesiredCredentialRevision != l.CredentialRevision) {
		return Runtime{}, ErrPending
	}
	rt := Runtime{Provider: provider, Epoch: p.Epoch, Identity: l.Identity, Home: s.Home(provider), ExternalAuth: codexOwned(r, provider)}
	if e := s.validateSelected(r, provider, l.Identity, l.ID); e != nil {
		return Runtime{}, e
	}
	return rt, nil
}
func (s *Store) ValidateRuntime(rt Runtime) error {
	current, e := s.Runtime(rt.Provider)
	if e != nil {
		return e
	}
	if current != rt {
		return ErrConflict
	}
	return nil
}

// PrepareEnvironment selects only the shared native home; tokens never enter
// launch metadata. Native auth selectors inherited from an owner shell lose.
func prepareEnvironment(rt Runtime, secureHome string, inherited []string) ([]string, error) {
	if !validProvider(rt.Provider) || !filepath.IsAbs(rt.Home) || filepath.Clean(rt.Home) != rt.Home || !validDigest(rt.Identity) || rt.Epoch == 0 {
		return nil, ErrIneligible
	}
	out := make([]string, 0, len(inherited)+3)
	for _, entry := range inherited {
		k, _, ok := strings.Cut(entry, "=")
		if !ok || k == "" || strings.ContainsAny(entry, "\x00\r\n") {
			return nil, ErrIneligible
		}
		if competingEnvironment(k) || (rt.Provider == ProviderClaude && k == "USER") {
			continue
		}
		out = append(out, entry)
	}
	if rt.Provider == ProviderCodex {
		out = append(out, "CODEX_HOME="+rt.Home)
	} else {
		if !filepath.IsAbs(secureHome) || filepath.Clean(secureHome) != secureHome {
			return nil, ErrIneligible
		}
		out = append(out, "CLAUDE_CONFIG_DIR="+rt.Home, "CLAUDE_SECURESTORAGE_CONFIG_DIR="+secureHome, "USER="+NativeUsername())
	}
	return out, nil
}

func (s *Store) PrepareEnvironment(rt Runtime, inherited []string) ([]string, error) {
	return s.environment(rt, inherited, "", "")
}

// PrepareSwitchEnvironment is reserved for a journal-owned replacement launch.
func (s *Store) PrepareSwitchEnvironment(id, source string, rt Runtime, inherited []string) ([]string, error) {
	return s.environment(rt, inherited, id, source)
}
func (s *Store) environment(rt Runtime, inherited []string, id, source string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.safeAnchor(); e != nil {
		return nil, e
	}
	r, e := s.load()
	if e != nil {
		return nil, e
	}
	if r.MigrationPending {
		return nil, ErrPending
	}
	if op, ok := r.Switches[rt.Provider]; ok {
		if id == "" || op.ID != id || (op.Phase != "committed" && op.Phase != "resuming") {
			return nil, ErrPending
		}
		owned := false
		for _, row := range op.Sessions {
			if row.SourceID == source && (row.Phase == "stopped" || row.Phase == "launched") {
				owned = true
				break
			}
		}
		if !owned {
			return nil, ErrPending
		}
	} else if id != "" {
		return nil, ErrPending
	}
	current, e := s.runtime(r, rt.Provider)
	if e != nil {
		return nil, e
	}
	if current != rt {
		return nil, ErrConflict
	}
	secure := rt.Home
	if rt.Provider == ProviderClaude {
		secure = s.savedNativeHome(r.Providers[rt.Provider].ActiveID)
	}
	return prepareEnvironment(rt, secure, inherited)
}
func competingEnvironment(k string) bool {
	switch k {
	case "CODEX_HOME", "CODEX_AUTH_JSON", "OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENAI_ORG_ID", "OPENAI_ORGANIZATION", "OPENAI_PROJECT_ID", "OPENAI_PROJECT", "CODEX_API_KEY", "CODEX_ACCESS_TOKEN", "CODEX_API_ENDPOINT", "CODEX_PROFILE", "CODEX_REFRESH_TOKEN_URL_OVERRIDE", "CODEX_REVOKE_TOKEN_URL_OVERRIDE", "CODEX_CLI_AUTH_CREDENTIALS_STORE", "CODEX_DISABLE_KEYRING", "CLAUDE_CONFIG_DIR", "CLAUDE_SECURESTORAGE_CONFIG_DIR", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "ANTHROPIC_CUSTOM_HEADERS", "AWS_BEARER_TOKEN_BEDROCK", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR", "CLAUDE_CODE_ACCOUNT_UUID", "CLAUDE_CODE_API_KEY", "CLAUDE_CODE_API_KEY_HELPER_TTL_MS", "CLAUDE_CODE_HOST_CREDS_FILE", "CLAUDE_CODE_HOST_GATEWAY_LINEAGE", "CLAUDE_CODE_GATEWAY_URL", "CLAUDE_CODE_USE_GATEWAY", "CLAUDE_CODE_FEDERATION_PROFILE", "CLAUDE_CODE_PROFILE", "CLAUDE_CODE_DEFAULT_PROFILE", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY", "CLAUDE_CODE_USE_ANTHROPIC_AWS", "CLAUDE_CODE_USE_ANTHROPIC_GOOGLE_CLOUD", "CLAUDE_CODE_USE_MANTLE", "CLAUDE_CODE_MANAGED_SETTINGS_PATH", "CLAUDE_CODE_REMOTE_SETTINGS_PATH":
		return true
	}
	return strings.HasPrefix(k, "CODEX_AUTH_") || strings.HasPrefix(k, "CODEX_REMOTE_") || strings.HasPrefix(k, "CLAUDE_CODE_HOST_") || strings.HasPrefix(k, "CLAUDE_CODE_GATEWAY_")
}
