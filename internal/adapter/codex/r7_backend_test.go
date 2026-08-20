package codex

// FAILING-FIRST (TDD RED, GG-5) for Mirror M4.1's adapter half -- the Codex adapter
// DESCRIBES its app-server backend and nothing else (ADR-010 amendment 2026-08-20,
// ADR-013 §R7.2). Bead: agents-tracker-hggx.8.
//
// THE ARGV IS RECORDED, NOT GUESSED. docs/verification/r1-codex-gate.md:53 ran
//
//	codex app-server --listen unix://$SCRATCH/codex.sock
//
// and :60 ran the TUI as
//
//	codex --remote unix://$SCRATCH/mitm.sock
//
// against a real 0.147.0 binary. Those two lines are the whole plan, and they are why
// `Program` is "codex" for BOTH halves: the agent and the backend are the same binary in
// two modes, which is also why the core's LookPath check (obligation 9a) resolves one name.

import (
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
)

const r7Sock = "/var/folders/xx/swarm/sessions/01JSESSION/codex.sock"

// r7Plan is the plan under test, or a fatal if the adapter proves no seam at all.
func r7Plan(t *testing.T, sock string) adapter.BackendPlan {
	t.Helper()
	src, ok := adapter.AsBackendSource(New())
	if !ok {
		t.Fatal("the codex adapter proves no BackendSource; M4.1 makes app-server a shim-owned " +
			"child DESCRIBED by the adapter, and without the seam the daemon would have to name " +
			"`codex` itself -- the exact property the Epic 9 freeze exists to protect")
	}
	plan, ok := src.Backend(adapter.BackendSpec{SocketPath: sock})
	if !ok {
		t.Fatal("codex.Backend answered ok==false; Codex is the ONE adapter in the tree that needs a backend")
	}
	return plan
}

// TestR7CodexBackend_DescribesTheRecordedAppServerArgv freezes the R1 gate's own command
// lines as the plan.
func TestR7CodexBackend_DescribesTheRecordedAppServerArgv(t *testing.T) {
	plan := r7Plan(t, r7Sock)

	if plan.Program != binary {
		t.Errorf("plan.Program = %q, want %q; the backend is the same binary as the agent in a "+
			"different mode (r1-codex-gate.md:53)", plan.Program, binary)
	}
	wantArgs := []string{"app-server", "--listen", "unix://" + r7Sock}
	if strings.Join(plan.Args, " ") != strings.Join(wantArgs, " ") {
		t.Errorf("plan.Args = %v, want %v; recorded at r1-codex-gate.md:53. Note that "+
			"`codex app-server proxy --sock` is NOT the bridge to this endpoint (gate correction 2) "+
			"and must never appear here", plan.Args, wantArgs)
	}
	wantAgent := []string{"--remote", "unix://" + r7Sock}
	if strings.Join(plan.AgentArgs, " ") != strings.Join(wantAgent, " ") {
		t.Errorf("plan.AgentArgs = %v, want %v; recorded at r1-codex-gate.md:60. `--remote` accepts "+
			"`unix://PATH` per `codex --help` at 0.147.0", plan.AgentArgs, wantAgent)
	}
}

// TestR7CodexBackend_NamesTheCoreSuppliedSocketAndNoPathOfItsOwn is the adapter side of
// obligation 9c. The CORE enforces containment (internal/daemon); the adapter's job is
// simply never to invent a path, and this fences that it does not.
func TestR7CodexBackend_NamesTheCoreSuppliedSocketAndNoPathOfItsOwn(t *testing.T) {
	for _, sock := range []string{r7Sock, "/tmp/other/codex.sock", "relative.sock"} {
		plan := r7Plan(t, sock)
		for _, arg := range append(append([]string(nil), plan.Args...), plan.AgentArgs...) {
			bare := strings.TrimPrefix(arg, "unix://")
			if !strings.HasPrefix(bare, "/") && !strings.HasPrefix(bare, ".") {
				continue // a flag or a subcommand, not a path
			}
			if bare != sock {
				t.Errorf("plan for socket %q names path %q, which the core did not supply; "+
					"BackendSpec.SocketPath is core-owned because the core owns the session dir", sock, bare)
			}
		}
	}
}

// TestR7CodexBackend_PlanIsPureTotalAndDeterministic is conformance obligation 7 applied to
// the real adapter, including the pathological socket paths a session dir can never hold but
// a malformed launch config can.
func TestR7CodexBackend_PlanIsPureTotalAndDeterministic(t *testing.T) {
	src, ok := adapter.AsBackendSource(New())
	if !ok {
		t.Skip("no seam yet; TestR7CodexBackend_DescribesTheRecordedAppServerArgv is the RED that matters")
	}
	for _, sock := range []string{"", "unix://", strings.Repeat("/a", 8192), "\x00", "  "} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("Backend panicked on %q: %v", sock, r)
				}
			}()
			a, aok := src.Backend(adapter.BackendSpec{SocketPath: sock})
			b, bok := src.Backend(adapter.BackendSpec{SocketPath: sock})
			if aok != bok || a.Program != b.Program || strings.Join(a.Args, " ") != strings.Join(b.Args, " ") {
				t.Errorf("Backend is not deterministic on %q", sock)
			}
		}()
	}
}

// TestR7CodexBackend_TheAdapterStillOwnsNoFd re-runs the E9.2 source grep over this package
// now that it names a second process. A `net.Dial` or an `exec.Command` appearing here is
// the exact drift ADR-001/E9.2 forbids: the adapter DESCRIBES, the core executes.
func TestR7CodexBackend_TheAdapterStillOwnsNoFd(t *testing.T) {
	scanBannedIO(t, ".")
}

// TestR7CodexBackend_NoKeystrokeSeamEverOnCodex is playbook §8.2's prohibition made
// STRUCTURAL rather than accidental: "No Codex semantic operation may be implemented by
// terminal keystroke injection."
//
// Today Codex is saved from being typed at only by the ACCIDENT that it implements
// ApprovalApplier and TurnInterrupter nowhere -- and it is NOT saved on the composer, which
// has no seam to be absent from (internal/skeleton/chat.go:236 writes into the PTY for every
// provider). After R7 all three are refusals by construction, and this test is what makes a
// later agent adding one of them fail loudly instead of quietly typing into a Codex TUI.
func TestR7CodexBackend_NoKeystrokeSeamEverOnCodex(t *testing.T) {
	a := New()
	if _, ok := adapter.AsApprovalApplier(a); ok {
		t.Error("the codex adapter proves an ApprovalApplier; approvals on Codex are answered by " +
			"JSON-RPC (M4.3, r1-codex-gate.md:125-134 -- NO KEY WAS EVER PRESSED IN THE TUI)")
	}
	if _, ok := adapter.AsTurnInterrupter(a); ok {
		t.Error("the codex adapter proves a TurnInterrupter; interrupt on Codex is turn/interrupt " +
			"(recorded: turn-interrupt.json, turn-completed-interrupted.json)")
	}
	if _, ok := adapter.AsKeystrokeComposer(a); ok {
		t.Error("the codex adapter proves a KeystrokeComposer; a phone send on Codex is turn/start " +
			"or turn/steer and NEVER a keystroke (playbook §8.2)")
	}
}
