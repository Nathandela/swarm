package skeleton

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/opencodebridge"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

type openCodeSendIntent struct {
	Origin      *protocol.MessageOrigin `json:"origin,omitempty"`
	Source      string                  `json:"source,omitempty"`
	Identity    opencodebridge.Identity `json:"identity"`
	MessageID   string                  `json:"message_id"`
	TextHash    string                  `json:"text_hash"`
	NotBeforeMS int64                   `json:"not_before_ms"`
}

func openCodeMessageID(operationID string, identity opencodebridge.Identity) string {
	hash := sha256.Sum256([]byte(operationID + "\x00" + identity.RootID + "\x00" + identity.BackendGeneration))
	return fmt.Sprintf("msg_%x", hash[:16])
}

func (d *Daemon) sendOpenCodeSemantic(local, operationID string, req protocol.ComposerSendReq, begin func() error, lane *composerLane, source string) (protocol.ErrorCode, error) {
	lane.endMu.Lock()
	defer lane.endMu.Unlock()
	var code protocol.ErrorCode
	attempt := func() error {
		lane.mu.Lock()
		ending := lane.ownerEnding || lane.retired || lane.recycling
		lane.mu.Unlock()
		if ending {
			code = protocol.CodeInputBusy
			return errors.New("OpenCode session is ending or recycling; nothing was submitted")
		}
		if d.core.UnobservedSemanticExecution(local, operationID) {
			return fmt.Errorf("%w: a prior native input has unconfirmed admission; no later message was submitted", protocol.ErrComposerOutcomeUnknown)
		}
		if d.srv != nil && d.srv.IsControlled(local) || d.remoteSrv != nil && d.remoteSrv.IsControlled(local) {
			code = protocol.CodeInputBusy
			return errors.New("OpenCode is controlled by an attached client; nothing was submitted")
		}
		view, ok := d.openCodeView(local)
		if !ok || !view.BusyKnown || view.Busy {
			code = protocol.CodeInputBusy
			return errors.New("OpenCode native activity is busy or unconfirmed; nothing was submitted")
		}
		m, ok := d.core.Get(local)
		if !ok || m.Status.Process != status.ProcessRunning {
			code = protocol.CodeStaleInstance
			return errors.New("OpenCode session is no longer running")
		}
		b, err := d.core.SessionOpenCodeBridge(local)
		if err != nil {
			return err
		}
		identity := opencodebridge.Identity{Version: b.Version, WorkerID: b.WorkerID, SessionInstance: b.SessionInstance, RootID: b.RootID, Directory: b.Directory, BackendGeneration: b.BackendGeneration, NativeCreatedAtMS: b.NativeCreatedAtMS}
		if !openCodeMetaMatches(m, identity) || view.Root != identity.RootID || view.Generation != identity.BackendGeneration {
			code = protocol.CodeStaleInstance
			return errors.New("OpenCode native generation changed before submission")
		}
		client, err := opencodebridge.NewClient(b.SocketPath, b.Token)
		if err != nil {
			return err
		}
		readyCtx, cancelReady := context.WithTimeout(context.Background(), 2*time.Second)
		ready, readyErr := client.Ready(readyCtx)
		cancelReady()
		client.Close()
		if readyErr != nil || ready.Identity != identity || !ready.Ready || !ready.Acknowledged || !ready.DraftGuardSupported || ready.InFlight {
			code = protocol.CodeInputBusy
			return errors.New("OpenCode has no qualified empty native composer; nothing was submitted")
		}
		action := protocol.OpSemanticSend
		if source == adapter.SourcePhone {
			action = protocol.ActionComposerSend
		}
		record, ok := d.core.ComposerOperation(operationID)
		if !ok || record.Action != action || record.SessionID != local || record.SessionInstance != req.SessionInstance {
			return errors.New("OpenCode submission has no matching durable message operation")
		}
		hash := sha256.Sum256([]byte(req.Text))
		if source == adapter.SourceOwner && strings.HasPrefix(operationID, "semantic:supervision:") {
			source = "supervisor"
		}
		intent := openCodeSendIntent{Origin: req.Origin, Source: source, Identity: identity, MessageID: openCodeMessageID(operationID, identity), TextHash: hex.EncodeToString(hash[:]), NotBeforeMS: time.Now().UnixMilli()}
		if len(record.Request) != 0 {
			var saved openCodeSendIntent
			if json.Unmarshal(record.Request, &saved) != nil || saved.Identity != identity || saved.MessageID != intent.MessageID || saved.TextHash != intent.TextHash || saved.NotBeforeMS <= 0 || !sameMessageOrigin(saved.Origin, req.Origin) || saved.Source != source && (saved.Source != "" || source != adapter.SourceOwner) {
				return errors.New("OpenCode immutable native request conflicts with this submission")
			}
			intent = saved
		}
		raw, err := json.Marshal(intent)
		if err != nil {
			return err
		}
		if err := d.core.BindSemanticRequest(operationID, raw); err != nil {
			return err
		}
		d.trackOpenCodeIntent(local, operationID, intent)
		if begin == nil {
			return errors.New("OpenCode requires a durable native execution boundary")
		}
		if err := begin(); err != nil {
			return fmt.Errorf("%w: could not persist native execution boundary", protocol.ErrComposerOutcomeUnknown)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 32*time.Second)
		defer cancel()
		receipt, err := d.core.DispatchOpenCode(ctx, local, opencodebridge.DispatchRequest{Version: 1, SessionInstance: b.SessionInstance, BackendGeneration: b.BackendGeneration, RootID: b.RootID, OperationID: operationID, Kind: "prompt", TargetID: b.RootID, MessageID: intent.MessageID, Text: req.Text})
		if err != nil {
			return fmt.Errorf("%w: native dispatch receipt unavailable", protocol.ErrComposerOutcomeUnknown)
		}
		if receipt.Status == "refused" {
			code = protocol.CodeInputBusy
			return fmt.Errorf("OpenCode refused submission before native dispatch: %s", receipt.Reason)
		}
		if receipt.Status == "rejected" {
			code = protocol.CodeInputBusy
			return fmt.Errorf("OpenCode rejected native submission: %s", receipt.Reason)
		}
		if receipt.Status != "admitted" || receipt.Identity != identity || receipt.Kind != "prompt" || receipt.TargetID != b.RootID || receipt.MessageID != intent.MessageID || receipt.Agent == "" || receipt.Model == nil || receipt.Model.ProviderID == "" || receipt.Model.ModelID == "" || receipt.SelectionBasis != "native_session_selection" {
			return fmt.Errorf("%w: exact native user admission is unconfirmed", protocol.ErrComposerOutcomeUnknown)
		}
		if err := d.recordOpenCodeAdmission(operationID, intent, receipt); err != nil {
			return fmt.Errorf("%w: native receipt persistence failed", protocol.ErrComposerOutcomeUnknown)
		}
		return nil
	}
	fenced := attempt
	if d.remoteSrv != nil {
		next := fenced
		fenced = func() error { return d.remoteSrv.WithInputFence(local, next) }
	}
	if d.srv != nil {
		next := fenced
		fenced = func() error { return d.srv.WithInputFence(local, next) }
	}
	err := fenced()
	if errors.Is(err, protocol.ErrComposerOutcomeUnknown) {
		code = protocol.CodeOutcomeUnknown
	}
	return code, err
}
