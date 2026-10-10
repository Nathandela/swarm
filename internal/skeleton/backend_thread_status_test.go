package skeleton

import (
	"fmt"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter/codex"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/status"
)

func TestBackendChildTurnCannotFinishParentDiscussion(t *testing.T) {
	r := newResumeIdentityBackendRig(t)
	var got status.Status
	r.sk.eng = engine.New(engine.Config{Emit: func(_ string, s status.Status) { got = s }})
	r.sk.eng.RegisterSession(resumeIdentityLocal, "", 0, codex.New().SignalSources())
	r.sk.adoptBackendThread(resumeIdentityLocal, resumeIdentityCodexID)
	frame := func(method, thread string) []byte {
		return []byte(fmt.Sprintf(`{"method":%q,"params":{"threadId":%q,"turn":{"id":"turn-1","status":"completed","items":[]}}}`, method, thread))
	}
	r.sk.ingestBackendFrame(resumeIdentityLocal, frame("turn/started", resumeIdentityCodexID), time.Now().UnixMilli())
	if got.Turn != status.TurnActive {
		t.Fatalf("parent start: %+v", got)
	}
	r.sk.ingestBackendFrame(resumeIdentityLocal, frame("turn/completed", resumeIdentityOtherCodex), time.Now().UnixMilli())
	if got.Turn != status.TurnActive {
		t.Fatalf("child completion changed parent status: %+v", got)
	}
	for _, raw := range []string{
		`{"method":"turn/completed","params":{"turn":{"id":"turn-1"}}}`,
		fmt.Sprintf(`{"method":"turn/completed","params":{"threadId":%q,"threadId":%q}}`, resumeIdentityOtherCodex, resumeIdentityCodexID),
		fmt.Sprintf(`{"method":"turn/completed","params":{"threadId":%q,"ThreadId":%q}}`, resumeIdentityCodexID, resumeIdentityOtherCodex),
		fmt.Sprintf(`{"method":"thread/started","params":{"thread":{"id":%q}}}`, resumeIdentityOtherCodex),
	} {
		r.sk.ingestBackendFrame(resumeIdentityLocal, []byte(raw), time.Now().UnixMilli())
		if got.Turn != status.TurnActive {
			t.Fatalf("unowned frame changed parent status: %s -> %+v", raw, got)
		}
		if id := r.meta(t).ConversationID; id != resumeIdentityCodexID {
			t.Fatalf("child repointed session to %s", id)
		}
	}
	r.sk.ingestBackendFrame(resumeIdentityLocal, frame("turn/completed", resumeIdentityCodexID), time.Now().UnixMilli())
	if got.Turn != status.TurnIdle {
		t.Fatalf("parent completion: %+v", got)
	}
}

func TestBackendPersistedIdentityFiltersBeforeRejoin(t *testing.T) {
	r := newResumeIdentityBackendRig(t)
	if err := r.core.SetConversationID(resumeIdentityLocal, resumeIdentityCodexID); err != nil {
		t.Fatal(err)
	}
	r.restart(t)
	var got status.Status
	r.sk.eng = engine.New(engine.Config{Emit: func(_ string, s status.Status) { got = s }})
	r.sk.eng.RegisterSession(resumeIdentityLocal, "", 0, codex.New().SignalSources())
	for _, event := range []struct{ method, thread string }{{"turn/started", resumeIdentityCodexID}, {"turn/completed", resumeIdentityOtherCodex}} {
		frame := []byte(fmt.Sprintf(`{"method":%q,"params":{"threadId":%q}}`, event.method, event.thread))
		r.sk.ingestBackendFrame(resumeIdentityLocal, frame, time.Now().UnixMilli())
	}
	if got.Turn != status.TurnActive {
		t.Fatalf("child frame admitted before backend rejoin: %+v", got)
	}
}

func TestBackendForeignFramesCannotReachTranscript(t *testing.T) {
	sk, ad, local := r7PumpRig(t)
	sk.backend.adopted = map[string]string{local: resumeIdentityOtherCodex}
	sk.ingestBackendFrame(local, r7DeltaFrame("foreign-message", "child prose"), time.Now().UnixMilli())
	sk.flushBackendFrames(local)
	if got := ad.payloads(); len(got) != 0 {
		t.Fatalf("foreign thread reached transcript: %+v", got)
	}
}
