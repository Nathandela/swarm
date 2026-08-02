package protocol

// FAILING-FIRST test for re-audit FINDING D (ADR-007 D8): on the remote tier handleLaunch
// validates filepath.EvalSymlinks(req.Cwd) against the launch policy, but hands the shim the
// UNRESOLVED req.Cwd. The CHECKED path must be the USED path, so a symlink cannot be validated
// and then re-pointed between the check and the launch. The daemon launch spec must carry the
// RESOLVED cwd.

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPolicy_LaunchUsesResolvedCwd: a remote launch whose cwd is an IN-ROOT symlink to an
// in-root directory is allowed, and the daemon receives the RESOLVED target path — not the
// unresolved symlink. RED today: daemonLaunchSpec carries req.Cwd (the symlink) verbatim.
func TestPolicy_LaunchUsesResolvedCwd(t *testing.T) {
	root := mustResolve(t, t.TempDir())
	target := filepath.Join(root, "work")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	link := filepath.Join(root, "link") // a symlink UNDER root pointing to target (also under root)
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	stub := newStubDaemon()
	sock := serveRemoteAPI(t, launchPolicyStub{stubDaemon: stub, roots: []string{root}})
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})

	req := policyLaunchReq(t)
	req.Cwd = link
	rc.writeControl(remoteLaunchControl(rep.EndpointID, req))
	if got := rc.readControl(); got.Op == OpError {
		t.Fatalf("remote launch via an in-root symlink refused: %q / %q (it resolves inside the root, so it must be allowed)", got.Error, got.ErrorCode)
	}

	specs := stub.launchSpecs()
	if len(specs) != 1 {
		t.Fatalf("daemon launched %d sessions; want 1", len(specs))
	}
	want := mustResolve(t, link) // == target
	if specs[0].Cwd != want {
		t.Fatalf("shim launch spec cwd = %q; want the RESOLVED path %q (ADR-007 D8: the checked path must be the used path, not the unresolved symlink %q)", specs[0].Cwd, want, link)
	}
}
