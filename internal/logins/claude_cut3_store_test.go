package logins

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/processcontain"
)

func TestClaudeDesiredStaleReadyCommitsPendingWithoutConsumingReceipt(t *testing.T) {
	for _, kind := range []string{"generation", "expiry", "malformed", "missing", "target"} {
		t.Run(kind, func(t *testing.T) {
			s, _, b, ref := newSettledReadyClaudeAuthTest(t)
			helperInventory := s.grantWriters
			s.grantWriters = func([]string, []string) ([]processcontain.Identity, error) { return nil, nil }
			if _, err := s.ProvisionClaudeProfile(snapshot(t, s).Revision, b.ID, []string{"/work"}); err != nil {
				t.Fatal(err)
			}
			s.grantWriters = helperInventory
			grant := filepath.Join(s.savedNativeHome(b.ID), ".credentials.json")
			switch kind {
			case "generation":
				if err := os.WriteFile(grant, claudeAuthGrant(time.Now().Add(2*time.Hour), []string{"user:inference", "user:profile"}), 0600); err != nil {
					t.Fatal(err)
				}
			case "expiry":
				if err := os.WriteFile(grant, claudeAuthGrant(time.Now().Add(-time.Hour), []string{"user:inference", "user:profile"}), 0600); err != nil {
					t.Fatal(err)
				}
			case "malformed":
				if err := os.WriteFile(grant, []byte(`{"claudeAiOauth":{"accessToken":"changed","expiresAt":1,"scopes":null}}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing":
				if err := os.Remove(grant); err != nil {
					t.Fatal(err)
				}
			case "target":
				intent, err := s.readClaudeAuthIntent(b.ID)
				if err != nil {
					t.Fatal(err)
				}
				intent.Ref.CredentialRevision++
				if err = s.writeClaudeAuthIntent(intent); err != nil {
					t.Fatal(err)
				}
			}
			before := snapshot(t, s)
			after, err := s.SelectClaudeProfile(before.Revision, b.ID)
			if kind == "generation" || kind == "expiry" {
				if err != nil || after.Providers[ProviderClaude].ActiveID != b.ID {
					t.Fatalf("desired stale-ready: %v", err)
				}
				if err = s.ValidateClaudeAuthReceipt(ref, after.Providers[ProviderClaude].Epoch); err == nil {
					t.Fatal("stale receipt consumed")
				}
				rt := Runtime{Provider: ProviderClaude, Epoch: after.Providers[ProviderClaude].Epoch, Identity: b.Identity}
				rt.Home, _ = s.ClaudeProfileHome(b.Identity)
				called := false
				if err = s.AdmitClaudeProfileBirth(rt, "/work", func() error { called = true; return nil }); err == nil || called {
					t.Fatal("stale birth admitted")
				}
				if _, err = s.Runtime(ProviderClaude); err == nil {
					t.Fatal("stale runtime ready")
				}
				if held, err := s.ReadClaudeAuth(ref); err != nil || held.Phase != "ready" {
					t.Fatal("selection erased retained receipt")
				}
			} else if err == nil || !reflect.DeepEqual(snapshot(t, s), before) {
				t.Fatal("invalid ready changed selection")
			}
		})
	}
}

func TestClaudeAuthAdmittedSlotsAcrossFreshStoresAndPausedCreator(t *testing.T) {
	s, a, _, paused := newClaudeAuthUnboundTest(t)
	c := add(t, s, ProviderClaude, "C", claudeCredential("c", "c"))
	d := add(t, s, ProviderClaude, "D", claudeCredential("d", "d"))
	if _, created, err := s.BeginClaudeAuth(snapshot(t, s).Revision, a.ID); err != nil || !created {
		t.Fatal(err)
	}
	before := snapshot(t, s)
	var wg sync.WaitGroup
	for _, l := range []Login{c, d} {
		wg.Go(func() {
			fresh, err := OpenExisting(s.StateRoot())
			if err != nil {
				t.Error(err)
				return
			}
			defer func() { _ = fresh.Close() }()
			fresh.keychain = nil
			if _, created, err := fresh.BeginClaudeAuth(before.Revision, l.ID); !errors.Is(err, ErrCapacity) || created {
				t.Errorf("third admitted: created=%v err=%v", created, err)
			}
		})
	}
	wg.Wait()
	if !reflect.DeepEqual(snapshot(t, s), before) {
		t.Fatal("capacity changed registry")
	}
	// The first creator is paused before Start; zero PIDs do not release its slot.
	held, err := s.ReadClaudeAuth(paused)
	if err != nil || held.Phase != "prepared" {
		t.Fatal("lost paused creator")
	}
	settleClaudeAuthUnboundTest(t, s, paused)
	if _, created, err := s.BeginClaudeAuth(snapshot(t, s).Revision, c.ID); err != nil || !created {
		t.Fatalf("settled slot not reusable: %v", err)
	}
	if _, _, err := s.ClaimClaudeAuthAttempt(paused, currentClaudeAuthWorker(t)); err == nil {
		t.Fatal("late creator admitted after revocation")
	}
}

func TestClaudeAuthAdmissionRefusesIncompleteInventoryAndUnsettledTerminal(t *testing.T) {
	for _, kind := range []string{"malformed", "orphan", "terminal-payload"} {
		t.Run(kind, func(t *testing.T) {
			s, a, b, ref := newClaudeAuthUnboundTest(t)
			if _, created, err := s.BeginClaudeAuth(snapshot(t, s).Revision, a.ID); err != nil || !created {
				t.Fatal(err)
			}
			c := add(t, s, ProviderClaude, "C", claudeCredential("c", "c"))
			switch kind {
			case "malformed":
				if err := os.WriteFile(filepath.Join(s.path, claudeAuthPath(b.ID)), []byte(`{}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "orphan":
				if err := os.Mkdir(filepath.Join(s.path, "claude-auth", strings.Repeat("f", 32)), 0700); err != nil {
					t.Fatal(err)
				}
			case "terminal-payload":
				settleClaudeAuthUnboundTest(t, s, ref)
				if err := os.Mkdir(filepath.Join(s.path, claudeAuthPayload(b.ID)), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(s.path, claudeAuthPayload(b.ID), ref.Nonce), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if _, created, err := s.BeginClaudeAuth(snapshot(t, s).Revision, c.ID); err == nil || created {
				t.Fatal("incomplete/unsettled inventory admitted")
			}
		})
	}
}

func TestClaudeProfileExtendsStoppedCWDAndPreservesNativeFields(t *testing.T) {
	s, l, common := profileFixture(t)
	r, err := s.ProvisionClaudeProfile(snapshot(t, s).Revision, l.ID, []string{"/work"})
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.SelectClaudeProfile(r.Revision, l.ID)
	if err != nil {
		t.Fatal(err)
	}
	home, _ := s.ClaudeProfileHome(l.Identity)
	raw, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	body := strings.TrimSuffix(string(raw), "}") + `,"oauthAccount":{"accountUuid":"keep"},"unknown":{"preserve":true}}`
	if err = os.WriteFile(filepath.Join(home, ".claude.json"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	source := `{"mcpServers":{"user":{"command":"server","env":{"SECRET":"mcp-secret"}}},"projects":{"/work":{"mcpServers":{"local":{"command":"local"}}},"/new":{"mcpServers":{"new":{"command":"new-server"}}}}}`
	if err = os.WriteFile(filepath.Join(common, ".claude.json"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	next, err := s.ProvisionClaudeProfile(r.Revision, l.ID, []string{"/new"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(next.ClaudeProfiles[l.Identity].CWDs, []string{"/new", "/work"}) {
		t.Fatal("lost old CWD")
	}
	got, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"oauthAccount", "unknown", "new-server", "local", "mcp-secret"} {
		if !strings.Contains(string(got), want) {
			t.Fatalf("lost %s", want)
		}
	}
}

func TestClaudeProfileWriteFreeExtensionRetainsLiveCustody(t *testing.T) {
	for _, kind := range []string{"live", "registry-full", "native-update"} {
		t.Run(kind, func(t *testing.T) {
			s, l, _, native := claudeCustodyFixture(t)
			writeClaudeCustodyJSON(t, s.StateRoot(), "source", NativeProcessFile, native)
			r := snapshot(t, s)
			if err := s.claudeAuthorityMutationAllowed(r, l); err == nil {
				t.Fatal("fixture does not retain live native custody")
			}
			home, _ := s.ClaudeProfileHome(l.Identity)
			path := filepath.Join(home, ".claude.json")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			body := strings.TrimSuffix(string(raw), "}") + `,"oauthAccount":{"accountUuid":"keep"},"unknown":{"preserve":true}}`
			if err = os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			s.grantWriters = func([]string, []string) ([]processcontain.Identity, error) {
				t.Fatal("write-free extension scanned native writers")
				return nil, nil
			}
			fail := kind == "registry-full"
			want := body
			s.writeOps.rename = func(old, new string) error {
				if strings.HasSuffix(new, ".claude.json") {
					t.Fatal("write-free extension replaced native config")
				}
				if new == "registry.json" {
					if fail {
						return syscall.ENOSPC
					}
					if kind == "native-update" {
						want = strings.TrimSuffix(body, "}") + `,"nativeUpdate":true}`
						if err := os.WriteFile(path+".native", []byte(want), 0600); err != nil {
							return err
						}
						if err := os.Rename(path+".native", path); err != nil {
							return err
						}
					}
				}
				return s.root.Rename(old, new)
			}
			next, err := s.ProvisionClaudeProfile(r.Revision, l.ID, []string{"/new"})
			if fail {
				if !errors.Is(err, syscall.ENOSPC) || !reflect.DeepEqual(snapshot(t, s), r) {
					t.Fatalf("registry failure changed membership: %v", err)
				}
				fail = false
				next, err = s.ProvisionClaudeProfile(r.Revision, l.ID, []string{"/new"})
			}
			if err != nil || next.Revision != r.Revision+1 || !reflect.DeepEqual(next.ClaudeProfiles[l.Identity].CWDs, []string{"/new", "/work"}) {
				t.Fatalf("write-free extension: revision=%d cwds=%v err=%v", next.Revision, next.ClaudeProfiles[l.Identity].CWDs, err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != want {
				t.Fatalf("native config changed/lost concurrent update: %v", err)
			}
			after, err := os.Stat(path)
			if err != nil || kind != "native-update" && !os.SameFile(before, after) {
				t.Fatalf("write-free extension replaced native inode: %v", err)
			}
			if err := s.claudeAuthorityMutationAllowed(snapshot(t, s), l); err == nil {
				t.Fatal("CWD membership released live native custody")
			}
			if _, err = s.ProvisionClaudeProfile(r.Revision, l.ID, []string{"/other"}); !errors.Is(err, ErrConflict) {
				t.Fatalf("stale registry revision admitted membership: %v", err)
			}
		})
	}
}

func TestClaudeProfileExtensionRereadsAfterWriterExclusion(t *testing.T) {
	for _, kind := range []string{"fresh", "conflict"} {
		t.Run(kind, func(t *testing.T) {
			s, l, common := profileFixture(t)
			r, err := s.ProvisionClaudeProfile(snapshot(t, s).Revision, l.ID, []string{"/work"})
			if err != nil {
				t.Fatal(err)
			}
			source := `{"mcpServers":{"user":{"command":"server","env":{"SECRET":"mcp-secret"}}},"projects":{"/new":{"mcpServers":{"new":{"command":"old-server"}}}}}`
			if err = os.WriteFile(filepath.Join(common, ".claude.json"), []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			home, _ := s.ClaudeProfileHome(l.Identity)
			path := filepath.Join(home, ".claude.json")
			var changed string
			scans := 0
			s.grantWriters = func([]string, []string) ([]processcontain.Identity, error) {
				scans++
				raw, err := os.ReadFile(path)
				if err != nil {
					return nil, err
				}
				changed = strings.TrimSuffix(string(raw), "}") + `,"unknown":"after-exclusion"}`
				if err = os.WriteFile(path, []byte(changed), 0600); err != nil {
					return nil, err
				}
				source = strings.Replace(source, "old-server", "fresh-server", 1)
				if kind == "conflict" {
					source = strings.Replace(source, `"command":"server"`, `"command":"conflicting-user"`, 1)
				}
				return nil, os.WriteFile(filepath.Join(common, ".claude.json"), []byte(source), 0600)
			}
			next, err := s.ProvisionClaudeProfile(r.Revision, l.ID, []string{"/new"})
			if scans != 1 {
				t.Fatalf("writer scans = %d", scans)
			}
			got, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if kind == "conflict" {
				if !errors.Is(err, ErrConflict) || !reflect.DeepEqual(snapshot(t, s), r) || string(got) != changed {
					t.Fatalf("post-exclusion conflict wrote stale config/membership: %v", err)
				}
			} else if err != nil || next.Revision != r.Revision+1 || !strings.Contains(string(got), "fresh-server") || strings.Contains(string(got), "old-server") || !strings.Contains(string(got), "after-exclusion") {
				t.Fatalf("post-exclusion source/destination not refreshed: %v %s", err, got)
			}
		})
	}
}

func TestClaudeProfileWriteFreeProbeRefusesIncompleteConfig(t *testing.T) {
	for _, kind := range []string{"source-invalid", "destination-invalid", "duplicate", "oversized", "missing", "empty", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			s, l, _, _ := claudeCustodyFixture(t)
			r := snapshot(t, s)
			home, _ := s.ClaudeProfileHome(l.Identity)
			path := filepath.Join(home, ".claude.json")
			body := "{"
			switch kind {
			case "source-invalid":
				path = filepath.Join(s.Home(ProviderClaude), ".claude.json")
			case "duplicate":
				body = `{"mcpServers":{},"mcpServers":{}}`
			case "oversized":
				body = strings.Repeat(" ", maxCredentialBytes+1)
			case "empty":
				body = ""
			case "missing", "symlink":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if kind == "symlink" {
					if err := os.Symlink(filepath.Join(s.Home(ProviderClaude), ".claude.json"), path); err != nil {
						t.Fatal(err)
					}
				}
			}
			if kind != "missing" && kind != "symlink" {
				if err := os.WriteFile(path, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			s.grantWriters = func([]string, []string) ([]processcontain.Identity, error) {
				t.Fatal("invalid/missing config escaped probe or retained custody")
				return nil, nil
			}
			if _, err := s.ProvisionClaudeProfile(r.Revision, l.ID, []string{"/new"}); err == nil || !reflect.DeepEqual(snapshot(t, s), r) {
				t.Fatalf("incomplete config published new CWD: %v", err)
			}
		})
	}
}

func TestClaudeProfileExtensionRequiresWriterExclusionAndDurableConfig(t *testing.T) {
	for _, kind := range []string{"writer", "scan-error", "unknown", "config-full", "registry-full", "identical", "empty-cwd-conflict"} {
		t.Run(kind, func(t *testing.T) {
			s, l, common := profileFixture(t)
			r, err := s.ProvisionClaudeProfile(snapshot(t, s).Revision, l.ID, []string{"/work"})
			if err != nil {
				t.Fatal(err)
			}
			home, _ := s.ClaudeProfileHome(l.Identity)
			source := `{"mcpServers":{"user":{"command":"server","env":{"SECRET":"mcp-secret"}}},"projects":{"/new":{"mcpServers":{"new":{"command":"new-server"}}}}}`
			if err = os.WriteFile(filepath.Join(common, ".claude.json"), []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "writer":
				s.grantWriters = func([]string, []string) ([]processcontain.Identity, error) {
					return []processcontain.Identity{{PID: 1, StartTime: 1}}, nil
				}
			case "scan-error":
				s.grantWriters = func([]string, []string) ([]processcontain.Identity, error) { return nil, syscall.EIO }
			case "unknown":
				if err = os.Mkdir(filepath.Join(s.StateRoot(), "lost-session"), 0700); err != nil {
					t.Fatal(err)
				}
			case "identical":
				if err = os.WriteFile(filepath.Join(home, ".claude.json"), []byte(source), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "empty-cwd-conflict" {
				if err = os.WriteFile(filepath.Join(common, ".claude.json"), []byte(`{"mcpServers":{"changed":{"command":"different"}}}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			nativeWrites := 0
			s.writeOps.rename = func(old, new string) error {
				if strings.HasSuffix(new, ".claude.json") {
					nativeWrites++
					if kind == "config-full" || kind == "identical" {
						return syscall.ENOSPC
					}
				}
				if kind == "registry-full" && new == "registry.json" {
					return syscall.ENOSPC
				}
				return s.root.Rename(old, new)
			}
			_, err = s.ProvisionClaudeProfile(r.Revision, l.ID, []string{"/new"})
			if kind == "identical" {
				if err != nil || nativeWrites != 0 {
					t.Fatalf("identical rewrite: %v %d", err, nativeWrites)
				}
			} else {
				if err == nil {
					t.Fatal("unproved/failed extension accepted")
				}
				if !reflect.DeepEqual(snapshot(t, s).ClaudeProfiles[l.Identity].CWDs, []string{"/work"}) {
					t.Fatal("failed extension admitted new CWD")
				}
			}
		})
	}
}

func TestClaudeAuthAdmissionCountsBoundTerminalUntilPositiveSettlement(t *testing.T) {
	s, a, b, ref := newSettledReadyClaudeAuthTest(t)
	intent, err := s.readClaudeAuthIntent(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	intent.UnitRetired = false
	if err = s.writeClaudeAuthIntent(intent); err != nil {
		t.Fatal(err)
	}
	if _, created, err := s.BeginClaudeAuth(snapshot(t, s).Revision, a.ID); err != nil || !created {
		t.Fatal(err)
	}
	c := add(t, s, ProviderClaude, "C", claudeCredential("c", "c"))
	if _, created, err := s.BeginClaudeAuth(snapshot(t, s).Revision, c.ID); !errors.Is(err, ErrCapacity) || created {
		t.Fatalf("terminal freed slot: %v", err)
	}
	intent.UnitRetired = true
	if err = s.writeClaudeAuthIntent(intent); err != nil {
		t.Fatal(err)
	}
	if _, created, err := s.BeginClaudeAuth(snapshot(t, s).Revision, c.ID); err != nil || !created {
		t.Fatalf("positive settlement kept slot: %v", err)
	}
	// A stale ready replacement is settled, but its new PREPARED still needs a slot.
	if err = os.WriteFile(filepath.Join(s.savedNativeHome(b.ID), ".credentials.json"), claudeAuthGrant(time.Now().Add(2*time.Hour), []string{"user:inference", "user:profile"}), 0600); err != nil {
		t.Fatal(err)
	}
	if _, created, err := s.ReplaceClaudeAuth(snapshot(t, s).Revision, ref); !errors.Is(err, ErrCapacity) || created {
		t.Fatalf("replacement exceeded admitted bound: %v", err)
	}
	if held, err := s.ReadClaudeAuth(ref); err != nil || held.Phase != "ready" {
		t.Fatal("capacity erased replacement receipt")
	}
}

func TestClaudeAuthOneRemainingSlotHasSingleFreshStoreWinner(t *testing.T) {
	s, a, _, _ := newClaudeAuthUnboundTest(t)
	c := add(t, s, ProviderClaude, "C", claudeCredential("c", "c"))
	rev := snapshot(t, s).Revision
	start := make(chan struct{})
	out := make(chan bool, 2)
	for _, l := range []Login{a, c} {
		go func() {
			fresh, err := OpenExisting(s.StateRoot())
			if err != nil {
				t.Error(err)
				out <- false
				return
			}
			defer func() { _ = fresh.Close() }()
			fresh.keychain = nil
			<-start
			_, created, err := fresh.BeginClaudeAuth(rev, l.ID)
			if err != nil && !errors.Is(err, ErrCapacity) {
				t.Error(err)
			}
			out <- created
		}()
	}
	close(start)
	first, second := <-out, <-out
	if first == second {
		t.Fatal("remaining slot did not have exactly one winner")
	}
}

func TestClaudeProfileExtensionRegistryFailureRetriesWithoutNativeRewrite(t *testing.T) {
	s, l, common := profileFixture(t)
	r, err := s.ProvisionClaudeProfile(snapshot(t, s).Revision, l.ID, []string{"/work"})
	if err != nil {
		t.Fatal(err)
	}
	source := `{"mcpServers":{"user":{"command":"server","env":{"SECRET":"mcp-secret"}}},"projects":{"/new":{"mcpServers":{"new":{"command":"new-server"}}}}}`
	if err = os.WriteFile(filepath.Join(common, ".claude.json"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	s.writeOps.rename = func(old, new string) error {
		if new == "registry.json" {
			return syscall.ENOSPC
		}
		return s.root.Rename(old, new)
	}
	if _, err = s.ProvisionClaudeProfile(r.Revision, l.ID, []string{"/new"}); !errors.Is(err, syscall.ENOSPC) {
		t.Fatal(err)
	}
	fresh, err := OpenExisting(s.StateRoot())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fresh.Close() }()
	fresh.grantWriters = func([]string, []string) ([]processcontain.Identity, error) { return nil, nil }
	fresh.writeOps.rename = func(old, new string) error {
		if strings.HasSuffix(new, ".claude.json") {
			t.Fatal("retry rewrote native config")
		}
		return fresh.root.Rename(old, new)
	}
	next, err := fresh.ProvisionClaudeProfile(r.Revision, l.ID, []string{"/new"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(next.ClaudeProfiles[l.Identity].CWDs, []string{"/new", "/work"}) {
		t.Fatal("retry lost CWD membership")
	}
}

func TestClaudeProfileExtensionAndFreshStoreSelectionShareFence(t *testing.T) {
	s, l, common := profileFixture(t)
	r, err := s.ProvisionClaudeProfile(snapshot(t, s).Revision, l.ID, []string{"/work"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(common, ".claude.json"), []byte(`{"projects":{"/new":{"mcpServers":{"new":{"command":"new-server"}}}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	other, err := OpenExisting(s.StateRoot())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	entered, release := make(chan struct{}), make(chan struct{})
	s.grantWriters = func([]string, []string) ([]processcontain.Identity, error) {
		close(entered)
		<-release
		return nil, nil
	}
	extension := make(chan error, 1)
	go func() { _, err := s.ProvisionClaudeProfile(r.Revision, l.ID, []string{"/new"}); extension <- err }()
	<-entered
	selected := make(chan error, 1)
	go func() { _, err := other.SelectClaudeProfile(r.Revision, l.ID); selected <- err }()
	select {
	case err := <-selected:
		t.Fatalf("selection crossed extension custody fence: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-extension; err != nil {
		t.Fatal(err)
	}
	if err := <-selected; !errors.Is(err, ErrConflict) {
		t.Fatalf("stale selection crossed extension: %v", err)
	}
}

func TestClaudeDesiredStaleSelectionReplacementRetainsEpochBudget(t *testing.T) {
	s, _, b, ref := newSettledReadyClaudeAuthTest(t)
	if err := os.WriteFile(filepath.Join(s.savedNativeHome(b.ID), ".credentials.json"), claudeAuthGrant(time.Now().Add(2*time.Hour), []string{"user:inference", "user:profile"}), 0600); err != nil {
		t.Fatal(err)
	}
	desired, err := s.SelectClaudeProfile(snapshot(t, s).Revision, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	next, created, err := s.ReplaceClaudeAuth(desired.Revision, ref)
	if err != nil || !created {
		t.Fatalf("pending desired replacement: %v", err)
	}
	failed := settleClaudeAuthUnboundTest(t, s, next)
	if failed.Selection != desired.Providers[ProviderClaude] {
		t.Fatal("replacement lost issuing desired selection")
	}
	for range 3 {
		if _, created, err = s.ReplaceClaudeAuth(desired.Revision, next); !errors.Is(err, ErrConflict) || created {
			t.Fatal("tick reset desired budget")
		}
	}
	desired, err = s.SelectClaudeProfile(desired.Revision, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err = s.ReplaceClaudeAuth(desired.Revision, next); err != nil || !created {
		t.Fatalf("explicit desired epoch did not permit one retry: %v", err)
	}
}

func TestClaudeProfileExtensionUncertainConfigNeedsFreshSettlement(t *testing.T) {
	s, l, common := profileFixture(t)
	r, err := s.ProvisionClaudeProfile(snapshot(t, s).Revision, l.ID, []string{"/work"})
	if err != nil {
		t.Fatal(err)
	}
	source := `{"mcpServers":{"user":{"command":"server","env":{"SECRET":"mcp-secret"}}},"projects":{"/new":{"mcpServers":{"new":{"command":"new-server"}}}}}`
	if err = os.WriteFile(filepath.Join(common, ".claude.json"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	s.writeOps.syncDir = func(string) error { return syscall.EIO }
	if _, err = s.ProvisionClaudeProfile(r.Revision, l.ID, []string{"/new"}); !errors.Is(err, ErrDurabilityUncertain) {
		t.Fatal(err)
	}
	if got := snapshot(t, s).ClaudeProfiles[l.Identity].CWDs; !reflect.DeepEqual(got, []string{"/work"}) {
		t.Fatal("uncertain config admitted CWD")
	}
	fresh, err := OpenExisting(s.StateRoot())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fresh.Close() }()
	fresh.grantWriters = func([]string, []string) ([]processcontain.Identity, error) { return nil, nil }
	synced := false
	fresh.writeOps.syncDir = func(dir string) error {
		if strings.Contains(dir, "profiles/") {
			synced = true
		}
		if dir == "." && !synced {
			t.Fatal("registry published before profile settlement")
		}
		return syncRootDir(fresh.root, dir)
	}
	if _, err = fresh.ProvisionClaudeProfile(r.Revision, l.ID, []string{"/new"}); err != nil {
		t.Fatal(err)
	}
	if !synced {
		t.Fatal("fresh retry omitted visible config settlement")
	}
}
