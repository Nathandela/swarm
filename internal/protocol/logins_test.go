package protocol

import (
	"errors"
	"fmt"
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

func TestLoginsUnsafeNativeCLIClosedErrorAcrossWire(t *testing.T) {
	s := &loginsStub{stubDaemon: newStubDaemon(), err: fmt.Errorf("private-path/credential-sentinel: %w", ErrLoginsUnsafeNativeCLI)}
	socket := serveContextGuardAPI(t, s)
	client := dialClient(t, socket, []string{CapLoginsManage})
	if _, err := client.Logins(LoginsReq{Action: "start", Provider: "codex", Label: "fixture"}); !errors.Is(err, ErrLoginsUnsafeNativeCLI) || strings.Contains(err.Error(), "sentinel") {
		t.Fatalf("typed client refusal: %v", err)
	}
	raw := rawDial(t, socket)
	hello := raw.hello(Version, []string{CapLoginsManage})
	request := LoginsReq{Action: "relogin", LoginID: "fixture"}
	raw.writeControl(Control{Op: OpLoginsManage, EndpointID: hello.EndpointID, LoginsRequest: &request})
	reply := raw.readControl()
	if reply.Op != OpError || reply.ErrorCode != CodeLoginsUnsafeNativeCLI || reply.Error != "provider CLI permissions are unsafe" {
		t.Fatalf("closed wire refusal=%#v", reply)
	}
	s.err = errors.New(ErrLoginsUnsafeNativeCLI.Error() + " login_unsafe_native_cli private-sentinel")
	if _, err := client.Logins(LoginsReq{Action: "start", Provider: "codex", Label: "fixture"}); err == nil || errors.Is(err, ErrLoginsUnsafeNativeCLI) || strings.Contains(err.Error(), "sentinel") {
		t.Fatalf("unknown backend text promoted/leaked: %v", err)
	}
}
func TestLoginsUnknownWireErrorCannotInjectActionableMessage(t *testing.T) {
	for _, code := range []ErrorCode{"unknown", "prefix_login_unsafe_native_cli", CodeInvalidField} {
		err := loginsReplyError(Control{Op: OpError, ErrorCode: code, Error: ErrLoginsUnsafeNativeCLI.Error() + " raw-secret-path"})
		if err == nil || errors.Is(err, ErrLoginsUnsafeNativeCLI) || strings.Contains(err.Error(), "raw-secret") {
			t.Fatal("unknown code exposed or promoted raw wire error")
		}
	}
}

func TestLoginsDeactivateRequiresProvider(t *testing.T) {
	for _, tc := range []struct {
		req  LoginsReq
		want bool
	}{
		{LoginsReq{Action: "deactivate", Provider: "codex"}, true},
		{LoginsReq{Action: "deactivate", Provider: "claude"}, true},
		{LoginsReq{Action: "deactivate"}, false},
		{LoginsReq{Action: "deactivate", Provider: "opencode"}, false},
		{LoginsReq{Action: "deactivated", Provider: "codex"}, false},
	} {
		if got := validateLoginsReq(tc.req); got != tc.want {
			t.Errorf("%+v: valid=%v", tc.req, got)
		}
	}
	s := &loginsStub{stubDaemon: newStubDaemon()}
	r := rawDial(t, serveContextGuardAPI(t, s))
	hello := r.hello(Version, []string{CapLoginsManage})
	body := LoginsReq{Action: "deactivate"}
	r.writeControl(Control{Op: OpLoginsManage, EndpointID: hello.EndpointID, LoginsRequest: &body})
	if got := r.readControl(); got.ErrorCode != CodeInvalidField || s.calls != 0 {
		t.Fatalf("providerless deactivate reply=%#v calls=%d", got, s.calls)
	}
	body.Provider = "codex"
	r.writeControl(Control{Op: OpLoginsManage, EndpointID: hello.EndpointID, LoginsRequest: &body})
	if got := r.readControl(); got.Op != OpLoginsManage || s.calls != 1 {
		t.Fatalf("deactivate reply=%#v calls=%d", got, s.calls)
	}
	remote := &loginsStub{stubDaemon: newStubDaemon()}
	rr := rawDial(t, serveRemoteAPI(t, remote))
	hello = rr.hello(Version, []string{CapLoginsManage})
	rr.writeControl(Control{Op: OpLoginsManage, EndpointID: hello.EndpointID, LoginsRequest: &body})
	if got := rr.readControl(); got.ErrorCode != CodeNotAuthorized || remote.calls != 0 {
		t.Fatalf("remote deactivate reply=%#v calls=%d", got, remote.calls)
	}
}
