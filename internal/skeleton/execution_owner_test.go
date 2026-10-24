package skeleton

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter/codex"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
)

type ownerFenceUpstream struct {
	*fakeUpstream
	order []string
}

func (u *ownerFenceUpstream) Input(p []byte) error { u.order = append(u.order, "input"); return nil }
func (u *ownerFenceUpstream) Submit(string) error  { u.order = append(u.order, "submit"); return nil }
func (u *ownerFenceUpstream) ControlInput([]byte) error {
	u.order = append(u.order, "control")
	return nil
}

func TestExecutionOwnerFencePrecedesEveryTapWrite(t *testing.T) {
	for _, verb := range []string{"input", "submit", "control"} {
		t.Run(verb, func(t *testing.T) {
			up := &ownerFenceUpstream{fakeUpstream: newFakeUpstream(mustSnap(t, ""))}
			mgr := newTapManager(func(string) (protocol.SessionStream, error) { return up, nil })
			var refuse bool
			mgr.beforeWrite = func(id string) error {
				if id != "s1" {
					t.Fatalf("wrong input identity %q", id)
				}
				up.order = append(up.order, "fence")
				if refuse {
					return errors.New("durable owner revision failed")
				}
				return nil
			}
			rw, err := mgr.subscribe("s1", readWrite)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rw.Close() }()
			ro, err := mgr.subscribe("s1", readOnly)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = ro.Close() }()
			write := func(s protocol.SessionStream) error {
				switch verb {
				case "input":
					return s.Input([]byte("x"))
				case "submit":
					return s.(protocol.MessageSubmitter).Submit("x")
				default:
					return s.(*tapSub).ControlKeys([]byte("x"))
				}
			}
			if err = write(ro); err != nil {
				t.Fatal(err)
			}
			if len(up.order) != 0 {
				t.Fatal("read-only observer advanced owner revision")
			}
			if err = rw.Resize(80, 24); err != nil {
				t.Fatal(err)
			}
			if len(up.order) != 0 {
				t.Fatal("resize advanced owner revision")
			}
			if err = write(rw); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(up.order, []string{"fence", verb}) {
				t.Fatalf("input crossed fence: %v", up.order)
			}
			up.order = nil
			refuse = true
			if err = write(rw); err == nil {
				t.Fatal("failed owner fence allowed write")
			}
			if !reflect.DeepEqual(up.order, []string{"fence"}) {
				t.Fatalf("bytes escaped refused revision: %v", up.order)
			}
		})
	}
}

type ownerFenceBackend struct {
	*backendWriteBoundaryRecorder
	beforeEffect func()
}

func (b *ownerFenceBackend) CallAtWriteBoundary(ctx context.Context, method string, params, out any, beforeWrite func() error, afterWrite func()) error {
	return b.backendWriteBoundaryRecorder.CallAtWriteBoundary(ctx, method, params, out, func() error {
		if err := beforeWrite(); err != nil {
			return err
		}
		b.beforeEffect()
		return nil
	}, afterWrite)
}

func TestExecutionOwnerNativeComposerFencesBeforeRPC(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		t.Run(fmt.Sprint(refuse), func(t *testing.T) {
			r := newCodexExecutionRig(t, resumeIdentityCodexID, true)
			base := newR7FakeBackend()
			base.reply["turn/start"] = json.RawMessage(`{"turn":{"id":"native-next-turn"}}`)
			writes := 0
			backend := &ownerFenceBackend{backendWriteBoundaryRecorder: &backendWriteBoundaryRecorder{base: base}, beforeEffect: func() {
				writes++
				saved, err := r.store.LoadExecution(r.local)
				if err != nil {
					t.Fatal(err)
				}
				if saved.OwnerRevision != 1 {
					t.Fatalf("native RPC preceded durable owner fence: %+v", saved)
				}
			}}
			if refuse {
				r.saveMu.Lock()
				r.saveHook = func(persist.ExecutionRecord) error { return errors.New("durable owner revision refused") }
				r.saveMu.Unlock()
			}
			lane := r.d.composerLaneFor(r.local)
			_, err := r.d.deliverComposerText(r.local, lane, 0, messageSink{backend: &sessionBackend{threadID: resumeIdentityCodexID, conn: backend}}, false, "", "synthetic owner input", "client-ref", nil)
			if (err != nil) != refuse {
				t.Fatalf("native owner effect err=%v", err)
			}
			if writes != map[bool]int{false: 1, true: 0}[refuse] {
				t.Fatalf("native writes=%d after owner fence", writes)
			}
		})
	}
}

func TestExecutionOwnerRevisionSurvivesExactSessionRegistration(t *testing.T) {
	r := newCodexExecutionRig(t, resumeIdentityCodexID, true)
	r.enqueue(t, "turn/started", map[string]any{"threadId": resumeIdentityCodexID, "turn": map[string]any{"id": "native-turn", "status": "inProgress"}})
	r.drain(t)
	if err := r.d.observeExecutionOwnerWrite(r.local); err != nil {
		t.Fatal(err)
	}
	saved, err := r.store.LoadExecution(r.local)
	if err != nil {
		t.Fatal(err)
	}
	r.d.registerExecutionSession(r.meta, "", codex.New().SignalSources(), false)
	restored, err := r.d.eng.ExecutionSnapshot(r.local)
	if err != nil {
		t.Fatal(err)
	}
	if restored.OwnerRevision != saved.OwnerRevision || restored.Codex == nil || !restored.Codex.UnknownCustody {
		t.Fatalf("exact restart lost revision/custody: before=%+v after=%+v", saved, restored)
	}
}
