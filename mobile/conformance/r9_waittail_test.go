package conformance_test

// FAILING-FIRST (TDD RED, GG-5) for Wave R9's playbook section-10 gap: "The 500 ms phone
// polling loop is removed in favor of bounded MailboxWait, with an explicit compatibility
// fallback only for old relays."
//
// WHAT IS UNDER TEST. The shipped phone's inbound drain (mobile/relay.go) against a relay
// that SUPPORTS mailbox_wait -- which is every relay since v0.7.0, including the one this
// harness runs. Two properties:
//
//  1. The drain is a WAIT, not a poll: the phone parks a bounded mailbox_wait at the relay
//     and issues ZERO mailbox_read ops. The 500 ms poll may survive only as the old-relay
//     compatibility fallback, which a supporting relay must never trigger.
//  2. A reconnect across the wait path re-delivers NOTHING: the durable relay cursor is
//     carried into the next generation's first wait, so the relay never re-serves an item
//     the phone already committed -- section 10's "Duplicate semantic operations after
//     retries/reconnect: zero", measured at the wire, where a duplicate would actually
//     originate.
//
// WHY THE WIRE AND NOT THE JOURNAL. phonecore's replay guard would hide a re-served item
// from the journal (crypto.ErrStaleSeq refuses it inside AcceptCommit), so a journal-only
// assertion is satisfied even by a drain that resumes from cursor zero. The proxy counts
// the items the relay actually put on the wire; three pushed events must cross exactly
// three times, however many connections the phone used to receive them.
//
// The proxy forwards every byte verbatim (the relay behind it is the real one); it only
// OBSERVES, except for the two test levers each test arms explicitly: suppressing
// mailbox_ack (so the relay retains everything and a cursor regression would be visible as
// a re-serve) and killing the live connections (to force a reconnect while the drain is
// parked in a wait).

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/coder/websocket"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// r9WireTap is a websocket proxy in front of the real relay. It counts, per op, what the
// PHONE puts on the wire (mailbox_wait / mailbox_read / mailbox_ack) and how many mailbox
// ITEMS the relay serves downstream (mailbox_read MsgOK pages and MsgWaitReply pages
// alike). With suppressAcks armed it answers mailbox_ack itself with an empty MsgOK and
// never forwards it, so the relay retains every item; Kill severs the live connections.
type r9WireTap struct {
	srv      *httptest.Server
	upstream string

	mu           sync.Mutex
	waitOps      int
	readOps      int
	ackOps       int
	itemsServed  int
	suppressAcks bool
	kill         []func()
}

func newR9WireTap(t *testing.T, upstream string) *r9WireTap {
	t.Helper()
	tap := &r9WireTap{upstream: upstream}
	tap.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		down, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = down.CloseNow() }()
		down.SetReadLimit(relay.MaxFrame + 64)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		up, _, err := websocket.Dial(ctx, tap.upstream, nil)
		if err != nil {
			return
		}
		defer func() { _ = up.CloseNow() }()
		up.SetReadLimit(relay.MaxFrame + 64)

		tap.mu.Lock()
		tap.kill = append(tap.kill, func() {
			cancel()
			_ = down.CloseNow()
			_ = up.CloseNow()
		})
		tap.mu.Unlock()

		var wmu sync.Mutex
		write := func(c *websocket.Conn, mt websocket.MessageType, b []byte) error {
			wmu.Lock()
			defer wmu.Unlock()
			return c.Write(ctx, mt, b)
		}

		done := make(chan struct{}, 2)
		go func() { // relay -> phone: count the items actually served
			defer func() { done <- struct{}{} }()
			for {
				mt, data, err := up.Read(ctx)
				if err != nil {
					return
				}
				tap.observeDown(data)
				if err := write(down, mt, data); err != nil {
					return
				}
			}
		}()
		go func() { // phone -> relay: count ops, optionally suppress acks
			defer func() { done <- struct{}{} }()
			for {
				mt, data, err := down.Read(ctx)
				if err != nil {
					return
				}
				if reply, forward := tap.observeUp(data); !forward {
					if err := write(down, websocket.MessageBinary, reply); err != nil {
						return
					}
					continue
				}
				if err := write(up, mt, data); err != nil {
					return
				}
			}
		}()
		<-done
	}))
	t.Cleanup(tap.srv.Close)
	return tap
}

func (p *r9WireTap) URL() string { return "ws" + strings.TrimPrefix(p.srv.URL, "http") }

// observeUp classifies one phone->relay frame. It returns (nil, true) to forward, or a
// synthetic reply and false when the frame is a suppressed mailbox_ack.
func (p *r9WireTap) observeUp(data []byte) (reply []byte, forward bool) {
	tag, payload, err := relay.ReadFrame(bytes.NewReader(data))
	if err != nil || tag != relay.MsgRelay {
		return nil, true
	}
	var env struct {
		Op string `json:"op"`
	}
	if json.Unmarshal(payload, &env) != nil {
		return nil, true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	switch env.Op {
	case "mailbox_wait":
		p.waitOps++
	case "mailbox_read":
		p.readOps++
	case "mailbox_ack":
		p.ackOps++
		if p.suppressAcks {
			// Answer like the relay would, commit nothing: the relay keeps every item.
			var buf bytes.Buffer
			if relay.WriteFrame(&buf, relay.MsgOK, []byte("{}")) == nil {
				return buf.Bytes(), false
			}
		}
	}
	return nil, true
}

// observeDown counts the mailbox items inside one relay->phone frame (a mailbox_read
// MsgOK page or a MsgWaitReply page; every other frame carries no "items").
func (p *r9WireTap) observeDown(data []byte) {
	tag, payload, err := relay.ReadFrame(bytes.NewReader(data))
	if err != nil || (tag != relay.MsgOK && tag != relay.MsgWaitReply) {
		return
	}
	var body struct {
		Items []json.RawMessage `json:"items"`
	}
	if json.Unmarshal(payload, &body) != nil {
		return
	}
	if len(body.Items) == 0 {
		return
	}
	p.mu.Lock()
	p.itemsServed += len(body.Items)
	p.mu.Unlock()
}

func (p *r9WireTap) SuppressAcks() {
	p.mu.Lock()
	p.suppressAcks = true
	p.mu.Unlock()
}

// Kill severs every live proxied connection; the phone's next dial builds a new one.
func (p *r9WireTap) Kill() {
	p.mu.Lock()
	fns := p.kill
	p.kill = nil
	p.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

func (p *r9WireTap) counts() (waits, reads, items int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waitOps, p.readOps, p.itemsServed
}

// r9TapPhone re-opens the harness phone behind a fresh tap, the drain_test.go pattern:
// same state directory, same relay-auth key, same mailbox.
func r9TapPhone(t *testing.T, h *harness) *r9WireTap {
	t.Helper()
	tap := newR9WireTap(t, h.RelayURL)
	if err := h.App.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	h.AppRelayURL = tap.URL()
	h.App = h.openApp()
	eventually(t, "the phone never came online through the tap", func() bool {
		st, err := h.App.ConnectionState()
		return err == nil && st == "online"
	})
	return tap
}

// r9JournalCount returns how many journal entries the phone holds for the given journal
// cursor -- the duplicate detector for the app-visible half of the budget.
func r9JournalCount(t *testing.T, h *harness, cursor int64) int {
	t.Helper()
	page, err := h.App.ReadJournal(0, 0)
	if err != nil {
		t.Fatalf("ReadJournal: %v", err)
	}
	n, err := page.Count()
	if err != nil {
		t.Fatalf("JournalPage.Count: %v", err)
	}
	seen := 0
	for i := 0; i < n; i++ {
		e, err := page.At(i)
		if err != nil {
			t.Fatalf("JournalPage.At: %v", err)
		}
		if e.Cursor == cursor {
			seen++
		}
	}
	return seen
}

// TestR9_TheDrainWaitsOnTheMailboxInsteadOfPolling: against a relay that supports
// mailbox_wait, the phone's drain must park bounded server-side waits and must issue no
// mailbox_read at all -- the 500 ms sleep-poll exists only as the old-relay fallback and a
// supporting relay must never see it (playbook section 10, ADR-007 B100).
func TestR9_TheDrainWaitsOnTheMailboxInsteadOfPolling(t *testing.T) {
	h := newHarness(t)
	tap := r9TapPhone(t, h)

	h.PushEvent(schema.JournalRecord{Cursor: 1, SessionID: testMachineID + "/r9-wait", Type: "launched", Group: "working"})
	eventually(t, "the pushed event never reached the phone through the wait tail", func() bool {
		return r9JournalCount(t, h, 1) >= 1
	})

	waits, reads, _ := tap.counts()
	if waits == 0 {
		t.Errorf("the phone issued ZERO mailbox_wait ops while receiving an item; the drain is "+
			"not the bounded server-side wait playbook section 10 requires (got %d mailbox_read ops "+
			"instead -- the 500 ms poll, ADR-007 B100)", reads)
	}
	if reads != 0 {
		t.Errorf("the phone issued %d mailbox_read ops against a relay that supports mailbox_wait; "+
			"the poll must survive only as the compatibility fallback an OLD relay's refusal selects", reads)
	}
}

// TestR9_AReconnectAcrossTheWaitPathRedeliversNothing: with acks suppressed (the relay
// retains everything) and the connection severed while the drain is parked in a wait, the
// next generation's first wait resumes from the durable cursor -- so the relay serves each
// item exactly once across the reconnect, and the journal holds each exactly once.
func TestR9_AReconnectAcrossTheWaitPathRedeliversNothing(t *testing.T) {
	h := newHarness(t)
	tap := r9TapPhone(t, h)
	tap.SuppressAcks()

	h.PushEvent(schema.JournalRecord{Cursor: 1, SessionID: testMachineID + "/r9-a", Type: "launched", Group: "working"})
	h.PushEvent(schema.JournalRecord{Cursor: 2, SessionID: testMachineID + "/r9-b", Type: "launched", Group: "working"})
	eventually(t, "the pre-reconnect events never reached the phone", func() bool {
		return r9JournalCount(t, h, 1) >= 1 && r9JournalCount(t, h, 2) >= 1
	})

	// Sever while the drain is parked in its next wait; the phone reconnects through the
	// same tap with the same durable cursor.
	tap.Kill()
	eventually(t, "the phone never came back online after the severed connection", func() bool {
		st, err := h.App.ConnectionState()
		return err == nil && st == "online"
	})

	h.PushEvent(schema.JournalRecord{Cursor: 3, SessionID: testMachineID + "/r9-c", Type: "launched", Group: "working"})
	eventually(t, "the post-reconnect event never reached the phone", func() bool {
		return r9JournalCount(t, h, 3) >= 1
	})

	waits, reads, items := tap.counts()
	if waits == 0 {
		t.Errorf("zero mailbox_wait ops crossed the tap; the reconnect was not exercised on the wait path")
	}
	if reads != 0 {
		t.Errorf("the phone fell back to %d mailbox_read ops across a plain reconnect; the fallback "+
			"is reserved for a relay that REFUSES mailbox_wait, not for a dropped link", reads)
	}
	if items != 3 {
		t.Errorf("the relay served %d mailbox items for 3 pushed events across the reconnect; with "+
			"acks suppressed a re-served item is a cursor regression, and section 10 budgets duplicate "+
			"deliveries after retries/reconnect at ZERO", items)
	}
	for c := int64(1); c <= 3; c++ {
		if got := r9JournalCount(t, h, c); got != 1 {
			t.Errorf("journal holds %d entries for cursor %d, want exactly 1", got, c)
		}
	}
}
