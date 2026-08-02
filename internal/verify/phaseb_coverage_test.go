// PB-DOC-2 (FAILING FIRST): "Phase B exit criteria in implementation-goals.md; a verification
// file maps every PB-* ID to evidence." Acceptance: "Full ID coverage."
//
// COVERAGE OF IDS, NOT OF PROSE. The criterion is the one thing about a traceability document
// that a reader cannot check by reading it: whether the 143rd requirement is in there. A file
// that maps 142 of 143 looks exactly like one that maps all of them, and the missing row is
// always the requirement nobody remembered -- which is the same failure mode that put PB-KEY-2,
// PB-STATE-10, PB-SAS-2, PB-GW-7, PB-GW-8, PB-KEY-8 and PB-PUSH-10 through three audit rounds
// as homeless requirements.
//
// HOW THIS DIFFERS FROM THE TWO CHECKS EITHER SIDE OF IT, since three overlapping guards over
// one table is how a gap survives all three:
//
//   - scripts/check-phaseb-manifest.py checks spec <-> MANIFEST. It never opens the generated
//     traceability document, so a stale generated file is invisible to it.
//   - TestPBE2E3_EveryShippedRequirementsEvidenceFileNamesIt (PB-E2E-3) checks that each
//     SHIPPED row's cited evidence file names it. It iterates the rows that exist and is
//     therefore blind by construction to a requirement with no row at all.
//   - this checks spec <-> the generated DOCUMENT, which is the artifact the audit reads.
//
// The coverage rule is a pure function of the two texts precisely so that it can be shown
// FAILING on inputs constructed to break it. A guard over checked-in documentation that has
// only ever been run against documentation that satisfies it has not been tested.
package verify_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// specIDRow matches a requirement's defining row in the spec's section 6 tables, and
// distinguishes a withdrawn one (struck through) from an active one.
var specIDRow = regexp.MustCompile(`^\|\s*(~~)?\*{0,2}(PB-[A-Z0-9]+-\d+)\*{0,2}(~~)?\s*\|`)

// traceIDRow matches a row of the generated per-requirement table.
var traceIDRow = regexp.MustCompile(`^\|\s*(PB-[A-Z0-9]+-\d+)\s*\|`)

// activeSpecIDs returns the requirement ids the spec defines and has not withdrawn.
func activeSpecIDs(spec string) []string {
	active, withdrawn := map[string]bool{}, map[string]bool{}
	for _, line := range strings.Split(spec, "\n") {
		m := specIDRow.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if m[1] != "" {
			withdrawn[m[2]] = true
		} else {
			active[m[2]] = true
		}
	}
	out := make([]string, 0, len(active))
	for id := range active {
		if !withdrawn[id] {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// tracedIDs returns the requirement ids the traceability document has a row for.
func tracedIDs(trace string) map[string]bool {
	out := map[string]bool{}
	for _, line := range strings.Split(trace, "\n") {
		if m := traceIDRow.FindStringSubmatch(line); m != nil {
			out[m[1]] = true
		}
	}
	return out
}

// coverageGaps is PB-DOC-2's criterion as a decidable function: ids the spec defines with no
// row in the traceability document, and rows naming ids the spec does not define. Both
// directions matter -- a row for a deleted requirement is how a count stays reassuring while
// what it counts drifts.
func coverageGaps(spec, trace string) (missing, phantom []string) {
	traced := tracedIDs(trace)
	active := activeSpecIDs(spec)
	activeSet := map[string]bool{}
	for _, id := range active {
		activeSet[id] = true
		if !traced[id] {
			missing = append(missing, id)
		}
	}
	for id := range traced {
		if !activeSet[id] {
			phantom = append(phantom, id)
		}
	}
	sort.Strings(phantom)
	return missing, phantom
}

func readDoc(t *testing.T, rel string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(repoRoot(t), rel))
	if err != nil {
		t.Fatalf("PB-DOC-2: cannot read %s: %v", rel, err)
	}
	return string(body)
}

// TestPBDOC2_EveryActiveRequirementIsInTheTraceabilityIndex is the criterion itself.
func TestPBDOC2_EveryActiveRequirementIsInTheTraceabilityIndex(t *testing.T) {
	spec := readDoc(t, "docs/specifications/remote-phaseB-requirements.md")
	trace := readDoc(t, "docs/verification/remote-phaseB-traceability.md")

	active := activeSpecIDs(spec)
	if len(active) < 100 {
		t.Fatalf("PB-DOC-2: only %d active requirements were parsed out of the spec; the row "+
			"pattern no longer matches its tables, so this guard is measuring nothing", len(active))
	}
	missing, phantom := coverageGaps(spec, trace)
	if len(missing) > 0 {
		t.Errorf("PB-DOC-2: %d of %d requirements have NO row in the traceability index, so the "+
			"per-requirement audit has nothing to read for them: %v", len(missing), len(active), missing)
	}
	if len(phantom) > 0 {
		t.Errorf("PB-DOC-2: the traceability index has rows for %d ids the spec does not define "+
			"(deleted or renamed): %v", len(phantom), phantom)
	}
}

// TestPBDOC2_TheCoverageRuleRejectsAnIncompleteIndex shows the rule failing. Without this, the
// test above is a green light whose bulb has never been checked: it has only ever seen inputs
// that satisfy it, and a coverageGaps that returned nil unconditionally would look identical.
func TestPBDOC2_TheCoverageRuleRejectsAnIncompleteIndex(t *testing.T) {
	const spec = "| PB-NET-1 | first | crit |\n" +
		"| PB-NET-2 | second | crit |\n" +
		"| ~~PB-NET-3~~ | withdrawn | n/a |\n"

	t.Run("a requirement with no row is missing", func(t *testing.T) {
		missing, phantom := coverageGaps(spec, "| PB-NET-1 | S6 | shipped | `e.md` |\n")
		if len(missing) != 1 || missing[0] != "PB-NET-2" {
			t.Fatalf("missing = %v, want [PB-NET-2]", missing)
		}
		if len(phantom) != 0 {
			t.Fatalf("phantom = %v, want none", phantom)
		}
	})

	t.Run("a row for an undefined id is a phantom", func(t *testing.T) {
		trace := "| PB-NET-1 | S6 | shipped | `e.md` |\n| PB-NET-2 | S6 | shipped | `e.md` |\n" +
			"| PB-NET-9 | S6 | shipped | `e.md` |\n"
		missing, phantom := coverageGaps(spec, trace)
		if len(missing) != 0 {
			t.Fatalf("missing = %v, want none", missing)
		}
		if len(phantom) != 1 || phantom[0] != "PB-NET-9" {
			t.Fatalf("phantom = %v, want [PB-NET-9]", phantom)
		}
	})

	t.Run("a withdrawn requirement is not owed a row", func(t *testing.T) {
		trace := "| PB-NET-1 | S6 | shipped | `e.md` |\n| PB-NET-2 | S6 | shipped | `e.md` |\n"
		if missing, phantom := coverageGaps(spec, trace); len(missing) != 0 || len(phantom) != 0 {
			t.Fatalf("a complete index over active ids reported missing=%v phantom=%v", missing, phantom)
		}
	})
}

// TestPBDOC2_PhaseBExitCriteriaAreStatedInImplementationGoals is the requirement's first half.
//
// The criteria are what the final committee validates production readiness AGAINST; without
// them it has to infer the bar from 143 requirement rows, which is the inference they exist to
// remove. This asserts they are present and enumerated -- not that they are met, which is the
// committee's job and not a regular expression's.
func TestPBDOC2_PhaseBExitCriteriaAreStatedInImplementationGoals(t *testing.T) {
	goals := readDoc(t, "docs/specifications/implementation-goals.md")
	if !strings.Contains(goals, "### Epic 15") {
		t.Fatal("PB-DOC-2: implementation-goals.md has no Epic 15 section, so Phase B has no " +
			"stated exit criteria for the final committee to validate against")
	}
	found := regexp.MustCompile(`(?m)^- E15\.\d+`).FindAllString(goals, -1)
	if len(found) < 8 {
		t.Fatalf("PB-DOC-2: only %d E15.x exit criteria are enumerated; Phase B's exit bar is "+
			"E15.1-E15.8", len(found))
	}
}
