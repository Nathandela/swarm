package main

// FAILING-FIRST tests for the S4b review findings F1 and F4 -- the two PB-LIFE-7 routes
// that survived the B15 socket-default fix.
//
// F1. B15 made the daemon's listen path and the unit's dial path come out of ONE function,
// which removes the DEFINITION mismatch. It cannot remove a TIMING one: the daemon decides
// its listener once, at its own start, while `swarm remote init` decides from the state dir
// as it stands now. When the running daemon PREDATES provisioning, the two disagree again
// -- gatewaySocket() names <stateDir>/remote.sock, nothing serves it, and the gateway is
// handed to the supervisor anyway, exit 0, operator told nothing. That is the third outcome
// PB-LIFE-7 forbids: a gateway pointed at nothing, reported as success.
//
// It is not reachable on a fresh install -- BeginPairing fails closed on a nil pairing
// config (internal/skeleton/pairing.go), so a machine cannot reach the paired state without
// a daemon that already saw the identity. It IS reachable on BINARY UPGRADE, where a
// pre-B15 daemon is still running on an already-provisioned, already-paired machine and
// `swarm remote init` is exactly the convergence command an upgrading owner runs. It is
// also reachable through the env-inheritance residual ADR-007 B15 records
// (internal/daemon/client.go spawns the daemon with append(os.Environ(), ...)).
//
// TestRemoteSocket_OneDefinition cannot catch this and is not expected to: it compares two
// CONFIG computations taken at the same instant, which agree here. Only a real dial of the
// RUNNING daemon distinguishes them, so that is what these tests do.
//
// F4. supervise.Spec permits an empty RemoteSocket ("optional; empty omits it",
// internal/remote/supervise/unit.go), but cmd/swarm-remote/main.go reads
// SWARM_DAEMON_REMOTE_SOCK with NO fallback. A unit rendered without it therefore hands the
// gateway "", which fails to dial, which the supervisor restarts -- PB-LIFE-7 by a third
// route. Today runRemoteInit always writes the identity before installing, so the empty
// case is unreachable; that ordering is now load-bearing and is pinned here rather than
// left to a comment.

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/remote/device"
)

// TestRemoteInit_TellsTheOperatorWhenTheRunningDaemonServesNoRemoteSocket drives the
// upgrade shape: a daemon that was already running before this machine had an identity,
// a device already in the registry, and the convergence command an owner is told to run.
//
// The control subtest is what keeps the fix honest. A warning printed unconditionally
// would satisfy the first subtest and turn every healthy install into a nag, so the same
// command on a machine whose daemon DOES serve the socket must say nothing about it.
func TestRemoteInit_TellsTheOperatorWhenTheRunningDaemonServesNoRemoteSocket(t *testing.T) {
	t.Run("daemon predates provisioning", func(t *testing.T) {
		dir := shortStateDir(t)
		t.Setenv(daemon.EnvStateDir, dir)
		unsetRemoteSocketEnv(t)

		// Order is the whole point: the daemon assembles from an UNPROVISIONED state dir,
		// so it opens no remote socket, and a running process never revisits that.
		startInstallDaemon(t, dir)
		seedDevice(t, dir, "Nathan's iPhone", device.CapFull)
		fakeGatewayBinaryOnPath(t)
		f := installFakeSupervisor(t)

		var stdout, stderr bytes.Buffer
		exit := runRemoteInit(nil, &stdout, &stderr)
		output := stdout.String() + stderr.String()

		// The identity is written and the device is enrolled: both durable, neither a
		// failure. PB-LIFE-3(a)'s "quiescence is not a failure" reasoning applies to the
		// exit code here too -- what is wrong is fixable, and is named below.
		if exit != 0 {
			t.Fatalf("runRemoteInit exit = %d, want 0; the provisioning it performed is durable. output=%q",
				exit, lastLines(output, 8))
		}
		if !strings.Contains(output, "swarm daemon restart") {
			t.Fatalf("PB-LIFE-7: the running daemon serves no remote socket, `swarm remote init` "+
				"handed the gateway to the supervisor anyway (calls=%v), and named no step the "+
				"operator can take. The daemon decided its listener before this machine had an "+
				"identity; `swarm daemon restart` is what picks it up. This is the phone-pairs-"+
				"then-silence configuration on the upgrade path. output=%q",
				f.calls, lastLines(output, 8))
		}
	})

	// CONTROL: identical machine, identical command, but the daemon assembled AFTER
	// provisioning and really serves the socket. Nothing is wrong, so nothing may be said.
	t.Run("control: daemon started after provisioning", func(t *testing.T) {
		dir := shortStateDir(t)
		t.Setenv(daemon.EnvStateDir, dir)
		unsetRemoteSocketEnv(t)
		fakeGatewayBinaryOnPath(t)
		f := installFakeSupervisor(t)

		var provOut, provErr bytes.Buffer
		if exit := runRemoteInit(nil, &provOut, &provErr); exit != 0 {
			t.Fatalf("provisioning runRemoteInit exit = %d, want 0; output=%q",
				exit, lastLines(provOut.String()+provErr.String(), 6))
		}
		startInstallDaemon(t, dir)
		seedDevice(t, dir, "Nathan's iPhone", device.CapFull)

		var stdout, stderr bytes.Buffer
		exit := runRemoteInit(nil, &stdout, &stderr)
		output := stdout.String() + stderr.String()
		if exit != 0 {
			t.Fatalf("runRemoteInit exit = %d on a healthy machine, want 0; output=%q", exit, lastLines(output, 6))
		}
		if got := f.count("ensure"); got != 1 {
			t.Fatalf("Ensure called %d times with one paired device and a served remote socket, want 1; calls=%v", got, f.calls)
		}
		if strings.Contains(output, "swarm daemon restart") {
			t.Fatalf("`swarm remote init` told the operator to restart the daemon on a machine whose "+
				"daemon is serving the remote socket correctly. A warning that fires on every install "+
				"is not a warning. output=%q", lastLines(output, 8))
		}
	})
}

// TestInstallGatewayUnit_RefusesAUnitWithNoRemoteSocket pins F4 at the only place that can
// still produce one: installGatewayUnit reads the socket from gatewaySocket(), which is
// empty on an unprovisioned state dir. A unit installed from that renders no
// SWARM_DAEMON_REMOTE_SOCK, and cmd/swarm-remote reads that variable with no fallback --
// so the gateway would dial "" forever on the supervisor's throttle interval.
//
// It warns and installs nothing rather than failing the command, matching every other
// failure in installGatewayUnit: the caller's durable work is already done.
func TestInstallGatewayUnit_RefusesAUnitWithNoRemoteSocket(t *testing.T) {
	dir := shortStateDir(t) // deliberately NOT provisioned: no <stateDir>/remote/machine.key
	unsetRemoteSocketEnv(t)
	fakeGatewayBinaryOnPath(t)
	f := installFakeSupervisor(t)

	var stderr bytes.Buffer
	if installGatewayUnit(dir, &stderr) {
		t.Fatalf("installGatewayUnit reported a unit installed for an unprovisioned state dir; "+
			"Spec.RemoteSocket = %q. cmd/swarm-remote reads SWARM_DAEMON_REMOTE_SOCK with no "+
			"fallback, so a unit rendered without it hands the gateway an empty dial target and "+
			"the supervisor restarts it forever.", f.spec.RemoteSocket)
	}
	if got := f.count("install"); got != 0 {
		t.Errorf("Install called %d times for an unprovisioned state dir, want 0; calls=%v", got, f.calls)
	}
	if stderr.Len() == 0 {
		t.Error("installGatewayUnit installed nothing and said nothing; every other refusal in it warns")
	}
}
