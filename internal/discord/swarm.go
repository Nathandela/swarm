package discord

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// SwarmClient opens short-lived owner connections; it never starts a daemon.
type SwarmClient struct{ Socket string }

func (s SwarmClient) dial(ctx context.Context) (*protocol.Client, error) {
	c, err := protocol.DialContext(ctx, s.Socket, []string{protocol.CapSemanticSend, protocol.CapMessageOrigin, protocol.CapNativeRequests, protocol.CapJournal, protocol.CapOpenCodeObservation})
	if err == nil && ctx.Err() != nil {
		_ = c.Close()
		return nil, ctx.Err()
	}
	return c, err
}
func projectWorkers(rows []protocol.SessionView) []Worker {
	out := make([]Worker, 0, len(rows))
	for _, row := range protocol.VisibleDiscussions(rows) {
		instance := "unavailable:" + row.ID
		if row.Capabilities != nil && row.Capabilities.SessionInstance != "" {
			instance = row.Capabilities.SessionInstance
		}
		name := row.Name
		if name == "" {
			name = row.Agent
		}
		state := string(row.Status.Process)
		if row.Status.Process == status.ProcessRunning && row.Status.Turn == status.TurnActive {
			state = "working"
		}
		out = append(out, Worker{Target: Target{ID: row.ID, Instance: instance, Name: name, Agent: row.Agent}, Project: filepath.Base(row.Cwd), State: state, Supersedes: row.Supersedes, Attention: row.Status.Interaction == status.InteractionPermission || row.Status.Interaction == status.InteractionPrompt})
	}
	return out
}
func (s SwarmClient) Roster(ctx context.Context) ([]Worker, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c, err := s.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	rows, err := c.List()
	if err != nil {
		return nil, err
	}
	return projectWorkers(rows), nil
}
func (s SwarmClient) Submit(ctx context.Context, operation string, req protocol.ComposerSendReq) (protocol.SemanticMessageReceipt, error) {
	if err := ctx.Err(); err != nil {
		return protocol.SemanticMessageReceipt{}, err
	}
	c, err := s.dial(ctx)
	if err != nil {
		return protocol.SemanticMessageReceipt{}, fmt.Errorf("%w: daemon unavailable: %v", protocol.ErrSemanticSendRejected, err)
	}
	defer func() { _ = c.Close() }()
	if err = c.SemanticSend(operation, req); err != nil {
		return protocol.SemanticMessageReceipt{}, err
	}
	r, err := c.SemanticOperation(req.Session, operation)
	if err != nil {
		return protocol.SemanticMessageReceipt{OperationID: operation, Session: req.Session, SessionInstance: req.SessionInstance, State: "submitted"}, nil
	}
	return r, nil
}
func (s SwarmClient) Launch(ctx context.Context, req protocol.LaunchReq) (string, string, error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	c, err := s.dial(ctx)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = c.Close() }()
	return c.Launch(req)
}
func (s SwarmClient) Resume(ctx context.Context, id string) (string, string, error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	c, err := s.dial(ctx)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = c.Close() }()
	rows, err := c.List()
	if err != nil {
		return "", "", err
	}
	for _, row := range rows {
		if row.ID == id {
			if row.Status.Process == status.ProcessRunning {
				return "", "", errors.New("discord: agent is already running")
			}
			return c.Launch(protocol.LaunchReq{Agent: row.Agent, Name: row.Name, Cwd: row.Cwd, Options: map[string]string{protocol.OptionResumeFrom: row.ID}, Cols: 80, Rows: 24})
		}
	}
	return "", "", ErrNotFound
}
func (s SwarmClient) Kill(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c, err := s.dial(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	return c.Kill(id)
}
func (s SwarmClient) Requests(ctx context.Context, req protocol.NativeRequestsReq) (protocol.NativeRequestsReply, error) {
	if err := ctx.Err(); err != nil {
		return protocol.NativeRequestsReply{}, err
	}
	c, err := s.dial(ctx)
	if err != nil {
		return protocol.NativeRequestsReply{}, err
	}
	defer func() { _ = c.Close() }()
	return c.NativeRequests(req)
}
func (s SwarmClient) Decide(ctx context.Context, operation string, req protocol.NativeDecisionReq) (protocol.NativeDecisionReceipt, error) {
	if err := ctx.Err(); err != nil {
		return protocol.NativeDecisionReceipt{}, err
	}
	c, err := s.dial(ctx)
	if err != nil {
		return protocol.NativeDecisionReceipt{}, err
	}
	defer func() { _ = c.Close() }()
	return c.NativeDecide(operation, req)
}
