package pushgw_test

// DELETE /v1/addresses/{push_address} (spec §3.4). Covers PG-REV-1..4: revoke by the
// phone's installation-key signature ("forget this computer") and by the machine's
// machine-revoke capability ("revoke this phone"), whole-address deletion, and the
// machine-revoke path's idempotent tombstoned retry.
//
// A GENUINE SPEC GAP THIS FILE WORKS AROUND, RECORDED HERE AND IN THE ROLE RETURN SUMMARY:
// §2.1/PG-AUTH-1 says the installation id is "in the path" for every installation-control
// request "so it is signature-covered," and explicitly includes "revoke address by owner"
// in that set — but §3.4's route is `DELETE /v1/addresses/{push_address}`, with NO
// installation_id path segment and no header carrying one either. PG-AUTH-4's nonce cache
// is keyed (installation_id, nonce), which needs an installation_id to exist for this
// exact request. The only installation-identifying data the declared wire shape supplies
// is the address→installation binding recorded at allocation time, so that is the
// resolution this suite assumes: look up push_address, verify the presented signature
// against THAT installation's public key. That resolution is unavailable once the address
// (and its binding) is already gone — which is exactly the case PG-REV-2 calls "free"
// idempotency for the owner-signature arm. TestRevoke_OwnerSignature_RetryAfterAlreadyGone
// below is left unasserted (skipped) rather than guessing which of {204, 401} an owner
// ruling will pick; the machine-revoke arm's tombstone (PG-REV-2, tested below) has no such
// gap because it does not depend on the address surviving.
//
// RED: package pushgw has no implementation yet; every pushgw.* reference below is an
// undefined symbol.

import (
	"net/http"
	"testing"
)

// revokeByOwner signs and sends the installation-credential revoke arm.
func revokeByOwner(t *testing.T, h *harness, r registered, pushAddress string) *http.Response {
	t.Helper()
	path := "/v1/addresses/" + pushAddress
	headers, _, _ := sign(t, r.priv, h.clock.Now(), signParams{method: "DELETE", path: path, body: []byte{}})
	return h.do("DELETE", path, nil, headers)
}

// revokeByMachine sends the machine-revoke-capability arm.
func revokeByMachine(h *harness, pushAddress, machineRevokeCapability string) *http.Response {
	path := "/v1/addresses/" + pushAddress
	return h.do("DELETE", path, nil, map[string]string{
		"Authorization": "Swarm-Revoke " + machineRevokeCapability,
	})
}

// TestRevoke_ByOwnerSignature_Returns204AndKillsTheWholeAddress is PG-REV-1: the address,
// both verifiers and the binding all go together — the old submit capability stops
// authorizing a wake.
func TestRevoke_ByOwnerSignature_Returns204AndKillsTheWholeAddress(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-revoke-owner")
	a := allocateAddress(t, h, r)

	resp := revokeByOwner(t, h, r, a.pushAddress)
	requireStatus(t, resp, http.StatusNoContent)

	env := buildWakeV1(t, decodeAddr(t, a.pushAddress), 1, h.clock.Now())
	wakeResp := submitTestWake(h, a.submitCapability, env)
	// §4's `unauthorized` row explicitly lists "revoked capability" as a 401 case; the
	// `address_revoked` 410 row is scoped to "a capability that once verified" within a
	// live wake obligation, which whole-address deletion (PG-REV-1, PG-RET-2 — actual
	// deletion, not a tombstone) does not leave behind for the submit capability. 401 is
	// this suite's primary reading; see the file header for the adjacent, unresolved
	// owner-arm-retry gap.
	requireStatus(t, wakeResp, http.StatusUnauthorized)
}

// TestRevoke_ByMachineRevokeCapability_Returns204AndKillsTheWholeAddress mirrors the
// above for the machine-side arm (PG-AUTH-10).
func TestRevoke_ByMachineRevokeCapability_Returns204AndKillsTheWholeAddress(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-revoke-machine")
	a := allocateAddress(t, h, r)

	resp := revokeByMachine(h, a.pushAddress, a.machineRevokeCapability)
	requireStatus(t, resp, http.StatusNoContent)

	env := buildWakeV1(t, decodeAddr(t, a.pushAddress), 1, h.clock.Now())
	wakeResp := submitTestWake(h, a.submitCapability, env)
	requireStatus(t, wakeResp, http.StatusUnauthorized)
}

// TestRevoke_MachineRevokeCapability_IdempotentRetryViaTombstone is PG-REV-2: the
// successful delete destroys the verifier it was presented with, so a durable retry across
// an ADR-011 M5 process exit must still see 204, not 401 — served from the bounded
// tombstone (hashed verifier + revoked-at, nothing else).
func TestRevoke_MachineRevokeCapability_IdempotentRetryViaTombstone(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-revoke-retry")
	a := allocateAddress(t, h, r)

	first := revokeByMachine(h, a.pushAddress, a.machineRevokeCapability)
	requireStatus(t, first, http.StatusNoContent)

	second := revokeByMachine(h, a.pushAddress, a.machineRevokeCapability)
	requireStatus(t, second, http.StatusNoContent)
}

// TestRevoke_WrongMachineRevokeCapability_Returns401Unauthorized: a capability that never
// verified against this (or any) address.
func TestRevoke_WrongMachineRevokeCapability_Returns401Unauthorized(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-revoke-wrongcap")
	a := allocateAddress(t, h, r)
	other := allocateAddress(t, h, r) // a second, real capability -- still not THIS address's

	resp := revokeByMachine(h, a.pushAddress, other.machineRevokeCapability)
	requireStatus(t, resp, http.StatusUnauthorized)
	if e := decodeError(t, resp); e.Code != "unauthorized" {
		t.Fatalf("code = %q, want unauthorized", e.Code)
	}
}

// TestRevoke_SubmitCapabilityPresentedAsMachineRevoke_Returns401 is PG-AUTH-8/9's mirror:
// the submit capability SHALL NOT authorize DELETE /v1/addresses/{addr}. revoke.go compares
// only against MachineRevokeHash, so presenting the OTHER capability of the same allocation
// under the Swarm-Revoke scheme must still be refused.
func TestRevoke_SubmitCapabilityPresentedAsMachineRevoke_Returns401(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-revoke-crossscheme")
	a := allocateAddress(t, h, r)
	resp := revokeByMachine(h, a.pushAddress, a.submitCapability)
	requireStatus(t, resp, http.StatusUnauthorized)
}

// TestRevoke_AnyBodyPresent_Returns400MalformedRequest is PG-TR-3's 0-byte row: any body
// at all on this operation is malformed_request, regardless of content.
func TestRevoke_AnyBodyPresent_Returns400MalformedRequest(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-revoke-body")
	a := allocateAddress(t, h, r)
	path := "/v1/addresses/" + a.pushAddress
	body := []byte(`{"unexpected":true}`)
	headers, _, _ := sign(t, r.priv, h.clock.Now(), signParams{method: "DELETE", path: path, body: body})
	resp := h.doJSON("DELETE", path, body, headers)
	requireStatus(t, resp, http.StatusBadRequest)
	if e := decodeError(t, resp); e.Code != "malformed_request" {
		t.Fatalf("code = %q, want malformed_request", e.Code)
	}
}

// TestRevoke_ContentEncodingPresent_Returns400MalformedRequest is PG-TR-4, covering the
// one operation that previously skipped this check: a zero-byte body with
// Content-Encoding set must still be refused before a valid capability is even consulted.
func TestRevoke_ContentEncodingPresent_Returns400MalformedRequest(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-revoke-enc")
	a := allocateAddress(t, h, r)
	resp := h.do("DELETE", "/v1/addresses/"+a.pushAddress, nil, map[string]string{
		"Content-Encoding": "gzip",
		"Authorization":    "Swarm-Revoke " + a.machineRevokeCapability,
	})
	requireStatus(t, resp, http.StatusBadRequest)
	if e := decodeError(t, resp); e.Code != "malformed_request" {
		t.Fatalf("code = %q, want malformed_request", e.Code)
	}
}

// TestRevoke_OwnerRevoke_MachineRetryStillGets204 is PG-REV-2: idempotency SHALL hold for
// BOTH credential kinds. Here the OWNER destroys the address; the machine's later durable
// retry of ITS OWN revoke, presenting the machine-revoke capability the address once had,
// must still see 204 -- not 401 -- because the tombstone has to be written on every path
// that destroys an address, not only the machine arm's own success.
func TestRevoke_OwnerRevoke_MachineRetryStillGets204(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-revoke-ownerthenmachine")
	a := allocateAddress(t, h, r)

	requireStatus(t, revokeByOwner(t, h, r, a.pushAddress), http.StatusNoContent)

	retry := revokeByMachine(h, a.pushAddress, a.machineRevokeCapability)
	requireStatus(t, retry, http.StatusNoContent)
}

// TestRevoke_OwnerSignature_CrossTenant_Returns401 is the MEDIUM verification-gap finding's
// cross-tenant authorization test: revokeByOwnerSignature resolves the verification key
// from the ADDRESS's own recorded installation binding (rec.InstallationID), never from
// anything the caller supplies -- so installation B's genuinely valid signature, over a
// well-formed, correctly-nonce'd DELETE naming installation A's address, must still be
// refused: B's key does not verify against A's public key.
func TestRevoke_OwnerSignature_CrossTenant_Returns401(t *testing.T) {
	h := newHarness(t, nil)
	rA := registerInstallation(t, h, "fcm-token-revoke-tenantA")
	aA := allocateAddress(t, h, rA)
	rB := registerInstallation(t, h, "fcm-token-revoke-tenantB")
	_ = allocateAddress(t, h, rB) // B's own, unrelated address

	path := "/v1/addresses/" + aA.pushAddress
	headers, _, _ := sign(t, rB.priv, h.clock.Now(), signParams{method: "DELETE", path: path, body: []byte{}})
	resp := h.do("DELETE", path, nil, headers)
	requireStatus(t, resp, http.StatusUnauthorized)

	// A's address must still be live: B's refused attempt destroyed nothing.
	env := buildWakeV1(t, decodeAddr(t, aA.pushAddress), 1, h.clock.Now())
	requireStatus(t, submitTestWake(h, aA.submitCapability, env), http.StatusOK)
}

// TestRevoke_OwnerSignature_RetryAfterAlreadyGone documents, rather than asserts, the open
// gap described in this file's header: PG-REV-2 calls the owner-arm retry "free," but this
// suite cannot derive which installation's key to re-verify against once the address→
// installation binding the resolution depends on has itself been deleted. Skipped pending
// an owner ruling (this bead's escalation), not silently guessed.
func TestRevoke_OwnerSignature_RetryAfterAlreadyGone(t *testing.T) {
	t.Skip("open spec question: see this file's header comment and the RED-phase return summary")
}
