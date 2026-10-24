package skeleton

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

func usageCodexGrant(account string) logins.Credential {
	payload, _ := json.Marshal(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "https://api.openai.com/auth": map[string]string{"chatgpt_user_id": "user-" + account, "chatgpt_account_id": account}})
	token := "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
	auth, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]string{"account_id": account, "access_token": token, "id_token": token, "refresh_token": "synthetic"}})
	return logins.Credential{Auth: auth}
}

func enableCodexNativeGrantUsage(t *testing.T, f *loginFixture) {
	t.Helper()
	r, err := f.m.store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	r, ownedLogin, err := f.m.store.Add(r.Revision, logins.ProviderCodex, "owned", usageCodexGrant("owned"))
	if err != nil {
		t.Fatal(err)
	}
	op, err := f.m.store.BeginSwitch(r.Revision, logins.ProviderCodex, ownedLogin.ID, "fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	op.Phase = "stopping"
	if err = f.m.store.SaveSwitch(op); err != nil {
		t.Fatal(err)
	}
	op.Phase = "stopped"
	if err = f.m.store.SaveSwitch(op); err != nil {
		t.Fatal(err)
	}
	if err = f.m.store.CaptureOutgoing(op.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.m.store.InstallIncoming(op.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.m.store.CommitSwitch(op.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.m.store.CompleteSwitch(op.ID); err != nil {
		t.Fatal(err)
	}
	r, err = f.m.store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, login := range r.Logins {
		if login.ID != ownedLogin.ID {
			if _, err = f.m.store.Remove(r.Revision, login.ID); err != nil {
				t.Fatal(err)
			}
			r, err = f.m.store.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	f.first, f.second = ownedLogin, logins.Login{}
	op, err = f.m.store.BeginCodexOwnershipMigration(r.Revision, nil)
	if err != nil {
		t.Fatal(err)
	}
	op.Phase = "stopping"
	if err = f.m.store.SaveSwitch(op); err != nil {
		t.Fatal(err)
	}
	op.Phase = "stopped"
	if err = f.m.store.SaveSwitch(op); err != nil {
		t.Fatal(err)
	}
	if err = f.m.store.CaptureOutgoing(op.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.m.store.MigrateCodexOwnership(op.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.m.store.CommitSwitch(op.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.m.store.CompleteSwitch(op.ID); err != nil {
		t.Fatal(err)
	}
	f.runtime, err = f.m.store.Runtime(logins.ProviderCodex)
	if err != nil {
		t.Fatal(err)
	}
	r, err = f.m.store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, login := range r.Logins {
		if login.ID == f.first.ID {
			f.first = login
		}
		if login.ID == f.second.ID {
			f.second = login
		}
	}
	for id, row := range f.rows {
		runtime := f.runtime
		row.LoginRuntime = &runtime
		f.rows[id] = row
	}
}

func usageView(t *testing.T, f *loginFixture) protocol.LoginView {
	t.Helper()
	reply, err := f.m.d.api.loginList()
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range reply.Logins {
		if l.Active {
			return l
		}
	}
	t.Fatal("missing active login")
	return protocol.LoginView{}
}

func usageFrame(used float64, minutes int, reset time.Time, primary bool) []byte {
	slot := "secondary"
	if primary {
		slot = "primary"
	}
	return codexQuotaFrame(map[string]any{"limitId": "codex", slot: map[string]any{"usedPercent": used, "windowDurationMins": minutes, "resetsAt": reset.Unix()}})
}

func TestLoginUsageWindowsValidateWithoutInventingCapacity(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name       string
		used       float64
		age, reset time.Duration
		want       int
	}{
		{"unused", 0, 0, time.Hour, 100}, {"empty", 100, 0, time.Hour, 0}, {"fraction", 37.4, 0, time.Hour, 62},
		{"negative", -1, 0, time.Hour, -1}, {"overfull", 101, 0, time.Hour, -1}, {"NaN", math.NaN(), 0, time.Hour, -1},
		{"infinite", math.Inf(1), 0, time.Hour, -1}, {"old", 10, 15 * time.Minute, time.Hour, -1},
		{"future", 10, -time.Second, time.Hour, -1}, {"reset", 10, 0, -time.Second, -1}, {"implausible reset", 10, 0, 9 * 24 * time.Hour, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := nativeUsageWindow(tc.used, now.Add(tc.reset).Unix(), now.Add(-tc.age), now)
			if tc.want < 0 {
				if got != nil {
					t.Fatal("invalid reading displayed")
				}
				return
			}
			if got == nil || got.RemainingPercent != tc.want {
				t.Fatalf("remaining=%+v, want%d", got, tc.want)
			}
		})
	}
}

func TestCodexUsageWeeklyEitherSlotAndNeverChangesRotation(t *testing.T) {
	for _, primary := range []bool{true, false} {
		f := newLoginFixture(t)
		before, _ := f.m.store.Snapshot()
		now := time.Now()
		f.m.observeCodexUsage("source-1", codexRateLimitsUpdated, usageFrame(100, 10080, now.Add(time.Hour), primary), now)
		v := usageView(t, f)
		if v.Weekly == nil || v.Weekly.RemainingPercent != 0 || v.FiveHour != nil {
			t.Fatalf("wrong projection: %+v", v)
		}
		after, _ := f.m.store.Snapshot()
		if !reflect.DeepEqual(before, after) {
			t.Fatal("usage mutated the registry")
		}
		f.m.step()
		after, _ = f.m.store.Snapshot()
		if len(after.Switches) != 0 || after.Logins[0].LimitKnown {
			t.Fatal("usage alone started rotation")
		}
		v.Weekly.RemainingPercent = 99
		if usageView(t, f).Weekly.RemainingPercent != 0 {
			t.Fatal("reply aliased mutable cache")
		}
	}
}

func TestCodexUsageAcceptsNativeGrantRuntime(t *testing.T) {
	f := newLoginFixture(t)
	enableCodexNativeGrantUsage(t, f)
	if !f.runtime.ExternalAuth {
		t.Fatal("fixture did not enable native-grant auth ownership")
	}
	now := time.Now()
	f.m.observeCodexUsage("source-1", codexRateLimitsUpdated, usageFrame(23, 10080, now.Add(time.Hour), true), now)
	if got := usageView(t, f).Weekly; got == nil || got.RemainingPercent != 77 {
		t.Fatalf("native-grant usage was not recorded: %+v", got)
	}

	// Exercise the read-side runtime comparison with a fresh matching cache.
	f.m.usage[loginProviderIndex(logins.ProviderCodex)] = loginUsageSnapshot{runtime: f.runtime, loginID: f.first.ID, credentialRevision: f.first.CredentialRevision, weekly: &protocol.LoginUsageWindow{RemainingPercent: 77, ObservedAt: now, ResetsAt: now.Add(time.Hour)}}
	r, err := f.m.store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	_, weekly := f.m.loginUsage(r, f.first, now)
	if weekly == nil || weekly.RemainingPercent != 77 {
		t.Fatalf("native-grant cached usage was not read: %+v", weekly)
	}
}

func TestCodexUsageSparseAndInvalidFramesDoNotRefreshWeekly(t *testing.T) {
	f := newLoginFixture(t)
	now := time.Now()
	observed := now.Add(-14 * time.Minute)
	reset := now.Add(time.Hour)
	f.m.observeCodexUsage("source-1", codexRateLimitsUpdated, usageFrame(25, 10080, reset, true), observed)
	for _, raw := range [][]byte{
		usageFrame(10, 300, reset, false), usageFrame(10, 0, reset, true), usageFrame(101, 10080, reset, true),
		[]byte(`{"params":{"rateLimits":{"limitId":"codex_other","primary":{"usedPercent":2,"windowDurationMins":10080,"resetsAt":9999999999}}}}`),
		[]byte(`{"params":{"rateLimits":{"primary":null}}}`),
		[]byte(`{"params":{"rateLimits":{},"rateLimits":{}}}`),
	} {
		f.m.observeCodexUsage("source-1", codexRateLimitsUpdated, raw, now)
	}
	v := usageView(t, f)
	if v.Weekly == nil || v.Weekly.RemainingPercent != 75 || !v.Weekly.ObservedAt.Equal(observed) {
		t.Fatalf("sparse reading renewed old weekly: %+v", v.Weekly)
	}
	r, _ := f.m.store.Snapshot()
	_, week := f.m.loginUsage(r, f.first, now.Add(time.Minute))
	if week != nil {
		t.Fatal("stale weekly still displayed")
	}
	f.m.observeCodexUsage("source-1", codexRateLimitsUpdated, usageFrame(40, 10080, reset, false), now)
	f.m.observeCodexUsage("source-1", codexRateLimitsUpdated, usageFrame(2, 10080, reset, false), now.Add(-time.Second))
	if usageView(t, f).Weekly.RemainingPercent != 60 {
		t.Fatal("late reading replaced newer reading")
	}
}

func TestLoginUsageDoesNotSurviveSwitchABAOrRelogin(t *testing.T) {
	f := newLoginFixture(t)
	observe := func(id string, used float64) {
		now := time.Now()
		f.m.observeCodexUsage(id, codexRateLimitsUpdated, usageFrame(used, 10080, now.Add(time.Hour), true), now)
	}
	observe("source-1", 20)
	if _, err := f.m.BeginSwitch(logins.ProviderCodex, f.second.ID, "test"); err != nil {
		t.Fatal(err)
	}
	if usageView(t, f).Weekly != nil {
		t.Fatal("pending switch retained estimate")
	}
	driveLoginSwitch(t, f)
	if usageView(t, f).Weekly != nil {
		t.Fatal("B inherited A usage")
	}
	observe("child-1", 30)
	if _, err := f.m.BeginSwitch(logins.ProviderCodex, f.first.ID, "test"); err != nil {
		t.Fatal(err)
	}
	driveLoginSwitch(t, f)
	if usageView(t, f).Weekly != nil {
		t.Fatal("A3 inherited A1 usage")
	}
	observe("child-3", 40)
	observe("source-1", 1)
	observe("child-1", 1)
	v := usageView(t, f)
	if v.Weekly == nil || v.Weekly.RemainingPercent != 60 {
		t.Fatal("late prior epoch replaced active reading")
	}
	if _, err := f.m.BeginRelogin(f.first.ID, testLoginCredential("first"), "test"); err != nil {
		t.Fatal(err)
	}
	driveLoginSwitch(t, f)
	if usageView(t, f).Weekly != nil {
		t.Fatal("relogin retained former credential estimate")
	}
}

// Seed metadata only: usage never reads native grants or invokes the Keychain.
func claudeUsageFixture(t *testing.T) *loginFixture {
	t.Helper()
	f := newLoginFixture(t)
	r, err := f.m.store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	p := r.Providers[logins.ProviderCodex]
	p.Provider = logins.ProviderClaude
	delete(r.Providers, logins.ProviderCodex)
	r.Providers[logins.ProviderClaude] = p
	for i := range r.Logins {
		r.Logins[i].Provider = logins.ProviderClaude
	}
	raw, _ := json.Marshal(r)
	if err = os.WriteFile(filepath.Join(f.m.store.StateRoot(), "logins", "registry.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	f.runtime.Provider = logins.ProviderClaude
	f.runtime.Home = f.m.store.Home(logins.ProviderClaude)
	for id, row := range f.rows {
		row.AgentType = logins.ProviderClaude
		row.LoginRuntime = &f.runtime
		f.rows[id] = row
	}
	f.m.d.eng = engine.New(engine.Config{})
	f.m.d.eng.RegisterSession("source-1", "private-token", 1, nil)
	return f
}

func TestClaudeUsageAuthenticatedPrivateAndIndependentWindows(t *testing.T) {
	f := claudeUsageFixture(t)
	emitted := 0
	f.m.d.eng = engine.New(engine.Config{Emit: func(string, status.Status) { emitted++ }})
	f.m.d.eng.RegisterSession("source-1", "private-token", 1, []adapter.SignalSource{{Kind: "hook", Descriptor: map[string]string{"event": "SwarmUsage", "turn": "active"}}}, status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle})
	now := time.Now()
	cb := engine.Callback{SessionID: "source-1", Token: "wrong", Sequence: 1, Event: "SwarmUsage", Payload: map[string]string{
		"session_id": f.rows["source-1"].ConversationID, "observed_at": now.Format(time.RFC3339Nano),
		"five_hour_used_percentage": "37.4", "five_hour_resets_at": strconv.FormatInt(now.Add(time.Hour).Unix(), 10),
		"seven_day_used_percentage": "0", "seven_day_resets_at": strconv.FormatInt(now.Add(24*time.Hour).Unix(), 10),
		engine.PayloadKeyTurn: string(status.TurnActive),
	}}
	send := func() error { raw, _ := json.Marshal(cb); return f.m.d.ingestHookBytes(raw) }
	if send() == nil || usageView(t, f).Weekly != nil {
		t.Fatal("unauthenticated usage admitted")
	}
	cb.Token = "private-token"
	before, _ := f.m.store.Snapshot()
	if err := send(); err != nil {
		t.Fatal(err)
	}
	v := usageView(t, f)
	if v.FiveHour == nil || v.FiveHour.RemainingPercent != 62 || v.Weekly == nil || v.Weekly.RemainingPercent != 100 {
		t.Fatalf("wrong Claude display: %+v", v)
	}
	after, _ := f.m.store.Snapshot()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("Claude usage mutated registry")
	}
	if emitted != 0 {
		t.Fatal("usage changed discussion status")
	}
	if f.m.d.hookSeqDuplicate(cb.SessionID, cb.Sequence) {
		t.Fatal("advisory usage consumed a real hook's sequence")
	}
	if err := send(); err != nil {
		t.Fatal(err)
	}
	if !usageView(t, f).Weekly.ObservedAt.Equal(now) {
		t.Fatal("duplicate report renewed sample")
	}
	cb.Sequence++
	cb.Payload["observed_at"] = now.Add(time.Second).Format(time.RFC3339Nano)
	cb.Payload["five_hour_used_percentage"] = "99"
	if err := send(); err != nil {
		t.Fatal(err)
	}
	if usageView(t, f).FiveHour.RemainingPercent != 62 {
		t.Fatal("future timestamp accepted")
	}
	cb.Sequence++
	cb.Payload["observed_at"] = time.Now().Format(time.RFC3339Nano)
	cb.Payload["session_id"] = "foreign-conversation"
	if err := send(); err != nil {
		t.Fatal(err)
	}
	if usageView(t, f).FiveHour.RemainingPercent != 62 {
		t.Fatal("foreign conversation accepted")
	}
	cb.Sequence++
	cb.Payload["session_id"] = f.rows["source-1"].ConversationID
	cb.Payload["observed_at"] = time.Now().Format(time.RFC3339Nano)
	delete(cb.Payload, "seven_day_used_percentage")
	delete(cb.Payload, "seven_day_resets_at")
	if err := send(); err != nil {
		t.Fatal(err)
	}
	v = usageView(t, f)
	if v.FiveHour.RemainingPercent != 1 || !v.Weekly.ObservedAt.Equal(now) {
		t.Fatal("one window refreshed the other's timestamp")
	}
}
