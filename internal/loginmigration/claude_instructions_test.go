package loginmigration

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestClaudeContextPreservesOwnerInstructionsAndNestedRules(t *testing.T) {
	f := newFixture(t)
	instructions := map[string]string{
		"CLAUDE.md":                  "Keep my user instructions. See @rules/workflows/testing.md\n",
		"rules/preferences.md":       "Use my preferred formatting.\n",
		"rules/workflows/testing.md": "---\npaths:\n  - \"**/*.go\"\n---\nRun focused Go tests.\n",
	}
	for relative, content := range instructions {
		put(t, filepath.Join(f.home, relative), content)
	}
	if err := prepareContext(f.state, "claude", []contextSource{{Home: f.home}}); err != nil {
		t.Fatal(err)
	}
	for relative, want := range instructions {
		for _, home := range []string{f.home, providerHome(f.state, "claude")} {
			raw, err := os.ReadFile(filepath.Join(home, relative))
			if err != nil {
				t.Fatal(err)
			}
			if string(raw) != want {
				t.Fatalf("instruction content changed at %s", relative)
			}
		}
	}
}

func TestClaudeInstructionsStayOutOfHistoryOnlySources(t *testing.T) {
	f := newFixture(t)
	put(t, filepath.Join(f.home, "CLAUDE.md"), "old copied-profile instructions must not override owner")
	put(t, filepath.Join(f.home, "rules", "preferences.md"), "old copied-profile rule")
	plan, err := planContext(f.state, "claude", []contextSource{{Home: f.home, HistoryOnly: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Files) != 0 {
		t.Fatalf("historical profile contributed instruction authority: %+v", plan.Files)
	}
}

func TestClaudeContextRefusesConflictingOwnerInstructions(t *testing.T) {
	for _, relative := range []string{"CLAUDE.md", "rules/workflows/testing.md"} {
		t.Run(relative, func(t *testing.T) {
			f := newFixture(t)
			other := filepath.Join(filepath.Dir(f.state), "other-claude-native")
			put(t, filepath.Join(f.home, relative), "first owner instruction")
			put(t, filepath.Join(other, relative), "conflicting owner instruction")
			if err := prepareContext(f.state, "claude", []contextSource{{Home: f.home}, {Home: other}}); !errors.Is(err, ErrHistoryConflict) {
				t.Fatalf("instruction conflict accepted: %v", err)
			}
			if _, err := os.Stat(filepath.Join(f.state, "logins")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("conflict mutated destination")
			}
		})
	}
}

func TestClaudeInstructionSourcesKeepNativeContextSafetyChecks(t *testing.T) {
	for _, relative := range []string{"CLAUDE.md", "rules/workflows/testing.md"} {
		for _, kind := range []string{"symlink", "hardlink", "writable"} {
			t.Run(relative+"/"+kind, func(t *testing.T) {
				f := newFixture(t)
				path := filepath.Join(f.home, relative)
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(filepath.Dir(f.state), "unrelated-instruction-source")
				put(t, target, "unsafe instruction source")
				var err error
				switch kind {
				case "symlink":
					err = os.Symlink(target, path)
				case "hardlink":
					err = os.Link(target, path)
				case "writable":
					put(t, path, "unsafe writable instructions")
					err = os.Chmod(path, 0666)
				}
				if err != nil {
					t.Fatal(err)
				}
				if err = prepareContext(f.state, "claude", []contextSource{{Home: f.home}}); !errors.Is(err, ErrUnsupported) {
					t.Fatalf("unsafe instruction source accepted: %v", err)
				}
				if _, err = os.Stat(filepath.Join(f.state, "logins")); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("unsafe instruction refusal mutated destination")
				}
			})
		}
	}
}
