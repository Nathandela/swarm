package relay

// R-REL.3 — presence + machine-went-silent. Presence is per-routing-id via
// keepalive; a gateway drop (laptop sleep) transitions to offline within a
// bound and triggers the silent-push path. No long-term presence history is
// retained.

import (
	"crypto/ed25519"
	"testing"
	"time"
)

// TestPresence_TransitionsAndSilentPush asserts that when the machine's gateway
// drops, presence goes offline within the configured bound and the relay emits
// exactly one silent push toward the paired device.
func TestPresence_TransitionsAndSilentPush(t *testing.T) {
	srv, _, apns, clk := startTestRelay(t, func(c *Config) {
		c.PresenceTimeout = 30 * time.Second
	})

	// A device registers its push token so the silent-push path has a target.
	dPub, dPriv := newRelayAuthKey(t)
	device := dialAuthed(t, srv.URL(), authFor(dPub, dPriv))
	if err := device.TokenRegister(testCtx(t), "apns-token-device"); err != nil {
		t.Fatalf("TokenRegister: %v", err)
	}

	// The machine's gateway connects (online), the machine authorizes the device,
	// then the gateway drops.
	mPub, mPriv := newRelayAuthKey(t)
	machine := dialAuthed(t, srv.URL(), authFor(mPub, mPriv))
	if err := machine.AuthorizeDevice(testCtx(t), ed25519.PublicKey(dPub)); err != nil {
		t.Fatalf("AuthorizeDevice: %v", err)
	}
	machineRID := RoutingID(mPub)
	if p, err := device.Presence(testCtx(t), machineRID); err != nil || p.State != PresenceOnline {
		t.Fatalf("presence while gateway connected: got %v err=%v, want PresenceOnline", p.State, err)
	}
	if err := machine.Close(); err != nil {
		t.Fatalf("machine.Close: %v", err)
	}

	// Before the bound elapses, no silent push yet. After the bound + a sweep on
	// the injected clock, presence is offline and exactly one silent push fired.
	clk.Advance(10 * time.Second)
	srv.SweepPresence(testCtx(t))
	if got := len(apns.all()); got != 0 {
		t.Fatalf("silent push fired before the presence bound: %d", got)
	}

	clk.Advance(30 * time.Second)
	srv.SweepPresence(testCtx(t))

	if p, err := device.Presence(testCtx(t), machineRID); err != nil || p.State != PresenceOffline {
		t.Fatalf("presence after bound: got %v err=%v, want PresenceOffline", p.State, err)
	}
	pushes := apns.all()
	if len(pushes) != 1 {
		t.Fatalf("silent-push count on gateway drop: got %d, want 1", len(pushes))
	}
	if pushes[0].token != "apns-token-device" {
		t.Fatalf("silent push aimed at %q, want the paired device token", pushes[0].token)
	}
}

// TestPresence_NoHistoryRetained asserts presence is ephemeral: it is not
// persisted, so after a relay restart a previously-online machine reads back as
// unknown rather than replayed from history.
func TestPresence_NoHistoryRetained(t *testing.T) {
	srv, cfg, apns, clk := startTestRelay(t, nil)

	mPub, mPriv := newRelayAuthKey(t)
	machine := dialAuthed(t, srv.URL(), authFor(mPub, mPriv))
	machineRID := RoutingID(mPub)

	dPub, dPriv := newRelayAuthKey(t)
	device := dialAuthed(t, srv.URL(), authFor(dPub, dPriv))
	if p, err := device.Presence(testCtx(t), machineRID); err != nil || p.State != PresenceOnline {
		t.Fatalf("presence online precondition: got %v err=%v", p.State, err)
	}
	_ = machine
	_ = device

	// Restart the relay against the same store; presence must not survive.
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	srv2, err := New(cfg, WithClock(clk), WithAPNsSink(apns))
	if err != nil {
		t.Fatalf("New(restart): %v", err)
	}
	if err := srv2.Start(testCtx(t)); err != nil {
		t.Fatalf("Start(restart): %v", err)
	}
	t.Cleanup(func() { _ = srv2.Close() })

	probePub, probePriv := newRelayAuthKey(t)
	probe := dialAuthed(t, srv2.URL(), authFor(probePub, probePriv))
	if p, err := probe.Presence(testCtx(t), machineRID); err != nil || p.State != PresenceUnknown {
		t.Fatalf("presence after restart: got %v err=%v, want PresenceUnknown (no history)", p.State, err)
	}
}
