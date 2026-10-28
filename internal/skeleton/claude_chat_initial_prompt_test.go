package skeleton

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/Nathandela/swarm/internal/protocol"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/idempotency"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/status"
)

type claudeNativeInitialCaller interface {
	sendClaudeNativeInitialPrompt(persist.Meta, string, string) error
}

func TestClaudeChatNativeInitialPromptUsesOneDurableSemanticEffect(t *testing.T) {
	f := newNativeA2Fixture(t, "success", "idle")
	caller, ok := any(f.d).(claudeNativeInitialCaller)
	if !ok {
		t.Fatal("APIabsence: native initial prompt has no bounded semantic caller")
	}
	const text = "first explicit owner 🐝\n--never-argv"
	operation := "claude-initial:" + f.meta.ID
	if err := caller.sendClaudeNativeInitialPrompt(f.meta, operation, text); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	writes := append([]shimwire.ClaudeMessageRequest(nil), f.writes...)
	f.mu.Unlock()
	if len(writes) != 1 || writes[0].OperationID != operation || writes[0].Text != text {
		t.Fatal("initial prompt did not use exactly one existing message dispatch")
	}
	record, exists := f.d.core.ComposerOperation(operation)
	intent, err := daemon.DecodeClaudeNativeIntent(record.Request)
	hash := sha256.Sum256([]byte(text))
	if !exists || err != nil || record.Action != "semantic_send" || record.SessionID != f.meta.ID || intent.Identity != f.identity || intent.TextSHA256 != hex.EncodeToString(hash[:]) || intent.NativeUserUUID != writes[0].NativeUserUUID || record.Phase == idempotency.PhasePrepared {
		t.Fatal("initial effect preceded durable exact semantic intent", err)
	}
	client := dialClient(t, f.d, protocol.CapSemanticSend)
	receipt, receiptErr := client.SemanticOperation(protocol.NamespacedID(f.d.machineID(), f.meta.ID), operation)
	if receiptErr != nil || receipt.Agent != "claude" || receipt.State != "submitted" || receipt.NativeMessageID != writes[0].NativeUserUUID {
		t.Fatal("wire initial receipt was not exact native submission", receipt, receiptErr)
	}
	before, _ := json.Marshal(record)
	// Same launch identity never acquires a fresh user UUID or rewrites provider input.
	_ = caller.sendClaudeNativeInitialPrompt(f.meta, operation, text)
	if err := caller.sendClaudeNativeInitialPrompt(f.meta, operation, "different text"); err == nil {
		t.Fatal("launch initial operation rebound to another prompt")
	}
	after, _ := f.d.core.ComposerOperation(operation)
	afterRaw, _ := json.Marshal(after)
	f.mu.Lock()
	count := len(f.writes)
	f.mu.Unlock()
	if count != 1 || string(before) != string(afterRaw) {
		t.Fatal("initial replay mutated custody or dispatched twice")
	}
}

func TestClaudeChatNativeInitialPromptNeedsStopAndNeverRedrivesExecuting(t *testing.T) {
	for _, kind := range []string{"missing-stop", "executing", "wrong-incarnation", "wrong-profile"} {
		t.Run(kind, func(t *testing.T) {
			f := newNativeA2Fixture(t, "success", "idle")
			caller, ok := any(f.d).(claudeNativeInitialCaller)
			if !ok {
				t.Fatal("APIabsence: native initial prompt semantic caller")
			}
			const text = "one initial prompt"
			operation := "claude-initial:" + f.meta.ID
			meta := f.meta
			switch kind {
			case "missing-stop":
				f.d.claudeNativeStops.Delete(meta.ID)
			case "executing":
				operation, _ = f.claim(t, text)
				if err := f.d.core.BeginComposerOperation(operation); err != nil {
					t.Fatal(err)
				}
			case "wrong-incarnation":
				meta.ShimStartTime++
			case "wrong-profile":
				copy := *meta.LoginRuntime
				copy.Epoch++
				meta.LoginRuntime = &copy
			}
			if err := caller.sendClaudeNativeInitialPrompt(meta, operation, text); err == nil {
				t.Fatalf("unqualified/previous initial effect admitted: %s", kind)
			}
			f.mu.Lock()
			count := len(f.writes)
			answers := f.answers
			f.mu.Unlock()
			if count != 0 || answers != 0 {
				t.Fatal("refused initial prompt wrote provider input")
			}
		})
	}
}

func TestClaudeChatNativeInitialOutcomeUnknownHydratesWithoutAnyReplay(t *testing.T) {
	f := newNativeA2Fixture(t, "outcome-unknown", "idle")
	caller, ok := any(f.d).(claudeNativeInitialCaller)
	if !ok {
		t.Fatal("APIabsence: native initial prompt semantic caller")
	}
	operation := "claude-initial:" + f.meta.ID
	const text = "uncertain initial owner 🐝\nretain exact text hash"
	if err := caller.sendClaudeNativeInitialPrompt(f.meta, operation, text); !errors.Is(err, protocol.ErrComposerOutcomeUnknown) {
		t.Fatalf("uncertain initial was not explicitly unknown: %v", err)
	}
	before, exists := f.d.core.ComposerOperation(operation)
	intent, err := daemon.DecodeClaudeNativeIntent(before.Request)
	if !exists || err != nil || intent.NativeUserUUID == "" {
		t.Fatal("uncertain initial lost durable UUID/request", err)
	}
	hash := sha256.Sum256([]byte(text))
	if intent.TextSHA256 != hex.EncodeToString(hash[:]) || intent.TextBytes != len(text) {
		t.Fatal("uncertain initial text binding changed")
	}
	f.mu.Lock()
	count := len(f.writes)
	f.mu.Unlock()
	if count != 1 {
		t.Fatal("uncertain write was retried before restart")
	}
	root := f.d.stateDir
	if err := f.d.Close(); err != nil {
		t.Fatal(err)
	}
	// Resolve the already-uncertain at-most-once disposition only after all old
	// writers have stopped. No second daemon writes this log concurrently.
	retained, err := idempotency.Open(filepath.Join(root, "idempotency"))
	if err != nil {
		t.Fatal(err)
	}
	if err := retained.ResolveOutcomeUnknown(operation); err != nil {
		t.Fatal(err)
	}
	f.d = assemble(t, func(c *Config) {
		c.StateDir = root
		c.SocketPath = filepath.Join(root, "d.sock")
		c.LockPath = filepath.Join(root, "d.lock")
		c.LogPath = filepath.Join(root, "d.log")
	})
	if err := f.d.registerClaudeNativeChat(f.meta); err != nil {
		t.Fatal(err)
	}
	hydrated, exists := f.d.core.ComposerOperation(operation)
	if !exists || hydrated.Phase != idempotency.PhaseOutcomeUnknown || !bytes.Equal(hydrated.Request, before.Request) || hydrated.RequestHash != before.RequestHash || hydrated.SessionInstance != before.SessionInstance {
		t.Fatal("hydration rebound uncertain initial request/UUID")
	}
	caller, ok = any(f.d).(claudeNativeInitialCaller)
	if !ok {
		t.Fatal("initial semantic caller missing after hydration")
	}
	if err := caller.sendClaudeNativeInitialPrompt(f.meta, operation, text); err == nil {
		t.Fatal("unknown initial was claimed as safely delivered after hydration")
	}
	after, _ := f.d.core.ComposerOperation(operation)
	f.mu.Lock()
	count = len(f.writes)
	f.mu.Unlock()
	if count != 1 || after.Phase != idempotency.PhaseOutcomeUnknown || !bytes.Equal(after.Request, before.Request) {
		t.Fatal("hydration or explicit retry replayed/replaced uncertain initial")
	}
}

func TestClaudeChatColdRegistrationRetainsIndependentStopWithoutSnapshot(t *testing.T) {
	f := newNativeA2Fixture(t, "success", "idle")
	root := f.d.stateDir
	if err := f.d.Close(); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.snapshotUnavailable = true
	f.allowStop = true
	f.mu.Unlock()
	f.d = assemble(t, func(c *Config) {
		c.StateDir = root
		c.SocketPath = filepath.Join(root, "d.sock")
		c.LockPath = filepath.Join(root, "d.lock")
		c.LogPath = filepath.Join(root, "d.log")
	})
	started := time.Now()
	// Input registration may refuse unavailable read authority. Stop-only custody
	// must already bind the anchored native scope and exact kernel shim birth.
	_ = f.d.registerClaudeNativeChat(f.meta)
	handle, present := f.d.claudeNativeStops.Load(f.meta.ID)
	if !present || handle.(claudeNativeStopHandle).binding.SessionID != f.meta.ID || handle.(claudeNativeStopHandle).binding.ScopeGeneration == "" {
		t.Fatal("cold registration depended on a primary snapshot for Stop custody")
	}
	if err := f.d.ownerKill(f.meta.ID); err != nil {
		t.Fatal("unavailable snapshot blocked independent Stop", err)
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("cold registration/Stop exceeded independent bound")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stops != 1 || len(f.writes) != 0 || f.answers != 0 {
		t.Fatal("cold hydration sent input or failed to reach exact Stop peer")
	}
}

func TestClaudeChatAuthenticatedWireStopBypassesHeldConsumerAndCoordinator(t *testing.T) {
	f := newNativeA2Fixture(t, "success", "idle")
	f.mu.Lock()
	f.allowStop = true
	beforeWrites, beforeAnswers := len(f.writes), f.answers
	f.mu.Unlock()
	control := dialClient(t, f.d)
	lane := f.d.composerLaneFor(f.meta.ID)
	state := f.d.claudeNativeState(f.meta.ID)
	if state == nil || f.d.logins == nil {
		t.Fatal("registered native state/coordinator absent")
	}
	f.d.logins.stepMu.Lock()
	lane.endMu.Lock()
	lane.mu.Lock()
	state.mu.Lock()
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { state.mu.Unlock(); lane.mu.Unlock(); lane.endMu.Unlock(); f.d.logins.stepMu.Unlock() })
	}
	defer release()
	done := make(chan error, 1)
	started := time.Now()
	go func() { done <- control.Kill(protocol.NamespacedID(f.d.api.endpointID, f.meta.ID)) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("authenticated Stop blocked/refused behind held consumer/coordinator", err)
		}
	case <-time.After(3250 * time.Millisecond):
		release()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("wire Stop did not join after test fence release")
		}
		t.Fatal("authenticated wire Stop exceeded independent bound")
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("wire Stop missed three-second contract")
	}
	// Assert the actual wire effect before releasing any blocked non-Stop lane.
	f.mu.Lock()
	stops, writes, answers := f.stops, len(f.writes), f.answers
	f.mu.Unlock()
	if stops != 1 || writes != beforeWrites || answers != beforeAnswers {
		t.Fatal("wire Stop did not reach exactly one retained peer without input")
	}
	if lane.ownerEnding || lane.retired || lane.recycling {
		t.Fatal("containment Stop entered lifecycle deletion/recycling")
	}
	release()
	rows := f.d.core.List()
	if len(rows) != 1 || rows[0].ID != f.meta.ID || rows[0].ShimPID != f.meta.ShimPID || rows[0].ShimStartTime != f.meta.ShimStartTime {
		t.Fatal("wire Stop deleted/replaced native custody or made a successor")
	}
}

func TestClaudeChatNativeInitialStartupRefusalIsVisibleAndAllowsExplicitRecovery(t *testing.T) {
	f := newNativeA2Fixture(t, "success", "idle")
	f.mu.Lock()
	f.snapshotUnavailable = true
	f.mu.Unlock()
	operation := "claude-initial:" + f.meta.ID
	done := make(chan error, 1)
	joined := make(chan struct{})
	defer func() {
		select {
		case <-joined:
		case <-time.After(8 * time.Second):
			t.Error("startup caller did not join")
		}
	}()
	go func() {
		defer close(joined)
		done <- f.d.bootstrapClaudeNativeLaunch(f.meta, "", "recover exact unsent initial draft")
	}()
	deadline := time.Now().Add(time.Second)
	for {
		if rec, ok := f.d.core.ComposerOperation(operation); ok {
			if rec.Phase != idempotency.PhasePrepared || len(rec.Request) != 0 {
				t.Fatal("startup invented typed input before primary proof")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("startup did not retain initial semantic claim")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("unavailable startup falsely delivered initial")
		}
	case <-time.After(7 * time.Second):
		t.Fatal("startup refusal exceeded finite bound")
	}
	client := dialClient(t, f.d, protocol.CapSemanticSend)
	receipt, err := client.SemanticOperation(protocol.NamespacedID(f.d.machineID(), f.meta.ID), operation)
	if err != nil || receipt.Agent != "claude" || receipt.State != "rejected" || !strings.Contains(receipt.Reason, "unsent") {
		t.Fatal("actual initial receipt hid affirmative unsent outcome", receipt, err)
	}
	f.mu.Lock()
	count := len(f.writes)
	f.snapshotUnavailable = false
	f.mu.Unlock()
	if count != 0 {
		t.Fatal("startup refused after writing input")
	}
	if err = f.drain(t); err != nil {
		t.Fatal(err)
	}
	if _, err = f.send(t, "explicit recovery is a new owner operation"); err != nil {
		t.Fatal("known unsent initial poisoned later owner recovery", err)
	}
}

func TestClaudeChatDelayedInitialCannotSupersedeNewExplicitOwner(t *testing.T) {
	f := newNativeA2Fixture(t, "success", "idle")
	if err := f.drain(t); err != nil {
		t.Fatal(err)
	}
	operation := "claude-initial:" + f.meta.ID
	req, fresh, err := f.d.claimClaudeNativeInitial(f.meta, operation, "older launch initial")
	if err != nil || !fresh {
		t.Fatal("initial owner claim missing", err)
	}
	if _, err = f.send(t, "newer explicit owner wins"); err != nil {
		t.Fatal(err)
	}
	if err = f.d.dispatchClaudeNativeInitial(f.meta, operation, req); err == nil {
		t.Fatal("delayed initial superseded newer explicit owner")
	}
	rec, ok := f.d.core.ComposerOperation(operation)
	execution, e := f.d.eng.ExecutionSnapshot(f.meta.ID)
	if !ok || rec.Phase != idempotency.PhaseFailed || len(rec.Request) != 0 || e != nil || execution.OwnerRevision != 1 {
		t.Fatal("superseded initial mutated native intent/revision", rec, e)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.writes) != 1 || f.writes[0].Text != "newer explicit owner wins" {
		t.Fatal("delayed launch initial acquired provider input")
	}
}

func TestClaudeChatNativeInitialUsesBoundedWinningSessionKeyForLongestLaunchID(t *testing.T) {
	f := newNativeA2Fixture(t, "success", "idle")
	if err := f.d.bootstrapClaudeNativeLaunch(f.meta, strings.Repeat("l", 128), "initial from bounded winning session identity"); err != nil {
		t.Fatal(err)
	}
	expected := "claude-initial:" + f.meta.ID
	rec, exists := f.d.core.ComposerOperation(expected)
	if !exists || rec.Phase != idempotency.PhaseCompleted || len(expected) > 128 {
		t.Fatal("long launch ID did not keep bounded deterministic initial operation", rec)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.writes) != 1 || f.writes[0].OperationID != expected {
		t.Fatal("long launch ID changed initial write identity")
	}
}

func TestClaudeChatColdIdleProofNeverRestoresNativeInputAuthority(t *testing.T) {
	f := newNativeA2Fixture(t, "success", "idle")
	root := f.d.stateDir
	f.d.drains.mu.Lock()
	cursor := f.d.drains.drainers[f.meta.ID].cursorPath
	f.d.drains.mu.Unlock()
	if err := f.d.Close(); err != nil {
		t.Fatal(err)
	}
	// Force the synthetic cold consumer to fold a complete pristine init prefix.
	// This is not a restart input-recovery or native-birth qualification.
	if err := os.Remove(cursor); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	f.d = assemble(t, func(c *Config) {
		c.StateDir = root
		c.SocketPath = filepath.Join(root, "d.sock")
		c.LockPath = filepath.Join(root, "d.lock")
		c.LogPath = filepath.Join(root, "d.log")
	})
	if err := f.d.registerClaudeNativeChat(f.meta); err != nil {
		t.Fatal(err)
	}
	f.d.startHookDrain(f.meta.ID)
	if err := f.drain(t); err != nil {
		t.Fatal(err)
	}
	state := f.d.claudeNativeState(f.meta.ID)
	state.mu.Lock()
	proved := state.healthy && state.initialized && state.ready
	state.mu.Unlock()
	if !proved {
		t.Fatal("cold idle proof did not fold; input-negative would be vacuous")
	}
	cap, ok := f.d.sessionCapabilities(f.meta.ID)
	if !ok || cap.NativeInteractionVersion != 2 || cap.NativeComposer {
		t.Fatal("cold idle evidence restored input authority", cap, ok)
	}
	if _, err := f.send(t, "explicit new message must not bypass cold input quarantine"); err == nil {
		t.Fatal("cold registration wrote native input")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.writes) != 0 || f.answers != 0 {
		t.Fatal("cold hydration caused provider input")
	}
}

func TestClaudeChatNativeMetadataUsesExactHouseStatusKeys(t *testing.T) {
	meta := persist.Meta{SchemaVersion: 6, ID: "native-status", AgentType: "claude", RequiredRuntime: "claude-chat", Status: status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone, Attention: true, NativeWorkUnknown: true}}
	raw, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if !strictClaudeNativeJSON(raw, "metadata", 0) {
		t.Fatal("authentic persisted house status casing was refused", string(raw))
	}
	alias := bytes.Replace(raw, []byte(`"Process":`), []byte(`"process":`), 1)
	if bytes.Equal(alias, raw) || strictClaudeNativeJSON(alias, "metadata", 0) {
		t.Fatal("case-aliased persisted status gained authority")
	}
	duplicate := bytes.Replace(raw, []byte(`"Process":"running"`), []byte(`"Process":"lost","Process":"running"`), 1)
	if bytes.Equal(duplicate, raw) || strictClaudeNativeJSON(duplicate, "metadata", 0) {
		t.Fatal("duplicate nested status gained authority")
	}
}

// Component-only pre-capability bootstrap: no native birth or provider-write qualification.
func TestClaudeChatFreshBootstrapBindsInstanceBeforeInitialClaim(t *testing.T) {
	for _, mode := range []string{"fresh", "stale-birth", "cold", "mint-failure", "generic-send"} {
		t.Run(mode, func(t *testing.T) {
			f := newNativeA2Fixture(t, "success", "idle")
			root := t.TempDir()
			closing := make(chan struct{})
			close(closing)
			// A separate component has no prior instance, capabilities or consumer.
			// Missing native scope plus closed shutdown stops after the early claim.
			d := &Daemon{core: f.d.core, stateDir: root, closing: closing}
			d.capStore.dir = root
			d.api = &coreAPI{endpointID: f.d.machineID()}
			d.api.claudeNativeLaunched = d.bootstrapClaudeNativeLaunch
			if mode != "cold" {
				d.noteClaudeNativeLiveLaunch(f.meta)
			}
			m := f.meta
			if mode == "stale-birth" {
				m.ShimStartTime++
			}
			if mode == "mint-failure" {
				blocker := filepath.Join(root, "blocked")
				if err := os.WriteFile(blocker, nil, 0600); err != nil {
					t.Fatal(err)
				}
				d.capStore.dir = blocker
			}
			operation := "claude-initial:" + m.ID
			var err error
			if mode == "generic-send" {
				err = d.sendClaudeNativeInitialPrompt(m, operation, "initial binding component")
			} else {
				err = d.api.claudeNativeLaunched(m, "", "initial binding component")
			}
			if err == nil {
				t.Fatal("unavailable component unexpectedly dispatched input")
			}
			record, claimed := d.core.ComposerOperation(operation)
			if mode == "fresh" {
				raw, readErr := os.ReadFile(filepath.Join(root, m.ID, sessionInstanceFile))
				instance, birth := decodeSessionInstance(string(raw))
				if readErr != nil || instance == "" || birth != incarnationOf(m.ShimPID, []int64{m.ShimStartTime}) {
					t.Fatalf("fresh bootstrap did not persist exact winning instance before initial claim: %v", readErr)
				}
				if !claimed || record.SessionInstance != instance || record.Phase != idempotency.PhaseFailed || len(record.Request) != 0 {
					t.Fatal("fresh bootstrap did not retain the shutdown-rejected initial claim", record)
				}
			} else {
				if claimed {
					t.Fatal("refused bootstrap acquired an initial operation", record)
				}
				if mode != "mint-failure" {
					if _, exists := d.sessionInstance(m.ID); exists {
						t.Fatal("refused bootstrap minted an instance")
					}
				}
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.writes) != 0 || f.answers != 0 {
				t.Fatal("early binding component wrote provider input")
			}
		})
	}
}
