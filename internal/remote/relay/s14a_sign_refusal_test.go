// Slice S14a review remediation: the ADR-007 B18(a) refusal path in authenticate()
// was a guard no test in the tree could fail.
//
// B18(a) makes ClientAuth.Sign failable precisely because its only production
// implementation -- crypto.KeyStore.SignRelayAuth behind a hardware-gated custody --
// can refuse. client.go handles that refusal by closing the connection and returning
// the error, but every Sign fixture in this package returns a nil error, so mutating
// the handler to `sig, _ := auth.Sign(...)` (the exact defect B14 removes, one layer
// up) left the relay, phonecore, crypto and mobile suites green. The phonecore AST
// call-site fence cannot catch it either: it matches the name SignRelayAuth, and the
// call here goes through the closure field, named Sign. PB-KEY-6 requires a failure
// driven through EVERY signing path; this drives one through the only production
// relay path there is.
//
// Three properties are asserted and each is load-bearing:
//
//   - the refusal SURFACES BY IDENTITY (errors.Is against a sentinel), so swallowing
//     it with `_` and letting the relay reject the resulting nil signature opaquely
//     is caught -- the caller would see a relay auth failure, not the custody error
//     that tells it to re-prompt or re-pair;
//   - the connection is CLOSED, so an implementation that returns the error and leaks
//     the socket is caught. An error-return-only assertion passes against that one;
//   - NOTHING reaches the relay after the refusal. Signing nil and sending it anyway
//     is the other half of the defect B18(a) names, and only observing the frames
//     excludes it.
package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// errS14ACustodyRefused stands in for crypto.ErrKeyAuthRequired: a distinct
// sentinel, so the test asserts the caller gets back THIS error rather than merely
// some error.
var errS14ACustodyRefused = errors.New("relay test: custody refused the relay-auth signature")

// recordingRelay is a verbatim websocket proxy in front of a real relay. It records
// the control op of every frame the CLIENT sends and reports when the client's socket
// goes away. It answers nothing and decides nothing: the handshake the client sees is
// the real relay's, so the observation cannot drift from server behaviour.
type recordingRelay struct {
	srv      *httptest.Server
	upstream string

	mu   sync.Mutex
	ops  []string
	gone chan struct{}
	once sync.Once
}

func newRecordingRelay(t *testing.T, upstream string) *recordingRelay {
	t.Helper()
	rr := &recordingRelay{upstream: upstream, gone: make(chan struct{})}
	rr.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		down, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = down.CloseNow() }()
		down.SetReadLimit(MaxFrame + 64)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		up, _, err := websocket.Dial(ctx, rr.upstream, nil)
		if err != nil {
			return
		}
		defer func() { _ = up.CloseNow() }()
		up.SetReadLimit(MaxFrame + 64)

		go func() {
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

		// The downstream read loop ending IS the client's connection going away: a
		// Close writes a close frame, and a dropped socket fails the read.
		defer rr.markGone()
		for {
			mt, data, err := down.Read(ctx)
			if err != nil {
				return
			}
			rr.record(data)
			if err := up.Write(ctx, mt, data); err != nil {
				return
			}
		}
	}))
	t.Cleanup(rr.srv.Close)
	return rr
}

func (rr *recordingRelay) URL() string {
	return "ws://" + strings.TrimPrefix(rr.srv.URL, "http://")
}

// record names one client frame by its control op (or its tag, for the ops that
// carry a dedicated one). An undecodable frame is recorded as such rather than
// dropped: a test that asserts "nothing was sent" must not be satisfied by garbage.
func (rr *recordingRelay) record(data []byte) {
	name := "undecodable"
	if tag, payload, err := ReadFrame(bytes.NewReader(data)); err == nil {
		var env struct {
			Op string `json:"op"`
		}
		switch {
		case tag == MsgRelay && json.Unmarshal(payload, &env) == nil && env.Op != "":
			name = env.Op
		default:
			name = fmt.Sprintf("tag:%d", tag)
		}
	}
	rr.mu.Lock()
	defer rr.mu.Unlock()
	rr.ops = append(rr.ops, name)
}

func (rr *recordingRelay) markGone() { rr.once.Do(func() { close(rr.gone) }) }

// clientGone is closed when the client's socket ends.
func (rr *recordingRelay) clientGone() <-chan struct{} { return rr.gone }

func (rr *recordingRelay) sentOps() []string {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	return append([]string(nil), rr.ops...)
}

// TestS14A_DialSurfacesASigningRefusalWithoutSendingAuthResp drives a custody refusal
// through Dial -- the only production path that reaches ClientAuth.Sign.
func TestS14A_DialSurfacesASigningRefusalWithoutSendingAuthResp(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)
	rr := newRecordingRelay(t, srv.URL())
	pub, _ := newRelayAuthKey(t)

	var signCalls int
	refusing := ClientAuth{
		RelayAuthPub: pub,
		Sign: func([]byte) ([]byte, error) {
			signCalls++
			return nil, errS14ACustodyRefused
		},
	}

	c, err := Dial(testCtx(t), rr.URL(), refusing)
	if c != nil {
		_ = c.Close()
		t.Fatalf("ADR-007 B18(a): Dial returned a Client despite the custody refusal; an unsigned "+
			"connection is not authenticated (err=%v)", err)
	}
	if signCalls != 1 {
		t.Fatalf("Sign was called %d times, want exactly 1; the fixture is not exercising the refusal path", signCalls)
	}
	if !errors.Is(err, errS14ACustodyRefused) {
		t.Fatalf("ADR-007 B18(a): Dial returned err %v, want the custody refusal itself. Discarding it with "+
			"`_` and signing nil re-creates one layer up exactly the errorless interface B14 removed: the "+
			"caller sees an opaque relay rejection instead of the refusal that tells it to re-prompt "+
			"(ErrKeyAuthRequired) or re-pair (ErrKeyInvalidated)", err)
	}

	// B18(a) requires close-AND-return. A refusal that returns the error but leaves
	// the socket open leaks one connection per refusal, and a phone whose custody is
	// locked refuses on every reconnect attempt.
	select {
	case <-rr.clientGone():
	case <-time.After(testDeadline):
		t.Fatalf("ADR-007 B18(a): the connection was still open %v after Dial returned the custody refusal; "+
			"the refusal path must close the connection, not just return", testDeadline)
	}

	// The refusal must stop the handshake where it stands. auth_init went out before
	// Sign was reached; nothing may follow it.
	ops := rr.sentOps()
	if len(ops) != 1 || ops[0] != "auth_init" {
		t.Fatalf("ADR-007 B18(a): the client sent %v, want exactly [auth_init]. Signing nil and letting the "+
			"relay reject it opaquely is the other half of the defect B18(a) names -- the caller still gets "+
			"an error, so only the frames distinguish it", ops)
	}
}
