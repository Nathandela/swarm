package skeleton

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter/codex"
	"github.com/Nathandela/swarm/internal/status"
)

func TestExecutionStartupPublishesRestoredCodexCustodyWithoutFrames(t *testing.T) {
	for _, human := range []bool{false, true} {
		name := "quiescent"
		if human {
			name = "pending-question"
		}
		t.Run(name, func(t *testing.T) {
			r := newCodexExecutionRig(t, resumeIdentityCodexID, true)
			r.enqueue(t, "turn/started", map[string]any{"threadId": resumeIdentityCodexID, "turn": map[string]any{"id": "native-turn", "status": "inProgress"}})
			if human {
				if !r.feed.ingress.enqueue("item/tool/requestUserInput", []byte(`{"id":"native-question","method":"item/tool/requestUserInput","params":{"threadId":"`+resumeIdentityCodexID+`","turnId":"native-turn","itemId":"question","questions":[]}}`), time.Now()) {
					t.Fatal("native question refused")
				}
			} else {
				r.enqueue(t, "turn/completed", map[string]any{"threadId": resumeIdentityCodexID, "turn": map[string]any{"id": "native-turn", "status": "completed", "error": nil}})
			}
			r.drain(t)
			// Persisted roster state can predate the native custody fold. Reconcile must
			// publish the restored engine projection without waiting for another frame.
			idle := status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone}
			if err := r.d.core.SetStatus(r.local, idle); err != nil {
				t.Fatal(err)
			}
			meta, ok := r.d.core.Get(r.local)
			if !ok {
				t.Fatal("session disappeared")
			}
			r.d.registerExecutionSession(meta, "", codex.New().SignalSources(), false)
			r.d.publishExecutionStatusesForRunning()
			got, ok := r.d.core.Get(r.local)
			if !ok {
				t.Fatal("session disappeared after startup publication")
			}
			if got.Status.Turn != status.TurnUnknown {
				t.Fatalf("restart exposed stale idle roster without later frames: %+v", got.Status)
			}
			want := status.GroupWorking
			if human {
				want = status.GroupNeedsInput
			}
			if group := status.Derive(got.Status); group != want {
				t.Fatalf("restored custody group=%s status=%+v", group, got.Status)
			}
			if human && got.Status.Interaction != status.InteractionPrompt {
				t.Fatalf("restored exact human question was hidden: %+v", got.Status)
			}
			if !r.d.executionCustodyUnsafe(r.local) {
				t.Fatal("startup publication granted a destructive boundary")
			}
			if snapshot, err := r.d.eng.ExecutionSnapshot(r.local); err != nil || snapshot.Codex == nil || !snapshot.Codex.UnknownCustody {
				t.Fatalf("startup requalified persisted native evidence: %+v %v", snapshot, err)
			}
		})
	}
}
