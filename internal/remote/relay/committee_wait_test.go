// FAILING-FIRST (TDD RED, GG-5) for the final-audit committee's relay-wait findings
// (Opus H1/M1/M2, codex quota HIGH): the wait capability must be NEGOTIATED through the
// r_hello handshake the protocol already has, and an unsolicited frame must never shift
// the request/reply pairing on a pumped connection.
//
// THE H1 SHAPE, reproduced byte for byte below. A pre-wait relay's dispatch has no
// "mailbox_wait" case, so a blind probe of the op is answered with an ordinary in-order
// MsgError. The client correlates only MsgWaitReply frames to the parked waiter, so that
// refusal lands in the pumped reply channel instead -- where the NEXT request/reply
// exchange consumes it as its own answer, and every reply after it is owed to the
// question before. The negotiation fix means a shipped phone never probes blind; this
// file is the DEFENSE IN DEPTH underneath it, at the client, against any peer that
// volunteers a frame nobody asked for.

package relay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestCommittee_HelloAdvertisesTheWaitCapability: the relay's r_hello capability set must
// name "wait", so a client can decide BEFORE its first mailbox_wait whether the op exists
// -- instead of probing blind and reading the answer out of a timeout.
func TestCommittee_HelloAdvertisesTheWaitCapability(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)

	conn := dialRaw(t, srv.URL())
	_, caps, err := conn.Hello(testCtx(t), ProtocolVersion, []string{"mailbox", "wait"})
	if err != nil {
		t.Fatalf("Hello: %v", err)
	}
	for _, c := range caps {
		if c == "wait" {
			return
		}
	}
	t.Fatalf("r_hello agreed caps = %v, want \"wait\" among them: the relay serves mailbox_wait "+
		"but never registered it as a capability, so a client cannot learn of the op except by "+
		"probing it blind -- which against an old relay desynchronises the reply stream (H1)", caps)
}

// committeeScriptedRelay is a from-scratch scripted peer speaking the relay frame
// protocol: it authenticates anything, assigns mailbox_append cursors 1,2,3,..., and can
// misbehave on cue. It is NOT the real relay.Server, deliberately -- the defect under
// test is the CLIENT's reply accounting against a peer that breaks the one-reply-per-
// request contract, which the real server never does on purpose.
type committeeScriptedRelay struct {
	srv *httptest.Server

	// waitSeen is signalled once per mailbox_wait op the script has received; the test
	// synchronises on it so "the refusal is stale by the time it is sent" is a fact of
	// the script, not a race.
	waitSeen chan struct{}
	// strayNow makes the script volunteer one MsgError frame right now, correlated to
	// nothing -- the shape an old relay's refusal of an unknown op arrives in.
	strayNow chan struct{}

	mu sync.Mutex
	// refusalArmed: the next mailbox_append is preceded by the stale MsgError the
	// parked wait provoked, exactly the interleaving H1 describes (the refusal lands
	// while an unrelated exchange is in flight).
	refusalArmed bool
	appendN      uint64
}

func newCommitteeScriptedRelay(t *testing.T) *committeeScriptedRelay {
	t.Helper()
	s := &committeeScriptedRelay{
		waitSeen: make(chan struct{}, 4),
		strayNow: make(chan struct{}),
	}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = ws.CloseNow() }()
		ws.SetReadLimit(MaxFrame + 64)
		ctx := r.Context()

		var wmu sync.Mutex
		write := func(tag MsgType, body any) {
			payload, err := json.Marshal(body)
			if err != nil {
				return
			}
			var buf bytes.Buffer
			if WriteFrame(&buf, tag, payload) != nil {
				return
			}
			wmu.Lock()
			defer wmu.Unlock()
			_ = ws.Write(ctx, websocket.MessageBinary, buf.Bytes())
		}

		go func() { // the volunteered stray, on the test's cue
			for {
				select {
				case <-ctx.Done():
					return
				case <-s.strayNow:
					write(MsgError, map[string]string{"code": "bad_request"})
				}
			}
		}()

		for {
			_, data, err := ws.Read(ctx)
			if err != nil {
				return
			}
			tag, payload, err := ReadFrame(bytes.NewReader(data))
			if err != nil {
				continue
			}
			switch tag {
			case MsgRelay:
				var env struct {
					Op string `json:"op"`
				}
				if json.Unmarshal(payload, &env) != nil {
					continue
				}
				switch env.Op {
				case "auth_init":
					write(MsgOK, map[string]any{"nonce": []byte("committee-nonce-16")})
				case "auth_resp":
					write(MsgOK, map[string]any{"routing_id": "committee-rig"})
				case "mailbox_wait":
					// Absorbed silently for now; the REFUSAL it provokes is deferred
					// until an unrelated exchange is in flight (the next append).
					s.mu.Lock()
					s.refusalArmed = true
					s.mu.Unlock()
					select {
					case s.waitSeen <- struct{}{}:
					default:
					}
				case "mailbox_wait_cancel":
					// An old dispatch would refuse this too; irrelevant to the shape.
				}
			case MsgMailboxAppend:
				s.mu.Lock()
				stale := s.refusalArmed
				s.refusalArmed = false
				s.appendN++
				n := s.appendN
				s.mu.Unlock()
				if stale {
					// The old relay's refusal of the parked wait, arriving while THIS
					// exchange is in flight.
					write(MsgError, map[string]string{"code": "bad_request"})
				}
				write(MsgOK, map[string]uint64{"cursor": n})
			}
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *committeeScriptedRelay) URL() string {
	return "ws" + strings.TrimPrefix(s.srv.URL, "http")
}

// TestCommittee_AnUnsolicitedFrameDoesNotShiftReplyPairing drives the two shapes an
// uncorrelated frame can arrive in, and asserts the pairing of request to reply survives
// both. The append CURSOR is the witness: the scripted peer assigns 1,2,3,... in arrival
// order, so a shifted reply stream hands a later append an earlier append's cursor.
func TestCommittee_AnUnsolicitedFrameDoesNotShiftReplyPairing(t *testing.T) {
	script := newCommitteeScriptedRelay(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	cl, err := Dial(ctx, script.URL(), ClientAuth{
		RelayAuthPub: pub,
		Sign:         func(c []byte) ([]byte, error) { return ed25519.Sign(priv, c), nil },
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = cl.Close() })

	// SHAPE 1: a stray with NOTHING in flight. No reply is owed to anyone, so the frame
	// answers no question and must be dropped at the pump -- never queued for the next
	// caller to consume as its own reply.
	script.strayNow <- struct{}{}
	time.Sleep(200 * time.Millisecond) // let the stray cross and reach the pump
	cursor, err := cl.MailboxAppend(ctx, "peer", []byte("a"))
	if err != nil || cursor != 1 {
		t.Fatalf("MailboxAppend after an idle-time stray = (%d, %v), want (1, nil).\n"+
			"A frame nobody is owed was queued as a pending reply, and the next exchange "+
			"consumed it as its own answer -- the reply stream is shifted by one from here on (H1)",
			cursor, err)
	}

	// SHAPE 2: the H1 interleaving. A parked wait's stale refusal arrives while an
	// unrelated append is in flight. That append is owed a reply, so the stray is
	// indistinguishable from its answer and the append fails -- ONE bounded casualty.
	// What must NOT happen is the cascade: the append's real reply must be dropped at
	// the pump (no reply owed by then), not queued to answer the NEXT exchange.
	wdone := make(chan struct{})
	wctx, wcancel := context.WithCancel(ctx)
	defer func() { wcancel(); <-wdone }()
	go func() {
		defer close(wdone)
		_, _, _ = cl.MailboxWait(wctx, 0)
	}()
	select {
	case <-script.waitSeen:
	case <-time.After(10 * time.Second):
		t.Fatal("the scripted peer never saw the mailbox_wait op")
	}

	if _, err := cl.MailboxAppend(ctx, "peer", []byte("b")); err == nil {
		t.Log("append #2 received a clean reply despite the stale refusal in flight; " +
			"interleaving landed after the exchange, which the next assertion still covers")
	}
	cursor3, err := cl.MailboxAppend(ctx, "peer", []byte("c"))
	if err != nil || cursor3 != 3 {
		t.Fatalf("MailboxAppend #3 = (%d, %v), want (3, nil).\n"+
			"The stale wait refusal displaced append #2's reply into the pending queue, and "+
			"append #3 consumed it as its own -- every exchange on this connection now answers "+
			"the question before it, for the life of the connection (H1)", cursor3, err)
	}
}
