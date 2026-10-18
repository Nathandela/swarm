package skeleton

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
)

func TestNativeSupervisionKeysSurviveUnknownAndDoNotRepeatImportedCompletion(t *testing.T) {
	parent := plainMeta("parent", stReady)
	child := plainMeta("kid", stReady)
	child.RequiredRuntime = persist.RequiredRuntimeOpenCode
	child.SpawnedFrom = "parent"
	child.SpawnIntent = protocol.SpawnIntentHandoff
	child.Supervision = protocol.SupervisionPassive
	f := newSupFakes(parent, child)
	event := nativeSupervisionEvent{Known: true, Fresh: true, Key: "input:a:completed"}
	s := &supervisor{endpointID: supEndpoint, dir: t.TempDir(), get: f.get, getSource: f.get, controlled: f.isControlled, send: f.send, records: map[string]*supervisionRecord{}, nativeEvent: func(persist.Meta) nativeSupervisionEvent { return event }}
	s.arm(child)
	s.signal("kid")
	if s.records["kid"].Pending == nil {
		t.Fatal("new native completion was not queued")
	}
	s.deliver("kid", "parent", *s.records["kid"].Pending)
	if len(f.delivered()) != 1 {
		t.Fatal("first notice not admitted")
	}
	raw, err := os.ReadFile(filepath.Join(s.dir, "kid.json"))
	if err != nil {
		t.Fatal(err)
	}
	var loaded supervisionRecord
	if json.Unmarshal(raw, &loaded) != nil || loaded.NativeDeliveredKey != event.Key {
		t.Fatal("delivered exact key not durable")
	}
	s.records["kid"] = &loaded
	event.Known = false
	f.setStatus("kid", stWorking)
	s.signal("kid")
	event.Known = true
	event.Fresh = false
	f.setStatus("kid", stReady)
	s.signal("kid")
	if s.pending("kid") || len(f.delivered()) != 1 {
		t.Fatal("rehydrated old completion became new attention")
	}
	event.Key = "input:b:completed"
	event.Fresh = true
	s.signal("kid")
	if !s.pending("kid") {
		t.Fatal("new exact input in same roster group was missed")
	}
	f.setSendErr(errors.New("unknown admission"))
	s.deliver("kid", "parent", *s.records["kid"].Pending)
	if s.records["kid"].NativeDeliveredKey == event.Key || !s.pending("kid") {
		t.Fatal("unadmitted key marked delivered")
	}
	pending := *s.records["kid"].Pending
	event.Known = false
	s.signal("kid")
	s.deliver("kid", "parent", pending)
	if !s.pending("kid") {
		t.Fatal("unknown reconnect discarded pending native event")
	}
	event.Known = true
	event.Fresh = false
	f.setSendErr(nil)
	s.signal("kid")
	s.deliver("kid", "parent", pending)
	if len(f.delivered()) != 2 || s.pending("kid") {
		t.Fatal("retained native pending event did not recover")
	}
	event.Key = "input:c:completed"
	event.Fresh = true
	s.signal("kid")
	pending = *s.records["kid"].Pending
	event.Key = "input:d:completed"
	s.deliver("kid", "parent", pending)
	if len(f.delivered()) != 2 {
		t.Fatal("delivery did not revalidate current exact child attention")
	}
}

func TestNativeSupervisionKeyUsesExactInputAndRequestContent(t *testing.T) {
	m := plainMeta("kid", stReady)
	m.RequiredRuntime = persist.RequiredRuntimeOpenCode
	m.ConversationID = "ses_" + strings.Repeat("a", 32)
	m.OpenCodeBinding = &persist.OpenCodeBinding{Generation: strings.Repeat("b", 32), Phase: persist.OpenCodePhaseReady, NativeCreatedAtMS: 123}
	v := OpenCodeView{Root: m.ConversationID, Generation: m.OpenCodeBinding.Generation, BusyKnown: true, CurrentScopeKnown: true, Inputs: []OpenCodeInput{{MessageID: "msg_one", Classified: true, Outcome: OpenCodeOutcomeCompleted, OutcomeSource: OpenCodeOutcomeSourceLive}}}
	m.OpenCodeBinding.Initial = &persist.OpenCodeInitial{Phase: persist.OpenCodeInitialStarted}
	if openCodeSupervisionEvent(m, v).Known {
		t.Fatal("pre-admission native initial outcome became notification authority")
	}
	m.OpenCodeBinding.Initial = nil
	a := openCodeSupervisionEvent(m, v)
	v.Epoch++
	v.Inputs[0].OutcomeSource = OpenCodeOutcomeSourceImported
	b := openCodeSupervisionEvent(m, v)
	if !a.Known || !a.Fresh || !b.Known || b.Fresh || a.Key == "" || a.Key != b.Key {
		t.Fatal("native completion identity depended on hydration/epoch")
	}
	v.Inputs[0].MessageID = "msg_two"
	if openCodeSupervisionEvent(m, v).Key == a.Key {
		t.Fatal("distinct input outcome collapsed")
	}
	v.Pending = []OpenCodeRequest{{Kind: "permission", ID: "per_one", SessionID: m.ConversationID, Raw: json.RawMessage(`{"id":"per_one","sessionID":"` + m.ConversationID + `","permission":"bash","patterns":["echo one"]}`)}}
	first := openCodeSupervisionEvent(m, v)
	v.Epoch++
	v.Pending[0].Raw = json.RawMessage(`{"patterns":["echo one"],"permission":"bash","sessionID":"` + m.ConversationID + `","id":"per_one"}`)
	if got := openCodeSupervisionEvent(m, v); !got.Known || !got.Fresh || got.Key != first.Key {
		t.Fatal("request ordering or epoch changed native identity")
	}
	v.Pending[0].Raw = json.RawMessage(strings.Replace(string(v.Pending[0].Raw), "echo one", "echo two", 1))
	if openCodeSupervisionEvent(m, v).Key == first.Key {
		t.Fatal("request content change was ignored")
	}
	v.Degraded = true
	if openCodeSupervisionEvent(m, v).Known {
		t.Fatal("degraded native state authorized attention")
	}
}

func TestNativeSupervisionClearedDraftUsesActualNativeFence(t *testing.T) {
	f := newOpenCodeSendFixture(t)
	if err := f.d.markDirectInputUnresolved(f.binding.WorkerID, protocol.DirectInputDraft); err != nil {
		t.Fatal(err)
	}
	if !f.d.sup.sourceSafe(f.binding.WorkerID) {
		t.Fatal("sticky generic draft marker defeats actual native empty composer")
	}
	if err := f.d.sendSupervision(f.binding.WorkerID, protocol.SendInputReq{Text: "native empty draft notice", Submit: true}, "semantic:supervision:cleared-draft"); err != nil {
		t.Fatal(err)
	}
	if f.calls.Load() != 1 {
		t.Fatal("native input fence did not admit once")
	}
}

func TestNativeSupervisionRecoversOnlyObservedOfflineCompletion(t *testing.T) {
	parent := plainMeta("parent", stReady)
	child := plainMeta("kid", stWorking)
	child.RequiredRuntime = persist.RequiredRuntimeOpenCode
	child.SpawnedFrom = "parent"
	child.SpawnIntent = protocol.SpawnIntentHandoff
	child.Supervision = protocol.SupervisionPassive
	f := newSupFakes(parent, child)
	event := nativeSupervisionEvent{Known: true, ObservedInputID: "msg_seen_active"}
	s := &supervisor{endpointID: supEndpoint, dir: t.TempDir(), get: f.get, getSource: f.get, controlled: f.isControlled, send: f.send, records: map[string]*supervisionRecord{}, nativeEvent: func(persist.Meta) nativeSupervisionEvent { return event }}
	s.arm(child)
	s.signal("kid")
	raw, err := os.ReadFile(filepath.Join(s.dir, "kid.json"))
	if err != nil {
		t.Fatal(err)
	}
	var loaded supervisionRecord
	if json.Unmarshal(raw, &loaded) != nil || loaded.NativeObservedInput != "msg_seen_active" {
		t.Fatal("active input was not checkpointed before crash")
	}
	s.records["kid"] = &loaded
	event = nativeSupervisionEvent{}
	s.signal("kid")
	event = nativeSupervisionEvent{Known: true, Key: "input:seen:completed", InputID: "msg_seen_active"}
	f.setStatus("kid", stReady)
	s.signal("kid")
	if !s.pending("kid") {
		t.Fatal("exact observed input completed offline but was lost on import")
	}
	s.deliver("kid", "parent", *s.records["kid"].Pending)
	event.Known = false
	s.signal("kid")
	event.Known = true
	s.signal("kid")
	if s.pending("kid") || len(f.delivered()) != 1 {
		t.Fatal("offline outcome replayed after subsequent restart")
	}
	event = nativeSupervisionEvent{Known: true, Key: "input:unrelated:completed", InputID: "msg_unrelated_history"}
	s.signal("kid")
	if s.pending("kid") {
		t.Fatal("unobserved imported historical outcome became a new notice")
	}
	event = nativeSupervisionEvent{Known: true, Owned: true, Key: "input:owned:completed", InputID: "msg_owned_initial"}
	s.signal("kid")
	if !s.pending("kid") {
		t.Fatal("durably admitted initial input lost offline completion")
	}
}

func TestNativeSupervisionObservesCurrentWorkWithoutInventingHistoryOwnership(t *testing.T) {
	m := plainMeta("kid", stWorking)
	m.RequiredRuntime = persist.RequiredRuntimeOpenCode
	m.ConversationID = "ses_" + strings.Repeat("a", 32)
	m.OpenCodeBinding = &persist.OpenCodeBinding{Generation: strings.Repeat("b", 32), Phase: persist.OpenCodePhaseReady, NativeCreatedAtMS: 123}
	input := OpenCodeInput{MessageID: "msg_work", Classified: true}
	view := OpenCodeView{Root: m.ConversationID, Generation: m.OpenCodeBinding.Generation, BusyKnown: true, Busy: true, CurrentScopeKnown: true, Current: &input}
	if got := openCodeSupervisionEvent(m, view); !got.Known || got.ObservedInputID != input.MessageID {
		t.Fatal("qualified active input was not observed")
	}
	view.Busy = false
	if openCodeSupervisionEvent(m, view).ObservedInputID != "" {
		t.Fatal("idle unresolved history invented active ownership")
	}
	view.Current = nil
	input.Outcome = OpenCodeOutcomeCompleted
	input.OutcomeSource = OpenCodeOutcomeSourceImported
	view.Inputs = []OpenCodeInput{input}
	if openCodeSupervisionEvent(m, view).Owned {
		t.Fatal("arbitrary imported input was treated as admitted initial")
	}
	m.OpenCodeBinding.Initial = &persist.OpenCodeInitial{Phase: persist.OpenCodeInitialAdmitted, MessageID: input.MessageID}
	if got := openCodeSupervisionEvent(m, view); !got.Owned || got.InputID != input.MessageID {
		t.Fatal("exact admitted initial proof was not carried")
	}
}

func TestNativeSupervisionRecoversDurablyOwnedInputBeforeObserverCheckpoint(t *testing.T) {
	f := newOpenCodeSendFixture(t)
	id, err := protocol.NewSemanticOperationID()
	if err != nil {
		t.Fatal(err)
	}
	if err = f.client.SemanticSend(id, f.request); err != nil {
		t.Fatal(err)
	}
	rec, ok := f.d.core.ComposerOperation(id)
	if !ok {
		t.Fatal("missing durable native intent")
	}
	var intent openCodeSendIntent
	if json.Unmarshal(rec.Request, &intent) != nil {
		t.Fatal("invalid durable intent")
	}
	input := OpenCodeInput{MessageID: intent.MessageID, Text: f.request.Text, Classified: true, At: intent.NotBeforeMS + 1, Outcome: OpenCodeOutcomeCompleted, OutcomeSource: OpenCodeOutcomeSourceImported, OutcomeAt: intent.NotBeforeMS + 2, Agent: "build", ProviderID: "fixture", ModelID: "fixture-model", Variant: "max"}
	view := OpenCodeView{Root: f.identity.RootID, Generation: f.identity.BackendGeneration, BusyKnown: true, CurrentScopeKnown: true, Inputs: []OpenCodeInput{input}}
	f.d.openCode.mu.Lock()
	f.d.openCode.sessions[f.binding.WorkerID].view = view
	f.d.openCode.mu.Unlock()
	f.d.nativeReceipts.mu.Lock()
	f.d.nativeReceipts.sessions = nil
	f.d.nativeReceipts.mu.Unlock()
	meta, _ := f.d.core.Get(f.binding.WorkerID)
	if got := f.d.nativeSupervisionState(meta); !got.Owned || got.InputID != intent.MessageID {
		t.Fatal("admitted native input lost because active observation had not checkpointed")
	}
	f.d.openCode.mu.Lock()
	f.d.openCode.sessions[f.binding.WorkerID].view.Inputs[0].Text = "conflicting native history"
	f.d.openCode.mu.Unlock()
	if f.d.nativeSupervisionState(meta).Owned {
		t.Fatal("owned ID with conflicting native body became recovered attention")
	}
	if f.calls.Load() != 1 {
		t.Fatal("ownership recovery posted another native message")
	}
}
