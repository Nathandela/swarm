package main

// FAILING-FIRST tests for agents-tracker-7lkv: the bare (0-arg) `swarm remote
// revoke` arrow-key device picker.
//
// Two layers are tested separately:
//
//   - pickerModel's Update/View are pure and unit-tested directly with
//     synthetic tea.KeyPressMsg values — no terminal, no tea.Program.
//   - runRemoteRevokePicker (the TTY-gated body, split out from
//     runRemoteRevokeInteractive so it can be driven without a real terminal)
//     is tested against a REAL in-process daemon, the same harness
//     remote_devices_test.go uses (shortStateDir/seedDevice/startCLIDaemon).
//     Its own device-selection step is replaced with a scripted pickDevice
//     stub, since the picker's key handling is already proven at the model
//     layer above.
//
// RED today: pickerModel, newPickerModel, runRemoteRevokePicker, and pickDevice
// do not exist yet, so this file does not compile.

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/device"
)

// threePickerDevices is a fixed 3-row fixture for the picker model tests.
func threePickerDevices() []protocol.DeviceView {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	return []protocol.DeviceView{
		{DeviceID: "aaaa1111", Name: "Phone A", Capability: "full", PairedAt: now},
		{DeviceID: "bbbb2222", Name: "Phone B", Capability: "read_only", PairedAt: now},
		{DeviceID: "cccc3333", Name: "Phone C", Capability: "full", PairedAt: now},
	}
}

// assertQuit fails unless cmd is tea.Quit (a command whose message is tea.QuitMsg).
func assertQuit(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("cmd = nil, want tea.Quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("cmd() = %T, want tea.QuitMsg", cmd())
	}
}

// TestPickerModel_NavigationWraps drives Down/j and Up/k across the 3-row fixture
// and asserts the cursor moves and wraps at both ends.
func TestPickerModel_NavigationWraps(t *testing.T) {
	m := newPickerModel(threePickerDevices())
	if m.cursor != 0 {
		t.Fatalf("initial cursor = %d, want 0", m.cursor)
	}

	press := func(k tea.KeyPressMsg) {
		next, _ := m.Update(k)
		m = next.(pickerModel)
	}

	press(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.cursor != 1 {
		t.Fatalf("after Down, cursor = %d, want 1", m.cursor)
	}
	press(tea.KeyPressMsg{Text: "j"})
	if m.cursor != 2 {
		t.Fatalf("after j, cursor = %d, want 2", m.cursor)
	}
	press(tea.KeyPressMsg{Code: tea.KeyDown}) // wraps 2 -> 0
	if m.cursor != 0 {
		t.Fatalf("after wrap-forward, cursor = %d, want 0", m.cursor)
	}
	press(tea.KeyPressMsg{Code: tea.KeyUp}) // wraps 0 -> 2
	if m.cursor != 2 {
		t.Fatalf("after wrap-backward, cursor = %d, want 2", m.cursor)
	}
	press(tea.KeyPressMsg{Text: "k"})
	if m.cursor != 1 {
		t.Fatalf("after k, cursor = %d, want 1", m.cursor)
	}
}

// TestPickerModel_EnterSelectsAndQuits proves Enter records the CURRENT cursor as
// the selection and emits tea.Quit, leaving cancelled false.
func TestPickerModel_EnterSelectsAndQuits(t *testing.T) {
	m := newPickerModel(threePickerDevices())
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // cursor -> 1
	m = next.(pickerModel)

	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(pickerModel)
	if m.selected != 1 {
		t.Errorf("selected = %d, want 1", m.selected)
	}
	if m.cancelled {
		t.Error("cancelled = true after Enter, want false")
	}
	assertQuit(t, cmd)
}

// TestPickerModel_EscCancelsAndQuits proves Esc cancels without selecting.
func TestPickerModel_EscCancelsAndQuits(t *testing.T) {
	m := newPickerModel(threePickerDevices())
	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m = next.(pickerModel)
	if !m.cancelled {
		t.Error("cancelled = false after Esc, want true")
	}
	if m.selected != -1 {
		t.Errorf("selected = %d after Esc, want -1 (none)", m.selected)
	}
	assertQuit(t, cmd)
}

// TestPickerModel_QCancelsAndQuits proves "q" is an alias for Esc.
func TestPickerModel_QCancelsAndQuits(t *testing.T) {
	m := newPickerModel(threePickerDevices())
	next, cmd := m.Update(tea.KeyPressMsg{Text: "q"})
	m = next.(pickerModel)
	if !m.cancelled {
		t.Error("cancelled = false after q, want true")
	}
	assertQuit(t, cmd)
}

// TestPickerModel_View_ShowsCursorAndFields proves the view renders every row's
// name/capability/paired-at (matching the devices table's column content, minus the
// id) and marks the selected row.
func TestPickerModel_View_ShowsCursorAndFields(t *testing.T) {
	m := newPickerModel(threePickerDevices())
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // cursor -> row 1 (Phone B)
	m = next.(pickerModel)

	view := m.View().Content
	for _, want := range []string{"Phone A", "Phone B", "Phone C", "full", "read_only"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q; got:\n%s", want, view)
		}
	}
	lines := strings.Split(view, "\n")
	found := false
	for _, line := range lines {
		if strings.Contains(line, "Phone B") {
			if !strings.Contains(line, "❯") { // the ❯ cursor
				t.Errorf("selected row %q missing the cursor marker", line)
			}
			found = true
		}
		if strings.Contains(line, "Phone A") && strings.Contains(line, "❯") {
			t.Errorf("non-selected row %q carries the cursor marker", line)
		}
	}
	if !found {
		t.Fatal("view has no row for Phone B")
	}
}

// stubPickDevice replaces the pickDevice seam for the duration of a test, returning
// a restore func.
func stubPickDevice(fn func([]protocol.DeviceView, io.Reader, io.Writer) (protocol.DeviceView, bool)) func() {
	orig := pickDevice
	pickDevice = fn
	return func() { pickDevice = orig }
}

// assertDeviceListed asserts a device id's presence (or absence) in `swarm remote
// devices`' table, the same after-the-fact check TestRemoteRevoke_Removes uses.
func assertDeviceListed(t *testing.T, id string, want bool) {
	t.Helper()
	var out, errBuf bytes.Buffer
	if exit := runRemote([]string{"devices"}, &out, &errBuf); exit != 0 {
		t.Fatalf("runRemote([devices]) exit = %d, want 0; stderr=%q", exit, errBuf.String())
	}
	got := strings.Contains(out.String(), id)
	if got != want {
		t.Errorf("device %q listed = %v, want %v; table:\n%s", id, got, want, out.String())
	}
}

// TestRemoteRevokePicker_NoDevices drives the TTY-gated body against a REAL
// in-process daemon with an empty registry: "no paired devices" to stderr, exit 1,
// and the picker is never reached (pickDevice is untouched — nothing to stub).
func TestRemoteRevokePicker_NoDevices(t *testing.T) {
	dir := shortStateDir(t)
	startCLIDaemon(t, dir)

	var stdout, stderr bytes.Buffer
	exit := runRemoteRevokePicker(strings.NewReader(""), &stdout, &stderr)
	if exit != 1 {
		t.Fatalf("exit = %d, want 1; stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "no paired devices") {
		t.Errorf("stderr = %q, want it to mention no paired devices", stderr.String())
	}
}

// TestRemoteRevokePicker_CancelAborts drives the picker-cancel branch (a stubbed
// pickDevice reporting ok=false, as Esc/q would): "aborted" to stderr, exit 1, and
// the seeded device is untouched — RevokeDevice must never be called.
func TestRemoteRevokePicker_CancelAborts(t *testing.T) {
	dir := shortStateDir(t)
	id := seedDevice(t, dir, "Nathan's iPhone", device.CapFull)
	startCLIDaemon(t, dir)

	defer stubPickDevice(func([]protocol.DeviceView, io.Reader, io.Writer) (protocol.DeviceView, bool) {
		return protocol.DeviceView{}, false
	})()

	var stdout, stderr bytes.Buffer
	exit := runRemoteRevokePicker(strings.NewReader(""), &stdout, &stderr)
	if exit != 1 {
		t.Fatalf("exit = %d, want 1; stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "aborted") {
		t.Errorf("stderr = %q, want aborted", stderr.String())
	}
	assertDeviceListed(t, id, true)
}

// TestRemoteRevokePicker_ConfirmNoAborts drives past a scripted device selection
// into the y/N confirm, answering "n": the chosen-device line and confirm prompt
// print to stdout, "aborted" to stderr, exit 1, and RevokeDevice is never called.
func TestRemoteRevokePicker_ConfirmNoAborts(t *testing.T) {
	dir := shortStateDir(t)
	id := seedDevice(t, dir, "Nathan's iPhone", device.CapFull)
	startCLIDaemon(t, dir)

	chosen := protocol.DeviceView{DeviceID: id, Name: "Nathan's iPhone", Capability: "full", PairedAt: time.Now()}
	defer stubPickDevice(func([]protocol.DeviceView, io.Reader, io.Writer) (protocol.DeviceView, bool) {
		return chosen, true
	})()

	var stdout, stderr bytes.Buffer
	exit := runRemoteRevokePicker(strings.NewReader("n\n"), &stdout, &stderr)
	if exit != 1 {
		t.Fatalf("exit = %d, want 1; stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
	wantPrompt := fmt.Sprintf("revoke %q (%s)? [y/N]: ", chosen.Name, deviceIDShort(id))
	if !strings.Contains(stdout.String(), wantPrompt) {
		t.Errorf("stdout = %q, want it to contain the confirm prompt %q", stdout.String(), wantPrompt)
	}
	if !strings.Contains(stderr.String(), "aborted") {
		t.Errorf("stderr = %q, want aborted", stderr.String())
	}
	assertDeviceListed(t, id, true)
}

// TestRemoteRevokePicker_ConfirmYesRevokes drives the full confirm-then-revoke
// flow, answering "y": the EXISTING revoke path runs (client.RevokeDevice) and the
// EXISTING success copy prints, and the device is actually gone afterward.
func TestRemoteRevokePicker_ConfirmYesRevokes(t *testing.T) {
	dir := shortStateDir(t)
	id := seedDevice(t, dir, "Nathan's iPhone", device.CapFull)
	startCLIDaemon(t, dir)

	chosen := protocol.DeviceView{DeviceID: id, Name: "Nathan's iPhone", Capability: "full", PairedAt: time.Now()}
	defer stubPickDevice(func([]protocol.DeviceView, io.Reader, io.Writer) (protocol.DeviceView, bool) {
		return chosen, true
	})()

	var stdout, stderr bytes.Buffer
	exit := runRemoteRevokePicker(strings.NewReader("y\n"), &stdout, &stderr)
	if exit != 0 {
		t.Fatalf("exit = %d, want 0; stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "Device: Nathan's iPhone") {
		t.Errorf("stdout missing the chosen-device line: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "revoked device "+id) {
		t.Errorf("stdout missing the existing success copy: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "run `swarm remote pair` to pair a device again") {
		t.Errorf("stdout missing the existing follow-up copy: %q", stdout.String())
	}
	assertDeviceListed(t, id, false)
}
