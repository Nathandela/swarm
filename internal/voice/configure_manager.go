package voice

import (
	"crypto/rand"
	"errors"
	"os"
	"syscall"
)

// ConfigureManager creates the conversation identity once, before any native launch.
func ConfigureManager(dir, place, provider, workspace, socket, model string, deviceListen *string) (Config, error) {
	d, err := openVoiceDir(dir, true)
	if err != nil {
		return Config{}, err
	}
	defer func() { _ = d.Close() }()
	if err = syscall.Flock(int(d.Fd()), syscall.LOCK_EX); err != nil {
		return Config{}, err
	}
	existing, err := LoadConfig(dir)
	if err == nil {
		if existing.Provider != provider || existing.WorkDir != workspace || existing.DaemonSocket != socket || existing.Model != model {
			return Config{}, errors.New("voice: setup conflicts with the configured manager conversation")
		}
		if deviceListen != nil && existing.DeviceListen != *deviceListen {
			existing.DeviceListen = *deviceListen
			if err := SaveConfig(dir, existing); err != nil {
				return Config{}, err
			}
		}
		return existing, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Config{}, err
	}
	config := Config{Version: 2, PlaceID: place, Name: "Swarm manager", Provider: provider, ConversationID: rand.Text(), WorkDir: workspace, Model: model, DaemonSocket: socket, Language: "any", VoiceID: "marin", Mode: "push_to_talk"}
	if deviceListen != nil {
		config.DeviceListen = *deviceListen
	}
	if err := SaveConfig(dir, config); err != nil {
		return Config{}, err
	}
	return config, nil
}
