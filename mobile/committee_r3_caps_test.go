// Committee round 3 (Opus nit 6): the phone's advertised r_hello capability set and the
// relay's serverCaps live in different packages and used to be kept in sync by nothing.
// This fence dials the SHIPPED relay and asserts it grants every capability the phone
// asks for, so a rename or a removal on either side fails a test instead of silently
// running every phone in a degraded mode against its own relay.

package swarmmobile

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/relay"
)

func TestCommitteeR3_PhoneHelloCapsAreServedByTheRelay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	rcfg := relay.DefaultConfig()
	rcfg.DBPath = filepath.Join(t.TempDir(), "relay.db")
	srv, err := relay.New(rcfg)
	if err != nil {
		t.Fatalf("relay.New: %v", err)
	}
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("relay start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	conn, err := relay.DialRaw(ctx, srv.URL())
	if err != nil {
		t.Fatalf("DialRaw: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	_, agreed, err := conn.Hello(ctx, relay.ProtocolVersion, helloRequestCaps)
	if err != nil {
		t.Fatalf("Hello: %v", err)
	}
	granted := make(map[string]bool, len(agreed))
	for _, c := range agreed {
		granted[c] = true
	}
	for _, want := range helloRequestCaps {
		if !granted[want] {
			t.Errorf("the phone requests capability %q but the shipped relay does not grant it "+
				"(agreed = %v); helloRequestCaps and relay serverCaps have drifted, so every "+
				"phone would silently run degraded against its own relay", want, agreed)
		}
	}
}
