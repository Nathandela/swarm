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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/relayv2"
)

var (
	testPhonePub = bytes.Repeat([]byte{7}, 32)
	testConsent  = bytes.Repeat([]byte{8}, 100)
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
	phonePub := bytes.Repeat([]byte{1}, 32)
	consent := bytes.Repeat([]byte{2}, 100)
	if _, err := s.Record("routing-a", "wss://relay-one", "machine-a", phonePub, consent); err != nil {
		t.Fatalf("Record: %v", err)
	}
	// The durability IS the feature: the process that recorded dies with the machine.
	re := openStore(t, dir)
	got, err := re.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(got) != 1 || got[0].RoutingID != "routing-a" ||
		!bytes.Equal(got[0].PhonePub, phonePub) || !bytes.Equal(got[0].Consent, consent) {
		t.Fatalf("Pending after reopen = %+v, want exactly the recorded routing id", got)
	}
}

func TestSH5Store_RecordingTheSameRoutingIDTwiceHoldsOneObligation(t *testing.T) {
	s := openStore(t, t.TempDir())
	if _, err := s.Record("routing-a", "wss://relay-one", "", testPhonePub, testConsent); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if _, err := s.Record("routing-a", "wss://relay-one", "", testPhonePub, testConsent); err != nil {
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
		if _, err := s.Record(rid, "wss://relay-one", "", testPhonePub, testConsent); err != nil {
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
	if _, err := s.Record("routing-a", "wss://relay-one", "", testPhonePub, testConsent); err != nil {
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
	if _, err := s.Record("routing-a", "wss://old-relay.example", "", testPhonePub, testConsent); err != nil {
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
	if _, err := a.Record("routing-a", "wss://relay-one", "", testPhonePub, testConsent); err != nil {
		t.Fatalf("a.Record: %v", err)
	}
	bAttempt, err := b.Record("routing-b", "wss://relay-one", "", testPhonePub, testConsent)
	if err != nil {
		t.Fatalf("b.Record: %v", err)
	}
	if _, err := b.Retire("routing-b", bAttempt.AttemptID); err != nil {
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
	if _, err := s.Record("", "wss://relay-one", "", testPhonePub, testConsent); err == nil {
		t.Fatalf("Record accepted an empty routing id: an unaddressable obligation " +
			"can never be driven or retired by id")
	}
}

func TestSH5Store_IncompleteRelayV2EvidenceIsRefused(t *testing.T) {
	s := openStore(t, t.TempDir())
	for _, tc := range []struct {
		name     string
		phonePub []byte
		consent  []byte
	}{
		{"missing phone key", nil, testConsent},
		{"short phone key", testPhonePub[:31], testConsent},
		{"missing consent", testPhonePub, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.Record("routing-a", "wss://relay-one", "machine-a", tc.phonePub, tc.consent); err == nil {
				t.Fatal("Record accepted incomplete relay-v2 evidence")
			}
		})
	}
}

func TestSH5Store_ARerecordedObligationAdoptsTheNewRelay(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	if _, err := s.Record("routing-a", "wss://relay-a.example", "", testPhonePub, testConsent); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if _, err := s.Record("routing-a", "wss://relay-b.example", "", testPhonePub, testConsent); err != nil {
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
	attempt, err := s.Record("routing-a", "wss://relay-one", "", testPhonePub, testConsent)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if _, err := s.Resolve("routing-a", attempt.AttemptID, "relay: store failure"); err != nil {
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
	if _, err := re.Retire("routing-a", attempt.AttemptID); err != nil {
		t.Fatalf("Retire: %v", err)
	}
	if resolved, err := openStore(t, dir).Resolved(); err != nil || len(resolved) != 1 {
		t.Fatalf("Retire erased the tombstone: %+v, %v", resolved, err)
	}
	// A NEW obligation for the same routing id records fresh beside the tombstone.
	if _, err := re.Record("routing-a", "wss://relay-one", "", testPhonePub, testConsent); err != nil {
		t.Fatalf("re-Record: %v", err)
	}
	if pending, err := re.Pending(); err != nil || len(pending) != 1 {
		t.Fatalf("a fresh obligation after a tombstone must be pending: %+v, %v", pending, err)
	}
}

func TestSH5Store_TheTombstoneClockStartsAtResolutionNotRecording(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	attempt, err := s.Record("routing-a", "wss://relay-one", "", testPhonePub, testConsent)
	if err != nil {
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
	if _, err := s.Resolve("routing-a", attempt.AttemptID, "relay: store failure"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// A Record triggers the prune path; the fresh tombstone must survive it.
	if _, err := s.Record("routing-b", "wss://relay-one", "", testPhonePub, testConsent); err != nil {
		t.Fatalf("Record b: %v", err)
	}
	if resolved, err := s.Resolved(); err != nil || len(resolved) != 1 {
		t.Fatalf("the tombstone was pruned the day it was created (aged from RecordedAt, "+
			"not ResolvedAt): %+v, %v", resolved, err)
	}
}

func TestPurgeAtRelayRejectsMismatchedEvidenceBeforeDial(t *testing.T) {
	err := purgeAtRelay("", "", Obligation{
		RoutingID: "00000000000000000000000000000000",
		PhonePub:  testPhonePub,
		Consent:   testConsent,
	})
	if err == nil || err.Error() != "relaypurge: stored relay-v2 evidence does not match routing id" {
		t.Fatalf("purgeAtRelay mismatched evidence = %v", err)
	}
}

func TestDeferredRelayV2RefusalClassificationRetriesRuntimeErrors(t *testing.T) {
	for _, code := range []string{"internal_error", "invalid_session", "rate_limited", "unknown"} {
		if relayv2.IsPermanentAuthorizeRefusal(&relayv2.ProtocolError{Code: code}) {
			t.Fatalf("%s was classified as terminal", code)
		}
	}
	for _, code := range []string{"invalid_consent", "member_limit", "retirement_limit", "generation_exhausted"} {
		if !relayv2.IsPermanentAuthorizeRefusal(&relayv2.ProtocolError{Code: code}) {
			t.Fatalf("%s was not classified as terminal", code)
		}
	}
}

func TestDriveMachineObligations_ResolvesIncompleteLegacyEvidence(t *testing.T) {
	stateDir := t.TempDir()
	path := StorePath(stateDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy, err := json.Marshal([]Obligation{{
		RoutingID:  "0123456789abcdef0123456789abcdef",
		RelayURL:   "wss://retired.example",
		RecordedAt: time.Now(),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	var logs []string
	if left := DriveMachineObligations(stateDir, func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}); left != 0 {
		t.Fatalf("legacy incomplete obligation left %d pending", left)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if pending, err := store.Pending(); err != nil || len(pending) != 0 {
		t.Fatalf("legacy incomplete obligation remains pending: %+v, %v", pending, err)
	}
	resolved, err := store.Resolved()
	if err != nil || len(resolved) != 1 || !strings.Contains(resolved[0].Refusal, "legacy") {
		t.Fatalf("legacy cleanup tombstone = %+v, %v", resolved, err)
	}
	if got := strings.Join(logs, "\n"); !strings.Contains(got, "legacy") || !strings.Contains(got, "manual") {
		t.Fatalf("legacy cleanup was not loud and actionable: %q", got)
	}
}

func TestAttemptID_StaleRetireAndResolveCannotTouchReplacement(t *testing.T) {
	s := openStore(t, t.TempDir())
	a, err := s.Record("routing-a", "wss://relay-one", "", testPhonePub, testConsent)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Record("routing-a", "wss://relay-two", "", testPhonePub, testConsent)
	if err != nil {
		t.Fatal(err)
	}
	if a.AttemptID == "" || b.AttemptID == "" || a.AttemptID == b.AttemptID {
		t.Fatalf("attempt ids = %q, %q; want distinct non-empty tokens", a.AttemptID, b.AttemptID)
	}
	if _, err := s.Retire(a.RoutingID, a.AttemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resolve(a.RoutingID, a.AttemptID, "stale refusal"); err != nil {
		t.Fatal(err)
	}
	pending, err := s.Pending()
	if err != nil || len(pending) != 1 || pending[0].AttemptID != b.AttemptID {
		t.Fatalf("stale transitions erased replacement: %+v, %v", pending, err)
	}
}

func TestDriveSnapshotCannotEraseConcurrentReplacement(t *testing.T) {
	s := openStore(t, t.TempDir())
	a, err := s.Record("routing-a", "wss://relay-one", "", testPhonePub, testConsent)
	if err != nil {
		t.Fatal(err)
	}
	var b Obligation
	if err := Drive(context.Background(), s, func(context.Context, Obligation) error {
		b, err = s.Record("routing-a", "wss://relay-two", "", testPhonePub, testConsent)
		return err
	}); !errors.Is(err, errAttemptSuperseded) {
		t.Fatalf("Drive = %v, want superseded attempt", err)
	}
	pending, err := s.Pending()
	if err != nil || len(pending) != 1 || pending[0].AttemptID != b.AttemptID || b.AttemptID == a.AttemptID {
		t.Fatalf("Drive snapshot erased concurrent replacement: %+v, %v", pending, err)
	}
}

func TestDriveAcceptsAnActThatResolvedItsExactAttempt(t *testing.T) {
	s := openStore(t, t.TempDir())
	if _, err := s.Record("routing-a", "wss://relay-one", "", testPhonePub, testConsent); err != nil {
		t.Fatal(err)
	}
	if err := Drive(context.Background(), s, func(_ context.Context, ob Obligation) error {
		matched, err := s.Resolve(ob.RoutingID, ob.AttemptID, "permanent refusal")
		if err == nil && !matched {
			return errAttemptSuperseded
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Resolved(); err != nil || len(got) != 1 {
		t.Fatalf("resolved tombstone = %+v, %v", got, err)
	}
}

func TestFinishMachineDriveDoesNotClaimASettledConcurrentAttemptIsPending(t *testing.T) {
	s := openStore(t, t.TempDir())
	var logs []string
	left := finishMachineDrive(s, 0, errAttemptSuperseded, func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	})
	if left != 0 || strings.Contains(strings.Join(logs, "\n"), "still pending") {
		t.Fatalf("settled concurrent drive = left %d, logs %q", left, logs)
	}
	if _, err := s.Record("routing-a", "wss://relay-one", "", testPhonePub, testConsent); err != nil {
		t.Fatal(err)
	}
	left = finishMachineDrive(s, 0, errAttemptSuperseded, func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	})
	if left != 1 || !strings.Contains(strings.Join(logs, "\n"), "still pending") {
		t.Fatalf("replacement drive = left %d, logs %q", left, logs)
	}
}
