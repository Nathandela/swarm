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

// TestInteraction_TheFacadeCannotAnswerAnApproval is the read-only half, adversarially: this
// workpackage exposes the pending card and NOTHING that decides it.
//
// It is a real property and not bookkeeping. IS-LIFE-4 makes an approval a SIGNED ActionApprove
// carrying a wire body (agent_instance, interaction_id, content_hash, expires_at, decision)
// that does not exist yet, and IS-APR-2 forbids the phone computing content_hash or expires_at
// -- it echoes both verbatim. A verb shipped here before that body exists could only send
// something the daemon refuses, or worse, be tempted into the blind keystroke ADR-007 D7 and
// IS-LIFE-6 both forbid the phone from authoring. When the ApproveReq slice lands it will add
// the verb, its row and its own guard; until then the absence is the contract.
func TestInteraction_TheFacadeCannotAnswerAnApproval(t *testing.T) {
	src := loadFacade(t)

	banned := []string{"approve", "answerapproval", "decide", "decideapproval", "denyapproval", "resolveapproval"}
	for _, s := range entryPoints(src) {
		low := strings.ToLower(s.Name)
		for _, b := range banned {
			if low == b {
				t.Errorf("the facade exports %s. Answering an approval is IS-LIFE-4's signed ActionApprove "+
					"with an ApproveReq wire body no slice has built; a verb here today can only send a "+
					"refusal, and the phone must never author the approving keystroke instead (ADR-007 D7, "+
					"IS-LIFE-6). The pending card is exposed READ ONLY on purpose", s.Line())
			}
		}
	}
}
