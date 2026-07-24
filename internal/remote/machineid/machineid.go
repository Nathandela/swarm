// Package machineid implements the machine's pairing identity bundle (A4-1a):
// the Noise-static/recipient X25519 keys, the grant-signing and relay-auth
// Ed25519 keypairs, epoch keys, epoch id, grant seq, hostname, and routing id
// that `swarm remote init` persists and internal/skeleton's pairingConfig
// consumes. It composes internal/remote/crypto's frozen primitives; it never
// modifies that package.
package machineid

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// Material is deterministic construction material for tests/KATs, mirroring
// crypto.KeyMaterial + crypto.NewIdentityFromMaterial's pattern.
type Material struct {
	NoiseStaticPriv [32]byte
	RecipientPriv   [32]byte
	GrantSignPriv   ed25519.PrivateKey
	RelayAuthPriv   ed25519.PrivateKey
	EpochKeys       crypto.EpochKeys
	EpochID         uint32
	GrantSeq        uint64
}

// Identity is the machine's full pairing identity bundle. The Noise-static and
// recipient private scalars have no public accessor (only derived
// public/opaque-handle access), mirroring crypto.Identity's own R-CRY.1
// posture; the grant-signing private DOES have a public accessor because
// enroll.Enroll legitimately needs the raw key to sign a grant.
type Identity struct {
	hostname string

	noiseStaticPriv [32]byte
	recipientPriv   [32]byte
	grantPriv       ed25519.PrivateKey
	relayPriv       ed25519.PrivateKey
	epochKeys       crypto.EpochKeys
	epochID         uint32
	grantSeq        uint64

	cryptoID  *crypto.Identity
	routingID []byte
}

// Generate creates a fresh machine identity: crypto/rand for the two X25519
// privates, ed25519.GenerateKey for the grant-signing and relay-auth
// keypairs, and crypto.NewEpochKeys for the epoch keys.
func Generate(hostname string) (*Identity, error) {
	var m Material
	if _, err := rand.Read(m.NoiseStaticPriv[:]); err != nil {
		return nil, fmt.Errorf("machineid: generate noise-static key: %w", err)
	}
	if _, err := rand.Read(m.RecipientPriv[:]); err != nil {
		return nil, fmt.Errorf("machineid: generate recipient key: %w", err)
	}
	_, grantPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("machineid: generate grant-signing key: %w", err)
	}
	m.GrantSignPriv = grantPriv
	_, relayPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("machineid: generate relay-auth key: %w", err)
	}
	m.RelayAuthPriv = relayPriv
	epochKeys, err := crypto.NewEpochKeys()
	if err != nil {
		return nil, fmt.Errorf("machineid: generate epoch keys: %w", err)
	}
	m.EpochKeys = epochKeys
	m.EpochID = 1
	m.GrantSeq = 1
	return NewFromMaterial(hostname, m), nil
}

// NewFromMaterial builds an identity from explicit material (deterministic,
// no I/O, cannot fail).
func NewFromMaterial(hostname string, m Material) *Identity {
	relayPub := m.RelayAuthPriv.Public().(ed25519.PublicKey)
	// relay.RoutingID always returns valid lowercase hex, so decoding it back
	// to bytes cannot fail.
	routingID, _ := hex.DecodeString(relay.RoutingID(relayPub))
	return &Identity{
		hostname:        hostname,
		noiseStaticPriv: m.NoiseStaticPriv,
		recipientPriv:   m.RecipientPriv,
		grantPriv:       append(ed25519.PrivateKey(nil), m.GrantSignPriv...),
		relayPriv:       append(ed25519.PrivateKey(nil), m.RelayAuthPriv...),
		epochKeys:       m.EpochKeys,
		epochID:         m.EpochID,
		grantSeq:        m.GrantSeq,
		cryptoID:        crypto.NewIdentityFromMaterial(m.NoiseStaticPriv, m.RecipientPriv),
		routingID:       routingID,
	}
}

// NoiseStatic returns the opaque handshake handle for the machine's
// Noise-static key.
func (id *Identity) NoiseStatic() *crypto.NoiseStatic { return id.cryptoID.NoiseStatic() }

// RecipientPublic returns a copy of the 32-byte sealed-box recipient public key.
func (id *Identity) RecipientPublic() []byte { return id.cryptoID.RecipientPublic() }

// GrantSignPublic returns the grant-signing Ed25519 public key.
func (id *Identity) GrantSignPublic() ed25519.PublicKey {
	return id.grantPriv.Public().(ed25519.PublicKey)
}

// GrantSignPrivate returns a copy of the raw grant-signing Ed25519 private
// key. enroll.Enroll needs this to sign a grant.
func (id *Identity) GrantSignPrivate() ed25519.PrivateKey {
	return append(ed25519.PrivateKey(nil), id.grantPriv...)
}

// RelayAuthPublic returns the relay-auth Ed25519 public key.
func (id *Identity) RelayAuthPublic() ed25519.PublicKey {
	return id.relayPriv.Public().(ed25519.PublicKey)
}

// RelayAuthSign signs a relay auth challenge with the machine's relay-auth
// Ed25519 private key, matching relay.ClientAuth.Sign (a plain
// func(challenge []byte) []byte). The gateway builds
// relay.ClientAuth{RelayAuthPub: id.RelayAuthPublic(), Sign: id.RelayAuthSign}.
func (id *Identity) RelayAuthSign(challenge []byte) []byte {
	return ed25519.Sign(id.relayPriv, challenge)
}

// RotateEpoch mints a fresh epoch on revoke (codex#1, ADR-007 2026-07-24): new
// crypto.NewEpochKeys(), the epoch id incremented, and the grant seq reset to 1 --
// mirroring Generate's epoch block. A subsequent Save persists it, so the NEXT
// paired device's grant seals under the new epoch and the revoked device's retained
// old-epoch content key is dead for all future traffic. The fields are unexported
// with no setters, so this MUST be an in-package method.
func (id *Identity) RotateEpoch() error {
	keys, err := crypto.NewEpochKeys()
	if err != nil {
		return fmt.Errorf("machineid: rotate epoch keys: %w", err)
	}
	id.epochKeys = keys
	id.epochID++ // wraps at uint32 max (~4e9 revokes); GrantReceiver wants a higher id
	id.grantSeq = 1
	return nil
}

// EpochKeys returns the wake/content key split for the current epoch.
func (id *Identity) EpochKeys() crypto.EpochKeys { return id.epochKeys }

// EpochID returns the current epoch id.
func (id *Identity) EpochID() uint32 { return id.epochID }

// GrantSeq returns the current grant sequence.
func (id *Identity) GrantSeq() uint64 { return id.grantSeq }

// Hostname returns the machine's hostname.
func (id *Identity) Hostname() string { return id.hostname }

// RoutingID returns a copy of the relay's opaque routing handle for this
// machine, derived from the relay-auth public key (relay.RoutingID).
func (id *Identity) RoutingID() []byte { return append([]byte(nil), id.routingID...) }

// String is a redacted representation: hostname, public-key fingerprints, and
// the routing id only — never the private scalars/keys (R-CRY.1). It backs
// %v, %s, %+v and fmt.Sprint.
func (id *Identity) String() string {
	return fmt.Sprintf(
		"machineid.Identity{hostname:%q noiseStatic:%s recipient:%s grantSign:%s relayAuth:%s routing:%s epoch:%d/%d}",
		id.hostname,
		keyFingerprint(id.cryptoID.NoiseStaticPublic()),
		keyFingerprint(id.cryptoID.RecipientPublic()),
		keyFingerprint(id.GrantSignPublic()),
		keyFingerprint(id.RelayAuthPublic()),
		hex.EncodeToString(id.routingID),
		id.epochID, id.grantSeq,
	)
}

// GoString backs %#v so it, too, cannot dump the private fields.
func (id *Identity) GoString() string { return id.String() }

// keyFingerprint is the first 8 bytes of SHA-256 of a public key,
// hex-encoded — a safe, non-secret identifier derived only from public
// material (mirrors crypto.Identity's own fingerprint, identity.go).
func keyFingerprint(pub []byte) string {
	h := sha256.Sum256(pub)
	return hex.EncodeToString(h[:8])
}

// errCorrupt is returned by Load for any file that is missing, truncated, or
// does not match the expected versioned layout. Machine key custody fails
// closed: Load never returns a partially-populated Identity.
var errCorrupt = errors.New("machineid: corrupt or malformed identity file")

const identityFileVersion = 1

// fixedTailLen is the byte length of every fixed-size field after the
// hostname: noiseStaticPriv(32) + recipientPriv(32) + grantPriv(64) +
// relayPriv(64) + wakeKey(32) + contentKey(32) + epochID(4) + grantSeq(8).
const fixedTailLen = 32 + 32 + ed25519.PrivateKeySize + ed25519.PrivateKeySize + 32 + 32 + 4 + 8

// maxHostnameLen bounds the hostname length field so a corrupt/hostile file
// cannot claim an absurd length and either overflow or exhaust memory.
const maxHostnameLen = 1 << 16

// marshal encodes the identity's full bundle into a single versioned buffer:
// version(1) | hostname_len(4) | hostname | noiseStaticPriv(32) |
// recipientPriv(32) | grantPriv(64) | relayPriv(64) | wakeKey(32) |
// contentKey(32) | epochID(4) | grantSeq(8).
func (id *Identity) marshal() []byte {
	host := []byte(id.hostname)
	buf := make([]byte, 0, 1+4+len(host)+fixedTailLen)
	buf = append(buf, identityFileVersion)
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(host)))
	buf = append(buf, host...)
	buf = append(buf, id.noiseStaticPriv[:]...)
	buf = append(buf, id.recipientPriv[:]...)
	buf = append(buf, id.grantPriv...)
	buf = append(buf, id.relayPriv...)
	buf = append(buf, id.epochKeys.WakeKey[:]...)
	buf = append(buf, id.epochKeys.ContentKey[:]...)
	buf = binary.BigEndian.AppendUint32(buf, id.epochID)
	buf = binary.BigEndian.AppendUint64(buf, id.grantSeq)
	return buf
}

// unmarshal is the lossless inverse of marshal. It validates the version,
// bounds the hostname length, and requires the buffer be EXACTLY the expected
// length for that hostname — any short, truncated, or garbage buffer is
// rejected rather than partially parsed.
func unmarshal(buf []byte) (*Identity, error) {
	if len(buf) < 5 || buf[0] != identityFileVersion {
		return nil, errCorrupt
	}
	hostLen := binary.BigEndian.Uint32(buf[1:5])
	if hostLen > maxHostnameLen {
		return nil, errCorrupt
	}
	want := 5 + int(hostLen) + fixedTailLen
	if len(buf) != want {
		return nil, errCorrupt
	}

	off := 5
	hostname := string(buf[off : off+int(hostLen)])
	off += int(hostLen)

	var m Material
	copy(m.NoiseStaticPriv[:], buf[off:off+32])
	off += 32
	copy(m.RecipientPriv[:], buf[off:off+32])
	off += 32
	m.GrantSignPriv = append(ed25519.PrivateKey(nil), buf[off:off+ed25519.PrivateKeySize]...)
	off += ed25519.PrivateKeySize
	m.RelayAuthPriv = append(ed25519.PrivateKey(nil), buf[off:off+ed25519.PrivateKeySize]...)
	off += ed25519.PrivateKeySize
	copy(m.EpochKeys.WakeKey[:], buf[off:off+32])
	off += 32
	copy(m.EpochKeys.ContentKey[:], buf[off:off+32])
	off += 32
	m.EpochID = binary.BigEndian.Uint32(buf[off : off+4])
	off += 4
	m.GrantSeq = binary.BigEndian.Uint64(buf[off : off+8])

	return NewFromMaterial(hostname, m), nil
}

// Load reads a persisted machine identity. Any I/O error, or any corrupt,
// truncated, or malformed content, is an error and never a partially
// populated Identity (fail closed — this is machine key custody).
func Load(path string) (*Identity, error) {
	buf, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return unmarshal(buf)
}

// Save persists the full bundle to a single file at exactly 0600, regardless
// of the process umask or any pre-existing looser mode on the target: it
// writes a fresh 0600 temp file in the same directory, fsyncs it, and
// atomically renames it over the target. A symlink at the target is refused
// outright. This replicates crypto/secretfile.go's writeSecretFile pattern
// locally, since that helper is unexported and this package must not modify
// internal/remote/crypto.
func (id *Identity) Save(path string) error {
	return writeSecretFile(path, id.marshal())
}

// errUnsafeIdentityFile is returned when a target path is a symlink — writing
// through it could redirect key material via a symlink-swap attack.
var errUnsafeIdentityFile = errors.New("machineid: refusing to write identity through a symlink")

func writeSecretFile(path string, data []byte) error {
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s", errUnsafeIdentityFile, path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".machineid-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	// Finding 4 (re-audit, durability): fsync the parent DIR so the RENAME survives power loss,
	// mirroring grant.Save / remotegw's persistSeqCeiling. Machine key custody: without it a
	// crash could lose a rotated-epoch rename and resurrect the OLD epoch key, reviving a
	// revoked device's retained content key (reopens codex#1).
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
