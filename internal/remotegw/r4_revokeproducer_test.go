package remotegw

// Wire coverage for the HTTP helper used by the daemon's durable pairing-push custody.
// Canonical custody and restart recovery are tested in internal/skeleton; this file pins
// the helper's exact DELETE request and authorization arm.

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
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
