package skeleton

import (
	"context"
	"github.com/Nathandela/swarm/internal/opencodebridge"
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

type openCodeQualifiedReadyFake struct {
	*ocReaderFake
	ready opencodebridge.Ready
}

func (f *openCodeQualifiedReadyFake) Ready(context.Context) (opencodebridge.Ready, error) {
	return f.ready, nil
}
func TestOpenCodeComposerSinkProofChecksEveryNativeReadyFence(t *testing.T) {
	for _, change := range []string{"valid", "version", "binding", "ack", "bound", "ready", "draft"} {
		t.Run(change, func(t *testing.T) {
			reader := &openCodeQualifiedReadyFake{ocReaderFake: newOCReaderFake()}
			reader.ready = opencodebridge.Ready{Identity: reader.identity, NativeVersion: "1.19.3", Acknowledged: true, Bound: true, Ready: true, DraftGuardSupported: true}
			switch change {
			case "version":
				reader.ready.NativeVersion = "1.19.4"
			case "binding":
				reader.ready.BackendGeneration = "another"
			case "ack":
				reader.ready.Acknowledged = false
			case "bound":
				reader.ready.Bound = false
			case "ready":
				reader.ready.Ready = false
			case "draft":
				reader.ready.DraftGuardSupported = false
			}
			projection := &openCodeTranscript{identity: reader.identity}
			projection.qualifySink(context.Background(), reader)
			if projection.sinkQualified.Load() != (change == "valid") {
				t.Fatal("native writer capability bypassed ready fence")
			}
		})
	}
}
