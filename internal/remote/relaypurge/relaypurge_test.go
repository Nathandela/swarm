package relaypurge

// FAILING-FIRST tests for SH5 (bead agents-tracker-dtc5): the durable relay-purge
// obligation. ADR-007 D9 says "an offline-at-revoke machine defers the purge to
// reconnect", and until this slice nothing in the tree deferred anything: the pending
// arm of `swarm remote revoke` printed "Nothing retries it" and abandoned the purge
// (cmd/swarm/remote.go, the honest-ceiling comment this slice retires).
//
// The store's contract mirrors the u37c push-gateway revoke obligation
// (internal/remotegw, cmd/swarm-remote/main.go machineRevoke): durable before the first
// network attempt, idempotent, retired only on an acknowledged purge -- with one
// addition u37c's round 3 taught: a routing id that is LIVE AGAIN (the same handset
// re-paired; device.key is per-install so the routing id repeats) must be RETIRED
// WITHOUT PURGING, because driving the stored purge would destroy the live pairing's
// mailbox and route while reporting success.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func openStore(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(filepath.Join(dir, "relay-purge-obligation.json"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestSH5Store_ARecordSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	if err := s.Record("routing-a", "wss://relay-one", ""); err != nil {
		t.Fatalf("Record: %v", err)
	}
	// The durability IS the feature: the process that recorded dies with the machine.
	re := openStore(t, dir)
	got, err := re.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(got) != 1 || got[0].RoutingID != "routing-a" {
		t.Fatalf("Pending after reopen = %+v, want exactly the recorded routing id", got)
	}
}

func TestSH5Store_RecordingTheSameRoutingIDTwiceHoldsOneObligation(t *testing.T) {
	s := openStore(t, t.TempDir())
	if err := s.Record("routing-a", "wss://relay-one", ""); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := s.Record("routing-a", "wss://relay-one", ""); err != nil {
		t.Fatalf("Record again: %v", err)
	}
	if got, err := s.Pending(); err != nil || len(got) != 1 {
		t.Fatalf("Pending = %+v, want one obligation: a re-run revoke owes one purge, not two", got)
	}
}

func TestSH5Store_DriveRetiresWhatThePurgeAcknowledges(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	for _, rid := range []string{"routing-a", "routing-b"} {
		if err := s.Record(rid, "wss://relay-one", ""); err != nil {
			t.Fatalf("Record %s: %v", rid, err)
		}
	}
	var purged []string
	err := Drive(context.Background(), s,
		func(_ context.Context, ob Obligation) error { purged = append(purged, ob.RoutingID); return nil })
	if err != nil {
		t.Fatalf("Drive: %v", err)
	}
	if len(purged) != 2 {
		t.Fatalf("purge ran for %v, want both pending routing ids", purged)
	}
	if got, err := openStore(t, dir).Pending(); err != nil || len(got) != 0 {
		t.Fatalf("Pending after an acknowledged drive = %+v, want none, durably", got)
	}
}

func TestSH5Store_AFailedPurgeStaysDurable(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	if err := s.Record("routing-a", "wss://relay-one", ""); err != nil {
		t.Fatalf("Record: %v", err)
	}
	boom := errors.New("relay unreachable")
	err := Drive(context.Background(), s,
		func(context.Context, Obligation) error { return boom })
	if err == nil {
		t.Fatalf("Drive with a failing purge returned nil; the caller cannot report what did not land")
	}
	if got, err := openStore(t, dir).Pending(); err != nil || len(got) != 1 {
		t.Fatalf("Pending after a FAILED drive = %+v, want the obligation kept for the next connection", got)
	}
}

// The live-routing-id guard moved out of Drive (which now has a single act callback)
// into DriveMachineObligations; its behavioral fence is cmd/swarm's
// TestSH5_ADriveNeverPurgesARoutingIDThatIsPairedAgain -- MOVED, not deleted.

func TestSH5Store_TheObligationRemembersItsRelay(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	if err := s.Record("routing-a", "wss://old-relay.example", ""); err != nil {
		t.Fatalf("Record: %v", err)
	}
	got, err := openStore(t, dir).Pending()
	if err != nil || len(got) != 1 || got[0].RelayURL != "wss://old-relay.example" {
		t.Fatalf("Pending = %+v, %v; the obligation must carry the relay it is owed "+
			"against, or a purge after a relay cutover retires against the WRONG relay "+
			"and reports success", got, err)
	}
}

func TestSH5Store_ConcurrentHandlesNeverEraseEachOthersObligations(t *testing.T) {
	dir := t.TempDir()
	a := openStore(t, dir)
	b := openStore(t, dir)
	// The lost-update shape: handle a records first, handle b -- opened before a's
	// write in the old design -- records its own and then retires its own. A
	// snapshot-based store would persist b's stale view and erase a's record.
	if err := a.Record("routing-a", "wss://relay-one", ""); err != nil {
		t.Fatalf("a.Record: %v", err)
	}
	if err := b.Record("routing-b", "wss://relay-one", ""); err != nil {
		t.Fatalf("b.Record: %v", err)
	}
	if err := b.Retire("routing-b"); err != nil {
		t.Fatalf("b.Retire: %v", err)
	}
	got, err := openStore(t, dir).Pending()
	if err != nil || len(got) != 1 || got[0].RoutingID != "routing-a" {
		t.Fatalf("Pending = %+v, %v; the other handle's obligation was erased -- a "+
			"purge lost with no diagnostic", got, err)
	}
}

func TestSH5Store_AnEmptyRoutingIDIsRefused(t *testing.T) {
	s := openStore(t, t.TempDir())
	if err := s.Record("", "wss://relay-one", ""); err == nil {
		t.Fatalf("Record accepted an empty routing id: an unaddressable obligation " +
			"can never be driven or retired by id")
	}
}

func TestSH5Store_ARerecordedObligationAdoptsTheNewRelay(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	if err := s.Record("routing-a", "wss://relay-a.example", ""); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := s.Record("routing-a", "wss://relay-b.example", ""); err != nil {
		t.Fatalf("re-Record: %v", err)
	}
	got, err := openStore(t, dir).Pending()
	if err != nil || len(got) != 1 {
		t.Fatalf("Pending = %+v, %v; want one obligation", got, err)
	}
	if got[0].RelayURL != "wss://relay-b.example" {
		t.Fatalf("the re-recorded obligation kept the OLD relay (%s): the mismatch ruling "+
			"would then retire it while the purge owed at the current relay vanished", got[0].RelayURL)
	}
}

func TestSH5Store_AResolvedRefusalIsATombstoneNotAPendingObligation(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	if err := s.Record("routing-a", "wss://relay-one", ""); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := s.Resolve("routing-a", "relay: store failure"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	re := openStore(t, dir)
	if pending, err := re.Pending(); err != nil || len(pending) != 0 {
		t.Fatalf("Pending = %+v, %v; a resolved refusal must not gate anything", pending, err)
	}
	resolved, err := re.Resolved()
	if err != nil || len(resolved) != 1 || resolved[0].Refusal != "relay: store failure" {
		t.Fatalf("Resolved = %+v, %v; the reason must survive reopen", resolved, err)
	}
	// Retire must not erase the tombstone (Drive retires on a nil act after Resolve).
	if err := re.Retire("routing-a"); err != nil {
		t.Fatalf("Retire: %v", err)
	}
	if resolved, err := openStore(t, dir).Resolved(); err != nil || len(resolved) != 1 {
		t.Fatalf("Retire erased the tombstone: %+v, %v", resolved, err)
	}
	// A NEW obligation for the same routing id records fresh beside the tombstone.
	if err := re.Record("routing-a", "wss://relay-one", ""); err != nil {
		t.Fatalf("re-Record: %v", err)
	}
	if pending, err := re.Pending(); err != nil || len(pending) != 1 {
		t.Fatalf("a fresh obligation after a tombstone must be pending: %+v, %v", pending, err)
	}
}

func TestSH5Store_TheTombstoneClockStartsAtResolutionNotRecording(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	if err := s.Record("routing-a", "wss://relay-one", ""); err != nil {
		t.Fatalf("Record: %v", err)
	}
	// Backdate the RECORDING past the retention -- the long-outage world the deferral
	// exists for -- by editing the store file directly.
	path := filepath.Join(dir, "relay-purge-obligation.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var obs []Obligation
	if err := json.Unmarshal(raw, &obs); err != nil || len(obs) != 1 {
		t.Fatalf("parse: %+v, %v", obs, err)
	}
	obs[0].RecordedAt = time.Now().Add(-120 * 24 * time.Hour)
	raw, err = json.Marshal(obs)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	s = openStore(t, dir)
	if err := s.Resolve("routing-a", "relay: store failure"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// A Record triggers the prune path; the fresh tombstone must survive it.
	if err := s.Record("routing-b", "wss://relay-one", ""); err != nil {
		t.Fatalf("Record b: %v", err)
	}
	if resolved, err := s.Resolved(); err != nil || len(resolved) != 1 {
		t.Fatalf("the tombstone was pruned the day it was created (aged from RecordedAt, "+
			"not ResolvedAt): %+v, %v", resolved, err)
	}
}
