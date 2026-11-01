package skeleton

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/loginworker"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

func claudeMaintenanceCaller(t *testing.T) (*coreAPI, logins.Login, map[string]int) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("public Store fixture uses synthetic Linux file authority, never Darwin Keychain")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, selected, _ := claudeOwnerBirthFixture(t, root, t.TempDir())
	d := &Daemon{stateDir: root}
	m := &loginManager{d: d, store: store}
	d.logins = m
	api := &coreAPI{logins: m}
	d.api = api
	qualifiedClaudeCallerFixture(t, m)
	if err := m.prepareClaudeSelection(context.Background()); err != nil {
		t.Fatal(err)
	}
	starts := map[string]int{}
	m.claudeAuthStart = func(_ context.Context, _ string, c loginworker.ClaudeAuthConfig) (logins.ClaudeAuthResult, error) {
		starts[c.Ref.LoginID]++
		return store.ReadClaudeAuth(c.Ref)
	}
	m.claudeAuthRecover = func(_ context.Context, c loginworker.ClaudeAuthConfig) (loginworker.ClaudeAuthRecovery, error) {
		if err := store.PrepareClaudeAuthRecovery(c.Ref); err != nil {
			return loginworker.ClaudeAuthRecovery{}, err
		}
		_, err := store.SettleClaudeAuth(c.Ref)
		return loginworker.ClaudeAuthRecovery{Settled: err == nil}, err
	}
	return api, selected, starts
}

func addClaudeMaintenanceLogin(t *testing.T, api *coreAPI, name string) logins.Login {
	t.Helper()
	grant, _ := json.Marshal(map[string]any{"claudeAiOauth": map[string]any{"accessToken": "synthetic-" + name, "refreshToken": "synthetic-refresh-" + name, "expiresAt": time.Now().Add(time.Hour).UnixMilli(), "scopes": []string{"user:inference", "user:profile"}}})
	identity, _ := json.Marshal(map[string]any{"oauthAccount": map[string]string{"accountUuid": name, "organizationUuid": "synthetic-org"}})
	r := mustLoginSnapshot(t, api.logins.store)
	_, login, err := api.logins.store.Add(r.Revision, logins.ProviderClaude, name, logins.Credential{Auth: grant, Identity: identity})
	if err != nil {
		t.Fatal(err)
	}
	return login
}

func TestClaudeMaintenanceCallerVisitsQuotaLimitedAndLaterParkedLogins(t *testing.T) {
	api, selected, starts := claudeMaintenanceCaller(t)
	b := addClaudeMaintenanceLogin(t, api, "parked-b")
	c := addClaudeMaintenanceLogin(t, api, "parked-c")
	store := api.logins.store
	if _, err := store.UpdateExhaustion(mustLoginSnapshot(t, store).Revision, b.ID, time.Now().Add(time.Hour), false); err != nil {
		t.Fatal(err)
	}
	for range 24 {
		api.runLoginJobs(context.Background())
	}
	if starts[b.ID] != 1 || starts[c.ID] != 1 || starts[selected.ID] != 0 {
		t.Fatalf("quota or first-candidate starvation / repeated failed budget: starts=%v", starts)
	}
	for range 8 {
		api.runLoginJobs(context.Background())
	}
	if starts[b.ID] != 1 || starts[c.ID] != 1 {
		t.Fatal("ordinary ticks reset failed parked helper budgets")
	}
}

func TestClaudeMaintenanceCallerKeepsSelectedNonrenewableAuthentication(t *testing.T) {
	api, selected, starts := claudeMaintenanceCaller(t)
	store := api.logins.store
	grant, _ := json.Marshal(map[string]any{"claudeAiOauth": map[string]any{"accessToken": "synthetic-no-refresh", "expiresAt": time.Now().Add(time.Hour).UnixMilli(), "scopes": []string{"user:inference", "user:profile"}}})
	credential := logins.Credential{Auth: grant, Identity: json.RawMessage(`{"oauthAccount":{"accountUuid":"synthetic-account","organizationUuid":"synthetic-org"},"userID":"synthetic-account"}`)}
	if _, login, err := store.Relogin(mustLoginSnapshot(t, store).Revision, selected.ID, credential); err != nil || login.Renewable {
		t.Fatalf("selected nonrenewable fixture: %+v %v", login, err)
	}
	for range 8 {
		api.runLoginJobs(context.Background())
	}
	if starts[selected.ID] != 1 {
		t.Fatalf("selected current-access authentication was dropped: %v", starts)
	}
}

func TestClaudeMaintenanceCallerNoisySelectedCannotStarveParked(t *testing.T) {
	api, selected, starts := claudeMaintenanceCaller(t)
	b := addClaudeMaintenanceLogin(t, api, "parked-b")
	c := addClaudeMaintenanceLogin(t, api, "parked-c")
	store := api.logins.store
	start := api.logins.claudeAuthStart
	api.logins.claudeAuthStart = func(ctx context.Context, executable string, cfg loginworker.ClaudeAuthConfig) (logins.ClaudeAuthResult, error) {
		result, err := start(ctx, executable, cfg)
		if err != nil || cfg.Ref.LoginID != selected.ID {
			return result, err
		}
		// Completed-worker boundary fixture, matching claudeOwnerBirthFixture.
		raw, err := os.ReadFile(filepath.Join(store.StateRoot(), "logins", "saved", selected.ID, ".credentials.json"))
		if err != nil {
			return result, err
		}
		var grant struct {
			OAuth struct {
				Expires int64 `json:"expiresAt"`
			} `json:"claudeAiOauth"`
		}
		if err := json.Unmarshal(raw, &grant); err != nil {
			return result, err
		}
		issued := time.Now().Add(-time.Second).UTC()
		receipt, _ := json.Marshal(map[string]any{"version": 1, "ref": cfg.Ref, "selection": mustLoginSnapshot(t, store).Providers[logins.ProviderClaude], "deadline": issued.Add(45 * time.Second), "attempt": 1, "phase": "ready", "started_at": issued, "generation": fmt.Sprintf("%x", sha256.Sum256(raw)), "worker": map[string]int{"pid": 999999998, "start_time": 1}, "native": map[string]int{"pid": 999999999, "start_time": 1}, "stop_proof": "native-tree", "expires_at": time.UnixMilli(grant.OAuth.Expires).UTC()})
		if err := os.WriteFile(filepath.Join(store.StateRoot(), "logins", "claude-auth", selected.ID+".json"), receipt, 0600); err != nil {
			return result, err
		}
		return store.ReadClaudeAuth(cfg.Ref)
	}
	path := filepath.Join(store.StateRoot(), "logins", "saved", selected.ID, ".credentials.json")
	for range 24 {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
		api.runLoginJobs(context.Background())
	}
	if starts[selected.ID] < 2 || starts[b.ID] != 1 || starts[c.ID] != 1 {
		t.Fatalf("noisy selected authentication monopolized parked opportunities: %v", starts)
	}
}

func TestClaudeMaintenanceCallerCapacityKeepsReadinessAndExistingRenewal(t *testing.T) {
	api, selected, starts := claudeMaintenanceCaller(t)
	store := api.logins.store
	for i := range logins.MaxHandoverGrantRefs - 1 {
		login := addClaudeMaintenanceLogin(t, api, fmt.Sprintf("capacity-%d", i))
		ref, _, err := store.BeginClaudeAuth(mustLoginSnapshot(t, store).Revision, login.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.PrepareClaudeAuthRecovery(ref); err != nil {
			t.Fatal(err)
		}
		if _, err := store.SettleClaudeAuth(ref); err != nil {
			t.Fatal(err)
		}
	}
	extra := addClaudeMaintenanceLogin(t, api, "additional-saved")
	reply, err := api.Logins(protocol.LoginsReq{Action: "list"})
	if err != nil {
		t.Fatal(err)
	}
	warning := "Authentication maintenance is at capacity; some saved logins are waiting to be prepared or renewed."
	found := false
	for _, view := range reply.Providers {
		if view.Provider == logins.ProviderClaude {
			found = view.State == "ready" && strings.Contains(view.Message, warning) && strings.Contains(view.Message, "Selected login is ready")
		}
	}
	if !found {
		t.Fatalf("capacity was hidden or invalidated selected readiness: %+v", reply.Providers)
	}
	previous, err := store.Runtime(logins.ProviderClaude)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SelectClaudeProfile(mustLoginSnapshot(t, store).Revision, selected.ID); err != nil {
		t.Fatal(err)
	}
	current, err := store.Runtime(logins.ProviderClaude)
	if err != nil {
		t.Fatal(err)
	}
	for _, earlier := range []bool{false, true} {
		rt := current
		wantState, reason := "waiting", "waiting for native startup"
		if earlier {
			rt = previous
			wantState, reason = "ready", "existing discussions retain their earlier login"
		}
		api.logins.d.authw = &authWatcher{list: func() []persist.Meta {
			return []persist.Meta{{ID: "projection-only", AgentType: logins.ProviderClaude, LoginRuntime: &rt, Status: status.Status{Process: status.ProcessRunning}}}
		}}
		reply, err := api.Logins(protocol.LoginsReq{Action: "list"})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, view := range reply.Providers {
			if view.Provider == logins.ProviderClaude {
				found = view.State == wantState && strings.Contains(view.Message, reason) && strings.Contains(view.Message, warning)
			}
		}
		if !found {
			t.Fatalf("capacity hid the existing readiness reason: %+v", reply.Providers)
		}
	}
	api.logins.d.authw = nil
	path := filepath.Join(store.StateRoot(), "logins", "saved", selected.ID, ".credentials.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	api.logins.claudeAuthCursor = 1
	api.runLoginJobs(context.Background())
	jobs, err := store.ListClaudeAuth()
	if err != nil || len(jobs) != logins.MaxHandoverGrantRefs || starts[selected.ID] != 1 || starts[extra.ID] != 0 {
		t.Fatalf("capacity evicted budgets or blocked existing renewal: jobs=%d starts=%v err=%v", len(jobs), starts, err)
	}
}

func TestClaudeMaintenanceCallerSelectedNonrenewableCapacityIsVisible(t *testing.T) {
	api, _, starts := claudeMaintenanceCaller(t)
	store := api.logins.store
	for i := range logins.MaxHandoverGrantRefs - 1 {
		login := addClaudeMaintenanceLogin(t, api, fmt.Sprintf("retained-%d", i))
		ref, _, err := store.BeginClaudeAuth(mustLoginSnapshot(t, store).Revision, login.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.PrepareClaudeAuthRecovery(ref); err != nil {
			t.Fatal(err)
		}
		if _, err := store.SettleClaudeAuth(ref); err != nil {
			t.Fatal(err)
		}
	}
	grant, _ := json.Marshal(map[string]any{"claudeAiOauth": map[string]any{"accessToken": "synthetic-selected-access", "expiresAt": time.Now().Add(time.Hour).UnixMilli(), "scopes": []string{"user:inference", "user:profile"}}})
	credential := logins.Credential{Auth: grant, Identity: json.RawMessage(`{"oauthAccount":{"accountUuid":"selected-no-refresh","organizationUuid":"synthetic-org"}}`)}
	r, selected, err := store.Add(mustLoginSnapshot(t, store).Revision, logins.ProviderClaude, "selected-no-refresh", credential)
	if err != nil || selected.Renewable {
		t.Fatalf("selected preparation fixture: %+v %v", selected, err)
	}
	if _, err := store.SelectClaudeProfile(r.Revision, selected.ID); err != nil {
		t.Fatal(err)
	}
	api.runLoginJobs(context.Background())
	reply, err := api.Logins(protocol.LoginsReq{Action: "list"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, view := range reply.Providers {
		if view.Provider == logins.ProviderClaude {
			found = view.ActiveID == selected.ID && view.State == "waiting" && strings.Contains(view.Message, "waiting for fresh native authentication") && strings.Contains(view.Message, "Authentication maintenance is at capacity;")
		}
	}
	jobs, err := store.ListClaudeAuth()
	if !found || err != nil || len(jobs) != logins.MaxHandoverGrantRefs || starts[selected.ID] != 0 {
		t.Fatalf("selected authentication preparation was silently blocked or evicted custody: views=%+v jobs=%d starts=%v err=%v", reply.Providers, len(jobs), starts, err)
	}
}
