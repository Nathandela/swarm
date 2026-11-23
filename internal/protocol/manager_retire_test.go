package protocol

import (
	"context"
	"slices"
	"strings"
	"testing"
)

type managerRetireStub struct {
	*managerRuntimeStub
	writes        int
	deletionState string
}

func (*managerRetireStub) ManagerRetireSupported() bool { return true }
func (s *managerRetireStub) ManagerRetire(_ context.Context, local, id string, req ManagerRetireReq, op string) (ManagerRetireReceipt, error) {
	if op == OpManagerRetireCancel {
		return ManagerRetireReceipt{OperationID: id, State: "cancelled"}, nil
	}
	if op == OpManagerRetireStatus && s.deletionState != "" {
		return ManagerRetireReceipt{OperationID: id, State: s.deletionState}, nil
	}
	if req.Binding.Session == local && op == OpManagerRetire {
		s.writes++
	}
	return ManagerRetireReceipt{OperationID: id, State: "uncertain"}, nil
}

func TestManagerRetireBindsOwnerStopAndStatus(t *testing.T) {
	stub := &managerRetireStub{managerRuntimeStub: &managerRuntimeStub{newStubDaemon()}}
	c, err := Dial(serveOwnerAPI(t, stub), []string{CapManagerProvenance, CapManagerRetire, CapManagerRetireCancel})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	runtime, err := c.ManagerRuntime(NamespacedID(c.EndpointID(), "sess1"), "instance")
	if err != nil {
		t.Fatal(err)
	}
	req := ManagerRetireReq{Binding: runtime.Binding, Generation: 4, Checkpoint: strings.Repeat("a", 64), ExpectedTurn: ManagerNativeTurn{State: "idle"}}
	first, err := c.ManagerRetire(t.Context(), req, false)
	if err != nil || first.State != "uncertain" || first.OperationID != ManagerRetireOperationID(req) {
		t.Fatalf("lost stop ack: %+v %v", first, err)
	}
	if got, err := c.ManagerRetire(t.Context(), req, true); err != nil || got != first || stub.writes != 1 {
		t.Fatalf("status changed exact effect: %+v %v (%d writes)", got, err, stub.writes)
	}
	for _, state := range []string{"deleted", "deleted_parked"} {
		stub.deletionState = state
		if got, err := c.ManagerRetire(t.Context(), req, true); err != nil || got.State != state || got.OperationID != first.OperationID || stub.writes != 1 {
			t.Fatalf("owner read did not return the exact deletion receipt: %+v %v (%d writes)", got, err, stub.writes)
		}
	}
	if got, err := c.CancelManagerRetire(t.Context(), req); err != nil || got.OperationID != first.OperationID || got.State != "cancelled" || stub.writes != 1 {
		t.Fatalf("cancellation affected native process: %+v %v", got, err)
	}
	next := req
	next.Generation++
	if ManagerRetireOperationID(next) == first.OperationID {
		t.Fatal("operation ID ignored controller generation")
	}
	remote := rawDial(t, serveRemoteAPI(t, stub))
	hello := remote.hello(Version, []string{CapRemoteGateway, CapManagerRetire, CapManagerRetireCancel})
	if slices.Contains(hello.Capabilities, CapManagerRetire) || slices.Contains(hello.Capabilities, CapManagerRetireCancel) {
		t.Fatal("manager retirement advertised to a remote client")
	}
	req.Binding.Session = NamespacedID(hello.EndpointID, "sess1")
	for _, op := range []string{OpManagerRetire, OpManagerRetireStatus} {
		remote.writeControl(Control{Op: op, EndpointID: hello.EndpointID, SessionID: req.Binding.Session,
			OperationID: ManagerRetireOperationID(req), BodyVersion: 1, ManagerRetireRequest: &req})
		if reply := remote.readControl(); reply.Op != OpError || reply.ErrorCode != CodeNotAuthorized {
			t.Fatalf("remote operation %s reached owner path: %+v", op, reply)
		}
	}
}
