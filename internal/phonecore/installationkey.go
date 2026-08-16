package phonecore

// The INSTALLATION KEY (PG-AUTH-1/2): the P-256 key every installation-control request to
// the push gateway is signed with, and the one thing that makes an installation id mean this
// phone rather than whoever presents it.
//
// WHERE IT LIVES, AND THE DEVIATION THAT IS. push-gateway-api.md PG-AUTH-2 asks for an
// ANDROID KEYSTORE key -- hardware-resident, non-exportable, so the private scalar never
// exists as bytes in this process. That needs a new reverse-bound platform interface (a
// Kotlin ECDSA signer, DER -> IEEE P1363 conversion, low-s normalisation on the Java side)
// and is PARKED with the rest of the owner-gated push work.
//
// What ships instead is the custody the REST of this phone's key material already has
// (mobile/keycustody.go): the scalar is generated here, and it is written only into
// push-state.sealed -- AEAD-sealed under the WAKE-tier KEK, which is itself an Android
// Keystore key the Go core never holds (ADR-007 B8/PB-KEY-2). So at rest it is exactly as
// protected as the per-pairing wake keys beside it; what it does NOT have is hardware
// residency while the process is alive. The wake tier is the right tier and not a shortcut:
// registration and rotation must work with no user present, which is the whole reason that
// tier is not authentication-gated.
//
// The gap is real and it is bounded: an attacker with code execution inside this app's
// process can sign installation-control requests, which lets them rotate the FCM token or
// revoke addresses for THIS installation. It buys them nothing about wake CONTENT (a wake is
// a constant over an empty plaintext, sealed under a per-pairing key) and nothing about
// session content (content tier, separate KEK). Closing it is the parked Keystore-signer
// bead.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"fmt"
	"math/big"
)

// installationSigner is InstallationSigner over one P-256 key. It is immutable once built,
// so it is safe to hand to a GatewayClient used from several goroutines.
type installationSigner struct {
	key *ecdsa.PrivateKey
}

// InstallationSigner returns this phone's installation signer, MINTING AND PERSISTING the
// key on first use. The key is durable: an installation id is bound to a public key at the
// gateway, so a phone that regenerated it would be an installation it can no longer
// authenticate as -- every rotation refused, and the 180-day inactivity floor running.
func (c *Core) InstallationSigner() (InstallationSigner, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if der := c.push.data.InstallationKey; len(der) != 0 {
		key, err := x509.ParseECPrivateKey(der)
		if err != nil {
			// FAIL CLOSED. Minting a replacement here would silently orphan the
			// installation this key authenticates, which is the same 180-day orphan
			// PG-REG-2 exists to prevent, reached from the other end.
			return nil, fmt.Errorf("phonecore: installation key is unreadable: %w", err)
		}
		return &installationSigner{key: key}, nil
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("phonecore: mint installation key: %w", err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("phonecore: marshal installation key: %w", err)
	}
	c.push.data.InstallationKey = der
	if err := c.push.persist(); err != nil {
		return nil, err
	}
	return &installationSigner{key: key}, nil
}

// PublicKey is the SEC1 uncompressed P-256 point (65 bytes), which is the encoding spec
// section 3.1's registration body carries and the gateway's own verifier reads.
func (s *installationSigner) PublicKey() []byte {
	pub, err := s.key.PublicKey.ECDH()
	if err != nil {
		// Unreachable: the key is P-256 by construction, both when minted and when parsed
		// back (a non-P-256 key would have failed the curve check in Sign first).
		return nil
	}
	return pub.Bytes()
}

// Sign is PG-AUTH-2's wire contract: ECDSA over SHA-256 of the canonical string, returned as
// IEEE P1363 fixed-width 64-byte r||s with s normalized LOW. The gateway refuses anything
// else -- including the ASN.1 DER shape crypto/ecdsa's own SignASN1 produces -- so the
// conversion is part of the contract and not a formatting preference.
func (s *installationSigner) Sign(canonical []byte) ([]byte, error) {
	if s.key.Curve != elliptic.P256() {
		return nil, errors.New("phonecore: installation key is not on P-256")
	}
	digest := sha256.Sum256(canonical)
	r, sig, err := ecdsa.Sign(rand.Reader, s.key, digest[:])
	if err != nil {
		return nil, err
	}
	// LOW-S NORMALISATION. ECDSA admits both s and n-s for the same message; the gateway
	// pins one so a signature has exactly one valid encoding and cannot be malleated into a
	// second distinct-looking credential.
	n := s.key.Curve.Params().N
	if sig.Cmp(new(big.Int).Rsh(n, 1)) > 0 {
		sig = new(big.Int).Sub(n, sig)
	}
	out := make([]byte, 64)
	r.FillBytes(out[:32])
	sig.FillBytes(out[32:])
	return out, nil
}
