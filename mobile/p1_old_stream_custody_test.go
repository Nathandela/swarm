package swarmmobile

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remotegw"
)

type p1BlockingAcker struct {
	entered chan struct{}
	release chan struct{}
}

func (a *p1BlockingAcker) Ack(uint64) error {
	close(a.entered)
	<-a.release
	return nil
}

func TestP1PairingWaitsForOldStreamAcceptBeforeRetiringIt(t *testing.T) {
	dir := t.TempDir()
	custody := r4r3Custody{}
	acker := &p1BlockingAcker{entered: make(chan struct{}), release: make(chan struct{})}
	config := phonecore.Config{
		Dir: dir, Ack: acker,
		WakeSealer:    custodySealer{tier: "wake", fetch: custody.WakeKEK},
		ContentSealer: custodySealer{tier: "content", fetch: custody.ContentKEK},
	}
	core, err := phonecore.Resume(config)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	oldPin := []byte("old relay pin")
	var keys crypto.EpochKeys
	for i := range keys.ContentKey {
		keys.ContentKey[i] = byte(i + 1)
	}
	if err := core.Mutate(func(st *phonecore.State) {
		st.Machine = "old-machine"
		st.EpochID = 1
		st.Keys = keys
		st.RelaySPKIPin = oldPin
	}); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	old := phonecore.PhoneBinding{
		Home:     "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		PhoneRID: "0123456789abcdef0123456789abcdef", Generation: 1, Active: true,
	}
	if err := core.ActivatePhoneBinding(old); err != nil {
		t.Fatalf("ActivatePhoneBinding: %v", err)
	}
	app := &App{
		core:     core,
		events:   newDispatcher(),
		needs:    map[string]string{},
		coalesce: phonecore.NewInputCoalescer(time.Now),
	}
	// Pairing must stop and JOIN this native stream before it can retire the binding.
	// The session sees cancellation but keeps done closed until the test permits its final
	// work to finish, which makes removal of pinWithStagedPushBinding's join observable.
	sessionCtx, sessionCancel := context.WithCancel(context.Background())
	sessionCancelled := make(chan struct{})
	sessionDoneGate := make(chan struct{})
	var releaseSession sync.Once
	oldSession := &session{ctx: sessionCtx, cancel: sessionCancel, done: make(chan struct{})}
	go func() {
		<-sessionCtx.Done()
		close(sessionCancelled)
		<-sessionDoneGate
		close(oldSession.done)
	}()
	app.mu.Lock()
	app.sess = oldSession
	app.mu.Unlock()
	t.Cleanup(func() {
		sessionCancel()
		releaseSession.Do(func() { close(sessionDoneGate) })
		_ = app.Stop()
		app.events.close()
	})

	raw, err := remotegw.SealControlReply(keys.ContentKey, 1, 1,
		protocol.Control{Op: protocol.OpOK, OperationID: "p1-old-stream"})
	if err != nil {
		t.Fatalf("SealControlReply: %v", err)
	}
	deliveryDone := make(chan error, 1)
	go func() {
		_, err := core.AcceptPhoneDelivery(old, raw, 1)
		deliveryDone <- err
	}()
	<-acker.entered // Accept committed durably and is now blocked only at its relay ACK.

	out := pairedOutcome("new-machine", 2)
	out.Machine.RelaySPKIPin = []byte("new relay pin")
	pinDone := make(chan error, 1)
	go func() { pinDone <- app.pinWithStagedPushBinding(out, nil, nil) }()
	<-sessionCancelled

	// The old delivery can now finish, but its Accept lock is no longer why pin waits:
	// it must still wait for the App-owned stream goroutine to close session.done.
	close(acker.release)
	if err := <-deliveryDone; err != nil {
		t.Fatalf("old-stream delivery: %v", err)
	}
	select {
	case err := <-pinDone:
		t.Fatalf("pairing crossed the cancelled-but-not-joined old stream: %v", err)
	case <-time.After(30 * time.Millisecond):
	}

	// A second core reads the file, rather than this core's memory, to prove neither the
	// pairing pin nor the old stream authority changed while App still waits for done.
	beforeCommit, err := phonecore.Resume(config)
	if err != nil {
		t.Fatalf("Resume before ACK: %v", err)
	}
	if got, ok := beforeCommit.PhoneBinding(); !ok || got != old {
		t.Fatalf("durable old binding before ACK = (%+v,%v), want (%+v,true)", got, ok, old)
	}
	if got := beforeCommit.State(); got.Machine != "old-machine" || !bytes.Equal(got.RelaySPKIPin, oldPin) {
		t.Fatalf("durable pin before ACK = machine %q pin %x, want old machine and pin %x", got.Machine, got.RelaySPKIPin, oldPin)
	}

	releaseSession.Do(func() { close(sessionDoneGate) })
	if err := <-pinDone; err != nil {
		t.Fatalf("pinWithStagedPushBinding: %v", err)
	}
	if err := app.Stop(); err != nil {
		t.Fatalf("Stop restarted session: %v", err)
	}
	if got, ok := core.PhoneBinding(); !ok || got.Active || got.Generation != old.Generation {
		t.Fatalf("binding after pairing = (%+v,%v), want retired old generation", got, ok)
	}

	restarted, err := phonecore.Resume(config)
	if err != nil {
		t.Fatalf("Resume after pairing: %v", err)
	}
	if got := restarted.State(); got.Machine != "new-machine" || !bytes.Equal(got.RelaySPKIPin, out.Machine.RelaySPKIPin) {
		t.Fatalf("restart pin = machine %q pin %x, want new paired values", got.Machine, got.RelaySPKIPin)
	}
	if _, err := restarted.AcceptPhoneDelivery(old, raw, 2); !errors.Is(err, phonecore.ErrPhoneBindingChanged) {
		t.Fatalf("old binding after restart = %v, want ErrPhoneBindingChanged", err)
	}
}
