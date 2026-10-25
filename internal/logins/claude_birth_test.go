package logins

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/processcontain"
)

func claudeBirthFixture(t *testing.T) (*Store, Login, Login, Runtime) {
	t.Helper()
	s, a, b, ref := newSettledReadyClaudeAuthTest(t)
	s.grantWriters = func([]string, []string) ([]processcontain.Identity, error) { return nil, nil }
	if _, err := s.ProvisionClaudeProfile(snapshot(t, s).Revision, b.ID, []string{"/work"}); err != nil {
		t.Fatal(err)
	}
	r, err := s.SelectClaudeProfile(snapshot(t, s).Revision, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateClaudeAuthReceipt(ref, r.Providers[ProviderClaude].Epoch); err != nil {
		t.Fatal(err)
	}
	home, err := s.ClaudeProfileHome(b.Identity)
	if err != nil {
		t.Fatal(err)
	}
	return s, a, b, Runtime{Provider: ProviderClaude, Identity: b.Identity, Epoch: r.Providers[ProviderClaude].Epoch, Home: home}
}

func TestClaudeProfileBirthRequiresActualCurrentReceiptProfileAndCWD(t *testing.T) {
	for _, kind := range []string{"valid", "epoch", "identity", "home", "external", "cwd", "generation"} {
		t.Run(kind, func(t *testing.T) {
			s, _, b, rt := claudeBirthFixture(t)
			cwd := "/work"
			switch kind {
			case "epoch":
				rt.Epoch++
			case "identity":
				rt.Identity = string(make([]byte, 64))
			case "home":
				rt.Home = s.Home(ProviderClaude)
			case "external":
				rt.ExternalAuth = true
			case "cwd":
				cwd = "/new"
			case "generation":
				if err := os.WriteFile(filepath.Join(s.savedNativeHome(b.ID), ".credentials.json"), []byte(`{}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			called := 0
			err := s.AdmitClaudeProfileBirth(rt, cwd, func() error { called++; return nil })
			if kind == "valid" {
				if err != nil || called != 1 {
					t.Fatalf("valid birth refused: called=%d err=%v", called, err)
				}
			} else if err == nil || called != 0 {
				t.Fatalf("invalid birth invoked native callback: %d %v", called, err)
			}
		})
	}
}

func TestClaudeProfileBirthAndRealSelectionShareRegistryFlock(t *testing.T) {
	s, a, _, rt := claudeBirthFixture(t)
	other, err := OpenExisting(s.StateRoot())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	other.keychain = nil
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.AdmitClaudeProfileBirth(rt, "/work", func() error {
			if err := syscall.Flock(int(other.lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); !errors.Is(err, syscall.EWOULDBLOCK) {
				if err == nil {
					_ = syscall.Flock(int(other.lock.Fd()), syscall.LOCK_UN)
				}
				return errors.New("birth did not hold cross-handle registry flock")
			}
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	selected := make(chan error, 1)
	go func() { _, err := other.SelectClaudeProfile(snapshot(t, other).Revision, a.ID); selected <- err }()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-selected; err != nil {
		t.Fatal(err)
	}
	called := false
	if err := s.AdmitClaudeProfileBirth(rt, "/work", func() error { called = true; return nil }); err == nil || called {
		t.Fatal("C-first selection admitted delayed B")
	}
}

func TestClaudeProfileBirthCallbackErrorReleasesGuardWithoutReplay(t *testing.T) {
	s, _, _, rt := claudeBirthFixture(t)
	want := errors.New("published start outcome uncertain")
	called := 0
	if err := s.AdmitClaudeProfileBirth(rt, "/work", func() error { called++; return want }); err != want || called != 1 {
		t.Fatalf("callback ownership: %d %v", called, err)
	}
	if _, err := s.SelectClaudeProfile(snapshot(t, s).Revision, snapshot(t, s).Providers[ProviderClaude].ActiveID); err != nil {
		t.Fatalf("failed callback stranded lock: %v", err)
	}
}

func TestClaudeProfileSelectionPersistsDesiredPendingAndCASPreservesOldState(t *testing.T) {
	s := testStore(t)
	a := add(t, s, ProviderClaude, "A", claudeCredential("a", "a"))
	r := snapshot(t, s)
	s.writeOps.rename = func(_, _ string) error { return syscall.ENOSPC }
	if _, err := s.SelectClaudeProfile(r.Revision, a.ID); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("selection write failure: %v", err)
	}
	s.writeOps.rename = nil
	if got := snapshot(t, s); got.Revision != r.Revision || got.Providers[ProviderClaude].ActiveID != "" {
		t.Fatal("failed save changed desired selection")
	}
	next, err := s.SelectClaudeProfile(r.Revision, a.ID)
	if err != nil || next.Providers[ProviderClaude].ActiveID != a.ID || next.Providers[ProviderClaude].AuthOwnerMode != ClaudeNativeProfileMode {
		t.Fatalf("desired pending: %+v %v", next, err)
	}
	if _, err := s.SelectClaudeProfile(r.Revision, a.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale selection CAS: %v", err)
	}
	again, err := s.SelectClaudeProfile(next.Revision, a.ID)
	if err != nil || again.Providers[ProviderClaude].Epoch != next.Providers[ProviderClaude].Epoch+1 {
		t.Fatalf("same-account desired retry: %+v %v", again, err)
	}
}

func TestClaudeProfileRuntimeEnvironmentUsesExactSelectedProfileAndSavedAuthority(t *testing.T) {
	s, _, b, want := claudeBirthFixture(t)
	rt, err := s.Runtime(ProviderClaude)
	if err != nil || rt != want {
		t.Fatalf("owned Runtime binding: %+v %v", rt, err)
	}
	env, err := s.PrepareEnvironment(rt, []string{"PATH=/bin", "CLAUDE_CONFIG_DIR=/foreign", "CLAUDE_SECURESTORAGE_CONFIG_DIR=/foreign", "ANTHROPIC_API_KEY=foreign"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "CLAUDE_CONFIG_DIR="+want.Home) || !strings.Contains(joined, "CLAUDE_SECURESTORAGE_CONFIG_DIR="+s.savedNativeHome(b.ID)) || strings.Contains(joined, "foreign") {
		t.Fatalf("incorrect native authority routing: %q", env)
	}
	readOnly, err := OpenReadOnly(s.StateRoot())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = readOnly.Close() }()
	readOnly.keychain = nil
	if got, err := readOnly.Runtime(ProviderClaude); err != nil || got != rt {
		t.Fatalf("read-only preflight: %+v %v", got, err)
	}
	if _, err := readOnly.PrepareEnvironment(rt, []string{"PATH=/bin"}); err != nil {
		t.Fatalf("read-only environment preflight: %v", err)
	}
	next, err := s.SelectClaudeProfile(snapshot(t, s).Revision, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if next.Providers[ProviderClaude].Epoch == rt.Epoch {
		t.Fatal("explicit desired retry did not advance epoch")
	}
	if _, err := s.PrepareEnvironment(rt, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("old immutable runtime was rebound: %v", err)
	}
}

func TestClaudeProfileOperationalSelectionAndBirthRequireRegistryFloorTen(t *testing.T) {
	s, _, b, rt := claudeBirthFixture(t)
	// A legacy unscoped receipt remains parseable at the inactive 7/8/9 floors;
	// the operational gate must refuse it independently of scope-schema parsing.
	intent, err := s.readClaudeAuthIntent(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	intent.Version = 1
	intent.Scope = ClaudeAuthScope{}
	intent.UnitRetired = false
	if err := s.writeClaudeAuthIntent(intent); err != nil {
		t.Fatal(err)
	}
	r := snapshot(t, s)
	if r.SchemaVersion != 10 {
		t.Fatalf("operational selection failed to fence old Store readers: schema=%d", r.SchemaVersion)
	}
	for _, version := range []int{7, 8, 9} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			s.mu.Lock()
			err := s.locked(func() error {
				old, err := s.load()
				if err != nil {
					return err
				}
				old.SchemaVersion = version
				return s.save(old)
			})
			s.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			rt.Epoch = snapshot(t, s).Providers[ProviderClaude].Epoch
			called := false
			if err := s.AdmitClaudeProfileBirth(rt, "/work", func() error { called = true; return nil }); err == nil || called {
				t.Fatal("inactive old schema authorized native admission")
			}
			if _, err := s.Runtime(ProviderClaude); err == nil {
				t.Fatal("inactive old schema authorized operational runtime")
			}
			if _, err := s.SelectClaudeProfile(snapshot(t, s).Revision, b.ID); err != nil {
				t.Fatal(err)
			}
			if got := snapshot(t, s).SchemaVersion; got != 10 {
				t.Fatalf("selection did not durably upgrade schema: %d", got)
			}
		})
	}
}

func TestClaudeOwnedProfileAuthSettingCannotOverrideSavedAuthority(t *testing.T) {
	s, _, _, rt := claudeBirthFixture(t)
	if err := os.WriteFile(filepath.Join(rt.Home, "settings.local.json"), []byte(`{"apiKeyHelper":"foreign-auth"}`), 0600); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := s.AdmitClaudeProfileBirth(rt, "/work", func() error { called = true; return nil }); err == nil || called {
		t.Fatal("profile auth override bypassed saved authority")
	}
}

func TestClaudeProfileSelectionCanFollowExistingStaleReadyRenewal(t *testing.T) {
	for _, kind := range []string{"generation", "expiry"} {
		t.Run(kind, func(t *testing.T) {
			s, a, b, ref := newSettledReadyClaudeAuthTest(t)
			before := snapshot(t, s)
			expires := time.Now().Add(2 * time.Hour)
			if kind == "expiry" {
				expires = time.Now().Add(-time.Minute)
			}
			if err := os.WriteFile(filepath.Join(s.savedNativeHome(b.ID), ".credentials.json"), claudeAuthGrant(expires, []string{"user:profile", "user:inference"}), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.SelectClaudeProfile(before.Revision, b.ID); err == nil {
				t.Fatal("stale ready selected as consumable")
			}
			if got := snapshot(t, s); got.Revision != before.Revision || got.Providers[ProviderClaude].ActiveID != a.ID {
				t.Fatal("refused selection changed desired account")
			}
			if _, created, err := s.ReplaceClaudeAuth(before.Revision, ref); err != nil || !created {
				t.Fatalf("existing warm renewal path refused: %v %v", created, err)
			}
			next, err := s.SelectClaudeProfile(before.Revision, b.ID)
			if err != nil || next.Providers[ProviderClaude].ActiveID != b.ID {
				t.Fatalf("renewed prepared helper stranded selection: %+v %v", next, err)
			}
		})
	}
}

func TestClaudeProfileModeRefusesNewLegacySwitch(t *testing.T) {
	s, a, _, _ := claudeBirthFixture(t)
	if _, err := s.BeginSwitch(snapshot(t, s).Revision, ProviderClaude, a.ID, "selection", nil); !errors.Is(err, ErrPending) {
		t.Fatalf("profile mode admitted legacy switch: %v", err)
	}
	if _, ok := snapshot(t, s).Switches[ProviderClaude]; ok {
		t.Fatal("legacy switch journal published")
	}
}
