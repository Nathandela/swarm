package protocol

import (
	"reflect"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

type ownerCapabilitiesStub struct {
	*durableComposerStub
	record  SessionCapabilities
	present bool
}

func (s *ownerCapabilitiesStub) SessionCapabilities(local string) (SessionCapabilities, bool) {
	return s.record, s.present && local == "sess1"
}

func TestOwnerRosterSuppliesIdentityForSemanticSend(t *testing.T) {
	backend := &ownerCapabilitiesStub{
		durableComposerStub: newDurableComposerStub(),
		present:             true,
		record:              SessionCapabilities{Provider: "codex", SessionInstance: "current-instance", StructuredChat: true},
	}
	meta := persist.Meta{ID: "sess1", AgentType: "codex", Status: status.Status{Process: status.ProcessRunning}}
	backend.setMetas(meta)
	client := dialClient(t, serveOwnerAPI(t, backend), []string{CapSemanticSend, CapSubscribe})
	rows, err := client.List()
	if err != nil || len(rows) != 1 {
		t.Fatalf("owner roster: rows=%d err=%v", len(rows), err)
	}
	if !reflect.DeepEqual(rows[0].Capabilities, &backend.record) {
		t.Fatalf("owner roster capabilities=%+v want=%+v", rows[0].Capabilities, backend.record)
	}
	operation, err := NewSemanticOperationID()
	if err != nil {
		t.Fatal(err)
	}
	if err := client.SemanticSend(operation, ComposerSendReq{Session: rows[0].ID, SessionInstance: rows[0].Capabilities.SessionInstance, Text: "exact owner message"}); err != nil {
		t.Fatalf("send using owner roster identity: %v", err)
	}
	receipt, sends, _ := backend.snapshot(operation)
	if sends != 1 || receipt.instance != backend.record.SessionInstance {
		t.Fatalf("semantic send lost roster identity: sends=%d receipt=%+v", sends, receipt)
	}
	events, err := client.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	backend.pushStatus(meta)
	event, ok := recvEvent(t, events, time.Second)
	if !ok || !reflect.DeepEqual(event.Session.Capabilities, &backend.record) {
		t.Fatalf("owner status event lost capabilities: event=%+v received=%v", event, ok)
	}
}

func TestOwnerRosterDoesNotInventOrLeakCapabilityRecords(t *testing.T) {
	meta := persist.Meta{ID: "sess1", AgentType: "codex"}
	backend := &ownerCapabilitiesStub{durableComposerStub: newDurableComposerStub(), present: true,
		record: SessionCapabilities{Provider: "codex", SessionInstance: "current-instance", StructuredChat: true}}
	for _, test := range []struct {
		name    string
		backend DaemonAPI
		remote  bool
	}{
		{"missing lookup", newStubDaemon(), false},
		{"remote tier", backend, true},
		{"missing record", &ownerCapabilitiesStub{durableComposerStub: newDurableComposerStub()}, false},
		{"invalid record", &ownerCapabilitiesStub{durableComposerStub: newDurableComposerStub(), present: true}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := &Server{d: test.backend, remoteTier: test.remote}
			row := server.stampView("endpoint", meta, status.GroupWorking, false, false, time.Time{})
			if row.Capabilities != nil {
				t.Fatalf("unexpected capabilities: %+v", row.Capabilities)
			}
		})
	}
}
