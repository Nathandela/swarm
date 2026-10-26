package skeleton

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/claude"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/status"
)

func TestExecutionHookCustodySurvivesSpoolRestart(t *testing.T) {
	for _, phase := range []string{"fold", "ack"} {
		t.Run(phase, func(t *testing.T) {
			root := t.TempDir()
			const id, token = "hook-custody-merge", "hook-token"
			sessionDir := filepath.Join(root, id)
			if err := os.MkdirAll(sessionDir, 0700); err != nil {
				t.Fatal(err)
			}
			spoolPath := filepath.Join(sessionDir, shim.HookSpoolFile)
			producer, err := shim.OpenHookSpoolProducer(spoolPath, 0)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = producer.Close() })
			child := engine.Callback{SessionID: id, Token: token, Sequence: 2, Event: "SubagentStart", Payload: map[string]string{"agent_id": "child"}, Raw: json.RawMessage(`{"agent_id":"child","opaque":"retain-exact-raw"}`)}
			rawChild, err := json.Marshal(child)
			if err != nil {
				t.Fatal(err)
			}
			prefix, err := json.Marshal(engine.Callback{SessionID: id, Token: token, Sequence: 1, Event: "UserPromptSubmit"})
			if err != nil {
				t.Fatal(err)
			}
			for _, raw := range [][]byte{prefix, rawChild} {
				if _, err := producer.Append(raw); err != nil {
					t.Fatal(err)
				}
			}
			if err := producer.Close(); err != nil {
				t.Fatal(err)
			}

			fail := true
			newConsumer := func() (*HookDrainer, *persist.Store) {
				store, err := persist.NewStore(root)
				if err != nil {
					t.Fatal(err)
				}
				e := engine.New(engine.Config{StoreExecution: func(record persist.ExecutionRecord) error {
					// Exercise both the durable actor fold and its later pending-receipt ACK.
					if fail && record.Actors["child"].Active && ((phase == "fold" && len(record.PendingCallbacks) != 0) || (phase == "ack" && len(record.PendingCallbacks) == 0)) {
						// A transient custody error must dominate a joined permanent tag.
						return errors.Join(syscall.ENOSPC, errHookIngestRejected)
					}
					return store.SaveExecution(record)
				}})
				var saved *persist.ExecutionRecord
				if record, err := store.LoadExecution(id); err == nil {
					saved = &record
				} else if !errors.Is(err, os.ErrNotExist) {
					t.Fatal(err)
				}
				initial := status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone}
				identity := engine.ExecutionIdentity{Incarnation: "same-shim-birth", ObservedFromStart: true}
				if err := e.RegisterExecutionSession(id, token, 1, claude.New().SignalSources(), initial, identity, saved); err != nil {
					t.Fatal(err)
				}
				d := &Daemon{eng: e, core: &daemon.Daemon{}, executionStore: store, stateDir: root, adapterFor: func(string) (adapter.Adapter, bool) { return nil, false }}
				hd := NewHookDrainer(d, id, "", filepath.Join(sessionDir, "hook.fold"))
				hd.SetSpoolPath(spoolPath)
				return hd, store
			}
			hd, _ := newConsumer()
			applied, skipped, err := hd.drainFromSpoolFile()
			var custody *engine.ExecutionCustodyError
			if applied != 1 || skipped != 0 || !errors.As(err, &custody) || !errors.Is(err, syscall.ENOSPC) || !errors.Is(err, errHookIngestRejected) {
				t.Fatalf("failed custody was discarded: applied=%d skipped=%d err=%v", applied, skipped, err)
			}

			var prefixCertificate []byte
			assertRetained := func() {
				t.Helper()
				if hd.cursor() != 1 {
					t.Fatalf("failed custody advanced fold cursor to %d", hd.cursor())
				}
				data, err := os.ReadFile(hd.cursorPath + ".cert")
				var certificate hookCursorCertificate
				if err != nil || json.Unmarshal(data, &certificate) != nil || certificate.Sequence != 1 || certificate.Incarnation == "" || certificate.Producer == "" {
					t.Fatalf("failed custody gained retirement authority: certificate=%+v err=%v", certificate, err)
				}
				if prefixCertificate == nil {
					prefixCertificate = append([]byte(nil), data...)
				} else if !bytes.Equal(data, prefixCertificate) {
					t.Fatal("failed custody changed the durable prefix certificate")
				}
				response, err := shim.ReadHookSpoolFileResponse(spoolPath, 1)
				if err != nil || response.Gap || len(response.Records) != 1 || response.Records[0].Seq != 2 || !bytes.Equal(response.Records[0].Body, rawChild) {
					t.Fatalf("exact failed payload no longer redeliverable: records=%d gap=%t err=%v", len(response.Records), response.Gap, err)
				}
			}
			assertRetained()
			if err := hd.retireCertifiedPrefix(); err != nil {
				t.Fatal(err)
			}
			assertRetained() // Only the already durable prefix may be compacted.

			// Drop both consumer and engine; restore solely from the actual store/cursor files.
			hd, store := newConsumer()
			assertRetained()
			durable, err := store.LoadExecution(id)
			if err != nil {
				t.Fatal(err)
			}
			if phase == "ack" {
				if !durable.Actors["child"].Active || len(durable.PendingCallbacks) != 1 || durable.PendingCallbacks["2"] == "" {
					t.Fatal("restart lost the durable actor or exact pending ACK")
				}
			} else if len(durable.Actors) != 0 || len(durable.PendingCallbacks) != 0 {
				t.Fatal("failed fold became durable across restart")
			}
			fail = false
			applied, skipped, err = hd.drainFromSpoolFile()
			wantApplied, wantSkipped := 1, 0
			if phase == "ack" {
				wantApplied, wantSkipped = 0, 1 // Exact seen redelivery durably releases its pending receipt.
			}
			if err != nil || applied != wantApplied || skipped != wantSkipped || hd.cursor() != 2 {
				t.Fatalf("retry/restart failed: applied=%d skipped=%d cursor=%d err=%v", applied, skipped, hd.cursor(), err)
			}
			durable, err = store.LoadExecution(id)
			if err != nil {
				t.Fatal(err)
			}
			actor := durable.Actors["child"]
			if len(durable.Actors) != 1 || !actor.Active || actor.Sequence != 2 || actor.StartSequence != 2 || len(durable.PendingCallbacks) != 0 {
				t.Fatalf("retry duplicated/lost actor or kept pending ACK: actors=%+v pending=%d", durable.Actors, len(durable.PendingCallbacks))
			}
			_, _, certificateSeq, err := ReadHookRetirementCertificate(hd.cursorPath)
			if err != nil || certificateSeq != 2 {
				t.Fatalf("durable retry lacks exact retirement certificate: sequence=%d err=%v", certificateSeq, err)
			}
			if applied, skipped, err := hd.drainFromSpoolFile(); err != nil || applied != 0 || skipped != 0 {
				t.Fatalf("durably folded callback redelivered again: applied=%d skipped=%d err=%v", applied, skipped, err)
			}
			if err := hd.retireCertifiedPrefix(); err != nil {
				t.Fatal(err)
			}
			if info, err := os.Stat(spoolPath); err != nil || info.Size() != 0 {
				t.Fatalf("durable retried raw payload was not retired: info=%v err=%v", info, err)
			}
		})
	}
}
