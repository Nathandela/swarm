package pairing

// ADR-007 B140 (agents-tracker-tr0n): the short pairing code is a second SPELLING of the same
// secret, not a second protocol. These tests pin the two halves of that claim: the derivation
// (code -> the same 16-byte rendezvous id and 32-byte PSK the QR carries, id a function of the
// public tag ALONE) and the human contract (Crockford base32, hyphens and case and the I/L/O
// slips all forgiven, ten data characters exactly).
//
// The golden vectors matter more than they look: the derivation runs independently on the Go
// machine and the phone, and a silent change to either KDF's salt or layout is two devices that
// can never meet. A vector pinned here is a change that must be MADE twice rather than survived
// once.

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
)

// fixedReader hands out a known byte stream so a mint is reproducible in a test.
type fixedReader struct{ data []byte }

func (f *fixedReader) Read(p []byte) (int, error) {
	n := copy(p, f.data)
	f.data = f.data[n:]
	return n, nil
}

func TestMintShortCode_RoundTripsThroughItsOwnDisplayForm(t *testing.T) {
	code, id, psk, err := MintShortCode(&fixedReader{data: bytes.Repeat([]byte{0xA7, 0x39, 0xC2}, 8)})
	if err != nil {
		t.Fatalf("MintShortCode: %v", err)
	}
	gotID, gotPSK, err := DeriveShortCode(code)
	if err != nil {
		t.Fatalf("DeriveShortCode(%q) refused the mint's own display form: %v", code, err)
	}
	if gotID != id || gotPSK != psk {
		t.Fatalf("derive(mint) disagrees with the mint:\nid  %x vs %x\npsk %x vs %x",
			gotID, id, gotPSK, psk)
	}
}

func TestMintShortCode_DisplayFormIsGroupedForReading(t *testing.T) {
	code, _, _, err := MintShortCode(&fixedReader{data: bytes.Repeat([]byte{0x5B}, 8)})
	if err != nil {
		t.Fatalf("MintShortCode: %v", err)
	}
	parts := strings.Split(code, "-")
	if len(parts) != 3 || len(parts[0]) != 3 || len(parts[1]) != 4 || len(parts[2]) != 3 {
		t.Fatalf("display form %q is not XXX-XXXX-XXX", code)
	}
	for _, r := range strings.ReplaceAll(code, "-", "") {
		if !strings.ContainsRune(shortCodeAlphabet, r) {
			t.Fatalf("display form %q contains %q, outside the Crockford alphabet", code, r)
		}
	}
}

func TestDeriveShortCode_ForgivesWhatHumansDo(t *testing.T) {
	// One canonical code, spelled the ways a person actually types it. Crockford's foldings:
	// I and L are 1, O is 0, case is noise, hyphens and spaces are grouping.
	canonical := "K73-M2QF-9TD"
	wantID, wantPSK, err := DeriveShortCode(canonical)
	if err != nil {
		t.Fatalf("DeriveShortCode(%q): %v", canonical, err)
	}
	for _, typed := range []string{
		"k73m2qf9td",
		"K73 M2QF 9TD",
		"k73-m2qf-9td",
		"K7 3M2QF9T D",
	} {
		id, psk, err := DeriveShortCode(typed)
		if err != nil {
			t.Fatalf("DeriveShortCode(%q): %v", typed, err)
		}
		if id != wantID || psk != wantPSK {
			t.Fatalf("DeriveShortCode(%q) disagrees with the canonical spelling", typed)
		}
	}
	// The foldings change characters, so they need their own canonical: a code containing 1
	// and 0 reached via I, L and O.
	folded, err1 := deriveBoth("W1L-0IOB-2C3")
	plain, err2 := deriveBoth("W11-010B-2C3")
	if err1 != nil || err2 != nil {
		t.Fatalf("folding derivations: %v, %v", err1, err2)
	}
	if folded != plain {
		t.Fatalf("I/L/O foldings do not land on 1/1/0")
	}
}

func TestDeriveShortCode_RefusesWhatItMust(t *testing.T) {
	for _, bad := range []struct{ typed, why string }{
		{"", "empty"},
		{"K73-M2QF-9T", "nine data characters"},
		{"K73-M2QF-9TDX", "eleven data characters"},
		{"KU3-M2QF-9TD", "U is not in the Crockford alphabet"},
		{"K73-M2QF-9T!", "punctuation"},
	} {
		if _, _, err := DeriveShortCode(bad.typed); err == nil {
			t.Errorf("DeriveShortCode(%q) accepted %s", bad.typed, bad.why)
		}
	}
}

func TestDeriveShortCode_IdIsAFunctionOfTheTagAlone(t *testing.T) {
	// B140's offline-oracle argument rests on this: the relay sees hex(id), and nothing about
	// the secret seven characters may be derivable from it.
	idA, pskA, err := DeriveShortCode("K73-M2QF-9TD")
	if err != nil {
		t.Fatal(err)
	}
	idB, pskB, err := DeriveShortCode("K73-ZZZZ-ZZZ")
	if err != nil {
		t.Fatal(err)
	}
	if idA != idB {
		t.Fatalf("two codes sharing tag K73 derived different rendezvous ids")
	}
	if pskA == pskB {
		t.Fatalf("two different secrets derived the same PSK")
	}
	idC, _, err := DeriveShortCode("X99-M2QF-9TD")
	if err != nil {
		t.Fatal(err)
	}
	if idC == idA {
		t.Fatalf("two different tags derived the same rendezvous id")
	}
}

func TestDeriveShortCode_GoldenVectors(t *testing.T) {
	// Pinned outputs of the exact construction B140 records:
	//   id  = HKDF-SHA256(ikm=tag,    salt="swarm-remote/1 short-code-id")        -> 16 bytes
	//   psk = HKDF-SHA256(ikm=secret, salt="swarm-remote/1 short-code-psk",
	//                     info=id)                                                 -> 32 bytes
	// Regenerate only for a DELIBERATE derivation change, and change the phone in the same
	// commit -- two sides that disagree here are two devices that can never pair.
	id, psk, err := DeriveShortCode("K73-M2QF-9TD")
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(id[:]); got != goldenID {
		t.Fatalf("id vector moved:\n got %s\nwant %s", got, goldenID)
	}
	if got := hex.EncodeToString(psk[:]); got != goldenPSK {
		t.Fatalf("psk vector moved:\n got %s\nwant %s", got, goldenPSK)
	}
}

// FROZEN 2026-08-03, and not from the code under test alone: the same values were computed by
// an independent HKDF-SHA256 implementation (python hmac/hashlib) over the construction B140
// records, so a bug in shortcode.go could not have pinned itself.
const (
	goldenID  = "ff7d2852faac40bd68a43eb4e278db45"
	goldenPSK = "761f77530a9ebe8305661d84fa5b5560d63c3285579f1f051d4500ee5fb4ab4d"
)

func deriveBoth(code string) (out struct {
	id  [16]byte
	psk [32]byte
}, err error) {
	out.id, out.psk, err = DeriveShortCode(code)
	return out, err
}
