package protocol

import (
	"sync/atomic"
	"testing"
	"time"
)

type semanticStatusBackend struct {
	*durableComposerStub
	reads atomic.Int32
}

func (s *semanticStatusBackend) SemanticOperation(local, operationID string) (SemanticMessageReceipt, error) {
	s.reads.Add(1)
	return SemanticMessageReceipt{OperationID: operationID, Session: local, State: "uncertain", NativeSessionID: "ses_exact", NativeMessageID: "msg_exact"}, nil
}

func TestOwnerSemanticStatusReadsExpiredOperationWithoutResubmitting(t *testing.T) {
	b := &semanticStatusBackend{durableComposerStub: newDurableComposerStub()}
	c, err := Dial(serveOwnerAPI(t, b), []string{CapSemanticSend})
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = c.Close() })
	id := semanticTestID(time.Now().Add(-48*time.Hour), "a")
	view, err := c.SemanticOperation(c.EndpointID()+"/sess1", id)
	if err != nil || view.OperationID != id || view.State != "uncertain" || view.NativeMessageID != "msg_exact" {
		t.Fatalf("expired operation read = %+v, %v", view, err)
	}
	if _, sends, claims := b.snapshot(id); sends != 0 || claims != 0 || b.reads.Load() != 1 { t.Fatal("receipt query mutated operation/provider custody") }
}

func TestSemanticStatusRefusesUnnegotiatedAndRemoteReads(t *testing.T) {
	for _, remote := range []bool{false,true} {
		b := &semanticStatusBackend{durableComposerStub: newDurableComposerStub()}
		sock := serveOwnerAPI(t,b)
		caps := []string{}
		if remote { sock = serveRemoteAPI(t,b); caps = []string{CapRemoteGateway,CapSemanticSend} }
		raw := rawDial(t,sock)
		hello := raw.hello(Version,caps)
		raw.writeControl(Control{Op:"semantic_status",EndpointID:hello.EndpointID,SessionID:hello.EndpointID+"/sess1",OperationID:semanticTestID(time.Now(),"b")})
		if got := raw.readControl(); got.Op != OpError || b.reads.Load() != 0 { t.Fatalf("unauthorized receipt read: %+v",got) }
	}
}
