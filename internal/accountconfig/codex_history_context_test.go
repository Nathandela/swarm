package accountconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCodexHistoryAliasSharesNativeRootWithoutReplacingPrivateHistory(t *testing.T) {
	f := codexContextFixture(t)
	c, err := PrepareCodexContext(f.candidate, f.cwd, f.env, codexContextLease)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareCodexHistoryAlias(c, f.candidate, false, codexContextLease); err == nil {
		t.Fatal("readonly preparation created absent source")
	}
	if _, err := os.Stat(filepath.Join(f.source, "sessions")); !os.IsNotExist(err) {
		t.Fatal("readonly preparation changed history")
	}
	proof, err := PrepareCodexHistoryAlias(c, f.candidate, true, codexContextLease)
	if err != nil {
		t.Fatal(err)
	}
	if !ValidCodexHistoryAlias(c, proof) {
		t.Fatal("intrinsic history proof invalid")
	}
	path := filepath.Join(f.candidate, "sessions", "synthetic.jsonl")
	put(t, path, "synthetic native rollout")
	raw, err := os.ReadFile(filepath.Join(f.source, "sessions", "synthetic.jsonl"))
	if err != nil || string(raw) != "synthetic native rollout" {
		t.Fatal("private native writer lost original history")
	}
	again, err := PrepareCodexHistoryAlias(c, f.candidate, false, codexContextLease)
	if err != nil || again != proof {
		t.Fatalf("existing history alias changed: %v", err)
	}
	second := filepath.Join(filepath.Dir(f.candidate), "second-account")
	if err := os.Mkdir(second, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, err := PrepareCodexContext(second, f.cwd, f.env, codexContextLease)
	if err != nil {
		t.Fatal(err)
	}
	other, err := PrepareCodexHistoryAlias(ctx, second, false, codexContextLease)
	if err != nil || other != proof {
		t.Fatalf("rotation lost original native root: %v", err)
	}
	if err := os.Rename(filepath.Join(f.source, "sessions"), filepath.Join(f.source, "sessions-old")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(f.source, "sessions"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := RevalidateCodexHistoryAlias(c, f.candidate, proof); err == nil {
		t.Fatal("replacement history root admitted")
	}
}
func TestCodexHistoryAliasRefusesExistingPrivateHistoryAndLeaseDenial(t *testing.T) {
	for _, kind := range []string{"directory", "foreign-alias", "source-alias", "lease-denied", "marker"} {
		t.Run(kind, func(t *testing.T) {
			f := codexContextFixture(t)
			c, err := PrepareCodexContext(f.candidate, f.cwd, f.env, codexContextLease)
			if err != nil {
				t.Fatal(err)
			}
			lease := codexContextLease
			switch kind {
			case "directory":
				put(t, filepath.Join(f.candidate, "sessions", "retained.jsonl"), "retained")
			case "foreign-alias":
				if err := os.Symlink(f.cwd, filepath.Join(f.candidate, "sessions")); err != nil {
					t.Fatal(err)
				}
			case "source-alias":
				if err := os.Symlink(f.cwd, filepath.Join(f.source, "sessions")); err != nil {
					t.Fatal(err)
				}
			case "lease-denied":
				lease = func(string, func() error) error { return Conflict("busy") }
			case "marker":
				put(t, filepath.Join(f.candidate, CodexHistoryMarker), `{}`)
			}
			if _, err := PrepareCodexHistoryAlias(c, f.candidate, true, lease); err == nil {
				t.Fatal("unsafe history admitted")
			}
			if kind == "directory" {
				raw, err := os.ReadFile(filepath.Join(f.candidate, "sessions", "retained.jsonl"))
				if err != nil || string(raw) != "retained" {
					t.Fatal("refusal modified existing private history")
				}
			}
		})
	}
}
