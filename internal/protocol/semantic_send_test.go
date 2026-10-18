package protocol

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type semanticBoundaryStub struct {
	*durableComposerStub
	ownerCalls atomic.Int64
}

func (s *durableComposerStub) SemanticSendTransactional(machine, id string, req ComposerSendReq, begin func() error) (ErrorCode, error) {
	return s.ComposerSendTransactional(machine, id, req, begin)
}

func (s *semanticBoundaryStub) SemanticSendTransactional(machine, id string, req ComposerSendReq, begin func() error) (ErrorCode, error) {
	s.ownerCalls.Add(1)
	return s.durableComposerStub.ComposerSendTransactional(machine, id, req, begin)
}

func TestOwnerSemanticSendUsesOwnerAttributionBoundary(t *testing.T) {
	stub := &semanticBoundaryStub{durableComposerStub: newDurableComposerStub()}
	rc := rawDial(t, serveOwnerAPI(t, stub))
	rep := rc.hello(Version, []string{"semantic-send"})
	rc.writeControl(semanticTestFrame(rep, semanticTestID(time.Now(), "c"), "owner automation"))
	if got := rc.readControl(); got.Op != OpOK {
		t.Fatalf("owner send: %+v", got)
	}
	if stub.ownerCalls.Load() != 1 {
		t.Fatal("owner semantic send went through the phone-only attribution seam")
	}
}

func semanticTestFrame(rep Control, id, text string) Control {
	session := rep.EndpointID + "/sess1"
	return Control{Op: "semantic_send", EndpointID: rep.EndpointID, SessionID: session,
		OperationID: id, BodyVersion: 1,
		ComposerSend: &ComposerSendReq{Session: session, SessionInstance: "test-instance", Text: text}}
}

func semanticTestID(at time.Time, nonce string) string {
	return fmt.Sprintf("semantic:%d:%s", at.UnixMilli(), strings.Repeat(nonce, 32))
}

func TestSemanticClientPreservesOperationAndPayloadAcrossRetry(t *testing.T) {
	stub := newDurableComposerStub()
	c, err := Dial(serveOwnerAPI(t, stub), []string{CapSemanticSend})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	id, err := NewSemanticOperationID()
	if err != nil || !validSemanticOperationID(id, time.Now()) {
		t.Fatalf("generated operation ID = %q, %v", id, err)
	}
	req := ComposerSendReq{Session: c.EndpointID() + "/sess1", SessionInstance: "test-instance", Text: "exact\nmessage"}
	for i := 0; i < 2; i++ {
		if err := c.SemanticSend(id, req); err != nil {
			t.Fatalf("send/retry %d: %v", i, err)
		}
	}
	if _, sends, _ := stub.snapshot(id); sends != 1 {
		t.Fatalf("one operation caused %d sends", sends)
	}
	req.Text = "changed"
	if err := c.SemanticSend(id, req); err == nil {
		t.Fatal("a changed payload reused the cached receipt")
	}
}

func TestSemanticClientRefusesUnnegotiatedRoute(t *testing.T) {
	stub := newDurableComposerStub()
	c, err := Dial(serveOwnerAPI(t, stub), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	id, err := NewSemanticOperationID()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SemanticSend(id, ComposerSendReq{Session: c.EndpointID() + "/sess1", SessionInstance: "test-instance", Text: "never send"}); err == nil {
		t.Fatal("unnegotiated semantic message succeeded")
	}
	if _, sends, claims := stub.snapshot(id); sends != 0 || claims != 0 {
		t.Fatal("unnegotiated client performed provider/custody I/O")
	}
}

func TestSemanticClientDoesNotDiscardPriorCustodyOnExpiredRetry(t *testing.T) {
	stub := newDurableComposerStub()
	c, err := Dial(serveOwnerAPI(t, stub), []string{CapSemanticSend})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	req := ComposerSendReq{Session: c.EndpointID() + "/sess1", SessionInstance: "test-instance", Text: "original"}
	err = c.SemanticSend(semanticTestID(time.Now().Add(-2*time.Hour), "e"), req)
	if err == nil || errors.Is(err, ErrSemanticSendRejected) {
		t.Fatal("expired retry was treated as proof that the original operation had no effect")
	}
	stub.sendCode, stub.sendErr = CodeInputBusy, errors.New("input was dirty; nothing sent")
	err = c.SemanticSend(semanticTestID(time.Now(), "f"), req)
	if !errors.Is(err, ErrSemanticSendRejected) {
		t.Fatalf("durably rejected operation did not expose its no-effect receipt: %v", err)
	}
}

func TestOwnerSemanticSendUsesDurableComposerBoundary(t *testing.T) {
	stub := newDurableComposerStub()
	rc := rawDial(t, serveOwnerAPI(t, stub))
	rep := rc.hello(Version, []string{"semantic-send"})
	if !slices.Contains(rep.Capabilities, "semantic-send") {
		t.Fatal("durable owner semantic route was not negotiated")
	}
	id := semanticTestID(time.Now(), "a")
	frame := semanticTestFrame(rep, id, "exact message")
	for i := 0; i < 2; i++ {
		rc.writeControl(frame)
		if got := rc.readControl(); got.Op != OpOK || got.OperationID != id {
			t.Fatalf("send/replay %d: %+v", i, got)
		}
	}
	rec, sends, _ := stub.snapshot(id)
	if sends != 1 || rec.phase != composerPhaseCompleted || rec.action != "semantic_send" {
		t.Fatalf("owner send bypassed durable action custody: sends=%d record=%+v", sends, rec)
	}
	changed := *frame.ComposerSend
	changed.Text = "different message"
	frame.ComposerSend = &changed
	rc.writeControl(frame)
	if got := rc.readControl(); got.Op != OpError {
		t.Fatalf("changed payload reused a receipt: %+v", got)
	}
	if _, sends, _ := stub.snapshot(id); sends != 1 {
		t.Fatal("conflicting payload reached the provider")
	}
}

func TestOwnerSemanticSendUncertainWriteNeverReplays(t *testing.T) {
	stub := newDurableComposerStub()
	stub.sendErr = ErrComposerOutcomeUnknown
	stub.sendCode = CodeOutcomeUnknown
	rc := rawDial(t, serveOwnerAPI(t, stub))
	rep := rc.hello(Version, []string{"semantic-send"})
	id := semanticTestID(time.Now(), "b")
	frame := semanticTestFrame(rep, id, "uncertain message")
	for i := 0; i < 2; i++ {
		rc.writeControl(frame)
		if got := rc.readControl(); got.Op != OpError || got.ErrorCode != CodeOutcomeUnknown {
			t.Fatalf("uncertain send/replay %d: %+v", i, got)
		}
	}
	rec, sends, _ := stub.snapshot(id)
	if sends != 1 || rec.phase != composerPhaseExecuting {
		t.Fatalf("uncertain native write lost its guard: sends=%d record=%+v", sends, rec)
	}
}

func TestOwnerSemanticSendRefusesExpiredOrMalformedOperationBeforeClaim(t *testing.T) {
	stub := newDurableComposerStub()
	rc := rawDial(t, serveOwnerAPI(t, stub))
	rep := rc.hello(Version, []string{"semantic-send"})
	for _, id := range []string{"", "opaque-id", semanticTestID(time.Now().Add(-25*time.Hour), "a"), semanticTestID(time.Now().Add(2*time.Minute), "a"), semanticTestID(time.Now(), "z")} {
		rc.writeControl(semanticTestFrame(rep, id, "never write"))
		if got := rc.readControl(); got.Op != OpError || got.ErrorCode != CodeInvalidField {
			t.Fatalf("operation %q was not refused by its replay horizon: %+v", id, got)
		}
	}
	if _, sends, claims := stub.snapshot(""); sends != 0 || claims != 0 {
		t.Fatalf("invalid operation reached custody/provider: sends=%d claims=%d", sends, claims)
	}
}

func TestOwnerSemanticSendRequiresNegotiationAndDurableBackend(t *testing.T) {
	stub := newDurableComposerStub()
	rc := rawDial(t, serveOwnerAPI(t, stub))
	rep := rc.hello(Version, nil)
	rc.writeControl(semanticTestFrame(rep, semanticTestID(time.Now(), "a"), "no capability"))
	if got := rc.readControl(); got.Op != OpError || got.ErrorCode != CodeNotImplemented {
		t.Fatalf("unnegotiated semantic send: %+v", got)
	}
	if _, sends, claims := stub.snapshot(""); sends != 0 || claims != 0 {
		t.Fatal("unnegotiated semantic route reached custody/provider")
	}
	older := newComposerIdempotencyStub()
	rc = rawDial(t, serveOwnerAPI(t, older))
	rep = rc.hello(Version, []string{"semantic-send"})
	if slices.Contains(rep.Capabilities, "semantic-send") {
		t.Fatal("server without durable composer custody advertised the route")
	}
	rc.writeControl(semanticTestFrame(rep, semanticTestID(time.Now(), "b"), "no durable backend"))
	if got := rc.readControl(); got.Op != OpError || older.sendCount() != 0 {
		t.Fatalf("semantic send fell back to a non-durable path: %+v", got)
	}
}

func TestRemoteTierCannotUseOwnerSemanticRoute(t *testing.T) {
	stub := newDurableComposerStub()
	rc := rawDial(t, serveRemoteAPI(t, stub))
	rep := rc.hello(Version, []string{CapRemoteGateway, "semantic-send"})
	if slices.Contains(rep.Capabilities, "semantic-send") {
		t.Fatal("owner semantic route advertised to remote tier")
	}
	rc.writeControl(semanticTestFrame(rep, semanticTestID(time.Now(), "a"), "remote attempt"))
	if got := rc.readControl(); got.Op != OpError || got.ErrorCode != CodeNotAuthorized {
		t.Fatalf("remote tier reached unsigned owner mutation: %+v", got)
	}
	if _, sends, claims := stub.snapshot(""); sends != 0 || claims != 0 {
		t.Fatal("remote attempt reached custody/provider")
	}
}
