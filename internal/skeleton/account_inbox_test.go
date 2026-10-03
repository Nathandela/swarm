package skeleton

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/claude"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/status"
)

func inboxClaudeFixture(t *testing.T) (*accountRotationManager, *authFake, persist.Meta) {
	t.Helper()
	manager := accountTestManager(t, accountTestState(t))
	candidate := accountTestCandidate(t, manager, accounts.ProviderClaude, "synthetic-inbox-account")
	account := accountTestAdmit(t, manager, candidate)
	source := accountTestSource(manager.stateRoot, accountTestBinding(t, manager, account))
	source.AgentType = accounts.ProviderClaude
	source.CLIIdentity.Version = "2.1.288"
	m, fake := rotationTestManager(t, manager.store, manager.stateRoot, source)
	return m, fake, source
}

func TestAccountInboxDurableAcceptanceSurvivesFullQueueAndRestart(t *testing.T) {
	m, _, source := inboxClaudeFixture(t)
	for i := 0; i < cap(m.w.managedOps); i++ {
		m.w.managedOps <- accountOwnerOperation{apply: func(*authWatcher) error { return nil }}
	}
	raw, _ := json.Marshal(map[string]string{"session_id": source.ConversationID, "error": "rate_limit", "error_description": "synthetic-secret-never-persist"})
	if err := m.NoteClaudeFailure(engine.Callback{SessionID: source.ID, Token: "synthetic-token-never-persist", Event: "StopFailure", Sequence: 7, Raw: raw}); err != nil {
		t.Fatal(err)
	}
	root, err := openAccountInbox(m.w.stateDir, false)
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := inboxEntries(root)
	data, err := root.ReadFile(entries[0].Name())
	_ = root.Close()
	if err != nil || strings.Contains(string(data), "synthetic-secret") || strings.Contains(string(data), "synthetic-token") {
		t.Fatal("raw credentials or error text entered the inbox")
	}
	fresh, _ := rotationTestManager(t, m.store, m.w.stateDir, source)
	if err := fresh.drainInbox(); err != nil {
		t.Fatal(err)
	}
	rec := fresh.w.state.AccountRotations[source.ID]
	if rec.State != accountObserved || rec.Incident.Model != source.LaunchOptions["model"] || len(rec.Incident.TriedAccounts) != 1 {
		t.Fatal("restart lost the accepted terminal fact or exact model")
	}
	loaded, err := loadAuthWatchStateChecked(m.w.stateDir)
	if err != nil || loaded.AccountSchemaVersion != accounts.RecoverySchemaVersion {
		t.Fatal("replayed evidence did not publish the guarded recovery schema", err)
	}
}

func TestAccountInboxModelCapacityKeepsLatestProofPending(t *testing.T) {
	m, fake, source := inboxClaudeFixture(t)
	m.w.state.AccountSchemaVersion = accounts.RecoverySchemaVersion
	for i := 0; i < 4096; i++ {
		m.w.state.AccountModels["retained-model-"+fmtUint(uint64(i))] = accountModelRecord{Binding: *source.AccountBinding, Model: "old-retained-model"}
	}
	if err := m.w.saveState(); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{"session_id": source.ConversationID, "model": "new-native-model"})
	if err := m.NoteConversation(source.ID, source.ConversationID, raw, 8); err != nil {
		t.Fatal(err)
	}
	if err := m.drainInbox(); err == nil {
		t.Fatal("over-capacity model unexpectedly published")
	}
	if m.effectiveModel(source) != "" {
		t.Fatal("pending new model fell back to stale saved launch model")
	}
	if err := m.reportFailure(m.w, source.ID, "quota", "old-hint", "pending-model-failure"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		m.step()
	}
	if len(fake.killed) != 0 || m.w.state.AccountRotations[source.ID].Incident.Model != "" {
		t.Fatal("pending model authorized destructive recovery")
	}
	delete(m.w.state.AccountModels, "retained-model-0")
	if err := m.drainInbox(); err != nil {
		t.Fatal(err)
	}
	if got := m.effectiveModel(source); got != "new-native-model" {
		t.Fatalf("durable model retry failed: %q", got)
	}
}

func TestAccountInboxUnreadableEvidenceHoldsAllManagedModelAuthority(t *testing.T) {
	store, root, bindings := accountTestStore(t, 1)
	source := accountTestSource(root, bindings[0])
	m, _ := rotationTestManager(t, store, root, source)
	dir := filepath.Join(root, accountInboxDirectory)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, strings.Repeat("a", 64)+".json"), []byte("malformed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.drainInbox(); err == nil || m.effectiveModel(source) != "" {
		t.Fatal("unreadable evidence left stale model authority usable")
	}
}

func TestAccountNativeAdmissionHoldSurvivesRestartAndOwnerEnd(t *testing.T) {
	store, root, bindings := accountTestStore(t, 1)
	source := accountTestSource(root, bindings[0])
	m, _ := rotationTestManager(t, store, root, source)
	dir := filepath.Join(root, accountInboxDirectory)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < accountInboxMaxEntries; i++ {
		rec, _ := m.inboxRecord(source.ID, "native", source.ConversationID, uint64(i+1))
		rec.ID = accountInboxID(rec)
		raw, _ := json.Marshal(rec)
		if err := os.WriteFile(filepath.Join(dir, rec.ID+".json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	input, _ := m.inboxRecord(source.ID, "failure", source.ConversationID, 999)
	input.Class = "quota"
	if err := m.acceptInbox(input); !errors.Is(err, errAccountInboxRetry) {
		t.Fatal("full inbox acknowledged missing evidence", err)
	}
	m.nativeInboxFailed(source.ID, "")
	fresh, fake := rotationTestManager(t, store, root, source)
	if err := fresh.drainObservationHolds(); err != nil {
		t.Fatal(err)
	}
	if fresh.effectiveModel(source) != "" {
		t.Fatal("restart lost native admission hold")
	}
	benign, _ := fresh.inboxRecord(source.ID, "model", source.ConversationID, 2000)
	benign.Model = "new-benign-model"
	// Retire the capacity fixture; benign evidence cannot remove the independent hold.
	for _, entry := range mustInboxEntries(t, fresh) {
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
			t.Fatal(err)
		}
	}
	if err := fresh.acceptInbox(benign); err != nil {
		t.Fatal(err)
	}
	if err := fresh.drainInbox(); err != nil {
		t.Fatal(err)
	}
	if fresh.effectiveModel(source) != "" {
		t.Fatal("benign evidence cleared an unknown terminal observation hold")
	}
	ended := source
	ended.Status.Process = status.ProcessExited
	fake.add(ended)
	if err := fresh.drainObservationHolds(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, source.ID, accountObservationHoldFile)); !os.IsNotExist(err) {
		t.Fatal("owner end failed to durably retire the hold", err)
	}
}

func mustInboxEntries(t *testing.T, m *accountRotationManager) []os.DirEntry {
	t.Helper()
	root, err := openAccountInbox(m.w.stateDir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	entries, err := inboxEntries(root)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestAccountHookAcknowledgementWaitsForDurableAcceptance(t *testing.T) {
	m, _, source := inboxClaudeFixture(t)
	manager := &accountManager{store: m.store, stateRoot: m.w.stateDir}
	d := &Daemon{eng: engine.New(engine.Config{}), stateDir: m.w.stateDir, core: accountTestCore(t, manager, nil), accountRotation: m}
	d.eng.RegisterSession(source.ID, "synthetic-live-token", os.Getpid(), nil)
	if err := os.Mkdir(filepath.Join(m.w.stateDir, source.ID), 0o700); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"session_id": source.ConversationID, "error": "rate_limit"})
	cb := engine.Callback{SessionID: source.ID, Token: "foreign-token", Sequence: 7, Event: "StopFailure", Raw: body}
	raw, _ := json.Marshal(cb)
	if err := d.ingestHookBytes(raw); err == nil {
		t.Fatal("unauthenticated callback accepted")
	}
	inbox := filepath.Join(m.w.stateDir, accountInboxDirectory)
	if _, err := os.Stat(inbox); !os.IsNotExist(err) {
		t.Fatal("foreign token touched the inbox")
	}
	if err := os.WriteFile(inbox, []byte("unsafe inbox path"), 0o600); err != nil {
		t.Fatal(err)
	}
	cb.Token = "synthetic-live-token"
	raw, _ = json.Marshal(cb)
	hd := &HookDrainer{d: d, cursorPath: filepath.Join(m.w.stateDir, source.ID, "test-cursor")}
	response := shim.HookDrainResponse{Records: []shim.HookRecord{{Seq: 1, Body: raw}}}
	if _, _, err := hd.applyLocked(response, 0); !errors.Is(err, errAccountInboxRetry) {
		t.Fatal("admission failure was not retryable", err)
	}
	if hd.cursor() != 0 || d.hookSeqDuplicate(source.ID, cb.Sequence) {
		t.Fatal("unaccepted observation was acknowledged")
	}
	if err := os.Remove(inbox); err != nil {
		t.Fatal(err)
	}
	// Status replay must not erase the independently accepted account effect.
	statusCB := cb
	statusCB.Payload = map[string]string{engine.PayloadKeyTurn: "idle"}
	if err := d.eng.HandleCallback(statusCB); err != nil {
		t.Fatal(err)
	}
	if _, _, err := hd.applyLocked(response, 0); err != nil {
		t.Fatal(err)
	}
	fresh, _ := rotationTestManager(t, m.store, m.w.stateDir, source)
	if err := fresh.drainInbox(); err != nil {
		t.Fatal(err)
	}
	if len(fresh.w.state.AccountRotations) != 1 {
		t.Fatal("engine status replay erased accepted recovery evidence")
	}
}

func TestAccountFirstManagedSessionStartPersistsDefaultModelBeforeAcknowledgement(t *testing.T) {
	m, _, source := inboxClaudeFixture(t)
	source.ConversationID, source.LaunchOptions = "", map[string]string{}
	source.Status.Process = status.ProcessExited
	disk, err := persist.NewStore(m.w.stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := disk.Save(source); err != nil {
		t.Fatal(err)
	}
	core := accountTestCore(t, &accountManager{store: m.store, stateRoot: m.w.stateDir}, nil)
	source.Status.Process = status.ProcessRunning
	if err := core.SetStatus(source.ID, source.Status); err != nil {
		t.Fatal(err)
	}
	m.w.get, m.w.list = core.Get, core.List
	d := &Daemon{eng: engine.New(engine.Config{}), core: core, stateDir: m.w.stateDir, accountRotation: m}
	d.setAdapterForTest(func(string) (adapter.Adapter, bool) { return claude.New(), true })
	d.eng.RegisterSession(source.ID, "first-start-token", os.Getpid(), nil)
	body, _ := json.Marshal(map[string]string{"session_id": hookConversationID, "model": "claude-native-default-model"})
	cb := engine.Callback{SessionID: source.ID, Token: "first-start-token", Sequence: 1, Event: "SessionStart", Payload: map[string]string{"session_id": hookConversationID}, Raw: body}
	raw, _ := json.Marshal(cb)
	// A failed identity write cannot acknowledge the callback or drop its model.
	metaPath := filepath.Join(m.w.stateDir, source.ID, "meta.json")
	if err := os.Remove(metaPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(metaPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := d.ingestHookBytes(raw); !errors.Is(err, errAccountInboxRetry) || d.hookSeqDuplicate(source.ID, cb.Sequence) {
		t.Fatal("failed initial identity persistence acknowledged critical evidence", err)
	}
	if err := os.Remove(metaPath); err != nil {
		t.Fatal(err)
	}
	if err := d.ingestHookBytes(raw); err != nil {
		t.Fatal(err)
	}
	actual, _ := core.Get(source.ID)
	if actual.ConversationID != hookConversationID || !d.hookSeqDuplicate(source.ID, cb.Sequence) {
		t.Fatal("authenticated SessionStart did not establish its write-once identity")
	}
	if err := m.drainInbox(); err != nil {
		t.Fatal(err)
	}
	if got := m.effectiveModel(actual); got != "claude-native-default-model" {
		t.Fatalf("first acknowledged SessionStart lost its exact native model: %q", got)
	}
}
