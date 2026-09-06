package main

// FAILING-FIRST tests for SH5 (bead agents-tracker-dtc5): the pending relay purge is
// DEFERRED TO RECONNECT rather than abandoned.
//
// The incident (2026-08-21, recorded in the bead): `swarm remote revoke` ran while the
// relay was down. The local half landed, the relay half printed "PENDING ... Nothing
// retries it, and this verb cannot re-address the device: the local record naming that
// routing id is already gone." -- and that was true: no pending-purge state was written
// and no later relay connection completed one (the honest-ceiling comment in
// performRevoke says exactly this, citing B120 F3). ADR-007 D9's own text blesses the
// deferral: "an offline-at-revoke machine defers the purge to reconnect". This slice
// builds the deferral D9 promised: performRevoke records a durable obligation, and the
// machine's next relay dial drives it.
//
// The rig is b1's (b1_revoke_relayverify_test.go): a real relay, a really-authorized
// route, a mailbox with b1MailboxItems frames -- what the purge exists to destroy.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remote/relaycfg"
	"github.com/Nathandela/swarm/internal/remote/relaypurge"
	"github.com/Nathandela/swarm/internal/remote/relayv2"
)

func TestImmediateRelayV2RefusalClassificationRetriesRuntimeErrors(t *testing.T) {
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

// sh5Pending is the store's pending list, fatally unwrapped.
func sh5Pending(t *testing.T, stateDir string) []relaypurge.Obligation {
	t.Helper()
	got, err := sh5Store(t, stateDir).Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	return got
}

// sh5RelayURL reads the relay URL `swarm remote init` persisted for the rig.
func sh5RelayURL(t *testing.T, stateDir string) string {
	t.Helper()
	cfg, found, err := relaycfg.Load(stateDir)
	if err != nil || !found {
		t.Fatalf("relaycfg.Load: found=%v err=%v", found, err)
	}
	return cfg.RelayURL
}

func sh5Store(t *testing.T, stateDir string) *relaypurge.Store {
	t.Helper()
	s, err := relaypurge.Open(filepath.Join(stateDir, "remote", "relay-purge-obligation.json"))
	if err != nil {
		t.Fatalf("relaypurge.Open: %v", err)
	}
	return s
}

// TestSH5_AnUnreachedRelayPurgeIsRecordedAsADurableObligation pins the record half: the
// pending arm's exit code and the local revocation are unchanged (B120 F3 owns those),
// but the abandonment is gone -- an obligation naming the routing id is durable on disk,
// and the operator line says the purge will be retried instead of disclaiming retry.
func TestSH5_AnUnreachedRelayPurgeIsRecordedAsADurableObligation(t *testing.T) {
	rig := b1NewRig(t, nil)
	if err := rig.relay.Close(); err != nil {
		t.Fatalf("close the relay to make it unreachable: %v", err)
	}

	exit, out := rig.b1Revoke(t)

	if exit == 0 {
		t.Errorf("B120 F3 still owns the exit code: pending purge must exit nonzero; output:\n%s", out)
	}
	rig.b1RequireDeviceGone(t)

	pending := sh5Pending(t, rig.stateDir)
	if len(pending) != 1 || pending[0].RoutingID != rig.routingID {
		t.Errorf("ADR-007 D9: the purge was deferred to reconnect, so a durable obligation "+
			"naming routing id %s must exist; got %+v", rig.routingID, pending)
	} else if pending[0].RelayURL == "" {
		t.Errorf("the obligation carries no relay URL: after a relay cutover the drive "+
			"cannot tell which relay it is owed against (review D2); got %+v", pending[0])
	}
	if strings.Contains(out, "nothing retries it") {
		t.Errorf("the output still disclaims the retry this slice built; output:\n%s", out)
	}
	if !strings.Contains(out, "next relay dial") {
		t.Errorf("the operator is owed the deferral in words -- when the purge will be "+
			"retried ('next relay dial') -- and the output does not say; output:\n%s", out)
	}
}

// TestSH5_ADriveNeverPurgesARoutingIDThatIsPairedAgain is u37c round 3's lesson applied
// here: the routing id is per-install, so the SAME handset re-paired comes back on the
// routing id a stale obligation names. Driving the purge then would empty the LIVE
// pairing's mailbox and ban its route while reporting success. The obligation must stay
// pending and undialed until an operator can settle it safely.
func TestSH5_ADriveNeverPurgesARoutingIDThatIsPairedAgain(t *testing.T) {
	rig := b1NewRig(t, nil) // the device is IN the registry: the re-paired world

	if _, err := sh5Store(t, rig.stateDir).Record(rig.routingID, sh5RelayURL(t, rig.stateDir), "", rig.rec.RelayAuthPub, rig.rec.ConsentSig); err != nil {
		t.Fatalf("record the stale obligation: %v", err)
	}

	var errOut bytes.Buffer
	if left := driveRelayPurgeObligations(rig.stateDir, &errOut); left != 1 {
		t.Fatalf("live obligation left=%d, want 1; stderr:\n%s", left, errOut.String())
	}

	if got := rig.relay.MailboxDepth(rig.routingID); got != b1MailboxItems {
		t.Errorf("the drive touched a LIVE routing id's mailbox: depth %d, want %d untouched",
			got, b1MailboxItems)
	}
	if pending := sh5Pending(t, rig.stateDir); len(pending) != 1 {
		t.Errorf("the live obligation must be kept, not retired: %+v", pending)
	}
	if !strings.Contains(strings.ToLower(errOut.String()), "not driven") {
		t.Errorf("the safety deferral is owed a reason on the operator channel; stderr:\n%s",
			errOut.String())
	}
}

// TestSH5_PairDrivesTheDeferredPurgeBeforeTheCeremony is a source gate in the repo's
// gate idiom, pinning the WIRING the unit tests above cannot see (the standing defect
// class: a verb can exist, be tested and be green while nothing calls it):
// runRemotePair must run the drive, and must run it BEFORE StartPairing, so a deferred
// ban lands before a new ceremony grants anything.
func TestSH5_PairDrivesTheDeferredPurgeBeforeTheCeremony(t *testing.T) {
	src, err := os.ReadFile("remote.go")
	if err != nil {
		t.Fatalf("read remote.go: %v", err)
	}
	i := strings.Index(string(src), "func runRemotePair(")
	if i < 0 {
		t.Fatalf("runRemotePair not found in remote.go")
	}
	body := string(src)[i:]
	if j := strings.Index(body[1:], "\nfunc "); j >= 0 {
		body = body[:j+1]
	}
	drive := strings.Index(body, "driveRelayPurgeObligations(")
	start := strings.Index(body, "StartPairing(")
	if drive < 0 {
		t.Fatalf("runRemotePair never drives the deferred relay purges: the obligation " +
			"store exists and nothing consumes it on the machine's next relay connection")
	}
	if start >= 0 && drive > start {
		t.Fatalf("runRemotePair drives the deferred purges AFTER StartPairing: the stale " +
			"ban must land before the new ceremony grants anything (B22 ordering)")
	}
}

// TestSH5_TheObligationIsRecordedBeforeTheDestructiveLocalRevoke is a source gate
// (review D1/codex #1): client.RevokeDevice deletes the only record carrying the
// routing id, so the obligation must be durable BEFORE it runs -- a crash or Ctrl-C in
// the relay stall between the two is otherwise the 2026-08-21 unrecoverable shape.
func TestSH5_TheObligationIsRecordedBeforeTheDestructiveLocalRevoke(t *testing.T) {
	src, err := os.ReadFile("remote.go")
	if err != nil {
		t.Fatalf("read remote.go: %v", err)
	}
	i := strings.Index(string(src), "func performRevoke(")
	if i < 0 {
		t.Fatalf("performRevoke not found")
	}
	body := string(src)[i:]
	if j := strings.Index(body[1:], "\nfunc "); j >= 0 {
		body = body[:j+1]
	}
	record := strings.Index(body, "recordPurgeObligation(")
	revoke := strings.Index(body, "client.RevokeDevice(")
	if record < 0 || revoke < 0 || record > revoke {
		t.Fatalf("performRevoke must record the purge obligation BEFORE client.RevokeDevice "+
			"(record at %d, revoke at %d)", record, revoke)
	}
}

// TestSH5_PairRefusesWhileAPurgeIsStillOwed (codex #4): granting new authority while
// the revoked route provably lives inverts B22's ordering -- and the replacement's own
// live routing id would then shield the stale mailbox from every later drive.
func TestSH5_PairRefusesWhileAPurgeIsStillOwed(t *testing.T) {
	rig := b1NewRig(t, nil)
	if err := rig.relay.Close(); err != nil {
		t.Fatalf("close the relay: %v", err)
	}
	if exit, _ := rig.b1Revoke(t); exit == 0 {
		t.Fatalf("precondition: the offline revoke must exit nonzero")
	}
	if len(sh5Pending(t, rig.stateDir)) != 1 {
		t.Fatalf("precondition: the offline revoke must leave one obligation")
	}

	var stdout, stderr bytes.Buffer
	exit := runRemote([]string{"pair"}, &stdout, &stderr)
	out := strings.ToLower(stdout.String() + stderr.String())

	if exit == 0 {
		t.Errorf("`swarm remote pair` exit = 0 while a deferred purge is owed and the relay "+
			"is unreachable; output:\n%s", out)
	}
	if !strings.Contains(out, "refused") {
		t.Errorf("the refusal is owed a sentence naming itself; output:\n%s", out)
	}
	if len(sh5Pending(t, rig.stateDir)) != 1 {
		t.Errorf("the refused pair must leave the obligation pending, not consume it")
	}
}

// TestSH5_AnObligationAgainstAnotherRelayIsRetiredLoudlyNotLanded (review D2): after
// `swarm remote init --relay-url` re-points the machine, a stale obligation can never
// land here. Retiring it silently as "landed" lies; keeping it blocks pairing forever
// behind a relay this machine no longer dials. It is retired LOUDLY, naming the old
// relay -- and the current relay's mailboxes are not touched.
func TestSH5_AnObligationAgainstAnotherRelayIsRetiredLoudlyNotLanded(t *testing.T) {
	rig := b1NewRig(t, nil)
	reg, err := device.Open(filepath.Join(rig.stateDir, "devices"))
	if err != nil {
		t.Fatalf("device.Open: %v", err)
	}
	if _, err := reg.Remove(rig.rec.DeviceID); err != nil {
		t.Fatalf("arrange the post-revoke registry: %v", err)
	}
	if _, err := sh5Store(t, rig.stateDir).Record(rig.routingID, "wss://old-relay.example", "", rig.rec.RelayAuthPub, rig.rec.ConsentSig); err != nil {
		t.Fatalf("record: %v", err)
	}

	var errOut bytes.Buffer
	if left := driveRelayPurgeObligations(rig.stateDir, &errOut); left != 0 {
		t.Errorf("a mismatched obligation must not count as still owed (it can never land "+
			"here); got %d, stderr:\n%s", left, errOut.String())
	}

	if got := rig.relay.MailboxDepth(rig.routingID); got != b1MailboxItems {
		t.Errorf("the drive purged the CURRENT relay for an obligation owed against another "+
			"one: depth %d, want %d", got, b1MailboxItems)
	}
	if pending := sh5Pending(t, rig.stateDir); len(pending) != 0 {
		t.Errorf("the mismatched obligation must be retired, not kept: %+v", pending)
	}
	if !strings.Contains(errOut.String(), "wss://old-relay.example") {
		t.Errorf("the retirement must name the relay the purge is owed against; stderr:\n%s",
			errOut.String())
	}
}

// TestSH5_APairedMachineNeverDialsForAForeignObligation pins the rule that preserves
// withMachineRelay's invariant: while ANY device is registered, the gateway owns this
// machine's single relay connection, so a drive finding a non-live obligation keeps it
// -- undialed, counted as owed -- rather than superseding the live gateway (review F5).
func TestSH5_APairedMachineNeverDialsForAForeignObligation(t *testing.T) {
	rig := b1NewRig(t, nil) // the device stays registered: a paired machine
	foreign := strings.Repeat("ab", 16)
	if _, err := sh5Store(t, rig.stateDir).Record(foreign, sh5RelayURL(t, rig.stateDir), "", rig.rec.RelayAuthPub, rig.rec.ConsentSig); err != nil {
		t.Fatalf("record: %v", err)
	}

	var errOut bytes.Buffer
	left := driveRelayPurgeObligations(rig.stateDir, &errOut)

	if left != 1 {
		t.Errorf("a foreign obligation on a paired machine must stay owed (got left=%d); "+
			"stderr:\n%s", left, errOut.String())
	}
	if pending := sh5Pending(t, rig.stateDir); len(pending) != 1 {
		t.Errorf("the foreign obligation must be kept, not retired: %+v", pending)
	}
	if got := rig.relay.MailboxDepth(rig.routingID); got != b1MailboxItems {
		t.Errorf("the live device's mailbox changed (depth %d, want %d): something dialed", got, b1MailboxItems)
	}
	if !strings.Contains(errOut.String(), "while a device is paired") {
		t.Errorf("the deferral is owed its reason; stderr:\n%s", errOut.String())
	}
}

// TestSH5_AMismatchedObligationRetiresLoudlyEvenOnAPairedMachine (round-2 codex #5):
// the mismatch ruling presents nothing to any relay, so the paired-machine-never-dials
// gate must not shadow it -- a mismatched obligation would otherwise sit forever on a
// paired machine, or worse, count against the pair gate.
func TestSH5_AMismatchedObligationRetiresLoudlyEvenOnAPairedMachine(t *testing.T) {
	rig := b1NewRig(t, nil) // device registered: a paired machine
	foreign := strings.Repeat("cd", 16)
	if _, err := sh5Store(t, rig.stateDir).Record(foreign, "wss://old-relay.example", "", rig.rec.RelayAuthPub, rig.rec.ConsentSig); err != nil {
		t.Fatalf("record: %v", err)
	}

	var errOut bytes.Buffer
	left := driveRelayPurgeObligations(rig.stateDir, &errOut)

	if left != 0 {
		t.Errorf("a mismatched obligation counted as owed on a paired machine (left=%d); "+
			"stderr:\n%s", left, errOut.String())
	}
	if pending := sh5Pending(t, rig.stateDir); len(pending) != 0 {
		t.Errorf("the mismatched obligation must retire regardless of pairing state: %+v", pending)
	}
	if !strings.Contains(errOut.String(), "wss://old-relay.example") {
		t.Errorf("the retirement must name the owed relay; stderr:\n%s", errOut.String())
	}
	if got := rig.relay.MailboxDepth(rig.routingID); got != b1MailboxItems {
		t.Errorf("something dialed the current relay (depth %d, want %d)", got, b1MailboxItems)
	}
}

// TestSH5_AnUndecodableReplyIsNotAnAnswerAndNeverTombstones (round-3 review F1,
// reproduced by the reviewer before the fix): a reply that fails to DECODE -- a
// truncated or oversized frame, a version-skewed relay, a middlebox -- is not the
// relay answering no. Substantive is an allowlist anchored on relay.ErrRelayAnswered
// (attached where answers are decoded); everything else defers. Before the fix this
// world printed "cleaning that state up at the relay is now a manual task" and
// permanently tombstoned a purge the next dial might have landed.
func TestSH5_AnUndecodableReplyIsNotAnAnswerAndNeverTombstones(t *testing.T) {
	rig := b1NewRig(t, sh5GarbageFront)

	exit, out := rig.b1Revoke(t)

	if exit == 0 {
		t.Errorf("an unlanded purge must exit nonzero; output:\n%s", out)
	}
	if !strings.Contains(out, "pending") {
		t.Errorf("a failed exchange is the PENDING state, not a refusal; output:\n%s", out)
	}
	if pending := sh5Pending(t, rig.stateDir); len(pending) != 1 {
		t.Errorf("the obligation must survive an undecodable reply: %+v", pending)
	}
	if resolved, err := sh5Store(t, rig.stateDir).Resolved(); err != nil || len(resolved) != 0 {
		t.Errorf("an undecodable reply must never tombstone: %+v, %v", resolved, err)
	}
}

// sh5GarbageFront forwards everything except device_revoke, which it answers with
// bytes that are not a well-formed relay frame at all.
func sh5GarbageFront(t *testing.T, upstream string) string {
	return sh5AnsweringFront(t, upstream, bytes.Repeat([]byte{0xFF}, 16))
}

// TestSH5_StatusSurfacesTheDeferredPurgeLedger: the owed and refused entries must be
// readable AFTER the revoke's stderr scrolled away -- `swarm remote status` is the
// forensic surface (round-3 review F2's "a JSON field with no reader").
func TestSH5_StatusSurfacesTheDeferredPurgeLedger(t *testing.T) {
	rig := b1NewRig(t, nil)
	// Exercise the formerly random collision in the status-only ledger fixture.
	// Status reads this ledger without requiring its RID to be a current device.
	rig.routingID = "aa" + rig.routingID[2:]
	refusedID := "bb" + rig.routingID[2:]
	st := sh5Store(t, rig.stateDir)
	if _, err := st.Record(rig.routingID, sh5RelayURL(t, rig.stateDir), "", rig.rec.RelayAuthPub, rig.rec.ConsentSig); err != nil {
		t.Fatalf("record: %v", err)
	}
	refusedAttempt, err := st.Record(refusedID, sh5RelayURL(t, rig.stateDir), "", rig.rec.RelayAuthPub, rig.rec.ConsentSig)
	if err != nil {
		t.Fatalf("record second: %v", err)
	}
	if _, err := st.Resolve(refusedID, refusedAttempt.AttemptID, "relay: store failure"); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if exit := runRemote([]string{"status"}, &stdout, &stderr); exit != 0 {
		t.Fatalf("status exit = %d; stderr=%q", exit, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "deferred relay purge OWED: routing id "+rig.routingID) {
		t.Errorf("status does not surface the owed purge; output:\n%s", out)
	}
	if !strings.Contains(out, "relay purge REFUSED") || !strings.Contains(out, "store failure") {
		t.Errorf("status does not surface the refused-purge tombstone with its reason; output:\n%s", out)
	}
}

// TestSH5_AProvisioningReadErrorKeepsTheObligation (round-2 Fable defect 1's fence,
// added round 4): a corrupt relay.json is a repairable operator condition, not a
// deprovisioning -- the drive must keep the obligation and count it owed, never
// retire it.
func TestSH5_AProvisioningReadErrorKeepsTheObligation(t *testing.T) {
	rig := b1NewRig(t, nil)
	reg, err := device.Open(filepath.Join(rig.stateDir, "devices"))
	if err != nil {
		t.Fatalf("device.Open: %v", err)
	}
	if _, err := reg.Remove(rig.rec.DeviceID); err != nil {
		t.Fatalf("arrange registry: %v", err)
	}
	if _, err := sh5Store(t, rig.stateDir).Record(rig.routingID, sh5RelayURL(t, rig.stateDir), "", rig.rec.RelayAuthPub, rig.rec.ConsentSig); err != nil {
		t.Fatalf("record: %v", err)
	}
	relayJSON := filepath.Join(rig.stateDir, "remote", "relay.json")
	good, err := os.ReadFile(relayJSON)
	if err != nil {
		t.Fatalf("read relay.json: %v", err)
	}
	if err := os.WriteFile(relayJSON, []byte("not json at all"), 0o600); err != nil {
		t.Fatalf("corrupt relay.json: %v", err)
	}
	t.Cleanup(func() { _ = os.WriteFile(relayJSON, good, 0o600) })

	var errOut bytes.Buffer
	left := driveRelayPurgeObligations(rig.stateDir, &errOut)

	if left == 0 {
		t.Errorf("a provisioning read error must count the obligation owed; stderr:\n%s", errOut.String())
	}
	if pending := sh5Pending(t, rig.stateDir); len(pending) != 1 {
		t.Errorf("a provisioning read error RETIRED the obligation (the round-2 fail-open, "+
			"returned): %+v", pending)
	}
}

// TestSH5_AnObligationUnderAPreviousMachineIdentityRetiresLoudly (round-3 codex #2):
// only the identity that owed the purge can present it; after machine.key is lost and
// regenerated, driving under the NEW identity must not let its not_authorized answer
// read as "settled" while the OLD identity's pairing survives at the relay.
func TestSH5_AnObligationUnderAPreviousMachineIdentityRetiresLoudly(t *testing.T) {
	rig := b1NewRig(t, nil)
	reg, err := device.Open(filepath.Join(rig.stateDir, "devices"))
	if err != nil {
		t.Fatalf("device.Open: %v", err)
	}
	if _, err := reg.Remove(rig.rec.DeviceID); err != nil {
		t.Fatalf("arrange registry: %v", err)
	}
	if _, err := sh5Store(t, rig.stateDir).Record(rig.routingID, sh5RelayURL(t, rig.stateDir),
		strings.Repeat("ef", 16), rig.rec.RelayAuthPub, rig.rec.ConsentSig); err != nil {
		t.Fatalf("record under the previous identity: %v", err)
	}

	var errOut bytes.Buffer
	left := driveRelayPurgeObligations(rig.stateDir, &errOut)

	if left != 0 {
		t.Errorf("an obligation only a dead identity can present must not stay owed (left=%d)", left)
	}
	if pending := sh5Pending(t, rig.stateDir); len(pending) != 0 {
		t.Errorf("the previous-identity obligation must retire (loudly), not wait forever: %+v", pending)
	}
	if !strings.Contains(errOut.String(), "previous machine identity") {
		t.Errorf("the retirement is owed its reason; stderr:\n%s", errOut.String())
	}
	if got := rig.relay.MailboxDepth(rig.routingID); got != b1MailboxItems {
		t.Errorf("something purged under the WRONG identity (depth %d, want %d)", got, b1MailboxItems)
	}
}

// TestSH5_ARegistryReadErrorFailsTheRevokeClosed (round-3 codex #1): with the routing
// id unreadable, the relay half can neither run nor be deferred -- the old behavior
// was a false exit-0 success over exactly that. The revoke must refuse BEFORE the
// destructive local delete.
func TestSH5_ARegistryReadErrorFailsTheRevokeClosed(t *testing.T) {
	rig := b1NewRig(t, nil)
	regFile := filepath.Join(rig.stateDir, "devices", "devices.json")
	good, err := os.ReadFile(regFile)
	if err != nil {
		t.Fatalf("read registry file: %v", err)
	}
	if err := os.WriteFile(regFile, []byte("not a registry"), 0o600); err != nil {
		t.Fatalf("corrupt registry: %v", err)
	}
	restore := func() { _ = os.WriteFile(regFile, good, 0o600) }
	t.Cleanup(restore)

	exit, out := rig.b1Revoke(t)

	if exit == 0 {
		t.Errorf("revoke exit = 0 with an unreadable registry: the relay half was neither "+
			"run nor deferred and the command claimed success; output:\n%s", out)
	}
	if !strings.Contains(out, "registry") {
		t.Errorf("the refusal is owed its reason; output:\n%s", out)
	}
	restore()
	var buf bytes.Buffer
	if exitDevices := runRemote([]string{"devices"}, &buf, &buf); exitDevices != 0 {
		t.Fatalf("devices after the refused revoke: exit %d", exitDevices)
	}
	if !strings.Contains(buf.String(), rig.rec.DeviceID) {
		t.Errorf("the refused revoke DELETED the device anyway -- it did not fail closed:\n%s",
			buf.String())
	}
}
