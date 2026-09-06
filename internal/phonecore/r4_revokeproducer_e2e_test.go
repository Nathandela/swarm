package phonecore

// FAILING-FIRST (TDD RED, GG-5) for Wave R4 deliverable 4's machine-side revoke
// PRODUCER -- bead agents-tracker-u37c: "phonecore.HonorMachineRevoke exists and is
// tested, but no machine-side path produces the revoke message to the phone." This file
// pins the phone-and-gateway half: THE MACHINE presents the machine-revoke capability,
// the real gateway deletes the pairing's push address and accepts the PG-REV-2 tombstoned
// retry, and THE PHONE HONORS (the already-shipped HonorMachineRevoke arm severs the
// local binding forever). Canonical daemon custody tests own persistence across machine
// process death and epoch rotation.
//
// THE GATEWAY IN EVERY TEST IS THE REAL internal/pushgw SERVER, in process, exactly as
// the R3 suite runs it (fixtures in r3a_installation_test.go). No mock of the contract.
//
// THE CONTRACT UNDER TEST:
//
//   - remotegw.HTTPAddressRevoker{BaseURL, MachineRevokeCapability, Client}: DELETE
//     /v1/addresses/{addr} bearing "Swarm-Revoke <capability>", no body.
//   - canonical daemon custody tests cover durable recovery before and after a failed
//     delete; this test drives the live HTTP helper twice against the real gateway,
//     including the gateway's tombstoned 204 retry.
//

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remotegw"
)

// TestR4_RevokeProducer_EndToEnd_MachineProducesPhoneHonors: the whole u37c gap, closed
// in one pass against the real gateway.
//
//  1. The phone registers, allocates, and adopts the binding (the pairing's local half).
//  2. The MACHINE's live HTTP helper drives the revoke: the gateway deletes the address,
//     and the machine's own submit capability is dead from that moment.
//  3. A second HTTP request re-presents the SAME delete and the tombstone answers 204.
//  4. The PHONE honors the revoke: the binding is severed forever, wakes under the dead
//     key are dropped and counted, the severance survives phone process death, and the
//     address can never be re-adopted.
func TestR4_RevokeProducer_EndToEnd_MachineProducesPhoneHonors(t *testing.T) {
	sender := &r3aSender{}
	hs := r3aGateway(t, sender, &r3aAttestVerifier{licensed: true})

	// 1. Phone half: register + allocate + adopt, one accepted wake proves it live.
	_, alloc := r3aRegisterAndAllocate(t, hs.URL, hs.Client())
	phone := &r3aPhone{dir: t.TempDir(), wake: s14aNewSealer(t), content: s14aNewSealer(t)}
	core := phone.resume(t)
	wakeKey, err := NewPairingWakeKey()
	if err != nil {
		t.Fatalf("NewPairingWakeKey: %v", err)
	}
	if err := core.AdoptPushBinding(alloc.Address, wakeKey); err != nil {
		t.Fatalf("AdoptPushBinding: %v", err)
	}
	if status := r3aSubmitWake(t, hs.URL, alloc.Address, alloc.SubmitCapability, 1); status != http.StatusOK {
		t.Fatalf("binding wake: status %d, want 200", status)
	}

	// 2. Machine half: the live HTTP helper. Canonical daemon custody owns durable
	// recovery; this test proves the helper's request reaches the real gateway contract.
	revoker := &remotegw.HTTPAddressRevoker{
		BaseURL: hs.URL, MachineRevokeCapability: alloc.MachineRevokeCapability, Client: hs.Client(),
	}
	if err := revoker.RevokeAddress(context.Background(), remotegw.PushAddress(alloc.Address)); err != nil {
		t.Fatalf("driving the revoke against the live gateway: %v", err)
	}

	// The pairing's submit capability is dead at the gateway: nothing further forwards.
	if status := r3aSubmitWake(t, hs.URL, alloc.Address, alloc.SubmitCapability, 2); status == http.StatusOK {
		t.Fatalf("a wake was accepted after the machine-side revoke")
	}
	if got := len(sender.snapshot()); got != 1 {
		t.Errorf("gateway forwarded %d wakes after the revoke, want still 1", got)
	}

	// 3. The gateway's machine-revoke tombstone accepts a real second request.
	if err := revoker.RevokeAddress(context.Background(), remotegw.PushAddress(alloc.Address)); err != nil {
		t.Fatalf("the tombstoned machine revoke retry failed: %v", err)
	}

	// 4. Phone honors: the aftermath HonorMachineRevoke already owns, now reachable end
	// to end because a production producer exists to trigger it.
	if err := core.HonorMachineRevoke(alloc.Address); err != nil {
		t.Fatalf("HonorMachineRevoke: %v", err)
	}
	drops := core.WakeDrops()
	if err := core.AcceptWakeV1(r3aSeal(t, wakeKey, alloc.Address, 2, time.Now())); err == nil {
		t.Fatalf("a wake under the machine-revoked binding was accepted")
	}
	if core.WakeDrops() != drops+1 {
		t.Errorf("the refused wake was not counted")
	}
	restarted := phone.resume(t)
	if err := restarted.AcceptWakeV1(r3aSeal(t, wakeKey, alloc.Address, 3, time.Now())); err == nil {
		t.Errorf("the severance did not survive phone process death")
	}
	freshKey, err := NewPairingWakeKey()
	if err != nil {
		t.Fatalf("NewPairingWakeKey: %v", err)
	}
	if err := restarted.AdoptPushBinding(alloc.Address, freshKey); !errors.Is(err, ErrPushAddressRevoked) {
		t.Errorf("re-adopting the machine-revoked address: got %v, want ErrPushAddressRevoked", err)
	}
}
