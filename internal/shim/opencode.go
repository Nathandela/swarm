package shim

import (
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"

	"github.com/Nathandela/swarm/internal/adapter"
)

const OpenCodeBindingFile = "opencode-binding.json"
const OpenCodeInputEmbargoFile = "opencode-input-embargo.json"

var errOpenCodeStarting = errors.New("opencode_starting")

// OpenCodeConfig requires the ordinary TUI process to remain input-fenced until binding is durable.
type OpenCodeConfig struct {
	RootID       string `json:"root_id"`
	Generation   string `json:"generation"`
	BindingFile  string `json:"binding_file"`
	EmbargoToken string `json:"embargo_token"`
}

func validHex(s string, n int) bool {
	if len(s) != n*2 || s != strings.ToLower(s) {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// ValidateOpenCodeConfig is checked before setsid and again before any native process executes.
func ValidateOpenCodeConfig(cfg Config) error {
	if cfg.OpenCode == nil {
		return nil
	}
	b := cfg.OpenCode
	dir, err := filepath.EvalSymlinks(cfg.SessionDir)
	if err != nil {
		return errOpenCodeStarting
	}
	if cfg.AccountBinding != nil || cfg.InputEmbargo != "" || cfg.Backend != nil || !adapter.IsOpenCodeConversationID(b.RootID) || !validHex(b.Generation, 16) || !validHex(b.EmbargoToken, 32) || !filepath.IsAbs(cfg.SessionDir) || b.BindingFile != filepath.Join(dir, OpenCodeBindingFile) || len(cfg.Argv) == 0 {
		return errOpenCodeStarting
	}
	for _, arg := range cfg.Argv[1:] {
		key, _, _ := strings.Cut(arg, "=")
		switch key {
		case "--prompt", "--session", "-s", "--continue", "-c", "--fork", "--mini", "--pure", "--port", "--hostname", "--mdns":
			return errOpenCodeStarting
		}
	}
	return nil
}
