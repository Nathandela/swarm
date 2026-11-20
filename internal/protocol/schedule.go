package protocol

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

const (
	CapSchedule          = "schedule"
	CapScheduleRoute     = "schedule-route-check"
	OpScheduleRoute      = "schedule_route_check"
	CapScheduleWorkspace = "schedule-workspace-check"
	CapScheduleHostTime  = "schedule-host-time"
	OpScheduleWorkspace  = "schedule_workspace_check"
	CapScheduleRunID     = "schedule-run-id"
	OpScheduleTick       = "schedule_tick"
	OpScheduleRun        = "schedule_run"
	OpScheduleComplete   = "schedule_complete"
)

// ServeScheduleCompletion exposes only the attempt-bound receipt operation.
// The owner listener's general session controls never enter this transport.
func ServeScheduleCompletion(d DaemonAPI, socketPath, endpointID string, ready <-chan struct{}) (*Server, error) {
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(socketPath, 0600); err != nil {
		_ = ln.Close()
		return nil, err
	}
	s := newServer(d)
	s.ln, s.endpointID, s.completionTier = ln, endpointID, true
	s.wg.Add(1)
	go s.acceptLoop(ready)
	return s, nil
}

type ScheduleSource interface {
	ScheduleTick() error
	ScheduleRun(jobID, operationID string) (string, error)
	ScheduleComplete(localSession string, receipt schema.ScheduleCompletion) error
}

func (c *Client) ScheduleTick() error {
	_, err := c.scheduleRequest(Control{Op: OpScheduleTick})
	return err
}

func (c *Client) ScheduleRun(jobID, operationID string) (string, error) {
	if !slices.Contains(c.caps, CapScheduleRunID) {
		return "", errors.New("run now requires a daemon that supports schedule operation IDs")
	}
	if operationID == "" {
		return "", errors.New("run now requires an operation ID")
	}
	return c.scheduleRequest(Control{Op: OpScheduleRun, ScheduleID: jobID, OperationID: operationID})
}

func (c *Client) ScheduleComplete(localSession string, receipt schema.ScheduleCompletion) error {
	_, err := c.scheduleRequest(Control{
		Op:                 OpScheduleComplete,
		SessionID:          NamespacedID(c.endpointID, localSession),
		ScheduleCompletion: &receipt,
	})
	return err
}

func (c *Client) scheduleRequest(req Control) (string, error) {
	if !slices.Contains(c.caps, CapSchedule) {
		return "", errors.New("scheduled automations require an updated local daemon")
	}
	req.EndpointID = c.endpointID
	resp, err := c.requestWithTimeout(req, 110*time.Second)
	if err != nil {
		return "", err
	}
	if req.Op == OpScheduleRun && (resp.EndpointID != c.endpointID || resp.OperationID != req.OperationID) {
		return "", errors.New("schedule run response could not be correlated with its operation ID")
	}
	if resp.Op == OpError {
		return "", fmt.Errorf("scheduled automations: %s", resp.Error)
	}
	if resp.Op != req.Op || resp.EndpointID != c.endpointID || (req.Op == OpScheduleRun && resp.ScheduleID == "") {
		return "", errors.New("scheduled automation response could not be correlated")
	}
	return resp.ScheduleID, nil
}

func (cc *clientConn) handleSchedule(req Control) {
	if cc.srv.remoteTier || !cc.hasCap(CapSchedule) {
		cc.replyErrorCode("scheduled automations require the local owner socket", CodeNotAuthorized)
		return
	}
	src, ok := cc.srv.d.(ScheduleSource)
	if !ok {
		cc.replyErrorCode("scheduled automations are unavailable", CodeNotImplemented)
		return
	}

	var runID string
	var err error
	switch req.Op {
	case OpScheduleTick:
		err = src.ScheduleTick()
	case OpScheduleRun:
		if !cc.hasCap(CapScheduleRunID) {
			cc.replyErrorCode("Run now requires a client that supports schedule operation IDs", CodeNotImplemented)
			return
		}
		if req.OperationID == "" {
			cc.replyErrorCode("Run now requires an operation ID", CodeInvalidField)
			return
		}
		runID, err = src.ScheduleRun(req.ScheduleID, req.OperationID)
	case OpScheduleComplete:
		origin, local, found := ParseID(req.SessionID)
		if !found || origin != cc.endpointID || !validLocalID(local) || req.ScheduleCompletion == nil {
			cc.replyErrorCode("completion requires the local worker session and its receipt", CodeInvalidField)
			return
		}
		err = src.ScheduleComplete(local, *req.ScheduleCompletion)
	}
	if err != nil {
		cc.replyErrorCode(err.Error(), CodeInvalidField)
		return
	}
	_ = cc.writeControl(Control{Op: req.Op, EndpointID: cc.endpointID, ScheduleID: runID, OperationID: req.OperationID})
}

// ScheduleWorkspaceConflict asks the local owner daemon for the same workspace
// conflict it checks during launch. An empty result means currently available.
func (c *Client) ScheduleWorkspaceConflict(path string) (string, error) {
	if !slices.Contains(c.caps, CapScheduleWorkspace) {
		return "", errors.New("restart the updated Swarm daemon to check workspace availability")
	}
	resp, err := c.requestWithTimeout(Control{Op: OpScheduleWorkspace, EndpointID: c.endpointID, ScheduleWorkspace: path}, 5*time.Second)
	if err != nil {
		return "", err
	}
	if resp.Op == OpError {
		return "", fmt.Errorf("workspace check: %s", resp.Error)
	}
	if resp.Op != OpScheduleWorkspace || resp.EndpointID != c.endpointID {
		return "", errors.New("invalid workspace check response")
	}
	return resp.ScheduleWorkspace, nil
}

type scheduleWorkspaceSource interface{ ScheduleWorkspaceConflict(string) string }

func (cc *clientConn) handleScheduleWorkspace(req Control) {
	src, ok := cc.srv.d.(scheduleWorkspaceSource)
	if !ok || cc.srv.remoteTier || cc.srv.completionTier || !cc.hasCap(CapScheduleWorkspace) {
		cc.replyErrorCode("workspace check requires the local owner socket", CodeNotAuthorized)
		return
	}
	if !filepath.IsAbs(req.ScheduleWorkspace) {
		cc.replyErrorCode("workspace check requires an absolute path", CodeInvalidField)
		return
	}
	_ = cc.writeControl(Control{Op: OpScheduleWorkspace, EndpointID: cc.endpointID, ScheduleWorkspace: src.ScheduleWorkspaceConflict(req.ScheduleWorkspace)})
}

type scheduleRouteSource interface{ ScheduleRouteAvailability(string) string }

func (c *Client) ScheduleRouteAvailability(agent string) (string, error) {
	if !slices.Contains(c.caps, CapScheduleRoute) {
		return "", errors.New("restart the updated Swarm daemon to check agent availability")
	}
	resp, err := c.requestWithTimeout(Control{Op: OpScheduleRoute, EndpointID: c.endpointID, ScheduleAgent: agent}, 10*time.Second)
	if err != nil {
		return "", err
	}
	if resp.Op == OpError {
		return "", fmt.Errorf("agent check: %s", resp.Error)
	}
	if resp.Op != OpScheduleRoute || resp.EndpointID != c.endpointID || resp.ScheduleAgent != agent {
		return "", errors.New("invalid agent check response")
	}
	return resp.ScheduleUnavailable, nil
}

func (cc *clientConn) handleScheduleRoute(req Control) {
	src, ok := cc.srv.d.(scheduleRouteSource)
	if !ok || cc.srv.remoteTier || cc.srv.completionTier || !cc.hasCap(CapScheduleRoute) {
		cc.replyErrorCode("agent check requires the local owner socket", CodeNotAuthorized)
		return
	}
	switch req.ScheduleAgent {
	case "codex", "claude", "agy":
	default:
		cc.replyErrorCode("unknown scheduled agent", CodeInvalidField)
		return
	}
	_ = cc.writeControl(Control{Op: OpScheduleRoute, EndpointID: cc.endpointID, ScheduleAgent: req.ScheduleAgent, ScheduleUnavailable: src.ScheduleRouteAvailability(req.ScheduleAgent)})
}
