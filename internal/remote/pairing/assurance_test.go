// Security-assurance coverage for the pairing slice (PR-H1). These tests pin the
// package's FAIL-CLOSED behavior against the current CORRECT implementation, so a
// future regression that opens any of these holes is caught. They are ADDITIVE:
// they modify no existing (frozen) test and add no implementation. Nothing in
// internal/remote/crypto is touched — only its frozen public API is referenced.
//
// Harness reuse: the in-memory two-party transport double (fakeRendezvous,
// newRendezvousPipe), the params builders (newMachineParams / newDeviceParams),
// the confirm helpers (acceptConfirm) and the refusingRendezvous double all come
// from harness_test.go. Where a test must tamper wire bytes it wraps the harness
// transport with a small local double (recvMutator / sendFailer) rather than
// editing the harness.
package pairing

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// ---------------------------------------------------------------------------
// Local transport doubles that tamper a specific in-flight frame. Each wraps an
// underlying RendezvousTransport (a fakeRendezvous end) and mutates exactly one
// Recv / Send, modeling a relay or on-path peer corrupting, truncating, or
// dropping a single handshake / decision frame. The counter is only ever touched
// from the single leg goroutine that owns the wrapper, so this is race-free.
// ---------------------------------------------------------------------------

// recvMutator applies mutate to the payload of the target-th (1-based) Recv. A
// mutate returning (nil, err) models a dropped / aborted frame.
type recvMutator struct {
	RendezvousTransport
	target int
	count  int
	mutate func([]byte) ([]byte, error)
}

func (r *recvMutator) Recv(ctx context.Context) ([]byte, error) {
	b, err := r.RendezvousTransport.Recv(ctx)
	if err != nil {
		return b, err
	}
	r.count++
	if r.count == r.target {
		return r.mutate(b)
	}
	return b, nil
}

// sendFailer fails the target-th (1-based) Send with err, modeling a peer /
// transport that aborts mid-handshake before a frame reaches the wire.
type sendFailer struct {
	RendezvousTransport
	target int
	count  int
	err    error
}

func (s *sendFailer) Send(ctx context.Context, msg []byte) error {
	s.count++
	if s.count == s.target {
		return s.err
	}
	return s.RendezvousTransport.Send(ctx, msg)
}

// flipLast / dropLast are byte tampers guaranteed to break an AEAD frame (a
// flipped or missing final tag byte fails authentication) without panicking.
func flipLast(b []byte) ([]byte, error) {
	c := append([]byte(nil), b...)
	if len(c) > 0 {
		c[len(c)-1] ^= 0xFF
	}
	return c, nil
}

func dropLast(b []byte) ([]byte, error) {
	c := append([]byte(nil), b...)
	if len(c) > 0 {
		c = c[:len(c)-1]
	}
	return c, nil
}

// drivePairCancel runs both legs on a cancelable context, cancels as soon as one
// leg returns, then joins both. A fail-closed abort on one side leaves the peer
// blocked on a Recv the aborted side will never answer; cancelling releases it,
// so the test neither deadlocks nor leaks a goroutine. The channel close before
// each read establishes the happens-before for the returned values (race-clean).
func drivePairCancel(t *testing.T, m *Machine, dp DeviceParams, mEnd, dEnd RendezvousTransport) (mo *MachineOutcome, mErr error, do *DeviceOutcome, dErr error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mDone := make(chan struct{})
	dDone := make(chan struct{})
	go func() { mo, mErr = m.Pair(ctx, mEnd); close(mDone) }()
	go func() { do, dErr = RunDevice(ctx, dp, dEnd); close(dDone) }()
	select {
	case <-mDone:
	case <-dDone:
	}
	cancel()
	<-mDone
	<-dDone
	return mo, mErr, do, dErr
}

// ---------------------------------------------------------------------------
// PR-H1 property 1: hostile / malformed QR decode fails closed with
// ErrQRMalformed and never panics.
// ---------------------------------------------------------------------------

// craftQRRaw builds a well-formed pre-base64 QR byte layout, which callers then
// mutate into hostile variants (see qr.go for the layout).
func craftQRRaw(flagStatic bool, url string) []byte {
	var raw []byte
	raw = append(raw, QRVersion)
	var flags byte
	if flagStatic {
		flags |= QRFlagMachineStaticPub
	}
	raw = append(raw, flags)
	raw = append(raw, byte(len(url)))
	raw = append(raw, []byte(url)...)
	raw = append(raw, make([]byte, 16)...) // rendezvous_id
	raw = append(raw, make([]byte, 32)...) // pairing_secret
	if flagStatic {
		raw = append(raw, make([]byte, 32)...) // machine_static_pub trailer
	}
	return raw
}

func qrString(raw []byte) string { return QRPrefix + base64.RawURLEncoding.EncodeToString(raw) }

// TestQR_HostileDecodeFailsClosed drives DecodeQR over crafted hostile inputs:
// bad prefix, wrong version byte, truncated body/header, a flags/trailer length
// mismatch, trailing extra bytes (field-splice), an oversized (>200-byte) blob,
// and invalid base64. Every one MUST fail closed with ErrQRMalformed and never
// panic (assurance: a future decoder regression is caught here).
func TestQR_HostileDecodeFailsClosed(t *testing.T) {
	valid := craftQRRaw(false, "ws://r")      // flag clear, no trailer (rest == 0)
	validStatic := craftQRRaw(true, "ws://r") // flag set, 32-byte trailer (rest == 32)

	badVersion := append([]byte(nil), valid...)
	badVersion[0] = 0x02 // unknown version

	flagSetNoTrailer := append([]byte(nil), valid...)
	flagSetNoTrailer[1] |= QRFlagMachineStaticPub // claims a trailer that is absent (rest == 0 != 32)

	spliceTrailing := append(append([]byte(nil), valid...), 0xAA, 0xBB) // flag clear but rest != 0

	staticWrongLen := append(append([]byte(nil), validStatic...), 0xCC, 0xDD, 0xEE) // rest == 35 != 32

	cases := []struct {
		name  string
		input string
	}{
		{"bad_prefix_wrong_version_tag", "swarm-pair:2:" + base64.RawURLEncoding.EncodeToString(valid)},
		{"not_our_scheme", "https://evil.example/" + base64.RawURLEncoding.EncodeToString(valid)},
		{"wrong_version_byte", qrString(badVersion)},
		{"truncated_body", qrString(valid[:len(valid)-8])}, // chops into the secret
		{"truncated_header", qrString([]byte{QRVersion})},  // len < 3
		{"flag_set_missing_trailer", qrString(flagSetNoTrailer)},
		{"trailing_splice_extra_bytes", qrString(spliceTrailing)},
		{"static_trailer_wrong_len", qrString(staticWrongLen)},
		{"oversized_garbage", qrString(bytes.Repeat([]byte{0xFF}, 300))}, // >200 bytes, fails version
		{"invalid_base64", QRPrefix + "!!!!not-base64!!!!"},
		{"empty_body", QRPrefix},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			// A panic on hostile input is itself a fail-open bug; recover so the
			// suite reports it as a failure rather than crashing.
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("DecodeQR panicked on hostile input: %v", r)
				}
			}()
			got, err := DecodeQR(tc.input)
			if !errors.Is(err, ErrQRMalformed) {
				t.Fatalf("DecodeQR err = %v, want ErrQRMalformed", err)
			}
			// Nothing usable must be returned on the error path (fail closed).
			if got.MachineStaticPub != nil || got.RelayURL != "" {
				t.Errorf("DecodeQR returned partial payload on the error path: %+v", got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// PR-H1 property 2: a relay / on-path peer WITHOUT the QR secret (PSK) cannot
// forge an accept. Tampering the authenticated decision frame, truncating it, or
// dropping it leaves the device with an error, a nil outcome, and no pin; and a
// peer that lacks the secret cannot even complete the handshake.
// ---------------------------------------------------------------------------

func TestPairing_ForgedAcceptWithoutSecretFailsClosed(t *testing.T) {
	// The device's Recv order is: [1] msg2, [2] the machine's decision frame.
	// Tampering / truncating / dropping Recv #2 models an on-path relay that
	// cannot mint a valid decision (it lacks the Noise transport keys).
	newRun := func(t *testing.T, mutateDeviceRecv2 func([]byte) ([]byte, error)) (*MachineOutcome, error, *DeviceOutcome, error) {
		mID, _ := crypto.GenerateIdentity()
		dID, _ := crypto.GenerateIdentity()
		rid := fill16(0x81)
		secret := fill32(0x82)

		mEnd, dEnd := newRendezvousPipe()
		tampered := &recvMutator{RendezvousTransport: dEnd, target: 2, mutate: mutateDeviceRecv2}
		m := NewMachine(newMachineParams(mID, secret, rid, acceptConfirm))
		dp := newDeviceParams(dID, secret, rid)
		return drivePairCancel(t, m, dp, mEnd, tampered)
	}

	t.Run("tampered_decision_frame", func(t *testing.T) {
		mo, mErr, do, dErr := newRun(t, flipLast)
		// The real machine affirmatively accepted...
		if mErr != nil || mo == nil {
			t.Fatalf("machine leg: mo=%v mErr=%v; want a clean accept", mo, mErr)
		}
		// ...yet a device fed a tampered decision cannot be tricked into accepting.
		if dErr == nil {
			t.Fatal("device accepted a tampered decision frame; forgeable accept")
		}
		if do != nil {
			t.Errorf("device produced an outcome from a tampered decision: %+v", do)
		}
	})

	t.Run("truncated_decision_frame", func(t *testing.T) {
		_, _, do, dErr := newRun(t, dropLast)
		if dErr == nil || do != nil {
			t.Fatalf("device did not fail closed on a truncated decision: do=%v dErr=%v", do, dErr)
		}
	})

	t.Run("missing_decision_frame", func(t *testing.T) {
		_, _, do, dErr := newRun(t, func([]byte) ([]byte, error) {
			return nil, errors.New("relay dropped the decision frame")
		})
		if dErr == nil || do != nil {
			t.Fatalf("device did not fail closed on a missing decision: do=%v dErr=%v", do, dErr)
		}
	})

	t.Run("wrong_psk_cannot_complete", func(t *testing.T) {
		// A peer that lacks the QR secret runs the device leg with a DIFFERENT PSK.
		// The XXpsk0 transcript diverges, msg2 fails AEAD, and the handshake never
		// completes -> no forged accept is even reachable.
		mID, _ := crypto.GenerateIdentity()
		dID, _ := crypto.GenerateIdentity()
		rid := fill16(0x83)

		m := NewMachine(newMachineParams(mID, fill32(0x84), rid, acceptConfirm))
		dp := newDeviceParams(dID, fill32(0x99), rid) // wrong / absent secret
		mEnd, dEnd := newRendezvousPipe()
		mo, _, do, dErr := drivePairCancel(t, m, dp, mEnd, dEnd)
		if dErr == nil || do != nil {
			t.Fatalf("device without the secret completed pairing: do=%v dErr=%v", do, dErr)
		}
		if mo != nil {
			t.Errorf("machine produced an outcome against a secretless peer: %+v", mo)
		}
	})
}

// ---------------------------------------------------------------------------
// PR-H1 property 3: a malformed handshake payload fails closed (no panic). Two
// angles: (a) the payload decoders reject crafted bytes with errMalformedPayload;
// (b) a corrupt msg2 / msg3 AEAD frame aborts the leg with no outcome.
// ---------------------------------------------------------------------------

func TestPairing_MalformedPayloadDecodeFailsClosed(t *testing.T) {
	machineDecode := func(b []byte) error { _, err := decodeMachinePayload(b); return err }
	deviceDecode := func(b []byte) error { _, err := decodeDevicePayload(b); return err }

	validMach := encodeMachinePayload(MachinePayload{
		Hostname: "h", MachineRoutingID: []byte("r"), MachineRelayAuthPub: []byte("a"),
		RecipientPub: []byte("p"), EpochID: 7,
	})
	validDev := encodeDevicePayload(DevicePayload{
		DeviceName: "d", DeviceRoutingID: []byte("r"), DeviceRelayAuthPub: []byte("a"),
		RecipientPub: []byte("p"),
	})

	// A length prefix that claims far more than the buffer holds (readField ok=false).
	overflow := binary.BigEndian.AppendUint32(nil, 0xFFFFFFFF)
	overflow = append(overflow, 0x01, 0x02)

	cases := []struct {
		name   string
		in     []byte
		decode func([]byte) error
	}{
		{"machine_nil", nil, machineDecode},
		{"machine_empty", []byte{}, machineDecode},
		{"machine_one_field_then_truncated", appendField(nil, []byte("host")), machineDecode},
		{"machine_length_overflow", overflow, machineDecode},
		{"machine_missing_epoch", validMach[:len(validMach)-4], machineDecode},                      // 4 fields, 0 trailing != 4
		{"machine_trailing_splice", append(append([]byte(nil), validMach...), 0x00), machineDecode}, // 5 trailing != 4
		{"device_nil", nil, deviceDecode},
		{"device_three_fields", appendField(appendField(appendField(nil, []byte("d")), []byte("r")), []byte("a")), deviceDecode},
		{"device_length_overflow", overflow, deviceDecode},
		{"device_trailing_splice", append(append([]byte(nil), validDev...), 0xEE), deviceDecode}, // trailing != 0
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			err := func() (err error) {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("payload decode panicked on crafted bytes: %v", r)
					}
				}()
				return tc.decode(tc.in)
			}()
			if !errors.Is(err, errMalformedPayload) {
				t.Fatalf("decode err = %v, want errMalformedPayload", err)
			}
		})
	}
}

func TestPairing_CorruptHandshakeMessageFailsClosed(t *testing.T) {
	// msg2 is the device's Recv #1; msg3 is the machine's Recv #2. Flipping the
	// final AEAD tag byte of either aborts that leg with an error and no outcome.
	t.Run("corrupt_msg2_device_aborts", func(t *testing.T) {
		mID, _ := crypto.GenerateIdentity()
		dID, _ := crypto.GenerateIdentity()
		rid := fill16(0x85)
		secret := fill32(0x86)

		mEnd, dEnd := newRendezvousPipe()
		corrupt := &recvMutator{RendezvousTransport: dEnd, target: 1, mutate: flipLast}
		m := NewMachine(newMachineParams(mID, secret, rid, acceptConfirm))
		dp := newDeviceParams(dID, secret, rid)
		_, _, do, dErr := drivePairCancel(t, m, dp, mEnd, corrupt)
		if dErr == nil || do != nil {
			t.Fatalf("device did not fail closed on a corrupt msg2: do=%v dErr=%v", do, dErr)
		}
	})

	t.Run("corrupt_msg3_machine_aborts", func(t *testing.T) {
		mID, _ := crypto.GenerateIdentity()
		dID, _ := crypto.GenerateIdentity()
		rid := fill16(0x87)
		secret := fill32(0x88)

		mEnd, dEnd := newRendezvousPipe()
		corrupt := &recvMutator{RendezvousTransport: mEnd, target: 2, mutate: flipLast}
		m := NewMachine(newMachineParams(mID, secret, rid, acceptConfirm))
		dp := newDeviceParams(dID, secret, rid)
		mo, mErr, _, _ := drivePairCancel(t, m, dp, corrupt, dEnd)
		if mErr == nil || mo != nil {
			t.Fatalf("machine did not fail closed on a corrupt msg3: mo=%v mErr=%v", mo, mErr)
		}
	})
}

// ---------------------------------------------------------------------------
// PR-H1 property 4: a peer that aborts mid-handshake (transport error before
// msg3 reaches the machine) yields no pin, no outcome, and — because the
// handshake never completed — does NOT consume the single-use secret, so the
// machine may retry.
// ---------------------------------------------------------------------------

func TestPairing_PeerAbortBeforeMsg3LeavesSecretRetryable(t *testing.T) {
	mID, _ := crypto.GenerateIdentity()
	dID, _ := crypto.GenerateIdentity()
	rid := fill16(0x89)
	secret := fill32(0x8A)

	mEnd, dEnd := newRendezvousPipe()
	// The device aborts on its 2nd Send (msg3): msg1 + msg2 exchange, then the
	// device dies before msg3 reaches the machine.
	aborting := &sendFailer{RendezvousTransport: dEnd, target: 2, err: errors.New("device aborted before msg3")}
	m := NewMachine(newMachineParams(mID, secret, rid, acceptConfirm))
	dp := newDeviceParams(dID, secret, rid)

	mo, mErr, do, dErr := drivePairCancel(t, m, dp, mEnd, aborting)
	if mErr == nil || mo != nil {
		t.Fatalf("machine leg: mo=%v mErr=%v; want a fail-closed abort with no outcome", mo, mErr)
	}
	if dErr == nil || do != nil {
		t.Fatalf("device leg: do=%v dErr=%v; want the abort surfaced with no outcome", do, dErr)
	}

	// The secret was NOT consumed: a fresh Pair on the same Machine is not refused
	// with ErrSecretConsumed (a not-yet-completed handshake is retryable). The
	// retry's own transport has no peer, so it errors for a DIFFERENT reason.
	_, retryErr := m.Pair(context.Background(), &refusingRendezvous{createErr: nil})
	if errors.Is(retryErr, ErrSecretConsumed) {
		t.Fatalf("an aborted (incomplete) handshake consumed the single-use secret: %v", retryErr)
	}
}

// ---------------------------------------------------------------------------
// PR-H1 property 5: a QR that pins a machine_static_pub NOT matching the
// machine's real Noise static makes the device abort with ErrPeerStaticMismatch
// and pin nothing.
// ---------------------------------------------------------------------------

func TestPairing_QRPinnedMachineStaticMismatchAborts(t *testing.T) {
	mID, _ := crypto.GenerateIdentity()
	dID, _ := crypto.GenerateIdentity()
	wrong, _ := crypto.GenerateIdentity() // a static that is NOT the real machine's
	rid := fill16(0x8B)
	secret := fill32(0x8C)

	mEnd, dEnd := newRendezvousPipe()
	m := NewMachine(newMachineParams(mID, secret, rid, acceptConfirm))
	dp := newDeviceParams(dID, secret, rid)
	dp.MachineStaticPub = wrong.NoiseStaticPublic() // QR-pinned to the wrong machine

	_, _, do, dErr := drivePairCancel(t, m, dp, mEnd, dEnd)
	if !errors.Is(dErr, crypto.ErrPeerStaticMismatch) {
		t.Fatalf("device err = %v, want crypto.ErrPeerStaticMismatch", dErr)
	}
	if do != nil {
		t.Errorf("device pinned an outcome despite a machine-static mismatch: %+v", do)
	}
}
