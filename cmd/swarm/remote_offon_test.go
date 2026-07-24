package main

// FAILING-FIRST test for A4-cli: `swarm remote off` / `swarm remote on` — the durable
// manual kill switch. `off` sets a durable owner override that disables remote control
// REGARDLESS of paired devices; `on` clears it. Both drive the real owner client
// (protocol.Client.SetRemoteControl) against a real in-process daemon; the durable flip
// is asserted via the manual_off field of <stateDir>/remote-state.json.
//
// RED today: runRemote has no "off"/"on" route, so the verb is an unknown-command error
// (nonzero exit), never flipping the durable state.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/device"
)

// readManualOff reads <stateDir>/remote-state.json and returns its manual_off field —
// the durable owner override A4 adds to the kill-switch mirror.
func readManualOff(t *testing.T, stateDir string) bool {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(stateDir, "remote-state.json"))
	if err != nil {
		t.Fatalf("read remote-state.json: %v", err)
	}
	var st struct {
		ManualOff bool `json:"manual_off"`
	}
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatalf("unmarshal remote-state.json: %v", err)
	}
	return st.ManualOff
}

// TestRemoteOffOn_FlipsDurableState drives `swarm remote off` then `swarm remote on`
// against a REAL in-process daemon with a paired device, and proves each flips the
// durable manual_off override.
func TestRemoteOffOn_FlipsDurableState(t *testing.T) {
	dir := shortStateDir(t)
	// A paired device makes remote control device-derived ON, so `off` must OVERRIDE it.
	seedDevice(t, dir, "Nathan's iPhone", device.CapFull)
	startCLIDaemon(t, dir)

	// `swarm remote off` -> durable manual override.
	var offOut, offErr bytes.Buffer
	if exit := runRemote([]string{"off"}, &offOut, &offErr); exit != 0 {
		t.Fatalf("runRemote([off]) exit = %d, want 0; stderr=%q", exit, offErr.String())
	}
	if !readManualOff(t, dir) {
		t.Fatal("`swarm remote off` did not set manual_off=true in remote-state.json")
	}

	// `swarm remote on` -> clears the override.
	var onOut, onErr bytes.Buffer
	if exit := runRemote([]string{"on"}, &onOut, &onErr); exit != 0 {
		t.Fatalf("runRemote([on]) exit = %d, want 0; stderr=%q", exit, onErr.String())
	}
	if readManualOff(t, dir) {
		t.Fatal("`swarm remote on` did not clear manual_off in remote-state.json")
	}
}
