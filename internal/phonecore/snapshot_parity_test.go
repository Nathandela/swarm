package phonecore

// FINAL AUDIT COMMITTEE, FENCE FOR BEAD 65bj (escalated by Opus M4). The terminal-
// snapshot plaintext is declared TWICE: internal/remotegw/relaysink.go's snapshotFrame
// (the sealing side) and this package's snapshotFrame (the opening side). The two are
// kept identical by convention, and each side's exact-bytes pin covers its own copy --
// but nothing tied them TOGETHER, so a json-tag drift on one side would ship: the phone
// would read a zero for the drifted key, and a view_epoch of 0 disables
// SnapshotCache.Apply's revision-monotonicity guard (snapshot.go), re-opening the frozen-
// screen failure T4-a exists to close. This test marshals IDENTICAL data through BOTH
// declarations -- the real RelaySink on the gateway side, this package's frame on the
// phone side -- and requires byte-identical plaintext, key set included.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remotegw"
)

// parityAppender captures the sealed envelopes the sink appends.
type parityAppender struct{ envs [][]byte }

func (a *parityAppender) MailboxAppend(_ context.Context, _ string, env []byte) (uint64, error) {
	a.envs = append(a.envs, append([]byte(nil), env...))
	return uint64(len(a.envs)), nil
}

func TestSnapshotWireParity_GatewayMarshalMatchesPhoneFrame(t *testing.T) {
	key := testContentKey()
	app := &parityAppender{}
	sink := remotegw.NewRelaySink(remotegw.RelayConfig{
		Appender:       app,
		Target:         "phone-routing-id",
		EpochID:        7,
		Key:            key,
		RecipientKeyID: [8]byte{1, 2, 3, 4, 5, 6, 7, 8},
		SenderKeyID:    [8]byte{9, 10, 11, 12, 13, 14, 15, 16},
	})

	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := sink.Terminal(protocol.TerminalViewV1{
		Session: "m/s1", SessionInstance: "inst-1", ViewEpoch: 3, Revision: 7, Reset: true,
		Cols: 80, Rows: 24, Lines: []string{"a", "b"},
		RenderedAt: at,
	}); err != nil {
		t.Fatalf("gateway Terminal: %v", err)
	}
	if len(app.envs) != 1 {
		t.Fatalf("appended %d envelopes; want 1", len(app.envs))
	}
	env, err := crypto.ParseEnvelope(app.envs[0])
	if err != nil {
		t.Fatalf("envelope parse: %v", err)
	}
	gateway, err := crypto.OpenMailbox(key, env)
	if err != nil {
		t.Fatalf("envelope does not open under the shared content key: %v", err)
	}

	phone, err := json.Marshal(snapshotFrame{
		Kind: kindTerminalSnapshot,
		TerminalSnapshot: protocol.TerminalSnapshot{
			Session: "m/s1", Lines: []string{"a", "b"}, Cols: 80, Rows: 24,
		},
		SessionInstance: "inst-1",
		ViewEpoch:       3,
		Revision:        7,
		Reset:           true,
		RenderedAt:      &at,
	})
	if err != nil {
		t.Fatalf("phone-side marshal: %v", err)
	}

	if string(gateway) != string(phone) {
		// Diagnose the drifted key(s) before failing on the bytes.
		keysOf := func(b []byte) map[string]bool {
			m := map[string]json.RawMessage{}
			if err := json.Unmarshal(b, &m); err != nil {
				t.Fatalf("plaintext not a JSON object: %v\n%s", err, b)
			}
			set := map[string]bool{}
			for k := range m {
				set[k] = true
			}
			return set
		}
		gk, pk := keysOf(gateway), keysOf(phone)
		for k := range gk {
			if !pk[k] {
				t.Errorf("key %q: sealed by the gateway, unknown to the phone frame (tag drift)", k)
			}
		}
		for k := range pk {
			if !gk[k] {
				t.Errorf("key %q: expected by the phone frame, never sealed by the gateway (tag drift)", k)
			}
		}
		t.Fatalf("the two snapshotFrame declarations no longer marshal identical bytes for identical data:\n"+
			"  gateway (remotegw/relaysink.go): %s\n"+
			"  phone   (phonecore/snapshot.go): %s\n"+
			"Both declarations must carry the same json tags for all of: session_instance, view_epoch, "+
			"revision, reset, rendered_at (bead 65bj)", gateway, phone)
	}
}
