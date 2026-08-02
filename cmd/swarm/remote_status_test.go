package main

// FAILING-FIRST test for A4-status: `swarm remote status` — the operator read that
// composes existing reads (durable kill-switch file + live device roster) into a plain
// report. No new wire op: it reads <stateDir>/remote-state.json (the same durable file
// `swarm remote off`/`on` write) for the manual override and dials the owner client for
// the paired-device roster, exactly like `swarm remote devices`.
//
// RED today: runRemote has no "status" route, so the verb is an unknown-command error
// (exit 2), never producing the report — the exit != 0 assertions fail.

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/device"
)

// TestRemoteStatus_ReportsKillSwitchAndDevices drives `swarm remote status` against a
// REAL in-process daemon with a paired device AFTER durably turning remote control OFF
// (manual override) via the real `swarm remote off` path, and proves the report names
// the OFF-manual kill-switch state AND still lists the paired device roster.
func TestRemoteStatus_ReportsKillSwitchAndDevices(t *testing.T) {
	dir := shortStateDir(t)
	id := seedDevice(t, dir, "Nathan's iPhone", device.CapFull)
	startCLIDaemon(t, dir)

	// Durably sever remote control via the real owner path (writes remote-state.json
	// with manual_off=true), so status must report OFF (manual) despite the paired device.
	var offOut, offErr bytes.Buffer
	if exit := runRemote([]string{"off"}, &offOut, &offErr); exit != 0 {
		t.Fatalf("runRemote([off]) exit = %d, want 0; stderr=%q", exit, offErr.String())
	}

	var stdout, stderr bytes.Buffer
	exit := runRemote([]string{"status"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("runRemote([status]) exit = %d, want 0; stderr=%q", exit, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "OFF (manual") {
		t.Errorf("status did not report the OFF (manual) kill-switch state; got:\n%s", out)
	}
	if !strings.Contains(out, id) {
		t.Errorf("status did not list seeded device id %q; got:\n%s", id, out)
	}
	if !strings.Contains(out, "Nathan's iPhone") {
		t.Errorf("status did not list seeded device name; got:\n%s", out)
	}
}

// TestRemoteStatus_DeviceDerivedOn drives `swarm remote status` against a REAL
// in-process daemon with a paired device and NO manual override (no `swarm remote off`
// was ever run, so remote-state.json carries no manual_off): remote control is
// device-derived ON, and status must report it as such.
func TestRemoteStatus_DeviceDerivedOn(t *testing.T) {
	dir := shortStateDir(t)
	id := seedDevice(t, dir, "Nathan's iPad", device.CapReadOnly)
	startCLIDaemon(t, dir)

	var stdout, stderr bytes.Buffer
	exit := runRemote([]string{"status"}, &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("runRemote([status]) exit = %d, want 0; stderr=%q", exit, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "ON (device-derived)") {
		t.Errorf("status did not report device-derived ON with a paired device; got:\n%s", out)
	}
	if !strings.Contains(out, id) {
		t.Errorf("status did not list seeded device id %q; got:\n%s", id, out)
	}
}
