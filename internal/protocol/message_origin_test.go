package protocol

import (
	"testing"
	"time"
)

func TestMessageOriginRequiresNegotiationBeforeCustody(t *testing.T) {
	for _, negotiated := range []bool{false, true} {
		t.Run(map[bool]string{false: "refused", true: "preserved"}[negotiated], func(t *testing.T) {
			stub := newDurableComposerStub()
			rc := rawDial(t, serveOwnerAPI(t, stub))
			caps := []string{CapSemanticSend}
			if negotiated {
				caps = append(caps, CapMessageOrigin)
			}
			rep := rc.hello(Version, caps)
			id := semanticTestID(time.Now(), "a")
			frame := semanticTestFrame(rep, id, "colleague context")
			frame.ComposerSend.Origin = &MessageOrigin{Kind: "external", InstallationID: "install", RequestID: "request", ActorID: "actor"}
			rc.writeControl(frame)
			got := rc.readControl()
			if !negotiated {
				if got.Op != OpError || got.ErrorCode != CodeNotImplemented {
					t.Fatalf("unnegotiated origin: %+v", got)
				}
				if _, sends, claims := stub.snapshot(id); sends != 0 || claims != 0 {
					t.Fatal("refused origin reached custody/provider")
				}
				return
			}
			if got.Op != OpOK {
				t.Fatalf("negotiated origin: %+v", got)
			}
			frame.ComposerSend.Origin.ActorID = "different"
			rc.writeControl(frame)
			if got := rc.readControl(); got.Op != OpError {
				t.Fatal("origin change reused receipt")
			}
		})
	}
}

func TestSemanticClientRefusesOriginWithoutCapability(t *testing.T) {
	stub := newDurableComposerStub()
	c, err := Dial(serveOwnerAPI(t, stub), []string{CapSemanticSend})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	id, _ := NewSemanticOperationID()
	err = c.SemanticSend(id, ComposerSendReq{Session: c.EndpointID() + "/sess1", SessionInstance: "test-instance", Text: "context", Origin: &MessageOrigin{Kind: "external", InstallationID: "install", RequestID: "request", ActorID: "actor"}})
	if err == nil {
		t.Fatal("client silently sent provenance to an unnegotiated daemon")
	}
	if _, sends, claims := stub.snapshot(id); sends != 0 || claims != 0 {
		t.Fatal("unnegotiated client reached custody/provider")
	}
}
