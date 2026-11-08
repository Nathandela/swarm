package protocol

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/status"
)

const (
	OpOwnerLaunch       = "owner_launch"
	OpOwnerLaunchStatus = "owner_launch_status"
	CapOwnerLaunch      = "owner-launch"
)

type OwnerLaunchSource interface {
	OwnerLaunchReplay(operationID, requestHash string) (persist.Meta, bool, error)
	OwnerLaunchOutcome(operationID string) (schema.OperationOutcomeView, bool)
}

// NewLaunchOperationID allocates a one-hour mutation ID. Persist it with the exact
// request before dispatch; status remains readable after the replay window.
func NewLaunchOperationID() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("launch:%d:%x", time.Now().UnixMilli(), nonce), nil
}

func validLaunchOperationID(id string, now time.Time) bool {
	parts := strings.Split(id, ":")
	if len(id) > 128 || len(parts) != 3 || parts[0] != "launch" || len(parts[1]) > 13 || len(parts[2]) != 32 {
		return false
	}
	ms, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || ms <= 0 || strconv.FormatInt(ms, 10) != parts[1] {
		return false
	}
	if _, err := hex.DecodeString(parts[2]); err != nil || strings.ToLower(parts[2]) != parts[2] {
		return false
	}
	issued := time.UnixMilli(ms)
	return !issued.After(now.Add(semanticClockSkew)) && now.Sub(issued) <= semanticReplayWindow
}

func ownerLaunchHash(req *LaunchReq) string {
	// encoding/json orders map keys; array order (including duplicate env keys)
	// remains significant. Bind owner-only env/lineage, unlike signed remote hash.
	body, _ := json.Marshal(req)
	hash := sha256.Sum256(body)
	return hex.EncodeToString(hash[:])
}

// LaunchOperation starts or reconciles an exact owner launch/resume operation.
// It never falls back to the legacy launch route when capability is unavailable.
func (c *Client) LaunchOperation(operationID string, req LaunchReq) (id, name string, err error) {
	if !slices.Contains(c.caps, CapOwnerLaunch) {
		return "", "", errors.New("protocol: reliable launch requires an updated daemon; run swarm daemon restart")
	}
	if req.Manager != nil && !slices.Contains(c.caps, CapManagerLaunch) {
		return "", "", errors.New("protocol: manager launch requires an updated daemon and manager-launch capability")
	}
	if !validLaunchOperationID(operationID, time.Now()) {
		return "", "", errors.New("protocol: invalid or expired launch operation ID; an earlier attempt may still have an effect")
	}
	if _, relocate := req.Options[OptionResumeRelocate]; relocate && !slices.Contains(c.caps, CapResumeRelocate) {
		return "", "", errors.New("resume relocation requires an updated daemon; run swarm daemon restart")
	}
	timeout := 100 * time.Second
	resp, err := c.requestWithTimeout(Control{Op: OpOwnerLaunch, EndpointID: c.endpointID, OperationID: operationID, Launch: &req}, timeout)
	if err != nil {
		return "", "", err
	}
	if resp.Op == OpError {
		return "", "", fmt.Errorf("protocol: owner launch: %s (%s)", resp.Error, resp.ErrorCode)
	}
	if resp.Op != OpLaunch || resp.OperationID != operationID || resp.Session == nil {
		_ = c.Close()
		return "", "", errors.New("protocol: uncorrelated launch receipt; outcome unknown")
	}
	return resp.Session.ID, resp.Session.Name, nil
}

func (c *Client) LaunchOperationStatus(operationID string) (schema.OperationOutcomeView, error) {
	if !slices.Contains(c.caps, CapOwnerLaunch) {
		return schema.OperationOutcomeView{}, errors.New("protocol: reliable launch status requires an updated daemon; run swarm daemon restart")
	}
	resp, err := c.request(Control{Op: OpOwnerLaunchStatus, EndpointID: c.endpointID, SubjectOperationID: operationID})
	if err != nil {
		return schema.OperationOutcomeView{}, err
	}
	if resp.Op == OpError {
		return schema.OperationOutcomeView{}, fmt.Errorf("protocol: launch status: %s (%s)", resp.Error, resp.ErrorCode)
	}
	if resp.Op != OpOwnerLaunchStatus || resp.SubjectOperationID != operationID || resp.OperationOutcome == nil {
		_ = c.Close()
		return schema.OperationOutcomeView{}, errors.New("protocol: uncorrelated launch status")
	}
	return *resp.OperationOutcome, nil
}

func (cc *clientConn) ownerLaunchSource() (OwnerLaunchSource, bool) {
	if cc.srv.remoteTier {
		cc.replyErrorCode("owner launch is available only on the owner socket", CodeNotAuthorized)
		return nil, false
	}
	if !cc.hasCap(CapOwnerLaunch) {
		cc.replyErrorCode("owner-launch capability was not negotiated", CodeNotImplemented)
		return nil, false
	}
	src, ok := cc.srv.d.(OwnerLaunchSource)
	if !ok {
		cc.replyErrorCode("reliable owner launch is unavailable", CodeNotImplemented)
	}
	return src, ok
}

func (cc *clientConn) handleOwnerLaunch(c Control) {
	src, ok := cc.ownerLaunchSource()
	if !ok {
		return
	}
	if c.Launch != nil && c.Launch.Manager != nil && !cc.hasCap(CapManagerLaunch) {
		cc.replyErrorCode("manager-launch capability was not negotiated", CodeNotImplemented)
		return
	}
	if c.Launch == nil || !validLaunchOperationID(c.OperationID, cc.srv.now()) {
		cc.replyErrorCode("owner launch requires an exact request and current timestamped operation ID", CodeInvalidField)
		return
	}
	unlock := cc.srv.lockComposerOperation(c.OperationID)
	defer unlock()
	m, known, err := src.OwnerLaunchReplay(c.OperationID, ownerLaunchHash(c.Launch))
	if known {
		if err != nil {
			cc.ownerLaunchError(err)
			return
		}
		_ = cc.writeControl(Control{Op: OpLaunch, EndpointID: cc.endpointID, OperationID: c.OperationID, Session: cc.stampView(m, status.Derive(m.Status))})
		return
	}
	cc.handleLaunch(c)
}

func (cc *clientConn) ownerLaunchError(err error) {
	code := CodeInvalidField
	if errors.Is(err, daemon.ErrLaunchOutcomeUnknown) || errors.Is(err, daemon.ErrLaunchOpConsumed) {
		code = schema.CodeOutcomeUnknown
	}
	if !errors.Is(err, daemon.ErrOwnerLaunchConflict) {
		if src, ok := cc.srv.d.(OwnerLaunchSource); ok {
			if _, known := src.OwnerLaunchOutcome(cc.opID); known {
				code = schema.CodeOutcomeUnknown
			}
		}
	}
	cc.replyErrorCode("owner launch: "+err.Error(), code)
}

func (cc *clientConn) handleOwnerLaunchStatus(c Control) {
	src, ok := cc.ownerLaunchSource()
	if !ok {
		return
	}
	if c.SubjectOperationID == "" || len(c.SubjectOperationID) > 128 {
		cc.replyErrorCode("launch status requires subject_operation_id", CodeInvalidField)
		return
	}
	out, known := src.OwnerLaunchOutcome(c.SubjectOperationID)
	if !known {
		out = schema.OperationOutcomeView{State: schema.OutcomeUnknownOperation}
	}
	_ = cc.writeControl(Control{Op: OpOwnerLaunchStatus, EndpointID: cc.endpointID, SubjectOperationID: c.SubjectOperationID, OperationOutcome: &out})
}
