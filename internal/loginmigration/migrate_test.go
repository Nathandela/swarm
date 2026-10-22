package loginmigration

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/processcontain"
	"github.com/Nathandela/swarm/internal/procstart"
	"github.com/Nathandela/swarm/internal/status"
)

const conversation = "123e4567-e89b-42d3-a456-426614174013"

type fixture struct {
	state, home, profile, project, id, account string
	meta                                       map[string]any
}

func put(t *testing.T, path string, v any) {
	t.Helper()
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		t.Fatal(e)
	}
	var b []byte
	switch x := v.(type) {
	case []byte:
		b = x
	case string:
		b = []byte(x)
	default:
		var e error
		b, e = json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
	}
	if e := os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
}
func quietWriters(t *testing.T) {
	t.Helper()
	oldEnvironment := environmentWriters
	environmentWriters = func([]string, []string) ([]processcontain.Identity, error) { return nil, nil }
	t.Cleanup(func() { environmentWriters = oldEnvironment })
	old := checkExternalWriters
	checkExternalWriters = func(string) error { return nil }
	t.Cleanup(func() { checkExternalWriters = old })
}
func newFixture(t *testing.T) fixture {
	t.Helper()
	quietWriters(t)
	root, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	f := fixture{state: filepath.Join(root, "state"), home: filepath.Join(root, "native"), project: filepath.Join(root, "project"), id: "retained-discussion", account: strings.Repeat("a", 32)}
	f.profile = filepath.Join(f.state, "accounts", "profiles", strings.Repeat("b", 32))
	for _, p := range []string{f.state, f.home, f.project, f.profile} {
		if e = os.MkdirAll(p, 0700); e != nil {
			t.Fatal(e)
		}
	}
	put(t, filepath.Join(f.profile, "auth.json"), `{"auth_mode":"chatgpt","tokens":{"account_id":"synthetic-account","access_token":"synthetic-access","refresh_token":"synthetic-refresh"}}`)
	put(t, filepath.Join(f.state, "accounts", "registry.json"), map[string]any{"schema_version": 1, "accounts": map[string]any{f.account: map[string]any{"id": f.account, "provider": "codex", "label": "Saved login", "lifecycle": "enabled", "auth": "valid", "current_generation": 1, "generations": map[string]any{"1": map[string]any{"profile_generation": strings.Repeat("b", 32)}}}}})
	put(t, filepath.Join(f.home, "config.toml"), "model = \"gpt-6-sol\"\n")
	history := filepath.Join(f.profile, "sessions", "2026", "10", "22", "rollout-"+conversation+".jsonl")
	put(t, history, "{\"type\":\"session_meta\",\"payload\":{\"id\":\""+conversation+"\"}}\n")
	f.meta = map[string]any{"schema_version": 2, "id": f.id, "agent_type": "codex", "name": "Keep my name", "cwd": f.project, "agent_cwd": f.project, "conversation_id": conversation, "launch_options": map[string]string{"model": "gpt-6-sol"}, "env": []string{"HOME=" + filepath.Dir(f.home)}, "status": status.Status{Process: status.ProcessExited, Turn: status.TurnIdle, Interaction: status.InteractionNone}, "roster_hidden": true, "resumed_from": "previous-discussion", "account_binding": map[string]any{"schema_version": 1, "provider": "codex", "account_id": f.account, "credential_generation": 1, "configuration_generation": 1, "identity": strings.Repeat("c", 64)}, "account_projection_ref": "", "account_claude_fallback": nil}
	put(t, filepath.Join(f.state, f.id, "meta.json"), f.meta)
	return f
}
func TestOfflineMigrationPreservesExactResumeAndGenericRecovery(t *testing.T) {
	f := newFixture(t)
	generic := map[string]any{"identities": map[string]string{"codex": "native-identity"}, "pending": map[string][]string{"codex": {f.id}}, "killed": map[string]bool{f.id: true}, "candidates": map[string]string{f.id: "replacement"}, "retries": map[string]any{f.id: map[string]any{"attempts": 2}}, "cli_refresh": map[string]any{}, "cli_candidates": map[string]any{}}
	old := map[string]any{}
	for k, v := range generic {
		old[k] = v
	}
	old["account_schema_version"] = 1
	old["account_rotations"] = map[string]any{"incident": map[string]string{"state": "complete", "source_id": f.id}}
	put(t, filepath.Join(f.state, "auth-watch-state.json"), old)
	if e := Run(f.state); e != nil {
		t.Fatal(e)
	}
	store, e := persist.NewStore(f.state)
	if e != nil {
		t.Fatal(e)
	}
	m, e := store.Load(f.id)
	if e != nil {
		t.Fatal(e)
	}
	if m.ConversationID != conversation || m.ProviderCwd() != f.project || m.LaunchOptions["model"] != "gpt-6-sol" || m.Name != "Keep my name" || !m.RosterHidden || m.ResumedFrom != "previous-discussion" {
		t.Fatalf("retained conversation changed: %+v", m)
	}
	b, e := os.ReadFile(filepath.Join(f.state, f.id, "meta.json"))
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(b), "account_binding") || strings.Contains(string(b), "account_projection_ref") {
		t.Fatal("retired runtime fields survived")
	}
	if !strings.Contains(strings.Join(m.Env, "\n"), "CODEX_HOME="+providerHome(f.state, "codex")) {
		t.Fatal("exact resume does not select imported native history")
	}
	var after map[string]any
	if e = readJSON(filepath.Join(f.state, "auth-watch-state.json"), &after); e != nil {
		t.Fatal(e)
	}
	normalized, _ := json.Marshal(generic)
	var want map[string]any
	_ = json.Unmarshal(normalized, &want)
	if !reflect.DeepEqual(after, want) {
		t.Fatalf("generic recovery state changed: %v", after)
	}
	s, e := logins.Open(f.state)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = s.Close() }()
	r, e := s.Snapshot()
	if e != nil || len(r.Logins) != 1 || r.MigrationPending || r.Providers["codex"].ActiveID != "" {
		t.Fatalf("unexpected login import: %+v %v", r, e)
	}
	if e = verifyConversations(s.Home("codex"), "codex", []contextSource{{Conversation: conversation}}); e != nil {
		t.Fatal(e)
	}
	if e = Run(f.state); e != nil {
		t.Fatalf("completed migration is not idempotent: %v", e)
	}
}
func TestMigrationRetriesAfterCommittedMetaRewrite(t *testing.T) {
	f := newFixture(t)
	injected := errors.New("simulated process death")
	old := migrationCheckpoint
	migrationCheckpoint = func(string) error { return injected }
	t.Cleanup(func() { migrationCheckpoint = old })
	if e := Run(f.state); !errors.Is(e, injected) {
		t.Fatalf("want injected crash, got %v", e)
	}
	migrationCheckpoint = old
	if e := Run(f.state); e != nil {
		t.Fatal(e)
	}
	s, e := logins.Open(f.state)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = s.Close() }()
	r, e := s.Snapshot()
	if e != nil || len(r.Logins) != 1 || r.MigrationPending {
		t.Fatalf("retry duplicated login or left migration pending: %+v %v", r, e)
	}
}
func TestMigrationRefusesLiveAndInterruptedOwnershipBeforeMutation(t *testing.T) {
	for _, kind := range []string{"live-shim", "old-recovery", "external-writer"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			switch kind {
			case "live-shim":
				f.meta["shim_pid"] = os.Getpid()
				start, e := procstart.StartTime(os.Getpid())
				if e != nil {
					t.Fatal(e)
				}
				f.meta["shim_start_time"] = start
				put(t, filepath.Join(f.state, f.id, "meta.json"), f.meta)
			case "old-recovery":
				put(t, filepath.Join(f.state, "auth-watch-state.json"), map[string]any{"account_rotations": map[string]any{"old-incident": map[string]string{"state": "old-stopped", "source_id": f.id}}})
			case "external-writer":
				checkExternalWriters = func(string) error { return ErrLiveWriter }
			}
			original, e := os.ReadFile(filepath.Join(f.state, f.id, "meta.json"))
			if e != nil {
				t.Fatal(e)
			}
			if e = Run(f.state); e == nil {
				t.Fatal("unsafe offline conversion succeeded")
			}
			after, _ := os.ReadFile(filepath.Join(f.state, f.id, "meta.json"))
			if string(after) != string(original) {
				t.Fatal("refusal changed retained metadata")
			}
			if _, e = os.Stat(filepath.Join(f.state, "logins")); !errors.Is(e, os.ErrNotExist) {
				t.Fatal("refusal mutated login storage")
			}
		})
	}
}
func TestSchema3HistoryAliasAndErasedCredentialRemainReadable(t *testing.T) {
	f := newFixture(t)
	history := filepath.Join(f.home, "sessions")
	if e := os.Rename(filepath.Join(f.profile, "sessions"), history); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(history, filepath.Join(f.profile, "sessions")); e != nil {
		t.Fatal(e)
	}
	info, e := os.Stat(history)
	if e != nil {
		t.Fatal(e)
	}
	stat := info.Sys().(*syscall.Stat_t)
	projection := map[string]any{"SchemaVersion": 3, "Provider": "codex", "Cwd": f.project, "CodexContext": map[string]any{"SourceProfile": map[string]string{"Path": f.home, "Canonical": f.home}}, "CodexHistoryAlias": map[string]any{"SourcePath": history, "Device": uint64(stat.Dev), "Inode": stat.Ino}}
	raw, _ := json.Marshal(projection)
	ref := digest(raw)
	put(t, filepath.Join(f.state, "accounts", "configurations", ref, "projection.json"), raw)
	f.meta["account_projection_ref"] = ref
	put(t, filepath.Join(f.state, f.id, "meta.json"), f.meta)
	var registry map[string]any
	if e = readJSON(filepath.Join(f.state, "accounts", "registry.json"), &registry); e != nil {
		t.Fatal(e)
	}
	registry["accounts"].(map[string]any)[f.account].(map[string]any)["generations"].(map[string]any)["1"].(map[string]any)["credential_erased"] = true
	put(t, filepath.Join(f.state, "accounts", "registry.json"), registry)
	if e = os.Remove(filepath.Join(f.profile, "auth.json")); e != nil {
		t.Fatal(e)
	}
	if e = Run(f.state); e != nil {
		t.Fatal(e)
	}
	if e = verifyConversations(providerHome(f.state, "codex"), "codex", []contextSource{{Conversation: conversation}}); e != nil {
		t.Fatal(e)
	}
}
func TestContextRefusesSameConversationDifferentHistory(t *testing.T) {
	f := newFixture(t)
	second := filepath.Join(filepath.Dir(f.state), "second-history")
	put(t, filepath.Join(second, "sessions", "different-prefix-"+conversation+".jsonl"), "{\"type\":\"session_meta\",\"payload\":{\"id\":\""+conversation+"\",\"different\":true}}\n")
	_, e := planContext(f.state, "codex", []contextSource{{Home: f.profile, Conversation: conversation}, {Home: second, Conversation: conversation}})
	if !errors.Is(e, ErrHistoryConflict) {
		t.Fatalf("want history conflict, got %v", e)
	}
	if _, e = os.Stat(providerHome(f.state, "codex")); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("collision preflight mutated destination")
	}
}
func TestContextBecomesIndependentAuthorityAfterAtomicBootstrap(t *testing.T) {
	f := newFixture(t)
	sources := []persist.Meta{{ID: f.id, AgentType: "codex", ConversationID: conversation, Cwd: f.project, Env: []string{"CODEX_HOME=" + f.profile}}}
	if e := PreflightProviderContext(f.state, "codex", sources); e != nil {
		t.Fatal(e)
	}
	if e := PrepareProviderContext(f.state, "codex", sources); e != nil {
		t.Fatal(e)
	}
	put(t, filepath.Join(f.profile, "sessions", "2026", "10", "22", "rollout-"+conversation+".jsonl"), "later owner edit")
	if e := PrepareProviderContext(f.state, "codex", sources); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(filepath.Join(providerHome(f.state, "codex"), "sessions", "2026", "10", "22", "rollout-"+conversation+".jsonl"))
	if e != nil || string(b) == "later owner edit" {
		t.Fatal("independent authority was overwritten")
	}
}

func TestImportClearedCredentialRemainsNeedsLogin(t *testing.T) {
	f := newFixture(t)
	var registry map[string]any
	if e := readJSON(filepath.Join(f.state, "accounts", "registry.json"), &registry); e != nil {
		t.Fatal(e)
	}
	identity, e := logins.CodexIdentity([]byte(`{"tokens":{"account_id":"synthetic-account"}}`))
	if e != nil {
		t.Fatal(e)
	}
	generation := registry["accounts"].(map[string]any)[f.account].(map[string]any)["generations"].(map[string]any)["1"].(map[string]any)
	generation["identity"] = identity
	put(t, filepath.Join(f.state, "accounts", "registry.json"), registry)
	if e = os.Remove(filepath.Join(f.profile, "auth.json")); e != nil {
		t.Fatal(e)
	}
	if e = Run(f.state); e != nil {
		t.Fatal(e)
	}
	s, e := logins.Open(f.state)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = s.Close() }()
	r, e := s.Snapshot()
	if e != nil || len(r.Logins) != 1 || r.Logins[0].AuthState != logins.AuthNeedsLogin || r.Logins[0].Identity != identity {
		t.Fatalf("unusable native identity lost: %+v %v", r, e)
	}
}
func TestCompletionMarkerRecoversPendingRegistryRelease(t *testing.T) {
	f := newFixture(t)
	if e := Run(f.state); e != nil {
		t.Fatal(e)
	}
	s, e := logins.Open(f.state)
	if e != nil {
		t.Fatal(e)
	}
	r, e := s.Snapshot()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.SetMigrationPending(r.Revision, true); e != nil {
		t.Fatal(e)
	}
	_ = s.Close()
	if e = Run(f.state); e != nil {
		t.Fatal(e)
	}
	if e = Preflight(f.state); e != nil {
		t.Fatal(e)
	}
	s, e = logins.Open(f.state)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = s.Close() }()
	r, e = s.Snapshot()
	if e != nil || r.MigrationPending {
		t.Fatal("completion crash left launch barrier permanently pending")
	}
}
func TestOnlyReferencedHistoryAndRequiredSiblingsAreCopied(t *testing.T) {
	f := newFixture(t)
	other := "123e4567-e89b-42d3-a456-426614174099"
	put(t, filepath.Join(f.profile, "sessions", "rollout-"+other+".jsonl"), "{\"type\":\"session_meta\",\"payload\":{\"id\":\""+other+"\"}}\n")
	put(t, filepath.Join(f.profile, "state_5.sqlite"), "owner database must never be copied")
	put(t, filepath.Join(f.profile, "history.jsonl"), "unrelated input history")
	plan, e := planContext(f.state, "codex", []contextSource{{Home: f.profile, Conversation: conversation}})
	if e != nil {
		t.Fatal(e)
	}
	for _, item := range plan.Files {
		if strings.Contains(item.Target, other) || strings.HasSuffix(item.Target, ".sqlite") || item.Target == "history.jsonl" {
			t.Fatalf("unrelated owner state selected: %s", item.Target)
		}
	}
	if len(plan.Files) != 1 {
		t.Fatalf("want one referenced rollout, got %d files", len(plan.Files))
	}
	root := filepath.Join(filepath.Dir(f.state), "claude-history")
	put(t, filepath.Join(root, "projects", "project-key", conversation+".jsonl"), "native conversation")
	put(t, filepath.Join(root, "projects", "project-key", conversation, "subagents", "agent-small.jsonl"), "required subagent")
	put(t, filepath.Join(root, "projects", "other-project", other+".jsonl"), "unrelated")
	plan, e = planContext(f.state, "claude", []contextSource{{Home: root, Conversation: conversation}})
	if e != nil {
		t.Fatal(e)
	}
	if len(plan.Files) != 2 {
		t.Fatalf("lost conversation sibling or copied unrelated history: %+v", plan.Files)
	}
}
func TestContextRejectsCompetingAuthBeforeMutation(t *testing.T) {
	f := newFixture(t)
	put(t, filepath.Join(f.profile, "config.toml"), "cli_auth_credentials_store=\"keyring\"\n")
	_, e := planContext(f.state, "codex", []contextSource{{Home: f.profile, Conversation: conversation}})
	if e == nil {
		t.Fatal("competing native auth accepted")
	}
	if _, e = os.Stat(filepath.Join(f.state, "logins")); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("read-only preflight changed native state")
	}
}
func TestTerminalAccountFenceClearsOnlyAfterOwnershipResolved(t *testing.T) {
	f := newFixture(t)
	f.meta["input_embargo"] = "old-incident"
	put(t, filepath.Join(f.state, f.id, "meta.json"), f.meta)
	put(t, filepath.Join(f.state, "auth-watch-state.json"), map[string]any{"account_rotations": map[string]any{f.id: map[string]any{"state": "complete", "source_id": f.id, "incident": map[string]string{"id": "old-incident"}}}})
	if e := Run(f.state); e != nil {
		t.Fatal(e)
	}
	var m persist.Meta
	if e := readJSON(filepath.Join(f.state, f.id, "meta.json"), &m); e != nil {
		t.Fatal(e)
	}
	if m.InputEmbargo != "" {
		t.Fatal("obsolete completed account fence survived")
	}
}
func TestObsoleteCredentialGrantIsErasedOnlyAfterImport(t *testing.T) {
	f := newFixture(t)
	if e := Run(f.state); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(filepath.Join(f.profile, "auth.json"))
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(b), "synthetic-refresh") {
		t.Fatal("old source retained an independently refreshable duplicate grant")
	}
	s, e := logins.Open(f.state)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = s.Close() }()
	if _, e = s.PrepareActivation(f.account); e != nil {
		t.Fatal("credential was erased before durable import", e)
	}
}

// Standalone conversion wrapper used only by isolated migration fixtures.
func Run(state string) error {
	if e := safePath(state); e != nil {
		return e
	}
	lock, e := os.OpenFile(filepath.Join(state, "daemon.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return e
	}
	defer func() { _ = lock.Close() }()
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return ErrLiveWriter
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()
	return RunLocked(state)
}
