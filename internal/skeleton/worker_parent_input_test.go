package skeleton

import (
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"strings"
	"testing"
)

func TestWorkerSpawnProvenanceRequiresCurrentNativeTurn(t *testing.T) {
	parent := persist.Meta{ID: "manager", ManagerConversationID: "thread"}
	n := protocol.ManagerRuntimeStatus{NativeCLI: true, Binding: protocol.ManagerRuntimeBinding{Session: "manager", SessionInstance: "instance", ConversationID: "thread"}, Turn: protocol.ManagerNativeTurn{State: "running", NativeInputID: strings.Repeat("a", 64), NativeTurnID: "turn"}}
	if h := runningParentInputHash(parent, "instance", n); len(h) != 64 || h == n.Turn.NativeInputID {
		t.Fatalf("invalid opaque binding %q", h)
	}
	for _, mutate := range []func(*protocol.ManagerRuntimeStatus){
		func(r *protocol.ManagerRuntimeStatus) { r.Turn.ExternallySteered = true },
		func(r *protocol.ManagerRuntimeStatus) { r.Turn.NativeInputID = "" },
		func(r *protocol.ManagerRuntimeStatus) { r.Turn.State = "idle" },
		func(r *protocol.ManagerRuntimeStatus) { r.Binding.ConversationID = "other" },
		func(r *protocol.ManagerRuntimeStatus) { r.Binding.SessionInstance = "other" },
	} {
		r := n
		mutate(&r)
		if runningParentInputHash(parent, "instance", r) != "" {
			t.Fatalf("unproven input %v", r)
		}
	}
}
