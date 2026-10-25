package logins

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAstraReloginAfterNativeClearsActiveAuth(t *testing.T) {
	for _, p := range []string{ProviderClaude, ProviderCodex} {
		t.Run(p, func(t *testing.T) {
			s := testStore(t)
			c := codexCredential("a", "old")
			fresh := codexCredential("a", "fresh")
			if p == ProviderClaude {
				c = claudeCredential("a", "old")
				fresh = claudeCredential("a", "fresh")
			}
			a := add(t, s, p, "A", c)
			activate(t, s, a)
			if p == ProviderClaude {
				_ = s.keychain.Delete(s.savedNativeHome(a.ID))
			} else {
				if e := s.root.Remove("provider/codex/native/auth.json"); e != nil {
					t.Fatal(e)
				}
			}
			r := snapshot(t, s)
			r, e := s.UpdateAvailability(r.Revision, a.ID, AuthNeedsLogin, false, time.Time{})
			if e != nil {
				t.Fatal(e)
			}
			var op Switch
			if p == ProviderClaude {
				op = seedLegacyClaudeSwitch(t, s, a, "historical relogin recovery", &fresh)
			} else {
				op, e = s.BeginRelogin(r.Revision, a.ID, fresh, "relogin", nil)
				if e != nil {
					t.Fatalf("fresh valid reauth blocked at BeginRelogin: %v", e)
				}
			}
			op.Phase = "stopping"
			if e = s.SaveSwitch(op); e != nil {
				t.Fatal(e)
			}
			op.Phase = "stopped"
			if e = s.SaveSwitch(op); e != nil {
				t.Fatal(e)
			}
			if e = s.CaptureOutgoing(op.ID); e != nil {
				t.Fatalf("fresh valid reauth blocked capturing erased revoked auth: %v", e)
			}
			if e = s.InstallIncoming(op.ID); e != nil {
				t.Fatalf("fresh valid reauth blocked installing: %v", e)
			}
			if _, e = s.CommitSwitch(op.ID); e != nil {
				t.Fatal(e)
			}
			if e = s.CompleteSwitch(op.ID); e != nil {
				t.Fatal(e)
			}
			if _, e = s.Runtime(p); e != nil {
				t.Fatalf("reauthenticated runtime refused: %v", e)
			}
		})
	}
}

func TestClearedOutgoingGrantCanSwitchAwayWithoutOverwritingSavedCredentials(t *testing.T) {
	for _, p := range []string{ProviderClaude, ProviderCodex} {
		t.Run(p, func(t *testing.T) {
			s := testStore(t)
			old, other := codexCredential("a", "old"), codexCredential("b", "other")
			if p == ProviderClaude {
				old, other = claudeCredential("a", "old"), claudeCredential("b", "other")
			}
			a := add(t, s, p, "A", old)
			b := add(t, s, p, "B", other)
			activate(t, s, a)
			before, e := readPrivate(s.root, s.savedPath(a), maxCredentialBytes*2)
			if e != nil {
				t.Fatal(e)
			}
			if p == ProviderClaude {
				if e = s.writeClaudeAuth(a.ID, []byte(`{"claudeAiOauth":{"accessToken":"","refreshToken":""}}`)); e != nil {
					t.Fatal(e)
				}
			} else {
				if _, e = durableReplace(s.root, "provider/codex/native/auth.json", []byte(`{"auth_mode":"chatgpt","tokens":{"account_id":"a","access_token":"","refresh_token":""}}`), writeOps{}); e != nil {
					t.Fatal(e)
				}
			}
			activate(t, s, b)
			after, e := readPrivate(s.root, s.savedPath(a), maxCredentialBytes*2)
			if e != nil || string(after) != string(before) {
				t.Fatalf("cleared auth overwrote latest saved snapshot: %s %v", after, e)
			}
			if got := snapshot(t, s).Logins[0]; got.AuthState != AuthNeedsLogin {
				t.Fatalf("cleared outgoing grant not classified: %+v", got)
			}
		})
	}
}

func TestOutgoingCaptureNeverTreatsForeignOrCorruptAuthAsSignout(t *testing.T) {
	for _, kind := range []string{"foreign", "malformed", "symlink", "missing-directory"} {
		t.Run(kind, func(t *testing.T) {
			s := testStore(t)
			a := add(t, s, ProviderCodex, "A", codexCredential("a", "a"))
			b := add(t, s, ProviderCodex, "B", codexCredential("b", "b"))
			activate(t, s, a)
			op := beginStopped(t, s, b)
			name := "provider/codex/native/auth.json"
			want := ErrInvalidCredentials
			switch kind {
			case "foreign":
				if _, e := durableReplace(s.root, name, []byte(`{"auth_mode":"chatgpt","tokens":{"account_id":"foreign","access_token":"","refresh_token":""}}`), writeOps{}); e != nil {
					t.Fatal(e)
				}
				want = ErrIdentityChanged
			case "malformed":
				if _, e := durableReplace(s.root, name, []byte(`{"tokens":`), writeOps{}); e != nil {
					t.Fatal(e)
				}
			case "symlink":
				if e := s.root.Remove(name); e != nil {
					t.Fatal(e)
				}
				if e := os.Symlink(filepath.Join(s.path, s.savedPath(a)), filepath.Join(s.path, name)); e != nil {
					t.Fatal(e)
				}
				want = ErrUnsafePath
			case "missing-directory":
				if e := s.root.Rename("provider/codex/native", "provider/codex/moved"); e != nil {
					t.Fatal(e)
				}
				want = os.ErrNotExist
			}
			if e := s.CaptureOutgoing(op.ID); !errors.Is(e, want) {
				t.Fatalf("%s misclassified as signout: %v", kind, e)
			}
			if snapshot(t, s).Switches[ProviderCodex].Phase != "stopped" {
				t.Fatal("failed capture transferred authority")
			}
		})
	}
}

func TestProviderDirectoryCreationSyncsParentAfterMkdir(t *testing.T) {
	s := testStore(t)
	var dirs []string
	name := "provider/claude/native/test"
	if e := mkdirPrivate(s.root, name, writeOps{syncDir: func(dir string) error { dirs = append(dirs, dir); return nil }}); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(dirs, []string{"provider/claude/native", name}) {
		t.Fatalf("parent entry not synced first: %v", dirs)
	}
	name = "provider/claude/native/uncertain"
	if e := mkdirPrivate(s.root, name, writeOps{syncDir: func(string) error { return errors.New("synthetic fsync") }}); !errors.Is(e, ErrDurabilityUncertain) {
		t.Fatalf("visible mkdir failure not uncertain: %v", e)
	}
	if _, e := s.root.Lstat(name); e != nil {
		t.Fatal("test did not reach visible mkdir")
	}
}

func TestProtectedHomesRemainAvailableWhileSwitchPending(t *testing.T) {
	s := testStore(t)
	a := add(t, s, ProviderClaude, "A", claudeCredential("a", "a"))
	b := add(t, s, ProviderClaude, "B", claudeCredential("b", "b"))
	activate(t, s, a)
	beginStopped(t, s, b)
	homes, e := s.ProtectedHomes(ProviderClaude)
	if e != nil {
		t.Fatal(e)
	}
	want := strings.Join([]string{s.Home(ProviderClaude), s.savedNativeHome(a.ID), s.savedNativeHome(b.ID)}, "\n")
	if strings.Join(homes, "\n") != want {
		t.Fatalf("writer homes missing: %v", homes)
	}
}
func TestAstraParkedReloginCrashRecoversReady(t *testing.T) {
	s := testStore(t)
	a := add(t, s, ProviderCodex, "A", codexCredential("a", "old"))
	r := snapshot(t, s)
	r, e := s.UpdateAvailability(r.Revision, a.ID, AuthNeedsLogin, true, time.Time{})
	if e != nil {
		t.Fatal(e)
	}
	s.writeOps.rename = func(old, new string) error {
		if new == "registry.json" {
			return errors.New("crash")
		}
		return s.root.Rename(old, new)
	}
	if _, _, e = s.Relogin(r.Revision, a.ID, codexCredential("a", "fresh")); e == nil {
		t.Fatal("expected crash")
	}
	s.writeOps = writeOps{}
	if e = s.Reconcile(); e != nil {
		t.Fatal(e)
	}
	got := snapshot(t, s).Logins[0]
	if got.AuthState != AuthReady || got.LimitKnown {
		t.Fatalf("new durable grant left unusable: %+v", got)
	}
}
func TestAstraClaudeReloginCrashBeforeMetadata(t *testing.T) {
	s := testStore(t)
	a := add(t, s, ProviderClaude, "A", claudeCredential("a", "old"))
	r := snapshot(t, s)
	r, e := s.UpdateAvailability(r.Revision, a.ID, AuthNeedsLogin, true, time.Time{})
	if e != nil {
		t.Fatal(e)
	}
	s.writeOps.rename = func(old, new string) error {
		if new == s.savedPath(a) {
			return errors.New("crash")
		}
		return s.root.Rename(old, new)
	}
	if _, _, e = s.Relogin(r.Revision, a.ID, claudeCredential("a", "fresh")); e == nil {
		t.Fatal("expected crash")
	}
	s.writeOps = writeOps{}
	if e = s.Reconcile(); e != nil {
		t.Fatal(e)
	}
	got := snapshot(t, s).Logins[0]
	if got.AuthState != AuthReady {
		t.Fatalf("replacement canonical grant invisible after crash: %+v", got)
	}
}
