package skeleton

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/loginworker"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
)

func TestClaudeOwnerSelectionPersistsPendingAndSupersedesWithoutFleetSwitch(t *testing.T) {
	f := claudeUsageFixture(t)
	qualifiedClaudeCallerFixture(t, f.m)
	r, err := f.m.store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	reply, err := f.m.d.api.Logins(protocol.LoginsReq{Action: "activate", LoginID: f.second.ID, ExpectedRevision: r.Revision})
	if err != nil {
		t.Fatal(err)
	}
	r, err = f.m.store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if r.Providers[logins.ProviderClaude].ActiveID != f.second.ID || r.Providers[logins.ProviderClaude].AuthOwnerMode != logins.ClaudeNativeProfileMode || len(r.Switches) != 0 {
		t.Fatal("owner action did not persist desired-only selection")
	}
	if len(f.launched) != 0 {
		t.Fatal("selection restarted discussions")
	}
	if len(reply.Providers) == 0 {
		t.Fatal("missing selection projection")
	}
	firstEpoch := r.Providers[logins.ProviderClaude].Epoch
	if _, err = f.m.d.api.Logins(protocol.LoginsReq{Action: "activate", LoginID: f.first.ID, ExpectedRevision: r.Revision}); err != nil {
		t.Fatal(err)
	}
	r, _ = f.m.store.Snapshot()
	if r.Providers[logins.ProviderClaude].ActiveID != f.first.ID || r.Providers[logins.ProviderClaude].Epoch != firstEpoch+1 {
		t.Fatal("later desired selection did not supersede")
	}
	f.m.step()
	after, _ := f.m.store.Snapshot()
	if after.Providers[logins.ProviderClaude] != r.Providers[logins.ProviderClaude] {
		t.Fatal("tick minted desired retry")
	}
}

func TestClaudeOwnedUsageUsesExactProfileHomeInBothReaders(t *testing.T) {
	f := claudeUsageFixture(t)
	r, _ := f.m.store.Snapshot()
	p := r.Providers[logins.ProviderClaude]
	p.AuthOwnerMode = logins.ClaudeNativeProfileMode
	r.Providers[logins.ProviderClaude] = p
	r.SchemaVersion = logins.ClaudeProfileCustodySchemaVersion
	raw, _ := json.Marshal(r)
	if err := os.WriteFile(filepath.Join(f.m.store.StateRoot(), "logins", "registry.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	home, err := f.m.store.ClaudeProfileHome(f.first.Identity)
	if err != nil {
		t.Fatal(err)
	}
	rt := f.runtime
	rt.Home = home
	row := f.rows["source-1"]
	row.LoginRuntime = &rt
	now := time.Now()
	w := &protocol.LoginUsageWindow{RemainingPercent: 45, ObservedAt: now, ResetsAt: now.Add(time.Hour)}
	f.m.recordLoginUsage(row, w, w)
	five, week := f.m.loginUsage(r, r.Logins[0], now)
	if five == nil || week == nil {
		t.Fatal("owned profile usage rejected")
	}
	old := f.runtime
	row.LoginRuntime = &old
	f.m.recordLoginUsage(row, &protocol.LoginUsageWindow{RemainingPercent: 1, ObservedAt: now.Add(time.Second), ResetsAt: now.Add(time.Hour)}, nil)
	five, _ = f.m.loginUsage(r, r.Logins[0], now)
	if five == nil || five.RemainingPercent != 45 {
		t.Fatal("old common-home observation relabeled owned profile")
	}
}

func TestClaudeSemanticGateRefusesStaleWhileLegacyRawGateRemainsUsable(t *testing.T) {
	f := claudeUsageFixture(t)
	r, _ := f.m.store.Snapshot()
	if _, err := f.m.store.SelectClaudeProfile(r.Revision, f.second.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.m.semanticInputGate("source-1"); !errors.Is(err, logins.ErrPending) {
		t.Fatalf("semantic old-account gate: %v", err)
	}
	if err := f.m.inputGate("source-1"); err != nil {
		t.Fatalf("opaque owner control inherited semantic fence: %v", err)
	}
}

func TestClaudeCompletionRecoversPreparedOutsideLoginAndAdmissionLocks(t *testing.T) {
	f := claudeUsageFixture(t)
	r, _ := f.m.store.Snapshot()
	r, err := f.m.store.SelectClaudeProfile(r.Revision, f.first.ID)
	if err != nil {
		t.Fatal(err)
	}
	ref := logins.ClaudeAuthRef{LoginID: f.first.ID, Identity: f.first.Identity, CredentialRevision: r.Logins[0].CredentialRevision, Nonce: strings.Repeat("e", 32)}
	dir := filepath.Join(f.m.store.StateRoot(), "logins", "claude-auth")
	if err = os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"version": 1, "ref": ref, "selection": r.Providers[logins.ProviderClaude], "deadline": time.Now().UTC().Add(45 * time.Second), "attempt": 1, "phase": "prepared", "worker": map[string]int{"pid": 0, "start_time": 0}, "native": map[string]int{"pid": 0, "start_time": 0}})
	if err = os.WriteFile(filepath.Join(dir, ref.LoginID+".json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	f.m.claudeAuthCLI = func(context.Context) (*persist.CLIIdentity, error) { return nil, loginworker.ErrUnsupported }
	entered := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.m.claudeAuthRecover = func(ctx context.Context, c loginworker.ClaudeAuthConfig) (loginworker.ClaudeAuthRecovery, error) {
		if !f.m.stepMu.TryLock() {
			t.Error("recovery held step mutex")
		} else {
			f.m.stepMu.Unlock()
		}
		if !f.m.admission[0].TryLock() {
			t.Error("recovery held admission mutex")
		} else {
			f.m.admission[0].Unlock()
		}
		if !f.m.d.api.loginMu.TryLock() {
			t.Error("recovery held login mutex")
		} else {
			f.m.d.api.loginMu.Unlock()
		}
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 3*time.Second {
			t.Error("unbounded maintenance context")
		}
		close(entered)
		<-ctx.Done()
		return loginworker.ClaudeAuthRecovery{}, ctx.Err()
	}
	done := make(chan struct{})
	go func() { f.m.d.api.runLoginJobs(ctx); close(done) }()
	select {
	case <-entered:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("existing completion worker did not recover retained PREPARED")
	}
	listed := make(chan error, 1)
	go func() { _, err := f.m.d.api.Logins(protocol.LoginsReq{Action: "list"}); listed <- err }()
	select {
	case err := <-listed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("stalled recovery blocked login list")
	}
	enrollment := loginworker.Config{SchemaVersion: 1, StateRoot: f.m.store.StateRoot(), JobID: strings.Repeat("c", 32), Generation: 1, Provider: logins.ProviderCodex, Label: "enrolled while helper waits", Deadline: time.Now().Add(time.Minute)}
	enrollmentHome := loginworker.NativeHome(enrollment.StateRoot, enrollment.JobID)
	if err := os.MkdirAll(enrollmentHome, 0700); err != nil {
		t.Fatal(err)
	}
	writeEnrollment := func(name string, value any) {
		raw, _ := json.Marshal(value)
		if err := os.WriteFile(filepath.Join(filepath.Dir(enrollmentHome), name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeEnrollment(loginworker.ConfigFile, enrollment)
	writeEnrollment(loginworker.ProgressFile, loginworker.Progress{SchemaVersion: 1, JobID: enrollment.JobID, Generation: 1, Phase: loginworker.PhaseReady, WritersStopped: true, NativeWritersStopped: true})
	if err := os.WriteFile(filepath.Join(enrollmentHome, "auth.json"), testLoginCredential("enrolled-during-helper").Auth, 0600); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("maintenance ignored cancellation")
	}
	f.m.claudeAuthRecover = nil
	f.m.d.api.runLoginJobs(context.Background())
	if completed, err := loginworker.Completed(enrollment); err != nil || !completed {
		if runtime.GOOS == "linux" || err != nil || !errors.Is(loginworker.Stopped(enrollment), loginworker.ErrUnavailable) {
			t.Fatalf("ordinary enrollment did not resume after bounded helper return: completed=%v receipt=%v", completed, err)
		}
		t.Log("Darwin process-environment custody is unavailable; enrollment completion is not qualified by this run")
	}
	held, err := f.m.store.ReadClaudeAuth(ref)
	if err != nil || held.Phase != "failed" {
		t.Fatalf("reopened PREPARED was not revoked and retained: %+v %v", held, err)
	}
	f.m.d.api.runLoginJobs(context.Background())
	held2, err := f.m.store.ReadClaudeAuth(ref)
	if err != nil || held2.Ref != held.Ref || held2.Attempt != held.Attempt {
		t.Fatal("worker reset retained budget")
	}
}

func TestClaudeWorkerCapacityCleanupLetsDesiredCreatorProgressOnce(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("synthetic saved file authority is Linux-only; never use Darwin Keychain")
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	cwd := t.TempDir()
	store, a, _ := claudeOwnerBirthFixture(t, root, cwd)
	held, err := store.ListClaudeAuth()
	if err != nil {
		t.Fatal(err)
	}
	if err = store.RetireClaudeAuth(held[0].Ref); err != nil {
		t.Fatal(err)
	}
	add := func(id string) logins.Login {
		r, _ := store.Snapshot()
		expiry := time.Now().Add(time.Hour).UnixMilli()
		grant, _ := json.Marshal(map[string]any{"claudeAiOauth": map[string]any{"accessToken": "synthetic-" + id, "refreshToken": "refresh-" + id, "expiresAt": expiry, "scopes": []string{"user:profile", "user:inference"}}})
		identity, _ := json.Marshal(map[string]any{"oauthAccount": map[string]string{"accountUuid": id, "organizationUuid": "synthetic-org"}, "userID": id})
		_, l, err := store.Add(r.Revision, logins.ProviderClaude, id, logins.Credential{Auth: grant, Identity: identity})
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	b, c := add("warm-b"), add("desired-c")
	r, _ := store.Snapshot()
	first, created, err := store.BeginClaudeAuth(r.Revision, a.ID)
	if err != nil || !created {
		t.Fatal(err)
	}
	r, _ = store.Snapshot()
	if _, created, err = store.BeginClaudeAuth(r.Revision, b.ID); err != nil || !created {
		t.Fatal(err)
	}
	r, _ = store.Snapshot()
	if _, err = store.SelectClaudeProfile(r.Revision, c.ID); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{stateDir: root}
	m := &loginManager{d: d, store: store}
	d.logins = m
	api := &coreAPI{logins: m}
	d.api = api
	m.claudeAuthCLI = func(context.Context) (*persist.CLIIdentity, error) {
		return &persist.CLIIdentity{Path: "/synthetic/claude", Version: "2.1.310", Fingerprint: "synthetic"}, nil
	}
	starts := 0
	m.claudeAuthStart = func(_ context.Context, _ string, cfg loginworker.ClaudeAuthConfig) (logins.ClaudeAuthResult, error) {
		starts++
		if cfg.Ref.LoginID != c.ID {
			t.Error("started warm instead of actual desired")
		}
		return store.ReadClaudeAuth(cfg.Ref)
	}
	api.runLoginJobs(context.Background()) // Capacity refusal must still recover one old slot.
	api.runLoginJobs(context.Background()) // The next pass creates C and starts exactly once.
	if starts != 1 {
		t.Fatalf("desired creator starts=%d", starts)
	}
	for range 4 {
		api.runLoginJobs(context.Background())
	}
	if starts != 1 {
		t.Fatal("enumeration/recovery recreated Start authority")
	}
	firstHeld, err := store.ReadClaudeAuth(first)
	if err != nil || firstHeld.Phase != "failed" {
		t.Fatal("cleanup lost old consumed budget")
	}
}

func TestClaudeLaunchPreparationReachesEstablishedMissingCWDAndResumeCheckout(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("synthetic saved file authority is Linux-only; never use Darwin Keychain")
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	original := t.TempDir()
	next := t.TempDir()
	store, login, _ := claudeOwnerBirthFixture(t, root, original)
	source := persist.Meta{ID: "source", AgentType: logins.ProviderClaude, Cwd: original, AgentCwd: next, ConversationID: "123e4567-e89b-42d3-a456-426614174000"}
	d := &Daemon{stateDir: root}
	m := &loginManager{d: d, store: store}
	d.authw = &authWatcher{get: func(id string) (persist.Meta, bool) { return source, id == source.ID }}
	d.logins = m
	api := &coreAPI{endpointID: "machine", logins: m}
	d.api = api
	m.admission[0].Lock()
	prepared := make(chan error, 1)
	go func() {
		prepared <- api.prepareClaudeProfile(daemon.LaunchSpec{AgentType: logins.ProviderClaude, Cwd: original, Options: map[string]string{protocol.OptionResumeFrom: "machine/source"}})
	}()
	select {
	case err := <-prepared:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("profile preparation waited for admission mutex")
	}
	m.admission[0].Unlock()
	r, _ := store.Snapshot()
	cwds := r.ClaudeProfiles[login.Identity].CWDs
	if !slices.Contains(cwds, next) || !slices.Contains(cwds, original) {
		t.Fatal("resume did not prepare exact established checkout")
	}
	spec := daemon.LaunchSpec{AgentType: logins.ProviderClaude, Cwd: next}
	if err := api.bindLoginLaunch(&spec); err != nil || spec.LoginRuntime == nil {
		t.Fatalf("prepared launch runtime: %v", err)
	}
}

func TestClaudeSelectedReloginCompletionKeepsPendingCopyAndAvoidsFleetRestart(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("synthetic saved file authority is Linux-only; never use Darwin Keychain")
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	cwd := t.TempDir()
	store, login, _ := claudeOwnerBirthFixture(t, root, cwd)
	d := &Daemon{stateDir: root}
	m := &loginManager{d: d, store: store}
	d.logins = m
	api := &coreAPI{logins: m}
	d.api = api
	cfg := loginworker.Config{SchemaVersion: 1, StateRoot: root, JobID: strings.Repeat("d", 32), Generation: 1, Provider: logins.ProviderClaude, LoginID: login.ID, Label: "signed in again", Deadline: time.Now().Add(time.Minute)}
	home := loginworker.NativeHome(root, cfg.JobID)
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(path string, value any) {
		raw, _ := json.Marshal(value)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(filepath.Dir(home), loginworker.ConfigFile), cfg)
	write(filepath.Join(filepath.Dir(home), loginworker.ProgressFile), loginworker.Progress{SchemaVersion: 1, JobID: cfg.JobID, Generation: 1, Phase: loginworker.PhaseReady, WritersStopped: true, NativeWritersStopped: true})
	grant, _ := os.ReadFile(filepath.Join(root, "logins", "saved", login.ID, ".credentials.json"))
	if err := os.WriteFile(filepath.Join(home, ".credentials.json"), grant, 0600); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(home, ".claude.json"), map[string]any{"oauthAccount": map[string]string{"accountUuid": "synthetic-account", "organizationUuid": "synthetic-org"}, "userID": "synthetic-account"})
	unknown := filepath.Join(root, "unknown-legacy")
	if err := os.Mkdir(unknown, 0700); err != nil {
		t.Fatal(err)
	}
	before, _ := store.Snapshot()
	if _, err := api.Logins(protocol.LoginsReq{Action: "complete", JobID: cfg.JobID}); !errors.Is(err, logins.ErrPending) {
		t.Fatalf("unknown custody did not hold relogin: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".credentials.json")); err != nil {
		t.Fatal("pending enrollment copy destroyed")
	}
	if done, _ := loginworker.Completed(cfg); done {
		t.Fatal("pending relogin marked complete")
	}
	if len(mustLoginSnapshot(t, store).Switches) != 0 {
		t.Fatal("owned relogin created fleet restart")
	}
	if err := os.Remove(unknown); err != nil {
		t.Fatal(err)
	}
	if _, err := api.Logins(protocol.LoginsReq{Action: "complete", JobID: cfg.JobID}); err != nil {
		t.Fatal(err)
	}
	after := mustLoginSnapshot(t, store)
	if after.Providers[logins.ProviderClaude].Epoch != before.Providers[logins.ProviderClaude].Epoch+1 || after.Logins[0].CredentialRevision != before.Logins[0].CredentialRevision+1 {
		t.Fatal("relogin did not invalidate old birth/receipt generation")
	}
	if done, _ := loginworker.Completed(cfg); !done {
		t.Fatal("settled relogin never completed")
	}
	if len(after.Switches) != 0 {
		t.Fatal("relogin created legacy journal")
	}
}

func mustLoginSnapshot(t *testing.T, store *logins.Store) logins.Registry {
	t.Helper()
	r, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestClaudeTypedComposerRefusesStaleButOwnerProtocolApprovalKeysPass(t *testing.T) {
	rig := newR7ComposerRig(t, true)
	f := claudeUsageFixture(t)
	r, _ := f.m.store.Snapshot()
	if _, err := f.m.store.SelectClaudeProfile(r.Revision, f.second.ID); err != nil {
		t.Fatal(err)
	}
	row := f.rows["source-1"]
	row.ID = rig.local
	f.rows[rig.local] = row
	var metaMu sync.RWMutex
	f.m.d.authw.get = func(id string) (persist.Meta, bool) {
		metaMu.RLock()
		defer metaMu.RUnlock()
		meta, ok := f.rows[id]
		return meta, ok
	}
	f.m.claudeAuthCLI = func(context.Context) (*persist.CLIIdentity, error) { return nil, loginworker.ErrUnsupported }
	rig.sk.logins = f.m
	rig.sk.api.logins = f.m
	rig.sk.srv.SetNewTurnGateFunc(f.m.semanticInputGate)
	rig.sk.srv.SetManualInputGateFunc(f.m.manualInputGate)
	if _, err := rig.send(t, "", "new old-account work", "devA:01JCLAUDESTALE00000000000"); err == nil || !strings.Contains(err.Error(), "earlier Claude login") {
		t.Fatalf("actual ComposerSend stale refusal: %v", err)
	}
	rig.backend.mu.Lock()
	calls := len(rig.backend.calls)
	rig.backend.mu.Unlock()
	if calls != 0 {
		t.Fatal("typed refusal dispatched backend operation")
	}
	if err := rig.att.Detach(); err != nil {
		t.Fatal(err)
	}
	owner := dialClient(t, rig.sk)
	for _, health := range []string{logins.AuthReady, "exhausted", logins.AuthNeedsLogin} {
		r := mustLoginSnapshot(t, f.m.store)
		auth := health
		if health == "exhausted" {
			auth = logins.AuthReady
		}
		if _, err := f.m.store.UpdateAvailability(r.Revision, f.second.ID, auth, health == "exhausted", time.Time{}); err != nil {
			t.Fatal(err)
		}
		for _, req := range []protocol.SendInputReq{{Text: "y"}, {Key: "enter"}} {
			if err := rig.sk.srv.SendInput(rig.local, req); err == nil {
				t.Fatalf("daemon %s send inherited manual authority", health)
			}
			if err := owner.SendInput(rig.session, req); err != nil {
				t.Fatalf("owner protocol %s approval key blocked: %v", health, err)
			}
		}
	}
	metaMu.Lock()
	row = f.rows[rig.local]
	row.ResumeError = "unresolved lifecycle custody"
	f.rows[rig.local] = row
	metaMu.Unlock()
	if err := owner.SendInput(rig.session, protocol.SendInputReq{Text: "y"}); err == nil {
		t.Fatal("manual owner gate bypassed lifecycle custody")
	}
	metaMu.Lock()
	row.ResumeError = ""
	f.rows[rig.local] = row
	metaMu.Unlock()
	pending := mustLoginSnapshot(t, f.m.store)
	pending.MigrationPending = true
	raw, _ := json.Marshal(pending)
	registryPath := filepath.Join(f.m.store.StateRoot(), "logins", "registry.json")
	if err := os.WriteFile(registryPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := owner.SendInput(rig.session, protocol.SendInputReq{Text: "y"}); err == nil {
		t.Fatal("manual owner gate bypassed migration")
	}
	pending.MigrationPending = false
	p := pending.Providers[logins.ProviderClaude]
	pending.Switches[logins.ProviderClaude] = logins.Switch{ID: strings.Repeat("b", 32), Provider: logins.ProviderClaude, FromID: p.ActiveID, ToID: f.first.ID, FromEpoch: p.Epoch, ToEpoch: p.Epoch + 1, Phase: "waiting", AttemptedIDs: []string{f.first.ID}}
	raw, _ = json.Marshal(pending)
	if err := os.WriteFile(registryPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := owner.SendInput(rig.session, protocol.SendInputReq{Text: "y"}); err == nil {
		t.Fatal("manual owner gate bypassed legacy journal")
	}
	delete(pending.Switches, logins.ProviderClaude)
	raw, _ = json.Marshal(pending)
	if err := os.WriteFile(registryPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, req := range []protocol.SendInputReq{{Key: "esc"}, {Key: "ctrl-c"}} {
		if err := owner.SendInput(rig.session, req); err != nil {
			t.Fatalf("owner protocol cancel blocked: %v", err)
		}
	}
}

func TestClaudeExistingJournalTakesPrecedenceOverDesiredSelection(t *testing.T) {
	f := claudeUsageFixture(t)
	r, _ := f.m.store.Snapshot()
	p := r.Providers[logins.ProviderClaude]
	op := logins.Switch{ID: strings.Repeat("b", 32), Provider: logins.ProviderClaude, FromID: f.first.ID, ToID: f.second.ID, FromEpoch: p.Epoch, ToEpoch: p.Epoch + 1, Phase: "waiting", AttemptedIDs: []string{f.second.ID}}
	r.Switches[logins.ProviderClaude] = op
	raw, _ := json.Marshal(r)
	if err := os.WriteFile(filepath.Join(f.m.store.StateRoot(), "logins", "registry.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.d.api.Logins(protocol.LoginsReq{Action: "activate", LoginID: f.second.ID, ExpectedRevision: r.Revision}); err != nil {
		t.Fatal(err)
	}
	after := mustLoginSnapshot(t, f.m.store)
	if after.Switches[logins.ProviderClaude].ID != op.ID || after.Providers[logins.ProviderClaude] != p {
		t.Fatal("existing journal was relabeled desired profile selection")
	}
}

func TestClaudeUnsupportedOwnerSelectionDoesNotCommitMode(t *testing.T) {
	f := claudeUsageFixture(t)
	before := mustLoginSnapshot(t, f.m.store)
	f.m.claudeAuthCLI = func(context.Context) (*persist.CLIIdentity, error) { return nil, loginworker.ErrUnsupported }
	if _, err := f.m.d.api.Logins(protocol.LoginsReq{Action: "activate", LoginID: f.second.ID, ExpectedRevision: before.Revision}); !errors.Is(err, loginworker.ErrUnsupported) {
		t.Fatalf("unsupported owner selection: %v", err)
	}
	after := mustLoginSnapshot(t, f.m.store)
	if after.Revision != before.Revision || after.Providers[logins.ProviderClaude] != before.Providers[logins.ProviderClaude] {
		t.Fatal("unsupported installation committed irreversible owner mode")
	}
	if _, err := f.m.d.api.Logins(protocol.LoginsReq{Action: "list"}); err != nil {
		t.Fatal("unsupported advanced path blocked independent login management")
	}
}

func qualifiedClaudeCallerFixture(t *testing.T, m *loginManager) {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := persist.CLIFingerprint(path)
	if err != nil {
		t.Fatal(err)
	}
	m.claudeAuthCLI = func(context.Context) (*persist.CLIIdentity, error) {
		return &persist.CLIIdentity{Path: path, Version: "2.1.310", Fingerprint: fingerprint}, nil
	}
}

func TestClaudeActualUnsupportedPlatformRefusesOwnerSelection(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("default unsupported-platform caller is exercised on non-Linux")
	}
	f := claudeUsageFixture(t)
	before := mustLoginSnapshot(t, f.m.store)
	if _, err := f.m.d.api.Logins(protocol.LoginsReq{Action: "activate", LoginID: f.second.ID, ExpectedRevision: before.Revision}); !errors.Is(err, loginworker.ErrUnsupported) {
		t.Fatalf("actual unsupported platform: %v", err)
	}
	after := mustLoginSnapshot(t, f.m.store)
	if after.Revision != before.Revision || after.Providers[logins.ProviderClaude] != before.Providers[logins.ProviderClaude] {
		t.Fatal("unsupported platform committed profile ownership")
	}
}

func TestClaudeFirstOwnerCapabilityProbeLeavesLoginListResponsive(t *testing.T) {
	f := claudeUsageFixture(t)
	qualifiedClaudeCallerFixture(t, f.m)
	probe := f.m.claudeAuthCLI
	entered, release := make(chan struct{}), make(chan struct{})
	calls := 0
	f.m.claudeAuthCLI = func(ctx context.Context) (*persist.CLIIdentity, error) {
		calls++
		close(entered)
		if !f.m.d.api.loginMu.TryLock() {
			t.Error("first owner capability probe held login mutex")
		} else {
			f.m.d.api.loginMu.Unlock()
		}
		<-release
		return probe(ctx)
	}
	r := mustLoginSnapshot(t, f.m.store)
	activated := make(chan error, 1)
	go func() {
		_, err := f.m.d.api.Logins(protocol.LoginsReq{Action: "activate", LoginID: f.second.ID, ExpectedRevision: r.Revision})
		activated <- err
	}()
	<-entered
	listed := make(chan error, 1)
	go func() { _, err := f.m.d.api.Logins(protocol.LoginsReq{Action: "list"}); listed <- err }()
	select {
	case err := <-listed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("capability probing blocked login list")
	}
	close(release)
	if err := <-activated; err != nil {
		t.Fatal(err)
	}
	if calls != 1 || mustLoginSnapshot(t, f.m.store).Providers[logins.ProviderClaude].ActiveID != f.second.ID {
		t.Fatal("ordinary first selection required another click")
	}
}

func TestClaudeConfirmedExhaustionUsesDesiredSelectionWithoutRetryEpochChurn(t *testing.T) {
	f := claudeUsageFixture(t)
	qualifiedClaudeCallerFixture(t, f.m)
	if err := f.m.prepareClaudeSelection(context.Background()); err != nil {
		t.Fatal(err)
	}
	r := mustLoginSnapshot(t, f.m.store)
	if _, err := f.m.store.UpdateExhaustion(r.Revision, f.first.ID, time.Now().Add(time.Hour), false); err != nil {
		t.Fatal(err)
	}
	f.m.step()
	selected := mustLoginSnapshot(t, f.m.store)
	if selected.Providers[logins.ProviderClaude].ActiveID != f.second.ID || len(selected.Switches) != 0 {
		t.Fatal("automatic exhaustion still used fleet restart")
	}
	for range 3 {
		f.m.step()
	}
	if mustLoginSnapshot(t, f.m.store).Providers[logins.ProviderClaude] != selected.Providers[logins.ProviderClaude] {
		t.Fatal("pending authentication minted automatic retry epochs")
	}
	if err := f.m.recordAuthFailure(f.rows["source-1"]); err != nil {
		t.Fatal(err)
	}
	after := mustLoginSnapshot(t, f.m.store)
	if after.Logins[1].AuthState != logins.AuthReady {
		t.Fatal("old-account failure relabeled desired login")
	}
}

func TestClaudeRepeatedSupportTimeoutCannotStarveRetainedRecovery(t *testing.T) {
	f := claudeUsageFixture(t)
	r, _ := f.m.store.Snapshot()
	r, err := f.m.store.SelectClaudeProfile(r.Revision, f.first.ID)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(f.m.store.StateRoot(), "logins", "claude-auth")
	if err = os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	var refs []logins.ClaudeAuthRef
	for _, login := range r.Logins {
		ref := logins.ClaudeAuthRef{LoginID: login.ID, Identity: login.Identity, CredentialRevision: login.CredentialRevision, Nonce: strings.Repeat("e", 32)}
		refs = append(refs, ref)
		raw, _ := json.Marshal(map[string]any{"version": 1, "ref": ref, "selection": r.Providers[logins.ProviderClaude], "deadline": time.Now().UTC().Add(45 * time.Second), "attempt": 1, "phase": "prepared", "worker": map[string]int{"pid": 0, "start_time": 0}, "native": map[string]int{"pid": 0, "start_time": 0}})
		if err = os.WriteFile(filepath.Join(dir, ref.LoginID+".json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err = f.m.store.PrepareClaudeAuthRecovery(refs[0]); err != nil {
		t.Fatal(err)
	}
	if _, err = f.m.store.SettleClaudeAuth(refs[0]); err != nil {
		t.Fatal(err)
	}
	probes := 0
	f.m.claudeAuthCLI = func(ctx context.Context) (*persist.CLIIdentity, error) { probes++; <-ctx.Done(); return nil, ctx.Err() }
	f.m.claudeAuthCursor = 1
	for range 6 {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
		f.m.d.api.runLoginJobs(ctx)
		cancel()
	}
	if probes < 2 {
		t.Fatal("test did not exercise repeated support timeouts")
	}
	for _, ref := range refs {
		held, err := f.m.store.ReadClaudeAuth(ref)
		if err != nil || held.Phase != "failed" || held.Attempt != 1 {
			t.Fatalf("slow support probe starved cleanup/reset budget: %+v %v", held, err)
		}
	}
}

func TestClaudeFinalizePreLaunchWorktreePreparesActualCWDAndRollsBackBeforeFork(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("synthetic saved file authority and before-fork process identity are Linux-only")
	}
	for _, kind := range []string{"prepared", "unknown-writer"} {
		t.Run(kind, func(t *testing.T) {
			root, err := os.MkdirTemp("", "swarm-cwd-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(root) })
			root, err = filepath.EvalSymlinks(root)
			if err != nil {
				t.Fatal(err)
			}
			original := t.TempDir()
			worktree := filepath.Join(t.TempDir(), "actual-worktree")
			store, login, rt := claudeOwnerBirthFixture(t, root, original)
			if kind == "unknown-writer" {
				if err = os.Mkdir(filepath.Join(root, "unknown-legacy"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			d := &Daemon{stateDir: root}
			d.logins = &loginManager{d: d, store: store}
			sentinel := errors.New("stop before native fork")
			var deleted persist.Meta
			finalCalled := false
			core, err := daemon.Open(daemon.Config{StateDir: root, SocketPath: filepath.Join(root, "d.sock"), LockPath: filepath.Join(root, "d.lock"), LogPath: filepath.Join(root, "d.log"), ShimBinary: "/never-started", MaxSessions: 2,
				PreLaunch: func(_ string, _ daemon.LaunchSpec) (string, error) {
					if err := os.Mkdir(worktree, 0700); err != nil {
						return "", err
					}
					return worktree, nil
				},
				FinalizeLaunch: func(id string, spec daemon.LaunchSpec) (daemon.LaunchSpec, error) {
					finalCalled = true
					if spec.Cwd != worktree {
						t.Error("Finalize did not receive actual worktree CWD")
					}
					prepared, err := d.prepareLoginLaunch(id, spec)
					if kind == "unknown-writer" {
						if !errors.Is(err, logins.ErrPending) {
							t.Errorf("unproven writers accepted: %v", err)
							return prepared, sentinel
						}
						return prepared, err
					}
					if err != nil {
						return prepared, err
					}
					return prepared, sentinel
				},
				PreDelete: func(meta persist.Meta) error { deleted = meta; return os.RemoveAll(meta.AgentCwd) },
			})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = core.Close() }()
			d.core = core
			path, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			fp, err := persist.CLIFingerprint(path)
			if err != nil {
				t.Fatal(err)
			}
			env, err := store.PrepareEnvironment(rt, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = core.Launch(daemon.LaunchSpec{AgentType: logins.ProviderClaude, Cwd: original, Argv: []string{path, "--settings", `{"hooks":{"Stop":[]}}`}, ClientEnv: env, LoginRuntime: &rt, LoginStateRoot: root, CLIIdentity: &persist.CLIIdentity{Path: path, Version: "2.1.310", Fingerprint: fp}})
			expected := sentinel
			if kind == "unknown-writer" {
				expected = logins.ErrPending
			}
			if !errors.Is(err, expected) || !finalCalled {
				t.Fatalf("actual PreLaunch/Finalize boundary: %v", err)
			}
			if deleted.AgentCwd != worktree || deleted.Cwd != original || deleted.ShimPID != 0 {
				t.Fatalf("exact before-fork rollback lost AgentCwd: %+v", deleted)
			}
			if len(core.List()) != 0 {
				t.Fatal("failed finalization retained a running phantom")
			}
			r := mustLoginSnapshot(t, store)
			if slices.Contains(r.ClaudeProfiles[login.Identity].CWDs, worktree) != (kind == "prepared") {
				t.Fatal("final CWD membership did not reflect durable provisioning")
			}
		})
	}
}

func TestClaudeActualUnqualifiedNativeImageCannotCommitMode(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("actual fixed Linux image/architecture refusal uses synthetic version/manager executables")
	}
	f := claudeUsageFixture(t)
	bin := t.TempDir()
	scripts := map[string]string{
		"claude":    "#!/bin/sh\nprintf '2.1.310 (Claude Code)\\n'\n",
		"systemctl": "#!/bin/sh\nif [ \"$*\" != '--user --version' ]; then exit 44; fi\nprintf 'systemd 249\\n'\n",
		"busctl":    "#!/bin/sh\ncase \"$*\" in '--user get-property org.freedesktop.systemd1 /org/freedesktop/systemd1 org.freedesktop.systemd1.Manager Version') printf 's \"249.0\"\\n';; *) exit 44;; esac\n",
	}
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	before := mustLoginSnapshot(t, f.m.store)
	if _, err := f.m.d.api.Logins(protocol.LoginsReq{Action: "activate", LoginID: f.second.ID, ExpectedRevision: before.Revision}); !errors.Is(err, loginworker.ErrUnsupported) {
		t.Fatalf("unqualified native image committed mode: %v", err)
	}
	after := mustLoginSnapshot(t, f.m.store)
	if after.Revision != before.Revision || after.Providers[logins.ProviderClaude] != before.Providers[logins.ProviderClaude] {
		t.Fatal("same-version wrong image changed irreversible selection")
	}
}

func TestLoginsStaleActivationPreservesProtocolErrorAndDoesNotProbe(t *testing.T) {
	for _, provider := range []string{logins.ProviderClaude, logins.ProviderCodex} {
		t.Run(provider, func(t *testing.T) {
			fixture := newLoginFixture
			if provider == logins.ProviderClaude {
				fixture = claudeUsageFixture
			}
			f := fixture(t)
			before := mustLoginSnapshot(t, f.m.store)
			registry := filepath.Join(f.m.store.StateRoot(), "logins", "registry.json")
			beforeBytes, err := os.ReadFile(registry)
			if err != nil {
				t.Fatal(err)
			}
			probes, stops := 0, 0
			f.m.claudeAuthCLI = func(context.Context) (*persist.CLIIdentity, error) {
				probes++
				return nil, loginworker.ErrUnsupported
			}
			f.m.qualifyCodexOwnership = func([]persist.Meta) (bool, error) {
				probes++
				return false, nil
			}
			f.m.d.authw.resolve = func(string, []string) (string, error) {
				probes++
				return "", loginworker.ErrUnsupported
			}
			f.m.d.authw.kill = func(string) error { stops++; return nil }
			_, err = f.m.d.api.Logins(protocol.LoginsReq{Action: "activate", LoginID: f.second.ID, ExpectedRevision: before.Revision - 1})
			if !errors.Is(err, protocol.ErrLoginsStaleRevision) {
				t.Errorf("stale activation lost protocol error: %v", err)
			}
			afterBytes, err := os.ReadFile(registry)
			if err != nil {
				t.Fatal(err)
			}
			if probes != 0 || stops != 0 || len(f.launched) != 0 || f.m.claudeAuthPin != nil || string(afterBytes) != string(beforeBytes) {
				t.Fatal("stale activation probed or changed selection/session state")
			}
		})
	}
}
