package loginmigration

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
)

type oldBinding struct {
	Provider             string `json:"provider"`
	AccountID            string `json:"account_id"`
	CredentialGeneration uint64 `json:"credential_generation"`
}
type oldGeneration struct {
	Profile  string `json:"profile_generation"`
	Erased   bool   `json:"credential_erased"`
	Erasing  bool   `json:"credential_erasing"`
	Identity string `json:"identity"`
}
type oldAccount struct {
	ID, Provider, Label, Lifecycle, Auth string
	Current                              uint64 `json:"current_generation"`
	Generations                          map[uint64]oldGeneration
}
type oldRegistry struct {
	Schema   int `json:"schema_version"`
	Accounts map[string]oldAccount
}
type importLogin struct {
	Login                  logins.Login
	AuthPath, IdentityPath string
	Unavailable            bool
}
type rewrite struct{ Path, Stage, Before, After string }
type migration struct {
	SchemaVersion int
	Phase         string
	Imports       []importLogin
	Contexts      map[string][]contextSource
	Rewrites      []rewrite
	Remove        []rewrite
	Secrets       []rewrite
}

// RunLocked converts retained state while the caller holds its configured
// daemon singleton lock through conversion and normal session loading.
func RunLocked(state string) error {
	if err := safePath(state); err != nil {
		return err
	}
	var e error
	journal := filepath.Join(state, "logins", "migration.json")
	var plan migration
	if plan, e = readMigration(state); e == nil {
		if plan.Phase == "complete" {
			store, openErr := logins.Open(state)
			if openErr != nil {
				return openErr
			}
			defer func() { _ = store.Close() }()
			registry, loadErr := store.Snapshot()
			if loadErr != nil {
				return loadErr
			}
			if registry.MigrationPending {
				_, loadErr = store.SetMigrationPending(registry.Revision, false)
			}
			return loadErr
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	} else {
		present, presenceErr := legacyPresent(state)
		if presenceErr != nil {
			return presenceErr
		}
		if !present {
			return nil
		}
		if e = revalidateMigrationWriters(state, nil); e != nil {
			return e
		}
		var staged map[string][]byte
		plan, staged, e = inventory(state)
		if e != nil {
			return e
		}
		if len(plan.Imports) == 0 && len(plan.Rewrites) == 0 && len(plan.Remove) == 0 {
			return nil
		}
		if e = validateMigration(state, plan); e != nil {
			return e
		}
		if e = revalidateMigrationWriters(state, &plan); e != nil {
			return e
		}
		for provider, sources := range plan.Contexts {
			if _, e = planContext(state, provider, sources); e != nil {
				return e
			}
			if e = checkExternalWriters(provider); e != nil {
				return e
			}
		}
		if e = privateDir(filepath.Join(state, "logins", "migration-stage")); e != nil {
			return e
		}
		for path, b := range staged {
			if e = privateDir(filepath.Dir(filepath.Join(state, path))); e != nil {
				return e
			}
			if e = replace(filepath.Join(state, path), b); e != nil {
				return e
			}
		}
		if e = writeJSON(journal, plan); e != nil {
			return e
		}
	}
	if e = revalidateMigrationWriters(state, &plan); e != nil {
		return e
	}
	store, e := logins.Open(state)
	if e != nil {
		return e
	}
	defer func() { _ = store.Close() }()
	registry, e := store.Snapshot()
	if e != nil {
		return e
	}
	if !registry.MigrationPending {
		registry, e = store.SetMigrationPending(registry.Revision, true)
		if e != nil {
			return e
		}
	}
	if plan.Phase != "cleanup" {
		for provider, sources := range plan.Contexts {
			if e = prepareContext(state, provider, sources); e != nil {
				return e
			}
		}
		for _, item := range plan.Imports {
			if item.Unavailable {
				registry, _, e = store.ImportUnavailable(registry.Revision, item.Login)
				if e != nil {
					return e
				}
				continue
			}
			auth, e := readFile(item.AuthPath, maxJSON, true)
			if e != nil {
				return e
			}
			c := logins.Credential{Auth: auth}
			if item.IdentityPath != "" {
				c.Identity, e = readFile(item.IdentityPath, maxJSON, true)
				if e != nil {
					return e
				}
			}
			registry, _, e = store.Import(registry.Revision, item.Login, c)
			if e != nil {
				return e
			}
		}
		for _, item := range plan.Rewrites {
			b, e := readFile(filepath.Join(state, item.Path), maxJSON, true)
			if e != nil {
				return e
			}
			sha := digest(b)
			if sha == item.After {
				continue
			}
			if sha != item.Before {
				return ErrHistoryConflict
			}
			b, e = readFile(filepath.Join(state, item.Stage), maxJSON, true)
			if e != nil || digest(b) != item.After {
				return ErrUnsupported
			}
			if e = replace(filepath.Join(state, item.Path), b); e != nil {
				return e
			}
			if e = migrationCheckpoint(item.Path); e != nil {
				return e
			}
		}
		plan.Phase = "cleanup"
		if e = writeJSON(journal, plan); e != nil {
			return e
		}
	}
	if e = revalidateMigrationWriters(state, &plan); e != nil {
		return e
	}
	if e = verifyCleanup(state, store, registry, plan); e != nil {
		return e
	}
	for _, item := range plan.Secrets {
		b, e := readFile(filepath.Join(state, item.Path), maxJSON, true)
		if e != nil {
			return e
		}
		if digest(b) == item.After {
			continue
		}
		if digest(b) != item.Before {
			return ErrHistoryConflict
		}
		b, e = readFile(filepath.Join(state, item.Stage), maxJSON, true)
		if e != nil || digest(b) != item.After {
			return ErrUnsupported
		}
		current, readErr := readFile(filepath.Join(state, item.Path), maxJSON, true)
		if readErr != nil {
			return readErr
		}
		expected, redactErr := redactCredential(item.Path, current)
		if redactErr != nil || digest(expected) != item.After || string(expected) != string(b) {
			return ErrUnsupported
		}
		if e = replace(filepath.Join(state, item.Path), b); e != nil {
			return e
		}
	}
	// Erase only individually inventoried obsolete writer-proof files. Retained
	// profiles, source credentials and external context are never blanket-deleted.
	for _, item := range plan.Remove {
		path := filepath.Join(state, item.Path)
		b, e := readFile(path, maxJSON, true)
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			return e
		}
		if digest(b) != item.Before {
			return ErrHistoryConflict
		}
		if e = os.Remove(path); e != nil {
			return e
		}
		if e = syncDir(filepath.Dir(path)); e != nil {
			return e
		}
	}
	plan.Phase = "complete"
	if e = writeJSON(journal, plan); e != nil {
		return e
	}
	_, e = store.SetMigrationPending(registry.Revision, false)
	return e
}

// Injection stops after a committed rewrite, exercising actual on-disk retry.
var migrationCheckpoint = func(string) error { return nil }

// Preflight reports old writer/recovery/history blockers without changing state.
func Preflight(state string) error {
	plan, err := readMigration(state)
	if err == nil && plan.Phase == "complete" {
		return nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err != nil {
		present, presenceErr := legacyPresent(state)
		if presenceErr != nil {
			return presenceErr
		}
		if !present {
			return nil
		}
		if err = revalidateMigrationWriters(state, nil); err != nil {
			return err
		}
		plan, _, err = inventory(state)
		if err != nil {
			return err
		}
	}
	if len(plan.Imports) == 0 && len(plan.Rewrites) == 0 && len(plan.Remove) == 0 {
		return nil
	}
	if err = validateMigration(state, plan); err != nil {
		return err
	}
	if err = revalidateMigrationWriters(state, &plan); err != nil {
		return err
	}
	for provider, sources := range plan.Contexts {
		if _, err = planContext(state, provider, sources); err != nil {
			return err
		}
		if err = checkExternalWriters(provider); err != nil {
			return err
		}
	}
	return nil
}

func inventory(state string) (migration, map[string][]byte, error) {
	plan := migration{SchemaVersion: 1, Phase: "planned", Contexts: map[string][]contextSource{}}
	staged := map[string][]byte{}
	var registry oldRegistry
	if e := readJSON(filepath.Join(state, "accounts", "registry.json"), &registry); e != nil && !errors.Is(e, os.ErrNotExist) {
		return plan, nil, e
	}
	if registry.Schema != 0 && registry.Schema != 1 {
		return plan, nil, ErrUnsupported
	}
	legacyProviders := map[string]bool{}
	for _, account := range registry.Accounts {
		legacyProviders[account.Provider] = true
	}
	keys := []string{}
	for id := range registry.Accounts {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	identities := map[string]string{}
	for _, id := range keys {
		a := registry.Accounts[id]
		if a.Provider != "codex" && a.Provider != "claude" {
			return plan, nil, ErrUnsupported
		}
		g, ok := a.Generations[a.Current]
		if !ok || !hexID(g.Profile, 32) || !hexID(id, 32) || g.Erasing {
			return plan, nil, ErrUnsupported
		}
		if g.Erased {
			continue
		}
		profile := filepath.Join(state, "accounts", "profiles", g.Profile)
		name := "auth.json"
		if a.Provider == "claude" {
			name = ".credentials.json"
		}
		authPath := filepath.Join(profile, name)
		auth, e := readFile(authPath, maxJSON, true)
		missing := errors.Is(e, os.ErrNotExist)
		if e != nil && !missing {
			return plan, nil, e
		}
		if missing {
			e = nil
		}
		unavailable := missing
		identityPath := ""
		var identity string
		if missing && hexID(g.Identity, 64) {
			identity = g.Identity
		} else if a.Provider == "codex" {
			identity, e = logins.CodexIdentity(auth)
		} else {
			identityPath = filepath.Join(profile, ".claude.json")
			b, readErr := readFile(identityPath, maxJSON, true)
			if readErr != nil {
				return plan, nil, readErr
			}
			identity, e = logins.ClaudeIdentity(b)
		}
		if e != nil {
			return plan, nil, e
		}
		if !missing {
			var credential struct {
				Tokens struct {
					Access  string `json:"access_token"`
					Refresh string `json:"refresh_token"`
				} `json:"tokens"`
				OAuth struct {
					Access string `json:"accessToken"`
				} `json:"claudeAiOauth"`
			}
			if validJSON(auth) != nil || json.Unmarshal(auth, &credential) != nil {
				return plan, nil, ErrUnsupported
			}
			unavailable = a.Provider == "codex" && (credential.Tokens.Access == "" || credential.Tokens.Refresh == "") || a.Provider == "claude" && credential.OAuth.Access == ""
		}
		if g.Identity != "" && g.Identity != identity {
			return plan, nil, logins.ErrIdentityChanged
		}
		key := a.Provider + ":" + identity
		if old, ok := identities[key]; ok {
			return plan, nil, fmt.Errorf("%w: duplicate native login identities %s and %s require explicit credential custody resolution", ErrUnsupported, old, id)
		}
		identities[key] = id
		authState := logins.AuthReady
		if a.Auth != "valid" || unavailable {
			authState = logins.AuthNeedsLogin
		}
		if a.Lifecycle != "enabled" {
			authState = logins.AuthPaused
		}
		label := a.Label
		if strings.TrimSpace(label) == "" {
			label = a.Provider + " login"
		}
		plan.Imports = append(plan.Imports, importLogin{Login: logins.Login{ID: id, Provider: a.Provider, Label: label, Identity: identity, AuthState: authState}, AuthPath: authPath, IdentityPath: identityPath, Unavailable: unavailable})
		if !unavailable {
			if e = stageSecretCleanup(state, authPath, &plan, staged); e != nil {
				return plan, nil, e
			}
			if identityPath != "" {
				if e = stageSecretCleanup(state, identityPath, &plan, staged); e != nil {
					return plan, nil, e
				}
			}
		}
	}
	entries, e := os.ReadDir(state)
	if e != nil {
		return plan, nil, e
	}
	for _, entry := range entries {
		if !entry.IsDir() || !persist.ValidID(entry.Name()) {
			continue
		}
		path := filepath.Join(state, entry.Name(), "meta.json")
		raw, e := readFile(path, maxJSON, true)
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			return plan, nil, e
		}
		if validJSON(raw) != nil {
			return plan, nil, ErrUnsupported
		}
		var fields map[string]json.RawMessage
		var m persist.Meta
		if json.Unmarshal(raw, &fields) != nil || json.Unmarshal(raw, &m) != nil {
			return plan, nil, ErrUnsupported
		}
		var binding *oldBinding
		if json.Unmarshal(fields["account_binding"], &binding) != nil && len(fields["account_binding"]) != 0 {
			return plan, nil, ErrUnsupported
		}
		if m.AgentType != "codex" && m.AgentType != "claude" {
			continue
		}
		if binding == nil && !legacyProviders[m.AgentType] {
			continue
		}
		if m.ID != entry.Name() {
			return plan, nil, ErrUnsupported
		}
		if live(m.ShimPID, m.ShimStartTime) {
			return plan, nil, fmt.Errorf("%w: discussion %s is still running", ErrLiveWriter, m.ID)
		}
		if m.SchemaVersion != 1 && m.SchemaVersion != 2 && m.SchemaVersion != persist.LoginSchemaVersion {
			return plan, nil, ErrUnsupported
		}
		if e = inventoryProof(state, m.ID, &plan); e != nil {
			return plan, nil, e
		}
		home, e := sourceHome(m.AgentType, m.Env)
		if e != nil {
			return plan, nil, e
		}
		historyOnly := false
		if binding != nil {
			// The owner context seeds settings; a copied legacy profile supplies
			// only its referenced conversation when no native alias exists.
			plan.Contexts[m.AgentType] = append(plan.Contexts[m.AgentType], contextSource{Home: home})
			a, ok := registry.Accounts[binding.AccountID]
			if !ok || a.Provider != m.AgentType {
				return plan, nil, ErrUnsupported
			}
			g, ok := a.Generations[binding.CredentialGeneration]
			if !ok || !hexID(g.Profile, 32) {
				return plan, nil, ErrUnsupported
			}
			home = filepath.Join(state, "accounts", "profiles", g.Profile)
			historyOnly = true
			var projection string
			_ = json.Unmarshal(fields["account_projection_ref"], &projection)
			if projection != "" {
				origin, e := projectionHome(state, projection, m.AgentType, m.ProviderCwd())
				if e != nil {
					return plan, nil, e
				}
				if origin != "" {
					home = origin
					historyOnly = false
				}
			}
		}
		if m.ConversationID != "" {
			if !uuidRE.MatchString(m.ConversationID) || uuidRE.FindString(m.ConversationID) != m.ConversationID {
				return plan, nil, ErrUnsupported
			}
			plan.Contexts[m.AgentType] = append(plan.Contexts[m.AgentType], contextSource{Home: home, Conversation: m.ConversationID, Cwd: m.ProviderCwd(), HistoryOnly: historyOnly})
		}
		if binding == nil {
			continue
		}
		if m.InputEmbargo != "" {
			if !terminalAccountEmbargo(state, m.InputEmbargo) {
				return plan, nil, fmt.Errorf("%w: discussion %s retains an unreconciled account input fence", ErrUnsupported, m.ID)
			}
			delete(fields, "input_embargo")
		}
		delete(fields, "account_binding")
		delete(fields, "account_projection_ref")
		delete(fields, "account_claude_fallback")
		fields["schema_version"] = json.RawMessage("1")
		env := commonEnvironment(m.AgentType, providerHome(state, m.AgentType), m.Env)
		fields["env"], _ = json.Marshal(env)
		after, e := json.Marshal(fields)
		if e != nil {
			return plan, nil, e
		}
		addRewrite(&plan, staged, filepath.Join(m.ID, "meta.json"), raw, after)
	}
	if e = inventoryRecovery(state, &plan, staged); e != nil {
		return plan, nil, e
	}
	if e = inventoryWorkers(state); e != nil {
		return plan, nil, e
	}
	return plan, staged, nil
}
func terminalAccountEmbargo(state, embargo string) bool {
	var document struct {
		Rotations map[string]struct {
			State    string `json:"state"`
			Incident struct {
				ID string `json:"id"`
			} `json:"incident"`
		} `json:"account_rotations"`
	}
	if readJSON(filepath.Join(state, "auth-watch-state.json"), &document) != nil {
		return false
	}
	for _, row := range document.Rotations {
		if (row.State == "complete" || row.State == "owner-canceled") && row.Incident.ID == embargo {
			return true
		}
	}
	return false
}
func addRewrite(plan *migration, staged map[string][]byte, path string, before, after []byte) {
	stage := filepath.Join("logins", "migration-stage", digest([]byte(path))+".json")
	plan.Rewrites = append(plan.Rewrites, rewrite{path, stage, digest(before), digest(after)})
	staged[stage] = after
}
func stageSecretCleanup(state, path string, plan *migration, staged map[string][]byte) error {
	raw, e := readFile(path, maxJSON, true)
	if e != nil {
		return e
	}
	after, e := redactCredential(path, raw)
	if e != nil {
		return e
	}
	relative, e := filepath.Rel(state, path)
	if e != nil || !filepath.IsLocal(relative) {
		return ErrUnsupported
	}
	stage := filepath.Join("logins", "migration-stage", digest([]byte("credential-cleanup:"+relative))+".json")
	plan.Secrets = append(plan.Secrets, rewrite{relative, stage, digest(raw), digest(after)})
	staged[stage] = after
	return nil
}
func commonEnvironment(provider, home string, env []string) []string {
	out := []string{}
	for _, entry := range env {
		k, _, _ := strings.Cut(entry, "=")
		if k != "CODEX_HOME" && k != "CLAUDE_CONFIG_DIR" && k != "CLAUDE_SECURESTORAGE_CONFIG_DIR" {
			out = append(out, entry)
		}
	}
	if provider == "codex" {
		return append(out, "CODEX_HOME="+home)
	}
	return append(out, "CLAUDE_CONFIG_DIR="+home, "CLAUDE_SECURESTORAGE_CONFIG_DIR="+home)
}
func hexID(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
func projectionHome(state, ref, provider, cwd string) (string, error) {
	if !hexID(ref, 64) {
		return "", ErrUnsupported
	}
	raw, e := readFile(filepath.Join(state, "accounts", "configurations", ref, "projection.json"), maxJSON, true)
	if e != nil {
		return "", e
	}
	if digest(raw) != ref {
		return "", ErrUnsupported
	}
	type alias struct{ Path, Canonical string }
	type context struct{ SourceProfile alias }
	type history struct {
		SourcePath    string
		Device, Inode uint64
	}
	var p struct {
		SchemaVersion                         int
		Provider, Cwd                         string
		CodexContext, ClaudeContext           *context
		CodexHistoryAlias, ClaudeHistoryAlias *history
	}
	if validJSON(raw) != nil || json.Unmarshal(raw, &p) != nil || p.Provider != provider || p.Cwd != cwd {
		return "", ErrUnsupported
	}
	if p.SchemaVersion != 1 && p.SchemaVersion != 2 && p.SchemaVersion != 3 {
		return "", ErrUnsupported
	}
	if p.SchemaVersion != 3 {
		return "", nil
	}
	ctx, h := p.CodexContext, p.CodexHistoryAlias
	if provider == "claude" {
		ctx, h = p.ClaudeContext, p.ClaudeHistoryAlias
	}
	if ctx == nil || h == nil {
		return "", ErrUnsupported
	}
	if e = safePath(h.SourcePath); e != nil {
		return "", e
	}
	i, e := os.Lstat(h.SourcePath)
	if e != nil || !owned(i, false) || !i.IsDir() {
		return "", ErrUnsupported
	}
	stat := i.Sys().(*syscall.Stat_t)
	if uint64(stat.Dev) != h.Device || stat.Ino != h.Inode {
		return "", ErrHistoryConflict
	}
	canonical, e := filepath.EvalSymlinks(ctx.SourceProfile.Path)
	if e != nil || canonical != ctx.SourceProfile.Canonical {
		return "", ErrHistoryConflict
	}
	return canonical, nil
}
func inventoryProof(state, id string, plan *migration) error {
	file := filepath.Join(id, "native-process.json")
	var native map[string]json.RawMessage
	raw, e := readFile(filepath.Join(state, file), maxJSON, true)
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	if validJSON(raw) != nil || json.Unmarshal(raw, &native) != nil {
		return ErrUnsupported
	}
	var schema int
	_ = json.Unmarshal(native["schema_version"], &schema)
	if schema == 3 {
		return nil
	}
	if schema != 2 {
		return ErrUnsupported
	}
	var processes struct {
		PID          int   `json:"pid"`
		Start        int64 `json:"start_time"`
		Backend      int   `json:"backend_pid"`
		BackendStart int64 `json:"backend_start_time"`
		Shim         int   `json:"shim_pid"`
		ShimStart    int64 `json:"shim_start_time"`
	}
	if json.Unmarshal(raw, &processes) != nil {
		return ErrUnsupported
	}
	if live(processes.PID, processes.Start) || live(processes.Backend, processes.BackendStart) || live(processes.Shim, processes.ShimStart) {
		return ErrLiveWriter
	}
	var stopped struct {
		Native  map[string]json.RawMessage `json:"native"`
		Stopped bool                       `json:"writers_stopped"`
	}
	proof := filepath.Join(id, "native-writers-stopped.json")
	proofRaw, e := readFile(filepath.Join(state, proof), maxJSON, true)
	if e != nil || validJSON(proofRaw) != nil || json.Unmarshal(proofRaw, &stopped) != nil || !stopped.Stopped {
		return fmt.Errorf("%w: discussion %s has no native containment proof", ErrLiveWriter, id)
	}
	a, _ := json.Marshal(native)
	b, _ := json.Marshal(stopped.Native)
	if !reflect.DeepEqual(a, b) {
		return ErrUnsupported
	}
	plan.Remove = append(plan.Remove, rewrite{Path: file, Before: digest(raw)}, rewrite{Path: proof, Before: digest(proofRaw)})
	return nil
}
func inventoryRecovery(state string, plan *migration, staged map[string][]byte) error {
	path := "auth-watch-state.json"
	raw, e := readFile(filepath.Join(state, path), maxJSON, true)
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	var fields map[string]json.RawMessage
	if validJSON(raw) != nil || json.Unmarshal(raw, &fields) != nil || fields == nil {
		return ErrUnsupported
	}
	changed := false
	if b := fields["account_rotations"]; len(b) > 0 {
		var rows map[string]struct {
			State  string `json:"state"`
			Source string `json:"source_id"`
		}
		if json.Unmarshal(b, &rows) != nil {
			return ErrUnsupported
		}
		for id, row := range rows {
			if row.State != "complete" && row.State != "owner-canceled" {
				return fmt.Errorf("%w: old account recovery %s for discussion %s is %s; finish or cancel it with the previous version before offline conversion", ErrUnsupported, id, row.Source, row.State)
			}
		}
	}
	for key := range fields {
		if strings.HasPrefix(key, "account_") {
			delete(fields, key)
			changed = true
		}
	}
	if b := fields["cli_refresh"]; len(b) > 0 {
		var rows map[string]map[string]json.RawMessage
		if json.Unmarshal(b, &rows) != nil {
			return ErrUnsupported
		}
		for id, row := range rows {
			if _, ok := row["account_enrollment"]; ok {
				var state string
				_ = json.Unmarshal(row["state"], &state)
				if state != "complete" && state != "blocked" {
					return fmt.Errorf("%w: old enrollment recovery for discussion %s is %s", ErrUnsupported, id, state)
				}
				delete(row, "account_enrollment")
				changed = true
			}
		}
		fields["cli_refresh"], _ = json.Marshal(rows)
	}
	if changed {
		after, _ := json.Marshal(fields)
		addRewrite(plan, staged, path, raw, after)
	}
	return nil
}
func inventoryWorkers(state string) error {
	_, _, err := inventoryEnrollmentWorkers(state)
	if err != nil {
		return err
	}
	return inventoryCheckWorkers(state)
}
