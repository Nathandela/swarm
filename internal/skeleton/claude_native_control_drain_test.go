package skeleton

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/claudeinput"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/status"
)

func TestClaudeNativeControlMalformedReceiptNeverAdvances(t *testing.T) {
	for _, body := range []string{`{}`, `{"event":null}`, `{"event":"CLAUDE-NATIVE-CONTROL"}`, `{"Event":"claude-native-control"}`, `{"event":"claude-native-control","event":"Stop"}`, `{"event":"claude-native-control","required_runtime":null}`, `{"event":"claude-native-control","version":999}`, `{`} {
		t.Run(body, func(t *testing.T) {
			d, m, _, _, _ := claudeControlExecutionFixture(t)
			cursor := filepath.Join(d.stateDir, m.ID, "strict-fold")
			hd := NewHookDrainer(d, m.ID, "", cursor)
			applied, skipped, err := hd.applyLocked(shim.HookDrainResponse{Records: []shim.HookRecord{{Seq: 1, Body: []byte(body)}}}, 0)
			var custody *engine.ExecutionCustodyError
			if !errors.As(err, &custody) || applied != 0 || skipped != 0 {
				t.Errorf("bound malformed receipt reached rejected skip: applied=%d skipped=%d err=%v", applied, skipped, err)
			}
			if hd.cursor() != 0 {
				t.Error("malformed bound receipt advanced fold cursor")
			}
			if _, err := os.Stat(cursor); !os.IsNotExist(err) {
				t.Error("malformed receipt persisted cursor")
			}
			if d.hookSeqDuplicate(m.ID, 1) || hd.certificatePending != nil {
				t.Fatal("malformed receipt advanced ingest/certificate")
			}
		})
	}
}

func TestClaudeNativeControlStrictDiskFinalAndRetirement(t *testing.T) {
	for _, final := range []bool{false, true} {
		t.Run(fmt.Sprint(final), func(t *testing.T) {
			d, m, _, _, _ := claudeControlExecutionFixture(t)
			path := filepath.Join(d.stateDir, m.ID, shim.HookSpoolFile)
			producer, err := shim.OpenHookSpoolProducer(path, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := producer.Append([]byte(`{"event":null}`)); err != nil {
				t.Fatal(err)
			}
			if err := producer.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			cursor := filepath.Join(d.stateDir, m.ID, "strict-fold")
			hd := NewHookDrainer(d, m.ID, "", cursor)
			hd.SetSpoolPath(path)
			if final {
				hd.FinalDrain()
			} else {
				_, _, err = hd.drainFromSpoolFile()
				var custody *engine.ExecutionCustodyError
				if !errors.As(err, &custody) {
					t.Fatal("disk malformed receipt not held", err)
				}
			}
			if hd.cursor() != 0 {
				t.Fatal("strict disk/final pass advanced cursor")
			}
			if err := hd.retireCertifiedPrefix(); err == nil {
				t.Fatal("malformed strict tail granted retirement")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("malformed strict tail bytes changed", err)
			}
		})
	}
}
func TestClaudeNativeControlBindingPrecedesDestructiveDrain(t *testing.T) {
	d, m, _ := nativeControlUnsafeFixture(t, "claude-native-control")
	cursor := filepath.Join(d.stateDir, m.ID, "strict-fold")
	if err := persistHookCursor(cursor, 7); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(d.stateDir, "strict-socket")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan shim.HookDrainRequest, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		var tag [1]byte
		if _, err := io.ReadFull(conn, tag[:]); err != nil {
			return
		}
		var request shim.HookDrainRequest
		if json.NewDecoder(conn).Decode(&request) != nil {
			return
		}
		requests <- request
		_ = json.NewEncoder(conn).Encode(shim.HookDrainResponse{})
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done })
	hd := NewHookDrainer(d, m.ID, path, cursor)
	if hd.claudeControl == nil {
		t.Fatal("constructor did not bind immutable runtime")
	}
	hd.claudeControl.meta.ShimStartTime++
	_, _, err = hd.DrainOnce()
	var custody *engine.ExecutionCustodyError
	if !errors.As(err, &custody) {
		t.Fatal("lost binding reached drain", err)
	}
	select {
	case request := <-requests:
		t.Fatalf("unbound destructive FoldSeq request sent: %+v", request)
	default:
	}
	if hd.cursor() != 7 {
		t.Fatal("lost binding changed existing cursor")
	}
}
func TestClaudeNativeControlDirectControlCannotApply(t *testing.T) {
	d, m, _ := nativeControlUnsafeFixture(t, "claude-native-control")
	d.eng = engine.New(engine.Config{})
	d.eng.RegisterSession(m.ID, "token", m.ShimPID, nil, m.Status)
	for _, body := range []string{`{"event":"claude-native-control","event":"Stop"}`, `{"required_runtime":"claude-native-control","event":"Stop"}`, `{"Event":"CLAUDE-NATIVE-CONTROL"}`} {
		var custody *engine.ExecutionCustodyError
		if err := d.ingestHookBytes([]byte(body)); !errors.As(err, &custody) {
			t.Fatal("best-effort accepted control-shaped receipt", err)
		}
	}
}

func TestClaudeNativeControlOrdinaryLaneAndEndedTail(t *testing.T) {
	t.Run("ordinary", func(t *testing.T) {
		d, m, _, _, _ := claudeControlExecutionFixture(t)
		raw, _ := json.Marshal(engine.Callback{SessionID: m.ID, Token: strings.Repeat("a", 64), Sequence: 1, Event: "PreToolUse", Payload: map[string]string{"tool_name": "Read"}, Raw: json.RawMessage(`{"toolUseId":"native","optional":null}`)})
		hd := NewHookDrainer(d, m.ID, "", filepath.Join(d.stateDir, m.ID, "strict-fold"))
		applied, skipped, err := hd.applyLocked(shim.HookDrainResponse{Records: []shim.HookRecord{{Seq: 1, Body: raw}}}, 0)
		if err != nil || applied != 1 || skipped != 0 || hd.cursor() != 1 {
			t.Fatalf("explicit ordinary lane changed: %d %d %v", applied, skipped, err)
		}
	})
	t.Run("ended-startup-retains-unread", func(t *testing.T) {
		d, m, _ := nativeControlUnsafeFixture(t, "claude-native-control")
		m.Status.Process = status.ProcessExited
		if err := d.core.SetStatus(m.ID, m.Status); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(d.stateDir, m.ID)
		path := filepath.Join(dir, shim.HookSpoolFile)
		producer, err := shim.OpenHookSpoolProducer(path, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := producer.Append([]byte(`{"event":null}`)); err != nil {
			t.Fatal(err)
		}
		incarnation, certificate := producer.IncarnationID(), producer.ProducerCertificate()
		if err := producer.Close(); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(map[string]string{"hook_socket_path": filepath.Join(dir, "no-socket")})
		if err := os.WriteFile(filepath.Join(dir, "shim-launch.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		// Models a restored cursor/certificate authored by an old permissive skip;
		// startup must not turn it into disposal of an unread strict receipt.
		if err := persistHookCursor(filepath.Join(dir, "hook.fold"), 1); err != nil {
			t.Fatal(err)
		}
		proof, _ := json.Marshal(hookCursorCertificate{Incarnation: incarnation, Producer: certificate, Sequence: 1})
		if err := os.WriteFile(filepath.Join(dir, "hook.fold.cert"), proof, 0600); err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(path)
		d.startHookDrainsForRunning()
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("ended startup retired strict receipt without real fold", err)
		}
		if d.eng != nil || len(d.drains.drainers) != 0 {
			t.Fatal("ended retention invented live authentication/replay")
		}
	})
}

func claudeControlExecutionFixture(t *testing.T) (*Daemon, persist.Meta, *persist.Store, *HookDrainer, claudeinput.ControlEnvelope) {
	t.Helper()
	root, err := os.MkdirTemp("", "c2-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := persist.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	meta := a2QuarantineMeta(t, root, "control", false)
	meta.RequiredRuntime = claudeinput.NativeControlRuntime
	if err := store.Save(meta); err != nil {
		t.Fatal(err)
	}
	// Synthetic restored metadata tests the assembly admission, not native birth.
	meta.ShimPID = 123
	meta.ShimStartTime = 456
	raw, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(root, meta.ID, "meta.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	core, err := daemon.Open(daemon.Config{StateDir: root, SocketPath: filepath.Join(root, "d"), LockPath: filepath.Join(root, "l"), LogPath: filepath.Join(root, "log"), MaxSessions: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = core.Close() })
	meta, ok := core.Get(meta.ID)
	if !ok || meta.ShimPID != 123 || meta.ShimStartTime != 456 {
		t.Fatal("synthetic metadata prerequisite lost", meta)
	}
	desc := claudeinput.Descriptor{SessionID: meta.ID, Generation: strings.Repeat("b", 32), Token: strings.Repeat("d", 64), SocketPath: filepath.Join(root, meta.ID, "claude-input.sock"), ProviderVersion: "2.1.310", ShimPID: 123, ShimStartTime: 456, LifecycleFence: true, RequiredRuntime: claudeinput.NativeControlRuntime}
	raw, _ = json.Marshal(desc)
	if err := os.WriteFile(filepath.Join(root, meta.ID, claudeinput.DescriptorFile), raw, 0600); err != nil {
		t.Fatal(err)
	}
	e := engine.New(engine.Config{StoreExecution: store.SaveExecution})
	if err := e.RegisterExecutionSession(meta.ID, strings.Repeat("a", 64), 123, nil, status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone}, engine.ExecutionIdentity{Incarnation: executionIncarnation(meta), ClaudeNativeControl: true}, nil); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{core: core, stateDir: root, eng: e, executionStore: store}
	hd := NewHookDrainer(d, meta.ID, "", filepath.Join(root, meta.ID, "strict-fold"))
	envelope := claudeinput.ControlEnvelope{Event: claudeinput.NativeControlRuntime, RequiredRuntime: claudeinput.NativeControlRuntime, Version: 1, SessionID: meta.ID, Token: strings.Repeat("a", 64), DescriptorGeneration: desc.Generation, ShimPID: 123, ShimStartTime: 456, Worker: claudeinput.Worker{NativeSessionID: "4a7a2465-d8f0-4c05-a7a9-c44d8077b22b", Generation: "guard", Version: "2.1.310"}, Sequence: 1, ReportHash: strings.Repeat("c", 64), Invocation: 1, NativeEvent: "turn.step", Phase: "entered", Context: json.RawMessage(`{"turn_id":"turn","index":0,"message_count":1,"origin":{"plugin":"engine","tier":"core"}}`), Data: json.RawMessage(`{}`)}
	return d, meta, store, hd, envelope
}
func TestClaudeNativeControlFirstControlBeforeClassicIdentity(t *testing.T) {
	d, meta, store, hd, envelope := claudeControlExecutionFixture(t)
	raw, _ := json.Marshal(envelope)
	applied, skipped, err := hd.applyLocked(shim.HookDrainResponse{Records: []shim.HookRecord{{Seq: 1, Body: raw}}}, 0)
	if err != nil || applied != 1 || skipped != 0 || hd.cursor() != 1 {
		t.Fatalf("first authenticated control deadlocked behind later SessionStart: %d %d %v", applied, skipped, err)
	}
	current, ok := d.core.Get(meta.ID)
	saved, saveErr := store.LoadExecution(meta.ID)
	if !ok || current.ConversationID != envelope.Worker.NativeSessionID || saveErr != nil || saved.Claude == nil || saved.Claude.Sequence != 1 {
		t.Fatalf("identity/fold not durable before cursor: meta=%+v record=%+v err=%v", current, saved, saveErr)
	}
}
func TestClaudeNativeControlFirstIdentityRequiresExactAuthority(t *testing.T) {
	for _, kind := range []string{"token", "descriptor", "shim", "no-store"} {
		t.Run(kind, func(t *testing.T) {
			d, meta, _, hd, envelope := claudeControlExecutionFixture(t)
			switch kind {
			case "token":
				envelope.Token = strings.Repeat("f", 64)
			case "descriptor":
				envelope.DescriptorGeneration = strings.Repeat("e", 32)
			case "shim":
				envelope.ShimPID++
			case "no-store":
				d.executionStore = nil
			}
			raw, _ := json.Marshal(envelope)
			_, _, err := hd.applyLocked(shim.HookDrainResponse{Records: []shim.HookRecord{{Seq: 1, Body: raw}}}, 0)
			if err == nil || hd.cursor() != 0 {
				t.Fatal("unqualified first control advanced", err)
			}
			current, _ := d.core.Get(meta.ID)
			if current.ConversationID != "" {
				t.Fatal("unqualified first control bound native identity")
			}
		})
	}
}

func TestClaudeNativeControlOrdinaryDuplicateSettlesPendingACK(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			d, meta, store, _, _ := claudeControlExecutionFixture(t)
			callback := engine.Callback{SessionID: meta.ID, Token: strings.Repeat("a", 64), Sequence: 1, Event: "PreToolUse", Payload: map[string]string{"tool_name": "Read"}}
			if err := d.eng.HandleCallback(callback); err != nil {
				t.Fatal("pending fixture not captured", err)
			}
			saved, err := store.LoadExecution(meta.ID)
			if err != nil || len(saved.PendingCallbacks) != 1 {
				t.Fatal("pending fixture not durable", err)
			}
			if err := d.markHookSeqIngested(meta.ID, 1); err != nil {
				t.Fatal(err)
			}
			calls := 0
			config := engine.Config{StoreExecution: func(record persist.ExecutionRecord) error {
				calls++
				if fail {
					return syscall.ENOSPC
				}
				return store.SaveExecution(record)
			}}
			d.eng = engine.New(config)
			d.hookSeq = nil
			if err := d.eng.RegisterExecutionSession(meta.ID, strings.Repeat("a", 64), 123, nil, status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone}, engine.ExecutionIdentity{Incarnation: executionIncarnation(meta), ClaudeNativeControl: true}, &saved); err != nil {
				t.Fatal(err)
			}
			hd := NewHookDrainer(d, meta.ID, "", filepath.Join(d.stateDir, meta.ID, "duplicate-fold"))
			raw, _ := json.Marshal(callback)
			applied, skipped, err := hd.applyLocked(shim.HookDrainResponse{Records: []shim.HookRecord{{Seq: 1, Body: raw}}}, 0)
			if calls != 1 {
				t.Errorf("ordinary duplicate bypassed durable pending ACK: writes=%d", calls)
			}
			if fail {
				if !errors.Is(err, syscall.ENOSPC) || hd.cursor() != 0 {
					t.Fatalf("failed pending ACK advanced cursor: applied=%d skipped=%d cursor=%d err=%v", applied, skipped, hd.cursor(), err)
				}
			} else {
				if err != nil || applied != 1 || skipped != 0 || hd.cursor() != 1 {
					t.Fatal("ordinary duplicate not folded once", applied, skipped, err)
				}
				after, err := store.LoadExecution(meta.ID)
				if err != nil || len(after.PendingCallbacks) != 0 {
					t.Fatal("ordinary duplicate stranded durable pending custody", err)
				}
			}
		})
	}
}

func TestClaudeNativeControlBrokerSpoolFoldLeasePipeline(t *testing.T) {
	d, meta, store, _, _ := claudeControlExecutionFixture(t)
	emitted := make(chan status.Status, 16)
	d.eng = engine.New(engine.Config{StoreExecution: store.SaveExecution, Emit: func(_ string, s status.Status) { emitted <- s }})
	if err := d.eng.RegisterExecutionSession(meta.ID, strings.Repeat("a", 64), 123, nil, status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone}, engine.ExecutionIdentity{Incarnation: executionIncarnation(meta), ClaudeNativeControl: true}, nil); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(d.stateDir, meta.ID)
	broker, err := claudeinput.StartNativeControl(dir, meta.ID, "2.1.310", 123, 456, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = broker.Close() }()
	producer, err := shim.OpenHookSpoolProducer(filepath.Join(dir, shim.HookSpoolFile), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = producer.Close() }()
	if err := broker.SetControlSink(func(e claudeinput.ControlEnvelope) (uint64, error) {
		e.Token = strings.Repeat("a", 64)
		raw, _ := json.Marshal(e)
		return producer.Append(raw)
	}); err != nil {
		t.Fatal(err)
	}
	desc := broker.Descriptor()
	hd := NewHookDrainer(d, meta.ID, "", filepath.Join(dir, "pipeline-fold"))
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", desc.SocketPath)
	}}}
	post := func(path string, value any) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(value)
		request, _ := http.NewRequest("POST", "http://localhost"+path, bytes.NewReader(raw))
		request.Header.Set("Authorization", "Bearer "+desc.Token)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		var result map[string]any
		if err := json.NewDecoder(response.Body).Decode(&result); err != nil || response.StatusCode != 200 {
			t.Fatal("private broker exchange failed", response.StatusCode, err)
		}
		return result
	}
	worker := claudeinput.Worker{NativeSessionID: "4a7a2465-d8f0-4c05-a7a9-c44d8077b22b", Generation: "guard", Version: "2.1.310"}
	hd.refreshClaudeLease() // The existing loop callback can run before any guard/control event.
	initial, _ := d.eng.ExecutionSnapshot(meta.ID)
	if initial.Claude.Failure != "" {
		t.Error("pre-bootstrap no-event view permanently poisoned epoch", initial.Claude.Failure)
	}
	first := post("/guard", map[string]any{"generation": desc.Generation, "worker": worker, "captured_sequence": 0, "queued": 0, "lost": false})
	hd.refreshClaudeLease()
	initial, _ = d.eng.ExecutionSnapshot(meta.ID)
	if initial.Claude.Failure != "" {
		t.Error("zero-sequence registered guard poisoned epoch", initial.Claude.Failure)
	}

	post("/control", map[string]any{"generation": desc.Generation, "worker": worker, "sequence": 1, "invocation": 1, "event": "turn.step", "phase": "entered", "context": map[string]any{"turn_id": "native-turn", "index": 0, "message_count": 1, "origin": map[string]string{"plugin": "engine", "tier": "core"}}, "data": map[string]any{}})
	hd.SetSpoolPath(filepath.Join(dir, shim.HookSpoolFile))
	applied, skipped, err := hd.drainFromSpoolFile()
	if err != nil || applied != 1 || skipped != 0 {
		t.Fatal("durable pipeline fold", applied, skipped, err)
	}
	post("/guard", map[string]any{"generation": desc.Generation, "worker": worker, "nonce": first["nonce"], "captured_sequence": 1, "queued": 0, "lost": false})
	hd.refreshClaudeLease()
	select {
	case got := <-emitted:
		if got.Turn != status.TurnActive {
			t.Fatal("exact lease did not expose active native step", got)
		}
	case <-time.After(time.Second):
		t.Fatal("no live projection from existing broker view")
	}
	broker.Revoke()
	hd.refreshClaudeLease()
	select {
	case got := <-emitted:
		if got.Turn != status.TurnUnknown {
			t.Fatal("revoked broker left live Working", got)
		}
	case <-time.After(time.Second):
		t.Fatal("no source-loss withdrawal")
	}
	saved, err := store.LoadExecution(meta.ID)
	if err != nil || saved.Claude.Failure == "" || !saved.UnknownActors || saved.Incomplete != persist.ClaudeObservationSubset || saved.Claude.ObservationComplete {
		t.Fatal("source loss did not durably retain unknown scope", saved.Claude, err)
	}
	if !d.executionCustodyUnsafe(meta.ID) {
		t.Fatal("positive native step/lease authorized automatic effects")
	}
}

func TestClaudeNativeControlReconcilePublishesUnknownBeforeIngress(t *testing.T) {
	d, meta, store, _, _ := claudeControlExecutionFixture(t)
	if err := d.core.SetStatus(meta.ID, status.Status{Process: status.ProcessRunning, Turn: status.TurnActive, Interaction: status.InteractionNone}); err != nil {
		t.Fatal(err)
	}
	d.eng = engine.New(engine.Config{StoreExecution: store.SaveExecution, Emit: func(id string, current status.Status) {
		if err := d.core.SetStatus(id, current); err != nil {
			t.Error(err)
		}
	}})
	if err := d.eng.RegisterExecutionSession(meta.ID, strings.Repeat("a", 64), 123, nil, status.Status{Process: status.ProcessRunning, Turn: status.TurnActive, Interaction: status.InteractionNone}, engine.ExecutionIdentity{Incarnation: executionIncarnation(meta), ClaudeNativeControl: true}, nil); err != nil {
		t.Fatal(err)
	}
	d.publishExecutionStatusesForRunning()
	current, _ := d.core.Get(meta.ID)
	if current.Status.Turn != status.TurnUnknown {
		t.Fatal("pre-ingress roster retained stale persisted Working", current.Status)
	}
}

func TestClaudeNativeControlCrashAfterExecutionSyncBeforeCursor(t *testing.T) {
	d, meta, store, hd, envelope := claudeControlExecutionFixture(t)
	raw, _ := json.Marshal(envelope)
	original := persistHookDrainCursor
	persistHookDrainCursor = func(string, uint64) error { return syscall.ENOSPC }
	_, _, err := hd.applyLocked(shim.HookDrainResponse{Records: []shim.HookRecord{{Seq: 1, Body: raw}}}, 0)
	persistHookDrainCursor = original
	if !errors.Is(err, syscall.ENOSPC) || hd.cursor() != 0 {
		t.Fatal("cursor failure not held", err)
	}
	saved, err := store.LoadExecution(meta.ID)
	if err != nil || saved.Claude.Sequence != 1 {
		t.Fatal("execution was not durable before cursor write", err)
	}
	current, _ := d.core.Get(meta.ID)
	d.eng = engine.New(engine.Config{StoreExecution: store.SaveExecution})
	if err := d.eng.RegisterExecutionSession(meta.ID, strings.Repeat("a", 64), 123, nil, status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone}, engine.ExecutionIdentity{Incarnation: executionIncarnation(current), ConversationID: current.ConversationID, ClaudeNativeControl: true}, &saved); err != nil {
		t.Fatal(err)
	}
	replay := NewHookDrainer(d, meta.ID, "", hd.cursorPath)
	applied, skipped, err := replay.applyLocked(shim.HookDrainResponse{Records: []shim.HookRecord{{Seq: 1, Body: raw}}}, 0)
	if err != nil || applied != 1 || skipped != 0 || replay.cursor() != 1 {
		t.Fatal("durable exact replay could not settle outer cursor", applied, skipped, err)
	}
	after, err := store.LoadExecution(meta.ID)
	if err != nil || len(after.Claude.Invocations) != 1 || len(after.Claude.Turns) != 1 || after.Claude.Sequence != 1 {
		t.Fatal("crash replay duplicated native fact", after.Claude, err)
	}
}
