package crypto

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/flynn/noise"
)

// NoiseSuite is the exact live-transport suite. SHA-256 (not BLAKE2s) is
// mandatory for Swift/CryptoKit interop (R-CRY.4).
const NoiseSuite = "Noise_XX_25519_ChaChaPoly_SHA256"

// Rekey thresholds (R-CRY.7): each direction rekeys after 1 GiB or 15 minutes.
const (
	RekeyAfterBytes    = 1 << 30
	RekeyAfterDuration = 15 * time.Minute
)

var (
	// ErrPeerStaticMismatch aborts a handshake whose peer static is not the
	// pinned value (R-CRY.6 — authenticated, not TOFU).
	ErrPeerStaticMismatch = errors.New("crypto: peer static key does not match pinned value")
	// ErrNoStatic is returned when a config omits the required static keypair.
	ErrNoStatic = errors.New("crypto: noise config requires a static keypair")
	// ErrNoPin is returned when a LIVE session is constructed without a valid
	// 32-byte peer-static pin. "Unpinned" is an explicit opt-in (AllowUnpinnedPeer)
	// used only by pairing, never by a live session (F1 — no fail-open).
	ErrNoPin = errors.New("crypto: live session requires a 32-byte peer-static pin")
	// ErrBadPSK is returned when a supplied PSK is not exactly 32 bytes.
	ErrBadPSK = errors.New("crypto: preshared key must be 32 bytes")
	// ErrHandshakeDone is returned by handshake calls after completion.
	ErrHandshakeDone = errors.New("crypto: handshake already complete")
	// ErrNotEstablished is returned by transport calls before completion.
	ErrNotEstablished = errors.New("crypto: transport not established")
	// ErrUnpinnedRequiresPSK forbids an unpinned session without a PSK: only
	// pairing (which always carries a 32-byte PSK) may run unpinned, so a live
	// session cannot opt out of static pinning (F1 — mechanically pairing-only).
	ErrUnpinnedRequiresPSK = errors.New("crypto: AllowUnpinnedPeer requires a 32-byte PSK (pairing only)")
	// ErrRekeyRequired is returned by Encrypt once a direction crosses its rekey
	// threshold; the transport MUST perform a coordinated Rekey() before more
	// traffic flows on the old key (R-CRY.7 / F7 — enforced, not advisory).
	ErrRekeyRequired = errors.New("crypto: rekey required before further traffic")
)

// noiseSuite is the single shared cipher-suite instance for the package.
var noiseSuite = noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashSHA256)

// NoiseStatic is an opaque handshake handle for an X25519 static keypair. It
// carries the private scalar for the DH but exposes no accessor that returns it,
// and redacts the private scalar under every fmt verb (F2 — see Format/String).
type NoiseStatic struct {
	dh noise.DHKey
}

func newNoiseStatic(priv, pub [32]byte) *NoiseStatic {
	return &NoiseStatic{dh: noise.DHKey{
		Private: append([]byte(nil), priv[:]...),
		Public:  append([]byte(nil), pub[:]...),
	}}
}

// String is a redacted representation: the public-key fingerprint only, never
// the private scalar. Value receiver so an embedded NoiseStatic (value or
// pointer) is covered too.
func (s NoiseStatic) String() string {
	return fmt.Sprintf("crypto.NoiseStatic{pub:%s}", fingerprint(s.dh.Public))
}

// GoString backs %#v so it cannot dump the private field.
func (s NoiseStatic) GoString() string { return s.String() }

// Format routes EVERY fmt verb (%v %+v %#v %s %x %q) through the redacted
// String, so no verb can spill the DH private scalar.
func (s NoiseStatic) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, s.String()) }

// NoiseConfig parameterises a single live/pairing XX session.
type NoiseConfig struct {
	Initiator bool
	Static    *NoiseStatic
	// PeerStatic is the pinned peer static. A live session (AllowUnpinnedPeer
	// false) requires exactly 32 bytes; the handshake aborts on mismatch.
	PeerStatic []byte
	// AllowUnpinnedPeer opts into learning the peer static instead of pinning
	// it. This is the ONLY path that may run without a pin and is used solely by
	// pairing (the SAS + desktop confirm are the out-of-band gate). A live
	// session never sets it.
	AllowUnpinnedPeer bool
	// PSK, when set, configures Noise XXpsk0 (PresharedKeyPlacement 0). This is
	// the pairing seam (R-PAIR.3); a live session leaves it nil.
	PSK      []byte
	Prologue []byte // binds protocol/role/route (R-CRY.5)

	// randReader is a TEST-ONLY ephemeral source (crypto/rand when nil). It is
	// unexported so no production caller can override the RNG (F6).
	randReader io.Reader
}

// NoiseSession is one Noise_XX session: handshake first (WriteMessage/
// ReadMessage), then transport (Encrypt/Decrypt). It retains no resumption
// state — the handshake state is dropped at completion (R-CRY.8).
type NoiseSession struct {
	hs        *noise.HandshakeState
	initiator bool
	pinned    []byte

	send     *noise.CipherState
	recv     *noise.CipherState
	peer     []byte
	binding  []byte
	complete bool

	// Rekey accounting (R-CRY.7 / F7): bytes moved per direction and the time
	// transport was established. now/rekeyBytes/rekeyDur are test seams (nil/0
	// select the real clock and the RekeyAfter* thresholds).
	sendBytes   uint64
	recvBytes   uint64
	established time.Time
	now         func() time.Time
	rekeyBytes  uint64
	rekeyDur    time.Duration
}

// String/GoString/Format redact NoiseSession and NoiseConfig: both transitively
// hold private key material (flynn's HandshakeState and transport CipherStates,
// the static keypair, and the PSK), so no fmt verb (%v %+v %#v %s) may print
// their contents (F2). Value receivers so both a value and a pointer are covered.
func (NoiseSession) String() string               { return "crypto.NoiseSession{redacted}" }
func (s NoiseSession) GoString() string           { return s.String() }
func (s NoiseSession) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, s.String()) }
func (NoiseConfig) String() string                { return "crypto.NoiseConfig{redacted}" }
func (c NoiseConfig) GoString() string            { return c.String() }
func (c NoiseConfig) Format(f fmt.State, _ rune)  { _, _ = io.WriteString(f, c.String()) }

// NewNoise starts an XX session. The pinned PeerStatic is held here and checked
// against the value learned on the wire; flynn/noise itself is not given a
// pre-message static (XX transmits it, and a preset value would be rejected).
func NewNoise(cfg NoiseConfig) (*NoiseSession, error) {
	if cfg.Static == nil {
		return nil, ErrNoStatic
	}
	// A live session MUST pin a valid 32-byte peer static before the handshake;
	// only pairing may run unpinned, and then explicitly (F1).
	if !cfg.AllowUnpinnedPeer && len(cfg.PeerStatic) != 32 {
		return nil, ErrNoPin
	}
	if len(cfg.PeerStatic) != 0 && len(cfg.PeerStatic) != 32 {
		return nil, ErrNoPin
	}
	if cfg.PSK != nil && len(cfg.PSK) != 32 {
		return nil, ErrBadPSK
	}
	// Unpinned is mechanically pairing-only: it requires a 32-byte PSK, so a live
	// session (no PSK) can never establish against an unpinned/substituted static.
	if cfg.AllowUnpinnedPeer && len(cfg.PSK) != 32 {
		return nil, ErrUnpinnedRequiresPSK
	}
	rng := cfg.randReader
	if rng == nil {
		rng = rand.Reader
	}
	ncfg := noise.Config{
		CipherSuite:   noiseSuite,
		Random:        rng,
		Pattern:       noise.HandshakeXX,
		Initiator:     cfg.Initiator,
		Prologue:      cfg.Prologue,
		StaticKeypair: cfg.Static.dh,
	}
	if len(cfg.PSK) > 0 {
		ncfg.PresharedKey = cfg.PSK
		ncfg.PresharedKeyPlacement = 0
	}
	hs, err := noise.NewHandshakeState(ncfg)
	if err != nil {
		return nil, err
	}
	return &NoiseSession{
		hs:        hs,
		initiator: cfg.Initiator,
		pinned:    append([]byte(nil), cfg.PeerStatic...),
	}, nil
}

// WriteMessage produces the next handshake message.
func (s *NoiseSession) WriteMessage(payload []byte) ([]byte, error) {
	if s.hs == nil {
		return nil, ErrHandshakeDone
	}
	msg, cs0, cs1, err := s.hs.WriteMessage(nil, payload)
	if err != nil {
		return nil, err
	}
	if cs0 != nil {
		if err := s.establish(cs0, cs1); err != nil {
			return nil, err
		}
	}
	return msg, nil
}

// ReadMessage consumes the next handshake message. As soon as the peer static
// is learned it is compared to the pinned value; a mismatch aborts before the
// session reaches transport mode (R-CRY.6). The comparison is constant-time.
func (s *NoiseSession) ReadMessage(message []byte) ([]byte, error) {
	if s.hs == nil {
		return nil, ErrHandshakeDone
	}
	out, cs0, cs1, err := s.hs.ReadMessage(nil, message)
	if err != nil {
		return nil, err
	}
	// XX transmits the peer static in msg2 (to the initiator) / msg3 (to the
	// responder), so PeerStatic() is empty on earlier reads. Compare only once
	// it is actually present; the establish() guard below guarantees a pinned
	// session cannot complete without a match, so this is not fail-open.
	if len(s.pinned) > 0 {
		if ps := s.hs.PeerStatic(); len(ps) == 32 && subtle.ConstantTimeCompare(ps, s.pinned) != 1 {
			s.hs = nil // fail closed: never reach transport mode
			return nil, ErrPeerStaticMismatch
		}
	}
	if cs0 != nil {
		if err := s.establish(cs0, cs1); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// establish captures the channel binding + peer static and splits transport
// cipher states, then drops the handshake state (no resumption secret retained).
func (s *NoiseSession) establish(cs0, cs1 *noise.CipherState) error {
	s.peer = append([]byte(nil), s.hs.PeerStatic()...)
	// Defense in depth (F1): a pinned session never reaches transport unless the
	// peer static was actually transmitted (len 32) and matches. XX always
	// transmits it before completion, so this only fires on a pattern/impl
	// regression — and then it fails closed with an explicit error.
	if len(s.pinned) > 0 && (len(s.peer) != 32 || subtle.ConstantTimeCompare(s.peer, s.pinned) != 1) {
		s.hs = nil
		return ErrPeerStaticMismatch
	}
	s.binding = append([]byte(nil), s.hs.ChannelBinding()...)
	if s.initiator {
		s.send, s.recv = cs0, cs1
	} else {
		s.send, s.recv = cs1, cs0
	}
	s.complete = true
	s.established = s.clockNow()
	s.hs = nil
	return nil
}

// clockNow reads the injectable clock (time.Now when unset).
func (s *NoiseSession) clockNow() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// HandshakeComplete reports whether transport mode has been reached.
func (s *NoiseSession) HandshakeComplete() bool { return s.complete }

// PeerStatic returns the peer's authenticated static public key.
func (s *NoiseSession) PeerStatic() []byte { return append([]byte(nil), s.peer...) }

// ChannelBinding returns the handshake hash, identical on both clean ends.
func (s *NoiseSession) ChannelBinding() []byte { return append([]byte(nil), s.binding...) }

// Suite returns the pinned suite string.
func (s *NoiseSession) Suite() string { return NoiseSuite }

// Encrypt seals a transport frame under the sending cipher state.
func (s *NoiseSession) Encrypt(plaintext []byte) ([]byte, error) {
	if !s.complete {
		return nil, ErrNotEstablished
	}
	// Enforce the rekey threshold (F7): once a direction is due, no more traffic
	// flows on the old key until the transport performs a coordinated Rekey().
	if s.RekeyDue() {
		return nil, ErrRekeyRequired
	}
	ct, err := s.send.Encrypt(nil, nil, plaintext)
	if err != nil {
		return nil, err
	}
	s.sendBytes += uint64(len(plaintext))
	return ct, nil
}

// Decrypt opens a transport frame under the receiving cipher state. A replayed
// or reordered frame fails the AEAD (the per-state nonce already advanced).
func (s *NoiseSession) Decrypt(ciphertext []byte) ([]byte, error) {
	if !s.complete {
		return nil, ErrNotEstablished
	}
	pt, err := s.recv.Decrypt(nil, nil, ciphertext)
	if err != nil {
		return nil, err
	}
	s.recvBytes += uint64(len(pt))
	return pt, nil
}

// RekeyDue reports whether either direction has crossed its byte budget or the
// session has crossed its time budget (R-CRY.7). The live transport MUST check
// this and drive a COORDINATED rekey (both ends call Rekey) before continuing;
// the crypto layer cannot rekey unilaterally without desyncing the peer.
func (s *NoiseSession) RekeyDue() bool {
	if !s.complete {
		return false
	}
	limitBytes := s.rekeyBytes
	if limitBytes == 0 {
		limitBytes = RekeyAfterBytes
	}
	limitDur := s.rekeyDur
	if limitDur == 0 {
		limitDur = RekeyAfterDuration
	}
	if s.sendBytes >= limitBytes || s.recvBytes >= limitBytes {
		return true
	}
	return s.clockNow().Sub(s.established) >= limitDur
}

// Rekey rotates both directions' key streams and resets the rekey accounting;
// peers must rekey in coordination.
func (s *NoiseSession) Rekey() {
	if s.send != nil {
		s.send.Rekey()
	}
	if s.recv != nil {
		s.recv.Rekey()
	}
	s.sendBytes = 0
	s.recvBytes = 0
	s.established = s.clockNow()
}

// appendLenField appends a 4-byte big-endian length prefix then the field, so
// no two distinct (field, field) pairs share an encoding (F11 — no splicing).
func appendLenField(b, f []byte) []byte {
	b = binary.BigEndian.AppendUint32(b, uint32(len(f)))
	return append(b, f...)
}

// LivePrologue binds the live protocol tag and both routing ids (R-CRY.5). Each
// routing id is length-prefixed so ("a","bc") cannot collide with ("ab","c").
func LivePrologue(machineRoutingID, deviceRoutingID []byte) []byte {
	b := []byte("swarm-remote/1 live")
	b = appendLenField(b, machineRoutingID)
	b = appendLenField(b, deviceRoutingID)
	return b
}

// PairPrologue binds the pairing protocol tag and the length-prefixed
// rendezvous id.
func PairPrologue(rendezvousID []byte) []byte {
	return appendLenField([]byte("swarm-remote/1 pair"), rendezvousID)
}
