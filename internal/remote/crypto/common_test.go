// Shared fixtures + helpers for the crypto foundation's FAILING-FIRST tests
// (TDD RED, GG-5). Every symbol these tests reference is the frozen contract a
// separate implementer supplies; until then the package does not compile and
// the only errors are "undefined" for the new symbols.
package crypto

import (
	"testing"
)

// fill returns a 32-byte array of the repeated byte b — deterministic key
// material for KATs and reflection sweeps (a recognizable sentinel so a leak
// into logs / wire is obvious).
func fill(b byte) [32]byte {
	var a [32]byte
	for i := range a {
		a[i] = b
	}
	return a
}

// devKeyStore builds a file-backed KeyStore in a fresh temp dir from explicit
// material, so a test knows the private scalars it fed in.
func devKeyStore(t *testing.T, m KeyMaterial) KeyStore {
	t.Helper()
	ks, err := NewFileKeyStoreFromMaterial(t.TempDir(), m)
	if err != nil {
		t.Fatalf("NewFileKeyStoreFromMaterial: %v", err)
	}
	return ks
}

// stdMaterial is a distinct-per-key set of deterministic device key material.
func stdMaterial() KeyMaterial {
	return KeyMaterial{
		NoiseStaticPriv: fill(0x11),
		RecipientPriv:   fill(0x22),
		CommandSignSeed: fill(0x33),
		RelayAuthSeed:   fill(0x44),
	}
}

// driveXX runs a full Noise XX handshake (3 messages, empty payloads) between
// an initiator and responder session, returning the first error encountered so
// adversarial tests can assert an abort.
func driveXX(ini, resp *NoiseSession) error {
	m1, err := ini.WriteMessage(nil) // -> e
	if err != nil {
		return err
	}
	if _, err = resp.ReadMessage(m1); err != nil {
		return err
	}
	m2, err := resp.WriteMessage(nil) // -> e, ee, s, es
	if err != nil {
		return err
	}
	if _, err = ini.ReadMessage(m2); err != nil {
		return err
	}
	m3, err := ini.WriteMessage(nil) // -> s, se
	if err != nil {
		return err
	}
	if _, err = resp.ReadMessage(m3); err != nil {
		return err
	}
	return nil
}

// newLivePair builds an initiator/responder XX session pair pinned to each
// other's static, over the live prologue, ready for driveXX.
func newLivePair(t *testing.T, ini, resp *Identity, prologue []byte) (*NoiseSession, *NoiseSession) {
	t.Helper()
	i, err := NewNoise(NoiseConfig{
		Initiator:  true,
		Static:     ini.NoiseStatic(),
		PeerStatic: resp.NoiseStaticPublic(),
		Prologue:   prologue,
	})
	if err != nil {
		t.Fatalf("NewNoise(initiator): %v", err)
	}
	r, err := NewNoise(NoiseConfig{
		Initiator:  false,
		Static:     resp.NoiseStatic(),
		PeerStatic: ini.NoiseStaticPublic(),
		Prologue:   prologue,
	})
	if err != nil {
		t.Fatalf("NewNoise(responder): %v", err)
	}
	return i, r
}
