package main

// FAILING-FIRST (TDD RED, GG-5) for agents-tracker-2pnu F6: StampedExec's parse failure is
// swallowed at its only production call site.
//
// THE CONTRACT IS THE FUNCTION'S OWN, in as many words (unit.go): "A unit naming no program is
// an ERROR and never the empty string. An empty answer would reach a caller as a path that
// merely is not executable, so the day this parse stops matching the templates it would
// silently re-stamp and reload every unit on every pair, which is the opposite of what a check
// exists for."
//
// restampGatewayUnit then wrote `if err != nil || isExecutableFile(exe) { return }`, which
// collapses the two answers the contract separates. ErrNotInstalled -- no unit at all -- is a
// legitimate quiet return and the caller prints its own hint for it. A PARSE failure is a unit
// that is right there on disk and cannot be read: a hand-edited plist, a template this build no
// longer matches, a truncated write. Swallowed, it reads exactly like a healthy unit, so the
// one check that exists to notice a gateway exec'ing a path that is gone notices nothing and
// says nothing -- and the owner is back to a phone that pairs and then goes quiet.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestF6_AnUnreadableUnitIsReportedRatherThanTreatedAsHealthy stages a unit that is installed
// and does not parse, then pairs. The check cannot say whether the stamped program still
// exists, and saying nothing is the one answer it must not give.
func TestF6_AnUnreadableUnitIsReportedRatherThanTreatedAsHealthy(t *testing.T) {
	dir := shortStateDir(t)
	host := newScriptedPairingHost()
	startFakePairingDaemon(t, dir, host)
	good := fakeGatewayBinaryOnPath(t)
	swapExecutablePath(t, writeGatewayExecutable(t, filepath.Join(t.TempDir(), "swarm")))
	var initOut, initErr bytes.Buffer
	if exit := runRemoteInit(nil, &initOut, &initErr); exit != 0 {
		t.Fatalf("runRemoteInit exit = %d, want 0; stderr=%q", exit, initErr.String())
	}
	path := installRealUnit(t, dir, good)
	// The operator's hand-edit, or a template this build no longer matches: a file where the
	// unit was, naming no program on either platform's parser.
	if err := os.WriteFile(path, []byte("# hand-edited to get the machine back\n"), 0o600); err != nil {
		t.Fatalf("write unreadable unit: %v", err)
	}
	f := installFakeSupervisor(t)

	var stdout, stderr bytes.Buffer
	if exit := runRemotePair(nil, strings.NewReader("y\n"), &stdout, &stderr); exit != 0 {
		t.Fatalf("runRemotePair exit = %d, want 0; stderr=%q", exit, stderr.String())
	}

	said := stdout.String() + stderr.String()
	if !strings.Contains(said, "supervision unit") || !strings.Contains(said, "could not be read") {
		t.Errorf("nothing reported that this machine's supervision unit does not parse. "+
			"StampedExec fails loudly by contract and its only production caller drops the "+
			"error on the floor, so an unreadable unit is indistinguishable from a healthy "+
			"one -- which is the whole of what this check was added to notice:\n%s", said)
	}
	// The restraint is unchanged: a unit nobody could read is not a unit to bootout on the
	// pair path. What the fix owes is the sentence above, not a re-stamp on a guess.
	if got := f.count("stop"); got != 0 {
		t.Errorf("Stop called %d times over a unit that could not be parsed, want 0: booting out "+
			"a job on a read failure drops the connection of the phone being paired", got)
	}
}

// TestF6_AHealthyUnitReportsNothing is the control on the sentence above. A warning printed on
// every pair is a warning nobody reads by the third one.
func TestF6_AHealthyUnitReportsNothing(t *testing.T) {
	dir := shortStateDir(t)
	host := newScriptedPairingHost()
	startFakePairingDaemon(t, dir, host)
	good := fakeGatewayBinaryOnPath(t)
	swapExecutablePath(t, writeGatewayExecutable(t, filepath.Join(t.TempDir(), "swarm")))
	var initOut, initErr bytes.Buffer
	if exit := runRemoteInit(nil, &initOut, &initErr); exit != 0 {
		t.Fatalf("runRemoteInit exit = %d, want 0; stderr=%q", exit, initErr.String())
	}
	installRealUnit(t, dir, good)
	installFakeSupervisor(t)

	var stdout, stderr bytes.Buffer
	if exit := runRemotePair(nil, strings.NewReader("y\n"), &stdout, &stderr); exit != 0 {
		t.Fatalf("runRemotePair exit = %d, want 0; stderr=%q", exit, stderr.String())
	}
	if said := stdout.String() + stderr.String(); strings.Contains(said, "could not be read") {
		t.Errorf("a unit that parses fine was reported as unreadable:\n%s", said)
	}
}
