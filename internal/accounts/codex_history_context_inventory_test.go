package accounts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestCodexRetirementPreservesExactSharedHistoryCustody(t *testing.T) {
	for _, change := range []string{"none", "target", "generation", "sessions-inode", "source-root", "pending"} {
		t.Run(change, func(t *testing.T) {
			store, c, account, _, registry := retiringNative(t, ProviderCodex)
			owner := filepath.Join(t.TempDir(), "owner")
			source := filepath.Join(owner, "sessions")
			if err := os.MkdirAll(source, 0700); err != nil {
				t.Fatal(err)
			}
			transcript := filepath.Join(source, "retained.jsonl")
			if err := os.WriteFile(transcript, []byte("retained synthetic native history"), 0600); err != nil {
				t.Fatal(err)
			}
			rootInfo, _ := os.Lstat(owner)
			rootStat := rootInfo.Sys().(*syscall.Stat_t)
			rootIdentity, _ := json.Marshal(struct {
				Path          string
				Device, Inode uint64
				Owner         uint32
			}{owner, uint64(rootStat.Dev), rootStat.Ino, rootStat.Uid})
			sum := sha256.Sum256(rootIdentity)
			context := struct {
				GlobalGeneration, SourceDirectorySHA256 string
				SourceProfile                           struct{ Canonical string }
			}{SourceDirectorySHA256: hex.EncodeToString(sum[:])}
			context.SourceProfile.Canonical = owner
			raw, _ := json.Marshal(context)
			sum = sha256.Sum256(raw)
			context.GlobalGeneration = hex.EncodeToString(sum[:])
			raw, _ = json.Marshal(context)
			if err := os.WriteFile(filepath.Join(c.ProfilePath, ".swarm-codex-context.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			info, _ := os.Lstat(source)
			stat := info.Sys().(*syscall.Stat_t)
			proof := struct {
				GlobalGeneration, SourcePath string
				Device, Inode                uint64
			}{context.GlobalGeneration, source, uint64(stat.Dev), stat.Ino}
			target := source
			switch change {
			case "target":
				target = owner
			case "generation":
				proof.GlobalGeneration = hex.EncodeToString(make([]byte, 32))
			case "sessions-inode":
				if err := os.Rename(source, source+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(source, 0700); err != nil {
					t.Fatal(err)
				}
				transcript = filepath.Join(source+"-old", "retained.jsonl")
			case "source-root":
				if err := os.Rename(owner, owner+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(owner, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(filepath.Join(owner+"-old", "sessions"), source); err != nil {
					t.Fatal(err)
				}
			case "pending":
				if err := os.WriteFile(filepath.Join(c.ProfilePath, ".swarm-codex-context-update.json"), []byte("pending"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			raw, _ = json.Marshal(proof)
			if err := os.WriteFile(filepath.Join(c.ProfilePath, ".swarm-codex-history.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(c.ProfilePath, "sessions")); err != nil {
				t.Fatal(err)
			}
			registry, err := store.BeginCredentialErasure(registry.Revision, account.ID, 1, "0.160.0")
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.EraseCredentials(registry.Revision, account.ID, 1, ErasureProof{WritersStopped: true})
			if change == "none" {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Lstat(filepath.Join(c.ProfilePath, "auth.json")); !os.IsNotExist(err) {
					t.Fatal("credential erasure incomplete")
				}
			} else {
				if err == nil {
					t.Fatal("changed history custody admitted credential erasure")
				}
				if _, err := os.Lstat(filepath.Join(c.ProfilePath, "auth.json")); err != nil {
					t.Fatal("custody hold deleted credentials")
				}
			}
			if raw, err := os.ReadFile(transcript); err != nil || string(raw) != "retained synthetic native history" {
				t.Fatal("erasure changed original history")
			}
			if _, err := os.Lstat(filepath.Join(c.ProfilePath, "sessions")); err != nil {
				t.Fatal("erasure removed history alias")
			}
		})
	}
}
