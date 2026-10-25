package persist

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/procstart"
)

func legacyClaudeMetaFixture(t *testing.T, cli bool) (*Store, Meta) {
	t.Helper()
	s, m, _ := claudeRetainedMetaFixture(t, false)
	m.SchemaVersion = LoginSchemaVersion
	rt := *m.LoginRuntime
	rt.Home = filepath.Join(s.root, "logins", "provider", "claude", "native")
	m.LoginRuntime = &rt
	if cli {
		old := *m.CLIIdentity
		old.Version = "2.0.1"
		m.CLIIdentity = &old
	} else {
		m.CLIIdentity = nil
	}
	raw, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(s.root, m.ID, metaFile), raw, 0600); err != nil {
		t.Fatal(err)
	}
	return s, m
}

func TestClaudeLegacyRetirementPreservesOptionalCLIAndPartialDeleteRetry(t *testing.T) {
	for _, kind := range []string{"issuer-no-cli", "stopped-no-cli", "stopped-old-cli"} {
		t.Run(kind, func(t *testing.T) {
			s, m := legacyClaudeMetaFixture(t, kind == "stopped-old-cli")
			var noFork func() error
			if kind == "issuer-no-cli" {
				noFork = func() error { return nil }
			} else {
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
				m.ShimPID, m.ShimStartTime = child.Process.Pid, started
				raw, _ := json.Marshal(m)
				if err := os.WriteFile(filepath.Join(s.root, m.ID, metaFile), raw, 0600); err != nil {
					t.Fatal(err)
				}
				native := logins.NativeProcessInfo{SchemaVersion: logins.NativeWriterSchemaVersion, Generation: strings.Repeat("c", 32), ShimPID: m.ShimPID, ShimStartTime: m.ShimStartTime, Provider: logins.ProviderClaude, Runtime: *m.LoginRuntime}
				proof := logins.NativeStoppedInfo{SchemaVersion: logins.NativeWriterSchemaVersion, Native: native, WritersStopped: true, DescendantsContained: true}
				for name, value := range map[string]any{logins.NativeProcessFile: native, logins.NativeStoppedFile: proof} {
					raw, _ := json.Marshal(value)
					if err := os.WriteFile(filepath.Join(s.root, m.ID, name), raw, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			retired, err := s.RetireClaudeReservation(m.ID, noFork)
			if err != nil {
				t.Fatal(err)
			}
			if retired.SchemaVersion != ClaudeSchemaVersion || retired.ClaudeRetirement == nil || logins.IsClaudeProfileRuntime(s.root, *retired.LoginRuntime) {
				t.Fatal("legacy retirement became an owned profile or lost marker")
			}
			if (m.CLIIdentity == nil) != (retired.CLIIdentity == nil) || m.CLIIdentity != nil && *m.CLIIdentity != *retired.CLIIdentity {
				t.Fatal("legacy CLI observation changed")
			}
			loaded, err := s.Load(m.ID)
			if err != nil || loaded.ClaudeRetirement == nil || *loaded.ClaudeRetirement != *retired.ClaudeRetirement {
				t.Fatalf("legacy marker roundtrip: %v", err)
			}
			for _, name := range []string{logins.NativeProcessFile, logins.NativeStoppedFile} {
				if err := os.Remove(filepath.Join(s.root, m.ID, name)); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
			}
			retired.Name = "after partial payload deletion"
			if err := s.Save(retired); err != nil {
				t.Fatal(err)
			}
			if err := s.Delete(m.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(filepath.Join(s.root, m.ID)); !os.IsNotExist(err) {
				t.Fatal("legacy retained marker did not allow exact cleanup retry")
			}
		})
	}
}

func TestClaudeLegacySchemaFiveRequiresExactTerminalMarker(t *testing.T) {
	for _, kind := range []string{"no-marker", "wrong-runtime", "wrong-cwd", "wrong-native-schema"} {
		t.Run(kind, func(t *testing.T) {
			s, m := legacyClaudeMetaFixture(t, true)
			m.SchemaVersion = ClaudeSchemaVersion
			if kind != "no-marker" {
				native := logins.NativeProcessInfo{SchemaVersion: logins.NativeWriterSchemaVersion, Generation: strings.Repeat("d", 32), ShimPID: 999999999, ShimStartTime: 1, Provider: logins.ProviderClaude, Runtime: *m.LoginRuntime}
				m.ShimPID, m.ShimStartTime = native.ShimPID, native.ShimStartTime
				marker := logins.ClaudeReservationRetirement{Version: 1, SessionID: m.ID, Runtime: *m.LoginRuntime, Cwd: m.Cwd, CLIFingerprint: m.CLIIdentity.Fingerprint, Source: logins.ClaudeRetirementStopped, Native: native}
				switch kind {
				case "wrong-runtime":
					marker.Runtime.Epoch++
				case "wrong-cwd":
					marker.Cwd = "/foreign"
				case "wrong-native-schema":
					marker.Native.SchemaVersion = logins.ClaudeNativeWriterSchemaVersion
				}
				m.ClaudeRetirement = &marker
			}
			raw, _ := json.Marshal(m)
			if err := os.WriteFile(filepath.Join(s.root, m.ID, metaFile), raw, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Load(m.ID); err == nil {
				t.Fatal("unretired or mismatched common-home schema5 accepted")
			}
		})
	}
}

func TestClaudeRetirementExactExistingMarkerDoesNotRewrite(t *testing.T) {
	s, m, _ := claudeRetainedMetaFixture(t, false)
	retired, err := s.RetireClaudeReservation(m.ID, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(s.root, m.ID, metaFile))
	if err != nil {
		t.Fatal(err)
	}
	original := writeTemp
	originalSync := syncMetadataDirectory
	syncCalls := 0
	syncMetadataDirectory = func(dir string) error { syncCalls++; return originalSync(dir) }
	t.Cleanup(func() { writeTemp = original; syncMetadataDirectory = originalSync })
	writeTemp = func(*os.File, []byte) (int, error) { return 0, syscall.ENOSPC }
	again, err := s.RetireClaudeReservation(m.ID, nil)
	if errors.Is(err, syscall.ENOSPC) {
		t.Fatal("exact durable marker was rewritten on retry")
	}
	if syncCalls != 1 {
		t.Fatal("exact marker retry skipped directory settlement")
	}
	if err != nil || again.ClaudeRetirement == nil || *again.ClaudeRetirement != *retired.ClaudeRetirement {
		t.Fatalf("exact marker reentry: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(s.root, m.ID, metaFile))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("exact marker retry changed committed bytes")
	}
}

func TestClaudeLegacyNativeThreeCannotBindUnpublishedShim(t *testing.T) {
	s, m := legacyClaudeMetaFixture(t, false)
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
	native := logins.NativeProcessInfo{SchemaVersion: logins.NativeWriterSchemaVersion, Generation: strings.Repeat("e", 32), Provider: logins.ProviderClaude, Runtime: *m.LoginRuntime, ShimPID: child.Process.Pid, ShimStartTime: started}
	stopped := logins.NativeStoppedInfo{SchemaVersion: logins.NativeWriterSchemaVersion, Native: native, WritersStopped: true, DescendantsContained: true}
	for name, value := range map[string]any{logins.NativeProcessFile: native, logins.NativeStoppedFile: stopped} {
		raw, _ := json.Marshal(value)
		if err := os.WriteFile(filepath.Join(s.root, m.ID, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Delete(m.ID); err == nil {
		t.Fatal("native3 proof borrowed a zeroShim reservation")
	}
	loaded, err := s.Load(m.ID)
	if err != nil || loaded.SchemaVersion != LoginSchemaVersion || loaded.ClaudeRetirement != nil {
		t.Fatalf("unbound legacy proof changed reservation: %v", err)
	}
}

func TestClaudeLegacyFloorPromotionMarkerFailureRetainsPayload(t *testing.T) {
	for _, floor := range []int{1, 9} {
		t.Run(strconv.Itoa(floor), func(t *testing.T) {
			s, m := legacyClaudeMetaFixture(t, false)
			registryPath := filepath.Join(s.root, "logins", "registry.json")
			raw, err := os.ReadFile(registryPath)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			fields["schema_version"], _ = json.Marshal(floor)
			raw, _ = json.Marshal(fields)
			if err := os.WriteFile(registryPath, raw, 0600); err != nil {
				t.Fatal(err)
			}
			var before logins.Registry
			if err := json.Unmarshal(raw, &before); err != nil {
				t.Fatal(err)
			}
			metaPath := filepath.Join(s.root, m.ID, metaFile)
			metaBefore, err := os.ReadFile(metaPath)
			if err != nil {
				t.Fatal(err)
			}
			payload := filepath.Join(s.root, m.ID, "workspace-custody")
			if err := os.WriteFile(payload, []byte("retained"), 0600); err != nil {
				t.Fatal(err)
			}
			original := writeTemp
			t.Cleanup(func() { writeTemp = original })
			writeTemp = func(*os.File, []byte) (int, error) { return 0, syscall.ENOSPC }
			if _, err := s.RetireClaudeReservation(m.ID, func() error { return nil }); !errors.Is(err, syscall.ENOSPC) {
				t.Fatalf("marker failure: %v", err)
			}
			raw, err = os.ReadFile(registryPath)
			if err != nil {
				t.Fatal(err)
			}
			var after logins.Registry
			if err := json.Unmarshal(raw, &after); err != nil {
				t.Fatal(err)
			}
			if after.SchemaVersion != logins.ClaudeProfileCustodySchemaVersion || after.Revision != before.Revision+1 || !reflect.DeepEqual(before.Providers, after.Providers) {
				t.Fatal("proof did not durably fence old writers without changing desired selection")
			}
			if got, err := os.ReadFile(metaPath); err != nil || !bytes.Equal(got, metaBefore) {
				t.Fatal("failed marker write changed retained metadata")
			}
			writeTemp = original
			if err := s.Delete(m.ID); err == nil {
				t.Fatal("failed marker write authorized lower payload cleanup")
			}
			if got, err := os.ReadFile(payload); err != nil || string(got) != "retained" {
				t.Fatal("failed marker write erased payload")
			}
			if _, err := s.RetireClaudeReservation(m.ID, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			raw, err = os.ReadFile(registryPath)
			if err != nil {
				t.Fatal(err)
			}
			var retried logins.Registry
			if err := json.Unmarshal(raw, &retried); err != nil {
				t.Fatal(err)
			}
			if retried.Revision != after.Revision {
				t.Fatal("marker retry repeated registry promotion")
			}
			if err := s.Delete(m.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestClaudeRetirementVisibleMarkerMustSettleBeforeFreshHandleCleanup(t *testing.T) {
	s, m, _ := claudeRetainedMetaFixture(t, false)
	dir := filepath.Join(s.root, m.ID)
	payload := filepath.Join(dir, "workspace-custody")
	if err := os.WriteFile(payload, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	originalSync, originalWrite := syncMetadataDirectory, writeTemp
	t.Cleanup(func() { syncMetadataDirectory = originalSync; writeTemp = originalWrite })
	calls := 0
	syncMetadataDirectory = func(got string) error {
		calls++
		if got != dir {
			t.Fatalf("wrong directory settled: %s", got)
		}
		return syscall.ENOSPC
	}
	if _, err := s.RetireClaudeReservation(m.ID, func() error { return nil }); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("postrename sync failure: %v", err)
	}
	visible, err := os.ReadFile(filepath.Join(dir, metaFile))
	if err != nil {
		t.Fatal(err)
	}
	stamp, err := s.Load(m.ID)
	if err != nil || stamp.ClaudeRetirement == nil {
		t.Fatalf("fixture did not reach visible renamed marker: %v", err)
	}
	fresh, err := NewStore(s.root)
	if err != nil {
		t.Fatal(err)
	}
	writeTemp = func(*os.File, []byte) (int, error) {
		t.Error("retained marker was rewritten")
		return 0, syscall.ENOSPC
	}
	if _, err := fresh.RetireClaudeReservation(m.ID, nil); !errors.Is(err, syscall.ENOSPC) {
		t.Errorf("fresh handle accepted unsettled marker: %v", err)
	}
	if err := fresh.Delete(m.ID); !errors.Is(err, syscall.ENOSPC) {
		t.Errorf("unsettled marker authorized payload deletion: %v", err)
	}
	if calls != 3 {
		t.Errorf("settlement calls: got %d want 3", calls)
	}
	if got, err := os.ReadFile(payload); err != nil || string(got) != "retained" {
		t.Fatal("sync failure erased retained payload")
	}
	if got, err := os.ReadFile(filepath.Join(dir, metaFile)); err != nil || !bytes.Equal(got, visible) {
		t.Fatal("sync retry changed marker bytes")
	}
	syncMetadataDirectory = func(dir string) error { calls++; return originalSync(dir) }
	retired, err := fresh.RetireClaudeReservation(m.ID, nil)
	if err != nil || retired.ClaudeRetirement == nil || *retired.ClaudeRetirement != *stamp.ClaudeRetirement {
		t.Fatalf("settled marker retry: %v", err)
	}
	if err := os.Remove(payload); err != nil {
		t.Fatal(err)
	} // partial payload cleanup
	if err := fresh.Delete(m.ID); err != nil {
		t.Fatal(err)
	}
	if calls != 5 {
		t.Errorf("successful exact retries skipped settlement: %d", calls)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatal("settled cleanup retained session payload")
	}
}
