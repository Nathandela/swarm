package skeleton

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/claude"
	"github.com/Nathandela/swarm/internal/claudeinput"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/idempotency"
	"github.com/Nathandela/swarm/internal/persist"
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

type claudeReceiptRig struct {
	sk                       *Daemon
	local, session, instance string
	broker                   *claudeinput.Broker
	client                   *claudeinput.Client
	worker                   claudeinput.Worker
}

func newClaudeReceiptRig(t *testing.T) *claudeReceiptRig {
	t.Helper()
	sk := assemble(t)
	script := filepath.Join(t.TempDir(), "agent.txt")
	if err := os.WriteFile(script, []byte("idle 600s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stamp, err := persist.CLIFingerprint(fakeAgentBin)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := sk.core.Launch(daemon.LaunchSpec{AgentType: "claude", Argv: []string{fakeAgentBin, script}, Cwd: t.TempDir(), ClientEnv: []string{"PATH=" + os.Getenv("PATH")}, CLIIdentity: &persist.CLIIdentity{Path: fakeAgentBin, Version: "2.1.296", Fingerprint: stamp}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(meta.ShimPID, syscall.SIGTERM) })
	b, err := claudeinput.Start(filepath.Join(sk.stateDir, meta.ID), meta.ID, "2.1.296", meta.ShimPID, meta.ShimStartTime)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	client := claudeinput.NewClient(b.Descriptor())
	worker := claudeinput.Worker{NativeSessionID: "native-fixture", Generation: "worker-generation", Version: "2.1.296"}
	if _, err := client.Exchange(context.Background(), worker, "", "", ""); err != nil {
		t.Fatal(err)
	}
	instance, _ := sk.sessionInstance(meta.ID)
	return &claudeReceiptRig{sk: sk, local: meta.ID, session: protocol.NamespacedID(sk.api.endpointID, meta.ID), instance: instance, broker: b, client: client, worker: worker}
}

func (r *claudeReceiptRig) prepare(t *testing.T, id, text string) composerOriginIntent {
	t.Helper()
	if _, _, err := r.sk.core.ClaimComposerOperation(id, protocol.OpSemanticSend, r.local, r.instance, "hash:"+id); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(text))
	intent := composerOriginIntent{Version: 1, Origin: &protocol.MessageOrigin{Kind: "external", InstallationID: "install", RequestID: id, ActorID: "actor"}, ClaudeInput: &claudeInputIntent{Generation: r.broker.Descriptor().Generation, Worker: r.worker, Hash: hex.EncodeToString(hash[:])}}
	raw, _ := json.Marshal(intent)
	if err := r.sk.core.BindSemanticRequest(id, raw); err != nil {
		t.Fatal(err)
	}
	if err := r.sk.core.BeginComposerOperation(id); err != nil {
		t.Fatal(err)
	}
	if _, err := r.client.Submit(context.Background(), r.worker, id, text); err != nil {
		t.Fatal(err)
	}
	msg, err := r.client.Exchange(context.Background(), r.worker, "", "", "")
	if err != nil || msg == nil || msg.OperationID != id {
		t.Fatalf("native claim: %+v %v", msg, err)
	}
	return intent
}

func TestClaudeNativeRejectedUnknownSettlesFailureAndUnfencesNextMessage(t *testing.T) {
	r := newClaudeReceiptRig(t)
	const prior = "native-rejected"
	r.prepare(t, prior, "prior message")
	if _, err := r.client.Exchange(context.Background(), r.worker, prior, "rejected", "native queue dropped request"); err != nil {
		t.Fatal(err)
	}
	receipt, err := r.sk.semanticOperation(r.local, prior)
	if err != nil || receipt.State != "rejected" {
		t.Fatalf("native rejection: %+v %v", receipt, err)
	}
	record, _ := r.sk.core.ComposerOperation(prior)
	if record.Phase != idempotency.PhaseFailed {
		t.Fatalf("rejected native input remains %s and fences later sends", record.Phase)
	}
	if phase, _, err := r.sk.core.ClaimComposerOperation(prior, protocol.OpSemanticSend, r.local, r.instance, "hash:"+prior); err != nil || phase != "failed" {
		t.Fatalf("replay forgot rejection: %s %v", phase, err)
	}
	const next = "native-after-rejection"
	if _, _, err := r.sk.core.ClaimComposerOperation(next, protocol.OpSemanticSend, r.local, r.instance, "hash:"+next); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			msg, err := r.client.Exchange(context.Background(), r.worker, "", "", "")
			if err != nil {
				return
			}
			if msg != nil {
				_, _ = r.client.Exchange(context.Background(), r.worker, msg.OperationID, "submitted", "")
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	lane := r.sk.composerLaneFor(r.local)
	barrier := lane.enter()
	defer lane.leave()
	code, err := r.sk.sendClaudeNative(r.local, next, protocol.ComposerSendReq{Session: r.session, SessionInstance: r.instance, Text: "next message", Origin: &protocol.MessageOrigin{Kind: "external", InstallationID: "install", RequestID: next, ActorID: "actor"}}, func() error { return r.sk.core.BeginComposerOperation(next) }, lane, barrier)
	if err != nil || code != "" {
		t.Fatalf("next native message remained fenced: %s %v", code, err)
	}
	<-done
}

func TestClaudeNativeUnretainedReceiptCannotCommitSuccess(t *testing.T) {
	r := newClaudeReceiptRig(t)
	const id = "native-unretained"
	r.prepare(t, id, "message")
	if _, err := r.client.Exchange(context.Background(), r.worker, id, "submitted", ""); err != nil {
		t.Fatal(err)
	}
	wrong := claudeinput.Message{OperationID: id, Hash: "conflicting retained request", State: "executing", Worker: r.worker}
	raw, _ := json.Marshal(wrong)
	if err := r.sk.core.ObserveSemanticReceipt(id, raw); err != nil {
		t.Fatal(err)
	}
	receipt, err := r.sk.semanticOperation(r.local, id)
	record, _ := r.sk.core.ComposerOperation(id)
	if err != nil || receipt.State != "uncertain" || record.Phase != idempotency.PhaseExecuting {
		t.Fatalf("refused evidence still became success: receipt=%+v phase=%s error=%v", receipt, record.Phase, err)
	}
}

func TestClaudeNativeSenderUsesStrongerConcurrentRejectedReceipt(t *testing.T) {
	r := newClaudeReceiptRig(t)
	client, err := protocol.Dial(r.sk.SocketPath(), []string{protocol.CapSemanticSend, protocol.CapMessageOrigin})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	id, err := protocol.NewSemanticOperationID()
	if err != nil {
		t.Fatal(err)
	}
	observed := make(chan error, 1)
	hook := func() {
		testHookClaudeNativeReceiptObserved.Store(nil)
		if _, err := r.client.Exchange(context.Background(), r.worker, id, "rejected", "native queue dropped request"); err != nil {
			observed <- err
			return
		}
		receipt, err := r.sk.semanticOperation(r.local, id)
		if err == nil && receipt.State != "rejected" {
			err = fmt.Errorf("stronger rejection became %s", receipt.State)
		}
		observed <- err
	}
	testHookClaudeNativeReceiptObserved.Store(&hook)
	t.Cleanup(func() { testHookClaudeNativeReceiptObserved.Store(nil) })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			msg, err := r.client.Exchange(ctx, r.worker, "", "", "")
			if err != nil {
				return
			}
			if msg != nil {
				_, _ = r.client.Exchange(ctx, r.worker, msg.OperationID, "submitted", "")
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	req := protocol.ComposerSendReq{Session: r.session, SessionInstance: r.instance, Text: "message", Origin: &protocol.MessageOrigin{Kind: "external", InstallationID: "install", RequestID: id, ActorID: "actor"}}
	if err := client.SemanticSend(id, req); !errors.Is(err, protocol.ErrSemanticSendRejected) {
		t.Fatalf("sender ignored stronger retained rejection: %v", err)
	}
	if err := <-observed; err != nil {
		t.Fatal(err)
	}
	<-done
	if err := client.SemanticSend(id, req); !errors.Is(err, protocol.ErrSemanticSendRejected) {
		t.Fatalf("replay forgot native rejection: %v", err)
	}
}
