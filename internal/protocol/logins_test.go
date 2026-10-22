package protocol

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

type loginsStub struct {
	*stubDaemon
	calls int
	err   error
}

func (s *loginsStub) Logins(LoginsReq) (LoginsReply, error) {
	s.calls++
	return LoginsReply{Revision: 7, Providers: []ProviderLoginView{{Provider: "codex", State: "native"}}}, s.err
}

func TestLoginsOwnerCapabilityCASAndRedaction(t *testing.T) {
	s := &loginsStub{stubDaemon: newStubDaemon()}
	c := dialClient(t, serveContextGuardAPI(t, s), []string{CapLoginsManage})
	got, err := c.Logins(LoginsReq{Action: "list"})
	if err != nil || got.Revision != 7 || s.calls != 1 {
		t.Fatalf("snapshot=%#v error=%v calls=%d", got, err, s.calls)
	}
	s.err = ErrLoginsStaleRevision
	if _, err = c.Logins(LoginsReq{Action: "activate"}); !errors.Is(err, ErrLoginsStaleRevision) {
		t.Fatalf("CAS error=%v", err)
	}
	s.err = errors.New("secret-token-sentinel")
	if _, err = c.Logins(LoginsReq{Action: "list"}); err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("backend error must be redacted: %v", err)
	}
}

func TestLoginsRefusalOrderingAndOldDaemon(t *testing.T) {
	t.Run("remote first", func(t *testing.T) {
		s := &loginsStub{stubDaemon: newStubDaemon()}
		r := rawDial(t, serveRemoteAPI(t, s))
		hello := r.hello(Version, []string{CapLoginsManage})
		if slices.Contains(hello.Capabilities, CapLoginsManage) {
			t.Fatal("remote offered account capability")
		}
		r.writeControl(Control{Op: OpLoginsManage, EndpointID: hello.EndpointID})
		got := r.readControl()
		if got.ErrorCode != CodeNotAuthorized || s.calls != 0 {
			t.Fatalf("remote reply=%#v calls=%d", got, s.calls)
		}
	})
	t.Run("capability then body", func(t *testing.T) {
		s := &loginsStub{stubDaemon: newStubDaemon()}
		r := rawDial(t, serveContextGuardAPI(t, s))
		hello := r.hello(Version, nil)
		r.writeControl(Control{Op: OpLoginsManage, EndpointID: hello.EndpointID})
		if got := r.readControl(); got.ErrorCode != CodeCapabilityRefused || s.calls != 0 {
			t.Fatalf("reply=%#v calls=%d", got, s.calls)
		}
	})
	t.Run("absent backend never advertised", func(t *testing.T) {
		r := rawDial(t, serveContextGuardAPI(t, newStubDaemon()))
		hello := r.hello(Version, []string{CapLoginsManage})
		if slices.Contains(hello.Capabilities, CapLoginsManage) {
			t.Fatal("unsupported capability advertised")
		}
	})
	t.Run("bounded secret validation", func(t *testing.T) {
		s := &loginsStub{stubDaemon: newStubDaemon()}
		r := rawDial(t, serveContextGuardAPI(t, s))
		hello := r.hello(Version, []string{CapLoginsManage})
		body := LoginsReq{Action: "start", Label: "secret\nsentinel"}
		r.writeControl(Control{Op: OpLoginsManage, EndpointID: hello.EndpointID, LoginsRequest: &body})
		got := r.readControl()
		if got.ErrorCode != CodeInvalidField || s.calls != 0 || strings.Contains(got.Error, "sentinel") {
			t.Fatalf("reply=%#v calls=%d", got, s.calls)
		}
	})
}
