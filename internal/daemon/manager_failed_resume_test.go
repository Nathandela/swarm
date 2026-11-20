package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/managerproof"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/status"
)

func TestFailedManagerResumeRequiresExactExitAndVerifiedEmptyInputLedger(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	code := 1
	meta := persist.Meta{ID: "attemptaaaaaaaaa", Role: "manager", ResumedFrom: "parkedaaaaaaaaaa", AgentType: "codex", ConversationID: "native", ManagerConversationID: "conversation",
		Status: status.Status{Process: status.ProcessExited}, ExitCode: &code,
		CreatedAt: time.Now().Add(-time.Second), ShimPID: 1 << 30, ShimStartTime: 1}
	dir := d.sessionDir(meta.ID)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	marker, err := managerproof.InitializeReplay(dir, meta.ID, meta.ManagerConversationID)
	if err != nil {
		t.Fatal(err)
	}
	meta.ManagerReplayID = marker
	if d.FailedManagerStartupInstance(meta) != "" {
		t.Fatal("missing exit proof")
	}
	if err := os.WriteFile(filepath.Join(dir, shim.ExitFile), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if d.FailedManagerStartupInstance(meta) != "" {
		t.Fatal("malformed exit proof")
	}
	writeExitFile(t, dir, 2, "")
	if d.FailedManagerStartupInstance(meta) != "" {
		t.Fatal("foreign exit code")
	}
	writeExitFile(t, dir, 1, "")
	if d.FailedManagerStartupInstance(meta) != "" {
		t.Fatal("missing physical instance")
	}
	// A frontend may exit before a capabilities file can ever be authored.
	if err := os.WriteFile(filepath.Join(dir, "session-instance"), []byte("attempt-instance 1073741824 1"), 0600); err != nil {
		t.Fatal(err)
	}
	if d.FailedManagerStartupInstance(meta) != "attempt-instance" {
		t.Fatal("failed startup was not proved")
	}
	// A different operation or a grandchild cannot spend the original retirement.
	parent := meta
	parent.ID, parent.ResumedFrom = meta.ResumedFrom, ""
	parent.ManagerResumeOperationID, parent.ManagerResumeRequestHash = "wake", "original-request"
	parent.ManagerRetirement = persist.ManagerRetirement{OperationID: "retire", Instance: "parked-instance", NativeSessionID: parent.ConversationID,
		ShimPID: parent.ShimPID, ShimStartTime: parent.ShimStartTime, ClaimedAt: meta.CreatedAt}
	if err := d.saveMeta(parent); err != nil {
		t.Fatal(err)
	}
	writeExitFile(t, d.sessionDir(parent.ID), 1, "")
	spec := LaunchSpec{Manager: &ManagerLaunchSpec{RetireOperationID: "retire", ConversationID: meta.ManagerConversationID},
		ConversationID: meta.ConversationID, ResumeSource: &ResumeSourceFence{Instance: "attempt-instance", ShimPID: meta.ShimPID, ShimStartTime: meta.ShimStartTime}}
	if d.ParkedManagerResumeAllowed(meta, spec) {
		t.Fatal("uncorroborated child launch")
	}
	if _, err := d.RecordOwnerLaunch(LaunchSpec{OperationID: parent.ManagerResumeOperationID, OwnerLaunchHash: parent.ManagerResumeRequestHash}, meta); err != nil {
		t.Fatal(err)
	}
	if !d.ParkedManagerResumeAllowed(meta, spec) {
		t.Fatal("exact failed child lost its retry")
	}
	if err := d.saveMeta(meta); err != nil {
		t.Fatal(err)
	}
	spec.ResumedFrom, spec.OperationID, spec.OwnerLaunchHash = meta.ID, "bounded-retry", "retry-request"
	if err := d.BindParkedManagerResume(spec); err != nil {
		t.Fatal("valid retry was refused", err)
	}
	if err := d.BindParkedManagerResume(spec); err != nil {
		t.Fatal("same retry lost its reservation", err)
	}
	competing := spec
	competing.OperationID = "parallel-retry"
	if err := d.BindParkedManagerResume(competing); !errors.Is(err, ErrResumeSourceChanged) {
		t.Fatal("failed successor was consumed twice", err)
	}
	unbound := meta
	unbound.ID = "unrelatedaaaaaaa"
	if d.ParkedManagerResumeAllowed(unbound, spec) {
		t.Fatal("foreign child was accepted")
	}
	replaced := spec
	replaced.ResumeSource = &ResumeSourceFence{Instance: "attempt-instance", ShimPID: meta.ShimPID, ShimStartTime: meta.ShimStartTime + 1}
	if d.ParkedManagerResumeAllowed(meta, replaced) {
		t.Fatal("replaced physical source was accepted")
	}
	other := meta
	other.ManagerConversationID = "unrelated-manager"
	if d.FailedManagerStartupInstance(other) != "" {
		t.Fatal("unrelated manager ledger was accepted")
	}
	replay, err := managerproof.OpenReplay(dir, meta.ID, meta.ManagerConversationID, marker)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = replay.Close() })
	if d.FailedManagerStartupInstance(meta) != "" {
		t.Fatal("active proof observer was ignored")
	}
	if err := replay.Claim("observation"); err != nil {
		t.Fatal(err)
	}
	binding := managerproof.Binding{Session: meta.ID, SessionInstance: "instance", ConversationID: meta.ManagerConversationID,
		Provider: "codex", NativeSessionID: "native", NativeGeneration: "generation", ObservationGeneration: "observation", LoginEpoch: "login"}
	if err := replay.Reserve(binding, managerproof.ReplayKey{Kind: managerproof.ReplayNonce, ID: "input", Owner: managerproof.ReplayFingerprint("input")}); err != nil {
		t.Fatal(err)
	}
	if err := replay.Close(); err != nil {
		t.Fatal(err)
	}
	if d.FailedManagerStartupInstance(meta) != "" {
		t.Fatal("a durably admitted input was ignored")
	}
}
