package shimwire

// FAILING-FIRST (TDD RED, GG-5) for the ONE new control verb Mirror M4.1 needs:
// `backend_attach`, the daemon's GO-AHEAD in ADR-013 §R7.2e's spawn-ordering handshake.
// Bead: agents-tracker-hggx.8.
//
// WHY A NEW VERB AT ALL. The shim starts the backend and then immediately spawns the agent,
// and the daemon is not in that loop: launchConfirmTimeout waits for the shim's CONTROL
// socket, which is bound BEFORE either (shim.go:116). There is no edge the daemon can act on,
// so "the daemon is a client before the agent exists" -- the property that makes it impossible
// to miss a `thread/started` and impossible to hit the gate's 15-17 s rollout race at cold
// start -- is not implementable without one. The verb rides the per-session control socket
// that already exists; no new listener, no new socket, no new auth surface.
//
// THE SHAPE: Control{Type: TypeBackendAttach, AgentArgs: [...]} daemon -> shim. The shim
// appends AgentArgs to the agent argv VERBATIM and then calls pty.StartWithSize. An EMPTY
// AgentArgs is the ordinary case, not a defect: it means "go ahead, I am connected, and I am
// not handing you a thread id" (the flag question of ADR-013 §R7.9, open either way).

import (
	"strings"
	"testing"
)

// TestR7BackendAttach_TheVerbExistsAndRoundTripsItsAgentArgs freezes the wire shape.
func TestR7BackendAttach_TheVerbExistsAndRoundTripsItsAgentArgs(t *testing.T) {
	in := Control{
		Type:      TypeBackendAttach,
		AgentArgs: []string{"resume", "01a00339-a80e-72a0-966f-116427b6b9ce"},
	}
	data, err := Encode(in)
	if err != nil {
		t.Fatalf("encode backend_attach: %v", err)
	}
	if !strings.Contains(string(data), `"backend_attach"`) {
		t.Errorf("the encoded verb is %s; the wire spelling must be backend_attach", data)
	}
	out, err := Decode(data)
	if err != nil {
		t.Fatalf("decode backend_attach: %v", err)
	}
	if out.Type != TypeBackendAttach {
		t.Fatalf("decoded type %q, want %q", out.Type, TypeBackendAttach)
	}
	if strings.Join(out.AgentArgs, " ") != strings.Join(in.AgentArgs, " ") {
		t.Errorf("AgentArgs round-tripped as %v, want %v; the shim appends them to the agent argv "+
			"VERBATIM, so a re-ordering or a dropped element launches a different agent", out.AgentArgs, in.AgentArgs)
	}
}

// TestR7BackendAttach_AnEmptyAgentArgsIsTheOrdinaryGoAhead pins the no-flag arm. Whether the
// TUI can be pointed at a daemon-created thread is NOT RECORDED (r7-open-questions.md Q2), and
// the handshake must be identical either way -- otherwise the answer to that question changes
// the topology instead of one field.
func TestR7BackendAttach_AnEmptyAgentArgsIsTheOrdinaryGoAhead(t *testing.T) {
	data, err := Encode(Control{Type: TypeBackendAttach})
	if err != nil {
		t.Fatalf("encode a bare go-ahead: %v", err)
	}
	out, err := Decode(data)
	if err != nil {
		t.Fatalf("decode a bare go-ahead: %v", err)
	}
	if out.Type != TypeBackendAttach {
		t.Fatalf("decoded type %q, want %q", out.Type, TypeBackendAttach)
	}
	if len(out.AgentArgs) != 0 {
		t.Errorf("a bare go-ahead decoded AgentArgs %v; empty must stay empty, or every session "+
			"gains an argv element nobody asked for", out.AgentArgs)
	}
}

// TestR7BackendAttach_AnOldShimToleratesTheVerbRatherThanErroring is this package's own
// stated rule ("unknown fields and unknown Type strings are tolerated, not errors"). A daemon
// upgraded ahead of a still-running pre-R7 shim must not wedge it.
func TestR7BackendAttach_AnOldShimToleratesTheVerbRatherThanErroring(t *testing.T) {
	out, err := Decode([]byte(`{"type":"backend_attach","agent_args":["--remote","unix:///x"],"unknown":1}`))
	if err != nil {
		t.Fatalf("decoding backend_attach errored: %v; unknown types and fields are TOLERATED here, "+
			"and a hard error would make a daemon upgrade under a live shim fatal to that session", err)
	}
	if out.Type != "backend_attach" {
		t.Errorf("decoded type %q", out.Type)
	}
}
