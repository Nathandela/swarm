package conformance_test

// Slice S11 REVIEW ROUND 1 -- FAILING-FIRST (TDD RED, GG-5) tests for three defects the
// review reproduced, all on the one path no unit test can reach: the facade with a REAL
// link that goes away underneath it.
//
// B2 -- PB-INPUT-1 / ADR-007 D7 are not met. App.SendInput resolves its destination through
// sendContext -> awaitConn, which POLLS FOR UP TO FIVE SECONDS for a new connection and then
// appends. With the link cut, a keystroke therefore blocks, rides the RECONNECTED link, and
// lands at the machine ~1 s late while SendInput returns nil. That is a keystroke surviving
// a disconnect, which ADR-007 D7 makes structural. It stayed invisible because the existing
// fence (TestS6B_KeystrokeNeverSurvivesADisconnectWhileFollowing) asserts on
// transport.SendLive, and App.SendInput never calls SendLive -- it calls
// relay.Client.MailboxAppend directly, so the fence guards a different code path.
//
// B3 -- PB-INPUT-2's FIRST enumerated severance event is unimplemented. suspendInput is
// reachable only from Stop and Close; run() handles a dropped connection internally and
// calls neither. So after a relay outage -- or a GATEWAY RESTART, which is the event the
// requirement names and which can seal no notice precisely because the gateway is gone --
// LeaseState still reports the pre-outage generation live and Require returns nil. The phone
// types against a lease the new gateway does not hold. s11_lease_test.go models the event
// ("gateway restart (no notice can arrive; the transport drops)" -> SeverAll, "which is why
// a disconnect must sever, not merely pause") and nothing in production does it.
//
// R1 -- the Undelivered ledger had no reader. PB-INPUT-1's criterion is that the state is
// SURFACED, and a ledger the app cannot read across the gomobile boundary surfaces nothing.
//
// R2 -- InputCoalescer.Insert had no production caller, so PB-INPUT-6's stated paste/IME
// treatment was unreachable and the shipped path did the OPPOSITE: a paste through SendInput
// goes via Type, which holds the sub-4 KiB tail for up to a full window.
//
// This file contains NO implementation.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// ---------------------------------------------------------------------------
// a relay proxy whose link can be cut and restored
// ---------------------------------------------------------------------------

// cuttableRelay is a websocket proxy in front of the real relay whose link the test can
// SEVER and later restore. It forwards bytes verbatim and decides nothing; cutting it models
// the only thing the phone can actually observe about a gateway restart or a network
// outage -- its own transport going away.
//
// It can also be SILENCED, which is the opposite failure and the harder one: the link stays
// up, everything the phone sends is still accepted, and no reply ever comes back. See Silence.
type cuttableRelay struct {
	srv      *httptest.Server
	upstream string

	mu     sync.Mutex
	cut    bool
	silent bool
	conns  []*websocket.Conn
}

func newCuttableRelay(t *testing.T, upstream string) *cuttableRelay {
	t.Helper()
	cr := &cuttableRelay{upstream: upstream}
	cr.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		down, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = down.CloseNow() }()
		down.SetReadLimit(relay.MaxFrame + 64)

		cr.mu.Lock()
		if cr.cut {
			cr.mu.Unlock()
			return // refuse while severed, so a reconnect attempt fails too
		}
		cr.conns = append(cr.conns, down)
		cr.mu.Unlock()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		up, _, err := websocket.Dial(ctx, cr.upstream, nil)
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
				// A SILENCED relay reads the reply and throws it away: the phone's socket is
				// untouched, so there is nothing for it to observe (see Silence).
				if cr.silenced() {
					continue
				}
				if err := down.Write(ctx, mt, data); err != nil {
					return
				}
			}
		}()
		go func() {
			defer func() { done <- struct{}{} }()
			for {
				mt, data, err := down.Read(ctx)
				if err != nil {
					return
				}
				if err := up.Write(ctx, mt, data); err != nil {
					return
				}
			}
		}()
		<-done
	}))
	t.Cleanup(cr.srv.Close)
	return cr
}

func (c *cuttableRelay) URL() string { return "ws" + strings.TrimPrefix(c.srv.URL, "http") }

// Cut severs every live connection and refuses new ones.
func (c *cuttableRelay) Cut() {
	c.mu.Lock()
	c.cut = true
	conns := c.conns
	c.conns = nil
	c.mu.Unlock()
	for _, conn := range conns {
		_ = conn.CloseNow()
	}
}

// Silence stops every reply reaching the phone, permanently, while leaving the connection UP
// and still accepting everything the phone writes.
//
// It is NOT a weaker Cut, it is a different failure: a cut socket is an event the phone can
// see (Conn.Done closes, the reconnect loop runs). A silent relay presents nothing to see --
// which is exactly what the untrusted relay gets for free by doing nothing, and what a
// half-open TCP after a WiFi -> cellular handoff looks like from the handset.
func (c *cuttableRelay) Silence() {
	c.mu.Lock()
	c.silent = true
	c.mu.Unlock()
}

func (c *cuttableRelay) silenced() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.silent
}

// Restore lets the phone reconnect.
func (c *cuttableRelay) Restore() {
	c.mu.Lock()
	c.cut = false
	c.mu.Unlock()
}

// s11rProxiedHarness stands up the phone behind a cuttable proxy, reconciled, with the
// session's lease confirmed, ready to type.
func s11rProxiedHarness(t *testing.T) (*harness, *cuttableRelay) {
	t.Helper()
	h := newHarness(t)
	proxy := newCuttableRelay(t, h.RelayURL)

	_ = h.App.Close()
	h.AppRelayURL = proxy.URL()
	h.App = h.openApp()

	eventually(t, "the phone never came online through the proxy", func() bool {
		s, err := h.App.ConnectionState()
		return err == nil && s == "online"
	})
	h.PushReconcile()
	eventually(t, "reconcile never adopted", func() bool {
		s, err := h.App.StateSummary()
		return err == nil && s.Reconciled
	})
	if _, err := h.App.TakeControl(testSession); err != nil {
		t.Fatalf("TakeControl: %v", err)
	}
	h.AwaitCommand(protocol.ActionTakeControl)
	h.AwaitLease(testSession)
	return h, proxy
}

// s11rSawInput reports whether any input frame the machine has drained contains want.
func s11rSawInput(h *harness, want string) bool {
	h.Drain()
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, in := range h.Inputs {
		if strings.Contains(string(in.Data), want) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// B3 + B2 -- a disconnect severs, and nothing typed into the gap is delivered late
// ---------------------------------------------------------------------------

// TestS11R_ADisconnectSeversTheLeaseAndNeverDeliversAKeystrokeLate fences both defects in
// ONE test, because each alone is satisfiable by the other's bug: a phone that refused
// input for the wrong reason would pass the delivery half, and a phone that severed
// correctly but still queued the bytes would pass the refusal half.
func TestS11R_ADisconnectSeversTheLeaseAndNeverDeliversAKeystrokeLate(t *testing.T) {
	h, proxy := s11rProxiedHarness(t)

	// PREMISE: typing works before the cut, so a refusal afterwards is caused by the cut.
	if err := h.App.SendInput(testSession, []byte("before\r")); err != nil {
		t.Fatalf("SendInput before the cut: %v", err)
	}
	h.AwaitInput("data")

	proxy.Cut()
	eventually(t, "the phone never noticed the link was cut", func() bool {
		s, err := h.App.ConnectionState()
		return err == nil && s != "online"
	})

	// The link comes back WHILE the keystroke is being handled -- the reviewer's probe, and
	// the ordinary case on a handset that loses signal for a moment. This is what turns
	// awaitConn's five-second wait into a delivery: the wait outlives the outage, so the
	// byte is appended to the RECONNECTED link and arrives at the machine ~1 s late.
	go func() {
		time.Sleep(300 * time.Millisecond)
		proxy.Restore()
	}()

	// (a) THE LEASE IS SEVERED. PB-INPUT-2's first enumerated event: the gateway may have
	// restarted, so no notice can arrive and the transport drop is the only signal there is.
	started := time.Now()
	err := h.App.SendInput(testSession, []byte("ZZZ-after-the-cut\r"))
	took := time.Since(started)
	if took > 2*time.Second {
		t.Errorf("SendInput blocked for %v with the link down. Input is LIVE-ONLY (ADR-007 D7): it "+
			"must resolve against the connection as it stands, not wait for one to come back -- "+
			"waiting IS how the keystroke ends up on the reconnected link", took)
	}
	if err == nil {
		t.Fatal("SendInput with the link cut returned nil. Either it queued the keystroke (ADR-007 D7 " +
			"forbids it) or it believes it still holds a lease the machine may no longer hold (PB-INPUT-2)")
	}
	if !errors.Is(err, phonecore.ErrNoLease) {
		t.Errorf("SendInput after the cut = %v, want ErrNoLease -- a dropped transport must SEVER "+
			"the lease, not merely fail the send: after a gateway restart the pre-restart generation "+
			"is dead and no severance notice can ever arrive to say so", err)
	}

	// (b) IT IS REPORTED, not silently dropped. PB-INPUT-1 requires the state be SURFACED,
	// and the ledger is only surfaced if the app can read it (R1).
	led, lerr := h.App.UndeliveredInputs()
	if lerr != nil {
		t.Fatalf("UndeliveredInputs: %v", lerr)
	}
	n, lerr := led.Count()
	if lerr != nil {
		t.Fatalf("UndeliveredList.Count: %v", lerr)
	}
	if n == 0 {
		t.Fatal("nothing on the undelivered ledger after a refused keystroke; PB-INPUT-1 resolves it " +
			"as an explicit \"delivery unknown / not sent\", never as a silent drop")
	}
	entry, lerr := led.At(0)
	if lerr != nil {
		t.Fatalf("UndeliveredList.At(0): %v", lerr)
	}
	if entry.Reason == "" {
		t.Error("the undelivered entry carries no reason; an empty reason surfaces nothing")
	}
	if entry.SessionID != testSession {
		t.Errorf("undelivered entry names session %q, want %q", entry.SessionID, testSession)
	}

	// (c) AND IT NEVER ARRIVES LATE. The link is already coming back; the bytes typed into
	// the gap must not ride it. This is the half awaitConn's five-second wait broke.
	proxy.Restore()
	eventually(t, "the phone never reconnected after the link was restored", func() bool {
		s, err := h.App.ConnectionState()
		return err == nil && s == "online"
	})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s11rSawInput(h, "ZZZ-after-the-cut") {
			t.Fatal("a keystroke typed while the link was down was DELIVERED on the reconnected link. " +
				"ADR-007 D7 is structural about this: it lands against a terminal state the user has " +
				"since changed, minutes after they gave up on it")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestS11R_TypingResumesAfterTheLeaseIsRetaken is the anti-brick control on the test above.
// A severance that could not be recovered from would satisfy every assertion there and make
// the phone useless (PB-STATE-10 forbids exactly that shape), so the recovery path is
// asserted on the same harness: reconnect, re-take control, and type again.
func TestS11R_TypingResumesAfterTheLeaseIsRetaken(t *testing.T) {
	h, proxy := s11rProxiedHarness(t)

	proxy.Cut()
	eventually(t, "the phone never noticed the link was cut", func() bool {
		s, err := h.App.ConnectionState()
		return err == nil && s != "online"
	})
	proxy.Restore()
	eventually(t, "the phone never reconnected", func() bool {
		s, err := h.App.ConnectionState()
		return err == nil && s == "online"
	})

	if _, err := h.App.TakeControl(testSession); err != nil {
		t.Fatalf("re-TakeControl after the outage: %v", err)
	}
	h.AwaitLease(testSession)
	if err := h.App.SendInput(testSession, []byte("recovered\r")); err != nil {
		t.Fatalf("SendInput after re-taking control = %v, want nil -- a severance that cannot be "+
			"recovered from is a permanent brick", err)
	}
	eventually(t, "the post-recovery keystroke never reached the machine", func() bool {
		return s11rSawInput(h, "recovered")
	})
}

// ---------------------------------------------------------------------------
// R2 -- the paste path exists and is atomic
// ---------------------------------------------------------------------------

// TestS11R_PasteIsAtomicThroughTheFacade is PB-INPUT-6's stated treatment, asserted at the
// only place it can ship broken: the facade. InputCoalescer.Insert had no production caller,
// so a paste went through Type, which chunks at 4 KiB and then HOLDS the sub-4 KiB tail for
// up to a full 125 ms window -- the user watches the end of their paste disappear.
//
// The assertion is that the WHOLE unit is on the wire by the time the call returns, which is
// what "atomic" means here and what the held tail breaks. The text is sized to leave a tail:
// two full 4 KiB frames plus a remainder.
func TestS11R_PasteIsAtomicThroughTheFacade(t *testing.T) {
	h, _ := s11rProxiedHarness(t)

	const tail = 500
	text := strings.Repeat("p", phonecore.MaxInputPayload*2+tail)
	if err := h.App.Paste(testSession, text); err != nil {
		t.Fatalf("Paste: %v", err)
	}

	// No wait, no window: everything must already be appended.
	h.Drain()
	h.mu.Lock()
	var got []byte
	for _, in := range h.Inputs {
		got = append(got, in.Data...)
	}
	frames := len(h.Inputs)
	h.mu.Unlock()

	if string(got) != text {
		t.Fatalf("the machine received %d bytes immediately after Paste returned, want all %d. "+
			"A paste routed through the keystroke path holds its sub-%d-byte tail for a full "+
			"%v window (PB-INPUT-6: a paste is one event, not a keystroke stream)",
			len(got), len(text), phonecore.MaxInputPayload, phonecore.InputFrameInterval)
	}
	// ... and it was split at the cap, not sent as one oversize frame the relay would refuse.
	if frames < 3 {
		t.Errorf("a %d-byte paste produced %d frames; the %d-byte cap forces at least 3",
			len(text), frames, phonecore.MaxInputPayload)
	}
}
