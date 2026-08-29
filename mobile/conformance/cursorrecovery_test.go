package conformance_test

// A DISCONTINUOUS RELAY READ CURSOR MUST RECOVER WITHOUT RE-PAIRING.
//
// The relay MINTS relay.Item.Cursor -- its own doc says "the relay's own monotonic storage
// cursor (UNTRUSTED ordering)" -- and the phone persists it as the point its next read
// resumes from. One item forwarded with that field rewritten past every real cursor ends
// every machine->phone delivery, permanently: the value is durable, it is monotonic, and it
// survives process death. Nothing else in the phone can move it back, and the designated
// repair channel cannot repair it because a reseed is delivered THROUGH the poisoned cursor.
// Before this test the only recovery was deleting the state directory and re-pairing.
//
// The relay's durable sequence and retained first item now expose when that coordinate no
// longer describes the current mailbox. The phone must rewind only the relay coordinate,
// preserve its authenticated replay guards, compact redeliveries, and resume by itself.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// cursorPoisonProxy is a websocket proxy in front of the real relay that rewrites the
// untrusted storage cursor on every mailbox item it forwards downstream. It changes NOTHING
// else -- the sealed envelope bytes pass through byte for byte, so every AEAD, every seq and
// every signature the phone checks still verifies.
type cursorPoisonProxy struct {
	srv      *httptest.Server
	upstream string
	value    atomic.Uint64 // 0 => pass through untouched
}

func newCursorPoisonProxy(t *testing.T, upstream string) *cursorPoisonProxy {
	t.Helper()
	pr := &cursorPoisonProxy{upstream: upstream}
	pr.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		down, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = down.CloseNow() }()
		down.SetReadLimit(relay.MaxFrame + 64)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		up, _, err := websocket.Dial(ctx, pr.upstream, nil)
		if err != nil {
			return
		}
		defer func() { _ = up.CloseNow() }()
		up.SetReadLimit(relay.MaxFrame + 64)

		var wmu sync.Mutex
		write := func(c *websocket.Conn, mt websocket.MessageType, b []byte) error {
			wmu.Lock()
			defer wmu.Unlock()
			return c.Write(ctx, mt, b)
		}

		done := make(chan struct{}, 2)
		go func() { // upstream -> phone: the poisoned direction
			defer func() { done <- struct{}{} }()
			for {
				mt, data, err := up.Read(ctx)
				if err != nil {
					return
				}
				if out, ok := pr.rewrite(data); ok {
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
	t.Cleanup(pr.srv.Close)
	return pr
}

func (p *cursorPoisonProxy) URL() string { return "ws" + strings.TrimPrefix(p.srv.URL, "http") }

// Poison arms the rewrite: every mailbox item forwarded after this carries v as its cursor.
func (p *cursorPoisonProxy) Poison(v uint64) { p.value.Store(v) }

// Heal stops rewriting; the relay behaves honestly again.
func (p *cursorPoisonProxy) Heal() { p.value.Store(0) }

// rewrite replaces the cursor on every item of a mailbox_read reply (MsgOK) or a
// MsgWaitReply. Every other frame is left alone.
//
// ONE item is rewritten per armed page, never a whole page of them, and that is deliberate:
// relay.Client already refuses a page whose cursors do not strictly advance, so rewriting
// several would be caught at the transport and would never reach the state this test is
// about. The single-item case is the one no ordering rule can see.
func (p *cursorPoisonProxy) rewrite(data []byte) ([]byte, bool) {
	v := p.value.Load()
	if v == 0 {
		return nil, false
	}
	tag, payload, err := relay.ReadFrame(strings.NewReader(string(data)))
	if err != nil {
		return nil, false
	}
	if tag != relay.MsgOK && tag != relay.MsgWaitReply {
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
	var items []relay.Item
	if err := json.Unmarshal(rawItems, &items); err != nil || len(items) != 1 {
		return nil, false
	}
	items[0].Cursor = v // the envelope beside it is untouched
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
	if err := relay.WriteFrame(&buf, tag, newPayload); err != nil {
		return nil, false
	}
	return []byte(buf.String()), true
}

// poisonedPhone stands the phone up behind the proxy, reconciled, with one delivered event
// proving the machine->phone leg works before anything is poisoned.
func poisonedPhone(t *testing.T) (*harness, *cursorPoisonProxy) {
	t.Helper()
	h := newHarness(t)
	proxy := newCursorPoisonProxy(t, h.RelayURL)

	_ = h.App.Close()
	h.AppRelayURL = proxy.URL()
	h.App = h.openApp()

	eventually(t, "the phone never came online through the proxy", func() bool {
		s, err := h.App.ConnectionState()
		return err == nil && s == "online"
	})
	// Reconciled, so the phone is in the state a real paired handset is in: streams NOT
	// stale. Without this the premise starts stale and carries no signal.
	h.PushReconcile()
	eventually(t, "reconcile never adopted", func() bool {
		s, err := h.App.StateSummary()
		return err == nil && s.Reconciled
	})

	h.PushEvent(schema.JournalRecord{Cursor: 1, SessionID: testMachineID + "/before", Type: "launched", Group: "working"})
	eventually(t, "premise: the phone never received the pre-poison event", func() bool {
		return phoneSawSession(t, h, "/before")
	})
	return h, proxy
}

// phoneSawSession reports whether the phone's journal page holds an entry for session.
func phoneSawSession(t *testing.T, h *harness, session string) bool {
	t.Helper()
	page, err := h.App.ReadJournal(0, 0)
	if err != nil {
		t.Fatalf("ReadJournal: %v", err)
	}
	n, err := page.Count()
	if err != nil {
		t.Fatalf("JournalPage.Count: %v", err)
	}
	for i := 0; i < n; i++ {
		e, err := page.At(i)
		if err != nil {
			t.Fatalf("JournalPage.At: %v", err)
		}
		if strings.Contains(e.SessionID, session) {
			return true
		}
	}
	return false
}

// TestDrain_AutomaticallyRecoversAPhoneWhoseRelayCursorWasPoisoned is the recovery, end to
// end: a real relay, a real relay.Client, the real facade, and the poison delivered over the
// wire rather than written into durable state by the test. No repair button or re-pair is
// allowed between the discontinuity and the recovered event.
func TestDrain_AutomaticallyRecoversAPhoneWhoseRelayCursorWasPoisoned(t *testing.T) {
	h, proxy := poisonedPhone(t)

	// 1<<63 rather than MaxUint64 deliberately: MaxUint64 exercises the store's own scan-start
	// bound instead (relay/cursorwrap_test.go). This is simply "past every real cursor".
	proxy.Poison(1 << 63)
	h.PushEvent(schema.JournalRecord{Cursor: 2, SessionID: testMachineID + "/poison", Type: "launched", Group: "working"})
	eventually(t, "the poisoned frame was never delivered", func() bool {
		return phoneSawSession(t, h, "/poison")
	})
	proxy.Heal()

	// THE RECOVERY, automatically on the next honest mailbox answer.
	h.PushEvent(schema.JournalRecord{Cursor: 3, SessionID: testMachineID + "/recovered", Type: "launched", Group: "working"})
	eventually(t, "the phone did not recover automatically from a discontinuous relay cursor",
		func() bool { return phoneSawSession(t, h, "/recovered") })

	// AND THE RECOVERY IS DURABLE. A rewind that lived only in memory would be undone by the
	// next process death, which on Android is routine rather than exceptional.
	_ = h.App.Close()
	h.App = h.openApp()
	eventually(t, "the phone never came back online after restart", func() bool {
		s, err := h.App.ConnectionState()
		return err == nil && s == "online"
	})
	h.PushEvent(schema.JournalRecord{Cursor: 4, SessionID: testMachineID + "/after-restart", Type: "launched", Group: "working"})
	eventually(t, "the poisoned cursor came back after a restart: the rewind never reached disk",
		func() bool { return phoneSawSession(t, h, "/after-restart") })
}
