package protocol

// FAILING-FIRST tests for the remote-tier launch policy guards (fix-pack 1a, closing
// the sharpest part of review finding HIGH-1: a remote launch executes with no policy
// guards). CONFIG-FREE slice: both guards are HARD-CODED and unconditional on the
// remote tier — no remote-policy.json (that is slice 1b).
//
// Enforcement layer: internal/protocol handleLaunch. This is the seam that already
// distinguishes remote from local for launch (cc.srv.remoteTier), owns the choke
// point requireRemoteAuthz, and builds the daemon LaunchSpec via daemonLaunchSpec —
// and the stubDaemon here lets a test OBSERVE the exact LaunchSpec (ClientEnv,
// Options) the daemon receives, which is what these guards must change.
//
// The two guards pinned here:
//   - R-POL.5 "No phone-supplied env": a REMOTE launch must IGNORE LaunchReq.Env
//     ENTIRELY (env sourced solely from daemon policy). Today daemonLaunchSpec sets
//     ClientEnv = persist.FilterEnv(req.Env), which merely FILTERS — allowlisted vars
//     (ANTHROPIC_API_KEY, PATH, ...) still survive. Because LaunchContentHash EXCLUDES
//     Env, a filtered-not-dropped env is an UNAUTHENTICATED channel a compromised
//     gateway can inject under a valid device signature. The remote-tier spec's
//     ClientEnv must be EMPTY.
//   - R-POL.4 "Hard-coded refusal of dangerous options": a REMOTE launch carrying a
//     forbidden option is refused (CodePolicy) with NO launch side effect. The real
//     wire representations of the "full-access" family for the two shipped adapters
//     are Claude's `dangerously-skip-permissions=true` (internal/adapter/claude/
//     claude.go:114,193) and Codex's `sandbox=danger-full-access` (internal/adapter/
//     codex/codex.go:89). The denylist is hard-coded, not config-overridable.
//
// R-POL.2 (authorization/policy BEFORE argv/cwd validation) and R-POL.1 (guards are
// remote-tier scoped; LOCAL/owner launches keep today's behavior) are pinned below.
//
// Seam for the implementer (protocol layer, handleLaunch):
//   - env-drop: when cc.srv.remoteTier, the built LaunchSpec.ClientEnv must be empty
//     (drop req.Env; do NOT FilterEnv it). daemonLaunchSpec is where ClientEnv is set.
//   - denylist: when cc.srv.remoteTier, refuse a forbidden option with CodePolicy
//     BEFORE the agent/cwd/options/dims validation (so the policy refusal precedes the
//     cwd stat, R-POL.2) and before any daemon side effect.

import (
	"path/filepath"
	"testing"
	"time"
)

// policyLaunchReq is a launch request valid in every OTHER respect (agent, cwd, dims),
// so each policy test isolates the ONE field under test (env or a single option).
func policyLaunchReq(t *testing.T) LaunchReq {
	t.Helper()
	return LaunchReq{Agent: "claude", Cwd: t.TempDir(), Cols: 80, Rows: 24}
}

// remoteLaunchControl builds a remote launch Control that PASSES the device-auth choke
// point (the stub authenticator accepts by default): operation_id + device identity
// fields present, a fresh expiry. The launch spec rides in Launch. Anything the guards
// then refuse is refused on policy grounds, not for a missing structural field.
func remoteLaunchControl(ep string, req LaunchReq) Control {
	exp := time.Now().Add(time.Minute)
	return Control{
		Op:          OpLaunch,
		EndpointID:  ep,
		Launch:      &req,
		OperationID: "devA:01JLAUNCH000000000000000",
		DeviceID:    "devA",
		DeviceSig:   "sig",
		ExpiresAt:   &exp,
	}
}

// TestPolicy_RemoteLaunchDropsClientEnv (R-POL.5): a remote launch carrying a poisoned
// env results in the daemon receiving a LaunchSpec with EMPTY env — phone-supplied env
// is DROPPED entirely, not filtered. RED today: FilterEnv keeps ANTHROPIC_API_KEY/PATH.
func TestPolicy_RemoteLaunchDropsClientEnv(t *testing.T) {
	stub := newStubDaemon()
	// This test is about R-POL.5 (env-drop), not R-POL.3 (cwd confinement), so wire a
	// permissive LaunchPolicy — otherwise the F4 fail-closed-absent guard would refuse the
	// launch before ClientEnv is ever inspected.
	sock := serveRemoteAPI(t, allowAllLaunchPolicy{stub})
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})

	req := policyLaunchReq(t)
	// A poisoned env: an injection vector AND an allowlisted credential. Under today's
	// FilterEnv, EVIL is dropped but ANTHROPIC_API_KEY/PATH SURVIVE — exactly the
	// unauthenticated channel R-POL.5 closes by dropping env entirely on the remote tier.
	req.Env = []string{"EVIL=x", "ANTHROPIC_API_KEY=leak", "PATH=/usr/bin"}
	rc.writeControl(remoteLaunchControl(rep.EndpointID, req))

	if got := rc.readControl(); got.Op == OpError {
		t.Fatalf("remote launch refused unexpectedly: %q / %q", got.Error, got.ErrorCode)
	}
	specs := stub.launchSpecs()
	if len(specs) != 1 {
		t.Fatalf("DaemonAPI.Launch called %d times, want 1", len(specs))
	}
	if n := len(specs[0].ClientEnv); n != 0 {
		t.Fatalf("remote launch forwarded %d env entries %v; R-POL.5 requires phone-supplied env be "+
			"DROPPED entirely (empty ClientEnv), not filtered — it is an unauthenticated channel "+
			"(LaunchContentHash excludes Env)", n, specs[0].ClientEnv)
	}
}

// TestPolicy_RemoteForbiddenOptionsRefused (R-POL.4): a remote launch whose Options
// carry a forbidden dangerous option is refused (CodePolicy) with NO launch side
// effect. Every other field is valid, so the ONLY reason to refuse is the option.
// RED today: no denylist exists, so the launch is forwarded and succeeds.
func TestPolicy_RemoteForbiddenOptionsRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts map[string]string
	}{
		// Claude's real skip-permissions option key (internal/adapter/claude/claude.go).
		{"dangerously-skip-permissions", map[string]string{"dangerously-skip-permissions": "true"}},
		// Codex's real full-access representation (internal/adapter/codex/codex.go): the
		// `sandbox` option set to `danger-full-access` — the plan's "full-access-sandbox".
		{"sandbox_danger-full-access", map[string]string{"sandbox": "danger-full-access"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := newStubDaemon()
			sock := serveRemote(t, stub)
			rc := rawDial(t, sock)
			rep := rc.hello(Version, []string{CapRemoteGateway})

			req := policyLaunchReq(t) // valid agent/cwd/dims: the ONLY defect is the option
			req.Options = tc.opts
			rc.writeControl(remoteLaunchControl(rep.EndpointID, req))

			got := rc.readControl()
			if got.Op != OpError || got.ErrorCode != CodePolicy {
				t.Fatalf("remote launch with forbidden option %v = op %q code %q; want error/policy "+
					"(hard-coded R-POL.4 denylist)", tc.opts, got.Op, got.ErrorCode)
			}
			if n := len(stub.launchSpecs()); n != 0 {
				t.Fatalf("daemon launched %d sessions for a forbidden-option op; want 0 (refused before side effect)", n)
			}
		})
	}
}

// TestPolicy_RemoteLaunchAllowsSafeOptions (R-POL.4 boundary guard): the denylist must
// be VALUE-aware, refusing only the dangerous VALUE of a guarded option — never the key
// wholesale. A remote launch carrying the SAFE default of an otherwise-guarded key must
// SUCCEED (the daemon receives the launch; the option is forwarded), NOT be refused.
// This is GREEN today and must STAY green after the value-aware denylist lands — it goes
// RED only if the implementer over-blocks by KEY alone, which would wrongly refuse a
// legitimate remote launch (a functional regression).
func TestPolicy_RemoteLaunchAllowsSafeOptions(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts map[string]string
	}{
		// The SAFE default of Claude's skip-permissions key (form default "false",
		// internal/tui/form_perf_test.go): guarded by VALUE ("true"), not by key.
		{"dangerously-skip-permissions_false_allowed", map[string]string{"dangerously-skip-permissions": "false"}},
		// Codex's safe default sandbox mode (internal/adapter/codex/codex.go:88-89): only
		// danger-full-access is dangerous; workspace-write must be allowed on remote.
		{"sandbox_workspace-write_allowed", map[string]string{"sandbox": "workspace-write"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := newStubDaemon()
			// This test is about R-POL.4's VALUE-aware denylist, not R-POL.3 (cwd
			// confinement), so wire a permissive LaunchPolicy — otherwise the F4
			// fail-closed-absent guard would refuse the launch regardless of the option.
			sock := serveRemoteAPI(t, allowAllLaunchPolicy{stub})
			rc := rawDial(t, sock)
			rep := rc.hello(Version, []string{CapRemoteGateway})

			req := policyLaunchReq(t) // valid agent/cwd/dims: the option is the ONLY variable
			req.Options = tc.opts
			rc.writeControl(remoteLaunchControl(rep.EndpointID, req))

			got := rc.readControl()
			if got.Op == OpError {
				t.Fatalf("remote launch with SAFE option %v refused (%q / %q); the denylist must be "+
					"VALUE-aware — only dangerous values are refused, never the key wholesale (R-POL.4)",
					tc.opts, got.Error, got.ErrorCode)
			}
			specs := stub.launchSpecs()
			if len(specs) != 1 {
				t.Fatalf("DaemonAPI.Launch called %d times for a safe-option remote launch; want 1", len(specs))
			}
			// The safe option must be FORWARDED to the daemon, not silently stripped.
			for k, v := range tc.opts {
				if specs[0].Options[k] != v {
					t.Errorf("safe option %s=%s not forwarded to the daemon (got Options %v)", k, v, specs[0].Options)
				}
			}
		})
	}
}

// TestPolicy_RemoteLaunchChecked_LocalLaunchNot (R-POL.1): the SAME poisoned env and
// the SAME forbidden option, on an OWNER/LOCAL-tier launch, are NEITHER dropped NOR
// refused — env is filtered per today's behavior and the option is forwarded. This is
// the contrast case proving the guards are remote-tier scoped; it is GREEN today and
// must STAY green (it goes RED only if the implementer wrongly applies the guards to
// the local tier).
func TestPolicy_RemoteLaunchChecked_LocalLaunchNot(t *testing.T) {
	stub := newStubDaemon()
	c := dialClient(t, serveStub(t, stub), nil) // MAIN (owner) tier — not ServeRemote

	req := LaunchReq{
		Agent: "claude",
		Cwd:   t.TempDir(),
		Cols:  80, Rows: 24,
		Env:     []string{"EVIL=x", "ANTHROPIC_API_KEY=keep", "PATH=/usr/bin"},
		Options: map[string]string{"dangerously-skip-permissions": "true"},
	}
	if _, err := c.Launch(req); err != nil {
		t.Fatalf("local launch refused: %v — R-POL.1 leaves LOCAL/owner launches unaffected", err)
	}
	specs := stub.launchSpecs()
	if len(specs) != 1 {
		t.Fatalf("local DaemonAPI.Launch called %d times, want 1", len(specs))
	}
	// Env: today's FilterEnv behavior is UNCHANGED on the local tier — allowlisted vars
	// survive, injection vectors are dropped. The remote env-drop must not leak here.
	keys := envKeys(specs[0].ClientEnv)
	if !keys["ANTHROPIC_API_KEY"] || !keys["PATH"] {
		t.Errorf("local launch dropped allowlisted env %v; the remote env-drop must be remote-tier scoped (R-POL.1)", specs[0].ClientEnv)
	}
	if keys["EVIL"] {
		t.Errorf("local launch forwarded a non-allowlisted env var: %v", specs[0].ClientEnv)
	}
	// Options: the remote denylist must NOT apply to a local launch.
	if specs[0].Options["dangerously-skip-permissions"] != "true" {
		t.Errorf("local launch stripped/altered a launch option %v; the remote denylist must be remote-tier scoped (R-POL.4)", specs[0].Options)
	}
}

// TestPolicy_AuthzPrecedesArgvValidation (R-POL.2): for a remote launch the
// authorization/policy verdict is reached BEFORE argv composition / cwd stat / any
// side effect, and errors distinguish not_authorized (a rejected signature/capability)
// from invalid_field (a missing structural field). The policy_refusal_precedes_cwd_stat
// sub-case is RED today: with a forbidden option AND a bad cwd, today's code has no
// denylist so it falls through to the cwd stat and returns a cwd error, not CodePolicy.
func TestPolicy_AuthzPrecedesArgvValidation(t *testing.T) {
	// A rejected device signature/capability on an otherwise-valid remote launch:
	// refused not_authorized, never launched. Authorization runs before argv/cwd work.
	t.Run("forged_sig_not_authorized", func(t *testing.T) {
		stub := newStubDaemon()
		stub.authzFn = func(DeviceCommandAuth) error { return errForged }
		sock := serveRemote(t, stub)
		rc := rawDial(t, sock)
		rep := rc.hello(Version, []string{CapRemoteGateway})

		req := policyLaunchReq(t) // valid cwd/agent/dims
		rc.writeControl(remoteLaunchControl(rep.EndpointID, req))
		got := rc.readControl()
		if got.Op != OpError || got.ErrorCode != CodeNotAuthorized {
			t.Fatalf("forged remote launch = op %q code %q; want error/not_authorized", got.Op, got.ErrorCode)
		}
		if n := len(stub.launchSpecs()); n != 0 {
			t.Fatalf("daemon launched %d sessions for a rejected op; want 0", n)
		}
	})

	// operation_id present but device identity fields absent: a STRUCTURAL defect ->
	// invalid_field (distinct from not_authorized), refused before any launch.
	t.Run("missing_device_fields_invalid_field", func(t *testing.T) {
		stub := newStubDaemon()
		sock := serveRemote(t, stub)
		rc := rawDial(t, sock)
		rep := rc.hello(Version, []string{CapRemoteGateway})

		req := policyLaunchReq(t)
		rc.writeControl(Control{
			Op:          OpLaunch,
			EndpointID:  rep.EndpointID,
			Launch:      &req,
			OperationID: "devA:01JLAUNCH000000000000000",
			// DeviceID/DeviceSig/ExpiresAt intentionally omitted.
		})
		got := rc.readControl()
		if got.Op != OpError || got.ErrorCode != CodeInvalidField {
			t.Fatalf("device-fields-missing remote launch = op %q code %q; want error/invalid_field", got.Op, got.ErrorCode)
		}
		if n := len(stub.launchSpecs()); n != 0 {
			t.Fatalf("daemon launched %d sessions for a structurally-invalid op; want 0", n)
		}
	})

	// A remote-policy refusal (forbidden option) must be evaluated BEFORE argv/cwd
	// validation: with a forbidden option AND a nonexistent cwd, the refusal must be the
	// POLICY one (CodePolicy), not a cwd error — proving the policy gate precedes the
	// cwd stat and produces no side effect.
	t.Run("policy_refusal_precedes_cwd_stat", func(t *testing.T) {
		stub := newStubDaemon()
		sock := serveRemote(t, stub)
		rc := rawDial(t, sock)
		rep := rc.hello(Version, []string{CapRemoteGateway})

		req := policyLaunchReq(t)
		req.Cwd = filepath.Join(t.TempDir(), "does", "not", "exist")            // argv-stage defect
		req.Options = map[string]string{"dangerously-skip-permissions": "true"} // policy-stage defect
		rc.writeControl(remoteLaunchControl(rep.EndpointID, req))
		got := rc.readControl()
		if got.Op != OpError || got.ErrorCode != CodePolicy {
			t.Fatalf("forbidden-option + bad-cwd remote launch = op %q code %q; want error/policy "+
				"(policy must be checked BEFORE the cwd stat, R-POL.2)", got.Op, got.ErrorCode)
		}
		if n := len(stub.launchSpecs()); n != 0 {
			t.Fatalf("daemon launched %d sessions for a policy-refused op; want 0", n)
		}
	})
}
