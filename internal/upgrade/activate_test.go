package upgrade

// The activation transaction's gates, each as a differential (lifecycle R3).
// The exec seam records instead of replacing the process, so every test drives
// Activate to the brink of the handoff and asserts what would have been exec'd.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

// stageBuild writes a staged build by hand: a runnable fake swarm that answers
// `version` with tag, plus the compatibility card.
func stageBuild(t *testing.T, stateDir, tag string, card *CompatManifest) {
	t.Helper()
	stage := StageDir(stateDir)
	if err := os.MkdirAll(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("#!/bin/sh\necho swarm %s\n", strings.TrimPrefix(tag, "v"))
	if err := os.WriteFile(filepath.Join(stage, "swarm"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "VERSION"), []byte(tag+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if card != nil {
		data, _ := json.Marshal(card)
		if err := os.WriteFile(filepath.Join(stage, "compat.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// runningSession persists a running meta with the given shim wire version.
func runningSession(t *testing.T, stateDir, id string, wire int) {
	t.Helper()
	dir := filepath.Join(stateDir, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	m := persist.Meta{ID: id, SchemaVersion: persist.SchemaVersion,
		Status: status.Status{Process: status.ProcessRunning}, ShimWireVersion: wire}
	data, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// captureExec substitutes the exec seam and reports what Activate handed off.
func captureExec(t *testing.T) *[][]string {
	t.Helper()
	var calls [][]string
	orig := execFn
	execFn = func(argv0 string, argv []string, env []string) error {
		guard := ""
		for _, kv := range env {
			if strings.HasPrefix(kv, handoffGuardEnv+"=") {
				guard = kv
			}
		}
		calls = append(calls, append([]string{argv0, guard}, argv...))
		return nil
	}
	t.Cleanup(func() { execFn = orig })
	return &calls
}

func installTarget(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "swarm")
	if err := os.WriteFile(p, []byte("#!/bin/sh\necho swarm 0.1.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestActivateInstallsAndHandsOffToTheNewBinarysConverge(t *testing.T) {
	state := t.TempDir()
	bin := installTarget(t)
	old, _ := os.ReadFile(bin)
	stageBuild(t, state, "v9.9.9", &CompatManifest{Version: "v9.9.9", Shimwire: 1, Protocol: 1, Schema: 1})
	calls := captureExec(t)

	st, err := Activate(ActivateOptions{StateDir: state, BinPath: bin})
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if st.Outcome != "activated" {
		t.Fatalf("outcome = %q (%s), want activated", st.Outcome, st.Detail)
	}
	if now, _ := os.ReadFile(bin); string(now) == string(old) {
		t.Error("the target binary was not replaced")
	}
	if prev, _ := os.ReadFile(filepath.Join(PrevDir(state), "swarm")); string(prev) != string(old) {
		t.Error("the rollback slot does not hold the outgoing binary")
	}
	if len(*calls) != 1 {
		t.Fatalf("exec calls = %d, want exactly one handoff", len(*calls))
	}
	installed, err := filepath.EvalSymlinks(bin)
	if err != nil {
		t.Fatalf("resolve installed binary: %v", err)
	}
	call := (*calls)[0]
	if call[0] != installed || call[1] != handoffGuardEnv+"=1" {
		t.Errorf("handoff = %v, want the INSTALLED binary with the loop guard", call)
	}
	if got := strings.Join(call[2:], " "); got != "swarm daemon restart --unattended" {
		t.Errorf("handoff argv = %q -- the one load-bearing line of the shell prototype, in Go", got)
	}
}

func TestActivateDefersOnAWireBumpWithLiveSessions(t *testing.T) {
	state := t.TempDir()
	bin := installTarget(t)
	old, _ := os.ReadFile(bin)
	runningSession(t, state, "livesession", 1)
	stageBuild(t, state, "v9.9.9", &CompatManifest{Version: "v9.9.9", Shimwire: 2, Protocol: 1, Schema: 1})
	calls := captureExec(t)

	st, err := Activate(ActivateOptions{StateDir: state, BinPath: bin})
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if st.Outcome != "deferred-wirebump" {
		t.Fatalf("outcome = %q, want deferred-wirebump", st.Outcome)
	}
	if now, _ := os.ReadFile(bin); string(now) != string(old) {
		t.Error("a wire-bumped build was INSTALLED under a live daemon -- the compat matrix pins this as ProcessLost (stage, do not install)")
	}
	if _, err := os.Stat(filepath.Join(StageDir(state), "swarm")); err != nil {
		t.Error("the staged build was discarded; a deferral must keep it for the idle night")
	}
	if len(*calls) != 0 {
		t.Error("a deferral handed off to converge")
	}
}

func TestActivateDefersWithoutACardWhenSessionsAreLive(t *testing.T) {
	state := t.TempDir()
	bin := installTarget(t)
	runningSession(t, state, "livesession", 0)
	stageBuild(t, state, "v9.9.9", nil) // no compat.json: an older pipeline's archive
	captureExec(t)

	st, err := Activate(ActivateOptions{StateDir: state, BinPath: bin})
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if st.Outcome != "deferred-wirebump" {
		t.Fatalf("outcome = %q, want deferred-wirebump (unknown gates conservatively)", st.Outcome)
	}
}

func TestActivateRefusesABuildThatCannotAnswerForItself(t *testing.T) {
	state := t.TempDir()
	bin := installTarget(t)
	old, _ := os.ReadFile(bin)
	stageBuild(t, state, "v9.9.9", &CompatManifest{Shimwire: 1})
	// Corrupt the staged binary AFTER staging: it now answers with the wrong
	// version, the wrong-arch/truncated-build stand-in (Gemini finding 2).
	if err := os.WriteFile(filepath.Join(StageDir(state), "swarm"), []byte("#!/bin/sh\necho swarm 0.0.0-wrong\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	captureExec(t)

	st, err := Activate(ActivateOptions{StateDir: state, BinPath: bin})
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if st.Outcome != "failed-sanity" {
		t.Fatalf("outcome = %q, want failed-sanity", st.Outcome)
	}
	if now, _ := os.ReadFile(bin); string(now) != string(old) {
		t.Error("a build that cannot answer for itself was installed -- the rollback command would live inside the brick")
	}
}

func TestActivateWithNothingStagedRefuses(t *testing.T) {
	state := t.TempDir()
	if _, err := Activate(ActivateOptions{StateDir: state, BinPath: installTarget(t)}); err != ErrNothingStaged {
		t.Fatalf("err = %v, want ErrNothingStaged", err)
	}
}

func TestRollbackRestoresHoldsAndHandsOff(t *testing.T) {
	state := t.TempDir()
	bin := installTarget(t)
	// Activate first, so real slots exist.
	stageBuild(t, state, "v9.9.9", &CompatManifest{Shimwire: 1, Schema: persist.SchemaVersion})
	old, _ := os.ReadFile(bin)
	calls := captureExec(t)
	if st, _ := Activate(ActivateOptions{StateDir: state, BinPath: bin}); st.Outcome != "activated" {
		t.Fatalf("setup activate: %q", st.Outcome)
	}

	st, err := Rollback(ActivateOptions{StateDir: state, BinPath: bin})
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if st.Outcome != "rolled-back" {
		t.Fatalf("outcome = %q (%s), want rolled-back", st.Outcome, st.Detail)
	}
	if now, _ := os.ReadFile(bin); string(now) != string(old) {
		t.Error("the previous binary was not restored")
	}
	if held := HeldVersion(state); held == "" {
		t.Error("no hold was written; the nightly would reinstall the rolled-back release within 24h")
	}
	if len(*calls) != 2 {
		t.Errorf("exec calls = %d, want activation's and rollback's handoffs", len(*calls))
	}
}

func TestRollbackRefusesAcrossASchemaBump(t *testing.T) {
	state := t.TempDir()
	bin := installTarget(t)
	prev := PrevDir(state)
	if err := os.MkdirAll(prev, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"swarm":       "#!/bin/sh\necho swarm 0.0.9\n",
		"VERSION":     "v0.0.9\n",
		"compat.json": `{"version":"v0.0.9","shimwire":1,"protocol":1,"schema":1}`,
	} {
		if err := os.WriteFile(filepath.Join(prev, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A session persisted by a NEWER schema than the rollback build supports.
	dir := filepath.Join(state, "newer")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"schema_version":2,"id":"newer"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	old, _ := os.ReadFile(bin)
	captureExec(t)

	st, err := Rollback(ActivateOptions{StateDir: state, BinPath: bin})
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if st.Outcome != "refused-schema" {
		t.Fatalf("outcome = %q, want refused-schema", st.Outcome)
	}
	if now, _ := os.ReadFile(bin); string(now) != string(old) {
		t.Error("a schema-incompatible rollback was installed -- the restored daemon would refuse the board at Open")
	}
}

func TestAHeldVersionIsSkippedByStage(t *testing.T) {
	f := newFixture(t, "v9.9.9")
	state := t.TempDir()
	if err := os.MkdirAll(filepath.Join(state, "upgrade"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(holdPath(state), []byte("v9.9.9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := Stage(t.Context(), Options{StateDir: state, BinPath: selfOwnedBin(t), Installed: "0.1.0", BaseURL: f.srv.URL})
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if st.Outcome != "held" {
		t.Fatalf("outcome = %q, want held: a rolled-back release must not come back until a newer one ships", st.Outcome)
	}
}
