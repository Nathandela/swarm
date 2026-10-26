package supervise

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func upgradeLogFixture(t *testing.T) ([]string, string) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home with 'quotes")
	prefix := filepath.Join(root, "brew with 'quotes")
	if err := os.MkdirAll(filepath.Join(home, ".local/state/swarm"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(prefix, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"brew": "#!/bin/sh\nprintf 'brew:%s\\n' \"$*\"\nexit 11\n", "swarm": "#!/bin/sh\nprintf 'restart:%s\\n' \"$*\"\nprintf 'stderr:current-run\\n' >&2\nexit 17\n"} {
		if err := os.WriteFile(filepath.Join(prefix, "bin", name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	raw := strings.ReplaceAll(strings.ReplaceAll(string(readUpgradeTemplate(t)), "@HOME@", home), "@PREFIX@", prefix)
	args, err := launchdProgramArguments([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return args, filepath.Join(home, ".local/state/swarm/upgrade.log")
}

func TestUpgradeLogRetainsLatestInvocationAndExactStatus(t *testing.T) {
	args, path := upgradeLogFixture(t)
	if err := os.WriteFile(path, []byte("previous-run\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		cmd := exec.Command(args[0], args[1:]...)
		out, err := cmd.CombinedOutput()
		exit, ok := err.(*exec.ExitError)
		if !ok || exit.ExitCode() != 17 {
			t.Fatalf("upgrade exit=%v output=%s", err, out)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		want := "brew:update\nbrew:upgrade --cask swarm\nrestart:daemon restart --unattended\nstderr:current-run\n"
		if string(got) != want {
			t.Fatalf("retained log=%q want=%q", got, want)
		}
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0600 {
			t.Fatalf("upgrade log mode=%o", info.Mode().Perm())
		}
	}
}

func TestUpgradeLogRefusesUnsafeTargets(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "directory", "public-parent"} {
		t.Run(kind, func(t *testing.T) {
			args, path := upgradeLogFixture(t)
			target := filepath.Join(filepath.Dir(path), "valuable")
			if err := os.WriteFile(target, []byte("keep"), 0644); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "symlink":
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(target, path); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "public-parent":
				if err := os.Chmod(filepath.Dir(path), 0777); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command(args[0], args[1:]...)
			if out, err := cmd.CombinedOutput(); err == nil {
				t.Fatalf("unsafe log accepted: %s", out)
			}
			got, _ := os.ReadFile(target)
			info, _ := os.Stat(target)
			if string(got) != "keep" || info.Mode().Perm() != 0644 {
				t.Fatal("unsafe target modified")
			}
		})
	}
}
