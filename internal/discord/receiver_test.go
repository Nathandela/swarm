package discord

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Nathandela/swarm/internal/protocol"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewOwnerWorkerReadsExactFirstTaskAndRepliesToSource(t *testing.T) {
	b, w := controlsRig(t)
	i := ownerInteraction("first-task", "1")
	route := Route{GuildID: "10", ChannelID: "20", MessageID: i.ID}
	task := "Owner exact task 'quoted' 🙂\nnext line"
	if _, e := b.controlCommand(context.Background(), i, Actor{ID: "1"}, route, controlOption("new", map[string]string{"cli": "codex", "prompt": task})); e != nil {
		t.Fatal(e)
	}
	prompt := w.launches[0].InitialPrompt
	if !strings.Contains(prompt, "swarm discord context") || !strings.Contains(prompt, "swarm discord reply") {
		t.Fatal("new worker lacks explicit Discord reply context", prompt)
	}
	record, e := b.Service.Store.ExistingEvent("control:"+i.ID, b.Service.Now())
	if e != nil {
		t.Fatal(e)
	}
	page, e := b.Service.Context(record.Ref, 0)
	if e != nil || len(page.Messages) != 1 {
		t.Fatal(page, e)
	}
	var source protocol.LaunchReq
	if e = json.Unmarshal([]byte(page.Messages[0].Text), &source); e != nil || source.InitialPrompt != task {
		t.Fatal("first task changed", source, e)
	}
	visible, e := b.Service.Store.Request(record.Ref, b.Service.Now())
	if e != nil || visible.Target.ID != "local/new" {
		t.Fatal("trusted launch receiver not bound", visible.Target, e)
	}
	if _, e = b.Service.Reply(context.Background(), record.Ref, "", "finished"); e != nil {
		t.Fatal(e)
	}
	messenger := b.Service.Messenger.(*fakeMessenger)
	if messenger.posts[len(messenger.posts)-1].Route != route {
		t.Fatal("reply left source audience")
	}
	if e = b.Service.Store.SetOptOut(visible.Target, true); e != nil {
		t.Fatal(e)
	}
	if _, e = b.Service.Context(record.Ref, 0); !errors.Is(e, ErrOptedOut) {
		t.Fatal(e)
	}
	if _, e = b.Service.Reply(context.Background(), record.Ref, "progress", "more"); !errors.Is(e, ErrOptedOut) {
		t.Fatal("new-agent exclusion did not block publication", e)
	}
}

func TestReceiverBindingPreservesAdmissionHashAcrossRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "discord")
	s, e := OpenStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	input := sampleRequest()
	input.Target = Target{ID: "control", Instance: "installation"}
	input.Receiver = &Target{ID: "forged", Instance: "forged"}
	r := mustAdmit(t, s, input)
	if r.Receiver != nil {
		t.Fatal("input supplied untrusted receiver")
	}
	if _, _, e = s.BeginDelivery(r.Ref, testNow); e != nil {
		t.Fatal(e)
	}
	receiver := Target{ID: "local/new", Instance: "launch:" + r.Ref, Name: "Worker", Agent: "codex"}
	if e = s.BindReceiver(r.Ref, receiver); e != nil {
		t.Fatal(e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = OpenStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = s.Close() }()
	if replay, fresh, e := s.Admit(input, testNow); e != nil || fresh || replay.Target.ID != "control" || replay.Receiver == nil || *replay.Receiver != receiver {
		t.Fatal("receiver changed immutable admission", replay, fresh, e)
	}
	other := receiver
	other.ID = "other"
	if e = s.BindReceiver(r.Ref, other); !errors.Is(e, ErrConflict) {
		t.Fatal("receiver rebound", e)
	}
}
func TestLostOwnerLaunchAckCannotPublishToUnknownReceiver(t *testing.T) {
	b, w := controlsRig(t)
	w.launchErr = errors.New("lost ack")
	i := ownerInteraction("lost-task", "1")
	route := Route{ChannelID: "20", MessageID: i.ID}
	_, _ = b.controlCommand(context.Background(), i, Actor{ID: "1"}, route, controlOption("new", map[string]string{"cli": "codex", "prompt": "task"}))
	record, e := b.Service.Store.ExistingEvent("control:"+i.ID, b.Service.Now())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = b.Service.Reply(context.Background(), record.Ref, "", "unknown"); e == nil {
		t.Fatal("unconfirmed launch published as an unknown worker")
	}
}
