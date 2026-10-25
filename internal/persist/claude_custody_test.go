package persist

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/procstart"
)

func claudeRetainedMetaFixture(t *testing.T, retired bool) (*Store, Meta, []byte) {
	t.Helper()
	s, root := newTestStore(t)
	loginStore, err := logins.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = loginStore.Close() })
	// This metadata-only fixture has no account or grant. Set only the
	// existing registry schema floor; operational tests use public Select instead.
	registryPath := filepath.Join(root, "logins", "registry.json")
	registryRaw, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	var registryFields map[string]json.RawMessage
	if err := json.Unmarshal(registryRaw, &registryFields); err != nil {
		t.Fatal(err)
	}
	registryFields["schema_version"], _ = json.Marshal(logins.ClaudeProfileCustodySchemaVersion)
	registryRaw, _ = json.Marshal(registryFields)
	if err := os.WriteFile(registryPath, registryRaw, 0600); err != nil {
		t.Fatal(err)
	}
	identity := strings.Repeat("a", 64)
	home, err := loginStore.ClaudeProfileHome(identity)
	if err != nil {
		t.Fatal(err)
	}
	m := fullMeta()
	m.SchemaVersion = logins.ClaudeReservationSchemaVersion
	m.ShimPID, m.ShimStartTime = 0, 0
	m.Cwd = filepath.Join(root, "project")
	m.LoginRuntime = &logins.Runtime{Provider: logins.ProviderClaude, Epoch: 1, Identity: identity, Home: home}
	m.CLIIdentity = &CLIIdentity{Path: "/synthetic/claude", Version: "2.1.310", Fingerprint: strings.Repeat("b", 64)}
	raw, _ := json.Marshal(m)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if retired {
		native := logins.NativeProcessInfo{SchemaVersion: logins.ClaudeNativeWriterSchemaVersion, Generation: strings.Repeat("c", 32), ShimPID: 999999999, ShimStartTime: 1, Provider: logins.ProviderClaude, Runtime: *m.LoginRuntime, SessionID: m.ID, Cwd: m.Cwd, CLIFingerprint: m.CLIIdentity.Fingerprint}
		auth := logins.ClaudeReservationRetirement{Version: 1, SessionID: m.ID, Runtime: *m.LoginRuntime, Cwd: m.Cwd, CLIFingerprint: m.CLIIdentity.Fingerprint, Source: logins.ClaudeRetirementStopped, Native: native}
		fields["claude_retirement"], _ = json.Marshal(auth)
	}
	raw, _ = json.Marshal(fields)
	if err := os.Mkdir(filepath.Join(root, m.ID), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, m.ID, metaFile), raw, 0600); err != nil {
		t.Fatal(err)
	}
	return s, m, raw
}

func TestClaudeCustodySaveCannotStripStoredOwnership(t *testing.T) {
	for _, stripped := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing-retirement", true: "plain-stale-writer"}[stripped], func(t *testing.T) {
			s, m, before := claudeRetainedMetaFixture(t, true)
			m.SchemaVersion = 1
			if stripped {
				m.LoginRuntime = nil
				m.CLIIdentity = nil
			}
			if err := s.Save(m); err == nil {
				t.Error("stale metadata writer erased retained owned custody")
			}
			after, err := os.ReadFile(filepath.Join(s.root, m.ID, metaFile))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("refused metadata mutation changed stored bytes: %v", err)
			}
		})
	}
}

func TestClaudeCustodyLowerDeleteRetainsUnresolvedReservation(t *testing.T) {
	s, m, before := claudeRetainedMetaFixture(t, false)
	payload := filepath.Join(s.root, m.ID, "payload")
	if err := os.WriteFile(payload, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(m.ID); err == nil {
		t.Error("lower Delete erased an unresolved owned reservation")
	}
	after, err := os.ReadFile(filepath.Join(s.root, m.ID, metaFile))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("unresolved reservation metadata was removed: %v", err)
	}
	if _, err := os.Stat(payload); err != nil {
		t.Fatalf("unresolved reservation payload was removed: %v", err)
	}
}

// Loading preserves the exact retirement value for later metadata writers; it
// does not by itself validate writer death or grant deletion authority.
func TestClaudeCustodySchemaFiveRoundTripRetainsRoutingAndRetirement(t *testing.T) {
	s, want, raw := claudeRetainedMetaFixture(t, true)
	got, err := s.Load(want.ID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var before, after map[string]json.RawMessage
	if err := json.Unmarshal(raw, &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &after); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"schema_version", "login_runtime", "cli_identity", "claude_retirement"} {
		if !bytes.Equal(before[key], after[key]) {
			t.Fatalf("schema5 lost %s: before=%s after=%s", key, before[key], after[key])
		}
	}
}

func TestClaudeCustodySchemaFiveRejectsOtherRuntimeShapes(t *testing.T) {
	for _, kind := range []string{"missing-runtime", "external-auth", "codex", "future"} {
		t.Run(kind, func(t *testing.T) {
			_, m, raw := claudeRetainedMetaFixture(t, false)
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(raw, &fields)
			switch kind {
			case "missing-runtime":
				delete(fields, "login_runtime")
			case "external-auth":
				m.LoginRuntime.ExternalAuth = true
				fields["login_runtime"], _ = json.Marshal(m.LoginRuntime)
			case "codex":
				m.LoginRuntime.Provider = logins.ProviderCodex
				fields["agent_type"], _ = json.Marshal(logins.ProviderCodex)
				fields["login_runtime"], _ = json.Marshal(m.LoginRuntime)
			case "future":
				fields["schema_version"] = json.RawMessage("6")
			}
			raw, _ = json.Marshal(fields)
			if _, err := decodeMeta(raw, m.ID); err == nil {
				t.Fatal("invalid schema5 runtime decoded")
			}
		})
	}
}

func TestClaudeCustodyRetirementRetainsExactScopeAfterProofPayloadRemoval(t *testing.T) {
	s, m, _ := claudeRetainedMetaFixture(t, false)
	// The synthetic process has no children. Capture and join its real identity;
	// this fixture never uses an installed native or claims native qualification.
	child := exec.Command("/bin/sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	started, err := procstart.StartTime(child.Process.Pid)
	_ = child.Process.Kill()
	_ = child.Wait()
	if err != nil {
		t.Fatal(err)
	}
	native := logins.NativeProcessInfo{SchemaVersion: logins.ClaudeNativeWriterSchemaVersion, Generation: strings.Repeat("d", 32), ShimPID: child.Process.Pid, ShimStartTime: started, Provider: logins.ProviderClaude, Runtime: *m.LoginRuntime, SessionID: m.ID, Cwd: m.Cwd, CLIFingerprint: m.CLIIdentity.Fingerprint}
	proof := logins.NativeStoppedInfo{SchemaVersion: logins.ClaudeNativeWriterSchemaVersion, Native: native, WritersStopped: true, DescendantsContained: true}
	for name, value := range map[string]any{logins.NativeProcessFile: native, logins.NativeStoppedFile: proof} {
		raw, _ := json.Marshal(value)
		if err := os.WriteFile(filepath.Join(s.root, m.ID, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	retired, err := s.RetireClaudeReservation(m.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if retired.ClaudeRetirement == nil || retired.ClaudeRetirement.Native != native || retired.SchemaVersion != ClaudeSchemaVersion {
		t.Fatal("retirement lost exact scope")
	}
	for _, name := range []string{logins.NativeProcessFile, logins.NativeStoppedFile} {
		if err := os.Remove(filepath.Join(s.root, m.ID, name)); err != nil {
			t.Fatal(err)
		}
	}
	// This is the crash/partial-delete retry shape: only retained authorization
	// remains. Ordinary metadata writes must preserve it before retrying Delete.
	retired.Name = "retained-cleanup"
	if err := s.Save(retired); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Load(m.ID)
	if err != nil || loaded.ClaudeRetirement == nil || *loaded.ClaudeRetirement != *retired.ClaudeRetirement {
		t.Fatalf("retirement changed on ordinary save: %v", err)
	}
	if err := s.Delete(m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(s.root, m.ID)); !os.IsNotExist(err) {
		t.Fatalf("retired retry retained payload: %v", err)
	}
}

func TestClaudeCustodyRetirementStampFailurePreservesUnretiredBytes(t *testing.T) {
	s, m, before := claudeRetainedMetaFixture(t, false)
	original := writeTemp
	t.Cleanup(func() { writeTemp = original })
	writeTemp = func(*os.File, []byte) (int, error) { return 0, syscall.ENOSPC }
	calls := 0
	_, err := s.RetireClaudeReservation(m.ID, func() error { calls++; return nil })
	if !errors.Is(err, syscall.ENOSPC) || calls != 1 {
		t.Fatalf("stamp failure: calls=%d err=%v", calls, err)
	}
	after, err := os.ReadFile(filepath.Join(s.root, m.ID, metaFile))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("failed stamp changed reservation: %v", err)
	}
	writeTemp = original
	if err := s.Delete(m.ID); err == nil {
		t.Fatal("failed stamp granted deletion authority")
	}
}
