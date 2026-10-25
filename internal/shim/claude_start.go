package shim

import (
	"errors"
	"os"
	"os/exec"
	"reflect"
	"runtime"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/loginworker"
	"github.com/creack/pty"
)

type claudeBirth struct {
	store  *logins.Store
	image  *os.File
	anchor os.FileInfo
}

// Preparation hashes/opens the qualified native image outside the registry
// lock. No Config callback can substitute or skip the final Store admission.
func prepareClaudeBirth(cfg Config) (*claudeBirth, error) {
	if cfg.LoginRuntime == nil || !logins.IsClaudeProfileRuntime(cfg.LoginStateRoot, *cfg.LoginRuntime) {
		return nil, nil
	}
	if cfg.CLIIdentity == nil || len(cfg.Argv) == 0 || cfg.Argv[0] != cfg.CLIIdentity.Path || cfg.Backend != nil || runtime.GOOS != "linux" {
		return nil, errLoginResolution
	}
	image, err := loginworker.OpenClaudeNativeImage(cfg.CLIIdentity.Path, cfg.CLIIdentity.Version, cfg.CLIIdentity.Fingerprint)
	if err != nil {
		return nil, errLoginResolution
	}
	anchor, err := image.Stat()
	if err != nil {
		_ = image.Close()
		return nil, errLoginResolution
	}
	store, err := logins.OpenExisting(cfg.LoginStateRoot)
	if err != nil {
		_ = image.Close()
		return nil, errLoginResolution
	}
	return &claudeBirth{store: store, image: image, anchor: anchor}, nil
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

// start never waits for the child. If publication fails after Start, return its
// live PTY/process to Run's existing drain/contain/one-Wait path. Never retry.
func (b *claudeBirth) start(scope *managedNativeScope, cfg Config, cmd *exec.Cmd, ws *pty.Winsize) (*os.File, error) {
	if b == nil || b.store == nil || b.image == nil || b.anchor == nil || runtime.GOOS != "linux" || cfg.LoginRuntime == nil || cfg.CLIIdentity == nil || cmd == nil || cmd.Path != cfg.CLIIdentity.Path || !reflect.DeepEqual(cmd.Args, cfg.Argv) || cmd.Dir != cfg.Cwd || len(cmd.ExtraFiles) != 0 {
		return nil, errLoginResolution
	}
	var ptmx *os.File
	err := b.store.AdmitClaudeProfileBirth(*cfg.LoginRuntime, cfg.Cwd, func() error {
		current, err := b.image.Stat()
		if err != nil || !os.SameFile(b.anchor, current) || current.Size() != b.anchor.Size() || current.Mode() != b.anchor.Mode() || !current.ModTime().Equal(b.anchor.ModTime()) {
			return errLoginResolution
		}
		if err := scope.claimClaudeReservation(cfg); err != nil {
			return err
		}
		// ExtraFiles remaps this exact held image to child fd3 before execve. The
		// original argv[0] and all flags remain unchanged; pathname is never reopened.
		cmd.Path = "/proc/self/fd/3"
		cmd.ExtraFiles = []*os.File{b.image}
		ptmx, err = pty.StartWithSize(cmd, ws)
		if err != nil {
			return err
		}
		if cmd.Process == nil {
			return errors.New("shim: native start has no process identity")
		}
		return scope.record(cmd.Process.Pid, nil)
	})
	return ptmx, err
}
