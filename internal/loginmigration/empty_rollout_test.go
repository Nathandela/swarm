package loginmigration

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexUnreferencedEmptyRolloutPreservesSource(t *testing.T) {
	f := newFixture(t)
	root := rolloutFixture(t, f.home, conversation, "", nil)
	empty := rolloutFixture(t, f.home, otherConversation, "", nil)
	put(t, empty, "")
	before, err := os.Stat(empty)
	if err != nil {
		t.Fatal(err)
	}
	if err = prepareContext(f.state, "codex", []contextSource{{Home: f.home, Conversation: conversation}}); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(empty)
	if err != nil || !os.SameFile(before, after) || after.Size() != 0 || before.Mode() != after.Mode() {
		t.Fatal("unreferenced empty source changed")
	}
	for _, path := range []string{root, empty} {
		rel, err := filepath.Rel(f.home, path)
		if err != nil {
			t.Fatal(err)
		}
		_, err = os.Stat(filepath.Join(providerHome(f.state, "codex"), rel))
		if path == root && err != nil || path == empty && !errors.Is(err, os.ErrNotExist) {
			t.Fatal("empty placeholder became managed history or required history was omitted")
		}
	}
}

func TestCodexEmptyRolloutNeverSatisfiesRequiredHistory(t *testing.T) {
	for _, kind := range []string{"thread", "base"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			path := rolloutFixture(t, f.home, conversation, "", nil)
			if kind == "base" {
				rolloutFixture(t, f.home, conversation, "", map[string]any{"history_mode": "paginated", "history_base": map[string]any{"thread_id": physicalConversation, "end_ordinal_exclusive": 1, "end_byte_offset": 1}})
				path = rolloutFixture(t, f.home, otherConversation, physicalConversation, nil)
			}
			put(t, path, "")
			if _, err := codexHistoryClosure([]contextSource{{Home: f.home}}, map[string]bool{conversation: true}); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("required empty history accepted: %v", err)
			}
		})
	}
}

func TestCodexEmptyRolloutKeepsSourceValidation(t *testing.T) {
	for _, kind := range []string{"nonempty-malformed", "oversized", "compressed-empty", "hardlink", "symlink", "writable"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			rolloutFixture(t, f.home, conversation, "", nil)
			path := rolloutFixture(t, f.home, otherConversation, "", nil)
			put(t, path, "")
			var err error
			switch kind {
			case "nonempty-malformed":
				put(t, path, "\n")
			case "oversized":
				put(t, path, strings.Repeat("x", maxRolloutHeader+1))
			case "compressed-empty":
				err = os.Rename(path, path+".zst")
			case "hardlink":
				err = os.Link(path, filepath.Join(f.home, "extra-link"))
			case "symlink":
				target := filepath.Join(f.home, "empty-target")
				if err = os.Rename(path, target); err == nil {
					err = os.Symlink(target, path)
				}
			case "writable":
				err = os.Chmod(path, 0660)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = codexHistoryClosure([]contextSource{{Home: f.home}}, map[string]bool{conversation: true}); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("unsafe source accepted: %v", err)
			}
		})
	}
}
