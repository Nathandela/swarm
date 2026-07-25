package swarmmobile_test

// FAILING-FIRST (TDD RED, GG-5) guard for PB-BIND-7: the exported surface is pinned so a
// breaking change to the UI contract cannot land silently.
//
// The golden holds TYPES, not parameter names: renaming a parameter is not a breaking
// change for a positional JNI binding, while an added argument, a changed type, a
// dropped method or a new exported field is. Every line is one element of the contract
// the Android app compiles against.
//
// Run with -update-surface to regenerate after a REVIEWED contract change. Regenerating
// is the point at which someone must justify the diff.

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var updateSurface = flag.Bool("update-surface", false, "rewrite testdata/exported_surface.golden")

func TestPBBIND7_ExportedSurfaceMatchesTheGolden(t *testing.T) {
	src := loadFacade(t)

	var lines []string
	for _, s := range exportedSurface(src) {
		lines = append(lines, s.Line())
	}
	got := strings.Join(lines, "\n") + "\n"

	goldenPath := filepath.Join(src.Dir, "testdata", "exported_surface.golden")
	if *updateSurface {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("rewrote %s (%d elements)", goldenPath, len(lines))
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("PB-BIND-7: no pinned surface at %s: %v", goldenPath, err)
	}
	if got == string(want) {
		return
	}

	added, removed := diffLines(strings.Split(strings.TrimRight(string(want), "\n"), "\n"), lines)
	t.Errorf("PB-BIND-7: the exported surface drifted from the pinned contract.\n"+
		"REMOVED (breaks the Android app):\n\t%s\nADDED (new API, must be traced in "+
		"screen_coverage.tsv):\n\t%s\nIf the change is intended and reviewed, re-run with "+
		"-update-surface and justify the diff in the slice evidence.",
		joinOrNone(removed), joinOrNone(added))
}

func diffLines(want, got []string) (added, removed []string) {
	inWant := map[string]bool{}
	for _, l := range want {
		if l != "" {
			inWant[l] = true
		}
	}
	inGot := map[string]bool{}
	for _, l := range got {
		if l != "" {
			inGot[l] = true
		}
	}
	for _, l := range got {
		if l != "" && !inWant[l] {
			added = append(added, l)
		}
	}
	for _, l := range want {
		if l != "" && !inGot[l] {
			removed = append(removed, l)
		}
	}
	return added, removed
}

func joinOrNone(in []string) string {
	if len(in) == 0 {
		return "(none)"
	}
	return strings.Join(in, "\n\t")
}
