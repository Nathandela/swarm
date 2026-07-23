package protocol

// FAILING-FIRST protocol tests for the pairing WIRE TYPES ONLY (slice A3.3-a,
// ADR-007 amendment "Pairing host: Option A"). This slice adds no handlers and no
// pairing LOGIC — just the four owner-tier pairing ops and their payload shape, so a
// later slice can add the handlers + PairingHost bridge against a frozen wire
// contract. RED is compile-fail-only: `go test ./internal/protocol/ -run
// TestControl_Pairing` fails to compile because the production symbols below do not
// exist yet.
//
// FROZEN API this slice's GREEN step must add (mirrors the existing
// Approve/*ApproveReq nested-pointer pattern in types.go):
//
//	const (
//	    OpPairStart   = "pair_start"
//	    OpPairPending = "pair_pending"
//	    OpPairConfirm = "pair_confirm"
//	    OpPairResult  = "pair_result"
//	)
//
//	// ONE new direct field on Control (minimizes GG-7 drift rows):
//	//   Pairing *PairingControl `json:"pairing,omitempty"`
//
//	type PairingControl struct {
//	    Capability   string     `json:"capability,omitempty"`    // pair_start req
//	    TTLSeconds   int        `json:"ttl_seconds,omitempty"`   // pair_start req
//	    QR           string     `json:"qr,omitempty"`            // pair_start reply
//	    RendezvousID string     `json:"rendezvous_id,omitempty"` // reply + pending + confirm correlation
//	    ExpiresAt    *time.Time `json:"expires_at,omitempty"`    // pair_start reply
//	    SAS          []string   `json:"sas,omitempty"`           // pair_pending
//	    DeviceName   string     `json:"device_name,omitempty"`   // pair_pending
//	    Allow        bool       `json:"allow,omitempty"`         // pair_confirm
//	    DeviceID     string     `json:"device_id,omitempty"`     // pair_result
//	    Name         string     `json:"name,omitempty"`          // pair_result
//	}
//
// A nested struct's own fields do NOT need protocol.md rows (only the single direct
// Control.Pairing field does) — see docs/specifications/protocol.md's GG-7 drift
// gate.

import (
	"strings"
	"testing"
	"time"
)

// TestControl_PairingFieldOmitEmpty (GG-7): a Control with Pairing == nil marshals
// to JSON that does NOT contain the "pairing" key — mirrors
// TestControl_AdditiveFieldsOmitEmpty in remote_hello_test.go. Pins the
// additive-safety invariant: an existing-shape Control is byte-unaffected by this
// slice.
func TestControl_PairingFieldOmitEmpty(t *testing.T) {
	old := Control{Op: OpKill, EndpointID: "ep-1", SessionID: "ep-1/sess1"}
	body, err := EncodeControl(old)
	if err != nil {
		t.Fatalf("EncodeControl(old): %v", err)
	}
	if strings.Contains(string(body), `"pairing"`) {
		t.Fatalf("old-shape Control emitted new key %q: %s (Pairing must be omitempty)", "pairing", body)
	}
}

// TestControl_PairingRoundTrip (A3.3-a): each pairing-phase subset of
// PairingControl survives an EncodeControl/DecodeControl round trip, exercising the
// three later phases (pair_pending, pair_confirm, pair_result) that carry
// distinct field subsets. pair_start is exercised separately below because it is
// the only phase with BOTH a request subset (Capability/TTLSeconds) and a reply
// subset (QR/RendezvousID/ExpiresAt) live at once.
func TestControl_PairingRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		in   Control
	}{
		{
			name: "pair_pending",
			in: Control{
				Op:         OpPairPending,
				EndpointID: "ep-1",
				Pairing: &PairingControl{
					SAS:          []string{"a", "b", "c", "d", "e", "f"},
					DeviceName:   "phone",
					RendezvousID: "rv1",
				},
			},
		},
		{
			name: "pair_confirm",
			in: Control{
				Op:         OpPairConfirm,
				EndpointID: "ep-1",
				Pairing: &PairingControl{
					Allow:        true,
					RendezvousID: "rv1",
				},
			},
		},
		{
			name: "pair_result",
			in: Control{
				Op:         OpPairResult,
				EndpointID: "ep-1",
				Pairing: &PairingControl{
					DeviceID: "dev-42",
					Name:     "phone",
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := EncodeControl(tc.in)
			if err != nil {
				t.Fatalf("EncodeControl(%s): %v", tc.name, err)
			}
			got, err := DecodeControl(body)
			if err != nil {
				t.Fatalf("DecodeControl(%s): %v", tc.name, err)
			}
			if !jsonEqual(tc.in, got) {
				t.Fatalf("round-trip mismatch for %s:\n in = %+v\nout = %+v", tc.name, tc.in, got)
			}
			if got.Pairing == nil {
				t.Fatalf("%s: round-tripped Control.Pairing is nil", tc.name)
			}
		})
	}
}

// TestControl_PairStartRoundTrip (A3.3-a): pair_start is the one op whose
// PairingControl mixes a REQUEST subset (Capability, TTLSeconds) with a REPLY
// subset (QR, RendezvousID, ExpiresAt) — asserted separately since a real exchange
// carries the request fields outbound and the reply fields inbound, but the wire
// type must round-trip both live at once (e.g. an echoed request alongside a
// reply, or a test fixture asserting the full field set survives).
func TestControl_PairStartRoundTrip(t *testing.T) {
	expires := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	in := Control{
		Op:         OpPairStart,
		EndpointID: "ep-1",
		Pairing: &PairingControl{
			Capability:   "full",
			TTLSeconds:   300,
			QR:           "data:image/png;base64,AAAA",
			RendezvousID: "rv-99",
			ExpiresAt:    &expires,
		},
	}
	body, err := EncodeControl(in)
	if err != nil {
		t.Fatalf("EncodeControl(pair_start): %v", err)
	}
	got, err := DecodeControl(body)
	if err != nil {
		t.Fatalf("DecodeControl(pair_start): %v", err)
	}
	if got.Pairing == nil {
		t.Fatalf("pair_start: round-tripped Control.Pairing is nil")
	}
	if got.Pairing.Capability != "full" || got.Pairing.TTLSeconds != 300 {
		t.Fatalf("pair_start: request subset did not survive: %+v", got.Pairing)
	}
	if got.Pairing.QR != in.Pairing.QR || got.Pairing.RendezvousID != in.Pairing.RendezvousID {
		t.Fatalf("pair_start: reply subset did not survive: %+v", got.Pairing)
	}
	if got.Pairing.ExpiresAt == nil || !got.Pairing.ExpiresAt.Equal(expires) {
		t.Fatalf("pair_start: ExpiresAt did not survive: %+v", got.Pairing.ExpiresAt)
	}
}
