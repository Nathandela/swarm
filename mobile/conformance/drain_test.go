package conformance_test

// FAILING-FIRST (TDD RED, GG-5) guard for PB-SYNC-6 on the drain loop: a hostile relay
// cannot drive unbounded work on a battery-powered device.
//
// The relay is the declared adversary and the drain loop is the only thing it can speak
// to unprompted, so the loop's exit condition is a security property, not a performance
// one. Measuring it needs the phone's OWN wire traffic, which is why this file proxies
// the relay rather than asking the relay how often it was read: the number that matters
// is what the handset sent.

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/Nathandela/swarm/internal/remote/relay"
)

// readBudget is 6.0's inbound drain bound: <= 3 mailbox_read/s per hop. Both
// mailbox_read and mailbox_ack meter against the relay's OpsPerMin (600), unlike
// mailbox_append, so a loop that reads without bound spends the same budget the live
// tail it is trying to receive depends on.
const readBudget = 3

// TestPBSYNC6_AnUndecodableTailFrameDoesNotSpinTheDrain.
//
// phonecore advances RelayCursor only inside commitReceive -- i.e. only on a frame it
// SUCCESSFULLY opened. So when the LAST item in the mailbox cannot be opened, the cursor
// never passes it and every subsequent read returns that same item. A drain loop whose
// only exit condition is "the page was empty" therefore re-reads it at full speed
// forever: one 100-byte frame, from the party the design assumes is hostile, and the
// phone is pinned at its quota and knocked offline.
//
// It is reachable BENIGNLY too: any frame that arrives before InstallContentKey cannot
// be opened, so a phone that is merely locked when the machine publishes is enough.
func TestPBSYNC6_AnUndecodableTailFrameDoesNotSpinTheDrain(t *testing.T) {
	h := newHarness(t)

	// Re-open the phone against a counting proxy so its reads are observable. Same state
	// directory, same relay-auth key, so the same mailbox: this is the process-death path
	// openApp already exists for.
	proxy := newCountingRelay(t, h.RelayURL)
	if err := h.App.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	h.AppRelayURL = proxy.URL()
	h.App = h.openApp()
	eventually(t, "the phone never came online through the proxy", func() bool {
		st, err := h.App.ConnectionState()
		return err == nil && st == "online"
	})

	// ONE undecodable envelope, as the mailbox TAIL. Not garbage in general: the tail
	// specifically, because that is the item the cursor can never advance past.
	if _, err := h.machineRelay.MailboxAppend(h.ctx, h.phoneTarget, bytes.Repeat([]byte{0xA5}, 100)); err != nil {
		t.Fatalf("append the undecodable frame: %v", err)
	}

	// Let the loop reach the bad tail, then measure a clean window.
	time.Sleep(time.Second)
	const window = 3 * time.Second
	start := proxy.reads()
	time.Sleep(window)
	got := proxy.reads() - start

	if max := readBudget * int(window/time.Second); got > max {
		t.Errorf("PB-SYNC-6: the phone issued %d mailbox_read ops in %s (%.1f/s) with ONE "+
			"undecodable frame at the mailbox tail; 6.0 budgets <= %d reads/s. The drain loop's "+
			"exit condition is an EMPTY page, but a page that cannot be opened is never empty and "+
			"never advances the cursor, so a single hostile frame spins it until the quota kills "+
			"the connection", got, window, float64(got)/window.Seconds(), readBudget)
	}
	if st, err := h.App.ConnectionState(); err != nil || st != "online" {
		t.Errorf("ConnectionState = %q, %v after one undecodable frame; the phone must stay online "+
			"(PB-APP-8), not flap through the relay's quota refusals", st, err)
	}
}

// ---- a counting relay proxy --------------------------------------------------

// countingRelay is a websocket proxy in front of the real relay that counts the
// mailbox_read control frames the PHONE sends. It forwards bytes verbatim in both
// directions and decides nothing: the relay behind it is the real one, and the frames it
// counts are the frames the handset actually put on the wire.
type countingRelay struct {
	srv *httptest.Server

	mu sync.Mutex
	n  int
}

func newCountingRelay(t *testing.T, upstream string) *countingRelay {
	t.Helper()
	cr := &countingRelay{}
	cr.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
				// The relay's control ops are a JSON body inside a binary frame; the op
				// name is matched on the raw bytes so the proxy never has to decode, and
				// therefore never has to stay in step with, the wire format.
				if bytes.Contains(data, []byte("mailbox_read")) {
					cr.mu.Lock()
					cr.n++
					cr.mu.Unlock()
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

func (c *countingRelay) URL() string { return "ws" + strings.TrimPrefix(c.srv.URL, "http") }

func (c *countingRelay) reads() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}
