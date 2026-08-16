package phonecore

// FAILING-FIRST (TDD RED, GG-5) for Wave R4 deliverable 4's machine-side revoke
// PRODUCER -- bead agents-tracker-u37c: "phonecore.HonorMachineRevoke exists and is
// tested, but no machine-side path produces the revoke message to the phone." This file
// pins the END-TO-END SHAPE the producer closes: THE MACHINE PRODUCES (a durable
// obligation presents the machine-revoke capability at the gateway, deleting the
// pairing's push address, retried across process death, idempotent through the PG-REV-2
// tombstone, independent of the local epoch rotation the revoke performs), and THE
// PHONE HONORS (the already-shipped HonorMachineRevoke arm severs the local binding
// forever). ADR-015 P6; playbook 3.2 (:146-147): "Machine-side device revocation uses
// the machine-revoke capability and retries deletion durably after local epoch
// rotation."
//
// THE GATEWAY IN EVERY TEST IS THE REAL internal/pushgw SERVER, in process, exactly as
// the R3 suite runs it (fixtures in r3a_installation_test.go). No mock of the contract.
//
// THE CONTRACT UNDER TEST (undefined in internal/remotegw today; its unit half lives in
// internal/remotegw/r4_revokeproducer_test.go):
//
//   - remotegw.HTTPAddressRevoker{BaseURL, MachineRevokeCapability, Client}: DELETE
//     /v1/addresses/{addr} bearing "Swarm-Revoke <capability>", no body.
//   - remotegw.OpenRevokeObligationStore(path): the durable custody of the one revoke
//     obligation, byte-file-backed like OpenObligationStore.
//   - remotegw.NewRevokeObligationMachine(remotegw.RevokeObligationConfig{...}): Record
//     durably registers the obligation; Drive presents it and classifies the outcome --
//     2xx/tombstoned-204 terminal, transport failure and 5xx retryable.
//
// NOTE ON B94: wiring the producer is also what deletes agents-tracker-u37c's
// disclosure in the R3 evidence; the GREEN slice must ledger that alongside the
// phonecore MM4 rows (internal/verify/phaseb_reachability_test.go:114-133).

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remotegw"
)

// TestR4_RevokeProducer_EndToEnd_MachineProducesPhoneHonors: the whole u37c gap, closed
// in one pass against the real gateway.
//
//  1. The phone registers, allocates, and adopts the binding (the pairing's local half).
//  2. The MACHINE records a durable revoke obligation and drives it: the gateway deletes
//     the address, and the machine's own submit capability is dead from that moment.
//  3. A durable retry after a simulated machine process death re-presents the SAME
//     delete and the tombstone answers 204 -- terminal, not an error.
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

	// 2. Machine half: the durable producer. Record BEFORE driving -- the obligation is
	// custody, not a fire-and-forget HTTP call.
	dir := t.TempDir()
	store, err := remotegw.OpenRevokeObligationStore(filepath.Join(dir, "revoke-obligation.json"))
	if err != nil {
		t.Fatalf("OpenRevokeObligationStore: %v", err)
	}
	machine := remotegw.NewRevokeObligationMachine(remotegw.RevokeObligationConfig{
		Store: store,
		Revoker: &remotegw.HTTPAddressRevoker{
			BaseURL:                 hs.URL,
			MachineRevokeCapability: alloc.MachineRevokeCapability,
			Client:                  hs.Client(),
		},
		Address: remotegw.PushAddress(alloc.Address),
	})
	if err := machine.Record(); err != nil {
		t.Fatalf("recording the revoke obligation: %v", err)
	}
	if err := machine.Drive(context.Background()); err != nil {
		t.Fatalf("driving the revoke obligation against the live gateway: %v", err)
	}

	// The pairing's submit capability is dead at the gateway: nothing further forwards.
	if status := r3aSubmitWake(t, hs.URL, alloc.Address, alloc.SubmitCapability, 2); status == http.StatusOK {
		t.Fatalf("a wake was accepted after the machine-side revoke")
	}
	if got := len(sender.snapshot()); got != 1 {
		t.Errorf("gateway forwarded %d wakes after the revoke, want still 1", got)
	}

	// 3. Machine process death: a fresh store over the same file must still know the
	// obligation, and re-driving it hits the tombstone's idempotent 204 -- a durable
	// retry across an exit is the obligation's whole reason to exist.
	store2, err := remotegw.OpenRevokeObligationStore(filepath.Join(dir, "revoke-obligation.json"))
	if err != nil {
		t.Fatalf("reopening the revoke-obligation store: %v", err)
	}
	machine2 := remotegw.NewRevokeObligationMachine(remotegw.RevokeObligationConfig{
		Store: store2,
		Revoker: &remotegw.HTTPAddressRevoker{
			BaseURL:                 hs.URL,
			MachineRevokeCapability: alloc.MachineRevokeCapability,
			Client:                  hs.Client(),
		},
		Address: remotegw.PushAddress(alloc.Address),
	})
	if err := machine2.Drive(context.Background()); err != nil {
		t.Fatalf("the durable retry after process death failed: %v (PG-REV-2's tombstone makes "+
			"the re-presented delete a 204, not an error)", err)
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
