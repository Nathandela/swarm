package voice

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// SwitchManager selects a fresh manager conversation while the service is
// stopped. It leaves prior native sessions and durable ledgers untouched.
func SwitchManager(dir, from, provider, workspace string) (Config, error) {
	if !configText(from, 128) || (provider != "codex" && provider != "claude") ||
		(workspace != "" && (!filepath.IsAbs(workspace) || filepath.Clean(workspace) != workspace)) {
		return Config{}, ErrInvalid
	}
	configDir, err := openVoiceDir(dir, false)
	if err != nil {
		return Config{}, err
	}
	defer func() { _ = configDir.Close() }()
	if err := syscall.Flock(int(configDir.Fd()), syscall.LOCK_EX); err != nil {
		return Config{}, err
	}
	lock, err := lockPrivateService(dir, "controller.lock")
	if errors.Is(err, errControllerRunning) {
		return Config{}, errors.New("voice: stop the voice service before switching managers")
	}
	if err != nil {
		return Config{}, err
	}
	defer func() { _ = lock.Close() }()
	current, err := LoadConfig(dir)
	if err != nil {
		return Config{}, err
	}
	if current.ConversationID != from {
		return Config{}, errors.New("voice: --from does not match the currently configured conversation")
	}
	store, err := OpenStore(dir)
	if err != nil {
		return Config{}, err
	}
	defer func() { _ = store.Close() }()
	state, err := store.Snapshot()
	if err != nil {
		return Config{}, err
	}
	if err := checkSwitchEffects(dir, state, from); err != nil {
		return Config{}, err
	}
	current.Provider = provider
	if workspace != "" {
		current.WorkDir = workspace
	}
	current.ConversationID = rand.Text()
	current.Model = ""
	current.IsolatedConversation = true
	if err := SaveConfig(dir, current); err != nil {
		return Config{}, err
	}
	return current, nil
}

func checkSwitchEffects(dir string, state State, conversation string) error {
	uncertain := errors.New("voice: current manager has unresolved native effects; restart its voice service and reconcile its work before switching")
	for _, action := range state.Actions {
		if action.ConversationID == conversation && action.State == "executing" {
			return uncertain
		}
	}
	for _, admission := range state.Admissions {
		if admission.ConversationID != conversation || !admission.SettledAt.IsZero() {
			continue
		}
		switch admission.Phase {
		case "":
			if admission.NativeInputID != "" {
				return uncertain
			}
		case "executing", "submitted", "admitted", "running", "uncertain":
			return uncertain
		}
	}
	if _, bound := state.Conversations[conversation]; bound {
		return nil
	}
	raw, err := readVoiceFile(dir, managerIntentName(conversation), MaxControlFrameBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var intent managerLaunchIntent
	if json.Unmarshal(raw, &intent) != nil || intent.Version != 1 || intent.ConversationID != conversation || intent.OperationID == "" {
		return ErrCorrupt
	}
	switch intent.Phase {
	case "prepared":
		if intent.SessionID != "" {
			return ErrCorrupt
		}
		return nil
	case "executing", "applied":
		return uncertain
	default:
		return ErrCorrupt
	}
}

func managerBridgeAuthName(cfg Config) string {
	if cfg.IsolatedConversation {
		return "bridge-" + hashBytes([]byte(cfg.ConversationID)) + ".auth"
	}
	return managerBridgeAuth
}

func deviceRegistryName(cfg Config) string {
	if cfg.IsolatedConversation {
		return "voice-devices-" + hashBytes([]byte(cfg.ConversationID)) + ".json"
	}
	return deviceRegistryFile
}
