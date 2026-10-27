package protocol

import (
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

type constructorEventBackend struct {
	*stubDaemon
	called chan struct{}
	once   sync.Once
}

func (d *constructorEventBackend) Events() <-chan persist.Meta {
	d.once.Do(func() { close(d.called) })
	return d.stubDaemon.Events()
}

func TestServerAssemblyStartsWorkersAfterConfiguration(t *testing.T) {
	backend := &constructorEventBackend{stubDaemon: newStubDaemon(), called: make(chan struct{})}
	server := newServer(backend)
	defer func() { _ = server.Close() }()
	select {
	case <-backend.called:
		t.Fatal("server fanout ran before constructor configuration")
	case <-time.After(20 * time.Millisecond):
	}
	server.endpointID = "stable-endpoint"
	server.remoteTier = true
	cc := &clientConn{endpointID: "fallback", eventQ: make(chan []byte, 1), done: make(chan struct{})}
	server.subMu.Lock()
	server.subs[cc] = struct{}{}
	server.subMu.Unlock()
	server.startWorkers()
	backend.events <- statusMeta("seed", status.TurnIdle, status.InteractionNone)
	select {
	case body := <-cc.eventQ:
		event, err := DecodeControl(body)
		if err != nil || event.EndpointID != "stable-endpoint" {
			t.Fatalf("first fanout endpoint=%q err=%v", event.EndpointID, err)
		}
	case <-time.After(time.Second):
		t.Fatal("configured server fanout did not start")
	}
}

type constructorJournalBackend struct {
	*journalStub
	subscriptions int
	canceled      chan struct{}
	stopped       chan struct{}
}

func (d *constructorJournalBackend) JournalSubscribe() (<-chan JournalRecord, func()) {
	d.subscriptions++
	return d.source, func() { close(d.canceled) }
}
func (d *constructorJournalBackend) stopEvents() { close(d.stopped) }

func TestUnstartedServerCloseDoesNotOwnASubscription(t *testing.T) {
	backend := &constructorJournalBackend{journalStub: newJournalStub(), canceled: make(chan struct{}), stopped: make(chan struct{})}
	server := newServer(backend)
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if backend.subscriptions != 0 {
		t.Fatal("unstarted server acquired a journal subscription")
	}
	select {
	case <-backend.stopped:
	default:
		t.Fatal("unstarted close did not stop the backend source")
	}
}

func TestNewServerCloseCancelsItsStartupSubscription(t *testing.T) {
	backend := &constructorJournalBackend{journalStub: newJournalStub(), canceled: make(chan struct{}), stopped: make(chan struct{})}
	server := NewServer(backend, "stable")
	t.Cleanup(func() { _ = server.Close() })
	if backend.subscriptions != 1 {
		t.Fatal("public constructor omitted journal fanout startup")
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-backend.canceled:
	default:
		t.Fatal("server close retained its startup subscription")
	}
	select {
	case <-backend.stopped:
	default:
		t.Fatal("server close did not stop the backend source")
	}
}
