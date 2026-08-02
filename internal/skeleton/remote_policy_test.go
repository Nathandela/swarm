package skeleton

// FAILING-FIRST tests for the remote launch-policy CONFIG FILE (fix-pack item 1b, R-POL.7):
// `<stateDir>/remote-policy.json` (0600, versioned) is the machine-configured source of the
// allowed-cwd roots that confine remote launches (R-POL.3). It is loaded on assembly start;
// a MALFORMED file FAILS CLOSED (empty-allowed, deny-all) — it must NEVER fail open; and a
// well-formed file round-trips.
//
// API pinned here (the implementer adds it, in the skeleton layer where the assembly reads
// <stateDir> config and wires the optional backends onto the coreAPI):
//
//	// writeRemoteLaunchPolicy persists the allowed cwd roots to <stateDir>/remote-policy.json
//	// as versioned JSON with 0600 perms (owner-only; the file governs remote launch).
//	func writeRemoteLaunchPolicy(stateDir string, roots []string) error
//
//	// loadRemoteLaunchPolicy reads <stateDir>/remote-policy.json into a protocol.LaunchPolicy.
//	// A MISSING or MALFORMED file yields an empty-allowed (deny-all) policy — fail-closed,
//	// never fail-open. It ALWAYS returns a non-nil, safe policy; any error is advisory.
//	func loadRemoteLaunchPolicy(stateDir string) (protocol.LaunchPolicy, error)
//
// The loaded protocol.LaunchPolicy is exactly the interface consulted by the remote-tier
// protocol server (internal/protocol/launch_roots_test.go): RemoteLaunchAllowed(resolvedCwd)
// returns nil to allow, non-nil to refuse. The assembly attaches it to the coreAPI so a
// remote-tier launch is confined to the file's roots — and with no file, fail-closed.
//
// RED today: neither loadRemoteLaunchPolicy nor writeRemoteLaunchPolicy exists, so this file
// does not compile — an acceptable compile-fail RED for a new API, unambiguous by name.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
)

// resolveDir returns the symlink-resolved (canonical) form of a path, so roots written to
// the policy match what filepath.EvalSymlinks yields for a launch cwd (macOS temp dirs live
// under symlinked /var and /tmp).
func resolveDir(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", p, err)
	}
	return r
}

// TestPolicy_MalformedConfigFailsClosed (R-POL.7): a corrupt/unparseable remote-policy.json
// loads to an empty-allowed (deny-all) policy — it must NOT fail open (allow-all) and must
// NOT return a nil policy. The security-critical property: on a config it cannot trust, the
// daemon denies remote launches rather than permitting them.
func TestPolicy_MalformedConfigFailsClosed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "remote-policy.json"), []byte("{ not valid json "), 0o600); err != nil {
		t.Fatalf("write malformed policy: %v", err)
	}

	pol, err := loadRemoteLaunchPolicy(dir)
	var _ protocol.LaunchPolicy = pol // pin: the loader returns exactly the interface the remote-tier server consults
	if pol == nil {
		t.Fatalf("malformed policy loaded a nil LaunchPolicy (err=%v); must fail closed to a deny-all policy, never nil", err)
	}
	// A path that a fail-OPEN loader might wrongly allow must be DENIED under fail-closed.
	if allowErr := pol.RemoteLaunchAllowed(resolveDir(t, dir)); allowErr == nil {
		t.Fatalf("malformed remote-policy.json ALLOWED a launch (fail-open); a corrupt config MUST deny all (fail-closed, R-POL.7)")
	}
}

// TestPolicy_MissingConfigFailsClosed (R-POL.3/.7 default): with NO remote-policy.json at
// all, the loader yields an empty-allowed (deny-all) policy — the fail-closed default the
// assembly relies on so a daemon with no configured roots refuses every remote launch.
func TestPolicy_MissingConfigFailsClosed(t *testing.T) {
	pol, err := loadRemoteLaunchPolicy(t.TempDir()) // empty state dir: no policy file
	if err != nil {
		t.Fatalf("loadRemoteLaunchPolicy on a missing file returned an error: %v (missing must be a clean deny-all, not an error)", err)
	}
	if pol == nil {
		t.Fatal("loadRemoteLaunchPolicy returned a nil policy for a missing file; want a deny-all policy")
	}
	if allowErr := pol.RemoteLaunchAllowed(resolveDir(t, t.TempDir())); allowErr == nil {
		t.Fatalf("missing remote-policy.json ALLOWED a launch; the default MUST fail closed (deny all, R-POL.3)")
	}
}

// TestPolicy_ConfigRoundTrip (R-POL.7): a well-formed policy written with roots round-trips
// — write -> load -> a cwd within (or equal to) a configured root is allowed, one outside
// every root is denied — and the on-disk file carries 0600 perms.
func TestPolicy_ConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	rootA := resolveDir(t, t.TempDir())
	rootB := resolveDir(t, t.TempDir())

	if err := writeRemoteLaunchPolicy(dir, []string{rootA, rootB}); err != nil {
		t.Fatalf("writeRemoteLaunchPolicy: %v", err)
	}

	// 0600 perms enforced: the file governs which cwds a remote phone may launch in.
	fi, err := os.Stat(filepath.Join(dir, "remote-policy.json"))
	if err != nil {
		t.Fatalf("stat policy file: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("remote-policy.json perms = %o; want 0600 (owner-only)", perm)
	}

	pol, err := loadRemoteLaunchPolicy(dir)
	if err != nil {
		t.Fatalf("loadRemoteLaunchPolicy after write: %v", err)
	}
	if pol == nil {
		t.Fatal("loadRemoteLaunchPolicy returned nil after a well-formed write")
	}
	// The roots round-tripped: within rootA, equal to rootB -> allowed; outside both -> denied.
	if allowErr := pol.RemoteLaunchAllowed(filepath.Join(rootA, "sub")); allowErr != nil {
		t.Errorf("cwd within rootA denied after round-trip: %v", allowErr)
	}
	if allowErr := pol.RemoteLaunchAllowed(rootB); allowErr != nil {
		t.Errorf("cwd equal to rootB denied after round-trip: %v", allowErr)
	}
	if allowErr := pol.RemoteLaunchAllowed(resolveDir(t, t.TempDir())); allowErr == nil {
		t.Error("cwd outside all roots allowed after round-trip; want denied")
	}
}
