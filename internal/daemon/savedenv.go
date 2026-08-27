package daemon

// The daemon's saved start environment (auto-upgrade plan, L2).
//
// A restart requested by nobody at a keyboard cannot spawn the replacement from the
// caller's environment: under a launchd timer that environment is
// PATH=/usr/bin:/bin:/usr/sbin:/sbin with no credentials, and every phone-launched
// session afterwards resolves its agent binary — and its billing — through
// PolicyEnv from the DAEMON's environment (policyenv.go). So the daemon records
// what it started with, and an unattended restart spawns from that file instead.
//
// What is recorded is persist.FilterEnv of the daemon's own environment: the same
// S-2 allowlist already written into every session's meta.json, so this file is not
// a new exposure class.

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// savedEnvFileName is the daemon's saved start environment within the state dir.
const savedEnvFileName = "daemon.env"

// SavedEnvPath returns the path of the saved start environment for a state dir. It
// only builds the path; the file may not exist.
func SavedEnvPath(stateDir string) string {
	return filepath.Join(stateDir, savedEnvFileName)
}

// writeSavedEnv records the daemon's current environment, allowlist-filtered, as
// one KEY=VALUE per line at SavedEnvPath, mode 0600 inside the 0700 state dir.
//
// The write is atomic (temp file + rename within the same directory), so a reader
// racing a daemon start sees either the previous content or the new one, never a
// half-written file. Every start overwrites it, which is what keeps the saved set
// as fresh as the last start.
//
// The line format cannot round-trip a value that itself contains a newline;
// LoadSavedEnv would read such an entry back as two. Nothing on the allowlist
// (PATH, HOME, SHELL, TERM, the locale family, venv/conda, the two API keys) holds
// a newline in practice, and the alternative encodings buy nothing this daemon
// needs.
func writeSavedEnv(stateDir string) error {
	env := PolicyEnv(nil) // FilterEnv over daemonEnviron(): the S-2 allowlist, one source

	var buf strings.Builder
	for _, kv := range env {
		buf.WriteString(kv)
		buf.WriteByte('\n')
	}

	tmp, err := os.CreateTemp(stateDir, savedEnvFileName+".tmp*") // created 0600
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after a successful rename

	if _, err := tmp.WriteString(buf.String()); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Explicit, so the mode does not depend on the daemon's umask.
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpName, SavedEnvPath(stateDir))
}

// LoadSavedEnv reads the environment a daemon saved at its last start, as a
// KEY=VALUE slice in the order it was written. Blank lines are ignored.
//
// When no daemon has ever saved one, the returned error satisfies
// errors.Is(err, os.ErrNotExist) — the caller's cue that an unattended restart has
// no environment to spawn from and must refuse rather than fall back to its own.
func LoadSavedEnv(stateDir string) ([]string, error) {
	f, err := os.Open(SavedEnvPath(stateDir))
	if err != nil {
		return nil, fmt.Errorf("daemon: read saved environment: %w", err)
	}
	defer func() { _ = f.Close() }()

	var env []string
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		if line := scan.Text(); line != "" {
			env = append(env, line)
		}
	}
	if err := scan.Err(); err != nil {
		return nil, fmt.Errorf("daemon: read saved environment: %w", err)
	}
	return env, nil
}
