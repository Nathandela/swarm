package verify_test

// The traceability table's DERIVATION column, fenced (ADR-007 B129).
//
// The column answers a question no other artifact in this project answers: has anyone ever
// independently made this row's fence FAIL ON PURPOSE. It is orthogonal to *Status* -- shipped
// says the work landed, derived says somebody attacked it -- and "green but unexamined" is the
// dominant risk here: seven tranches have been re-derived and seven produced findings.
//
// IT IS FENCED BECAUSE AN UNCHECKED NUMBER IS THE HOLE IT EXISTS TO CLOSE. The column is
// generated from self-reported markers in evidence files, so nothing stops a marker being
// written for work nobody did -- except that a DERIVED verdict must name the mutation IN THE
// SAME ROW, and these tests assert the generated table says exactly what the markers say. A
// derivation count that drifted from its own source would be precisely the section 6.0-shaped
// defect this project has spent eight rounds closing: a figure everyone cites and nothing
// verifies.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// traceDerivRow reads the five-column generated row. The derivation cell is group 4.
var traceDerivRow = regexp.MustCompile(
	`^\|\s*(PB-[A-Z0-9]+-\d+)\s*\|\s*([^|]*)\|\s*([^|]*)\|\s*([^|]*)\|\s*(.*?)\s*\|$`)

// derivMarkerRow reads a marker row from an evidence file's `## Derivation` section.
var derivMarkerRow = regexp.MustCompile(
	`^\|\s*(PB-[A-Z0-9]+-\d+)\s*\|\s*([^|]*?)\s*\|\s*(.*?)\s*\|\s*$`)

// derivationMarkers returns requirement -> derived, read from every evidence file's
// `## Derivation` section, by the same rules the generator applies: only rows inside the
// section, NOT DERIVED tested before DERIVED (it contains it as a substring), and a DERIVED
// verdict with no mutation named counted as NOT DERIVED.
func derivationMarkers(t *testing.T, root string) map[string]bool {
	t.Helper()
	dir := filepath.Join(root, "docs", "verification")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("cannot read %s: %v", dir, err)
	}
	out := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		_, section, found := strings.Cut(string(body), "## Derivation")
		if !found {
			continue
		}
		for _, line := range strings.Split(section, "\n") {
			if strings.HasPrefix(line, "## ") {
				break // the section ends at the next h2
			}
			m := derivMarkerRow.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			verdict, mutation := strings.ToUpper(m[2]), strings.TrimSpace(m[3])
			switch {
			case strings.Contains(verdict, "NOT DERIVED"):
				out[m[1]] = false
			case strings.Contains(verdict, "DERIVED"):
				out[m[1]] = mutation != ""
			}
		}
	}
	return out
}

// TestDerivation_TheGeneratedColumnSaysWhatTheMarkersSay is the agreement check. It has a
// vacuity control built in for the reason TestPBE2E3's walk does: a parse that matched nothing
// would satisfy "no disagreements" perfectly, which is how a table-reading guard dies silently
// when the table's formatting changes.
func TestDerivation_TheGeneratedColumnSaysWhatTheMarkersSay(t *testing.T) {
	root := repoRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "docs", "verification", "remote-phaseB-traceability.md"))
	if err != nil {
		t.Fatalf("cannot read the traceability table: %v", err)
	}
	markers := derivationMarkers(t, root)

	rows, derivedInTable := 0, 0
	for _, line := range strings.Split(string(body), "\n") {
		m := traceDerivRow.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		req, cell := m[1], strings.TrimSpace(m[4])
		rows++
		tableSaysDerived := strings.Contains(cell, "DERIVED")
		if tableSaysDerived {
			derivedInTable++
		}
		if tableSaysDerived != markers[req] {
			t.Errorf("%s: the traceability table says derived=%v, the evidence markers say %v. "+
				"The column is generated from those markers, so a disagreement means the table "+
				"was hand-edited or the generator was not re-run: `python3 "+
				"scripts/phaseb-traceability.py > docs/verification/remote-phaseB-traceability.md`",
				req, tableSaysDerived, markers[req])
		}
	}

	if rows == 0 {
		t.Fatal("parsed no requirement rows from the traceability table. The five-column shape " +
			"changed and this guard is asserting over nothing -- which is what it would report " +
			"as success")
	}
	if derivedInTable == 0 {
		t.Fatalf("parsed %d rows and NOT ONE reads DERIVED. Either every marker was lost or the "+
			"derivation cell is not being read; both make this fence vacuous", rows)
	}
}

// TestDerivation_ADerivedVerdictNamesTheMutationThatWasBroken gives the self-reported marker its
// only teeth. "Derived" is a claim about work nobody else witnessed; requiring the mutation in
// the same row means the claim carries the evidence for itself, and a row that cannot name what
// it broke is not a derivation.
func TestDerivation_ADerivedVerdictNamesTheMutationThatWasBroken(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "docs", "verification")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("cannot read %s: %v", dir, err)
	}
	checked := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		_, section, found := strings.Cut(string(body), "## Derivation")
		if !found {
			continue
		}
		for _, line := range strings.Split(section, "\n") {
			if strings.HasPrefix(line, "## ") {
				break
			}
			m := derivMarkerRow.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			verdict := strings.ToUpper(m[2])
			if strings.Contains(verdict, "NOT DERIVED") || !strings.Contains(verdict, "DERIVED") {
				continue
			}
			checked++
			if strings.TrimSpace(m[3]) == "" {
				t.Errorf("%s in %s claims DERIVED and names no mutation. The token may not be "+
					"claimed without saying what was made to fail", m[1], e.Name())
			}
		}
	}
	if checked == 0 {
		t.Fatal("no DERIVED marker rows were parsed at all, so this guard asserted nothing")
	}
}
