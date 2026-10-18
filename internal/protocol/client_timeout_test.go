package protocol

import (
	"strings"
	"testing"
	"time"
)

func TestRequestTimeoutRetiresConnectionBeforeLateMutationReceipt(t *testing.T) {
	stub := newDurableComposerStub()
	stub.commitEntered = make(chan struct{})
	stub.releaseCommit = make(chan struct{})
	sock := tmpSock(t)
	server, err := Serve(stub, sock)
	if err != nil {
		t.Fatal(err)
	}
	server.endpointID = "machine"
	t.Cleanup(func() { _ = server.Close() })
	c, err := Dial(sock, []string{CapSemanticSend})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	id := semanticTestID(time.Now(), "d")
	frame := semanticTestFrame(Control{EndpointID: c.EndpointID()}, id, "exact payload")
	result := make(chan error, 1)
	go func() {
		_, err := c.requestWithTimeout(frame, 100*time.Millisecond)
		result <- err
	}()
	select {
	case <-stub.commitEntered:
	case <-time.After(time.Second):
		close(stub.releaseCommit)
		t.Fatal("mutation did not reach terminal commit")
	}
	err = <-result
	select {
	case <-c.done:
	default:
		close(stub.releaseCommit)
		t.Fatal("timed out connection can accept a late receipt as a later request's answer")
	}
	close(stub.releaseCommit)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout = %v", err)
	}
	if _, err := c.requestWithTimeout(frame, time.Second); err == nil {
		t.Fatal("retired connection accepted another mutation")
	}
	retry, err := Dial(sock, []string{CapSemanticSend})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = retry.Close() })
	frame.EndpointID = retry.EndpointID()
	frame.SessionID = retry.EndpointID() + "/sess1"
	frame.ComposerSend.Session = frame.SessionID
	response, err := retry.request(frame)
	if err != nil || response.Op != OpOK || response.OperationID != id {
		t.Fatalf("reconnected exact retry = %+v, %v", response, err)
	}
	if _, sends, _ := stub.snapshot(id); sends != 1 {
		t.Fatalf("exact retry delivered %d native mutations", sends)
	}
}
