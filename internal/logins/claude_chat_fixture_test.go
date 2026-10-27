package logins

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestClaudeChatFakeSixProjectionNeverGrantsManagedCustody(t *testing.T) {
	s := testStore(t)
	root := s.StateRoot()
	id := "fake-chat"
	dir := filepath.Join(root, id)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	fields := map[string]any{"schema_version": 6, "id": id, "agent_type": "fake", "cwd": root, "required_runtime": "claude-chat"}
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "meta.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	parsed, err := parseClaudeSessionMeta(raw, root, id)
	if err != nil {
		t.Fatalf("strict fake6 structural projection refused: %v", err)
	}
	if claudeOwnedMetadata(parsed) || parsed.Runtime != nil || parsed.CLI != nil || parsed.Retirement != nil {
		t.Fatal("fake6 pair conferred managed custody")
	}
	for _, version := range []int{NativeWriterSchemaVersion, ClaudeNativeWriterSchemaVersion} {
		unowned := parsed
		unowned.ShimPID, unowned.ShimStartTime = 123, 1
		native := NativeProcessInfo{SchemaVersion: version, Provider: "fake", ShimPID: 123, ShimStartTime: 1, SessionID: id}
		if claudeNativeMatchesMeta(native, unowned) {
			t.Fatal("fake6 matched native custody")
		}
	}
	before := snapshot(t, s).SchemaVersion
	fields["name"] = "fixture label"
	next, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	if err := WithClaudeMetadataSave(root, id, next, func() error { calls++; return os.WriteFile(path, next, 0600) }); err != nil || calls != 1 {
		t.Fatalf("unmanaged fake6 update: calls=%d err=%v", calls, err)
	}
	if snapshot(t, s).SchemaVersion != before {
		t.Fatal("fixture promoted global credential schema")
	}
	retired := false
	if err := WithClaudeReservationRetirement(root, id, nil, func(marker ClaudeReservationRetirement) error {
		if marker != (ClaudeReservationRetirement{}) {
			t.Error("fixture manufactured managed retirement authority")
		}
		retired = true
		return nil
	}); err != nil || !retired {
		t.Fatalf("unmanaged fixture classification: called=%t err=%v", retired, err)
	}
	for _, name := range []string{NativeProcessFile, NativeStoppedFile} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("fixture manufactured native custody %s: %v", name, err)
		}
	}
}

func TestClaudeChatFixtureStorageFencesExactModeInBothRoots(t *testing.T) {
	for _, initialized := range []bool{false, true} {
		for _, kind := range []string{"downgrade", "ordinary-header-chat", "drop-mode", "other-mode", "provider", "login", "cli", "retirement", "opencode", "embargo", "native-claim", "mode-alias", "duplicate-mode", "wrong-id", "cwd", "agent-cwd", "shim-pid", "shim-start", "clear-tuple"} {
			t.Run(map[bool]string{false: "unmanaged/", true: "initialized/"}[initialized]+kind, func(t *testing.T) {
				root, err := filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				if initialized {
					root = testStore(t).StateRoot()
				}
				const id = "fake-chat"
				dir := filepath.Join(root, id)
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(dir, "meta.json")
				fields := map[string]any{"schema_version": 6, "id": id, "agent_type": "fake", "cwd": root, "required_runtime": "claude-chat"}
				if kind == "shim-pid" || kind == "shim-start" || kind == "clear-tuple" {
					fields["shim_pid"], fields["shim_start_time"] = 123, 1
				}
				before, err := json.Marshal(fields)
				if err != nil {
					t.Fatal(err)
				}
				if err := WithClaudeMetadataSave(root, id, before, func() error { return os.WriteFile(path, before, 0600) }); err != nil {
					t.Fatalf("valid fake6 baseline refused: %v", err)
				}
				switch kind {
				case "downgrade":
					fields["schema_version"] = 1
					delete(fields, "required_runtime")
				case "ordinary-header-chat":
					fields["schema_version"] = 1
				case "drop-mode":
					delete(fields, "required_runtime")
				case "other-mode":
					fields["required_runtime"] = "other"
				case "provider":
					fields["agent_type"] = "claude"
				case "login":
					fields["login_runtime"] = map[string]any{"provider": "claude"}
				case "cli":
					fields["cli_identity"] = map[string]any{"path": "/synthetic/claude"}
				case "retirement":
					fields["claude_retirement"] = map[string]any{"version": 1}
				case "opencode":
					fields["opencode_binding"] = map[string]any{"revision": 1}
				case "embargo":
					fields["input_embargo"] = "pending"
				case "native-claim":
					fields["native_provider"] = "claude"
				case "mode-alias":
					fields["Required_Runtime"] = fields["required_runtime"]
					delete(fields, "required_runtime")
				case "wrong-id":
					fields["id"] = "other-fake"
				case "cwd":
					fields["cwd"] = filepath.Join(root, "changed-cwd")
				case "agent-cwd":
					fields["agent_cwd"] = filepath.Join(root, "changed-agent-cwd")
				case "shim-pid":
					fields["shim_pid"] = 124
				case "shim-start":
					fields["shim_start_time"] = 2
				case "clear-tuple":
					fields["shim_pid"], fields["shim_start_time"] = 0, 0
				}
				next, err := json.Marshal(fields)
				if err != nil {
					t.Fatal(err)
				}
				if kind == "duplicate-mode" {
					next = append(bytes.TrimSuffix(next, []byte("}")), []byte(`,"required_runtime":"claude-chat"}`)...)
				}
				calls := 0
				err = WithClaudeMetadataSave(root, id, next, func() error { calls++; return nil })
				if err == nil || calls != 0 {
					t.Fatalf("fake6 mutation reached write callback: calls=%d err=%v", calls, err)
				}
				actual, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(actual, before) {
					t.Fatal("refused fake6 mutation changed stored row", err)
				}
				// A plain fake1 document is valid on its own; only the stored fake6 fence forbids its downgrade.
				if kind != "downgrade" {
					if err := os.WriteFile(path, next, 0600); err != nil {
						t.Fatal(err)
					}
					calls = 0
					standaloneValid := kind == "cwd" || kind == "agent-cwd" || kind == "shim-pid" || kind == "shim-start" || kind == "clear-tuple"
					err = WithClaudeReservationRetirement(root, id, nil, func(marker ClaudeReservationRetirement) error {
						calls++
						if marker != (ClaudeReservationRetirement{}) {
							t.Error("unmanaged standalone row gained managed retirement authority")
						}
						return nil
					})
					if standaloneValid {
						// Valid standalone shape, but changing the stored binding was forbidden.
						if err != nil || calls != 1 {
							t.Fatalf("valid standalone fake6 misclassified as malformed: calls=%d err=%v", calls, err)
						}
					} else if err == nil || calls != 0 {
						t.Fatalf("malformed fake6 reached delete callback: calls=%d err=%v", calls, err)
					}
					for _, name := range []string{NativeProcessFile, NativeStoppedFile} {
						if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
							t.Fatal("fixture mutation manufactured native proof", err)
						}
					}
					actual, err = os.ReadFile(path)
					if err != nil || !bytes.Equal(actual, next) {
						t.Fatal("malformed fixture was rewritten during refusal", err)
					}
				}
			})
		}
	}
}

func TestClaudeChatFixturePublishesThenRetainsShimTupleInBothRoots(t *testing.T) {
	for _, initialized := range []bool{false, true} {
		t.Run(map[bool]string{false: "unmanaged", true: "initialized"}[initialized], func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if initialized {
				root = testStore(t).StateRoot()
			}
			const id = "fake-chat"
			dir := filepath.Join(root, id)
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "meta.json")
			fields := map[string]any{"schema_version": 6, "id": id, "agent_type": "fake", "cwd": root, "required_runtime": "claude-chat", "shim_pid": 0, "shim_start_time": 0}
			// Synthetic tuple data only; this never observes or claims a native process.
			for _, stage := range []string{"zero", "publish", "stable"} {
				if stage == "publish" {
					fields["shim_pid"], fields["shim_start_time"] = 123, 1
				}
				if stage == "stable" {
					fields["name"] = "same established tuple"
				}
				raw, err := json.Marshal(fields)
				if err != nil {
					t.Fatal(err)
				}
				calls := 0
				err = WithClaudeMetadataSave(root, id, raw, func() error { calls++; return os.WriteFile(path, raw, 0600) })
				if err != nil || calls != 1 {
					t.Fatalf("%s tuple write refused: calls=%d err=%v", stage, calls, err)
				}
				actual, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(actual, raw) {
					t.Fatal("tuple write did not publish exact bytes", err)
				}
				parsed, err := parseClaudeSessionMeta(actual, root, id)
				if err != nil || claudeOwnedMetadata(parsed) || parsed.Runtime != nil || parsed.CLI != nil || parsed.Retirement != nil {
					t.Fatal("fake tuple became managed or malformed", err)
				}
				wantPID, wantStart := 0, int64(0)
				if stage != "zero" {
					wantPID, wantStart = 123, 1
				}
				if parsed.ShimPID != wantPID || parsed.ShimStartTime != wantStart {
					t.Fatal("shim tuple was lost or changed")
				}
			}
			calls := 0
			err = WithClaudeReservationRetirement(root, id, nil, func(marker ClaudeReservationRetirement) error {
				calls++
				if marker != (ClaudeReservationRetirement{}) {
					t.Error("established fake tuple manufactured managed retirement marker")
				}
				return nil
			})
			if err != nil || calls != 1 {
				t.Fatalf("established fake row classification: calls=%d err=%v", calls, err)
			}
			for _, name := range []string{NativeProcessFile, NativeStoppedFile} {
				if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
					t.Fatal("fake tuple manufactured native proof", err)
				}
			}
		})
	}
}
