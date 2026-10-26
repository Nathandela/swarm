package skeleton

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/claude"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/protocol"
)

func TestClaudeOriginRequiresNativeIngressWithoutTypingOrBegin(t *testing.T) {
	sk := assemble(t)
	sk.setAdapterForTest(func(string) (adapter.Adapter, bool) { return claude.New(), true })
	script := filepath.Join(t.TempDir(), "agent.txt")
	if err := os.WriteFile(script, []byte("ask got:\nidle 600s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	meta, err := sk.core.Launch(daemon.LaunchSpec{AgentType: "claude", Argv: []string{fakeAgentBin, script}, Cwd: t.TempDir(), ClientEnv: []string{"PATH=" + os.Getenv("PATH")}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(meta.ShimPID, syscall.SIGTERM) })
	client := dialClient(t, sk)
	session := protocol.NamespacedID(sk.api.endpointID, meta.ID)
	att, err := client.Attach(session)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = att.Detach() }()
	began := false
	code, err := sk.api.SemanticSendTransactional(sk.api.endpointID, "native-unproven", protocol.ComposerSendReq{Session: session, Text: "external context", Origin: &protocol.MessageOrigin{Kind: "external", InstallationID: "install", RequestID: "request", ActorID: "actor"}}, func() error { began = true; return nil })
	if err == nil || code != protocol.CodeStructuredUnsupported {
		t.Fatalf("unproven native input: code=%s error=%v", code, err)
	}
	if began {
		t.Fatal("unproven native sink crossed execution boundary")
	}
}
