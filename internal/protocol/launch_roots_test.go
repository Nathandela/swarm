package protocol

// FAILING-FIRST tests for the remote-tier ALLOWED-CWD-ROOTS launch guard (fix-pack item
// 1b — the second half of the remote launch policy; item 1a landed env-drop + the option
// denylist). This slice confines a REMOTE launch to machine-configured cwd roots, checked
// against the RESOLVED (symlink-hardened) cwd, and fails CLOSED when no roots are
// configured. R-POL.3 (allowed-cwd roots), with R-POL.1 (remote tier only) and R-POL.2
// (policy before the cwd stat) preserved from 1a.
//
// SEAM pinned here (the implementer adds it — a new OPTIONAL backend interface discovered
// by type-assertion on the remote-tier Server's DaemonAPI, the SAME seam as KillSwitch /
// DeviceAuthenticator / JournalBackend):
//
//	// LaunchPolicy confines a remote launch to machine-configured cwd roots (R-POL.3).
//	// On the remote tier, handleLaunch resolves the request cwd with filepath.EvalSymlinks
//	// and calls RemoteLaunchAllowed(resolvedCwd); a non-nil error refuses the launch with
//	// CodePolicy — AFTER authz but BEFORE the cwd stat / any daemon side effect (R-POL.2),
//	// so a resolved cwd outside every root is refused with no side effect. An EMPTY root
//	// set denies every launch (fail-closed). A backend that does NOT implement it is
//	// unaffected (additive, like KillSwitch); production fail-closed is delivered by the
//	// assembly ALWAYS wiring a config-derived policy (empty-allowed by default) onto the
//	// coreAPI — see internal/skeleton/remote_policy_test.go.
//	type LaunchPolicy interface {
//	    RemoteLaunchAllowed(resolvedCwd string) error
//	}
//
// WHY EvalSymlinks in the protocol layer: symlink hardening is a security-critical
// invariant that must hold regardless of which LaunchPolicy is wired, so handleLaunch
// resolves the cwd BEFORE consulting the policy — the RESOLVED real path is what is
// checked. TestPolicy_SymlinkEscapeRefused proves it: a cwd that is a symlink TEXTUALLY
// under a root but RESOLVING outside it must still be refused.
//
// These tests COMPILE against today's tree (they do not name the not-yet-defined
// LaunchPolicy type — they rely on the method being discovered by type-assertion once the
// interface exists) and go RED at RUNTIME for the right reason: with no roots enforcement,
// an out-of-roots / symlink-escaping / empty-roots remote launch is ALLOWED today, so the
// expected CodePolicy refusal never comes.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// launchPolicyStub is a remote backend that is a full DaemonAPI + DeviceAuthenticator (via
// the embedded *stubDaemon, whose authzFn accepts by default) AND a LaunchPolicy confining
// launches to `roots`. Because it satisfies both optional interfaces, a remote-tier Server
// type-asserts BOTH off the same backend — exactly the production assembly. `roots` are
// CANONICAL (already symlink-resolved by the test), so matching the protocol layer's
// resolved cwd is a pure prefix check; an EMPTY roots slice denies every launch.
type launchPolicyStub struct {
	*stubDaemon
	roots []string
}

// RemoteLaunchAllowed makes launchPolicyStub the pinned LaunchPolicy: it allows a launch
// only when the (already-resolved) cwd is one of, or lies within, the configured roots.
func (p launchPolicyStub) RemoteLaunchAllowed(resolvedCwd string) error {
	for _, r := range p.roots {
		if resolvedCwd == r || strings.HasPrefix(resolvedCwd, r+string(filepath.Separator)) {
			return nil
		}
	}
	return fmt.Errorf("cwd %q is outside all %d configured root(s)", resolvedCwd, len(p.roots))
}

// mustResolve returns the symlink-resolved (canonical) form of a path, so a test's roots
// and its expectations match what filepath.EvalSymlinks yields for the launch cwd — on
// macOS the temp dir itself lives under symlinked /var and /tmp.
func mustResolve(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", p, err)
	}
	return r
}

// TestPolicy_LaunchOutsideAllowedRootsRefused (R-POL.3): a remote launch whose cwd resolves
// OUTSIDE every configured root is refused CodePolicy with no daemon side effect; a launch
// whose cwd lies INSIDE a root is allowed (the guard must confine, not block wholesale).
func TestPolicy_LaunchOutsideAllowedRootsRefused(t *testing.T) {
	root := mustResolve(t, t.TempDir())
	outside := mustResolve(t, t.TempDir()) // a sibling temp dir, OUTSIDE root

	t.Run("outside_root_refused", func(t *testing.T) {
		stub := newStubDaemon()
		sock := serveRemoteAPI(t, launchPolicyStub{stubDaemon: stub, roots: []string{root}})
		rc := rawDial(t, sock)
		rep := rc.hello(Version, []string{CapRemoteGateway})

		req := policyLaunchReq(t)
		req.Cwd = outside // an existing directory, but outside the one allowed root
		rc.writeControl(remoteLaunchControl(rep.EndpointID, req))

		got := rc.readControl()
		if got.Op != OpError || got.ErrorCode != CodePolicy {
			t.Fatalf("remote launch with cwd outside all roots = op %q code %q; want error/policy (R-POL.3)", got.Op, got.ErrorCode)
		}
		if n := len(stub.launchSpecs()); n != 0 {
			t.Fatalf("daemon launched %d sessions for an out-of-roots op; want 0 (refused before side effect)", n)
		}
	})

	t.Run("inside_root_allowed", func(t *testing.T) {
		work := filepath.Join(root, "work")
		if err := os.MkdirAll(work, 0o700); err != nil {
			t.Fatalf("mkdir work: %v", err)
		}
		stub := newStubDaemon()
		sock := serveRemoteAPI(t, launchPolicyStub{stubDaemon: stub, roots: []string{root}})
		rc := rawDial(t, sock)
		rep := rc.hello(Version, []string{CapRemoteGateway})

		req := policyLaunchReq(t)
		req.Cwd = work // inside the allowed root
		rc.writeControl(remoteLaunchControl(rep.EndpointID, req))

		if got := rc.readControl(); got.Op == OpError {
			t.Fatalf("remote launch with cwd inside an allowed root refused: %q / %q (R-POL.3 must ALLOW in-root launches)", got.Error, got.ErrorCode)
		}
		if n := len(stub.launchSpecs()); n != 1 {
			t.Fatalf("daemon launched %d sessions for an in-root op; want 1", n)
		}
	})
}

// TestPolicy_SymlinkEscapeRefused (R-POL.3 symlink hardening): a cwd that is a symlink
// TEXTUALLY under an allowed root but whose target RESOLVES outside it must be refused —
// the RESOLVED real path (filepath.EvalSymlinks) is what is checked, so a symlink cannot
// smuggle a launch out of the configured roots. RED today: no resolution/roots check runs,
// so the os.Stat (which follows the symlink to a valid dir) passes and the launch succeeds.
func TestPolicy_SymlinkEscapeRefused(t *testing.T) {
	root := mustResolve(t, t.TempDir())
	outside := mustResolve(t, t.TempDir()) // the real target directory, OUTSIDE root
	link := filepath.Join(root, "escape")  // a symlink placed UNDER root ...
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err) // ... but pointing OUTSIDE it
	}

	stub := newStubDaemon()
	sock := serveRemoteAPI(t, launchPolicyStub{stubDaemon: stub, roots: []string{root}})
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})

	req := policyLaunchReq(t)
	req.Cwd = link // textually within root; resolves to `outside`
	rc.writeControl(remoteLaunchControl(rep.EndpointID, req))

	got := rc.readControl()
	if got.Op != OpError || got.ErrorCode != CodePolicy {
		t.Fatalf("remote launch whose cwd symlink escapes the root = op %q code %q; want error/policy "+
			"(the RESOLVED path must be checked — filepath.EvalSymlinks, R-POL.3)", got.Op, got.ErrorCode)
	}
	if n := len(stub.launchSpecs()); n != 0 {
		t.Fatalf("daemon launched %d sessions for a symlink-escape op; want 0", n)
	}
}

// TestPolicy_EmptyRootsFailClosed (R-POL.3/.7 fail-closed): a remote-tier policy that
// configures NO roots refuses EVERY remote launch (even one with a perfectly valid,
// existing cwd) — an empty root set denies, it never fails open. The R-POL.1 contrast:
// the SAME empty-roots backend on the OWNER (main) tier launches normally, because cwd
// roots confine the REMOTE tier only; local/owner launches stay unconfined.
func TestPolicy_EmptyRootsFailClosed(t *testing.T) {
	t.Run("remote_empty_roots_denies", func(t *testing.T) {
		stub := newStubDaemon()
		sock := serveRemoteAPI(t, launchPolicyStub{stubDaemon: stub, roots: nil}) // present, but no roots
		rc := rawDial(t, sock)
		rep := rc.hello(Version, []string{CapRemoteGateway})

		req := policyLaunchReq(t) // a valid, existing cwd — the ONLY defect is "no root allows it"
		rc.writeControl(remoteLaunchControl(rep.EndpointID, req))

		got := rc.readControl()
		if got.Op != OpError || got.ErrorCode != CodePolicy {
			t.Fatalf("remote launch with NO configured roots = op %q code %q; want error/policy "+
				"(empty roots MUST fail closed — no remote launch, R-POL.3/.7)", got.Op, got.ErrorCode)
		}
		if n := len(stub.launchSpecs()); n != 0 {
			t.Fatalf("daemon launched %d sessions under an empty-roots (fail-closed) policy; want 0", n)
		}
	})

	t.Run("local_unaffected_by_empty_roots", func(t *testing.T) {
		stub := newStubDaemon()
		c := dialClient(t, serveOwner(t, launchPolicyStub{stubDaemon: stub, roots: nil}), nil)

		req := LaunchReq{Agent: "claude", Cwd: t.TempDir(), Cols: 80, Rows: 24}
		if _, err := c.Launch(req); err != nil {
			t.Fatalf("owner-tier launch refused under an empty-roots policy: %v — R-POL.1 leaves LOCAL launches unconfined", err)
		}
		if n := len(stub.launchSpecs()); n != 1 {
			t.Fatalf("owner-tier launch executed %d times; want 1 (cwd roots govern the remote tier only)", n)
		}
	})
}
