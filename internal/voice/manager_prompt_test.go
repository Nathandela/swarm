package voice

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
)

func TestManagerStartingPromptOnTurnAndWorkerWake(t *testing.T) {
	controller, store, runtime, binding := newControllerFixture(t)
	root := t.TempDir()
	controller.managerSettingsDir = root
	if got, err := LoadManagerStartingPrompt(root); got != "" || err != nil {
		t.Fatalf("unset prompt = %q, %v", got, err)
	}
	if err := SaveManagerStartingPrompt(root, "Check the real output."); err != nil {
		t.Fatal(err)
	}
	trackControllerFixtureWorker(t, store, binding)
	for i, id := range []string{"first", "wake"} {
		if i == 0 {
			if _, err := controller.SubmitText(Utterance{ID: id, DeviceID: "device", VoiceSessionID: "voice", MediaEpoch: 1}, "hello"); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := SaveManagerStartingPrompt(root, "Acknowledge the worker."); err != nil {
				t.Fatal(err)
			}
			event := Event{ID: id, Cursor: JournalCursor{Binding: "journal", Sequence: 1}, Metadata: json.RawMessage(`{"task":"finished"}`)}
			if _, err := controller.AcceptWorkerEvent("machine/worker", "instance", event); err != nil {
				t.Fatal(err)
			}
		}
		runtime.beforeSend = func(_ string, req protocol.ComposerSendReq) {
			want := []string{"Check the real output.", "Acknowledge the worker."}[i]
			if !strings.Contains(req.Text, want) {
				t.Errorf("current settings absent from turn %d: %q", i, req.Text)
			}
		}
		if err := controller.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(runtime.sent) != i+1 {
			t.Fatalf("turn %d did not send: %+v", i, runtime.sent)
		}
		runtime.status.Turn = protocol.ManagerNativeTurn{State: "completed", NativeInputID: runtime.sent[i].ManagerAdmission.Nonce, NativeTurnID: id}
	}
	if got, err := LoadManagerStartingPrompt(root); got != "Acknowledge the worker." || err != nil {
		t.Fatalf("saved prompt = %q, %v", got, err)
	}
}
