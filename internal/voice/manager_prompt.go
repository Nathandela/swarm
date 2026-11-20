package voice

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxManagerStartingPrompt = 1024

var errManagerStartingPrompt = errors.New("manager prompt must be a single line of valid text, up to 1024 bytes")

// LoadManagerStartingPrompt reads the local Swarm setting afresh for each turn.
func LoadManagerStartingPrompt(stateDir string) (string, error) {
	raw, err := readVoiceFile(filepath.Join(stateDir, "manager-settings"), "starting-prompt", maxManagerStartingPrompt)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	text := string(raw)
	if !validManagerStartingPrompt(text) {
		return "", errManagerStartingPrompt
	}
	return text, nil
}

// SaveManagerStartingPrompt replaces the owner-local setting atomically.
func SaveManagerStartingPrompt(stateDir, text string) error {
	if !validManagerStartingPrompt(text) {
		return errManagerStartingPrompt
	}
	return writeVoiceFile(filepath.Join(stateDir, "manager-settings"), "starting-prompt", []byte(strings.TrimSpace(text)))
}

func validManagerStartingPrompt(s string) bool {
	return len(s) <= maxManagerStartingPrompt && utf8.ValidString(s) && strings.IndexFunc(s, unicode.IsControl) < 0
}
