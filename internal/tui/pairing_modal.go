package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// pairing_modal.go is the A4 pairing-confirm overlay: the anti-MITM SAS gate as
// the operator sees it. When the daemon pushes a pair_pending (the SAS + the
// device's name), a modal renders the SAS the peer also shows and blocks the
// operator on an allow/deny keypress, which emits the pair_confirm decision.
//
// It is a pure render+decision surface driven by messages (mock-flow, A4 DoD): a
// pairPendingMsg opens it, a keypress emits a pairConfirmMsg. Wiring it to a live
// pairing session (protocol.Client.StartPairing) is a thin tea.Cmd layered on top
// and is out of this slice — the emitted decision is that integration point.

// pairPendingMsg carries one pushed pair_pending (SAS gate) into the Update loop,
// opening the pairing-confirm modal over whatever screen is showing.
type pairPendingMsg struct {
	SAS        []string
	DeviceName string
}

// pairConfirmMsg is the operator's answer at the SAS gate: Allow=true approves the
// pairing (pair_confirm allow), Allow=false declines it. It is emitted as a
// command result so the decision is observable to a wire layer (and to tests).
type pairConfirmMsg struct{ Allow bool }

// pairingModal is the open SAS-gate overlay: the SAS to verify against the peer
// and the name of the device requesting to pair. A nil *pairingModal on the
// router means no gate is open.
type pairingModal struct {
	sas        []string
	deviceName string
}

// confirmPairing emits the operator's SAS-gate decision as a command.
func confirmPairing(allow bool) tea.Cmd {
	return func() tea.Msg { return pairConfirmMsg{Allow: allow} }
}

// updatePairing handles a keypress while the SAS gate is open. y/Enter approve,
// n/Esc decline; both answers close the modal and emit the decision. Any other
// key is swallowed (the gate is modal — keys never fall through to the board).
func (m rootModel) updatePairing(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.Code {
	case 'y', tea.KeyEnter:
		m.pairing = nil
		return m, confirmPairing(true)
	case 'n', tea.KeyEsc:
		m.pairing = nil
		return m, confirmPairing(false)
	}
	return m, nil
}

// view renders the SAS gate: the requesting device and the SAS to compare against
// the one the peer shows. The SAS words are joined verbatim so the operator reads
// exactly what the peer displays (A4: "it equals the SAS the peer shows").
func (p pairingModal) view() string {
	var b strings.Builder
	b.WriteString(styleTitle.Render("Pair device"))
	b.WriteString("\n\n")
	b.WriteString("Device: " + styleAgent.Render(p.deviceName) + "\n\n")
	b.WriteString("Verify this code matches the one shown on the device:\n\n")
	b.WriteString("  " + strings.Join(p.sas, " ") + "\n")
	return b.String()
}
