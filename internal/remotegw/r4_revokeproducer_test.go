package remotegw

// FAILING-FIRST (TDD RED, GG-5) for Wave R4 deliverable 4's machine-side revoke
// producer, unit half (bead agents-tracker-u37c; the end-to-end shape against the real
// gateway is internal/phonecore/r4_revokeproducer_e2e_test.go). ADR-015 P6 and playbook
// 3.2 (:146-147): "Machine-side device revocation uses the machine-revoke capability
// and retries deletion durably after local epoch rotation."
//
// WHAT IS PINNED HERE:
//
//   - THE WIRE SHAPE: DELETE /v1/addresses/{base64url-addr}, Authorization
//     "Swarm-Revoke <capability>", no body -- push-gateway-api.md 3.4's machine arm,
//     the exact request internal/pushgw's handleRevoke verifies (revoke.go:50-52
//     reads the "Swarm-Revoke " prefix; a "Swarm-Capability" submit header must NOT
//     revoke, PG-AUTH-8/9).
//   - DURABILITY: the obligation is recorded to its store BEFORE the first network
//     attempt and survives a reopen; a crash between record and drive loses nothing.
//   - RETRY CLASSIFICATION: transport failure and 5xx leave the obligation retryable;
//     204 resolves it terminally; the retry after a success is the TOMBSTONE's 204,
//     handled as done, never as an error.
//   - EPOCH INDEPENDENCE: the capability the obligation presents is pairing material
//     conveyed at pairing time (ADR-015 P7's family), NOT epoch material -- rotating
//     the local epoch between record and drive must not change a byte of the request.
//     The seam is structural: the obligation record holds the capability verbatim and
//     Drive reads only the record.

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
)

// r4EncodeAddress is the wire form of a push address (16 opaque bytes, base64url
// unpadded) -- spelled here from the spec, not from a helper, so the assertion pins the
// contract rather than echoing the implementation.
func r4EncodeAddress(addr PushAddress) string {
	return base64.RawURLEncoding.EncodeToString(addr[:])
}

// r4RevokeCapture records every revoke request a test gateway double sees.
type r4RevokeCapture struct {
	mu       sync.Mutex
	requests []*http.Request
	status   int
}

func (c *r4RevokeCapture) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		clone := r.Clone(r.Context())
		c.requests = append(c.requests, clone)
		status := c.status
		c.mu.Unlock()
		w.WriteHeader(status)
	}
}

func (c *r4RevokeCapture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.requests)
}

func (c *r4RevokeCapture) last() *http.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.requests) == 0 {
		return nil
	}
	return c.requests[len(c.requests)-1]
}

func r4TestAddress(b byte) PushAddress {
	var addr PushAddress
	for i := range addr {
		addr[i] = b
	}
	return addr
}

// TestR4_HTTPAddressRevoker_PresentsTheMachineRevokeCapabilityExactly: the wire shape.
// One DELETE, the spec path, the Swarm-Revoke header (never Swarm-Capability), and an
// empty body -- handleRevoke refuses any body as malformed.
func TestR4_HTTPAddressRevoker_PresentsTheMachineRevokeCapabilityExactly(t *testing.T) {
	capture := &r4RevokeCapture{status: http.StatusNoContent}
	hs := httptest.NewServer(capture.handler())
	defer hs.Close()

	addr := r4TestAddress(0x4E)
	revoker := &HTTPAddressRevoker{
		BaseURL:                 hs.URL,
		MachineRevokeCapability: "cap-r4-machine-revoke-000000000000",
		Client:                  hs.Client(),
	}
	if err := revoker.RevokeAddress(context.Background(), addr); err != nil {
		t.Fatalf("RevokeAddress against a 204 gateway: %v", err)
	}

	if capture.count() != 1 {
		t.Fatalf("gateway saw %d requests, want exactly 1", capture.count())
	}
	req := capture.last()
	if req.Method != http.MethodDelete {
		t.Errorf("method %s, want DELETE", req.Method)
	}
	if want := "/v1/addresses/" + r4EncodeAddress(addr); req.URL.Path != want {
		t.Errorf("path %q, want %q", req.URL.Path, want)
	}
	if got := req.Header.Get("Authorization"); got != "Swarm-Revoke cap-r4-machine-revoke-000000000000" {
		t.Errorf("Authorization %q, want the Swarm-Revoke machine arm -- presenting the submit "+
			"capability's Swarm-Capability header is PG-AUTH-8's forbidden crossover", got)
	}
	if req.ContentLength > 0 {
		t.Errorf("the revoke carried a %d-byte body; handleRevoke refuses any body as malformed", req.ContentLength)
	}
}

// TestR4_RevokeObligation_IsDurableBeforeTheFirstAttempt: Record persists the
// obligation -- address and capability verbatim -- before any network attempt, and a
// store reopened over the same file (the crash-between-record-and-drive world) still
// holds it, still undone.
func TestR4_RevokeObligation_IsDurableBeforeTheFirstAttempt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "revoke-obligation.json")
	store, err := OpenRevokeObligationStore(path)
	if err != nil {
		t.Fatalf("OpenRevokeObligationStore: %v", err)
	}

	addr := r4TestAddress(0x51)
	machine := NewRevokeObligationMachine(RevokeObligationConfig{
		Store: store,
		// NO Revoker on purpose: Record must not need the network at all.
		Address: addr,
	})
	if err := machine.Record(); err != nil {
		t.Fatalf("Record: %v", err)
	}

	reopened, err := OpenRevokeObligationStore(path)
	if err != nil {
		t.Fatalf("reopening the store: %v", err)
	}
	ob, ok := reopened.Pending()
	if !ok {
		t.Fatalf("the recorded obligation is GONE after a reopen; a crash between the revoke " +
			"decision and the delete must lose nothing (durable retry, playbook :146-147)")
	}
	if ob.Address != addr {
		t.Errorf("reopened obligation names address %v, want %v", ob.Address, addr)
	}
}

// TestR4_RevokeObligation_RetryClassification: a transport failure and a 5xx leave the
// obligation pending (retryable); a 204 resolves it terminally; and the retry AFTER a
// success -- the tombstone's 204 -- is done, never an error.
func TestR4_RevokeObligation_RetryClassification(t *testing.T) {
	capture := &r4RevokeCapture{status: http.StatusInternalServerError}
	hs := httptest.NewServer(capture.handler())
	defer hs.Close()

	path := filepath.Join(t.TempDir(), "revoke-obligation.json")
	store, err := OpenRevokeObligationStore(path)
	if err != nil {
		t.Fatalf("OpenRevokeObligationStore: %v", err)
	}
	addr := r4TestAddress(0x52)
	machine := NewRevokeObligationMachine(RevokeObligationConfig{
		Store: store,
		Revoker: &HTTPAddressRevoker{
			BaseURL:                 hs.URL,
			MachineRevokeCapability: "cap-r4-retry-0000000000000000000",
			Client:                  hs.Client(),
		},
		Address: addr,
	})
	if err := machine.Record(); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// 5xx: still pending.
	if err := machine.Drive(context.Background()); err == nil {
		t.Fatalf("a 500 drive reported success")
	}
	if _, ok := store.Pending(); !ok {
		t.Fatalf("a 500 resolved the obligation; 5xx is retryable, not terminal")
	}

	// 204: terminal.
	capture.mu.Lock()
	capture.status = http.StatusNoContent
	capture.mu.Unlock()
	if err := machine.Drive(context.Background()); err != nil {
		t.Fatalf("the 204 drive failed: %v", err)
	}
	if _, ok := store.Pending(); ok {
		t.Fatalf("a 204 left the obligation pending; the delete is done")
	}

	// A later re-drive (restart racing the terminal write) is a no-op, not a crash.
	if err := machine.Drive(context.Background()); err != nil {
		t.Errorf("re-driving a resolved obligation errored: %v", err)
	}
}

// TestR4_RevokeObligation_RequestBytesAreIndependentOfEpochRotation: playbook :146-147
// says deletion is retried "durably after local epoch rotation". The producer's request
// is built from the obligation record ALONE -- so two drives of the same record, with
// arbitrary local key churn between them, present byte-identical method, path and
// Authorization. The assertion is over the wire artifact, because that is what the
// gateway verifies.
func TestR4_RevokeObligation_RequestBytesAreIndependentOfEpochRotation(t *testing.T) {
	capture := &r4RevokeCapture{status: http.StatusServiceUnavailable}
	hs := httptest.NewServer(capture.handler())
	defer hs.Close()

	path := filepath.Join(t.TempDir(), "revoke-obligation.json")
	store, err := OpenRevokeObligationStore(path)
	if err != nil {
		t.Fatalf("OpenRevokeObligationStore: %v", err)
	}
	addr := r4TestAddress(0x53)
	machine := NewRevokeObligationMachine(RevokeObligationConfig{
		Store: store,
		Revoker: &HTTPAddressRevoker{
			BaseURL:                 hs.URL,
			MachineRevokeCapability: "cap-r4-epoch-0000000000000000000",
			Client:                  hs.Client(),
		},
		Address: addr,
	})
	if err := machine.Record(); err != nil {
		t.Fatalf("Record: %v", err)
	}

	_ = machine.Drive(context.Background()) // 503: retryable by design.
	first := capture.last()

	// "Local epoch rotation" between attempts: the producer holds NO epoch material, so
	// there is nothing to rotate that could reach it -- the second attempt must repeat
	// the first byte for byte.
	_ = machine.Drive(context.Background())
	second := capture.last()

	if capture.count() != 2 {
		t.Fatalf("gateway saw %d attempts, want 2", capture.count())
	}
	if first.Method != second.Method || first.URL.Path != second.URL.Path ||
		first.Header.Get("Authorization") != second.Header.Get("Authorization") {
		t.Errorf("two drives of one obligation differ on the wire: %s %s %q vs %s %s %q",
			first.Method, first.URL.Path, first.Header.Get("Authorization"),
			second.Method, second.URL.Path, second.Header.Get("Authorization"))
	}
}
