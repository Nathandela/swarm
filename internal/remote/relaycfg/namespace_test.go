package relaycfg_test

import (
	"testing"

	"github.com/Nathandela/swarm/internal/remote/relaycfg"
)

func TestRelayConfigRequiresNamespaceWithRelayURL(t *testing.T) {
	dir := t.TempDir()
	if err := relaycfg.Save(dir, relaycfg.Config{RelayURL: "wss://relay.example"}); err == nil {
		t.Fatal("Save accepted a relay URL without an operator namespace")
	}
	if err := relaycfg.Save(dir, relaycfg.Config{OperatorNamespace: "owner"}); err == nil {
		t.Fatal("Save accepted an operator namespace without a relay URL")
	}
}
