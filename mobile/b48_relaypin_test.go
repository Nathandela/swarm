package swarmmobile

// ADR-007 B48, phone half: the pairing dial runs unverified, so what it presented is
// compared against the pin the machine authored in msg2. These pin the decision itself;
// internal/remote/relay/b48_spki_test.go pins that the certificate is observable at all,
// and internal/remote/pairing/b48_verify_test.go pins that the check runs at msg2.

import (
	"bytes"
	"errors"
	"testing"
)

func TestB48_CheckRelayPin(t *testing.T) {
	pin := bytes.Repeat([]byte{0xD4}, 32)
	other := bytes.Repeat([]byte{0xEE}, 32)

	for _, tc := range []struct {
		name       string
		machinePin []byte
		presented  []byte
		want       error
	}{
		{"the certificate is the one the machine pinned", pin, pin, nil},
		{"a terminator presented something else", pin, other, errRelayPinUnmatched},
		{"the machine configured no pin, so it claimed nothing", nil, other, nil},
		{"a cleartext loopback dial observed no certificate", pin, nil, nil},
		{"neither side has anything to say", nil, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := checkRelayPin(tc.machinePin, tc.presented); !errors.Is(err, tc.want) {
				t.Fatalf("checkRelayPin = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestB48_APrefixOfThePinIsNotAMatch. A comparison that truncated -- or that accepted a
// prefix -- would let a terminator match on a fraction of the digest, which is the
// difference between a pin and a hint.
func TestB48_APrefixOfThePinIsNotAMatch(t *testing.T) {
	pin := bytes.Repeat([]byte{0xD4}, 32)
	if err := checkRelayPin(pin, pin[:16]); !errors.Is(err, errRelayPinUnmatched) {
		t.Fatalf("checkRelayPin with a truncated observation = %v, want errRelayPinUnmatched", err)
	}
	if err := checkRelayPin(pin[:16], pin); !errors.Is(err, errRelayPinUnmatched) {
		t.Fatalf("checkRelayPin with a truncated pin = %v, want errRelayPinUnmatched", err)
	}
}
