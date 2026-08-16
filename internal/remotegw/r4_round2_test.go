package remotegw

// FAILING-FIRST (TDD RED, GG-5) for Wave R4 round 2 (bead agents-tracker-u37c): the
// adversarial review of the round-1 GREEN found two obligation-store defects, pinned
// here BEFORE their fixes.
//
//   - Record() silently CLOBBERS a pending obligation: a durable, undelivered revoke
//     for address A is destroyed by a later revoke for address B, leaving A live at
//     the gateway with no durable record that it should not be.
//   - A refusal RevokeAddress itself documents as terminal ("a dead capability cannot
//     come alive by retrying") leaves the obligation pending forever: Drive returns
//     the error without resolving the record, so every process start re-presents a
//     request the gateway has already refused -- reachable via pushgw's 401 on a
//     capability/address mismatch (internal/pushgw/revoke_test.go:101), i.e. any
//     re-provisioning between the record and the retry.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestR4R2_RevokeObligation_RecordRefusesToClobberAPendingObligation: one durable
// record is custody, not a scratch slot. A pending obligation survives a later Record
// for a DIFFERENT address (refused), and a Record for the SAME address is idempotent --
// the producer records on every revoked exit.
func TestR4R2_RevokeObligation_RecordRefusesToClobberAPendingObligation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "revoke-obligation.json")
	store, err := OpenRevokeObligationStore(path)
	if err != nil {
		t.Fatalf("OpenRevokeObligationStore: %v", err)
	}
	addrA := r4TestAddress(0x61)
	addrB := r4TestAddress(0x62)

	first := NewRevokeObligationMachine(RevokeObligationConfig{Store: store, Address: addrA})
	if err := first.Record(); err != nil {
		t.Fatalf("recording the first obligation: %v", err)
	}

	second := NewRevokeObligationMachine(RevokeObligationConfig{Store: store, Address: addrB})
	if err := second.Record(); err == nil {
		t.Fatalf("recording a second obligation silently clobbered a PENDING one: the "+
			"undelivered revoke for %v is destroyed and its address stays live at the "+
			"gateway with no durable record that it should not be", addrA)
	}
	if ob, ok := store.Pending(); !ok || ob.Address != addrA {
		t.Fatalf("the pending obligation did not survive the refused clobber: pending=%v ob=%v", ok, ob)
	}

	// Same address again: idempotent, never a refusal.
	if err := first.Record(); err != nil {
		t.Errorf("re-recording the SAME pending address errored: %v", err)
	}

	reopened, err := OpenRevokeObligationStore(path)
	if err != nil {
		t.Fatalf("reopening the store: %v", err)
	}
	if ob, ok := reopened.Pending(); !ok || ob.Address != addrA {
		t.Errorf("the durable file no longer holds the first obligation: pending=%v ob=%v", ok, ob)
	}
}

// TestR4R2_RevokeObligation_TerminalRefusalResolvesWithTheReasonPreserved: a 401 is the
// class RevokeAddress classifies terminal, so Drive must RESOLVE the obligation --
// durably, with the refusal preserved -- rather than re-present a dead capability on
// every start forever. The error still surfaces to the caller; what changes is that
// the store converges.
func TestR4R2_RevokeObligation_TerminalRefusalResolvesWithTheReasonPreserved(t *testing.T) {
	capture := &r4RevokeCapture{status: http.StatusUnauthorized}
	hs := httptest.NewServer(capture.handler())
	defer hs.Close()

	path := filepath.Join(t.TempDir(), "revoke-obligation.json")
	store, err := OpenRevokeObligationStore(path)
	if err != nil {
		t.Fatalf("OpenRevokeObligationStore: %v", err)
	}
	machine := NewRevokeObligationMachine(RevokeObligationConfig{
		Store: store,
		Revoker: &HTTPAddressRevoker{
			BaseURL:                 hs.URL,
			MachineRevokeCapability: "cap-r4-terminal-0000000000000000",
			Client:                  hs.Client(),
		},
		Address: r4TestAddress(0x63),
	})
	if err := machine.Record(); err != nil {
		t.Fatalf("Record: %v", err)
	}

	if err := machine.Drive(context.Background()); err == nil {
		t.Fatalf("a 401 drive reported success; the refusal must still reach the caller")
	}
	if _, ok := store.Pending(); ok {
		t.Fatalf("a terminal 401 refusal left the obligation PENDING; every future start " +
			"re-presents a capability the gateway already refused as unauthorized -- the " +
			"obligation never converges")
	}

	// The resolution is durable and the reason survives the process.
	if reopened, err := OpenRevokeObligationStore(path); err != nil {
		t.Fatalf("reopening the store: %v", err)
	} else if _, ok := reopened.Pending(); ok {
		t.Fatalf("the terminal resolution did not survive a reopen")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the durable record: %v", err)
	}
	if !strings.Contains(string(raw), "401") {
		t.Errorf("the durable record preserves no trace of WHY the obligation resolved "+
			"refused (want the 401 named): %s", raw)
	}

	// A re-drive is a no-op: no request is spent re-proving a dead capability.
	before := capture.count()
	if err := machine.Drive(context.Background()); err != nil {
		t.Errorf("re-driving a terminally-refused obligation errored: %v", err)
	}
	if got := capture.count(); got != before {
		t.Errorf("the re-drive presented %d further request(s) for a terminally-refused "+
			"obligation", got-before)
	}
}
