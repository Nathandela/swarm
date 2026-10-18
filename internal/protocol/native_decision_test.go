package protocol

import (
	"bytes"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

type nativeDecisionStub struct {
	*stubDaemon
	calls atomic.Int32
}

func (s *nativeDecisionStub) ApplyNativeDecision(_, id string, req NativeDecisionReq) (NativeDecisionReceipt, ErrorCode, error) {
	s.calls.Add(1)
	return NativeDecisionReceipt{OperationID: id, Session: req.Session, SessionInstance: req.SessionInstance, RootID: req.RootID, Generation: req.Generation, NativeSessionID: req.NativeSessionID, RequestID: req.RequestID, Kind: req.Kind, State: "admitted", NativeEffect: "single_request"}, "", nil
}
func nativeDecisionBody(session string) NativeDecisionReq {
	expires := time.Now().Add(time.Minute)
	return NativeDecisionReq{Session: session, SessionInstance: "instance", RootID: "ses_rootaaaaaaa", Generation: "generation", NativeSessionID: "ses_childaaaaaaa", RequestID: "que_requestaaaaa", RequestHash: strings.Repeat("a", 64), ExpiresAt: &expires, Kind: "question_reply", Answers: [][]string{{"A", "custom"}, {}}}
}

func TestNativeDecisionOwnerClientNegotiationAndReceipt(t *testing.T) {
	s := &nativeDecisionStub{stubDaemon: newStubDaemon()}
	socket := serveOwnerAPI(t, s)
	c, err := Dial(socket, []string{CapNativeRequests})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	body := nativeDecisionBody(c.EndpointID() + "/sess1")
	receipt, err := c.NativeDecide("owner-native-decision", body)
	if err != nil || receipt.State != "admitted" || receipt.RequestID != body.RequestID || s.calls.Load() != 1 {
		t.Fatalf("receipt: %+v %v", receipt, err)
	}
	old, err := Dial(socket, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = old.Close() }()
	if _, err := old.NativeDecide("unnegotiated", body); err == nil || s.calls.Load() != 1 {
		t.Fatal("unnegotiated native effect")
	}
}

func TestNativeDecisionRemoteSignsWholeAnswerBeforeDispatch(t *testing.T) {
	s := &nativeDecisionStub{stubDaemon: newStubDaemon()}
	rc := rawDial(t, serveRemoteAPI(t, s))
	hello := rc.hello(Version, []string{CapRemoteGateway, CapNativeRequests})
	body := nativeDecisionBody(hello.EndpointID + "/sess1")
	frame := Control{Op: OpNativeDecide, EndpointID: hello.EndpointID, SessionID: body.Session, OperationID: "device:native-decision", DeviceID: "devA", DeviceSig: "sig", ExpiresAt: body.ExpiresAt, BodyVersion: 1, NativeDecision: &body}
	rc.writeControl(frame)
	response := rc.readControl()
	if response.Op != OpNativeDecide || response.NativeDecisionReceipt == nil || s.calls.Load() != 1 {
		t.Fatalf("remote native decision: %+v", response)
	}
	tuples := s.authorizedTuples()
	if len(tuples) != 1 || tuples[0].Action != ActionNativeDecide || !bytes.Equal(tuples[0].ContentHash, schema.NativeDecisionContentHash(&body)) {
		t.Fatalf("wrong signing tuple %+v", tuples)
	}
	s.authzFn = func(DeviceCommandAuth) error { return errors.New("signature refused") }
	rc.writeControl(frame)
	if response = rc.readControl(); response.Op != OpError || s.calls.Load() != 1 {
		t.Fatal("unauthorized effect")
	}
}

func TestNativeDecisionInvalidBodyCannotReachBackend(t *testing.T) {
	for _, change := range []func(*NativeDecisionReq){func(r *NativeDecisionReq) { r.RequestHash = "bad" }, func(r *NativeDecisionReq) { r.ExpiresAt = nil }, func(r *NativeDecisionReq) { r.Kind = "prompt" }, func(r *NativeDecisionReq) { r.Answers = [][]string{{strings.Repeat("x", 4097)}} }} {
		s := &nativeDecisionStub{stubDaemon: newStubDaemon()}
		rc := rawDial(t, serveOwnerAPI(t, s))
		hello := rc.hello(Version, []string{CapNativeRequests})
		body := nativeDecisionBody(hello.EndpointID + "/sess1")
		change(&body)
		rc.writeControl(Control{Op: OpNativeDecide, EndpointID: hello.EndpointID, SessionID: body.Session, OperationID: "invalid", BodyVersion: 1, NativeDecision: &body})
		if response := rc.readControl(); response.Op != OpError || s.calls.Load() != 0 {
			t.Fatalf("invalid request reached backend: %+v", response)
		}
	}
}
