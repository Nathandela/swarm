package voice

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// ProtocolControllerClient isolates each owner RPC: a lost send reply never blocks native proof reads.
// Only service shutdown cancels accepted mutations; device/bridge disconnect does not own their lifetime.
type ProtocolControllerClient struct {
	ctx              context.Context
	socket, endpoint string
}

func NewProtocolControllerClient(ctx context.Context, socket, endpoint string) (*ProtocolControllerClient, error) {
	if ctx == nil || ctx.Err() != nil || !filepath.IsAbs(socket) || filepath.Clean(socket) != socket || !configText(endpoint, 128) || strings.Contains(endpoint, "/") {
		return nil, ErrInvalid
	}
	return &ProtocolControllerClient{ctx: ctx, socket: socket, endpoint: endpoint}, nil
}
func controllerOwnerCall[T any](p *ProtocolControllerClient, caller context.Context, limit time.Duration, caps []string, call func(*protocol.Client) (T, error)) (T, error) {
	var zero T
	operation, cancel := context.WithTimeout(p.ctx, limit)
	defer cancel()
	stop := context.AfterFunc(caller, cancel)
	defer stop()
	if caller.Err() != nil {
		return zero, caller.Err()
	}
	client, err := protocol.DialContext(operation, p.socket, caps)
	if err != nil {
		return zero, err
	}
	defer func() { _ = client.Close() }()
	if client.EndpointID() != p.endpoint {
		return zero, ErrStale
	}
	for _, capability := range caps {
		if !slices.Contains(client.Capabilities(), capability) {
			return zero, ErrControllerUnsupported
		}
	}
	result, err := call(client)
	if operation.Err() != nil {
		return zero, operation.Err()
	}
	return result, err
}
func (p *ProtocolControllerClient) ManagerRuntime(session, instance string) (protocol.ManagerRuntimeStatus, error) {
	return controllerOwnerCall(p, p.ctx, 2*time.Second, []string{protocol.CapManagerProvenance}, func(c *protocol.Client) (protocol.ManagerRuntimeStatus, error) {
		return c.ManagerRuntime(session, instance)
	})
}
func (p *ProtocolControllerClient) ResolveManagerCall(ctx context.Context, req protocol.ManagerCallReq) (protocol.ManagerCallProof, error) {
	return controllerOwnerCall(p, ctx, 2*time.Second, []string{protocol.CapManagerProvenance}, func(c *protocol.Client) (protocol.ManagerCallProof, error) { return c.ResolveManagerCall(ctx, req) })
}
func (p *ProtocolControllerClient) ManagerInputStatus(ctx context.Context, binding protocol.ManagerRuntimeBinding, nonce string) (protocol.ManagerNativeTurn, error) {
	return controllerOwnerCall(p, ctx, 2*time.Second, []string{protocol.CapManagerProvenance}, func(c *protocol.Client) (protocol.ManagerNativeTurn, error) {
		return c.ManagerInputStatus(ctx, binding, nonce)
	})
}
func (p *ProtocolControllerClient) SemanticSend(operation string, req protocol.ComposerSendReq) error {
	caps := []string{protocol.CapSemanticSend}
	if req.Origin != nil {
		caps = append(caps, protocol.CapMessageOrigin)
	}
	if req.ManagerAdmission != nil {
		caps = append(caps, protocol.CapManagerProvenance)
	}
	_, err := controllerOwnerCall(p, p.ctx, 35*time.Second, caps, func(c *protocol.Client) (struct{}, error) { return struct{}{}, c.SemanticSend(operation, req) })
	return err
}
func (p *ProtocolControllerClient) SemanticOperation(session, operation string) (protocol.SemanticMessageReceipt, error) {
	return controllerOwnerCall(p, p.ctx, 35*time.Second, []string{protocol.CapSemanticSend}, func(c *protocol.Client) (protocol.SemanticMessageReceipt, error) {
		return c.SemanticOperation(session, operation)
	})
}
func (p *ProtocolControllerClient) List() ([]protocol.SessionView, error) {
	return p.ListContext(p.ctx)
}

func (p *ProtocolControllerClient) ListContext(ctx context.Context) ([]protocol.SessionView, error) {
	return controllerOwnerCall(p, ctx, 10*time.Second, nil, func(c *protocol.Client) ([]protocol.SessionView, error) { return c.List() })
}
func (p *ProtocolControllerClient) NativeHistoryPage(req schema.InteractionHistoryReq) (schema.NativeHistoryPage, error) {
	return controllerOwnerCall(p, p.ctx, 10*time.Second, []string{protocol.CapJournal}, func(c *protocol.Client) (schema.NativeHistoryPage, error) { return c.NativeHistoryPage(req) })
}
func (p *ProtocolControllerClient) LaunchOperation(operation string, req protocol.LaunchReq) (string, string, error) {
	type launchResult struct{ ID, Name string }
	caps := []string{protocol.CapOwnerLaunch}
	if req.Manager != nil {
		caps = append(caps, protocol.CapManagerLaunch)
		if req.Manager.RetireOperationID != "" {
			caps = append(caps, protocol.CapManagerRetire)
		}
	}
	if req.ResumeSourceInstance != "" {
		caps = append(caps, protocol.CapOwnerBoundResume)
	}
	if req.WorkerBridge != "" {
		caps = append(caps, protocol.CapTeammateWorkerBridge)
	}
	result, err := controllerOwnerCall(p, p.ctx, 100*time.Second, caps, func(c *protocol.Client) (launchResult, error) {
		id, name, err := c.LaunchOperation(operation, req)
		return launchResult{id, name}, err
	})
	return result.ID, result.Name, err
}
func (p *ProtocolControllerClient) LaunchOperationStatus(operation string) (schema.OperationOutcomeView, error) {
	return controllerOwnerCall(p, p.ctx, 10*time.Second, []string{protocol.CapOwnerLaunch}, func(c *protocol.Client) (schema.OperationOutcomeView, error) {
		return c.LaunchOperationStatus(operation)
	})
}
func (p *ProtocolControllerClient) NativeRequests(req protocol.NativeRequestsReq) (protocol.NativeRequestsReply, error) {
	return controllerOwnerCall(p, p.ctx, 10*time.Second, []string{protocol.CapNativeRequests}, func(c *protocol.Client) (protocol.NativeRequestsReply, error) { return c.NativeRequests(req) })
}
func (p *ProtocolControllerClient) NativeDecide(operation string, req protocol.NativeDecisionReq) (protocol.NativeDecisionReceipt, error) {
	return controllerOwnerCall(p, p.ctx, 35*time.Second, []string{protocol.CapNativeRequests}, func(c *protocol.Client) (protocol.NativeDecisionReceipt, error) {
		return c.NativeDecide(operation, req)
	})
}
func (p *ProtocolControllerClient) NativeDecisionStatus(session, operation string) (protocol.NativeDecisionReceipt, error) {
	return controllerOwnerCall(p, p.ctx, 35*time.Second, []string{protocol.CapNativeRequests}, func(c *protocol.Client) (protocol.NativeDecisionReceipt, error) {
		return c.NativeDecisionStatus(session, operation)
	})
}

var _ ControllerRuntime = (*ProtocolControllerClient)(nil)
var _ ControllerWorkers = (*ProtocolControllerClient)(nil)

func (p *ProtocolControllerClient) RetainedHistoryPage(req schema.InteractionHistoryReq) (protocol.RetainedHistoryPage, error) {
	return controllerOwnerCall(p, p.ctx, 10*time.Second, []string{protocol.CapJournal}, func(c *protocol.Client) (protocol.RetainedHistoryPage, error) { return c.RetainedHistoryPage(req) })
}

func (p *ProtocolControllerClient) ManagerRetire(ctx context.Context, req protocol.ManagerRetireReq, statusOnly bool) (protocol.ManagerRetireReceipt, error) {
	return controllerOwnerCall(p, ctx, 20*time.Second, []string{protocol.CapManagerRetire}, func(c *protocol.Client) (protocol.ManagerRetireReceipt, error) {
		return c.ManagerRetire(ctx, req, statusOnly)
	})
}

// discussionSupported fences connector work before any native launch on an older daemon.
func (p *ProtocolControllerClient) discussionSupported(ctx context.Context) error {
	caps := append([]string{
		protocol.CapOwnerLaunch, protocol.CapOwnerBoundResume, protocol.CapManagerLaunch, protocol.CapManagerProvenance,
		protocol.CapManagerRetire, protocol.CapTeammateWorkerBridge, protocol.CapMessageOrigin, protocol.CapSemanticSend,
	}, controllerJournalCaps...)
	_, err := controllerOwnerCall(p, ctx, 3*time.Second, caps, func(*protocol.Client) (struct{}, error) {
		return struct{}{}, nil
	})
	return err
}
