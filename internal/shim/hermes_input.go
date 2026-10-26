package shim

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/Nathandela/swarm/internal/hermesinput"
	"github.com/Nathandela/swarm/internal/procstart"
)

func prepareHermesViewer(cfg *Config) (*hermesinput.Descriptor, error) {
	if cfg.HermesInput == nil {
		return nil, nil
	}
	if cfg.Backend != nil || cfg.OpenCode != nil || cfg.CLIIdentity == nil || cfg.HermesInput.ProviderVersion != cfg.CLIIdentity.Version {
		return nil, errors.New("shim: conflicting Hermes native runtime")
	}
	started, err := procstart.StartTime(os.Getpid())
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	native := *cfg.HermesInput
	native.WorkerID = cfg.SessionID
	native.Cwd = cfg.Cwd
	desc, err := hermesinput.Prepare(ctx, native, cfg.Env, cfg.SessionDir, os.Getpid(), started)
	if err != nil {
		return nil, err
	}
	argv, err := hermesinput.ViewerArgv(native.Program, desc.Binding.SessionID)
	if err != nil {
		return nil, err
	}
	if native.InitialPrompt != "" {
		argv = append(argv, "-q", native.InitialPrompt)
	}
	cfg.Argv = argv
	cfg.Env = hermesinput.SessionEnvironment(hermesinput.SessionEnvironment(cfg.Env, native.Origins), []string{"HERMES_HOME=" + desc.Binding.Home})
	return &desc, nil
}

func hermesViewerEnded(cfg Config, desc *hermesinput.Descriptor) error {
	if desc == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	return hermesinput.ViewerEnded(ctx, cfg.SessionDir, *desc)
}
