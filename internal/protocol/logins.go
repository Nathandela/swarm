package protocol

import (
	"errors"
	"strings"
	"unicode"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

var ErrLoginsUnavailable = errors.New("protocol: login management unavailable")
var ErrLoginsStaleRevision = errors.New("protocol: login list changed; refresh and retry")
var ErrLoginsUnsafeNativeCLI = errors.New("protocol: provider CLI permissions are unsafe")

const CodeLoginsUnsafeNativeCLI = schema.CodeLoginsUnsafeNativeCLI

type LoginsBackend interface {
	Logins(LoginsReq) (LoginsReply, error)
}

func (c *Client) Logins(req LoginsReq) (LoginsReply, error) {
	r, err := c.request(Control{Op: OpLoginsManage, EndpointID: c.endpointID, LoginsRequest: &req})
	if err != nil {
		return LoginsReply{}, err
	}
	if r.Op == OpError {
		return LoginsReply{}, loginsReplyError(r)
	}
	if r.Op != OpLoginsManage || r.LoginsResult == nil {
		return LoginsReply{}, ErrLoginsUnavailable
	}
	return *r.LoginsResult, nil
}

func loginsReplyError(reply Control) error {
	switch reply.ErrorCode {
	case CodeLoginsUnsafeNativeCLI:
		return ErrLoginsUnsafeNativeCLI
	case CodeStaleRevision:
		return ErrLoginsStaleRevision
	case CodeUnavailable, CodeCapabilityRefused:
		return ErrLoginsUnavailable
	default:
		return errors.New("protocol: login operation refused")
	}
}

func validateLoginsReq(r LoginsReq) bool {
	for _, s := range []string{r.Provider, r.LoginID, r.Label, r.JobID} {
		if len(s) > 128 || strings.ContainsFunc(s, unicode.IsControl) {
			return false
		}
	}
	if r.Provider != "" && r.Provider != "claude" && r.Provider != "codex" {
		return false
	}
	switch r.Action {
	case "list", "start", "status", "cancel_login", "complete", "activate", "relogin", "remove", "cancel_switch":
		return true
	}
	return false
}

func (cc *clientConn) handleLogins(c Control) {
	if cc.srv.remoteTier {
		cc.replyErrorCode("login management is owner-local only", CodeNotAuthorized)
		return
	}
	if !cc.hasCap(CapLoginsManage) {
		cc.replyErrorCode("login capability was not negotiated", CodeCapabilityRefused)
		return
	}
	if c.LoginsRequest == nil || !validateLoginsReq(*c.LoginsRequest) {
		cc.replyErrorCode("invalid login operation", CodeInvalidField)
		return
	}
	b, ok := cc.srv.d.(LoginsBackend)
	if !ok {
		cc.replyErrorCode("login management unavailable", CodeUnavailable)
		return
	}
	r, err := b.Logins(*c.LoginsRequest)
	if err != nil {
		switch {
		case errors.Is(err, ErrLoginsUnsafeNativeCLI):
			cc.replyErrorCode("provider CLI permissions are unsafe", CodeLoginsUnsafeNativeCLI)
		case errors.Is(err, ErrLoginsStaleRevision):
			cc.replyErrorCode("login list changed; refresh and retry", CodeStaleRevision)
		case errors.Is(err, ErrLoginsUnavailable):
			cc.replyErrorCode("login management unavailable", CodeUnavailable)
		default:
			cc.replyErrorCode("login operation refused", CodeInvalidField)
		}
		return
	}
	_ = cc.writeControl(Control{Op: OpLoginsManage, EndpointID: cc.endpointID, LoginsResult: &r})
}
