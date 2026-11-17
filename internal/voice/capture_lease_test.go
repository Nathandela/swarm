package voice

import (
	"context"
	"errors"
	"testing"
)

func TestCaptureTakeoverFencesExternalResultAndOldServer(t *testing.T) {
	controller, store, _, binding := newControllerFixture(t)
	entered, finish := make(chan struct{}), make(chan struct{})
	c := &managerControl{controller: controller, store: store, binding: binding, wake: make(chan struct{}, 1), preparePTT: func() (pttTranscriber, error) {
		return func(context.Context, []byte) (string, error) { close(entered); <-finish; return "prior result", nil }, nil
	}}
	if _, err := c.beginCapture("first", "device:alice:ptt", "first-session", ""); err != nil {
		t.Fatal(err)
	}
	current := c.capture.activeLocked()
	done := make(chan error, 1)
	go func() {
		_, err := c.finalizeScopedPTT(t.Context(), "first", "device:alice:ptt", "first-session", []byte{1, 2}, nil, nil)
		done <- err
	}()
	<-entered
	if _, err := c.beginCapture("replacement", localPTTDevice, "", "wrong-lease"); !errors.Is(err, ErrStale) {
		t.Fatal("stale owner takeover", err)
	}
	if _, err := c.beginCapture("replacement", localPTTDevice, "", current.ID); err != nil {
		t.Fatal(err)
	}
	close(finish)
	if err := <-done; !errors.Is(err, ErrStale) {
		t.Fatal("old result crossed takeover", err)
	}
	state, err := store.Snapshot()
	if err != nil || state.Utterances[key(binding.ConversationID, "first")].Admission != 0 {
		t.Fatal("old result was admitted", err)
	}
	if _, err := c.beginPTT("different"); !errors.Is(err, ErrCaptureBusy) {
		t.Fatal("old finalizer released replacement", err)
	}
	restart := &managerControl{controller: controller, store: store, binding: binding, preparePTT: c.preparePTT}
	if _, err := restart.beginPTT("replacement"); !errors.Is(err, ErrExpired) {
		t.Fatal("reconstructed stale capture", err)
	}
	if _, err := restart.finalizeScopedPTT(t.Context(), "replacement", localPTTDevice, "", []byte{1, 2}, nil, nil); !errors.Is(err, ErrStale) {
		t.Fatal("lost service epoch finalized", err)
	}
	if _, err := restart.beginPTT("new-after-restart"); err != nil {
		t.Fatal(err)
	}
}
