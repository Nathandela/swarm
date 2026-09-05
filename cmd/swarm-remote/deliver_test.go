package main

// Gateway grant delivery appends the persisted sealed EpochGrant to the already-authorized
// DEVICE mailbox as a tagged plaintext bootstrap frame the phone can find WITHOUT a
// ContentKey. Native generation authorization is covered at the runGeneration seam.
//
// The helper is exercised at its generic mailbox boundary; native transport and generation
// authorization have their own protocol tests.

import (
	"context"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/grant"
)

type grantCapture struct {
	target string
	frame  []byte
}

func (c *grantCapture) MailboxAppend(_ context.Context, target string, frame []byte) (uint64, error) {
	c.target = target
	c.frame = append([]byte(nil), frame...)
	return 1, nil
}

func TestDeliverEpochGrant_AppendsBootstrapToAuthorizedMailbox(t *testing.T) {
	seeded := &crypto.EpochGrant{EpochID: 7, GrantSeq: 9, Sealed: []byte("sealed"), Sig: []byte("sig")}
	p := gatewayParams{PhoneTarget: "phone-routing-id", Grant: seeded}
	capture := new(grantCapture)

	if err := deliverEpochGrant(context.Background(), capture, p); err != nil {
		t.Fatalf("deliverEpochGrant: %v", err)
	}
	if capture.target != p.PhoneTarget {
		t.Fatalf("append target = %q, want %q", capture.target, p.PhoneTarget)
	}
	found, ok := grant.ParseBootstrap(capture.frame)
	if !ok {
		t.Fatalf("append is not an epoch_grant_bootstrap: %q", capture.frame)
	}
	if found.EpochID != seeded.EpochID || found.GrantSeq != seeded.GrantSeq ||
		string(found.Sealed) != string(seeded.Sealed) || string(found.Sig) != string(seeded.Sig) {
		t.Fatal("delivered bootstrap grant != seeded grant")
	}
}
