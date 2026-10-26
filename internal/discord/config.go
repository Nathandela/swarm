package discord

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"
)

// Config contains non-secret configuration for one local installation.
type Config struct {
	Version           int      `json:"version"`
	InstallationID    string   `json:"installation_id"`
	Name              string   `json:"name"`
	ApplicationID     string   `json:"application_id"`
	OwnerID           string   `json:"owner_id"`
	TokenFile         string   `json:"token_file"`
	DaemonSocket      string   `json:"daemon_socket"`
	GuildIDs          []string `json:"guild_ids"`
	ChannelIDs        []string `json:"channel_ids"`
	UserIDs           []string `json:"user_ids"`
	ReceptionistCLI   string   `json:"receptionist_cli"`
	ReceptionistModel string   `json:"receptionist_model"`
	WorkDir           string   `json:"work_dir"`
}

func ConfigPath(dir string) string { return filepath.Join(dir, "config.json") }
func (c Config) Validate() error {
	if c.Version != 1 {
		return errors.New("discord: unsupported config version")
	}
	validText := func(s string, max int) bool {
		if s == "" || len(s) > max || !utf8.ValidString(s) {
			return false
		}
		for _, r := range s {
			if unicode.IsControl(r) {
				return false
			}
		}
		return true
	}
	if !validText(c.InstallationID, 128) || !validText(c.Name, 256) {
		return errors.New("discord: invalid installation identity or name")
	}
	id := func(s string) bool {
		if len(s) == 0 || len(s) > 20 {
			return false
		}
		for _, r := range s {
			if r < '0' || r > '9' {
				return false
			}
		}
		return true
	}
	if !id(c.ApplicationID) || !id(c.OwnerID) {
		return errors.New("discord: invalid application or owner ID")
	}
	for _, ids := range [][]string{c.GuildIDs, c.ChannelIDs, c.UserIDs} {
		for _, s := range ids {
			if !id(s) {
				return errors.New("discord: invalid allowlist ID")
			}
		}
	}
	for _, p := range []string{c.TokenFile, c.DaemonSocket, c.WorkDir} {
		if !filepath.IsAbs(p) || !validText(p, 4096) {
			return errors.New("discord: invalid config path")
		}
	}
	for _, s := range []string{c.ReceptionistCLI, c.ReceptionistModel} {
		if s != "" && !validText(s, 256) {
			return errors.New("discord: invalid receptionist setting")
		}
	}
	return nil
}
func privateDir(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	entry, err := os.Lstat(dir)
	if err != nil || !entry.IsDir() {
		return errors.New("discord: invalid state directory")
	}
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.IsDir() || !os.SameFile(entry, opened) {
		return errors.New("discord: invalid state directory")
	}
	return f.Chmod(0700)
}
func singleFile(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.Mode().IsRegular() && stat.Nlink == 1
}
func openPrivateFile(path string) (*os.File, error) {
	entry, err := os.Lstat(path)
	if err != nil || !singleFile(entry) {
		return nil, errors.New("discord: invalid private file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("discord: open private file failed")
	}
	opened, err := f.Stat()
	if err != nil || !singleFile(opened) || !os.SameFile(entry, opened) {
		f.Close()
		return nil, errors.New("discord: invalid private file")
	}
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return nil, errors.New("discord: secure private file failed")
	}
	return f, nil
}
func SaveConfig(dir string, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if err := privateDir(dir); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	f, err := os.CreateTemp(dir, ".config-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), ConfigPath(dir))
}
func LoadConfig(dir string) (Config, error) {
	var c Config
	if err := privateDir(dir); err != nil {
		return c, err
	}
	f, err := openPrivateFile(ConfigPath(dir))
	if err != nil {
		return c, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return c, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&c); err != nil {
		return Config{}, errors.New("discord: malformed config")
	}
	if err = dec.Decode(&struct{}{}); err != io.EOF {
		return Config{}, errors.New("discord: malformed config")
	}
	if err = c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}
func LoadToken(path string) (string, error) {
	f, err := openPrivateFile(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return "", errors.New("discord: read token file failed")
	}
	s := strings.TrimSpace(string(b))
	if s == "" || len(b) > 4096 || strings.ContainsAny(s, "\r\n") {
		return "", errors.New("discord: invalid token file")
	}
	return s, nil
}
