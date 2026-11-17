package protocol

import (
	"context"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

type managerInterruptStub struct {
	*managerRuntimeStub
	writes atomic.Int32
}

func (*managerInterruptStub) ManagerInterruptSupported() bool { return true }
func (s *managerInterruptStub) ManagerInterrupt(_ context.Context, local, id string, req ManagerInterruptReq, statusOnly bool) (ManagerInterruptReceipt, error) {
	if req.Binding.Session == local && !statusOnly {
		s.writes.Add(1)
	}
	return ManagerInterruptReceipt{OperationID: id, NativeInputID: req.NativeInputID, NativeTurnID: req.NativeTurnID, State: "pending"}, nil
}

func TestManagerInterruptOwnerProtocolBindsTheOriginalTurn(t *testing.T) {
	stub := &managerInterruptStub{managerRuntimeStub: &managerRuntimeStub{newStubDaemon()}}
	socket := serveOwnerAPI(t, stub)
	client, err := Dial(socket, []string{CapManagerProvenance, CapManagerInterrupt})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	binding, err := client.ManagerRuntime(NamespacedID(client.EndpointID(), "sess1"), "instance")
	if err != nil {
		t.Fatal(err)
	}
	req := ManagerInterruptReq{Binding: binding.Binding, NativeInputID: strings.Repeat("a", 64), NativeTurnID: "turn"}
	receipt, err := client.ManagerInterrupt(t.Context(), req, false)
	if err != nil || receipt.State != "pending" || receipt.OperationID != ManagerInterruptOperationID(req) {
		t.Fatalf("exact owner stop %+v: %v", receipt, err)
	}
	if replay, err := client.ManagerInterrupt(t.Context(), req, true); err != nil || replay != receipt || stub.writes.Load() != 1 {
		t.Fatalf("status dispatched another native stop: %+v %v", replay, err)
	}
	remote := rawDial(t, serveRemoteAPI(t, stub))
	hello := remote.hello(Version, []string{CapRemoteGateway, CapManagerInterrupt})
	if slices.Contains(hello.Capabilities, CapManagerInterrupt) {
		t.Fatal("owner stop advertised to a remote client")
	}
	req.Binding.Session = NamespacedID(hello.EndpointID, "sess1")
	remote.writeControl(Control{Op: OpManagerInterrupt, EndpointID: hello.EndpointID, SessionID: req.Binding.Session,
		OperationID: ManagerInterruptOperationID(req), BodyVersion: 1, ManagerInterruptRequest: &req})
	if reply := remote.readControl(); reply.Op != OpError || reply.ErrorCode != CodeNotAuthorized || stub.writes.Load() != 1 {
		t.Fatalf("remote stop reached owner path: %+v", reply)
	}
}
