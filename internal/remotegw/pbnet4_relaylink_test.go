package remotegw

// PB-NET-8 OWNS THIS FILE'S SUBJECT as of 2026-07-31. The tests were written against
// PB-NET-4, whose text says "automatic reconnect" without naming a hop and whose fences are
// the PHONE's; the machine hop had no row at all, which is why its recovery mechanism was
// absent for the whole project (ADR-007 B120/F1). The row now exists and cites this file, so
// the id is named here: a fence nobody can find by grepping the requirement is a fence the
// next audit re-derives from scratch.
//
// PB-NET-4 / ADR-007 section 6.0 ("client reconnect backoff + jitter on BOTH hops"),
// MACHINE half, FAILING FIRST.
//
// THE DEFECT THIS IS WRITTEN AGAINST. The gateway dials the relay ONCE per process
// (cmd/swarm-remote's run calls relay.DialSecure a single time) and NOTHING observes that
// the connection is gone. Service.Run has no relay reconnect -- runJournal reconnects to
// the DAEMON, not the relay -- and CommandBridge.Run treats every relay error as
// transient, retrying the same dead client forever. relay.Client.Done() exists precisely
// to notice a drop without issuing a request and has zero production callers.
//
// So a desktop WiFi blip ends remote control until a human restarts the sidecar: the
// process never exits, so neither launchd's KeepAlive nor systemd's Restart= ever fires,
// the phone reconnects and reports "online", and the machine is silently dead.
//
// THE ASSERTION IS ON Run's RETURN, not on any new symbol: a Service whose only relay
// connection has died must STOP, so the process that owns the dial can redial. Nothing
// here names a sentinel or a constant that does not exist yet, so this test compiles
// against the defect and fails by RUNNING it.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// linkMailbox is a scriptedMailbox that carries the relay client's LIVENESS seam. A real
// *relay.Client closes Done() when its read pump sees the socket die (client.go's
// markDone), and answers every in-flight call with ErrConnClosed from that moment on --
// which is exactly what this fake does, so the Service under test sees what production
// sees when a link drops underneath it.
type linkMailbox struct {
	scriptedMailbox
	done chan struct{}
}

func newLinkMailbox() *linkMailbox { return &linkMailbox{done: make(chan struct{})} }

// Done reports the connection's death. It is the seam a caller can watch while IDLE:
// a loop that only learns of a drop when a request fails cannot notice a link that dies
// with nothing outstanding.
func (m *linkMailbox) Done() <-chan struct{} { return m.done }

// MailboxWait parks like the real bounded server-side wait, then reports the connection
// closed once the link dies -- so the command loop is in exactly the state production is
// in: erroring, backing off, and retrying a client that will never answer again.
func (m *linkMailbox) MailboxWait(ctx context.Context, _ uint64) ([]relay.Item, bool, error) {
	select {
	case <-ctx.Done():
		return nil, false, ctx.Err()
	case <-m.done:
		return nil, false, relay.ErrConnClosed
	}
}

func (m *linkMailbox) MailboxAppend(ctx context.Context, target string, env []byte) (uint64, error) {
	select {
	case <-m.done:
		return 0, relay.ErrConnClosed
	default:
	}
	return m.scriptedMailbox.MailboxAppend(ctx, target, env)
}

// TestPBNET4_ServiceRunEndsWhenTheRelayLinkDies is the fence. The Service is given a live
// link, both loops are allowed to reach it, and then the link dies with the context still
// very much alive. Run must return; a Run that keeps running is the zombie sidecar.
//
// No wall-clock duration is asserted -- only that Run ends within a budget generous enough
// to survive a loaded host.
func TestPBNET4_ServiceRunEndsWhenTheRelayLinkDies(t *testing.T) {
	mb := newLinkMailbox()
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 2)
	}
	svc := NewService(ServiceConfig{
		DaemonSocket:   "/nonexistent/remote.sock", // no daemon: the journal side just retries
		Relay:          mb,
		PhoneTarget:    "phone",
		Key:            key,
		EpochID:        1,
		ReconnectDelay: 20 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan error, 1)
	go func() { out <- svc.Run(ctx) }()

	time.Sleep(200 * time.Millisecond) // let both loops reach the relay
	close(mb.done)                     // the desktop's WiFi goes away

	select {
	case err := <-out:
		if err == nil {
			t.Fatal("Run returned nil when its relay link died; the caller that owns the dial " +
				"cannot distinguish that from an orderly shutdown and will not redial")
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Run reported %v when its relay link died, which is the ORDERLY SHUTDOWN "+
				"identity; the caller must be able to tell a dead link from a signal", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Service.Run was STILL RUNNING 30s after its only relay connection died, with " +
			"nothing left that could ever redial it: the phone reconnects, reports online, and " +
			"the machine is silently and permanently dead (PB-NET-4, ADR-007 section 6.0)")
	}
}
