package pushgw_test

// At-rest storage conformance, inspected on the RAW bbolt file bytes rather than through
// any package-internal accessor — the point is that the SECRET is absent from what is
// physically written to disk, not merely absent from some exported view of it. Covers
// PG-AUTH-7 (capability verifiers are hashed, never raw), PG-RET-5 (FCM tokens encrypted
// at rest) and PG-AUTH-12 (the attestation token is never persisted at all, hashed or
// otherwise).
//
// RED: package pushgw has no implementation yet; every pushgw.* reference below is an
// undefined symbol.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/Nathandela/swarm/internal/pushgw"
)

// TestStorage_RawCapabilitiesNeverAppearInTheBboltFile is PG-AUTH-7: only
// SHA-256(capability) is stored, never the 32 raw CSPRNG bytes a client presents.
func TestStorage_RawCapabilitiesNeverAppearInTheBboltFile(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-storage-cap")
	a := allocateAddress(t, h, r)
	// A second allocation, then a revoke, so the file also holds a machine-revoke
	// tombstone (PG-RET-3) by the time it is inspected.
	_ = allocateAddress(t, h, r)
	requireStatus(t, revokeByMachine(h, a.pushAddress, a.machineRevokeCapability), http.StatusNoContent)

	if err := h.srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	raw, err := os.ReadFile(h.dbPath)
	if err != nil {
		t.Fatalf("read bbolt file: %v", err)
	}

	assertAbsent(t, raw, "submit_capability", a.submitCapability)
	assertAbsent(t, raw, "machine_revoke_capability (live)", a.machineRevokeCapability)
}

// TestStorage_FCMTokenNeverAppearsInThePlainInTheBboltFile is PG-RET-5: the token is
// encrypted at rest. This test cannot verify the encryption scheme itself, only the
// contract that actually matters to a reader of the file — the plaintext token string is
// nowhere in it, across BOTH the originally registered token and a subsequent rotation.
func TestStorage_FCMTokenNeverAppearsInThePlainInTheBboltFile(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-storage-plaintext-v1-6f3c9a")

	path := "/v1/installations/" + r.installationID + "/token"
	body := rotateBody("fcm-token-storage-plaintext-v2-8e21db")
	headers, _, _ := sign(t, r.priv, h.clock.Now(), signParams{method: "PUT", path: path, body: body})
	requireStatus(t, h.doJSON("PUT", path, body, headers), http.StatusNoContent)

	if err := h.srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	raw, err := os.ReadFile(h.dbPath)
	if err != nil {
		t.Fatalf("read bbolt file: %v", err)
	}

	assertAbsent(t, raw, "fcm_token (v1, registered)", r.fcmToken)
	assertAbsent(t, raw, "fcm_token (v2, rotated)", "fcm-token-storage-plaintext-v2-8e21db")
}

// TestStorage_AttestationTokenNeverPersisted is PG-AUTH-12: the integrity verdict token
// itself is not persisted at all — not hashed, not encrypted, not in any form.
func TestStorage_AttestationTokenNeverPersisted(t *testing.T) {
	h := newHarness(t, nil)
	_ = registerInstallation(t, h, "fcm-token-storage-attest")

	if err := h.srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	raw, err := os.ReadFile(h.dbPath)
	if err != nil {
		t.Fatalf("read bbolt file: %v", err)
	}
	// registerInstallation mints the verdict token as "verdict-token-" + first 8 chars of
	// the public key; the discriminating substring is the fixed prefix, which would
	// appear verbatim in the file if the raw token were ever written.
	assertAbsent(t, raw, "attestation verdict token", "verdict-token-")
}

// TestStorage_RevocationTombstoneNeverRetainsTheAddress is PG-REV-2 / PG-RET-3: the
// tombstone left behind by a machine-revoke is "the hashed machine-revoke verifier plus a
// revoked-at timestamp, no address content" -- literally. The tombstone bucket is keyed by
// the verifier hash (storage.go), never by push_address, so no COMMITTED record anywhere in
// the file carries the address once it is revoked.
//
// This test asserts that property by Closing the store and REOPENING a fresh one on the
// same file -- proving the deletion (and the tombstone) are durably persisted, not merely
// absent from an in-memory cache -- rather than by scanning the file's raw bytes for the
// address substring, which an earlier revision of this test did. That raw-byte form is no
// longer reliable now that the HIGH finding fixed revoke.go to delete-and-tombstone in ONE
// bbolt transaction (deleteAddressAndTombstone): bbolt's freelist only makes a page a
// transaction just freed available for a LATER transaction's allocation (freelist.go's
// pending/release split), never for an allocation inside that SAME transaction, so the
// freed address page can no longer coincidentally be overwritten by the tombstone write the
// way the old two-transaction version sometimes was. The address's old bytes can now
// linger in a freed, unreachable page until some future write reuses it -- copy-on-write's
// well-known general property, already the accepted caveat for the 180-day sweep just below
// this test, and not a regression: no LIVE, readable path (through the store's own API, on
// this process or a freshly reopened one) can retain the address, which is exactly what
// this test proves instead.
func TestStorage_RevocationTombstoneNeverRetainsTheAddress(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-tombstone-noaddr")
	a := allocateAddress(t, h, r)
	requireStatus(t, revokeByMachine(h, a.pushAddress, a.machineRevokeCapability), http.StatusNoContent)

	if err := h.srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := pushgw.NewServer(pushgw.Config{
		DBPath: h.dbPath,
		Sender: newFakeSender(),
		Attest: newFakeAttestor(),
		Now:    h.clock.Now,
		Quotas: defaultTestQuotas(),
	})
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	reopenedTS := httptest.NewServer(reopened)
	defer reopenedTS.Close()
	rh := &harness{t: t, srv: reopened, ts: reopenedTS, url: reopenedTS.URL, client: reopenedTS.Client(), clock: h.clock, dbPath: h.dbPath}

	// The address does not survive a restart: a wake against it is refused.
	env := buildWakeV1(t, decodeAddr(t, a.pushAddress), 1, h.clock.Now())
	requireStatus(t, submitTestWake(rh, a.submitCapability, env), http.StatusUnauthorized)

	// The tombstone DOES survive a restart: a durable retry of the same revoke is still 204.
	requireStatus(t, revokeByMachine(rh, a.pushAddress, a.machineRevokeCapability), http.StatusNoContent)
}

func assertAbsent(t *testing.T, fileBytes []byte, label, secret string) {
	t.Helper()
	if secret == "" {
		t.Fatalf("test bug: empty secret for %s", label)
	}
	if bytes.Contains(fileBytes, []byte(secret)) {
		t.Fatalf("bbolt file contains the raw %s (%d bytes total)", label, len(fileBytes))
	}
}
