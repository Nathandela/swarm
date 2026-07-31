package relay

// The relay MINTS Item.Cursor and both consumers adopt it as a DURABLE resume point, so the
// one thing a client can check without knowing the relay's storage is that a page OBEYS THE
// READ CONTRACT the relay states for itself: items strictly greater than the requested
// cursor, in strictly ascending order. A page that repeats one value across its items is not
// a storage cursor -- it is a rewrite.
//
// This is HALF of a fence and is documented as half: a page of ONE item still says nothing,
// so a relay that rewrites the cursor of a single delivered item is not caught here. Bounding
// THAT needs a limit on how far a cursor may move per page, which no requirement states.

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"
)

// cursorRewritingRelay is a websocket proxy in front of the REAL relay that rewrites the
// storage cursor on every mailbox item it forwards downstream, and changes NOTHING else --
// the sealed envelope bytes pass through byte for byte. It is the silentRelay of
// calldeadline_test.go with a different lie.
type cursorRewritingRelay struct {
	srv      *httptest.Server
	upstream string
	value    atomic.Uint64 // 0 => pass through untouched
}

func newCursorRewritingRelay(t *testing.T, upstream string) *cursorRewritingRelay {
	t.Helper()
	cr := &cursorRewritingRelay{upstream: upstream}
	cr.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		down, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = down.CloseNow() }()
		down.SetReadLimit(MaxFrame + 64)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		up, _, err := websocket.Dial(ctx, cr.upstream, nil)
		if err != nil {
			return
		}
		defer func() { _ = up.CloseNow() }()
		up.SetReadLimit(MaxFrame + 64)

		var wmu sync.Mutex
		write := func(c *websocket.Conn, mt websocket.MessageType, b []byte) error {
			wmu.Lock()
			defer wmu.Unlock()
			return c.Write(ctx, mt, b)
		}

		done := make(chan struct{}, 2)
		go func() { // upstream -> client: the rewritten direction
			defer func() { done <- struct{}{} }()
			for {
				mt, data, err := up.Read(ctx)
				if err != nil {
					return
				}
				if out, ok := cr.rewrite(data); ok {
					data = out
				}
				if err := write(down, mt, data); err != nil {
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
				if err := write(up, mt, data); err != nil {
					return
				}
			}
		}()
		<-done
	}))
	t.Cleanup(cr.srv.Close)
	return cr
}

func (p *cursorRewritingRelay) URL() string { return "ws" + strings.TrimPrefix(p.srv.URL, "http") }

// Rewrite arms the lie: every mailbox item forwarded after this carries v as its cursor.
func (p *cursorRewritingRelay) Rewrite(v uint64) { p.value.Store(v) }

func (p *cursorRewritingRelay) rewrite(data []byte) ([]byte, bool) {
	v := p.value.Load()
	if v == 0 {
		return nil, false
	}
	tag, payload, err := ReadFrame(strings.NewReader(string(data)))
	if err != nil {
		return nil, false
	}
	if tag != MsgOK && tag != MsgWaitReply {
		return nil, false
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(payload, &body); err != nil {
		return nil, false
	}
	rawItems, ok := body["items"]
	if !ok {
		return nil, false
	}
	var items []Item
	if err := json.Unmarshal(rawItems, &items); err != nil || len(items) == 0 {
		return nil, false
	}
	for i := range items {
		items[i].Cursor = v // the envelope beside it is untouched
	}
	enc, err := json.Marshal(items)
	if err != nil {
		return nil, false
	}
	body["items"] = enc
	newPayload, err := json.Marshal(body)
	if err != nil {
		return nil, false
	}
	var buf strings.Builder
	if err := WriteFrame(&buf, tag, newPayload); err != nil {
		return nil, false
	}
	return []byte(buf.String()), true
}

// TestMailboxRead_RefusesAPageWhoseCursorsDoNotAdvance drives a REAL authenticated reader
// through the proxy: the relay is the one that lies, not a constant this test hands the
// client.
func TestMailboxRead_RefusesAPageWhoseCursorsDoNotAdvance(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, nil)
	proxy := newCursorRewritingRelay(t, srv.URL())

	mPub, mPriv := newRelayAuthKey(t)
	dPub, dPriv := newRelayAuthKey(t)
	machine := dialAuthed(t, srv.URL(), authFor(mPub, mPriv))
	device := dialAuthed(t, proxy.URL(), authFor(dPub, dPriv))
	if err := machine.AuthorizeDevice(testCtx(t), ed25519.PublicKey(dPub),
		consentTo(dPriv, machine.RoutingID())); err != nil {
		t.Fatalf("AuthorizeDevice: %v", err)
	}
	devRID := RoutingID(dPub)
	sp := newSealParty(t, []byte("machine-sender-pub-000000000000x"), []byte("device-recipient-pub-0000000000x"))

	for i := uint64(1); i <= 3; i++ {
		if _, err := machine.MailboxAppend(testCtx(t), devRID, sp.sealMailbox(t, i, []byte{byte('a' + i)}, clk)); err != nil {
			t.Fatalf("MailboxAppend #%d: %v", i, err)
		}
	}

	// Premise: through the proxy, untouched, the page is what the relay stored.
	items, err := device.MailboxRead(testCtx(t), 0)
	if err != nil {
		t.Fatalf("premise MailboxRead: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("premise: read %d items, want 3", len(items))
	}

	proxy.Rewrite(1 << 63)
	if _, err := device.MailboxRead(testCtx(t), 0); err == nil {
		t.Fatalf("MailboxRead accepted a page whose three items all carry cursor 1<<63: the " +
			"consumer adopts the highest as its DURABLE resume point, so one such page ends " +
			"delivery permanently")
	}
}

// TestMailboxRead_RefusesAnItemAtOrBelowTheRequestedCursor is the other half of the same
// contract: a page must not contain what the caller already asked to skip past.
func TestMailboxRead_RefusesAnItemAtOrBelowTheRequestedCursor(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, nil)
	proxy := newCursorRewritingRelay(t, srv.URL())

	mPub, mPriv := newRelayAuthKey(t)
	dPub, dPriv := newRelayAuthKey(t)
	machine := dialAuthed(t, srv.URL(), authFor(mPub, mPriv))
	device := dialAuthed(t, proxy.URL(), authFor(dPub, dPriv))
	if err := machine.AuthorizeDevice(testCtx(t), ed25519.PublicKey(dPub),
		consentTo(dPriv, machine.RoutingID())); err != nil {
		t.Fatalf("AuthorizeDevice: %v", err)
	}
	devRID := RoutingID(dPub)
	sp := newSealParty(t, []byte("machine-sender-pub-000000000000x"), []byte("device-recipient-pub-0000000000x"))

	for i := uint64(1); i <= 2; i++ {
		if _, err := machine.MailboxAppend(testCtx(t), devRID, sp.sealMailbox(t, i, []byte{byte('a' + i)}, clk)); err != nil {
			t.Fatalf("MailboxAppend #%d: %v", i, err)
		}
	}

	// Resuming past cursor 1 really does yield the second item.
	items, err := device.MailboxRead(testCtx(t), 1)
	if err != nil {
		t.Fatalf("premise MailboxRead(1): %v", err)
	}
	if len(items) != 1 || items[0].Cursor != 2 {
		t.Fatalf("premise: read %d items (first cursor %v), want 1 at cursor 2", len(items), items)
	}

	// ONE item, rewritten to the cursor the caller asked to resume PAST. It is the single-item
	// case, so nothing about ordering catches it -- only the strictly-greater clause does.
	proxy.Rewrite(1)
	if _, err := device.MailboxRead(testCtx(t), 1); err == nil {
		t.Fatalf("MailboxRead(1) accepted an item at cursor 1: the relay's own contract is " +
			"STRICTLY greater, and an item at or below the cursor is one the caller has " +
			"already consumed")
	}
}
