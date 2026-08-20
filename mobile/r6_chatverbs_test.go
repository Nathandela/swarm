package swarmmobile

// FAILING-FIRST (TDD RED, GG-5) for Wave R6's FACADE half of the chat verbs -- Mirror
// M2.4: the structured composer send as a signed op, the Stop that becomes a signed
// turn_interrupt op, the stale_turn refusal surfaced as its own gentle nameable class,
// and the two additive TranscriptItem fields the Kotlin transcript renders from.
// Bead: agents-tracker-hggx.7. RED is undefined-only: (*App).ComposerSend,
// ErrClassStaleTurn, TranscriptItem.ToolKind and TranscriptItem.Source do not exist.
//
// SUPERSESSION, PRE-RECORDED: (*App).Interrupt currently rides the LEASE INPUT PLANE
// (mobile/commands.go: "an interrupt IS a keystroke", Op.Action "interrupt", untracked,
// undeliverable ones recorded on the undelivered-input ledger). That decision's own
// stated premise -- "MINTING A NEW SIGNED ACTION WAS REJECTED [because] a command bearing
// the new action would be refused ... one hop short of the daemon" -- was dissolved by
// Wave R1, which mapped ActionTurnInterrupt at every hop, and M2.4 now commands the
// replacement ("Stop becomes a signed interrupt op"). The GREEN slice that lands it must
// quote and retire the superseded assertions wherever they are pinned (the commands.go
// doc comment, and any test asserting the input-plane ride), per the M1.2 authorized-
// rewrite precedent. Nothing in THIS file weakens them silently: the new pins live in
// new tests beside the old until that authorized rewrite.

import (
	"os"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// chatApp mirrors approveApp: a machine destination and an epoch content key, everything
// resolved EXCEPT the connection -- the exact state B43's live-only rule is about.
func chatApp(t *testing.T) *App {
	t.Helper()
	a := transcriptApp(t)
	a.machineTarget = "machine-routing-id"
	key := make([]byte, len(crypto.ContentKey{}))
	for i := range key {
		key[i] = byte(i + 1)
	}
	if err := a.InstallContentKey(key); err != nil {
		t.Fatalf("InstallContentKey: %v", err)
	}
	return a
}

// TestR6ChatVerbs_ComposerSendIsLiveOnlyAndNeverQueued: with no relay connection the
// send is refused with the offline class and NOTHING is stored for later -- a message
// replayed out of a queue lands in a turn nobody rendered it against, which is the very
// race expected_turn exists to kill (ADR-009 (6): "A send that cannot get [through] is
// shown refused, not silently swallowed").
func TestR6ChatVerbs_ComposerSendIsLiveOnlyAndNeverQueued(t *testing.T) {
	a := chatApp(t)

	op, err := a.ComposerSend("m1/s1", "01JTURN", "ship it")
	if err == nil {
		t.Fatalf("ComposerSend succeeded with no connection (op %+v); it is live-only", op)
	}
	if !strings.Contains(err.Error(), ErrClassOffline) {
		t.Errorf("ComposerSend offline error = %q; want the %s class so the composer shows its "+
			"refused state gently rather than a bug report", err, ErrClassOffline)
	}
	n, cerr := a.PendingOpCount()
	if cerr != nil {
		t.Fatalf("PendingOpCount: %v", cerr)
	}
	if n != 0 {
		t.Errorf("a refused send left %d ops in flight; want 0", n)
	}
}

// TestR6ChatVerbs_ComposerSendRefusesStructurallyEmptyInput: an empty session or an
// empty text is the screen's bug, refused locally with the invalid-request class --
// never sealed, never a relay round trip to find out.
func TestR6ChatVerbs_ComposerSendRefusesStructurallyEmptyInput(t *testing.T) {
	a := chatApp(t)
	if _, err := a.ComposerSend("", "01JTURN", "hello"); err == nil {
		t.Error("ComposerSend accepted an empty session")
	} else if !strings.Contains(err.Error(), ErrClassInvalidRequest) {
		t.Errorf("empty-session error = %q; want the %s class", err, ErrClassInvalidRequest)
	}
	if _, err := a.ComposerSend("m1/s1", "01JTURN", ""); err == nil {
		t.Error("ComposerSend accepted an empty text")
	} else if !strings.Contains(err.Error(), ErrClassInvalidRequest) {
		t.Errorf("empty-text error = %q; want the %s class", err, ErrClassInvalidRequest)
	}
}

// TestR6ChatVerbs_StaleTurnIsItsOwnGentleClass: the daemon's stale_turn refusal maps to
// its OWN facade class -- not unknown, not internal -- because the screen's remedy is
// specific and mild: the conversation moved on; re-read it and send again. The taxonomy
// row is pinned beside the constant so the three-directional taxonomy fences
// (s16_taxonomy_test.go and its Kotlin/conformance siblings) pick the row up.
func TestR6ChatVerbs_StaleTurnIsItsOwnGentleClass(t *testing.T) {
	if ErrClassStaleTurn == ErrClassUnknown || ErrClassStaleTurn == ErrClassInternal {
		t.Fatalf("ErrClassStaleTurn = %q; a stale turn is an ordinary race, not a bug report", ErrClassStaleTurn)
	}
	raw, err := os.ReadFile("error_taxonomy.tsv")
	if err != nil {
		t.Fatalf("read error_taxonomy.tsv: %v", err)
	}
	if !strings.Contains(string(raw), "STALE_TURN") {
		t.Error("error_taxonomy.tsv has no STALE_TURN row; every class the facade exports takes " +
			"a row naming the state the user is shown and what they can do about it (PB-APP-9)")
	}
}

// TestR6ChatVerbs_InterruptIsTheSignedTurnInterruptOp: the Stop verb names the signed op
// it rides. The old input-plane ride returned Op.Action "interrupt" and could not fail
// visibly once a lease existed; the signed op resolves -- visible success AND refusal
// per verb is this wave's UX bar.
// AMENDED BY THE WAVE R6 REVIEW FIX-PACK (finding B7): the verb takes the turn the screen
// DREW Stop against, exactly as ComposerSend does, because both are tapped under the same
// race. A probe showed a Stop rendered against turn A typing the cancel sequence into turn B
// -- in playbook §8.1, the turn the OWNER just started at the terminal. Every assertion below
// is unchanged; the call simply names its turn.
func TestR6ChatVerbs_InterruptIsTheSignedTurnInterruptOp(t *testing.T) {
	a := chatApp(t)

	op, err := a.Interrupt("m1/s1", "01JTURN")
	if err != nil {
		// Offline is an acceptable refusal here -- but it must be the offline CLASS,
		// which is a legible refusal, not a silent success.
		if !strings.Contains(err.Error(), ErrClassOffline) {
			t.Fatalf("offline Interrupt error = %q; want the %s class", err, ErrClassOffline)
		}
	} else if op == nil || op.Action != "turn_interrupt" {
		t.Fatalf("Interrupt op = %+v, want Action \"turn_interrupt\": Stop rides the signed op "+
			"now (M2.4), not the lease input plane", op)
	}

	// LIVE-ONLY, and no input-ledger residue: the signed op is not a keystroke, so an
	// undeliverable one must NOT appear on the undelivered-INPUT ledger the keystroke
	// plane owns. Its failure surfaces on the op itself.
	list, lerr := a.UndeliveredInputs()
	if lerr != nil {
		t.Fatalf("UndeliveredInputs: %v", lerr)
	}
	if c, cerr := list.Count(); cerr != nil || c != 0 {
		t.Errorf("an offline Interrupt left %d undelivered-input entries (err %v); the signed op "+
			"reports on itself, not on the keystroke ledger", c, cerr)
	}
}

// TestR6Fix_InterruptRefusesWithoutTheTurnItWasRenderedAgainst is finding B7's facade half:
// a Stop that names no turn is refused BEFORE anything is signed or sealed, so the unsafe
// frame cannot even be authored. Structural, like ComposerSend's own empty-text refusal.
func TestR6Fix_InterruptRefusesWithoutTheTurnItWasRenderedAgainst(t *testing.T) {
	a := chatApp(t)
	if _, err := a.Interrupt("m1/s1", ""); err == nil {
		t.Fatal("Interrupt with no expected_turn was accepted; there is deliberately no spelling " +
			"of \"interrupt whatever is running\" -- that spelling is what let a late Stop land " +
			"its cancel key at an idle prompt and clear the terminal user's half-typed line")
	}
	if _, err := a.Interrupt("", "01JTURN"); err == nil {
		t.Fatal("Interrupt with no session was accepted")
	}
}

// TestR6ChatVerbs_TranscriptItemCarriesToolKindAndSource freezes the two additive bound
// fields (mobile/types.go): the Kotlin bridge maps them onto InteractionItem, ToolCard
// picks glyphs from toolKind, and the transcript shows source attribution -- all without
// parsing Body (IS-TOOL-1's posture at this boundary).
func TestR6ChatVerbs_TranscriptItemCarriesToolKindAndSource(t *testing.T) {
	it := TranscriptItem{Kind: "tool_run", ToolKind: "search", Source: ""}
	if it.ToolKind != "search" {
		t.Errorf("ToolKind = %q, want search", it.ToolKind)
	}
	um := TranscriptItem{Kind: "user_message", Source: "phone"}
	if um.Source != "phone" {
		t.Errorf("Source = %q, want phone", um.Source)
	}
}
