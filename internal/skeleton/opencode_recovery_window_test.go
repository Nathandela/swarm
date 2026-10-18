package skeleton

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/opencodebridge"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
)

func TestOpenCodeOwnedInputRecoversBeyondStartupHistoryWindow(t *testing.T) {
	fixture := newOpenCodeSendFixture(t)
	for _, scenario := range []string{"exact", "lost admission", "lost admission wrong body", "unowned parent", "wrong body", "foreign row", "synthetic text", "old row", "initial exact", "initial wrong body", "initial foreign row", "initial wrong tuple", "retained failure", "retained completion newer", "native error later", "retained completion tie", "retained failure tie", "retained failure present", "retained completion tie present", "initial omitted variant", "semantic omitted variant", "lost omitted variant", "variant mismatch"} {
		t.Run(scenario, func(t *testing.T) {
			f := fixture
			initial := strings.HasPrefix(scenario, "initial ")
			if initial || strings.Contains(scenario, "omitted variant") || scenario == "lost admission" || scenario == "lost admission wrong body" {
				f = newOpenCodeSendFixture(t, func(f *openCodeSendFixture) {
					f.initialNativeMode = initial
					f.defaultNativeVariant = strings.Contains(scenario, "omitted variant")
				})
			}
			id, err := protocol.NewSemanticOperationID()
			if err != nil {
				t.Fatal(err)
			}
			lost := scenario == "lost admission" || scenario == "lost admission wrong body" || scenario == "lost omitted variant"
			var intent openCodeSendIntent
			if !initial {
				f.dropReply.Store(lost)
				err = f.client.SemanticSend(id, f.request)
				f.dropReply.Store(false)
				if (err != nil) != lost {
					t.Fatalf("unexpected native delivery result for %s: %v", scenario, err)
				}
				record, _ := f.d.core.ComposerOperation(id)
				if err = json.Unmarshal(record.Request, &intent); err != nil {
					t.Fatal(err)
				}
			}
			meta, _ := f.d.core.Get(f.binding.WorkerID)
			if initial {
				i := meta.OpenCodeBinding.Initial
				id = i.OperationID
				intent = openCodeSendIntent{Identity: f.identity, MessageID: i.MessageID, TextHash: i.RequestHash, NotBeforeMS: meta.OpenCodeBinding.NativeCreatedAtMS}
			}
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
			wantOutcome := OpenCodeOutcomeCompleted
			if scenario == "retained failure" || scenario == "retained failure tie" || scenario == "retained failure present" {
				wantOutcome = OpenCodeOutcomeFailed
			}
			if scenario == "native error later" {
				wantOutcome = OpenCodeOutcomeCancelled
			}
			rows := []json.RawMessage{}
			assistantAt := time.Now().UnixMilli()
			for n := 0; n < 50; n++ {
				finish := "tool-calls"
				if n == 49 {
					finish = "stop"
				}
				row, _ := json.Marshal(map[string]any{"info": map[string]any{"id": fmt.Sprintf("msg_history_%03d", n), "sessionID": f.identity.RootID, "role": "assistant", "parentID": parent, "finish": finish, "time": map[string]int64{"created": assistantAt + int64(n) + 1, "completed": assistantAt + int64(n) + 2}}, "parts": []any{}})
				if (scenario == "native error later" || scenario == "retained failure tie") && n == 49 {
					var final map[string]any
					_ = json.Unmarshal(row, &final)
					final["info"].(map[string]any)["error"] = map[string]any{"name": "MessageAbortedError", "data": map[string]string{"message": "cancelled"}}
					row, _ = json.Marshal(final)
				}
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
			case "wrong body", "lost admission wrong body", "initial wrong body":
				part["text"] = "different input"
			case "foreign row", "initial foreign row":
				info["sessionID"] = "ses_foreign"
			case "variant mismatch":
				info["model"].(map[string]any)["variant"] = "high"
			case "initial wrong tuple":
				info["agent"] = "different-agent"
			case "synthetic text":
				part["synthetic"] = true
			case "old row":
				info["time"].(map[string]any)["created"] = intent.NotBeforeMS - 1
			}
			if strings.HasPrefix(scenario, "retained ") || scenario == "native error later" {
				prior := OpenCodeOutcomeFailed
				at := assistantAt + 1
				if scenario == "retained completion newer" || strings.HasPrefix(scenario, "retained completion tie") {
					prior = OpenCodeOutcomeCompleted
					at = assistantAt + 200
				}
				if strings.HasPrefix(scenario, "retained completion tie") || scenario == "retained failure tie" {
					at = assistantAt + 51
				}
				inputAt := int64(info["time"].(map[string]any)["created"].(float64))
				f.d.checkpointOpenCodeOutcomes(f.binding.WorkerID, OpenCodeView{Root: f.identity.RootID, Generation: f.identity.BackendGeneration, Inputs: []OpenCodeInput{{MessageID: intent.MessageID, Classified: true, At: inputAt, Outcome: prior, OutcomeSource: OpenCodeOutcomeSourceLive, OutcomeAt: at}}})
			}
			raw, _ := json.Marshal(user)
			present := strings.HasSuffix(scenario, " present")
			if present {
				rows = append([]json.RawMessage{raw}, rows[1:]...)
			}
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
			qualified := scenario == "exact" || scenario == "initial exact" || scenario == "lost admission" || strings.HasPrefix(scenario, "retained ") || strings.Contains(scenario, "omitted variant") || scenario == "native error later"
			if !qualified && scenario != "unowned parent" {
				if startupErr == nil {
					t.Fatal("invalid full owned user row qualified startup")
				}
			} else {
				if startupErr != nil {
					t.Fatal(startupErr)
				}
				view := nextOCView(t, views, func(view OpenCodeView) bool { return view.BusyKnown })
				if qualified {
					if len(view.Inputs) != 1 || !view.Inputs[0].PlainTextInput || view.Inputs[0].Outcome != wantOutcome || len(view.Uncertain) != 0 {
						t.Fatalf("bounded history recovery inputs=%+v uncertain=%+v", view.Inputs, view.Uncertain)
					}
					if scenario == "retained completion newer" && (view.Inputs[0].OutcomeAt != assistantAt+200 || view.Inputs[0].OutcomeSource != OpenCodeOutcomeSourceLive) {
						t.Fatal("newer retained outcome/source lost during hydration")
					}
					if (strings.HasPrefix(scenario, "retained completion tie") || scenario == "retained failure tie") && view.Inputs[0].OutcomeSource != OpenCodeOutcomeSourceLive {
						t.Fatal("equal-time retained outcome lost first source")
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
			if initial {
				if len(f.d.core.SemanticRequestOperations(f.binding.WorkerID)) != 0 || exactReads.Load() != 1 {
					t.Fatal("initial recovery used semantic custody or repeated native reads")
				}
				return
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
			if present {
				wantReads = 0
			}
			if qualified {
				want = string(wantOutcome)
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

func TestOpenCodeInitialRecoveryRequiresExactAdmittedBinding(t *testing.T) {
	f := newOpenCodeSendFixture(t, func(f *openCodeSendFixture) { f.initialNativeMode = true })
	original, _ := f.d.core.Get(f.binding.WorkerID)
	for _, scenario := range []string{"valid", "unadmitted", "wrong hash", "wrong generation", "wrong root", "wrong target", "missing creation"} {
		t.Run(scenario, func(t *testing.T) {
			meta := original
			meta.OpenCodeBinding = persist.CloneOpenCodeBinding(original.OpenCodeBinding)
			target := meta.OpenCodeBinding.Initial.MessageID
			switch scenario {
			case "unadmitted":
				meta.OpenCodeBinding.Initial.Phase = persist.OpenCodeInitialUncertain
			case "wrong hash":
				meta.OpenCodeBinding.Initial.RequestHash = "bad"
			case "wrong generation":
				meta.OpenCodeBinding.Generation = strings.Repeat("e", 32)
			case "wrong root":
				meta.ConversationID = "ses_" + strings.Repeat("e", 32)
			case "wrong target":
				target = "msg_" + strings.Repeat("e", 32)
			case "missing creation":
				meta.OpenCodeBinding.NativeCreatedAtMS = 0
			}
			entry, expected := initialOpenCodeRecovery(meta, f.identity, target)
			if (expected != nil) != (scenario == "valid") {
				t.Fatalf("initial recovery accepted unqualified %s custody", scenario)
			}
			if expected != nil && (entry.intent.TextHash != meta.OpenCodeBinding.Initial.RequestHash || entry.intent.NotBeforeMS != f.identity.NativeCreatedAtMS) {
				t.Fatal("initial recovery lost exact hash or native creation bound")
			}
		})
	}
}
