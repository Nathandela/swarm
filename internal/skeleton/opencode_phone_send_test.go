package skeleton

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/idempotency"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func (f *openCodeSendFixture) phoneSend(t *testing.T, operation string) (protocol.ErrorCode, error) {
	t.Helper()
	if _, _, err := f.d.core.ClaimComposerOperation(operation, protocol.ActionComposerSend, f.binding.WorkerID, f.request.SessionInstance, hex.EncodeToString(schema.ComposerSendContentHash(&f.request))); err != nil {
		t.Fatal(err)
	}
	code, err := f.d.api.ComposerSendTransactional(f.client.EndpointID(), operation, f.request, func() error {
		return f.d.core.BeginComposerOperation(operation)
	})
	if err == nil {
		if commitErr := f.d.core.CommitComposerOperation(operation, []byte(`{"ok":true}`), true); commitErr != nil {
			t.Fatal(commitErr)
		}
	}
	return code, err
}

func (f *openCodeSendFixture) enablePhoneComposer(t *testing.T) {
	t.Helper()
	cap, ok := f.d.sessionCapabilities(f.binding.WorkerID)
	if !ok {
		t.Fatal("missing session capability record")
	}
	cap.NativeInteractionVersion, cap.NativeComposer = 2, true
	f.d.registerSessionCapabilities(f.binding.WorkerID, cap)
}

func TestOpenCodePhoneSendRequiresCurrentNativeComposerProof(t *testing.T) {
	f := newOpenCodeSendFixture(t)
	f.d.openCode.mu.Lock()
	observer := f.d.openCode.sessions[f.binding.WorkerID]
	f.d.openCode.mu.Unlock()
	observer.transcript.sinkQualified.Store(false)
	f.d.setOpenCodeTranscriptCapability(f.binding.WorkerID, observer, true, false)
	code, err := f.phoneSend(t, "phone:no-proof")
	record, _ := f.d.core.ComposerOperation("phone:no-proof")
	if err == nil || code != protocol.CodeStructuredUnsupported || f.calls.Load() != 0 || record.Phase != idempotency.PhasePrepared {
		t.Fatalf("unproven phone composer reached native execution: code=%s err=%v calls=%d phase=%s", code, err, f.calls.Load(), record.Phase)
	}
}

func TestOpenCodePhoneSendRetainsNativeAdmissionAndPhoneAttribution(t *testing.T) {
	f := newOpenCodeSendFixture(t)
	f.enablePhoneComposer(t)
	if code, err := f.phoneSend(t, "phone:admitted"); err != nil {
		t.Fatalf("qualified native phone send: %s %v", code, err)
	}
	record, _ := f.d.core.ComposerOperation("phone:admitted")
	var intent openCodeSendIntent
	if json.Unmarshal(record.Request, &intent) != nil || intent.Source != adapter.SourcePhone || record.Phase != idempotency.PhaseCompleted || len(record.Evidence) == 0 || f.calls.Load() != 1 {
		t.Fatalf("phone send lacks exact durable native custody: %+v intent=%+v calls=%d", record, intent, f.calls.Load())
	}
	// Rebuild attribution from durable custody, as a new daemon does.
	f.d.nativeReceipts.mu.Lock()
	f.d.nativeReceipts.sessions = nil
	f.d.nativeReceipts.mu.Unlock()
	view := OpenCodeView{Root: intent.Identity.RootID, Generation: intent.Identity.BackendGeneration, Inputs: []OpenCodeInput{{MessageID: intent.MessageID}}}
	owned := f.d.ownedOpenCodeInputs(f.binding.WorkerID, view)
	if owned[intent.MessageID].operation != "phone:admitted" || owned[intent.MessageID].intent.Source != adapter.SourcePhone {
		t.Fatalf("phone attribution disappeared on daemon recovery: %+v", owned)
	}
}

func TestOpenCodeLostPhoneAdmissionFencesOtherSourcesAndReconcilesWithoutPost(t *testing.T) {
	f := newOpenCodeSendFixture(t)
	f.enablePhoneComposer(t)
	f.dropReply.Store(true)
	if _, err := f.phoneSend(t, "phone:lost"); !errors.Is(err, protocol.ErrComposerOutcomeUnknown) {
		t.Fatalf("lost phone admission became definite: %v", err)
	}
	id, err := protocol.NewSemanticOperationID()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.client.SemanticSend(id, f.request); err == nil || !strings.Contains(err.Error(), string(protocol.CodeOutcomeUnknown)) || f.calls.Load() != 1 {
		t.Fatalf("owner overtook unconfirmed phone input: err=%v calls=%d", err, f.calls.Load())
	}
	f.rowAvailable.Store(true)
	receipt, err := f.d.semanticOperation(f.binding.WorkerID, "phone:lost")
	record, _ := f.d.core.ComposerOperation("phone:lost")
	if err != nil || receipt.State != "admitted" || record.Phase != idempotency.PhaseCompleted || f.calls.Load() != 1 {
		t.Fatalf("read-only phone admission recovery failed: receipt=%+v err=%v phase=%s calls=%d", receipt, err, record.Phase, f.calls.Load())
	}
}
