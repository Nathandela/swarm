package pushgw_test

// POST /v1/installations (spec §3.1). Covers PG-REG-1..3, PG-AUTH-11..13, PG-TR-3's 12 KiB
// register row (and the exact 10423-octet arithmetic §1 derives it from), and PG-TR-5's
// content-type gate before registration-proof verification.
//
// RED: package pushgw has no implementation yet; every pushgw.* reference below is an
// undefined symbol.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/pushgw"
	"github.com/Nathandela/swarm/internal/pushreg"
)

func TestRegister_ProofRejectsNonHolderAndMutationsBeforeAttestation(t *testing.T) {
	h := newHarness(t, nil)
	priv, pub := genInstallationKey(t)
	body, _ := json.Marshal(map[string]any{
		"installation_public_key": pub,
		"fcm_token":               "fcm-token-proof",
		"attestation":             map[string]any{"kind": "play_integrity", "token": "verdict-proof"},
	})
	idem := fixedIdemKey("proof")
	valid := registrationHeaders(t, priv, idem, body)["Swarm-Registration-Proof"]
	other, _ := genInstallationKey(t)
	prefix := "p256-sha256 "
	encoded := strings.TrimPrefix(valid, prefix)
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	noncanonicalBytes := []byte(encoded)
	last := strings.IndexByte(alphabet, noncanonicalBytes[len(noncanonicalBytes)-1])
	if last < 0 || last%4 != 0 {
		t.Fatalf("canonical final base64url digit has index %d, want a multiple of 4", last)
	}
	noncanonicalBytes[len(noncanonicalBytes)-1] = alphabet[last+1]
	if same, err := base64.RawURLEncoding.DecodeString(string(noncanonicalBytes)); err != nil || !bytes.Equal(same, decoded) {
		t.Fatalf("noncanonical fixture must decode to the same 64 bytes: equal=%v err=%v", bytes.Equal(same, decoded), err)
	}

	highSBytes, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	s := new(big.Int).SetBytes(highSBytes[32:])
	s.Sub(priv.Curve.Params().N, s).FillBytes(highSBytes[32:])
	highS := prefix + base64RawURL(highSBytes)
	domainMutation := bytes.Replace(pushreg.RegistrationProofMessage(idem, body), []byte("swarm-pg-register-v1"), []byte("swarm-pg-register-v2"), 1)

	tests := []struct {
		name, proof string
	}{
		{name: "missing"},
		{name: "different private key", proof: registrationHeaders(t, other, idem, body)["Swarm-Registration-Proof"]},
		{name: "different body", proof: registrationHeaders(t, priv, idem, append(append([]byte(nil), body...), ' '))["Swarm-Registration-Proof"]},
		{name: "different idempotency key", proof: registrationHeaders(t, priv, fixedIdemKey("other"), body)["Swarm-Registration-Proof"]},
		{name: "different domain", proof: prefix + base64RawURL(signP256(t, priv, domainMutation))},
		{name: "high s", proof: highS},
		{name: "bad scheme", proof: "ecdsa-sha256 " + encoded},
		{name: "short signature", proof: prefix + encoded[:len(encoded)-1]},
		{name: "long signature", proof: prefix + encoded + "A"},
		{name: "padded base64", proof: prefix + encoded + "="},
		{name: "noncanonical trailing bits", proof: prefix + string(noncanonicalBytes)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attestCalls := 0
			h.attest.setFunc(func(context.Context, string) (pushgw.VerdictBinding, error) {
				attestCalls++
				return pushgw.VerdictBinding{}, nil
			})
			headers := map[string]string{"Idempotency-Key": idem}
			if tt.proof != "" {
				headers["Swarm-Registration-Proof"] = tt.proof
			}
			resp := h.doJSON(http.MethodPost, "/v1/installations", body, headers)
			requireStatus(t, resp, http.StatusUnauthorized)
			if attestCalls != 0 {
				t.Fatalf("attestation calls=%d, want 0", attestCalls)
			}
		})
	}
}

func TestRegister_ProofRejectsDuplicateHeaderAndRawQuery(t *testing.T) {
	h := newHarness(t, nil)
	priv, pub := genInstallationKey(t)
	body, _ := json.Marshal(map[string]any{
		"installation_public_key": pub,
		"fcm_token":               "fcm-token-ambiguous",
		"attestation":             map[string]any{"kind": "play_integrity", "token": "verdict-ambiguous"},
	})
	idem := fixedIdemKey("ambiguous")
	proof := registrationHeaders(t, priv, idem, body)["Swarm-Registration-Proof"]

	for _, tc := range []struct {
		name, path     string
		duplicateProof bool
		duplicateIdem  bool
		want           int
	}{
		{name: "duplicate proof", path: "/v1/installations", duplicateProof: true, want: http.StatusUnauthorized},
		{name: "duplicate idempotency key", path: "/v1/installations", duplicateIdem: true, want: http.StatusBadRequest},
		{name: "query", path: "/v1/installations?ignored=1", want: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tc.path, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Idempotency-Key", idem)
			req.Header.Add("Swarm-Registration-Proof", proof)
			if tc.duplicateProof {
				req.Header.Add("Swarm-Registration-Proof", proof)
			}
			if tc.duplicateIdem {
				req.Header.Add("Idempotency-Key", idem)
			}
			rr := httptest.NewRecorder()
			h.srv.ServeHTTP(rr, req)
			if rr.Code != tc.want {
				t.Fatalf("status=%d body=%s, want %d", rr.Code, rr.Body.String(), tc.want)
			}
		})
	}
}

func TestRegister_LegacyTestServerHasNoUnsignedFallback(t *testing.T) {
	clock := newFakeClock()
	attestor := newFakeAttestor()
	attestationCalls := 0
	attestor.setFunc(func(context.Context, string) (pushgw.VerdictBinding, error) {
		attestationCalls++
		return pushgw.VerdictBinding{}, nil
	})
	server, err := pushgw.NewServer(pushgw.Config{
		DBPath: t.TempDir() + "/push.db", Sender: newFakeSender(), Attest: attestor, Now: clock.Now, Quotas: defaultTestQuotas(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	_, pub := genInstallationKey(t)
	body, _ := json.Marshal(map[string]any{
		"installation_public_key": pub,
		"fcm_token":               "legacy-test-token",
		"attestation":             map[string]any{"kind": "play_integrity", "token": "legacy-test-verdict"},
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/installations", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", fixedIdemKey("legacy-proof"))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if attestationCalls != 0 {
		t.Fatalf("attestation calls=%d, want 0", attestationCalls)
	}
}

// TestRegister_HappyPath_ReturnsOpaqueIDAndAllocatesNoAddress is PG-REG-1 plus the basic
// 201 shape: an opaque installation_id and a refresh_before, and nothing address-shaped.
func TestRegister_HappyPath_ReturnsOpaqueIDAndAllocatesNoAddress(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-happy-path")
	if r.installationID == r.pubKey {
		t.Fatalf("installation_id must be gateway-minted, not echo the public key")
	}
	if r.refreshBefore == "" {
		t.Fatalf("refresh_before must be present (180-day inactivity floor)")
	}
}

// TestRegister_RepeatedIdempotencyKey_ReturnsSameInstallationID is PG-REG-2: a lost
// response on a flaky handset network must not mint a second durable installation.
func TestRegister_RepeatedIdempotencyKey_ReturnsSameInstallationID(t *testing.T) {
	h := newHarness(t, nil)
	priv, pub := genInstallationKey(t)
	body, _ := json.Marshal(map[string]any{
		"installation_public_key": pub,
		"fcm_token":               "fcm-token-idem",
		"attestation": map[string]any{
			"kind":  "play_integrity",
			"token": "verdict-idem",
		},
	})
	hash := jcsRequestHash(t, body)
	h.attest.setFunc(func(context.Context, string) (pushgw.VerdictBinding, error) {
		return pushgw.VerdictBinding{RequestHash: hash, LicensedBuild: true}, nil
	})
	idemKey := fixedIdemKey("idem-key")
	first := h.doJSON("POST", "/v1/installations", body, registrationHeaders(t, priv, idemKey, body))
	requireStatus(t, first, http.StatusCreated)
	var firstOut struct {
		InstallationID string `json:"installation_id"`
	}
	decodeJSON(t, first, &firstOut)

	second := h.doJSON("POST", "/v1/installations", body, registrationHeaders(t, priv, idemKey, body))
	requireStatus(t, second, http.StatusCreated)
	var secondOut struct {
		InstallationID string `json:"installation_id"`
	}
	decodeJSON(t, second, &secondOut)

	if firstOut.InstallationID != secondOut.InstallationID {
		t.Fatalf("repeated Idempotency-Key minted two installations: %q vs %q", firstOut.InstallationID, secondOut.InstallationID)
	}
}

// TestRegister_AttestationHashMismatch_Returns403AttestationInvalid is PG-AUTH-11's
// recompute-and-refuse-on-mismatch rule: the verdict the fake "Google" returns is bound to
// a different request than the one actually sent.
func TestRegister_AttestationHashMismatch_Returns403AttestationInvalid(t *testing.T) {
	h := newHarness(t, nil)
	priv, pub := genInstallationKey(t)
	body, _ := json.Marshal(map[string]any{
		"installation_public_key": pub,
		"fcm_token":               "fcm-token-mismatch",
		"attestation": map[string]any{
			"kind":  "play_integrity",
			"token": "verdict-mismatch",
		},
	})
	h.attest.setFunc(func(context.Context, string) (pushgw.VerdictBinding, error) {
		return pushgw.VerdictBinding{RequestHash: [32]byte{0xDE, 0xAD}, LicensedBuild: true}, nil
	})
	idem := fixedIdemKey("mismatch-key")
	resp := h.doJSON("POST", "/v1/installations", body, registrationHeaders(t, priv, idem, body))
	requireStatus(t, resp, http.StatusForbidden)
	if e := decodeError(t, resp); e.Code != "attestation_invalid" || e.Retryable {
		t.Fatalf("got code=%q retryable=%v, want attestation_invalid/false", e.Code, e.Retryable)
	}
}

// TestRegister_UnlicensedBuild_Returns403AttestationInvalid: the verdict names a build
// that is not the licensed Play-signed package (PG-AUTH-11's third refusal reason).
func TestRegister_UnlicensedBuild_Returns403AttestationInvalid(t *testing.T) {
	h := newHarness(t, nil)
	priv, pub := genInstallationKey(t)
	body, _ := json.Marshal(map[string]any{
		"installation_public_key": pub,
		"fcm_token":               "fcm-token-unlicensed",
		"attestation": map[string]any{
			"kind":  "play_integrity",
			"token": "verdict-unlicensed",
		},
	})
	hash := jcsRequestHash(t, body)
	h.attest.setFunc(func(context.Context, string) (pushgw.VerdictBinding, error) {
		return pushgw.VerdictBinding{RequestHash: hash, LicensedBuild: false}, nil
	})
	idem := fixedIdemKey("unlicensed-key")
	resp := h.doJSON("POST", "/v1/installations", body, registrationHeaders(t, priv, idem, body))
	requireStatus(t, resp, http.StatusForbidden)
	if e := decodeError(t, resp); e.Code != "attestation_invalid" {
		t.Fatalf("code = %q, want attestation_invalid", e.Code)
	}
}

// TestRegister_AttestationUnavailable_Returns403Retryable: Google's verification endpoint
// could not be reached (PG-ERR table row). The handset retries; it is never enrolled
// unattested (PG-AUTH-13).
func TestRegister_AttestationUnavailable_Returns403Retryable(t *testing.T) {
	h := newHarness(t, nil)
	priv, pub := genInstallationKey(t)
	body, _ := json.Marshal(map[string]any{
		"installation_public_key": pub,
		"fcm_token":               "fcm-token-unavailable",
		"attestation": map[string]any{
			"kind":  "play_integrity",
			"token": "verdict-unavailable",
		},
	})
	h.attest.setFunc(func(context.Context, string) (pushgw.VerdictBinding, error) {
		return pushgw.VerdictBinding{}, pushgw.ErrAttestationUnavailable
	})
	idem := fixedIdemKey("unavail-key")
	resp := h.doJSON("POST", "/v1/installations", body, registrationHeaders(t, priv, idem, body))
	requireStatus(t, resp, http.StatusForbidden)
	e := decodeError(t, resp)
	if e.Code != "attestation_unavailable" || !e.Retryable {
		t.Fatalf("got code=%q retryable=%v, want attestation_unavailable/true", e.Code, e.Retryable)
	}
}

// TestRegister_BodyOverTwelveKiB_Returns413BeforeParsing pins PG-TR-3's register row: the
// bound is enforced on raw bytes, before any JSON parsing — so a body that is not even
// syntactically valid JSON still gets 413, not a parse-shaped 400.
func TestRegister_BodyOverTwelveKiB_Returns413BeforeParsing(t *testing.T) {
	h := newHarness(t, nil)
	oversized := bytes.Repeat([]byte("x"), 12*1024+1)
	resp := h.doJSON("POST", "/v1/installations", oversized, map[string]string{"Idempotency-Key": fixedIdemKey("oversize-key")})
	requireStatus(t, resp, http.StatusRequestEntityTooLarge)
	if e := decodeError(t, resp); e.Code != "body_too_large" || e.Retryable {
		t.Fatalf("got code=%q retryable=%v, want body_too_large/false", e.Code, e.Retryable)
	}
}

// TestRegister_MaximalSchemaLegalBody_FitsUnderTwelveKiB reproduces §1's own arithmetic:
// every field at its declared maxLength sums to exactly 10423 octets, comfortably inside
// the 12288-octet (12 KiB) cap — the reason the register row is 12 KiB and not 8. A gateway
// that used 8 KiB here would refuse a body this document itself declares valid.
func TestRegister_MaximalSchemaLegalBody_FitsUnderTwelveKiB(t *testing.T) {
	h := newHarness(t, nil)
	priv, pub := genInstallationKey(t) // pattern-legal 87 chars regardless of value chosen
	maxFCMToken := strings.Repeat("a", 4096)
	maxAttestationToken := strings.Repeat("b", 6144)
	body, err := json.Marshal(map[string]any{
		"installation_public_key": pub,
		"fcm_token":               maxFCMToken,
		"attestation": map[string]any{
			"kind":  "play_integrity",
			"token": maxAttestationToken,
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(body) != 10423 {
		t.Fatalf("fixture arithmetic drifted from §1: body is %d octets, spec derives 10423", len(body))
	}
	if len(body) >= 12*1024 {
		t.Fatalf("fixture body (%d octets) does not fit under the 12 KiB cap it is meant to test", len(body))
	}
	hash := jcsRequestHash(t, body)
	h.attest.setFunc(func(context.Context, string) (pushgw.VerdictBinding, error) {
		return pushgw.VerdictBinding{RequestHash: hash, LicensedBuild: true}, nil
	})
	idem := fixedIdemKey("maximal-key")
	resp := h.doJSON("POST", "/v1/installations", body, registrationHeaders(t, priv, idem, body))
	if resp.StatusCode == http.StatusRequestEntityTooLarge {
		t.Fatalf("a schema-maximal body (%d octets) was refused 413 under the 12 KiB cap", len(body))
	}
	requireStatus(t, resp, http.StatusCreated)
}

// TestRegister_WrongContentType_Returns400MalformedRequest is PG-TR-5 before proof verification.
func TestRegister_WrongContentType_Returns400MalformedRequest(t *testing.T) {
	h := newHarness(t, nil)
	_, pub := genInstallationKey(t)
	body, _ := json.Marshal(map[string]any{
		"installation_public_key": pub,
		"fcm_token":               "fcm-token-ct",
		"attestation":             map[string]any{"kind": "play_integrity", "token": "verdict-ct"},
	})
	resp := h.do("POST", "/v1/installations", body, map[string]string{
		"Content-Type":    "text/plain",
		"Idempotency-Key": fixedIdemKey("ct-key"),
	})
	requireStatus(t, resp, http.StatusBadRequest)
	if e := decodeError(t, resp); e.Code != "malformed_request" {
		t.Fatalf("code = %q, want malformed_request", e.Code)
	}
}

// TestRegister_UnknownField_Returns400MalformedRequest: §3's additionalProperties:false
// discipline — an unknown field is either a client bug or a locator-smuggling attempt.
func TestRegister_UnknownField_Returns400MalformedRequest(t *testing.T) {
	h := newHarness(t, nil)
	_, pub := genInstallationKey(t)
	body, _ := json.Marshal(map[string]any{
		"installation_public_key": pub,
		"fcm_token":               "fcm-token-extra",
		"attestation":             map[string]any{"kind": "play_integrity", "token": "verdict-extra"},
		"hostname":                "shouldnt-be-here",
	})
	resp := h.doJSON("POST", "/v1/installations", body, map[string]string{"Idempotency-Key": fixedIdemKey("extra-key")})
	requireStatus(t, resp, http.StatusBadRequest)
	if e := decodeError(t, resp); e.Code != "malformed_request" {
		t.Fatalf("code = %q, want malformed_request", e.Code)
	}
}

// TestRegister_MissingRequiredField_Returns400MalformedRequest.
func TestRegister_MissingRequiredField_Returns400MalformedRequest(t *testing.T) {
	h := newHarness(t, nil)
	body, _ := json.Marshal(map[string]any{
		"fcm_token":   "fcm-token-missing-key",
		"attestation": map[string]any{"kind": "play_integrity", "token": "verdict-missing"},
	})
	resp := h.doJSON("POST", "/v1/installations", body, map[string]string{"Idempotency-Key": fixedIdemKey("missing-key")})
	requireStatus(t, resp, http.StatusBadRequest)
	if e := decodeError(t, resp); e.Code != "malformed_request" {
		t.Fatalf("code = %q, want malformed_request", e.Code)
	}
}

// TestRegister_MissingIdempotencyKey_Returns400: the header is `required: true` (§3.6).
func TestRegister_MissingIdempotencyKey_Returns400(t *testing.T) {
	h := newHarness(t, nil)
	_, pub := genInstallationKey(t)
	body, _ := json.Marshal(map[string]any{
		"installation_public_key": pub,
		"fcm_token":               "fcm-token-no-idem",
		"attestation":             map[string]any{"kind": "play_integrity", "token": "verdict-no-idem"},
	})
	resp := h.doJSON("POST", "/v1/installations", body, nil)
	requireStatus(t, resp, http.StatusBadRequest)
}

// TestRegister_IdempotencyKeyWrongLength_Returns400 pins §3.6's normative pattern
// (^[A-Za-z0-9_-]{22}$): a 1-character key must not be accepted as a valid retry token,
// or the idempotency cache's collision space becomes whatever a client sends.
func TestRegister_IdempotencyKeyWrongLength_Returns400(t *testing.T) {
	h := newHarness(t, nil)
	_, pub := genInstallationKey(t)
	body, _ := json.Marshal(map[string]any{
		"installation_public_key": pub,
		"fcm_token":               "fcm-token-idemfmt",
		"attestation":             map[string]any{"kind": "play_integrity", "token": "verdict-idemfmt"},
	})
	resp := h.doJSON("POST", "/v1/installations", body, map[string]string{"Idempotency-Key": "x"})
	requireStatus(t, resp, http.StatusBadRequest)
	if e := decodeError(t, resp); e.Code != "malformed_request" {
		t.Fatalf("code = %q, want malformed_request", e.Code)
	}
}

// A reused key with a different body is rejected from durable idempotency state before
// Play Integrity. Besides preventing identity disclosure, this ordering is required for
// standard Integrity tokens whose verdict can be cleared on a repeated decode.
func TestRegister_IdempotencyKeyReplayedWithDifferentBody_ConflictsBeforeAttestation(t *testing.T) {
	h := newHarness(t, nil)

	priv1, pub1 := genInstallationKey(t)
	body1, _ := json.Marshal(map[string]any{
		"installation_public_key": pub1,
		"fcm_token":               "victim-fcm-token",
		"attestation":             map[string]any{"kind": "play_integrity", "token": "verdict-victim"},
	})
	hash1 := jcsRequestHash(t, body1)
	h.attest.setFunc(func(context.Context, string) (pushgw.VerdictBinding, error) {
		return pushgw.VerdictBinding{RequestHash: hash1, LicensedBuild: true}, nil
	})
	idem := fixedIdemKey("sharedkey")
	victim := h.doJSON("POST", "/v1/installations", body1, registrationHeaders(t, priv1, idem, body1))
	requireStatus(t, victim, http.StatusCreated)
	var victimOut struct {
		InstallationID string `json:"installation_id"`
	}
	decodeJSON(t, victim, &victimOut)

	attestCalls := 0
	h.attest.setFunc(func(context.Context, string) (pushgw.VerdictBinding, error) {
		attestCalls++
		return pushgw.VerdictBinding{}, pushgw.ErrAttestationUnavailable
	})
	priv2, pub2 := genInstallationKey(t)
	body2, _ := json.Marshal(map[string]any{
		"installation_public_key": pub2,
		"fcm_token":               "attacker-fcm-token",
		"attestation":             map[string]any{"kind": "play_integrity", "token": "verdict-attacker"},
	})
	attacker := h.doJSON("POST", "/v1/installations", body2, registrationHeaders(t, priv2, idem, body2))

	if attestCalls != 0 {
		t.Fatalf("attestation calls = %d, want 0 for a durable body conflict", attestCalls)
	}
	requireStatus(t, attacker, http.StatusConflict)
	if e := decodeError(t, attacker); e.Code != "idempotency_conflict" {
		t.Fatalf("code = %q, want idempotency_conflict", e.Code)
	}
}

// TestRegister_IdempotencyKeyReplayedWithSameBody_StillReturnsSameID is the non-regression
// half of the fix above: a genuine byte-identical retry (PG-REG-2) must still work.
func TestRegister_IdempotencyKeyReplayedWithSameBody_StillReturnsSameID(t *testing.T) {
	h := newHarness(t, nil)
	priv, pub := genInstallationKey(t)
	body, _ := json.Marshal(map[string]any{
		"installation_public_key": pub,
		"fcm_token":               "fcm-token-samebody",
		"attestation":             map[string]any{"kind": "play_integrity", "token": "verdict-samebody"},
	})
	hash := jcsRequestHash(t, body)
	h.attest.setFunc(func(context.Context, string) (pushgw.VerdictBinding, error) {
		return pushgw.VerdictBinding{RequestHash: hash, LicensedBuild: true}, nil
	})
	idem := fixedIdemKey("samebodykey")
	first := h.doJSON("POST", "/v1/installations", body, registrationHeaders(t, priv, idem, body))
	requireStatus(t, first, http.StatusCreated)
	var firstOut struct {
		InstallationID string `json:"installation_id"`
	}
	decodeJSON(t, first, &firstOut)

	// A second attestation call would fail, proving the retry was served from the cache.
	h.attest.setFunc(func(context.Context, string) (pushgw.VerdictBinding, error) {
		return pushgw.VerdictBinding{}, pushgw.ErrAttestationUnavailable
	})
	second := h.doJSON("POST", "/v1/installations", body, registrationHeaders(t, priv, idem, body))
	requireStatus(t, second, http.StatusCreated)
	var secondOut struct {
		InstallationID string `json:"installation_id"`
	}
	decodeJSON(t, second, &secondOut)
	if firstOut.InstallationID != secondOut.InstallationID {
		t.Fatalf("byte-identical retry minted a different installation_id: %q vs %q", firstOut.InstallationID, secondOut.InstallationID)
	}
}

func TestRegister_ClosedBetaRefusesBeforeAttestation(t *testing.T) {
	attestCalls := 0
	h := newHarness(t, func(cfg *pushgw.Config) {
		cfg.RegistrationAdmission = func(string) bool { return false }
	})
	h.attest.setFunc(func(context.Context, string) (pushgw.VerdictBinding, error) {
		attestCalls++
		return pushgw.VerdictBinding{}, nil
	})
	_, pub := genInstallationKey(t)
	body, _ := json.Marshal(map[string]any{
		"installation_public_key": pub,
		"fcm_token":               "fcm-token-closed-beta",
		"attestation":             map[string]any{"kind": "play_integrity", "token": "verdict-closed-beta"},
	})
	resp := h.doJSON("POST", "/v1/installations", body, map[string]string{"Idempotency-Key": fixedIdemKey("closed-beta")})
	requireStatus(t, resp, http.StatusForbidden)
	if e := decodeError(t, resp); e.Code != "beta_closed" || e.Retryable {
		t.Fatalf("got code=%q retryable=%v, want beta_closed/false", e.Code, e.Retryable)
	}
	if attestCalls != 0 {
		t.Fatalf("attestation calls=%d, want 0 before operator admission", attestCalls)
	}
}

func TestRegister_AttestationPastAttemptExpiryCannotCommit(t *testing.T) {
	h := newHarness(t, nil)
	priv, pub := genInstallationKey(t)
	body, _ := json.Marshal(map[string]any{
		"installation_public_key": pub,
		"fcm_token":               "fcm-token-slow-attestation",
		"attestation":             map[string]any{"kind": "play_integrity", "token": "verdict-slow-attestation"},
	})
	wantHash := jcsRequestHash(t, body)
	h.attest.setFunc(func(context.Context, string) (pushgw.VerdictBinding, error) {
		h.clock.advance(10 * time.Minute)
		return pushgw.VerdictBinding{RequestHash: wantHash, LicensedBuild: true}, nil
	})
	idem := fixedIdemKey("slow-attestation")
	resp := h.doJSON("POST", "/v1/installations", body, registrationHeaders(t, priv, idem, body))
	requireStatus(t, resp, http.StatusServiceUnavailable)
	if e := decodeError(t, resp); e.Code != "service_unavailable" || !e.Retryable {
		t.Fatalf("got code=%q retryable=%v, want service_unavailable/true", e.Code, e.Retryable)
	}
}
