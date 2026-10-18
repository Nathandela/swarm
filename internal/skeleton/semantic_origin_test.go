package skeleton

import (
	"testing"

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
