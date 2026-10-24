package logins

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/processcontain"
	"github.com/Nathandela/swarm/internal/procstart"
)

func claudeAuthGrant(expiry time.Time, scopes []string) []byte {
	raw, _ := json.Marshal(map[string]any{"claudeAiOauth": map[string]any{"accessToken": "synthetic-access", "refreshToken": "synthetic-refresh", "expiresAt": expiry.UnixMilli(), "scopes": scopes}})
	return raw
}

func newClaudeAuthUnboundTest(t *testing.T) (*Store, Login, Login, ClaudeAuthRef) {
	t.Helper()
	s := testStore(t)
	s.keychain = nil
	a := add(t, s, ProviderClaude, "A", claudeCredential("a", "a"))
	credential := claudeCredential("b", "b")
	credential.Auth = claudeAuthGrant(time.Now().Add(time.Hour), []string{"user:inference", "user:profile"})
	b := add(t, s, ProviderClaude, "B", credential)
	activate(t, s, a)
	ref, created, err := s.BeginClaudeAuth(snapshot(t, s).Revision, b.ID)
	if err != nil || !created {
		t.Fatalf("begin helper: created=%v err=%v", created, err)
	}
	s.grantWriters = func(homes, sessions []string) ([]processcontain.Identity, error) {
		for _, home := range homes {
			if home == s.savedNativeHome(a.ID) || home == s.savedNativeHome(b.ID) || home == s.Home(ProviderClaude) {
				t.Fatal("helper custody scanned a shared native authority")
			}
		}
		if len(homes) == 0 || len(sessions) != 0 {
			t.Fatal("missing exact helper-only writer inventory")
		}
		return nil, nil
	}
	return s, a, b, ref
}

func newClaudeAuthTest(t *testing.T) (*Store, Login, Login, ClaudeAuthRef) {
	s, a, b, ref := newClaudeAuthUnboundTest(t)
	if err := s.BindClaudeAuthScope(ref, claudeAuthTestScope(t, ref)); err != nil {
		t.Fatal(err)
	}
	return s, a, b, ref
}

func currentClaudeAuthWorker(t *testing.T) processcontain.Identity {
	t.Helper()
	started, err := procstart.StartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	return processcontain.Identity{PID: os.Getpid(), StartTime: started}
}

func claimClaudeAuthTest(t *testing.T, s *Store, ref ClaudeAuthRef) ClaudeAuthAttempt {
	t.Helper()
	if result, err := s.ReadClaudeAuth(ref); err == nil && result.Scope == (ClaudeAuthScope{}) {
		if err := s.BindClaudeAuthScope(ref, claudeAuthTestScope(t, ref)); err != nil {
			t.Fatal(err)
		}
	}
	attempt, _, err := s.ClaimClaudeAuthAttempt(ref, currentClaudeAuthWorker(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(attempt.ConfigHome, 0700); err != nil {
		t.Fatal(err)
	}
	return attempt
}

func nativeClaudeAuthTest(t *testing.T, s *Store, ref ClaudeAuthRef, attempt ClaudeAuthAttempt) func() {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	cmd := exec.CommandContext(ctx, "/bin/sleep", "30")
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	var once sync.Once
	stop := func() { once.Do(func() { _ = cmd.Process.Kill(); _ = cmd.Wait(); cancel() }) }
	t.Cleanup(stop)
	started, err := procstart.StartTime(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordClaudeAuthNative(ref, attempt.Number, processcontain.Identity{PID: cmd.Process.Pid, StartTime: started}); err != nil {
		t.Fatal(err)
	}
	return stop
}

func writeClaudeAuthProfileTest(t *testing.T, attempt ClaudeAuthAttempt) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(attempt.ConfigHome, ".claude.json"), claudeAuthProfile("b", "org", attempt.StartedAt), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeAuthStableReceiptIsWarmUntilActualSelectionAndGenerationCheck(t *testing.T) {
	s, _, b, ref := newClaudeAuthTest(t)
	attempt := claimClaudeAuthTest(t, s, ref)
	stop := nativeClaudeAuthTest(t, s, ref, attempt)
	writeClaudeAuthProfileTest(t, attempt)
	if _, err := s.FinishClaudeAuthAttempt(ref, attempt.Number); !errors.Is(err, ErrInUse) {
		t.Fatalf("live helper accepted: %v", err)
	}
	stop()
	if err := s.RecordClaudeAuthContained(ref, attempt.Number); err != nil {
		t.Fatal(err)
	}
	result, err := s.FinishClaudeAuthAttempt(ref, attempt.Number)
	if err != nil || result.Phase != "ready" {
		t.Fatalf("native receipt: %+v %v", result, err)
	}
	if err := s.ValidateClaudeAuthReceipt(ref, snapshot(t, s).Providers[ProviderClaude].Epoch); err == nil {
		t.Fatal("warm target receipt claimed adoption while A selected")
	}
	runtime := activate(t, s, b)
	if err := s.ValidateClaudeAuthReceipt(ref, runtime.Epoch); err != nil {
		t.Fatalf("actual B selection rejected stable receipt: %v", err)
	}
	grant := claudeAuthGrant(time.Now().Add(2*time.Hour), []string{"user:inference", "user:profile"})
	if err := os.WriteFile(filepath.Join(attempt.AuthorityHome, ".credentials.json"), grant, 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateClaudeAuthReceipt(ref, runtime.Epoch); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("native grant rotation reused old receipt: %v", err)
	}
}

func TestClaudeAuthNativeRenewalRequiresFreshSecondAttemptAndPreservesBudget(t *testing.T) {
	s, _, _, ref := newClaudeAuthTest(t)
	first := claimClaudeAuthTest(t, s, ref)
	stop := nativeClaudeAuthTest(t, s, ref, first)
	writeClaudeAuthProfileTest(t, first)
	rotated := claudeAuthGrant(time.Now().Add(2*time.Hour), []string{"user:inference", "user:profile"})
	if err := os.WriteFile(filepath.Join(first.AuthorityHome, ".credentials.json"), rotated, 0600); err != nil {
		t.Fatal(err)
	}
	stop()
	if err := s.RecordClaudeAuthContained(ref, first.Number); err != nil {
		t.Fatal(err)
	}
	result, err := s.FinishClaudeAuthAttempt(ref, first.Number)
	if !errors.Is(err, ErrPending) || result.Phase != "retry" {
		t.Fatalf("rotation must require confirmation: %+v %v", result, err)
	}
	if _, _, err := s.ClaimClaudeAuthAttempt(ref, processcontain.Identity{PID: 1, StartTime: 1}); err == nil {
		t.Fatal("different worker acquired confirmation")
	}
	reopened, err := Open(s.StateRoot())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	reopened.keychain, reopened.grantWriters = nil, s.grantWriters
	second := claimClaudeAuthTest(t, reopened, ref)
	if second.Number != 2 || second.ConfigHome == first.ConfigHome || !second.Deadline.Equal(first.Deadline) {
		t.Fatalf("restart reset confirmation budget/config: first=%+v second=%+v", first, second)
	}
	stop = nativeClaudeAuthTest(t, reopened, ref, second)
	writeClaudeAuthProfileTest(t, second)
	stop()
	if err := reopened.RecordClaudeAuthContained(ref, second.Number); err != nil {
		t.Fatal(err)
	}
	if result, err := reopened.FinishClaudeAuthAttempt(ref, second.Number); err != nil || result.Phase != "ready" {
		t.Fatalf("stable confirmation failed: %+v %v", result, err)
	}
	if _, _, err := reopened.ClaimClaudeAuthAttempt(ref, currentClaudeAuthWorker(t)); !errors.Is(err, ErrInUse) {
		t.Fatalf("third native attempt admitted: %v", err)
	}
	if raw, err := os.ReadFile(filepath.Join(first.AuthorityHome, ".credentials.json")); err != nil || string(raw) != string(rotated) {
		t.Fatal("helper rolled back native credential rotation")
	}
}

func TestClaudeAuthUnknownLaunchAndExpiredDeadlineNeverReleaseCustody(t *testing.T) {
	s, _, b, ref := newClaudeAuthTest(t)
	attempt := claimClaudeAuthTest(t, s, ref)
	intent, err := s.readClaudeAuthIntent(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	intent.Deadline = time.Now().Add(-time.Second)
	intent.StartedAt = intent.Deadline.Add(-time.Second)
	if err := s.writeClaudeAuthIntent(intent); err != nil {
		t.Fatal(err)
	}
	if again, created, err := s.BeginClaudeAuth(snapshot(t, s).Revision, b.ID); err != nil || created || again != ref {
		t.Fatalf("timeout recreated launch: %+v %v %v", again, created, err)
	}
	if _, err := s.FinishClaudeAuthAttempt(ref, attempt.Number); !errors.Is(err, ErrInUse) {
		t.Fatalf("missing native PID treated as stopped: %v", err)
	}
	if err := retireClaudeAuthTest(t, s, ref); !errors.Is(err, ErrInUse) {
		t.Fatalf("unknown launch retired: %v", err)
	}
	if _, err := s.Remove(snapshot(t, s).Revision, b.ID); !errors.Is(err, ErrInUse) {
		t.Fatalf("timeout released canonical authority: %v", err)
	}
}

func TestClaudeAuthPreseededConfigCannotProduceReceipt(t *testing.T) {
	s, _, b, ref := newClaudeAuthTest(t)
	intent, err := s.readClaudeAuthIntent(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	config := s.claudeAuthConfig(intent, 1)
	if err := os.MkdirAll(config, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, ".claude.json"), claudeAuthProfile("b", "org", time.Now()), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ClaimClaudeAuthAttempt(ref, currentClaudeAuthWorker(t)); !errors.Is(err, ErrConflict) {
		t.Fatalf("seeded config admitted: %v", err)
	}
}

func TestClaudeAuthExcludesAllSwarmCredentialMutationSeams(t *testing.T) {
	s, _, b, _ := newClaudeAuthTest(t)
	c := claudeCredential("b", "replacement")
	checks := map[string]func() error{
		"saved":            func() error { return s.writeSaved(b, c) },
		"metadata":         func() error { return s.writeSavedMetadata(b, c) },
		"native-authority": func() error { return s.writeClaudeAuth(b.ID, c.Auth) },
		"capture":          func() error { _, _, err := s.captureCredential(b); return err },
		"install":          func() error { return s.installCredential(ProviderClaude, c) },
		"stage":            func() error { return s.stageCredential(strings.Repeat("e", 32), b, c, "") },
		"relogin":          func() error { _, _, err := s.Relogin(snapshot(t, s).Revision, b.ID, c); return err },
	}
	for name, check := range checks {
		t.Run(name, func(t *testing.T) {
			if err := check(); !errors.Is(err, ErrInUse) {
				t.Fatalf("helper authority use lost exclusion: %v", err)
			}
		})
	}
	c.Auth = claudeAuthGrant(b.ExpiresAt, []string{"user:inference", "user:profile"})
	intent, _ := json.Marshal(reloginIntent{Login: b, Credential: c})
	if _, err := durableReplace(s.root, reloginPath(b.ID), intent, writeOps{}); err != nil {
		t.Fatal(err)
	}
	r := snapshot(t, s)
	if _, err := s.reconcileRelogins(&r); !errors.Is(err, ErrInUse) {
		t.Fatalf("relogin recovery overwrote helper authority: %v", err)
	}
}

func TestClaudeAuthIntentENOSPCNeverGrantsLaunchAuthority(t *testing.T) {
	s := testStore(t)
	b := add(t, s, ProviderClaude, "B", claudeCredential("b", "b"))
	s.writeOps.rename = func(_, _ string) error { return syscall.ENOSPC }
	if _, created, err := s.BeginClaudeAuth(snapshot(t, s).Revision, b.ID); !errors.Is(err, syscall.ENOSPC) || created {
		t.Fatalf("failed durable intent allowed launch: created=%v err=%v", created, err)
	}
	if _, err := s.root.Lstat(claudeAuthPath(b.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("intent exists before schema fence: %v", err)
	}
}

func TestClaudeAuthDeadWorkerCannotInventPositiveContainment(t *testing.T) {
	s, _, b, ref := newClaudeAuthTest(t)
	attempt := claimClaudeAuthTest(t, s, ref)
	stop := nativeClaudeAuthTest(t, s, ref, attempt)
	writeClaudeAuthProfileTest(t, attempt)
	stop()
	intent, err := s.readClaudeAuthIntent(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	intent.Worker = processcontain.Identity{PID: 999999999, StartTime: 1}
	intent.Scope.Worker = intent.Worker
	if err := s.writeClaudeAuthIntent(intent); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishClaudeAuthAttempt(ref, attempt.Number); !errors.Is(err, ErrInUse) {
		t.Fatalf("dead worker and empty inventory fabricated containment: %v", err)
	}
}

func TestClaudeAuthRejectsMalformedDurableOwnership(t *testing.T) {
	s, _, b, ref := newClaudeAuthTest(t)
	_ = claimClaudeAuthTest(t, s, ref)
	base, err := s.readClaudeAuthIntent(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		edit func(*claudeAuthIntent)
	}{
		{"partial-native", func(i *claudeAuthIntent) { i.Native.PID = 123 }},
		{"negative-native", func(i *claudeAuthIntent) { i.Native.PID, i.Native.StartTime = -1, 1 }},
		{"unbounded-deadline", func(i *claudeAuthIntent) { i.Deadline = time.Now().Add(time.Hour) }},
		{"nonterminal-expiry", func(i *claudeAuthIntent) { i.ExpiresAt = time.Now().Add(time.Hour) }},
		{"future-start", func(i *claudeAuthIntent) {
			i.StartedAt = time.Now().Add(time.Hour)
			i.Deadline = i.StartedAt.Add(time.Second)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			intent := base
			test.edit(&intent)
			if err := s.writeClaudeAuthIntent(intent); err != nil {
				t.Fatal(err)
			}
			if _, err := s.ReadClaudeAuth(ref); err == nil {
				t.Fatal("malformed durable helper custody accepted")
			}
		})
	}
}

func TestClaudeAuthCompletionRejectsGrantChangeDuringReceiptPublication(t *testing.T) {
	s, _, b, ref := newClaudeAuthTest(t)
	attempt := claimClaudeAuthTest(t, s, ref)
	stop := nativeClaudeAuthTest(t, s, ref, attempt)
	writeClaudeAuthProfileTest(t, attempt)
	stop()
	if err := s.RecordClaudeAuthContained(ref, attempt.Number); err != nil {
		t.Fatal(err)
	}
	rotated := false
	s.writeOps.rename = func(old, next string) error {
		raw, err := s.root.ReadFile(old)
		if err != nil {
			return err
		}
		var intent claudeAuthIntent
		if err := json.Unmarshal(raw, &intent); err != nil {
			return err
		}
		if intent.Phase == "ready" && !rotated {
			rotated = true
			if err := os.WriteFile(filepath.Join(s.savedNativeHome(b.ID), ".credentials.json"), claudeAuthGrant(time.Now().Add(2*time.Hour), []string{"user:inference", "user:profile"}), 0600); err != nil {
				return err
			}
		}
		return s.root.Rename(old, next)
	}
	if result, err := s.FinishClaudeAuthAttempt(ref, attempt.Number); !errors.Is(err, ErrPending) || result.Phase != "retry" || !rotated {
		t.Fatalf("publication raced canonical renewal: %+v err=%v rotated=%v", result, err, rotated)
	}
}

func TestClaudeAuthMissingProfileNeverBecomesSuccessfulOnDuplicateFinish(t *testing.T) {
	s, _, _, ref := newClaudeAuthTest(t)
	attempt := claimClaudeAuthTest(t, s, ref)
	stop := nativeClaudeAuthTest(t, s, ref, attempt)
	stop()
	if err := s.RecordClaudeAuthContained(ref, attempt.Number); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 2; n++ {
		if result, err := s.FinishClaudeAuthAttempt(ref, attempt.Number); err == nil || result.Phase != "failed" {
			t.Fatalf("missing profile reported success on finish%d: %+v %v", n, result, err)
		}
	}
}

func TestClaudeAuthSecondChurnNeedsExplicitResolvedRetirementBeforeNewJob(t *testing.T) {
	s, _, b, ref := newClaudeAuthTest(t)
	var deadline time.Time
	for n := 1; n <= 2; n++ {
		attempt := claimClaudeAuthTest(t, s, ref)
		if n == 1 {
			deadline = attempt.Deadline
		}
		stop := nativeClaudeAuthTest(t, s, ref, attempt)
		writeClaudeAuthProfileTest(t, attempt)
		stop()
		if err := s.RecordClaudeAuthContained(ref, attempt.Number); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(attempt.AuthorityHome, ".credentials.json"), claudeAuthGrant(time.Now().Add(time.Duration(n+1)*time.Hour), []string{"user:inference", "user:profile"}), 0600); err != nil {
			t.Fatal(err)
		}
		result, err := s.FinishClaudeAuthAttempt(ref, n)
		want := "retry"
		if n == 2 {
			want = "failed"
		}
		if !errors.Is(err, ErrPending) || result.Phase != want || !result.Deadline.Equal(deadline) {
			t.Fatalf("churn reset attempt budget: %+v %v", result, err)
		}
	}
	if _, created, err := s.BeginClaudeAuth(snapshot(t, s).Revision, b.ID); err != nil || created {
		t.Fatalf("terminal failure silently reset budget: %v %v", created, err)
	}
	if err := retireClaudeAuthTest(t, s, ref); err != nil {
		t.Fatal(err)
	}
	if _, err := s.root.Lstat(claudeAuthPayload(b.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("resolved helper artifacts retained: %v", err)
	}
	next, created, err := s.BeginClaudeAuth(snapshot(t, s).Revision, b.ID)
	if err != nil || !created || next.Nonce == ref.Nonce {
		t.Fatalf("explicitly new finite job unavailable: %+v %v %v", next, created, err)
	}
}

func TestClaudeAuthSavedRemovalRetiresOnlyPositivelyStoppedTerminalJob(t *testing.T) {
	s, _, b, ref := newClaudeAuthTest(t)
	attempt := claimClaudeAuthTest(t, s, ref)
	stop := nativeClaudeAuthTest(t, s, ref, attempt)
	writeClaudeAuthProfileTest(t, attempt)
	stop()
	if err := s.RecordClaudeAuthContained(ref, attempt.Number); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishClaudeAuthAttempt(ref, attempt.Number); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Remove(snapshot(t, s).Revision, b.ID); !errors.Is(err, ErrPending) {
		t.Fatalf("saved removal bypassed unresolved unit custody: %v", err)
	}
	if held, err := s.ReadClaudeAuth(ref); err != nil || held.Phase != "retiring" || held.Scope == (ClaudeAuthScope{}) {
		t.Fatal("removal lost unit recovery evidence")
	}
	if err := retireClaudeAuthTest(t, s, ref); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Remove(snapshot(t, s).Revision, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.root.Lstat(claudeAuthPayload(b.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed saved login leaked a helper job: %v", err)
	}
}

func TestClaudeAuthMissingIntentCannotReconstructLaunchOrReleaseAuthority(t *testing.T) {
	s, _, b, ref := newClaudeAuthTest(t)
	_ = claimClaudeAuthTest(t, s, ref)
	if err := s.root.Remove(claudeAuthPath(b.ID)); err != nil {
		t.Fatal(err)
	}
	if _, created, err := s.BeginClaudeAuth(snapshot(t, s).Revision, b.ID); err == nil || created {
		t.Fatalf("missing intent recreated launch authority: %v %v", created, err)
	}
	if _, err := s.Remove(snapshot(t, s).Revision, b.ID); err == nil {
		t.Fatal("missing intent released possibly owned authority")
	}
}

func TestClaudeAuthSidecarENOSPCNeverAuthorizesLaunchAndFirstIssuerCanRetry(t *testing.T) {
	s := testStore(t)
	b := add(t, s, ProviderClaude, "B", claudeCredential("b", "b"))
	s.writeOps.rename = func(old, next string) error {
		if next == claudeAuthPath(b.ID) {
			return syscall.ENOSPC
		}
		return s.root.Rename(old, next)
	}
	if _, created, err := s.BeginClaudeAuth(snapshot(t, s).Revision, b.ID); !errors.Is(err, syscall.ENOSPC) || created {
		t.Fatalf("sidecar failure granted launch: created=%v err=%v", created, err)
	}
	if snapshot(t, s).SchemaVersion != claudeAuthSchemaVersion {
		t.Fatal("intent preceded old-reader fence")
	}
	s.writeOps = writeOps{}
	if _, created, err := s.BeginClaudeAuth(snapshot(t, s).Revision, b.ID); err != nil || !created {
		t.Fatalf("known prepublication failure stranded first issuer: %v %v", created, err)
	}
}

func claudeAuthProfile(account, org string, stamp time.Time) []byte {
	raw, _ := json.Marshal(map[string]any{"oauthAccount": map[string]any{"accountUuid": account, "organizationUuid": org, "profileFetchedAt": stamp.UnixMilli()}})
	return raw
}

func TestClaudeAuthFreshReceiptRequiresPositiveNativeIdentityAndUsableGrant(t *testing.T) {
	started := time.Now().UTC().Truncate(time.Millisecond)
	ended := started.Add(time.Second)
	expires := ended.Add(time.Hour)
	profile := claudeAuthProfile("b", "org", started)
	identity, err := ClaudeIdentity(profile)
	if err != nil {
		t.Fatal(err)
	}
	grant := claudeAuthGrant(expires, []string{"user:inference", "user:profile"})
	if got, err := validateClaudeAuthReceipt(profile, grant, identity, started, ended); err != nil || !got.Equal(expires) {
		t.Fatalf("fresh native receipt rejected: expiry=%v err=%v", got, err)
	}
	for _, test := range []struct {
		name    string
		profile []byte
		grant   []byte
	}{
		{"initialize-only", nil, grant},
		{"wrong-account", claudeAuthProfile("foreign", "org", started), grant},
		{"wrong-org", claudeAuthProfile("b", "foreign", started), grant},
		{"old-stamp", claudeAuthProfile("b", "org", started.Add(-time.Millisecond)), grant},
		{"future-stamp", claudeAuthProfile("b", "org", ended.Add(time.Millisecond)), grant},
		{"malformed", []byte(`{"oauthAccount":`), grant},
		{"missing-stamp", []byte(`{"oauthAccount":{"accountUuid":"b","organizationUuid":"org"}}`), grant},
		{"scope", profile, claudeAuthGrant(expires, []string{"user:profile"})},
		{"expiry", profile, claudeAuthGrant(ended.Add(5*time.Minute), []string{"user:profile", "user:inference"})},
		{"duplicate", profile, []byte(`{"claudeAiOauth":{},"claudeAiOauth":{}}`)},
		{"profile-key-alias", []byte(strings.Replace(string(profile), "oauthAccount", "OAuthAccount", 1)), grant},
		{"grant-key-alias", profile, []byte(strings.Replace(string(grant), "claudeAiOauth", "ClaudeAiOauth", 1))},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := validateClaudeAuthReceipt(test.profile, test.grant, identity, started, ended); err == nil {
				t.Fatal("unproven authentication accepted")
			}
		})
	}
}

func TestClaudeAuthConsumptionChecksCanonicalExpiryRatherThanOnlyReceiptMetadata(t *testing.T) {
	s, _, b, ref := newClaudeAuthTest(t)
	attempt := claimClaudeAuthTest(t, s, ref)
	stop := nativeClaudeAuthTest(t, s, ref, attempt)
	writeClaudeAuthProfileTest(t, attempt)
	stop()
	if err := s.RecordClaudeAuthContained(ref, attempt.Number); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishClaudeAuthAttempt(ref, attempt.Number); err != nil {
		t.Fatal(err)
	}
	intent, err := s.readClaudeAuthIntent(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	intent.ExpiresAt = intent.ExpiresAt.Add(time.Hour)
	if err := s.writeClaudeAuthIntent(intent); err != nil {
		t.Fatal(err)
	}
	runtime := activate(t, s, b)
	if err := s.ValidateClaudeAuthReceipt(ref, runtime.Epoch); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("receipt metadata replaced canonical expiry: %v", err)
	}
}

func TestClaudeAuthConcurrentBeginsCoalesceAcrossStoreHandles(t *testing.T) {
	s := testStore(t)
	s.keychain = nil
	b := add(t, s, ProviderClaude, "B", claudeCredential("b", "b"))
	other, err := Open(s.StateRoot())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	other.keychain = nil
	revision := snapshot(t, s).Revision
	type outcome struct {
		ref     ClaudeAuthRef
		created bool
		err     error
	}
	start, results := make(chan struct{}), make(chan outcome, 2)
	for _, store := range []*Store{s, other} {
		go func() {
			<-start
			ref, created, err := store.BeginClaudeAuth(revision, b.ID)
			results <- outcome{ref, created, err}
		}()
	}
	close(start)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.ref != second.ref || first.created == second.created {
		t.Fatalf("competing callbacks obtained multiple launch authorities: %+v %+v", first, second)
	}
}

func TestClaudeAuthBeginAndReloginSerializeCanonicalAuthorityUse(t *testing.T) {
	s := testStore(t)
	s.keychain = nil
	b := add(t, s, ProviderClaude, "B", claudeCredential("b", "b"))
	other, err := Open(s.StateRoot())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	other.keychain = nil
	revision := snapshot(t, s).Revision
	start, result := make(chan struct{}), make(chan error, 1)
	go func() {
		<-start
		_, _, err := other.Relogin(revision, b.ID, claudeCredential("b", "replacement"))
		result <- err
	}()
	close(start)
	_, created, beginErr := s.BeginClaudeAuth(revision, b.ID)
	reloginErr := <-result
	if created {
		if beginErr != nil || !errors.Is(reloginErr, ErrInUse) {
			t.Fatalf("helper did not exclude relogin: %v %v", beginErr, reloginErr)
		}
	} else if !errors.Is(beginErr, ErrConflict) || reloginErr != nil {
		t.Fatalf("relogin did not fence old helper revision: %v %v", beginErr, reloginErr)
	}
}

func TestClaudeAuthPartialPayloadCleanupKeepsDurableAuthorityAcrossReopen(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses the fixture's directory permissions")
	}
	s, _, b, ref := newClaudeAuthTest(t)
	attempt := claimClaudeAuthTest(t, s, ref)
	stop := nativeClaudeAuthTest(t, s, ref, attempt)
	writeClaudeAuthProfileTest(t, attempt)
	stop()
	if err := s.RecordClaudeAuthContained(ref, attempt.Number); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishClaudeAuthAttempt(ref, attempt.Number); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(attempt.ConfigHome, "blocked")
	if err := os.Mkdir(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocked, "left"), []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blocked, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0700) })
	if err := retireClaudeAuthTest(t, s, ref); err == nil {
		t.Fatal("real partial cleanup unexpectedly succeeded")
	}
	if _, err := s.readClaudeAuthIntent(b.ID); err != nil {
		t.Fatalf("partial cleanup deleted its authority first: %v", err)
	}
	other, err := Open(s.StateRoot())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	if err := os.Chmod(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	if err := retireClaudeAuthTest(t, other, ref); err != nil {
		t.Fatalf("reopened partial cleanup lost retirement authority: %v", err)
	}
}

func TestClaudeAuthWarmTargetBeginIsOneDurableLaunchClaim(t *testing.T) {
	s := testStore(t)
	a := add(t, s, ProviderClaude, "A", claudeCredential("a", "a"))
	b := add(t, s, ProviderClaude, "B", claudeCredential("b", "b"))
	activate(t, s, a)
	ref, created, err := s.BeginClaudeAuth(snapshot(t, s).Revision, b.ID)
	if err != nil || !created || ref.LoginID != b.ID || ref.Identity != b.Identity || ref.CredentialRevision != b.CredentialRevision {
		t.Fatalf("parked target must warm while A stays selected: %+v created=%v err=%v", ref, created, err)
	}
	again, created, err := s.BeginClaudeAuth(snapshot(t, s).Revision, b.ID)
	if err != nil || created || again != ref {
		t.Fatalf("repeat granted another launch: %+v created=%v err=%v", again, created, err)
	}
	other, err := Open(s.StateRoot())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	again, created, err = other.BeginClaudeAuth(snapshot(t, s).Revision, b.ID)
	if err != nil || created || again != ref {
		t.Fatalf("restart reconstructed launch authority: %+v created=%v err=%v", again, created, err)
	}
	_ = claimClaudeAuthTest(t, s, ref)
	if _, err := s.Remove(snapshot(t, s).Revision, b.ID); !errors.Is(err, ErrInUse) {
		t.Fatalf("unpublished helper lost custody on removal: %v", err)
	}
}

func TestClaudeAuthNativeNoStartRequiresOriginalUnpersistedIssuer(t *testing.T) {
	for _, handed := range []bool{false, true} {
		s, _, _, ref := newClaudeAuthTest(t)
		attempt, launch, err := s.ClaimClaudeAuthAttempt(ref, currentClaudeAuthWorker(t))
		if err != nil || launch == nil {
			t.Fatalf("native issuer missing: %v", err)
		}
		var refusal error
		if handed {
			if err := launch.Handoff(); err != nil {
				t.Fatal(err)
			}
			if err := s.AbortClaudeAuthLaunch(launch, nil); err == nil {
				t.Fatal("handoff inferred no start from absent PID")
			}
			if err := s.AbortClaudeAuthLaunch(launch, context.DeadlineExceeded); err == nil {
				t.Fatal("generic timeout authorized no-start")
			}
			if err := s.AbortClaudeAuthLaunch(launch, ClaudeAuthLaunchNotStarted(ref, 2, os.ErrPermission)); err == nil {
				t.Fatal("another ordinal's refusal authorized no-start")
			}
			refusal = ClaudeAuthLaunchNotStarted(ref, attempt.Number, os.ErrPermission)
		}
		if err := s.AbortClaudeAuthLaunch(launch, refusal); err != nil {
			t.Fatal(err)
		}
		if err := s.AbortClaudeAuthLaunch(launch, refusal); err == nil {
			t.Fatal("no-start issuer reused")
		}
		if err := retireClaudeAuthTest(t, s, ref); err != nil {
			t.Fatal(err)
		}
	}
}

func TestClaudeAuthPreparedRevocationFencesLateClaimAndNewNonce(t *testing.T) {
	s, _, b, ref := newClaudeAuthTest(t)
	other, err := Open(s.StateRoot())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	other.keychain = nil
	if err := retireClaudeAuthTest(t, other, ref); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ClaimClaudeAuthAttempt(ref, currentClaudeAuthWorker(t)); err == nil {
		t.Fatal("revoked prepared worker could claim")
	}
	next, created, err := s.BeginClaudeAuth(snapshot(t, s).Revision, b.ID)
	if err != nil || !created || next.Nonce == ref.Nonce {
		t.Fatalf("new job after prepared revocation: %+v %v %v", next, created, err)
	}
	if _, _, err := other.ClaimClaudeAuthAttempt(ref, currentClaudeAuthWorker(t)); !errors.Is(err, ErrConflict) {
		t.Fatalf("old worker crossed into new nonce: %v", err)
	}
}

func TestClaudeAuthClaimAndPreparedRevocationHaveOneWinner(t *testing.T) {
	s, _, _, ref := newClaudeAuthTest(t)
	other, err := Open(s.StateRoot())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	other.keychain = nil
	start, result := make(chan struct{}), make(chan error, 1)
	go func() { <-start; result <- retireClaudeAuthTest(t, other, ref) }()
	close(start)
	_, launch, claimErr := s.ClaimClaudeAuthAttempt(ref, currentClaudeAuthWorker(t))
	retireErr := <-result
	if claimErr == nil {
		if !errors.Is(retireErr, ErrInUse) {
			t.Fatalf("running native-zero claim was revoked: %v", retireErr)
		}
		if err := s.AbortClaudeAuthLaunch(launch, nil); err != nil {
			t.Fatal(err)
		}
	} else if retireErr != nil {
		t.Fatalf("claim and revocation both lost: %v %v", claimErr, retireErr)
	}
}

func TestClaudeAuthKnownClaimWriteFailureRollsBackOnlyNewEmptyConfig(t *testing.T) {
	s, _, _, ref := newClaudeAuthTest(t)
	s.writeOps.rename = func(_, _ string) error { return syscall.ENOSPC }
	if _, launch, err := s.ClaimClaudeAuthAttempt(ref, currentClaudeAuthWorker(t)); !errors.Is(err, syscall.ENOSPC) || launch != nil {
		t.Fatalf("claim failure granted native launch: %v %v", launch, err)
	}
	s.writeOps = writeOps{}
	if _, launch, err := s.ClaimClaudeAuthAttempt(ref, currentClaudeAuthWorker(t)); err != nil || launch == nil {
		t.Fatalf("known same-call config rollback was lost: %v", err)
	}
}

func TestClaudeAuthExistingAttachDoesNotBootstrapMissingStore(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenExisting(root)
	if store != nil {
		_ = store.Close()
	}
	if err == nil {
		t.Fatal("existing-only worker attach initialized a missing store")
	}
	if _, err := os.Lstat(filepath.Join(root, "logins")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preclaim attach created directories: %v", err)
	}
}

func TestClaudeAuthExistingAttachCanClaimWithoutBootstrapWrites(t *testing.T) {
	s, _, _, ref := newClaudeAuthTest(t)
	for _, dir := range []string{"enrollment", "provider/codex/native"} {
		if err := s.root.Remove(dir); err != nil {
			t.Fatal(err)
		}
	}
	other, err := OpenExisting(s.StateRoot())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	other.keychain = nil
	for _, dir := range []string{"enrollment", "provider/codex/native"} {
		if _, err := s.root.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("existing-only attach recreated %s: %v", dir, err)
		}
	}
	if _, launch, err := other.ClaimClaudeAuthAttempt(ref, currentClaudeAuthWorker(t)); err != nil || launch == nil {
		t.Fatalf("existing-only writable lock cannot claim: %v", err)
	}
}

func TestClaudeAuthContainedRetryCanRetireAfterWorkerDeath(t *testing.T) {
	s, _, b, ref := newClaudeAuthTest(t)
	attempt := claimClaudeAuthTest(t, s, ref)
	stop := nativeClaudeAuthTest(t, s, ref, attempt)
	writeClaudeAuthProfileTest(t, attempt)
	stop()
	if err := s.RecordClaudeAuthContained(ref, attempt.Number); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(attempt.AuthorityHome, ".credentials.json"), claudeAuthGrant(time.Now().Add(2*time.Hour), []string{"user:profile", "user:inference"}), 0600); err != nil {
		t.Fatal(err)
	}
	if result, err := s.FinishClaudeAuthAttempt(ref, attempt.Number); !errors.Is(err, ErrPending) || result.Phase != "retry" {
		t.Fatalf("expected contained retry: %+v %v", result, err)
	}
	intent, err := s.readClaudeAuthIntent(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	intent.Worker = processcontain.Identity{PID: 999999999, StartTime: 1}
	intent.Scope.Worker = intent.Worker
	if err := s.writeClaudeAuthIntent(intent); err != nil {
		t.Fatal(err)
	}
	if err := retireClaudeAuthTest(t, s, ref); err != nil {
		t.Fatalf("durable positive containment became unretirable after worker death: %v", err)
	}
}

func TestClaudeAuthLateOldRetirementNeverDeletesNewJob(t *testing.T) {
	s, _, b, ref := newClaudeAuthTest(t)
	attempt := claimClaudeAuthTest(t, s, ref)
	stop := nativeClaudeAuthTest(t, s, ref, attempt)
	writeClaudeAuthProfileTest(t, attempt)
	stop()
	if err := s.RecordClaudeAuthContained(ref, attempt.Number); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishClaudeAuthAttempt(ref, attempt.Number); err != nil {
		t.Fatal(err)
	}
	oldPayload := filepath.Join(claudeAuthPayload(b.ID), ref.Nonce)
	entered, release, result := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var released sync.Once
	joined := false
	finish := func() {
		released.Do(func() { close(release) })
		if !joined {
			<-result
			joined = true
		}
	}
	var calls atomic.Int32
	s.writeOps.remove = func(name string) error {
		if name == oldPayload {
			if calls.Add(1) == 1 {
				close(entered)
				<-release
			}
			return s.root.RemoveAll(name)
		}
		return s.root.Remove(name)
	}
	go func() { result <- retireClaudeAuthTest(t, s, ref) }()
	t.Cleanup(finish)
	<-entered
	if err := retireClaudeAuthTest(t, s, ref); err != nil {
		finish()
		t.Fatal(err)
	}
	next, created, err := s.BeginClaudeAuth(snapshot(t, s).Revision, b.ID)
	if err != nil || !created {
		finish()
		t.Fatalf("new generation unavailable: %v %v", created, err)
	}
	newAttempt := claimClaudeAuthTest(t, s, next)
	sentinel := filepath.Join(newAttempt.ConfigHome, "keep")
	if err := os.WriteFile(sentinel, []byte("new generation"), 0600); err != nil {
		t.Fatal(err)
	}
	finish()
	if _, err := s.ReadClaudeAuth(next); err != nil {
		t.Fatalf("old retirement deleted new authoritative intent: %v", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("old retirement deleted new payload: %v", err)
	}
}

func TestClaudeAuthConsumptionHonorsAuthStateAndMigrationFence(t *testing.T) {
	s, _, b, ref := newClaudeAuthTest(t)
	attempt := claimClaudeAuthTest(t, s, ref)
	stop := nativeClaudeAuthTest(t, s, ref, attempt)
	writeClaudeAuthProfileTest(t, attempt)
	stop()
	if err := s.RecordClaudeAuthContained(ref, attempt.Number); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishClaudeAuthAttempt(ref, attempt.Number); err != nil {
		t.Fatal(err)
	}
	runtime := activate(t, s, b)
	for _, state := range []string{AuthPaused, AuthNeedsLogin} {
		if _, err := s.UpdateAvailability(snapshot(t, s).Revision, b.ID, state, false, time.Time{}); err != nil {
			t.Fatal(err)
		}
		if err := s.ValidateClaudeAuthReceipt(ref, runtime.Epoch); !errors.Is(err, ErrIneligible) {
			t.Fatalf("auth state %s bypassed at consumption: %v", state, err)
		}
	}
	if _, err := s.UpdateAvailability(snapshot(t, s).Revision, b.ID, AuthReady, false, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetMigrationPending(snapshot(t, s).Revision, true); err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateClaudeAuthReceipt(ref, runtime.Epoch); !errors.Is(err, ErrPending) {
		t.Fatalf("migration bypassed at consumption: %v", err)
	}
	if result, err := s.ReadClaudeAuth(ref); err != nil || result.Phase != "ready" {
		t.Fatalf("historical account identity was conflated with admission: %+v %v", result, err)
	}
}

func TestClaudeAuthUncertainRunningWriteNeverRestoresPrepared(t *testing.T) {
	s, _, b, ref := newClaudeAuthTest(t)
	visible := false
	s.writeOps.rename = func(old, next string) error {
		if err := s.root.Rename(old, next); err != nil {
			return err
		}
		visible = next == claudeAuthPath(b.ID)
		return nil
	}
	s.writeOps.syncDir = func(dir string) error {
		if visible {
			return errors.New("synthetic post-rename sync failure")
		}
		return syncRootDir(s.root, dir)
	}
	if _, launch, err := s.ClaimClaudeAuthAttempt(ref, currentClaudeAuthWorker(t)); !errors.Is(err, ErrDurabilityUncertain) || launch != nil {
		t.Fatalf("uncertain claim granted effects: %v %v", launch, err)
	}
	other, err := OpenExisting(s.StateRoot())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	if result, err := other.ReadClaudeAuth(ref); err != nil || result.Phase != "running" {
		t.Fatalf("uncertain running claim became prepared: %+v %v", result, err)
	}
	if err := retireClaudeAuthTest(t, other, ref); !errors.Is(err, ErrInUse) {
		t.Fatalf("uncertain running/native-zero was revoked: %v", err)
	}
}

func TestClaudeAuthIntentParserRejectsAliasesAndUnknownFields(t *testing.T) {
	s, _, b, ref := newClaudeAuthTest(t)
	raw, err := s.root.ReadFile(claudeAuthPath(b.ID))
	if err != nil {
		t.Fatal(err)
	}
	for _, changed := range []string{
		strings.Replace(string(raw), `"phase":`, `"Phase":`, 1),
		strings.Replace(string(raw), `"nonce":`, `"Nonce":`, 1),
		strings.Replace(string(raw), `"pid":`, `"PID":`, 1),
		strings.Replace(string(raw), `"version":`, `"unknown":0,"version":`, 1),
	} {
		if err := os.WriteFile(filepath.Join(s.path, claudeAuthPath(b.ID)), []byte(changed), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ReadClaudeAuth(ref); err == nil {
			t.Fatal("ambiguous persisted authority accepted")
		}
	}
}

// Existing Store fixtures have a synthetic unit, not an actual systemd service.
// Supply its trusted retirement observation explicitly; lifecycle tests exercise
// the real observer separately. Never mark a running native claim stopped.
func retireClaudeAuthTest(t *testing.T, s *Store, ref ClaudeAuthRef) error {
	t.Helper()
	intent, err := s.readClaudeAuthIntent(ref.LoginID)
	if err == nil && intent.Ref == ref && intent.Scope != (ClaudeAuthScope{}) && !intent.UnitRetired && intent.Phase != "running" {
		if intent.Phase == "prepared" {
			if _, err := s.RevokeClaudeAuthPrepared(ref); err != nil {
				return err
			}
			intent, err = s.readClaudeAuthIntent(ref.LoginID)
			if err != nil {
				return err
			}
		}
		if s.proveClaudeAuthStopped(intent) == nil {
			intent.UnitRetired = true
			if err := s.writeClaudeAuthIntent(intent); err != nil {
				return err
			}
		}
	}
	return s.RetireClaudeAuth(ref)
}
