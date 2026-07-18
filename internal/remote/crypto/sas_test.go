// R-PAIR.4 — short authentication string (SAS) from the pairing handshake.
//
// okm = HKDF-SHA256(salt="swarm-remote/1 sas", ikm=Noise ChannelBinding());
// the first 3 bytes are read as four big-endian 6-bit indices into a fixed
// 64-entry emoji table committed identically in Go and Swift. A clean handshake
// yields the same SAS on both ends; a tampered transcript yields different SAS.
//
// FROZEN CONTRACT (subset):
//
//	func SAS(channelBinding []byte) ([4]string, error)
package crypto

import "testing"

// TestSAS_MatchOnCleanHandshake pins that both ends of a clean XX handshake
// (identical channel binding) derive the identical 4-emoji SAS.
func TestSAS_MatchOnCleanHandshake(t *testing.T) {
	a, _ := GenerateIdentity()
	b, _ := GenerateIdentity()
	ini, resp := newLivePair(t, a, b, PairPrologue([]byte("rendezvous-16byte")))
	if err := driveXX(ini, resp); err != nil {
		t.Fatalf("handshake: %v", err)
	}

	sasIni, err := SAS(ini.ChannelBinding())
	if err != nil {
		t.Fatalf("SAS(ini): %v", err)
	}
	sasResp, err := SAS(resp.ChannelBinding())
	if err != nil {
		t.Fatalf("SAS(resp): %v", err)
	}
	if sasIni != sasResp {
		t.Errorf("SAS mismatch on a clean handshake: %v vs %v", sasIni, sasResp)
	}
	for i, e := range sasIni {
		if e == "" {
			t.Errorf("SAS index %d is empty; the 64-entry table must map every index", i)
		}
	}
}

// TestSAS_MismatchOnTamper pins that a tampered transcript (different channel
// binding on the two ends, as an active MITM would produce) yields a different
// SAS, which the operators compare out-of-band and reject.
func TestSAS_MismatchOnTamper(t *testing.T) {
	// Two independent handshakes stand in for the two divergent transcripts a
	// MITM produces: different bindings must (with overwhelming probability)
	// give different SAS.
	binding := func() []byte {
		a, _ := GenerateIdentity()
		b, _ := GenerateIdentity()
		ini, resp := newLivePair(t, a, b, PairPrologue([]byte("rendezvous-16byte")))
		if err := driveXX(ini, resp); err != nil {
			t.Fatalf("handshake: %v", err)
		}
		return ini.ChannelBinding()
	}
	s1, err := SAS(binding())
	if err != nil {
		t.Fatalf("SAS(1): %v", err)
	}
	s2, err := SAS(binding())
	if err != nil {
		t.Fatalf("SAS(2): %v", err)
	}
	if s1 == s2 {
		t.Errorf("SAS collided across divergent transcripts: %v", s1)
	}
}

// TestSAS_KnownAnswer pins a cross-language KAT: a fixed channel binding maps to
// a fixed 4-emoji SAS in both Go and Swift. Derive-and-pin — HKDF-SHA256 cannot
// be computed by hand; the implementer records wantSAS from the reference
// implementation (and mirrors it in the Swift table test) at first green. The
// always-on determinism assertion still fails RED on the undefined SAS symbol.
func TestSAS_KnownAnswer(t *testing.T) {
	binding := make([]byte, 32)
	for i := range binding {
		binding[i] = byte(i)
	}
	got, err := SAS(binding)
	if err != nil {
		t.Fatalf("SAS: %v", err)
	}
	again, err := SAS(binding)
	if err != nil {
		t.Fatalf("SAS(again): %v", err)
	}
	if got != again {
		t.Fatal("SAS is not deterministic for a fixed channel binding")
	}
	if wantSAS == ([4]string{}) {
		t.Log("TODO(impl): pin wantSAS for the fixed channel binding at first green (mirror in Swift)")
	} else if got != wantSAS {
		t.Errorf("SAS KAT = %v, want %v", got, wantSAS)
	}
}

// wantSAS is a derive-and-pin cross-language KAT for the fixed channel binding
// above; filled by the implementer at first green.
var wantSAS [4]string
