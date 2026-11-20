package skeleton

import (
	"errors"
	"testing"

	"github.com/Nathandela/swarm/internal/managerproof"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func TestManagerQuotaRequiresTerminalFailureOfExactRootTurn(t *testing.T) {
	d, b, feed := newCodexManagerSendRigForConversation(t, "discord-teammate-quota")
	if err := d.core.SetConversationID(b.Session, b.NativeSessionID); err != nil {
		t.Fatal(err)
	}
	const method = "turn/completed"
	const turn = "quota-turn"
	frame := func(thread, root string) []byte {
		return codexManagerFrame(method, thread, map[string]any{"turn": map[string]any{"id": turn, "rootTurnId": root, "status": "failed", "error": map[string]any{"codexErrorInfo": "usageLimitExceeded"}}})
	}
	for _, raw := range [][]byte{frame("unrelated", turn), frame(b.NativeSessionID, "other-root")} {
		d.observeScheduledCodexFailure(b.Session, method, raw)
	}
	meta, _ := d.core.Get(b.Session)
	if meta.ManagerQuotaTurn != "" {
		t.Fatalf("false provider exhaustion %q", meta.ManagerQuotaTurn)
	}
	raw := frame(b.NativeSessionID, turn)
	d.observeScheduledCodexFailure(b.Session, method, raw)
	// An authenticated terminal failure on its own must not retire an unrelated current turn.
	view, err := d.managerRuntime(b.Session, b.SessionInstance)
	if err != nil || view.ProviderExhausted {
		t.Fatalf("quota before matching native turn: %v %+v", err, view)
	}
	d.captureCodexManagerFrame(b.Session, feed, "turn/started", codexManagerTurnFrame("turn/started", b.NativeSessionID, turn, "inProgress"))
	d.captureCodexManagerFrame(b.Session, feed, method, raw)
	waitCodexCustody(t, feed)
	view, err = d.managerRuntime(b.Session, b.SessionInstance)
	if err != nil || !view.ProviderExhausted {
		t.Fatalf("missing joined quota evidence: %v %+v", err, view)
	}
}

func TestQuarantinedManagerSeparatesSubmittedLineAndUnsentDraft(t *testing.T) {
	d, b, _ := newCodexManagerSendRigForConversation(t, "discord-teammate-submitted")
	d.restoreDirectInputState([]string{b.Session})
	d.backend.managerFailures.Store(b.Session, true)
	for _, tc := range []struct {
		class   protocol.DirectInputClass
		want    error
		blocked bool
	}{
		{protocol.DirectInputSubmitted, managerproof.ErrQuarantined, false},
		{protocol.DirectInputDraft, managerproof.ErrDirectInputUnresolved, true},
	} {
		if err := d.markDirectInputUnresolved(b.Session, tc.class); err != nil {
			t.Fatal(err)
		}
		d.restoreDirectInputState([]string{b.Session})
		if _, err := d.managerRuntime(b.Session, b.SessionInstance); !errors.Is(err, tc.want) {
			t.Fatalf("%s: %v", tc.class, err)
		}
		if d.blockedRetirementInput(b.Session, schema.ManagerReplaceCustody) != tc.blocked || !d.blockedRetirementInput(b.Session, "") {
			t.Fatalf("wrong retirement fence for %s", tc.class)
		}
	}
}
