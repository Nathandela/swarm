package skeleton

// SH5 (bead agents-tracker-dtc5, round-3 codex #3): the DAEMON refuses a pairing
// ceremony while a deferred relay purge is owed. The CLI checks too, but the CLI's
// check races a concurrent revoke across processes; BeginPairing is where pairing and
// the revoke's registry delete serialize, so the gate here is the one a racing record
// cannot slip past.

import (
	"context"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/relaypurge"
)

func TestSH5_BeginPairingRefusesWhileARelayPurgeIsOwed(t *testing.T) {
	sk := assemble(t)
	injectPairing(t, sk)

	store, err := relaypurge.Open(relaypurge.StorePath(sk.api.stateDir))
	if err != nil {
		t.Fatalf("relaypurge.Open: %v", err)
	}
	if err := store.Record("ab12cd34ab12cd34ab12cd34ab12cd34", "wss://relay.example", ""); err != nil {
		t.Fatalf("Record: %v", err)
	}

	_, err = sk.api.BeginPairing(context.Background(),
		protocol.PairStartReq{Capability: "full"},
		func([]string, string) (bool, error) { return false, nil },
		func(protocol.PairResult) {})
	if err == nil {
		t.Fatal("BeginPairing proceeded while a deferred relay purge is owed: a concurrent " +
			"revoke's obligation would be shielded by the new pairing's own live routing id")
	}
	if !strings.Contains(err.Error(), "deferred relay purge") {
		t.Fatalf("the refusal is owed its reason; got: %v", err)
	}

	// And once the obligation is gone, the same daemon pairs normally.
	if err := store.Retire("ab12cd34ab12cd34ab12cd34ab12cd34"); err != nil {
		t.Fatalf("Retire: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if _, err := sk.api.BeginPairing(ctx,
		protocol.PairStartReq{Capability: "full"},
		func([]string, string) (bool, error) { return false, nil },
		func(protocol.PairResult) {}); err != nil {
		t.Fatalf("BeginPairing after the ledger cleared: %v", err)
	}
}
