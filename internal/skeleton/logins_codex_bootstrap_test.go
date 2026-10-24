package skeleton

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/status"
)

func TestCodexBootstrapFreshAssemblyRefusalIsNonceBound(t *testing.T) {
	for _, fresh := range []bool{true, false} {
		spec := daemon.LaunchSpec{AgentType: logins.ProviderCodex, OperationID: "fresh-bootstrap", BootstrapFirstAttempt: fresh, Options: map[string]string{protocol.OptionResumeRelocate: "true"}}
		_, err := (&coreAPI{}).Launch(spec)
		if err == nil || daemon.BootstrapLaunchDidNotStart(err, spec.OperationID) != fresh {
			t.Fatal("assembly refusal lost fresh-call authority or authorized a loaded claim")
		}
	}
}

func TestCodexBootstrapExternalLaunchReleasesAdmissionBeforeCore(t *testing.T) {
	f := newLoginFixture(t)
	enableCodexNativeGrantUsage(t, f)
	bin := nativeCodexVersionFixture(t)
	gate := &f.m.admission[loginProviderIndex(logins.ProviderCodex)]
	available := false
	core, err := daemon.Open(daemon.Config{StateDir: f.m.store.StateRoot(), SocketPath: filepath.Join(f.m.store.StateRoot(), "test.sock"), LockPath: filepath.Join(f.m.store.StateRoot(), "test.lock"), LogPath: filepath.Join(f.m.store.StateRoot(), "test.log"), MaxSessions: 4, FinalizeLaunch: func(_ string, spec daemon.LaunchSpec) (daemon.LaunchSpec, error) {
		available = gate.TryLock()
		if available {
			gate.Unlock()
		}
		return spec, errors.New("pre-fork fixture refusal")
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = core.Close() }()
	api := &coreAPI{core: core, logins: f.m}
	_, err = api.launchResolved(daemon.LaunchSpec{AgentType: logins.ProviderCodex, Cwd: t.TempDir(), Argv: []string{bin}, ClientEnv: []string{"PATH=/bin", "SWARM_TEST_CODEX_VERSION=0.163.0"}, OperationID: "first-bootstrap", BootstrapFirstAttempt: true})
	if err == nil || !available {
		t.Fatal("external core launch retained provider admission needed by backend bootstrap")
	}
	if !daemon.BootstrapLaunchDidNotStart(err, "first-bootstrap") {
		t.Fatal("core pre-fork refusal lost original claim proof")
	}
}

func TestCodexBootstrapRetiredNativeRequiresExactContainedRecords(t *testing.T) {
	for _, scenario := range []string{"valid", "cleared_meta", "missing", "replaced_backend", "duplicate", "wrong_nonce", "wrong_runtime", "zero_backend", "zero_native", "wrong_generation", "alias", "uncontained", "public_record", "public_dir", "symlink_dir", "live_group"} {
		t.Run(scenario, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			meta := persist.Meta{ID: "child", AgentType: logins.ProviderCodex, ConversationID: "thread", ShimPID: 1 << 29, ShimStartTime: 11, InputEmbargo: strings.Repeat("b", 32), Status: status.Status{Process: status.ProcessExited}, LoginRuntime: &logins.Runtime{Provider: logins.ProviderCodex, Epoch: 1, Identity: strings.Repeat("a", 64), Home: filepath.Join(root, "home"), ExternalAuth: true}}
			dir := filepath.Join(root, meta.ID)
			if err = os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			native := shim.NativeProcessInfo{SchemaVersion: shim.NativeWriterSchemaVersion, Generation: strings.Repeat("c", 32), PID: 1<<29 + 1, PGID: 1<<29 + 1, StartTime: 12, BackendPID: 1<<29 + 2, BackendStartTime: 13, ShimPID: meta.ShimPID, ShimStartTime: meta.ShimStartTime, Provider: logins.ProviderCodex, Runtime: *meta.LoginRuntime, IncidentID: meta.InputEmbargo}
			backend := shim.BackendInfo{PID: native.BackendPID, PGID: native.BackendPID, StartedAtMS: native.BackendStartTime, SocketPath: filepath.Join(dir, "codex.sock")}
			if scenario == "wrong_nonce" {
				native.IncidentID = strings.Repeat("d", 32)
			}
			if scenario == "replaced_backend" {
				backend.StartedAtMS++
			}
			if scenario == "live_group" {
				native.BackendPID = syscall.Getpgrp()
				native.BackendStartTime = 1
				backend.PID = native.BackendPID
				backend.PGID = native.BackendPID
				backend.StartedAtMS = 1
			}
			switch scenario {
			case "wrong_runtime":
				native.Runtime.Epoch++
			case "zero_backend":
				native.BackendPID = 0
				backend.PID = 0
				backend.PGID = 0
			case "zero_native":
				native.PID = 0
				native.PGID = 0
			case "wrong_generation":
				native.Generation = "invalid"
			}
			stopped := shim.NativeStoppedInfo{SchemaVersion: shim.NativeWriterSchemaVersion, Native: native, WritersStopped: true, DescendantsContained: scenario != "uncontained"}
			for name, value := range map[string]any{shim.NativeProcessFile: native, shim.NativeStoppedFile: stopped, shim.BackendFile: backend} {
				raw, _ := json.Marshal(value)
				if scenario == "duplicate" && name == shim.NativeProcessFile {
					raw = []byte(strings.Replace(string(raw), `"provider":"codex"`, `"provider":"codex","provider":"codex"`, 1))
				}
				if scenario == "alias" && name == shim.NativeProcessFile {
					raw = []byte(strings.Replace(string(raw), `"schema_version":`, `"Schema_Version":`, 1))
				}
				if err = os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "missing" {
				_ = os.Remove(filepath.Join(dir, shim.NativeProcessFile))
			}
			if scenario == "public_record" {
				_ = os.Chmod(filepath.Join(dir, shim.NativeProcessFile), 0644)
			}
			if scenario == "public_dir" {
				_ = os.Chmod(dir, 0755)
			}
			if scenario == "symlink_dir" {
				target := dir + "-other"
				if err = os.Rename(dir, target); err != nil {
					t.Fatal(err)
				}
				if err = os.Symlink(target, dir); err != nil {
					t.Fatal(err)
				}
			}
			d := &Daemon{stateDir: root}
			expectedNonce := meta.InputEmbargo
			if scenario == "cleared_meta" {
				meta.InputEmbargo = ""
			}
			n, err := d.codexBootstrapNative(meta, expectedNonce)
			valid := scenario == "valid" || scenario == "cleared_meta" || scenario == "uncontained" && runtime.GOOS != "linux"
			if scenario == "cleared_meta" {
				d.authw = &authWatcher{list: func() []persist.Meta { return []persist.Meta{meta} }}
				c := newCodexHandoverCoordinator(&loginManager{d: d})
				w := newCodexBootstrapWorker(c)
				defer w.cancel()
				h := logins.Handover{MigrationSwitchID: expectedNonce, Members: []logins.HandoverMember{{Native: logins.NativeMember{SessionID: "same-thread-other", ConversationID: meta.ConversationID}, Phase: "pending"}}}
				b := logins.BootstrapObligation{SourceID: "source", ConversationID: meta.ConversationID, LegacyChildID: meta.ID, Phase: "ambiguous"}
				meta.ResumedFrom = b.SourceID
				if _, _, found := w.findChild(h, b); !found {
					t.Fatal("cleared metadata embargo stranded exact privately proven legacy child")
				}
				b.LegacyChildID = "wrong-child"
				if _, _, found := w.findChild(h, b); found {
					t.Fatal("hint recovered another child")
				}
			}
			if (err == nil) != valid {
				t.Fatalf("native proof accepted=%v, expected=%v: %v", err == nil, valid, err)
			}
			if valid && (n.NativePID != native.BackendPID || n.NativeStartTime != native.BackendStartTime || n.NativeIncarnation != fmt.Sprintf("%d:%d", native.BackendPID, native.BackendStartTime)) {
				t.Fatal("retired tuple lost exact backend incarnation")
			}
		})
	}
}

func committedBootstrapFixture(t *testing.T) (*loginFixture, *codexHandoverCoordinator, logins.Switch) {
	t.Helper()
	f := codexMigrationFixture(t)
	op, err := f.m.BeginSwitch(logins.ProviderCodex, f.second.ID, "migration")
	if err != nil {
		t.Fatal(err)
	}
	for range 12 {
		r, _ := f.m.store.Snapshot()
		op = r.Switches[logins.ProviderCodex]
		if op.Phase == "committed" {
			c := newCodexHandoverCoordinator(f.m)
			f.m.codexHandover = c
			c.members = func() ([]logins.NativeMember, error) { return nil, nil }
			c.bootstrapNative = func(meta persist.Meta, _ string) (logins.NativeMember, error) {
				return logins.NativeMember{SessionID: meta.ID, ConversationID: meta.ConversationID, NativeIncarnation: meta.ID + "-native", NativePID: 100 + len(meta.ID), NativeStartTime: 200}, nil
			}
			return f, c, op
		}
		f.m.step()
	}
	t.Fatal("migration never committed")
	return nil, nil, op
}

func TestCodexBootstrapTransferClaimsBeforeOneLaunchAndNeverReplaysUnknown(t *testing.T) {
	f, c, op := committedBootstrapFixture(t)
	r, _ := f.m.store.Snapshot()
	gate := &f.m.admission[loginProviderIndex(logins.ProviderCodex)]
	gate.Lock()
	_, err := f.m.transferCodexMigration(r, op, f.second.ID)
	gate.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	worker := newCodexBootstrapWorker(c)
	defer worker.cancel()
	calls := 0
	worker.launch = func(spec daemon.LaunchSpec) (persist.Meta, error) {
		calls++
		r, _ := f.m.store.Snapshot()
		h := r.Handovers[logins.ProviderCodex]
		if _, legacy := r.Switches[logins.ProviderCodex]; legacy {
			t.Fatal("launch preceded legacy transfer")
		}
		found := false
		for _, b := range h.Bootstrap {
			if b.Launch != nil && b.Launch.ID == spec.OperationID && b.Phase == "launching" {
				found = true
			}
		}
		if !found || spec.OperationID != spec.InputEmbargo || !spec.BootstrapFirstAttempt || spec.LoginSwitchID != "" || spec.InitialPrompt != "" {
			t.Fatal("launch lacked exact durable one-shot claim")
		}
		return persist.Meta{}, errors.New("uncertain launch result")
	}
	worker.step()
	first := calls
	worker.step()
	if first != len(op.Sessions) || calls != first {
		t.Fatal("unknown launch was lost or replayed")
	}
	r, _ = f.m.store.Snapshot()
	if len(r.Handovers[logins.ProviderCodex].Bootstrap) != len(op.Sessions) {
		t.Fatal("transfer dropped source obligations")
	}
}

func TestCodexBootstrapRetargetWhileLaunchBlockedPreservesIssuedTuple(t *testing.T) {
	f, c, op := committedBootstrapFixture(t)
	r, _ := f.m.store.Snapshot()
	gate := &f.m.admission[loginProviderIndex(logins.ProviderCodex)]
	gate.Lock()
	_, err := f.m.transferCodexMigration(r, op, f.second.ID)
	gate.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	worker := newCodexBootstrapWorker(c)
	defer worker.cancel()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var first sync.Once
	worker.launch = func(spec daemon.LaunchSpec) (persist.Meta, error) {
		first.Do(func() { close(entered); <-release })
		return persist.Meta{}, errors.New("uncertain")
	}
	go func() { worker.step(); close(done) }()
	<-entered
	switched := make(chan error, 1)
	go func() { _, err := f.m.BeginSwitch(logins.ProviderCodex, f.first.ID, "manual"); switched <- err }()
	select {
	case err := <-switched:
		if err != nil {
			close(release)
			<-done
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		close(release)
		<-done
		t.Fatal("manual retarget waited for bootstrap launch")
	}
	r, _ = f.m.store.Snapshot()
	h := r.Handovers[logins.ProviderCodex]
	found := false
	for _, b := range h.Bootstrap {
		if b.Launch != nil {
			found = true
			if b.Launch.ToID != f.second.ID || h.ToID != f.first.ID || b.Launch.Epoch >= h.Epoch {
				t.Error("retarget rewrote issued launch tuple")
			}
			break
		}
	}
	close(release)
	<-done
	if !found {
		t.Fatal("missing original durable claim")
	}
}

func TestCodexBootstrapBindsAndRepublishesOnlyChildBeforeRelease(t *testing.T) {
	for _, cut := range []string{"none", "invalidated", "outcome_write_refused"} {
		t.Run(cut, func(t *testing.T) {
			f, c, op := committedBootstrapFixture(t)
			c.ended = func(logins.NativeMember) bool { return false }
			op.Sessions[1].Phase = "cancelled"
			if err := f.m.store.SaveSwitch(op); err != nil {
				t.Fatal(err)
			}
			r, _ := f.m.store.Snapshot()
			gate := &f.m.admission[loginProviderIndex(logins.ProviderCodex)]
			gate.Lock()
			_, err := f.m.transferCodexMigration(r, op, f.second.ID)
			gate.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			r, _ = f.m.store.Snapshot()
			selection, _ := selectedCodexAuth(r)
			c.usageSelection, c.usageAllowed, c.usageValid = selection, true, true
			healthy := &codexHandoverFeed{ready: true, readySelection: selection, adopted: selection, current: func() bool { return true }, auth: &backendAuthCoordinator{syncWake: make(chan struct{}, 1)}}
			c.feeds[newBackendFeed()] = healthy
			t.Cleanup(func() { c.feeds = make(map[*backendFeed]*codexHandoverFeed) })
			worker := newCodexBootstrapWorker(c)
			defer worker.cancel()
			var childFeed *codexHandoverFeed
			launchCalls := 0
			worker.launch = func(spec daemon.LaunchSpec) (persist.Meta, error) {
				launchCalls++
				source := f.rows[op.Sessions[0].SourceID]
				_, modelBlanked := spec.Options["model"]
				if spec.InitialPrompt != "" || spec.Cwd != source.Cwd || modelBlanked {
					t.Fatal("bootstrap changed prompt/config")
				}
				child := persist.Meta{ID: "exact-child", AgentType: logins.ProviderCodex, ConversationID: source.ConversationID, ResumedFrom: source.ID, LoginRuntime: spec.LoginRuntime, InputEmbargo: spec.InputEmbargo, Status: status.Status{Process: status.ProcessRunning}}
				f.rows[child.ID] = child
				n, _ := c.bootstrapNative(child, spec.InputEmbargo)
				feed := newBackendFeed()
				s := &codexHandoverFeed{native: n, feed: feed, installed: selection, adopted: selection, readySelection: selection, ready: true, current: func() bool { return true }, grantCurrent: func() bool { return true }, auth: &backendAuthCoordinator{syncWake: make(chan struct{}, 1)}}
				s.publish = func(selection codexAuthSelection, phase string, usage *codexScopedUsage) error {
					return f.m.publishCodexAdoption(s, selection, phase, usage)
				}
				childFeed = s
				c.feeds[feed] = s
				return child, nil
			}
			originalStore := f.m.store
			t.Cleanup(func() { f.m.store = originalStore })
			f.m.release = func(id, nonce string) error {
				r, _ := f.m.store.Snapshot()
				h := r.Handovers[logins.ProviderCodex]
				b, _ := bootstrapRow(h, op.Sessions[0].SourceID)
				if b.ChildID != id || b.Phase != "bound" || b.Launch == nil || b.Launch.ID != nonce || len(h.Members) != 1 || h.Members[0].Phase != "adopted" {
					t.Fatal("release preceded exact durable binding/adoption")
				}
				if f.rows[id].InputEmbargo != nonce {
					t.Fatal("release changed persisted embargo lineage")
				}
				f.released++
				if f.released == 1 {
					if cut == "invalidated" {
						childFeed.invalidate()
					}
					if cut == "outcome_write_refused" {
						readOnly, e := logins.OpenReadOnly(f.m.d.stateDir)
						if e != nil {
							t.Fatal(e)
						}
						t.Cleanup(func() { _ = readOnly.Close() })
						f.m.store = readOnly
					}
				}
				return nil
			}
			worker.step()
			if cut != "none" {
				snapshot, _ := f.m.store.Snapshot()
				row, _ := bootstrapRow(snapshot.Handovers[logins.ProviderCodex], op.Sessions[0].SourceID)
				if row.Phase != "bound" {
					t.Fatal("lost readiness during shim acknowledgment published completion")
				}
				f.m.store = originalStore
				if cut == "outcome_write_refused" {
					// A fresh worker must safely repeat the idempotent shim ack;
					// production ReleaseInputEmbargo leaves Meta.InputEmbargo intact.
					launch := worker.launch
					worker.cancel()
					worker = newCodexBootstrapWorker(c)
					worker.launch = launch
					defer worker.cancel()
				}
				childFeed.mu.Lock()
				childFeed.ready = true
				childFeed.readySequence = childFeed.sequence.Load()
				childFeed.mu.Unlock()
			}
			worker.step()
			r, _ = f.m.store.Snapshot()
			h := r.Handovers[logins.ProviderCodex]
			b, _ := bootstrapRow(h, op.Sessions[0].SourceID)
			expectedAcks := 1
			if cut != "none" {
				expectedAcks = 2
			}
			if b.Phase != "ready" || launchCalls != 1 || f.released != expectedAcks || !healthy.ready || len(healthy.auth.syncWake) != 0 {
				t.Fatal("child completion repeated a launch, lost idempotent acknowledgment, or invalidated healthy feed")
			}
		})
	}
}

func TestCodexBootstrapOwnerEndRefusesUnboundClaimWithoutForgettingIt(t *testing.T) {
	f, c, op := committedBootstrapFixture(t)
	_ = c
	r, _ := f.m.store.Snapshot()
	gate := &f.m.admission[loginProviderIndex(logins.ProviderCodex)]
	gate.Lock()
	_, err := f.m.transferCodexMigration(r, op, f.second.ID)
	gate.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	r, _ = f.m.store.Snapshot()
	h := r.Handovers[logins.ProviderCodex]
	nonce := strings.Repeat("f", 32)
	if _, _, err = f.m.store.ClaimCodexBootstrapLaunch(r.Revision, h.ID, op.Sessions[0].SourceID, nonce); err != nil {
		t.Fatal(err)
	}
	before, _ := f.m.store.Snapshot()
	if err = f.m.OwnerEnd(op.Sessions[0].SourceID, "delete"); !errors.Is(err, logins.ErrPending) {
		t.Fatal("unbound cancellation was acknowledged")
	}
	after, _ := f.m.store.Snapshot()
	if after.Revision != before.Revision || !f.m.owns(op.Sessions[0].SourceID) {
		t.Fatal("owner refusal forgot unresolved launch")
	}
}

func TestCodexBootstrapExternalBirthRejectsUnknownWrapperBeforeCore(t *testing.T) {
	f := newLoginFixture(t)
	enableCodexNativeGrantUsage(t, f)
	bin := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 'codex-cli 0.163.0'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	entered := false
	core, err := daemon.Open(daemon.Config{StateDir: f.m.store.StateRoot(), SocketPath: filepath.Join(f.m.store.StateRoot(), "test.sock"), LockPath: filepath.Join(f.m.store.StateRoot(), "test.lock"), LogPath: filepath.Join(f.m.store.StateRoot(), "test.log"), MaxSessions: 4, FinalizeLaunch: func(_ string, s daemon.LaunchSpec) (daemon.LaunchSpec, error) {
		entered = true
		return s, errors.New("fixture")
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = core.Close() }()
	_, err = (&coreAPI{core: core, logins: f.m}).launchResolved(daemon.LaunchSpec{AgentType: logins.ProviderCodex, Cwd: t.TempDir(), Argv: []string{bin}, ClientEnv: []string{"PATH=/bin"}})
	if err == nil || entered {
		t.Fatal("unrecognized wrapper reached managed birth")
	}
}

func TestCodexBootstrapOnlyCausalNoStartResetsAndRetriesBoundedly(t *testing.T) {
	f, c, op := committedBootstrapFixture(t)
	r, _ := f.m.store.Snapshot()
	if _, err := f.m.transferCodexMigration(r, op, f.second.ID); err != nil {
		t.Fatal(err)
	}
	w := newCodexBootstrapWorker(c)
	defer w.cancel()
	calls := 0
	w.launch = func(spec daemon.LaunchSpec) (persist.Meta, error) {
		calls++
		return persist.Meta{}, daemon.BootstrapLaunchNotStarted(spec.OperationID)
	}
	for attempt := 0; attempt < 4; attempt++ {
		for source, a := range w.attempts {
			a.at = time.Now().Add(-time.Minute)
			w.attempts[source] = a
		}
		w.step()
	}
	if calls != 3*len(op.Sessions) {
		t.Fatalf("causal retries unbounded or stranded: %d", calls)
	}
	r, _ = f.m.store.Snapshot()
	for _, b := range r.Handovers[logins.ProviderCodex].Bootstrap {
		if b.Phase != "pending" || b.Launch != nil {
			t.Fatal("known no-start was retained as ambiguous")
		}
	}
}

func TestCodexBootstrapRestartNeverReplaysPersistedClaim(t *testing.T) {
	f, c, op := committedBootstrapFixture(t)
	r, _ := f.m.store.Snapshot()
	if _, err := f.m.transferCodexMigration(r, op, f.second.ID); err != nil {
		t.Fatal(err)
	}
	r, _ = f.m.store.Snapshot()
	h := r.Handovers[logins.ProviderCodex]
	for index, b := range h.Bootstrap {
		var err error
		r, _, err = f.m.store.ClaimCodexBootstrapLaunch(r.Revision, h.ID, b.SourceID, fmt.Sprintf("%032x", index+1))
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, b := range h.Bootstrap {
		f.m.d.clearAuthRecycle(b.SourceID)
	}
	if err := f.m.restoreClaims(); err != nil {
		t.Fatal(err)
	}
	for _, b := range h.Bootstrap {
		lane := f.m.d.composerLaneFor(b.SourceID)
		lane.mu.Lock()
		held := lane.recycling
		lane.mu.Unlock()
		if !held {
			t.Fatal("restart lost durable source authority")
		}
	}
	w := newCodexBootstrapWorker(c)
	defer w.cancel()
	w.launch = func(daemon.LaunchSpec) (persist.Meta, error) {
		t.Fatal("loaded claim was replayed")
		return persist.Meta{}, nil
	}
	before, _ := f.m.store.Snapshot()
	w.step()
	after, _ := f.m.store.Snapshot()
	if before.Revision != after.Revision {
		t.Fatal("unknown persisted claims were rewritten")
	}
}

func TestCodexBootstrapLegacyAmbiguityDoesNotBlockHealthyCohortOrPruneMissingCandidate(t *testing.T) {
	f, c, op := committedBootstrapFixture(t)
	op.Phase = "resuming"
	if err := f.m.store.SaveSwitch(op); err != nil {
		t.Fatal(err)
	}
	var members []logins.NativeMember
	for _, id := range []string{"old-child-one", "old-child-two"} {
		meta := persist.Meta{ID: id, AgentType: logins.ProviderCodex, ResumedFrom: op.Sessions[0].SourceID, ConversationID: op.Sessions[0].ConversationID, LoginRuntime: &logins.Runtime{Provider: logins.ProviderCodex, ExternalAuth: true, Epoch: op.ToEpoch}, InputEmbargo: op.ID, Status: status.Status{Process: status.ProcessRunning}}
		f.rows[id] = meta
		native, _ := c.bootstrapNative(meta, op.ID)
		members = append(members, native)
	}
	c.members = func() ([]logins.NativeMember, error) { return members, nil }
	c.ended = func(logins.NativeMember) bool { return false }
	r, _ := f.m.store.Snapshot()
	if _, err := f.m.transferCodexMigration(r, op, f.first.ID); err != nil {
		t.Fatalf("ambiguous source blocked desired cohort: %v", err)
	}
	r, _ = f.m.store.Snapshot()
	h := r.Handovers[logins.ProviderCodex]
	b, _ := bootstrapRow(h, op.Sessions[0].SourceID)
	if b.Phase != "ambiguous" || b.ChildID != "" || len(h.Members) != 2 {
		t.Fatal("transfer guessed a child or omitted live cohort")
	}
	delete(f.rows, "old-child-one")
	w := newCodexBootstrapWorker(c)
	defer w.cancel()
	w.progress(h.ID, b.SourceID)
	r, _ = f.m.store.Snapshot()
	b, _ = bootstrapRow(r.Handovers[logins.ProviderCodex], b.SourceID)
	if b.ChildID != "" {
		t.Fatal("stale roster absence resolved retained ambiguous custody")
	}
}

func TestCodexBootstrapBoundOwnerEndHonorsCancellationAfterReleaseRefusal(t *testing.T) {
	f, c, op := committedBootstrapFixture(t)
	r, _ := f.m.store.Snapshot()
	if _, err := f.m.transferCodexMigration(r, op, f.second.ID); err != nil {
		t.Fatal(err)
	}
	r, _ = f.m.store.Snapshot()
	h := r.Handovers[logins.ProviderCodex]
	source := op.Sessions[0]
	nonce := strings.Repeat("d", 32)
	r, _, err := f.m.store.ClaimCodexBootstrapLaunch(r.Revision, h.ID, source.SourceID, nonce)
	if err != nil {
		t.Fatal(err)
	}
	child := persist.Meta{ID: "bound-child", AgentType: logins.ProviderCodex, ResumedFrom: source.SourceID, ConversationID: source.ConversationID, LoginRuntime: &f.runtime, InputEmbargo: nonce, Status: status.Status{Process: status.ProcessRunning}}
	f.rows[child.ID] = child
	n, _ := c.bootstrapNative(child, nonce)
	if _, _, err = f.m.store.RecordCodexBootstrapChild(r.Revision, h.ID, source.SourceID, nonce, n); err != nil {
		t.Fatal(err)
	}
	f.m.d.restoreAuthRecycle(child.ID)
	if err = f.m.OwnerEnd(child.ID, "kill"); err != nil {
		t.Fatalf("bound child cancellation refused: %v", err)
	}
	r, _ = f.m.store.Snapshot()
	b, _ := bootstrapRow(r.Handovers[logins.ProviderCodex], source.SourceID)
	if b.Phase != "cancelled" || f.rows[child.ID].Status.Process != status.ProcessExited {
		t.Fatal("owner cancellation was acknowledged without ending bound child")
	}
}

func TestCodexBootstrapCloseCancelsAndJoinsSingleWorker(t *testing.T) {
	f, c, op := committedBootstrapFixture(t)
	r, _ := f.m.store.Snapshot()
	if _, err := f.m.transferCodexMigration(r, op, f.second.ID); err != nil {
		t.Fatal(err)
	}
	w := newCodexBootstrapWorker(c)
	entered := make(chan struct{})
	w.launch = func(spec daemon.LaunchSpec) (persist.Meta, error) {
		close(entered)
		<-w.ctx.Done()
		return persist.Meta{}, daemon.BootstrapLaunchNotStarted(spec.OperationID)
	}
	c.bootstrap = w
	go w.run()
	w.wake <- struct{}{}
	<-entered
	closed := make(chan struct{})
	go func() { c.close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("coordinator close did not cancel and join bootstrap worker")
	}
	select {
	case <-w.done:
	default:
		t.Fatal("bootstrap worker survived coordinator close")
	}
	r, _ = f.m.store.Snapshot()
	for _, b := range r.Handovers[logins.ProviderCodex].Bootstrap {
		if b.Launch != nil {
			t.Fatal("causally cancelled no-start claim remained stranded")
		}
	}
}

func TestCodexBootstrapUnprovedLegacyChildTransfersHintAndRefusesOwnerEnd(t *testing.T) {
	f, c, op := committedBootstrapFixture(t)
	op.Phase = "resuming"
	op.Sessions[0].ChildID = "missing-legacy-child"
	op.Sessions[0].Phase = "ready"
	if err := f.m.store.SaveSwitch(op); err != nil {
		t.Fatal(err)
	}
	healthy := logins.NativeMember{SessionID: "healthy", ConversationID: "other-thread", NativeIncarnation: "healthy-native", NativePID: 12345, NativeStartTime: 55}
	c.members = func() ([]logins.NativeMember, error) { return []logins.NativeMember{healthy}, nil }
	c.ended = func(logins.NativeMember) bool { return false }
	r, _ := f.m.store.Snapshot()
	if _, err := f.m.transferCodexMigration(r, op, f.first.ID); err != nil {
		t.Fatalf("missing child proof blocked desired cohort: %v", err)
	}
	before, _ := f.m.store.Snapshot()
	h := before.Handovers[logins.ProviderCodex]
	b, _ := bootstrapRow(h, op.Sessions[0].SourceID)
	if b.LegacyChildID != "missing-legacy-child" || b.ChildID != "" || b.Phase != "ambiguous" || len(h.Members) != 1 {
		t.Fatal("legacy name became authority or lost healthy member")
	}
	if !f.m.owns(b.LegacyChildID) {
		t.Fatal("unproved child hint escaped owner refusal")
	}
	if err := f.m.OwnerEnd(b.LegacyChildID, "kill"); !errors.Is(err, logins.ErrPending) {
		t.Fatal("hint acknowledged owner cancellation")
	}
	after, _ := f.m.store.Snapshot()
	if after.Revision != before.Revision {
		t.Fatal("hint refusal changed durable recovery")
	}
}
