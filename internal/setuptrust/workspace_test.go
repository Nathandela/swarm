package setuptrust

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWorkspaceEnrollment(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Setenv("HOME", t.TempDir())
	} else {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	}
	root := t.TempDir()
	cwd := filepath.Join(root, "workspace")
	if err := os.Mkdir(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(cwd, link); err != nil {
		t.Fatal(err)
	}
	if _, on := Workspace(link); on {
		t.Fatal("enabled before enrolling")
	}
	if _, err := Set(link, true); err != nil {
		t.Fatal(err)
	}
	if _, on := Workspace(cwd); !on {
		t.Fatal("missing exact physical workspace")
	}
	child := filepath.Join(cwd, "child")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	if _, on := Workspace(child); on {
		t.Fatal("enrolled child implicitly")
	}
	if err := os.Rename(cwd, cwd+".previous"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	if _, on := Workspace(cwd); on {
		t.Fatal("enrolled replacement directory")
	}
	if err := os.Remove(cwd); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(cwd+".previous", cwd); err != nil {
		t.Fatal(err)
	}
	if _, err := Set(cwd, false); err != nil {
		t.Fatal(err)
	}
	if _, on := Workspace(cwd); on {
		t.Fatal("still enabled after disabling")
	}
}
