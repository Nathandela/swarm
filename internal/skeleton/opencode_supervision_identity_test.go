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
