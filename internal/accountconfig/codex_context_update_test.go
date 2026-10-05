package accountconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCodexContextRefreshInterruptedCommitRetriesWithoutLosingHistory(t *testing.T) {
	for _, stage := range []string{"intent", "config", "history", "context"} {
		t.Run(stage, func(t *testing.T) {
			f := codexContextFixture(t)
			old, err := PrepareCodexContext(f.candidate, f.cwd, f.env, codexContextLease)
			if err != nil {
				t.Fatal(err)
			}
			history, err := PrepareCodexHistoryAlias(old, f.candidate, true, codexContextLease)
			if err != nil {
				t.Fatal(err)
			}
			put(t, filepath.Join(f.source, "config.toml"), "model_reasoning_effort=\"medium\"\n")
			nextProfile := filepath.Join(filepath.Dir(f.candidate), "next-cohort")
			if err := os.Mkdir(nextProfile, 0700); err != nil {
				t.Fatal(err)
			}
			next, err := PrepareCodexContext(nextProfile, f.cwd, f.env, codexContextLease)
			if err != nil {
				t.Fatal(err)
			}
			intent, _ := json.Marshal(struct {
				SchemaVersion  int
				Previous, Next CodexContext
			}{1, old, next})
			put(t, filepath.Join(f.candidate, ".swarm-codex-context-update.json"), string(intent))
			if stage != "intent" {
				raw, err := os.ReadFile(filepath.Join(nextProfile, "config.toml"))
				if err != nil {
					t.Fatal(err)
				}
				put(t, filepath.Join(f.candidate, "config.toml"), string(raw))
			}
			if stage == "history" || stage == "context" {
				history.GlobalGeneration = next.GlobalGeneration
				raw, _ := json.Marshal(history)
				put(t, filepath.Join(f.candidate, CodexHistoryMarker), string(raw))
			}
			if stage == "context" {
				raw, _ := json.Marshal(next)
				put(t, filepath.Join(f.candidate, CodexContextMarker), string(raw))
			}
			if _, err := PrepareCodexContext(f.candidate, f.cwd, f.env, codexContextLease); err == nil {
				t.Fatal("pending refresh accepted without owner authority")
			}
			got, err := PrepareCodexContext(f.candidate, f.cwd, f.env, codexContextLease, true)
			if err != nil {
				t.Fatalf("known interrupted %s stage wedged: %v", stage, err)
			}
			history.GlobalGeneration = got.GlobalGeneration
			if err := RevalidateCodexHistoryAlias(got, f.candidate, history); err != nil {
				t.Fatalf("history not committed with context: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(f.candidate, ".swarm-codex-context-update.json")); !os.IsNotExist(err) {
				t.Fatal("durable intent remains after complete commit")
			}
		})
	}
}

func TestCodexContextPendingRefreshHoldsUnrelatedWritesAndOwnerDrift(t *testing.T) {
	for _, drift := range []string{"private-config", "owner-config", "foreign-alias"} {
		t.Run(drift, func(t *testing.T) {
			f := codexContextFixture(t)
			old, err := PrepareCodexContext(f.candidate, f.cwd, f.env, codexContextLease)
			if err != nil {
				t.Fatal(err)
			}
			put(t, filepath.Join(f.source, "config.toml"), "model_reasoning_effort=\"medium\"\n")
			nextProfile := filepath.Join(filepath.Dir(f.candidate), "next-cohort")
			if err := os.Mkdir(nextProfile, 0700); err != nil {
				t.Fatal(err)
			}
			next, err := PrepareCodexContext(nextProfile, f.cwd, f.env, codexContextLease)
			if err != nil {
				t.Fatal(err)
			}
			if err := writeCodexContextUpdate(f.candidate, codexContextUpdate{1, old, next}); err != nil {
				t.Fatal(err)
			}
			switch drift {
			case "private-config":
				put(t, filepath.Join(f.candidate, "config.toml"), "model_reasoning_effort=\"low\"\n")
			case "owner-config":
				put(t, filepath.Join(f.source, "config.toml"), "model_reasoning_effort=\"low\"\n")
			case "foreign-alias":
				if err := os.Symlink(f.cwd, filepath.Join(f.candidate, "skills")); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(filepath.Join(f.candidate, "config.toml"))
			if _, err := PrepareCodexContext(f.candidate, f.cwd, f.env, codexContextLease, true); err == nil {
				t.Fatal("unknown partial state overwritten")
			}
			after, _ := os.ReadFile(filepath.Join(f.candidate, "config.toml"))
			if string(before) != string(after) {
				t.Fatal("failed repair changed config")
			}
			if _, err := os.Lstat(filepath.Join(f.candidate, CodexContextUpdateMarker)); err != nil {
				t.Fatal("uncertain intent discarded")
			}
		})
	}
}

func TestCodexContextRefreshAssetDeletionIsRecoverable(t *testing.T) {
	for _, removed := range []bool{false, true} {
		f := codexContextFixture(t)
		put(t, filepath.Join(f.source, "AGENTS.md"), "owner instructions")
		old, err := PrepareCodexContext(f.candidate, f.cwd, f.env, codexContextLease)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(f.source, "AGENTS.md")); err != nil {
			t.Fatal(err)
		}
		nextProfile := filepath.Join(filepath.Dir(f.candidate), "next-cohort")
		if err := os.Mkdir(nextProfile, 0700); err != nil {
			t.Fatal(err)
		}
		next, err := PrepareCodexContext(nextProfile, f.cwd, f.env, codexContextLease)
		if err != nil {
			t.Fatal(err)
		}
		if err := writeCodexContextUpdate(f.candidate, codexContextUpdate{1, old, next}); err != nil {
			t.Fatal(err)
		}
		if removed {
			if err := os.Remove(filepath.Join(f.candidate, "AGENTS.md")); err != nil {
				t.Fatal(err)
			}
		}
		got, err := PrepareCodexContext(f.candidate, f.cwd, f.env, codexContextLease, true)
		if err != nil {
			t.Fatal(err)
		}
		if err := RevalidateCodexContext(got, f.candidate); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(filepath.Join(f.candidate, "AGENTS.md")); !os.IsNotExist(err) {
			t.Fatal("deleted owner asset still aliased")
		}
	}
}
