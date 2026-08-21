package protocol

// WAVE R8 / SLICES S6 + S7 -- terminal_control_begin/end, terminal_input, keepalive.
// Failing-first (TDD RED, GG-5).
//
// WHAT IS THERE TODAY. `server.go:1182-1185` routes both control ops to `handleRefusalOp`,
// which answers `op_not_implemented` -- the Wave R1 placeholder. The capability GATE is
// already real and already correct: `internal/skeleton/deviceauth.go:19-27` maps
// `ActionTerminalControlBegin` / `ActionTerminalControlEnd` to `device.ActionControl`
// alongside launch, kill and take_control. Amendment T6-a RATIFIES that mapping rather than
// re-deriving it, so this slice replaces the refusal and touches no tier.
//
// WHAT THE OPS MUST BIND, and why each one is a separate assertion below rather than a
// paragraph: T6 says `terminal_control_begin` "is bound to the selected remote profile, the
// paired device's command signing key, and the fallback session INSTANCE", and mints "one
// non-transferable generation bound to that sender". T8-a adds that a generation whose
// instance no longer matches is refused. T6-e adds that every input frame re-evaluates kill
// switch, device registration, capability record and generation liveness. Each of those is a
// different way for the wave to ship a keyboard pointed at the wrong terminal.
//
// AND WHAT THEY MUST NOT BIND. `terminal_input` and `terminal_control_keepalive` ride the
// E2EE frame's authenticated sender/sequence plus the confirmed generation, and are NOT
// individually signed -- "this is the sole exception to full-body signatures in the remote
// protocol, and it is deliberately the SAME exception that already exists" (T6, citing
// ADR-007's 2026-07-24 Decision 1). OPEN-C4: the path never touches `LeaseManager`. A
// generation is not a lease; routing it through the lease plane would give the fallback the
// visible take-control ceremony ADR-013 R3 removed from the chat path and would make a
// fallback session compete for the shim's single interactive subscriber slot.
//
// THE SEAMS (undefined symbols -> compile-fail RED):
//
//	type TerminalControlBeginReq struct { Session, SessionInstance string; Profile int; ExpiresAt *time.Time }
//	type TerminalInputReq       struct { Session, SessionInstance, ControlGeneration string; Bytes []byte }
//	Control gains: TerminalControlBegin *TerminalControlBeginReq `json:"terminal_control_begin,omitempty"`
//	Control gains: TerminalInput        *TerminalInputReq        `json:"terminal_input,omitempty"`
//	Control gains: ControlGeneration    string                   `json:"control_generation,omitempty"`
//	const OpTerminalInput, OpTerminalControlKeepalive
//	func schema.TerminalControlBeginContentHash(*TerminalControlBeginReq) []byte
//	CodeStaleGeneration = "stale_generation"; CodeStaleInstance = "stale_instance"

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// TestR8Control_BeginIsNoLongerARefusalStub is the slice's own precondition: while
// `handleRefusalOp` owns the op, every assertion below about binding is unreachable.
func TestR8Control_BeginIsNoLongerARefusalStub(t *testing.T) {
	src := readProtocolSource(t, "server.go")
	if strings.Contains(src, "cc.handleRefusalOp(c, ActionTerminalControlBegin") {
		t.Errorf("ADR-017 T6: OpTerminalControlBegin is still dispatched to handleRefusalOp " +
			"(server.go:1182-1185), the Wave R1 placeholder that answers op_not_implemented. R8b " +
			"replaces the refusal with the real handler; until it does, the control half of the wave " +
			"does not exist and the fallback is read-only in the strongest possible sense.")
	}
	if strings.Contains(src, "cc.handleRefusalOp(c, ActionTerminalControlEnd") {
		t.Errorf("ADR-017 T6: OpTerminalControlEnd is still a refusal stub. `end` must land before " +
			"`begin` does, or the first generation a user opens can only be closed by a timeout.")
	}
}

// TestR8Control_BeginWireShapeBindsProfileAndInstance pins the wire names the phone signs
// over. T6 binds the op to the selected remote profile AND the session instance; a body that
// omits either is a signature that verifies over bytes which do not name what it authorised.
func TestR8Control_BeginWireShapeBindsProfileAndInstance(t *testing.T) {
	exp := time.Unix(1755600000, 0).UTC()
	c := Control{Op: OpTerminalControlBegin, TerminalControlBegin: &TerminalControlBeginReq{
		Session: "ep/sess1", SessionInstance: "inst-a", Profile: 1, ExpiresAt: &exp,
	}}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{
		`"terminal_control_begin"`, `"session":"ep/sess1"`, `"session_instance":"inst-a"`, `"profile":1`,
	} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("ADR-017 T6: serialized terminal_control_begin %s lacks %s", raw, key)
		}
	}
	if raw, err := json.Marshal(Control{Op: OpKill}); err != nil || strings.Contains(string(raw), "terminal_control_begin") {
		t.Errorf("a control-less Control leaks the new key: %s (err %v)", raw, err)
	}
}

// TestR8Control_InputAndKeepaliveAreTheSameTwoBodyTypesAndNoMore is the fence on the
// exception itself.
//
// T6 holds the unsigned exception "to exactly two body types and one live generation, which
// is what keeps it an exception". A third unsigned body is not a new feature, it is a wider
// exception -- and the alternatives section rejects per-frame signatures precisely on the
// grounds that the exception stays this size.
func TestR8Control_InputAndKeepaliveAreTheSameTwoBodyTypesAndNoMore(t *testing.T) {
	unsigned := map[string]bool{
		string(OpTerminalInput):            true,
		string(OpTerminalControlKeepalive): true,
	}
	if len(unsigned) != 2 {
		t.Fatalf("the unsigned exception covers exactly two body types; found %d", len(unsigned))
	}
	// The two ops must not collide with the signed ones: a signed op that happened to share a
	// string with an unsigned one would be authorised by the weaker of the two rules.
	for _, signed := range []string{
		string(OpTerminalControlBegin), string(OpTerminalControlEnd), string(OpComposerSend),
		string(OpApprove), string(OpSessionLaunch), string(OpTurnInterrupt),
	} {
		if unsigned[signed] {
			t.Errorf("op %q is both a signed op and an unsigned frame kind; the weaker rule wins at "+
				"dispatch and the stronger one becomes decoration", signed)
		}
	}
}

// TestR8Control_InputNeverTouchesTheLeasePlane is OPEN-C4, asserted as a source obligation
// because it is a statement about a path that must NOT exist.
//
// A generation is not a lease. Routing `terminal_input` through `LeaseManager` would (a) make
// a fallback session compete for the shim's single interactive subscriber slot, which is what
// ADR-013's co-presence finding says the owner already holds, and (b) resurrect the visible
// take-control ceremony R3 removed from the chat path, on the one surface where T6 wants a
// DIFFERENT ceremony with different lifetime rules.
func TestR8Control_InputNeverTouchesTheLeasePlane(t *testing.T) {
	src := readProtocolSource(t, "server.go")
	idx := strings.Index(src, "handleTerminalInput")
	if idx < 0 {
		t.Fatalf("ADR-017 T6: server.go has no handleTerminalInput. The unsigned input plane does not " +
			"exist yet, so the fallback cannot be typed into at all -- which is R8a's correct state and " +
			"R8b's starting point.")
	}
	end := idx + 4000
	if end > len(src) {
		end = len(src)
	}
	body := src[idx:end]
	for _, forbidden := range []string{"Leases", "LeaseManager", "handleTakeControl", "leaseGen"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("OPEN-C4 / ADR-017 T6: handleTerminalInput names %q. The control generation is not "+
				"the control lease: they have different lifetimes, different ceremonies and different "+
				"authority, and sharing the plane makes a fallback session compete for the shim's single "+
				"interactive subscriber slot the owner already holds.", forbidden)
		}
	}
}

// TestR8Control_StaleGenerationAndStaleInstanceAreDistinctSealedCodes.
//
// They are different facts with different remedies, and collapsing them is how a user is told
// to "try again" about a session that no longer exists. A stale GENERATION means the horizon
// passed or control was released: re-entering control is the remedy. A stale INSTANCE means
// the session was REPLACED: the screen the user was reading is gone, and re-entering control
// on the new incarnation is a different, deliberate act.
func TestR8Control_StaleGenerationAndStaleInstanceAreDistinctSealedCodes(t *testing.T) {
	if got := string(CodeStaleGeneration); got != "stale_generation" {
		t.Errorf("CodeStaleGeneration = %q, want the sealed wire value \"stale_generation\"", got)
	}
	if got := string(CodeStaleInstance); got != "stale_instance" {
		t.Errorf("CodeStaleInstance = %q, want the sealed wire value \"stale_instance\"", got)
	}
	for _, pair := range [][2]string{
		{string(CodeStaleGeneration), string(CodeStaleInstance)},
		{string(CodeStaleGeneration), string(CodeStaleTurn)},
		{string(CodeStaleInstance), string(CodeCapabilityRefused)},
	} {
		if pair[0] == pair[1] {
			t.Errorf("refusal codes %q and %q collapsed; they answer different questions with different "+
				"remedies, and a phone that cannot tell them apart tells the user to retry into a session "+
				"that no longer exists", pair[0], pair[1])
		}
	}
}

// readProtocolSource reads one file of this package as text, for the obligations that are
// about which code paths exist rather than about what one call returns.
func readProtocolSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}
