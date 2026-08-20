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
// deterministic: it reads only spec.SocketPath and composes two fixed argv shapes around it,
// so a pathological path yields a pathological plan rather than a panic -- and the CORE's
// containment check (adapter.ResolveBackend, obligation 9c) is what refuses it.
//
// Codex is the ONE adapter in the tree that needs a backend, which is why ok is
// unconditionally true here and false everywhere else.
func (codexAdapter) Backend(spec adapter.BackendSpec) (adapter.BackendPlan, bool) {
	endpoint := backendScheme + spec.SocketPath
	return adapter.BackendPlan{
		Program:   binary,
		Args:      []string{"app-server", "--listen", endpoint},
		AgentArgs: []string{"--remote", endpoint},
	}, true
}
