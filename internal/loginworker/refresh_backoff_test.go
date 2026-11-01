package loginworker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/procstart"
)

func retainedRefreshFailure(t *testing.T, f *jobFiles, c RefreshConfig, creator ProcessIdentity, code string, failures uint8, retry time.Time) RefreshProgress {
	t.Helper()
	p := RefreshProgress{Version: refreshRecordVersion, Grant: c.Grant, Creator: creator, LaunchNonce: c.LaunchNonce, Generation: c.Generation, Worker: ProcessIdentity{PID: 999999999, StartTime: 1}, Native: ProcessIdentity{PID: 999999998, StartTime: 1}, Phase: PhaseFailed, WritersStopped: true, ErrorCode: code, Failures: failures, RetryAfter: retry}
	if err := f.write(refreshConfigFile, c); err != nil {
		t.Fatal(err)
	}
	if err := f.write(refreshProgressFile, p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCodexRefreshBackoffAdmissionDurableGate(t *testing.T) {
	f, c, creator, _ := refreshAdmissionFixture(t)
	retainedRefreshFailure(t, f, c, creator, "refresh_account_read_failed", 3, time.Now().Add(time.Hour).UTC())
	before, err := readPrivate(f.root, refreshProgressFile, 4096)
	if err != nil {
		t.Fatal(err)
	}
	c.Generation++
	c.LaunchNonce = strings.Repeat("3", 32)
	if _, err := prepareCodexRefresh(f, c, creator); !errors.Is(err, logins.ErrPending) {
		t.Fatalf("future durable retry gate: %v", err)
	}
	after, err := readPrivate(f.root, refreshProgressFile, 4096)
	if err != nil || string(before) != string(after) {
		t.Fatal("early caller replaced durable gate")
	}
	p := retainedRefreshFailure(t, f, c, creator, "refresh_account_read_failed", 3, time.Now().Add(-time.Second).UTC())
	c.Generation++
	c.LaunchNonce = strings.Repeat("4", 32)
	admitted, err := prepareCodexRefresh(f, c, creator)
	if err != nil {
		t.Fatalf("positively stopped unchanged grant did not retry: %v", err)
	}
	got := admitted
	if got.Failures != p.Failures || !got.RetryAfter.Equal(p.RetryAfter) || got.Phase != refreshPrepared {
		t.Fatalf("retry reset durable budget: %+v", got)
	}
}

func TestCodexRefreshBackoffAdmissionRefusesUnsettledOrChanged(t *testing.T) {
	for _, kind := range []string{"unstopped", "live-worker", "live-native", "changed-generation", "changed-owner", "changed-identity", "stale-revision", "unsafe-code", "unknown-code", "legacy-budget"} {
		t.Run(kind, func(t *testing.T) {
			f, c, creator, _ := refreshAdmissionFixture(t)
			p := retainedRefreshFailure(t, f, c, creator, "refresh_transport_failed", 1, time.Now().Add(-time.Second).UTC())
			switch kind {
			case "unstopped":
				p.WritersStopped = false
			case "live-worker":
				p.Worker = creator
			case "live-native":
				p.Native = creator
			case "changed-generation":
				c.Grant.Generation = strings.Repeat("d", 64)
			case "changed-owner":
				c.Grant.Owner = strings.Repeat("d", 64)
			case "changed-identity":
				c.Grant.Identity = strings.Repeat("d", 64)
			case "stale-revision":
				c.Grant.CredentialRevision--
			case "unsafe-code":
				p.ErrorCode = "refresh_socket_failed"
			case "unknown-code":
				p.ErrorCode = "native_login_failed"
			case "legacy-budget":
				p.Failures, p.RetryAfter = 0, time.Time{}
			}
			if err := f.write(refreshProgressFile, p); err != nil {
				t.Fatal(err)
			}
			before, _ := readPrivate(f.root, refreshProgressFile, 4096)
			if _, err := prepareCodexRefresh(f, c, creator); err == nil {
				t.Fatal("unqualified retry admitted")
			}
			after, _ := readPrivate(f.root, refreshProgressFile, 4096)
			if string(before) != string(after) {
				t.Fatal("held outcome overwritten")
			}
		})
	}
}

func TestCodexRefreshBackoffBoundLateWorkerPublishesGate(t *testing.T) {
	_, c := refreshBoundStore(t)
	f, err := openRefreshJob(c.StateRoot, c.LoginID, true)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	start, err := procstart.StartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	self := ProcessIdentity{PID: os.Getpid(), StartTime: start}
	c.Generation, c.LaunchNonce, c.Deadline = 1, strings.Repeat("2", 32), time.Now().Add(-time.Second)
	p, err := prepareCodexRefresh(f, c, self)
	if err != nil {
		t.Fatal(err)
	}
	p.Worker, p.Phase = self, PhaseStarting
	if err := f.write(refreshProgressFile, p); err != nil {
		t.Fatal(err)
	}
	t.Setenv(refreshLaunchEnv, c.LaunchNonce)
	before := time.Now()
	if err := RunCodexRefreshConfig(context.Background(), filepath.Join(f.path, refreshConfigFile)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	p, err = readRefreshProgress(f)
	if err != nil {
		t.Fatal(err)
	}
	got := p
	if !got.NoNativeStarted || !got.WritersStopped || got.Failures != 1 || got.RetryAfter.Before(before.Add(time.Minute)) || got.RetryAfter.After(time.Now().Add(time.Minute)) {
		t.Fatalf("own stopped late refusal lost first durable retry: %+v", got)
	}
}

func TestCodexRefreshBackoffDelaysSaturateWithoutAttemptCap(t *testing.T) {
	f, c, creator, _ := refreshAdmissionFixture(t)
	p := RefreshProgress{Version: refreshRecordVersion, Grant: c.Grant, Creator: creator, LaunchNonce: c.LaunchNonce, Generation: c.Generation, Worker: ProcessIdentity{PID: 999999999, StartTime: 1}, Native: ProcessIdentity{PID: 999999998, StartTime: 1}, Phase: PhaseFailed, WritersStopped: true, ErrorCode: "refresh_account_read_failed"}
	for i, delay := range []time.Duration{60, 120, 240, 480, 900, 900, 900, 900} {
		before := time.Now()
		recordRefreshFailure(&p, c.Grant)
		got := p
		failures := uint8(i + 1)
		if failures > 5 {
			failures = 5
		}
		if got.Failures != failures || got.RetryAfter.Before(before.Add(delay*time.Second)) || got.RetryAfter.After(time.Now().Add(delay*time.Second)) {
			t.Fatalf("failure %d: %+v", i+1, got)
		}
		// Reopen the durable receipt between attempts rather than resetting the counter.
		if err := f.write(refreshProgressFile, p); err != nil {
			t.Fatal(err)
		}
		var err error
		p, err = readRefreshProgress(f)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestCodexRefreshBackoffUnqualifiedCompletionConsumesOldGate(t *testing.T) {
	for _, kind := range []string{"changed-generation", "unknown-ref", "unsafe", "unstopped"} {
		t.Run(kind, func(t *testing.T) {
			f, c, creator, _ := refreshAdmissionFixture(t)
			wire := retainedRefreshFailure(t, f, c, creator, "refresh_transport_failed", 3, time.Now().Add(-time.Second).UTC())
			p, err := readRefreshProgress(f)
			if err != nil {
				t.Fatal(err)
			}
			current := c.Grant
			switch kind {
			case "changed-generation":
				current.Generation = strings.Repeat("d", 64)
			case "unknown-ref":
				current = logins.CodexRefreshRef{}
			case "unsafe":
				p.ErrorCode = "unsafe_profile"
			case "unstopped":
				p.WritersStopped = false
			}
			recordRefreshFailure(&p, current)
			got := p
			if got.Failures != wire.Failures || !got.RetryAfter.IsZero() {
				t.Fatalf("uncertain outcome reused old retry authority: %+v", got)
			}
			if err := f.write(refreshProgressFile, p); err != nil {
				t.Fatal(err)
			}
			if _, err := prepareCodexRefresh(f, c, creator); !errors.Is(err, logins.ErrPending) {
				t.Fatalf("uncertain outcome admitted: %v", err)
			}
		})
	}
}

func TestCodexRefreshBackoffFailureCodeAllowlist(t *testing.T) {
	for _, code := range []string{"deadline_exceeded", "canceled", "native_context_canceled", "refresh_native_start_failed", "refresh_native_exited", "refresh_transport_failed", "refresh_initialize_failed", "refresh_initialized_failed", "refresh_account_read_failed"} {
		t.Run(code, func(t *testing.T) {
			f, c, creator, _ := refreshAdmissionFixture(t)
			retainedRefreshFailure(t, f, c, creator, code, 1, time.Now().Add(-time.Second).UTC())
			if _, err := prepareCodexRefresh(f, c, creator); err != nil {
				t.Fatalf("settled allowlisted failure held: %v", err)
			}
		})
	}
	for _, code := range []string{"refresh_version_probe_failed", "refresh_native_identity_failed", "refresh_socket_failed", "refresh_native_stop_failed", "refresh_native_inventory_failed", "refresh_grant_stop_failed", "refresh_socket_cleanup_failed", "refresh_version_stop_failed", "invalid_native_schema", "unsafe_profile", "stale_native_attempt", "native_version_unsupported", "native_login_failed", "new_unknown_code"} {
		t.Run(code, func(t *testing.T) {
			f, c, creator, _ := refreshAdmissionFixture(t)
			retainedRefreshFailure(t, f, c, creator, code, 1, time.Now().Add(-time.Second).UTC())
			if _, err := prepareCodexRefresh(f, c, creator); !errors.Is(err, logins.ErrPending) {
				t.Fatalf("structural/uncertain failure admitted: %v", err)
			}
		})
	}
}

func TestCodexRefreshBackoffReadyAndCommittedReloginReset(t *testing.T) {
	for _, kind := range []string{"ready", "relogin", "relogin-live-worker", "relogin-live-native", "relogin-unstopped"} {
		t.Run(kind, func(t *testing.T) {
			s, c := refreshBoundStore(t)
			f, err := openRefreshJob(c.StateRoot, c.LoginID, true)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			start, err := procstart.StartTime(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			creator := ProcessIdentity{PID: os.Getpid(), StartTime: start}
			c.Generation, c.LaunchNonce = 1, strings.Repeat("2", 32)
			p := retainedRefreshFailure(t, f, c, creator, "refresh_account_read_failed", 4, time.Now().Add(time.Hour).UTC())
			if kind == "ready" {
				p.Phase, p.ErrorCode, p.Failures, p.RetryAfter = PhaseReady, "", 0, time.Time{}
				output := c.Grant
				output.Generation = strings.Repeat("d", 64)
				p.Output, p.ExpiresAt = &output, time.Now().Add(time.Hour)
				c.Grant = output
			} else {
				lease, err := s.AcquireCodexGrant(c.LoginID)
				if err != nil {
					t.Fatal(err)
				}
				r, err := s.Snapshot()
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err := lease.ReplaceCredential(r.Revision, logins.Credential{Auth: refreshFixtureCredential(time.Now().Add(2 * time.Hour).Unix())}, strings.Repeat("5", 32)); err != nil {
					t.Fatal(err)
				}
				if err := lease.Close(); err != nil {
					t.Fatal(err)
				}
				lease, err = s.AcquireCodexGrant(c.LoginID)
				if err != nil {
					t.Fatal(err)
				}
				c.Grant, err = lease.RefreshRef()
				if err != nil {
					t.Fatal(err)
				}
				if err := lease.Close(); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "relogin-live-worker":
					p.Worker = creator
				case "relogin-live-native":
					p.Native = creator
				case "relogin-unstopped":
					p.WritersStopped = false
				}
			}
			if err := f.write(refreshProgressFile, p); err != nil {
				t.Fatal(err)
			}
			next, err := prepareCodexRefresh(f, c, creator)
			if strings.HasPrefix(kind, "relogin-") {
				if !errors.Is(err, logins.ErrPending) {
					t.Fatalf("relogin bypassed old custody: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got := next
			if got.Failures != 0 || !got.RetryAfter.IsZero() {
				t.Fatalf("qualified reset retained failure budget: %+v", got)
			}
		})
	}
}

func TestCodexRefreshBackoffBoundWorkerCompletion(t *testing.T) {
	for _, mode := range []string{"rpc-error", "renew"} {
		t.Run(mode, func(t *testing.T) {
			tmp, err := filepath.EvalSymlinks(os.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			root, err := os.MkdirTemp(tmp, "swarm-r-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(root) })
			_, c := refreshBoundStoreAt(t, root)
			f, err := openRefreshJob(c.StateRoot, c.LoginID, true)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			start, err := procstart.StartTime(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			self := ProcessIdentity{PID: os.Getpid(), StartTime: start}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			native := newRefreshFixtureTransport(t, f.path, refreshFixtureTransport{Target: executable, Version: "0.163.0", Prefix: []string{"-test.run=^TestCodexRefreshNativeFixture$"}})
			if err := os.WriteFile(filepath.Join(f.path, "fixture-mode"), []byte(mode), 0600); err != nil {
				t.Fatal(err)
			}
			fingerprint, err := persist.CLIFingerprint(native)
			if err != nil {
				t.Fatal(err)
			}
			c.Generation, c.LaunchNonce, c.NativePath, c.NativeVersion, c.NativeFingerprint, c.Deadline = 1, strings.Repeat("2", 32), native, "0.163.0", fingerprint, time.Now().Add(10*time.Second)
			retainedRefreshFailure(t, f, c, self, "refresh_transport_failed", 3, time.Now().Add(-time.Second).UTC())
			p, err := prepareCodexRefresh(f, c, self)
			if err != nil {
				t.Fatal(err)
			}
			p.Worker, p.Phase = self, PhaseStarting
			if err := f.write(refreshProgressFile, p); err != nil {
				t.Fatal(err)
			}
			t.Setenv(refreshLaunchEnv, c.LaunchNonce)
			mask := syscall.Umask(0077)
			defer syscall.Umask(mask)
			before := time.Now()
			result := RunCodexRefreshConfig(context.Background(), filepath.Join(f.path, refreshConfigFile))
			p, err = readRefreshProgress(f)
			if err != nil {
				t.Fatal(err)
			}
			if p.NoNativeStarted || !p.WritersStopped || p.Native.PID <= 0 || p.Native.Alive() {
				t.Fatalf("native custody not positively settled: %+v", p)
			}
			if mode == "renew" {
				if result != nil || p.Phase != PhaseReady || p.Failures != 0 || !p.RetryAfter.IsZero() || p.Output == nil || p.Output.Generation == c.Grant.Generation {
					t.Fatalf("validated success did not reset budget: %+v %v", p, result)
				}
			} else if result == nil || p.Phase != PhaseFailed || p.ErrorCode != "refresh_account_read_failed" || p.Failures != 4 || p.RetryAfter.Before(before.Add(8*time.Minute)) || p.RetryAfter.After(time.Now().Add(8*time.Minute)) {
				t.Fatalf("post-native unchanged-grant settlement lost durable retry: %+v %v", p, result)
			}
		})
	}
}

func TestCodexRefreshBackoffRejectsInvalidBudgetShape(t *testing.T) {
	for _, kind := range []string{"overflow", "gate-without-count", "ready-budget", "null", "alias"} {
		t.Run(kind, func(t *testing.T) {
			f, c, creator, _ := refreshAdmissionFixture(t)
			p := retainedRefreshFailure(t, f, c, creator, "refresh_transport_failed", 1, time.Now().Add(time.Minute).UTC())
			switch kind {
			case "overflow":
				p.Failures = 6
			case "gate-without-count":
				p.Failures = 0
			case "ready-budget":
				p.Phase = PhaseReady
				output := c.Grant
				output.Generation = strings.Repeat("d", 64)
				p.Output, p.ExpiresAt = &output, time.Now().Add(time.Hour)
			}
			raw, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "null" {
				raw = []byte(strings.Replace(string(raw), `"failures":1`, `"failures":null`, 1))
			}
			if kind == "alias" {
				raw = []byte(strings.Replace(string(raw), `"failures":1`, `"Failures":1`, 1))
			}
			if err := os.WriteFile(filepath.Join(f.path, refreshProgressFile), raw, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := readRefreshProgress(f); err == nil {
				t.Fatal("ambiguous backoff budget accepted")
			}
		})
	}
}
