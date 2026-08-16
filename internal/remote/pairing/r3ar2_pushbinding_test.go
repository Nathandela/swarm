// FAILING-FIRST (TDD RED, GG-5) for Wave R3 ROUND 2, the pairing-decoder finding from the
// round-1 review (docs/verification/r3-green/android-green.txt):
//
// BLOCKING: decodePushBinding read four length-prefixed fields and validated NO length --
// an all-empty PushBinding round-tripped to a NON-NIL record with a nil wake key, a nil
// address and both capabilities empty, and a 3-byte wake key with a 1-byte address decoded
// cleanly. MachineOutcome.PushBinding's doc promises "never a zero-valued record a machine
// might persist as real", and the machine persists the record BEFORE confirming pairing; a
// nil or short WakeKey copied into crypto.WakeKey's [32]byte silently becomes an all-zero
// or low-entropy wake key. The decoder must refuse with errMalformedPayload, on which the
// frame already declines-and-burns.
package pairing

import (
	"errors"
	"reflect"
	"testing"
)

// r3ar2ValidBinding is one fully-populated binding: 32-byte key, 16-byte address, both
// capabilities present.
func r3ar2ValidBinding() *PushBinding {
	key := make([]byte, 32)
	addr := make([]byte, 16)
	for i := range key {
		key[i] = byte(i + 1)
	}
	for i := range addr {
		addr[i] = byte(0xB0 + i)
	}
	return &PushBinding{
		WakeKey:                 key,
		PushAddress:             addr,
		SubmitCapability:        "submit-cap",
		MachineRevokeCapability: "revoke-cap",
		CapabilityRecordVersion: 1,
	}
}

// TestR3AR2_DecodePushBinding_RefusesAZeroValuedOrShortRecord: every malformed variant is
// refused with errMalformedPayload, and the one valid shape still round-trips exactly.
func TestR3AR2_DecodePushBinding_RefusesAZeroValuedOrShortRecord(t *testing.T) {
	cases := map[string]*PushBinding{
		"all-empty record":                {},
		"3-byte wake key, 1-byte address": {WakeKey: []byte{1, 2, 3}, PushAddress: []byte{9}, SubmitCapability: "s", MachineRevokeCapability: "r"},
		"31-byte wake key":                func() *PushBinding { b := r3ar2ValidBinding(); b.WakeKey = b.WakeKey[:31]; return b }(),
		"15-byte address":                 func() *PushBinding { b := r3ar2ValidBinding(); b.PushAddress = b.PushAddress[:15]; return b }(),
		"empty submit capability":         func() *PushBinding { b := r3ar2ValidBinding(); b.SubmitCapability = ""; return b }(),
		"empty machine-revoke capability": func() *PushBinding { b := r3ar2ValidBinding(); b.MachineRevokeCapability = ""; return b }(),
	}
	for name, malformed := range cases {
		got, err := decodePushBinding(encodePushBinding(malformed))
		if err == nil {
			t.Errorf("%s: decoded to %+v, want errMalformedPayload (a machine would persist it as real)", name, got)
			continue
		}
		if !errors.Is(err, errMalformedPayload) {
			t.Errorf("%s: got %v, want errMalformedPayload (the decline-and-burn error)", name, err)
		}
	}

	valid := r3ar2ValidBinding()
	got, err := decodePushBinding(encodePushBinding(valid))
	if err != nil {
		t.Fatalf("the fully-populated binding was refused: %v", err)
	}
	if !reflect.DeepEqual(got, valid) {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", got, valid)
	}
}

// TestR3AR2_DecodeConsentFrame_RefusesAnEmptyBindingBesideTheConsent: the full msg4 path.
// A framed consent carrying a zero-valued binding must fail the frame (decline-and-burn),
// never hand the machine a non-nil empty record beside a valid consent; the same frame
// with a fully-populated binding still decodes both halves.
func TestR3AR2_DecodeConsentFrame_RefusesAnEmptyBindingBesideTheConsent(t *testing.T) {
	consent := []byte("the-signed-consent")

	if _, b, err := decodeConsentFrame(encodeConsentFrame(consent, &PushBinding{})); err == nil {
		t.Fatalf("a frame carrying an all-empty binding decoded cleanly (binding %+v)", b)
	}

	gotConsent, gotBinding, err := decodeConsentFrame(encodeConsentFrame(consent, r3ar2ValidBinding()))
	if err != nil {
		t.Fatalf("the valid frame was refused: %v", err)
	}
	if string(gotConsent) != string(consent) {
		t.Errorf("consent round-trip mismatch: got %q", gotConsent)
	}
	if !reflect.DeepEqual(gotBinding, r3ar2ValidBinding()) {
		t.Errorf("binding round-trip mismatch: got %+v", gotBinding)
	}
}
