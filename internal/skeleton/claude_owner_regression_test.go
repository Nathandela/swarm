package skeleton

import (
	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/claude"
	"github.com/Nathandela/swarm/internal/claudeinput"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/protocol"
	"os"
	"path/filepath"
	"testing"
)

func TestReviewOrdinaryClaudeOwnerSendRemainsAvailable(t *testing.T) {
	sk := assemble(t)
	sk.setAdapterForTest(func(string) (adapter.Adapter, bool) { return claude.New(), true })
	m, err := sk.Core().Launch(daemon.LaunchSpec{AgentType: "claude", Argv: []string{fakeAgentBin, mustScript(t, "idle 60s\n")}, Cwd: t.TempDir(), ClientEnv: []string{"PATH=" + os.Getenv("PATH")}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sk.Core().Kill(m.ID) })
	upstream := newOrderedComposerUpstream()
	sk.api.tap = newTapManager(func(string) (protocol.SessionStream, error) { return upstream, nil })
	began := false
	code, err := sk.api.SemanticSendTransactional(sk.api.endpointID, "ordinary-owner", protocol.ComposerSendReq{Session: protocol.NamespacedID(sk.api.endpointID, m.ID), Text: "ordinary owner input"}, func() error { began = true; return nil })
	if err != nil || code != "" || !began || len(upstream.recordedOrder()) != 1 {
		t.Fatalf("ordinary owner input regressed: code=%s error=%v begin=%t writes=%v", code, err, began, upstream.recordedOrder())
	}
}

func TestClaudeEnrolledOwnerNeverFallsBackWhenProofIsLost(t *testing.T) {
	r := newClaudeReceiptRig(t)
	if err := os.Remove(filepath.Join(r.sk.stateDir, r.local, claudeinput.DescriptorFile)); err != nil {
		t.Fatal(err)
	}
	began := false
	code, err := r.sk.api.SemanticSendTransactional(r.sk.api.endpointID, "owner-lost-proof", protocol.ComposerSendReq{Session: r.session, SessionInstance: r.instance, Text: "must not type"}, func() error { began = true; return nil })
	if err == nil || code != protocol.CodeStructuredUnsupported || began {
		t.Fatalf("lost enrollment proof bypass: code=%s err=%v began=%t", code, err, began)
	}
}

func TestClaudeOwnerUnknownEnrollmentNeverFallsBack(t *testing.T) {
	r := newClaudeReceiptRig(t)
	if err := os.Remove(filepath.Join(r.sk.stateDir, r.local, "shim-launch.json")); err != nil {
		t.Fatal(err)
	}
	began := false
	code, err := r.sk.api.SemanticSendTransactional(r.sk.api.endpointID, "owner-unknown", protocol.ComposerSendReq{Session: r.session, Text: "must not type"}, func() error { began = true; return nil })
	if err == nil || code != protocol.CodeStructuredUnsupported || began {
		t.Fatalf("unknown enrollment bypass: code=%s err=%v began=%t", code, err, began)
	}
}
