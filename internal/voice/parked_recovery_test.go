package voice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/status"
)

type parkedRecoveryRuntime struct {
	discussionTestRuntime
	failures  int
	uncertain bool
	requests  []protocol.LaunchReq
	receipts  map[string]schema.OperationOutcomeView
}

func (r *parkedRecoveryRuntime) record(request protocol.LaunchReq, op string) string {
	r.requests = append(r.requests, request)
	n := len(r.requests)
	id := fmt.Sprintf("machine/attempt%d", n)
	instance := fmt.Sprintf("instance%d", n)
	row := protocol.SessionView{ID: id, EndpointID: "machine", Role: "manager", ManagerConversationID: request.Manager.ConversationID, Agent: request.Agent, Cwd: request.Cwd, Capabilities: &protocol.SessionCapabilities{SessionInstance: instance}}
	receipt := schema.OperationOutcomeView{State: schema.OutcomeApplied, SessionID: id}
	if n <= r.failures {
		row.Status.Process = status.ProcessExited
		if !r.uncertain {
			receipt.FailedManagerInstance = instance
		}
	} else {
		row.Status.Process = status.ProcessRunning
		r.status.NativeCLI = request.Manager.NativeCLI
		r.status.Binding.Session = id
		r.status.Binding.SessionInstance = instance
		if request.ResumeSourceInstance == "" {
			r.status.Binding.NativeSessionID = "fresh-history"
		}
	}
	r.rows = append(r.rows, row)
	r.receipts[op] = receipt
	return id
}

func (r *parkedRecoveryRuntime) LaunchOperation(op string, request protocol.LaunchReq) (string, string, error) {
	return r.record(request, op), "manager", nil
}
func (r *parkedRecoveryRuntime) LaunchOperationStatus(op string) (schema.OperationOutcomeView, error) {
	return r.receipts[op], nil
}
func (r *parkedRecoveryRuntime) ManagerRuntime(string, string) (protocol.ManagerRuntimeStatus, error) {
	return r.status, nil
}

func TestParkedResumeRecoversOldAppliedIntentWithoutReplayingItsLaunch(t *testing.T) {
	for _, tc := range []struct {
		name      string
		failures  int
		uncertain bool
		pruned    bool
	}{
		{"native history on second attempt", 1, false, false}, {"fresh manager after two failed resumes", 2, false, false},
		{"fresh manager also exits", 3, false, false}, {"physical exit without proof holds", 1, true, false}, {"executing launch with compacted receipt holds", 1, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, store, controllerRuntime, binding := newControllerFixture(t)
			r := &parkedRecoveryRuntime{discussionTestRuntime: discussionTestRuntime{controllerRuntimeFixture: controllerRuntime}, failures: tc.failures, uncertain: tc.uncertain, receipts: make(map[string]schema.OperationOutcomeView)}
			cfg := testConfig()
			binding.ConversationID = DiscussionConversationPrefix + "retry"
			binding, err := store.BindManager(binding)
			if err != nil {
				t.Fatal(err)
			}
			r.status.Binding = runtimeBinding(binding)
			cfg.ConversationID = binding.ConversationID
			dir := privateConfigDir(t)
			d := &DiscussionController{cfg: cfg, store: store, runtime: r, dir: dir, endpoint: "machine", bridge: &privateHTTP{socket: dir + "/bridge.sock"}, now: time.Now}
			r.status.BridgeSocket, r.status.AuthFile = d.refs().Socket, d.refs().AuthFile
			if err := store.update(func(st *State) error {
				c := st.Conversations[cfg.ConversationID]
				c.Parked = true
				c.Retirement = &protocol.ManagerRetireReq{Binding: runtimeBinding(binding), Generation: 1, Checkpoint: hashBytes([]byte("parked"))}
				putRecord(st, "conversations", &st.Conversations, cfg.ConversationID, c)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			st, err := store.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			conv := st.Conversations[cfg.ConversationID]
			req := managerLaunchRequest(cfg, d.refs())
			req.Options[protocol.OptionResumeFrom] = binding.SessionID
			req.ResumeSourceInstance = binding.SessionInstance
			req.Manager.RetireOperationID = protocol.ManagerRetireOperationID(*conv.Retirement)
			op := fmt.Sprintf("launch:%d:%s", time.Now().Add(-2*time.Hour).UnixMilli(), strings.Repeat("a", 32))
			id := r.record(req, op)
			file := "resume-" + hashBytes([]byte(req.Manager.RetireOperationID)) + ".json"
			intent := managerLaunchIntent{Version: 1, ConversationID: cfg.ConversationID, Phase: "applied", SessionID: id, Request: req, OperationID: op}
			if tc.pruned {
				intent.Phase, intent.SessionID = "executing", ""
				delete(r.receipts, op)
			}
			if err = saveManagerIntent(dir, file, intent); err != nil {
				t.Fatal(err)
			}
			attempt := func() (ManagerBinding, error) {
				state, e := store.Snapshot()
				if e != nil {
					t.Fatal(e)
				}
				return d.wakeParked(context.Background(), state.Generation, state.Conversations[cfg.ConversationID])
			}
			next, err := attempt()
			if tc.failures == 3 && !tc.uncertain {
				if err == nil {
					t.Fatal("missing failed startup")
				}
				for range 3 {
					_, _ = attempt()
				}
				if len(r.requests) != 3 {
					t.Fatal("retry before deadline")
				}
				d.now = func() time.Time { return time.Now().Add(discussionReplacementInterval + time.Second) }
				next, err = attempt()
				if err != nil || next.SessionID != "machine/attempt4" || len(r.requests) != 4 || r.requests[3].ResumeSourceInstance != "" {
					t.Fatalf("fresh recovery: %+v, %v, launches %d", next, err, len(r.requests))
				}
				return
			}
			if tc.uncertain {
				if !errors.Is(err, ErrDiscussionActivationUnavailable) {
					t.Fatalf("missing visible hold: %v", err)
				}
				_, _ = attempt()
				if len(r.requests) != tc.failures {
					t.Fatalf("redrove an uncertain or exhausted launch: %+v", r.requests)
				}
				return
			}
			if err != nil || len(r.requests) != tc.failures+1 || r.requests[1].Options[protocol.OptionResumeFrom] != id || r.requests[1].ResumeSourceInstance != "instance1" {
				t.Fatalf("bad recovery: next=%+v requests=%+v err=%v", next, r.requests, err)
			}
			st, err = store.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			recovered := st.Conversations[cfg.ConversationID]
			fresh := tc.failures == 2
			if recovered.Parked || recovered.FailedResume != nil || recovered.Retirement != nil || recovered.NeedsHandoff != fresh || (next.NativeSessionID != binding.NativeSessionID) != fresh || len(st.Publications) != map[bool]int{false: 0, true: 1}[fresh] {
				t.Fatalf("incorrect continuity disposition: %+v", recovered)
			}
			if fresh && (r.requests[2].Options[protocol.OptionResumeFrom] != "" || r.requests[2].ResumeSourceInstance != "") {
				t.Fatal("replacement reused failed source")
			}
		})
	}
}

func TestParkedLegacyManagerMigratesOnceAfterRetirementProof(t *testing.T) {
	_, store, base, old := newControllerFixture(t)
	cfg := testConfig()
	cfg.ConversationID, cfg.VMProjectAccess = old.ConversationID, true
	dir := privateConfigDir(t)
	r := &parkedRecoveryRuntime{discussionTestRuntime: discussionTestRuntime{controllerRuntimeFixture: base}, receipts: make(map[string]schema.OperationOutcomeView)}
	d := &DiscussionController{cfg: cfg, store: store, runtime: r, dir: dir, endpoint: "machine", bridge: &privateHTTP{socket: dir + "/bridge.sock"}, now: time.Now}
	r.status.BridgeSocket, r.status.AuthFile = d.refs().Socket, d.refs().AuthFile
	ret := &protocol.ManagerRetireReq{Binding: runtimeBinding(old), Generation: 1, Checkpoint: hashBytes([]byte("checkpoint"))}
	if err := store.update(func(st *State) error {
		c := st.Conversations[cfg.ConversationID]
		c.Parked, c.Retirement = true, ret
		putRecord(st, "conversations", &st.Conversations, cfg.ConversationID, c)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	attempt := func() (ManagerBinding, error) {
		st, err := store.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		return d.wakeParked(context.Background(), st.Generation, st.Conversations[cfg.ConversationID])
	}
	r.retirement.State = "uncertain"
	if _, err := attempt(); !errors.Is(err, ErrDiscussionActivationUnavailable) || len(r.requests) != 0 {
		t.Fatalf("unproven migration launched: %v", err)
	}
	r.retirement.State = "retired"
	next, err := attempt()
	if err != nil || !next.NativeCLI || next.NativeSessionID == old.NativeSessionID || len(r.requests) != 1 || r.requests[0].ResumeSourceInstance != "" {
		t.Fatalf("migration %+v, launches %+v: %v", next, r.requests, err)
	}
	st, err := store.Snapshot()
	if err != nil || st.Conversations[cfg.ConversationID].Parked || !st.Conversations[cfg.ConversationID].NeedsHandoff || len(st.Publications) != 1 {
		t.Fatalf("migration lost context handoff: %v", err)
	}
}
