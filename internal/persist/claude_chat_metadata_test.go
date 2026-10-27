package persist

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

// This is retained metadata, not a grant, authenticated process or native proof.
func claudeChatMetaFixture(t *testing.T, retired bool) (*Store, Meta, []byte) {
	t.Helper()
	s, m, raw := claudeRetainedMetaFixture(t, retired)
	if _, err := s.Load(m.ID); err != nil {
		t.Fatalf("ordinary owned fixture is invalid: %v", err)
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m.SchemaVersion, m.RequiredRuntime = 6, "claude-chat"
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.root, m.ID, metaFile), raw, 0600); err != nil {
		t.Fatal(err)
	}
	return s, m, raw
}

func TestClaudeChatManagedSixRoundTrip(t *testing.T) {
	for _, retired := range []bool{false, true} {
		t.Run(map[bool]string{false: "reservation", true: "retired"}[retired], func(t *testing.T) {
			s, want, _ := claudeChatMetaFixture(t, retired)
			got, err := s.Load(want.ID)
			if err != nil {
				t.Fatalf("exact managed chat metadata refused: %v", err)
			}
			if got.SchemaVersion != 6 || got.RequiredRuntime != "claude-chat" || !reflect.DeepEqual(got.LoginRuntime, want.LoginRuntime) || !reflect.DeepEqual(got.CLIIdentity, want.CLIIdentity) || !reflect.DeepEqual(got.ClaudeRetirement, want.ClaudeRetirement) {
				t.Fatal("chat round trip lost runtime/custody")
			}
		})
	}
}

func TestClaudeChatSaveIncomingOneKeepsSixAndSyncsDirectory(t *testing.T) {
	s, m, _ := claudeChatMetaFixture(t, false)
	original := syncMetadataDirectory
	calls := 0
	syncMetadataDirectory = func(dir string) error { calls++; return original(dir) }
	t.Cleanup(func() { syncMetadataDirectory = original })
	m.SchemaVersion, m.Name = 1, "updated chat"
	if err := s.Save(m); err != nil {
		t.Fatalf("ordinary metadata update of managed chat refused: %v", err)
	}
	got, err := s.Load(m.ID)
	if err != nil || got.SchemaVersion != 6 || got.RequiredRuntime != "claude-chat" || got.Name != m.Name || calls == 0 {
		t.Fatalf("write downgraded/lost durability: version=%d syncs=%d err=%v", got.SchemaVersion, calls, err)
	}
}

func TestClaudeChatDirectorySyncFailureRetainsSixForRetry(t *testing.T) {
	s, m, _ := claudeChatMetaFixture(t, false)
	original := syncMetadataDirectory
	fail := true
	syncMetadataDirectory = func(dir string) error {
		if fail {
			return syscall.ENOSPC
		}
		return original(dir)
	}
	t.Cleanup(func() { syncMetadataDirectory = original })
	m.SchemaVersion, m.Tag = 1, "pending-sync"
	if err := s.Save(m); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("owned chat skipped directory-sync boundary: %v", err)
	}
	got, err := s.Load(m.ID)
	if err != nil || got.SchemaVersion != 6 || got.RequiredRuntime != "claude-chat" {
		t.Fatalf("visible failed-sync row lost version6: %+v %v", got, err)
	}
	fail = false
	if err := s.Save(m); err != nil {
		t.Fatalf("exact metadata retry failed: %v", err)
	}
}

func TestClaudeChatStrictPairsAndModeImmutability(t *testing.T) {
	for _, kind := range []string{"five-chat", "six-empty", "six-other", "unmanaged", "alias"} {
		t.Run(kind, func(t *testing.T) {
			_, m, raw := claudeChatMetaFixture(t, false)
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "five-chat":
				fields["schema_version"] = json.RawMessage(`5`)
			case "six-empty":
				fields["required_runtime"] = json.RawMessage(`""`)
			case "six-other":
				fields["required_runtime"] = json.RawMessage(`"other"`)
			case "unmanaged":
				delete(fields, "login_runtime")
			case "alias":
				fields["Required_Runtime"] = fields["required_runtime"]
				delete(fields, "required_runtime")
			}
			bad, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeMeta(bad, m.ID); err == nil {
				t.Fatalf("unsupported chat shape accepted: %s", kind)
			}
		})
	}
	for _, kind := range []string{"drop-mode", "other-mode", "strip-runtime"} {
		t.Run(kind, func(t *testing.T) {
			s, m, before := claudeChatMetaFixture(t, false)
			m.SchemaVersion = 1
			switch kind {
			case "drop-mode":
				m.RequiredRuntime = ""
			case "other-mode":
				m.RequiredRuntime = "other"
			case "strip-runtime":
				m.LoginRuntime = nil
				m.CLIIdentity = nil
			}
			if err := s.Save(m); err == nil {
				t.Fatal("stored chat custody was converted in place")
			}
			after, err := os.ReadFile(filepath.Join(s.root, m.ID, metaFile))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("refusal changed metadata: %v", err)
			}
		})
	}
}

func TestClaudeChatRetirementAndExactRetryKeepSix(t *testing.T) {
	s, m, _ := claudeChatMetaFixture(t, false)
	retired, err := s.RetireClaudeReservation(m.ID, func() error { return nil })
	if err != nil || retired.SchemaVersion != 6 || retired.RequiredRuntime != "claude-chat" || retired.ClaudeRetirement == nil {
		t.Fatalf("chat retirement lost exact metadata version: %+v %v", retired, err)
	}
	before, err := os.ReadFile(filepath.Join(s.root, m.ID, metaFile))
	if err != nil {
		t.Fatal(err)
	}
	originalWrite, originalSync := writeTemp, syncMetadataDirectory
	syncs := 0
	writeTemp = func(*os.File, []byte) (int, error) { return 0, syscall.ENOSPC }
	syncMetadataDirectory = func(dir string) error { syncs++; return originalSync(dir) }
	t.Cleanup(func() { writeTemp = originalWrite; syncMetadataDirectory = originalSync })
	again, err := s.RetireClaudeReservation(m.ID, nil)
	if err != nil || again.SchemaVersion != 6 || again.ClaudeRetirement == nil || *again.ClaudeRetirement != *retired.ClaudeRetirement || syncs != 1 {
		t.Fatalf("exact chat retirement retry rewrote/downgraded marker: version=%d syncs=%d err=%v", again.SchemaVersion, syncs, err)
	}
	after, err := os.ReadFile(filepath.Join(s.root, m.ID, metaFile))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("exact retry changed committed bytes: %v", err)
	}
}

func TestClaudeChatDoesNotChangeOrdinaryMetadata(t *testing.T) {
	s, m, _ := claudeRetainedMetaFixture(t, false)
	m.SchemaVersion, m.Name = 1, "ordinary owned"
	if err := s.Save(m); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(m.ID)
	if err != nil || got.SchemaVersion != 5 || got.RequiredRuntime != "" {
		t.Fatalf("ordinary5 changed: %+v %v", got, err)
	}
	plain, _ := newTestStore(t)
	base := fullMeta()
	if err := plain.Save(base); err != nil {
		t.Fatal(err)
	}
	got, err = plain.Load(base.ID)
	if err != nil || got.SchemaVersion != 1 {
		t.Fatalf("ordinary1 changed: %+v %v", got, err)
	}
	loginStore, _ := newTestStore(t)
	login := loginMeta(filepath.Join(t.TempDir(), "native"))
	if err := loginStore.Save(login); err != nil {
		t.Fatal(err)
	}
	got, err = loginStore.Load(login.ID)
	if err != nil || got.SchemaVersion != 4 {
		t.Fatalf("ordinary login4 changed: %+v %v", got, err)
	}
	openCodeStore, _ := newTestStore(t)
	binding := plannedOpenCode()
	openCode := Meta{ID: "ordinary-opencode", AgentType: "opencode", Cwd: "/project", ConversationID: "ses_" + strings.Repeat("d", 32), RequiredRuntime: RequiredRuntimeOpenCode, OpenCodeBinding: &binding}
	if err := openCodeStore.Save(openCode); err != nil {
		t.Fatal(err)
	}
	got, err = openCodeStore.Load(openCode.ID)
	if err != nil || got.SchemaVersion != 3 {
		t.Fatalf("ordinary OpenCode3 changed: %+v %v", got, err)
	}
}
