package agyacp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestRelayBindsPromptAndLatestClosingMessage(t *testing.T) {
	const attempt, conv = "attempt-1", "b2cf6dff-e44b-42f1-9b5c-1826c732b597"
	const prompt = "First line.\nSecond line with O’Brian."
	dir := t.TempDir()
	if err := QueuePrompt(dir, attempt, prompt); err != nil {
		t.Fatal(err)
	}
	if err := QueuePrompt(dir, attempt, "different prompt"); err == nil {
		t.Fatal("second queued prompt replaced the first")
	}
	transcript := strings.Join([]string{
		`{"id":1,"result":{"protocolVersion":1}}`,
		`{"id":2,"result":{"sessionId":"` + conv + `"}}`,
		update(conv, "agent_message_chunk", "Earlier misleading summary"),
		update(conv, "tool_call", ""),
		update(conv, "tool_call_update", "A tool response is not final"),
		update("another-session", "agent_message_chunk", "foreign output"),
		update(conv, "agent_thought_chunk", "private thought"),
		update(conv, "agent_message_chunk", "Final "),
		update(conv, "agent_message_chunk", "assistant response."),
		`{"id":3,"result":{"stopReason":"end_turn"}}`,
	}, "\n")
	var wire, output bytes.Buffer
	r := relay{bufio.NewScanner(strings.NewReader(transcript)), json.NewEncoder(&wire), bufio.NewScanner(strings.NewReader("")), &output}
	if err := r.run(context.Background(), t.TempDir(), dir, attempt, Server{Name: "swarm_schedule"}, func() {}); err != nil {
		t.Fatal(err)
	}
	evidence, found := ReadEvidence(dir, attempt)
	if !found || !evidence.EndTurn || evidence.Conversation != conv || evidence.Text != "Final assistant response." {
		t.Fatalf("unexpected completion evidence: %+v", evidence)
	}
	if _, found := ReadEvidence(dir, "other-attempt"); found {
		t.Fatal("evidence was accepted for another attempt")
	}
	if output.String() != "Final assistant response.\n" || !strings.Contains(wire.String(), `"text":"First line.\nSecond line with O’Brian."`) {
		t.Fatalf("native prompt or final output lost: %s / %s", wire.String(), output.String())
	}
}

func TestRelayDoesNotAcceptIncompleteTurnOrAutomaticallyAllowOperation(t *testing.T) {
	const attempt, conv = "attempt-1", "session-1"
	dir := t.TempDir()
	if err := QueuePrompt(dir, attempt, "Original opening prompt"); err != nil {
		t.Fatal(err)
	}
	transcript := strings.Join([]string{
		`{"id":1,"result":{"protocolVersion":1}}`,
		`{"id":2,"result":{"sessionId":"` + conv + `"}}`,
		`{"id":"permission-4","method":"session/request_permission","params":{"sessionId":"session-1","toolCall":{"title":"execute"},"options":[{"optionId":"allow","name":"Allow"}]}}`,
		update(conv, "agent_message_chunk", "Should not be accepted."),
		`{"id":3,"result":{"stopReason":"max_tokens"}}`,
	}, "\n")
	var wire, output bytes.Buffer
	r := relay{bufio.NewScanner(strings.NewReader(transcript)), json.NewEncoder(&wire), bufio.NewScanner(strings.NewReader("0\n")), &output}
	if err := r.run(context.Background(), t.TempDir(), dir, attempt, Server{}, func() {}); err == nil {
		t.Fatal("incomplete native turn accepted")
	}
	evidence, found := ReadEvidence(dir, attempt)
	if !found || evidence.StartupRejected || evidence.EndTurn || evidence.Text != "" {
		t.Fatalf("unexpected evidence: %+v", evidence)
	}
	if !strings.Contains(wire.String(), `"id":"permission-4","jsonrpc":"2.0","result":{"outcome":{"outcome":"cancelled"}}`) {
		t.Fatal("native permission request was not cancelled")
	}
}

func TestRelayRecordsRejectedStartup(t *testing.T) {
	dir := t.TempDir()
	var wire bytes.Buffer
	r := relay{bufio.NewScanner(strings.NewReader(`{"id":1,"result":{"protocolVersion":0}}`)), json.NewEncoder(&wire), nil, &wire}
	if err := r.run(context.Background(), t.TempDir(), dir, "attempt", Server{}, func() {}); err == nil {
		t.Fatal("unsupported protocol accepted")
	}
	evidence, found := ReadEvidence(dir, "attempt")
	if !found || !evidence.StartupRejected || evidence.Conversation != "" {
		t.Fatalf("startup rejection not recorded: %+v", evidence)
	}
}

func update(session, kind, text string) string {
	item := map[string]any{"method": "session/update", "params": map[string]any{
		"sessionId": session,
		"update":    map[string]any{"sessionUpdate": kind, "content": map[string]string{"type": "text", "text": text}},
	}}
	data, _ := json.Marshal(item)
	return string(data)
}
