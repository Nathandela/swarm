package skeleton

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter/codex"
	"github.com/Nathandela/swarm/internal/appserver"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
)

type ownerFenceUpstream struct {
	*fakeUpstream
	order []string
}

func (u *ownerFenceUpstream) Input(p []byte) error { u.order = append(u.order, "input"); return nil }
func (u *ownerFenceUpstream) Submit(string) error  { u.order = append(u.order, "submit"); return nil }
func (u *ownerFenceUpstream) ControlInput([]byte) error {
	u.order = append(u.order, "control")
	return nil
}

type ownerFencePasteUpstream struct {
	*ownerFenceUpstream
	pasteErr error
}

func (u *ownerFencePasteUpstream) PasteInput([]byte) error {
	u.order = append(u.order, "paste")
	return u.pasteErr
}

func TestExecutionOwnerFencePrecedesPasteAndFallback(t *testing.T) {
	for _, mode := range []string{"paste", "unsupported", "absent"} {
		for _, refuse := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/refuse=%v", mode, refuse), func(t *testing.T) {
				base := &ownerFenceUpstream{fakeUpstream: newFakeUpstream(mustSnap(t, ""))}
				var upstream protocol.SessionStream = base
				if mode != "absent" {
					paste := &ownerFencePasteUpstream{ownerFenceUpstream: base}
					if mode == "unsupported" {
						paste.pasteErr = protocol.ErrPasteInputUnsupported
					}
					upstream = paste
				}
				binding := launchGateIdentity{"owned", 123, 456}
				mgr := newTapManager(func(string) (protocol.SessionStream, error) { return upstream, nil })
				mgr.gateIdentity = func(string) (launchGateIdentity, bool) { return binding, true }
				mgr.foreignWrite = func(id string, actual launchGateIdentity) {
					if id != "s1" || actual != binding {
						t.Fatal("paste used another launch binding")
					}
					base.order = append(base.order, "foreign")
				}
				mgr.beforeWrite = func(id string) error {
					if id != "s1" {
						t.Fatal("paste used another owner identity")
					}
					base.order = append(base.order, "fence")
					if refuse {
						return errors.New("current owner revision refused")
					}
					return nil
				}
				rw, err := mgr.subscribe("s1", readWrite)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = rw.Close() })
				ro, err := mgr.subscribe("s1", readOnly)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = ro.Close() })
				if err := ro.PasteInput([]byte("observe")); err != nil || len(base.order) != 0 {
					t.Fatal("read-only paste advanced custody or wrote")
				}
				err = rw.PasteInput([]byte("message"))
				want := []string{"foreign", "fence"}
				if !refuse {
					if mode != "absent" {
						want = append(want, "paste")
					}
					if mode != "paste" {
						want = append(want, "input")
					}
				}
				if (err != nil) != refuse || !reflect.DeepEqual(base.order, want) {
					t.Fatalf("paste escaped owner fence or fenced fallback twice: err=%v order=%v want=%v", err, base.order, want)
				}
			})
		}
	}
}

func TestExecutionOwnerPasteUncertainErrorNeverFallsBack(t *testing.T) {
	base := &ownerFenceUpstream{fakeUpstream: newFakeUpstream(mustSnap(t, ""))}
	uncertain := errors.New("paste write outcome unknown")
	up := &ownerFencePasteUpstream{ownerFenceUpstream: base, pasteErr: uncertain}
	mgr := newTapManager(func(string) (protocol.SessionStream, error) { return up, nil })
	mgr.foreignWrite = func(string, launchGateIdentity) { base.order = append(base.order, "foreign") }
	mgr.beforeWrite = func(string) error { base.order = append(base.order, "fence"); return nil }
	rw, err := mgr.subscribe("s1", readWrite)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rw.Close() })
	if err := rw.PasteInput([]byte("message")); !errors.Is(err, uncertain) {
		t.Fatalf("uncertain write error changed: %v", err)
	}
	if !reflect.DeepEqual(base.order, []string{"foreign", "fence", "paste"}) {
		t.Fatalf("uncertain paste was retried or lacked owner fences: %v", base.order)
	}
}

func TestExecutionOwnerFencePrecedesEveryTapWrite(t *testing.T) {
	for _, verb := range []string{"input", "submit", "control"} {
		t.Run(verb, func(t *testing.T) {
			up := &ownerFenceUpstream{fakeUpstream: newFakeUpstream(mustSnap(t, ""))}
			mgr := newTapManager(func(string) (protocol.SessionStream, error) { return up, nil })
			var refuse bool
			mgr.beforeWrite = func(id string) error {
				if id != "s1" {
					t.Fatalf("wrong input identity %q", id)
				}
				up.order = append(up.order, "fence")
				if refuse {
					return errors.New("durable owner revision failed")
				}
				return nil
			}
			rw, err := mgr.subscribe("s1", readWrite)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rw.Close() }()
			ro, err := mgr.subscribe("s1", readOnly)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = ro.Close() }()
			write := func(s protocol.SessionStream) error {
				switch verb {
				case "input":
					return s.Input([]byte("x"))
				case "submit":
					return s.(protocol.MessageSubmitter).Submit("x")
				default:
					return s.(*tapSub).ControlKeys([]byte("x"))
				}
			}
			if err = write(ro); err != nil {
				t.Fatal(err)
			}
			if len(up.order) != 0 {
				t.Fatal("read-only observer advanced owner revision")
			}
			if err = rw.Resize(80, 24); err != nil {
				t.Fatal(err)
			}
			if len(up.order) != 0 {
				t.Fatal("resize advanced owner revision")
			}
			if err = write(rw); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(up.order, []string{"fence", verb}) {
				t.Fatalf("input crossed fence: %v", up.order)
			}
			up.order = nil
			refuse = true
			if err = write(rw); err == nil {
				t.Fatal("failed owner fence allowed write")
			}
			if !reflect.DeepEqual(up.order, []string{"fence"}) {
				t.Fatalf("bytes escaped refused revision: %v", up.order)
			}
		})
	}
}

type ownerFenceBackend struct {
	*backendWriteBoundaryRecorder
	beforeEffect func()
}

func (b *ownerFenceBackend) CallAtWriteBoundary(ctx context.Context, method string, params, out any, beforeWrite func() error, afterWrite func()) error {
	return b.backendWriteBoundaryRecorder.CallAtWriteBoundary(ctx, method, params, out, func() error {
		if err := beforeWrite(); err != nil {
			return err
		}
		b.beforeEffect()
		return nil
	}, afterWrite)
}

func TestExecutionOwnerNativeComposerFencesBeforeRPC(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		t.Run(fmt.Sprint(refuse), func(t *testing.T) {
			r := newCodexExecutionRig(t, resumeIdentityCodexID, true)
			base := newR7FakeBackend()
			base.reply["turn/start"] = json.RawMessage(`{"turn":{"id":"native-next-turn"}}`)
			writes := 0
			backend := &ownerFenceBackend{backendWriteBoundaryRecorder: &backendWriteBoundaryRecorder{base: base}, beforeEffect: func() {
				writes++
				saved, err := r.store.LoadExecution(r.local)
				if err != nil {
					t.Fatal(err)
				}
				if saved.OwnerRevision != 1 {
					t.Fatalf("native RPC preceded durable owner fence: %+v", saved)
				}
			}}
			if refuse {
				r.saveMu.Lock()
				r.saveHook = func(persist.ExecutionRecord) error { return errors.New("durable owner revision refused") }
				r.saveMu.Unlock()
			}
			lane := r.d.composerLaneFor(r.local)
			_, err := r.d.deliverComposerText(r.local, lane, 0, messageSink{backend: &sessionBackend{threadID: resumeIdentityCodexID, conn: backend}}, false, "", "synthetic owner input", "client-ref", nil)
			if (err != nil) != refuse {
				t.Fatalf("native owner effect err=%v", err)
			}
			if writes != map[bool]int{false: 1, true: 0}[refuse] {
				t.Fatalf("native writes=%d after owner fence", writes)
			}
		})
	}
}

func TestExecutionOwnerRevisionSurvivesExactSessionRegistration(t *testing.T) {
	r := newCodexExecutionRig(t, resumeIdentityCodexID, true)
	r.enqueue(t, "turn/started", map[string]any{"threadId": resumeIdentityCodexID, "turn": map[string]any{"id": "native-turn", "status": "inProgress"}})
	r.drain(t)
	if err := r.d.observeExecutionOwnerWrite(r.local); err != nil {
		t.Fatal(err)
	}
	saved, err := r.store.LoadExecution(r.local)
	if err != nil {
		t.Fatal(err)
	}
	r.d.registerExecutionSession(r.meta, "", codex.New().SignalSources(), false)
	restored, err := r.d.eng.ExecutionSnapshot(r.local)
	if err != nil {
		t.Fatal(err)
	}
	if restored.OwnerRevision != saved.OwnerRevision || restored.Codex == nil || !restored.Codex.UnknownCustody {
		t.Fatalf("exact restart lost revision/custody: before=%+v after=%+v", saved, restored)
	}
}

type codexHostReceiptBackend struct {
	*codexExecutionTestConn
	beforeEffect func()
	beforeReply  func()
	result       json.RawMessage
	failure      error
	reply        func(string, any) error
}

func (b *codexHostReceiptBackend) Call(ctx context.Context, method string, params, out any) error {
	if method == "turn/interrupt" {
		return nil
	}
	return b.codexExecutionTestConn.Call(ctx, method, params, out)
}

func (b *codexHostReceiptBackend) CallAtWriteBoundary(_ context.Context, method string, _, out any, before func() error, after func()) error {
	if err := before(); err != nil {
		return err
	}
	if b.beforeEffect != nil {
		b.beforeEffect()
	}
	after()
	if b.beforeReply != nil {
		b.beforeReply()
	}
	if b.failure != nil {
		return b.failure
	}
	if b.reply != nil {
		return b.reply(method, out)
	}
	return json.Unmarshal(b.result, out)
}

func qualifiedHostBackend(t *testing.T) (*codexExecutionRig, *sessionBackend, *codexHostReceiptBackend) {
	t.Helper()
	r := newCodexExecutionRig(t, resumeIdentityCodexID, true)
	r.enqueue(t, "turn/started", map[string]any{"threadId": resumeIdentityCodexID, "turn": map[string]any{"id": "old", "status": "inProgress"}})
	r.drain(t)
	b := &codexHostReceiptBackend{codexExecutionTestConn: r.conn, result: json.RawMessage(`{"turnId":"old"}`)}
	r.d.backend.mu.Lock()
	registered := r.d.backend.live[r.local]
	registered.conn = b
	r.d.backend.mu.Unlock()
	return r, registered, b
}

func TestCodexHostComposerBlockedReplyReleasesStopAndBackendGuard(t *testing.T) {
	r, registered, backend := qualifiedHostBackend(t)
	written, release := make(chan struct{}), make(chan struct{})
	backend.beforeReply = func() { close(written); <-release }
	lane := r.d.composerLaneFor(r.local)
	r.d.withComposerInteractionState(lane, func() {
		r.d.initInteractionsLocked()
		r.d.turnIDs[r.local], r.d.nativeTurns[r.local] = "host-turn", "old"
	})
	done := make(chan error, 1)
	go func() {
		_, err := r.d.deliverComposerText(r.local, lane, 0, messageSink{backend: registered}, true, "old", "input", "client", nil)
		done <- err
	}()
	select {
	case <-written:
	case <-time.After(5 * time.Second):
		t.Fatal("native write not reached")
	}
	stop := make(chan error, 1)
	go func() {
		_, err := r.d.interruptTurn("devA", "stop-op", protocol.TurnInterruptReq{Session: protocol.NamespacedID("devA", r.local), ExpectedTurn: "host-turn"})
		stop <- err
	}()
	var stopErr error
	blocked := false
	select {
	case stopErr = <-stop:
	case <-time.After(time.Second):
		blocked = true
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("composer did not finish")
	}
	if blocked {
		select {
		case <-stop:
		case <-time.After(5 * time.Second):
			t.Fatal("Stop remained blocked")
		}
		t.Fatal("Stop waited for provider reply")
	}
	if stopErr != nil {
		t.Fatal(stopErr)
	}
	saved, err := r.store.LoadExecution(r.local)
	if err != nil || len(saved.Codex.HostInputs) != 1 || !saved.Codex.UnknownCustody {
		t.Fatalf("Stop or missing echo lost conservative custody: %+v %v", saved.Codex, err)
	}
}

func TestCodexHostComposerAmbiguousRepliesKeepExactClaim(t *testing.T) {
	for _, scenario := range []string{"missing", "foreign", "transport", "retired"} {
		t.Run(scenario, func(t *testing.T) {
			r, registered, backend := qualifiedHostBackend(t)
			switch scenario {
			case "missing":
				backend.result = json.RawMessage(`{}`)
			case "foreign":
				backend.result = json.RawMessage(`{"turnId":"other"}`)
			case "transport":
				backend.failure = context.DeadlineExceeded
			case "retired":
				backend.beforeReply = func() { r.feed.retired.Store(true) }
			}
			lane := r.d.composerLaneFor(r.local)
			_, err := r.d.deliverComposerText(r.local, lane, 0, messageSink{backend: registered}, true, "old", "input", "client", nil)
			if !errors.Is(err, protocol.ErrComposerOutcomeUnknown) {
				t.Fatalf("ambiguous reply: %v", err)
			}
			saved, err := r.store.LoadExecution(r.local)
			if err != nil || len(saved.Codex.HostInputs) != 1 || saved.OwnerRevision != 1 || saved.Codex.HostInputs["client"].AcceptedTurnID != "" {
				t.Fatalf("ambiguous dispatch lost exact custody: %+v %v", saved.Codex, err)
			}
		})
	}
}

func TestCodexHostComposerNoActiveFallbackUsesFreshClaim(t *testing.T) {
	r, registered, backend := qualifiedHostBackend(t)
	calls := 0
	backend.reply = func(method string, out any) error {
		calls++
		if method == "turn/steer" {
			return &appserver.RPCError{Code: -32600, Message: "no active turn to steer"}
		}
		if method != "turn/start" {
			t.Fatalf("unexpected retry method %q", method)
		}
		saved, err := r.store.LoadExecution(r.local)
		if err != nil || saved.OwnerRevision != 2 || len(saved.Codex.HostInputs) != 1 || saved.Codex.HostInputs["client"].Claim.ExpectedTurnID != "" {
			t.Fatalf("retry reused refused claim: %+v %v", saved.Codex, err)
		}
		return json.Unmarshal([]byte(`{"turn":{"id":"next"}}`), out)
	}
	lane := r.d.composerLaneFor(r.local)
	_, err := r.d.deliverComposerText(r.local, lane, 0, messageSink{backend: registered}, true, "old", "input", "client", nil)
	if err != nil || calls != 2 {
		t.Fatalf("definitive refusal fallback: calls=%d err=%v", calls, err)
	}
}

func TestCodexHostComposerReplacementDuringPersistKeepsClaimWithoutBlockingRouting(t *testing.T) {
	r, registered, _ := qualifiedHostBackend(t)
	persisting, release := make(chan struct{}), make(chan struct{})
	r.saveMu.Lock()
	r.saveHook = func(record persist.ExecutionRecord) error {
		if record.Codex.HostInputs["client"].AcceptedTurnID != "" && !record.Codex.UnknownCustody {
			close(persisting)
			<-release
		}
		return nil
	}
	r.saveMu.Unlock()
	lane := r.d.composerLaneFor(r.local)
	done := make(chan error, 1)
	go func() {
		_, err := r.d.deliverComposerText(r.local, lane, 0, messageSink{backend: registered}, true, "old", "input", "client", nil)
		done <- err
	}()
	select {
	case <-persisting:
	case <-time.After(5 * time.Second):
		t.Fatal("receipt persistence not reached")
	}
	replaced := make(chan struct{})
	go func() {
		r.d.backend.mu.Lock()
		r.d.backend.live[r.local] = &sessionBackend{threadID: registered.threadID, conn: registered.conn, sessionInstance: registered.sessionInstance, feed: registered.feed}
		r.d.backend.mu.Unlock()
		close(replaced)
	}()
	blocked := false
	select {
	case <-replaced:
	case <-time.After(time.Second):
		blocked = true
	}
	close(release)
	select {
	case err := <-done:
		if !errors.Is(err, protocol.ErrComposerOutcomeUnknown) {
			t.Fatalf("changed backend receipt: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("composer remained blocked")
	}
	if blocked {
		<-replaced
		t.Fatal("receipt fsync held global backend routing lock")
	}
	saved, err := r.store.LoadExecution(r.local)
	if err != nil || len(saved.Codex.HostInputs) != 1 || !saved.Codex.UnknownCustody {
		t.Fatalf("replacement lost durable conservative claim: %+v %v", saved.Codex, err)
	}
}

func TestCodexHostReceiptPhysicalCheckDoesNotLockIngressState(t *testing.T) {
	r, registered, _ := qualifiedHostBackend(t)
	_, current := r.d.codexHostInputConnection(r.local, registered)
	if current == nil {
		t.Fatal("qualified connection not captured")
	}
	r.feed.codexExecution.mu.Lock()
	done := make(chan bool, 1)
	go func() { done <- current() }()
	blocked := false
	var valid bool
	select {
	case valid = <-done:
	case <-time.After(time.Second):
		blocked = true
	}
	r.feed.codexExecution.mu.Unlock()
	if blocked {
		<-done
		t.Fatal("physical receipt check locked ingress state while Engine owns emitMu")
	}
	if !valid {
		t.Fatal("exact captured connection refused")
	}
}
func TestCodexHostComposerJoinsCurrentNativeReceipt(t *testing.T) {
	for _, echoFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(echoFirst), func(t *testing.T) {
			r := newCodexExecutionRig(t, resumeIdentityCodexID, true)
			r.enqueue(t, "turn/started", map[string]any{"threadId": resumeIdentityCodexID, "turn": map[string]any{"id": "old", "status": "inProgress"}})
			r.enqueue(t, "turn/completed", map[string]any{"threadId": resumeIdentityCodexID, "turn": map[string]any{"id": "old", "status": "completed"}})
			r.drain(t)
			backend := &codexHostReceiptBackend{codexExecutionTestConn: r.conn, result: json.RawMessage(`{"turn":{"id":"next"}}`)}
			r.d.backend.mu.Lock()
			registered := r.d.backend.live[r.local]
			registered.conn = backend
			r.d.backend.mu.Unlock()
			backend.beforeEffect = func() {
				saved, err := r.store.LoadExecution(r.local)
				if err != nil || len(saved.Codex.HostInputs) != 1 || saved.OwnerRevision != 1 {
					t.Fatalf("RPC preceded durable exact host claim: %+v %v", saved.Codex, err)
				}
			}
			echo := func() {
				r.enqueue(t, "turn/started", map[string]any{"threadId": resumeIdentityCodexID, "turn": map[string]any{"id": "next", "status": "inProgress"}})
				for _, method := range []string{"item/started", "item/completed"} {
					r.enqueue(t, method, map[string]any{"threadId": resumeIdentityCodexID, "turnId": "next", "item": map[string]any{"type": "userMessage", "id": "native-user", "clientId": "host-client"}})
				}
				r.drain(t)
			}
			if echoFirst {
				backend.beforeReply = echo
			}
			lane := r.d.composerLaneFor(r.local)
			turn, err := r.d.deliverComposerText(r.local, lane, 0, messageSink{backend: registered}, false, "", "synthetic input", "host-client", nil)
			if err != nil || turn != "next" {
				t.Fatalf("host RPC err=%v turn=%q", err, turn)
			}
			if !echoFirst {
				echo()
			}
			saved, err := r.store.LoadExecution(r.local)
			if err != nil || len(saved.Codex.HostInputs) != 0 || saved.Codex.OwnerRevision != 1 || saved.Codex.UnknownCustody {
				t.Fatalf("host receipt did not join: %+v %v", saved.Codex, err)
			}
		})
	}
}
