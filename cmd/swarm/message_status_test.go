package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

type fakeSemanticReceiptClient struct {
	*fakeSemanticClient
	receipt protocol.SemanticMessageReceipt
	reads   int
}

func (c *fakeSemanticReceiptClient) SemanticOperation(session, operationID string) (protocol.SemanticMessageReceipt, error) {
	c.reads++
	view := c.receipt
	view.Session, view.OperationID = session, operationID
	return view, nil
}

func TestMessageStatusReadsAnEndedSessionWithoutSubmitting(t *testing.T) {
	c := &fakeSemanticReceiptClient{fakeSemanticClient: newFakeSemanticClient(), receipt: protocol.SemanticMessageReceipt{State: "admitted", NativeMessageID: "msg_exact"}}
	c.sessions[0].Status.Process = status.ProcessExited
	var out, stderr bytes.Buffer
	id := "semantic:1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	code := runMessage([]string{"local/ami", "--status", "--operation-id", id}, c, &out, &stderr)
	var receipt protocol.SemanticMessageReceipt
	if err := json.Unmarshal(out.Bytes(), &receipt); err != nil || code != 0 || receipt.State != "admitted" || receipt.OperationID != id || receipt.NativeMessageID != "msg_exact" || c.reads != 1 || len(c.messages) != 0 || len(c.sendCalls()) != 0 {
		t.Fatalf("receipt lookup submitted or lost its exact coordinates: exit%d %s %s", code, out.String(), stderr.String())
	}
}

func TestPilotAwaitKeepsNativeInputCorrelationWhenLaterWorkRuns(t *testing.T) {
	c := &fakeSemanticReceiptClient{fakeSemanticClient: newFakeSemanticClient(), receipt: protocol.SemanticMessageReceipt{State: "admitted", NativeMessageID: "msg_owned"}}
	path := enterPilotForTest(t, c)
	if code, _, stderr := pilotCall(c, "--context", path, "send", "local/ami", "--text", "original input"); code != 0 {
		t.Fatal(stderr)
	}
	c.receipt.State = "completed"
	code, out, stderr := pilotCall(c, "--context", path, "await", "local/ami", "--timeout", "1ms")
	var receipt struct {
		Receipt   string `json:"receipt"`
		WorkState string `json:"work_state"`
	}
	if err := json.Unmarshal([]byte(out), &receipt); err != nil || code != 0 || receipt.Receipt != "input_completed" || receipt.WorkState != "working" {
		t.Fatalf("requested input completion was hidden by later activity: exit%d %s %s", code, out, stderr)
	}
}

func TestPilotAwaitDoesNotBorrowAnotherInputsReadyState(t *testing.T) {
	c := &fakeSemanticReceiptClient{fakeSemanticClient: newFakeSemanticClient(), receipt: protocol.SemanticMessageReceipt{State: "admitted", NativeMessageID: "msg_owned"}}
	path := enterPilotForTest(t, c)
	if code, _, stderr := pilotCall(c, "--context", path, "send", "local/ami", "--text", "original input"); code != 0 {
		t.Fatal(stderr)
	}
	c.sessions[0].Group = status.GroupReadyForReview
	c.sessions[0].GroupEnteredAt = time.Now().Add(time.Second)
	code, out, stderr := pilotCall(c, "--context", path, "await", "local/ami", "--timeout", "1ms")
	var receipt struct {
		Receipt string `json:"receipt"`
	}
	if err := json.Unmarshal([]byte(out), &receipt); err != nil || code != watchTimeoutExit || receipt.Receipt != "waiting" {
		t.Fatalf("unrelated ready state was treated as this native input's completion: exit%d %s %s", code, out, stderr)
	}
}

func TestPilotAwaitNativeReconciliationUnblocksNextMessage(t *testing.T) {
	c := &fakeSemanticReceiptClient{fakeSemanticClient: newFakeSemanticClient(), receipt: protocol.SemanticMessageReceipt{State: "completed", NativeMessageID: "msg_owned"}}
	path := enterPilotForTest(t, c)
	c.messageErr = errors.New("original reply lost")
	if code, _, _ := pilotCall(c, "--context", path, "send", "local/ami", "--text", "original input"); code == 0 {
		t.Fatal("lost receipt returned success")
	}
	if code, _, stderr := pilotCall(c, "--context", path, "await", "local/ami", "--timeout", "1ms"); code != 0 {
		t.Fatal(stderr)
	}
	c.messageErr = nil
	if code, out, stderr := pilotCall(c, "--context", path, "send", "local/ami", "--text", "next input"); code != 0 || len(c.messages) != 2 || c.messages[0].operation == c.messages[1].operation {
		t.Fatalf("reconciled input kept blocking a fresh message: exit%d %s %s", code, out, stderr)
	}
}
