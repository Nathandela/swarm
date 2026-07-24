package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// A4 DoD — "TUI pairing-confirm shows the SAS; it equals the SAS the peer shows
// (mock flow)." When the daemon pushes a pair_pending (SAS gate), the modal
// renders the exact SAS the peer displays plus the device name, and a keypress
// emits the operator's pair_confirm decision. Mock-flow: the modal is driven by a
// synthetic pairPendingMsg — no live relay, no daemon.

// sasFixture is the six-emoji SAS a peer shows at the gate.
var sasFixture = []string{"🐙", "🦊", "🐢", "🦉", "🐝", "🦄"}

func TestTUI_PairingModal_RendersSASAndConfirms(t *testing.T) {
	const device = "phone-x"
	m := newModel(t, newFakeClient(), detectMixed())

	// The daemon pushes the SAS gate: the modal opens.
	m = send(m, pairPendingMsg{SAS: sasFixture, DeviceName: device})

	v := view(m)
	// It "equals the SAS the peer shows": the exact six emoji appear, in order.
	if want := strings.Join(sasFixture, " "); !strings.Contains(v, want) {
		t.Fatalf("pairing modal must render the SAS %q the peer shows:\n%s", want, v)
	}
	if !strings.Contains(v, device) {
		t.Fatalf("pairing modal must name the pairing device %q:\n%s", device, v)
	}

	// Approve at the SAS gate: 'y' emits the allow decision and dismisses the modal.
	m2, cmd := m.Update(keyRune('y'))
	if cmd == nil {
		t.Fatal("confirm keypress must emit a decision command")
	}
	dec, ok := cmd().(pairConfirmMsg)
	if !ok {
		t.Fatalf("confirm keypress must emit pairConfirmMsg; got %T", cmd())
	}
	if !dec.Allow {
		t.Fatal("'y' at the SAS gate must emit an allow (Allow=true) decision")
	}
	if m2.(rootModel).pairing != nil {
		t.Fatal("an answered SAS gate must dismiss the pairing modal")
	}
}

func TestTUI_PairingModal_DenyEmitsFalseAndDismisses(t *testing.T) {
	m := newModel(t, newFakeClient(), detectMixed())
	m = send(m, pairPendingMsg{SAS: sasFixture, DeviceName: "phone-x"})

	m2, cmd := m.Update(keyRune('n'))
	if cmd == nil {
		t.Fatal("deny keypress must emit a decision command")
	}
	dec, ok := cmd().(pairConfirmMsg)
	if !ok {
		t.Fatalf("deny keypress must emit pairConfirmMsg; got %T", cmd())
	}
	if dec.Allow {
		t.Fatal("'n' at the SAS gate must emit a deny (Allow=false) decision")
	}
	if m2.(rootModel).pairing != nil {
		t.Fatal("a denied SAS gate must dismiss the pairing modal")
	}
}

// A modal is modal: while it is up, its keys are consumed by the gate, not the
// underlying board (e.g. 'n' must not fall through to the general view).
func TestTUI_PairingModal_ConsumesKeysWhileOpen(t *testing.T) {
	m := newModel(t, newFakeClient(), detectMixed())
	m = send(m, pairPendingMsg{SAS: sasFixture, DeviceName: "phone-x"})

	// Enter approves too (the modal owns Enter/Esc as well as y/n).
	m2, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter must emit a decision at the SAS gate")
	}
	if dec, ok := cmd().(pairConfirmMsg); !ok || !dec.Allow {
		t.Fatalf("enter must emit an allow decision; got %#v (ok=%v)", cmd(), ok)
	}
	if m2.(rootModel).pairing != nil {
		t.Fatal("enter must dismiss the pairing modal")
	}
}
