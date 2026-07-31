package verify_test

// THE STRUCTURAL HOLE UNDER ADR-007 B121, closed here.
//
// internal/verify checks that every requirement ID appears in the traceability index, and
// NOTHING checked section 6.0's budget table for an owner or a fence. So a binding number
// could sit in the specification, never implemented, and pass every gate this project has --
// which is exactly what happened: "cached-state freshness, 5 min without a successful poll"
// was written in v2, grep returned ZERO hits for it outside the requirements file for six
// audit rounds, and the staleness decision it governs had no clock input at any layer. Section
// 6.0's own preamble says changing one of its values "requires committee agreement, not
// implementer discretion" -- and IGNORING one required nothing at all. That asymmetry is the
// defect this file removes.
//
// WHY A CITATION AND NOT AN INFERENCE. Two weaker rules were considered and rejected, and the
// reasons matter more than the rule:
//
//   - "the owning requirement has tests somewhere" PASSES on the defect. PB-APP-8 has a dozen
//     tests; none of them was about the freshness budget. A rule that green-lights the thing
//     it was written to catch is worse than no rule, because it is also an alibi.
//   - "the budget's number appears in a test" invites the transcription B113 names: a test
//     that asserts a constant equals itself moves with the constant and fences nothing.
//
// What is left is the one thing a machine can check and a human cannot forget: the row must
// SAY where its fence is, the file must exist, and it must NAME the requirement it is cited
// for -- so a citation cannot point at whatever test happens to be nearby. Whether that fence
// is any good remains a reading, and this file does not pretend otherwise.
//
// UNFENCED IS A FIRST-CLASS ANSWER, AND IT IS RATCHETED. A row with no fence must say so, in
// the table, where a reader of the budget sees it -- not in a test file where only an
// implementer would. The set of such rows is pinned below: closing one fails here until the
// ledger is updated, and adding one fails here until somebody writes down what they are
// admitting. Writing this column found two, and both are recorded rather than papered over.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// budgetSectionStart and budgetSectionEnd bracket section 6.0's table in the spec.
const (
	budgetSectionStart = "### 6.0 The numeric budget"
	budgetSectionEnd   = "### 6.0b"
)

// budgetRowFloor is the "cannot pass by measuring nothing" control. Section 6.0 carries 26
// rows today; a run that parses materially fewer has stopped understanding the table and would
// report perfect compliance over an empty set.
const budgetRowFloor = 20

// unfencedBudgets are the budgets with no fence, by the label in the table's first column.
//
// THEY ARE FINDINGS, NOT EXEMPTIONS. Each one is a binding number that an implementation can
// violate today with every gate green, which is the same condition PB-APP-11 was in. They are
// listed so the count can only move deliberately, and so a reader of this file learns what the
// project currently cannot check about its own budget.
var unfencedBudgets = map[string]string{
	"Resync rate": "mobile/app.go resyncBudget implements both halves (<= 1 per stream per 5 s, " +
		"<= 12 per 5 min) and no test drives either. The relay decides when a stream looks broken " +
		"enough to ask for a repair, so this is a budget the declared adversary meters against",
	"Inbound drain rate (reads + acks), each hop": "the READS half's token bucket is fenced only " +
		"in internal/remote/transport, a package whose Session has zero production constructions " +
		"(ADR-007 B94/B121) -- so what is measured is not what the shipped phone runs. The ACKS " +
		"half is fenced in production and cited",
}

// budgetRow is one row of section 6.0.
type budgetRow struct {
	label     string   // first column, markdown stripped
	owners    []string // requirement ids named in the row
	fences    []string // repo-relative paths cited by "fence:"
	unfenced  bool     // the row cites UNFENCED
	withdrawn bool     // the budget itself is withdrawn, so it owes no fence
	line      int
}

var (
	budgetIDPattern    = regexp.MustCompile(`PB-[A-Z0-9]+-\d+`)
	budgetFencePattern = regexp.MustCompile(`\*\*fence:\*\*\s*(.+?)\s*\|?$`)
	budgetPathPattern  = regexp.MustCompile(`[\w./-]+_test\.go`)
	markdownNoise      = regexp.MustCompile("[*`]")
)

// parseBudgetRows reads section 6.0's table. It is a pure function of the text so that it can
// be shown FAILING on inputs constructed to break it, which is the control the coverage guard
// next door already carries: a rule that has only ever seen compliant input has not been run.
func parseBudgetRows(spec string) []budgetRow {
	lines := strings.Split(spec, "\n")
	start, end := -1, len(lines)
	for i, l := range lines {
		if start < 0 && strings.HasPrefix(l, budgetSectionStart) {
			start = i
			continue
		}
		if start >= 0 && strings.HasPrefix(l, budgetSectionEnd) {
			end = i
			break
		}
	}
	if start < 0 {
		return nil
	}
	var out []budgetRow
	for i := start; i < end; i++ {
		l := lines[i]
		if !strings.HasPrefix(l, "| ") || strings.HasPrefix(l, "|---") || strings.Contains(l, "| Budget |") {
			continue
		}
		cells := strings.Split(strings.Trim(strings.TrimSpace(l), "|"), "|")
		row := budgetRow{
			label: strings.TrimSpace(markdownNoise.ReplaceAllString(cells[0], "")),
			line:  i + 1,
		}
		for _, id := range budgetIDPattern.FindAllString(l, -1) {
			row.owners = append(row.owners, id)
		}
		sort.Strings(row.owners)
		row.owners = dedupe(row.owners)
		row.withdrawn = strings.Contains(l, "WITHDRAWN")
		if m := budgetFencePattern.FindStringSubmatch(l); m != nil {
			cited := m[1]
			row.unfenced = strings.Contains(cited, "UNFENCED")
			row.fences = budgetPathPattern.FindAllString(cited, -1)
		}
		out = append(out, row)
	}
	return out
}

func dedupe(in []string) []string {
	var out []string
	for i, s := range in {
		if i == 0 || in[i-1] != s {
			out = append(out, s)
		}
	}
	return out
}

// TestPBDOC7_EveryBudgetHasAnOwnerAndAFence is the rule itself.
func TestPBDOC7_EveryBudgetHasAnOwnerAndAFence(t *testing.T) {
	root := repoRoot(t)
	spec := readDoc(t, "docs/specifications/remote-phaseB-requirements.md")
	rows := parseBudgetRows(spec)
	if len(rows) < budgetRowFloor {
		t.Fatalf("only %d rows parsed out of section 6.0's budget table; the table's shape has "+
			"changed and this guard is measuring nothing", len(rows))
	}
	active := map[string]bool{}
	for _, id := range activeSpecIDs(spec) {
		active[id] = true
	}

	seenUnfenced := map[string]bool{}
	for _, row := range rows {
		// (1) AN OWNER. A budget nobody owns is a number with no requirement to violate.
		var owned []string
		for _, id := range row.owners {
			if active[id] {
				owned = append(owned, id)
			}
		}
		if len(owned) == 0 {
			t.Errorf("section 6.0:%d: the %q budget names no requirement the spec still defines "+
				"(found %v). A binding number with no owner cannot be violated by anything, which "+
				"is indistinguishable from not being binding", row.line, row.label, row.owners)
			continue
		}
		if row.withdrawn {
			continue // the budget itself is withdrawn; there is nothing left to fence
		}

		// (2) A FENCE, CITED. Not inferred from the owner's other tests -- see the header.
		if row.unfenced {
			if _, known := unfencedBudgets[row.label]; !known {
				t.Errorf("section 6.0:%d: the %q budget is marked UNFENCED and is not in this "+
					"file's ledger. Admitting that a binding number is unchecked is allowed and is "+
					"a finding: write down what is being admitted, here, where the next reader "+
					"counts them", row.line, row.label)
			}
			seenUnfenced[row.label] = true
			continue
		}
		if len(row.fences) == 0 {
			t.Errorf("section 6.0:%d: the %q budget (owned by %v) cites no fence. Section 6.0 says "+
				"changing one of its values needs committee agreement -- and until this rule "+
				"existed, IGNORING one needed nothing at all: that is how PB-APP-8's 5-minute "+
				"freshness budget sat unimplemented for six audit rounds with every gate green "+
				"(ADR-007 B121)", row.line, row.label, owned)
			continue
		}
		// (3) THE FENCE MUST EXIST AND NAME WHAT IT IS CITED FOR, so a citation cannot point at
		// whatever test happens to be nearby.
		for _, rel := range row.fences {
			body, err := os.ReadFile(filepath.Join(root, rel))
			if err != nil {
				t.Errorf("section 6.0:%d: the %q budget cites %s, which does not exist",
					row.line, row.label, rel)
				continue
			}
			if !namesAny(string(body), owned) {
				t.Errorf("section 6.0:%d: the %q budget cites %s, which never names %v. A citation "+
					"that names nothing is a pointer at a neighbourhood, not at a fence",
					row.line, row.label, rel, owned)
			}
		}
	}

	// The ledger is closed in BOTH directions: a budget that has since been fenced must be
	// removed from it, or the count goes on reporting a gap that is no longer there and the
	// next reader discounts the whole list.
	for label := range unfencedBudgets {
		if !seenUnfenced[label] {
			t.Errorf("the unfenced ledger carries %q, and section 6.0 no longer marks it UNFENCED. "+
				"Either the budget was fenced (delete the row here) or its label changed (which "+
				"silently emptied this guard)", label)
		}
	}
}

func namesAny(body string, ids []string) bool {
	for _, id := range ids {
		if strings.Contains(body, id) {
			return true
		}
	}
	return false
}

// TestPBDOC7_TheBudgetRuleRejectsAnUnownedOrUnfencedRow shows the rule failing on inputs built
// to break it. Without it the test above is a green light whose bulb has never been checked --
// and this rule exists BECAUSE a guard that only ever saw compliant input let a binding number
// go unimplemented for six rounds.
func TestPBDOC7_TheBudgetRuleRejectsAnUnownedOrUnfencedRow(t *testing.T) {
	const spec = budgetSectionStart + "\n\n" +
		"| Budget | Value | Where it binds |\n|---|---|---|\n" +
		"| Fenced budget | 5 min | PB-NET-1 **fence:** internal/verify/phaseb_budget_test.go |\n" +
		"| Unfenced budget | 8/s | PB-NET-2 |\n" +
		"| Homeless budget | 3 | implementer discretion |\n" +
		"| Admitted budget | 12 | PB-NET-3 **fence:** UNFENCED (nobody built it) |\n" +
		"| Withdrawn budget | n/a | WITHDRAWN -- unbuildable | PB-NET-4 |\n" +
		budgetSectionEnd + " next section\n"

	rows := parseBudgetRows(spec)
	if len(rows) != 5 {
		t.Fatalf("parsed %d rows, want 5: the parser cannot see the table it is about to judge", len(rows))
	}
	byLabel := map[string]budgetRow{}
	for _, r := range rows {
		byLabel[r.label] = r
	}

	if got := byLabel["Fenced budget"]; len(got.fences) != 1 || got.unfenced {
		t.Errorf("a cited fence was not read: %+v", got)
	}
	if got := byLabel["Unfenced budget"]; len(got.fences) != 0 || got.unfenced {
		t.Errorf("a row with no fence clause must read as neither cited nor admitted: %+v", got)
	}
	if got := byLabel["Homeless budget"]; len(got.owners) != 0 {
		t.Errorf("a row naming no requirement must read as unowned: %+v", got)
	}
	if got := byLabel["Admitted budget"]; !got.unfenced || len(got.fences) != 0 {
		t.Errorf("an UNFENCED admission must be read as one: %+v", got)
	}
	if got := byLabel["Withdrawn budget"]; !got.withdrawn {
		t.Errorf("a withdrawn budget must be exempt from the fence rule: %+v", got)
	}

	// And the citation check itself, on a path that does not exist and on one that exists but
	// names something else -- the two ways a citation can be present and worthless.
	root := repoRoot(t)
	if _, err := os.ReadFile(filepath.Join(root, "internal/verify/no_such_file_test.go")); err == nil {
		t.Fatal("the negative control names a file that exists; it proves nothing")
	}
	body := readDoc(t, "docs/specifications/remote-phaseB-manifest.tsv")
	if namesAny(body, []string{"PB-NOT-A-REQUIREMENT-9"}) {
		t.Fatal("namesAny matched an id that appears nowhere")
	}
	if !namesAny(body, []string{"PB-APP-11"}) {
		t.Fatal("namesAny failed to match an id that is present, so the citation check is vacuous")
	}
}
