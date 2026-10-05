package shim

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/Nathandela/swarm/internal/persist"
)

// CLIObservationFile is written only after the PTY (and declared backend) starts
// with the same installation metadata seen before either spawn. It is not proof
// of provider conversation adoption, loaded wrapper dependencies, or process version.
const CLIObservationFile = "observed-cli.json"

func cliInstallationMatches(cfg Config) bool {
	identity := cfg.CLIIdentity
	if identity == nil || len(cfg.Argv) == 0 || identity.Path != cfg.Argv[0] {
		return false
	}
	if !persist.MatchCLIFingerprint(identity.Path, identity.Fingerprint) {
		return false
	}
	if cfg.Backend != nil {
		if !persist.MatchCLIFingerprint(cfg.Backend.Program, identity.Fingerprint) {
			return false
		}
	}
	return true
}

func recordCLIObservation(cfg Config) {
	if !cliInstallationMatches(cfg) {
		return
	}
	data, err := json.Marshal(cfg.CLIIdentity)
	if err == nil {
		_ = writeFileAtomic(cfg.SessionDir, CLIObservationFile, data)
	}
}

// ReadCLIObservation returns nil for absent/invalid observations (including old shims).
func ReadCLIObservation(sessionDir string) *persist.CLIIdentity {
	data, err := os.ReadFile(filepath.Join(sessionDir, CLIObservationFile))
	if err != nil {
		return nil
	}
	var identity persist.CLIIdentity
	if json.Unmarshal(data, &identity) != nil || identity.Path == "" || identity.Version == "" || identity.Fingerprint == "" {
		return nil
	}
	return &identity
}
