package skeleton

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/idempotency"
	"github.com/Nathandela/swarm/internal/managerproof"
	"github.com/Nathandela/swarm/internal/protocol"
)

func fixtureInterruptRequest(b protocol.ManagerRuntimeBinding, endpoint string) (protocol.ManagerInterruptReq, string) {
	req := protocol.ManagerInterruptReq{Binding: b, NativeInputID: strings.Repeat("a", 64), NativeTurnID: "turn"}
	wire := req
	wire.Binding.Session = protocol.NamespacedID(endpoint, b.Session)
	return req, protocol.ManagerInterruptOperationID(wire)
}

func TestManagerInterruptLostReplyReconcilesExactTerminalTurnAfterRestart(t *testing.T) {
	rig := newResumeAPIRig(t, "codex", migratedConversationID, nil)
	api := rig.api
	b := codexProofFixture()
	b.Session = rig.sourceID
	req, id := fixtureInterruptRequest(b, api.endpointID)
	ctx := t.Context()
	nativeWrites := 0
	api.managerInterruptTurn = func(_ context.Context, _ string, op string, sent protocol.ManagerInterruptReq, begin func() error) error {
		if sent != req || id != op {
			t.Fatal("native interrupt changed its exact target")
		}
		if err := begin(); err != nil {
			return err
		}
		record, ok := api.core.ComposerOperation(op)
		if !ok || record.Phase != idempotency.PhaseExecuting || len(record.Request) == 0 {
			t.Fatal("native effect preceded durable request and executing marker")
		}
		nativeWrites++
		return errors.New("native reply lost")
	}
	observed := protocol.ManagerNativeTurn{NativeInputID: req.NativeInputID, NativeTurnID: req.NativeTurnID, State: "running"}
	api.managerInputStatus = func(_ context.Context, _ string, input protocol.ManagerAdmission) (protocol.ManagerNativeTurn, error) {
		if input.Binding != req.Binding || input.Nonce != req.NativeInputID {
			return protocol.ManagerNativeTurn{}, managerproof.ErrStale
		}
		return observed, nil
	}
	if got, err := api.ManagerInterrupt(ctx, b.Session, id, req, true); err != nil || got.State != "unknown" {
		t.Fatalf("status reserved an operation: %+v %v", got, err)
	}
	if got, err := api.ManagerInterrupt(ctx, b.Session, id, req, false); err != nil || got.State != "pending" {
		t.Fatalf("lost reply was claimed as an interrupt: %+v %v", got, err)
	}
	if err := rig.core.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := daemon.Open(daemon.Config{StateDir: rig.stateDir, SocketPath: filepath.Join(rig.stateDir, "daemon.sock"),
		LockPath: filepath.Join(rig.stateDir, "daemon.lock"), LogPath: filepath.Join(rig.stateDir, "daemon.log"), MaxSessions: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	api.core = reopened
	observed.State, observed.Reason, observed.NativeTurnID = "completed", "aborted", "unrelated-turn"
	if got, err := api.ManagerInterrupt(ctx, b.Session, id, req, false); err != nil || got.State != "uncertain" || nativeWrites != 1 {
		t.Fatalf("restart redrove or accepted unrelated terminal evidence: %+v %v, sends %d", got, err, nativeWrites)
	}
	observed.NativeTurnID = req.NativeTurnID
	got, err := api.ManagerInterrupt(ctx, b.Session, id, req, true)
	if err != nil || got.State != "interrupted" || got.Reason != "aborted" {
		t.Fatalf("qualified terminal disposition did not settle: %+v %v", got, err)
	}
	api.managerInputStatus = func(context.Context, string, protocol.ManagerAdmission) (protocol.ManagerNativeTurn, error) {
		return protocol.ManagerNativeTurn{}, managerproof.ErrUnavailable
	}
	if repeat, err := api.ManagerInterrupt(ctx, b.Session, id, req, true); err != nil || repeat != got || nativeWrites != 1 {
		t.Fatalf("original terminal receipt not replayable: %+v %v", repeat, err)
	}
	changed := req
	changed.Binding.SessionInstance = "other-instance"
	if _, err := api.ManagerInterrupt(ctx, b.Session, id, changed, false); !errors.Is(err, managerproof.ErrStale) {
		t.Fatalf("another incarnation reused exact operation: %v", err)
	}
}

func TestManagerInterruptUsesExactCodexNativeThreadAndTurn(t *testing.T) {
	sk, b, feed := newCodexManagerSendRig(t)
	req, id := fixtureInterruptRequest(b, "machine")
	nativeWrites := 0
	sk.backend.live[b.Session].conn = &codexManagerTestConn{call: func(_ context.Context, method string, params, _ any) error {
		p := params.(map[string]any)
		switch method {
		case "turn/start":
			sk.captureCodexManagerFrame(b.Session, feed, "turn/started", codexManagerTurnFrame("turn/started", b.NativeSessionID, req.NativeTurnID, "inProgress"))
			sk.captureCodexManagerFrame(b.Session, feed, "item/completed", codexManagerUserFrame(b.NativeSessionID, req.NativeTurnID, "native-message", req.NativeInputID, "input"))
			return errors.New("fixture input reply lost")
		case "turn/interrupt":
			nativeWrites++
			if p["threadId"] != b.NativeSessionID || p["turnId"] != req.NativeTurnID {
				t.Fatalf("wrong Codex native stop target: %+v", p)
			}
			return nil
		default:
			t.Fatalf("unexpected native method %s", method)
		}
		return nil
	}}
	const sendOp = "manager-input-test"
	if _, _, err := sk.core.ClaimComposerOperation(sendOp, protocol.OpSemanticSend, b.Session, b.SessionInstance, "hash"); err != nil {
		t.Fatal(err)
	}
	lane := sk.composerLaneFor(b.Session)
	barrier := lane.enter()
	_, _ = sk.sendCodexManager(b.Session, sendOp, protocol.ComposerSendReq{Text: "input", ManagerAdmission: &protocol.ManagerAdmission{Binding: b, Nonce: req.NativeInputID}}, func() error { return sk.core.BeginComposerOperation(sendOp) }, lane, barrier)
	lane.leave()
	waitCodexCustody(t, feed)
	api := &coreAPI{core: sk.core, endpointID: "machine", managerInputStatus: sk.managerInputStatus, managerInterruptTurn: sk.interruptManagerTurn}
	wrong := req
	wrong.NativeInputID = strings.Repeat("b", 64)
	wire := wrong
	wire.Binding.Session = protocol.NamespacedID(api.endpointID, b.Session)
	wrongID := protocol.ManagerInterruptOperationID(wire)
	if _, err := api.ManagerInterrupt(t.Context(), b.Session, wrongID, wrong, false); !errors.Is(err, managerproof.ErrStale) || nativeWrites != 0 {
		t.Fatalf("a different admission reached the Codex backend: %v", err)
	}
	if _, known := sk.core.ComposerOperation(wrongID); known {
		t.Fatal("stale admission reserved stop custody")
	}
	if got, err := api.ManagerInterrupt(t.Context(), b.Session, id, req, false); err != nil || got.State != "pending" || nativeWrites != 1 {
		t.Fatalf("current exact turn did not remain pending on generic ACK: %+v %v, sends %d", got, err, nativeWrites)
	}
	sk.captureCodexManagerFrame(b.Session, feed, "turn/completed", codexManagerTurnFrame("turn/completed", b.NativeSessionID, req.NativeTurnID, "interrupted"))
	waitCodexCustody(t, feed)
	if got, err := api.ManagerInterrupt(t.Context(), b.Session, id, req, true); err != nil || got.State != "interrupted" || nativeWrites != 1 {
		t.Fatalf("retained Codex abort did not settle original stop: %+v %v, sends %d", got, err, nativeWrites)
	}
}
