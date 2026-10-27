package hermesinput

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Nathandela/swarm/internal/procstart"
	"golang.org/x/mod/semver"
)

const DescriptorFile = "hermes-input.json"
const StopFile = "hermes-stopped.json"

type SessionConfig struct {
	Program         string            `json:"program"`
	ProviderVersion string            `json:"provider_version"`
	Profile         string            `json:"profile,omitempty"`
	WorkerID        string            `json:"worker_id"`
	Cwd             string            `json:"cwd"`
	Options         map[string]string `json:"options,omitempty"`
	Origins         []string          `json:"origins,omitempty"`
	ResumeID        string            `json:"resume_id,omitempty"`
	InitialPrompt   string            `json:"initial_prompt,omitempty"`
}

type Descriptor struct {
	WorkerID        string  `json:"worker_id"`
	Generation      string  `json:"generation"`
	ProviderVersion string  `json:"provider_version"`
	ShimPID         int     `json:"shim_pid"`
	ShimStartTime   int64   `json:"shim_start_time"`
	Binding         Binding `json:"binding"`
}

func SupportedVersion(version string) bool {
	v := "v" + version
	return semver.IsValid(v) && semver.Prerelease(v) == "" && semver.Compare(v, "v"+MinimumVersion) >= 0
}

func (c SessionConfig) EnsureArgv() []string {
	argv := []string{c.Program}
	if c.Profile != "" {
		argv = append(argv, "--profile", c.Profile)
	}
	return append(argv, "gateway", "ensure")
}

// SessionEnvironment preserves native home selection and credentials in the
// ordinary provider environment. It never clones a home or opens an auth file.
func SessionEnvironment(base []string, origins []string) []string {
	selected := map[string]string{}
	for _, entry := range origins {
		key, _, ok := strings.Cut(entry, "=")
		if ok && (key == "HERMES_HOME" || key == "HERMES_GATEWAY_LOCK_DIR" || key == "XDG_STATE_HOME") {
			selected[key] = entry
		}
	}
	out := make([]string, 0, len(base)+len(selected))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if key == "HERMES_TUI_GATEWAY_URL" || key == "HERMES_TUI_RESUME" || key == "HERMES_TUI_INITIAL_QUERY" {
			continue
		}
		if _, ok := selected[key]; ok {
			continue
		}
		out = append(out, entry)
	}
	for _, key := range []string{"HERMES_HOME", "HERMES_GATEWAY_LOCK_DIR", "XDG_STATE_HOME"} {
		if entry, ok := selected[key]; ok {
			out = append(out, entry)
		}
	}
	return out
}

func WriteDescriptor(dir string, d Descriptor) error { return writeRecord(dir, DescriptorFile, d) }
func ReadDescriptor(dir string) (Descriptor, error) {
	var d Descriptor
	if err := privateNode(filepath.Join(dir, DescriptorFile), 0); err != nil {
		return d, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, DescriptorFile))
	if err != nil {
		return d, err
	}
	if len(raw) > 16384 {
		return d, errors.New("hermes descriptor is oversized")
	}
	err = json.Unmarshal(raw, &d)
	if err == nil && (d.WorkerID == "" || len(d.Generation) != 32 || !SupportedVersion(d.ProviderVersion) || d.ShimPID <= 0 || d.ShimStartTime <= 0 || !ValidSessionID(d.Binding.SessionID) || d.Binding.OwnerPID <= 0 || d.Binding.OwnerStartTime <= 0 || d.Binding.InstanceID == "" || d.Binding.AuthorityEpoch <= 0 || d.Binding.CodeSHA == "" || !filepath.IsAbs(d.Binding.Home) || !filepath.IsAbs(d.Binding.ControlHome)) {
		err = errors.New("invalid hermes native descriptor")
	}
	return d, err
}

func writeRecord(dir, name string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".hermes-record-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), filepath.Join(dir, name)); err != nil {
		return err
	}
	root, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return root.Sync()
}

// Ensure uses the provider's normal persistent gateway lifecycle. No gateway
// process belongs to this shim, and closing a viewer never signals the owner.
func Ensure(ctx context.Context, cfg SessionConfig, env []string) (Binding, error) {
	var b Binding
	if !SupportedVersion(cfg.ProviderVersion) || cfg.Program == "" {
		return b, errors.New("hermes native input runtime is unqualified")
	}
	argv := cfg.EnsureArgv()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = SessionEnvironment(env, cfg.Origins)
	cmd.Dir = cfg.Cwd
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return b, fmt.Errorf("hermes gateway ensure failed: %w", err)
	}
	if stdout.Len() > 65536 {
		return b, errors.New("hermes ensure response is oversized")
	}
	var result struct {
		State     string `json:"state"`
		ClientSHA string `json:"client_code_sha"`
		Endpoint  struct {
			Profile     string `json:"profile_id"`
			ControlHome string `json:"control_home"`
			Instance    string `json:"instance_id"`
			Epoch       int64  `json:"authority_epoch"`
			Protocol    int    `json:"runtime_protocol"`
			SHA         string `json:"code_sha"`
		} `json:"endpoint"`
	}
	if json.Unmarshal(stdout.Bytes(), &result) != nil || result.State != "ready" || result.Endpoint.Protocol != 1 || result.Endpoint.Instance == "" || result.Endpoint.Epoch <= 0 || result.Endpoint.SHA == "" || result.Endpoint.SHA != result.ClientSHA {
		return b, errors.New("hermes native gateway identity is unproven; use native setup or refresh")
	}
	b = Binding{Home: result.Endpoint.Profile, ControlHome: result.Endpoint.ControlHome, InstanceID: result.Endpoint.Instance, AuthorityEpoch: result.Endpoint.Epoch, CodeSHA: result.Endpoint.SHA}
	if b.ControlHome == "" {
		b.ControlHome = b.Home
	}
	var e endpoint
	if err := control(ctx, b.ControlHome, "identify", map[string]any{}, &e); err != nil {
		return b, err
	}
	b.OwnerPID = e.PID
	start, err := procstart.StartTime(b.OwnerPID)
	if err != nil || start <= 0 {
		return b, errors.New("hermes native owner start identity unavailable")
	}
	b.OwnerStartTime = start
	_, err = identify(ctx, b)
	return b, err
}

// Prepare creates/resumes one canonical native session and persists its exact
// owner/shim binding before the viewer is spawned. It submits no message.
func Prepare(ctx context.Context, cfg SessionConfig, env []string, dir string, pid int, start int64) (Descriptor, error) {
	var d Descriptor
	if cfg.WorkerID == "" || cfg.Cwd == "" {
		return d, errors.New("hermes native session requires worker and cwd")
	}
	cwd, err := filepath.EvalSymlinks(cfg.Cwd)
	if err != nil {
		return d, errors.New("hermes native session cwd is unavailable")
	}
	cfg.Cwd = cwd
	b, err := Ensure(ctx, cfg, env)
	if err != nil {
		return d, err
	}
	c, err := connect(ctx, b)
	if err != nil {
		return d, err
	}
	defer func() { _ = c.CloseNow() }()
	var status struct {
		Configured bool `json:"provider_configured"`
	}
	if err = rpc(ctx, c, 1, "setup.status", map[string]any{}, &status); err != nil {
		return d, err
	}
	if !status.Configured {
		return d, errors.New("hermes native provider is not configured; use normal Hermes setup once for this home")
	}
	params := map[string]any{"request_id": "swarm:" + cfg.WorkerID, "source": "tui", "cwd": cfg.Cwd}
	for _, key := range []string{"provider", "model", "reasoning", "toolsets", "skills"} {
		if v := cfg.Options[key]; v != "" {
			if key == "toolsets" || key == "skills" {
				params[key] = strings.Split(v, ",")
			} else {
				params[key] = v
			}
		}
	}
	if cfg.Options["yolo"] == "true" {
		params["yolo"] = true
	}
	method := "session.create"
	if cfg.ResumeID != "" {
		method = "session.resume"
		params = map[string]any{"session_id": cfg.ResumeID}
	}
	var snapshot Snapshot
	if err = rpc(ctx, c, 2, method, params, &snapshot); err != nil {
		return d, err
	}
	if !ValidSessionID(snapshot.SessionID) || strings.HasPrefix(cfg.ResumeID, "local-") && snapshot.SessionID != cfg.ResumeID || snapshot.AuthorityEpoch != b.AuthorityEpoch || snapshot.Info.Cwd != cfg.Cwd {
		return d, errors.New("hermes native session binding is unproven")
	}
	b.SessionID = snapshot.SessionID
	b.Directory = snapshot.Info.Cwd
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return d, err
	}
	d = Descriptor{WorkerID: cfg.WorkerID, Generation: hex.EncodeToString(nonce[:]), ProviderVersion: cfg.ProviderVersion, ShimPID: pid, ShimStartTime: start, Binding: b}
	if err = WriteDescriptor(dir, d); err != nil {
		return d, err
	}
	return d, nil
}
