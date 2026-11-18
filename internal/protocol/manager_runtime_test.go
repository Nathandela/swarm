package protocol

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type managerRuntimeStub struct{ *stubDaemon }

func (s *managerRuntimeStub) ManagerInputStatus(ctx context.Context, local string, req ManagerAdmission) (ManagerNativeTurn, error) {
	return ManagerNativeTurn{NativeInputID: req.Nonce, NativeTurnID: "historical", State: "completed", Reason: "answer"}, nil
}
func (s *managerRuntimeStub) ManagerRuntime(local, instance string) (ManagerRuntimeStatus, error) {
	return ManagerRuntimeStatus{Binding: ManagerRuntimeBinding{Session: local, SessionInstance: instance, ConversationID: "conversation", Provider: "claude", NativeSessionID: "native", NativeGeneration: "generation", ObservationGeneration: "observer-generation", LoginEpoch: "account"}, Turn: ManagerNativeTurn{State: "idle"}}, nil
}
func (s *managerRuntimeStub) ResolveManagerCall(ctx context.Context, local string, req ManagerCallReq) (ManagerCallProof, error) {
	return ManagerCallProof{NativeInputID: strings.Repeat("a", 64), NativeTurnID: "turn", NativeCallID: "call", Main: true}, nil
}
func TestManagerRuntimeFacadeOwnerOnlyAndCancellable(t *testing.T) {
	stub := &managerRuntimeStub{newStubDaemon()}
	c, err := Dial(serveOwnerAPI(t, stub), []string{CapManagerProvenance})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	session := c.EndpointID() + "/sess1"
	result, err := c.ManagerRuntime(session, "instance")
	if err != nil || result.Binding.Session != session {
		t.Fatalf("runtime: %+v %v", result, err)
	}
	proof, err := c.ResolveManagerCall(context.Background(), ManagerCallReq{Binding: result.Binding, ToolName: "publish", ArgumentsHash: strings.Repeat("b", 64), HostMeta: []byte(`{"claudecode/toolUseId":"call"}`)})
	if err != nil || !proof.Main || proof.NativeTurnID != "turn" {
		t.Fatalf("proof: %+v %v", proof, err)
	}
	history, err := c.ManagerInputStatus(context.Background(), result.Binding, strings.Repeat("a", 64))
	if err != nil || history.NativeTurnID != "historical" || history.State != "completed" {
		t.Fatalf("historical nonce route: %+v %v", history, err)
	}
	c.reqMu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	_, err = c.ResolveManagerCall(ctx, ManagerCallReq{Binding: result.Binding, ToolName: "publish", ArgumentsHash: strings.Repeat("b", 64), HostMeta: []byte(`{}`)})
	c.reqMu.Unlock()
	if !errors.Is(err, context.Canceled) || time.Since(started) > time.Second {
		t.Fatalf("owner wait ignored cancellation: %v", err)
	}
	remote := rawDial(t, serveRemoteAPI(t, stub))
	hello := remote.hello(Version, []string{CapRemoteGateway, CapManagerProvenance})
	remote.writeControl(Control{Op: OpManagerRuntime, EndpointID: hello.EndpointID, SessionID: hello.EndpointID + "/sess1", BodyVersion: 1, ManagerRuntimeRequest: &ManagerRuntimeReq{SessionInstance: "instance"}})
	if result := remote.readControl(); result.Op != OpError || result.ErrorCode != CodeNotAuthorized {
		t.Fatalf("remote native authority exposed: %+v", result)
	}
}
