package adapter

// AuthProbe is the OPTIONAL extension a CLI implements when swarm has
// CHARACTERIZED where that CLI stores its login credentials on disk. It is
// discovered by TYPE ASSERTION (AsAuthProbe), never by a method on Adapter: the
// frozen method set gains nothing (ADR-010 Non-goals; the TranscriptLayout
// precedent).
//
// WHY IT EXISTS (ADR-024). A provider's long-lived processes -- the PTY CLI and
// any per-session backend -- load the credentials file once at startup and hold
// its tokens in memory. A logout/login to another account afterwards rotates the
// stored account; every process started before the change keeps failing token
// refresh until it is RESTARTED (the 2026-09-01 "access token could not be
// refreshed" incident). The probe gives the daemon the two pure halves of the
// detection: where the credentials live, and what counts as "the same account".
// Everything else -- reading the file, remembering the last identity, deciding
// to recycle a session -- is the core's job.
//
// IT NAMES AND DERIVES; IT NEVER OPENS. Both halves are PURE and TOTAL on the
// same terms as Command/Resume: deterministic, no panic on any input, and NO
// filesystem access whatsoever. Every byte of I/O stays in the core (ADR-001).
//
// ABSENCE IS THE SIGNAL (ADR-010 section 5). An adapter whose credential
// layout nobody has characterized implements nothing, the assertion fails, and
// the watcher simply does not watch that provider.
type AuthProbe interface {
	// AuthCredentialsFile is the credentials file path RELATIVE to the user
	// home directory (e.g. ".codex/auth.json"). It returns a relative path,
	// never an absolute one: the core joins it beneath the daemon's home, and
	// joining is the core's act.
	AuthCredentialsFile() string

	// AuthIdentity derives a stable ACCOUNT identity from the file's raw bytes.
	// The contract that makes the watcher sound: the identity is INVARIANT
	// under routine token refreshes (providers rewrite the file with fresh
	// tokens on a cadence, so mtime or a whole-file hash would false-positive)
	// and changes exactly when the logged-in account changes. The value must
	// never carry a secret -- derive a digest, not the token. ok==false means
	// the bytes do not parse as this provider's credentials (mid-login, a
	// truncated write, garbage), which the watcher treats as "unknown: hold".
	AuthIdentity(raw []byte) (identity string, ok bool)
}

// AsAuthProbe reports whether a has a characterized credentials layout.
func AsAuthProbe(a Adapter) (AuthProbe, bool) {
	p, ok := a.(AuthProbe)
	return p, ok
}
