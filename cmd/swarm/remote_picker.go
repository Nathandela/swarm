package main

// remote_picker.go is agents-tracker-7lkv's arrow-key device picker: the bare
// (0-arg) `swarm remote revoke` interactive path. The explicit `swarm remote
// revoke <device-id>` path in remote.go is untouched — this file only adds the
// alternative entry a caller reaches by typing no id at all.

import (
	"fmt"
	"io"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"

	"github.com/Nathandela/swarm/internal/protocol"
)

// remoteRevokeNoTTYMsg is what bare `swarm remote revoke` prints when stdin/stdout
// are not both interactive terminals: the picker needs a screen to draw into and a
// keyboard to read from, so a scripted or piped caller is pointed at the two
// commands that still work without one.
const remoteRevokeNoTTYMsg = "remote revoke: not a terminal; run `swarm remote revoke <device-id>` " +
	"(find a device id with `swarm remote devices`)\n"

// runRemoteRevokeInteractive is the bare (0-arg) `swarm remote revoke` path: an
// arrow-key device picker in place of a caller having to know the full 64-char hex
// device id. It refuses BEFORE dialing when stdin/stdout are not both TTYs — cheap,
// mirrors interactiveTTY (main.go) — so a non-interactive invocation costs nothing
// but the refusal.
func runRemoteRevokeInteractive(stdin io.Reader, stdout, stderr io.Writer) int {
	if !revokeStdioIsTTY(stdin, stdout) {
		fmt.Fprint(stderr, remoteRevokeNoTTYMsg)
		return 2
	}
	return runRemoteRevokePicker(stdin, stdout, stderr)
}

// revokeStdioIsTTY mirrors interactiveTTY (main.go:169) for the injected stdin
// runRemoteRevoke carries: both stdin and stdout must be a real terminal, not just
// stdout, since the picker reads keystrokes from stdin exactly as the TUI's attach
// passthrough does.
func revokeStdioIsTTY(stdin io.Reader, stdout io.Writer) bool {
	in, ok := stdin.(*os.File)
	if !ok || !term.IsTerminal(in.Fd()) {
		return false
	}
	out, ok := stdout.(*os.File)
	if !ok || !term.IsTerminal(out.Fd()) {
		return false
	}
	return true
}

// runRemoteRevokePicker is the TTY-gated body: dial, list devices, run the picker,
// confirm, revoke. Split out from runRemoteRevokeInteractive so it can be driven
// directly in tests without a real terminal — the picker's own key handling is
// unit-tested on pickerModel, so this only has to prove the dial/list/confirm/
// revoke wiring around it.
func runRemoteRevokePicker(stdin io.Reader, stdout, stderr io.Writer) int {
	client, err := dialClient([]string{protocol.CapPairing})
	if err != nil {
		fmt.Fprintf(stderr, "remote revoke: %v\n", err)
		return 1
	}
	defer client.Close()

	devices, err := client.ListDevices()
	if err != nil {
		fmt.Fprintf(stderr, "remote revoke: %v\n", err)
		return 1
	}
	if len(devices) == 0 {
		fmt.Fprintln(stderr, "remote revoke: no paired devices")
		return 1
	}

	chosen, ok := pickDevice(devices, stdin, stdout)
	if !ok {
		fmt.Fprintln(stderr, "aborted")
		return 1
	}

	// Outside the picker, plain stdout — consistent with runRemotePair's own
	// "Device: %s\n" line before its confirm prompt.
	fmt.Fprintf(stdout, "Device: %s\n", chosen.Name)
	fmt.Fprintf(stdout, "revoke %q (%s)? [y/N]: ", chosen.Name, deviceIDShort(chosen.DeviceID))
	if !readYesNo(stdin) {
		fmt.Fprintln(stderr, "aborted")
		return 1
	}

	return performRevoke(client, chosen.DeviceID, stdout, stderr)
}

// pickDevice runs the arrow-key picker and reports the chosen device, or ok=false
// on Esc/q cancel (or a Program error). A var — like newGatewaySupervisor and
// osExecutable in remote.go/main.go — so a test can substitute a scripted choice
// instead of driving a real bubbletea program over a terminal.
var pickDevice = func(devices []protocol.DeviceView, stdin io.Reader, stdout io.Writer) (protocol.DeviceView, bool) {
	final, err := tea.NewProgram(newPickerModel(devices), tea.WithInput(stdin), tea.WithOutput(stdout)).Run()
	if err != nil {
		return protocol.DeviceView{}, false
	}
	pm, ok := final.(pickerModel)
	if !ok || pm.cancelled || pm.selected < 0 {
		return protocol.DeviceView{}, false
	}
	return devices[pm.selected], true
}

// deviceIDShortLen is how many leading hex characters of a 64-char device id the
// revoke confirm shows: enough to tell devices apart at a glance without echoing
// the full id back at the operator, the same convention as a git short hash.
const deviceIDShortLen = 8

// deviceIDShort truncates id to deviceIDShortLen characters (unchanged if already
// shorter).
func deviceIDShort(id string) string {
	if len(id) <= deviceIDShortLen {
		return id
	}
	return id[:deviceIDShortLen]
}

// pickerModel is the arrow-key device list bare `swarm remote revoke` shows: one
// row per DeviceView (name, capability, paired-at — the same fields the `devices`
// table prints besides the id itself), a cursor marker, Up/Down/j/k to move, Enter
// to choose, Esc/q to cancel. Single-device v1 means it usually renders a list of
// one, but it is built for N rows so ADR-011 multi-device needs no picker rewrite.
type pickerModel struct {
	devices   []protocol.DeviceView
	cursor    int
	selected  int // -1 until Enter
	cancelled bool
}

func newPickerModel(devices []protocol.DeviceView) pickerModel {
	return pickerModel{devices: devices, selected: -1}
}

func (m pickerModel) Init() tea.Cmd { return nil }

func (m pickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, ok := msg.(tea.KeyPressMsg)
	n := len(m.devices)
	if !ok || n == 0 {
		return m, nil
	}
	switch {
	case k.Code == tea.KeyDown || k.Text == "j":
		m.cursor = (m.cursor + 1) % n
	case k.Code == tea.KeyUp || k.Text == "k":
		m.cursor = (m.cursor - 1 + n) % n
	case k.Code == tea.KeyEnter:
		m.selected = m.cursor
		return m, tea.Quit
	case k.Code == tea.KeyEsc || k.Text == "q":
		m.cancelled = true
		return m, tea.Quit
	}
	return m, nil
}

// View is plain text, at most styleDim-like subtlety (no colors): a "❯ " marker on
// the selected row and two spaces of alignment on every other row.
func (m pickerModel) View() tea.View {
	var b strings.Builder
	b.WriteString("select a device to revoke:\n\n")
	for i, d := range m.devices {
		cursor := "  "
		if i == m.cursor {
			cursor = "❯ "
		}
		fmt.Fprintf(&b, "%s%s  %s  %s\n", cursor, d.Name, d.Capability, d.PairedAt.Format(timeFormat))
	}
	b.WriteString("\nup/down or j/k move, enter select, esc/q cancel\n")
	return tea.NewView(b.String())
}
