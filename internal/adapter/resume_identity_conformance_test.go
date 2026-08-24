package adapter

import "testing"

// panicOnSecondConversationIdentity is total on the first probe and panics only on the
// determinism recheck. The conformance harness must report that second panic rather than
// discarding the safe wrapper's panic bit and accidentally certifying the extension.
type panicOnSecondConversationIdentity struct {
	baseAdapter
	calls int
}

func (a *panicOnSecondConversationIdentity) ConversationIDFromEvent(HookPayload) (string, bool) {
	a.calls++
	if a.calls == 2 {
		panic("second identity invocation")
	}
	return "", false
}

func TestCheckConformance_ConversationIdentitySecondInvocationPanicIsReported(t *testing.T) {
	err := CheckConformance(&panicOnSecondConversationIdentity{})
	if !errsContain(err, "conversationidfromevent") || !errsContain(err, "panic") {
		t.Fatalf("second ConversationIDFromEvent panic was not reported: %v", err)
	}
}
