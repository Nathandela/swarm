package protocol

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

type accountsStub struct {
	*stubDaemon
	calls int
	err   error
}

func (s *accountsStub) Accounts(AccountsReq) (AccountsReply, error) {
	s.calls++
	return AccountsReply{Revision: 7, Enabled: map[string]bool{"codex": false}}, s.err
}

func TestAccountsOwnerCapabilityCASAndRedaction(t *testing.T) {
	s := &accountsStub{stubDaemon: newStubDaemon()}
	c := dialClient(t, serveContextGuardAPI(t, s), []string{CapAccountsManage})
	got, err := c.Accounts(AccountsReq{Action: "list"})
	if err != nil || got.Revision != 7 || s.calls != 1 {
		t.Fatalf("snapshot=%#v error=%v calls=%d", got, err, s.calls)
	}
	s.err = ErrAccountsStaleRevision
	if _, err = c.Accounts(AccountsReq{Action: "enable"}); !errors.Is(err, ErrAccountsStaleRevision) {
		t.Fatalf("CAS error=%v", err)
	}
	s.err = errors.New("secret-token-sentinel")
	if _, err = c.Accounts(AccountsReq{Action: "list"}); err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("backend error must be redacted: %v", err)
	}
}

func TestAccountsRefusalOrderingAndOldDaemon(t *testing.T) {
	t.Run("remote first", func(t *testing.T) {
		s := &accountsStub{stubDaemon: newStubDaemon()}
		r := rawDial(t, serveRemoteAPI(t, s))
		hello := r.hello(Version, []string{CapAccountsManage})
		if slices.Contains(hello.Capabilities, CapAccountsManage) {
			t.Fatal("remote offered account capability")
		}
		r.writeControl(Control{Op: OpAccountsManage, EndpointID: hello.EndpointID})
		got := r.readControl()
		if got.ErrorCode != CodeNotAuthorized || s.calls != 0 {
			t.Fatalf("remote reply=%#v calls=%d", got, s.calls)
		}
	})
	t.Run("capability then body", func(t *testing.T) {
		s := &accountsStub{stubDaemon: newStubDaemon()}
		r := rawDial(t, serveContextGuardAPI(t, s))
		hello := r.hello(Version, nil)
		r.writeControl(Control{Op: OpAccountsManage, EndpointID: hello.EndpointID})
		if got := r.readControl(); got.ErrorCode != CodeCapabilityRefused || s.calls != 0 {
			t.Fatalf("reply=%#v calls=%d", got, s.calls)
		}
	})
	t.Run("absent backend never advertised", func(t *testing.T) {
		r := rawDial(t, serveContextGuardAPI(t, newStubDaemon()))
		hello := r.hello(Version, []string{CapAccountsManage})
		if slices.Contains(hello.Capabilities, CapAccountsManage) {
			t.Fatal("unsupported capability advertised")
		}
	})
	t.Run("bounded secret validation", func(t *testing.T) {
		s := &accountsStub{stubDaemon: newStubDaemon()}
		r := rawDial(t, serveContextGuardAPI(t, s))
		hello := r.hello(Version, []string{CapAccountsManage})
		body := AccountsReq{Action: "start", Token: "secret\nsentinel"}
		r.writeControl(Control{Op: OpAccountsManage, EndpointID: hello.EndpointID, AccountsRequest: &body})
		got := r.readControl()
		if got.ErrorCode != CodeInvalidField || s.calls != 0 || strings.Contains(got.Error, "sentinel") {
			t.Fatalf("reply=%#v calls=%d", got, s.calls)
		}
	})
}
