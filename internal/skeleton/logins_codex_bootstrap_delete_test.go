package skeleton

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/status"
)

// This uses the real core's persisted roster and irreversible discussion delete.
// Native processes are already stopped synthetic identities, with actual private
// scope/containment records; no installed provider or credentials are involved.
func TestCodexBootstrapDeleteSettlesBeforeRemovingNativeProofAndRecoversCuts(t *testing.T) {
	for _, cut := range []string{"store_refusal", "after_settlement", "partial_payload", "partial_without_authorization"} {
		t.Run(cut, func(t *testing.T) {
			if cut == "partial_payload" && os.Geteuid() == 0 {
				t.Skip("real permission-denied partial removal requires an unprivileged test process")
			}
			f, c, op := committedBootstrapFixture(t)
			r, _ := f.m.store.Snapshot()
			if _, err := f.m.transferCodexMigration(r, op, f.second.ID); err != nil {
				t.Fatal(err)
			}
			r, _ = f.m.store.Snapshot()
			h := r.Handovers[logins.ProviderCodex]
			source := op.Sessions[0]
			nonce := strings.Repeat("a", 32)
			r, _, err := f.m.store.ClaimCodexBootstrapLaunch(r.Revision, h.ID, source.SourceID, nonce)
			if err != nil {
				t.Fatal(err)
			}
			runtime, err := f.m.store.Runtime(logins.ProviderCodex)
			if err != nil {
				t.Fatal(err)
			}
			root := f.m.d.stateDir
			meta := persist.Meta{ID: "actual-core-child", AgentType: logins.ProviderCodex, Cwd: t.TempDir(), ResumedFrom: source.SourceID, ConversationID: source.ConversationID, LoginRuntime: &runtime, InputEmbargo: nonce, ShimPID: 1 << 29, ShimStartTime: 11, Status: status.Status{Process: status.ProcessExited}}
			disk, err := persist.NewStore(root)
			if err != nil {
				t.Fatal(err)
			}
			if err = disk.Save(meta); err != nil {
				t.Fatal(err)
			}
			native := shim.NativeProcessInfo{SchemaVersion: shim.NativeWriterSchemaVersion, Generation: strings.Repeat("b", 32), PID: 1<<29 + 1, PGID: 1<<29 + 1, StartTime: 12, BackendPID: 1<<29 + 2, BackendStartTime: 13, ShimPID: meta.ShimPID, ShimStartTime: meta.ShimStartTime, Provider: meta.AgentType, Runtime: runtime, IncidentID: nonce}
			for name, value := range map[string]any{shim.NativeProcessFile: native, shim.NativeStoppedFile: shim.NativeStoppedInfo{SchemaVersion: shim.NativeWriterSchemaVersion, Native: native, WritersStopped: true, DescendantsContained: true}, shim.BackendFile: shim.BackendInfo{PID: native.BackendPID, PGID: native.BackendPID, StartedAtMS: native.BackendStartTime, SocketPath: filepath.Join(root, meta.ID, "codex.sock")}} {
				raw, _ := json.Marshal(value)
				if err = os.WriteFile(filepath.Join(root, meta.ID, name), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			blocked := filepath.Join(root, meta.ID, "zz-blocked-payload")
			t.Cleanup(func() { _ = os.Chmod(blocked, 0700) })
			deleteCalls := 0
			refuseDelete := cut == "after_settlement"
			cfg := daemon.Config{StateDir: root, SocketPath: filepath.Join(root, "core.sock"), LockPath: filepath.Join(root, "core.lock"), LogPath: filepath.Join(root, "core.log"), MaxSessions: 4, PreDelete: func(removed persist.Meta) error {
				if removed.ID != meta.ID {
					t.Fatal("deleted a foreign child")
				}
				deleteCalls++
				snapshot, e := f.m.store.Snapshot()
				if e != nil {
					t.Fatal(e)
				}
				current := snapshot.Handovers[logins.ProviderCodex]
				row, _ := bootstrapRow(current, source.SourceID)
				retired := false
				for _, member := range current.Members {
					if member.Native.SessionID == meta.ID && member.Phase == "retired" {
						retired = true
					}
				}
				if row.Phase != "cancelled" || !retired {
					t.Error("irreversible delete preceded durable cancellation and exact native retirement")
				}
				if cut == "partial_payload" && deleteCalls == 1 {
					if e := os.Mkdir(blocked, 0700); e != nil {
						t.Fatal(e)
					}
					if e := os.WriteFile(filepath.Join(blocked, "retained"), []byte("synthetic"), 0600); e != nil {
						t.Fatal(e)
					}
					if e := os.Chmod(blocked, 0500); e != nil {
						t.Fatal(e)
					}
				}
				if refuseDelete {
					return errors.New("injected cut after durable cancellation")
				}
				return nil
			}}
			openCore := func() *daemon.Daemon {
				core, e := daemon.Open(cfg)
				if e != nil {
					t.Fatal(e)
				}
				f.m.d.core = core
				f.m.d.api = &coreAPI{core: core, stateDir: root}
				return core
			}
			core := openCore()
			defer func() { _ = core.Close() }()
			c.bootstrapNative = func(m persist.Meta, incident string) (logins.NativeMember, error) {
				return f.m.d.codexBootstrapNative(m, incident)
			}
			member, err := c.bootstrapNative(meta, nonce)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = f.m.store.RecordCodexBootstrapChild(r.Revision, h.ID, source.SourceID, nonce, member); err != nil {
				t.Fatal(err)
			}
			originalStore := f.m.store
			if cut == "store_refusal" {
				readOnly, e := logins.OpenReadOnly(root)
				if e != nil {
					t.Fatal(e)
				}
				defer func() { _ = readOnly.Close() }()
				f.m.store = readOnly
			}
			if cut == "partial_without_authorization" {
				if err = f.m.OwnerEnd(meta.ID, "kill"); err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{shim.NativeProcessFile, shim.NativeStoppedFile, shim.BackendFile} {
					if err = os.Remove(filepath.Join(root, meta.ID, name)); err != nil {
						t.Fatal(err)
					}
				}
				if err = core.Close(); err != nil {
					t.Fatal(err)
				}
				core = openCore()
				if err = f.m.OwnerEnd(meta.ID, "delete"); !errors.Is(err, logins.ErrPending) {
					t.Fatal("ordinary cancellation/retirement bypassed missing first-delete stop proof")
				}
				if _, exists := core.Get(meta.ID); !exists || deleteCalls != 0 {
					t.Fatal("unproved first delete removed retained row")
				}
				return
			}
			if err = f.m.OwnerEnd(meta.ID, "delete"); err == nil {
				t.Fatal("injected cut was acknowledged")
			}
			f.m.store = originalStore
			retained, exists := core.Get(meta.ID)
			if !exists || retained.Status.Process == status.ProcessRunning {
				t.Fatal("failed durable cancellation/delete removed the stopped recovery row")
			}
			if cut == "partial_payload" {
				if _, err = c.bootstrapNative(retained, nonce); err == nil {
					t.Fatal("partial removal did not actually erase native proof")
				}
				if _, err = os.Stat(filepath.Join(root, meta.ID, shim.NativeProcessFile)); !os.IsNotExist(err) {
					t.Fatal("real partial delete retained native payload")
				}
				if err = os.Chmod(blocked, 0700); err != nil {
					t.Fatal(err)
				}
			} else if _, err = c.bootstrapNative(retained, nonce); err != nil {
				t.Fatal("failed cancellation/delete removed exact native proof")
			}

			if cut == "store_refusal" && deleteCalls != 0 {
				t.Fatal("store refusal reached irreversible delete")
			}
			// Reopen the actual core before retry: recovery cannot rely on an in-memory row.
			if err = core.Close(); err != nil {
				t.Fatal(err)
			}
			core = openCore()
			refuseDelete = false
			if err = f.m.OwnerEnd(meta.ID, "delete"); err != nil {
				t.Fatalf("restart could not finish exact owner deletion: %v", err)
			}
			if _, exists = core.Get(meta.ID); exists {
				t.Fatal("settled owner deletion retained core row")
			}
			if _, err = os.Lstat(filepath.Join(root, meta.ID)); !os.IsNotExist(err) {
				t.Fatal("settled owner deletion retained native directory")
			}
			if err = f.m.OwnerEnd(meta.ID, "delete"); err != nil {
				t.Fatalf("completed exact deletion was not idempotent: %v", err)
			}
		})
	}
}

func TestCodexBootstrapDeleteNeverLaunchedSourceIsDurableAndIdempotent(t *testing.T) {
	f, _, op := committedBootstrapFixture(t)
	r, _ := f.m.store.Snapshot()
	if _, err := f.m.transferCodexMigration(r, op, f.second.ID); err != nil {
		t.Fatal(err)
	}
	source := f.rows[op.Sessions[0].SourceID]
	root := f.m.d.stateDir
	disk, err := persist.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = disk.Save(source); err != nil {
		t.Fatal(err)
	}
	core, err := daemon.Open(daemon.Config{StateDir: root, SocketPath: filepath.Join(root, "core.sock"), LockPath: filepath.Join(root, "core.lock"), LogPath: filepath.Join(root, "core.log"), MaxSessions: 4, PreDelete: func(persist.Meta) error {
		snapshot, e := f.m.store.Snapshot()
		if e != nil {
			t.Fatal(e)
		}
		b, _ := bootstrapRow(snapshot.Handovers[logins.ProviderCodex], source.ID)
		if b.Phase != "cancelled" || b.ChildID != "" || b.Launch != nil {
			t.Fatal("source deletion preceded durable cancellation or invented a child")
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = core.Close() }()
	f.m.d.core = core
	f.m.d.api = &coreAPI{core: core, stateDir: root}
	if err = f.m.OwnerEnd(source.ID, "delete"); err != nil {
		t.Fatal(err)
	}
	if err = f.m.OwnerEnd(source.ID, "delete"); err != nil {
		t.Fatalf("completed source deletion was not idempotent: %v", err)
	}
}
