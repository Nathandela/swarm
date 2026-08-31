package conformance_test

import (
	"errors"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remotegw"
)

// TestRefreshRoster_CommandOutcomeAfterDiscardRecoveryIsAcceptedAndAcked pins the
// physical reconnect sequence at the phone boundary. An explicit stale-mailbox discard is
// followed by the machine's reconcile+roster repair; the next composer outcome must remain
// above the adopted reply ceiling, resolve the exact operation, and advance the relay ack.
//
// The depth-two mailbox makes the final assertion observable without inspecting private
// relay state. Once the outcome and one probe occupy both slots, a second probe can enter
// only after the phone has acked a cursor at or beyond the outcome.
func TestRefreshRoster_CommandOutcomeAfterDiscardRecoveryIsAcceptedAndAcked(t *testing.T) {
	h := newHarnessWithRelayConfig(t, func(cfg *relay.Config) {
		cfg.Quotas.MailboxMaxItems = 2
	})
	// Advertise the capability schema the structured roster below requires. This mirrors
	// transitionHarness while retaining the depth-two relay configuration used for the ack
	// proof in this test.
	h.sink = remotegw.NewRelaySink(remotegw.RelayConfig{
		Appender:       h.machineRelay,
		Target:         h.phoneTarget,
		Machine:        h.Machine,
		EpochID:        h.EpochID,
		Key:            h.Keys.ContentKey,
		RecipientKeyID: [8]byte{},
		SenderKeyID:    h.senderKeyID,
		Authorities:    fixedAuthorities{},
		Now:            h.sealNow,
		Profile: protocol.RemoteProfileV1{
			Version:                  1,
			InteractionSchemaVersion: 1,
			TerminalViewVersion:      1,
			CapabilityRecordVersion:  1,
		},
	})

	h.PushRoster(schema.JournalRecord{
		SessionID: testSession, Type: "roster", Group: "working", Capabilities: liveCaps(true),
	})
	eventually(t, "initial session never reached the phone", func() bool {
		s, err := h.App.Session(testSession)
		return err == nil && s.StructuredChat
	})
	// The phone batches relay acks off the delivery path. Start the quota-bound stale page
	// only after the initial reconcile+roster pair has been compacted.
	time.Sleep(1500 * time.Millisecond)

	h.SealOffset(-phonecore.InboundMaxAge - time.Minute)
	h.PushEvent(schema.JournalRecord{Cursor: 1, SessionID: testSession, Type: "group_transition", Group: "working"})
	h.PushEvent(schema.JournalRecord{Cursor: 2, SessionID: testSession, Type: "group_transition", Group: "working"})
	eventually(t, "stale mailbox head never made the phone require recovery", func() bool {
		state, err := h.App.ConnectionState()
		return err == nil && state == "offline"
	})

	if err := h.App.RefreshRoster(); err != nil {
		t.Fatalf("RefreshRoster: %v", err)
	}
	recovery := h.AwaitCommand(schema.ActionJournalResync)
	if !recovery.DiscardedBacklog || recovery.DiscardRecoveryToken == "" {
		t.Fatalf("recovery command = %#v, want explicit discard proof and token", recovery)
	}

	h.SealOffset(0)
	if err := h.sink.RecoverySnapshot([]schema.JournalRecord{{
		SessionID: testSession, Type: "roster", Group: "working", Capabilities: liveCaps(true),
	}}, 2, recovery.DiscardRecoveryToken); err != nil {
		t.Fatalf("RecoverySnapshot: %v", err)
	}
	eventually(t, "discard recovery did not restore the structured session", func() bool {
		s, err := h.App.Session(testSession)
		return err == nil && s.StructuredChat
	})

	op, err := h.App.ComposerSend(testSession, "", "after recovery")
	if err != nil {
		t.Fatalf("ComposerSend after recovery: %v", err)
	}
	command := h.AwaitCommand(schema.ActionComposerSend)
	if command.OperationID != op.OperationID {
		t.Fatalf("composer command operation = %q, want %q", command.OperationID, op.OperationID)
	}

	appendReplyEventually(t, h, schema.Control{
		Op:          protocol.OpError,
		ErrorCode:   protocol.CodeInputBusy,
		Error:       "the daemon retained no input",
		EndpointID:  h.Machine,
		SessionID:   testSession,
		OperationID: op.OperationID,
	})
	eventually(t, "post-recovery command outcome never resolved its operation", func() bool {
		out, outcomeErr := h.App.Outcome(op.OperationID)
		return outcomeErr == nil && out.Resolved && out.Code == string(protocol.CodeInputBusy)
	})

	// Probe one can share the depth-two mailbox with the outcome. Probe two cannot be
	// accepted into relay custody until the phone's coalesced ack has passed the outcome.
	appendReplyEventually(t, h, schema.Control{Op: protocol.OpOK, OperationID: "ack-probe-1"})
	appendReplyEventually(t, h, schema.Control{Op: protocol.OpOK, OperationID: "ack-probe-2"})
}

// appendReplyEventually allocates one reply sequence, seals once, and retries those exact
// bytes only when the honest test relay reports its depth quota. Re-sealing a retry would
// turn this ack probe into the delivery-unknown sequence bug the gateway forbids.
func appendReplyEventually(t *testing.T, h *harness, ctrl schema.Control) {
	t.Helper()
	h.mu.Lock()
	h.replySeq++
	seq := h.replySeq
	h.mu.Unlock()
	env, err := remotegw.SealControlReply(h.Keys.ContentKey, h.EpochID, seq, ctrl)
	if err != nil {
		t.Fatalf("SealControlReply(%q): %v", ctrl.OperationID, err)
	}
	eventually(t, "reply never entered relay custody: "+ctrl.OperationID, func() bool {
		_, appendErr := h.machineRelay.MailboxAppend(h.ctx, h.phoneTarget, env)
		if appendErr == nil {
			return true
		}
		if errors.Is(appendErr, relay.ErrQuotaExceeded) {
			return false
		}
		t.Fatalf("append reply %q: %v", ctrl.OperationID, appendErr)
		return false
	})
}
