package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
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

func (c *fakeSemanticClient) List() ([]protocol.SessionView, error) {
	rows, err := c.fakeSteerClient.List()
	for i := range rows {
		if rows[i].Capabilities == nil {
			rows[i].Capabilities = &protocol.SessionCapabilities{SessionInstance: "native-incarnation"}
		}
	}
	return rows, err
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
	if err := json.Unmarshal(out.Bytes(), &receipt); err != nil || receipt["operation_id"] != id || receipt["state"] != "submitted" {
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
				c.sessions[0].Capabilities = &protocol.SessionCapabilities{}
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

func TestVMDispatcherCanMessageAnUnrelatedOwnerSession(t *testing.T) {
	id, err := protocol.NewSemanticOperationID()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"local/ami", "--text", "Follow up in this existing session.", "--operation-id", id}
	var normal, dispatcher, stderr bytes.Buffer
	operator := newFakeSemanticClient()
	if code := runMessage(args, operator, &normal, &stderr); code != 0 {
		t.Fatal(code, stderr.String())
	}
	t.Setenv(schema.EnvManagerVMCLI, "1")
	t.Setenv("SWARM_SESSION_ID", "unrelated-dispatcher")
	vm := newFakeSemanticClient()
	if code := runMessage(args, vm, &dispatcher, &stderr); code != 0 || dispatcher.String() != normal.String() || len(vm.messages) != 1 || vm.messages[0] != operator.messages[0] {
		t.Fatalf("VM message lost ordinary owner semantics: code=%d receipt=%q %s", code, dispatcher.String(), stderr.String())
	}
}

type resolvingSemanticClient struct {
	*fakeSemanticClient
}

func (c *resolvingSemanticClient) List() ([]protocol.SessionView, error) {
	return nil, errors.New("authoritative send must not consult the roster")
}

func (c *resolvingSemanticClient) SemanticTarget(session string) (string, error) {
	if session != "local/ami" {
		return "", errors.New("wrong endpoint")
	}
	return "verified-instance", nil
}

func TestMessageResolvesExactTargetWithoutRoster(t *testing.T) {
	c := &resolvingSemanticClient{newFakeSemanticClient()}
	var out, stderr bytes.Buffer
	if code := runMessage([]string{"local/ami", "--text", "first send"}, c, &out, &stderr); code != 0 {
		t.Fatal(code, stderr.String())
	}
	if len(c.messages) != 1 || c.messages[0].req.SessionInstance != "verified-instance" || len(c.sendCalls()) != 0 {
		t.Fatalf("calls: %+v", c.messages)
	}
	out.Reset()
	stderr.Reset()
	if code := runMessage([]string{"wrong/ami", "--text", "wrong endpoint"}, c, &out, &stderr); code == 0 ||
		len(c.messages) != 1 || out.Len() != 0 || stderr.String() != "message: operation_id=: wrong endpoint\n" {
		t.Fatalf("refused target: %d %s %s", code, out.String(), stderr.String())
	}
}

type warmingSemanticClient struct {
	*fakeSemanticClient
	lists int
}

func (c *warmingSemanticClient) List() ([]protocol.SessionView, error) {
	c.lists++
	if c.lists == 1 {
		row := c.sessions[0]
		row.Capabilities = nil
		return []protocol.SessionView{row}, nil
	}
	return c.fakeSemanticClient.List()
}

func TestMessageRefreshesOlderDaemonBeforeSending(t *testing.T) {
	c := &warmingSemanticClient{fakeSemanticClient: newFakeSemanticClient()}
	var out, stderr bytes.Buffer
	if code := runMessage([]string{"local/ami", "--text", "first send"}, c, &out, &stderr); code != 0 || c.lists != 2 ||
		len(c.messages) != 1 || c.messages[0].req.SessionInstance != "native-incarnation" {
		t.Fatalf("message did not wait for identity: %d, %s, %d, %+v", code, stderr.String(), c.lists, c.messages)
	}
}
