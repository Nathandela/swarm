package accountconfig

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"github.com/Nathandela/swarm/internal/codexstock"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func putCodexStockSkills(t *testing.T, destination string) {
	t.Helper()
	file, err := os.Open("../codexstock/testdata/skills-0.160.0.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	zipped, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = zipped.Close() }()
	reader := tar.NewReader(zipped)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil || !filepath.IsLocal(header.Name) {
			t.Fatal("invalid stock fixture")
		}
		path := filepath.Join(destination, header.Name)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0775); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0775); err != nil {
				t.Fatal(err)
			}
		case tar.TypeReg:
			put(t, path, "")
			out, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0664)
			if err != nil {
				t.Fatal(err)
			}
			_, copyErr := io.Copy(out, reader)
			closeErr := out.Close()
			if copyErr != nil || closeErr != nil {
				t.Fatal("cannot extract stock fixture")
			}
			if err := os.Chmod(path, 0664); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatal("unsupported stock fixture entry")
		}
	}
}

func TestCodexStockSkillsAdoptionPreservesStockAndOwnerAssets(t *testing.T) {
	f := codexContextFixture(t)
	put(t, filepath.Join(f.source, "skills", "owner", "SKILL.md"), "owner skill")
	putCodexStockSkills(t, filepath.Join(f.candidate, "skills"))
	c, err := PrepareCodexContext(f.candidate, f.cwd, f.env, codexContextLease)
	if err != nil {
		t.Fatal(err)
	}
	if err := RevalidateCodexContext(c, f.candidate); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareCodexContext(f.candidate, f.cwd, f.env, codexContextLease); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(filepath.Join(f.candidate, "skills"))
	if err != nil || target != filepath.Join(f.source, "skills") {
		t.Fatal("owner skills were not retained")
	}
	if _, err := os.Stat(filepath.Join(f.candidate, ".swarm-codex-stock-skills-0.160.0", ".system", ".codex-system-skills.marker")); err != nil {
		t.Fatal("native stock was not preserved")
	}
}

func TestCodexStockSkillsAbsentSourceAllowsNativeInitialization(t *testing.T) {
	f := codexContextFixture(t)
	c, err := PrepareCodexContext(f.candidate, f.cwd, f.env, codexContextLease)
	if err != nil {
		t.Fatal(err)
	}
	putCodexStockSkills(t, filepath.Join(f.candidate, "skills"))
	if err := RevalidateCodexContext(c, f.candidate); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareCodexContext(f.candidate, f.cwd, f.env, codexContextLease); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Readlink(filepath.Join(f.candidate, "skills")); err == nil {
		t.Fatal("absent owner source was aliased")
	}
}

func TestCodexStockSkillsAbsentToPresentOwnerRefresh(t *testing.T) {
	f := codexContextFixture(t)
	if _, err := PrepareCodexContext(f.candidate, f.cwd, f.env, codexContextLease); err != nil {
		t.Fatal(err)
	}
	putCodexStockSkills(t, filepath.Join(f.candidate, "skills"))
	put(t, filepath.Join(f.source, "skills", "owner", "SKILL.md"), "new owner skill")
	c, err := PrepareCodexContext(f.candidate, f.cwd, f.env, codexContextLease, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := RevalidateCodexContext(c, f.candidate); err != nil {
		t.Fatal(err)
	}
}

func stockAdoptionPlanFixture(t *testing.T) (projectionFixture, CodexContext, codexstock.Receipt) {
	t.Helper()
	f := codexContextFixture(t)
	put(t, filepath.Join(f.source, "skills", "owner", "SKILL.md"), "owner skill")
	putCodexStockSkills(t, filepath.Join(f.candidate, "skills"))
	other := filepath.Join(filepath.Dir(f.candidate), "context-proof")
	if err := os.Mkdir(other, 0700); err != nil {
		t.Fatal(err)
	}
	c, err := PrepareCodexContext(other, f.cwd, f.env, codexContextLease)
	if err != nil {
		t.Fatal(err)
	}
	record, pending, err := planCodexStockSkills(c, f.candidate)
	if err != nil || pending || record == nil {
		t.Fatal("cannot plan stock adoption")
	}
	return f, c, *record
}

func TestCodexStockSkillsEveryInterruptedPhaseRetriesInsideLease(t *testing.T) {
	for _, stage := range []string{"outer", "intent", "rename", "alias", "context", "custody", "inner-removed"} {
		t.Run(stage, func(t *testing.T) {
			f, c, record := stockAdoptionPlanFixture(t)
			raw, _ := json.Marshal(codexstock.OuterGuard{SchemaVersion: 2, StockSkills: record})
			put(t, filepath.Join(f.candidate, CodexContextUpdateMarker), string(raw))
			if stage != "outer" {
				raw, _ = json.Marshal(record)
				put(t, filepath.Join(f.candidate, codexstock.PendingFile), string(raw))
			}
			if stage != "outer" && stage != "intent" {
				if err := os.Rename(filepath.Join(f.candidate, "skills"), filepath.Join(f.candidate, codexstock.RetainedDirectory)); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "alias" || stage == "context" || stage == "custody" || stage == "inner-removed" {
				if err := os.Symlink(record.SourceTarget, filepath.Join(f.candidate, "skills")); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "context" || stage == "custody" || stage == "inner-removed" {
				raw, _ := json.Marshal(c)
				put(t, filepath.Join(f.candidate, CodexContextMarker), string(raw))
				other := filepath.Join(filepath.Dir(f.candidate), "context-proof", "config.toml")
				config, err := os.ReadFile(other)
				if err != nil {
					t.Fatal(err)
				}
				put(t, filepath.Join(f.candidate, "config.toml"), string(config))
			}
			if stage == "custody" || stage == "inner-removed" {
				raw, _ = json.Marshal(record)
				put(t, filepath.Join(f.candidate, codexstock.CustodyFile), string(raw))
			}
			if stage == "inner-removed" {
				if err := os.Remove(filepath.Join(f.candidate, codexstock.PendingFile)); err != nil {
					t.Fatal(err)
				}
			}
			if !PendingNativeContextUpdate(f.candidate, "codex") || MatchingInstalledContext(f.candidate, "codex", c.GlobalGeneration) {
				t.Fatal("pending custody bypassed stopped-writer fence")
			}
			if err := RevalidateCodexContext(c, f.candidate); err == nil {
				t.Fatal("read-only revalidation finished pending custody")
			}
			called := false
			got, err := PrepareCodexContext(f.candidate, f.cwd, f.env, func(_ string, install func() error) error { called = true; return install() })
			if err != nil || !called || got.GlobalGeneration != c.GlobalGeneration {
				t.Fatalf("interrupted %s could not replay under lease: %v", stage, err)
			}
			if PendingNativeContextUpdate(f.candidate, "codex") {
				t.Fatal("completed stock intent remains")
			}
			if err := RevalidateCodexContext(got, f.candidate); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCodexStockSkillsUnknownCustomizationAndLostCustodyHold(t *testing.T) {
	for _, change := range []string{"extra", "marker-preserving-change", "symlink", "hardlink", "world-write", "retained-without-receipt", "pending-without-outer", "changed-source", "duplicate-stock"} {
		t.Run(change, func(t *testing.T) {
			f, c, record := stockAdoptionPlanFixture(t)
			skills := filepath.Join(f.candidate, "skills")
			sample := filepath.Join(skills, ".system", "imagegen", "SKILL.md")
			switch change {
			case "extra":
				put(t, filepath.Join(skills, "custom", "SKILL.md"), "custom asset")
			case "marker-preserving-change":
				put(t, sample, "private native edit")
			case "symlink":
				if err := os.Remove(sample); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(f.source, "config.toml"), sample); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(sample, filepath.Join(f.cwd, "external-link")); err != nil {
					t.Fatal(err)
				}
			case "world-write":
				if err := os.Chmod(sample, 0666); err != nil {
					t.Fatal(err)
				}
			case "retained-without-receipt":
				if err := os.Rename(skills, filepath.Join(f.candidate, codexstock.RetainedDirectory)); err != nil {
					t.Fatal(err)
				}
			case "pending-without-outer":
				raw, _ := json.Marshal(record)
				put(t, filepath.Join(f.candidate, codexstock.PendingFile), string(raw))
			case "changed-source", "duplicate-stock":
				raw, _ := json.Marshal(codexstock.OuterGuard{SchemaVersion: 2, StockSkills: record})
				put(t, filepath.Join(f.candidate, CodexContextUpdateMarker), string(raw))
				if change == "changed-source" {
					put(t, filepath.Join(f.source, "config.toml"), "model_reasoning_effort=\"low\"\n")
				} else {
					putCodexStockSkills(t, filepath.Join(f.candidate, codexstock.RetainedDirectory))
				}
			}
			before, _ := os.ReadFile(filepath.Join(f.candidate, "auth.json"))
			if _, err := PrepareCodexContext(f.candidate, f.cwd, f.env, codexContextLease); err == nil {
				t.Fatal("unknown native stock state admitted")
			}
			after, _ := os.ReadFile(filepath.Join(f.candidate, "auth.json"))
			if string(before) != string(after) {
				t.Fatal("failed admission changed credentials")
			}
			if _, err := os.Lstat(filepath.Join(f.candidate, "config.toml")); !os.IsNotExist(err) {
				t.Fatal("failed stock admission wrote configuration")
			}
			_ = c
		})
	}
}

func TestCodexStockSkillsCustodySurvivesGlobalRefreshAndSourceDeletion(t *testing.T) {
	f, _, _ := stockAdoptionPlanFixture(t)
	_, err := PrepareCodexContext(f.candidate, f.cwd, f.env, codexContextLease)
	if err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(f.source, "config.toml"), "model_reasoning_effort=\"low\"\n")
	_, err = PrepareCodexContext(f.candidate, f.cwd, f.env, codexContextLease, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(f.source, "skills"), filepath.Join(f.source, "skills-retained")); err != nil {
		t.Fatal(err)
	}
	c, err := PrepareCodexContext(f.candidate, f.cwd, f.env, codexContextLease, true)
	if err != nil {
		t.Fatal(err)
	}
	putCodexStockSkills(t, filepath.Join(f.candidate, "skills"))
	if err := RevalidateCodexContext(c, f.candidate); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareCodexContext(f.candidate, f.cwd, f.env, codexContextLease); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.candidate, codexstock.RetainedDirectory)); err != nil {
		t.Fatal("retained stock disappeared")
	}
}

func TestCodexStockSkillsCompletedCustodyCannotBeForgedOrLost(t *testing.T) {
	for _, change := range []string{"missing-receipt", "changed-retained", "invalid-basename"} {
		t.Run(change, func(t *testing.T) {
			f, _, _ := stockAdoptionPlanFixture(t)
			c, err := PrepareCodexContext(f.candidate, f.cwd, f.env, codexContextLease)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "missing-receipt":
				if err := os.Remove(filepath.Join(f.candidate, codexstock.CustodyFile)); err != nil {
					t.Fatal(err)
				}
			case "changed-retained":
				put(t, filepath.Join(f.candidate, codexstock.RetainedDirectory, ".system", "imagegen", "SKILL.md"), "changed native bytes")
			case "invalid-basename":
				raw, err := os.ReadFile(filepath.Join(f.candidate, codexstock.CustodyFile))
				if err != nil {
					t.Fatal(err)
				}
				put(t, filepath.Join(f.candidate, codexstock.CustodyFile), strings.ReplaceAll(string(raw), codexstock.RetainedDirectory, "../unsafe"))
			}
			if err := RevalidateCodexContext(c, f.candidate); err == nil {
				t.Fatal("lost custody admitted")
			}
			if _, err := PrepareCodexContext(f.candidate, f.cwd, f.env, codexContextLease); err == nil {
				t.Fatal("lost custody regenerated")
			}
		})
	}
}

func TestCodexStockSkillsDeniedLeaseAndOtherCollisionDoNotMutate(t *testing.T) {
	for _, cause := range []string{"writers-active", "rules-collision"} {
		t.Run(cause, func(t *testing.T) {
			f := codexContextFixture(t)
			put(t, filepath.Join(f.source, "skills", "owner", "SKILL.md"), "owner skill")
			putCodexStockSkills(t, filepath.Join(f.candidate, "skills"))
			lease := codexContextLease
			if cause == "writers-active" {
				lease = func(_ string, _ func() error) error { return errors.New("active writer") }
			} else {
				put(t, filepath.Join(f.candidate, "rules", "custom.rules"), "private rule")
			}
			root, err := os.OpenRoot(f.candidate)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = root.Close() }()
			before, err := codexstock.Validate(root, "skills")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := PrepareCodexContext(f.candidate, f.cwd, f.env, lease); err == nil {
				t.Fatal("unsafe adoption succeeded")
			}
			after, err := codexstock.Validate(root, "skills")
			if err != nil || before != after {
				t.Fatal("refusal changed stock")
			}
			for _, name := range []string{codexstock.PendingFile, codexstock.CustodyFile, codexstock.ContextUpdateFile, codexstock.RetainedDirectory, "config.toml"} {
				if _, err := os.Lstat(filepath.Join(f.candidate, name)); !os.IsNotExist(err) {
					t.Fatal("refusal wrote", name)
				}
			}
		})
	}
}

func TestCodexStockSkillsRefreshSharesExistingGlobalJournal(t *testing.T) {
	for _, phase := range []string{"global-intent", "stock-intent", "stock-aliased"} {
		t.Run(phase, func(t *testing.T) {
			f := codexContextFixture(t)
			previous, err := PrepareCodexContext(f.candidate, f.cwd, f.env, codexContextLease)
			if err != nil {
				t.Fatal(err)
			}
			putCodexStockSkills(t, filepath.Join(f.candidate, "skills"))
			put(t, filepath.Join(f.source, "skills", "owner", "SKILL.md"), "new owner asset")
			other := filepath.Join(filepath.Dir(f.candidate), "new-proof")
			if err := os.Mkdir(other, 0700); err != nil {
				t.Fatal(err)
			}
			next, err := PrepareCodexContext(other, f.cwd, f.env, codexContextLease)
			if err != nil {
				t.Fatal(err)
			}
			update := codexContextUpdate{SchemaVersion: 1, Previous: previous, Next: next}
			if err := writeCodexContextUpdate(f.candidate, update); err != nil {
				t.Fatal(err)
			}
			originalJournal, err := os.ReadFile(filepath.Join(f.candidate, CodexContextUpdateMarker))
			if err != nil {
				t.Fatal(err)
			}
			record, _, err := planCodexStockSkills(next, f.candidate)
			if err != nil || record == nil {
				t.Fatal("stock plan", err)
			}
			if phase == "stock-intent" {
				raw, _ := json.Marshal(record)
				put(t, filepath.Join(f.candidate, codexstock.PendingFile), string(raw))
			}
			if phase == "stock-aliased" {
				if err := applyCodexStockSkills(*record, f.candidate); err != nil {
					t.Fatal(err)
				}
			}
			journal, err := os.ReadFile(filepath.Join(f.candidate, CodexContextUpdateMarker))
			if err != nil || string(journal) != string(originalJournal) {
				t.Fatal("stock adoption overwrote global journal")
			}
			if !PendingNativeContextUpdate(f.candidate, "codex") {
				t.Fatal("refresh pending fence missing")
			}
			if _, err := PrepareCodexContext(f.candidate, f.cwd, f.env, codexContextLease); err == nil {
				t.Fatal("refresh replay lacked explicit owner authorization")
			}
			got, err := PrepareCodexContext(f.candidate, f.cwd, f.env, codexContextLease, true)
			if err != nil || got.GlobalGeneration != next.GlobalGeneration {
				t.Fatal("refresh stock replay", err)
			}
			if PendingNativeContextUpdate(f.candidate, "codex") {
				t.Fatal("refresh journal survived commit")
			}
			if err := RevalidateCodexContext(got, f.candidate); err != nil {
				t.Fatal(err)
			}
		})
	}
}
