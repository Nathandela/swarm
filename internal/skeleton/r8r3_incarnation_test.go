package skeleton

// WAVE R8 / ROUND 3 -- THE INSTANCE BINDS AN INCARNATION, NOT A SESSION ID.
//
// THE DEFECT. `recordSessionInstance` had exactly ONE production caller,
// `adoptOrMintSessionInstance`, which mints only when the session has none. There was no
// production path that minted on REPLACEMENT, and `spawnShim` (internal/daemon/launch.go) has
// one caller -- `Launch` -- which always mints a fresh session id. So the instance was, in
// practice, PER SESSION ID: the very thing D-INSTANCE's own mutation fence says it must not
// be ("bind the generation to session id instead of instance -> the replacement test
// authorizes a stale generation against a new PTY"). Round 2's replacement arm reached the
// property by calling `d2.recordSessionInstance(...)` FROM THE TEST, so the helper was
// exercised and the production call site was unfenced -- defect class (5) verbatim.
//
// THE FIX IS AN OBSERVABLE, NOT A CALL SITE. What makes an incarnation an incarnation is the
// SHIM PROCESS: a daemon restart re-adopts the same shim (same pid), a replacement is a new
// shim (new pid). The instance side-file therefore records the incarnation it was minted
// for, and `adoptOrMintSessionInstance` re-mints when the incarnation it is asked about is
// not the one on file. Every production authoring path already carries a persist.Meta with a
// ShimPID, so the binding costs no new plumbing and no new call site to forget.
//
// A ZERO INCARNATION ADOPTS RATHER THAN RE-MINTS. Two states are indistinguishable from an
// unknown pid -- a pre-R8 side-file that records none, and a caller that has no meta -- and
// re-minting on either would reset the phone's view of every existing session exactly once,
// for no incarnation change at all.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestR8R3_AReplacementShimMintsANewInstanceThroughTheProductionPath is the fence the round-2
// version did not have: the replacement arm is reached by the PRODUCTION resolver, driven
// with a different incarnation, not by the test calling the recorder.
func TestR8R3_AReplacementShimMintsANewInstanceThroughTheProductionPath(t *testing.T) {
	dir := r8StateDir(t)
	const sessionID = "sess-r8r3-replacement"
	d := assembleAt(t, dir)

	first, err := d.adoptOrMintSessionInstance(sessionID, 4242)
	if err != nil {
		t.Fatalf("adoptOrMintSessionInstance (first incarnation): %v", err)
	}
	if first == "" {
		t.Fatalf("the first incarnation bound no instance")
	}

	// SAME incarnation: adopted, never re-minted. This is a daemon asking twice, which every
	// authoring path does.
	again, err := d.adoptOrMintSessionInstance(sessionID, 4242)
	if err != nil {
		t.Fatalf("adoptOrMintSessionInstance (same incarnation): %v", err)
	}
	if again != first {
		t.Errorf("asking twice about the SAME incarnation changed the instance (%q -> %q); the "+
			"phone would see an epoch reset with no shim restart behind it", first, again)
	}

	// A NEW SHIM for the same session id: a replacement. This is the arm that had no
	// production path at all.
	replaced, err := d.adoptOrMintSessionInstance(sessionID, 9999)
	if err != nil {
		t.Fatalf("adoptOrMintSessionInstance (replacement): %v", err)
	}
	if replaced == first {
		t.Fatalf("a REPLACEMENT shim kept the previous instance %q.\n"+
			"ADR-017 T8-a binds the capability record, the control generation and every "+
			"snapshot to the session INSTANCE, and makes replacement a synchronous severance "+
			"trigger. An instance that does not change across a new shim is the session id "+
			"wearing another name, and a generation minted against the old PTY would authorise "+
			"raw bytes into its replacement.", first)
	}
}

// TestR8R3_AnUnknownIncarnationAdoptsRatherThanReMints: the pre-R8 side-file, and any caller
// with no meta, must not be read as a replacement.
func TestR8R3_AnUnknownIncarnationAdoptsRatherThanReMints(t *testing.T) {
	dir := r8StateDir(t)
	const sessionID = "sess-r8r3-unknown"
	d := assembleAt(t, dir)

	first, err := d.adoptOrMintSessionInstance(sessionID, 111)
	if err != nil {
		t.Fatalf("adoptOrMintSessionInstance: %v", err)
	}
	got, err := d.adoptOrMintSessionInstance(sessionID, 0)
	if err != nil {
		t.Fatalf("adoptOrMintSessionInstance (unknown incarnation): %v", err)
	}
	if got != first {
		t.Errorf("an UNKNOWN incarnation re-minted (%q -> %q). Two states are indistinguishable "+
			"from a zero pid -- a pre-R8 side-file and a caller with no meta -- and re-minting "+
			"on either resets every existing session's view once, for no shim restart.", first, got)
	}
}

// TestR8R3_APreR8SideFileIsAdoptedNotDiscarded: the instance file this wave shipped in round 1
// and round 2 carries no incarnation. A daemon that read it as "incarnation 0, therefore not
// mine" would re-mint for every session on the upgrade that lands this change.
func TestR8R3_APreR8SideFileIsAdoptedNotDiscarded(t *testing.T) {
	dir := r8StateDir(t)
	const sessionID = "sess-r8r3-legacyfile"
	d := assembleAt(t, dir)

	// Write the ROUND-2 file format by hand: the bare instance, nothing else.
	path := filepath.Join(dir, sessionID, sessionInstanceFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	const legacy = "0123456789abcdef0123456789abcdef"
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatalf("plant the round-2 file: %v", err)
	}

	got, err := d.adoptOrMintSessionInstance(sessionID, 777)
	if err != nil {
		t.Fatalf("adoptOrMintSessionInstance over a pre-R8 file: %v", err)
	}
	if got != legacy {
		t.Fatalf("a pre-R8 instance file was discarded (%q -> %q); every session on the machine "+
			"would show the phone an epoch reset on the upgrade that lands the incarnation "+
			"binding", legacy, got)
	}
}

// TestR8R3_TheInstanceFileRecordsTheIncarnationItWasMintedFor pins the durable half: without
// it on disk, a daemon restart has nothing to compare against and every restart is a
// replacement.
func TestR8R3_TheInstanceFileRecordsTheIncarnationItWasMintedFor(t *testing.T) {
	dir := r8StateDir(t)
	const sessionID = "sess-r8r3-durable"
	d1 := assembleAt(t, dir)
	first, err := d1.adoptOrMintSessionInstance(sessionID, 31337)
	if err != nil {
		t.Fatalf("adoptOrMintSessionInstance: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, sessionID, sessionInstanceFile))
	if err != nil {
		t.Fatalf("read the instance side-file: %v", err)
	}
	if !strings.Contains(string(raw), "31337") {
		t.Fatalf("the instance side-file %q does not record the incarnation it was minted for; "+
			"a restarted daemon has nothing to compare a shim pid against and cannot tell "+
			"adoption from replacement", string(raw))
	}
	if err := d1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// A FRESH daemon, no memory: same shim, adopt; different shim, replace.
	d2 := assembleAt(t, dir)
	adopted, err := d2.adoptOrMintSessionInstance(sessionID, 31337)
	if err != nil {
		t.Fatalf("adoptOrMintSessionInstance after restart: %v", err)
	}
	if adopted != first {
		t.Errorf("a daemon restart over the SAME shim changed the instance (%q -> %q)", first, adopted)
	}
	replaced, err := d2.adoptOrMintSessionInstance(sessionID, 40404)
	if err != nil {
		t.Fatalf("adoptOrMintSessionInstance after restart (replacement): %v", err)
	}
	if replaced == first {
		t.Errorf("a daemon restart followed by a REPLACEMENT shim kept the old instance %q", first)
	}
}
