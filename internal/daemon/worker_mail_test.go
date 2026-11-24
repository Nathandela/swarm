package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/workermail"
)

func TestOrdinaryDispatcherWorkerBindingPrecedesExecAndSurvivesLostReceipt(t *testing.T) {
	cfg := daemonConfig(t)
	childPID := filepath.Join(t.TempDir(), "child.pid")
	cfg.WorkerInstance = func(_ string, pid int, start ...int64) (string, error) {
		if _, err := os.Stat(childPID); !errors.Is(err, os.ErrNotExist) || pid == 0 || len(start) != 1 || start[0] <= 0 {
			t.Fatal("child ran before its exact shim was bound")
		}
		return "confirmed-instance", nil
	}
	d := openDaemon(t, cfg)
	spec := announceSpec(t, filepath.Join(t.TempDir(), "manager.pid"))
	spec.AgentType = "claude"
	spec.CLIIdentity = &persist.CLIIdentity{Path: spec.Argv[0], Version: "2.1.319"}
	spec.Manager = &ManagerLaunchSpec{NativeCLI: true, ConversationID: "discord-teammate-test", Command: cfg.ShimBinary}
	parent, err := d.Launch(spec)
	if err != nil {
		t.Fatal(err)
	}
	child := announceSpec(t, childPID)
	child.SpawnedFrom = parent.ID
	child.SpawnIntent = "delegate"
	child.OwnerLaunchHash = "exact-request"
	child.OperationID = "stable-launch"
	token, _ := readShimLaunchToken(d.sessionDir(parent.ID))
	child.DispatcherProof = workermail.Proof("different-parent", "spawn")
	if _, err = d.Launch(child); err == nil {
		t.Fatal("unrelated caller claimed dispatcher parent")
	}
	child.DispatcherProof = workermail.Proof(token, "spawn")
	child.WorkerParentInputHash = "observed-input-hash"
	child.WorkerParentShimPID, child.WorkerParentShimStart = parent.ShimPID, parent.ShimStartTime
	m, err := d.Launch(child)
	if err != nil {
		t.Fatal(err)
	}
	readPIDFile(t, childPID)
	if m.WorkerInstance != "confirmed-instance" || m.WorkerDiscussion != parent.ManagerConversationID || m.WorkerAttempt == "" || m.WorkerParentInputHash != child.WorkerParentInputHash {
		t.Fatalf("incomplete binding %+v", m)
	}
	again, known, err := d.OwnerLaunchReplay(child.OperationID, child.OwnerLaunchHash)
	if err != nil || !known || again.ID != m.ID || again.WorkerInstance != m.WorkerInstance {
		t.Fatalf("spawn retry %v %v %v", again.ID, known, err)
	}
	own, _ := readShimLaunchToken(d.sessionDir(m.ID))
	req := schema.WorkerMailReq{ID: "test-note", Kind: "needs-input", Action: "message", Text: "which region?", Proof: workermail.Proof(token, "worker")}
	for _, action := range []string{"message", "reply", "receipt"} {
		req.Action = action
		if _, err = d.WorkerMail("ep", m.ID, req, true); err == nil {
			t.Fatalf("sibling proof allowed %s", action)
		}
	}
	req.Action = "message"
	req.Proof = workermail.Proof(own, "worker")
	// Hold the same custody lock a retiring manager holds through its claim.
	unlock, err := d.lockComposerCustody()
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		receipt schema.WorkerMailReceipt
		err     error
	}
	done := make(chan outcome, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		receipt, err := d.WorkerMail("ep", m.ID, req, true)
		done <- outcome{receipt, err}
	}()
	<-started
	select {
	case <-done:
		unlock()
		t.Fatal("worker acknowledged before the retirement decision released custody")
	case <-time.After(50 * time.Millisecond):
	}
	unlock()
	result := <-done
	receipt, err := result.receipt, result.err
	if err != nil || receipt.State != "queued" {
		t.Fatalf("note: %+v %v", receipt, err)
	}
	receipt, err = d.WorkerMail("ep", m.ID, req, false)
	if err != nil || receipt.State != "queued" {
		t.Fatalf("expired replay: %+v %v", receipt, err)
	}
	rows, err := workermail.Pending(cfg.StateDir, m.WorkerDiscussion)
	if err != nil || len(rows) != 1 || rows[0].Instance != m.WorkerInstance || rows[0].ParentInputHash != child.WorkerParentInputHash {
		t.Fatalf("pending %+v, %v", rows, err)
	}

	const fixture = "name,value\nscope,worker\n"
	file := filepath.Join(m.ProviderCwd(), "handoff.csv")
	if err = os.WriteFile(file, []byte(fixture), 0600); err != nil {
		t.Fatal(err)
	}
	files := schema.WorkerMailReq{ID: "file-note", Kind: "complete", Action: "message", Paths: []string{file}, Proof: workermail.Proof(token, "worker")}
	if _, err = d.WorkerMail("ep", m.ID, files, true); err == nil {
		t.Fatal("parent proof allowed worker file handoff")
	}
	files.Proof = req.Proof
	staged, err := d.WorkerMail("ep", m.ID, files, true)
	if err != nil || staged.State != "queued" || len(staged.Files) != 1 || staged.Files[0].Name != "handoff.csv" {
		t.Fatalf("file handoff %+v, %v", staged, err)
	}
	if err = os.Remove(file); err != nil {
		t.Fatal(err)
	}
	files.Action, files.Paths = "receipt", nil
	readback, err := d.WorkerMail("ep", m.ID, files, false)
	if err != nil || len(readback.Files) != 1 || readback.Files[0] != staged.Files[0] {
		t.Fatalf("retained file receipt %+v, %v", readback, err)
	}
	binding := rows[0].Binding
	contents, err := workermail.ReadFile(cfg.StateDir, binding, files.ID, staged.Files[0].ID)
	if err != nil || string(contents) != fixture {
		t.Fatalf("retained file bytes %q, %v", contents, err)
	}
}

func TestWorkerLaunchRepairsUnexecutedReservationAcrossDaemonRestart(t *testing.T) {
	cfg := daemonConfig(t)
	cfg.WorkerInstance = func(string, int, ...int64) (string, error) { return "exact", nil }
	d, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { killSessions(d); _ = d.Close() })
	manager := announceSpec(t, filepath.Join(t.TempDir(), "parent"))
	manager.AgentType = "claude"
	manager.CLIIdentity = &persist.CLIIdentity{Path: manager.Argv[0], Version: "2.1.319"}
	manager.Manager = &ManagerLaunchSpec{NativeCLI: true, ConversationID: "discord-teammate-test", Command: cfg.ShimBinary}
	parent, err := d.Launch(manager)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "first-worker-command")
	child := announceSpec(t, path)
	child.OperationID = "original"
	child.OwnerLaunchHash = "request-hash"
	child.SpawnIntent = "delegate"
	child.SpawnedFrom = parent.ID
	token, _ := readShimLaunchToken(d.sessionDir(parent.ID))
	child.DispatcherProof = workermail.Proof(token, "spawn")
	stop := errors.New("crash after reservation")
	before, err := d.launch(child, func(p launchPhase, _ persist.Meta) error {
		if p == phaseReserved {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) {
		t.Fatalf("inject crash: %v", err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("worker started before reservation committed")
	}
	d.abandon()
	restored := openDaemon(t, cfg)
	after, err := restored.Launch(child)
	if err != nil || before.ID != after.ID || len(restored.List()) != 2 || after.WorkerAttempt != before.WorkerAttempt {
		t.Fatalf("restored %s -> %s, err %v", before.ID, after.ID, err)
	}
	readPIDFile(t, path)
}

func TestWorkerLaunchRepairFailureKeepsInheritedCheckout(t *testing.T) {
	cfg := daemonConfig(t)
	inherited := t.TempDir()
	path := filepath.Join(inherited, "unsaved")
	if err := os.WriteFile(path, []byte("operator work"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.WorkerInstance = func(string, int, ...int64) (string, error) { return "instance", nil }
	cfg.PreLaunch = func(_ string, spec LaunchSpec) (string, error) {
		if spec.RepairWorkerReservation {
			return inherited, nil
		}
		return "", nil
	}
	cfg.FinalizeLaunch = func(_ string, spec LaunchSpec) (LaunchSpec, error) {
		if spec.RepairWorkerReservation {
			return spec, errors.New("injected finalize failure")
		}
		return spec, nil
	}
	cfg.PreDelete = func(m persist.Meta) error {
		if m.AgentCwd == inherited {
			return os.Remove(path)
		}
		return nil
	}
	d, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { killSessions(d); _ = d.Close() })
	m := announceSpec(t, filepath.Join(t.TempDir(), "manager"))
	m.AgentType = "claude"
	m.CLIIdentity = &persist.CLIIdentity{Path: m.Argv[0], Version: "2.1.319"}
	m.Manager = &ManagerLaunchSpec{NativeCLI: true, ConversationID: "discord-teammate-test", Command: cfg.ShimBinary}
	parent, err := d.Launch(m)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := readShimLaunchToken(d.sessionDir(parent.ID))
	spec := announceSpec(t, filepath.Join(t.TempDir(), "child"))
	spec.OperationID = "repair"
	spec.OwnerLaunchHash = "exact"
	spec.SpawnedFrom = parent.ID
	spec.SpawnIntent = "delegate"
	spec.DispatcherProof = workermail.Proof(token, "spawn")
	if _, _, err = d.idem.PrepareWorkerLaunch(spec.OperationID, "reservedworker", spec.OwnerLaunchHash); err != nil {
		t.Fatal(err)
	}
	d.abandon()
	restored := openDaemon(t, cfg)
	_, err = restored.Launch(spec)
	if err == nil || !strings.Contains(err.Error(), "injected finalize failure") {
		t.Fatalf("missing repair fault: %v", err)
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != "operator work" {
		t.Fatalf("existing checkout changed on failed repair: %v", err)
	}
}
