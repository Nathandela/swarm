package hermesinput

import (
	"errors"
	"strings"
)

// MinimumVersion is the stable source release containing the qualified owner API.
// Actual source/PTY qualification is recorded separately; a version alone is no receipt.
const MinimumVersion = "0.22.5"

func ValidSessionID(id string) bool {
	if len(id) != 70 || !strings.HasPrefix(id, "local-") {
		return false
	}
	for _, c := range id[6:] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ViewerArgv attaches the provider's multiclient TUI to the exact owned session.
// The legacy classic adapter remains separate and keeps its existing command.
func ViewerArgv(program, sessionID string) ([]string, error) {
	if program == "" || !ValidSessionID(sessionID) {
		return nil, errors.New("hermes native viewer requires a canonical local session ID")
	}
	return []string{program, "chat", "--tui", "--resume", sessionID}, nil
}
