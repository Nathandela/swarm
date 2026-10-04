package protocol

import (
	"errors"
	"strings"
)

var ErrAccountsUnavailable = errors.New("protocol: accounts unavailable")
var ErrAccountsStaleRevision = errors.New("protocol: stale accounts revision")
var ErrAccountMoveUnmanaged = errors.New("protocol: this discussion predates account pools; its original writers and saved configuration must be verified before moving it")

// AccountsBackend is optional so older daemons/test doubles remain compatible.
type AccountsBackend interface {
	Accounts(AccountsReq) (AccountsReply, error)
}

func (c *Client) Accounts(req AccountsReq) (AccountsReply, error) {
	resp, err := c.request(Control{Op: OpAccountsManage, EndpointID: c.endpointID, AccountsRequest: &req})
	if err != nil {
		return AccountsReply{}, err
	}
	if resp.Op == OpError {
		if resp.ErrorCode == "account_move_unmanaged" {
			return AccountsReply{}, ErrAccountMoveUnmanaged
		}
		if resp.ErrorCode == CodeStaleRevision {
			return AccountsReply{}, ErrAccountsStaleRevision
		}
		if resp.ErrorCode == CodeUnavailable || resp.ErrorCode == CodeCapabilityRefused {
			return AccountsReply{}, ErrAccountsUnavailable
		}
		return AccountsReply{}, errors.New("protocol: account operation refused")
	}
	if resp.Op != OpAccountsManage || resp.AccountsResult == nil {
		return AccountsReply{}, ErrAccountsUnavailable
	}
	return *resp.AccountsResult, nil
}

func validateAccountsReq(req AccountsReq) bool {
	if len(req.Token) > 32<<10 || strings.ContainsAny(req.Token, "\x00\r\n") {
		return false
	}
	if len(req.Label) > 128 || len(req.SourcePath) > 4096 || len(req.AccountID) > 128 || len(req.JobID) > 128 || len(req.SessionID) > 128 {
		return false
	}
	switch req.Action {
	case "list", "start", "status", "cancel", "admit", "import", "update", "enable", "refresh", "retry", "remove", "move":
		return true
	default:
		return false
	}
}

func (cc *clientConn) handleAccounts(c Control) {
	// Refuse the remote tier before inspecting even secret request input.
	if cc.srv.remoteTier {
		cc.replyErrorCode("account management is owner-local only", CodeNotAuthorized)
		return
	}
	if !cc.hasCap(CapAccountsManage) {
		cc.replyErrorCode("account management capability was not negotiated", CodeCapabilityRefused)
		return
	}
	if c.AccountsRequest == nil || !validateAccountsReq(*c.AccountsRequest) {
		cc.replyErrorCode("invalid account operation", CodeInvalidField)
		return
	}
	backend, ok := cc.srv.d.(AccountsBackend)
	if !ok {
		cc.replyErrorCode("account management unavailable", CodeUnavailable)
		return
	}
	result, err := backend.Accounts(*c.AccountsRequest)
	if err != nil {
		if errors.Is(err, ErrAccountMoveUnmanaged) {
			cc.replyErrorCode(ErrAccountMoveUnmanaged.Error(), "account_move_unmanaged")
			return
		}
		if errors.Is(err, ErrAccountsStaleRevision) {
			cc.replyErrorCode("accounts revision is stale", CodeStaleRevision)
			return
		}
		if errors.Is(err, ErrAccountsUnavailable) {
			cc.replyErrorCode("account management unavailable", CodeUnavailable)
			return
		}
		cc.replyErrorCode("account operation refused", CodeInvalidField)
		return
	}
	_ = cc.writeControl(Control{Op: OpAccountsManage, EndpointID: cc.endpointID, AccountsResult: &result})
}
