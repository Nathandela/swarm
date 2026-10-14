package skeleton

// FAILING-FIRST for ADR-010 Amendment 9: send_input's text reaches the shim through the tap,
// so the tap forwards the paste op to an upstream that has one and degrades to Input for one
// that does not -- the ControlKeys precedent. A read-only peek drives nothing.

import (
	"sync"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
)

type pasteRecordingUpstream struct {
	mu      sync.Mutex
	inputs  []string
	pasted  []string
	noPaste bool
}

func (u *pasteRecordingUpstream) Snapshot() []byte      { return nil }
func (u *pasteRecordingUpstream) Frames() <-chan []byte { return nil }
func (u *pasteRecordingUpstream) Resize(int, int) error { return nil }
func (u *pasteRecordingUpstream) Close() error          { return nil }
func (u *pasteRecordingUpstream) Input(p []byte) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.inputs = append(u.inputs, string(p))
	return nil
}
func (u *pasteRecordingUpstream) PasteInput(p []byte) error {
	if u.noPaste {
		return protocol.ErrPasteInputUnsupported
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.pasted = append(u.pasted, string(p))
	return nil
}

func TestTapSub_PasteInput_ForwardsToThePastingUpstream(t *testing.T) {
	up := &pasteRecordingUpstream{}
	sub := &tapSub{t: &tap{up: up}, mode: readWrite}
	var _ protocol.PasteInputWriter = sub
	if err := sub.PasteInput([]byte("ship it")); err != nil {
		t.Fatalf("PasteInput: %v", err)
	}
	if len(up.pasted) != 1 || up.pasted[0] != "ship it" || len(up.inputs) != 0 {
		t.Fatalf("upstream pasted=%q inputs=%q, want one paste of %q", up.pasted, up.inputs, "ship it")
	}
}

func TestTapSub_PasteInput_DegradesToInputForAnOldShim(t *testing.T) {
	up := &pasteRecordingUpstream{noPaste: true}
	sub := &tapSub{t: &tap{up: up}, mode: readWrite}
	if err := sub.PasteInput([]byte("ship it")); err != nil {
		t.Fatalf("PasteInput: %v", err)
	}
	if len(up.inputs) != 1 || up.inputs[0] != "ship it" || len(up.pasted) != 0 {
		t.Fatalf("upstream pasted=%q inputs=%q, want the text as one Input", up.pasted, up.inputs)
	}
}

func TestTapSub_PasteInput_ReadOnlyDrivesNothing(t *testing.T) {
	up := &pasteRecordingUpstream{}
	sub := &tapSub{t: &tap{up: up}, mode: readOnly}
	if err := sub.PasteInput([]byte("ship it")); err != nil {
		t.Fatalf("PasteInput: %v", err)
	}
	if len(up.inputs)+len(up.pasted) != 0 {
		t.Fatalf("a read-only peek wrote pasted=%q inputs=%q", up.pasted, up.inputs)
	}
}
