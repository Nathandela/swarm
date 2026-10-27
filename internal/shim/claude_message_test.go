package shim

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/wire"
)

func claudeMessageHello(t *testing.T, socket string) (net.Conn, shimwire.Control) {
	t.Helper()
	c := dialShim(t, socket)
	_ = c.conn.SetDeadline(time.Now().Add(3 * time.Second))
	c.writeControl(shimwire.Control{Type: shimwire.TypeHello, WireVersion: shimwire.Version})
	typ, raw, err := wire.ReadFrame(c.conn)
	if err != nil || typ != wire.TControl {
		t.Fatal("primary hello transport failed", err)
	}
	hello, err := shimwire.Decode(raw)
	if err != nil || hello.Type != shimwire.TypeHello {
		t.Fatal("primary hello decode failed", err)
	}
	return c.conn, hello
}

func claudeMessageReady(t *testing.T) (Config, shimwire.ClaudePrimarySnapshot) {
	t.Helper()
	cfg := claudeChatFixtureConfig(t)
	finished := runShimAsync(cfg)
	t.Cleanup(func() {
		conn, err := net.DialTimeout("unix", cfg.SocketPath, time.Second)
		if err == nil {
			_ = conn.SetDeadline(time.Now().Add(time.Second))
			for _, ctrl := range []shimwire.Control{{Type: shimwire.TypeHello, WireVersion: shimwire.Version}, {Type: shimwire.TypeSignal, Sig: shimwire.SigKill}} {
				raw, _ := shimwire.Encode(ctrl)
				_ = wire.WriteFrame(conn, wire.TControl, raw)
			}
			_ = conn.Close()
		}
		select {
		case <-finished:
		case <-time.After(13 * time.Second):
			t.Error("message primary did not join within finite fixture lifetime")
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, hello := claudeMessageHello(t, cfg.SocketPath)
		_ = conn.Close()
		if !hello.ClaudePrimaryV1 {
			t.Fatal("actual stdio constructor advertises no typed primary snapshot")
		}
		if p := hello.ClaudePrimary; p != nil && p.Healthy && !p.PendingPartial && p.ObservedSequence > 0 && p.DurableSequence == p.ObservedSequence {
			if p.Identity.SessionID != cfg.SessionID || p.Identity.NativeSessionID != cfg.ConversationID || p.Identity.PrimaryPID <= 0 || p.Identity.PrimaryStartTime <= 0 || p.Identity.ShimPID != os.Getpid() {
				t.Fatal("typed snapshot has wrong primary identity")
			}
			return cfg, *p
		}
		if !time.Now().Before(deadline) {
			t.Fatal("primary never reached durably captured init")
		}
		time.Sleep(time.Millisecond)
	}
}

func claudeMessageCall(t *testing.T, cfg Config, request shimwire.ClaudeMessageRequest) shimwire.ClaudeMessageResult {
	t.Helper()
	conn, _ := claudeMessageHello(t, cfg.SocketPath)
	defer func() { _ = conn.Close() }()
	raw, err := shimwire.Encode(shimwire.Control{Type: shimwire.TypeClaudeMessage, ClaudeMessage: &request})
	if err != nil {
		t.Fatal(err)
	}
	if err := wire.WriteFrame(conn, wire.TControl, raw); err != nil {
		t.Fatal(err)
	}
	typ, raw, err := wire.ReadFrame(conn)
	if err != nil || typ != wire.TControl {
		t.Fatal("typed native write has no result", err)
	}
	ctrl, err := shimwire.Decode(raw)
	if err != nil || ctrl.Type != shimwire.TypeClaudeMessageResult || ctrl.ClaudeMessageResult == nil {
		t.Fatal("typed result missing", err)
	}
	r := *ctrl.ClaudeMessageResult
	if r.Version != 1 || r.OperationID != request.OperationID || r.NativeUserUUID != request.NativeUserUUID || r.Identity != request.Identity || r.OwnerRevision != request.OwnerRevision {
		t.Fatal("result not bound to exact write request")
	}
	return r
}

func TestClaudeChatMessageWritesOneBoundLineAndNeverResends(t *testing.T) {
	cfg, p := claudeMessageReady(t)
	request := shimwire.ClaudeMessageRequest{Version: 1, OperationID: "fixture-message-one", NativeUserUUID: "1389ef09-4c19-4d50-8fdd-1fc95bdcfd4a", Text: "hello\n雪\r\"", Identity: p.Identity, ExpectedOwnerRevision: 0, OwnerRevision: 1, ThroughStreamSequence: p.ObservedSequence, ReaderRevision: p.ReaderRevision}
	if r := claudeMessageCall(t, cfg, request); r.Status != "written" {
		t.Fatal("valid bound native line was not written", r.Status)
	}
	deadline := time.Now().Add(5 * time.Second)
	var rows [][]byte
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(filepath.Join(cfg.SessionDir, "claude-fixture-transcript.jsonl"))
		if err == nil {
			rows = bytes.Split(bytes.TrimSuffix(raw, []byte{'\n'}), []byte{'\n'})
			if len(rows) == 3 && json.Valid(rows[0]) && json.Valid(rows[1]) && json.Valid(rows[2]) {
				break
			}
		}
		time.Sleep(time.Millisecond)
	}
	if len(rows) != 3 {
		t.Fatal("real fake agent did not write complete transcript")
	}
	var user struct {
		Type    string `json:"type"`
		UUID    string `json:"uuid"`
		Message struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(rows[0], &user) != nil || user.Type != "user" || user.UUID != request.NativeUserUUID || user.Message.Role != "user" || len(user.Message.Content) != 1 || user.Message.Content[0].Text != request.Text {
		t.Fatal("native stdin line lost exact UUID/text")
	}
	fresh := claudeMessageFreshCompleted(t, cfg, request.Identity, deadline)
	claudeMessageOwnerFenceRefusals(t, cfg, request, fresh)
}

func TestClaudeChatMessageRejectsWrongCustodyBeforeAttempt(t *testing.T) {
	cfg, p := claudeMessageReady(t)
	good := shimwire.ClaudeMessageRequest{Version: 1, OperationID: "fixture-message-refuse", NativeUserUUID: "1389ef09-4c19-4d50-8fdd-1fc95bdcfd4a", Text: "must-not-write", Identity: p.Identity, ExpectedOwnerRevision: 0, OwnerRevision: 1, ThroughStreamSequence: p.ObservedSequence, ReaderRevision: p.ReaderRevision}
	for _, mutate := range []func(*shimwire.ClaudeMessageRequest){
		func(r *shimwire.ClaudeMessageRequest) { r.Identity.PrimaryStartTime++ },
		func(r *shimwire.ClaudeMessageRequest) { r.ReaderRevision++ },
		func(r *shimwire.ClaudeMessageRequest) { r.ThroughStreamSequence++ },
		func(r *shimwire.ClaudeMessageRequest) { r.ExpectedOwnerRevision++ },
		func(r *shimwire.ClaudeMessageRequest) { r.NativeUserUUID = "not-uuid" },
		func(r *shimwire.ClaudeMessageRequest) { r.Text = strings.Repeat("x", (16<<10)+1) },
	} {
		request := good
		mutate(&request)
		if r := claudeMessageCall(t, cfg, request); r.Status != "refused" {
			t.Fatal("wrong custody reached native write")
		}
	}
	conn, hello := claudeMessageHello(t, cfg.SocketPath)
	_ = conn.Close()
	if hello.ClaudePrimary == nil || hello.ClaudePrimary.OwnerRevision != 0 {
		t.Fatal("prewrite refusal advanced attempted revision")
	}
	raw, err := os.ReadFile(filepath.Join(cfg.SessionDir, "claude-fixture-transcript.jsonl"))
	if err != nil || len(raw) != 0 {
		t.Fatal("prewrite refusal changed genuine transcript", err)
	}
}

func TestClaudeChatMessageWireRejectsRawFallback(t *testing.T) {
	cfg, _ := claudeMessageReady(t)
	conn, _ := claudeMessageHello(t, cfg.SocketPath)
	defer func() { _ = conn.Close() }()
	if err := wire.WriteFrame(conn, wire.TDataIn, []byte("not-native")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := wire.ReadFrame(conn); err != io.EOF {
		t.Fatal("raw byte fallback not refused", err)
	}
}

func TestClaudeChatMessageLostAckNeverResendsNativeInput(t *testing.T) {
	cfg, p := claudeMessageReady(t)
	request := shimwire.ClaudeMessageRequest{Version: 1, OperationID: "lost-ack", NativeUserUUID: "1389ef09-4c19-4d50-8fdd-1fc95bdcfd4a", Text: "one transport attempt", Identity: p.Identity, OwnerRevision: 1, ThroughStreamSequence: p.ObservedSequence, ReaderRevision: p.ReaderRevision}
	conn, _ := claudeMessageHello(t, cfg.SocketPath)
	raw, err := shimwire.Encode(shimwire.Control{Type: shimwire.TypeClaudeMessage, ClaudeMessage: &request})
	if err != nil {
		t.Fatal(err)
	}
	if err := wire.WriteFrame(conn, wire.TControl, raw); err != nil {
		t.Fatal(err)
	}
	// Do not consume the ACK. First witness actual input acceptance in the
	// fixture's own file; WriteFrame success alone is not delivery proof.
	deadlineBeforeClose := time.Now().Add(5 * time.Second)
	delivered := false
	for time.Now().Before(deadlineBeforeClose) {
		raw, e := os.ReadFile(filepath.Join(cfg.SessionDir, "claude-fixture-transcript.jsonl"))
		if e == nil {
			lines := bytes.Split(raw, []byte{'\n'})
			if len(lines) > 1 && json.Valid(lines[0]) {
				var row struct {
					Type string `json:"type"`
					UUID string `json:"uuid"`
				}
				if json.Unmarshal(lines[0], &row) == nil && row.Type == "user" && row.UUID == request.NativeUserUUID {
					delivered = true
					break
				}
			}
		}
		time.Sleep(time.Millisecond)
	}
	_ = conn.Close() // ACK may be queued, but is deliberately never read by caller
	if !delivered {
		t.Fatal("lost ACK control did not witness a genuine user row")
	}
	deadline := deadlineBeforeClose // one unchanged aggregate5s witness/join budget
	var rows [][]byte
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(filepath.Join(cfg.SessionDir, "claude-fixture-transcript.jsonl"))
		if err == nil {
			rows = bytes.Split(bytes.TrimSuffix(raw, []byte{'\n'}), []byte{'\n'})
			if len(rows) == 3 && json.Valid(rows[2]) {
				break
			}
		}
		time.Sleep(time.Millisecond)
	}
	if len(rows) != 3 {
		t.Fatal("lost ACK control never reached real native input")
	}
	fresh := claudeMessageFreshCompleted(t, cfg, request.Identity, deadline)
	claudeMessageOwnerFenceRefusals(t, cfg, request, fresh)
}

// Keep the old attempted operation/text/UUID but remove stale read custody as
// an alternative refusal reason. This is deliberately not a new user intent.
func claudeMessageFreshCompleted(t *testing.T, cfg Config, identity shimwire.ClaudePrimaryIdentity, deadline time.Time) shimwire.ClaudePrimarySnapshot {
	t.Helper()
	for {
		conn, hello := claudeMessageHello(t, cfg.SocketPath)
		_ = conn.Close()
		if p := hello.ClaudePrimary; p != nil && p.Identity == identity && p.Healthy && !p.PendingPartial && !p.StdoutEOF && !p.StderrEOF && p.ObservedSequence == 4 && p.DurableSequence == 4 && p.OwnerRevision == 1 {
			return *p
		}
		if !time.Now().Before(deadline) {
			t.Fatal("exact echo/result capture never reached healthy durable4")
		}
		time.Sleep(time.Millisecond)
	}
}

func claudeMessageOwnerFenceRefusals(t *testing.T, cfg Config, original shimwire.ClaudeMessageRequest, fresh shimwire.ClaudePrimarySnapshot) {
	t.Helper()
	for _, revision := range []struct {
		expected, next uint64
		reason         string
	}{{0, 2, "stale expected"}, {1, 1, "non-increasing"}} {
		retry := original
		retry.ReaderRevision = fresh.ReaderRevision
		retry.ThroughStreamSequence = fresh.ObservedSequence
		retry.ExpectedOwnerRevision = revision.expected
		retry.OwnerRevision = revision.next
		if result := claudeMessageCall(t, cfg, retry); result.Status != "refused" {
			t.Fatalf("%s owner fence allowed a repeated native input: %s", revision.reason, result.Status)
		}
	}
}
