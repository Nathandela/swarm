package skeleton

// FAILING-FIRST (TDD RED) for W2.1 of the phone refit
// (docs/specifications/phone-refit-playbook.md §3, bead agents-tracker-d45a.2): daemon-authored
// control keys carry their provenance.
//
// THE DEFECT, END TO END. interruptTurn (chat.go) and applyDecision (inject.go) write the
// adapter's cancel sequence and dialog keys through tapSub.Input, which the shim receives as
// wire.TDataIn -- the frame for bytes somebody TYPED -- and counts against the next submit.
// So on the shipped phone, Stop then send is refused input_busy, and so is Approve then send,
// until someone presses Enter at the machine. The fix puts the provenance on the frame
// (shimwire.TypeControlInput) and routes both call sites through tapSub.ControlKeys; the
// shim's byte-level proof is internal/shim/controlinput_test.go, this is the daemon's.

import (
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
)

// TestPhoneStopThenSend_IsDelivered: the phone presses Stop on a live Claude session and then
// sends. Nobody typed at the machine; the only bytes since the last submit are the daemon's
// own control key, and the send must be delivered.
func TestPhoneStopThenSend_IsDelivered(t *testing.T) {
	r := newInjectRig(t, composerGrid, claudeApproval("req-stop-then-send"))
	// The capability record a Claude-shaped session carries, pinned the way
	// s0_writerserialise_test.go pins it: without it the daemon derives one lazily, and the
	// send could refuse for a reason that has nothing to do with this file's subject.
	r.sk.registerSessionCapabilities(r.local, protocol.SessionCapabilities{
		Provider: "claude", ProviderVersion: "1.0.0", AdapterRevision: "rev",
		StructuredChat: true, TerminalFallback: false,
	})
	turn := r6FixOpenTurn(t, r.sk, r.local, "start the work")

	code, err := r.sk.api.InterruptTurn(r.sk.api.endpointID, "devA:01JW2STOPTHENSEND00000000",
		protocol.TurnInterruptReq{Session: r.session, ExpectedTurn: turn})
	if err != nil || code != "" {
		t.Fatalf("CONTROL BROKEN: the phone's Stop was refused: code %q err %v", code, err)
	}

	const text = "and now carry on"
	code, err = r.sk.api.ComposerSend(r.sk.api.endpointID, "devA:01JW2SENDAFTERSTOP0000000",
		protocol.ComposerSendReq{Session: r.session, ExpectedTurn: turn, Text: text})
	if code != "" || err != nil {
		t.Fatalf("phone send after a phone Stop = code %q err %v, want delivered.\n"+
			"The daemon's own ESC travelled as typed input, so the shim counts the line as "+
			"dirty and refuses every send until someone presses Enter at the machine "+
			"(phone-refit-playbook W2.1).", code, err)
	}

	ok, drained := awaitFrames(r.att, "got:", 20*time.Second)
	if !ok {
		t.Fatalf("the fake CLI never reported its stdin after the send; drained %q", drained)
	}
	line := drained[strings.Index(drained, "got:"):]
	if j := strings.IndexAny(line, "\r\n"); j >= 0 {
		line = line[:j]
	}
	if !strings.Contains(line, text) {
		t.Fatalf("the session's stdin submitted %q, want it to carry the phone's %q", line, text)
	}
}

// TestPhoneApproveThenSend_IsDelivered is the other daemon-authored key: the phone answers a
// dialog (the recorded allow key) and then sends. The answer is not somebody typing either.
func TestPhoneApproveThenSend_IsDelivered(t *testing.T) {
	r := newInjectRig(t, bashDialogGrid, claudeApproval("req-approve-then-send"))
	r.sk.registerSessionCapabilities(r.local, protocol.SessionCapabilities{
		Provider: "claude", ProviderVersion: "1.0.0", AdapterRevision: "rev",
		StructuredChat: true, TerminalFallback: false,
	})
	code, err := r.sk.approveInteraction(r.sk.api.endpointID, "op-approve-then-send",
		approveFor(t, r.sk, r.local, r.item, "allow"))
	if err != nil || code != "" {
		t.Fatalf("CONTROL BROKEN: the phone's allow was refused: code %q err %v", code, err)
	}

	const text = "and then this"
	code, err = r.sk.api.ComposerSend(r.sk.api.endpointID, "devA:01JW2SENDAFTERALLOW000000",
		protocol.ComposerSendReq{Session: r.session, Text: text})
	if code != "" || err != nil {
		t.Fatalf("phone send after a phone approval = code %q err %v, want delivered "+
			"(phone-refit-playbook W2.1)", code, err)
	}
	ok, drained := awaitFrames(r.att, "got:", 20*time.Second)
	if !ok {
		t.Fatalf("the fake CLI never reported its stdin after the send; drained %q", drained)
	}
	line := drained[strings.Index(drained, "got:"):]
	if j := strings.IndexAny(line, "\r\n"); j >= 0 {
		line = line[:j]
	}
	if !strings.Contains(line, text) {
		t.Fatalf("the session's stdin submitted %q, want it to carry the phone's %q", line, text)
	}
}
