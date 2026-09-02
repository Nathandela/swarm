package swarmmobile

import (
	"errors"
	"fmt"
)

// PushAttestor is Android's Play Integrity Standard API boundary. Go supplies the exact
// 32-byte registration request hash; Kotlin returns the opaque token Google minted for it.
type PushAttestor interface {
	Attest(requestHash []byte) (string, error)
}

// PushInstallationSigner is Android's non-exportable Keystore P-256 authority. PublicKey
// is the 65-byte SEC1 point; Sign returns the fixed 64-byte P1363 low-S signature. No method
// can export private key bytes.
type PushInstallationSigner interface {
	PublicKey() []byte
	Sign(canonical []byte) ([]byte, error)
}

// ConfigurePushRegistration installs Android's two reverse-bound production authorities.
// It must run once, immediately after NewApp and before any token callback. Builds that do
// not call it retain the named, fail-closed foreground-only parked behavior.
func (a *App) ConfigurePushRegistration(attestor PushAttestor, signer PushInstallationSigner) (err error) {
	defer barrier(&err)
	if a == nil || a.core == nil {
		return errNoReceiver
	}
	if attestor == nil || signer == nil {
		return classed(ErrClassInvalidRequest, errors.New(
			"swarmmobile: push registration requires both Play Integrity and Android Keystore providers"))
	}

	a.pushProviderMu.Lock()
	defer func() {
		a.pushProviderMu.Unlock()
		if err == nil {
			a.schedulePendingPairingPushRevokes()
		}
	}()
	a.mu.Lock()
	already := a.pushAttestor != nil || a.pushSigner != nil || a.pushClient != nil
	a.mu.Unlock()
	if already {
		return classed(ErrClassInvalidRequest, errors.New(
			"swarmmobile: push registration providers are already configured or in use"))
	}
	public := signer.PublicKey()
	if len(public) != 65 || public[0] != 4 {
		return classed(ErrClassInvalidRequest, fmt.Errorf(
			"swarmmobile: Android Keystore installation public key is not a 65-byte uncompressed P-256 point"))
	}
	if err := a.core.PreparePlatformInstallationSigner(public); err != nil {
		return err
	}
	a.mu.Lock()
	a.pushAttestor = attestor
	a.pushSigner = signer
	a.mu.Unlock()
	return nil
}
