package accounts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestClaude289ErasureRetainsOnlyContextAssetAliases(t *testing.T) {
	for _, tamper := range []bool{false, true} {
		t.Run(map[bool]string{false: "known", true: "wrong-target"}[tamper], func(t *testing.T) {
			store, c, a, _, registry := retiringNative(t, ProviderClaude)
			owner := t.TempDir()
			if err := os.Mkdir(filepath.Join(owner, "skills"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(owner, "skills", "retained.md"), []byte("owner asset"), 0600); err != nil {
				t.Fatal(err)
			}
			type asset struct{ Name, Target, SHA256 string }
			context := struct {
				GlobalGeneration string
				SourceProfile    struct{ Path, Canonical string }
				Assets           []asset
			}{}
			context.SourceProfile.Path = owner
			context.SourceProfile.Canonical = owner
			for _, name := range []string{"skills", "agents", "commands", "plugins", "CLAUDE.md"} {
				digest := "absent"
				if name == "skills" {
					digest = hex.EncodeToString(make([]byte, 32))
				}
				context.Assets = append(context.Assets, asset{name, filepath.Join(owner, name), digest})
			}
			raw, _ := json.Marshal(context)
			sum := sha256.Sum256(raw)
			context.GlobalGeneration = hex.EncodeToString(sum[:])
			raw, _ = json.Marshal(context)
			if err := os.WriteFile(filepath.Join(c.ProfilePath, ".swarm-claude-context.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(owner, "skills")
			if tamper {
				target = owner
			}
			if err := os.Symlink(target, filepath.Join(c.ProfilePath, "skills")); err != nil {
				t.Fatal(err)
			}
			registry, err := store.BeginCredentialErasure(registry.Revision, a.ID, 1, "2.1.289")
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.EraseCredentials(registry.Revision, a.ID, 1, ErasureProof{WritersStopped: true})
			if tamper {
				if !errors.Is(err, ErrUnsafePath) {
					t.Fatal("wrong alias target admitted")
				}
				if _, err := os.Stat(filepath.Join(c.ProfilePath, ".credentials.json")); err != nil {
					t.Fatal("refusal removed credentials")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(c.ProfilePath, ".credentials.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("credentials remain")
			}
			if _, err := os.Lstat(filepath.Join(c.ProfilePath, "skills")); err != nil {
				t.Fatal("asset alias removed")
			}
			raw, err = os.ReadFile(filepath.Join(owner, "skills", "retained.md"))
			if err != nil || string(raw) != "owner asset" {
				t.Fatal("erasure followed owner alias")
			}
		})
	}
}

func TestClaude289ErasureHoldsUnknownAuthenticationInventory(t *testing.T) {
	store, c, a, _, registry := retiringNative(t, ProviderClaude)
	if err := os.WriteFile(filepath.Join(c.ProfilePath, "hfi-auth.json"), []byte("synthetic opaque native auth"), 0600); err != nil {
		t.Fatal(err)
	}
	registry, err := store.BeginCredentialErasure(registry.Revision, a.ID, 1, "2.1.289")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.EraseCredentials(registry.Revision, a.ID, 1, ErasureProof{WritersStopped: true}); !errors.Is(err, ErrIneligible) {
		t.Fatal("unknown native auth inventory erased")
	}
	if _, err := os.Stat(filepath.Join(c.ProfilePath, ".credentials.json")); err != nil {
		t.Fatal("refusal removed credentials")
	}
}

func TestClaude289ErasureRetainsOriginalHistoryAlias(t *testing.T) {
	for _, tamper := range []bool{false, true} {
		t.Run(map[bool]string{false: "known", true: "wrong-target"}[tamper], func(t *testing.T) {
			store, c, a, _, registry := retiringNative(t, ProviderClaude)
			owner := t.TempDir()
			key := "-synthetic-project"
			source := filepath.Join(owner, "projects", key)
			if err := os.MkdirAll(source, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(source, "conversation.jsonl"), []byte("original conversation"), 0600); err != nil {
				t.Fatal(err)
			}
			context := struct {
				GlobalGeneration string
				SourceProfile    struct{ Canonical string }
			}{}
			context.SourceProfile.Canonical = owner
			raw, _ := json.Marshal(context)
			sum := sha256.Sum256(raw)
			context.GlobalGeneration = hex.EncodeToString(sum[:])
			raw, _ = json.Marshal(context)
			if err := os.WriteFile(filepath.Join(c.ProfilePath, ".swarm-claude-context.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			history := struct {
				SchemaVersion int
				Aliases       map[string]struct {
					GlobalGeneration, ProjectKey, SourcePath string
					Device, Inode                            uint64
				}
			}{SchemaVersion: 1}
			history.Aliases = map[string]struct {
				GlobalGeneration, ProjectKey, SourcePath string
				Device, Inode                            uint64
			}{key: {context.GlobalGeneration, key, source, 1, 1}}
			raw, _ = json.Marshal(history)
			if err := os.WriteFile(filepath.Join(c.ProfilePath, ".swarm-claude-history.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(c.ProfilePath, "projects"), 0700); err != nil {
				t.Fatal(err)
			}
			target := source
			if tamper {
				target = owner
			}
			if err := os.Symlink(target, filepath.Join(c.ProfilePath, "projects", key)); err != nil {
				t.Fatal(err)
			}
			registry, err := store.BeginCredentialErasure(registry.Revision, a.ID, 1, "2.1.289")
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.EraseCredentials(registry.Revision, a.ID, 1, ErasureProof{WritersStopped: true})
			if tamper {
				_, credentialsErr := os.Stat(filepath.Join(c.ProfilePath, ".credentials.json"))
				if !errors.Is(err, ErrUnsafePath) || credentialsErr != nil {
					t.Fatal("unsafe history alias did not hold credential erasure")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(filepath.Join(c.ProfilePath, "projects", key)); err != nil {
				t.Fatal("history alias removed")
			}
			raw, err = os.ReadFile(filepath.Join(source, "conversation.jsonl"))
			if err != nil || string(raw) != "original conversation" {
				t.Fatal("credential erasure changed original history")
			}
		})
	}
}
