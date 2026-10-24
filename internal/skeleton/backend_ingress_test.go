package skeleton

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter/claude"
	"github.com/Nathandela/swarm/internal/appserver"
	"github.com/Nathandela/swarm/internal/contextguard"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/coder/websocket"
)

func TestBackendIngressPreservesFIFOAndTracksInFlight(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	processed := make(chan string, 4)
	q := newBackendIngress(func(f backendIngressFrame) {
		if f.method == "one" {
			close(entered)
			<-release
		}
		processed <- f.method
	}, func() bool {
		t.Error("unexpected loss")
		return true
	}, func() bool { return true }, nil, nil)
	defer q.stop()
	q.enqueue("one", []byte(`{}`), time.Now())
	<-entered
	for _, method := range []string{"two", "three", "four"} {
		if !q.enqueue(method, []byte(`{}`), time.Now()) {
			t.Fatal("ordinary frame refused")
		}
	}
	if q.automationSafe() {
		t.Fatal("in-flight processor appeared caught up")
	}
	close(release)
	for _, expected := range []string{"one", "two", "three", "four"} {
		select {
		case got := <-processed:
			if got != expected {
				t.Fatalf("frame order %q, want %q", got, expected)
			}
		case <-time.After(time.Second):
			t.Fatal("ingress stalled")
		}
	}
	waitBackendIngress(t, func() bool { return q.automationSafe() })
	q.stop()
	select {
	case <-q.done:
	case <-time.After(time.Second):
		t.Fatal("idle ingress did not exit")
	}
}

func TestBackendIngressOverflowImmediatelyFencesWhileProcessorBlocked(t *testing.T) {
	for _, kind := range []string{"count", "bytes", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			entered := make(chan struct{})
			release := make(chan struct{})
			loss := make(chan struct{}, 1)
			var processing atomic.Int32
			q := newBackendIngress(func(backendIngressFrame) {
				if processing.Add(1) == 1 {
					close(entered)
					<-release
				}
			}, func() bool {
				loss <- struct{}{}
				return true
			}, func() bool { return true }, nil, nil)
			defer q.stop()
			if kind == "oversize" {
				if q.enqueue("large", make([]byte, backendIngressMaxBytes+1), time.Now()) {
					t.Fatal("oversize frame admitted")
				}
				close(release)
			} else {
				first := []byte(`{}`)
				if kind == "bytes" {
					first = make([]byte, 700<<10)
				}
				q.enqueue("blocked", first, time.Now())
				<-entered
				if kind == "count" {
					for i := 1; i < backendIngressMaxFrames; i++ {
						if !q.enqueue("queued", []byte(`{}`), time.Now()) {
							t.Fatal("queue filled too early")
						}
					}
					if q.enqueue("lost", []byte(`{}`), time.Now()) {
						t.Fatal("count overflow admitted")
					}
				} else {
					if q.enqueue("lost", make([]byte, 400<<10), time.Now()) {
						t.Fatal("aggregate byte overflow admitted")
					}
				}
				if !q.lost.Load() || q.automationSafe() {
					t.Fatal("overflow was not immediately unsafe")
				}
				select {
				case <-loss:
					t.Fatal("loss callback ran concurrently with blocked processor")
				default:
				}
				close(release)
			}
			select {
			case <-loss:
			case <-time.After(time.Second):
				t.Fatal("loss was not applied after processor release")
			}
			waitBackendIngress(t, func() bool { return q.pending.Load() == 0 })
			if q.automationSafe() {
				t.Fatal("drained queue silently recovered from loss")
			}
			q.stop()
			<-q.done
		})
	}
}

func TestBackendIngressRetiredAndCloseDiscardPendingWithoutBlocking(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var current atomic.Bool
	current.Store(true)
	var calls atomic.Int32
	q := newBackendIngress(func(backendIngressFrame) { calls.Add(1); close(entered); <-release }, func() bool {
		return true
	}, current.Load, nil, nil)
	q.enqueue("first", []byte(`{}`), time.Now())
	<-entered
	q.enqueue("second", []byte(`{}`), time.Now())
	current.Store(false)
	q.stop()
	if q.automationSafe() || q.enqueue("late", []byte(`{}`), time.Now()) {
		t.Fatal("closed ingress admitted work")
	}
	select {
	case <-q.done:
		t.Fatal("close claimed a blocked processor was joined")
	default:
	}
	close(release)
	select {
	case <-q.done:
	case <-time.After(time.Second):
		t.Fatal("retired ingress leaked worker")
	}
	if calls.Load() != 1 {
		t.Fatal("retired pending frame processed")
	}
}

func TestBackendIngressBlockedProcessorDoesNotBlockPrivateAuthRPC(t *testing.T) {
	sock := backendAuthTestSocket(t)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	reply := make(chan []byte, 1)
	q := newBackendIngress(func(backendIngressFrame) { close(entered); <-release }, func() bool {
		return true
	}, func() bool { return true }, nil, nil)
	defer func() { close(release); q.stop(); <-q.done }()
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, e := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if e != nil {
			return
		}
		defer func() { _ = ws.CloseNow() }()
		_ = ws.Write(r.Context(), websocket.MessageText, []byte(`{"method":"thread/started","params":{}}`))
		<-entered
		_ = ws.Write(r.Context(), websocket.MessageText, []byte(`{"id":7,"method":"account/chatgptAuthTokens/refresh","params":{"reason":"unauthorized"}}`))
		_, raw, e := ws.Read(r.Context())
		if e == nil {
			reply <- raw
		}
	})}
	go func() { _ = server.Serve(ln) }()
	defer func() { _ = server.Close() }()
	auth := newBackendAuthCoordinator(func(context.Context, bool) (backendAuthTokens, error) {
		return backendAuthTokens{AccessToken: "PRIVATE", AccountID: "account"}, nil
	}, func() bool { return true })
	conn, err := appserver.Dial(context.Background(), sock, appserver.Options{OnFrame: func(method string, raw json.RawMessage) {
		if !auth.intercept(method, raw) {
			q.enqueue(method, raw, time.Now())
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	auth.bind(conn)
	select {
	case raw := <-reply:
		var result struct {
			ID     int `json:"id"`
			Result struct {
				Token string `json:"accessToken"`
			} `json:"result"`
		}
		if json.Unmarshal(raw, &result) != nil || result.ID != 7 || result.Result.Token != "PRIVATE" {
			t.Fatal("private auth response lost behind blocked generic ingress")
		}
	case <-time.After(time.Second):
		t.Fatal("backend reader blocked behind generic processing")
	}
}
func waitBackendIngress(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatal("ingress did not reach expected state")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestBackendIngressContextGuardWaitsForCatchupAtNativeWrite(t *testing.T) {
	r := newDispatchRig(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	q := newBackendIngress(func(backendIngressFrame) { close(entered); <-release }, func() bool {
		return true
	}, func() bool { return true }, nil, func() { r.nudge() })
	defer func() { q.stop(); <-q.done }()
	r.manager.mu.Lock()
	session := r.manager.sessions["session"]
	session.ingressSafe = q.automationSafe
	r.manager.mu.Unlock()
	q.enqueue("generic", []byte(`{}`), time.Now())
	<-entered
	r.ingestUsage(t, 1, 85, 100)
	r.waitCalls(t, 1)
	r.waitPhase(t, contextguard.StatePendingIdle)
	if r.conn.snapshot()[0].wrote {
		t.Fatal("ContextGuard wrote while generic ingress lagged")
	}
	close(release)
	r.waitCalls(t, 2)
	r.waitPhase(t, contextguard.StateAwaitingConfirmation)
	if !r.conn.snapshot()[1].wrote {
		t.Fatal("caught-up edge did not preserve ContextGuard retry")
	}
}

func TestBackendIngressLossInvalidatesExecutionAndGuardBeforeLaterFrames(t *testing.T) {
	manager, _ := contextGuardTestManager(t, true, 80)
	d := &Daemon{contextGuards: manager, eng: engine.New(engine.Config{})}
	d.capStore.instances = map[string]string{"session": "instance"}
	if err := d.eng.RegisterExecutionSession("session", "token", 1, claude.New().SignalSources(), status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone}, engine.ExecutionIdentity{Incarnation: "native", ObservedFromStart: true}, nil); err != nil {
		t.Fatal(err)
	}
	feed := newBackendFeed()
	feed.guardReady = true
	key := contextGuardTestKey("instance", feed.epoch)
	source, action := contextGuardTestSource(t)
	if !manager.register("session", key, source, action) {
		t.Fatal("guard registration failed")
	}
	d.backend.live = map[string]*sessionBackend{"session": {feed: feed, sessionInstance: "instance", threadID: key.ProviderThreadID}}
	entered := make(chan struct{})
	release := make(chan struct{})
	later := make(chan struct{}, 1)
	q := newBackendIngress(func(f backendIngressFrame) {
		if f.method == "first" {
			close(entered)
			<-release
			return
		}
		record, err := d.eng.ExecutionSnapshot("session")
		if err != nil || !record.UnknownActors || record.Incomplete != "observation_gap" {
			t.Error("later frame preceded execution invalidation")
		}
		later <- struct{}{}
	}, func() bool {
		d.invalidateBackendIngress("session", "instance", feed)
		return true
	}, func() bool { return true }, nil, nil)
	feed.ingress = q
	q.enqueue("first", make([]byte, 700<<10), time.Now())
	<-entered
	q.enqueue("later", []byte(`{}`), time.Now())
	if q.enqueue("lost_permission", make([]byte, 400<<10), time.Now()) {
		t.Fatal("overflow was admitted")
	}
	if feed.ingressAutomationSafe() {
		t.Fatal("loss did not immediately fence native effects")
	}
	close(release)
	select {
	case <-later:
	case <-time.After(time.Second):
		t.Fatal("later frame was not retained for UI processing")
	}
	awaitContextGuardView(t, manager, "session", func(v protocol.ContextGuardView) bool { return v.Phase == string(contextguard.StateEventLossHold) })
	q.stop()
	<-q.done
}

func TestBackendIngressReplacementInheritsPendingLoss(t *testing.T) {
	r := newR7R4Rig(t, "")
	instance, _ := r.sk.sessionInstance(r.local)
	entered := make(chan struct{})
	release := make(chan struct{})
	old := newBackendFeed()
	old.ingress = newBackendIngress(func(backendIngressFrame) { close(entered); <-release }, func() bool {
		return true
	}, func() bool { return true }, nil, nil)
	if !r.sk.registerBackendFeedForInstance(r.local, instance, fakeCodexThreadID, newR7FakeBackend(), old) {
		t.Fatal("old registration refused")
	}
	old.ingress.enqueue("pending", []byte(`{}`), time.Now())
	<-entered
	next := newBackendFeed()
	if !r.sk.startBackendIngress(r.local, instance, next) {
		t.Fatal("new ingress refused")
	}
	if !r.sk.registerBackendFeedForInstance(r.local, instance, fakeCodexThreadID, newR7FakeBackend(), next) {
		t.Fatal("replacement registration refused")
	}
	if !next.ingress.lost.Load() || next.ingressAutomationSafe() {
		t.Fatal("replacement silently discarded predecessor evidence")
	}
	close(release)
	<-old.ingress.done
	r.sk.forgetBackend(r.local)
	<-next.ingress.done
}

func TestBackendIngressForeignFeedCannotInvalidateCurrentExecution(t *testing.T) {
	d := &Daemon{eng: engine.New(engine.Config{})}
	d.capStore.instances = map[string]string{"session": "instance"}
	if err := d.eng.RegisterExecutionSession("session", "token", 1, claude.New().SignalSources(), status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone}, engine.ExecutionIdentity{Incarnation: "native", ObservedFromStart: true}, nil); err != nil {
		t.Fatal(err)
	}
	current := newBackendFeed()
	d.backend.live = map[string]*sessionBackend{"session": {feed: current, sessionInstance: "instance"}}
	before, _ := d.eng.ExecutionSnapshot("session")
	foreign := newBackendFeed()
	d.invalidateBackendIngress("session", "instance", foreign)
	current.retired.Store(true)
	d.invalidateBackendIngress("session", "instance", current)
	current.retired.Store(false)
	d.invalidateBackendIngress("session", "obsolete-instance", current)
	after, _ := d.eng.ExecutionSnapshot("session")
	if before.Incomplete != after.Incomplete || before.UnknownActors != after.UnknownActors {
		t.Fatal("foreign/retired callback invalidated current execution")
	}
}

func TestBackendIngressFailedDialsAndDaemonCloseJoinWorkers(t *testing.T) {
	d := &Daemon{backendReady: 20 * time.Millisecond}
	d.capStore.instances = map[string]string{"session": "instance"}
	for i := 0; i < 3; i++ {
		if conn, _, err := d.dialSessionBackend("session", "instance", daemon.BackendChannel{SocketPath: backendAuthTestSocket(t)}); err == nil {
			_ = conn.Close()
			t.Fatal("missing backend dial succeeded")
		}
	}
	waitBackendIngress(t, func() bool {
		d.backendIngressMu.Lock()
		defer d.backendIngressMu.Unlock()
		return len(d.backendIngress) == 0
	})
	feed := newBackendFeed()
	if !d.startBackendIngress("session", "instance", feed) {
		t.Fatal("ingress registration refused")
	}
	sourceClosed := make(chan struct{})
	if !feed.ingress.bindConnection(func() error { close(sourceClosed); return nil }) {
		t.Fatal("source bind refused")
	}
	d.stopBackendIngress()
	select {
	case <-sourceClosed:
	default:
		t.Fatal("daemon close left native reader connected")
	}
	select {
	case <-feed.ingress.done:
	default:
		t.Fatal("daemon close failed to join ingress")
	}
	late := newBackendFeed()
	if d.startBackendIngress("session", "instance", late) {
		t.Fatal("closed daemon admitted a dial worker")
	}
	select {
	case <-late.ingress.done:
	case <-time.After(time.Second):
		t.Fatal("late closed ingress leaked worker")
	}
}

func TestBackendIngressRemovedFeedLossFollowsOnlySameInstanceRejoin(t *testing.T) {
	for _, replaceInstance := range []bool{false, true} {
		t.Run(map[bool]string{false: "same_instance", true: "new_instance"}[replaceInstance], func(t *testing.T) {
			r := newR7R4Rig(t, "")
			instance, _ := r.sk.sessionInstance(r.local)
			old := newBackendFeed()
			if !r.sk.startBackendIngress(r.local, instance, old) {
				t.Fatal("ingress refused")
			}
			if !r.sk.registerBackendFeedForInstance(r.local, instance, fakeCodexThreadID, newR7FakeBackend(), old) {
				t.Fatal("registration refused")
			}
			old.ingress.markLoss()
			if !r.sk.forgetBackendForFeed(r.local, old.epoch) {
				t.Fatal("feed removal refused")
			}
			<-old.ingress.done
			if replaceInstance {
				instance = mintSessionInstance()
				meta, _ := r.sk.core.Get(r.local)
				if err := r.sk.recordSessionInstance(r.local, instance, meta.ShimPID, meta.ShimStartTime); err != nil {
					t.Fatal(err)
				}
			}
			next := newBackendFeed()
			if !r.sk.startBackendIngress(r.local, instance, next) {
				t.Fatal("rejoin ingress refused")
			}
			if !r.sk.registerBackendFeedForInstance(r.local, instance, fakeCodexThreadID, newR7FakeBackend(), next) {
				t.Fatal("rejoin registration refused")
			}
			if next.ingress.lost.Load() == replaceInstance {
				t.Fatal("loss crossed wrong session incarnation")
			}
			r.sk.backend.mu.Lock()
			retained := len(r.sk.backend.ingressLoss)
			r.sk.backend.mu.Unlock()
			if retained != 0 {
				t.Fatal("consumed loss tombstone retained")
			}
			r.sk.forgetBackend(r.local)
			<-next.ingress.done
		})
	}
}

func TestBackendIngressDirectNewInstanceReplacementDoesNotInheritLoss(t *testing.T) {
	r := newR7R4Rig(t, "")
	instance, _ := r.sk.sessionInstance(r.local)
	old := newBackendFeed()
	if !r.sk.startBackendIngress(r.local, instance, old) {
		t.Fatal("old ingress refused")
	}
	if !r.sk.registerBackendFeedForInstance(r.local, instance, fakeCodexThreadID, newR7FakeBackend(), old) {
		t.Fatal("old registration refused")
	}
	old.ingress.markLoss()
	meta, _ := r.sk.core.Get(r.local)
	nextInstance := mintSessionInstance()
	if err := r.sk.recordSessionInstance(r.local, nextInstance, meta.ShimPID, meta.ShimStartTime); err != nil {
		t.Fatal(err)
	}
	next := newBackendFeed()
	if !r.sk.startBackendIngress(r.local, nextInstance, next) {
		t.Fatal("new ingress refused")
	}
	if !r.sk.registerBackendFeedForInstance(r.local, nextInstance, fakeCodexThreadID, newR7FakeBackend(), next) {
		t.Fatal("new registration refused")
	}
	if next.ingress.lost.Load() {
		t.Fatal("displaced old incarnation contaminated fresh native feed")
	}
	r.sk.forgetBackend(r.local)
	<-next.ingress.done
	<-old.ingress.done
}

func TestBackendIngressPreregisteredLossWaitsForAuthority(t *testing.T) {
	var authoritative atomic.Bool
	checked := make(chan struct{}, 1)
	invalidated := make(chan struct{}, 1)
	q := newBackendIngress(func(backendIngressFrame) {}, func() bool {
		invalidated <- struct{}{}
		return true
	}, func() bool {
		value := authoritative.Load()
		select {
		case checked <- struct{}{}:
		default:
		}
		return value
	}, nil, nil)
	defer func() { q.stop(); <-q.done }()
	q.markLoss()
	select {
	case <-checked:
	case <-time.After(time.Second):
		t.Fatal("loss did not check feed authority")
	}
	authoritative.Store(true)
	q.signal()
	select {
	case <-invalidated:
	case <-time.After(time.Second):
		t.Fatal("preregistered loss was consumed before authoritative installation")
	}
}

func TestBackendIngressPreregisteredOverflowIsAppliedAfterInstall(t *testing.T) {
	r := newR7R4Rig(t, "")
	instance, _ := r.sk.sessionInstance(r.local)
	old := newBackendFeed()
	if !r.sk.registerBackendFeedForInstance(r.local, instance, fakeCodexThreadID, newR7FakeBackend(), old) {
		t.Fatal("old registration refused")
	}
	next := newBackendFeed()
	if !r.sk.startBackendIngress(r.local, instance, next) {
		t.Fatal("next ingress refused")
	}
	checked := make(chan bool, 1)
	current := next.ingress.current
	// No frame/loss has been admitted yet, so the idle worker cannot read this seam.
	next.ingress.current = func() bool {
		value := current()
		select {
		case checked <- value:
		default:
		}
		return value
	}
	if next.ingress.enqueue("oversize", make([]byte, backendIngressMaxBytes+1), time.Now()) {
		t.Fatal("overflow admitted")
	}
	select {
	case authoritative := <-checked:
		if authoritative {
			t.Fatal("preregistered feed borrowed old feed authority")
		}
	case <-time.After(time.Second):
		t.Fatal("loss authority not checked")
	}
	if !r.sk.registerBackendFeedForInstance(r.local, instance, fakeCodexThreadID, newR7FakeBackend(), next) {
		t.Fatal("next registration refused")
	}
	waitBackendIngress(t, func() bool { next.guardMu.Lock(); defer next.guardMu.Unlock(); return next.guardDropped })
	r.sk.forgetBackend(r.local)
	<-next.ingress.done
}

func TestBackendIngressPersistenceFailureKeepsEffectsFenced(t *testing.T) {
	var fail atomic.Bool
	d := &Daemon{eng: engine.New(engine.Config{StoreExecution: func(persist.ExecutionRecord) error {
		if fail.Load() {
			return errors.New("injected disk failure")
		}
		return nil
	}})}
	d.capStore.instances = map[string]string{"session": "instance"}
	if err := d.eng.RegisterExecutionSession("session", "token", 1, claude.New().SignalSources(), status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone}, engine.ExecutionIdentity{Incarnation: "native", ObservedFromStart: true}, nil); err != nil {
		t.Fatal(err)
	}
	feed := newBackendFeed()
	if !d.startBackendIngress("session", "instance", feed) {
		t.Fatal("ingress refused")
	}
	fail.Store(true)
	feed.ingress.markLoss()
	waitBackendIngress(t, func() bool {
		record, e := d.eng.ExecutionSnapshot("session")
		return e == nil && record.Incomplete == "persistence_failure"
	})
	if feed.ingressAutomationSafe() {
		t.Fatal("failed persistence allowed automatic effects")
	}
	feed.retireIngress()
	<-feed.ingress.done
}
