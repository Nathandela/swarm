package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Nathandela/swarm/internal/protocol"
	"os"
	"testing"
	"time"
)

func TestPilotSemanticUncertainSendPreservesExactRetryCustody(t *testing.T) {
	c := newFakeSemanticClient()
	path := enterPilotForTest(t, c)
	c.messageErr = errors.New("reply lost after submission")
	code, out, stderr := pilotCall(c, "--context", path, "send", "local/ami", "--text", "exact payload")
	if code == 0 || out != "" || len(c.messages) != 1 || len(c.sendCalls()) != 0 {
		t.Fatalf("uncertain send bypassed semantic boundary: exit%d output%q err%q messages%+v", code, out, stderr, c.messages)
	}
	data, err := os.ReadFile(pilotPendingPath(path, "local/ami"))
	if err != nil {
		t.Fatal(err)
	}
	var pending map[string]any
	if json.Unmarshal(data, &pending) != nil || pending["operation_id"] != c.messages[0].operation || pending["uncertain"] != true {
		t.Fatalf("lost custody after ambiguous submission: %s", data)
	}
	if code, _, _ := pilotCall(c, "--context", path, "send", "local/ami", "--text", "different payload"); code == 0 || len(c.messages) != 1 {
		t.Fatal("a new payload overtook the unresolved operation")
	}
	c.messageErr = nil
	code, out, stderr = pilotCall(c, "--context", path, "send", "local/ami", "--text", "exact payload")
	var receipt struct {
		Receipt string `json:"receipt"`
	}
	if json.Unmarshal([]byte(out), &receipt) != nil || code != 0 || receipt.Receipt != "submitted" || len(c.messages) != 2 || c.messages[0] != c.messages[1] {
		t.Fatalf("retry changed operation/payload: exit%d output%q err%q messages%+v", code, out, stderr, c.messages)
	}
}

func TestPilotSemanticKnownRejectionAllowsFreshOperation(t *testing.T) {
	c := newFakeSemanticClient()
	path := enterPilotForTest(t, c)
	c.messageErr = fmt.Errorf("%w: busy; nothing sent", protocol.ErrSemanticSendRejected)
	if code, _, _ := pilotCall(c, "--context", path, "send", "local/ami", "--text", "first payload"); code == 0 {
		t.Fatal("rejected native send returned success")
	}
	c.messageErr = nil
	if code, out, stderr := pilotCall(c, "--context", path, "send", "local/ami", "--text", "new payload"); code != 0 || len(c.messages) != 2 || c.messages[0].operation == c.messages[1].operation {
		t.Fatalf("known rejection blocked a fresh request: exit%d %q %q", code, out, stderr)
	}
}

func TestPilotSemanticMarkerFailurePreventsNativeWrite(t *testing.T) {
	c := newFakeSemanticClient()
	path := enterPilotForTest(t, c)
	if err := os.Mkdir(pilotPendingPath(path, "local/ami"), 0o700); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := pilotCall(c, "--context", path, "send", "local/ami", "--text", "do it"); code == 0 || out != "" || len(c.messages) != 0 || len(c.sendCalls()) != 0 {
		t.Fatal("native effect preceded durable pilot custody")
	}
}

func TestPilotUncertainSubmissionDoesNotClaimAnUnrelatedFreshReply(t *testing.T) {
	c := newFakeSemanticClient()
	path := enterPilotForTest(t, c)
	c.messageErr = errors.New("connection closed after write")
	if code, _, _ := pilotCall(c, "--context", path, "send", "local/ami", "--text", "question"); code == 0 {
		t.Fatal("ambiguous send returned success")
	}
	c.sessions[0].Group = "ready_for_review"
	c.sessions[0].GroupEnteredAt = time.Now().Add(time.Second)
	code, out, stderr := pilotCall(c, "--context", path, "await", "local/ami", "--timeout", "1ms")
	var receipt struct {
		Receipt string `json:"receipt"`
	}
	if json.Unmarshal([]byte(out), &receipt) != nil || code != watchTimeoutExit || receipt.Receipt != "waiting" {
		t.Fatalf("ambiguous submission borrowed an unrelated reply: exit%d %q %q", code, out, stderr)
	}
}
