package skeleton

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"syscall"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func fixtureObject(raw []byte, keys ...string) (map[string]json.RawMessage, bool) {
	object, ok := decodeStrictObject(raw)
	if !ok || len(object) != len(keys) {
		return nil, false
	}
	for _, key := range keys {
		if _, ok := object[key]; !ok {
			return nil, false
		}
	}
	return object, true
}

func decodeClaudeFixtureFrame(raw []byte) (claudeFixtureFrame, error) {
	var frame claudeFixtureFrame
	object, ok := decodeStrictObject(raw)
	if !ok || json.Unmarshal(raw, &frame) != nil {
		return frame, errClaudeFixtureMessage
	}
	var keys []string
	switch frame.Type {
	case "system":
		keys = []string{"type", "subtype", "session_id", "fixture_pid", "fixture_pgid", "fixture_stdio"}
		stdio, valid := fixtureObject(object["fixture_stdio"], "stdin_tty", "stdout_tty", "stderr_tty")
		if !valid || !bytes.Equal(stdio["stdin_tty"], []byte("false")) || !bytes.Equal(stdio["stdout_tty"], []byte("false")) || !bytes.Equal(stdio["stderr_tty"], []byte("false")) {
			return frame, errClaudeFixtureMessage
		}
	case "user", "assistant":
		keys = []string{"type", "uuid", "session_id", "parent_tool_use_id", "message"}
		messageKeys := []string{"role", "content"}
		if frame.Type == "assistant" {
			messageKeys = append(messageKeys, "id")
		}
		message, valid := fixtureObject(object["message"], messageKeys...)
		var content []json.RawMessage
		if !valid || json.Unmarshal(message["content"], &content) != nil || len(content) != 1 {
			return frame, errClaudeFixtureMessage
		}
		if _, valid = fixtureObject(content[0], "type", "text"); !valid {
			return frame, errClaudeFixtureMessage
		}
	case "result":
		keys = []string{"type", "subtype", "uuid", "session_id", "is_error", "result"}
		if !bytes.Equal(object["is_error"], []byte("false")) {
			return frame, errClaudeFixtureMessage
		}
	default:
		return frame, errClaudeFixtureMessage
	}
	if _, ok = fixtureObject(raw, keys...); !ok {
		return frame, errClaudeFixtureMessage
	}
	return frame, nil
}

func (d *Daemon) readClaudeFixtureHistory(local string, s *claudeFixtureState) ([]json.RawMessage, error) {
	state, err := os.OpenRoot(d.stateDir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = state.Close() }()
	before, err := state.Lstat(local)
	if err != nil || !before.IsDir() || before.Mode().Perm()&0077 != 0 {
		return nil, errClaudeFixtureMessage
	}
	root, err := state.OpenRoot(local)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	opened, err := root.Lstat(".")
	if err != nil || !os.SameFile(before, opened) {
		return nil, errClaudeFixtureMessage
	}
	f, err := root.OpenFile("claude-fixture-transcript.jsonl", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 || info.Size() > schema.MaxNativeHistoryPageBytes {
		return nil, errClaudeFixtureMessage
	}
	raw, err := io.ReadAll(io.LimitReader(f, schema.MaxNativeHistoryPageBytes+1))
	if err != nil || len(raw) > schema.MaxNativeHistoryPageBytes || len(raw) > 0 && raw[len(raw)-1] != '\n' {
		return nil, errClaudeFixtureMessage
	}
	s.mu.Lock()
	intent, identity, evidence := s.intent, s.identity, s.evidence
	s.mu.Unlock()
	var rows []json.RawMessage
	var previous, userText string
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		if len(rows) >= 3 {
			return nil, errClaudeFixtureMessage
		}
		var row struct {
			V               int `json:"v"`
			Type, UUID      string
			Parent          *string         `json:"parentUuid"`
			Session         string          `json:"sessionId"`
			Message         json.RawMessage `json:"message"`
			Subtype, Result string
		}
		if json.Unmarshal(line, &row) != nil || row.V != 1 || row.Session != identity.NativeSessionID || !adapter.IsCanonicalConversationID(row.UUID) {
			return nil, errClaudeFixtureMessage
		}
		keys := []string{"v", "type", "uuid", "parentUuid", "sessionId", "message"}
		if len(rows) == 2 {
			keys = []string{"v", "type", "uuid", "parentUuid", "sessionId", "subtype", "result"}
		}
		if _, valid := fixtureObject(line, keys...); !valid {
			return nil, errClaudeFixtureMessage
		}
		if len(rows) < 2 {
			if len(rows) == 0 {
				if row.Type != "user" || row.UUID != intent.NativeUserUUID || row.Parent != nil {
					return nil, errClaudeFixtureMessage
				}
			} else {
				if row.Type != "assistant" || row.Parent == nil || *row.Parent != previous || row.UUID != evidence.AssistantUUID {
					return nil, errClaudeFixtureMessage
				}
			}
			synthetic, _ := json.Marshal(map[string]any{"type": row.Type, "uuid": row.UUID, "session_id": row.Session, "parent_tool_use_id": nil, "message": row.Message})
			message, e := decodeClaudeFixtureFrame(synthetic)
			if e != nil {
				return nil, e
			}
			text, valid := fixtureText(message)
			if !valid || message.Message.Role != row.Type {
				return nil, errClaudeFixtureMessage
			}
			if len(rows) == 0 {
				h := sha256.Sum256([]byte(text))
				if len(text) != intent.TextBytes || hex.EncodeToString(h[:]) != intent.TextSHA256 {
					return nil, errClaudeFixtureMessage
				}
				userText = text
			} else if message.Message.ID != row.UUID || text != "fixture reply: "+userText {
				return nil, errClaudeFixtureMessage
			}
		} else if row.Type != "result" || row.Parent == nil || *row.Parent != previous || row.UUID == previous || row.UUID == intent.NativeUserUUID || row.Subtype != "success" || row.Result != "fixture reply: "+userText {
			return nil, errClaudeFixtureMessage
		}

		previous = row.UUID
		rows = append(rows, append(json.RawMessage(nil), line...))
	}
	return rows, nil
}

func (d *Daemon) claudeFixtureHistory(req schema.InteractionHistoryReq, local string) (schema.NativeHistoryPage, protocol.ErrorCode, error) {
	s := d.claudeFixtureState(local)
	if s == nil || req.NativeCursor != "" || req.BeforeItem != "" || req.Limit < 3 {
		return schema.NativeHistoryPage{}, protocol.CodeUnavailable, errors.New("fixture history scope unavailable")
	}
	d.startHookDrain(local)
	s.mu.Lock()
	drainer := s.drainer
	s.mu.Unlock()
	if drainer == nil {
		return schema.NativeHistoryPage{}, protocol.CodeUnavailable, errClaudeFixtureMessage
	}
	if _, _, err := drainer.DrainOnce(); err != nil {
		return schema.NativeHistoryPage{}, protocol.CodeUnavailable, err
	}
	if err := d.proveClaudeFixtureReady(local, s); err != nil {
		return schema.NativeHistoryPage{}, protocol.CodeUnavailable, err
	}
	identity, ready, _, _ := s.snapshot()
	if !ready {
		return schema.NativeHistoryPage{}, protocol.CodeUnavailable, errClaudeFixtureMessage
	}
	rows, err := d.readClaudeFixtureHistory(local, s)
	if err != nil {
		s.mu.Lock()
		s.ready = false
		s.mu.Unlock()
		return schema.NativeHistoryPage{}, protocol.CodeUnavailable, err
	}
	instance, ok := d.sessionInstance(local)
	if !ok {
		return schema.NativeHistoryPage{}, protocol.CodeUnavailable, errClaudeFixtureMessage
	}
	bound, _ := json.Marshal(struct {
		Identity any
		Instance string
	}{identity, instance})
	scope := sha256.Sum256(bound)
	return schema.NativeHistoryPage{Version: 1, Scope: hex.EncodeToString(scope[:]), Items: rows, Floor: true}, "", nil
}
