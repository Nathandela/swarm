package codexstock

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func metadataFixture(t *testing.T) (*os.Root, Receipt) {
	t.Helper()
	path := t.TempDir()
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	id, err := ProfileIdentity(root)
	if err != nil {
		t.Fatal(err)
	}
	return root, Receipt{SchemaVersion: 1, NativeVersion: Version, GlobalGeneration: strings.Repeat("a", 64), SourceTarget: filepath.Join(path, "owner", "skills"), SourceSHA256: strings.Repeat("b", 64), RetainedDirectory: RetainedDirectory, StockSHA256: Digest, Profile: id, Stock: Identity{1, 1}}
}

func TestReceiptMetadataRequiresExactCanonicalProfileCustody(t *testing.T) {
	for _, change := range []string{"none", "wrong-profile", "wrong-digest", "duplicate-key", "unknown-key", "nonprivate", "symlink", "hardlink"} {
		t.Run(change, func(t *testing.T) {
			root, r := metadataFixture(t)
			if change == "wrong-profile" {
				r.Profile.Inode++
			}
			if change == "wrong-digest" {
				r.StockSHA256 = strings.Repeat("c", 64)
			}
			raw, _ := json.Marshal(r)
			if change == "duplicate-key" {
				raw = []byte(strings.Replace(string(raw), `"SchemaVersion":1`, `"SchemaVersion":1,"SchemaVersion":1`, 1))
			}
			if change == "unknown-key" {
				raw = append(raw[:len(raw)-1], []byte(`,"Extra":true}`)...)
			}
			if err := root.WriteFile(CustodyFile, raw, 0600); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "nonprivate":
				if err := root.Chmod(CustodyFile, 0664); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := root.Rename(CustodyFile, "other"); err != nil {
					t.Fatal(err)
				}
				if err := root.Symlink("other", CustodyFile); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(filepath.Join(root.Name(), CustodyFile), filepath.Join(root.Name(), "other")); err != nil {
					t.Fatal(err)
				}
			}
			result, err := ReadReceipt(root, CustodyFile)
			if change == "none" {
				if err != nil || result == nil || *result != r {
					t.Fatal("valid custody rejected", err)
				}
			} else if err == nil {
				t.Fatal("unsafe receipt accepted")
			}
		})
	}
}

func TestOuterGuardRecognizesOnlyBoundedKnownSchemas(t *testing.T) {
	for _, shape := range []string{"stock", "large-legacy", "unknown-schema", "oversized-stock", "malformed"} {
		t.Run(shape, func(t *testing.T) {
			root, r := metadataFixture(t)
			raw, _ := json.Marshal(OuterGuard{SchemaVersion: 2, StockSkills: r})
			switch shape {
			case "large-legacy":
				raw = []byte(`{"SchemaVersion":1,"Preserved":"` + strings.Repeat("x", 32768) + `"}`)
			case "unknown-schema":
				raw = []byte(`{"SchemaVersion":3}`)
			case "oversized-stock":
				raw = append(raw, []byte(strings.Repeat(" ", 32768))...)
			case "malformed":
				raw = []byte(`{"SchemaVersion":`)
			}
			if err := root.WriteFile(ContextUpdateFile, raw, 0600); err != nil {
				t.Fatal(err)
			}
			got, err := ReadOuterGuard(root)
			switch shape {
			case "stock":
				if err != nil || got == nil || *got != r {
					t.Fatal("stock sentinel rejected", err)
				}
			case "large-legacy":
				if err != nil || got != nil {
					t.Fatal("existing bounded legacy journal rejected", err)
				}
			default:
				if err == nil {
					t.Fatal("unknown or invalid guard accepted")
				}
			}
		})
	}
}

func TestStockOwnershipRejectsForeignOwner(t *testing.T) {
	path := t.TempDir()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat := *info.Sys().(*syscall.Stat_t)
	stat.Uid = uint32(os.Getuid()) + 1
	if _, ok := identity(stockForeignInfo{info, &stat}); ok {
		t.Fatal("foreign owner admitted")
	}
}

type stockForeignInfo struct {
	os.FileInfo
	stat *syscall.Stat_t
}

func (f stockForeignInfo) Sys() any { return f.stat }
