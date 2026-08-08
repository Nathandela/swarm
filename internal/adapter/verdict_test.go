package adapter

// FAILING-FIRST (TDD RED, GG-5) for the OWNER RULING of 2026-08-07: a DecisionChoice carries a
// VERDICT — allow | deny | other — that the adapter sets AT CAPTURE from its own CLI vocabulary.
//
// WHY THE ADAPTER AND NOT THE DAEMON. interaction-schema.md §3.5 makes the decision ids the CLI's
// OWN vocabulary and says so with examples: Codex offers accept | acceptWithExecpolicyAmendment |
// cancel, Claude Code a numbered dialog. Nothing downstream can classify those. The daemon needs
// the grant/refuse polarity to resolve §3.6's allowed/denied split, and the adapter is the only
// party that knows it — so it is captured beside the id, exactly as Mode captures the apply
// mechanism at the moment it is decidable (ADR-010 §4).
//
// The verdict is MACHINE-SIDE, like Keystrokes: no wire field is added, because the card labels
// its buttons from decisions[].label (IS-APR-3) and nothing on the phone switches on polarity.

import (
	"strings"
	"testing"
)

// TestInteractionValidate_RejectsAnUnknownDecisionVerdict — the SHAPE half. A verdict outside the
// three-value vocabulary is a violation on the same terms as an unknown `mode` or `action.type`:
// the daemon switches on it, so a fourth value would be silently misread as "not a denial".
func TestInteractionValidate_RejectsAnUnknownDecisionVerdict(t *testing.T) {
	in := Interaction{
		Kind: KindApprovalRequest, Status: StatusInProgress, Mode: ModeCard,
		Decisions: []DecisionChoice{{ID: "accept", Label: "Allow", Verdict: "maybe"}},
	}
	err := in.Validate()
	if err == nil {
		t.Fatalf("Validate accepted decisions[0].verdict = %q; it is a CLOSED vocabulary "+
			"(%s | %s | %s) and the daemon resolves §3.6's allowed/denied split off it",
			"maybe", VerdictAllow, VerdictDeny, VerdictOther)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "verdict") {
		t.Errorf("violation %q does not name the verdict", err)
	}
}

// TestInteractionValidate_AcceptsEachDefinedVerdict — the control. `other` is in the vocabulary
// for IS-TOOL-2's reason: a decision the adapter cannot classify is declared unclassified rather
// than guessed at.
func TestInteractionValidate_AcceptsEachDefinedVerdict(t *testing.T) {
	for _, v := range []string{VerdictAllow, VerdictDeny, VerdictOther} {
		in := Interaction{
			Kind: KindApprovalRequest, Status: StatusInProgress, Mode: ModeCard,
			Decisions: []DecisionChoice{{ID: "d", Label: "L", Verdict: v}},
		}
		if err := in.Validate(); err != nil {
			t.Errorf("Validate rejected verdict %q: %v", v, err)
		}
	}
}

// TestConformance_RequiresAVerdictOnEveryApprovalDecision — the OBLIGATION half, and the reason
// it lives in conformance rather than in Validate: an adapter that ships a verdict-less decision
// is not malformed, it is INCOMPLETE, and the daemon's fallback for one ("this is not a denial")
// is the wrong answer given quietly. Conformance is where an adapter's completeness is proved.
func TestConformance_RequiresAVerdictOnEveryApprovalDecision(t *testing.T) {
	if errsContain(CheckConformance(captureAdapter{}), "verdict") {
		t.Error("the conformant capturing stub was flagged for a verdict it carries")
	}
	errs := CheckConformance(decisionWithoutVerdict{})
	if len(errs) == 0 {
		t.Fatal("an approval_request whose decision carries NO verdict was NOT flagged. The daemon " +
			"classifies §3.6's allowed/denied off the chosen decision's verdict, so a decision without " +
			"one resolves as `allowed` whatever the owner tapped")
	}
	if !errsContain(errs, "verdict") {
		t.Errorf("violations %v do not name the verdict", errs)
	}
}
