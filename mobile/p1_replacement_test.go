package swarmmobile

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/pairing"
	"github.com/Nathandela/swarm/internal/remote/relayv2"
)

// PB-BIND-6 replacement coverage for the deleted v1 conformance harness.

type p1Listener func(*Event)

func (l p1Listener) OnEvent(e *Event) { l(e) }

func TestP1ConfirmOriginDialsOnlyTheDisplayedOrigin(t *testing.T) {
	app, err := NewApp(&Config{StateDir: t.TempDir()}, r4r3Custody{})
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })

	origin := "wss://relay.example:443"
	qr, err := pairing.EncodeQR(pairing.QRPayload{RelayURL: origin})
	if err != nil {
		t.Fatalf("EncodeQR: %v", err)
	}
	dials := make(chan relayv2.Profile, 1)
	original := relayV2DialPair
	relayV2DialPair = func(_ context.Context, profile relayv2.Profile, _ string) (pairTransport, error) {
		dials <- profile
		return nil, errors.New("test dial")
	}
	t.Cleanup(func() { relayV2DialPair = original })

	p, err := app.BeginPairing(qr)
	if err != nil {
		t.Fatalf("BeginPairing: %v", err)
	}
	select {
	case <-dials:
		t.Fatal("BeginPairing dialed before ConfirmOrigin")
	default:
	}
	if err := p.ConfirmOrigin(origin + "/swapped"); err == nil {
		t.Fatal("ConfirmOrigin accepted an origin other than the displayed one")
	}
	select {
	case <-dials:
		t.Fatal("origin-swap refusal dialed")
	default:
	}

	p, err = app.BeginPairing(qr)
	if err != nil {
		t.Fatalf("second BeginPairing: %v", err)
	}
	if err := p.ConfirmOrigin(origin); err != nil {
		t.Fatalf("ConfirmOrigin(%q): %v", origin, err)
	}
	select {
	case profile := <-dials:
		if profile.RelayURL != origin {
			t.Fatalf("dialed %q, want displayed origin %q", profile.RelayURL, origin)
		}
	case <-time.After(time.Second):
		t.Fatal("exact ConfirmOrigin did not dial")
	}
}

func TestP1PinUpdatesHandsetSecurity(t *testing.T) {
	app := w9App(t, "wss://relay.example:443")
	static := bytes.Repeat([]byte{1}, 32)
	pin := bytes.Repeat([]byte{2}, 32)

	for _, tc := range []struct {
		name string
		pin  []byte
		want []byte
	}{
		{"pinned_spki installs the pin", pin, pin},
		{"later nil pin clears it", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := app.pin(&pairing.DeviceOutcome{MachineStatic: static, Machine: pairing.MachinePayload{
				MachineEndpointID: "m1", RelayTLSPolicy: "pinned_spki", RelaySPKIPin: tc.pin,
			}})
			if err != nil {
				t.Fatalf("pin: %v", err)
			}
			if got := app.handsetSecurity().PinnedSPKISHA256; !bytes.Equal(got, tc.want) {
				t.Fatalf("handset pin = %x, want %x", got, tc.want)
			}
		})
	}
}

func TestP1DispatcherRecoversListenerPanic(t *testing.T) {
	d := newDispatcher()
	t.Cleanup(d.close)
	seen := make(chan *Event, 1)
	calls := 0
	d.setListener(p1Listener(func(e *Event) {
		calls++
		if calls == 1 {
			panic("listener panic")
		}
		seen <- e
	}))
	d.emit(&Event{Kind: "first"})
	d.emit(&Event{Kind: "second"})
	select {
	case got := <-seen:
		if got.Kind != "second" {
			t.Fatalf("event after panic = %q, want second", got.Kind)
		}
	case <-time.After(time.Second):
		t.Fatal("listener panic stopped later delivery")
	}
}

func TestP1DispatcherBoundsAndSurfacesDrops(t *testing.T) {
	d := &dispatcher{wake: make(chan struct{}, 1), stop: make(chan struct{})}
	for i := 0; i <= CallbackQueueSize; i++ {
		d.emit(&Event{Cursor: int64(i)})
	}
	d.mu.Lock()
	gotLen, gotFirst, gotDropped := len(d.queue), d.queue[0].Cursor, d.dropped
	d.mu.Unlock()
	if gotLen != CallbackQueueSize || gotFirst != 1 || gotDropped != 1 {
		t.Fatalf("queue len=%d first=%d dropped=%d, want len=%d first=1 dropped=1", gotLen, gotFirst, gotDropped, CallbackQueueSize)
	}
	d.setListener(p1Listener(func(*Event) {}))
	l, overflow, ok := d.next()
	if !ok || overflow.Kind != "overflow" || overflow.Dropped != 1 {
		t.Fatalf("next overflow = %+v, ok=%v; want one observable dropped event", overflow, ok)
	}
	deliver(l, overflow)
}

func TestP1StartStopCloseRace(t *testing.T) {
	for i := 0; i < 20; i++ {
		app, err := NewApp(&Config{StateDir: t.TempDir()}, r4r3Custody{})
		if err != nil {
			t.Fatalf("NewApp: %v", err)
		}
		app.bootstrap = nil // exercise Start's session ownership path without a network destination
		var wg sync.WaitGroup
		for _, f := range []func() error{app.Start, app.Stop, app.Close} {
			wg.Add(1)
			go func(f func() error) {
				defer wg.Done()
				for n := 0; n < 10; n++ {
					_ = f()
				}
			}(f)
		}
		wg.Wait()
		if err := app.Close(); err != nil {
			t.Fatalf("final Close: %v", err)
		}
	}
}
