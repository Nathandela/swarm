package accounts

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/codexstock"
)

func inventoryStockFixture(t *testing.T, destination string) {
	t.Helper()
	f, err := os.Open("../codexstock/testdata/skills-0.160.0.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	z, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = z.Close() }()
	r := tar.NewReader(z)
	for {
		h, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil || !filepath.IsLocal(h.Name) {
			t.Fatal("invalid fixture")
		}
		path := filepath.Join(destination, h.Name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0775); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0775); err != nil {
				t.Fatal(err)
			}
		case tar.TypeReg:
			data, err := io.ReadAll(r)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0664); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatal("unsupported fixture entry")
		}
	}
}

func TestCodexCredentialErasurePreservesStockCustody(t *testing.T) {
	for _, change := range []string{"none", "pending", "outer-only", "missing-receipt", "changed-stock", "missing-stock", "wrong-alias", "missing-context"} {
		t.Run(change, func(t *testing.T) {
			store, c, account, _, registry := retiringNative(t, ProviderCodex)
			owner := t.TempDir()
			source := filepath.Join(owner, "skills")
			if err := os.Mkdir(source, 0700); err != nil {
				t.Fatal(err)
			}
			ownerFile := filepath.Join(source, "retained.md")
			if err := os.WriteFile(ownerFile, []byte("owner asset"), 0600); err != nil {
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
			for _, name := range []string{"skills", "rules", "hooks.json", "AGENTS.md", "plugins", ".tmp/marketplaces"} {
				digest := "absent"
				if name == "skills" {
					digest = strings.Repeat("a", 64)
				}
				context.Assets = append(context.Assets, asset{name, filepath.Join(owner, name), digest})
			}
			raw, _ := json.Marshal(context)
			sum := sha256.Sum256(raw)
			context.GlobalGeneration = hex.EncodeToString(sum[:])
			raw, _ = json.Marshal(context)
			marker := filepath.Join(c.ProfilePath, ".swarm-codex-context.json")
			if err := os.WriteFile(marker, raw, 0600); err != nil {
				t.Fatal(err)
			}
			retained := filepath.Join(c.ProfilePath, codexstock.RetainedDirectory)
			inventoryStockFixture(t, retained)
			root, err := os.OpenRoot(c.ProfilePath)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = root.Close() }()
			profileID, err := codexstock.ProfileIdentity(root)
			if err != nil {
				t.Fatal(err)
			}
			stockID, err := codexstock.Validate(root, codexstock.RetainedDirectory)
			if err != nil {
				t.Fatal(err)
			}
			receipt := codexstock.Receipt{SchemaVersion: 1, NativeVersion: codexstock.Version, GlobalGeneration: context.GlobalGeneration, SourceTarget: source, SourceSHA256: strings.Repeat("a", 64), RetainedDirectory: codexstock.RetainedDirectory, StockSHA256: codexstock.Digest, Profile: profileID, Stock: stockID}
			raw, _ = json.Marshal(receipt)
			receiptPath := filepath.Join(c.ProfilePath, codexstock.CustodyFile)
			if err := os.WriteFile(receiptPath, raw, 0600); err != nil {
				t.Fatal(err)
			}
			target := source
			if change == "wrong-alias" {
				target = owner
			}
			if err := os.Symlink(target, filepath.Join(c.ProfilePath, "skills")); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "pending":
				if err := os.WriteFile(filepath.Join(c.ProfilePath, codexstock.PendingFile), raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "outer-only":
				raw, _ = json.Marshal(codexstock.OuterGuard{SchemaVersion: 2, StockSkills: receipt})
				if err := os.WriteFile(filepath.Join(c.ProfilePath, codexstock.ContextUpdateFile), raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "missing-receipt":
				if err := os.Remove(receiptPath); err != nil {
					t.Fatal(err)
				}
			case "changed-stock":
				if err := os.WriteFile(filepath.Join(retained, ".system", ".codex-system-skills.marker"), []byte("altered"), 0664); err != nil {
					t.Fatal(err)
				}
			case "missing-stock":
				if err := os.Rename(retained, retained+"-held"); err != nil {
					t.Fatal(err)
				}
			case "missing-context":
				if err := os.Remove(marker); err != nil {
					t.Fatal(err)
				}
			}
			authPath := filepath.Join(c.ProfilePath, "auth.json")
			authBefore, err := os.ReadFile(authPath)
			if err != nil {
				t.Fatal(err)
			}
			registry, err = store.BeginCredentialErasure(registry.Revision, account.ID, 1, "0.160.0")
			if err != nil {
				t.Fatal(err)
			}
			_, err = store.EraseCredentials(registry.Revision, account.ID, 1, ErasureProof{WritersStopped: true})
			if change == "none" {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Lstat(authPath); !os.IsNotExist(err) {
					t.Fatal("credentials retained")
				}
				if _, err := codexstock.Validate(root, codexstock.RetainedDirectory); err != nil {
					t.Fatal("stock erased", err)
				}
			} else {
				if err == nil {
					t.Fatal("unsafe custody admitted erasure")
				}
				after, e := os.ReadFile(authPath)
				if e != nil || string(after) != string(authBefore) {
					t.Fatal("custody refusal changed credentials")
				}
			}
			if after, e := os.ReadFile(ownerFile); e != nil || string(after) != "owner asset" {
				t.Fatal("erasure followed owner alias")
			}
			if _, e := os.Lstat(filepath.Join(c.ProfilePath, "skills")); e != nil {
				t.Fatal("erasure removed alias")
			}
			if change != "missing-receipt" {
				if _, e := os.Lstat(receiptPath); e != nil {
					t.Fatal("erasure removed custody")
				}
			}
		})
	}
}
