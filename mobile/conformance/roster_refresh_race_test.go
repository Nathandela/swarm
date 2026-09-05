package conformance_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// stalePageGate holds the first non-empty relay mailbox page immediately before it reaches
// the phone. It makes the production race deterministic: RefreshRoster starts after the
// stale head exists and after the drain requested it, but before MailboxRouter has diagnosed
// its authenticated age.
type stalePageGate struct {
	srv     *httptest.Server
	blocked chan struct{}
	release chan struct{}
	once    sync.Once
}

func newStalePageGate(t *testing.T, upstream string) *stalePageGate {
	t.Helper()
	g := &stalePageGate{blocked: make(chan struct{}), release: make(chan struct{})}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		down, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = down.CloseNow() }()
		down.SetReadLimit(relay.MaxFrame + 64)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		up, _, err := websocket.Dial(ctx, upstream, nil)
		if err != nil {
			return
		}
		defer func() { _ = up.CloseNow() }()
		up.SetReadLimit(relay.MaxFrame + 64)

		done := make(chan struct{}, 2)
		go func() {
			defer func() { done <- struct{}{} }()
			for {
				mt, data, err := up.Read(ctx)
				if err != nil {
					return
				}
				if bytes.Contains(data, []byte(`"items":[{`)) {
					g.once.Do(func() {
						close(g.blocked)
						select {
						case <-g.release:
						case <-ctx.Done():
						}
					})
				}
				if down.Write(ctx, mt, data) != nil {
					return
				}
			}
		}()
		go func() {
			defer func() { done <- struct{}{} }()
			for {
				mt, data, err := down.Read(ctx)
				if err != nil || up.Write(ctx, mt, data) != nil {
					return
				}
			}
		}()
		<-done
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *stalePageGate) URL() string { return "ws" + strings.TrimPrefix(g.srv.URL, "http") }

func TestRefreshRoster_OnePressDiagnosesAnInFlightStaleHeadBeforePublishing(t *testing.T) {
	h := newHarnessWithRelayConfig(t, func(cfg *relay.Config) {
		cfg.Quotas.MailboxMaxItems = 2
	})
	h.PushRoster(schema.JournalRecord{SessionID: testMachineID + "/cached", Type: "roster", Group: "working"})
	eventually(t, "initial roster never reached the phone", func() bool {
		return phoneRosterSawSession(t, h, "/cached")
	})
	time.Sleep(1500 * time.Millisecond)
	if err := h.App.Close(); err != nil {
		t.Fatalf("close phone before stale delivery: %v", err)
	}

	h.SealOffset(-phonecore.InboundMaxAge - time.Minute)
	h.PushEvent(schema.JournalRecord{Cursor: 1, SessionID: testMachineID + "/old-a", Type: "launched"})
	h.PushEvent(schema.JournalRecord{Cursor: 2, SessionID: testMachineID + "/old-b", Type: "launched"})

	gate := newStalePageGate(t, h.RelayURL)
	h.AppRelayURL = gate.URL()
	h.App = h.openApp()
	select {
	case <-gate.blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("phone never requested the stale mailbox page through the gate")
	}

	refreshDone := make(chan error, 1)
	go func() { refreshDone <- h.App.RefreshRoster() }()
	select {
	case err := <-refreshDone:
		t.Fatalf("RefreshRoster returned %v before the in-flight stale page was diagnosed; it can publish the replacement behind an unreachable backlog", err)
	case <-time.After(150 * time.Millisecond):
		// The one explicit gesture is waiting on the drain-owned diagnosis barrier.
	}
	close(gate.release)
	select {
	case err := <-refreshDone:
		if err != nil {
			t.Fatalf("RefreshRoster after stale diagnosis: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RefreshRoster did not complete after the stale page was released")
	}

	cmd := h.AwaitCommand(schema.ActionJournalResync)
	if !cmd.RosterOnly || !cmd.DiscardedBacklog || cmd.DiscardRecoveryToken == "" {
		t.Fatalf("race-crossing refresh command = %#v, want one tokened roster recovery", cmd)
	}
	if !phoneRosterSawSession(t, h, "/cached") {
		t.Fatal("diagnose-then-discard cleared the last durable roster before replacement arrived")
	}
}

// An old frame can be past InboundMaxAge and still be safe to compact normally when the
// durable authenticated receive high-water already covers its sequence. AcceptCommit returns
// ErrStaleAge for that replay, but deliberately does NOT raise InboundAgeRefused. Treating the
// error alone as destructive proof erases the fresh, never-applied tail behind it.
func TestRefreshRoster_DoesNotDiscardFreshTailBehindAnAckableOldReplay(t *testing.T) {
	h := newHarness(t)
	h.PushRoster(schema.JournalRecord{SessionID: testMachineID + "/cached", Type: "roster", Group: "working"})
	eventually(t, "initial roster never reached the phone", func() bool {
		return phoneRosterSawSession(t, h, "/cached")
	})
	time.Sleep(1500 * time.Millisecond)
	if err := h.App.Close(); err != nil {
		t.Fatalf("close phone before replay setup: %v", err)
	}

	store, err := phonecore.OpenStore(filepath.Join(h.CoreDir, phonecore.StateFileName), h.Machine,
		h.Custody.wakeSealer(), h.Custody.contentSealer())
	if err != nil {
		t.Fatalf("open phone state: %v", err)
	}
	st := store.Load()
	bucket := phonecore.Bucket{Sender: h.senderKeyID, Epoch: h.EpochID}
	replayedSeq := st.Receive[bucket] + 1

	// Make the replay occupy one complete bounded page, leaving the fresh event on the next
	// page. The record is never decoded: its authenticated age is rejected first.
	h.SealOffset(-phonecore.InboundMaxAge - time.Minute)
	h.PushEvent(schema.JournalRecord{
		Cursor: replayedSeq, SessionID: testMachineID + "/covered-old", Type: "launched",
		Name: strings.Repeat("x", 780_000),
	})
	st.Receive[bucket] = replayedSeq // model a relay restore of a frame already committed
	if err := store.Save(st); err != nil {
		t.Fatalf("seed durable receive coverage for replay: %v", err)
	}
	h.SealOffset(0)
	h.PushEvent(schema.JournalRecord{
		Cursor: replayedSeq + 1, SessionID: testMachineID + "/fresh-tail", Type: "launched", Group: "working",
	})

	gate := newStalePageGate(t, h.RelayURL)
	h.AppRelayURL = gate.URL()
	h.App = h.openApp()
	select {
	case <-gate.blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("phone never requested the replay page through the gate")
	}
	refreshDone := make(chan error, 1)
	go func() { refreshDone <- h.App.RefreshRoster() }()
	select {
	case err := <-refreshDone:
		t.Fatalf("RefreshRoster returned %v before replay diagnosis", err)
	case <-time.After(150 * time.Millisecond):
	}
	close(gate.release)
	select {
	case err := <-refreshDone:
		if err != nil {
			t.Fatalf("RefreshRoster after covered replay diagnosis: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RefreshRoster did not complete after replay release")
	}

	cmd := h.AwaitCommand(schema.ActionJournalResync)
	if cmd.DiscardedBacklog || cmd.DiscardRecoveryToken != "" {
		t.Fatalf("covered-replay refresh command = %#v; an ackable replay is not destructive proof", cmd)
	}
	eventually(t, "fresh tail was discarded behind an already-covered replay", func() bool {
		return phoneRosterSawSession(t, h, "/fresh-tail")
	})
}

// A healthy diagnostic is not complete merely because the phone committed the page. Until
// its relay ack lands, the item still consumes mailbox depth; publishing the refresh command
// first can make the daemon's replacement fail at the relay quota boundary. The explicit
// gesture may pay one synchronous ack here: refresh is already a user-visible round trip,
// and keeping the ack generation-fenced is more important than the ordinary drain's latency.
func TestRefreshRoster_AcksHealthyDiagnosisBeforeReplacementAtMailboxCapacity(t *testing.T) {
	h := newHarnessWithRelayConfig(t, func(cfg *relay.Config) {
		cfg.Quotas.MailboxMaxItems = 2
	})
	h.PushRoster(schema.JournalRecord{SessionID: testMachineID + "/cached", Type: "roster", Group: "working"})
	eventually(t, "initial roster never reached the phone", func() bool {
		return phoneRosterSawSession(t, h, "/cached")
	})
	// Let the ordinary wait batcher release the setup item so the single-slot mailbox can
	// express only the ordering under test below.
	time.Sleep(1500 * time.Millisecond)
	if err := h.App.Close(); err != nil {
		t.Fatalf("close phone before capacity setup: %v", err)
	}

	h.PushEvent(schema.JournalRecord{
		Cursor: 1, SessionID: testMachineID + "/healthy-a", Type: "launched", Group: "working",
	})
	h.PushEvent(schema.JournalRecord{
		Cursor: 2, SessionID: testMachineID + "/healthy-b", Type: "launched", Group: "working",
	})
	gate := newStalePageGate(t, h.RelayURL)
	h.AppRelayURL = gate.URL()
	h.App = h.openApp()
	select {
	case <-gate.blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("phone never requested the healthy full-mailbox page through the gate")
	}
	// Cross one empty batcher tick while the page is held. After release the next ordinary
	// async tick is safely far away, making an early command-before-ack observable rather
	// than scheduler-dependent.
	time.Sleep(1100 * time.Millisecond)
	refreshDone := make(chan error, 1)
	go func() { refreshDone <- h.App.RefreshRoster() }()
	select {
	case err := <-refreshDone:
		t.Fatalf("RefreshRoster returned %v before healthy diagnosis", err)
	case <-time.After(150 * time.Millisecond):
	}
	close(gate.release)
	select {
	case err := <-refreshDone:
		if err != nil {
			t.Fatalf("RefreshRoster after healthy diagnosis: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RefreshRoster did not complete after the healthy page was released")
	}

	cmd := h.AwaitCommand(schema.ActionJournalResync)
	if cmd.DiscardedBacklog || cmd.DiscardRecoveryToken != "" {
		t.Fatalf("healthy full-mailbox refresh command = %#v; diagnosis must not be destructive", cmd)
	}
	// This append is the daemon's replacement. With command-before-ack ordering the relay
	// refuses it because /healthy-a and /healthy-b still occupy both mailbox slots.
	h.PushRoster(schema.JournalRecord{
		SessionID: testMachineID + "/replacement", Type: "roster", Group: "working",
	})
	eventually(t, "replacement roster did not arrive after capacity was synchronously released", func() bool {
		return phoneRosterSawSession(t, h, "/replacement")
	})
}
