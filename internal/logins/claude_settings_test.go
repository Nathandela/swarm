package logins

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"

	"github.com/Nathandela/swarm/internal/processcontain"
)

func TestClaudeSettingsAbsentProvisioning(t *testing.T) {
	for _, stage := range []string{"fresh", "known", "extend"} {
		t.Run(stage, func(t *testing.T) {
			s, l, common := profileFixture(t)
			if stage != "fresh" {
				if _, err := s.ProvisionClaudeProfile(snapshot(t, s).Revision, l.ID, []string{"/work"}); err != nil {
					t.Fatal(err)
				}
			}
			name := filepath.Join(common, "settings.json")
			if err := os.Remove(name); err != nil {
				t.Fatal(err)
			}
			cwds := []string{"/work"}
			if stage == "extend" {
				cwds = append(cwds, "/new")
			}
			if _, err := s.ProvisionClaudeProfile(snapshot(t, s).Revision, l.ID, cwds); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(name)
			if err != nil || string(raw) != "{}\n" {
				t.Fatalf("missing private complete settings: %q %v", raw, err)
			}
			before, err := os.Lstat(name)
			if err != nil || !privateInfo(before, false) || before.Mode().Perm() != 0600 {
				t.Fatalf("unsafe settings: %v %v", before, err)
			}
			prefs := []byte(`{"theme":"light","permissions":{"defaultMode":"default"}}`)
			if err := os.WriteFile(name, prefs, 0600); err != nil {
				t.Fatal(err)
			}
			s.grantWriters = func([]string, []string) ([]processcontain.Identity, error) {
				t.Fatal("existing settings triggered writer inventory")
				return nil, nil
			}
			if _, err := s.ProvisionClaudeProfile(snapshot(t, s).Revision, l.ID, cwds); err != nil {
				t.Fatal(err)
			}
			after, _ := os.Lstat(name)
			got, _ := os.ReadFile(name)
			if !os.SameFile(before, after) || string(got) != string(prefs) {
				t.Fatal("existing preferences replaced")
			}
		})
	}
}

func TestClaudeSettingsAbsentExcludesAllProfileWriters(t *testing.T) {
	for _, kind := range []string{"writer", "inventory-error", "pending-other-profile"} {
		t.Run(kind, func(t *testing.T) {
			s, l, common := profileFixture(t)
			if _, err := s.ProvisionClaudeProfile(snapshot(t, s).Revision, l.ID, []string{"/work"}); err != nil {
				t.Fatal(err)
			}
			other := add(t, s, ProviderClaude, "B", claudeCredential("b", "grant-b"))
			if _, err := s.ProvisionClaudeProfile(snapshot(t, s).Revision, other.ID, []string{"/work"}); err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(common, "settings.json")
			if err := os.Remove(name); err != nil {
				t.Fatal(err)
			}
			home, _ := s.ClaudeProfileHome(other.Identity)
			if kind == "pending-other-profile" {
				writeClaudeCustodyJSON(t, s.StateRoot(), "pending", "meta.json", map[string]any{"schema_version": ClaudeReservationSchemaVersion, "id": "pending", "agent_type": ProviderClaude, "cwd": "/work", "shim_pid": 0, "shim_start_time": 0, "login_runtime": Runtime{Provider: ProviderClaude, Epoch: 1, Identity: other.Identity, Home: home}, "cli_identity": map[string]string{"path": "/qualified/claude", "version": "2.1.310", "fingerprint": other.Identity}})
			} else {
				s.grantWriters = func(homes, _ []string) ([]processcontain.Identity, error) {
					if !slices.Contains(homes, home) || !slices.Contains(homes, common) {
						t.Fatalf("incomplete shared writer scope: %v", homes)
					}
					if kind == "inventory-error" {
						return nil, errors.New("inventory unavailable")
					}
					return []processcontain.Identity{{PID: 42, StartTime: 1}}, nil
				}
			}
			before := snapshot(t, s).Revision
			if _, err := s.ProvisionClaudeProfile(before, l.ID, []string{"/work"}); err == nil {
				t.Fatal("missing settings initialized without exclusion")
			}
			if _, err := os.Lstat(name); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("settings unexpectedly published: %v", err)
			}
			if snapshot(t, s).Revision != before {
				t.Fatal("refusal changed registry")
			}
		})
	}
}

func TestClaudeSettingsMissingBirthRefusesWithoutRepair(t *testing.T) {
	s, _, _, rt := claudeBirthFixture(t)
	name := filepath.Join(s.Home(ProviderClaude), "settings.json")
	if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	called := false
	if err := s.AdmitClaudeProfileBirth(rt, "/work", func() error { called = true; return nil }); err == nil || called {
		t.Fatalf("missing settings admitted: called=%v err=%v", called, err)
	}
	if _, err := os.Lstat(name); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("read-only birth repaired settings")
	}
}

func TestClaudeSettingsExclusiveWinnerAndFaults(t *testing.T) {
	for _, kind := range []string{"preferences", "writable", "symlink", "hardlink", "fifo", "auth", "oversized", "link-failure", "unlink-failure", "sync-failure"} {
		t.Run(kind, func(t *testing.T) {
			s, l, common := profileFixture(t)
			if _, err := s.ProvisionClaudeProfile(snapshot(t, s).Revision, l.ID, []string{"/work"}); err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(common, "settings.json")
			if err := os.Remove(name); err != nil {
				t.Fatal(err)
			}
			revision := snapshot(t, s).Revision
			var winner os.FileInfo
			prefs := []byte(`{"theme":"dark","model":"opus"}`)
			s.writeOps.link = func(old, next string) error {
				switch kind {
				case "link-failure":
					return syscall.ENOSPC
				case "preferences", "writable", "auth", "oversized":
					raw := prefs
					mode := os.FileMode(0600)
					if kind == "writable" {
						mode = 0664
					}
					if kind == "auth" {
						raw = []byte(`{"apiKeyHelper":"foreign"}`)
					}
					if kind == "oversized" {
						raw = make([]byte, maxCredentialBytes+1)
					}
					if err := os.WriteFile(name, raw, mode); err != nil {
						return err
					}
					if err := os.Chmod(name, mode); err != nil {
						return err
					}
				case "symlink":
					if err := os.Symlink(filepath.Join(common, "CLAUDE.md"), name); err != nil {
						return err
					}
				case "hardlink":
					if err := os.Link(filepath.Join(common, "CLAUDE.md"), name); err != nil {
						return err
					}
				case "fifo":
					if err := syscall.Mkfifo(name, 0600); err != nil {
						return err
					}
				default:
					return s.root.Link(old, next)
				}
				winner, _ = os.Lstat(name)
				return s.root.Link(old, next)
			}
			if kind == "unlink-failure" {
				s.writeOps.remove = func(string) error { return syscall.EIO }
			}
			if kind == "sync-failure" {
				s.writeOps.syncDir = func(string) error { return syscall.EIO }
			}
			_, err := s.ProvisionClaudeProfile(revision, l.ID, []string{"/work"})
			if kind == "preferences" {
				if err != nil {
					t.Fatal(err)
				}
				after, _ := os.Lstat(name)
				raw, _ := os.ReadFile(name)
				if !os.SameFile(winner, after) || string(raw) != string(prefs) {
					t.Fatal("concurrent preferences clobbered")
				}
			} else if kind == "unlink-failure" || kind == "sync-failure" {
				if !errors.Is(err, ErrDurabilityUncertain) || !s.uncertain {
					t.Fatalf("uncertainty not retained: %v", err)
				}
				if _, e := s.ProvisionClaudeProfile(revision, l.ID, []string{"/work"}); !errors.Is(e, ErrDurabilityUncertain) {
					t.Fatalf("old handle bypassed durability latch: %v", e)
				}
				fresh, e := OpenExisting(s.StateRoot())
				if e != nil {
					t.Fatal(e)
				}
				defer func() { _ = fresh.Close() }()
				fresh.keychain = &fakeKeychain{}
				fresh.grantWriters = func([]string, []string) ([]processcontain.Identity, error) { return nil, nil }
				before, _ := os.Lstat(name)
				_, e = fresh.ProvisionClaudeProfile(revision, l.ID, []string{"/work"})
				if kind == "unlink-failure" {
					if !errors.Is(e, ErrUnsafePath) {
						t.Fatalf("hardlink silently repaired: %v", e)
					}
				} else {
					if e != nil {
						t.Fatal(e)
					}
					after, _ := os.Lstat(name)
					if !os.SameFile(before, after) {
						t.Fatal("settlement rewrote settings")
					}
				}
			} else if err == nil {
				t.Fatalf("unsafe/fault %s accepted", kind)
			}
			if kind == "link-failure" {
				if _, e := os.Lstat(name); !errors.Is(e, os.ErrNotExist) {
					t.Fatal("failed link published")
				}
			}
			if winner != nil {
				after, e := os.Lstat(name)
				if e != nil || !os.SameFile(winner, after) {
					t.Fatal("unsafe winner replaced")
				}
			}
			if kind != "unlink-failure" {
				temps, _ := filepath.Glob(filepath.Join(common, ".login-tmp-*"))
				if len(temps) != 0 {
					t.Fatalf("temporary leaked: %v", temps)
				}
			}
		})
	}
}

func TestClaudeSettingsRuntimeDoesNotCreate(t *testing.T) {
	s, _, _, _ := claudeBirthFixture(t)
	name := filepath.Join(s.Home(ProviderClaude), "settings.json")
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	_, _ = s.Runtime(ProviderClaude)
	if _, err := os.Lstat(name); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Runtime initialized settings")
	}
}

func TestClaudeSettingsConcurrentStoresPreserveWinner(t *testing.T) {
	s, l, common := profileFixture(t)
	if _, err := s.ProvisionClaudeProfile(snapshot(t, s).Revision, l.ID, []string{"/work"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(common, "settings.json")); err != nil {
		t.Fatal(err)
	}
	other, err := OpenExisting(s.StateRoot())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	other.keychain = &fakeKeychain{}
	other.grantWriters = s.grantWriters
	revision := snapshot(t, s).Revision
	entered, release := make(chan struct{}), make(chan struct{})
	s.writeOps.link = func(a, b string) error { close(entered); <-release; return s.root.Link(a, b) }
	done := make(chan error, 2)
	go func() { _, e := s.ProvisionClaudeProfile(revision, l.ID, []string{"/work"}); done <- e }()
	<-entered
	go func() { _, e := other.ProvisionClaudeProfile(revision, l.ID, []string{"/work"}); done <- e }()
	close(release)
	for range 2 {
		if e := <-done; e != nil {
			t.Fatal(e)
		}
	}
	raw, e := os.ReadFile(filepath.Join(common, "settings.json"))
	if e != nil || string(raw) != "{}\n" {
		t.Fatalf("bad result: %q %v", raw, e)
	}
}
