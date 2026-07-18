package crypto

import (
	"bytes"
	"crypto/rand"
	"errors"
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
	// ErrHandshakeDone is returned by handshake calls after completion.
	ErrHandshakeDone = errors.New("crypto: handshake already complete")
	// ErrNotEstablished is returned by transport calls before completion.
	ErrNotEstablished = errors.New("crypto: transport not established")
)

// noiseSuite is the single shared cipher-suite instance for the package.
var noiseSuite = noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashSHA256)

// NoiseStatic is an opaque handshake handle for an X25519 static keypair. It
// carries the private scalar for the DH but exposes no accessor that returns it.
type NoiseStatic struct {
	dh noise.DHKey
}

func newNoiseStatic(priv, pub [32]byte) *NoiseStatic {
	return &NoiseStatic{dh: noise.DHKey{
		Private: append([]byte(nil), priv[:]...),
		Public:  append([]byte(nil), pub[:]...),
	}}
}

// NoiseConfig parameterises a single live/pairing XX session.
type NoiseConfig struct {
	Initiator  bool
	Static     *NoiseStatic
	PeerStatic []byte    // pinned peer static; the handshake aborts on mismatch
	Prologue   []byte    // binds protocol/role/route (R-CRY.5)
	Rand       io.Reader // ephemeral source; crypto/rand when nil
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
}

// NewNoise starts an XX session. The pinned PeerStatic is held here and checked
// against the value learned on the wire; flynn/noise itself is not given a
// pre-message static (XX transmits it, and a preset value would be rejected).
func NewNoise(cfg NoiseConfig) (*NoiseSession, error) {
	if cfg.Static == nil {
		return nil, ErrNoStatic
	}
	rng := cfg.Rand
	if rng == nil {
		rng = rand.Reader
	}
	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:   noiseSuite,
		Random:        rng,
		Pattern:       noise.HandshakeXX,
		Initiator:     cfg.Initiator,
		Prologue:      cfg.Prologue,
		StaticKeypair: cfg.Static.dh,
	})
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
		s.establish(cs0, cs1)
	}
	return msg, nil
}

// ReadMessage consumes the next handshake message. As soon as the peer static
// is learned it is compared to the pinned value; a mismatch aborts before the
// session reaches transport mode (R-CRY.6).
func (s *NoiseSession) ReadMessage(message []byte) ([]byte, error) {
	if s.hs == nil {
		return nil, ErrHandshakeDone
	}
	out, cs0, cs1, err := s.hs.ReadMessage(nil, message)
	if err != nil {
		return nil, err
	}
	if ps := s.hs.PeerStatic(); len(ps) > 0 && len(s.pinned) > 0 && !bytes.Equal(ps, s.pinned) {
		s.hs = nil // fail closed: never reach transport mode
		return nil, ErrPeerStaticMismatch
	}
	if cs0 != nil {
		s.establish(cs0, cs1)
	}
	return out, nil
}

// establish captures the channel binding + peer static and splits transport
// cipher states, then drops the handshake state (no resumption secret retained).
func (s *NoiseSession) establish(cs0, cs1 *noise.CipherState) {
	s.peer = append([]byte(nil), s.hs.PeerStatic()...)
	s.binding = append([]byte(nil), s.hs.ChannelBinding()...)
	if s.initiator {
		s.send, s.recv = cs0, cs1
	} else {
		s.send, s.recv = cs1, cs0
	}
	s.complete = true
	s.hs = nil
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
	return s.send.Encrypt(nil, nil, plaintext)
}

// Decrypt opens a transport frame under the receiving cipher state. A replayed
// or reordered frame fails the AEAD (the per-state nonce already advanced).
func (s *NoiseSession) Decrypt(ciphertext []byte) ([]byte, error) {
	if !s.complete {
		return nil, ErrNotEstablished
	}
	return s.recv.Decrypt(nil, nil, ciphertext)
}

// Rekey rotates both directions' key streams; peers must rekey in coordination.
func (s *NoiseSession) Rekey() {
	if s.send != nil {
		s.send.Rekey()
	}
	if s.recv != nil {
		s.recv.Rekey()
	}
}

// LivePrologue binds the live protocol tag and both routing ids (R-CRY.5).
func LivePrologue(machineRoutingID, deviceRoutingID []byte) []byte {
	b := []byte("swarm-remote/1 live")
	b = append(b, machineRoutingID...)
	b = append(b, deviceRoutingID...)
	return b
}

// PairPrologue binds the pairing protocol tag and the 16-byte rendezvous id.
func PairPrologue(rendezvousID []byte) []byte {
	return append([]byte("swarm-remote/1 pair"), rendezvousID...)
}
