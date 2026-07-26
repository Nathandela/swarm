package main

// FAILING-FIRST tests for the `swarm remote revoke <unpaired-id>` asymmetry recorded in
// docs/verification/remote-phaseB-residuals.md §3.
//
// THE DEFECT. `swarm remote revoke` printed "revoked device <id>" and exited 0 for a device
// id the machine has never paired. During a device-loss incident that is exactly the output
// which says the lost phone is cut off, produced by a command that cut nothing off -- and a
// mistyped id is the likeliest way to reach it, because the operator is copying a 64-hex id
// under pressure.
//
// IT IS NOT A PURE NO-OP EITHER, which the residual did not record: runRemoteRevoke runs
// purgeOutboundCustody unconditionally once the daemon reports no error, so a mistyped id
// ALSO empties the machine's outbound journal -- the frames queued for the phone that IS
// still paired. The refusal therefore has to land BEFORE those side effects, which is why
// these tests assert on the surviving device and the exit code together.
//
// `swarm remote regrant` already refuses the same id properly (skeleton.RegrantDevice fails
// closed on an unknown id), so the second test pins the two verbs to the SAME shape rather
// than inventing a second vocabulary for the same condition.
//
// INTENDED PRODUCTION (RED before it exists): handleDeviceRevoke replies an error when an
// OWNER-tier revoke removed nothing. Owner-tier only, because the remote tier is the phone's
// panic button arriving over an at-least-once relay, where a retry of an already-successful
// revoke legitimately removes nothing and must still be answered OK.

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/device"
)

// unpairedDeviceID is a well-formed device id that no test seeds: 64 hex characters, the
// shape device.DeviceIDFor produces, so the refusal cannot be attributed to a malformed
// argument that some earlier validation happened to catch.
const unpairedDeviceID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// TestRemoteRevoke_UnpairedIDIsRefused drives `swarm remote revoke <unpaired-id>` against a
// REAL in-process daemon holding one paired device, and pins the whole refusal: nonzero
// exit, no success line on stdout, a reason on stderr naming the id, and the device that IS
// paired still paired.
func TestRemoteRevoke_UnpairedIDIsRefused(t *testing.T) {
	dir := shortStateDir(t)
	keepID := seedDevice(t, dir, "Nathan's iPhone", device.CapFull)
	startCLIDaemon(t, dir)

	var stdout, stderr bytes.Buffer
	exit := runRemote([]string{"revoke", unpairedDeviceID}, &stdout, &stderr)

	if exit == 0 {
		t.Errorf("runRemote([revoke %s]) exit = 0 for a device that was never paired; an "+
			"operator reading this during a device-loss incident is told the lost handset is "+
			"cut off. stdout=%q stderr=%q", unpairedDeviceID, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), "revoked device") {
		t.Errorf("stdout claims a revocation that did not happen:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "no such device") {
		t.Errorf("stderr does not say the id is not paired; got:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), unpairedDeviceID) {
		t.Errorf("stderr does not name the rejected id %q, so the operator cannot see the "+
			"typo; got:\n%s", unpairedDeviceID, stderr.String())
	}

	var listOut, listErr bytes.Buffer
	if exit := runRemote([]string{"devices"}, &listOut, &listErr); exit != 0 {
		t.Fatalf("runRemote([devices]) exit = %d; stderr=%q", exit, listErr.String())
	}
	if !strings.Contains(listOut.String(), keepID) {
		t.Errorf("the paired device %q is gone after revoking an UNPAIRED id; devices table:\n%s",
			keepID, listOut.String())
	}
}

// TestRemoteRevoke_UnpairedRefusalMatchesRegrant puts the SAME unpaired id through both
// verbs and pins them to one shape. regrant is the reference because it already refused
// correctly; revoke is the one that had to change.
//
// It is a separate test from the one above so a regression tells you WHICH property broke:
// that revoke refuses at all, or that its refusal drifted away from regrant's.
func TestRemoteRevoke_UnpairedRefusalMatchesRegrant(t *testing.T) {
	dir := shortStateDir(t)
	seedDevice(t, dir, "Nathan's iPhone", device.CapFull)
	startCLIDaemon(t, dir)

	var revokeOut, revokeErr bytes.Buffer
	revokeExit := runRemote([]string{"revoke", unpairedDeviceID}, &revokeOut, &revokeErr)

	var regrantOut, regrantErr bytes.Buffer
	regrantExit := runRemote([]string{"regrant", unpairedDeviceID}, &regrantOut, &regrantErr)

	// The reference verb must actually be refusing, or every comparison below is vacuous.
	if regrantExit == 0 {
		t.Fatalf("precondition: `remote regrant %s` exit = 0 for an unpaired id; stdout=%q stderr=%q",
			unpairedDeviceID, regrantOut.String(), regrantErr.String())
	}
	if regrantExit != revokeExit {
		t.Errorf("`remote revoke` exits %d and `remote regrant` exits %d for the same unpaired "+
			"id; one condition must not have two exit statuses. revoke stderr=%q",
			revokeExit, regrantExit, revokeErr.String())
	}
	if revokeOut.Len() != 0 {
		t.Errorf("`remote revoke` wrote to stdout while refusing; regrant writes nothing. got:\n%s",
			revokeOut.String())
	}
	for _, phrase := range []string{"no such device", unpairedDeviceID} {
		if !strings.Contains(regrantErr.String(), phrase) {
			t.Fatalf("precondition: regrant's refusal does not contain %q; got:\n%s",
				phrase, regrantErr.String())
		}
		if !strings.Contains(revokeErr.String(), phrase) {
			t.Errorf("revoke's refusal does not contain %q, which regrant's does; got:\n%s",
				phrase, revokeErr.String())
		}
	}
}
