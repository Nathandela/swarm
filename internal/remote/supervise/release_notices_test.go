package supervise

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestGoreleaserTUIArchiveCarriesCopiedLicenses(t *testing.T) {
	root := repoRoot(t)
	archives := scanListBlock(t, filepath.Join(root, ".goreleaser.yaml"), "archives")
	found := false
	for _, archive := range archives {
		if archive.name == "swarm" {
			found = true
			if !slices.Contains(archive.lists["files"], "THIRD_PARTY_NOTICES") {
				t.Fatal("swarm's binary/Homebrew archive omits THIRD_PARTY_NOTICES")
			}
		}
	}
	if !found {
		t.Fatal("swarm archive missing")
	}
	notice, err := os.ReadFile(filepath.Join(root, "THIRD_PARTY_NOTICES"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"internal/thirdparty/bubbletea/LICENSE", "internal/testutil/teatest/LICENSE"} {
		license, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(notice), string(license)) {
			t.Errorf("shipped notice omits the complete %s", path)
		}
	}
}
