package logins

import (
	"encoding/json"
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

func TestClaudeWriteFreeCWDStillRequiresCurrentBirthAuthority(t *testing.T) {
	for _, kind := range []string{"valid", "selection", "receipt", "alias", "auth-config"} {
		t.Run(kind, func(t *testing.T) {
			s, a, b, rt := claudeBirthFixture(t)
			if err := os.Mkdir(filepath.Join(s.StateRoot(), "live-discussion"), 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ProvisionClaudeProfile(snapshot(t, s).Revision, b.ID, []string{"/new"}); err != nil {
				t.Fatalf("write-free CWD preparation: %v", err)
			}
			switch kind {
			case "selection":
				if _, err := s.SelectClaudeProfile(snapshot(t, s).Revision, a.ID); err != nil {
					t.Fatal(err)
				}
			case "receipt":
				if err := os.WriteFile(filepath.Join(s.savedNativeHome(b.ID), ".credentials.json"), []byte(`{}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "alias":
				if err := os.Remove(filepath.Join(rt.Home, "commands")); err != nil {
					t.Fatal(err)
				}
			case "auth-config":
				if err := os.WriteFile(filepath.Join(rt.Home, "settings.local.json"), []byte(`{"apiKeyHelper":"foreign-auth"}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			err := s.AdmitClaudeProfileBirth(rt, "/new", func() error { calls++; return nil })
			if kind == "valid" {
				if err != nil || calls != 1 {
					t.Fatalf("current authority refused: calls=%d err=%v", calls, err)
				}
			} else if err == nil || calls != 0 {
				t.Fatalf("membership bypassed current birth authority: calls=%d err=%v", calls, err)
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
			s, _, b, ref := newSettledReadyClaudeAuthTest(t)
			before := snapshot(t, s)
			expires := time.Now().Add(2 * time.Hour)
			if kind == "expiry" {
				expires = time.Now().Add(-time.Minute)
			}
			if err := os.WriteFile(filepath.Join(s.savedNativeHome(b.ID), ".credentials.json"), claudeAuthGrant(expires, []string{"user:profile", "user:inference"}), 0600); err != nil {
				t.Fatal(err)
			}
			next, err := s.SelectClaudeProfile(before.Revision, b.ID)
			if err != nil || next.Providers[ProviderClaude].ActiveID != b.ID {
				t.Fatalf("stale ready stranded desired selection: %v", err)
			}
			if err := s.ValidateClaudeAuthReceipt(ref, next.Providers[ProviderClaude].Epoch); err == nil {
				t.Fatal("pending desired selection consumed stale receipt")
			}
			if _, created, err := s.ReplaceClaudeAuth(next.Revision, ref); err != nil || !created {
				t.Fatalf("existing renewal path refused pending desired: %v %v", created, err)
			}
			if got := snapshot(t, s); got.Revision != next.Revision || got.Providers[ProviderClaude] != next.Providers[ProviderClaude] {
				t.Fatal("renewal changed desired selection or required another click")
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

func TestClaudeCustodyClaimAllowsOrdinaryPreStartRefusalsWithoutStartAuthority(t *testing.T) {
	for _, kind := range []string{"old_epoch", "expired_grant", "unready_auth", "missing_grant", "unhealthy_profile", "unprovisioned_profile", "unprovisioned_cwd", "missing_image"} {
		t.Run(kind, func(t *testing.T) {
			s, a, b, rt := claudeBirthFixture(t)
			cwd := "/work"
			switch kind {
			case "old_epoch":
				if _, err := s.SelectClaudeProfile(snapshot(t, s).Revision, a.ID); err != nil {
					t.Fatal(err)
				}
			case "expired_grant":
				if err := os.WriteFile(filepath.Join(s.savedNativeHome(b.ID), ".credentials.json"), claudeAuthGrant(time.Now().Add(-time.Hour), []string{"user:profile", "user:inference"}), 0600); err != nil {
					t.Fatal(err)
				}
			case "unready_auth":
				s.mu.Lock()
				err := s.locked(func() error {
					r, err := s.load()
					if err != nil {
						return err
					}
					find(&r, b.ID).AuthState = AuthNeedsLogin
					return s.save(r)
				})
				s.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			case "missing_grant":
				if err := os.Remove(filepath.Join(s.savedNativeHome(b.ID), ".credentials.json")); err != nil {
					t.Fatal(err)
				}
			case "unhealthy_profile":
				if err := os.WriteFile(filepath.Join(rt.Home, "settings.local.json"), []byte(`{"apiKeyHelper":"foreign"}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "unprovisioned_profile":
				s.mu.Lock()
				err := s.locked(func() error {
					r, err := s.load()
					if err != nil {
						return err
					}
					p := r.ClaudeProfiles[b.Identity]
					p.Provisioned = false
					r.ClaudeProfiles[b.Identity] = p
					return s.save(r)
				})
				s.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			case "unprovisioned_cwd":
				cwd = "/not-provisioned"
			case "missing_image":
				if _, err := os.Stat("/definitely-missing-custody-cli"); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("image fixture exists")
				}
			}
			called := 0
			if err := s.ClaimClaudeProfileCustody(rt, cwd, func() error { called++; return nil }); err != nil || called != 1 {
				t.Fatalf("ordinary refusal prevented cleanup custody: %d %v", called, err)
			}
			if kind != "missing_image" {
				started := false
				if err := s.AdmitClaudeProfileBirth(rt, cwd, func() error { started = true; return nil }); err == nil || started {
					t.Fatal("custody claim weakened final native admission")
				}
			}
		})
	}
}

func TestClaudeCustodyClaimRejectsUnknownFutureOrUnsafeAuthority(t *testing.T) {
	for _, kind := range []string{"future", "unknown_profile", "unknown_login", "missing_saved", "foreign_saved", "old_schema", "unsafe_home", "unsafe_cwd", "nil_callback"} {
		t.Run(kind, func(t *testing.T) {
			s, _, b, rt := claudeBirthFixture(t)
			cwd := "/work"
			switch kind {
			case "future":
				rt.Epoch++
			case "unsafe_home":
				rt.Home = s.Home(ProviderClaude)
			case "unsafe_cwd":
				cwd = "relative"
			case "missing_saved":
				if err := os.Remove(filepath.Join(s.savedNativeHome(b.ID), "credential.json")); err != nil {
					t.Fatal(err)
				}
			case "foreign_saved":
				saved, err := s.readSavedRecord(b)
				if err != nil {
					t.Fatal(err)
				}
				saved.Identity = strings.Repeat("e", 64)
				raw, _ := json.Marshal(saved)
				if err := os.WriteFile(filepath.Join(s.savedNativeHome(b.ID), "credential.json"), raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "unknown_profile", "unknown_login", "old_schema":
				s.mu.Lock()
				err := s.locked(func() error {
					r, err := s.load()
					if err != nil {
						return err
					}
					switch kind {
					case "unknown_profile":
						delete(r.ClaudeProfiles, b.Identity)
					case "unknown_login":
						for i := range r.Logins {
							if r.Logins[i].ID == b.ID {
								r.Logins = append(r.Logins[:i], r.Logins[i+1:]...)
								break
							}
						}
						p := r.Providers[ProviderClaude]
						p.ActiveID = ""
						r.Providers[ProviderClaude] = p
					case "old_schema":
						r.SchemaVersion = 9
					}
					return s.save(r)
				})
				s.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			}
			called := false
			claim := func() error { called = true; return nil }
			if kind == "nil_callback" {
				claim = nil
			}
			if err := s.ClaimClaudeProfileCustody(rt, cwd, claim); err == nil || called {
				t.Fatalf("unknown authority claimed cleanup custody: %v %v", called, err)
			}
		})
	}
}

func TestClaudeCustodyClaimHoldsRegistryFenceAndPropagatesExclusiveCallback(t *testing.T) {
	s, _, _, rt := claudeBirthFixture(t)
	other, err := OpenExisting(s.StateRoot())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	path := filepath.Join(rt.Home, "custody-claim-fixture")
	calls := 0
	claim := func() error {
		calls++
		if err := syscall.Flock(int(other.lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); !errors.Is(err, syscall.EWOULDBLOCK) {
			if err == nil {
				_ = syscall.Flock(int(other.lock.Fd()), syscall.LOCK_UN)
			}
			return errors.New("custody callback escaped registry fence")
		}
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		return file.Close()
	}
	if err := s.ClaimClaudeProfileCustody(rt, "/work", claim); err != nil {
		t.Fatal(err)
	}
	if err := s.ClaimClaudeProfileCustody(rt, "/work", claim); !errors.Is(err, os.ErrExist) || calls != 2 {
		t.Fatalf("duplicate callback silently retried/coalesced: %d %v", calls, err)
	}
	for _, want := range []error{ErrConflict, ErrPending, syscall.ENOSPC} {
		calls = 0
		if err := s.ClaimClaudeProfileCustody(rt, "/work", func() error { calls++; return want }); err != want || calls != 1 {
			t.Fatalf("changed/retired/uncertain callback repeated: %d %v", calls, err)
		}
	}
}

func TestClaudeFreshHandleBirthGuardsSettleRegistryBeforeCustodyConsumption(t *testing.T) {
	for _, kind := range []string{"claim", "admit"} {
		t.Run(kind, func(t *testing.T) {
			s, _, _, rt := claudeBirthFixture(t)
			state := s.StateRoot()
			visible := claudeUnsettledRegistryTest(t, s)
			fresh := claudeFreshStoreTest(t, state)
			fresh.writeOps.syncDir = func(string) error { return syscall.EIO }
			if got, err := fresh.Runtime(ProviderClaude); err != nil || got != rt {
				t.Fatalf("readonly Runtime acquired settlement requirement: %+v %v", got, err)
			}
			calls := 0
			consume := func() error { calls++; return nil }
			var err error
			if kind == "claim" {
				err = fresh.ClaimClaudeProfileCustody(rt, "/work", consume)
			} else {
				err = fresh.AdmitClaudeProfileBirth(rt, "/work", consume)
			}
			if !errors.Is(err, ErrDurabilityUncertain) || calls != 0 {
				t.Fatalf("birth consumed visible unsettled registry: %d %v", calls, err)
			}
			if got := snapshot(t, fresh); got.Revision != visible.Revision {
				t.Fatal("settlement failure rewrote revision")
			}
			fresh = claudeFreshStoreTest(t, state)
			if kind == "claim" {
				err = fresh.ClaimClaudeProfileCustody(rt, "/work", consume)
			} else {
				err = fresh.AdmitClaudeProfileBirth(rt, "/work", consume)
			}
			if err != nil || calls != 1 || snapshot(t, fresh).Revision != visible.Revision {
				t.Fatalf("settled birth callback/revision: %d %v", calls, err)
			}
		})
	}
}
