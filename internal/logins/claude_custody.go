package logins

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/Nathandela/swarm/internal/processcontain"
	"github.com/Nathandela/swarm/internal/procstart"
)

const (
	ClaudeProfileCustodySchemaVersion  = 10
	NativeWriterSchemaVersion          = 3
	ClaudeNativeWriterSchemaVersion    = 4
	ClaudeReservationSchemaVersion     = 5
	ClaudeChatReservationSchemaVersion = 6
	claudeChatRequiredRuntime          = "claude-chat"
	NativeProcessFile                  = "native-process.json"
	NativeStoppedFile                  = "native-writers-stopped.json"
	ClaudeRetirementVersion            = 1
	ClaudeRetirementStopped            = "native-stopped"
	ClaudeRetirementNoFork             = "issuer-before-fork"
)

// NativeProcessInfo is the exact process scope retained by the per-session shim.
// The final three fields are present only on owned Claude schema4 records.
type NativeProcessInfo struct {
	SchemaVersion    int     `json:"schema_version"`
	Generation       string  `json:"generation"`
	BackendPID       int     `json:"backend_pid,omitempty"`
	BackendStartTime int64   `json:"backend_start_time,omitempty"`
	PID              int     `json:"pid"`
	PGID             int     `json:"pgid"`
	StartTime        int64   `json:"start_time"`
	ShimPID          int     `json:"shim_pid"`
	ShimStartTime    int64   `json:"shim_start_time"`
	Provider         string  `json:"provider"`
	Runtime          Runtime `json:"runtime"`
	IncidentID       string  `json:"incident_id,omitempty"`
	SessionID        string  `json:"session_id,omitempty"`
	Cwd              string  `json:"cwd,omitempty"`
	CLIFingerprint   string  `json:"cli_fingerprint,omitempty"`
}

// NativeStoppedInfo binds positive containment to one exact native scope.
type NativeStoppedInfo struct {
	SchemaVersion        int               `json:"schema_version"`
	Native               NativeProcessInfo `json:"native"`
	WritersStopped       bool              `json:"writers_stopped"`
	DescendantsContained bool              `json:"descendants_contained"`
}

// ClaudeReservationRetirement permanently revokes one immutable reservation;
// it authorizes payload deletion, never a new native launch.
type ClaudeReservationRetirement struct {
	Version        int                     `json:"version"`
	SessionID      string                  `json:"session_id"`
	Runtime        Runtime                 `json:"runtime"`
	Cwd            string                  `json:"cwd"`
	CLIFingerprint string                  `json:"cli_fingerprint"`
	Source         string                  `json:"source"`
	Native         NativeProcessInfo       `json:"native"`
	Issuer         processcontain.Identity `json:"issuer"`
}

const (
	maxClaudeCustodySessions       = 1024
	maxClaudeCustodyRootEntries    = 2048
	maxClaudeCustodySessionEntries = 256
	maxClaudeCustodyBytes          = 16 << 20
)

type claudeCLIIdentity struct {
	Path        string `json:"path"`
	Version     string `json:"version"`
	Fingerprint string `json:"fingerprint"`
}

type claudeSessionMeta struct {
	HermesNative    bool                         `json:"hermes_native"`
	SchemaVersion   int                          `json:"schema_version"`
	ID              string                       `json:"id"`
	AgentType       string                       `json:"agent_type"`
	RequiredRuntime string                       `json:"required_runtime"`
	InputEmbargo    string                       `json:"input_embargo"`
	OpenCodeBinding *json.RawMessage             `json:"opencode_binding"`
	NativeProvider  string                       `json:"native_provider"`
	Cwd             string                       `json:"cwd"`
	AgentCwd        string                       `json:"agent_cwd"`
	ShimPID         int                          `json:"shim_pid"`
	ShimStartTime   int64                        `json:"shim_start_time"`
	Runtime         *Runtime                     `json:"login_runtime"`
	CLI             *claudeCLIIdentity           `json:"cli_identity"`
	Retirement      *ClaudeReservationRetirement `json:"claude_retirement"`
}

func (m claudeSessionMeta) effectiveCWD() string {
	if m.AgentCwd != "" {
		return m.AgentCwd
	}
	return m.Cwd
}

// persist imports logins; this local routing projection cannot import persist.
func validClaudeSessionID(id string) bool {
	if id == "" || len(id) > 128 || id == "." || id == ".." || id[0] == '-' {
		return false
	}
	for _, c := range id {
		if c != '.' && c != '_' && c != '-' && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// IsClaudeCLIFingerprint accepts installation observation formats, not native
// executable qualification, authentication or permission to start a process.
func IsClaudeCLIFingerprint(fp string) bool {
	if validDigest(fp) {
		return true
	}
	parts := strings.Split(fp, ":")
	return len(parts) == 3 && parts[0] == "sha256" && validDigest(parts[1]) && validDigest(parts[2])
}

func (m claudeSessionMeta) fingerprint() string {
	if m.CLI != nil {
		return m.CLI.Fingerprint
	}
	return ""
}

func claudeLegacyRuntime(stateRoot string, m claudeSessionMeta) bool {
	return m.Runtime != nil && m.AgentType == ProviderClaude && m.Runtime.Provider == ProviderClaude && !m.Runtime.ExternalAuth && m.Runtime.Home == filepath.Join(stateRoot, "logins", "provider", "claude", "native")
}

func claudeRuntimeObject(raw []byte) error {
	doc, err := claudeAuthObject(raw, "provider", "epoch", "identity", "home", "external_auth")
	if err != nil {
		return err
	}
	return claudeNonNull(doc)
}

func claudeNonNull(doc map[string]json.RawMessage) error {
	for _, raw := range doc {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return ErrUnsafePath
		}
	}
	return nil
}

func claudeNativeObject(raw []byte) (map[string]json.RawMessage, error) {
	doc, err := claudeAuthObject(raw, "schema_version", "generation", "backend_pid", "backend_start_time", "pid", "pgid", "start_time", "shim_pid", "shim_start_time", "provider", "runtime", "incident_id", "session_id", "cwd", "cli_fingerprint")
	if err != nil {
		return nil, err
	}
	return doc, claudeNonNull(doc)
}

func parseClaudeSessionMeta(raw []byte, stateRoot, id string) (claudeSessionMeta, error) {
	var m claudeSessionMeta
	doc, err := jsonObject(raw)
	if err != nil {
		return m, ErrUnsafePath
	}
	routing := []string{"schema_version", "id", "agent_type", "cwd", "agent_cwd", "shim_pid", "shim_start_time", "login_runtime", "cli_identity", "claude_retirement", "required_runtime", "input_embargo", "opencode_binding", "native_provider", "hermes_native"}
	for name := range doc {
		for _, key := range routing {
			if name != key && strings.EqualFold(name, key) {
				return m, ErrUnsafePath
			}
		}
	}
	for _, key := range []string{"schema_version", "id", "agent_type", "cwd", "agent_cwd", "shim_pid", "shim_start_time"} {
		if bytes.Equal(bytes.TrimSpace(doc[key]), []byte("null")) {
			return m, ErrUnsafePath
		}
	}
	if json.Unmarshal(raw, &m) != nil || !validClaudeSessionID(id) || m.ID != id || m.SchemaVersion < 0 || m.SchemaVersion > ClaudeChatReservationSchemaVersion || m.SchemaVersion == 2 || !claudeAuthProcessTuple(processcontain.Identity{PID: m.ShimPID, StartTime: m.ShimStartTime}) && !claudeUnrelatedHermesMetadata(m) {
		return m, ErrUnsafePath
	}
	if m.Runtime != nil {
		if claudeRuntimeObject(doc["login_runtime"]) != nil || m.Runtime.Provider != m.AgentType || m.Runtime.Epoch == 0 || !validDigest(m.Runtime.Identity) || !filepath.IsAbs(m.Runtime.Home) || filepath.Clean(m.Runtime.Home) != m.Runtime.Home || strings.ContainsAny(m.Runtime.Home, "\x00\r\n") {
			return m, ErrUnsafePath
		}
	}
	if m.CLI != nil {
		if _, err := claudeAuthObject(doc["cli_identity"], "path", "version", "fingerprint"); err != nil {
			return m, ErrUnsafePath
		}
	}
	if claudeFakeChatMetadata(m) {
		if !validClaudeCWDs([]string{m.Cwd}) || !validClaudeCWDs([]string{m.effectiveCWD()}) {
			return m, ErrUnsafePath
		}
		return m, nil
	}
	if m.HermesNative {
		if !claudeUnrelatedHermesMetadata(m) {
			return m, ErrUnsafePath
		}
		return m, nil
	}
	owned := m.Runtime != nil && IsClaudeProfileRuntime(stateRoot, *m.Runtime)
	if m.SchemaVersion == ClaudeReservationSchemaVersion || m.SchemaVersion == ClaudeChatReservationSchemaVersion || m.RequiredRuntime == claudeChatRequiredRuntime || owned || m.Retirement != nil {
		terminalLegacy := claudeLegacyRuntime(stateRoot, m) && m.Retirement != nil
		if !claudeOwnedMetadata(m) || (!owned && (!terminalLegacy || m.SchemaVersion == ClaudeChatReservationSchemaVersion)) || (!validClaudeCWDs([]string{m.Cwd}) || !validClaudeCWDs([]string{m.effectiveCWD()})) {
			return m, ErrUnsafePath
		}
		if owned && (m.CLI == nil || !filepath.IsAbs(m.CLI.Path) || filepath.Clean(m.CLI.Path) != m.CLI.Path || strings.ContainsAny(m.CLI.Path, "\x00\r\n") || m.CLI.Version != "2.1.310" || !IsClaudeCLIFingerprint(m.CLI.Fingerprint)) {
			return m, ErrUnsafePath
		}
		if owned && m.Retirement != nil && m.Retirement.Source == ClaudeRetirementStopped && m.Retirement.Native.SchemaVersion != ClaudeNativeWriterSchemaVersion {
			return m, ErrUnsafePath
		}
		if terminalLegacy && m.Retirement.Source == ClaudeRetirementStopped && m.Retirement.Native.SchemaVersion != NativeWriterSchemaVersion {
			return m, ErrUnsafePath
		}
		if m.Retirement != nil {
			if err := validateClaudeRetirement(doc["claude_retirement"], m); err != nil {
				return m, err
			}
		}
	} else if m.Runtime != nil && m.SchemaVersion != 4 {
		return m, ErrUnsafePath
	}
	return m, nil
}

func parseClaudeNative(raw []byte) (NativeProcessInfo, error) {
	var n NativeProcessInfo
	doc, err := claudeNativeObject(raw)
	if err != nil || claudeRuntimeObject(doc["runtime"]) != nil || json.Unmarshal(raw, &n) != nil {
		return n, ErrUnsafePath
	}
	if (n.SchemaVersion != NativeWriterSchemaVersion && n.SchemaVersion != ClaudeNativeWriterSchemaVersion) || !validID(n.Generation) || n.ShimPID <= 0 || n.ShimStartTime <= 0 || !claudeAuthProcessTuple(processcontain.Identity{PID: n.PID, StartTime: n.StartTime}) || !claudeAuthProcessTuple(processcontain.Identity{PID: n.BackendPID, StartTime: n.BackendStartTime}) || n.PGID != n.PID || len(n.IncidentID) > 128 || strings.ContainsAny(n.IncidentID, "/\\\x00\r\n") {
		return n, ErrUnsafePath
	}
	if n.SchemaVersion == NativeWriterSchemaVersion && (n.SessionID != "" || n.Cwd != "" || n.CLIFingerprint != "") {
		return n, ErrUnsafePath
	}
	if n.SchemaVersion == ClaudeNativeWriterSchemaVersion && (n.Provider != ProviderClaude || !validClaudeSessionID(n.SessionID) || !validClaudeCWDs([]string{n.Cwd}) || !IsClaudeCLIFingerprint(n.CLIFingerprint) || n.BackendPID != 0) {
		return n, ErrUnsafePath
	}
	return n, nil
}

func validateClaudeRetirement(raw []byte, m claudeSessionMeta) error {
	doc, err := claudeAuthObject(raw, "version", "session_id", "runtime", "cwd", "cli_fingerprint", "source", "native", "issuer")
	if err != nil || claudeRuntimeObject(doc["runtime"]) != nil {
		return ErrUnsafePath
	}
	if _, err := claudeAuthObject(doc["issuer"], "pid", "start_time"); err != nil {
		return ErrUnsafePath
	}
	marker := m.Retirement
	if marker == nil || marker.Version != ClaudeRetirementVersion || marker.SessionID != m.ID || marker.Runtime != *m.Runtime || marker.Cwd != m.effectiveCWD() || marker.CLIFingerprint != m.fingerprint() {
		return ErrUnsafePath
	}
	switch marker.Source {
	case ClaudeRetirementStopped:
		n, err := parseClaudeNative(doc["native"])
		if err != nil || n != marker.Native || !claudeNativeMatchesMeta(n, m) || marker.Issuer != (processcontain.Identity{}) {
			return ErrUnsafePath
		}
	case ClaudeRetirementNoFork:
		nativeDoc, err := claudeNativeObject(doc["native"])
		if err != nil || claudeRuntimeObject(nativeDoc["runtime"]) != nil {
			return ErrUnsafePath
		}
		if marker.Native != (NativeProcessInfo{}) || marker.Issuer.PID <= 0 || marker.Issuer.StartTime <= 0 || m.ShimPID != 0 {
			return ErrUnsafePath
		}
	default:
		return ErrUnsafePath
	}
	return nil
}

func claudeNativeMatchesMeta(n NativeProcessInfo, m claudeSessionMeta) bool {
	if claudeFakeChatMetadata(m) {
		return false
	}
	expected := Runtime{}
	if m.Runtime != nil {
		expected = *m.Runtime
	}
	if n.Provider != m.AgentType || n.Runtime != expected {
		return false
	}
	if n.SchemaVersion == ClaudeNativeWriterSchemaVersion {
		return claudeOwnedMetadata(m) && n.SessionID == m.ID && n.Cwd == m.effectiveCWD() && m.CLI != nil && n.CLIFingerprint == m.CLI.Fingerprint && (m.ShimPID == 0 || n.ShimPID == m.ShimPID && n.ShimStartTime == m.ShimStartTime)
	}
	return m.ShimPID > 0 && n.ShimPID == m.ShimPID && n.ShimStartTime == m.ShimStartTime
}

func claudeProcessAlive(pid int, start int64) bool {
	if pid <= 0 {
		return false
	}
	observed, err := procstart.StartTime(pid)
	if err != nil {
		probe := syscall.Kill(pid, 0)
		return !errors.Is(probe, os.ErrNotExist) && !errors.Is(probe, syscall.ESRCH)
	}
	return start <= 0 || observed == start
}

func openClaudeCustodyState(stateRoot string) (*os.Root, error) {
	if checkAbsolute(stateRoot, true) != nil {
		return nil, ErrUnsafePath
	}
	before, err := os.Lstat(stateRoot)
	if err != nil || !before.IsDir() {
		return nil, ErrUnsafePath
	}
	owner, ok := before.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Getuid()) || before.Mode().Perm()&0022 != 0 {
		return nil, ErrUnsafePath
	}
	state, err := os.OpenRoot(stateRoot)
	if err != nil {
		return nil, err
	}
	after, err := state.Lstat(".")
	if err != nil || !os.SameFile(before, after) {
		_ = state.Close()
		return nil, ErrUnsafePath
	}
	return state, nil
}

func readClaudeCustody(state *os.Root, name string, limit int64, bytes *int64) ([]byte, error) {
	raw, err := readPrivate(state, name, limit)
	if err != nil {
		return nil, err
	}
	*bytes += int64(len(raw))
	if *bytes > maxClaudeCustodyBytes {
		return nil, ErrCapacity
	}
	return raw, nil
}

func readClaudeMeta(state *os.Root, stateRoot, id string, bytes *int64) (claudeSessionMeta, error) {
	raw, err := readClaudeCustody(state, filepath.Join(id, "meta.json"), 1<<20, bytes)
	if err != nil {
		return claudeSessionMeta{}, err
	}
	return parseClaudeSessionMeta(raw, stateRoot, id)
}

func claudeStoppedScope(state *os.Root, m claudeSessionMeta, bytes *int64) (NativeProcessInfo, error) {
	raw, err := readClaudeCustody(state, filepath.Join(m.ID, NativeProcessFile), 64<<10, bytes)
	if err != nil {
		return NativeProcessInfo{}, err
	}
	native, err := parseClaudeNative(raw)
	if err != nil || !claudeNativeMatchesMeta(native, m) {
		return native, ErrUnsafePath
	}
	if m.Retirement != nil {
		if m.Retirement.Source != ClaudeRetirementStopped || m.Retirement.Native != native {
			return native, ErrUnsafePath
		}
		return native, nil
	}
	raw, err = readClaudeCustody(state, filepath.Join(m.ID, NativeStoppedFile), 64<<10, bytes)
	if err != nil {
		return native, err
	}
	doc, err := claudeAuthObject(raw, "schema_version", "native", "writers_stopped", "descendants_contained")
	if err != nil {
		return native, ErrUnsafePath
	}
	proofNative, err := parseClaudeNative(doc["native"])
	if err != nil || proofNative != native {
		return native, ErrUnsafePath
	}
	var stopped NativeStoppedInfo
	if json.Unmarshal(raw, &stopped) != nil || stopped.SchemaVersion != native.SchemaVersion || stopped.Native != native {
		return native, ErrUnsafePath
	}
	if !stopped.WritersStopped || (runtime.GOOS == "linux" || native.SchemaVersion == ClaudeNativeWriterSchemaVersion) && !stopped.DescendantsContained {
		return native, ErrPending
	}
	if claudeProcessAlive(native.ShimPID, native.ShimStartTime) || claudeProcessAlive(native.PID, native.StartTime) || claudeProcessAlive(native.BackendPID, native.BackendStartTime) {
		return native, ErrPending
	}
	for _, pgid := range []int{native.PGID, native.BackendPID} {
		if pgid > 0 && syscall.Kill(-pgid, 0) != syscall.ESRCH {
			return native, ErrPending
		}
	}
	return native, nil
}

// VerifyClaudeNativeWritersStopped uses private durable routing and exact scope
// proof, including an unpublished native PID; display process status is not proof.
func VerifyClaudeNativeWritersStopped(stateRoot, sessionID string) error {
	state, err := openClaudeCustodyState(stateRoot)
	if err != nil {
		return err
	}
	defer func() { _ = state.Close() }()
	bytes := int64(0)
	m, err := readClaudeMeta(state, stateRoot, sessionID, &bytes)
	if err != nil {
		return err
	}
	if m.Retirement != nil {
		_, err := state.Lstat(filepath.Join(sessionID, NativeProcessFile))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	_, err = claudeStoppedScope(state, m, &bytes)
	if errors.Is(err, os.ErrNotExist) {
		return ErrPending
	}
	return err
}

func (s *Store) claudeAuthorityMutationAllowed(r Registry, login Login) error {
	if login.Provider != ProviderClaude {
		return nil
	}
	return s.claudeMutationAllowed(r, &login)
}

func (s *Store) claudeCommonMutationAllowed(r Registry) error { return s.claudeMutationAllowed(r, nil) }

// A nil login denotes the common native identity file shared by legacy writers.
func (s *Store) claudeMutationAllowed(r Registry, login *Login) error {
	state, err := openClaudeCustodyState(s.StateRoot())
	if err != nil {
		return err
	}
	defer func() { _ = state.Close() }()
	dir, err := state.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	entries, err := dir.ReadDir(maxClaudeCustodyRootEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(entries) > maxClaudeCustodyRootEntries {
		return ErrCapacity
	}
	sessions, bytes := 0, int64(0)
	for _, entry := range entries {
		info, err := state.Lstat(entry.Name())
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrUnsafePath
		}
		if !info.IsDir() {
			if !info.Mode().IsRegular() && info.Mode()&os.ModeSocket == 0 {
				return ErrUnsafePath
			}
			continue
		}
		if !privateInfo(info, true) {
			return ErrUnsafePath
		}
		switch entry.Name() {
		case "logins", "journal", "idempotency", "devices", "supervision", "remote", "upgrade", "accounts":
			continue
		}
		sessions++
		if sessions > maxClaudeCustodySessions {
			return ErrCapacity
		}
		if !validClaudeSessionID(entry.Name()) {
			return ErrUnsafePath
		}
		session, err := openPrivateDir(state, entry.Name())
		if err != nil {
			return err
		}
		file, err := session.Open(".")
		if err != nil {
			_ = session.Close()
			return err
		}
		children, readErr := file.ReadDir(maxClaudeCustodySessionEntries + 1)
		_ = file.Close()
		_ = session.Close()
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		if len(children) > maxClaudeCustodySessionEntries {
			return ErrCapacity
		}
		m, err := readClaudeMeta(state, s.StateRoot(), entry.Name(), &bytes)
		if errors.Is(err, os.ErrNotExist) {
			return ErrPending
		}
		if err != nil {
			return err
		}
		if m.AgentType == "" {
			return ErrPending
		}
		if claudeOwnedMetadata(m) && r.SchemaVersion < ClaudeProfileCustodySchemaVersion {
			return ErrPending
		}
		if m.AgentType != ProviderClaude {
			continue
		}
		if m.Runtime != nil {
			if m.Runtime.ExternalAuth {
				return ErrUnsafePath
			}
			if IsClaudeProfileRuntime(s.StateRoot(), *m.Runtime) {
				if _, known := r.ClaudeProfiles[m.Runtime.Identity]; !known {
					return ErrPending
				}
			} else if m.Runtime.Home != s.Home(ProviderClaude) {
				return ErrUnsafePath
			}
		}
		foreign := m.Runtime != nil && ((login != nil && m.Runtime.Identity != login.Identity) || (login == nil && m.Runtime.Home != s.Home(ProviderClaude)))
		if foreign {
			if _, err := state.Lstat(filepath.Join(m.ID, NativeProcessFile)); err == nil {
				raw, err := readClaudeCustody(state, filepath.Join(m.ID, NativeProcessFile), 64<<10, &bytes)
				if err != nil {
					return err
				}
				native, err := parseClaudeNative(raw)
				if err != nil || !claudeNativeMatchesMeta(native, m) {
					return ErrUnsafePath
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			continue
		}
		if m.Retirement != nil {
			if err := verifyClaudeRetiredPayload(state, m, &bytes); err != nil {
				return err
			}
			continue
		}
		if _, err := claudeStoppedScope(state, m, &bytes); errors.Is(err, os.ErrNotExist) {
			return ErrPending
		} else if err != nil {
			return err
		}
	}
	return nil
}

func verifyClaudeRetiredPayload(state *os.Root, m claudeSessionMeta, bytes *int64) error {
	if _, err := state.Lstat(filepath.Join(m.ID, NativeProcessFile)); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if m.Retirement.Source != ClaudeRetirementStopped {
		return ErrUnsafePath
	}
	_, err := claudeStoppedScope(state, m, bytes)
	return err
}

// A fresh handle must settle visible registry authority before consuming it.
// Call only under the registry flock after routing identifies managed Claude.
func (s *Store) settleClaudeRegistry() error {
	if s == nil {
		return ErrPending
	}
	syncDir := s.writeOps.syncDir
	if syncDir == nil {
		syncDir = func(string) error { return syncRootDir(s.root, ".") }
	}
	if err := syncDir("."); err != nil {
		s.uncertain = true
		return ErrDurabilityUncertain
	}
	return nil
}

func withClaudeMetadataRegistry(stateRoot string, fn func(*os.Root, *Store, *Registry) error) error {
	state, err := openClaudeCustodyState(stateRoot)
	if err != nil {
		return err
	}
	defer func() { _ = state.Close() }()
	if _, err := state.Lstat("logins"); errors.Is(err, os.ErrNotExist) {
		return fn(state, nil, nil)
	} else if err != nil {
		return err
	}
	store, err := OpenExisting(stateRoot)
	if errors.Is(err, os.ErrNotExist) {
		return ErrPending
	}
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	anchor, err := state.Lstat("logins")
	if err != nil || !os.SameFile(anchor, store.anchor) {
		return ErrUnsafePath
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.locked(func() error {
		r, err := store.load()
		if err != nil {
			return err
		}
		return fn(state, store, &r)
	})
}

func claudeSameRuntimeMeta(a, b claudeSessionMeta) bool {
	return a.ID == b.ID && a.AgentType == b.AgentType && a.Cwd == b.Cwd && a.AgentCwd == b.AgentCwd && a.Runtime != nil && b.Runtime != nil && *a.Runtime == *b.Runtime && ((a.CLI == nil && b.CLI == nil) || (a.CLI != nil && b.CLI != nil && *a.CLI == *b.CLI))
}

// This is a strict parsed metadata pair, not launch or writer-stop authority.
func claudeOwnedMetadata(m claudeSessionMeta) bool {
	return !m.HermesNative && m.AgentType == ProviderClaude && m.Runtime != nil && m.Runtime.Provider == ProviderClaude && (m.SchemaVersion == ClaudeReservationSchemaVersion && m.RequiredRuntime == "" || m.SchemaVersion == ClaudeChatReservationSchemaVersion && m.RequiredRuntime == claudeChatRequiredRuntime)
}

// Both runtimes use schema 5; only this exact unrelated runtime bypasses Claude custody.
func claudeUnrelatedHermesMetadata(m claudeSessionMeta) bool {
	return m.SchemaVersion == 5 && m.HermesNative && m.AgentType == "hermes" && m.CLI != nil && m.CLI.Version != "" && m.Runtime == nil && m.Retirement == nil && m.RequiredRuntime == "" && m.OpenCodeBinding == nil && m.InputEmbargo == "" && m.NativeProvider == ""
}

func claudeFakeChatMetadata(m claudeSessionMeta) bool {
	return m.SchemaVersion == ClaudeChatReservationSchemaVersion && m.RequiredRuntime == claudeChatRequiredRuntime && m.AgentType == "fake" && !m.HermesNative && m.Runtime == nil && m.CLI == nil && m.Retirement == nil && m.InputEmbargo == "" && m.OpenCodeBinding == nil && m.NativeProvider == ""
}

// ValidateClaudeChatFixtureMetadata validates storage shape only, never launch or custody.
func ValidateClaudeChatFixtureMetadata(raw []byte, id string) error {
	m, err := parseClaudeSessionMeta(raw, "", id)
	if err != nil {
		return err
	}
	if !claudeFakeChatMetadata(m) {
		return ErrUnsafePath
	}
	return nil
}

func claudeFakeChatTransition(old, next claudeSessionMeta) error {
	if !claudeFakeChatMetadata(old) && !claudeFakeChatMetadata(next) {
		return nil
	}
	if !claudeFakeChatMetadata(old) || !claudeFakeChatMetadata(next) || old.ID != next.ID || old.Cwd != next.Cwd || old.AgentCwd != next.AgentCwd || old.ShimPID > 0 && (next.ShimPID != old.ShimPID || next.ShimStartTime != old.ShimStartTime) {
		return ErrConflict
	}
	return nil
}

func claudeSameImmutableMeta(a, b claudeSessionMeta) bool {
	return claudeOwnedMetadata(a) && claudeOwnedMetadata(b) && a.SchemaVersion == b.SchemaVersion && a.RequiredRuntime == b.RequiredRuntime && claudeSameRuntimeMeta(a, b)
}

// Absence is meaningful only while bootstrap cannot publish login storage.
// Initialized roots use their registry fence without acquiring this inode lock.
func withClaudeMetadataFence(stateRoot string, unmanaged, managed func() error) error {
	if _, err := os.Lstat(filepath.Join(stateRoot, "logins")); err == nil {
		return managed()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	state, err := os.OpenRoot(stateRoot)
	if err != nil {
		return err
	}
	defer func() { _ = state.Close() }()
	fence, err := lockStateDirectory(state)
	if err != nil {
		return err
	}
	defer func() {
		if fence != nil {
			_ = fence.Close()
		}
	}()
	locked, err := fence.Stat()
	if err != nil {
		return err
	}
	current, err := os.Stat(stateRoot)
	if err != nil || !os.SameFile(locked, current) {
		return ErrUnsafePath
	}
	if _, err := state.Lstat("logins"); err == nil {
		_ = fence.Close()
		fence = nil
		return managed()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return unmanaged()
}

// WithClaudeMetadataSave guards stored and incoming ownership through the exact
// raw write/rename callback, including unrelated rows in a managed state root.
// The callback must write incoming and must not reenter Store/persist writers.
func WithClaudeMetadataSave(stateRoot, sessionID string, incoming []byte, save func() error) error {
	if save == nil || len(incoming) > 1<<20 || !validClaudeSessionID(sessionID) {
		return ErrIneligible
	}
	return withClaudeMetadataFence(stateRoot, func() error {
		if err := claudeUnmanagedRow(incoming, stateRoot, sessionID); err != nil {
			return err
		}
		old, err := readClaudeUnmanagedMeta(stateRoot, sessionID)
		if err == nil {
			if err := claudeUnmanagedRow(old, stateRoot, sessionID); err != nil {
				return err
			}
			var previous, next claudeSessionMeta
			_ = json.Unmarshal(old, &previous)
			_ = json.Unmarshal(incoming, &next)
			if err := claudeFakeChatTransition(previous, next); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return save()

	}, func() error {
		return withClaudeMetadataRegistry(stateRoot, func(state *os.Root, store *Store, registry *Registry) error {
			return store.saveClaudeMetadata(state, registry, stateRoot, sessionID, incoming, save)
		})
	})
}

func (s *Store) saveClaudeMetadata(state *os.Root, registry *Registry, stateRoot, sessionID string, incoming []byte, save func() error) error {
	next, err := parseClaudeSessionMeta(incoming, stateRoot, sessionID)
	if err != nil {
		return err
	}
	commit := func() error {
		if next.Runtime != nil && (IsClaudeProfileRuntime(stateRoot, *next.Runtime) || claudeLegacyRuntime(stateRoot, next)) {
			if err := s.settleClaudeRegistry(); err != nil {
				return err
			}
		}
		return save()
	}
	bytes := int64(0)
	old, err := readClaudeMeta(state, stateRoot, sessionID, &bytes)
	if errors.Is(err, os.ErrNotExist) {
		for _, name := range []string{NativeProcessFile, NativeStoppedFile} {
			if _, err := state.Lstat(filepath.Join(sessionID, name)); err == nil {
				return ErrPending
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if next.Retirement != nil {
			return ErrConflict
		}
		if claudeOwnedMetadata(next) && (registry == nil || registry.SchemaVersion < ClaudeProfileCustodySchemaVersion) {
			return ErrPending
		}
		return commit()
	}
	if err != nil {
		return err
	}
	if err := claudeFakeChatTransition(old, next); err != nil {
		return err
	}
	if registry != nil && old.SchemaVersion == 4 && old.AgentType == ProviderClaude {
		if !claudeLegacyRuntime(stateRoot, old) {
			return ErrPending
		}
		if next.SchemaVersion != 4 || !claudeSameRuntimeMeta(old, next) || next.Retirement != nil || old.ShimPID > 0 && (next.ShimPID != old.ShimPID || next.ShimStartTime != old.ShimStartTime) {
			return ErrConflict
		}
	}
	if claudeOwnedMetadata(old) {
		if registry == nil || registry.SchemaVersion < ClaudeProfileCustodySchemaVersion || !claudeSameImmutableMeta(old, next) {
			return ErrConflict
		}
		if old.Retirement == nil && next.Retirement != nil || old.Retirement != nil && (next.Retirement == nil || *old.Retirement != *next.Retirement) {
			return ErrConflict
		}
		if old.ShimPID > 0 && (next.ShimPID != old.ShimPID || next.ShimStartTime != old.ShimStartTime) {
			return ErrConflict
		}
		raw, err := readClaudeCustody(state, filepath.Join(sessionID, NativeProcessFile), 64<<10, &bytes)
		if err == nil {
			native, err := parseClaudeNative(raw)
			if err != nil || !claudeNativeMatchesMeta(native, old) || !claudeNativeMatchesMeta(native, next) {
				return ErrConflict
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else if claudeOwnedMetadata(next) {
		return ErrConflict
	}
	return commit()
}

// WithClaudeReservationRetirement authorizes one exact owned reservation's
// permanent revocation before the non-reentrant stamp/delete callback. Only the
// original launch stack may supply originalNoFork; restart paths must pass nil.
// A zero marker denotes positively unrelated or entirely absent session state.
func WithClaudeReservationRetirement(stateRoot, sessionID string, originalNoFork func() error, retire func(ClaudeReservationRetirement) error) error {
	if retire == nil || !validClaudeSessionID(sessionID) {
		return ErrIneligible
	}
	return withClaudeMetadataFence(stateRoot, func() error {
		old, err := readClaudeUnmanagedMeta(stateRoot, sessionID)
		if errors.Is(err, os.ErrNotExist) {
			if _, err := os.Lstat(filepath.Join(stateRoot, sessionID)); errors.Is(err, os.ErrNotExist) {
				return retire(ClaudeReservationRetirement{})
			}
			if err := claudeUnmanagedExecutionOnly(stateRoot, sessionID); err != nil {
				return err
			}
			return retire(ClaudeReservationRetirement{})
		}
		if err != nil {
			return err
		}
		if err := claudeUnmanagedRow(old, stateRoot, sessionID); err != nil {
			return err
		}
		return retire(ClaudeReservationRetirement{})

	}, func() error {
		return withClaudeMetadataRegistry(stateRoot, func(state *os.Root, store *Store, registry *Registry) error {
			bytes := int64(0)
			m, err := readClaudeMeta(state, stateRoot, sessionID, &bytes)
			if errors.Is(err, os.ErrNotExist) {
				if _, err := state.Lstat(sessionID); errors.Is(err, os.ErrNotExist) {
					return retire(ClaudeReservationRetirement{})
				}
				return ErrPending
			}
			if err != nil {
				return err
			}
			return store.retireClaudeReservation(state, registry, m, originalNoFork, retire)
		})
	})
}

func (s *Store) retireClaudeReservation(state *os.Root, registry *Registry, m claudeSessionMeta, originalNoFork func() error, retire func(ClaudeReservationRetirement) error) error {
	if s == nil {
		return ErrPending
	}
	bytes := int64(0)
	legacy := m.SchemaVersion == 4 && m.AgentType == ProviderClaude
	if legacy && !claudeLegacyRuntime(s.StateRoot(), m) {
		return ErrPending
	}
	if !claudeOwnedMetadata(m) && !legacy {
		return retire(ClaudeReservationRetirement{})
	}
	if registry == nil || (!legacy && registry.SchemaVersion < ClaudeProfileCustodySchemaVersion) {
		return ErrPending
	}
	publish := func(marker ClaudeReservationRetirement) error {
		if legacy && registry.SchemaVersion < ClaudeProfileCustodySchemaVersion {
			if registry.Revision == ^uint64(0) {
				return ErrIneligible
			}
			promoted := *registry
			promoted.SchemaVersion = ClaudeProfileCustodySchemaVersion
			promoted.Revision++
			if err := s.save(promoted); err != nil {
				return err
			}
			*registry = promoted
		} else if err := s.settleClaudeRegistry(); err != nil {
			return err
		}
		return retire(marker)
	}

	if m.Retirement != nil {
		if err := verifyClaudeRetiredPayload(state, m, &bytes); err != nil {
			return err
		}
		return publish(*m.Retirement)
	}
	marker := ClaudeReservationRetirement{Version: ClaudeRetirementVersion, SessionID: m.ID, Runtime: *m.Runtime, Cwd: m.effectiveCWD(), CLIFingerprint: m.fingerprint()}
	native, stopErr := claudeStoppedScope(state, m, &bytes)
	if stopErr == nil {
		marker.Source, marker.Native = ClaudeRetirementStopped, native
		return publish(marker)
	}
	if originalNoFork == nil {
		if errors.Is(stopErr, os.ErrNotExist) {
			return ErrPending
		}
		return stopErr
	}
	if m.ShimPID != 0 {
		return ErrPending
	}
	for _, name := range []string{NativeProcessFile, NativeStoppedFile} {
		if _, err := state.Lstat(filepath.Join(m.ID, name)); err == nil {
			return ErrPending
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := originalNoFork(); err != nil {
		return err
	}
	started, err := procstart.StartTime(os.Getpid())
	if err != nil || started <= 0 {
		return ErrPending
	}
	marker.Source, marker.Issuer = ClaudeRetirementNoFork, processcontain.Identity{PID: os.Getpid(), StartTime: started}
	return publish(marker)
}

// Unmanaged persistence retains its historical path/schema semantics only after
// bounded routing inspection positively excludes an owned-profile reservation.
func claudeUnmanagedRow(raw []byte, stateRoot, sessionID string) error {
	doc, err := jsonObject(raw)
	if err != nil {
		return ErrUnsafePath
	}
	var version int
	for key := range doc {
		for _, field := range []string{"schema_version", "login_runtime", "claude_retirement"} {
			if key != field && strings.EqualFold(key, field) {
				return ErrUnsafePath
			}
		}
	}
	if json.Unmarshal(doc["schema_version"], &version) != nil && len(doc["schema_version"]) != 0 {
		return ErrUnsafePath
	}
	var candidate claudeSessionMeta
	_ = json.Unmarshal(raw, &candidate)
	if version == ClaudeChatReservationSchemaVersion || candidate.RequiredRuntime == claudeChatRequiredRuntime {
		return ValidateClaudeChatFixtureMetadata(raw, sessionID)
	}
	if candidate.HermesNative {
		parsed, err := parseClaudeSessionMeta(raw, stateRoot, sessionID)
		if err != nil || !claudeUnrelatedHermesMetadata(parsed) {
			return ErrUnsafePath
		}
		return nil
	}
	if version >= ClaudeReservationSchemaVersion {
		return ErrPending
	}
	if _, known := doc["claude_retirement"]; known {
		return ErrPending
	}
	var rt *Runtime
	if rawRT, exists := doc["login_runtime"]; exists {
		if json.Unmarshal(rawRT, &rt) != nil {
			return ErrUnsafePath
		}
	}
	if rt != nil {
		canonical, err := filepath.EvalSymlinks(stateRoot)
		if err != nil {
			return err
		}
		canonical, err = filepath.Abs(canonical)
		if err != nil {
			return err
		}
		if rt.Home == filepath.Join(canonical, "logins", "provider", "claude", "native") {
			return ErrPending
		}
	}
	if rt != nil && rt.Provider == ProviderClaude && strings.Contains(filepath.ToSlash(rt.Home), "/provider/claude/profiles/") {
		return ErrPending
	}
	return nil
}

func readClaudeUnmanagedMeta(stateRoot, id string) ([]byte, error) {
	f, err := os.OpenFile(filepath.Join(stateRoot, id, "meta.json"), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, ErrUnsafePath
	}
	raw, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil || len(raw) > 1<<20 {
		return nil, ErrUnsafePath
	}
	return raw, nil
}

func (s *Store) claudeSavedWriteAllowed(r Registry, login Login) error {
	if login.Provider != ProviderClaude {
		return nil
	}
	if find(&r, login.ID) == nil {
		if _, err := s.root.Lstat(s.savedPath(login)); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
	}
	return s.claudeAuthorityMutationAllowed(r, login)
}

func (s *Store) claudeAuthWriteAllowed(id string) error {
	r, err := s.load()
	if err != nil {
		return err
	}
	if login := find(&r, id); login != nil {
		if login.Provider != ProviderClaude {
			return ErrIneligible
		}
		return s.claudeAuthorityMutationAllowed(r, *login)
	}
	raw, err := readPrivate(s.root, filepath.Join("saved", id, "credential.json"), maxCredentialBytes*2)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var saved savedCredential
	if validateJSON(raw) != nil || json.Unmarshal(raw, &saved) != nil || saved.Revision == 0 || !validDigest(saved.Identity) {
		return ErrInvalidCredentials
	}
	return s.claudeAuthorityMutationAllowed(r, Login{ID: id, Provider: ProviderClaude, Identity: saved.Identity, CredentialRevision: saved.Revision})
}

func claudeUnmanagedExecutionOnly(stateRoot, id string) error {
	root, err := os.OpenRoot(filepath.Join(stateRoot, id))
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat(".")
	if err != nil || !privateInfo(info, true) {
		return ErrUnsafePath
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	entries, err := dir.ReadDir(3)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(entries) == 0 || len(entries) > 2 {
		return ErrPending
	}
	for _, entry := range entries {
		// These are the two actual replaceExecution filenames in persist.
		if entry.Name() != "execution.json" && entry.Name() != "execution.pending" {
			return ErrPending
		}
		info, err := root.Lstat(entry.Name())
		if err != nil || !privateInfo(info, false) {
			return ErrUnsafePath
		}
	}
	return nil
}

func (s *Store) claudeSettledRemovalRef(r Registry, login Login) (ClaudeAuthRef, error) {
	intent, err := s.readClaudeAuthIntent(login.ID)
	if errors.Is(err, os.ErrNotExist) {
		return ClaudeAuthRef{}, nil
	}
	if err != nil {
		return ClaudeAuthRef{}, err
	}
	if claudeAuthTarget(r, intent.Ref) != nil {
		return ClaudeAuthRef{}, ErrConflict
	}
	if intent.Phase != "ready" && intent.Phase != "failed" {
		return ClaudeAuthRef{}, ErrInUse
	}
	if intent.Scope != (ClaudeAuthScope{}) && !intent.UnitRetired {
		return ClaudeAuthRef{}, ErrPending
	}
	if err := s.proveClaudeAuthStopped(intent); err != nil {
		return ClaudeAuthRef{}, err
	}
	if err := s.claudeAuthPayloadEmpty(login.ID); err != nil {
		return ClaudeAuthRef{}, err
	}
	return intent.Ref, nil
}

func (s *Store) orphanSavedLogin(id string) (Login, error) {
	raw, err := readPrivate(s.root, filepath.Join("saved", id, "credential.json"), maxCredentialBytes*2)
	if errors.Is(err, os.ErrNotExist) {
		return Login{}, errUnknownSavedData
	}
	if err != nil {
		return Login{}, err
	}
	var saved savedCredential
	if validateJSON(raw) != nil || json.Unmarshal(raw, &saved) != nil || saved.Revision == 0 || !validDigest(saved.Identity) {
		return Login{}, ErrInvalidCredentials
	}
	login := Login{ID: id, Identity: saved.Identity, CredentialRevision: saved.Revision}
	if len(saved.Credential.Identity) > 0 {
		identity, err := ClaudeIdentity(saved.Credential.Identity)
		if err != nil || identity != saved.Identity {
			return Login{}, ErrConflict
		}
		login.Provider = ProviderClaude
	} else if saved.AuthOwner != nil {
		login.Provider = ProviderCodex
	} else if len(saved.Credential.Auth) > 0 {
		identity, err := CodexIdentity(saved.Credential.Auth)
		if err != nil || identity != saved.Identity {
			return Login{}, ErrConflict
		}
		login.Provider = ProviderCodex
	} else {
		return Login{}, errUnknownSavedData
	}
	if intent, err := s.readClaudeAuthIntent(id); err == nil {
		if login.Provider != ProviderClaude || intent.Ref.Identity != login.Identity || intent.Ref.CredentialRevision != login.CredentialRevision {
			return Login{}, ErrConflict
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Login{}, err
	}
	return login, nil
}
