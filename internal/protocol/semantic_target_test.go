package protocol

import (
	"sync/atomic"
	"testing"
)

type semanticTargetBackend struct {
	*durableComposerStub
	lookups atomic.Int32
}

func (b *semanticTargetBackend) ResolveSemanticTarget(local string) (string, ErrorCode, error) {
	b.lookups.Add(1)
	return local + "-instance", "", nil
}

func TestSemanticTargetIsReadOnlyAndEndpointBound(t *testing.T) {
	b := &semanticTargetBackend{durableComposerStub: newDurableComposerStub()}
	c, err := Dial(serveOwnerAPI(t, b), []string{CapSemanticTarget})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	instance, err := c.SemanticTarget(c.EndpointID() + "/sess1")
	if err != nil || instance != "sess1-instance" {
		t.Fatalf("target = %q, %v", instance, err)
	}
	if _, err := c.SemanticTarget("wrong/sess1"); err == nil || b.lookups.Load() != 1 {
		t.Fatalf("wrong endpoint reached the backend: %v (%d lookups)", err, b.lookups.Load())
	}
	if _, sends, claims := b.snapshot(""); sends != 0 || claims != 0 {
		t.Fatalf("target lookup mutated operation custody: %d sends, %d claims", sends, claims)
	}
	raw := rawDial(t, serveRemoteAPI(t, b))
	hello := raw.hello(Version, []string{CapRemoteGateway, CapSemanticTarget})
	raw.writeControl(Control{Op: OpSemanticTarget, EndpointID: hello.EndpointID, SessionID: hello.EndpointID + "/sess1"})
	if got := raw.readControl(); got.Op != OpError || b.lookups.Load() != 1 {
		t.Fatalf("remote target lookup: %+v", got)
	}
}
