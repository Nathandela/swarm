package skeleton

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Nathandela/swarm/internal/protocol"
)

// remotePolicyFile is the machine-configured remote launch policy, read on assembly start
// from <stateDir>/remote-policy.json (R-POL.7). It confines remote launches to allowed cwd
// roots (R-POL.3); a missing or malformed file fails CLOSED to a deny-all policy.
const remotePolicyFile = "remote-policy.json"

// remotePolicyDoc is the on-disk schema: a version tag plus the allowed cwd roots. Kept
// minimal and versioned so the format can evolve without a silent reinterpretation.
type remotePolicyDoc struct {
	Version         int      `json:"version"`
	AllowedCwdRoots []string `json:"allowed_cwd_roots"`
}

// remoteLaunchPolicy is the loaded protocol.LaunchPolicy (R-POL.3): a remote launch is
// allowed only when its RESOLVED cwd equals, or lies within, one of the configured roots.
// An EMPTY root set denies every launch (fail-closed, R-POL.7). Roots are cleaned/resolved
// at load so matching the protocol layer's EvalSymlinks-resolved cwd is a pure prefix check.
type remoteLaunchPolicy struct {
	roots []string
}

// RemoteLaunchAllowed allows resolvedCwd iff it equals, or lies within, a configured root
// (R-POL.3). The trailing-separator guard makes "/a/bc" NOT match root "/a/b".
func (p remoteLaunchPolicy) RemoteLaunchAllowed(resolvedCwd string) error {
	clean := filepath.Clean(resolvedCwd)
	for _, r := range p.roots {
		if clean == r || strings.HasPrefix(clean, r+string(filepath.Separator)) {
			return nil
		}
	}
	return fmt.Errorf("cwd %q is outside all %d configured launch root(s)", clean, len(p.roots))
}

// cleanRoots normalizes configured roots so they match the protocol layer's resolved cwd:
// each is filepath.Clean'd and, best-effort, symlink-resolved (an unresolvable root keeps
// its cleaned form). Empty entries are dropped, leaving an empty (deny-all) set when none.
func cleanRoots(roots []string) []string {
	out := make([]string, 0, len(roots))
	for _, r := range roots {
		if r == "" {
			continue
		}
		c := filepath.Clean(r)
		if resolved, err := filepath.EvalSymlinks(c); err == nil {
			c = resolved
		}
		out = append(out, c)
	}
	return out
}

// writeRemoteLaunchPolicy persists the allowed cwd roots to <stateDir>/remote-policy.json as
// versioned JSON with 0600 perms (owner-only; the file governs which cwds a remote phone may
// launch in, R-POL.7).
func writeRemoteLaunchPolicy(stateDir string, roots []string) error {
	data, err := json.Marshal(remotePolicyDoc{Version: 1, AllowedCwdRoots: roots})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(stateDir, remotePolicyFile), data, 0o600)
}

// loadRemoteLaunchPolicy reads <stateDir>/remote-policy.json into a protocol.LaunchPolicy. A
// MISSING file is a clean empty-allowed (deny-all) policy with no error; a MALFORMED or
// unreadable file also fails CLOSED to the same deny-all policy (never fail-open, never nil),
// returning the underlying error as advisory only. It ALWAYS returns a non-nil, safe policy
// (R-POL.3/.7) — the assembly relies on this to be fail-closed by default with no config.
func loadRemoteLaunchPolicy(stateDir string) (protocol.LaunchPolicy, error) {
	data, err := os.ReadFile(filepath.Join(stateDir, remotePolicyFile))
	if os.IsNotExist(err) {
		return remoteLaunchPolicy{}, nil // missing => deny-all default (fail-closed)
	}
	if err != nil {
		return remoteLaunchPolicy{}, err // unreadable => deny-all; error advisory only
	}
	var doc remotePolicyDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return remoteLaunchPolicy{}, err // malformed => deny-all (fail-closed); error advisory
	}
	return remoteLaunchPolicy{roots: cleanRoots(doc.AllowedCwdRoots)}, nil
}
