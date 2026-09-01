package phonecore

// The INSTALLATION KEY (PG-AUTH-1/2): the P-256 key every installation-control request to
// the push gateway is signed with, and the one thing that makes an installation id mean this
// phone rather than whoever presents it.
//
// Android production does NOT use the implementation in this file. It installs the
// reverse-bound PushInstallationSigner implemented by AndroidInstallationSigner, whose P-256
// private key is non-exportable in Android Keystore. PreparePlatformInstallationSigner removes
// any unregistered scalar left by a pre-production build before that provider is installed.
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
// This Go signer remains for non-Android callers and protocol tests. A mobile build with no
// platform provider does not call it merely to reach an attestation refusal: the parked mobile
// path owns no private scalar at all.

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
)

// installationSigner is InstallationSigner over one P-256 key. It is immutable once built,
// so it is safe to hand to a GatewayClient used from several goroutines.
type installationSigner struct {
	key *ecdsa.PrivateKey
}

// PreparePlatformInstallationSigner removes the pre-production exportable installation
// scalar before Android installs its non-exportable Keystore signer. It refuses once any
// registration authority or outcome-unknown request exists: replacing that signer would
// orphan an installation the old key alone can authenticate.
func (c *Core) PreparePlatformInstallationSigner(public []byte) error {
	if !validPlatformInstallationPublicKey(public) {
		return errors.New("phonecore: platform installation public key is not canonical P-256 SEC1")
	}
	c.regMu.Lock()
	defer c.regMu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	bound := c.push.data.InstallationPublicKey
	if len(bound) != 0 {
		if len(c.push.data.InstallationKey) != 0 || !bytes.Equal(bound, public) {
			return errors.New("phonecore: cannot replace the signer of an existing or pending installation")
		}
		return nil
	}

	// d958's first platform build did not persist the public half beside an accepted
	// installation. Absence of the old exportable scalar proves this sealed state was in
	// platform mode; bind the Keystore key once. An installed state that still carries the
	// old scalar is a known different authority and must never be overwritten.
	if c.push.data.InstallationID != "" {
		if len(c.push.data.InstallationKey) != 0 {
			return errors.New("phonecore: cannot replace the signer of an existing installation")
		}
		return c.persistPlatformInstallationPublicKeyLocked(public)
	}

	// An outcome-unknown legacy registration already names the exact public key in its
	// durable request body. Rebind only that key; accepting the current Keystore alias by
	// fiat could orphan an installation the gateway created before the response was lost.
	if pending := c.push.data.PendingRegister; pending != nil {
		var body struct {
			InstallationPublicKey string `json:"installation_public_key"`
		}
		if err := json.Unmarshal(pending.Body, &body); err != nil {
			return fmt.Errorf("phonecore: decode pending registration authority: %w", err)
		}
		pendingPublic, err := base64.RawURLEncoding.DecodeString(body.InstallationPublicKey)
		if err != nil || !validPlatformInstallationPublicKey(pendingPublic) || !bytes.Equal(pendingPublic, public) {
			return errors.New("phonecore: platform signer does not match pending registration authority")
		}
		return c.persistPlatformInstallationPublicKeyLocked(public)
	}

	legacy := append([]byte(nil), c.push.data.InstallationKey...)
	c.push.data.InstallationKey = nil
	if err := c.persistPlatformInstallationPublicKeyLocked(public); err != nil {
		if !atomicWriteCommitted(err) {
			c.push.data.InstallationKey = legacy
		}
		return fmt.Errorf("phonecore: replace unregistered legacy installation key: %w", err)
	}
	return nil
}

func (c *Core) persistPlatformInstallationPublicKeyLocked(public []byte) error {
	previous := append([]byte(nil), c.push.data.InstallationPublicKey...)
	c.push.data.InstallationPublicKey = append([]byte(nil), public...)
	if err := c.push.persist(); err != nil {
		if !atomicWriteCommitted(err) {
			c.push.data.InstallationPublicKey = previous
		}
		return err
	}
	return nil
}

func validPlatformInstallationPublicKey(public []byte) bool {
	if len(public) != 65 || public[0] != 4 {
		return false
	}
	x, y := elliptic.Unmarshal(elliptic.P256(), public)
	return x != nil && y != nil && bytes.Equal(elliptic.Marshal(elliptic.P256(), x, y), public)
}

// InstallationSigner returns this phone's installation signer, MINTING AND PERSISTING the
// key on first use. The key is durable: an installation id is bound to a public key at the
// gateway, so a phone that regenerated it would be an installation it can no longer
// authenticate as -- every rotation refused, and the 180-day inactivity floor running.
func (c *Core) InstallationSigner() (InstallationSigner, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.push.data.InstallationPublicKey) != 0 {
		return nil, errors.New("phonecore: platform installation authority requires its external signer")
	}

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
