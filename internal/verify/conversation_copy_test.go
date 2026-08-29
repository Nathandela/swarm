// The conversation drawing's copy table is machine-checked, and the check is capable of
// failing on every clause it claims to enforce.
//
// WHY THIS EXISTS AT ALL. The owner-signed drawing stated, in section 03, that "The gate checks
// copy as recorded text. These are the strings; anything not on this sheet is not on the
// screen." No test read the sheet. The claim was demonstrably empty: a sentence shipped in
// Composer.kt for the ENDED composer state that appeared on no row of the table, and nothing
// failed. A design-honesty review found it by reading the promise against the tests.
//
// The cheap repair was to retract the sentence, and that is what was done first. This is the
// expensive repair, and it is better: the sheet now says something true because the check
// exists, rather than saying something weaker so that it can be true.
//
// WHY A BYTE COMPARISON AND NOT A REVIEW. Copy drift is invisible to reading. This tree held
// FIVE different sentences for the single fact that a turn had moved on -- two in Composer.kt,
// one in ErrorRouting.kt, one state label, and the sheet's own -- and every one of them read
// plausibly in isolation. The failure mode is the near-miss: an en dash for an em dash, or a
// curled apostrophe for a straight one, renders identically to a reviewer and is not the same
// string. The checker prints the non-ASCII codepoints of every extracted sentence for exactly
// that reason.
//
// WHY THE MUTATIONS BELOW. scripts/check-conversation-copy.py exits 0 on this repository, and
// an exit-0 check is indistinguishable from a check that cannot fail -- this repository's
// most-repeated defect, and the reason PB-DOC-7's own tests are built the same way
// (phaseb_manifest_test.go's header states it). Every clause is therefore exercised by
// MUTATING a copy of the inputs and requiring the checker to reject it, with the unmutated
// copy as the positive control.
package verify_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// copyCheckInputs are the files a mutation may perturb: the sheet that tables the copy, and
// every source that must carry it.
//
// THE CHECKER NAMES THEM, not this file, and that is the repair for agents-tracker-3jop. This
// list used to be typed here: four files, against the five the checker binds. Every mutation
// run therefore failed on the two that were never copied -- so each control was passing on a
// rejection its mutation had not caused, and the one that asserted nothing beyond "rejected"
// would have passed against a tree with no mutation in it at all. Deriving the list makes that
// drift impossible rather than detectable.
func copyCheckInputs(t *testing.T) []string {
	t.Helper()
	script := filepath.Join(repoRoot(t), "scripts", "check-conversation-copy.py")
	out, err := exec.Command("python3", script, "--inputs").CombinedOutput()
	if err != nil {
		t.Fatalf("cannot ask the checker which files it reads: %v\n%s", err, out)
	}
	var rels []string
	for _, ln := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			rels = append(rels, ln)
		}
	}
	if len(rels) < 2 {
		t.Fatalf("the checker named %d input file(s). A harness that copies one file cannot "+
			"mutate a tree:\n%s", len(rels), out)
	}
	return rels
}

// runCopyChecker runs the checker against an arbitrary root. Taking the root as an argument is
// what makes the negative controls possible at all: without it the checker can only ever be run
// against the one tree that passes.
func runCopyChecker(t *testing.T, root string) (string, bool) {
	t.Helper()
	script := filepath.Join(repoRoot(t), "scripts", "check-conversation-copy.py")
	out, err := exec.Command("python3", script, root).CombinedOutput()
	if err != nil {
		if _, isExit := err.(*exec.ExitError); !isExit {
			// python3 missing or unrunnable is a FAILURE, never a skip: a gate that
			// evaporates on the machine that lacks its interpreter is not a gate.
			t.Fatalf("cannot run the conversation copy checker: %v\n%s", err, out)
		}
		return string(out), false
	}
	return string(out), true
}

// copyTree copies the inputs into a scratch root, optionally rewriting one of them. The
// perturbation happens on the COPY and never in the shared working tree -- several agents
// compile these files concurrently, and a source state that exists in no commit costs a
// verification pass and then a reconciliation (recorded 2026-08-03).
func copyTree(t *testing.T, mutate func(rel, body string) string) string {
	t.Helper()
	root := repoRoot(t)
	dst := t.TempDir()
	for _, rel := range copyCheckInputs(t) {
		body, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		text := string(body)
		if mutate != nil {
			text = mutate(rel, text)
		}
		out := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(out, []byte(text), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return dst
}

// runMutation runs one negative control, having FIRST proved that the same tree without the
// mutation passes. It returns the mutated run's output and whether the checker accepted it.
//
// THE FIRST RUN IS THE WHOLE POINT. A negative control that asserts only "the checker rejected
// this tree" says nothing unless the same tree, unmutated, is accepted: otherwise it is
// passing on a rejection it did not cause, and it would go on passing with the mutation
// deleted. That is a check that cannot fail -- the exact defect this gate exists to catch --
// and it shipped inside the gate (agents-tracker-3jop): the scratch root held four files while
// the checker bound five, so every mutation run died on the two it could not open.
func runMutation(t *testing.T, mutate func(rel, body string) string) (string, bool) {
	t.Helper()
	clean, ok := runCopyChecker(t, copyTree(t, nil))
	if !ok {
		t.Fatalf("the UNMUTATED copy of the checker's inputs was rejected, so this control "+
			"cannot tell a mutated tree from a clean one and proves nothing about the "+
			"mutation below:\n%s", clean)
	}
	if strings.Contains(clean, "UNREADABLE") {
		t.Fatalf("the scratch root is missing a file the checker binds. Whatever the mutated "+
			"run below reports, it is not about the mutation:\n%s", clean)
	}
	return runCopyChecker(t, copyTree(t, mutate))
}

// TestCopySheet_TheShippedSentencesAreTheSignedOnes is the positive control.
func TestCopySheet_TheShippedSentencesAreTheSignedOnes(t *testing.T) {
	out, ok := runCopyChecker(t, repoRoot(t))
	if !ok {
		t.Fatalf("the shipped copy has drifted from the sheet that signs it:\n%s", out)
	}
	// The count is PARSED and not matched as a substring. This guard read
	// `strings.Contains(out, "0 binding(s) checked")` until the twentieth binding was added,
	// at which point "20 binding(s) checked" contained it and the positive control failed on a
	// healthy tree. The same string would have matched 10, 30 and 100 -- so on any other day
	// it was a guard that fired for the wrong reason, which is the same family of defect as
	// one that never fires.
	if n := bindingsChecked(t, out); n == 0 {
		t.Fatalf("zero bindings compared. A check that compares nothing passes perfectly and "+
			"protects nothing:\n%s", out)
	}
}

// TestCopySheet_TheScratchRootCarriesEveryBoundFile is the harness's own control, and it is
// here because the harness was the defect. The checker reported 19 comparisons against this
// repository and 12 against the scratch root, because two of the five files it binds were
// never copied there -- so every mutation run was rejected for a missing file rather than for
// its mutation. Counting both runs is the assertion that cannot be satisfied by prose.
func TestCopySheet_TheScratchRootCarriesEveryBoundFile(t *testing.T) {
	here, ok := runCopyChecker(t, repoRoot(t))
	if !ok {
		t.Fatalf("the shipped copy has drifted from the sheet that signs it:\n%s", here)
	}
	scratch, ok := runCopyChecker(t, copyTree(t, nil))
	if !ok {
		t.Fatalf("an UNMUTATED copy of the checker's inputs was rejected:\n%s", scratch)
	}
	if a, b := bindingsChecked(t, here), bindingsChecked(t, scratch); a != b {
		t.Fatalf("the checker compared %d binding(s) against this repository and %d against the "+
			"scratch root the mutations run in. The two must be equal: a mutation can only be "+
			"proved to cause a rejection in a tree the checker can read whole.\n\nscratch run:\n%s",
			a, b, scratch)
	}
}

// bindingsChecked reads the count off the checker's own summary line.
func bindingsChecked(t *testing.T, out string) int {
	t.Helper()
	m := regexp.MustCompile(`(\d+) binding\(s\) checked`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("the checker printed no summary line, so it reported no comparisons:\n%s", out)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("unreadable binding count %q: %v", m[1], err)
	}
	return n
}

// TestCopySheet_TheCheckerCatchesARetypedSentence is the mutation that matters most, because
// it is the one a human reviewer cannot catch: one character, rendering identically.
func TestCopySheet_TheCheckerCatchesARetypedSentence(t *testing.T) {
	const straight = "Not sent. There's a new reply. Read it, then send again."
	const curled = "Not sent. There’s a new reply. Read it, then send again." // U+2019, a curled apostrophe

	out, ok := runMutation(t, func(rel, body string) string {
		if strings.HasSuffix(rel, "Composer.kt") {
			return strings.Replace(body, straight, curled, 1)
		}
		return body
	})
	if ok {
		t.Fatalf("a curled apostrophe substituted for the straight one passed the checker. That is the exact "+
			"near-miss this gate exists for: it renders identically, reads identically, and is "+
			"a different string:\n%s", out)
	}
	if !strings.Contains(out, "DRIFT") {
		t.Fatalf("the checker rejected the tree without naming the drift, so a reader cannot "+
			"tell which sentence moved:\n%s", out)
	}
}

// TestCopySheet_TheCheckerCatchesASentenceThatLeftTheCode: a bound row whose sentence is no
// longer shipped anywhere. This is the direction that catches copy deleted in a refactor.
func TestCopySheet_TheCheckerCatchesASentenceThatLeftTheCode(t *testing.T) {
	const sync = "Some updates may be missing."

	out, ok := runMutation(t, func(rel, body string) string {
		if strings.HasSuffix(rel, "ConnectionUi.kt") {
			return strings.Replace(body, sync, "The link is having trouble.", 1)
		}
		return body
	})
	if ok {
		t.Fatalf("a screen replaced a signed sentence with an invented one and the checker "+
			"passed:\n%s", out)
	}
	if !strings.Contains(out, "DRIFT") {
		t.Fatalf("the checker rejected the tree without naming the sentence that left the "+
			"code, so a reader cannot tell what to put back:\n%s", out)
	}
}

// TestCopySheet_AShortLabelIsComparedAndNotDismissedAsATemplate: `bubble.pending` tables one
// word, `sending`, and that word is the whole of R6 -- the row that says a bubble stays pending
// until its own echo. The checker skipped every cell under twelve characters and printed the
// reason as "template only", which is FALSE for a short literal: `sending` is not a
// <placeholder>, it has bytes, and they are the bytes the screen shows. So the one row the
// pending-bubble ruling turns on was unbound, unchecked, and reported as if it were a template
// the check could not be expected to reach (agents-tracker-3jop).
//
// A twelve-character floor is not superstition -- a bare substring search for a short word
// matches inside an identifier and proves nothing -- but the answer is to compare the QUOTED
// Kotlin literal, not to drop the row and misdescribe why.
func TestCopySheet_AShortLabelIsComparedAndNotDismissedAsATemplate(t *testing.T) {
	out, ok := runCopyChecker(t, repoRoot(t))
	if !ok {
		t.Fatalf("the shipped copy has drifted from the sheet that signs it:\n%s", out)
	}
	if !strings.Contains(out, "bubble.pending") {
		t.Fatalf("the checker never mentioned bubble.pending, so the pending label ships "+
			"unreconciled against the sheet that signs it:\n%s", out)
	}
	if strings.Contains(out, "template only") {
		t.Fatalf("the checker still dismisses a cell as \"template only\". A short literal is "+
			"not a template, and a skip reported under a false reason reads as a limit of the "+
			"check rather than a row nobody is checking:\n%s", out)
	}
}

// TestCopySheet_TheCheckerCatchesAShortLabelThatLeftTheCode is the negative control for the
// row above: one word, compared as the literal the code actually writes.
func TestCopySheet_TheCheckerCatchesAShortLabelThatLeftTheCode(t *testing.T) {
	out, ok := runMutation(t, func(rel, body string) string {
		if strings.HasSuffix(rel, "Composer.kt") {
			return strings.Replace(body, `"sending"`, `"in flight"`, 1)
		}
		return body
	})
	if ok {
		t.Fatalf("the pending bubble's label was rewritten in the code and the checker passed. "+
			"R6 turns on this word: a bubble is pending until its own echo, and `sending` is "+
			"what the reader sees while it waits:\n%s", out)
	}
	if !strings.Contains(out, "DRIFT") {
		t.Fatalf("the checker rejected the tree without naming the drift:\n%s", out)
	}
}

// TestCopySheet_ABoundRowWithNothingToCompareIsAFailure closes the vacuity the false skip
// reason was hiding. A row in BOUND is an assertion that a named file carries that copy; if no
// cell of the row can be compared, the assertion is not being made at all, and the run says so
// in one line and moves on. That is a binding which cannot fail -- the shape of every defect
// this gate was built for -- so it is a fault, not a note.
func TestCopySheet_ABoundRowWithNothingToCompareIsAFailure(t *testing.T) {
	const cell = "<b>Some updates may be missing.</b>"

	out, ok := runMutation(t, func(rel, body string) string {
		if strings.HasSuffix(rel, "conversation-drawing.html") {
			if !strings.Contains(body, cell) {
				t.Fatalf("the sync row no longer tables its sentence; this control cannot run")
			}
			return strings.Replace(body, cell, "<b>&lt;notice&gt;</b>", 1)
		}
		return body
	})
	if ok {
		t.Fatalf("a bound row was reduced to a template and the checker passed. Its binding "+
			"then compares nothing, and a binding that compares nothing is indistinguishable "+
			"from one that holds:\n%s", out)
	}
	if !strings.Contains(out, "NOTHING TO COMPARE") {
		t.Fatalf("the checker rejected the tree without saying that a binding had stopped "+
			"comparing anything:\n%s", out)
	}
}

// TestCopySheet_TheCheckerCatchesARowLeavingTheSheet: the other direction. A row deleted from
// the drawing while the code still ships it means the screen carries unsigned copy -- which is
// precisely the defect that proved the sheet's original gate claim was empty.
func TestCopySheet_TheCheckerCatchesARowLeavingTheSheet(t *testing.T) {
	out, ok := runMutation(t, func(rel, body string) string {
		if strings.HasSuffix(rel, "conversation-drawing.html") {
			i := strings.Index(body, `<tr><td class="k">sync</td>`)
			if i < 0 {
				t.Fatalf("the sync row is not on the sheet; this control cannot run")
			}
			j := strings.Index(body[i:], "</tr>")
			return body[:i] + body[i+j+len("</tr>"):]
		}
		return body
	})
	if ok {
		t.Fatalf("a bound row was deleted from the sheet and the checker passed. A screen "+
			"shipping copy no sheet records is the state the gate was built to make "+
			"impossible:\n%s", out)
	}
	if !strings.Contains(out, "MISSING ROW") {
		t.Fatalf("the checker rejected the tree without saying a row had left the sheet:\n%s", out)
	}
}

// TestCopySheet_AnUnparseableSheetIsAFailureAndNotAPass guards the vacuity direction: a parse
// that matches nothing reports "no violations" perfectly.
func TestCopySheet_AnUnparseableSheetIsAFailureAndNotAPass(t *testing.T) {
	out, ok := runMutation(t, func(rel, body string) string {
		if strings.HasSuffix(rel, "conversation-drawing.html") {
			return "<html><body>the sheet, with its copy table removed</body></html>"
		}
		return body
	})
	if ok {
		t.Fatalf("a sheet with no copy table at all passed. A checker that parses nothing "+
			"finds no violations and is indistinguishable from one that works:\n%s", out)
	}
	if !strings.Contains(out, "PARSED ZERO ROWS") && !strings.Contains(out, "MISSING ROW") {
		t.Fatalf("the checker failed without explaining that it had parsed nothing:\n%s", out)
	}
}
