package logins

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func claudeChatCustodyFixture(t *testing.T) (*Store, map[string]any, NativeProcessInfo) {
	t.Helper()
	s, _, meta, native := claudeCustodyFixture(t)
	raw, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseClaudeSessionMeta(raw, s.StateRoot(), "source"); err != nil {
		t.Fatalf("ordinary owned fixture invalid: %v", err)
	}
	meta["schema_version"], meta["required_runtime"] = 6, "claude-chat"
	writeClaudeCustodyJSON(t, s.StateRoot(), "source", "meta.json", meta)
	return s, meta, native
}

func TestClaudeChatCustodyRecognizesExactManagedSix(t *testing.T) {
	s, meta, native := claudeChatCustodyFixture(t)
	raw, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseClaudeSessionMeta(raw, s.StateRoot(), "source")
	if err != nil {
		t.Fatalf("exact managed6 custody refused: %v", err)
	}
	if !claudeNativeMatchesMeta(native, parsed) {
		t.Fatal("chat metadata changed the existing native schema4 custody binding")
	}
	beforeRegistry := snapshot(t, s).SchemaVersion
	meta["name"] = "updated chat label"
	updated, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	if err := WithClaudeMetadataSave(s.StateRoot(), "source", updated, func() error {
		calls++
		return os.WriteFile(filepath.Join(s.StateRoot(), "source", "meta.json"), updated, 0600)
	}); err != nil || calls != 1 {
		t.Fatalf("managed6 metadata fence refused exact update: calls=%d err=%v", calls, err)
	}
	if snapshot(t, s).SchemaVersion != beforeRegistry || ClaudeReservationSchemaVersion != 5 || ClaudeNativeWriterSchemaVersion != 4 {
		t.Fatal("chat session support changed global registry/ordinary/native proof formats")
	}
}

func TestClaudeChatCustodyModeAndPairChangesRefuseBeforeWrite(t *testing.T) {
	for _, kind := range []string{"drop-mode", "other-mode", "downgrade-five", "unmanaged", "mode-alias"} {
		t.Run(kind, func(t *testing.T) {
			s, meta, _ := claudeChatCustodyFixture(t)
			path := filepath.Join(s.StateRoot(), "source", "meta.json")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "drop-mode":
				delete(meta, "required_runtime")
			case "other-mode":
				meta["required_runtime"] = "other"
			case "downgrade-five":
				meta["schema_version"] = 5
				delete(meta, "required_runtime")
			case "unmanaged":
				delete(meta, "login_runtime")
			case "mode-alias":
				meta["Required_Runtime"] = meta["required_runtime"]
				delete(meta, "required_runtime")
			}
			raw, err := json.Marshal(meta)
			if err != nil {
				t.Fatal(err)
			}
			called := false
			err = WithClaudeMetadataSave(s.StateRoot(), "source", raw, func() error { called = true; return nil })
			if err == nil || called {
				t.Fatalf("changed chat custody reached writer: called=%t err=%v", called, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("refusal changed stored custody: %v", err)
			}
		})
	}
}

func TestClaudeChatCustodyRetirementRetainsSixAndExistingProof(t *testing.T) {
	s, meta, native := claudeChatCustodyFixture(t)
	writeClaudeCustodyJSON(t, s.StateRoot(), "source", NativeProcessFile, native)
	writeClaudeCustodyJSON(t, s.StateRoot(), "source", NativeStoppedFile, NativeStoppedInfo{SchemaVersion: ClaudeNativeWriterSchemaVersion, Native: native, WritersStopped: true, DescendantsContained: true})
	var marker ClaudeReservationRetirement
	err := WithClaudeReservationRetirement(s.StateRoot(), "source", nil, func(got ClaudeReservationRetirement) error {
		marker = got
		meta["claude_retirement"] = got
		writeClaudeCustodyJSON(t, s.StateRoot(), "source", "meta.json", meta)
		return nil
	})
	if err != nil || marker.Native != native || marker.Source != ClaudeRetirementStopped {
		t.Fatalf("chat retirement lost exact native4 evidence: %+v %v", marker, err)
	}
	for _, name := range []string{NativeProcessFile, NativeStoppedFile} {
		if err := os.Remove(filepath.Join(s.StateRoot(), "source", name)); err != nil {
			t.Fatal(err)
		}
	}
	err = WithClaudeReservationRetirement(s.StateRoot(), "source", nil, func(got ClaudeReservationRetirement) error {
		if got != marker {
			t.Fatal("partial deletion lost the exact chat retirement marker")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("chat exact-marker retry refused: %v", err)
	}
}
