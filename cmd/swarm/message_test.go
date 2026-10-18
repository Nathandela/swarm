package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

type semanticCall struct {
	operation string
	req       protocol.ComposerSendReq
}

type fakeSemanticClient struct {
	*fakeSteerClient
	messages   []semanticCall
	messageErr error
}

func newFakeSemanticClient() *fakeSemanticClient {
	c := &fakeSemanticClient{fakeSteerClient: newFakeSteerClient()}
	row := view("local/ami", "opencode", "AMI", status.GroupWorking)
	row.Capabilities = &protocol.SessionCapabilities{SessionInstance: "native-incarnation"}
	c.sessions = []protocol.SessionView{row}
	return c
}

func (c *fakeSemanticClient) SemanticSend(operation string, req protocol.ComposerSendReq) error {
	c.messages = append(c.messages, semanticCall{operation, req})
	return c.messageErr
}

func TestMessageUsesExactSemanticPayloadAndIncarnation(t *testing.T) {
	c := newFakeSemanticClient()
	id, err := protocol.NewSemanticOperationID()
	if err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	payload := "Please inspect this.\nKeep the line break."
	if code := runMessage([]string{"local/ami", "--text", payload, "--operation-id", id}, c, &out, &stderr); code != 0 {
		t.Fatalf("message exit %d: %s", code, stderr.String())
	}
	if len(c.messages) != 1 || c.messages[0] != (semanticCall{id, protocol.ComposerSendReq{Session: "local/ami", SessionInstance: "native-incarnation", Text: payload}}) || len(c.sendCalls()) != 0 {
		t.Fatalf("message crossed the wrong boundary: %+v", c.messages)
	}
	var receipt map[string]string
	if err := json.Unmarshal(out.Bytes(), &receipt); err != nil || receipt["operation_id"] != id || receipt["receipt"] != "submitted" {
		t.Fatalf("uncorrelated receipt %q: %v", out.String(), err)
	}
}

func TestMessageRefusesLegacyOrUnboundSessionsWithoutTerminalFallback(t *testing.T) {
	for _, kind := range []string{"legacy", "unbound", "uncertain"} {
		t.Run(kind, func(t *testing.T) {
			c := newFakeSemanticClient()
			var client agentClient = c
			switch kind {
			case "legacy":
				client = c.fakeSteerClient
			case "unbound":
				c.sessions[0].Capabilities = nil
			case "uncertain":
				c.messageErr = errors.New("outcome unknown")
			}
			var out, stderr bytes.Buffer
			if code := runMessage([]string{"local/ami", "--text", "do it"}, client, &out, &stderr); code == 0 || out.Len() != 0 || len(c.sendCalls()) != 0 {
				t.Fatalf("refused message generated success or terminal effect: exit %d, %q, %q", code, out.String(), stderr.String())
			}
		})
	}
}
