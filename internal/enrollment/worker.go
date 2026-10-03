package enrollment

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/procstart"
)

func loadConfig(path string) (Config, *jobFiles, error) {
	if filepath.Base(path) != ConfigFile {
		return Config{}, nil, ErrInvalid
	}
	if _, err := safeAbsolute(filepath.Dir(path), true, true); err != nil {
		return Config{}, nil, err
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return Config{}, nil, ErrUnsafe
	}
	raw, err := readPrivate(root, ConfigFile, 16<<10)
	_ = root.Close()
	if err != nil {
		return Config{}, nil, err
	}
	var cfg Config
	if json.Unmarshal(raw, &cfg) != nil {
		return Config{}, nil, ErrInvalid
	}
	if err := validateConfig(cfg); err != nil {
		return Config{}, nil, err
	}
	f, err := openJob(cfg.StateRoot, cfg.JobID, false)
	if err != nil {
		return Config{}, nil, err
	}
	if filepath.Join(f.path, ConfigFile) != path {
		f.Close()
		return Config{}, nil, ErrUnsafe
	}
	return cfg, f, nil
}

// RunConfig is the `swarm internal account-enroll CONFIG` entrypoint. The outer
// process supervises a private same-binary runner and reaps orphaned writers.
func RunConfig(ctx context.Context, path string) error {
	// Linux PDEATHSIG follows the creating OS thread. Keep it alive for the
	// lifetime of all children instead of letting the Go runtime retire it.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	cfg, files, err := loadConfig(path)
	if err != nil {
		return err
	}
	defer files.Close()
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	ctx, cancel := context.WithDeadline(ctx, cfg.Deadline)
	defer cancel()
	if parent := os.Getenv(runnerEnv); parent != "" {
		parts := strings.Split(parent, ":")
		if len(parts) != 2 {
			return ErrInvalid
		}
		pid, err := strconv.Atoi(parts[0])
		if err != nil {
			return ErrInvalid
		}
		start, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return ErrInvalid
		}
		worker := ProcessIdentity{PID: pid, StartTime: start}
		if !worker.Alive() || os.Getppid() != pid {
			return ErrStale
		}
		if err := enableSubreaper(); err != nil {
			return err
		}
		return runLogin(ctx, cfg, files, worker)
	}
	if err := enableSubreaper(); err != nil {
		return err
	}
	start, err := procstart.StartTime(os.Getpid())
	if err != nil {
		return ErrUnavailable
	}
	worker := ProcessIdentity{PID: os.Getpid(), StartTime: start}
	progress := Progress{SchemaVersion: SchemaVersion, JobID: cfg.JobID, Generation: cfg.Generation, Phase: PhaseStarting, Worker: worker, UpdatedAt: time.Now().UTC()}
	if files.canceled(cfg.Generation) {
		progress.Phase = PhaseCanceled
		progress.WritersStopped = true
		progress.NativeWritersStopped = true
		return files.write(ProgressFile, progress)
	}
	if err := files.write(ProgressFile, progress); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return ErrUnavailable
	}
	cmd := exec.Command(executable, "internal", "account-enroll", path)
	cmd.Dir = files.path
	cmd.Env = append(cleanEnvironment(os.Environ()), runnerEnv+"="+strconv.Itoa(worker.PID)+":"+strconv.FormatInt(worker.StartTime, 10))
	cmd.SysProcAttr = childAttrs()
	if err := cmd.Start(); err != nil {
		progress.Phase = PhaseFailed
		progress.ErrorCode = "runner_start_failed"
		progress.WritersStopped = true
		progress.NativeWritersStopped = true
		_ = files.write(ProgressFile, progress)
		return ErrUnavailable
	}
	runnerStart, err := procstart.StartTime(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return ErrUnavailable
	}
	progress.Runner = ProcessIdentity{PID: cmd.Process.Pid, StartTime: runnerStart}
	// The runner publishes its own creation identity in the separate progress record.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var exitErr error
	select {
	case exitErr = <-done:
	case <-ctx.Done():
		_ = signalIdentity(progress.Runner, syscall.SIGTERM)
		select {
		case exitErr = <-done:
		case <-time.After(2 * time.Second):
			_ = signalIdentity(progress.Runner, syscall.SIGKILL)
			exitErr = <-done
		}
	}
	cleanupErr := containDescendants(os.Getpid())
	reapOrphans()
	if latest, err := ReadProgress(cfg.StateRoot, cfg.JobID); err == nil && latest.Generation == cfg.Generation && latest.Worker == worker {
		progress = latest
	}
	progress.Runner = ProcessIdentity{PID: cmd.Process.Pid, StartTime: runnerStart}
	progress.WritersStopped = cleanupErr == nil
	// This subreaper has joined the runner and contained every descendant,
	// including native children whose runner died before publishing its proof.
	progress.NativeWritersStopped = cleanupErr == nil
	progress.UpdatedAt = time.Now().UTC()
	if files.canceled(cfg.Generation) {
		progress.Phase = PhaseCanceled
		progress.ErrorCode = ""
	} else if ctx.Err() != nil {
		progress.Phase = PhaseFailed
		progress.ErrorCode = "deadline_or_worker_stopped"
	} else if exitErr != nil || progress.Phase != PhaseReady {
		if progress.Phase != PhaseFailed {
			progress.Phase = PhaseFailed
			progress.ErrorCode = "runner_stopped"
		}
	}
	if cleanupErr != nil {
		progress.Phase = PhaseFailed
		progress.ErrorCode = "writers_still_live"
	}
	if progress.Phase == PhaseReady && cleanupErr == nil {
		if err := verifyNativeFiles(cfg); err != nil {
			progress.Phase = PhaseFailed
			progress.ErrorCode = classifyError(err)
		}
		if files.canceled(cfg.Generation) {
			progress.Phase = PhaseCanceled
			progress.ErrorCode = ""
		}
	}
	if err := files.write(ProgressFile, progress); err != nil {
		return err
	}
	if cleanupErr != nil {
		return ErrUnavailable
	}
	if progress.Phase == PhaseCanceled {
		return ErrCanceled
	}
	if progress.Phase != PhaseReady {
		return ErrUnavailable
	}
	return nil
}

// stopNative signals the bound leader identity, reaps it, then contains every
// descendant, including writers that moved into another process group/session.
func stopNative(cmd *exec.Cmd, identity ProcessIdentity, done <-chan error) error {
	if cmd == nil {
		return nil
	}
	if err := signalIdentity(identity, syscall.SIGKILL); err != nil {
		return err
	}
	select {
	case <-done:
		err := containDescendants(os.Getpid())
		reapOrphans()
		return err
	case <-time.After(3 * time.Second):
		return ErrUnavailable
	}
}

func classifyError(err error) string {
	var failure nativeFailure
	if errors.As(err, &failure) {
		return string(failure)
	}
	switch {
	case errors.Is(err, ErrCanceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, ErrUnsafe):
		return "unsafe_profile"
	case errors.Is(err, ErrUnsupported):
		return "native_version_unsupported"
	case errors.Is(err, ErrInvalid):
		return "invalid_native_schema"
	case errors.Is(err, ErrStale):
		return "stale_native_attempt"
	case errors.Is(err, context.Canceled):
		return "native_context_canceled"
	default:
		return "native_login_failed"
	}
}

type nativeFailure string

func (e nativeFailure) Error() string { return string(e) }
