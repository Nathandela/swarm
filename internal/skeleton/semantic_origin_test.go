package skeleton

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/protocol"
)

func TestOwnerSemanticSendKeepsOwnerAttribution(t *testing.T) {
	r := newR7ComposerRig(t, true)
	sender, ok := any(r.sk.api).(interface {
		SemanticSendTransactional(string, string, protocol.ComposerSendReq, func() error) (protocol.ErrorCode, error)
	})
	if !ok {
		t.Fatal("owner semantic dispatch needs its own attribution boundary")
	}
	const operationID = "semantic-owner-origin-test"
	code, err := sender.SemanticSendTransactional(r.sk.api.endpointID, operationID,
		protocol.ComposerSendReq{Session: r.session, Text: "owner automation"}, func() error { return nil })
	if err != nil || code != "" {
		t.Fatalf("owner semantic send: code=%q error=%v", code, err)
	}
	if r.sk.phoneRecentlyActive(r.local) {
		t.Fatal("owner automation was labeled as recent phone activity")
	}
	params := r7CallParams(t, r.backend, "turn/start")
	ref, ok := params["clientUserMessageId"].(string)
	if !ok || ref == "" {
		t.Fatal("owner semantic send lost exact native echo correlation")
	}
	fields := map[string]any{}
	r.sk.itemMu.Lock()
	r.sk.stampComposerEchoLocked(r.local, "owner automation", ref, fields)
	r.sk.itemMu.Unlock()
	if fields["source"] != adapter.SourceOwner || fields["operation_id"] != operationID {
		t.Fatalf("owner native echo attribution: %+v", fields)
	}
}

func TestSemanticOriginSurvivesExactEchoAndReceipt(t *testing.T) {
	r := newR7ComposerRig(t, true)
	origin := protocol.MessageOrigin{Kind: "external", InstallationID: "install", RequestID: "request", ActorID: "colleague"}
	req := protocol.ComposerSendReq{Session: r.session, Text: "context", Origin: &origin}
	const id = "semantic-origin-exact"
	if _, _, err := r.sk.core.ClaimComposerOperation(id, protocol.OpSemanticSend, r.local, "", "hash"); err != nil {
		t.Fatal(err)
	}
	code, err := r.sk.api.SemanticSendTransactional(r.sk.api.endpointID, id, req, func() error { return nil })
	if err != nil || code != "" {
		t.Fatalf("send: %s %v", code, err)
	}
	// Mutation by an in-process caller must not rewrite retained provenance.
	origin.ActorID = "mutated"
	params := r7CallParams(t, r.backend, "turn/start")
	fields := map[string]any{}
	r.sk.itemMu.Lock()
	r.sk.stampComposerEchoLocked(r.local, "context", params["clientUserMessageId"].(string), fields)
	r.sk.itemMu.Unlock()
	raw, _ := json.Marshal(fields["origin"])
	var got protocol.MessageOrigin
	_ = json.Unmarshal(raw, &got)
	if got.ActorID != "colleague" || fields["source"] != adapter.SourceOwner {
		t.Fatalf("exact attribution: %+v", fields)
	}
	receipt, err := r.sk.semanticOperation(r.local, id)
	if err != nil || receipt.Origin == nil || receipt.Origin.ActorID != "colleague" {
		t.Fatalf("receipt: %+v %v", receipt, err)
	}
}

func TestSemanticOriginNeverClaimsTextOnlyEcho(t *testing.T) {
	origin := &protocol.MessageOrigin{Kind: "owner_endorsed", InstallationID: "install", RequestID: "request", ActorID: "colleague", EndorserID: "owner"}
	d := &Daemon{pendingSends: map[string][]pendingSend{"session": {{text: "yes", operationID: "op", source: adapter.SourceOwner, origin: origin, at: time.Now()}}}}
	fields := map[string]any{}
	d.stampComposerEchoLocked("session", "yes", "", fields)
	if _, ok := fields["origin"]; ok {
		t.Fatalf("text coincidence fabricated authority: %+v", fields)
	}
	if fields["source"] != adapter.SourceOwner {
		t.Fatal("legacy transport attribution changed")
	}
}

func TestOpenCodeOriginRetainedThroughNativeReceipt(t *testing.T) {
	f := newOpenCodeSendFixture(t)
	f.request.Origin = &protocol.MessageOrigin{Kind: "external", InstallationID: "install", RequestID: "request", ActorID: "colleague"}
	client, err := protocol.Dial(f.d.SocketPath(), []string{protocol.CapSemanticSend, protocol.CapMessageOrigin})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	id, _ := protocol.NewSemanticOperationID()
	if err := client.SemanticSend(id, f.request); err != nil {
		t.Fatal(err)
	}
	rec, _ := f.d.core.ComposerOperation(id)
	var intent openCodeSendIntent
	if json.Unmarshal(rec.Request, &intent) != nil || intent.Origin == nil || intent.Origin.ActorID != "colleague" {
		t.Fatalf("retained intent: %s", rec.Request)
	}
	receipt, err := client.SemanticOperation(f.request.Session, id)
	if err != nil || receipt.Origin == nil || *receipt.Origin != *f.request.Origin {
		t.Fatalf("receipt: %+v %v", receipt, err)
	}
}

func TestOpenCodeImportedOriginRequiresRetainedExactIntent(t *testing.T) {
	f := newOpenCodeSendFixture(t)
	f.request.Origin = &protocol.MessageOrigin{Kind: "owner_endorsed", InstallationID: "install", RequestID: "request", ActorID: "colleague", EndorserID: "owner"}
	client, err := protocol.Dial(f.d.SocketPath(), []string{protocol.CapSemanticSend, protocol.CapMessageOrigin})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	id, _ := protocol.NewSemanticOperationID()
	if err := client.SemanticSend(id, f.request); err != nil {
		t.Fatal(err)
	}
	meta, _ := f.d.core.Get(f.binding.WorkerID)
	source, err := newOpenCodeTranscript(f.d, meta, f.identity)
	if err != nil {
		t.Fatal(err)
	}
	row := f.nativeRow.Load().(json.RawMessage)
	if err := source.history([]json.RawMessage{row}, "imported"); err != nil {
		t.Fatal(err)
	}
	found := false
	for until := time.Now().Add(2 * time.Second); time.Now().Before(until) && !found; {
		records, err := f.d.core.JournalReadFrom(0)
		if err != nil {
			t.Fatal(err)
		}
		for _, record := range records.Events {
			var item struct {
				OperationID string                  `json:"operation_id"`
				Origin      *protocol.MessageOrigin `json:"origin"`
				Provenance  string                  `json:"provenance"`
			}
			if json.Unmarshal(record.Payload, &item) == nil && item.OperationID == id && item.Provenance == "reconciled" {
				if item.Origin == nil || *item.Origin != *f.request.Origin {
					t.Fatalf("imported exact origin lost: %s", record.Payload)
				}
				found = true
			}
		}
		if !found {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if !found {
		t.Fatal("qualified imported row never carried retained origin")
	}
	// An imported native row without retained custody cannot inherit an author.
	unknown := json.RawMessage(strings.ReplaceAll(string(row), openCodeMessageID(id, f.identity), "msg_unknown"))
	if err := source.history([]json.RawMessage{unknown}, "imported"); err != nil {
		t.Fatal(err)
	}
	unknownID := nativeProjectionID(source.scope, "message_snapshot", f.identity.RootID+":msg_unknown")
	if source.seen[unknownID].provenance != "imported" {
		t.Fatal("unknown imported native input claimed retained origin")
	}
}

func TestOpenCodeOriginSurvivesDaemonRestart(t *testing.T) {
	f := newOpenCodeSendFixture(t)
	f.request.Origin = &protocol.MessageOrigin{Kind: "external", InstallationID: "install", RequestID: "request", ActorID: "colleague"}
	client, err := protocol.Dial(f.d.SocketPath(), []string{protocol.CapSemanticSend, protocol.CapMessageOrigin})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := protocol.NewSemanticOperationID()
	if err := client.SemanticSend(id, f.request); err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	old := f.d
	_ = f.client.Close()
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	replacement, err := Serve(Config{StateDir: old.stateDir, SocketPath: old.socketPath, LockPath: filepath.Join(old.stateDir, "d.lock"), LogPath: filepath.Join(old.stateDir, "d.log"), ShimBinary: swarmBin, FakeAgentBin: fakeAgentBin, MaxSessions: 16, PollInterval: 50 * time.Millisecond, StalenessThreshold: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	f.d = replacement
	t.Cleanup(func() { _ = replacement.Close() })
	receipt, err := replacement.semanticOperation(f.binding.WorkerID, id)
	if err != nil || receipt.Origin == nil || *receipt.Origin != *f.request.Origin {
		t.Fatalf("restarted origin: %+v %v", receipt, err)
	}
	if f.calls.Load() != 1 {
		t.Fatal("restart replayed original input")
	}
}
