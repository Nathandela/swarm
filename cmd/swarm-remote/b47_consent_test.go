package main

// Shared consent fixture (ADR-007 B47): a route consent is bound to the pairing ceremony
// that produced it, so it is the ceremony-bound statement plus the ceremony id naming it,
// packaged as one credential. These tests authorize at the relay without running a
// ceremony, so each mints a fresh id -- which is what a real pairing produces.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"

	"github.com/Nathandela/swarm/internal/remote/relay"
)

func e2eConsent(priv ed25519.PrivateKey, granteeRID string) []byte {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("swarm-remote test: crypto/rand: " + err.Error())
	}
	id := hex.EncodeToString(b[:])
	return relay.MarshalConsent(id, ed25519.Sign(priv, relay.ConsentMessage(id, granteeRID)))
}
