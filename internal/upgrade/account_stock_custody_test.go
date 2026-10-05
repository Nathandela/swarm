package upgrade

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/codexstock"
)

// An unreferenced profile can retain an interrupted install before any immutable
// projection or discussion exists. Registry-only compatibility checks miss it.
func stockCustodyFixture(t *testing.T, marker string) (string, string, codexstock.Receipt) {
	t.Helper()
	state := t.TempDir()
	if err := os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := accounts.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(state, "accounts", "profiles", strings.Repeat("a", 32))
	if err := os.Mkdir(profile, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(profile)
	if err != nil {
		t.Fatal(err)
	}
	id, err := codexstock.ProfileIdentity(root)
	_ = root.Close()
	if err != nil {
		t.Fatal(err)
	}
	stockName := "skills"
	if marker == codexstock.CustodyFile {
		stockName = codexstock.RetainedDirectory
	}
	stockPath := filepath.Join(profile, stockName)
	if err := os.Mkdir(stockPath, 0775); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(stockPath)
	if err != nil {
		t.Fatal(err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	r := codexstock.Receipt{SchemaVersion: 1, NativeVersion: codexstock.Version,
		GlobalGeneration: strings.Repeat("b", 64), SourceTarget: filepath.Join(state, "missing-owner", "skills"),
		SourceSHA256: strings.Repeat("c", 64), RetainedDirectory: codexstock.RetainedDirectory,
		StockSHA256: codexstock.Digest, Profile: id, Stock: codexstock.Identity{Device: uint64(stat.Dev), Inode: stat.Ino}}
	if marker != codexstock.CustodyFile {
		writeStockOuterGuard(t, profile, r)
	}
	if marker != codexstock.ContextUpdateFile {
		writeStockReceipt(t, profile, marker, r)
	}
	return state, profile, r
}

func writeStockOuterGuard(t *testing.T, profile string, r codexstock.Receipt) {
	t.Helper()
	raw, err := json.Marshal(codexstock.OuterGuard{SchemaVersion: 2, StockSkills: r})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, codexstock.ContextUpdateFile), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func writeStockReceipt(t *testing.T, profile, marker string, r codexstock.Receipt) {
	t.Helper()
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, marker), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestAccountStockCustodyGuardWithoutProjectionOrRegistryReference(t *testing.T) {
	for _, marker := range []string{codexstock.ContextUpdateFile, codexstock.PendingFile, codexstock.CustodyFile} {
		t.Run(marker, func(t *testing.T) {
			state, _, _ := stockCustodyFixture(t, marker)
			if err := accountStateGuard(state, boundaryCard(3)); err == nil {
				t.Fatal("old reader accepted stock custody before projection publication")
			}
			if err := accountStateGuard(state, boundaryCard(4)); err != nil {
				t.Fatal("current reader read mutable owner assets or refused valid custody", err)
			}
		})
	}
}

func TestAccountStockCustodyGuardFailsClosed(t *testing.T) {
	for _, change := range []string{"malformed", "future-schema", "profile-identity", "symlink", "hardlink", "public", "conflicting-receipts", "unreceipted-retained", "unsafe-profile", "profile-alias", "unknown-profile-name", "missing-outer-guard", "missing-stock", "missing-completed-stock", "retained-alias", "outer-future-schema"} {
		t.Run(change, func(t *testing.T) {
			marker := codexstock.PendingFile
			if change == "missing-completed-stock" || change == "retained-alias" {
				marker = codexstock.CustodyFile
			}
			state, profile, r := stockCustodyFixture(t, marker)
			path := filepath.Join(profile, codexstock.PendingFile)
			switch change {
			case "malformed":
				if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			case "future-schema":
				r.SchemaVersion++
				writeStockReceipt(t, profile, codexstock.PendingFile, r)
			case "profile-identity":
				r.Profile.Inode++
				writeStockReceipt(t, profile, codexstock.PendingFile, r)
			case "symlink":
				outside := filepath.Join(state, "receipt")
				if err := os.Rename(path, outside); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, path); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(path, filepath.Join(state, "receipt")); err != nil {
					t.Fatal(err)
				}
			case "public":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "conflicting-receipts":
				r.SourceSHA256 = strings.Repeat("d", 64)
				writeStockReceipt(t, profile, codexstock.CustodyFile, r)
			case "unreceipted-retained":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(profile, codexstock.RetainedDirectory), 0775); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(filepath.Join(profile, codexstock.ContextUpdateFile)); err != nil {
					t.Fatal(err)
				}
			case "missing-outer-guard":
				if err := os.Remove(filepath.Join(profile, codexstock.ContextUpdateFile)); err != nil {
					t.Fatal(err)
				}
			case "missing-stock":
				if err := os.Remove(filepath.Join(profile, "skills")); err != nil {
					t.Fatal(err)
				}
			case "missing-completed-stock":
				if err := os.Remove(filepath.Join(profile, codexstock.RetainedDirectory)); err != nil {
					t.Fatal(err)
				}
			case "retained-alias":
				if err := os.Remove(filepath.Join(profile, codexstock.RetainedDirectory)); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(state, filepath.Join(profile, codexstock.RetainedDirectory)); err != nil {
					t.Fatal(err)
				}
			case "outer-future-schema":
				if err := os.WriteFile(filepath.Join(profile, codexstock.ContextUpdateFile), []byte(`{"SchemaVersion":3}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "unsafe-profile":
				if err := os.Chmod(profile, 0755); err != nil {
					t.Fatal(err)
				}
			case "profile-alias":
				outside := filepath.Join(state, "profile")
				if err := os.Rename(profile, outside); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, profile); err != nil {
					t.Fatal(err)
				}
			case "unknown-profile-name":
				if err := os.Rename(profile, filepath.Join(filepath.Dir(profile), "unknown")); err != nil {
					t.Fatal(err)
				}
			}
			for _, version := range []int{3, 4} {
				if err := accountStateGuard(state, boundaryCard(version)); err == nil {
					t.Fatal("unverifiable stock custody admitted", version)
				}
			}
		})
	}
}

func TestAccountStockCustodyGuardAcceptsInterruptedMetadataPhases(t *testing.T) {
	for _, phase := range []string{"renamed", "completed-before-cleanup", "global-refresh", "large-global-refresh"} {
		t.Run(phase, func(t *testing.T) {
			state, profile, r := stockCustodyFixture(t, codexstock.PendingFile)
			switch phase {
			case "renamed", "completed-before-cleanup":
				if err := os.Rename(filepath.Join(profile, "skills"), filepath.Join(profile, codexstock.RetainedDirectory)); err != nil {
					t.Fatal(err)
				}
				if phase == "completed-before-cleanup" {
					writeStockReceipt(t, profile, codexstock.CustodyFile, r)
				}
			case "global-refresh", "large-global-refresh":
				// The compatibility scan discriminates the existing global journal
				// header; the configuration reader separately validates its body.
				padding := ""
				if phase == "large-global-refresh" {
					padding = strings.Repeat("x", 20000)
				}
				raw, err := json.Marshal(struct {
					SchemaVersion int
					Padding       string
				}{1, padding})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(profile, codexstock.ContextUpdateFile), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := accountStateGuard(state, boundaryCard(3)); err == nil {
				t.Fatal("old reader admitted interrupted custody")
			}
			if err := accountStateGuard(state, boundaryCard(4)); err != nil {
				t.Fatal("current reader refused a known interrupted metadata phase", err)
			}
		})
	}
}

func TestAccountStockCustodyGuardKeepsLegacyConfigurationRequirement(t *testing.T) {
	state, profile, _ := stockCustodyFixture(t, codexstock.PendingFile)
	if err := os.Remove(filepath.Join(profile, codexstock.PendingFile)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, codexstock.ContextUpdateFile), []byte(`{"SchemaVersion":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := accountStateGuard(state, boundaryCard(3)); err != nil {
		t.Fatal("legacy global journal acquired a stock requirement", err)
	}
}
