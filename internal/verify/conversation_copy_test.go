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
	"strings"
	"testing"
)

// copyCheckInputs are the files a mutation may perturb: the sheet that tables the copy, and
// the sources that must carry it.
var copyCheckInputs = []string{
	"docs/design/conversation-drawing.html",
	"android/app/src/main/kotlin/dev/swarm/phone/ui/ErrorRouting.kt",
	"android/app/src/main/kotlin/dev/swarm/phone/ui/ConnectionUi.kt",
	"android/app/src/main/kotlin/dev/swarm/phone/ui/kit/Composer.kt",
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
	for _, rel := range copyCheckInputs {
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

// TestCopySheet_TheShippedSentencesAreTheSignedOnes is the positive control.
func TestCopySheet_TheShippedSentencesAreTheSignedOnes(t *testing.T) {
	out, ok := runCopyChecker(t, repoRoot(t))
	if !ok {
		t.Fatalf("the shipped copy has drifted from the sheet that signs it:\n%s", out)
	}
	if !strings.Contains(out, "binding(s) checked") {
		t.Fatalf("the checker reported no comparisons, so its exit 0 says nothing:\n%s", out)
	}
	if strings.Contains(out, "0 binding(s) checked") {
		t.Fatalf("zero bindings compared. A check that compares nothing passes perfectly and "+
			"protects nothing:\n%s", out)
	}
}

// TestCopySheet_TheCheckerCatchesARetypedSentence is the mutation that matters most, because
// it is the one a human reviewer cannot catch: one character, rendering identically.
func TestCopySheet_TheCheckerCatchesARetypedSentence(t *testing.T) {
	const em = "Not sent — the terminal's input line was not empty."
	const en = "Not sent – the terminal's input line was not empty." // U+2013, an en dash

	root := copyTree(t, func(rel, body string) string {
		if strings.HasSuffix(rel, "Composer.kt") {
			return strings.Replace(body, em, en, 1)
		}
		return body
	})
	out, ok := runCopyChecker(t, root)
	if ok {
		t.Fatalf("an en dash substituted for an em dash passed the checker. That is the exact "+
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
	const sync = "This view may be missing events. It repairs itself when the link recovers."

	root := copyTree(t, func(rel, body string) string {
		if strings.HasSuffix(rel, "ConnectionUi.kt") {
			return strings.Replace(body, sync, "The link is having trouble.", 1)
		}
		return body
	})
	if out, ok := runCopyChecker(t, root); ok {
		t.Fatalf("a screen replaced a signed sentence with an invented one and the checker "+
			"passed:\n%s", out)
	}
}

// TestCopySheet_TheCheckerCatchesARowLeavingTheSheet: the other direction. A row deleted from
// the drawing while the code still ships it means the screen carries unsigned copy -- which is
// precisely the defect that proved the sheet's original gate claim was empty.
func TestCopySheet_TheCheckerCatchesARowLeavingTheSheet(t *testing.T) {
	root := copyTree(t, func(rel, body string) string {
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
	out, ok := runCopyChecker(t, root)
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
	root := copyTree(t, func(rel, body string) string {
		if strings.HasSuffix(rel, "conversation-drawing.html") {
			return "<html><body>the sheet, with its copy table removed</body></html>"
		}
		return body
	})
	out, ok := runCopyChecker(t, root)
	if ok {
		t.Fatalf("a sheet with no copy table at all passed. A checker that parses nothing "+
			"finds no violations and is indistinguishable from one that works:\n%s", out)
	}
	if !strings.Contains(out, "PARSED ZERO ROWS") && !strings.Contains(out, "MISSING ROW") {
		t.Fatalf("the checker failed without explaining that it had parsed nothing:\n%s", out)
	}
}
