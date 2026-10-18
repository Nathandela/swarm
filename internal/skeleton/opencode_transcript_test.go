package skeleton

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
)

func TestOpenCodeTranscriptStableIDsAcrossBackendGenerations(t *testing.T) {
	meta := persist.Meta{ConversationID: "ses_root", Cwd: t.TempDir(), OpenCodeBinding: &persist.OpenCodeBinding{NativeCreatedAtMS: 10, Generation: "old", ProfileEnv: []string{"HOME=" + t.TempDir()}}}
	first, err := nativeProjectionScope(meta)
	if err != nil {
		t.Fatal(err)
	}
	meta.OpenCodeBinding.Generation = "new"
	next, err := nativeProjectionScope(meta)
	if err != nil {
		t.Fatal(err)
	}
	if first != next || nativeProjectionID(first, "message_snapshot", "msg_user") != nativeProjectionID(next, "message_snapshot", "msg_user") {
		t.Fatal("reconnect changed native projection identity")
	}
	meta.OpenCodeBinding.ProfileEnv = []string{"HOME=" + t.TempDir()}
	other, err := nativeProjectionScope(meta)
	if err != nil {
		t.Fatal(err)
	}
	if nativeProjectionID(first, "message_snapshot", "msg_user") == nativeProjectionID(other, "message_snapshot", "msg_user") {
		t.Fatal("different native profiles share object identity")
	}
}

func TestOpenCodeTranscriptUsesSharedBoundedJournalAndImportedProvenance(t *testing.T) {
	fixture := newOpenCodeSendFixture(t)
	meta, _ := fixture.d.core.Get(fixture.binding.WorkerID)
	source, err := newOpenCodeTranscript(fixture.d, meta, fixture.identity)
	if err != nil {
		t.Fatal(err)
	}
	row := json.RawMessage(`{"info":{"id":"msg_historical","sessionID":"` + meta.ConversationID + `","role":"assistant","parentID":"msg_owner","time":{"created":1000,"completed":1100},"finish":"stop"},"parts":[{"id":"prt_historical","messageID":"msg_historical","sessionID":"` + meta.ConversationID + `","type":"text","text":"earlier answer"}]}`)
	if err := source.history([]json.RawMessage{row}, "imported"); err != nil {
		t.Fatal(err)
	}
	seen := len(source.seen)
	if err := source.history([]json.RawMessage{row}, "imported"); err != nil {
		t.Fatal(err)
	}
	if len(source.seen) != seen {
		t.Fatal("duplicate native row produced another logical item")
	}
	fixture.d.itemMu.Lock()
	legacyRefs := len(fixture.d.itemIDs)
	fixture.d.itemMu.Unlock()
	if legacyRefs != 0 {
		t.Fatal("native objects grew the session-lifetime legacy ref map")
	}
	time.Sleep(200 * time.Millisecond)
	result, err := fixture.d.core.JournalReadFrom(0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, record := range result.Events {
		if strings.Contains(string(record.Payload), "earlier answer") {
			var item map[string]any
			if err := json.Unmarshal(record.Payload, &item); err != nil {
				t.Fatal(err)
			}
			if item["kind"] != "message_snapshot" || item["v"] != float64(2) || item["provenance"] != "imported" {
				t.Fatalf("native history was published as live/v1: %s", record.Payload)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("native source did not reach existing journal")
	}
}

func TestOpenCodeTranscriptRefusesForeignRows(t *testing.T) {
	fixture := newOpenCodeSendFixture(t)
	meta, _ := fixture.d.core.Get(fixture.binding.WorkerID)
	source, err := newOpenCodeTranscript(fixture.d, meta, fixture.identity)
	if err != nil {
		t.Fatal(err)
	}
	row := json.RawMessage(`{"info":{"id":"msg_foreign","sessionID":"ses_other","role":"user","time":{"created":1000}},"parts":[]}`)
	if err := source.history([]json.RawMessage{row}, "imported"); err == nil {
		t.Fatal("foreign native history entered worker transcript")
	}
	if len(source.seen) != 0 {
		t.Fatal("foreign row changed projection")
	}
}

func TestOpenCodeTranscriptCapabilityNeedsCurrentProjectorProof(t *testing.T) {
	f := newOpenCodeSendFixture(t)
	caps, ok := f.d.sessionCapabilities(f.binding.WorkerID)
	if !ok || caps.NativeInteractionVersion != 2 || caps.StructuredChat {
		t.Fatalf("connected native read capability absent or grants writes: %+v", caps)
	}
	f.d.stopOpenCodeObservation(f.binding.WorkerID)
	caps, ok = f.d.sessionCapabilities(f.binding.WorkerID)
	if !ok || caps.NativeInteractionVersion != 0 {
		t.Fatal("stopped projector still advertises native reads")
	}
}
