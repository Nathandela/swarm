package protocol

import (
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type managerLaunchStub struct{ *ownerLaunchStub }

func (*managerLaunchStub) ManagerLaunchSupported() bool { return true }

func privateManagerRequest(t *testing.T) *ManagerLaunchReq {
	t.Helper()
	dir, err := os.MkdirTemp("", "manager-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	physical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(physical, "bridge.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if err := os.Chmod(socket, 0600); err != nil {
		t.Fatal(err)
	}
	auth := filepath.Join(physical, "auth")
	if err := os.WriteFile(auth, []byte("dummy-auth-reference"), 0600); err != nil {
		t.Fatal(err)
	}
	return &ManagerLaunchReq{ConversationID: "manager-conversation", BridgeSocket: socket, AuthFile: auth}
}

func TestManagerLaunchRequiresDedicatedCapabilityAndOwnerRoute(t *testing.T) {
	for _, remote := range []bool{false, true} {
		backend := &managerLaunchStub{&ownerLaunchStub{durableComposerStub: newDurableComposerStub()}}
		socket := serveOwnerAPI(t, backend)
		caps := []string{CapOwnerLaunch}
		if remote {
			socket = serveRemoteAPI(t, backend)
			caps = append(caps, CapRemoteGateway, CapManagerLaunch)
		}
		raw := rawDial(t, socket)
		hello := raw.hello(Version, caps)
		if slices.Contains(hello.Capabilities, CapManagerLaunch) {
			t.Fatal("manager launch offered without owner capability negotiation")
		}
		id, err := NewLaunchOperationID()
		if err != nil {
			t.Fatal(err)
		}
		for _, op := range []string{OpLaunch, OpOwnerLaunch} {
			raw.writeControl(Control{Op: op, EndpointID: hello.EndpointID, OperationID: id, Launch: &LaunchReq{Agent: "claude", Cwd: t.TempDir(), Cols: 80, Rows: 24, Manager: privateManagerRequest(t)}})
			if got := raw.readControl(); got.Op != OpError {
				t.Fatalf("unsupported manager route accepted: %+v", got)
			}
		}
	}
}

func TestManagerLaunchValidatesPrivateReferencesAndExactPayload(t *testing.T) {
	req := LaunchReq{Agent: "claude", Manager: privateManagerRequest(t)}
	if err := validateManagerLaunch(req); err != nil {
		t.Fatal(err)
	}
	original := ownerLaunchHash(&req)
	req.Manager.ConversationID = "different"
	if ownerLaunchHash(&req) == original {
		t.Fatal("manager binding not covered by owner request hash")
	}
	if err := os.Chmod(req.Manager.AuthFile, 0644); err != nil {
		t.Fatal(err)
	}
	if err := validateManagerLaunch(req); err == nil {
		t.Fatal("public auth file accepted")
	}
	if err := os.Chmod(req.Manager.AuthFile, 0600); err != nil {
		t.Fatal(err)
	}
	link := req.Manager.AuthFile + "-alias"
	if err := os.Symlink(req.Manager.AuthFile, link); err != nil {
		t.Fatal(err)
	}
	req.Manager.AuthFile = link
	if err := validateManagerLaunch(req); err == nil {
		t.Fatal("symlink auth file accepted")
	}
}

func TestManagerLaunchPreservesOnlyItsNativeClaudeSelector(t *testing.T) {
	req := LaunchReq{Agent: "claude", Manager: privateManagerRequest(t), Env: []string{"HOME=/native/home", "CLAUDE_CONFIG_DIR=/native/profile", "UNRELATED_SECRET=not-retained"}}
	spec := daemonLaunchSpec(&req, false, "operation")
	if spec.Manager.ClaudeConfigDir == nil || *spec.Manager.ClaudeConfigDir != "/native/profile" {
		t.Fatal("selected native origin lost")
	}
	for _, entry := range spec.ClientEnv {
		if strings.Contains(entry, "not-retained") {
			t.Fatal("manager retained unrelated credentials")
		}
	}
	req.Manager = nil
	for _, entry := range daemonLaunchSpec(&req, false, "ordinary").ClientEnv {
		if strings.HasPrefix(entry, "CLAUDE_CONFIG_DIR=") {
			t.Fatal("manager selector widened ordinary worker environment")
		}
	}
}

func TestManagerLaunchRefusesUnsupportedNativeAccountBeforeFiltering(t *testing.T) {
	for _, test := range []struct {
		agent   string
		env     []string
		allowed bool
	}{
		{"claude", []string{"ANTHROPIC_API_KEY=synthetic-secret"}, false},
		{"claude", []string{"ANTHROPIC_AUTH_TOKEN=false"}, false},
		{"claude", []string{"CLAUDE_CODE_USE_VERTEX=true"}, false},
		{"claude", []string{"CLAUDE_CODE_USE_BEDROCK=0"}, true},
		{"claude", []string{"CLAUDE_CODE_USE_VERTEX=false"}, true},
		{"claude", []string{"CLAUDE_CODE_USE_VERTEX=maybe"}, false},
		{"claude", []string{"CLAUDE_CODE_USE_VERTEX=false", "CLAUDE_CODE_USE_VERTEX=true"}, false},
		{"codex", []string{"OPENAI_API_KEY=synthetic-secret"}, false},
		{"codex", []string{"CODEX_HOME=/private/other-profile"}, false},
	} {
		t.Run(test.agent+"/"+test.env[0], func(t *testing.T) {
			req := LaunchReq{Agent: test.agent, Manager: privateManagerRequest(t), Env: test.env}
			err := validateManagerLaunch(req)
			if (err == nil) != test.allowed {
				t.Fatalf("manager account selection allowed=%v: %v", test.allowed, err)
			}
			if err != nil && strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatal("credential entered refusal text")
			}
		})
	}
}
