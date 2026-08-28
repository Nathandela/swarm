// Package upgrade is the update transaction (lifecycle plan R2): resolve the
// latest release, decide whether this install may act, download, verify, and
// STAGE -- never activate. Activation (rename + re-exec + converge) is R3's;
// everything here is safe to run on a machine doing live work.
package upgrade

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// releasePublicKeys are the ed25519 public keys a release's checksums.txt.sig
// may verify against, hex-encoded. TWO SLOTS BY DESIGN (rotation): the verifying
// key ships inside the artifact it verifies, so a compromised key can only be
// replaced by an update signed with a key the fleet already trusts -- current
// signs today, next is the standby that a rotation release starts signing with
// while old binaries still carry it. The private halves live only in the release
// pipeline's secret (SWARM_RELEASE_SIGNING_KEY; scripts/signchecksums).
//
// THREAT MODEL, stated honestly (committee B3/H2): this defends against a
// swapped release asset, a stolen publish token, and a hostile mirror -- anyone
// who can write to the release but not to the signing secret. It does NOT
// defend against a compromised release workflow, which holds the secret and
// would sign what it builds.
var releasePublicKeys = []string{
	"d7938ebe67f5cb7ff202724352a7890cd0af8ca422c22e3e7af32b6284e7885a", // current (2026-08-28)
}

// VerifyChecksums reports whether sig (the checksums.txt.sig release asset,
// base64 of a raw ed25519 signature) signs checksums over any trusted key.
func VerifyChecksums(checksums, sig []byte) error {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil {
		return fmt.Errorf("upgrade: checksums signature is not base64: %w", err)
	}
	for _, kh := range releasePublicKeys {
		pub, err := hex.DecodeString(kh)
		if err != nil || len(pub) != ed25519.PublicKeySize {
			continue // a malformed table entry must not mask a valid sibling
		}
		if ed25519.Verify(ed25519.PublicKey(pub), checksums, raw) {
			return nil
		}
	}
	return fmt.Errorf("upgrade: checksums signature verifies against no trusted release key")
}
