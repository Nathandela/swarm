package shim

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"syscall"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/loginworker"
	"github.com/creack/pty"
)

type claudeBirth struct {
	store  *logins.Store
	image  *os.File
	anchor os.FileInfo
}

// Custody precedes slow preparation so ordinary refusals can finish their own
// exact zero-PID scope. It grants no authentication or Start authority.
func prepareClaudeBirth(cfg Config, scope *managedNativeScope) (*claudeBirth, error) {
	if cfg.LoginRuntime == nil || !logins.IsClaudeProfileRuntime(cfg.LoginStateRoot, *cfg.LoginRuntime) {
		return nil, nil
	}
	if cfg.CLIIdentity == nil || len(cfg.Argv) == 0 || cfg.Argv[0] != cfg.CLIIdentity.Path || cfg.Backend != nil || runtime.GOOS != "linux" {
		return nil, errLoginResolution
	}
	store, err := logins.OpenExisting(cfg.LoginStateRoot)
	if err != nil {
		return nil, errLoginResolution
	}
	birth := &claudeBirth{store: store}
	if err := store.ClaimClaudeProfileCustody(*cfg.LoginRuntime, cfg.Cwd, func() error { return scope.claimClaudeReservation(cfg) }); err != nil {
		birth.close()
		return nil, errors.Join(errLoginResolution, err)
	}
	return birth, nil
}

// Hash/open the fixed qualified image outside the registry fence. No Config
// callback can substitute or skip this constructor or final Store admission.
func (b *claudeBirth) prepareImage(cfg Config) error {
	if b == nil {
		return nil
	}
	image, err := loginworker.OpenClaudeNativeImage(cfg.CLIIdentity.Path, cfg.CLIIdentity.Version, cfg.CLIIdentity.Fingerprint)
	if err != nil {
		return errLoginResolution
	}
	anchor, err := image.Stat()
	if err != nil {
		_ = image.Close()
		return errLoginResolution
	}
	b.image, b.anchor = image, anchor
	return nil
}

func (b *claudeBirth) close() {
	if b == nil {
		return
	}
	if b.image != nil {
		_ = b.image.Close()
	}
	if b.store != nil {
		_ = b.store.Close()
	}
}

// Cancellation is distinct from an ordinary auth/preparation refusal. Retain
// independent durability/containment errors when a stop is observed as well.
func (b *claudeBirth) preStartError(signals <-chan os.Signal, err error) error {
	if b == nil {
		return err
	}
	select {
	case <-signals:
		if err == nil || err == errLoginResolution {
			return context.Canceled
		}
		return errors.Join(context.Canceled, err)
	default:
		return err
	}
}

// start never waits for the child. If publication fails after Start, return its
// live PTY/process to Run's existing drain/contain/one-Wait path. Never retry.
func (b *claudeBirth) start(scope *managedNativeScope, cfg Config, cmd *exec.Cmd, ws *pty.Winsize, signals <-chan os.Signal) (*os.File, error) {
	return b.startOwned(scope, cfg, cmd, ws, nil, signals)
}

// Dormant prerequisite: explicit descriptors keep Wait independent of I/O pumps.
func (b *claudeBirth) startStdio(scope *managedNativeScope, cfg Config, cmd *exec.Cmd, srv *server, signals <-chan os.Signal) error {
	if cmd == nil || srv == nil || !reflect.DeepEqual(cmd.SysProcAttr, &syscall.SysProcAttr{Setpgid: true}) {
		return errLoginResolution
	}
	for _, stream := range []any{cmd.Stdin, cmd.Stdout, cmd.Stderr} {
		if file, ok := stream.(*os.File); !ok || file == nil {
			return errLoginResolution
		}
	}
	_, err := b.startOwned(scope, cfg, cmd, nil, srv, signals)
	return err
}

func (b *claudeBirth) startOwned(scope *managedNativeScope, cfg Config, cmd *exec.Cmd, ws *pty.Winsize, srv *server, signals <-chan os.Signal) (*os.File, error) {
	if b == nil || b.store == nil || b.image == nil || b.anchor == nil || runtime.GOOS != "linux" || cfg.LoginRuntime == nil || cfg.CLIIdentity == nil || cmd == nil || cmd.Path != cfg.CLIIdentity.Path || !reflect.DeepEqual(cmd.Args, cfg.Argv) || cmd.Dir != cfg.Cwd || len(cmd.ExtraFiles) != 0 {
		return nil, errLoginResolution
	}
	var ptmx *os.File
	err := b.store.AdmitClaudeProfileBirth(*cfg.LoginRuntime, cfg.Cwd, func() error {
		current, err := b.image.Stat()
		if err != nil || !os.SameFile(b.anchor, current) || current.Size() != b.anchor.Size() || current.Mode() != b.anchor.Mode() || !current.ModTime().Equal(b.anchor.ModTime()) {
			return errLoginResolution
		}
		if err := scope.validateClaudeStart(cfg); err != nil {
			return err
		}
		if err := b.preStartError(signals, nil); err != nil {
			return err
		}
		// ExtraFiles remaps this exact held image to child fd3 before execve. The
		// original argv[0] and all flags remain unchanged; pathname is never reopened.
		cmd.Path = "/proc/self/fd/3"
		cmd.ExtraFiles = []*os.File{b.image}
		if srv == nil {
			scope.startAttempted = true
			ptmx, err = pty.StartWithSize(cmd, ws)
		} else {
			srv.pgidMu.Lock()
			if srv.pendingSig != 0 {
				srv.pgidMu.Unlock()
				return context.Canceled
			}
			if err := b.preStartError(signals, nil); err != nil {
				srv.pgidMu.Unlock()
				return err
			}
			scope.startAttempted = true
			err = cmd.Start()
			if err == nil && cmd.Process != nil {
				srv.pgid = cmd.Process.Pid
			}
			srv.pgidMu.Unlock()
		}
		if err != nil {
			return err
		}
		if cmd.Process == nil {
			return errors.New("shim: native start has no process identity")
		}
		return scope.record(cmd.Process.Pid, nil)
	})
	if cmd.Process == nil {
		err = b.preStartError(signals, err)
	}
	return ptmx, err
}
