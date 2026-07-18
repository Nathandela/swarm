package crypto

import (
	"crypto/ed25519"
	"encoding/binary"
	"errors"
)

// ErrBadCommandKey is returned when a command-signing public key is malformed.
var ErrBadCommandKey = errors.New("crypto: command-signing public key must be 32 bytes")

// ErrCommandSig is returned when a command signature does not verify.
var ErrCommandSig = errors.New("crypto: command signature verification failed")

// ErrBadCommand is returned when a command is missing a mandatory identifier, an
// oversized field, or a content hash that is neither absent nor exactly 32 bytes.
var ErrBadCommand = errors.New("crypto: malformed command")

// maxCommandField caps each variable-length field so a length prefix cannot
// overflow uint32 and a hostile command cannot balloon the signing input.
const maxCommandField = 1 << 16

// commandDomain domain-separates command signatures from any other Ed25519 use
// of the same key.
const commandDomain = "swarm-remote/1 cmd\x00"

// Command is the canonical tuple every remote mutating op is signed over (D4).
// The signature binds all fields including the optional content hash, so a
// captured command cannot be replayed under a new operation_id or a pushed-out
// expiry.
type Command struct {
	Action      string
	Machine     string
	Session     string
	OperationID string
	ExpiresAt   int64
	ContentHash []byte
}

// Canonical is the unambiguous, length-prefixed signing input, or an error if
// the command is malformed (F13). Length prefixes make every field boundary
// explicit, so no two distinct well-formed commands share an encoding. A nil
// and an empty content hash canonicalize identically (both encode as length 0):
// "no content hash". The mandatory identifiers must be present and no field may
// exceed maxCommandField; the content hash is absent or exactly 32 bytes.
func (c Command) Canonical() ([]byte, error) {
	if c.Action == "" || c.Machine == "" || c.Session == "" || c.OperationID == "" {
		return nil, ErrBadCommand
	}
	if len(c.ContentHash) != 0 && len(c.ContentHash) != 32 {
		return nil, ErrBadCommand
	}
	for _, f := range []string{c.Action, c.Machine, c.Session, c.OperationID} {
		if len(f) > maxCommandField {
			return nil, ErrBadCommand
		}
	}
	b := []byte(commandDomain)
	b = appendField(b, []byte(c.Action))
	b = appendField(b, []byte(c.Machine))
	b = appendField(b, []byte(c.Session))
	b = appendField(b, []byte(c.OperationID))
	b = binary.BigEndian.AppendUint64(b, uint64(c.ExpiresAt))
	b = appendField(b, c.ContentHash)
	return b, nil
}

func appendField(b, f []byte) []byte {
	b = binary.BigEndian.AppendUint32(b, uint32(len(f)))
	return append(b, f...)
}

// VerifyCommandSig verifies a detached command signature against the pinned
// device command-signing key. A malformed key is rejected without panic.
func VerifyCommandSig(commandSigningPub, msg, sig []byte) error {
	if len(commandSigningPub) != ed25519.PublicKeySize {
		return ErrBadCommandKey
	}
	if !ed25519.Verify(commandSigningPub, msg, sig) {
		return ErrCommandSig
	}
	return nil
}
