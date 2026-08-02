// R-PAIR.4 — short authentication string (SAS) from the pairing handshake.
//
// okm = HKDF-SHA256(salt="swarm-remote/1 sas", ikm=Noise ChannelBinding());
// five bytes of okm are read as SIX big-endian 6-bit indices into a fixed
// 64-entry emoji table committed identically in Go and Swift. A clean handshake
// yields the same SAS on both ends; a tampered transcript yields different SAS.
//
// ADR-007 amendment 2026-07-23 widens the SAS from 24 bits (four emoji) to 36
// bits (six emoji) to close the pairing grind attack (review finding MED-1): a
// leaked-QR attacker holding a live man-in-the-middle position could grind
// ~2^24 candidate keypairs (seconds on commodity hardware) to force its channel
// binding to a SAS equal to the honest leg's; six emoji raise that to ~2^36,
// computationally infeasible inside a rate-limited pairing window. This is a
// LENGTH EXTENSION ONLY — the salt ("swarm-remote/1 sas"), the 64-emoji
// wordlist, and the HKDF construction are unchanged.
//
// FROZEN CONTRACT (subset):
//
//	func SAS(channelBinding []byte) ([6]string, error)
package crypto

import (
	"errors"
	"testing"
)

// inSASTable reports whether e is one of the 64 canonical SAS emoji. The SAS is
// only meaningful if every rendered element indexes the shared, byte-identical
// table (mirrored in the Swift/Android clients).
func inSASTable(e string) bool {
	for _, w := range sasWords {
		if w == e {
			return true
		}
	}
	return false
}

// TestSAS_MatchOnCleanHandshake pins that both ends of a clean XX handshake
// (identical channel binding) derive the identical 6-emoji SAS, and that every
// emoji is a non-empty entry of the 64-table.
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
		if !inSASTable(e) {
			t.Errorf("SAS index %d = %q is not in the 64-emoji table", i, e)
		}
	}
}

// TestSAS_MismatchOnTamper pins that a tampered transcript (different channel
// binding on the two ends, as an active MITM would produce) yields a different
// SAS, which the operators compare out-of-band and reject. Widening the SAS only
// strengthens this divergence; the intent is unchanged.
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

// TestSAS_EmptyBindingErrors pins that an empty channel binding is rejected with
// ErrEmptyBinding and yields the zero SAS (unchanged by the width extension).
func TestSAS_EmptyBindingErrors(t *testing.T) {
	got, err := SAS(nil)
	if !errors.Is(err, ErrEmptyBinding) {
		t.Fatalf("SAS(nil) err = %v, want ErrEmptyBinding", err)
	}
	if got != ([6]string{}) {
		t.Errorf("SAS(nil) = %v, want the zero value on error", got)
	}
}

// TestSAS_KnownAnswer pins a cross-language KAT (interop lock): a fixed channel
// binding maps to a fixed SIX-emoji SAS in Go, Swift, and Android. HKDF-SHA256
// cannot be computed by hand, so the implementer records wantSAS from the
// reference implementation at first green and MUST mirror it byte-identically in
// the on-device SAS table test. That cross-language KAT is an explicit release
// gate (ADR-007 amendment 2026-07-23): if the six emoji diverge across
// platforms, pairing SAS comparison breaks. Until wantSAS is pinned, the
// always-on structural assertions (determinism, len 6, every emoji in the
// 64-table) still fail RED against the [4]string SAS symbol.
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
	for i, e := range got {
		if !inSASTable(e) {
			t.Errorf("SAS KAT index %d = %q is not in the 64-emoji table", i, e)
		}
	}
	if got != wantSAS {
		t.Errorf("SAS KAT = %v, want %v", got, wantSAS)
	}
}

// wantSAS is the derive-and-pin cross-language KAT for the fixed channel binding
// above (32 bytes, binding[i] = byte(i)): the concrete SIX-emoji SAS the Go
// reference implementation produces at first green under the ADR-007 2026-07-23
// 36-bit layout. This EXACT six-emoji sequence MUST be mirrored byte-identically
// in the Swift/Android SAS table KAT — the on-device cross-language KAT is a
// release gate (not verifiable in this repo): if the six emoji diverge across
// platforms, pairing SAS comparison breaks.
var wantSAS = [6]string{"🐷", "🐧", "🐰", "🦉", "🔨", "🐔"}
