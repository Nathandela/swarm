package protocol

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

type nativeOwnerInputStub struct{ *stubDaemon }

func (s *nativeOwnerInputStub) SessionCapabilities(local string) (SessionCapabilities, bool) {
	return SessionCapabilities{Provider: "claude", SessionInstance: "instance", StructuredChat: true}, true
}
func (s *nativeOwnerInputStub) OwnerSessionCapabilities(local string, _ int, _ int64) (SessionCapabilities, bool) {
	return SessionCapabilities{Provider: "claude", SessionInstance: "instance", StructuredChat: true, NativeOwnerInput: local == "sess1"}, true
}
func (s *nativeOwnerInputStub) NativeOwnerInput(local, instance string) bool {
	return local == "sess1" && instance == "instance"
}
func TestOwnerRosterCarriesFreshNativeInputCapability(t *testing.T) {
	s := &Server{d: &nativeOwnerInputStub{newStubDaemon()}}
	view := s.stampView("endpoint", persist.Meta{ID: "sess1", AgentType: "claude", Status: status.Status{Process: status.ProcessRunning}}, status.Group(""), false, false, time.Time{})
	if view.Capabilities == nil || !view.Capabilities.NativeOwnerInput || view.Capabilities.NativeComposer {
		t.Fatal("owner roster lost distinct live native input capability")
	}
	s.remoteTier = true
	view = s.stampView("endpoint", persist.Meta{ID: "sess1", AgentType: "claude", Status: status.Status{Process: status.ProcessRunning}}, status.Group(""), false, false, time.Time{})
	if view.Capabilities != nil {
		t.Fatal("owner input capability appeared on remote roster")
	}
}

type slowNativeOwner struct {
	*nativeOwnerInputStub
	release chan struct{}
}

func (s *slowNativeOwner) NativeOwnerInput(string, string) bool { <-s.release; return false }
func TestStatusFanoutDoesNotWaitForNativeProbe(t *testing.T) {
	source := &slowNativeOwner{nativeOwnerInputStub: &nativeOwnerInputStub{newStubDaemon()}, release: make(chan struct{})}
	server := &Server{d: source, endpointID: "endpoint"}
	started := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		server.distribute(persist.Meta{ID: "sess1", AgentType: "claude", Status: status.Status{Process: status.ProcessRunning}})
	}()
	select {
	case <-done:
		t.Logf("status fanout latency=%s", time.Since(started))
	case <-time.After(75 * time.Millisecond):
		t.Error("single status fanout blocked by optional native input probe")
	}
	close(source.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("fanout did not unblock")
	}
}
