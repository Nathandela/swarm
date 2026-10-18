package skeleton

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/opencodebridge"
	"github.com/Nathandela/swarm/internal/protocol"
)

func TestOpenCodeOwnedInputRecoversBeyondStartupHistoryWindow(t *testing.T) {
	fixture := newOpenCodeSendFixture(t)
	for _, scenario := range []string{"exact", "lost admission", "lost admission wrong body", "unowned parent", "wrong body", "foreign row", "synthetic text", "old row"} {
		t.Run(scenario, func(t *testing.T) {
			f := fixture
			if scenario == "lost admission" || scenario == "lost admission wrong body" {
				f = newOpenCodeSendFixture(t)
			}
			id, err := protocol.NewSemanticOperationID()
			if err != nil {
				t.Fatal(err)
			}
			lost := scenario == "lost admission" || scenario == "lost admission wrong body"
			f.dropReply.Store(lost)
			err = f.client.SemanticSend(id, f.request)
			f.dropReply.Store(false)
			if (err != nil) != lost {
				t.Fatalf("unexpected native delivery result for %s: %v", scenario, err)
			}
			record, _ := f.d.core.ComposerOperation(id)
			var intent openCodeSendIntent
			if err = json.Unmarshal(record.Request, &intent); err != nil {
				t.Fatal(err)
			}
			meta, _ := f.d.core.Get(f.binding.WorkerID)
			projection, err := newOpenCodeTranscript(f.d, meta, f.identity)
			if err != nil {
				t.Fatal(err)
			}
			reader := &openCodeQualifiedReadyFake{ocReaderFake: newOCReaderFake()}
			reader.identity = f.identity
			reader.ready = opencodebridge.Ready{Identity: f.identity, NativeVersion: "1.19.3", Acknowledged: true, Bound: true, Ready: true, DraftGuardSupported: true}
			parent := intent.MessageID
			if scenario == "unowned parent" {
				parent = "msg_other_compaction_input"
			}
			rows := []json.RawMessage{}
			assistantAt := time.Now().UnixMilli()
			for n := 0; n < 50; n++ {
				finish := "tool-calls"
				if n == 49 {
					finish = "stop"
				}
				row, _ := json.Marshal(map[string]any{"info": map[string]any{"id": fmt.Sprintf("msg_history_%03d", n), "sessionID": f.identity.RootID, "role": "assistant", "parentID": parent, "finish": finish, "time": map[string]int64{"created": assistantAt + int64(n) + 1, "completed": assistantAt + int64(n) + 2}}, "parts": []any{}})
				rows = append(rows, row)
			}
			root, _ := json.Marshal(map[string]any{"id": f.identity.RootID, "directory": f.identity.Directory, "time": map[string]int64{"created": f.identity.NativeCreatedAtMS}})
			reader.snapshot = func(int) opencodebridge.Snapshot {
				snapshot := reader.idleSnapshot()
				snapshot.Session = root
				snapshot.Messages = rows
				return snapshot
			}
			var user map[string]any
			if err = json.Unmarshal(f.nativeRow.Load().(json.RawMessage), &user); err != nil {
				t.Fatal(err)
			}
			info := user["info"].(map[string]any)
			part := user["parts"].([]any)[0].(map[string]any)
			switch scenario {
			case "wrong body", "lost admission wrong body":
				part["text"] = "different input"
			case "foreign row":
				info["sessionID"] = "ses_foreign"
			case "synthetic text":
				part["synthetic"] = true
			case "old row":
				info["time"].(map[string]any)["created"] = intent.NotBeforeMS - 1
			}
			raw, _ := json.Marshal(user)
			var exactReads atomic.Int32
			reader.message = func(message string) (json.RawMessage, error) {
				exactReads.Add(1)
				if message != intent.MessageID {
					return nil, fmt.Errorf("unexpected native lookup %s", message)
				}
				return raw, nil
			}
			state, _ := NewOpenCodeState(f.identity.RootID, f.identity.BackendGeneration)
			ctx, cancel := context.WithCancel(context.Background())
			done, ready := make(chan struct{}), make(chan error, 1)
			views := make(chan OpenCodeView, 16)
			go func() {
				defer close(done)
				observeOpenCode(ctx, reader, f.identity, state, func(view OpenCodeView) {
					f.d.checkpointOpenCodeOutcomes(f.binding.WorkerID, view)
					select {
					case views <- view:
					case <-ctx.Done():
					}
				}, func(err error) { ready <- err }, projection)
			}()
			defer func() { cancel(); <-done }()
			startupErr := <-ready
			if scenario != "exact" && scenario != "lost admission" && scenario != "unowned parent" {
				if startupErr == nil {
					t.Fatal("invalid full owned user row qualified startup")
				}
			} else {
				if startupErr != nil {
					t.Fatal(startupErr)
				}
				view := nextOCView(t, views, func(view OpenCodeView) bool { return view.BusyKnown })
				if scenario == "exact" || scenario == "lost admission" {
					if len(view.Inputs) != 1 || !view.Inputs[0].PlainTextInput || view.Inputs[0].Outcome != OpenCodeOutcomeCompleted || len(view.Uncertain) != 0 {
						t.Fatalf("bounded history did not recover exact owned input: %+v", view)
					}
					reader.events <- opencodebridge.Event{Type: "permission.replied", Properties: json.RawMessage(`{"sessionID":"` + f.identity.RootID + `","requestID":"per_done","reply":"once"}`)}
					deadline := time.Now().Add(2 * time.Second)
					for time.Now().Before(deadline) {
						reader.mu.Lock()
						reads := reader.reads
						reader.mu.Unlock()
						if reads >= 2 {
							break
						}
						time.Sleep(5 * time.Millisecond)
					}
				} else if len(view.Inputs) != 0 {
					t.Fatal("unowned continuation borrowed original input")
				}
			}
			receipt, err := f.client.SemanticOperation(f.request.Session, id)
			if err != nil {
				t.Fatal(err)
			}
			want := "admitted"
			if scenario == "lost admission wrong body" {
				want = "uncertain"
			}
			wantReads := int32(1)
			if scenario == "exact" || scenario == "lost admission" {
				want = "completed"
			}
			if scenario == "unowned parent" {
				wantReads = 0
			}
			if receipt.State != want || exactReads.Load() != wantReads {
				t.Fatalf("receipt=%s exact reads=%d, want %s/%d", receipt.State, exactReads.Load(), want, wantReads)
			}
		})
	}
}
