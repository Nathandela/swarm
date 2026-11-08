package claudeinput

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/managerproof"
)

const managerSetupFile = "claude-manager-input.json"
const managerEnvelopePrefix = "[SWARM_MANAGER_INPUT_V1:"

type managerSetup struct {
	ConversationID string `json:"conversation_id"`
	SenderName     string `json:"sender_name"`
}

// InstallManager creates a unique per-invocation native source; ordinary workers use Install.
func InstallManager(dir, conversationID string) (string, error) {
	if conversationID == "" || len(conversationID) > 128 {
		return "", errors.New("invalid manager conversation")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	setup := managerSetup{ConversationID: conversationID, SenderName: "swarm-manager-input-" + hex.EncodeToString(nonce[:])}
	raw, _ := json.Marshal(setup)
	if err := os.WriteFile(filepath.Join(dir, managerSetupFile), raw, 0600); err != nil {
		return "", err
	}
	return install(dir, setup.SenderName)
}
func readManagerSetup(dir string) (managerSetup, error) {
	var setup managerSetup
	raw, err := os.ReadFile(filepath.Join(dir, managerSetupFile))
	if err != nil {
		return setup, err
	}
	if len(raw) > 1024 || json.Unmarshal(raw, &setup) != nil || setup.ConversationID == "" || len(setup.ConversationID) > 128 || !strings.HasPrefix(setup.SenderName, "swarm-manager-input-") || len(setup.SenderName) != len("swarm-manager-input-")+32 {
		return managerSetup{}, os.ErrInvalid
	}
	suffix := strings.TrimPrefix(setup.SenderName, "swarm-manager-input-")
	if _, err := hex.DecodeString(suffix); err != nil || strings.ToLower(suffix) != suffix {
		return managerSetup{}, os.ErrInvalid
	}
	return setup, nil
}
func managerProjection(sender, envelope string) string {
	// The qualified 2.1.319 native wrapper uses a 23-character display name;
	// source authentication separately retains the complete ASCII plugin name.
	if len(sender) > 23 {
		sender = sender[:23] + "…"
	}
	return "The " + sender + " plugin sent a message:\n" + envelope + "\n\nThis is how Claude Code surfaces a prompt a plugin submits between turns — it starts this turn in the user's place. Address the message above."
}

// ManagerEnvelope separates registered machine admission from ordinary human prose.
func ManagerEnvelope(nonce, text string) (string, error) {
	envelope := managerEnvelopePrefix + nonce + "]\n" + text
	if !managerproof.ValidHash(nonce) || text == "" || !utf8.ValidString(text) || strings.Contains(text, managerEnvelopePrefix) || len(envelope) > 4096 {
		return "", managerproof.ErrInvalid
	}
	return envelope, nil
}
