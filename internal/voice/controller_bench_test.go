package voice

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Nathandela/swarm/internal/protocol"
	bolt "go.etcd.io/bbolt"
	"testing"
	"time"
)

// This measures controller reconciliation with a fake native read, not daemon/model latency.
func BenchmarkControllerIdleTick(b *testing.B) {
	for _, records := range []int{1000, 10000} {
		b.Run(fmt.Sprintf("records=%d", records), func(b *testing.B) {
			store, binding := benchmarkStore(b, records)
			if err := store.db.Update(func(tx *bolt.Tx) error {
				st, err := readState(tx)
				if err != nil {
					return err
				}
				now := time.Now().UTC()
				for k, a := range st.Actions {
					a.State = "settled"
					a.SettledAt = now
					a.Receipt = json.RawMessage(`{"accepted":true}`)
					st.Actions[k] = a
				}
				for k, a := range st.Admissions {
					a.SettledAt = now
					st.Admissions[k] = a
				}
				convo := st.Conversations[binding.ConversationID]
				convo.RetiredThrough = convo.LastAdmission
				st.Conversations[binding.ConversationID] = convo
				raw, err := json.Marshal(st)
				if err != nil {
					return err
				}
				return writeStateRecord(tx, raw)
			}); err != nil {
				b.Fatal(err)
			}
			native := &controllerRuntimeFixture{status: protocol.ManagerRuntimeStatus{ReadyForInput: true, Binding: runtimeBinding(binding), BridgeSocket: "/private/bridge.sock", AuthFile: "/private/bridge.auth", Turn: protocol.ManagerNativeTurn{State: "idle"}}, receipts: map[string]protocol.SemanticMessageReceipt{}}
			controller, err := NewController(store, native, binding, ManagerBridgeRefs{Socket: native.status.BridgeSocket, AuthFile: native.status.AuthFile})
			if err != nil {
				b.Fatal(err)
			}
			if err = controller.Tick(context.Background()); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err = controller.Tick(context.Background()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
