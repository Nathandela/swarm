package daemon

import (
	"crypto/rand"
	"encoding/base32"
	"path/filepath"
	"strings"
	"syscall"
)

// idEncoding is unpadded base32 (RFC 4648). Its alphabet is A-Z and 2-7; we
// lowercase the result so ids are lowercase-only (the Epic 1 case-collision
// guard) while staying within the store's path-safe set [a-z0-9._-] and never
// starting with '-' or being "." / "..".
var idEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// generateID returns a fresh lowercase, collision-resistant, path-safe session
// id. 10 random bytes give 80 bits of entropy (16 base32 chars) — collisions are
// negligible across any realistic session count, and lowercasing cannot merge
// two ids since every id is already lowercase.
func generateID() string {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is catastrophic and effectively never happens; a
		// panic here is preferable to emitting a weak or empty id.
		panic("daemon: generateID: " + err.Error())
	}
	return strings.ToLower(idEncoding.EncodeToString(b[:]))
}

// shimSocketPath is the DETERMINISTIC per-session shim socket path: both the
// launch path (which tells the shim where to bind) and the reconcile path (which
// dials it to reconnect) compute the same <stateDir>/<id>/shim.sock.
func shimSocketPath(stateDir, id string) string {
	return filepath.Join(stateDir, id, "shim.sock")
}

// pidAlive reports whether pid is a live process (a signal-0 probe). EPERM means
// it exists but we may not signal it — still alive. (Named to avoid colliding
// with the identically-purposed processAlive test helper in this package.)
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
