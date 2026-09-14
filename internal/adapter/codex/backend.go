package codex

// Mirror M4.1's adapter half: Codex DESCRIBES its app-server backend and nothing else
// (ADR-010's amendment of 2026-08-20, ADR-013 §R7.2).
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
// Program is the same binary for both halves: the agent and the backend are one executable
// in two modes.
//
// `codex app-server proxy --sock` is NOT the bridge to this endpoint (R1 gate correction 2)
// and must never appear here.

import "github.com/Nathandela/swarm/internal/adapter"

// backendScheme is the URL scheme `--listen` and `--remote` accept for a UDS at 0.147.0.
const backendScheme = "unix://"

// Backend describes the per-session `codex app-server`. It is pure, total and
// deterministic: it uses the socket and composed agent command without performing I/O.
// A pathological path yields a pathological plan rather than a panic -- and the CORE's
// containment check (adapter.ResolveBackend, obligation 9c) is what refuses it.
//
// Codex is the ONE adapter in the tree that needs a backend, which is why ok is
// unconditionally true here and false everywhere else.
func (codexAdapter) Backend(spec adapter.BackendSpec) (adapter.BackendPlan, bool) {
	endpoint := backendScheme + spec.SocketPath
	// The recorded argv plus the sandbox override: the app-server is the process that
	// executes the agent's commands, so its sandbox is the one the swarm CLI runs under.
	return adapter.BackendPlan{
		Program:          binary,
		Args:             []string{"app-server", "--listen", endpoint, "-c", sandboxNetworkOverride},
		AgentArgs:        []string{"--remote", endpoint},
		AgentCommandArgs: remoteResumeArgs(spec.AgentArgv),
	}, true
}

// Codex 0.154.0 treats --remote over our local socket as a remote workspace.
// Its TUI rejects permission overrides on resume and restores the server's saved
// permission profile instead (tui/src/app/startup.rs and app_server_session.rs).
// Only remove the two permission flags Swarm composes, only for that attachment.
// Fresh launches and a failed-backend standalone fallback keep their full policy.
func remoteResumeArgs(argv []string) []string {
	if len(argv) < 3 || argv[1] != "resume" {
		return nil
	}
	args := append([]string(nil), argv[1:3]...)
	for i := 3; i < len(argv); i++ {
		if i+1 < len(argv) {
			switch argv[i] {
			case "--sandbox":
				i++
				continue
			case "-c", "--model":
				if argv[i] != "-c" || argv[i+1] != sandboxNetworkOverride {
					args = append(args, argv[i], argv[i+1])
				}
				i++
				continue
			}
		}
		args = append(args, argv[i])
	}
	return args
}
