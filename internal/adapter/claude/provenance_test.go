package claude

// TestFixtureProvenance_MatchesDeclaredSource is BEAD nq0q's fence: every fixture under
// testdata/interaction/ that PROVENANCE.md marks "copied" must stay byte-identical to the S-B
// source it declares. A fixture marked "reconstructed" is exempt -- PROVENANCE.md says plainly
// there is no verbatim recording behind it, so there is nothing to diff against.
//
// The check is factored into checkFixtureProvenance so the RED demonstration (recorded in
// docs/verification/a1b-claude-producer.md) can point it at a corrupted TEMP COPY of the corpus
// without ever touching a tracked fixture.

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// provenanceRowRE reads PROVENANCE.md's table rows: file, source and marker cells are each
// backtick- or bare-quoted; the trailing "What it grounds" cell is free text and is not captured.
var provenanceRowRE = regexp.MustCompile("(?m)^\\| `([^`]+)` \\| `([^`]+)` \\| (copied|reconstructed) \\|")

// checkFixtureProvenance parses dir/PROVENANCE.md and, for every row marked "copied", asserts
// dir/<file> is sha256-identical to repoRoot/<source>. repoRoot is resolved independently of dir
// so this can run against a throwaway copy of the corpus while sources still read from the real
// tree.
func checkFixtureProvenance(t *testing.T, dir, repoRoot string) {
	t.Helper()
	doc, err := os.ReadFile(filepath.Join(dir, "PROVENANCE.md"))
	if err != nil {
		t.Fatalf("reading PROVENANCE.md: %v", err)
	}
	rows := provenanceRowRE.FindAllStringSubmatch(string(doc), -1)
	if len(rows) == 0 {
		t.Fatalf("no provenance rows matched in %s; the table format moved and this fence is blind", dir)
	}
	for _, row := range rows {
		file, source, marker := row[1], row[2], row[3]
		t.Run(file, func(t *testing.T) {
			if marker == "reconstructed" {
				t.Skipf("PROVENANCE.md marks %s reconstructed; no verbatim source to diff against", file)
			}
			got, err := os.ReadFile(filepath.Join(dir, file))
			if err != nil {
				t.Fatalf("reading fixture: %v", err)
			}
			want, err := os.ReadFile(filepath.Join(repoRoot, source))
			if err != nil {
				t.Fatalf("reading declared source %s: %v", source, err)
			}
			gotSum, wantSum := sha256.Sum256(got), sha256.Sum256(want)
			if gotSum != wantSum {
				t.Errorf("sha256 = %x, declared source %s sha256 = %x; PROVENANCE.md marks this a "+
					"verbatim copy and the bytes have diverged", gotSum, source, wantSum)
			}
		})
	}
}

func TestFixtureProvenance_MatchesDeclaredSource(t *testing.T) {
	checkFixtureProvenance(t, filepath.Join("testdata", "interaction"), filepath.Join("..", "..", ".."))
}
