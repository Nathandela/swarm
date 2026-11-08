package protocol

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/Nathandela/swarm/internal/managerproof"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

const (
	CapManagerProvenance = "manager-provenance"
	OpManagerInputStatus = "manager_input_status"
	OpManagerRuntime     = "manager_runtime"
	OpManagerResolveCall = "manager_resolve_call"
)

type (
	ManagerRuntimeBinding = schema.ManagerRuntimeBinding
	ManagerAdmission      = schema.ManagerAdmission
	ManagerNativeTurn     = schema.ManagerNativeTurn
	ManagerRuntimeReq     = schema.ManagerRuntimeReq
	ManagerRuntimeStatus  = schema.ManagerRuntimeStatus
	ManagerCallReq        = schema.ManagerCallReq
	ManagerCallProof      = schema.ManagerCallProof
)
type ManagerRuntimeSource interface {
	ManagerInputStatus(context.Context, string, ManagerAdmission) (ManagerNativeTurn, error)
	ManagerRuntime(local, instance string) (ManagerRuntimeStatus, error)
	ResolveManagerCall(context.Context, string, ManagerCallReq) (ManagerCallProof, error)
}

func (c *Client) ManagerRuntime(session, instance string) (ManagerRuntimeStatus, error) {
	if !slices.Contains(c.caps, CapManagerProvenance) {
		return ManagerRuntimeStatus{}, errors.New("protocol: manager provenance requires an updated daemon")
	}
	response, err := c.managerRequest(context.Background(), Control{Op: OpManagerRuntime, EndpointID: c.endpointID, SessionID: session, BodyVersion: 1, ManagerRuntimeRequest: &ManagerRuntimeReq{SessionInstance: instance}})
	if err != nil {
		return ManagerRuntimeStatus{}, err
	}
	if response.Op != OpManagerRuntime || response.ManagerRuntime == nil || response.ManagerRuntime.Binding.Session != session || response.ManagerRuntime.Binding.SessionInstance != instance || response.ManagerRuntime.Binding.Validate() != nil {
		return ManagerRuntimeStatus{}, managerproof.ErrUnavailable
	}
	return *response.ManagerRuntime, nil
}
func (c *Client) ResolveManagerCall(ctx context.Context, req ManagerCallReq) (ManagerCallProof, error) {
	if err := ctx.Err(); err != nil {
		return ManagerCallProof{}, err
	}
	if !slices.Contains(c.caps, CapManagerProvenance) {
		return ManagerCallProof{}, errors.New("protocol: manager provenance was not negotiated")
	}
	response, err := c.managerRequest(ctx, Control{Op: OpManagerResolveCall, EndpointID: c.endpointID, SessionID: req.Binding.Session, BodyVersion: 1, ManagerCall: &req})
	if err != nil {
		return ManagerCallProof{}, err
	}
	if response.Op != OpManagerResolveCall || response.ManagerCallProof == nil || !response.ManagerCallProof.Main || !managerproof.ValidHash(response.ManagerCallProof.NativeInputID) || response.ManagerCallProof.NativeTurnID == "" || response.ManagerCallProof.NativeCallID == "" {
		return ManagerCallProof{}, managerproof.ErrUnavailable
	}
	return *response.ManagerCallProof, nil
}

func (c *Client) ManagerInputStatus(ctx context.Context, binding ManagerRuntimeBinding, nonce string) (ManagerNativeTurn, error) {
	if err := ctx.Err(); err != nil {
		return ManagerNativeTurn{}, err
	}
	if !slices.Contains(c.caps, CapManagerProvenance) {
		return ManagerNativeTurn{}, errors.New("protocol: manager provenance was not negotiated")
	}
	request := ManagerAdmission{Binding: binding, Nonce: nonce}
	if request.Validate() != nil {
		return ManagerNativeTurn{}, managerproof.ErrInvalid
	}
	response, err := c.managerRequest(ctx, Control{Op: OpManagerInputStatus, EndpointID: c.endpointID, SessionID: binding.Session, BodyVersion: 1, ManagerInputStatusRequest: &request})
	if err != nil {
		return ManagerNativeTurn{}, err
	}
	if response.Op != OpManagerInputStatus || response.ManagerInputStatus == nil || response.ManagerInputStatus.NativeInputID != nonce {
		return ManagerNativeTurn{}, managerproof.ErrUnavailable
	}
	return *response.ManagerInputStatus, nil
}

// Cancellation closes the unsequenced reply channel; a late reply cannot serve another call.
func (c *Client) managerRequest(ctx context.Context, request Control) (Control, error) {
	operation, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if operation.Err() != nil {
		return Control{}, operation.Err()
	}
	stop := context.AfterFunc(operation, func() { _ = c.Close() })
	defer stop()
	for !c.reqMu.TryLock() {
		select {
		case <-operation.Done():
			return Control{}, operation.Err()
		case <-c.done:
			return Control{}, managerproof.ErrUnavailable
		case <-time.After(time.Millisecond):
		}
	}
	defer c.reqMu.Unlock()
	if operation.Err() != nil {
		return Control{}, operation.Err()
	}
	select {
	case <-c.done:
		return Control{}, managerproof.ErrUnavailable
	default:
	}
	select {
	case <-c.respCh:
	default:
	}
	if err := c.writeControl(request); err != nil {
		_ = c.Close()
		if operation.Err() != nil {
			return Control{}, operation.Err()
		}
		return Control{}, managerproof.ErrUnavailable
	}
	select {
	case response := <-c.respCh:
		if operation.Err() != nil {
			return Control{}, operation.Err()
		}
		return response, nil
	case <-operation.Done():
		_ = c.Close()
		return Control{}, operation.Err()
	case <-c.done:
		if operation.Err() != nil {
			return Control{}, operation.Err()
		}
		return Control{}, managerproof.ErrUnavailable
	}
}
func (cc *clientConn) handleManagerRuntime(c Control) {
	if cc.srv.remoteTier {
		cc.replyErrorCode("manager provenance is owner-only", CodeNotAuthorized)
		return
	}
	if !cc.hasCap(CapManagerProvenance) {
		cc.replyErrorCode("manager provenance was not negotiated", CodeNotImplemented)
		return
	}
	if !cc.requireBodyVersion(c) {
		return
	}
	source, ok := cc.srv.d.(ManagerRuntimeSource)
	if !ok {
		cc.replyErrorCode("manager provenance is unavailable", CodeNotImplemented)
		return
	}
	local, ok := cc.resolveSession(c)
	if !ok {
		return
	}
	if c.Op == OpManagerRuntime {
		if c.ManagerRuntimeRequest == nil || c.ManagerRuntimeRequest.SessionInstance == "" {
			cc.replyErrorCode("manager runtime instance is required", CodeInvalidField)
			return
		}
		status, err := source.ManagerRuntime(local, c.ManagerRuntimeRequest.SessionInstance)
		if err != nil || status.Binding.Validate() != nil || status.Binding.Session != local || status.Binding.SessionInstance != c.ManagerRuntimeRequest.SessionInstance {
			cc.replyErrorCode("manager runtime proof is unavailable", CodeUnavailable)
			return
		}
		status.Binding.Session = c.SessionID
		_ = cc.writeControl(Control{Op: OpManagerRuntime, EndpointID: cc.endpointID, SessionID: c.SessionID, ManagerRuntime: &status})
		return
	}
	if c.Op == OpManagerInputStatus {
		if c.ManagerInputStatusRequest == nil || c.ManagerInputStatusRequest.Validate() != nil || c.ManagerInputStatusRequest.Binding.Session != c.SessionID {
			cc.replyErrorCode("invalid manager input status request", CodeInvalidField)
			return
		}
		req := *c.ManagerInputStatusRequest
		req.Binding.Session = local
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		outcome, err := source.ManagerInputStatus(ctx, local, req)
		if err != nil || outcome.NativeInputID != req.Nonce {
			cc.replyErrorCode("manager input outcome unavailable", CodeUnavailable)
			return
		}
		_ = cc.writeControl(Control{Op: OpManagerInputStatus, EndpointID: cc.endpointID, SessionID: c.SessionID, ManagerInputStatus: &outcome})
		return
	}
	if c.ManagerCall == nil || c.ManagerCall.Binding.Validate() != nil || c.ManagerCall.Binding.Session != c.SessionID || !managerproof.ValidHash(c.ManagerCall.ArgumentsHash) || c.ManagerCall.ToolName == "" {
		cc.replyErrorCode("invalid manager native-call request", CodeInvalidField)
		return
	}
	req := *c.ManagerCall
	req.Binding.Session = local
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	proof, err := source.ResolveManagerCall(ctx, local, req)
	if err != nil || !proof.Main || !managerproof.ValidHash(proof.NativeInputID) || proof.NativeTurnID == "" || proof.NativeCallID == "" {
		cc.replyErrorCode("manager native-call proof is unavailable", CodeUnavailable)
		return
	}
	_ = cc.writeControl(Control{Op: OpManagerResolveCall, EndpointID: cc.endpointID, SessionID: c.SessionID, ManagerCallProof: &proof})
}
