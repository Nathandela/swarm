package protocol

import (
	"crypto/sha256"
	"encoding/binary"
	"sort"
)

// LaunchContentHash is the 32-byte content hash bound into a remote launch command's
// signature (R-POL.9 launch content-binding). A launch has no session to name, so the
// signed tuple would otherwise not cover WHAT is launched -- letting a compromised
// gateway swap the agent, cwd, options, or prompt of a validly-signed launch. Both the
// phone-core (signer) and the daemon (verifier) compute this hash over exactly the
// fields the daemon acts on; a mismatch makes VerifyCommandSig fail.
//
// Bound fields: Agent, Cwd, sorted Options, InitialPrompt, Worktree. Env is excluded
// (a remote launch drops client env entirely, R-POL.5) and Cols/Rows are excluded
// (cosmetic terminal dimensions). The encoding is length-prefixed so no two distinct
// specs share an encoding.
func LaunchContentHash(req *LaunchReq) []byte {
	h := sha256.New()
	writeHashField(h, []byte(req.Agent))
	writeHashField(h, []byte(req.Cwd))
	writeHashField(h, []byte(req.InitialPrompt))
	var wt byte
	if req.Worktree {
		wt = 1
	}
	h.Write([]byte{wt})

	keys := make([]string, 0, len(req.Options))
	for k := range req.Options {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var count [4]byte
	binary.BigEndian.PutUint32(count[:], uint32(len(keys)))
	h.Write(count[:])
	for _, k := range keys {
		writeHashField(h, []byte(k))
		writeHashField(h, []byte(req.Options[k]))
	}
	return h.Sum(nil)
}

// writeHashField writes a big-endian uint32 length prefix followed by the bytes, so
// field boundaries are explicit and unambiguous.
func writeHashField(h interface{ Write([]byte) (int, error) }, b []byte) {
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(b)))
	_, _ = h.Write(n[:])
	_, _ = h.Write(b)
}
