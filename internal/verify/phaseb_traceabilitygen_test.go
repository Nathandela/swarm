package verify_test

// THE GENERATOR'S OWN GUARD (agents-tracker-6g9).
//
// scripts/phaseb-traceability.py writes the document every other check in this package
// reads. Its shipped list and its not-met dict are HAND-MAINTAINED -- the script says so
// itself -- and they have fallen behind the manifest: the list stops at S20 while the
// manifest owns requirements to S22, S23 and S24.
//
// WHAT THAT COSTS, and it is not what it looks like. Running the generator today does not
// produce an obviously broken document. It produces a SELF-CONSISTENT one: eighteen
// requirements revert from shipped or NOT MET to pending, the sixteen design-system rows
// added by hand lose their verification narrative, and the header table is rewritten to
// agree with the reduced rows -- so every gate in this package stays GREEN over a document
// that has silently lost a day of curation. A red lane gets investigated. A green one does
// not.
//
// AND THE SHELL TRUNCATES BEFORE PYTHON STARTS. The command this script documented for
// itself was `python3 scripts/phaseb-traceability.py > <the document>`, where the `>`
// opens and empties the target before the interpreter runs. A script that discovers a
// problem and exits without printing therefore leaves an EMPTY document, not an intact
// one. Refusing to run is only safe if the script owns the file, so it now takes the
// output path as an argument and writes it itself.
//
// These tests drive the real script, because a guard against destroying a file is worth
// exactly as much as the evidence that it has been seen to refuse.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const traceGenScript = "scripts/phaseb-traceability.py"

// runTraceGen runs the generator with the repository as its input root and returns its
// combined output and whether it exited 0.
func runTraceGen(t *testing.T, script string, args ...string) (string, bool) {
	t.Helper()
	cmd := exec.Command("python3", append([]string{script}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if _, isExit := err.(*exec.ExitError); !isExit {
			// A missing interpreter is a FAILURE, never a skip, for the same reason the
			// manifest checker's harness says so: a gate that evaporates on the machine
			// without its interpreter is not a gate.
			t.Fatalf("6g9: cannot run the traceability generator: %v\n%s", err, out)
		}
		return string(out), false
	}
	return string(out), true
}

func traceGenPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRoot(t), filepath.FromSlash(traceGenScript))
}

// TestTraceabilityGen_RefusesWithoutAnOutputPath pins the half of the fix that the shell
// makes necessary: the script must own the file it replaces. If it still wrote to stdout,
// the only documented way to use it would be a redirect, and a redirect has already
// destroyed the target by the time any guard below could run.
func TestTraceabilityGen_RefusesWithoutAnOutputPath(t *testing.T) {
	out, ok := runTraceGen(t, traceGenPath(t))
	if ok {
		t.Fatalf("6g9: the generator ran with no output path and exited 0. Its only usable "+
			"invocation is then a shell redirect, which truncates the document before the "+
			"script starts and puts every guard below out of reach.\nOutput:\n%s", head(out, 400))
	}
	if !strings.Contains(strings.ToLower(out), "output") {
		t.Errorf("6g9: refusing without an output path should say so; got:\n%s", head(out, 400))
	}
}

// TestTraceabilityGen_WritesTheDocumentItself is the positive control. A guard that refused
// everything would satisfy the test above and be useless.
func TestTraceabilityGen_WritesTheDocumentItself(t *testing.T) {
	target := filepath.Join(t.TempDir(), "fresh.md")

	out, ok := runTraceGen(t, traceGenPath(t), "--root", repoRoot(t), target)
	if !ok {
		t.Fatalf("6g9: the generator refused to write a target that does not exist yet, so "+
			"there is no way to produce the document at all:\n%s", head(out, 600))
	}
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("6g9: the generator exited 0 and wrote no file: %v", err)
	}
	if n := strings.Count(string(body), "\n| PB-"); n < 100 {
		t.Errorf("6g9: the generated document holds %d requirement rows; the manifest owns far "+
			"more, so the run produced something truncated", n)
	}
	if strings.Contains(string(body), traceGenScript+" >") {
		t.Errorf("6g9: the generated document still tells its reader to regenerate with a shell " +
			"redirect. That command truncates the document before the script starts, which is " +
			"the failure this change exists to remove")
	}
}

// TestTraceabilityGen_RefusesToDemoteCuratedRows is the guard itself, over the exact
// situation that exists today: a curated document on disk, and a generator whose shipped
// list no longer covers the slices that document reports as shipped.
func TestTraceabilityGen_RefusesToDemoteCuratedRows(t *testing.T) {
	live := filepath.Join(repoRoot(t), "docs", "verification", "remote-phaseB-traceability.md")
	before, err := os.ReadFile(live)
	if err != nil {
		t.Fatalf("6g9: cannot read the live document: %v", err)
	}
	target := filepath.Join(t.TempDir(), "curated.md")
	if err := os.WriteFile(target, before, 0o644); err != nil {
		t.Fatalf("6g9: cannot stage the curated document: %v", err)
	}

	out, ok := runTraceGen(t, traceGenPath(t), "--root", repoRoot(t), target)
	if ok {
		t.Fatalf("6g9: the generator OVERWROTE a curated document whose rows it would demote to "+
			"pending, and exited 0. That is the silent revert this guard exists to stop.\n%s",
			head(out, 600))
	}
	after, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("6g9: the target is unreadable after a refused run: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("6g9: the generator refused AND still modified the target (%d bytes before, %d "+
			"after). Refusing has to mean leaving the file alone, or the refusal is the same "+
			"data loss with a louder message", len(before), len(after))
	}
	// The message has to name what it is protecting, or the next person deletes the guard.
	for _, want := range []string{"S22", "pending"} {
		if !strings.Contains(out, want) {
			t.Errorf("6g9: the refusal does not mention %q, so it does not tell the reader which "+
				"rows it is protecting or why. Got:\n%s", want, head(out, 800))
		}
	}
}

// TestTraceabilityGen_DerivesTheVoidSplitFromItsOwnData covers the sentence the issue was
// filed for. It was a hardcoded literal, which is only ever right by coincidence: the
// document's own prose gate reads it, so a stale literal makes the generator emit a claim
// that contradicts the rows it emitted beside it.
//
// The control perturbs exactly one input -- one more requirement moved into NOT_MET in a
// COPY of the script -- and asserts the sentence follows. A hardcoded literal cannot.
func TestTraceabilityGen_DerivesTheVoidSplitFromItsOwnData(t *testing.T) {
	src, err := os.ReadFile(traceGenPath(t))
	if err != nil {
		t.Fatalf("6g9: cannot read the generator: %v", err)
	}
	// PB-DOC-1 is owned by a shipped slice and is not in NOT_MET, so moving it in raises the
	// not-met count by exactly one without touching the void count.
	const anchor = "NOT_MET = {"
	if !strings.Contains(string(src), anchor) {
		t.Fatalf("6g9: the generator no longer declares %s, so this control cannot perturb it", anchor)
	}
	perturbed := strings.Replace(string(src), anchor,
		anchor+"\n    \"PB-DOC-1\": \"an added not-met row, for a negative control\",", 1)

	dir := t.TempDir()
	copyScript := filepath.Join(dir, "perturbed.py")
	if err := os.WriteFile(copyScript, []byte(perturbed), 0o755); err != nil {
		t.Fatalf("6g9: cannot stage the perturbed generator: %v", err)
	}
	target := filepath.Join(dir, "out.md")

	if out, ok := runTraceGen(t, copyScript, "--root", repoRoot(t), target); !ok {
		t.Fatalf("6g9: the perturbed generator refused to write a fresh target:\n%s", head(out, 600))
	}
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("6g9: perturbed run wrote no file: %v", err)
	}
	if !strings.Contains(string(body), "11 not met + 1 void") {
		t.Errorf("6g9: with one more requirement in NOT_MET the document should read "+
			"\"11 not met + 1 void\"; the split is not computed from the not-met set. Found: %q",
			findSplit(string(body)))
	}
}

func findSplit(body string) string {
	i := strings.Index(body, "not met + ")
	if i < 0 {
		return "(no not-met-plus-void sentence at all)"
	}
	start := i - 12
	if start < 0 {
		start = 0
	}
	return body[start : i+20]
}

func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
