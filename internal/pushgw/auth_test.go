package pushgw_test

// PG-AUTH-2's malleability control. helpers_test.go's sign() always normalizes s LOW
// (n - s when s is high), so nothing else in this suite exercises auth.go's explicit
// sInt.Cmp(half) > 0 refusal -- deleting that check would leave the rest of the suite green.
// This file signs a request normally, then flips its s value to the HIGH complement
// (n - s): under ECDSA's well-known malleability property, (r, n-s) verifies for the exact
// same message and key whenever (r, s) does, so this specifically exercises PG-AUTH-2's
// explicit high-s refusal, not merely "signature does not verify".

import (
	"encoding/base64"
	"math/big"
	"net/http"
	"strings"
	"testing"
)

// TestAllocate_HighSSignature_Returns401Unauthorized is PG-AUTH-2: a high-s signature SHALL
// be refused even though it is a mathematically valid signature over the same digest.
func TestAllocate_HighSSignature_Returns401Unauthorized(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-highs")
	path := "/v1/installations/" + r.installationID + "/addresses"
	body := []byte("{}")
	headers, _, _ := sign(t, r.priv, h.clock.Now(), signParams{method: "POST", path: path, body: body})

	const prefix = "p256-sha256 "
	sigBytes, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(headers["Swarm-Signature"], prefix))
	if err != nil || len(sigBytes) != 64 {
		t.Fatalf("decode test-signed signature: %v", err)
	}
	n := r.priv.Curve.Params().N
	s := new(big.Int).SetBytes(sigBytes[32:])
	highS := new(big.Int).Sub(n, s) // still a valid signature for the same message (r, n-s)
	highS.FillBytes(sigBytes[32:])
	headers["Swarm-Signature"] = prefix + base64RawURL(sigBytes)

	resp := h.doJSON("POST", path, body, headers)
	requireStatus(t, resp, http.StatusUnauthorized)
	if e := decodeError(t, resp); e.Code != "unauthorized" {
		t.Fatalf("code = %q, want unauthorized", e.Code)
	}
}
