package skeleton

import (
	"errors"
	"path/filepath"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
)

// Pin the exact target observed by the probe; an updater may move its launcher next.
func pinProbedCLIExecutable(spec *daemon.LaunchSpec, identity *persist.CLIIdentity) error {
	if identity == nil || len(spec.Argv) == 0 || !persist.MatchCLIFingerprint(identity.Path, identity.Fingerprint) {
		return errors.New("launch: native installation changed")
	}
	if spec.Backend != nil {
		if spec.Backend.Program != spec.Argv[0] {
			path, err := lookPathIn(spec.Backend.Program, spec.ClientEnv)
			if err != nil {
				return errors.New("launch: backend installation differs")
			}
			target, err := filepath.EvalSymlinks(path)
			if err != nil || target != identity.Path {
				return errors.New("launch: backend installation differs")
			}
		}
		backend := *spec.Backend
		backend.Program = identity.Path
		spec.Backend = &backend
	}
	spec.Argv = append([]string(nil), spec.Argv...)
	spec.Argv[0] = identity.Path
	return nil
}

// Historical launcher spelling is compatible only while it still names this
// exact probed installation; a retargeted launcher cannot repair an old identity.
func sameProbedCLIInstallation(expected, observed *persist.CLIIdentity) bool {
	if expected == nil || observed == nil {
		return false
	}
	if *expected == *observed {
		return true
	}
	if expected.Version != observed.Version || expected.Fingerprint != observed.Fingerprint {
		return false
	}
	target, err := filepath.EvalSymlinks(expected.Path)
	return err == nil && target == observed.Path && persist.MatchCLIFingerprint(expected.Path, expected.Fingerprint) && persist.MatchCLIFingerprint(observed.Path, observed.Fingerprint)
}
