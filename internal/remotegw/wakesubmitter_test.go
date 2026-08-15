package remotegw

// Bead agents-tracker-hggx.4 (Wave R3, machine side) -- FAILING-FIRST (TDD RED, GG-5)
// tests for the gateway HTTP submitter (docs/specifications/push-gateway-api.md §3.5,
// §4, §6.4). This is item (4) of the task: an httptest server speaking the spec's
// POST /v1/wakes surface, success only on 2xx, every non-2xx code mapped to its §6.4
// transition, and byte-identical retry proven by capturing both requests.
//
// THE SEAM this file pins:
//
//	// HTTPWakeSubmitter is the production WakeSubmitter (spec §3.5): POST the 74 raw
//	// octets, unopened and unmodified, under the submit capability, over HTTPS only
//	// (PG-TR-1). It parses the gateway's typed error body when present. A response with
//	// NO parseable body is folded into the SAME unconditionally-retryable case as a
//	// response that never arrived at all (see wakesubmitter.go's header for why this
//	// reading of PG-ERR-1/PG-ERR-3/§6.4 -- not the literal status-based fallback -- is
//	// the one this machine implements): both are returned PLAIN, never wrapped as
//	// *WakeSubmitError, which is what tells WakeObligationMachine.Drive to treat them as
//	// P9's unconditionally-retryable transport-failure case rather than as a parsed
//	// gateway refusal.
//	type HTTPWakeSubmitter struct {
//		BaseURL          string
//		SubmitCapability string
//		Client           *http.Client // nil => defaultWakeSubmitClient (an explicit timeout)
//	}
//	func (s *HTTPWakeSubmitter) SubmitWake(ctx context.Context, envelope []byte) error
//
// This file contains NO implementation.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// wakeGatewayRequest is one request the fake gateway observed.
type wakeGatewayRequest struct {
	method        string
	path          string
	contentType   string
	authorization string
	body          []byte
}

// wakeGatewayServer is an httptest double for the spec's POST /v1/wakes surface. Each
// call to respond determines the outcome for the NEXT request; requests past the end of
// the list repeat the last response, mirroring fakeSubmitter's outcome-list shape.
type wakeGatewayServer struct {
	srv       *httptest.Server
	reqs      []wakeGatewayRequest
	responses []func(w http.ResponseWriter)
}

// newWakeGatewayServer serves over TLS (httptest.NewTLSServer), matching PG-TR-1's
// HTTPS-only requirement, which HTTPWakeSubmitter now enforces before it will submit
// anything (wakesubmitter.go). g.client() returns a client pre-configured to trust this
// server's certificate, exactly as a real submitter would trust the ordinary Web PKI.
func newWakeGatewayServer(t *testing.T, responses ...func(w http.ResponseWriter)) *wakeGatewayServer {
	t.Helper()
	g := &wakeGatewayServer{responses: responses}
	g.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		g.reqs = append(g.reqs, wakeGatewayRequest{
			method: r.Method, path: r.URL.Path,
			contentType: r.Header.Get("Content-Type"), authorization: r.Header.Get("Authorization"),
			body: body,
		})
		idx := len(g.reqs) - 1
		if idx >= len(g.responses) {
			idx = len(g.responses) - 1
		}
		if idx < 0 {
			writeWakeAccepted(w)
			return
		}
		g.responses[idx](w)
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func writeWakeAccepted(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"provider_accepted"}`))
}

// writeWakeError writes the spec §3.6 Error schema: {code, message, retryable}.
func writeWakeError(status int, code string, retryable bool) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			Retryable bool   `json:"retryable"`
		}{Code: code, Message: "test double", Retryable: retryable})
	}
}

// writeWakeBodyless writes a bare status with NO parseable error body -- PG-ERR-1's
// single status-reading exception: a proxy-truncated or otherwise bodyless response.
func writeWakeBodyless(status int) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) { w.WriteHeader(status) }
}

// writeWakeOKWrongStatus writes 200 with a JSON body whose `status` field is NOT the
// §3.5-pinned `provider_accepted` constant -- e.g. a compromised or misbehaving gateway
// (spec §7.3) answering 200 without FCM actually having accepted the request.
func writeWakeOKWrongStatus(status string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(struct {
			Status string `json:"status"`
		}{Status: status})
	}
}

// newTestHTTPWakeSubmitter builds a submitter that trusts g's TLS certificate, exactly
// as a real submitter trusts the ordinary Web PKI in production.
func newTestHTTPWakeSubmitter(g *wakeGatewayServer) *HTTPWakeSubmitter {
	return &HTTPWakeSubmitter{BaseURL: g.srv.URL, SubmitCapability: "test-submit-capability", Client: g.srv.Client()}
}

// --- request shape ---------------------------------------------------------------

// TestHTTPWakeSubmitter_RequestShapeMatchesTheSpec pins PG-SUB-1/PG-TR-5: exactly the
// 74 raw octets, POST /v1/wakes, application/octet-stream, the submit capability header
// -- and success only on the exact 2xx the spec uses (200, never 202: PG-SUB-2 forbids
// the gateway acknowledging before FCM accepts, which is what 202 would connote).
func TestHTTPWakeSubmitter_RequestShapeMatchesTheSpec(t *testing.T) {
	g := newWakeGatewayServer(t, writeWakeAccepted)
	s := &HTTPWakeSubmitter{BaseURL: g.srv.URL, SubmitCapability: "cap-abc123", Client: g.srv.Client()}
	env := make([]byte, WakeV1Size)
	for i := range env {
		env[i] = byte(i)
	}
	if err := s.SubmitWake(context.Background(), env); err != nil {
		t.Fatalf("SubmitWake on a 200 response: %v", err)
	}
	if len(g.reqs) != 1 {
		t.Fatalf("gateway saw %d requests, want 1", len(g.reqs))
	}
	req := g.reqs[0]
	if req.method != http.MethodPost || req.path != "/v1/wakes" {
		t.Fatalf("request = %s %s, want POST /v1/wakes", req.method, req.path)
	}
	if req.contentType != "application/octet-stream" {
		t.Fatalf("Content-Type = %q, want application/octet-stream (PG-TR-5)", req.contentType)
	}
	if req.authorization != "Swarm-Capability cap-abc123" {
		t.Fatalf("Authorization = %q, want the submit-capability scheme (§2.2)", req.authorization)
	}
	if !bytes.Equal(req.body, env) {
		t.Fatal("request body differs from the submitted envelope -- PG-SUB-1 requires it forwarded unchanged")
	}
}

// --- §4 / §6.4: the full non-2xx mapping ------------------------------------------

// TestWakeObligation_SubmitOutcomeMapping is the table-driven proof of item (4): every
// non-2xx code the spec's §4 table and §6.4 transitions define, driven through the REAL
// HTTPWakeSubmitter against a REAL httptest server and into Drive's state transition.
func TestWakeObligation_SubmitOutcomeMapping(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		code      string
		retryable bool
		want      ObligationState
	}{
		{"quota_exceeded", http.StatusTooManyRequests, "quota_exceeded", true, ObligationPending},
		{"upstream_unavailable", http.StatusBadGateway, "upstream_unavailable", true, ObligationPending},
		{"service_unavailable", http.StatusServiceUnavailable, "service_unavailable", true, ObligationPending},
		{"internal", http.StatusInternalServerError, "internal", true, ObligationPending},
		{"upstream_refused", http.StatusBadGateway, "upstream_refused", false, ObligationAbandoned},
		{"address_revoked", http.StatusGone, "address_revoked", false, ObligationAbandoned},
		{"push_token_unregistered", http.StatusGone, "push_token_unregistered", false, ObligationAbandoned},
		{"wake_malformed", http.StatusBadRequest, "wake_malformed", false, ObligationAbandoned},
		{"malformed_request", http.StatusBadRequest, "malformed_request", false, ObligationAbandoned},
		{"unauthorized", http.StatusUnauthorized, "unauthorized", false, ObligationAbandoned},
		{"version_unsupported", http.StatusNotFound, "version_unsupported", false, ObligationAbandoned},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := newWakeGatewayServer(t, writeWakeError(c.status, c.code, c.retryable))
			store := newFakeObligationStore()
			addr := testPushAddress(byte(0xB0 + len(c.name)))
			seq, err := OpenSeqSource("")
			if err != nil {
				t.Fatal(err)
			}
			m := NewWakeObligationMachine(WakeObligationConfig{
				Store: store, Submitter: newTestHTTPWakeSubmitter(g), WakeKey: testWakeKey(),
				Address: addr, Seq: seq, Now: time.Now,
			})
			if err := m.Trigger(); err != nil {
				t.Fatalf("Trigger: %v", err)
			}
			if err := m.Drive(context.Background()); err != nil {
				t.Logf("Drive returned %v (a non-nil error here is fine; the state assertion below is the contract)", err)
			}
			ob, ok, _ := store.Get(addr)
			if !ok {
				t.Fatal("obligation vanished; PG-OBL-3 forbids a failure hiding the record")
			}
			if ob.State != c.want {
				t.Fatalf("gateway status=%d code=%q retryable=%v -> obligation state = %q, want %q",
					c.status, c.code, c.retryable, ob.State, c.want)
			}
			if len(g.reqs) != 1 {
				t.Fatalf("gateway saw %d requests, want exactly 1", len(g.reqs))
			}
		})
	}
}

// TestWakeObligation_SuccessDelivers is the 200 half the table above deliberately
// excludes (it is not a "non-2xx code" case): PG-SUB-2, success only after FCM accepts.
func TestWakeObligation_SuccessDelivers(t *testing.T) {
	g := newWakeGatewayServer(t, writeWakeAccepted)
	store := newFakeObligationStore()
	addr := testPushAddress(0xC0)
	seq, err := OpenSeqSource("")
	if err != nil {
		t.Fatal(err)
	}
	m := NewWakeObligationMachine(WakeObligationConfig{
		Store: store, Submitter: newTestHTTPWakeSubmitter(g), WakeKey: testWakeKey(),
		Address: addr, Seq: seq, Now: time.Now,
	})
	if err := m.Trigger(); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if err := m.Drive(context.Background()); err != nil {
		t.Fatalf("Drive: %v", err)
	}
	ob, _, _ := store.Get(addr)
	if ob.State != ObligationDelivered {
		t.Fatalf("state = %q, want %q", ob.State, ObligationDelivered)
	}
}

// TestWakeObligation_RetryAfterTransientFailureIsByteIdentical closes item (4)'s last
// requirement: a retryable failure followed by success submits the SAME 74 bytes twice,
// captured at the server -- proving PG-WAKE-12's seal-once discipline holds across a
// real HTTP retry, not merely across the in-memory fake of obligation_test.go.
func TestWakeObligation_RetryAfterTransientFailureIsByteIdentical(t *testing.T) {
	g := newWakeGatewayServer(t,
		writeWakeError(http.StatusServiceUnavailable, "service_unavailable", true),
		writeWakeAccepted,
	)
	store := newFakeObligationStore()
	addr := testPushAddress(0xC1)
	seq, err := OpenSeqSource("")
	if err != nil {
		t.Fatal(err)
	}
	m := NewWakeObligationMachine(WakeObligationConfig{
		Store: store, Submitter: newTestHTTPWakeSubmitter(g), WakeKey: testWakeKey(),
		Address: addr, Seq: seq, Now: time.Now,
	})
	if err := m.Trigger(); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if err := m.Drive(context.Background()); err != nil {
		t.Logf("Drive #1 (expected retryable failure): %v", err)
	}
	if ob, _, _ := store.Get(addr); ob.State != ObligationPending {
		t.Fatalf("state after the 503 = %q, want %q", ob.State, ObligationPending)
	}
	if err := m.Drive(context.Background()); err != nil {
		t.Fatalf("Drive #2 (retry): %v", err)
	}
	if ob, _, _ := store.Get(addr); ob.State != ObligationDelivered {
		t.Fatalf("state after the retry = %q, want %q", ob.State, ObligationDelivered)
	}
	if len(g.reqs) != 2 {
		t.Fatalf("gateway saw %d requests, want 2", len(g.reqs))
	}
	if !bytes.Equal(g.reqs[0].body, g.reqs[1].body) {
		t.Fatal("the retried request body differs from the first -- a retry must replay the byte-identical WakeV1 (PG-WAKE-12), never reseal")
	}
}

// --- PG-ERR-1 / PG-ERR-3 / §6.4: the bodyless case -----------------------------------

// TestHTTPWakeSubmitter_BodylessResponseIsUnconditionallyRetryableRegardlessOfStatus
// pins the reading this file's header records: a response with no parseable error body
// is §6.4's "transport failure" row, NOT PG-ERR-1's literal status-based fallback. Every
// status -- both ones PG-ERR-1 would call retryable and ones it would not -- comes back
// as a PLAIN error, never a *WakeSubmitError, so Drive folds all of them into the same
// unconditionally-retryable (pending) transition. This is deliberately the OPPOSITE of
// what a literal PG-ERR-1 reading would assert (a bodyless 400/401 would be terminal
// there): see wakesubmitter.go's header for why that reading is unsafe.
func TestHTTPWakeSubmitter_BodylessResponseIsUnconditionallyRetryableRegardlessOfStatus(t *testing.T) {
	cases := []struct {
		name   string
		status int
	}{
		{"bodyless-500", http.StatusInternalServerError},
		{"bodyless-429", http.StatusTooManyRequests},
		{"bodyless-400", http.StatusBadRequest},
		{"bodyless-401", http.StatusUnauthorized},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := newWakeGatewayServer(t, writeWakeBodyless(c.status))
			s := newTestHTTPWakeSubmitter(g)
			err := s.SubmitWake(context.Background(), make([]byte, WakeV1Size))
			if err == nil {
				t.Fatal("SubmitWake on a bodyless non-2xx response returned nil, want an error")
			}
			if _, ok := err.(*WakeSubmitError); ok {
				t.Fatalf("SubmitWake err = %v (%T) for status %d with no body, want a PLAIN error "+
					"(the transport-failure row), not a *WakeSubmitError", err, err, c.status)
			}

			// And Drive must land pending, never abandoned, regardless of status.
			store := newFakeObligationStore()
			addr := testPushAddress(byte(0xD0 + c.status%16))
			seq, err2 := OpenSeqSource("")
			if err2 != nil {
				t.Fatal(err2)
			}
			m := NewWakeObligationMachine(WakeObligationConfig{
				Store: store, Submitter: s, WakeKey: testWakeKey(), Address: addr, Seq: seq, Now: time.Now,
			})
			if err := m.Trigger(); err != nil {
				t.Fatalf("Trigger: %v", err)
			}
			if err := m.Drive(context.Background()); err == nil {
				t.Fatal("Drive against a bodyless non-2xx response returned nil, want the error surfaced")
			}
			ob, _, _ := store.Get(addr)
			if ob.State != ObligationPending {
				t.Fatalf("status %d, no body -> obligation state = %q, want %q (PG-ERR-3/§6.4, not PG-ERR-1's literal status fallback)",
					c.status, ob.State, ObligationPending)
			}
		})
	}
}

// TestHTTPWakeSubmitter_200WithoutProviderAcceptedIsNotTreatedAsDelivered is the
// regression test for the R3 obligations-review LOW finding: SubmitWake used to treat
// ANY 200 status code as delivered without ever reading the body. A gateway is a named,
// potentially-compromised party (spec §7.3): a bare 200, or one carrying the wrong
// `status` value, must not retire the obligation as `delivered` unless the exact
// §3.5-pinned body `{"status":"provider_accepted"}` is present -- otherwise a gateway
// that answers 200 without FCM ever having accepted the request permanently loses the
// wake with no retry.
func TestHTTPWakeSubmitter_200WithoutProviderAcceptedIsNotTreatedAsDelivered(t *testing.T) {
	cases := []struct {
		name    string
		respond func(w http.ResponseWriter)
	}{
		{"no-body-at-all", writeWakeBodyless(http.StatusOK)},
		{"wrong-status-value", writeWakeOKWrongStatus("queued")},
		{"empty-status-value", writeWakeOKWrongStatus("")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := newWakeGatewayServer(t, c.respond)
			s := newTestHTTPWakeSubmitter(g)
			err := s.SubmitWake(context.Background(), make([]byte, WakeV1Size))
			if err == nil {
				t.Fatal("SubmitWake on a 200 without the pinned provider_accepted body returned nil, want an error")
			}
			if _, ok := err.(*WakeSubmitError); ok {
				t.Fatalf("SubmitWake err = %v (%T), want a PLAIN error, not a *WakeSubmitError "+
					"(the gateway declared no §3.6 error code)", err, err)
			}

			// And Drive must never mark the obligation delivered on this response.
			store := newFakeObligationStore()
			addr := testPushAddress(0xE1)
			seq, err2 := OpenSeqSource("")
			if err2 != nil {
				t.Fatal(err2)
			}
			m := NewWakeObligationMachine(WakeObligationConfig{
				Store: store, Submitter: s, WakeKey: testWakeKey(), Address: addr, Seq: seq, Now: time.Now,
			})
			if err := m.Trigger(); err != nil {
				t.Fatalf("Trigger: %v", err)
			}
			if err := m.Drive(context.Background()); err == nil {
				t.Fatal("Drive against a 200-without-provider_accepted response returned nil, want the error surfaced")
			}
			ob, _, _ := store.Get(addr)
			if ob.State == ObligationDelivered {
				t.Fatalf("obligation state = %q after a 200 that did not carry provider_accepted -- "+
					"a bare/wrong-bodied 200 must never retire the obligation as delivered", ob.State)
			}
			if ob.State != ObligationPending {
				t.Fatalf("obligation state = %q, want %q (unconditionally retryable, same as the bodyless-non-2xx case)",
					ob.State, ObligationPending)
			}
		})
	}
}

// --- transport failure: no response at all -----------------------------------------

// TestHTTPWakeSubmitter_TransportFailureIsPlainAndUnconditionallyRetryable pins P9's
// "timeout, transport failure ... leave the obligation retryable" for the case where no
// HTTP response was ever received: this must NOT be a *WakeSubmitError (there is no
// status to fall back on), so Drive treats it as the unconditional-retry case.
func TestHTTPWakeSubmitter_TransportFailureIsPlainAndUnconditionallyRetryable(t *testing.T) {
	g := newWakeGatewayServer(t, writeWakeAccepted)
	s := newTestHTTPWakeSubmitter(g) // captures BaseURL and the trusting Client before the close below
	g.srv.Close()                    // the port is now refusing connections

	err := s.SubmitWake(context.Background(), make([]byte, WakeV1Size))
	if err == nil {
		t.Fatal("SubmitWake against a closed server returned nil, want a transport error")
	}
	if _, ok := err.(*WakeSubmitError); ok {
		t.Fatalf("SubmitWake err = %v (%T), want a PLAIN error for a genuine transport failure, not a parsed gateway refusal", err, err)
	}

	// Drive must treat it exactly as pending/retryable.
	store := newFakeObligationStore()
	addr := testPushAddress(0xC2)
	seq, err2 := OpenSeqSource("")
	if err2 != nil {
		t.Fatal(err2)
	}
	m := NewWakeObligationMachine(WakeObligationConfig{
		Store: store, Submitter: s, WakeKey: testWakeKey(), Address: addr, Seq: seq, Now: time.Now,
	})
	if err := m.Trigger(); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if err := m.Drive(context.Background()); err == nil {
		t.Fatal("Drive against an unreachable gateway returned nil, want the transport error surfaced")
	}
	ob, _, _ := store.Get(addr)
	if ob.State != ObligationPending {
		t.Fatalf("state after a transport failure = %q, want %q (unconditionally retryable per ADR-015 P9)", ob.State, ObligationPending)
	}
}

// --- PG-TR-1: HTTPS only, and a bounded default client ------------------------------

// TestHTTPWakeSubmitter_RefusesANonHTTPSBaseURL pins PG-TR-1: the submit capability and
// the WakeV1 authenticator must never be put on a cleartext wire. The refusal is local
// (no request leaves the process) and PLAIN (never a *WakeSubmitError -- there was no
// gateway response to parse), so Drive treats a misconfigured BaseURL as retryable rather
// than silently abandoning the obligation.
func TestHTTPWakeSubmitter_RefusesANonHTTPSBaseURL(t *testing.T) {
	g := newWakeGatewayServer(t, writeWakeAccepted) // TLS server; never dialed by this test
	s := &HTTPWakeSubmitter{BaseURL: "http://" + g.srv.Listener.Addr().String(), SubmitCapability: "cap"}
	err := s.SubmitWake(context.Background(), make([]byte, WakeV1Size))
	if err == nil {
		t.Fatal("SubmitWake with an http:// BaseURL returned nil, want a refusal (PG-TR-1)")
	}
	if _, ok := err.(*WakeSubmitError); ok {
		t.Fatalf("SubmitWake err = %v (%T), want a PLAIN local refusal, not a parsed gateway response", err, err)
	}
	if len(g.reqs) != 0 {
		t.Fatalf("gateway saw %d requests, want 0: an http:// BaseURL must be refused BEFORE any request leaves", len(g.reqs))
	}
}

// TestHTTPWakeSubmitter_NeverFollowsARedirect is the regression test for the HIGH
// finding of the R3 GREEN review: left to Go's default redirect policy, a same-host
// scheme-downgrading 302 from a compromised or misbehaving gateway (a named adversary,
// spec §7.3) would have SubmitWake replay the submit capability and the WakeV1 envelope
// to the redirect target -- over cleartext if the target is http://. The submit surface
// is a single POST; a redirect is never a valid answer to it, so the target here must
// never see a request at all, regardless of scheme or host.
func TestHTTPWakeSubmitter_NeverFollowsARedirect(t *testing.T) {
	var hit bool
	var sawAuth string
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		sawAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(attacker.Close)

	g := newWakeGatewayServer(t, func(w http.ResponseWriter) {
		w.Header().Set("Location", attacker.URL+"/v1/wakes")
		w.WriteHeader(http.StatusFound)
	})
	s := newTestHTTPWakeSubmitter(g)

	err := s.SubmitWake(context.Background(), make([]byte, WakeV1Size))
	if err == nil {
		t.Fatal("SubmitWake on an unfollowed 302: want a non-nil error (no 2xx was ever seen), got nil")
	}
	var wse *WakeSubmitError
	if errors.As(err, &wse) {
		t.Fatalf("SubmitWake returned a *WakeSubmitError (%v); want the bodyless/no-parseable-body "+
			"case, since the unfollowed 302 carries no §3.6 Error JSON", wse)
	}
	if hit {
		t.Fatalf("the redirect target received a request (Authorization=%q): SubmitWake must never "+
			"follow a redirect (PG-TR-1)", sawAuth)
	}
}

// TestHTTPWakeSubmitter_DefaultClientHasAnExplicitTimeout pins that a nil Client does not
// mean an unbounded call: a restart re-drive path (PG-OBL-8) may invoke SubmitWake with a
// context carrying no deadline of its own, and this is what bounds that call anyway.
func TestHTTPWakeSubmitter_DefaultClientHasAnExplicitTimeout(t *testing.T) {
	if defaultWakeSubmitClient.Timeout <= 0 {
		t.Fatalf("defaultWakeSubmitClient.Timeout = %v, want a positive bound", defaultWakeSubmitClient.Timeout)
	}
}

// TestHTTPWakeSubmitter_RefusesAMisshapenEnvelopeLocally pins the local fail-closed shape
// check: a caller bug that hands SubmitWake anything other than exactly WakeV1Size bytes
// must be refused before a request leaves, never submitted and left for the gateway's own
// (terminal, per this file's mapping) wake_malformed refusal to catch.
func TestHTTPWakeSubmitter_RefusesAMisshapenEnvelopeLocally(t *testing.T) {
	g := newWakeGatewayServer(t, writeWakeAccepted)
	s := newTestHTTPWakeSubmitter(g)
	err := s.SubmitWake(context.Background(), make([]byte, WakeV1Size-1))
	if err == nil {
		t.Fatal("SubmitWake with a short envelope returned nil, want a local refusal")
	}
	if _, ok := err.(*WakeSubmitError); ok {
		t.Fatalf("SubmitWake err = %v (%T), want a PLAIN local refusal, not a parsed gateway response", err, err)
	}
	if len(g.reqs) != 0 {
		t.Fatalf("gateway saw %d requests, want 0: a misshapen envelope must be refused BEFORE any request leaves", len(g.reqs))
	}
}

// --- PG-RET-6/7-adjacent: gateway-controlled text is bounded -------------------------

// TestHTTPWakeSubmitter_SanitizesTheGatewayMessage pins that an oversized or
// control-character-laden `message` field from the gateway -- a named, potentially
// compromised party (spec §7.3) -- cannot inject unbounded or multi-line text into
// whatever surface reads WakeSubmitError.Message or .Error().
func TestHTTPWakeSubmitter_SanitizesTheGatewayMessage(t *testing.T) {
	hostile := "line one\nline two\x00\x07" + strings.Repeat("A", maxGatewayMessageLen+500)
	g := newWakeGatewayServer(t, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			Retryable bool   `json:"retryable"`
		}{Code: "wake_malformed", Message: hostile, Retryable: false})
	})
	s := newTestHTTPWakeSubmitter(g)
	err := s.SubmitWake(context.Background(), make([]byte, WakeV1Size))
	wse, ok := err.(*WakeSubmitError)
	if !ok {
		t.Fatalf("SubmitWake err = %v (%T), want a *WakeSubmitError", err, err)
	}
	if strings.ContainsAny(wse.Message, "\n\x00\x07") {
		t.Fatalf("Message %q still contains a control character", wse.Message)
	}
	if len(wse.Message) > maxGatewayMessageLen+len("...(truncated)") {
		t.Fatalf("Message is %d bytes, want it bounded near maxGatewayMessageLen=%d", len(wse.Message), maxGatewayMessageLen)
	}
}
