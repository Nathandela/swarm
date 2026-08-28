package upgrade

// Round-2 differentials: the R2+R3 audit's findings (codex 1-7, Fable H1-M5),
// each pinned so the fix cannot regress silently.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func noDaemon() func() bool { return func() bool { return false } }

// codex 1: the durable pending phase -- activation leaves the marker, and only
// a confirmed converge (the unattended retry path) clears it.
func TestActivateLeavesThePendingConvergeMarker(t *testing.T) {
	state := t.TempDir()
	bin := installTarget(t)
	stageBuild(t, state, "v9.9.9", &CompatManifest{Shimwire: 1, Schema: 1})
	captureExec(t)
	if st, _ := Activate(ActivateOptions{StateDir: state, BinPath: bin, DaemonAlive: noDaemon()}); st.Outcome != "activated" {
		t.Fatalf("outcome %q", st.Outcome)
	}
	if got := PendingConverge(state); got != "v9.9.9" {
		t.Fatalf("PendingConverge = %q, want v9.9.9 -- without the marker, a converge that "+
			"defers around work leaves the old daemon running forever behind a binary that reads current", got)
	}
	if err := ClearPendingConverge(state); err != nil || PendingConverge(state) != "" {
		t.Fatalf("ClearPendingConverge: %v / %q", err, PendingConverge(state))
	}
}

// Fable M3b + codex 4: a retry of an interrupted activation must not rebuild
// the rollback slots from the half-upgraded install.
func TestARetryPreservesTheOriginalRollbackSlots(t *testing.T) {
	state := t.TempDir()
	bin := installTarget(t)
	// The interrupted attempt's leftovers: marker set, true originals in prev.
	if err := os.MkdirAll(PrevDir(state), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(PrevDir(state), "swarm"), []byte("THE ORIGINAL"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(PrevDir(state), "VERSION"), []byte("v0.0.1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(pendingPath(state)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pendingPath(state), []byte("v9.9.9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stageBuild(t, state, "v9.9.9", &CompatManifest{Shimwire: 1, Schema: 1})
	captureExec(t)
	if st, _ := Activate(ActivateOptions{StateDir: state, BinPath: bin, DaemonAlive: noDaemon()}); st.Outcome != "activated" {
		t.Fatalf("outcome %q", st.Outcome)
	}
	if got, _ := os.ReadFile(filepath.Join(PrevDir(state), "swarm")); string(got) != "THE ORIGINAL" {
		t.Error("the retry rebuilt the rollback slots from the half-upgraded install, destroying the only true originals")
	}
}

// Fable M3b: a staged tag equal to the installed version is an interrupted
// activation's leftover, answered current -- never re-run against itself.
func TestActivateNoopsWhenTheStagedBuildIsAlreadyInstalled(t *testing.T) {
	state := t.TempDir()
	bin := installTarget(t)
	stageBuild(t, state, "v9.9.9", &CompatManifest{Shimwire: 1, Schema: 1})
	calls := captureExec(t)
	st, err := Activate(ActivateOptions{StateDir: state, BinPath: bin, Installed: "9.9.9", DaemonAlive: noDaemon()})
	if err != nil {
		t.Fatal(err)
	}
	if st.Outcome != "current" {
		t.Fatalf("outcome = %q, want current", st.Outcome)
	}
	if len(*calls) != 0 {
		t.Error("an already-installed build was handed off again")
	}
	if _, err := os.Stat(StageDir(state)); !os.IsNotExist(err) {
		t.Error("the spent stage was kept")
	}
}

// codex 2: unreadable session state gates CLOSED.
func TestAnUnreadableSessionMetaDefersActivation(t *testing.T) {
	state := t.TempDir()
	bin := installTarget(t)
	dir := filepath.Join(state, "corrupt")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	stageBuild(t, state, "v9.9.9", &CompatManifest{Shimwire: 2, Schema: 1})
	captureExec(t)
	st, err := Activate(ActivateOptions{StateDir: state, BinPath: bin, DaemonAlive: noDaemon()})
	if err != nil {
		t.Fatal(err)
	}
	if st.Outcome != "deferred-wirebump" || !strings.Contains(st.Detail, "cannot be read") {
		t.Fatalf("outcome = %q (%s), want a fail-closed deferral naming the unreadable record", st.Outcome, st.Detail)
	}
}

// codex 2: a wire bump with ZERO sessions still defers while a daemon holds
// the lock -- and proceeds when none does.
func TestAWireBumpRespectsDaemonLivenessNotJustSessions(t *testing.T) {
	state := t.TempDir()
	bin := installTarget(t)
	stageBuild(t, state, "v9.9.9", &CompatManifest{Shimwire: 2, Schema: 1})
	captureExec(t)

	alive := func() bool { return true }
	st, err := Activate(ActivateOptions{StateDir: state, BinPath: bin, DaemonAlive: alive})
	if err != nil {
		t.Fatal(err)
	}
	if st.Outcome != "deferred-wirebump" {
		t.Fatalf("outcome with a live daemon = %q, want deferred-wirebump: zero sessions is not zero daemon", st.Outcome)
	}

	st, err = Activate(ActivateOptions{StateDir: state, BinPath: bin, DaemonAlive: noDaemon()})
	if err != nil {
		t.Fatal(err)
	}
	if st.Outcome != "activated" {
		t.Fatalf("outcome with no daemon = %q (%s), want activated", st.Outcome, st.Detail)
	}
}

// The nil probe fails closed.
func TestANilDaemonProbeAssumesAlive(t *testing.T) {
	state := t.TempDir()
	bin := installTarget(t)
	stageBuild(t, state, "v9.9.9", &CompatManifest{Shimwire: 2, Schema: 1})
	captureExec(t)
	st, _ := Activate(ActivateOptions{StateDir: state, BinPath: bin})
	if st.Outcome != "deferred-wirebump" {
		t.Fatalf("outcome = %q, want deferred-wirebump: an absent probe must never fail open", st.Outcome)
	}
}

// codex 4: a target swarm-remote with no staged sibling refuses outright.
func TestAMissingStagedRemoteRefusesTheMixedPair(t *testing.T) {
	state := t.TempDir()
	bin := installTarget(t)
	if err := os.WriteFile(filepath.Join(filepath.Dir(bin), "swarm-remote"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	stageBuild(t, state, "v9.9.9", &CompatManifest{Shimwire: 1, Schema: 1}) // stages swarm only
	captureExec(t)
	st, err := Activate(ActivateOptions{StateDir: state, BinPath: bin, DaemonAlive: noDaemon()})
	if err != nil {
		t.Fatal(err)
	}
	if st.Outcome != "failed-install" || !strings.Contains(st.Detail, "mixed pair") {
		t.Fatalf("outcome = %q (%s), want the mixed-pair refusal", st.Outcome, st.Detail)
	}
}

// Fable M1: activation refuses across a persistence-schema bump exactly as
// rollback does -- the --allow-downgrade brick, closed.
func TestActivateRefusesAcrossASchemaBump(t *testing.T) {
	state := t.TempDir()
	bin := installTarget(t)
	dir := filepath.Join(state, "newer")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"schema_version":2,"id":"newer"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stageBuild(t, state, "v0.0.9", &CompatManifest{Shimwire: 1, Schema: 1})
	captureExec(t)
	st, err := Activate(ActivateOptions{StateDir: state, BinPath: bin, DaemonAlive: noDaemon()})
	if err != nil {
		t.Fatal(err)
	}
	if st.Outcome != "refused-schema" {
		t.Fatalf("outcome = %q (%s), want refused-schema", st.Outcome, st.Detail)
	}
}

// Fable H1: rollback consumes its slots -- a second run finds nothing, and the
// first hold survives.
func TestASecondRollbackRefusesAndTheHoldSurvives(t *testing.T) {
	state := t.TempDir()
	bin := installTarget(t)
	stageBuild(t, state, "v9.9.9", &CompatManifest{Shimwire: 1, Schema: 1})
	captureExec(t)
	if st, _ := Activate(ActivateOptions{StateDir: state, BinPath: bin, DaemonAlive: noDaemon()}); st.Outcome != "activated" {
		t.Fatalf("setup activate: %q", st.Outcome)
	}
	if st, _ := Rollback(ActivateOptions{StateDir: state, BinPath: bin, DaemonAlive: noDaemon()}); st.Outcome != "rolled-back" {
		t.Fatalf("first rollback: %q", st.Outcome)
	}
	firstHold := HeldVersion(state)
	if firstHold != currentVersionTag() {
		t.Fatalf("hold = %q, want %q (the version rolled AWAY from)", firstHold, currentVersionTag())
	}
	if _, err := Rollback(ActivateOptions{StateDir: state, BinPath: bin, DaemonAlive: noDaemon()}); err != ErrNothingToRollBack {
		t.Fatalf("second rollback err = %v, want ErrNothingToRollBack -- re-running must not overwrite the hold and re-arm the bad release", err)
	}
	if HeldVersion(state) != firstHold {
		t.Error("the second rollback attempt changed the hold")
	}
}

// Fable/codex 5: rollback slots without a readable card refuse.
func TestRollbackRefusesSlotsWithoutACard(t *testing.T) {
	state := t.TempDir()
	bin := installTarget(t)
	prev := PrevDir(state)
	if err := os.MkdirAll(prev, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prev, "swarm"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prev, "VERSION"), []byte("v0.0.9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	captureExec(t)
	st, err := Rollback(ActivateOptions{StateDir: state, BinPath: bin, DaemonAlive: noDaemon()})
	if err != nil {
		t.Fatal(err)
	}
	if st.Outcome != "refused-card" {
		t.Fatalf("outcome = %q, want refused-card: an unverifiable restore is refused, never risked", st.Outcome)
	}
}

// Fable L9: the sanity check matches the version as a whole token.
func TestSanityCheckRejectsAPrefixVersion(t *testing.T) {
	state := t.TempDir()
	bin := installTarget(t)
	stageBuild(t, state, "v0.1.1", &CompatManifest{Shimwire: 1, Schema: 1})
	if err := os.WriteFile(filepath.Join(StageDir(state), "swarm"), []byte("#!/bin/sh\necho swarm 0.1.10\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	captureExec(t)
	st, _ := Activate(ActivateOptions{StateDir: state, BinPath: bin, DaemonAlive: noDaemon()})
	if st.Outcome != "failed-sanity" {
		t.Fatalf("outcome = %q, want failed-sanity: 0.1.10 is not 0.1.1", st.Outcome)
	}
}

// Fable M5: the REAL signer round-trips against the REAL verifier -- the two
// deliberately independent implementations, proven byte-compatible.
func TestSignchecksumsRoundTripsThroughVerifyChecksums(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	orig := releasePublicKeys
	releasePublicKeys = []string{hex.EncodeToString(pub)}
	t.Cleanup(func() { releasePublicKeys = orig })

	dir := t.TempDir()
	in := filepath.Join(dir, "checksums.txt")
	if err := os.WriteFile(in, []byte("deadbeef  swarm_9.9.9_linux_amd64.tar.gz\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(context.Background(), "go", "run", "../../scripts/signchecksums", "--in", in)
	cmd.Env = append(os.Environ(), "SWARM_RELEASE_SIGNING_KEY="+base64.StdEncoding.EncodeToString(priv.Seed()))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("signchecksums: %v (%s)", err, out)
	}
	cs, _ := os.ReadFile(in)
	sig, err := os.ReadFile(in + ".sig")
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyChecksums(cs, sig); err != nil {
		t.Fatalf("the production verifier rejects the production signer's output: %v -- a byte-level "+
			"drift here is a fleet-wide silent update outage", err)
	}
}
