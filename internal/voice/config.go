package voice

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/jsontext"
)

const maxConfigBytes = 16 << 10

// Config contains only settings; the speech key is kept in a separate private file.
type Config struct {
	Version        int    `json:"version"`
	PlaceID        string `json:"place_id"`
	Name           string `json:"name"`
	Provider       string `json:"provider"`
	ConversationID string `json:"conversation_id"`
	WorkDir        string `json:"work_dir"`
	Model          string `json:"model,omitempty"`
	DaemonSocket   string `json:"daemon_socket"`
	Region         string `json:"region,omitempty"`
	Language       string `json:"language"`
	VoiceID        string `json:"voice_id"`
	Mode           string `json:"mode"`
	DeviceListen   string `json:"device_listen,omitempty"`
}

func (c Config) Validate() error {
	if c.Version == 1 {
		return errors.New("voice: Gradium settings require migration; run swarm voice setup to switch to the global OpenAI endpoint and reset the speech voice to marin")
	}
	if c.Version != 2 {
		return errors.New("voice: unsupported configuration version")
	}
	if c.Provider != "codex" && c.Provider != "claude" {
		return errors.New("voice: choose Codex or Claude")
	}
	for _, s := range []string{c.PlaceID, c.ConversationID, c.VoiceID} {
		if !configText(s, 128) {
			return ErrInvalid
		}
	}
	if !configText(c.Name, 256) || (c.Model != "" && !configText(c.Model, 256)) {
		return ErrInvalid
	}
	for _, p := range []string{c.WorkDir, c.DaemonSocket} {
		if !configText(p, 4096) || !filepath.IsAbs(p) || filepath.Clean(p) != p {
			return ErrInvalid
		}
	}
	if c.Region != "" || strings.IndexFunc(c.VoiceID, unicode.IsSpace) >= 0 {
		return ErrInvalid
	}
	if c.Language != "en" && c.Language != "fr" && c.Language != "es" && c.Language != "pt" && c.Language != "de" && c.Language != "any" {
		return ErrInvalid
	}
	if c.DeviceListen != "" && !validDeviceEndpoint(c.DeviceListen, true) {
		return errors.New("voice: device listener requires 127.0.0.1 or [::1] with a port")
	}
	if c.Mode != "conversation" && c.Mode != "push_to_talk" {
		return ErrInvalid
	}
	return nil
}

func configText(s string, limit int) bool {
	return s != "" && len(s) <= limit && utf8.ValidString(s) && strings.IndexFunc(s, unicode.IsControl) < 0
}

func SaveConfig(dir string, config Config) error {
	if err := config.Validate(); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	if len(raw)+1 > maxConfigBytes {
		return ErrCapacity
	}
	return writeVoiceFile(dir, "config.json", append(raw, '\n'))
}

func LoadConfig(dir string) (Config, error) {
	config, err := readConfig(dir)
	if err != nil {
		return Config{}, err
	}
	if err = config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func readConfig(dir string) (Config, error) {
	raw, err := readVoiceFile(dir, "config.json", maxConfigBytes)
	if err != nil {
		return Config{}, err
	}
	var config Config
	if err := validateConfigJSON(raw); err != nil {
		return config, err
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return Config{}, ErrCorrupt
	}
	return config, nil
}

func validateConfigJSON(raw []byte) error {
	return validateObjectMembers(raw, "version", "place_id", "name", "provider", "conversation_id", "work_dir", "model", "daemon_socket", "region", "language", "voice_id", "mode", "device_listen")
}

func validateObjectMembers(raw []byte, allowed ...string) error {
	if jsontext.ValidateUnicode(raw) != nil {
		return ErrCorrupt
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return ErrCorrupt
	}
	seen := make(map[string]bool)
	for d.More() {
		token, err = d.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return ErrCorrupt
		}
		if !slices.Contains(allowed, key) {
			return ErrCorrupt
		}
		seen[key] = true
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return ErrCorrupt
		}
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') {
		return ErrCorrupt
	}
	if _, err = d.Token(); err != io.EOF {
		return ErrCorrupt
	}
	return nil
}

// SetupOpenAISpeech explicitly switches v1 installations to the global OpenAI
// endpoint, preserving the manager identity and resetting the provider-specific voice.
func SetupOpenAISpeech(dir string, input io.Reader) error {
	config, err := readConfig(dir)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	migrate := err == nil && config.Version == 1
	if err == nil {
		if migrate {
			if (config.Region != "auto" && config.Region != "eu" && config.Region != "us") || !configText(config.VoiceID, 128) {
				return ErrCorrupt
			}
			config.Version, config.Region, config.VoiceID = 2, "", "marin"
		}
		if err := config.Validate(); err != nil {
			return err
		}
	}
	if err := SaveOpenAIKey(dir, input); err != nil {
		return err
	}
	if migrate {
		return SaveConfig(dir, config)
	}
	return nil
}

// SaveOpenAIKey accepts a raw key from hidden input, never from a command argument.
func SaveOpenAIKey(dir string, input io.Reader) error {
	raw, err := io.ReadAll(io.LimitReader(input, 4097))
	if err != nil {
		return errors.New("voice: could not read speech credential")
	}
	key, err := parseOpenAIKey(raw)
	if err != nil {
		return err
	}
	return writeVoiceFile(dir, "openai.key", []byte(key))
}

func LoadOpenAIKey(dir string) (string, error) {
	raw, err := readVoiceFile(dir, "openai.key", 4096)
	if err != nil {
		return "", err
	}
	return parseOpenAIKey(raw)
}

func parseOpenAIKey(raw []byte) (string, error) {
	key := strings.TrimSpace(string(raw))
	if len(raw) > 4096 || key == "" || strings.IndexFunc(key, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
		return "", errors.New("voice: invalid speech credential")
	}
	return key, nil
}

func openVoiceDir(dir string, create bool) (*os.File, error) {
	if create {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
	}
	f, err := os.OpenFile(dir, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err == nil {
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != uint32(os.Getuid()) || info.Mode().Perm() != 0700 {
			err = errors.New("voice: settings directory must be owned by you with mode 0700")
		}
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func readVoiceFile(dir, name string, limit int64) ([]byte, error) {
	d, err := openVoiceDir(dir, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = d.Close() }()
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !privateRegular(info) || info.Mode().Perm() != 0600 {
		return nil, errors.New("voice: settings file must be a private owner-only regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, ErrCapacity
	}
	return raw, nil
}

func writeVoiceFile(dir, name string, raw []byte) error {
	d, err := openVoiceDir(dir, true)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	path := filepath.Join(dir, name)
	if info, err := os.Lstat(path); err == nil {
		if !privateRegular(info) || info.Mode().Perm() != 0600 {
			return errors.New("voice: refusing to replace unsafe settings file")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	f, err := os.CreateTemp(dir, ".voice-settings-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err = f.Write(raw); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	return d.Sync()
}
