package skeleton

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/claude"
	"github.com/Nathandela/swarm/internal/claudeinput"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/protocol"
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

	client, err := protocol.Dial(sk.SocketPath(), []string{protocol.CapSemanticTarget, protocol.CapSemanticSend})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	target := protocol.NamespacedID(client.EndpointID(), m.ID)
	instance, err := client.SemanticTarget(target)
	if err != nil || instance == "" {
		t.Fatalf("ordinary Claude target lookup: %q %v", instance, err)
	}
	op, err := protocol.NewSemanticOperationID()
	if err != nil {
		t.Fatal(err)
	}
	req := protocol.ComposerSendReq{Session: target, SessionInstance: instance, Text: "first exact owner message"}
	if err := client.SemanticSend(op, req); err != nil {
		t.Fatal(err)
	}
	if err := client.SemanticSend(op, req); err != nil || len(upstream.recordedOrder()) != 2 {
		t.Fatalf("exact protocol replay changed composer effects: %v writes=%v", err, upstream.recordedOrder())
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

func TestClaudeRetiredTargetAndUnsupportedRequestsReportDifferentReasons(t *testing.T) {
	r := newClaudeReceiptRig(t)
	c, err := protocol.Dial(r.sk.SocketPath(), []string{protocol.CapSemanticTarget, protocol.CapNativeRequests})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if instance, err := c.SemanticTarget(r.session); err != nil || instance != r.instance {
		t.Fatalf("live target: %q %v", instance, err)
	}
	if err := r.client.Revoke(t.Context(), r.worker); err != nil {
		t.Fatal(err)
	}
	if instance, err := c.SemanticTarget(r.session); instance != "" || err == nil || !strings.Contains(err.Error(), claudeinput.ReadinessRetired.Error()) {
		t.Fatalf("retired target: %q %v", instance, err)
	}
	for _, req := range []protocol.NativeRequestsReq{
		{Session: r.session},
		{Session: r.session, RequestID: "request", NativeSessionID: "native"},
	} {
		if _, err := c.NativeRequests(req); err == nil || !strings.Contains(err.Error(), protocol.ErrNativeRequestsUnsupported.Error()+" ("+string(protocol.CodeNotImplemented)+")") {
			t.Fatalf("Claude request capability: %v", err)
		}
	}
}
