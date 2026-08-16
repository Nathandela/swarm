// FAILING-FIRST (TDD RED, GG-5) for Wave R3's Android/phone slice, part 4 of the scope:
// BOTH REVOCATION PATHS (ADR-015 P6, push-gateway-api.md sections 2.3 and 3.4), plus
// PG-ALLOC-3's pairing-failure cleanup, from the phone's side.
//
// THE TWO PATHS ARE TWO PARTIES HOLDING TWO CREDENTIALS, not two routes to one operation
// (PG-AUTH-10), and this file keeps them separate the way the record does:
//
//   - PHONE-initiated ("forget this computer"): (*GatewayClient).RevokeAddress, signed by
//     the INSTALLATION key -- the phone may have discarded the capabilities and must still
//     be able to delete the address. Verified against the real gateway's own owner-arm
//     signature verification.
//   - MACHINE-initiated ("revoke this phone"): the machine presents the machine-revoke
//     capability. The machine's own durable-retry obligation is machine-side work (already
//     delivered); what is the PHONE's to honour is the aftermath: (*Core).HonorMachineRevoke
//     severs the local binding, refuses every wake under the dead key, and refuses to
//     re-adopt the revoked address -- the successor is a DIFFERENT address with its own
//     high-water (PG-WAKE-14), never a resurrected one.
//
// THE GATEWAY IN EVERY TEST IS THE REAL internal/pushgw SERVER, in process, with its two
// declared seams faked (fixtures in r3a_installation_test.go). No mock of the contract.
//
// NOTHING HERE TOUCHES FCM, GOOGLE, OR A HANDSET. PB-E2E-5 and R3's physical exit are not
// claimed by any test in this file.
package phonecore

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

// r3aRegisterAndAllocate registers one installation and allocates one address against the
// real gateway, returning the client and the allocation.
func r3aRegisterAndAllocate(t *testing.T, gwURL string, hc *http.Client) (*GatewayClient, PushAllocation) {
	t.Helper()
	client := NewGatewayClient(gwURL, newR3ASigner(t), r3aAttestor(t), hc)
	reg, err := client.Register(context.Background(), "fcm-token-alpha")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	alloc, err := client.AllocateAddress(context.Background(), reg.InstallationID)
	if err != nil {
		t.Fatalf("AllocateAddress: %v", err)
	}
	return client, alloc
}

// TestR3A_PhoneRevoke_DeletesTheAddressWithTheInstallationKey: the phone-initiated path
// end to end. The address is bound (one accepted wake), then revoked with the
// installation signature; afterwards the machine's submit capability is dead at the
// gateway and no further wake is forwarded.
func TestR3A_PhoneRevoke_DeletesTheAddressWithTheInstallationKey(t *testing.T) {
	sender := &r3aSender{}
	hs := r3aGateway(t, sender, &r3aAttestVerifier{licensed: true})

	client, alloc := r3aRegisterAndAllocate(t, hs.URL, hs.Client())

	// Bind the allocation the way a completing pairing does: one accepted wake.
	if status := r3aSubmitWake(t, hs.URL, alloc.Address, alloc.SubmitCapability, 1); status != http.StatusOK {
		t.Fatalf("binding wake: status %d, want 200", status)
	}
	if len(sender.snapshot()) != 1 {
		t.Fatalf("gateway forwarded %d wakes before the revoke, want 1", len(sender.snapshot()))
	}

	// "Forget this computer": the installation key deletes the whole address.
	if err := client.RevokeAddress(context.Background(), alloc.Address); err != nil {
		t.Fatalf("RevokeAddress with the installation key: %v", err)
	}

	// The machine's submit capability is dead: the gateway refuses and forwards nothing.
	status := r3aSubmitWake(t, hs.URL, alloc.Address, alloc.SubmitCapability, 2)
	if status == http.StatusOK {
		t.Fatalf("a wake for a revoked address was accepted (status %d)", status)
	}
	if got := len(sender.snapshot()); got != 1 {
		t.Errorf("gateway forwarded %d wakes after the revoke, want still 1 (nothing forwarded post-revoke)", got)
	}
}

// TestR3A_PhoneRevoke_PairingFailureDeletesTheAllocationImmediately: PG-ALLOC-3. A failed
// or abandoned pairing must not leave a live UNBOUND allocation waiting for the ten-minute
// sweep: the phone deletes it at once with the credential it still holds. Observable at
// the gateway: the submit capability conveyed to the (never-completed) machine is already
// dead.
func TestR3A_PhoneRevoke_PairingFailureDeletesTheAllocationImmediately(t *testing.T) {
	sender := &r3aSender{}
	hs := r3aGateway(t, sender, &r3aAttestVerifier{licensed: true})

	client, alloc := r3aRegisterAndAllocate(t, hs.URL, hs.Client())

	// The pairing fails (SAS mismatch, abort, crash) BEFORE any wake bound the address.
	if err := client.RevokeAddress(context.Background(), alloc.Address); err != nil {
		t.Fatalf("RevokeAddress on the abandoned allocation: %v", err)
	}

	if status := r3aSubmitWake(t, hs.URL, alloc.Address, alloc.SubmitCapability, 1); status == http.StatusOK {
		t.Fatalf("a wake bound an allocation the failed pairing should have deleted (status %d)", status)
	}
	if got := len(sender.snapshot()); got != 0 {
		t.Errorf("gateway forwarded %d wakes for a deleted allocation, want 0", got)
	}

	// The pairing that retries allocates FRESH: a new address with new capabilities, never
	// the old triple back (PG-ALLOC-5: no in-place swap, no resurrection).
	reg2, err := client.AllocateAddress(context.Background(), r3aInstallationIDOf(t, client))
	if err != nil {
		t.Fatalf("re-allocating after the failed pairing: %v", err)
	}
	if reg2.Address == alloc.Address {
		t.Error("the retry was handed the revoked address again")
	}
	if reg2.SubmitCapability == alloc.SubmitCapability {
		t.Error("the retry was handed the revoked submit capability again")
	}
}

// r3aInstallationIDOf returns the installation id the client registered under; part of
// the frozen GatewayClient contract (the client knows its own installation).
func r3aInstallationIDOf(t *testing.T, client *GatewayClient) string {
	t.Helper()
	id := client.InstallationID()
	if id == "" {
		t.Fatal("GatewayClient.InstallationID is empty after a successful Register")
	}
	return id
}

// TestR3A_HonorMachineRevoke_TheMachineArmWorksAgainstTheRealGateway: the machine-side
// credential's contract, exercised as the machine (a raw DELETE bearing Swarm-Revoke)
// against the real gateway: it deletes the address, it is idempotent across a retry (the
// tombstone answers 204 after the verifier is destroyed, PG-REV-2), and it is REFUSED
// when presented with the submit capability (PG-AUTH-8/9: the two capabilities are not
// interchangeable).
func TestR3A_HonorMachineRevoke_TheMachineArmWorksAgainstTheRealGateway(t *testing.T) {
	sender := &r3aSender{}
	hs := r3aGateway(t, sender, &r3aAttestVerifier{licensed: true})

	_, alloc := r3aRegisterAndAllocate(t, hs.URL, hs.Client())
	if status := r3aSubmitWake(t, hs.URL, alloc.Address, alloc.SubmitCapability, 1); status != http.StatusOK {
		t.Fatalf("binding wake: status %d, want 200", status)
	}

	// The submit capability must NOT revoke.
	if status := r3aMachineRevoke(t, hs.URL, alloc.Address, alloc.SubmitCapability); status == http.StatusNoContent {
		t.Fatal("the SUBMIT capability revoked the address; PG-AUTH-8 forbids it")
	}

	// The machine-revoke capability does, and a durable retry still sees 204.
	if status := r3aMachineRevoke(t, hs.URL, alloc.Address, alloc.MachineRevokeCapability); status != http.StatusNoContent {
		t.Fatalf("machine revoke: status %d, want 204", status)
	}
	if status := r3aMachineRevoke(t, hs.URL, alloc.Address, alloc.MachineRevokeCapability); status != http.StatusNoContent {
		t.Fatalf("machine revoke retry: status %d, want 204 (PG-REV-2 tombstone)", status)
	}

	if status := r3aSubmitWake(t, hs.URL, alloc.Address, alloc.SubmitCapability, 2); status == http.StatusOK {
		t.Fatal("a wake was accepted for a machine-revoked address")
	}
}

// r3aMachineRevoke acts as swarm-remote: DELETE /v1/addresses/{addr} with the
// machine-revoke capability, no body.
func r3aMachineRevoke(t *testing.T, gwURL string, addr PushAddress, capability string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, gwURL+"/v1/addresses/"+r3aEncodeAddress(addr), nil)
	if err != nil {
		t.Fatalf("building the revoke request: %v", err)
	}
	req.Header.Set("Authorization", "Swarm-Revoke "+capability)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("machine revoke: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// r3aEncodeAddress is the wire form of a push address (16 opaque bytes, base64url
// unpadded) -- part of the frozen contract, spelled here so the test does not depend on
// the client for the machine's half.
func r3aEncodeAddress(addr PushAddress) string {
	return EncodePushAddress(addr)
}

// TestR3A_HonorMachineRevoke_SeversTheLocalBindingForever: the PHONE honouring a
// machine-side revoke. Once the phone learns the pairing's push binding is gone, the
// binding is dead locally in every direction: wakes under the old key are dropped and
// counted, the state survives process death, and the revoked address can never be
// re-adopted -- the successor of a revoked address is a DIFFERENT address (ADR-015 P6),
// so re-adoption is the pin-the-window lever handed back to whoever captured old wakes.
func TestR3A_HonorMachineRevoke_SeversTheLocalBindingForever(t *testing.T) {
	phone := &r3aPhone{dir: t.TempDir(), wake: s14aNewSealer(t), content: s14aNewSealer(t)}
	core := phone.resume(t)
	addr, key := r3aBinding(t, core, 0xC1)

	// One wake delivered before the revoke: the binding was live.
	if err := core.AcceptWakeV1(r3aSeal(t, key, addr, 1, time.Now())); err != nil {
		t.Fatalf("pre-revoke wake: %v", err)
	}

	if err := core.HonorMachineRevoke(addr); err != nil {
		t.Fatalf("HonorMachineRevoke: %v", err)
	}

	// A wake sealed under the old key is now dropped and counted, never acted on.
	dropsBefore := core.WakeDrops()
	if err := core.AcceptWakeV1(r3aSeal(t, key, addr, 2, time.Now())); err == nil {
		t.Fatal("a wake under a machine-revoked binding was accepted")
	}
	if core.WakeDrops() != dropsBefore+1 {
		t.Errorf("the post-revoke wake was not counted: drops %d -> %d", dropsBefore, core.WakeDrops())
	}

	// The severance is durable.
	restarted := phone.resume(t)
	if err := restarted.AcceptWakeV1(r3aSeal(t, key, addr, 3, time.Now())); err == nil {
		t.Fatal("the severed binding came back after process death")
	}

	// And the revoked address cannot be re-adopted, under the old key or a fresh one.
	freshKey, err := NewPairingWakeKey()
	if err != nil {
		t.Fatalf("NewPairingWakeKey: %v", err)
	}
	if err := restarted.AdoptPushBinding(addr, freshKey); !errors.Is(err, ErrPushAddressRevoked) {
		t.Fatalf("re-adopting a machine-revoked address: got %v, want ErrPushAddressRevoked", err)
	}
}

// TestR3A_DropPushBinding_ForgetThisComputerRemovesTheLocalHalf: the local half of the
// phone-initiated path. Forgetting a machine drops its wake key and address; wakes under
// the old key are refused and counted from that moment, including across process death.
func TestR3A_DropPushBinding_ForgetThisComputerRemovesTheLocalHalf(t *testing.T) {
	phone := &r3aPhone{dir: t.TempDir(), wake: s14aNewSealer(t), content: s14aNewSealer(t)}
	core := phone.resume(t)
	addr, key := r3aBinding(t, core, 0xC2)

	if err := core.AcceptWakeV1(r3aSeal(t, key, addr, 1, time.Now())); err != nil {
		t.Fatalf("pre-forget wake: %v", err)
	}
	if err := core.DropPushBinding(addr); err != nil {
		t.Fatalf("DropPushBinding: %v", err)
	}
	if err := core.AcceptWakeV1(r3aSeal(t, key, addr, 2, time.Now())); err == nil {
		t.Fatal("a wake under a forgotten binding was accepted")
	}
	restarted := phone.resume(t)
	if err := restarted.AcceptWakeV1(r3aSeal(t, key, addr, 3, time.Now())); err == nil {
		t.Fatal("the forgotten binding came back after process death")
	}
}
