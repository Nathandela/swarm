package swarmmobile_test

// FAILING-FIRST (TDD RED, GG-5) source-level guard for the interaction program's place on the
// bound surface (PB-BIND-3, ADR-009): the two transcript verbs are traced to a checked-in row,
// and the approval one is traced as READ ONLY.
//
// WHY A SEPARATE FILE, for the reason S16's and S17's equivalents state: coverage_test.go's
// requiredScreenElements is S8's transcription of PB-BIND-3, and putting this slice's RED
// inside S8's test names leaves an auditor reading the S8 evidence with failures that evidence
// never mentions. These elements are this slice's, they point at the SAME checked-in table, and
// they are enforced the way S8 enforces its own.
//
// It reads the facade's source and the table; it imports no runtime and claims nothing about a
// handset.

import (
	"sort"
	"strings"
	"testing"
)

// interactionScreenElements are the elements ADR-009's transcript adds. Hard-coded HERE, not
// read from the TSV, for the reason S8 states: deleting a row from the table must not make the
// requirement vanish.
var interactionScreenElements = map[string]string{
	// ADR-009 makes the transcript the phone's PRIMARY surface -- "the phone renders a
	// transcript of items and nothing else" -- and no existing verb serves it. journal.read is
	// not it: that page is an in-memory log of record TYPES, rebuilt empty by every process
	// death, while the transcript is the folded durable model (interaction-schema.md §6).
	"transcript.read": "ADR-009,PB-APP-3,PB-APP-8",
	// IS-LIFE-3's whole purpose is that an unresolved approval_request stays answerable across
	// a reconnect and a process death. A retention exemption with no surface to render what it
	// preserved is a requirement satisfied while the defect ships.
	"transcript.approvals_pending": "ADR-009,PB-APP-2,PB-APP-3",
	// W-APPROVE: ANSWERING one. IS-LIFE-4 makes a phone approval the existing signed
	// ActionApprove, and until this slice the whole path between the card and
	// approveInteraction was missing -- so "the machine is blocked and you may not act" was the
	// product. A rendered card with no answering verb is the requirement's failure mode, not
	// its scope.
	"transcript.approve": "ADR-009,PB-APP-2,PB-APP-3",
}

func TestInteraction_TheTranscriptVerbsAreTracedToFacadeMethods(t *testing.T) {
	src := loadFacade(t)
	rows := s17CoverageRows(t, src.Dir)

	have := map[string]bool{}
	for _, s := range exportedSurface(src) {
		switch s.Kind {
		case "func":
			have[s.Name] = true
		case "method", "field":
			have[s.Owner+"."+s.Name] = true
		}
	}

	elements := make([]string, 0, len(interactionScreenElements))
	for el := range interactionScreenElements {
		elements = append(elements, el)
	}
	sort.Strings(elements)

	for _, el := range elements {
		methods, ok := rows[el]
		if !ok {
			t.Errorf("PB-BIND-3 COVERAGE FAILURE: screen element %q (%s) has no row in "+
				"screen_coverage.tsv. ADR-009 moves the product's main screen onto this model, so a "+
				"transcript verb no row traces is the whole feature untraceable to a requirement",
				el, interactionScreenElements[el])
			continue
		}
		if len(methods) == 0 {
			t.Errorf("PB-BIND-3 COVERAGE FAILURE: screen element %q has a row and no facade method (%s)",
				el, interactionScreenElements[el])
			continue
		}
		for _, m := range methods {
			if !have[m] {
				t.Errorf("PB-BIND-3: screen_coverage.tsv maps %q to %q, which the facade does not export", el, m)
			}
		}
	}
}

// TestInteraction_TheFacadeAnswersAnApprovalAndNothingElse REPLACES the read-only guard that
// stood here, and the amendment is deliberate rather than a test bent to fit an
// implementation.
//
// WHAT THE OLD GUARD SAID, verbatim in its own words: the facade may export no verb named
// approve / answerapproval / decide / ... because "answering an approval is IS-LIFE-4's signed
// ActionApprove with an ApproveReq wire body no slice has built; a verb here today can only
// send a refusal". Its final sentence named its own expiry: "When the ApproveReq slice lands
// it will add the verb, its row and its own guard; until then the absence is the contract."
//
// THAT SLICE IS THIS ONE. schema.RemoteCommand carries the ApproveReq body, opForAction routes
// it, protocol.OpApprove serves it and approveInteraction validates it -- so the premise the
// ban rested on ("can only send a refusal") is now false, and keeping the ban would be
// enforcing a condition that no longer exists against the requirement it was protecting.
//
// WHAT REPLACES IT IS NOT WEAKER. The thing the old guard was really defending is ADR-007 D7
// and IS-LIFE-6: the phone must never author the approving KEYSTROKE. That danger does not
// expire with the wire body -- it gets sharper, because a screen now has a button. So the ban
// stays, narrowed to the verbs that would express it: nothing here may send a keystroke, key
// or prompt reply in the name of an approval, and the one answering verb is the signed
// command. The names below are the ones an implementer reaching for the forbidden shortcut
// would actually type.
func TestInteraction_TheFacadeAnswersAnApprovalAndNothingElse(t *testing.T) {
	src := loadFacade(t)

	// The verb the interaction program owes: one, signed, and named for what it is.
	answers := false
	for _, s := range entryPoints(src) {
		if s.Name == "Approve" {
			answers = true
		}
	}
	if !answers {
		t.Error("the facade exports no Approve. IS-LIFE-3 keeps an unresolved approval_request " +
			"answerable across a reconnect and a process death precisely so a surface can act on it; " +
			"a card the user can see and not answer leaves the machine blocked and calls that the product")
	}

	// ADR-007 D7 / IS-LIFE-6: the DECISION travels as the signed ActionApprove and the
	// machine-side adapter applies it. A phone that typed the approving keystroke -- even for
	// prompt_card, where a keystroke IS what eventually lands -- would be authoring an approval
	// the daemon never validated, which is the one thing both rules forbid in as many words.
	banned := []string{
		"approvekeystroke", "answerprompt", "sendapproval", "approvewithkey",
		"approvekey", "answerapprovalkey", "resolveapprovalkey", "typeapproval",
	}
	for _, s := range entryPoints(src) {
		low := strings.ToLower(s.Name)
		for _, b := range banned {
			if low == b {
				t.Errorf("the facade exports %s. An approval is answered by the signed ActionApprove and "+
					"applied MACHINE-side; the phone must never author the approving keystroke (ADR-007 D7: "+
					"an approve is \"never translated into a blind keystroke\"; IS-LIFE-6)", s.Line())
			}
		}
	}
}
