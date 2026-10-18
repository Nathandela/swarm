package skeleton

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Nathandela/swarm/internal/opencodebridge"
	"github.com/Nathandela/swarm/internal/protocol"
	"testing"
	"time"
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
	err   error
}

func (f *openCodeQualifiedReadyFake) Ready(context.Context) (opencodebridge.Ready, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ready, f.err
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

func TestOpenCodeComposerReadyProofRecoversWithoutNativeEvents(t *testing.T) {
	f := newOpenCodeSendFixture(t)
	meta, _ := f.d.core.Get(f.binding.WorkerID)
	projection, err := newOpenCodeTranscript(f.d, meta, f.identity)
	if err != nil {
		t.Fatal(err)
	}
	reader := &openCodeQualifiedReadyFake{ocReaderFake: newOCReaderFake()}
	reader.identity = f.identity
	reader.ready = opencodebridge.Ready{Identity: f.identity, NativeVersion: "1.19.3", Acknowledged: true, Bound: true, Ready: true}
	root, _ := json.Marshal(map[string]any{"id": f.identity.RootID, "directory": f.identity.Directory, "time": map[string]int64{"created": f.identity.NativeCreatedAtMS}})
	reader.snapshot = func(int) opencodebridge.Snapshot {
		snapshot := reader.idleSnapshot()
		snapshot.Session = root
		return snapshot
	}
	state, err := NewOpenCodeState(f.identity.RootID, f.identity.BackendGeneration)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done, ready := make(chan struct{}), make(chan error, 1)
	views := make(chan OpenCodeView, 16)
	go func() {
		defer close(done)
		observeOpenCode(ctx, reader, f.identity, state, func(view OpenCodeView) {
			select {
			case views <- view:
			case <-ctx.Done():
			}
		}, func(err error) { ready <- err }, projection)
	}()
	defer func() { cancel(); <-done }()
	if err := <-ready; err != nil {
		t.Fatal(err)
	}
	if projection.sinkQualified.Load() {
		t.Fatal("unmounted composer gained write proof")
	}
	reader.mu.Lock()
	reader.ready.DraftGuardSupported = true
	reader.mu.Unlock()
	await := func(label string, want func(OpenCodeView) bool) {
		t.Helper()
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		for {
			select {
			case view := <-views:
				if want(view) {
					return
				}
			case <-timer.C:
				t.Fatal(label)
			}
		}
	}
	await("native composer stayed disabled after exact Ready became usable without a native event", func(OpenCodeView) bool { return projection.sinkQualified.Load() })
	reader.mu.Lock()
	reader.ready.Ready = false
	reader.ready.DraftGuardSupported = false
	reader.mu.Unlock()
	await("healthy unavailable composer retained write proof or withdrew native reads", func(view OpenCodeView) bool {
		return !projection.sinkQualified.Load() && projection.ready.Load() && view.BusyKnown && view.CurrentScopeKnown
	})
	reader.mu.Lock()
	reader.err = errors.New("native bridge stopped responding")
	reader.mu.Unlock()
	await("silent native failure retained fresh status", func(view OpenCodeView) bool {
		return !projection.sinkQualified.Load() && !projection.ready.Load() && !view.BusyKnown && !view.CurrentScopeKnown
	})
	reader.mu.Lock()
	reads := reader.reads
	reader.mu.Unlock()
	if reads != 1 {
		t.Fatalf("Ready requalification fetched full native history %d times", reads)
	}
	reader.mu.Lock()
	reader.err = nil
	reader.ready.Ready = true
	reader.ready.DraftGuardSupported = true
	reader.mu.Unlock()
	await("native health recovery did not reconcile current state", func(view OpenCodeView) bool {
		return projection.sinkQualified.Load() && projection.ready.Load() && view.BusyKnown && view.CurrentScopeKnown && !view.Degraded
	})
	reader.mu.Lock()
	reads = reader.reads
	reader.mu.Unlock()
	if reads != 2 {
		t.Fatalf("native recovery performed %d history reads, want startup and one recovery", reads)
	}
}
