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

func refreshAdmissionFixture(t *testing.T) (*jobFiles, RefreshConfig, ProcessIdentity, *os.File) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := logins.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	id := strings.Repeat("1", 32)
	f, err := openRefreshJob(root, id, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.Close)
	lock, err := refreshJobLock(f, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	c := RefreshConfig{Version: 2, StateRoot: root, LoginID: id, Generation: 1, LaunchNonce: strings.Repeat("2", 32), Deadline: time.Now().Add(20 * time.Second), Grant: logins.CodexRefreshRef{LoginID: id, Identity: strings.Repeat("a", 64), Owner: strings.Repeat("b", 64), CredentialRevision: 1, Generation: strings.Repeat("c", 64)}}
	started, err := procstart.StartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	return f, c, ProcessIdentity{PID: os.Getpid(), StartTime: started}, lock
}

func TestCodexRefreshAdmissionPersistsBoundIntentBeforeWorker(t *testing.T) {
	f, c, creator, _ := refreshAdmissionFixture(t)
	p, err := prepareCodexRefresh(f, c, creator)
	if err != nil || p.Version != 2 || p.Grant != c.Grant || p.Creator != creator || p.Worker != (ProcessIdentity{}) || p.Phase != "prepared" {
		t.Fatalf("missing exact creator-only durable intent: %+v %v", p, err)
	}
	read, err := readRefreshProgress(f)
	if err != nil || read.Grant != c.Grant || read.Phase != "prepared" {
		t.Fatalf("prepared intent did not survive read: %+v %v", read, err)
	}
	raw, err := readPrivate(f.root, refreshProgressFile, 4096)
	if err != nil {
		t.Fatal(err)
	}
	var legacy struct {
		Generation uint64          `json:"generation"`
		Worker     ProcessIdentity `json:"worker"`
	}
	if json.Unmarshal(raw, &legacy) != nil {
		t.Fatal("old parser control did not decode JSON")
	}
	// Frozen pre-v2 readRefreshProgress requires both of these worker fields.
	if legacy.Generation != 0 && legacy.Worker.PID > 0 && legacy.Worker.StartTime > 0 {
		t.Fatal("old reader could overwrite or launch from the v2 progress record")
	}
	p.Worker, p.Phase = ProcessIdentity{PID: 999999999, StartTime: 1}, PhaseStarting
	if err := f.write(refreshProgressFile, p); err != nil {
		t.Fatal(err)
	}
	raw, err = readPrivate(f.root, refreshProgressFile, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(raw, &legacy) != nil || legacy.Worker.PID != 0 {
		t.Fatal("old reader accepted a published nonzero worker_ref")
	}
	if _, err := prepareCodexRefresh(f, c, creator); !errors.Is(err, logins.ErrPending) {
		t.Fatalf("missing/dead worker recreated prepared authority: %v", err)
	}
}

func TestCodexRefreshAdmissionRetainsConfigWithoutProgress(t *testing.T) {
	f, c, creator, _ := refreshAdmissionFixture(t)
	legacy := []byte(`{"generation":1,"launch_nonce":"unbound"}`)
	if err := os.WriteFile(filepath.Join(f.path, refreshConfigFile), legacy, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareCodexRefresh(f, c, creator); !errors.Is(err, logins.ErrPending) {
		t.Fatalf("config-only legacy custody rearmed: %v", err)
	}
	if after, err := os.ReadFile(filepath.Join(f.path, refreshConfigFile)); err != nil || string(after) != string(legacy) {
		t.Fatal("config-only legacy record overwritten")
	}
	if _, err := f.root.Lstat(refreshProgressFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unbound config received fabricated progress")
	}
}

func TestCodexRefreshAdmissionMissingConfigCannotRecreateCreator(t *testing.T) {
	f, c, creator, _ := refreshAdmissionFixture(t)
	if _, err := prepareCodexRefresh(f, c, creator); err != nil {
		t.Fatal(err)
	}
	p, err := readRefreshProgress(f)
	if err != nil || p.Phase != refreshPrepared || p.Worker != (ProcessIdentity{}) {
		t.Fatalf("prepared intent lost: %+v %v", p, err)
	}
	if err := f.root.Remove(refreshConfigFile); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareCodexRefresh(f, c, creator); !errors.Is(err, logins.ErrPending) {
		t.Fatalf("missing config/worker recreated a consumed creator: %v", err)
	}
}

func refreshBoundStore(t *testing.T) (*logins.Store, RefreshConfig) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := logins.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	r, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	r, login, err := s.Add(r.Revision, logins.ProviderCodex, "bound", logins.Credential{Auth: refreshFixtureCredential(time.Now().Add(time.Hour).Unix())})
	if err != nil {
		t.Fatal(err)
	}
	op, err := s.BeginCodexOwnershipMigrationTo(r.Revision, login.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"stopping", "stopped"} {
		op.Phase = phase
		if err := s.SaveSwitch(op); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CaptureOutgoing(op.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.MigrateCodexOwnership(op.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CommitSwitch(op.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteSwitch(op.ID); err != nil {
		t.Fatal(err)
	}
	lease, err := s.AcquireCodexGrant(login.ID)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := lease.RefreshRef()
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	return s, RefreshConfig{Version: refreshRecordVersion, StateRoot: root, LoginID: login.ID, Grant: ref}
}

func TestCodexRefreshAdmissionRevalidatesWorkerGrantBeforeNativeChild(t *testing.T) {
	for _, field := range []string{"current", "identity", "owner", "revision", "generation"} {
		t.Run(field, func(t *testing.T) {
			s, c := refreshBoundStore(t)
			switch field {
			case "identity":
				c.Grant.Identity = strings.Repeat("f", 64)
			case "owner":
				c.Grant.Owner = strings.Repeat("f", 64)
			case "revision":
				c.Grant.CredentialRevision++
			case "generation":
				c.Grant.Generation = strings.Repeat("f", 64)
			}
			lease, err := acquireRefreshGrant(s, c)
			if field == "current" {
				if err != nil {
					t.Fatal(err)
				}
				_ = lease.Close()
			} else if err == nil {
				_ = lease.Close()
				t.Fatalf("%s stale input acquired native refresh authority", field)
			}
			if _, err := os.Stat(filepath.Join(s.StateRoot(), "logins", "saved", c.LoginID, ".grant-owner")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("reference check published native process custody")
			}
		})
	}
}

func TestCodexRefreshAdmissionRetainsLegacyFailedAndUncertainRecords(t *testing.T) {
	for _, phase := range []string{"legacy", "failed", "starting"} {
		t.Run(phase, func(t *testing.T) {
			f, c, creator, _ := refreshAdmissionFixture(t)
			p := RefreshProgress{Version: 2, Grant: c.Grant, Creator: creator, LaunchNonce: c.LaunchNonce, Generation: c.Generation, Worker: ProcessIdentity{PID: 999999999, StartTime: 1}, Phase: PhaseFailed, WritersStopped: true}
			if phase == "legacy" {
				if err := os.WriteFile(filepath.Join(f.path, refreshProgressFile), []byte(`{"generation":1,"worker":{"pid":999999999,"start_time":1},"phase":"failed","writers_stopped":true}`), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if phase == "starting" {
					p.Phase, p.WritersStopped = PhaseStarting, false
				}
				if err := f.write(refreshProgressFile, p); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(filepath.Join(f.path, refreshProgressFile))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := prepareCodexRefresh(f, c, creator); !errors.Is(err, logins.ErrPending) {
				t.Fatalf("unbound/failed/uncertain outcome rearmed: %v", err)
			}
			after, err := os.ReadFile(filepath.Join(f.path, refreshProgressFile))
			if err != nil || string(before) != string(after) {
				t.Fatal("retained outcome overwritten")
			}
		})
	}
}

func TestCodexRefreshAdmissionReaderRejectsAliasesUnknownAndTrailingData(t *testing.T) {
	for _, extra := range []string{`,"unknown":true}`, `,"Version":2}`, `}{}`} {
		t.Run(extra, func(t *testing.T) {
			f, c, creator, _ := refreshAdmissionFixture(t)
			p := RefreshProgress{Version: 2, Grant: c.Grant, Creator: creator, LaunchNonce: c.LaunchNonce, Generation: c.Generation, Phase: PhaseStarting, Worker: ProcessIdentity{PID: 999999999, StartTime: 1}}
			raw, _ := json.Marshal(p)
			raw = []byte(strings.TrimSuffix(string(raw), "}") + extra)
			if err := os.WriteFile(filepath.Join(f.path, refreshProgressFile), raw, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := readRefreshProgress(f); err == nil {
				t.Fatal("ambiguous refresh authority accepted")
			}
		})
	}
}

func TestCodexRefreshAdmissionNoNativeStateCombinations(t *testing.T) {
	for _, phase := range []string{PhaseReady, PhaseFailed} {
		for _, stopped := range []bool{false, true} {
			f, c, creator, _ := refreshAdmissionFixture(t)
			p := RefreshProgress{Version: refreshRecordVersion, Grant: c.Grant, Creator: creator, Worker: creator, LaunchNonce: c.LaunchNonce, Generation: c.Generation, Phase: phase, NoNativeStarted: true, WritersStopped: stopped}
			if phase == PhaseReady {
				output := c.Grant
				output.Generation = strings.Repeat("d", 64)
				p.Output = &output
				p.Native = ProcessIdentity{PID: 999999999, StartTime: 1}
				p.ExpiresAt = time.Now().Add(time.Hour)
			}
			if err := f.write(refreshProgressFile, p); err != nil {
				t.Fatal(err)
			}
			_, err := readRefreshProgress(f)
			if (phase == PhaseReady && err == nil) || (phase == PhaseFailed && err != nil) {
				t.Fatalf("contradictory or empty-scope state: phase=%s stopped=%t err=%v", phase, stopped, err)
			}
		}
	}
}

func TestCodexRefreshAdmissionBoundWorkerRefusals(t *testing.T) {
	for _, kind := range []string{"late", "stale-grant", "unauthorized", "starting-native", "starting-stopped"} {
		t.Run(kind, func(t *testing.T) {
			s, c := refreshBoundStore(t)
			f, err := openRefreshJob(c.StateRoot, c.LoginID, true)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			lock, err := refreshJobLock(f, true)
			if err != nil {
				t.Fatal(err)
			}
			started, err := procstart.StartTime(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			self := ProcessIdentity{PID: os.Getpid(), StartTime: started}
			probe := filepath.Join(f.path, "native-counter")
			if err := os.WriteFile(probe, []byte("#!/bin/sh\nprintf ran > \"$0.ran\"\nprintf 'codex-cli 0.163.0\\n'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			native := newRefreshFixtureTransport(t, f.path, refreshFixtureTransport{Target: probe})
			fingerprint, err := persist.CLIFingerprint(native)
			if err != nil {
				t.Fatal(err)
			}
			c.Generation, c.LaunchNonce, c.NativePath, c.NativeVersion, c.NativeFingerprint, c.Deadline = 1, strings.Repeat("2", 32), native, "0.163.0", fingerprint, time.Now().Add(20*time.Second)
			if kind == "late" {
				c.Deadline = time.Now().Add(-time.Second)
			}
			p, err := prepareCodexRefresh(f, c, self)
			if err != nil {
				t.Fatal(err)
			}
			p.Worker, p.Phase = self, PhaseStarting
			if kind == "unauthorized" {
				p.Worker = ProcessIdentity{PID: 999999999, StartTime: 1}
			}
			if kind == "starting-native" {
				p.Native = ProcessIdentity{PID: 999999999, StartTime: 1}
			}
			if kind == "starting-stopped" {
				p.WritersStopped = true
			}
			if err := f.write(refreshProgressFile, p); err != nil {
				t.Fatal(err)
			}
			if err := lock.Close(); err != nil {
				t.Fatal(err)
			}
			if kind == "stale-grant" {
				lease, err := s.AcquireCodexGrant(c.LoginID)
				if err != nil {
					t.Fatal(err)
				}
				err = os.WriteFile(filepath.Join(lease.Home(), "auth.json"), refreshFixtureCredential(time.Now().Add(2*time.Hour).Unix()), 0600)
				_ = lease.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			before, err := readPrivate(f.root, refreshProgressFile, 4096)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv(refreshLaunchEnv, c.LaunchNonce)
			mask := syscall.Umask(0077)
			defer syscall.Umask(mask)
			err = RunCodexRefreshConfig(context.Background(), filepath.Join(f.path, refreshConfigFile))
			want := ErrStale
			if kind == "late" {
				want = context.DeadlineExceeded
			}
			if kind == "starting-native" || kind == "starting-stopped" {
				want = ErrStale
			}
			if !errors.Is(err, want) {
				t.Fatalf("bound pre-native refusal: %v want %v", err, want)
			}
			if _, err := os.Stat(probe + ".ran"); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("native/version child ran before bound grant validation")
			}
			if kind == "unauthorized" || kind == "starting-native" || kind == "starting-stopped" {
				after, err := readPrivate(f.root, refreshProgressFile, 4096)
				if err != nil || string(after) != string(before) {
					t.Fatal("unauthorized worker changed another attempt")
				}
				return
			}
			got, err := readRefreshProgress(f)
			if err != nil || got.Phase != PhaseFailed || !got.NoNativeStarted || !got.WritersStopped || got.Native != (ProcessIdentity{}) {
				t.Fatalf("positive own no-native proof lost: %+v %v", got, err)
			}
		})
	}
}
