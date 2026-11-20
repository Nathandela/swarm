package daemon

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/managerproof"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/workermail"
)

func TestManagerRetirementRequiresOriginalExitReport(t *testing.T) {
	claimed := time.Now().Add(-time.Minute)
	target := persist.ManagerRetirement{OperationID: "operation", Instance: "instance", NativeSessionID: "native", ShimPID: 123, ShimStartTime: 456, ClaimedAt: claimed}
	meta := persist.Meta{Role: "manager", ConversationID: "native", ShimPID: 123, ShimStartTime: 456, Status: status.Status{Process: status.ProcessExited}, ManagerRetirement: target}
	exit := shim.ExitInfo{FinishedAt: claimed.Add(time.Second)}
	if !managerRetirementProven(meta, target, exit, true, false) {
		t.Fatal("same physical incarnation with an exit after the claim did not prove retirement")
	}
	if managerRetirementProven(meta, target, exit, false, false) || managerRetirementProven(meta, target, exit, true, true) {
		t.Fatal("missing report or live shim was accepted")
	}
	oldExit := shim.ExitInfo{FinishedAt: claimed.Add(-time.Second)}
	if managerRetirementProven(meta, target, oldExit, true, false) {
		t.Fatal("old exit report was accepted")
	}
	meta.ShimStartTime++
	if managerRetirementProven(meta, target, exit, true, false) {
		t.Fatal("replacement shim's exit was accepted")
	}
	meta.ShimStartTime--
	meta.Status.Process = status.ProcessLost
	if managerRetirementProven(meta, target, exit, true, false) {
		t.Fatal("lost status was treated as a verified exit")
	}
}

func TestManagerRetireStopsOriginalShimAndKeepsResumeCustodyAfterRestart(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)
	pidPath := filepath.Join(t.TempDir(), "agent.pid")
	m, err := d.Launch(announceSpec(t, pidPath))
	if err != nil {
		t.Fatal(err)
	}
	var nativePID int
	deadline := time.Now().Add(pollTimeout)
	for nativePID == 0 && time.Now().Before(deadline) {
		raw, _ := os.ReadFile(pidPath)
		nativePID, _ = strconv.Atoi(string(raw))
		if nativePID == 0 {
			time.Sleep(pollStep)
		}
	}
	start, err := processStartTime(nativePID)
	if err != nil || nativePID <= 0 {
		t.Fatalf("no live test agent: %v", err)
	}
	m.Role, m.AgentType, m.ConversationID, m.ManagerConversationID = "manager", "claude", "native", "discussion"
	m.ManagerBridgeSocket, m.ManagerAuthFile = "/private/bridge", "/private/auth"
	if err := d.saveMeta(m); err != nil {
		t.Fatal(err)
	}
	generation := strings.Repeat("a", 32)
	raw, _ := json.Marshal(shim.NativeProcessInfo{
		SchemaVersion: shim.NativeWriterSchemaVersion, Generation: generation, Provider: m.AgentType,
		ShimPID: m.ShimPID, ShimStartTime: m.ShimStartTime, PID: nativePID, StartTime: start,
	})
	if err := os.WriteFile(filepath.Join(d.sessionDir(m.ID), shim.NativeProcessFile), raw, 0600); err != nil {
		t.Fatal(err)
	}
	target := persist.ManagerRetirement{OperationID: "retire-exact", Instance: "instance", NativeSessionID: m.ConversationID,
		NativeGeneration: generation, Generation: 1, Checkpoint: strings.Repeat("b", 64), ShimPID: m.ShimPID, ShimStartTime: m.ShimStartTime}
	stale := target
	stale.ShimStartTime++
	if claimed, err := d.RetireManager(t.Context(), m.ID, m.ManagerConversationID, m.AgentType, stale); claimed || err == nil {
		t.Fatalf("stale shim target dispatched: %v %v", claimed, err)
	}
	// Simulate a worker note accepted after the connector prepared its checkpoint.
	binding := workermail.Binding{Discussion: m.ManagerConversationID, Session: "home/worker", Instance: "worker-inc", Parent: m.ID, Attempt: "job"}
	note, err := workermail.Submit(d.cfg.StateDir, binding, "preclaim", "complete", "evidence", true)
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := d.RetireManager(t.Context(), m.ID, m.ManagerConversationID, m.AgentType, target); claimed || !errors.Is(err, managerproof.ErrStale) {
		t.Fatalf("preclaim note lost to retirement: claimed=%v err=%v", claimed, err)
	}
	if !pidAlive(nativePID) {
		t.Fatal("preclaim note did not keep the manager usable")
	}
	if err := workermail.Acknowledge(d.cfg.StateDir, note, ""); err != nil {
		t.Fatal(err)
	}
	cancelled := target
	cancelled.OperationID = "cancel-before-rpc"
	if err := d.CancelManagerRetire(t.Context(), m.ID, m.ManagerConversationID, m.AgentType, cancelled); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	d = openDaemon(t, cfg)
	m, _ = d.Get(m.ID)
	if claimed, err := d.RetireManager(t.Context(), m.ID, m.ManagerConversationID, m.AgentType, cancelled); claimed || !errors.Is(err, managerproof.ErrStale) {
		t.Fatalf("delayed cancelled request after restart: claimed=%v err=%v", claimed, err)
	}
	// Model the daemon stopping after fsync of the claim and before delivering TERM.
	target.ClaimedAt = time.Now().UTC()
	m.ManagerRetirement = target
	if err := d.saveMeta(m); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	d = openDaemon(t, cfg)
	m, _ = d.Get(m.ID)
	if err := d.CancelManagerRetire(t.Context(), m.ID, m.ManagerConversationID, m.AgentType, target); !errors.Is(err, managerproof.ErrStale) {
		t.Fatalf("cancelled claimed retirement: %v", err)
	}
	// Notes arriving after claim must survive while the original signal is re-driven.
	if _, err := workermail.Submit(d.cfg.StateDir, binding, "postclaim", "needs-input", "question", true); err != nil {
		t.Fatal(err)
	}
	if got := d.ManagerRetirementStatus(m, target); got != "uncertain" {
		t.Fatalf("durable claim without effect reported %q", got)
	}
	if claimed, err := d.RetireManager(t.Context(), m.ID, m.ManagerConversationID, m.AgentType, target); !claimed || err != nil {
		t.Fatalf("exact retirement not dispatched: %v %v", claimed, err)
	}
	deadline = time.Now().Add(pollTimeout)
	for time.Now().Before(deadline) {
		m, _ = d.Get(m.ID)
		if d.ManagerRetirementStatus(m, m.ManagerRetirement) == "retired" {
			break
		}
		time.Sleep(pollStep)
	}
	if d.ManagerRetirementStatus(m, m.ManagerRetirement) != "retired" || pidAlive(nativePID) {
		t.Fatal("retirement did not confirm both original shim exit and contained agent exit")
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	d = openDaemon(t, cfg)
	if pending, err := workermail.Pending(d.cfg.StateDir, binding.Discussion); err != nil || len(pending) != 1 || pending[0].ID != "postclaim" {
		t.Fatalf("lost postclaim note after restart: %+v %v", pending, err)
	}
	recovered, _ := d.Get(m.ID)
	if d.ManagerRetirementStatus(recovered, m.ManagerRetirement) != "retired" {
		t.Fatal("lost response could not be reconciled after daemon restart")
	}
	spec := LaunchSpec{ResumedFrom: m.ID, ConversationID: m.ConversationID, OperationID: "wake-one", OwnerLaunchHash: "hash",
		ResumeSource: &ResumeSourceFence{Instance: target.Instance, ShimPID: m.ShimPID, ShimStartTime: m.ShimStartTime},
		Manager:      &ManagerLaunchSpec{RetireOperationID: target.OperationID, ConversationID: m.ManagerConversationID, BridgeSocket: m.ManagerBridgeSocket, AuthFile: m.ManagerAuthFile}}
	if err := d.BindParkedManagerResume(spec); err != nil {
		t.Fatal(err)
	}
	if err := d.BindParkedManagerResume(spec); err != nil {
		t.Fatalf("same resume operation failed to reconcile: %v", err)
	}
	exact := spec
	spec.OwnerLaunchHash = "changed-request"
	if err := d.BindParkedManagerResume(spec); !errors.Is(err, ErrResumeSourceChanged) {
		t.Fatalf("the same launch ID accepted a changed request: %v", err)
	}
	spec = exact
	spec.OperationID = "competing-wake"
	if err := d.BindParkedManagerResume(spec); !errors.Is(err, ErrResumeSourceChanged) {
		t.Fatalf("stale retirement was available to a second operation: %v", err)
	}
}
