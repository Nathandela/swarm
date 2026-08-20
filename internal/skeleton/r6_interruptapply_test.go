package skeleton

// FAILING-FIRST (TDD RED, GG-5) for Wave R6's daemon-side semantic interrupt -- Mirror
// M2.4 ("Stop becomes a signed interrupt op"), the complete-chat Interrupt row, ADR-017
// T2 rule 3 (the phone renders from the capability record and infers nothing).
// Bead: agents-tracker-hggx.7.
//
// THE CONTRACT these tests freeze:
//
//   - adapter.AsTurnInterrupter(a) (adapter.TurnInterrupter, bool): the OPTIONAL,
//     ADDITIVE lifecycle seam ADR-010's extension trick already plays for capture --
//     pure data out (InterruptKeys() []byte, the CLI's own cancel sequence), the CORE
//     does the writing. An adapter that implements nothing here is complete and fully
//     supported; absence is the signal (ADR-010 §5). This discharges the exact deferral
//     capability.go records ("interrupt is left at its zero value ... nothing in
//     internal/adapter exposes a LifecycleSink-style interrupt seam to consult") for the
//     one adapter R6 ships complete chat on.
//   - func (a *coreAPI) InterruptTurn(machine, operationID string, req
//     protocol.TurnInterruptReq) (protocol.ErrorCode, error): resolves the session's
//     adapter, and either injects the seam's declared sequence through the daemon's own
//     input path (claude), or refuses protocol.CodeInterruptUnsupported having typed
//     NOTHING (an adapter with no seam) -- never a guessed keystroke, never a silent OK.
//
//     SIGNATURE SUPERSEDED (Wave R6 review fix-pack, finding B7; recorded in
//     docs/verification/r6-red/chat-red.txt). The seam took a bare `session string`, and
//     a probe showed a Stop rendered against turn A typing the cancel sequence into turn
//     B -- in playbook §8.1, the turn the OWNER just started at the terminal, whose
//     half-typed line the cancel key clears. It now takes the whole
//     TurnInterruptReq(session, expected_turn) and refuses protocol.CodeStaleTurn,
//     having typed nothing, when the named turn is no longer current. Every assertion
//     below is otherwise unchanged; each call simply names the turn it is stopping.
//   - deriveSessionCapabilities derives Interrupt from the SAME seam: true for claude
//     (which now proves one), false for an adapter that proves none. The capability
//     record is what gates the phone's Stop affordance; deriving it from anything but
//     the seam would assert a capability nothing proves (ADR-017 T2 rule 3).

import (
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/claude"
	"github.com/Nathandela/swarm/internal/adapter/opencode"
	"github.com/Nathandela/swarm/internal/protocol"
)

// TestR6Interrupt_TheClaudeAdapterProvesANonEmptyInterruptSequence pins the seam on the
// shipped adapter: claude declares its own cancel sequence as pure data, and it is not
// empty -- an empty sequence would be a capability that does nothing when exercised.
func TestR6Interrupt_TheClaudeAdapterProvesANonEmptyInterruptSequence(t *testing.T) {
	ti, ok := adapter.AsTurnInterrupter(claude.New())
	if !ok {
		t.Fatal("claude proves no TurnInterrupter seam; R6's complete chat needs a semantic " +
			"interrupt on its reference provider (complete-chat table, Interrupt row)")
	}
	if keys := ti.InterruptKeys(); len(keys) == 0 {
		t.Fatal("claude's InterruptKeys() is empty; a declared interrupt that types nothing is " +
			"a Stop button that does nothing")
	}
}

// TestR6Interrupt_AnAdapterWithoutTheSeamIsNotATurnInterrupter: absence stays a signal.
func TestR6Interrupt_AnAdapterWithoutTheSeamIsNotATurnInterrupter(t *testing.T) {
	if _, ok := adapter.AsTurnInterrupter(opencode.New()); ok {
		t.Fatal("opencode claims a TurnInterrupter seam no capture ever proved; ADR-010 makes " +
			"the extension optional exactly so absence stays honest")
	}
}

// TestR6Interrupt_AnInterruptOnAClaudeSessionTypesTheDeclaredSequence: the applied half.
// The daemon writes exactly the seam's declared bytes into the session's PTY; the fake
// CLI reports them on its stdin.
func TestR6Interrupt_AnInterruptOnAClaudeSessionTypesTheDeclaredSequence(t *testing.T) {
	r := newInjectRig(t, composerGrid, claudeApproval("req-interrupt-applies"))
	ti, ok := adapter.AsTurnInterrupter(claude.New())
	if !ok {
		t.Fatal("claude proves no TurnInterrupter seam")
	}

	turn := r6FixOpenTurn(t, r.sk, r.local, "start the work")
	code, err := r.sk.api.InterruptTurn(r.sk.api.endpointID, "devA:01JSTOP",
		protocol.TurnInterruptReq{Session: r.session, ExpectedTurn: turn})
	if err != nil || code != "" {
		t.Fatalf("interrupt on a live claude session refused: code %q err %v", code, err)
	}
	if got := r.readBack(t); !strings.Contains(got, string(ti.InterruptKeys())) {
		t.Errorf("the session's stdin held %q after the interrupt, want it to contain the "+
			"adapter-declared sequence %q: the daemon executes the adapter's data, it does not "+
			"improvise its own", got, ti.InterruptKeys())
	}
}

// TestR6Interrupt_ANoSeamSessionRefusesInterruptUnsupportedAndTypesNothing is ADR-017's
// honest degrade at the op level: the refusal is CODED (the phone renders "this provider
// has no safe remote interrupt"), and the PTY is untouched -- a guessed keystroke into a
// CLI whose cancel key nobody recorded is exactly what IS-TOOL-2's never-guess posture
// forbids one layer down.
func TestR6Interrupt_ANoSeamSessionRefusesInterruptUnsupportedAndTypesNothing(t *testing.T) {
	r := newInjectRig(t, composerGrid, claudeApproval("req-interrupt-unsup"))
	// The same live session, resolved through an adapter that proves no seam: the rig's
	// resolver is overridable exactly for this kind of counterfactual.
	r.sk.adapterFor = func(string) (adapter.Adapter, bool) { return opencode.New(), true }

	turn := r6FixOpenTurn(t, r.sk, r.local, "start the work")
	code, err := r.sk.api.InterruptTurn(r.sk.api.endpointID, "devA:01JUNSUP",
		protocol.TurnInterruptReq{Session: r.session, ExpectedTurn: turn})
	if code != protocol.CodeInterruptUnsupported {
		t.Fatalf("interrupt via a seamless adapter = code %q err %v, want interrupt_unsupported", code, err)
	}
	r.assertNothingWasTyped(t)
}

// TestR6Interrupt_AnUnknownSessionRefusesWithACode: never a silent no-op.
func TestR6Interrupt_AnUnknownSessionRefusesWithACode(t *testing.T) {
	sk := assemble(t)
	code, err := sk.api.InterruptTurn(sk.api.endpointID, "devA:01JGHOST", protocol.TurnInterruptReq{
		Session: protocol.NamespacedID(sk.api.endpointID, "no-such-session"), ExpectedTurn: "01JTURN"})
	if err == nil || code == "" {
		t.Fatalf("interrupt on an unknown session answered code %q err %v; want a coded refusal", code, err)
	}
}

// TestR6Interrupt_TheCapabilityRecordDerivesInterruptFromTheSeam closes the loop with
// ADR-017 T2: the record the phone renders its Stop affordance from is derived from the
// seam that makes the affordance real -- true where a seam is proven, false where none
// is, never guessed from structured_chat.
func TestR6Interrupt_TheCapabilityRecordDerivesInterruptFromTheSeam(t *testing.T) {
	if got := deriveSessionCapabilities("claude", claude.New(), "9.9.9", "rev-test", false); !got.Interrupt {
		t.Error("claude's capability record says interrupt=false while the adapter proves a " +
			"TurnInterrupter seam; the phone would hide a Stop that works")
	}
	if got := deriveSessionCapabilities("opencode", opencode.New(), "9.9.9", "rev-test", false); got.Interrupt {
		t.Error("opencode's capability record says interrupt=true with no seam behind it; the " +
			"phone would show a Stop that cannot work (ADR-017 T2 rule 3)")
	}
}
