package daemon

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

const scheduledGateHost = "SWARM_TEST_SCHEDULED_GATE_HOST"

func scheduledGateSpec(t *testing.T, dir string) LaunchSpec {
	spec := announceSpec(t, filepath.Join(dir, "agent.pid"))
	spec.OperationID = "01K00000000000000000000001"
	spec.OwnerLaunchHash = strings.Repeat("a", 64)
	spec.Scheduled = &ScheduledLaunch{AttemptID: "attempt", RunID: "run", Token: "test-proof"}
	return spec
}

func TestScheduledLaunchGateHost(t *testing.T) {
	mode := os.Getenv(scheduledGateHost)
	if mode == "" {
		t.Skip("subprocess entrypoint")
	}
	dir := os.Getenv(envDaemonState)
	cfg := Config{
		StateDir: dir, ShimBinary: os.Getenv("SWARM_TEST_GATE_SHIM"), MaxSessions: 4,
		SocketPath: filepath.Join(dir, "daemon.sock"), LockPath: filepath.Join(dir, "daemon.lock"),
		LogPath: filepath.Join(dir, "daemon.log"),
	}
	spec := scheduledGateSpec(t, dir)
	if mode == "worker-released" {
		spec.Scheduled = nil
		spec.workerDiscussion = "discord-teammate-fixture"
		cfg.WorkerInstance = func(string, int, ...int64) (string, error) { return "exact", nil }
	}
	ready := func(pid int) error {
		return publishEnvDump(filepath.Join(dir, "boundary"), []byte(strconv.Itoa(pid)))
	}
	switch mode {
	case "before-identity":
		// The test parent kills this daemon while the syscall-backed child
		// exists and the only release writer is still open in this daemon.
		procStartTimeFn = func(pid int) (int64, error) {
			if err := ready(pid); err != nil {
				return 0, err
			}
			time.Sleep(2 * pollTimeout)
			return 0, errors.New("parent did not kill daemon at the identity boundary")
		}
		spec.Backend = &BackendSpec{
			Program: selfExe(t), Args: []string{markerAnnounce, filepath.Join(dir, "backend.pid")},
		}
	case "before-release":
		cfg.onMetaSave = func(m persist.Meta) {
			if m.ShimPID == 0 {
				return
			}
			if err := ready(m.ShimPID); err != nil {
				t.Fatal(err)
			}
			time.Sleep(2 * pollTimeout)
			t.Fatal("parent did not kill daemon at the release boundary")
		}
	case "released", "worker-released":
		// Hold the child before it can read permission or bind its socket.
		// The daemon will commit, release the pipe, and then signal the parent.
		cfg.onMetaSave = func(m persist.Meta) {
			if m.ShimPID > 0 {
				if err := syscall.Kill(m.ShimPID, syscall.SIGSTOP); err != nil {
					t.Fatalf("stop shim before release: %v", err)
				}
			}
		}
		cfg.OnSessionStart = func(m persist.Meta, _ string) {
			if err := ready(m.ShimPID); err != nil {
				t.Fatal(err)
			}
		}
	default:
		t.Fatalf("unknown crash boundary %q", mode)
	}
	d := openDaemon(t, cfg)
	_, err := d.Launch(spec)
	t.Fatalf("launch returned before daemon was killed: %v", err)
}

func TestScheduledLaunchGateRealDaemonCrash(t *testing.T) {
	for _, mode := range []string{"before-identity", "before-release", "released", "worker-released"} {
		t.Run(mode, func(t *testing.T) {
			cfg := daemonConfig(t)
			spec := scheduledGateSpec(t, cfg.StateDir)
			if mode == "worker-released" {
				spec.Scheduled = nil
				spec.workerDiscussion = "discord-teammate-fixture"
			}
			host := exec.Command(selfExe(t), "-test.run", "^TestScheduledLaunchGateHost$")
			host.Env = append(os.Environ(), scheduledGateHost+"="+mode, envDaemonState+"="+cfg.StateDir, "SWARM_TEST_GATE_SHIM="+swarmBin)
			host.Stdout, host.Stderr = os.Stderr, os.Stderr
			if err := host.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- host.Wait() }()
			t.Cleanup(func() { _ = host.Process.Kill() })

			shimPID := readPIDFile(t, filepath.Join(cfg.StateDir, "boundary"))
			start, err := processStartTime(shimPID)
			if err != nil {
				t.Fatalf("read shim start time: %v", err)
			}
			t.Cleanup(func() {
				if liveStart, err := processStartTime(shimPID); err == nil && liveStart == start {
					killTree(shimPID)
				}
			})
			assertScheduledGateNoExec(t, cfg.StateDir)
			if err := host.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
					t.Fatalf("daemon did not die of SIGKILL: %v", err)
				}
			case <-time.After(pollTimeout):
				t.Fatal("daemon did not exit after SIGKILL")
			}
			d := openDaemon(t, cfg)
			m := assertScheduledGateUnknown(t, d, spec)
			if mode != "released" && mode != "worker-released" {
				if mode == "before-identity" && m.ShimPID != 0 {
					t.Fatalf("reservation acquired an identity before commit: %+v", m)
				}
				if mode == "before-release" && (m.ShimPID != shimPID || m.ShimStartTime != start) {
					t.Fatalf("reservation lost its committed identity: %+v", m)
				}
				waitScheduledGateEOF(t, cfg.LogPath)
				waitStatus(t, d, m.ID, status.ProcessLost, pollTimeout)
				assertScheduledGateNoExec(t, cfg.StateDir)
				return
			}
			if m.ShimPID != shimPID || m.ShimStartTime != start || m.Status.Process != status.ProcessRunning {
				t.Fatalf("starting shim was not reconnected by identity: %+v", m)
			}
			if err := syscall.Kill(shimPID, syscall.SIGCONT); err != nil {
				t.Fatal(err)
			}
			agentPID := readPIDFile(t, filepath.Join(cfg.StateDir, "agent.pid"))
			t.Cleanup(func() { killTree(agentPID) })
			waitFile(t, shimSocketPath(cfg.StateDir, m.ID), pollTimeout)
			replayed, err := d.Launch(spec)
			if err != nil || replayed.ID != m.ID || replayed.ShimPID != shimPID {
				t.Fatalf("replay did not adopt released worker: id=%q pid=%d err=%v", replayed.ID, replayed.ShimPID, err)
			}
			outcome, ok := d.OwnerLaunchOutcome(spec.OperationID)
			if !ok || outcome != (OpOutcome{State: OpStateApplied, SessionID: m.ID}) {
				t.Fatalf("released owner outcome: %v, %t", outcome, ok)
			}
		})
	}
}

func TestScheduledLaunchGateIdentityFailureRetainsReservation(t *testing.T) {
	for _, fault := range []string{"read", "persist"} {
		t.Run(fault, func(t *testing.T) {
			cfg := daemonConfig(t)
			d := openDaemon(t, cfg)
			spec := scheduledGateSpec(t, cfg.StateDir)
			var stoppedPID int
			var restoreMeta func()
			original := procStartTimeFn
			t.Cleanup(func() { procStartTimeFn = original })
			procStartTimeFn = func(pid int) (int64, error) {
				// A stopped shim cannot observe EOF or run a TERM handler. Failure
				// must retain the reservation even while cleanup is unconfirmed.
				if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
					t.Fatalf("stop shim: %v", err)
				}
				stoppedPID = pid
				t.Cleanup(func() {
					if stoppedPID != 0 {
						_ = syscall.Kill(stoppedPID, syscall.SIGCONT)
					}
				})
				if fault == "read" {
					return 0, errors.New("injected start time failure")
				}
				reservation := d.List()[0]
				path := filepath.Join(d.sessionDir(reservation.ID), "meta.json")
				backup := path + ".reservation"
				if err := os.Rename(path, backup); err != nil {
					t.Fatal(err)
				}
				restoreMeta = func() {
					t.Helper()
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(backup, path); err != nil {
						t.Fatal(err)
					}
				}
				// Renaming the synced new meta over a directory fails on both hosts.
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
				return processStartTime(pid)
			}
			m, err := d.Launch(spec)
			if err == nil || m.ID == "" || stoppedPID == 0 {
				t.Fatalf("launch: id=%q shim=%d err=%v", m.ID, stoppedPID, err)
			}
			if restoreMeta != nil {
				restoreMeta()
			}
			reserved, err := d.store.Load(m.ID)
			if err != nil || reserved.ShimPID != 0 || reserved.ScheduleAttempt != spec.Scheduled.AttemptID {
				t.Fatalf("retained on-disk reservation: %+v, %v", reserved, err)
			}
			assertScheduledGateUnknown(t, d, spec)
			assertScheduledGateNoExec(t, cfg.StateDir)
			if err := syscall.Kill(stoppedPID, syscall.SIGCONT); err != nil {
				t.Fatal(err)
			}
			stoppedPID = 0
			waitScheduledGateEOF(t, cfg.LogPath)
			waitStatus(t, d, m.ID, status.ProcessLost, pollTimeout)
			assertScheduledGateNoExec(t, cfg.StateDir)
			assertScheduledGateUnknown(t, d, spec)
		})
	}
}

func assertScheduledGateUnknown(t *testing.T, d *Daemon, spec LaunchSpec) persist.Meta {
	t.Helper()
	rows := d.List()
	if len(rows) != 1 || spec.Scheduled != nil && rows[0].ScheduleAttempt != spec.Scheduled.AttemptID || spec.Scheduled == nil && rows[0].WorkerDiscussion != spec.workerDiscussion {
		t.Fatalf("expected one bound reservation, got %+v", rows)
	}
	outcome, ok := d.OwnerLaunchOutcome(spec.OperationID)
	if !ok || outcome.State != OpStateOutcomeUnknown {
		t.Fatalf("reserved owner outcome: %v, %t", outcome, ok)
	}
	if _, err := d.Launch(spec); !errors.Is(err, ErrLaunchOutcomeUnknown) {
		t.Fatalf("exact replay should remain unknown: %v", err)
	}
	spec.OwnerLaunchHash += "0"
	if _, err := d.Launch(spec); !errors.Is(err, ErrOwnerLaunchConflict) {
		t.Fatalf("changed request accepted: %v", err)
	}
	return rows[0]
}

func assertScheduledGateNoExec(t *testing.T, dir string) {
	t.Helper()
	for _, marker := range []string{"agent.pid", "backend.pid"} {
		if _, err := os.Stat(filepath.Join(dir, marker)); !os.IsNotExist(err) {
			t.Fatalf("%s should not exist, stat returned %v", marker, err)
		}
	}
}

func waitScheduledGateEOF(t *testing.T, logPath string) {
	t.Helper()
	deadline := time.Now().Add(pollTimeout)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(logPath); err == nil && strings.Contains(string(data), "launch identity gate closed before release: EOF") {
			return
		}
		time.Sleep(pollStep)
	}
	t.Fatalf("shim did not report aborted identity gate in %s", logPath)
}
