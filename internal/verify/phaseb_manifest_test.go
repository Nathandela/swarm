// PB-DOC-7 (FAILING FIRST): the slice-ownership manifest is machine-checked, and the check
// is capable of failing on every clause it claims to enforce.
//
//	"Every concrete PB-* id appears exactly once as an owned requirement, wildcard ownership
//	 ("all") is prohibited, every dependency edge is enumerated, and acyclicity is validated
//	 in CI. [...] A test parses section 11 and the requirement tables and fails on any unowned
//	 id, duplicate owner, wildcard, dangling edge, or cycle."
//
// WHY THIS EXISTS ON TOP OF THE SCRIPT. scripts/check-phaseb-manifest.py already exits 0 on
// this repository, and an exit-0 check is indistinguishable from a check that cannot fail --
// which is this phase's most-repeated defect and the reason PB-DOC-7 was written at all. Every
// clause below is therefore exercised by MUTATING a copy of the inputs and requiring the
// checker to reject it, with the unmutated copy as the positive control. A parse that silently
// matched nothing would pass "no violations" perfectly; the accepted-clause count at the end is
// the guard against that.
//
// TWO CLAUSES THE SCRIPT DID NOT ENFORCE BEFORE THIS SLICE, both named verbatim in the
// requirement. It read remote-phaseB-manifest.tsv and never section 11 at all, so (a) a
// wildcard in section 11 -- the exact v3 defect where S19 and S20 were both given the
// dependency "all", making each depend on the other -- was invisible to it, and (b) section 11
// could assign a requirement to a slice the manifest gives to another, which is the readable
// table quietly disagreeing with the source of truth. The spec records the second as carried
// hygiene ("section 11's readable table not cross-checked against the authoritative
// manifest"); this closes both.
package verify_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// checkerInputs are the three files the checker reads. A mutation test copies all three and
// perturbs exactly one, so a rejection is attributable to the perturbation.
var checkerInputs = []string{
	"docs/specifications/remote-phaseB-requirements.md",
	"docs/specifications/remote-phaseB-manifest.tsv",
	"docs/specifications/remote-phaseB-slices.tsv",
}

// runChecker runs the manifest checker against an arbitrary document root and returns its
// combined output and whether it exited 0. Taking the root as an argument is what makes the
// negative controls possible at all: without it the checker can only ever be run against the
// one tree that passes.
func runChecker(t *testing.T, root string, args ...string) (string, bool) {
	t.Helper()
	script := filepath.Join(repoRoot(t), "scripts", "check-phaseb-manifest.py")
	cmd := exec.Command("python3", append([]string{script, root}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if _, isExit := err.(*exec.ExitError); !isExit {
			// python3 missing or unrunnable is a FAILURE, never a skip: a gate that
			// evaporates on the machine that lacks its interpreter is not a gate.
			t.Fatalf("PB-DOC-7: cannot run the manifest checker: %v\n%s", err, out)
		}
		return string(out), false
	}
	return string(out), true
}

// stageDocs copies the checker's inputs into a fresh root, applying one replacement to one
// file. Passing an empty old/new leaves the copy pristine (the positive control).
func stageDocs(t *testing.T, file, old, new string) string {
	t.Helper()
	root := t.TempDir()
	src := repoRoot(t)
	for _, rel := range checkerInputs {
		body, err := os.ReadFile(filepath.Join(src, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if rel == file && old != "" {
			if !strings.Contains(string(body), old) {
				t.Fatalf("PB-DOC-7: the mutation anchor %q is no longer present in %s, so this "+
					"negative control is not perturbing what it claims to", old, rel)
			}
			body = []byte(strings.Replace(string(body), old, new, 1))
		}
		dst := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(dst, body, 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return root
}

const (
	specDoc     = "docs/specifications/remote-phaseB-requirements.md"
	manifestDoc = "docs/specifications/remote-phaseB-manifest.tsv"
	slicesDoc   = "docs/specifications/remote-phaseB-slices.tsv"
)

// orphanProbe is the slice id the orphan negative control invents. It MUST NOT be a slice the
// DAG already declares -- redeclaring an existing one produces a duplicate row, not an orphan --
// which is what TestPBDOC7_TheOrphanControlInventsItsSlice exists to keep true.
const orphanProbe = "S99"

// TestPBDOC7_TheRepositoryPasses is the positive control for every negative control below: if
// the pristine copy did not pass, a rejection would prove nothing about the mutation.
func TestPBDOC7_TheRepositoryPasses(t *testing.T) {
	out, ok := runChecker(t, stageDocs(t, "", "", ""))
	if !ok {
		t.Fatalf("PB-DOC-7: the checker rejects the unmutated repository:\n%s", out)
	}
	if !strings.Contains(out, "manifest OK") {
		t.Fatalf("PB-DOC-7: the checker exited 0 without reporting a verdict:\n%s", out)
	}
}

// TestPBDOC7_EveryEnforcedClauseCanFail walks the clauses PB-DOC-7 enumerates and requires a
// rejection naming each. The token match matters as much as the exit code: a checker that
// rejected everything for one reason would satisfy a bare "exit != 0" for all of them.
func TestPBDOC7_EveryEnforcedClauseCanFail(t *testing.T) {
	cases := []struct {
		clause string // what PB-DOC-7 calls it
		file   string
		old    string
		new    string
		token  string // the diagnostic the checker must emit
	}{
		{
			clause: "unowned id",
			file:   manifestDoc,
			old:    "PB-BIND-0\tS1\n",
			new:    "",
			token:  "UNOWNED   PB-BIND-0",
		},
		{
			clause: "duplicate owner",
			file:   manifestDoc,
			old:    "PB-BIND-0\tS1\n",
			new:    "PB-BIND-0\tS1\nPB-BIND-0\tS8\n",
			token:  "MULTIOWN  PB-BIND-0",
		},
		{
			clause: "manifest names an id the spec does not define",
			file:   manifestDoc,
			old:    "PB-BIND-0\tS1\n",
			new:    "PB-BIND-0\tS1\nPB-GHOST-1\tS1\n",
			token:  "PHANTOM   PB-GHOST-1",
		},
		{
			clause: "manifest owns a withdrawn requirement",
			file:   manifestDoc,
			old:    "PB-BIND-0\tS1\n",
			new:    "PB-BIND-0\tS1\nPB-DOC-6\tS20\n",
			token:  "WITHDRAWN PB-DOC-6",
		},
		{
			clause: "manifest names an unknown slice",
			file:   manifestDoc,
			old:    "PB-BIND-0\tS1\n",
			new:    "PB-BIND-0\tS99\n",
			token:  "BADSLICE  PB-BIND-0",
		},
		{
			clause: "dangling edge",
			file:   slicesDoc,
			old:    "S6\tS1\n",
			new:    "S6\tS1,S404\n",
			token:  "DANGLING  S6",
		},
		{
			clause: "cycle",
			file:   slicesDoc,
			old:    "S1\t-\n",
			new:    "S1\tS6\n",
			token:  "CYCLE",
		},
		{
			// The injected slice hangs off S1, so it has no dangling edge and no cycle: the
			// only thing wrong with it is that nothing on the S19 exit path pulls it in.
			clause: "orphan slice",
			file:   slicesDoc,
			old:    "S20\tS19\n",
			new:    "S20\tS19\n" + orphanProbe + "\tS1\n",
			token:  "ORPHAN    " + orphanProbe,
		},
		{
			// Two rows for one slice: the DAG the checker validates is then decided by line
			// order rather than by the file, and one of the two dependency sets is dropped on
			// the floor unseen. This is also the shape the orphan control silently decayed
			// into once S22 became a real slice -- see TestPBDOC7_TheOrphanControlInventsItsSlice.
			clause: "duplicate slice declaration",
			file:   slicesDoc,
			old:    "S22\tS13,S16\n",
			new:    "S22\tS13,S16\nS22\tS1\n",
			token:  "DUPSLICE  S22",
		},
		{
			clause: "wildcard ownership in section 11's requirements column",
			file:   specDoc,
			old:    "| S9 Façade<->transport integration | PB-NET-1 | opus | S8 |",
			new:    "| S9 Façade<->transport integration | all | opus | S8 |",
			token:  "WILDCARD  S9",
		},
		{
			clause: "wildcard dependency in section 11 (the v3 S19/S20 defect)",
			file:   specDoc,
			old:    "| S9 Façade<->transport integration | PB-NET-1 | opus | S8 |",
			new:    "| S9 Façade<->transport integration | PB-NET-1 | opus | all |",
			token:  "WILDCARD  S9",
		},
		{
			clause: "section 11 assigns a requirement the manifest gives to another slice",
			file:   specDoc,
			old:    "| S9 Façade<->transport integration | PB-NET-1 | opus | S8 |",
			new:    "| S9 Façade<->transport integration | PB-NET-1, PB-BIND-0 | opus | S8 |",
			token:  "S11REQ    S9",
		},
		{
			clause: "section 11 claims a dependency edge the DAG does not have",
			file:   specDoc,
			old:    "| S9 Façade<->transport integration | PB-NET-1 | opus | S8 |",
			new:    "| S9 Façade<->transport integration | PB-NET-1 | opus | S8, S3 |",
			token:  "S11DEP    S9",
		},
		{
			clause: "section 11 names a slice the DAG has never heard of",
			file:   specDoc,
			old:    "| S9 Façade<->transport integration | PB-NET-1 | opus | S8 |",
			new:    "| S9 Façade<->transport integration | PB-NET-1 | opus | S8 |\n| S77 Invented | PB-NET-1 | opus | S8 |",
			token:  "S11SLICE  S77",
		},
	}

	for _, tc := range cases {
		t.Run(tc.clause, func(t *testing.T) {
			out, ok := runChecker(t, stageDocs(t, tc.file, tc.old, tc.new))
			if ok {
				t.Fatalf("PB-DOC-7: the checker ACCEPTED a tree with a %s:\n%s", tc.clause, out)
			}
			if !strings.Contains(out, tc.token) {
				t.Fatalf("PB-DOC-7: the checker rejected the %s but never emitted %q, so the "+
					"rejection is not attributable to this clause:\n%s", tc.clause, tc.token, out)
			}
		})
	}

	if len(cases) < 14 {
		t.Fatalf("PB-DOC-7: only %d clauses are exercised; PB-DOC-7 enumerates unowned id, "+
			"duplicate owner, wildcard, dangling edge and cycle, and the checker enforces more",
			len(cases))
	}
}

// TestPBDOC7_TheOrphanControlInventsItsSlice keeps the orphan negative control from decaying the
// way it did between 60ed08d and 9b90704.
//
// The control was written against a DAG that ended at S21, so injecting "S22" added a slice the
// file had never declared: unreachable from S19, absent from the terminal list, an orphan. Five
// days later 9b90704 made S22 a real terminal slice, and the same injection became a second row
// for an existing exempt slice -- which last-wins parsing discards outright, leaving the checker's
// output byte-identical to the pristine run. The clause was still enforced; the mutation had
// stopped expressing it, and the control reported that as "the checker ACCEPTED an orphan".
//
// A mutation test can only be as honest as its mutation, so this asserts the premise directly:
// the probe id is one slices.tsv has never heard of.
func TestPBDOC7_TheOrphanControlInventsItsSlice(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(repoRoot(t), slicesDoc))
	if err != nil {
		t.Fatalf("read %s: %v", slicesDoc, err)
	}
	if strings.Contains(string(body), orphanProbe) {
		t.Fatalf("PB-DOC-7: the orphan negative control injects %s as a slice the DAG has never "+
			"heard of, but %s now mentions it. Redeclaring an existing slice is a duplicate row, "+
			"not an orphan: give the control a fresh id.", orphanProbe, slicesDoc)
	}
}

// TestPBDOC7_StrictSection11ReportsTheReadableTablesOmissions is the completeness half, and it
// is deliberately NOT the default.
//
// Section 11 states its own contract -- "The slice table above is the readable view; the
// manifest is the source of truth" -- so an omission there is drift, not a contradiction, and
// the two deserve different verdicts. The default run rejects only CONTRADICTIONS (a
// requirement assigned to the wrong slice, an edge the DAG does not have, a wildcard). --strict
// additionally requires the readable table to be COMPLETE, which is what an operator amending
// section 11 needs in order to know when they are finished.
//
// This test asserts strict mode is a real mode with real teeth: it must reject a tree whose
// section 11 omits an owned requirement, and it must name the omission.
func TestPBDOC7_StrictSection11ReportsTheReadableTablesOmissions(t *testing.T) {
	root := stageDocs(t, specDoc,
		"| S9 Façade<->transport integration | PB-NET-1 | opus | S8 |",
		"| S9 Façade<->transport integration | | opus | S8 |")
	out, ok := runChecker(t, root, "--strict-section11")
	if ok {
		t.Fatalf("PB-DOC-7: --strict-section11 accepted a section 11 row that owns nothing:\n%s", out)
	}
	if !strings.Contains(out, "S11MISS   S9") || !strings.Contains(out, "PB-NET-1") {
		t.Fatalf("PB-DOC-7: --strict-section11 rejected the tree without naming the omitted "+
			"requirement:\n%s", out)
	}
}
