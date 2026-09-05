package relayv2

import (
	"crypto/ed25519"
	"encoding/base64"
	"testing"
)

func TestContractVectorsAndCanonicalDecoding(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i)
	}
	pub := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	rid := RoutingID(pub)
	if rid != "88564c8ede170d2ed321e21e61354184" {
		t.Fatalf("RoutingID = %s", rid)
	}
	if got := HomeID("local-test", rid); got != "cc634f54c634813fc554848c78763e63b3dbdff50975c0d789de730e5570beaa" {
		t.Fatalf("HomeID = %s", got)
	}
	if _, err := decode64("AB", -1); err == nil {
		t.Fatal("accepted non-canonical base64url trailing bits")
	}
	canonical := base64.RawURLEncoding.EncodeToString([]byte{0})
	if _, err := decode64(canonical, 1); err != nil {
		t.Fatalf("canonical base64url rejected: %v", err)
	}
	for _, value := range []string{"", "01", "18446744073709551616"} {
		if _, err := parseUint64(value); err == nil {
			t.Fatalf("accepted non-canonical/overflow uint64 %q", value)
		}
	}
}

func TestFrameRejectsUnknownFields(t *testing.T) {
	_, err := decodeFrame([]byte(`{"v":2,"type":"REVOKED","request_id":"r","peer_rid":"88564c8ede170d2ed321e21e61354184","future":true}`))
	if err == nil {
		t.Fatal("accepted an unknown v2 response field")
	}
}
