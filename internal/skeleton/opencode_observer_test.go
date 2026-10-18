package skeleton

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/opencodebridge"
)

type ocReaderFake struct {
	mu            sync.Mutex
	identity      opencodebridge.Identity
	subscribed    chan struct{}
	subscribeOnce sync.Once
	events        chan opencodebridge.Event
	handled       chan struct{}
	snapshot      func(int) opencodebridge.Snapshot
	reads         int
}

func newOCReaderFake() *ocReaderFake {
	f := &ocReaderFake{identity: opencodebridge.Identity{Version: 1, WorkerID: "worker", SessionInstance: "instance", RootID: "ses_root", Directory: "/workspace", BackendGeneration: "generation", NativeCreatedAtMS: 1}, subscribed: make(chan struct{}), events: make(chan opencodebridge.Event, 8), handled: make(chan struct{}, 8)}
	f.snapshot = func(int) opencodebridge.Snapshot { return f.idleSnapshot() }
	return f
}
func (f *ocReaderFake) idleSnapshot() opencodebridge.Snapshot {
	return opencodebridge.Snapshot{Identity: f.identity, Session: json.RawMessage(`{"id":"ses_root","directory":"/workspace","time":{"created":1}}`), Statuses: map[string]json.RawMessage{}, Messages: []json.RawMessage{}, Permissions: []json.RawMessage{}, Questions: []json.RawMessage{}, Children: []json.RawMessage{}}
}
func (f *ocReaderFake) Snapshot(ctx context.Context, _ int, _ string) (opencodebridge.Snapshot, error) {
	select {
	case <-f.subscribed:
	default:
		return opencodebridge.Snapshot{}, errors.New("snapshot before subscription")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	return f.snapshot(f.reads), nil
}
func (f *ocReaderFake) Events(ctx context.Context, handle func(opencodebridge.Event) error) error {
	f.subscribeOnce.Do(func() { close(f.subscribed) })
	raw, _ := json.Marshal(f.identity)
	if err := handle(opencodebridge.Event{Type: "swarm.bridge.connected", Properties: raw}); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event := <-f.events:
			if err := handle(event); err != nil {
				return err
			}
			select {
			case f.handled <- struct{}{}:
			default:
			}
		}
	}
}

func runOCObserverTest(t *testing.T, f *ocReaderFake) (<-chan OpenCodeView, <-chan error, context.CancelFunc) {
	t.Helper()
	state, err := NewOpenCodeState(f.identity.RootID, f.identity.BackendGeneration)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	views := make(chan OpenCodeView, 32)
	ready := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		observeOpenCode(ctx, f, f.identity, state, func(v OpenCodeView) { views <- v }, ready)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("observer did not stop")
		}
	})
	return views, ready, cancel
}

func nextOCView(t *testing.T, views <-chan OpenCodeView, want func(OpenCodeView) bool) OpenCodeView {
	t.Helper()
	for {
		select {
		case v := <-views:
			if want(v) {
				return v
			}
		case <-time.After(3 * time.Second):
			t.Fatal("missing expected native view")
			return OpenCodeView{}
		}
	}
}

func TestOpenCodeObserverSubscribesBeforeSnapshot(t *testing.T) {
	f := newOCReaderFake()
	views, ready, _ := runOCObserverTest(t, f)
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("readiness blocked")
	}
	v := nextOCView(t, views, func(v OpenCodeView) bool { return v.BusyKnown })
	if v.Busy || v.Degraded {
		t.Fatalf("idle snapshot: %+v", v)
	}
}

func TestOpenCodeObserverRefusesForeignSnapshot(t *testing.T) {
	f := newOCReaderFake()
	f.snapshot = func(int) opencodebridge.Snapshot {
		s := f.idleSnapshot()
		s.BackendGeneration = "replacement"
		return s
	}
	views, ready, _ := runOCObserverTest(t, f)
	select {
	case err := <-ready:
		if err == nil {
			t.Fatal("foreign snapshot became ready")
		}
	case <-time.After(time.Second):
		t.Fatal("readiness blocked")
	}
	v := nextOCView(t, views, func(v OpenCodeView) bool { return v.Degraded })
	if v.BusyKnown {
		t.Fatal("foreign snapshot granted status authority")
	}
}

func TestOpenCodeObserverStatusOverlapRefetches(t *testing.T) {
	f := newOCReaderFake()
	f.snapshot = func(n int) opencodebridge.Snapshot {
		if n == 1 {
			f.events <- opencodebridge.Event{Type: "session.status", Properties: json.RawMessage(`{"sessionID":"ses_root","status":{"type":"busy"}}`)}
			<-f.handled
		}
		return f.idleSnapshot()
	}
	views, ready, _ := runOCObserverTest(t, f)
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("readiness blocked")
	}
	v := nextOCView(t, views, func(v OpenCodeView) bool { return v.BusyKnown })
	if v.Busy {
		t.Fatal("buffered older busy overwrote current idle snapshot")
	}
	f.mu.Lock()
	reads := f.reads
	f.mu.Unlock()
	if reads < 2 {
		t.Fatal("overlapping root status was not reconciled")
	}
}

func TestOpenCodeObserverStatusChurnRecoversWithoutAnotherEvent(t *testing.T) {
	f := newOCReaderFake()
	f.snapshot = func(n int) opencodebridge.Snapshot {
		if n <= 3 {
			f.events <- opencodebridge.Event{Type: "session.status", Properties: json.RawMessage(`{"sessionID":"ses_root","status":{"type":"busy"}}`)}
			<-f.handled
		}
		return f.idleSnapshot()
	}
	views, ready, _ := runOCObserverTest(t, f)
	if err := <-ready; err != nil {
		t.Fatal(err)
	}
	v := nextOCView(t, views, func(v OpenCodeView) bool { return v.BusyKnown })
	if v.Busy {
		t.Fatal("quiet root did not reconcile to idle after status churn")
	}
}

func TestOpenCodeObserverGapWithdrawsStatus(t *testing.T) {
	f := newOCReaderFake()
	views, ready, _ := runOCObserverTest(t, f)
	if err := <-ready; err != nil {
		t.Fatal(err)
	}
	nextOCView(t, views, func(v OpenCodeView) bool { return v.BusyKnown })
	f.events <- opencodebridge.Event{Type: "swarm.bridge.gap", Properties: json.RawMessage(`{}`)}
	v := nextOCView(t, views, func(v OpenCodeView) bool { return v.Degraded })
	if v.BusyKnown {
		t.Fatal("stream gap retained status authority")
	}
}
