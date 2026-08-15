package pushgw_test

// POST /v1/wakes (spec §3.5, §5.1). Covers PG-SUB-1..5, PG-TR-3's 74-byte wake row and
// PG-TEST-11's length table, and the FCM-outcome error mapping (push_token_unregistered,
// upstream_unavailable, upstream_refused). PG-WAKE-8..10's AAD-tuple byte encoding is
// PG-FIX territory owned by internal/remotegw (producer) and internal/phonecore
// (receiver) — both off-limits to this bead — so this file's WakeV1 conformance is scoped
// to what the GATEWAY itself is responsible for: exact size, the two-byte pre-AEAD shape
// check (PG-WAKE-3), and unopened, byte-identical forwarding (PG-SUB-1).
//
// RED: package pushgw has no implementation yet; every pushgw.* reference below is an
// undefined symbol.

import (
	"bytes"
	"io"
	"net/http"
	"testing"

	"github.com/Nathandela/swarm/internal/pushgw"
)

// TestSubmitWake_HappyPath_Returns200ProviderAccepted.
func TestSubmitWake_HappyPath_Returns200ProviderAccepted(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-wake-happy")
	a := allocateAddress(t, h, r)
	env := buildWakeV1(t, decodeAddr(t, a.pushAddress), 1, h.clock.Now())
	resp := submitTestWake(h, a.submitCapability, env)
	requireStatus(t, resp, http.StatusOK)
	var out struct {
		Status string `json:"status"`
	}
	decodeJSON(t, resp, &out)
	if out.Status != "provider_accepted" {
		t.Fatalf("status = %q, want provider_accepted", out.Status)
	}
}

// TestSubmitWake_ForwardsByteIdenticalEnvelopeNeverOpeningIt is PG-SUB-1: the exact 74
// received octets reach the sender unchanged, and — because the trailing 40 bytes here are
// intentionally NOT a valid AEAD seal (see buildWakeV1) — a 200 alongside byte-identical
// forwarding is itself proof the gateway never attempted to open the envelope: it has no
// wake key to do so with (§7.1) and PG-WAKE-3 requires the shape check to happen before
// any AEAD is touched, not instead of forwarding.
func TestSubmitWake_ForwardsByteIdenticalEnvelopeNeverOpeningIt(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-wake-identical")
	a := allocateAddress(t, h, r)
	env := buildWakeV1(t, decodeAddr(t, a.pushAddress), 7, h.clock.Now())

	resp := submitTestWake(h, a.submitCapability, env)
	requireStatus(t, resp, http.StatusOK)

	calls := h.sender.calls()
	if len(calls) != 1 {
		t.Fatalf("sender received %d calls, want 1", len(calls))
	}
	if !bytes.Equal(calls[0].envelope, env[:]) {
		t.Fatalf("sender received %x, want byte-identical %x", calls[0].envelope, env[:])
	}
	if calls[0].token != r.fcmToken {
		t.Fatalf("sender received token %q, want %q", calls[0].token, r.fcmToken)
	}
}

// TestSubmitWake_AuthorizationMissingSchemePrefix_Returns401 is PG-AUTH-6: the wire form
// is fixed as "Authorization: Swarm-Capability <value>". A bare capability with no scheme
// prefix at all must not authenticate.
func TestSubmitWake_AuthorizationMissingSchemePrefix_Returns401(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-wake-noscheme")
	a := allocateAddress(t, h, r)
	env := buildWakeV1(t, decodeAddr(t, a.pushAddress), 1, h.clock.Now())
	resp := h.do("POST", "/v1/wakes", env[:], map[string]string{
		"Content-Type":  "application/octet-stream",
		"Authorization": a.submitCapability, // no "Swarm-Capability " prefix
	})
	requireStatus(t, resp, http.StatusUnauthorized)
	if e := decodeError(t, resp); e.Code != "unauthorized" {
		t.Fatalf("code = %q, want unauthorized", e.Code)
	}
}

// TestSubmitWake_UnregisteredToken_MarksMappingDeadAndRefusesSubsequentSubmits is
// PG-ROT-2/PG-RET-2: once FCM reports UNREGISTERED, the token bytes are deleted and the
// mapping is marked dead -- a SECOND submit must be refused push_token_unregistered
// WITHOUT calling the provider again.
func TestSubmitWake_UnregisteredToken_MarksMappingDeadAndRefusesSubsequentSubmits(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-wake-deadmap")
	a := allocateAddress(t, h, r)
	addr := decodeAddr(t, a.pushAddress)

	h.sender.setBehavior(func(string, []byte) error { return pushgw.ErrUnregistered })
	first := submitTestWake(h, a.submitCapability, buildWakeV1(t, addr, 1, h.clock.Now()))
	requireStatus(t, first, http.StatusGone)
	providerCallsAfterFirst := len(h.sender.calls())

	second := submitTestWake(h, a.submitCapability, buildWakeV1(t, addr, 2, h.clock.Now()))
	requireStatus(t, second, http.StatusGone)
	if e := decodeError(t, second); e.Code != "push_token_unregistered" || e.Retryable {
		t.Fatalf("got code=%q retryable=%v, want push_token_unregistered/false", e.Code, e.Retryable)
	}
	if got := len(h.sender.calls()); got != providerCallsAfterFirst {
		t.Fatalf("provider called again for a wake against a dead mapping: calls after first=%d, after second=%d", providerCallsAfterFirst, got)
	}
}

// TestSubmitWake_StaleUnregisteredVerdict_DoesNotKillAConcurrentlyRotatedToken is
// PG-ROT-2's race guard: a wake dispatched against token T1 whose UNREGISTERED verdict
// arrives AFTER the installation has already rotated to T2 must not delete T2 or mark the
// mapping dead -- T2 was never sent and may be perfectly live. The fake sender's behavior
// callback performs the rotation itself, synchronously, standing in for "the handset
// rotated while this send was in flight."
func TestSubmitWake_StaleUnregisteredVerdict_DoesNotKillAConcurrentlyRotatedToken(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-race-v1")
	a := allocateAddress(t, h, r)
	addr := decodeAddr(t, a.pushAddress)

	h.sender.setBehavior(func(string, []byte) error {
		path := "/v1/installations/" + r.installationID + "/token"
		body := rotateBody("fcm-token-race-v2")
		headers, _, _ := sign(t, r.priv, h.clock.Now(), signParams{method: "PUT", path: path, body: body})
		requireStatus(t, h.doJSON("PUT", path, body, headers), http.StatusNoContent)
		return pushgw.ErrUnregistered
	})
	first := submitTestWake(h, a.submitCapability, buildWakeV1(t, addr, 1, h.clock.Now()))
	requireStatus(t, first, http.StatusGone)

	// The rotation that raced past this stale verdict must still be live: a later wake
	// succeeds rather than being refused against a mapping the stale verdict wrongly killed.
	h.sender.setBehavior(nil)
	second := submitTestWake(h, a.submitCapability, buildWakeV1(t, addr, 2, h.clock.Now()))
	requireStatus(t, second, http.StatusOK)
}

// TestSubmitWake_MachineRevokeCapabilityPresentedAsSubmit_Returns401 is PG-AUTH-8: the
// machine-revoke capability SHALL NOT authorize POST /v1/wakes. wake.go compares only
// against SubmitCapHash, so presenting the OTHER capability of the same allocation under
// the submit scheme must still be refused -- distinctness enforced by construction, not
// merely by minting two different random values.
func TestSubmitWake_MachineRevokeCapabilityPresentedAsSubmit_Returns401(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-wake-crossscheme")
	a := allocateAddress(t, h, r)
	env := buildWakeV1(t, decodeAddr(t, a.pushAddress), 1, h.clock.Now())
	resp := submitTestWake(h, a.machineRevokeCapability, env)
	requireStatus(t, resp, http.StatusUnauthorized)
}

// TestSubmitWake_UnknownCapability_Returns401.
func TestSubmitWake_UnknownCapability_Returns401(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-wake-unknowncap")
	a := allocateAddress(t, h, r)
	env := buildWakeV1(t, decodeAddr(t, a.pushAddress), 1, h.clock.Now())
	resp := submitTestWake(h, "not-a-real-capability-AAAAAAAAAAAAAAAAAAAAAAA", env)
	requireStatus(t, resp, http.StatusUnauthorized)
	if e := decodeError(t, resp); e.Code != "unauthorized" {
		t.Fatalf("code = %q, want unauthorized", e.Code)
	}
}

// TestSubmitWake_CapabilityAddressMismatch_Returns401 is PG-SUB-3: a capability that
// verifies against a DIFFERENT address than the one named inside the envelope.
func TestSubmitWake_CapabilityAddressMismatch_Returns401(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-wake-mismatch")
	a := allocateAddress(t, h, r)
	b := allocateAddress(t, h, r)
	env := buildWakeV1(t, decodeAddr(t, b.pushAddress), 1, h.clock.Now())
	resp := submitTestWake(h, a.submitCapability, env) // a's capability, b's address
	requireStatus(t, resp, http.StatusUnauthorized)
}

// TestSubmitWake_WrongContentType_Returns400MalformedRequest is PG-TR-5.
func TestSubmitWake_WrongContentType_Returns400MalformedRequest(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-wake-ct")
	a := allocateAddress(t, h, r)
	env := buildWakeV1(t, decodeAddr(t, a.pushAddress), 1, h.clock.Now())
	resp := h.do("POST", "/v1/wakes", env[:], map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Swarm-Capability " + a.submitCapability,
	})
	requireStatus(t, resp, http.StatusBadRequest)
	if e := decodeError(t, resp); e.Code != "malformed_request" {
		t.Fatalf("code = %q, want malformed_request", e.Code)
	}
}

// TestSubmitWake_ContentEncodingPresent_Returns400MalformedRequest is PG-TR-4: a
// compressed fixed-size shape is a variable-size shape, and this refusal is DISTINCT from
// wake_malformed (§6.4's transition table routes the two codes differently).
func TestSubmitWake_ContentEncodingPresent_Returns400MalformedRequest(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-wake-enc")
	a := allocateAddress(t, h, r)
	env := buildWakeV1(t, decodeAddr(t, a.pushAddress), 1, h.clock.Now())
	resp := h.do("POST", "/v1/wakes", env[:], map[string]string{
		"Content-Type":     "application/octet-stream",
		"Content-Encoding": "gzip",
		"Authorization":    "Swarm-Capability " + a.submitCapability,
	})
	requireStatus(t, resp, http.StatusBadRequest)
	if e := decodeError(t, resp); e.Code != "malformed_request" {
		t.Fatalf("code = %q, want malformed_request", e.Code)
	}
}

// unknownLenReader deliberately does not implement Len(), so net/http.NewRequest cannot
// special-case it into a Content-Length header — it is sent chunked, simulating HTTP/2 or
// a client that omits Content-Length (PG-TR-3's "absent or unusable Content-Length" case).
type unknownLenReader struct{ r io.Reader }

func (u *unknownLenReader) Read(p []byte) (int, error) { return u.r.Read(p) }

// TestSubmitWake_LengthTable is PG-TEST-11: every length other than exactly 74 is
// wake_malformed, NEVER body_too_large, including a Content-Length-less body.
func TestSubmitWake_LengthTable(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-wake-lengths")
	a := allocateAddress(t, h, r)
	addr := decodeAddr(t, a.pushAddress)

	// submitTestWake's [74]byte parameter can't express a WRONG length, so every case
	// below sends raw bytes directly instead.
	seventyThree := buildWakeV1(t, addr, 2, h.clock.Now())
	rawCases := []struct {
		name string
		body []byte
	}{
		{"zero-bytes", []byte{}},
		{"seventy-three-bytes", seventyThree[:73]},
	}
	for _, c := range rawCases {
		t.Run(c.name, func(t *testing.T) {
			resp := h.do("POST", "/v1/wakes", c.body, map[string]string{
				"Content-Type":  "application/octet-stream",
				"Authorization": "Swarm-Capability " + a.submitCapability,
			})
			requireStatus(t, resp, http.StatusBadRequest)
			e := decodeError(t, resp)
			if e.Code != "wake_malformed" || e.Retryable {
				t.Fatalf("%s: got code=%q retryable=%v, want wake_malformed/false", c.name, e.Code, e.Retryable)
			}
		})
	}

	t.Run("seventy-five-bytes", func(t *testing.T) {
		env := buildWakeV1(t, addr, 3, h.clock.Now())
		body := append(append([]byte{}, env[:]...), 0x00)
		resp := h.do("POST", "/v1/wakes", body, map[string]string{
			"Content-Type":  "application/octet-stream",
			"Authorization": "Swarm-Capability " + a.submitCapability,
		})
		requireStatus(t, resp, http.StatusBadRequest)
		if e := decodeError(t, resp); e.Code != "wake_malformed" {
			t.Fatalf("code = %q, want wake_malformed", e.Code)
		}
	})

	t.Run("no-content-length-oversized", func(t *testing.T) {
		junk := bytes.Repeat([]byte{0xAB}, 100) // > 75, and unmeasurable up front
		req, err := http.NewRequest("POST", h.url+"/v1/wakes", &unknownLenReader{r: bytes.NewReader(junk)})
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("Authorization", "Swarm-Capability "+a.submitCapability)
		req.ContentLength = -1 // force chunked / unknown-length on the wire
		resp, err := h.client.Do(req)
		if err != nil {
			t.Fatalf("do: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		requireStatus(t, resp, http.StatusBadRequest)
		if e := decodeError(t, resp); e.Code != "wake_malformed" {
			t.Fatalf("code = %q, want wake_malformed", e.Code)
		}
	})

	t.Run("type-byte-not-wakev1", func(t *testing.T) {
		env := buildWakeV1(t, addr, 4, h.clock.Now())
		env[1] = 0x01 // TypeMailbox, not WakeV1's 0x03
		resp := submitTestWake(h, a.submitCapability, env)
		requireStatus(t, resp, http.StatusBadRequest)
		if e := decodeError(t, resp); e.Code != "wake_malformed" {
			t.Fatalf("code = %q, want wake_malformed", e.Code)
		}
	})

	t.Run("version-byte-not-one", func(t *testing.T) {
		env := buildWakeV1(t, addr, 5, h.clock.Now())
		env[0] = 0x02
		resp := submitTestWake(h, a.submitCapability, env)
		requireStatus(t, resp, http.StatusBadRequest)
		if e := decodeError(t, resp); e.Code != "wake_malformed" {
			t.Fatalf("code = %q, want wake_malformed", e.Code)
		}
	})
}

// TestSubmitWake_IdempotentByteIdenticalRetry_CallsSenderOnce is PG-SUB-4: a
// byte-identical retry may be answered from the idempotency cache rather than re-sent.
func TestSubmitWake_IdempotentByteIdenticalRetry_CallsSenderOnce(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-wake-idem")
	a := allocateAddress(t, h, r)
	env := buildWakeV1(t, decodeAddr(t, a.pushAddress), 1, h.clock.Now())

	first := submitTestWake(h, a.submitCapability, env)
	requireStatus(t, first, http.StatusOK)
	second := submitTestWake(h, a.submitCapability, env)
	requireStatus(t, second, http.StatusOK)

	if got := len(h.sender.calls()); got != 1 {
		t.Fatalf("sender called %d times for one byte-identical wake, want 1 (PG-SUB-4)", got)
	}
}

// TestSubmitWake_UpstreamUnavailable_Returns502Retryable: FCM transport failure or 5xx.
func TestSubmitWake_UpstreamUnavailable_Returns502Retryable(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-wake-unavail")
	a := allocateAddress(t, h, r)
	h.sender.setBehavior(func(string, []byte) error { return pushgw.ErrUnavailable })
	env := buildWakeV1(t, decodeAddr(t, a.pushAddress), 1, h.clock.Now())
	resp := submitTestWake(h, a.submitCapability, env)
	requireStatus(t, resp, http.StatusBadGateway)
	e := decodeError(t, resp)
	if e.Code != "upstream_unavailable" || !e.Retryable {
		t.Fatalf("got code=%q retryable=%v, want upstream_unavailable/true", e.Code, e.Retryable)
	}
}

// TestSubmitWake_UpstreamRefused_Returns502NotRetryable: a non-retryable 4xx that is not
// UNREGISTERED — retrying spends quota to reproduce an identical refusal.
func TestSubmitWake_UpstreamRefused_Returns502NotRetryable(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-wake-refused")
	a := allocateAddress(t, h, r)
	h.sender.setBehavior(func(string, []byte) error { return pushgw.ErrRefused })
	env := buildWakeV1(t, decodeAddr(t, a.pushAddress), 1, h.clock.Now())
	resp := submitTestWake(h, a.submitCapability, env)
	requireStatus(t, resp, http.StatusBadGateway)
	e := decodeError(t, resp)
	if e.Code != "upstream_refused" || e.Retryable {
		t.Fatalf("got code=%q retryable=%v, want upstream_refused/false", e.Code, e.Retryable)
	}
}
