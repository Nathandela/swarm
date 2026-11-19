package protocol

import (
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

const (
	CapSchedule        = "schedule"
	CapScheduleRunID   = "schedule-run-id"
	OpScheduleTick     = "schedule_tick"
	OpScheduleRun      = "schedule_run"
	OpScheduleComplete = "schedule_complete"
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
