package accountcheck

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/accountconfig"
	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/processcontain"
	"github.com/Nathandela/swarm/internal/procstart"
)

func AvailabilityArguments(model string) ([]string, error) {
	if model == "" || len(model) > 128 || strings.ContainsAny(model, "\x00\r\n\t ") {
		return nil, ErrUnavailable
	}
	return []string{"--safe-mode", "--setting-sources", "", "--settings", "{}", "--tools", "", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--disable-slash-commands", "--no-session-persistence", "--output-format", "json", "--max-turns", "1", "--model", model, "--system-prompt", "Reply OK. Use no tools.", "-p", "Reply OK."}, nil
}

// RunWorker is called only by the dedicated internal account-check re-exec.
// Admission arrives via stdin after the daemon persisted exact worker custody.
func RunWorker(ctx context.Context, path string, input io.Reader, output io.Writer) (err error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	stateRoot := filepath.Dir(filepath.Dir(filepath.Dir(path)))
	if !validGeneration(filepath.Base(path)) || path != filepath.Join(stateRoot, "accounts", "checks", filepath.Base(path)) {
		return ErrUnavailable
	}
	checks, err := openChecks(stateRoot)
	if err != nil {
		return err
	}
	defer func() { _ = checks.Close() }()
	files, err := openGeneration(checks, filepath.Base(path))
	if err != nil {
		return err
	}
	defer func() { _ = files.Close() }()
	var ref Ref
	// The parent writes this durable ref before sending any admission bytes.
	// A rejected admission closes stdin; the same cleanup then proves no exec.
	deadline := time.Now().Add(3 * time.Second)
	for readJSON(files, WorkerFile, &ref) != nil {
		if time.Now().After(deadline) {
			return ErrUnavailable
		}
		time.Sleep(10 * time.Millisecond)
	}
	if ref.SchemaVersion != SchemaVersion || ref.Generation != filepath.Base(path) || ref.Worker.PID != os.Getpid() || !ref.Worker.Alive() {
		return ErrUnavailable
	}
	if err := processcontain.EnableSubreaper(); err != nil {
		return err
	}
	proof := Stopped{SchemaVersion: SchemaVersion, Ref: ref}
	var out boundedOutput
	// No goroutine can spawn a native process once this function enters cleanup.
	defer func() {
		if cleanupErr := processcontain.ContainAndReap(); cleanupErr != nil {
			err = errors.Join(err, ErrCustodyUnknown)
			return
		}
		proof.WritersStopped = true
		proof.ExitOK = err == nil
		if writeJSON(files, StoppedFile, proof) != nil {
			err = errors.Join(err, ErrCustodyUnknown)
			return
		}
		// Native stdout is bounded and released only after the durable clean proof.
		if err == nil {
			_, err = output.Write(out.data)
		}
	}()
	var cfg Config
	decoder := json.NewDecoder(io.LimitReader(input, 128<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&cfg) != nil {
		return ErrUnavailable
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return ErrUnavailable
	}
	if cfg.StateRoot != stateRoot || ref.Binding != cfg.Binding || ref.Mode != cfg.Mode {
		return ErrUnavailable
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	store, err := accounts.Open(cfg.StateRoot)
	if err != nil {
		return ErrUnavailable
	}
	registry, err := store.Snapshot()
	_ = store.Close()
	if err != nil || registry.Accounts[cfg.Binding.AccountID].Lifecycle != accounts.LifecycleEnabled {
		return ErrUnavailable
	}
	if cfg.CLI.Path == "" || cfg.Binding.Provider != accounts.ProviderClaude || !accountconfig.SupportedNativeVersion(cfg.Binding.Provider, cfg.CLI.Version) || !filepath.IsAbs(cfg.Cwd) {
		return ErrUnavailable
	}
	fingerprint, err := persist.CLIFingerprint(cfg.CLI.Path)
	if err != nil || fingerprint != cfg.CLI.Fingerprint {
		return ErrUnavailable
	}
	env, err := accounts.ResolveBoundEnvironment(cfg.StateRoot, cfg.Binding, cfg.Env)
	if err != nil {
		return ErrUnavailable
	}
	env = isolatedEnvironment(env)
	args := []string{"auth", "status", "--json"}
	nativeCwd := cfg.Cwd
	switch cfg.Mode {
	case ModeAvailability:
		args, err = AvailabilityArguments(cfg.Model)
		if err != nil {
			return err
		}
	case ModeAuthStatus:
		// Auth-status runs in a private empty context too. The discussion's project
		// cannot contribute hooks, settings, MCP or runtime loader policy to it.
		if err := files.Mkdir("native-cwd", 0o700); err != nil {
			return ErrUnavailable
		}
		nativeCwd = filepath.Join(path, "native-cwd")
	default:
		return ErrUnavailable
	}
	store, err = accounts.Open(cfg.StateRoot)
	if err != nil {
		return err
	}
	profile, err := store.ProfilePath(cfg.Binding)
	_ = store.Close()
	if err != nil {
		return err
	}
	if err := accountconfig.ValidateAvailabilityCheck(cfg.StateRoot, profile, nativeCwd, env); err != nil {
		return ErrUnavailable
	}
	if cfg.Deadline.IsZero() || !cfg.Deadline.After(time.Now()) || cfg.Deadline.After(time.Now().Add(2*time.Minute)) {
		return ErrUnavailable
	}
	ctx, cancel := context.WithDeadline(ctx, cfg.Deadline)
	defer cancel()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	out = boundedOutput{maximum: 64 << 10}
	cmd := exec.Command(cfg.CLI.Path, args...)
	cmd.Env, cmd.Dir = env, nativeCwd
	cmd.SysProcAttr = processcontain.ChildAttrs()
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		return err
	}
	start, err := procstart.StartTime(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return ErrUnavailable
	}
	proof.Native = processcontain.Identity{PID: cmd.Process.Pid, StartTime: start}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
	case <-ctx.Done():
		_ = processcontain.SignalIdentity(proof.Native, syscall.SIGKILL)
		err = <-done
		err = errors.Join(err, ctx.Err())
	}
	if out.exceeded {
		return ErrUnavailable
	}
	return err
}

func isolatedEnvironment(env []string) []string {
	out := make([]string, 0, len(env)+3)
	for _, item := range env {
		key, _, _ := strings.Cut(item, "=")
		switch key {
		case "CLAUDE_CODE_MAX_RETRIES", "CLAUDE_CODE_MAX_OUTPUT_TOKENS", "CLAUDE_CODE_NONSTREAMING_TIMEOUT_RETRIES":
			continue
		}
		out = append(out, item)
	}
	return append(out, "CLAUDE_CODE_MAX_RETRIES=0", "CLAUDE_CODE_MAX_OUTPUT_TOKENS=16", "CLAUDE_CODE_NONSTREAMING_TIMEOUT_RETRIES=0")
}
