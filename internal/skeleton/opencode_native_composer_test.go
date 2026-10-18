package skeleton

import (
	"github.com/Nathandela/swarm/internal/protocol"
	"testing"
)

func TestOpenCodeComposerCapabilityNeedsCurrentQualifiedProjection(t *testing.T) {
	f := newOpenCodeSendFixture(t)
	caps, ok := f.d.sessionCapabilities(f.binding.WorkerID)
	if !ok || !caps.NativeComposer || caps.NativeInteractionVersion != 2 || caps.StructuredChat {
		t.Fatalf("qualified native composer not independently available: %+v", caps)
	}
	f.d.openCode.mu.Lock()
	observer := f.d.openCode.sessions[f.binding.WorkerID]
	view := cloneOpenCodeView(observer.view)
	f.d.openCode.mu.Unlock()
	view.Degraded = true
	view.DegradedReason = "native observation has an unresolved gap"
	f.d.publishOpenCodeView(f.binding.WorkerID, observer, view)
	caps, _ = f.d.sessionCapabilities(f.binding.WorkerID)
	if caps.NativeComposer || caps.NativeInteractionVersion != 2 {
		t.Fatalf("degraded authority did not retain readonly transcript: %+v", caps)
	}
	f.d.stopOpenCodeObservation(f.binding.WorkerID)
	caps, _ = f.d.sessionCapabilities(f.binding.WorkerID)
	if caps.NativeComposer || caps.NativeInteractionVersion != 0 {
		t.Fatal("stopped native observer kept composer capability")
	}
}
func TestOpenCodeComposerCapabilityCannotRecoverFromDiskRecordAlone(t *testing.T) {
	d := &Daemon{}
	d.capStore.byID = map[string]protocol.SessionCapabilities{"worker": {Provider: "opencode", SessionInstance: "instance", NativeInteractionVersion: 2, NativeComposer: true, TerminalFallback: true}}
	caps, ok := d.sessionCapabilities("worker")
	if !ok || caps.NativeComposer || caps.NativeInteractionVersion != 0 {
		t.Fatalf("persisted record restored native authority before rejoin: %+v", caps)
	}
}
