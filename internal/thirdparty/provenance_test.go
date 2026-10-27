package thirdparty_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type sourceComponent struct {
	Directory string            `json:"directory"`
	Copied    map[string]string `json:"copied_sha256"`
}

func checkCopiedSources(root string, components []sourceComponent) error {
	for _, component := range components {
		dir := filepath.Join(root, component.Directory)
		for name, pin := range component.Copied {
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				return err
			}
			hash := sha256.Sum256(data)
			if hex.EncodeToString(hash[:]) != pin {
				return fmt.Errorf("copied source drift: %s/%s", component.Directory, name)
			}
		}
		if err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.Name() == "go.mod" || entry.Name() == "go.work" || entry.Name() == "vendor" {
				return fmt.Errorf("copied source cannot contain a nested module/vendor boundary: %s", path)
			}
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				return nil
			}
			name, err := filepath.Rel(dir, path)
			if err != nil {
				return err
			}
			if _, ok := component.Copied[filepath.ToSlash(name)]; !ok {
				return fmt.Errorf("untracked copied production source: %s", path)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// shortcut: relocation hides canonical vulnerability identity, retire the copy through h6sc.26.1 after upstream qualification.
func TestCopiedSourceProvenance(t *testing.T) {
	data, err := os.ReadFile("provenance.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct{ Components []sourceComponent }
	if err = json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Components) != 2 {
		t.Fatal("both copied components must have provenance")
	}
	if err = checkCopiedSources(filepath.Join("..", ".."), manifest.Components); err != nil {
		t.Fatal(err)
	}
}

func TestCopiedSourceGuardRejectsDriftAndNestedModules(t *testing.T) {
	root := t.TempDir()
	data := []byte("package copied\n")
	hash := sha256.Sum256(data)
	components := []sourceComponent{{Directory: ".", Copied: map[string]string{"source.go": hex.EncodeToString(hash[:])}}}
	path := filepath.Join(root, "source.go")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkCopiedSources(root, components); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, 'x'), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkCopiedSources(root, components); err == nil {
		t.Fatal("source modification bypassed the provenance guard")
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module copied\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkCopiedSources(root, components); err == nil {
		t.Fatal("nested module bypassed the module ZIP boundary guard")
	}
}
