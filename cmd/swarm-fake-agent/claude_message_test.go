package main

import (
	"bytes"
	"encoding/json"
	"github.com/Nathandela/swarm/internal/adapter"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestClaudePrimaryFixtureEchoesOneTurnAndWritesOwnTranscript(t *testing.T) {
	root := t.TempDir()
	if info, err := os.Stat(root); err == nil {
		t.Logf("fixture directory initial mode=%#o", info.Mode().Perm())
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SWARM_CLAUDE_PRIMARY_FIXTURE_DIR", root)
	const session = "8e4c6620-bfdd-43c3-93de-e69a2f97a01f"
	const user = "1389ef09-4c19-4d50-8fdd-1fc95bdcfd4a"
	text := "hello\nsecond\r\t\"雪"
	raw, err := json.Marshal(map[string]any{"type": "user", "uuid": user, "session_id": session, "parent_tool_use_id": nil, "message": map[string]any{"role": "user", "content": []map[string]string{{"type": "text", "text": text}}}})
	if err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := run([]string{"--claude-primary-fixture", "--session-id=" + session}, bytes.NewReader(append(raw, '\n')), &out, &stderr); code != 0 {
		t.Fatalf("real dev input refused: code=%d", code)
	}
	lines := bytes.Split(bytes.TrimSuffix(out.Bytes(), []byte{'\n'}), []byte{'\n'})
	if len(lines) != 4 || !bytes.Equal(lines[1], raw) {
		t.Fatal("fixture did not echo the exact user bytes once")
	}
	var assistant struct {
		Type    string `json:"type"`
		UUID    string `json:"uuid"`
		Session string `json:"session_id"`
		Parent  any    `json:"parent_tool_use_id"`
		Message struct {
			ID      string `json:"id"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(lines[2], &assistant) != nil || assistant.Type != "assistant" || !adapter.IsCanonicalConversationID(assistant.UUID) || assistant.Message.ID != assistant.UUID || assistant.Session != session || assistant.Parent != nil || assistant.Message.Role != "assistant" || len(assistant.Message.Content) != 1 || assistant.Message.Content[0].Type != "text" || assistant.Message.Content[0].Text != "fixture reply: "+text {
		t.Fatal("fixture assistant is not its own complete main reply")
	}
	var result struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		UUID    string `json:"uuid"`
		Session string `json:"session_id"`
		IsError bool   `json:"is_error"`
		Result  string `json:"result"`
	}
	if json.Unmarshal(lines[3], &result) != nil || result.Type != "result" || result.Subtype != "success" || result.IsError || !adapter.IsCanonicalConversationID(result.UUID) || result.UUID == assistant.UUID || result.Session != session || result.Result != "fixture reply: "+text {
		t.Fatal("fixture result missing or unbound")
	}
	path := filepath.Join(root, "claude-fixture-transcript.jsonl")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 512<<10 {
		t.Fatal("fixture did not own a bounded private transcript", err)
	}
	transcript, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rows := bytes.Split(bytes.TrimSuffix(transcript, []byte{'\n'}), []byte{'\n'})
	if len(rows) != 3 {
		t.Fatalf("transcript rows=%d", len(rows))
	}
	for i, want := range []struct{ kind, uuid, parent string }{{"user", user, ""}, {"assistant", assistant.UUID, user}, {"result", result.UUID, assistant.UUID}} {
		var row struct {
			V       int             `json:"v"`
			Type    string          `json:"type"`
			UUID    string          `json:"uuid"`
			Parent  *string         `json:"parentUuid"`
			Session string          `json:"sessionId"`
			Message json.RawMessage `json:"message"`
			Result  string          `json:"result"`
		}
		if json.Unmarshal(rows[i], &row) != nil || row.V != 1 || row.Type != want.kind || row.UUID != want.uuid || row.Session != session || want.parent == "" && row.Parent != nil || want.parent != "" && (row.Parent == nil || *row.Parent != want.parent) {
			t.Fatal("genuine transcript parent chain mismatch")
		}
		if i == 2 && row.Result != result.Result {
			t.Fatal("transcript result differs from stdout")
		}
		if i < 2 {
			var native map[string]json.RawMessage
			if json.Unmarshal(lines[i+1], &native) != nil {
				t.Fatal("bad native message")
			}
			var a, b any
			if json.Unmarshal(row.Message, &a) != nil || json.Unmarshal(native["message"], &b) != nil || !reflect.DeepEqual(a, b) {
				t.Fatal("transcript message was fabricated or altered")
			}
		}
	}
}

func TestClaudePrimaryFixtureRefusesOversizeBeforeEcho(t *testing.T) {
	for _, size := range []int{16 << 10, (16 << 10) + 1} {
		t.Run(map[bool]string{true: "fits", false: "over"}[size == 16<<10], func(t *testing.T) {
			root := t.TempDir()
			if info, err := os.Stat(root); err == nil {
				t.Logf("fixture directory initial mode=%#o", info.Mode().Perm())
			}
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("SWARM_CLAUDE_PRIMARY_FIXTURE_DIR", root)
			text := strings.Repeat("\x01", size) // worst-case JSON escaping, not raw-size estimation
			raw, _ := json.Marshal(map[string]any{"type": "user", "uuid": "1389ef09-4c19-4d50-8fdd-1fc95bdcfd4a", "session_id": "8e4c6620-bfdd-43c3-93de-e69a2f97a01f", "parent_tool_use_id": nil, "message": map[string]any{"role": "user", "content": []map[string]string{{"type": "text", "text": text}}}})
			var out, stderr bytes.Buffer
			code := run([]string{"--claude-primary-fixture", "--session-id=8e4c6620-bfdd-43c3-93de-e69a2f97a01f"}, bytes.NewReader(append(raw, '\n')), &out, &stderr)
			if size == 16<<10 {
				if code != 0 || bytes.Count(out.Bytes(), []byte{'\n'}) != 4 {
					t.Fatal("bounded escaped turn refused", code)
				}
				info, err := os.Stat(filepath.Join(root, "claude-fixture-transcript.jsonl"))
				if err != nil || info.Size() > 512<<10 {
					t.Fatal("encoded transcript cap violated")
				}
			} else {
				if code != 3 || bytes.Count(out.Bytes(), []byte{'\n'}) != 1 {
					t.Fatal("oversize turn emitted native effects", code)
				}
				if raw, err := os.ReadFile(filepath.Join(root, "claude-fixture-transcript.jsonl")); err == nil && len(raw) != 0 {
					t.Fatal("oversize input entered transcript")
				}
			}
		})
	}
}
