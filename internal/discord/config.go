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
const (
	ProjectAccessVM       = "vm"
	ProjectAccessEnrolled = "enrolled"
)

type Config struct {
	ProjectAccess      string              `json:"project_access,omitempty"`
	PreviousSupervisor *PreviousSupervisor `json:"previous_supervisor,omitempty"`
	Supervisor         *SupervisorPaths    `json:"supervisor,omitempty"`
	Mode               string              `json:"mode,omitempty"`
	ManagerCLI         string              `json:"manager_cli,omitempty"`
	Projects           map[string]string   `json:"projects,omitempty"`
	Enabled            bool                `json:"enabled"`
	Version            int                 `json:"version"`
	InstallationID     string              `json:"installation_id"`
	Name               string              `json:"name"`
	ApplicationID      string              `json:"application_id"`
	DMChannelID        string              `json:"dm_channel_id,omitempty"`
	OwnerID            string              `json:"owner_id"`
	TokenFile          string              `json:"token_file"`
	DaemonSocket       string              `json:"daemon_socket"`
	GuildIDs           []string            `json:"guild_ids"`
	ChannelIDs         []string            `json:"channel_ids"`
	UserIDs            []string            `json:"user_ids"`
	ReceptionistCLI    string              `json:"receptionist_cli"`
	ReceptionistModel  string              `json:"receptionist_model"`
	WorkDir            string              `json:"work_dir"`
}

// SupervisorPaths retain the operator's chosen place when the user service starts after logout.
type SupervisorPaths struct {
	StateDir string `json:"state_dir"`
	Lock     string `json:"lock"`
	Log      string `json:"log"`
}

// PreviousSupervisor retains the original owner of discussions bound before cutover.
type PreviousSupervisor struct {
	SupervisorPaths
	Socket   string `json:"socket"`
	Endpoint string `json:"endpoint"`
}

func ConfigPath(dir string) string { return filepath.Join(dir, "config.json") }

func ActiveState(daemonDir string) (string, error) {
	path := filepath.Join(daemonDir, "discord-active.json")
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return filepath.Join(daemonDir, "discord"), nil
	} else if err != nil {
		return "", err
	}
	return SelectedState(daemonDir)
}

// SelectedState requires a persisted selection for a place migration.
func SelectedState(daemonDir string) (string, error) {
	return readSelectedState(daemonDir, openPrivateFile)
}

func readSelectedState(daemonDir string, open func(string) (*os.File, error)) (string, error) {
	f, err := open(filepath.Join(daemonDir, "discord-active.json"))
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, 8193))
	var dir string
	if err != nil || len(raw) > 8192 || json.Unmarshal(raw, &dir) != nil || !validStatePath(dir) {
		return "", errors.New("discord: invalid active integration")
	}
	return dir, nil
}
func validStatePath(dir string) bool {
	return filepath.IsAbs(dir) && len(dir) <= 4096 && utf8.ValidString(dir) && strings.IndexFunc(dir, unicode.IsControl) < 0
}
func SelectState(daemonDir, dir string) error {
	if !validStatePath(dir) {
		return errors.New("discord: invalid active integration")
	}
	raw, err := json.Marshal(dir)
	if err != nil {
		return err
	}
	return savePrivate(daemonDir, "discord-active.json", raw)
}

func NativeInputEnabled(dir string) bool {
	if _, err := os.Lstat(ConfigPath(dir)); err != nil {
		return false
	}
	c, err := LoadConfig(dir)
	return err == nil && c.Enabled
}
func (c Config) TeammateMode() bool { return c.Mode == "teammate" || c.Mode == "personal" }

// EffectiveProjectAccess preserves the pre-policy default for saved installations.
func (c Config) EffectiveProjectAccess() string {
	if !c.TeammateMode() {
		return ""
	}
	if c.ProjectAccess == ProjectAccessVM {
		return ProjectAccessVM
	}
	return ProjectAccessEnrolled
}

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
	if p := c.PreviousSupervisor; p != nil && (!c.TeammateMode() || c.Supervisor == nil || !validStatePath(p.StateDir) || !validStatePath(p.Socket) || !validStatePath(p.Lock) || !validStatePath(p.Log) || !validText(p.Endpoint, 128) || p.Endpoint == "" || p.StateDir == c.Supervisor.StateDir || p.Socket == c.DaemonSocket) {
		return errors.New("discord: invalid previous supervisor enrollment")
	}
	if c.ProjectAccess != "" && (!c.TeammateMode() || (c.ProjectAccess != ProjectAccessVM && c.ProjectAccess != ProjectAccessEnrolled)) {
		return errors.New("discord: project access must be enrolled or vm for a teammate or personal manager")
	}
	if c.Mode != "personal" && c.DMChannelID != "" {
		return errors.New("discord: DM channel requires personal mode")
	}
	switch c.Mode {
	case "", "legacy":
		if !id(c.OwnerID) {
			return errors.New("discord: invalid owner ID")
		}
	case "teammate", "personal":
		if p := c.Supervisor; p != nil && (!validStatePath(p.StateDir) || !validStatePath(p.Lock) || !validStatePath(p.Log)) {
			return errors.New("discord: invalid enrolled supervisor paths")
		}
		if c.ManagerCLI != "codex" && c.ManagerCLI != "claude" {
			return errors.New("discord: teammate requires a Codex or Claude manager")
		}
		if c.Mode == "personal" {
			if !snowflake(c.OwnerID) || !snowflake(c.DMChannelID) || c.OwnerID == c.ApplicationID ||
				len(c.GuildIDs)+len(c.ChannelIDs)+len(c.UserIDs) != 0 {
				return errors.New("discord: personal mode requires one owner and a verified DM channel")
			}
		} else if len(c.GuildIDs) == 0 {
			return errors.New("discord: teammate requires an enrolled server")
		}
		if c.OwnerID != "" && !id(c.OwnerID) {
			return errors.New("discord: invalid owner ID")
		}
		if len(c.Projects) > 32 {
			return errors.New("discord: too many teammate projects")
		}
		for name, root := range c.Projects {
			if !validText(name, 128) || strings.TrimSpace(name) != name || !validStatePath(root) || filepath.Clean(root) != root {
				return errors.New("discord: invalid named project")
			}
		}
	default:
		return errors.New("discord: unknown connection mode")
	}
	if !id(c.ApplicationID) {
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
	defer func() { _ = f.Close() }()
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
		_ = f.Close()
		return nil, errors.New("discord: invalid private file")
	}
	if err = f.Chmod(0600); err != nil {
		_ = f.Close()
		return nil, errors.New("discord: secure private file failed")
	}
	return f, nil
}
func SaveConfig(dir string, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return savePrivate(dir, "config.json", data)
}
func savePrivate(dir, name string, data []byte) error {
	if err := privateDir(dir); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".config-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err = f.Write(data); err != nil {
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
	if err = os.Rename(f.Name(), filepath.Join(dir, name)); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}
func LoadConfig(dir string) (Config, error) {
	if err := privateDir(dir); err != nil {
		return Config{}, err
	}
	return readConfig(dir, openPrivateFile)
}

func readConfig(dir string, open func(string) (*os.File, error)) (Config, error) {
	var c Config
	f, err := open(ConfigPath(dir))
	if err != nil {
		return c, err
	}
	defer func() { _ = f.Close() }()
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
	defer func() { _ = f.Close() }()
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
