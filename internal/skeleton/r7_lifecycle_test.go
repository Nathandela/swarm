package skeleton

// FAILING-FIRST (TDD RED, GG-5) for the lifecycle trio and the capability honesty §R7.7 owes:
// daemon restart, shim crash, CLI upgrade -- and what a session with a dead or
// never-connected backend TELLS THE PHONE. Bead: agents-tracker-hggx.8. ADR-013 §R7.6/§R7.7,
// ADR-017 T2.
//
// THE THREE CASES, DISTINGUISHED so the ONE-WAY degrade is never fired on a recoverable one.
// markSessionDegraded (capability.go:250) is one-way AND durable, so a rule that degraded on
// every daemon restart would PERMANENTLY remove the composer from every live Codex session on
// the first `swarm daemon restart` -- the operation ADR-001 exists to make ordinary.
//
// THESE THREE TESTS CALL THE HELPERS DIRECTLY, AND THAT IS WHY THEY ARE NOT ENOUGH (review
// round 4, MEDIUM 4). They pin what noteBackendUnavailable / noteBackendRejoined /
// noteBackendLost DO; they drive nothing that CALLS them, so for three rounds every failure arm
// of joinSessionBackend and both arms of watchSessionBackend were reachable only from
// production. Two blocking defects lived in exactly that blind spot. The behavioural fences at
// the real call sites, against a real WebSocket app-server, are in r7r4_join_test.go; these
// stay as the unit-level statement of each case's rule.
//
//  1. NEVER CONNECTED. The backend was declared and its socket never became servable, or the
//     daemon could not dial it at launch-confirm. This session will never have a structured
//     plane: emit structured_gap with reason `backend_unavailable` AT LAUNCH, and degrade
//     durably. The composer is correctly off BEFORE the first tap.
//  2. TRANSIENT (daemon restart, rejoin succeeds). No gap, no degrade.
//  3. DIED MID-SESSION. The session ends (§R7.6) and a structured_gap covers the tail.
//
// AND THE DERIVATION DEFECT, which is REAL and is R7's to land: deriveSessionCapabilities
// (capability.go:333-334) derives structured_chat from adapter.AsInteractionSource(a) -- a
// fact about the ADAPTER TYPE. The moment the Codex adapter implements InteractionSource, a
// PRE-UPGRADE Codex session with no backend at all would claim structured_chat=true. It must
// become SEAM AND LIVE BACKEND, PER SESSION INSTANCE. The same correction applies to
// Interrupt, derived from AsTurnInterrupter, which would read FALSE for a Codex session whose
// RPC interrupt works perfectly.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/journal"
	"github.com/Nathandela/swarm/internal/protocol"
)

// r7Gaps returns the structured_gap records journalled for a session, decoded.
func r7Gaps(t *testing.T, sk *Daemon, session string) []map[string]any {
	t.Helper()
	res, err := sk.Core().JournalReadFrom(0)
	if err != nil {
		t.Fatalf("JournalReadFrom: %v", err)
	}
	var out []map[string]any
	for _, rec := range res.Events {
		if rec.Type != journal.TypeStructuredGap || rec.SessionID != session {
			continue
		}
		var body map[string]any
		if json.Unmarshal(rec.Payload, &body) == nil {
			out = append(out, body)
		}
	}
	return out
}

func r7AwaitGap(t *testing.T, sk *Daemon, session string) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if g := r7Gaps(t, sk, session); len(g) > 0 {
			return g
		}
		time.Sleep(20 * time.Millisecond)
	}
	return nil
}

// ---------------------------------------------------------------------------
// §R7.7 -- the three cases
// ---------------------------------------------------------------------------

// TestR7Lifecycle_ABackendThatNEVERConnectedEmitsAGapAtLaunchAndDegradesDurably is case 1, and
// the reason it must fire AT LAUNCH rather than at the first tap is a phone fact: the composer's
// availability reads `structured_gap` off the TRANSCRIPT (SessionDetailPanel.kt:763-772,
// `structuredChat = !transcript.structureTorn`), not a capability record. So without this, a
// Codex session whose backend never came up still SHOWS a composer, the owner types, and the
// refusal arrives AFTER the tap. ADR-017 T2 wants that surfaced before it.
func TestR7Lifecycle_ABackendThatNEVERConnectedEmitsAGapAtLaunchAndDegradesDurably(t *testing.T) {
	sk := assemble(t)
	ad := &r7CodexAdapter{Adapter: newPlainAdapter().Adapter}
	sk.setAdapterForTest(func(string) (adapter.Adapter, bool) { return ad, true })
	m := launchFake(t, sk, "idle 60s\n")

	// The launch declared a backend and the daemon could never dial it.
	sk.noteBackendUnavailable(m.ID)

	gaps := r7AwaitGap(t, sk, m.ID)
	if len(gaps) == 0 {
		t.Fatal("a Codex session whose backend never connected journalled NO structured_gap; the " +
			"phone reads gaps off the transcript to decide whether to show a composer, so this " +
			"session shows one, the owner types, and the refusal arrives after the tap")
	}
	if reason, _ := gaps[0]["reason"].(string); reason != "backend_unavailable" {
		t.Errorf("the gap's reason is %q, want \"backend_unavailable\"; a reason nobody can "+
			"distinguish is a gap nobody can explain", reason)
	}
	if !sk.sessionDegraded(m.ID) {
		t.Error("the session was not marked degraded; this one WILL NEVER have a structured plane, " +
			"which is exactly the case the durable one-way marker is for")
	}
}

// TestR7Lifecycle_ADaemonRestartWithASuccESSFULRejoinNeitherGapsNorDegrades is case 2, and it
// is the one that would be catastrophic to get wrong: markSessionDegraded is ONE-WAY and
// DURABLE, so degrading here removes the composer from every live Codex session permanently,
// on an operation the owner is meant to run without thinking about it.
func TestR7Lifecycle_ADaemonRestartWithASuccessfulRejoinNeitherGapsNorDegrades(t *testing.T) {
	sk := assemble(t)
	ad := &r7CodexAdapter{Adapter: newPlainAdapter().Adapter}
	sk.setAdapterForTest(func(string) (adapter.Adapter, bool) { return ad, true })
	m := launchFake(t, sk, "idle 60s\n")

	backend := newR7FakeBackend()
	sk.registerBackend(m.ID, "01a00339-a80e-72a0-966f-116427b6b9ce", backend)
	// The daemon went away and came back; the shim and its app-server survived (ADR-001), the
	// dial succeeded, initialize/initialized ran, and the thread was rejoined.
	sk.forgetBackend(m.ID)
	sk.noteBackendRejoined(m.ID, "01a00339-a80e-72a0-966f-116427b6b9ce", backend)

	if gaps := r7Gaps(t, sk, m.ID); len(gaps) != 0 {
		t.Errorf("a SUCCESSFUL rejoin journalled %d structured_gap(s): %v. A successful rejoin is "+
			"NOT a proven gap, and ADR-017's whole posture is that a gap is proven, never assumed",
			len(gaps), gaps)
	}
	if sk.sessionDegraded(m.ID) {
		t.Fatal("a daemon restart PERMANENTLY degraded a live Codex session. markSessionDegraded is " +
			"one-way and durable, so the composer is now gone from this session forever -- and from " +
			"every other one, on the first `swarm daemon restart`")
	}
}

// TestR7Lifecycle_ABackendThatDiedMidSessionGapsTheTail is case 3. History must be honest
// about what was not captured; the session itself ends (that half is fenced in
// internal/shim/r7_backend_test.go, where the shim owns the escalation).
func TestR7Lifecycle_ABackendThatDiedMidSessionGapsTheTail(t *testing.T) {
	sk := assemble(t)
	ad := &r7CodexAdapter{Adapter: newPlainAdapter().Adapter}
	sk.setAdapterForTest(func(string) (adapter.Adapter, bool) { return ad, true })
	m := launchFake(t, sk, "idle 60s\n")

	backend := newR7FakeBackend()
	sk.registerBackend(m.ID, "01a00339-a80e-72a0-966f-116427b6b9ce", backend)
	sk.noteBackendLost(m.ID, "connection closed")

	gaps := r7AwaitGap(t, sk, m.ID)
	if len(gaps) == 0 {
		t.Fatal("a backend that died mid-session journalled no structured_gap; the transcript would " +
			"end mid-turn with nothing marking the discontinuity, which is the one move ADR-017 forbids")
	}
}

// ---------------------------------------------------------------------------
// The capability derivation defect
// ---------------------------------------------------------------------------

// TestR7Capabilities_StructuredChatIsSeamANDLiveBackendPerSessionInstance is the defect stated
// as a test. Today the derivation is a fact about the adapter TYPE, so it flips to true for
// EVERY Codex session -- including one launched by the pre-R7 binary with argv `codex`, no
// --remote, no backend child and no backend_socket_path -- on the day the adapter gains
// Interactions.
func TestR7Capabilities_StructuredChatIsSeamANDLiveBackendPerSessionInstance(t *testing.T) {
	ad := &r7CodexAdapter{Adapter: newPlainAdapter().Adapter}

	withBackend := deriveSessionCapabilities("codex", ad, "0.147.0", "r7", true)
	if !withBackend.StructuredChat {
		t.Error("a Codex session with a LIVE backend does not claim structured_chat")
	}
	if withBackend.TerminalFallback {
		t.Error("a Codex session with a live backend claims terminal_fallback")
	}

	noBackend := deriveSessionCapabilities("codex", ad, "0.147.0", "r7", false)
	if noBackend.StructuredChat {
		t.Error("a Codex session with NO live backend claims structured_chat. This is the pre-R7 " +
			"session -- argv `codex`, no --remote, no backend child -- and it has exactly the " +
			"heuristic status it has always had. Claiming a structured plane it does not have is " +
			"what makes the phone show a composer whose every send is refused")
	}
	if !noBackend.TerminalFallback {
		t.Error("a Codex session with no backend does not declare terminal_fallback")
	}
}

// TestR7Capabilities_InterruptIsAlsoPerSessionInstanceAndNotPerAdapterType is the same
// correction on the other field, in the OPPOSITE direction: derived from AsTurnInterrupter
// alone, a Codex session whose RPC interrupt works perfectly reads `interrupt: false` and the
// phone hides a Stop button that would have worked.
func TestR7Capabilities_InterruptIsAlsoPerSessionInstanceAndNotPerAdapterType(t *testing.T) {
	ad := &r7CodexAdapter{Adapter: newPlainAdapter().Adapter}

	if !deriveSessionCapabilities("codex", ad, "0.147.0", "r7", true).Interrupt {
		t.Error("a Codex session with a live backend reads interrupt=false; turn/interrupt is " +
			"RECORDED working (turn-interrupt.json, turn-completed-interrupted.json) and the phone " +
			"would hide a Stop button that works")
	}
	if deriveSessionCapabilities("codex", ad, "0.147.0", "r7", false).Interrupt {
		t.Error("a Codex session with NO backend reads interrupt=true; the adapter proves no " +
			"keystroke seam, so the op would refuse interrupt_unsupported after the tap")
	}
}

// TestR7Capabilities_ClaudeIsUnaffectedByTheDerivationChange is the regression fence on the
// provider R7 is not touching. Claude has no backend and must keep claiming exactly what it
// claims at HEAD.
func TestR7Capabilities_ClaudeIsUnaffectedByTheDerivationChange(t *testing.T) {
	ad := &r7KeystrokeCaptureAdapter{captureAdapter: newCaptureAdapter()}
	got := deriveSessionCapabilities("claude", ad, "2.1.214", "r7", false)
	if !got.StructuredChat {
		t.Error("Claude lost structured_chat to the backend condition. Claude's structured plane is " +
			"its HOOK channel and has nothing to do with a backend; ANDing a backend fact into " +
			"every provider's derivation turns R7 into a Claude regression")
	}
}

// r7KeystrokeCaptureAdapter is Claude's shape: an InteractionSource AND a keystroke composer,
// with no backend.
type r7KeystrokeCaptureAdapter struct{ *captureAdapter }

func (r7KeystrokeCaptureAdapter) ComposerKeys(text string) []byte { return []byte(text) }

// ---------------------------------------------------------------------------
// The composer gate, end to end on the honesty path
// ---------------------------------------------------------------------------

// TestR7Lifecycle_TheComposerIsREFUSEDOnADegradedBackendSessionAndTypesNothing joins the two
// halves: §R7.5's sink resolution is what makes it SAFE, and §R7.7's gap is what makes it
// HONEST. Both must hold, and the safety must not depend on the honesty -- R7 may ship the
// composer branch without the capability-publication slice, because requireStructuredComposer
// cannot help at all (registerSessionCapabilities has no production caller).
func TestR7Lifecycle_TheComposerIsREFUSEDOnADegradedBackendSessionAndTypesNothing(t *testing.T) {
	r := newR7ComposerRig(t, true)
	r.sk.noteBackendLost(r.local, "connection closed")

	code, err := r.send(t, "", "are you there", "devA:01JDEAD00000000000000000")
	if code == "" && err == nil {
		t.Fatal("a composer send to a session whose backend is DEAD reported success; the message " +
			"went nowhere and the transcript can never show it -- the gap silently bridged")
	}
	if code != protocol.CodeStructuredUnsupported {
		t.Errorf("send on a dead-backend session = %q, want structured_unsupported", code)
	}
	if len(r.backend.calls) != 0 {
		t.Errorf("RPCs %v were made on a connection the daemon had lost", methodsOf(r.backend))
	}
	r.assertPTYUntouched(t)
}
