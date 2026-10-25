package shim

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

// claimClaudeReservation is called only inside ClaimClaudeProfileCustody's
// registry fence. A durable claim grants cleanup custody, never Start authority.
func (s *managedNativeScope) claimClaudeReservation(cfg Config) error {
	if s == nil || s.ownPreclaimDurable || s.recorded || s.startAttempted {
		return errLoginResolution
	}
	if err := s.validateClaudeReservation(cfg); err != nil {
		return err
	}
	for _, name := range []string{NativeProcessFile, NativeStoppedFile} {
		if _, err := s.root.Lstat(name); !os.IsNotExist(err) {
			return errLoginResolution
		}
	}
	expected := s.info
	if err := writeNativeRecord(s.root, NativeProcessFile, expected); err != nil {
		return err
	}
	// A rename whose directory sync failed remains uncertain. Only the fully
	// successful writer grants this in-memory issuer permission to finish.
	s.preclaim = expected
	s.ownPreclaimDurable = true
	return nil
}

func (s *managedNativeScope) validateClaudeStart(cfg Config) error {
	if s == nil || !s.ownPreclaimDurable || s.recorded || s.startAttempted || s.info != s.preclaim {
		return errLoginResolution
	}
	if err := s.validateClaudeReservation(cfg); err != nil {
		return err
	}
	if err := s.verifyOwnedRecord(); err != nil {
		return err
	}
	if _, err := s.root.Lstat(NativeStoppedFile); !os.IsNotExist(err) {
		return errLoginResolution
	}
	return nil
}

func (s *managedNativeScope) validateClaudeReservation(cfg Config) error {
	if s == nil || s.root == nil || cfg.LoginRuntime == nil || !logins.IsClaudeProfileRuntime(cfg.LoginStateRoot, *cfg.LoginRuntime) || cfg.NativeProvider != logins.ProviderClaude || cfg.Backend != nil || cfg.OpenCode != nil || cfg.LoginSwitchID != "" || cfg.LoginSwitchSource != "" || cfg.InputEmbargo != "" || !persist.ValidID(cfg.SessionID) || cfg.SessionDir != filepath.Join(cfg.LoginStateRoot, cfg.SessionID) || cfg.CLIIdentity == nil || !filepath.IsAbs(cfg.CLIIdentity.Path) || filepath.Clean(cfg.CLIIdentity.Path) != cfg.CLIIdentity.Path || strings.ContainsAny(cfg.CLIIdentity.Path, "\x00\r\n") || cfg.CLIIdentity.Version != "2.1.310" || !persist.IsCLIContentFingerprint(cfg.CLIIdentity.Fingerprint) || len(cfg.Argv) == 0 || cfg.Argv[0] != cfg.CLIIdentity.Path {
		return errLoginResolution
	}
	expected := s.info
	if expected.SchemaVersion != ClaudeNativeWriterSchemaVersion || expected.Provider != logins.ProviderClaude || expected.Runtime != *cfg.LoginRuntime || expected.SessionID != cfg.SessionID || expected.Cwd != cfg.Cwd || expected.CLIFingerprint != cfg.CLIIdentity.Fingerprint || expected.ShimPID != os.Getpid() || expected.ShimStartTime <= 0 || expected.PID != 0 || expected.PGID != 0 || expected.StartTime != 0 || expected.BackendPID != 0 || expected.BackendStartTime != 0 {
		return errLoginResolution
	}
	state, err := os.OpenRoot(cfg.LoginStateRoot)
	if err != nil {
		return errLoginResolution
	}
	defer func() { _ = state.Close() }()
	info, err := state.Lstat(cfg.SessionID)
	owner, ok := infoSys(info)
	if err != nil || !ok || !info.IsDir() || info.Mode().Perm()&0077 != 0 || owner.Uid != uint32(os.Getuid()) {
		return errLoginResolution
	}
	prepared, err := s.root.Lstat(".")
	if err != nil || !os.SameFile(info, prepared) {
		return errLoginResolution
	}
	f, err := s.root.OpenFile("meta.json", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return errLoginResolution
	}
	defer func() { _ = f.Close() }()
	info, err = f.Stat()
	owner, ok = infoSys(info)
	if err != nil || !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || owner.Uid != uint32(os.Getuid()) || owner.Nlink != 1 || info.Size() > 1<<20 {
		return errLoginResolution
	}
	raw, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 || !uniqueBirthJSON(raw) {
		return errLoginResolution
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return errLoginResolution
	}
	for key := range fields {
		lower := strings.ToLower(key)
		switch lower {
		case "schema_version", "id", "agent_type", "cwd", "agent_cwd", "login_runtime", "cli_identity", "claude_retirement", "shim_pid", "shim_start_time", "input_embargo", "required_runtime", "opencode_binding", "status":
			if lower != key {
				return errLoginResolution
			}
		}
	}
	if !sameBirthJSON(fields["login_runtime"], cfg.LoginRuntime) || !sameBirthJSON(fields["cli_identity"], cfg.CLIIdentity) {
		return errLoginResolution
	}
	var m persist.Meta
	if json.Unmarshal(raw, &m) != nil || m.SchemaVersion != persist.ClaudeSchemaVersion || m.ID != cfg.SessionID || m.AgentType != logins.ProviderClaude || m.LoginRuntime == nil || *m.LoginRuntime != *cfg.LoginRuntime || m.ProviderCwd() != cfg.Cwd || !reflect.DeepEqual(m.CLIIdentity, cfg.CLIIdentity) || m.ClaudeRetirement != nil || m.InputEmbargo != "" || m.RequiredRuntime != "" || m.OpenCodeBinding != nil || m.Status.Process != status.ProcessRunning {
		return errLoginResolution
	}
	if m.ShimPID == 0 {
		if m.ShimStartTime != 0 {
			return errLoginResolution
		}
	} else if m.ShimPID != s.info.ShimPID || m.ShimStartTime != s.info.ShimStartTime {
		return errLoginResolution
	}
	return nil
}
