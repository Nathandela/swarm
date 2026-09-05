package relayv2

import (
	"context"
	"crypto/ed25519"
	"strings"
	"testing"
)

func TestDialRejectsNoncanonicalOperatorNamespaceBeforeNetwork(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Dial(context.Background(), Profile{
		RelayURL: "wss://127.0.0.1:1", MachineRID: strings.Repeat("0", 32), OperatorNamespace: "Owner",
	}, Auth{PublicKey: pub, Sign: func([]byte) ([]byte, error) { return make([]byte, ed25519.SignatureSize), nil }, Role: RoleMachine, Purpose: PurposeStream})
	if err == nil || !strings.Contains(err.Error(), "operator namespace") {
		t.Fatalf("Dial error = %v, want operator namespace refusal", err)
	}
}
