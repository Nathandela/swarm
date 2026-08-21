package swarmmobile

// FAILING-FIRST (TDD RED, GG-5) for Wave R9's playbook section-10 clause: the 500 ms poll
// survives ONLY as "an explicit compatibility fallback ... for old relays", and the
// fallback must be selected by the old relay's real refusal of the mailbox_wait op --
// never by a config flag.
//
// WHAT AN OLD RELAY'S REFUSAL LOOKS LIKE ON THE WIRE, because it is the shape this test
// reproduces byte for byte. A pre-v0.7.0 relay's dispatch has no "mailbox_wait" case, so
// the op falls to `default: replyErr(codeBadRequest)` -- an ORDINARY in-order MsgError
// frame. The client's parked wait correlates only MsgWaitReply frames (ADR-007 B7), so
// that refusal never reaches the waiter: the wait sits silent until the phone's own
// per-wait deadline ends it. "The relay answered its authenticated handshake and then
// answered NOTHING to the very first wait" is therefore the only phone-visible evidence an
// old relay can produce, and it is what must select the poll -- after a reconnect, because
// the refused wait's un-correlated MsgError has already desynchronised that connection's
// request/reply accounting.
//
// The rig: a real relay.Server behind a proxy that answers the two wait ops exactly as an
// old relay's dispatch would (MsgError bad_request, nothing forwarded) and forwards every
// other frame verbatim. A journal event sealed by the real machine-side RelaySink must
// still reach the phone -- through the fallback poll -- and the drain must have recorded
// the unsupported verdict rather than defaulted to it.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remotegw"
)

// r9OldRelay proxies the real relay but answers mailbox_wait and mailbox_wait_cancel the
// way a pre-v0.7.0 relay's dispatch does: MsgError {code: bad_request}, nothing forwarded.
// Every other frame crosses verbatim. It counts the refusals it issued and the
// mailbox_read ops it forwarded, so the test can prove the fallback was REACHED BY the
// refusal rather than taken by default.
type r9OldRelay struct {
	srv      *httptest.Server
	upstream string

	mu       sync.Mutex
	refusals int
	reads    int
}

func newR9OldRelay(t *testing.T, upstream string) *r9OldRelay {
	t.Helper()
	p := &r9OldRelay{upstream: upstream}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		down, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = down.CloseNow() }()
		down.SetReadLimit(relay.MaxFrame + 64)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		up, _, err := websocket.Dial(ctx, p.upstream, nil)
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
		go func() { // relay -> phone, verbatim
			defer func() { done <- struct{}{} }()
			for {
				mt, data, err := up.Read(ctx)
				if err != nil {
					return
				}
				if err := write(down, mt, data); err != nil {
					return
				}
			}
		}()
		go func() { // phone -> relay: refuse the wait ops like an old dispatch, forward the rest
			defer func() { done <- struct{}{} }()
			for {
				mt, data, err := down.Read(ctx)
				if err != nil {
					return
				}
				if refusal := p.classify(data); refusal != nil {
					if err := write(down, websocket.MessageBinary, refusal); err != nil {
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
	t.Cleanup(p.srv.Close)
	return p
}

func (p *r9OldRelay) URL() string { return "ws" + strings.TrimPrefix(p.srv.URL, "http") }

// classify returns the old relay's MsgError reply for a wait op, or nil to forward.
func (p *r9OldRelay) classify(data []byte) []byte {
	tag, payload, err := relay.ReadFrame(bytes.NewReader(data))
	if err != nil || tag != relay.MsgRelay {
		return nil
	}
	var env struct {
		Op string `json:"op"`
	}
	if json.Unmarshal(payload, &env) != nil {
		return nil
	}
	switch env.Op {
	case "mailbox_wait", "mailbox_wait_cancel":
		p.mu.Lock()
		p.refusals++
		p.mu.Unlock()
		body, _ := json.Marshal(map[string]string{"code": "bad_request"})
		var buf bytes.Buffer
		if relay.WriteFrame(&buf, relay.MsgError, body) != nil {
			return nil
		}
		return buf.Bytes()
	case "mailbox_read":
		p.mu.Lock()
		p.reads++
		p.mu.Unlock()
	}
	return nil
}

func (p *r9OldRelay) counts() (refusals, reads int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.refusals, p.reads
}

// r9Eventually is the conformance suite's eventually, in-package.
func r9Eventually(t *testing.T, msg string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(msg)
}

// r9SawCursor reports whether the app's journal page holds an entry at cursor.
func r9SawCursor(t *testing.T, app *App, cursor int64) bool {
	t.Helper()
	page, err := app.ReadJournal(0, 0)
	if err != nil {
		return false
	}
	n, err := page.Count()
	if err != nil {
		return false
	}
	for i := 0; i < n; i++ {
		if e, err := page.At(i); err == nil && e != nil && e.Cursor == cursor {
			return true
		}
	}
	return false
}

// TestR9_AnOldRelayRefusingMailboxWaitDropsThePhoneToThePollFallback: the drain's first
// wait against the old relay goes unanswered (its MsgError refusal cannot be correlated to
// the parked wait), the phone records the unsupported verdict at its per-wait deadline,
// reconnects, and the compatibility poll delivers the machine's journal event.
func TestR9_AnOldRelayRefusingMailboxWaitDropsThePhoneToThePollFallback(t *testing.T) {
	// The per-wait bound is §6.0's 25 s server ceiling plus one request budget in
	// production; against a relay that will never answer, a test that sat it out would
	// spend 35 s proving a timeout fires. Shorten the bound, not the mechanism. Registered
	// FIRST so it is restored after the app's own cleanup closed the drain.
	oldTimeout := waitTimeout
	waitTimeout = 1 * time.Second
	t.Cleanup(func() { waitTimeout = oldTimeout })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	rcfg := relay.DefaultConfig()
	rcfg.DBPath = filepath.Join(t.TempDir(), "relay.db")
	srv, err := relay.New(rcfg)
	if err != nil {
		t.Fatalf("relay.New: %v", err)
	}
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("relay start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	// A paired phone state directory, the conformance seeding path in-package: provision
	// the device custody, authorize it at the relay, seed the durable coordinates.
	const machine = "r9-machine-endpoint"
	const epoch = uint32(7)
	dir := t.TempDir()
	custody := r4r3Custody{}
	wake := custodySealer{tier: "wake", fetch: custody.WakeKEK}
	content := custodySealer{tier: "content", fetch: custody.ContentKEK}
	provision, err := phonecore.Resume(phonecore.Config{
		Dir: dir, Machine: machine, WakeSealer: wake, ContentSealer: content,
	})
	if err != nil {
		t.Fatalf("phonecore.Resume: %v", err)
	}
	ks := provision.KeyStore()
	phoneTarget := relay.RoutingID(ks.RelayAuthPublic())

	keys, err := crypto.NewEpochKeys()
	if err != nil {
		t.Fatalf("epoch keys: %v", err)
	}
	signPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("machine sign key: %v", err)
	}
	machineID, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatalf("machine identity: %v", err)
	}
	mPub, mPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("machine relay-auth key: %v", err)
	}
	machineRelay, err := relay.Dial(ctx, srv.URL(), relay.ClientAuth{
		RelayAuthPub: mPub,
		Sign:         func(c []byte) ([]byte, error) { return ed25519.Sign(mPriv, c), nil },
	})
	if err != nil {
		t.Fatalf("machine dial: %v", err)
	}
	t.Cleanup(func() { _ = machineRelay.Close() })

	var ceremony [16]byte
	if _, err := rand.Read(ceremony[:]); err != nil {
		t.Fatalf("crypto/rand: %v", err)
	}
	sig, err := ks.SignRelayAuth(relay.ConsentMessage(hex.EncodeToString(ceremony[:]), relay.RoutingID(mPub)))
	if err != nil {
		t.Fatalf("phone signs its route consent: %v", err)
	}
	if err := machineRelay.AuthorizeDevice(ctx, ks.RelayAuthPublic(),
		relay.MarshalConsent(hex.EncodeToString(ceremony[:]), sig)); err != nil {
		t.Fatalf("authorize phone: %v", err)
	}

	store, err := phonecore.OpenStore(filepath.Join(dir, phonecore.StateFileName), machine, wake, content)
	if err != nil {
		t.Fatalf("open phone state: %v", err)
	}
	if err := store.Save(phonecore.State{
		Machine:             machine,
		MachineSignPub:      signPub,
		MachineRelayAuthPub: mPub,
		RoutingID:           phoneTarget,
		EpochID:             epoch,
		Keys:                keys,
	}); err != nil {
		t.Fatalf("seed phone state: %v", err)
	}

	sink := remotegw.NewRelaySink(remotegw.RelayConfig{
		Appender:       machineRelay,
		Target:         phoneTarget,
		Machine:        machine,
		EpochID:        epoch,
		Key:            keys.ContentKey,
		RecipientKeyID: crypto.KeyID(ks.RecipientPublic()),
		SenderKeyID:    crypto.KeyID(machineID.RecipientPublic()),
	})

	proxy := newR9OldRelay(t, srv.URL())
	app, err := NewApp(&Config{StateDir: dir, MachineID: machine, RelayURL: proxy.URL()}, custody)
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	if err := app.Start(); err != nil {
		t.Fatalf("App.Start: %v", err)
	}

	// The machine publishes one journal event through the REAL relay; it sits in the
	// mailbox until whichever drain mode the phone lands in reads it.
	if err := sink.Event(schema.JournalRecord{
		Cursor: 1, SessionID: machine + "/r9-fallback", Type: "launched", Group: "working",
	}); err != nil {
		t.Fatalf("sink.Event: %v", err)
	}

	r9Eventually(t, "the journal event never reached the phone; an old relay's refusal of "+
		"mailbox_wait must drop the drain into the 500 ms compatibility poll, and delivery must "+
		"survive it", func() bool {
		return r9SawCursor(t, app, 1)
	})

	refusals, reads := proxy.counts()
	if refusals == 0 {
		t.Error("the old relay never refused a mailbox_wait: the phone did not try the wait first, " +
			"so whatever delivered the event was not the refusal-selected fallback")
	}
	if reads == 0 {
		t.Error("no mailbox_read crossed the proxy: the event arrived outside the compatibility poll")
	}
	if got := app.waitSupport.Load(); got != waitUnsupported {
		t.Errorf("waitSupport = %d after the refusal, want waitUnsupported (%d): the fallback must be "+
			"recorded as the old relay's refusal verdict, not re-probed or defaulted", got, waitUnsupported)
	}
}
