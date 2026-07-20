// R-PAIR.2 — pairing QR payload codec (byte-exact, <=200 bytes).
package pairing

import (
	"bytes"
	"encoding/base64"
	"testing"
)

// TestQR_RoundTripKAT pins a hand-authored known-answer vector: the raw pre-
// base64 byte layout is written out byte-by-byte, and the KAT asserts EncodeQR
// produces QRPrefix + base64url(no padding) of exactly those bytes, and that
// DecodeQR round-trips the struct byte-exact. Authoring the raw layout (not the
// base64 text) pins the actual contract while keeping the vector verifiable.
func TestQR_RoundTripKAT(t *testing.T) {
	p := QRPayload{
		Flags:            QRFlagMachineStaticPub,
		RelayURL:         "wss://relay.example:443",
		RendezvousID:     fill16(0xA1),
		PairingSecret:    fill32(0xB2),
		MachineStaticPub: bytes.Repeat([]byte{0xC3}, 32),
	}

	var raw []byte
	raw = append(raw, QRVersion)              // version:u8 = 0x01
	raw = append(raw, QRFlagMachineStaticPub) // flags:u8
	raw = append(raw, byte(len(p.RelayURL)))  // relay_url_len:u8
	raw = append(raw, []byte(p.RelayURL)...)  // relay_url:L
	raw = append(raw, p.RendezvousID[:]...)   // rendezvous_id:16
	raw = append(raw, p.PairingSecret[:]...)  // pairing_secret:32
	raw = append(raw, p.MachineStaticPub...)  // machine_static_pub:32
	want := QRPrefix + base64.RawURLEncoding.EncodeToString(raw)

	got, err := EncodeQR(p)
	if err != nil {
		t.Fatalf("EncodeQR: %v", err)
	}
	if got != want {
		t.Fatalf("EncodeQR KAT mismatch:\n got %q\nwant %q", got, want)
	}

	back, err := DecodeQR(got)
	if err != nil {
		t.Fatalf("DecodeQR: %v", err)
	}
	if back.Flags != p.Flags {
		t.Errorf("Flags = %#x, want %#x", back.Flags, p.Flags)
	}
	if back.RelayURL != p.RelayURL {
		t.Errorf("RelayURL = %q, want %q", back.RelayURL, p.RelayURL)
	}
	if back.RendezvousID != p.RendezvousID {
		t.Errorf("RendezvousID = %x, want %x", back.RendezvousID, p.RendezvousID)
	}
	if back.PairingSecret != p.PairingSecret {
		t.Errorf("PairingSecret round-trip mismatch")
	}
	if !bytes.Equal(back.MachineStaticPub, p.MachineStaticPub) {
		t.Errorf("MachineStaticPub = %x, want %x", back.MachineStaticPub, p.MachineStaticPub)
	}
}

// TestQR_SizeBudget pins R-PAIR.2's hard <=200-byte budget for the whole encoded
// string, including the optional 32-byte machine_static_pub trailer.
func TestQR_SizeBudget(t *testing.T) {
	p := QRPayload{
		Flags:            QRFlagMachineStaticPub,
		RelayURL:         "wss://swarm-relay.example.com:443",
		RendezvousID:     fill16(0x01),
		PairingSecret:    fill32(0x02),
		MachineStaticPub: bytes.Repeat([]byte{0x03}, 32),
	}
	s, err := EncodeQR(p)
	if err != nil {
		t.Fatalf("EncodeQR: %v", err)
	}
	if len(s) > QRMaxBytes {
		t.Fatalf("encoded QR is %d bytes, exceeds the %d-byte budget (incl. machine_static_pub)", len(s), QRMaxBytes)
	}
}

// TestQR_RendezvousIndependentOfSecret pins that rendezvous_id and pairing_secret
// are independent fields: changing only the secret changes the encoding (the
// secret IS carried) yet both encodings decode to the SAME rendezvous_id (the id
// does not depend on the secret), and the two fields are distinct bytes. The
// relay only ever sees rendezvous_id.
func TestQR_RendezvousIndependentOfSecret(t *testing.T) {
	rid := fill16(0x77)
	base := QRPayload{RelayURL: "ws://x", RendezvousID: rid, PairingSecret: fill32(0x01)}
	other := base
	other.PairingSecret = fill32(0x02) // different secret, SAME rendezvous id

	e1, err := EncodeQR(base)
	if err != nil {
		t.Fatalf("EncodeQR(base): %v", err)
	}
	e2, err := EncodeQR(other)
	if err != nil {
		t.Fatalf("EncodeQR(other): %v", err)
	}
	if e1 == e2 {
		t.Fatal("changing only the secret did not change the encoding; the secret is not carried as its own field")
	}

	d1, err := DecodeQR(e1)
	if err != nil {
		t.Fatalf("DecodeQR(e1): %v", err)
	}
	d2, err := DecodeQR(e2)
	if err != nil {
		t.Fatalf("DecodeQR(e2): %v", err)
	}
	if d1.RendezvousID != rid || d2.RendezvousID != rid {
		t.Fatalf("rendezvous id changed with the secret: %x / %x, want %x", d1.RendezvousID, d2.RendezvousID, rid)
	}
	if d1.PairingSecret == d2.PairingSecret {
		t.Fatal("the two distinct secrets did not round-trip as distinct values")
	}
	if bytes.Equal(d1.RendezvousID[:], d1.PairingSecret[:16]) {
		t.Fatal("rendezvous id overlaps the secret bytes; the two must be independent fields")
	}
}
