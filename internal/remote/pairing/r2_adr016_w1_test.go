package pairing

// ADR-016 W1 (FAILING FIRST, RED phase, bd agents-tracker-hggx.3.5): "The policy is a
// field of ... pairing.MachinePayload, alongside relay_host -- the hostname the machine
// itself dials."
//
// MachinePayload gains RelayTLSPolicy and RelayHost, additive fields riding BEFORE the
// existing epoch trailer (the same convention RelaySPKIPin already follows -- see
// encodeMachinePayload's own doc comment), independent of the RelaySPKIPin field that
// already exists on this struct. RelayHost is a NEW concept distinct from Hostname:
// Hostname is the machine's own display label (machineid.Identity.Hostname()), RelayHost
// is "the hostname the machine itself dials" -- the relay's DNS name, used by the phone's
// W4 migration probe to confirm a republished profile still names the SAME destination
// before it is trusted (W4 step 2, "a profile that changes the destination ... is refused
// as stale_profile").
//
// Internal test file (package pairing, not pairing_test) because encodeMachinePayload/
// decodeMachinePayload are unexported -- the same access pattern this package's existing
// wire tests use.

import (
	"encoding/binary"
	"testing"
)

// TestADR016W1_MachinePayloadCarriesTLSPolicyAndHostIndependentOfThePin round-trips a
// payload with the policy set, the host set, and NO pin -- the exact state W9's ladder
// starts from ("a webpki machine and configures no pin at all -- the intended end state")
// -- and a second payload with a pin and NO policy, proving neither field leaks into the
// other's absence.
func TestADR016W1_MachinePayloadCarriesTLSPolicyAndHostIndependentOfThePin(t *testing.T) {
	webpkiNoPin := MachinePayload{
		Hostname:            "nathans-mbp",
		MachineRoutingID:    []byte("routing-id-32-bytes-of-filler!!"),
		MachineRelayAuthPub: []byte("relay-auth-pub-32-bytes-filler!"),
		RecipientPub:        []byte("recipient-pub-32-bytes-filler!!"),
		MachineSignPub:      []byte("sign-pub-32-bytes-of-filler!!!!"),
		MachineEndpointID:   "ep-abc123",
		RelayTLSPolicy:      "webpki",
		RelayHost:           "swarm-relay.example.com",
		EpochID:             7,
	}
	got, err := decodeMachinePayload(encodeMachinePayload(webpkiNoPin))
	if err != nil {
		t.Fatalf("decode(encode(webpki, no pin)): %v", err)
	}
	if got.RelayTLSPolicy != "webpki" {
		t.Errorf("RelayTLSPolicy = %q, want %q", got.RelayTLSPolicy, "webpki")
	}
	if got.RelayHost != "swarm-relay.example.com" {
		t.Errorf("RelayHost = %q, want %q", got.RelayHost, "swarm-relay.example.com")
	}
	if len(got.RelaySPKIPin) != 0 {
		t.Errorf("RelaySPKIPin = %x, want empty -- a webpki payload with no pin configured must "+
			"round-trip that absence, not manufacture one", got.RelaySPKIPin)
	}

	pinnedNoPolicyHost := MachinePayload{
		Hostname:            "nathans-mbp",
		MachineRoutingID:    []byte("routing-id-32-bytes-of-filler!!"),
		MachineRelayAuthPub: []byte("relay-auth-pub-32-bytes-filler!"),
		RecipientPub:        []byte("recipient-pub-32-bytes-filler!!"),
		MachineSignPub:      []byte("sign-pub-32-bytes-of-filler!!!!"),
		MachineEndpointID:   "ep-abc123",
		RelaySPKIPin:        []byte("32-byte-sha256-digest-of-spki!!"),
		EpochID:             7,
	}
	got2, err := decodeMachinePayload(encodeMachinePayload(pinnedNoPolicyHost))
	if err != nil {
		t.Fatalf("decode(encode(pinned, no policy/host)): %v", err)
	}
	if got2.RelayTLSPolicy != "" {
		t.Errorf("RelayTLSPolicy = %q, want empty: a MachinePayload{} literal with no policy set "+
			"must not encode-then-decode one from nowhere. This does NOT exercise a genuine "+
			"pre-ADR-016 WIRE payload -- see TestADR016W9_DecodeMachinePayloadAcceptsALegacySevenFieldWire "+
			"for that", got2.RelayTLSPolicy)
	}
	if got2.RelayHost != "" {
		t.Errorf("RelayHost = %q, want empty", got2.RelayHost)
	}
	if string(got2.RelaySPKIPin) != string(pinnedNoPolicyHost.RelaySPKIPin) {
		t.Errorf("RelaySPKIPin = %x, want %x", got2.RelaySPKIPin, pinnedNoPolicyHost.RelaySPKIPin)
	}
}

// TestADR016W1_MachinePayloadEncodingIsForwardCompatibleWithTheEpochTrailer pins the
// ADDITIVE ordering rule encodeMachinePayload's doc comment states for every field added
// so far ("rides BEFORE the epoch trailer, so the epoch-trailer contract is undisturbed"):
// the new fields must not move EpochID, which decodeMachinePayload locates as "whatever is
// left after the last length-prefixed field, and must be exactly 4 bytes".
func TestADR016W1_MachinePayloadEncodingIsForwardCompatibleWithTheEpochTrailer(t *testing.T) {
	p := MachinePayload{
		Hostname:            "h",
		MachineRoutingID:    []byte("r"),
		MachineRelayAuthPub: []byte("a"),
		RecipientPub:        []byte("p"),
		MachineSignPub:      []byte("s"),
		MachineEndpointID:   "e",
		RelayTLSPolicy:      "webpki",
		RelayHost:           "relay.example.com",
		RelaySPKIPin:        []byte("pin"),
		EpochID:             42,
	}
	b := encodeMachinePayload(p)
	got, err := decodeMachinePayload(b)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.EpochID != 42 {
		t.Errorf("EpochID = %d, want 42 -- the new fields displaced the epoch trailer", got.EpochID)
	}
}

// TestADR016W9_DecodeMachinePayloadAcceptsALegacySevenFieldWire is W9's N/N-1 claim, proven
// against an ACTUAL pre-ADR-016 wire shape rather than a new encoder's zero values: a
// payload built with the SEVEN fields encodeMachinePayload wrote before RelayTLSPolicy and
// RelayHost existed, followed directly by the 4-byte epoch trailer -- exactly what a machine
// binary built before this ADR still sends. "New app / old machine. No policy field means
// pinned_spki with whatever pin the payload carries, i.e. today's behaviour exactly" (W9) is
// false if this refuses to decode at all.
func TestADR016W9_DecodeMachinePayloadAcceptsALegacySevenFieldWire(t *testing.T) {
	var b []byte
	b = appendField(b, []byte("nathans-mbp"))
	b = appendField(b, []byte("routing-id-32-bytes-of-filler!!"))
	b = appendField(b, []byte("relay-auth-pub-32-bytes-filler!"))
	b = appendField(b, []byte("recipient-pub-32-bytes-filler!!"))
	b = appendField(b, []byte("sign-pub-32-bytes-of-filler!!!!"))
	b = appendField(b, []byte("ep-abc123"))
	pin := []byte("32-byte-sha256-digest-of-spki!!")
	b = appendField(b, pin)
	b = binary.BigEndian.AppendUint32(b, 7) // the epoch trailer, with NOTHING after it

	got, err := decodeMachinePayload(b)
	if err != nil {
		t.Fatalf("decodeMachinePayload refused a legacy (pre-ADR-016) seven-field wire payload: %v.\n"+
			"A new app talking to an old machine can never pair at all -- W9's N/N-1 claim "+
			"('additive, version-skew safe') is false", err)
	}
	if got.RelayTLSPolicy != "" {
		t.Errorf("RelayTLSPolicy = %q, want empty on a legacy wire payload", got.RelayTLSPolicy)
	}
	if got.RelayHost != "" {
		t.Errorf("RelayHost = %q, want empty on a legacy wire payload", got.RelayHost)
	}
	if string(got.RelaySPKIPin) != string(pin) {
		t.Errorf("RelaySPKIPin = %x, want %x -- the legacy pin must still carry", got.RelaySPKIPin, pin)
	}
	if got.EpochID != 7 {
		t.Errorf("EpochID = %d, want 7", got.EpochID)
	}
	if got.MachineEndpointID != "ep-abc123" {
		t.Errorf("MachineEndpointID = %q, want %q", got.MachineEndpointID, "ep-abc123")
	}
}
