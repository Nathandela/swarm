package adapter

import "time"

// LiveName is one fact a CLI publishes about a running session: the name it shows the
// user right now, and when that name was set. Since is the newest-wins clock of
// ADR-021: the assembly adopts the name only if Since is later than the last name
// swarm itself stamped on the session.
type LiveName struct {
	Name  string
	Since time.Time
}

// LiveNameSource is the optional extension for a CLI that keeps a per-process registry
// of its running sessions under the user's home (Claude Code: ~/.claude/sessions/<pid>.json).
// The adapter stays pure: it names the directory and parses one file; the assembly
// lists the directory, reads the files and applies the result. Absence is supported:
// a CLI without such a registry simply never has its name adopted.
type LiveNameSource interface {
	// LiveNameDir is the registry directory, relative to the daemon user's home.
	LiveNameDir() string
	// LiveNameFromFile parses one registry file. ok reports that the file names
	// conversationID's session AND carries a usable name and timestamp. Total and
	// deterministic on any input.
	LiveNameFromFile(raw []byte, conversationID string) (LiveName, bool)
}

// AsLiveNameSource reports whether a publishes live session names.
func AsLiveNameSource(a Adapter) (LiveNameSource, bool) {
	src, ok := a.(LiveNameSource)
	return src, ok
}
