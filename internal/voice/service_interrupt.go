package voice

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Nathandela/swarm/internal/managerproof"
	"github.com/Nathandela/swarm/internal/protocol"
)

type managerInterruptRuntime interface {
	ManagerInterrupt(context.Context, protocol.ManagerInterruptReq, bool) (protocol.ManagerInterruptReceipt, error)
}

func (p *ProtocolControllerClient) ManagerInterrupt(ctx context.Context, req protocol.ManagerInterruptReq, statusOnly bool) (protocol.ManagerInterruptReceipt, error) {
	return controllerOwnerCall(p, ctx, 3*time.Second, []string{protocol.CapManagerInterrupt}, func(c *protocol.Client) (protocol.ManagerInterruptReceipt, error) {
		return c.ManagerInterrupt(ctx, req, statusOnly)
	})
}

func (c *managerControl) interruptManager(ctx context.Context, req managerRecoveryRequest, statusOnly bool) (protocol.ManagerInterruptReceipt, error) {
	var none protocol.ManagerInterruptReceipt
	request := protocol.ManagerInterruptReq{Binding: runtimeBinding(req.Expected), NativeInputID: req.NativeInputID, NativeTurnID: req.NativeTurnID}
	currentEndpoint, _, valid := protocol.ParseID(c.binding.SessionID)
	targetEndpoint, _, targetValid := protocol.ParseID(req.Expected.SessionID)
	if request.Validate() != nil || !valid || !targetValid || currentEndpoint != targetEndpoint {
		return none, ErrInvalid
	}
	if req.Expected.Generation > c.binding.Generation || req.Expected.ConversationID != c.binding.ConversationID ||
		(!statusOnly && (c.controller == nil || c.binding != req.Expected)) {
		return none, ErrStale
	}
	if !statusOnly {
		if err := c.store.checkControllerBinding(c.binding); err != nil {
			return none, localRecoveryError(err)
		}
	}
	var runtime managerInterruptRuntime
	if c.controller != nil {
		runtime, _ = c.controller.runtime.(managerInterruptRuntime)
	} else if c.replacement != nil {
		runtime, _ = c.replacement.runtime.(managerInterruptRuntime)
	}
	if runtime == nil {
		return none, ErrControllerUnsupported
	}
	receipt, err := runtime.ManagerInterrupt(ctx, request, statusOnly)
	if errors.Is(err, managerproof.ErrStale) {
		return none, ErrStale
	}
	return receipt, localRecoveryError(err)
}

func activeManagerInterruptTarget(st State, binding ManagerBinding) (input, turn string) {
	if checkBinding(&st, binding) != nil || st.Conversations[binding.ConversationID].CustodyLost {
		return "", ""
	}
	for _, a := range st.Admissions {
		if a.ConversationID != binding.ConversationID || a.NativeBindingRetired || !a.SettledAt.IsZero() ||
			a.Phase != "running" || !managerproof.ValidHash(a.NativeInputID) || a.NativeTurnID == "" {
			continue
		}
		if input != "" {
			return "", ""
		}
		input, turn = a.NativeInputID, a.NativeTurnID
	}
	return input, turn
}

// InterruptManager asks to stop one exact admitted manager turn.
func (c *ManagerClient) InterruptManager(ctx context.Context, expected ManagerBinding, nativeInputID, nativeTurnID string) (protocol.ManagerInterruptReceipt, error) {
	return c.managerInterrupt(ctx, "/v1/recovery/interrupt", expected, nativeInputID, nativeTurnID)
}

// ManagerInterruptStatus reads the original receipt without dispatching another interrupt.
func (c *ManagerClient) ManagerInterruptStatus(ctx context.Context, expected ManagerBinding, nativeInputID, nativeTurnID string) (protocol.ManagerInterruptReceipt, error) {
	return c.managerInterrupt(ctx, "/v1/recovery/interrupt-status", expected, nativeInputID, nativeTurnID)
}

func (c *ManagerClient) managerInterrupt(ctx context.Context, path string, expected ManagerBinding, nativeInputID, nativeTurnID string) (protocol.ManagerInterruptReceipt, error) {
	var receipt protocol.ManagerInterruptReceipt
	req := &managerRecoveryRequest{Expected: expected, NativeInputID: nativeInputID, NativeTurnID: nativeTurnID}
	if err := c.callJSON(ctx, http.MethodPost, path, req, &receipt); err != nil {
		return protocol.ManagerInterruptReceipt{}, err
	}
	id := protocol.ManagerInterruptOperationID(protocol.ManagerInterruptReq{Binding: runtimeBinding(expected),
		NativeInputID: nativeInputID, NativeTurnID: nativeTurnID})
	if receipt.OperationID != id || receipt.NativeInputID != nativeInputID || receipt.NativeTurnID != nativeTurnID || !receipt.ValidState() {
		return protocol.ManagerInterruptReceipt{}, ErrCorrupt
	}
	return receipt, nil
}
