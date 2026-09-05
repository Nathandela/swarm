package swarmmobile

import (
	"testing"

	"github.com/Nathandela/swarm/internal/remote/pairing"
)

func TestPairingPinsAuthenticatedOperatorNamespaceBeforeCompletion(t *testing.T) {
	a := freshnessApp(t)
	out := pairedOutcome("machine-a", 7)
	out.Machine.OperatorNamespace = "owner"
	if err := a.pin(out); err != nil {
		t.Fatalf("pin: %v", err)
	}
	if got := a.core.State().OperatorNamespace; got != "owner" {
		t.Fatalf("State.OperatorNamespace = %q, want owner", got)
	}
}

func TestPairingMachineValidationRejectsNamespaceBeforePin(t *testing.T) {
	for _, namespace := range []string{"", "Owner", "owner_2"} {
		m := pairing.MachinePayload{OperatorNamespace: namespace}
		if err := verifyPairingMachine(m, nil); err == nil {
			t.Errorf("verifyPairingMachine accepted %q", namespace)
		} else if class := classifyMessage(err.Error()); class != ErrClassPairingFailed {
			t.Errorf("verifyPairingMachine(%q) class = %q, want %q", namespace, class, ErrClassPairingFailed)
		}
	}
}
