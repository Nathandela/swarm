package voice

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/managerproof"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/status"
)

type deletedActiveManagerRuntime struct {
	discussionTestRuntime
	receipt string
	wrongID bool
}

func (r *deletedActiveManagerRuntime) ManagerRetire(ctx context.Context, req protocol.ManagerRetireReq, statusOnly bool) (protocol.ManagerRetireReceipt, error) {
	receipt, err := r.discussionTestRuntime.ManagerRetire(ctx, req, statusOnly)
	receipt.State = r.receipt
	if r.wrongID {
		receipt.OperationID = "wrong-operation"
	}
	return receipt, err
}

func TestDiscussionRecoversOnlyProvenDeletedActiveOrRetiringOwner(t *testing.T) {
	audienceLost := errors.New("discussion audience unavailable")
	for _, tc := range []struct {
		name, receipt, retirement string
		wrongID, competing        bool
		ownerListed, noAudience   bool
		want                      error
	}{
		{name: "active", receipt: "deleted"},
		{name: "normal retirement after restart", receipt: "deleted", retirement: "idle"},
		{name: "replacement in progress after restart", receipt: "deleted", retirement: schema.ManagerReplaceCustody},
		{name: "legacy exit cannot replace active dispatcher", receipt: "deleted_parked", want: ErrControllerUncertain},
		{name: "legacy exit during incomplete retirement", receipt: "deleted_parked", retirement: "idle", want: ErrControllerUncertain},
		{name: "missing without proof", receipt: "unknown", want: ErrControllerUncertain},
		{name: "retired without roster proof", receipt: "retired", want: ErrControllerUncertain},
		{name: "wrong operation", receipt: "deleted", wrongID: true, want: ErrControllerUncertain},
		{name: "competing owner", receipt: "deleted", competing: true, want: errCompetingManagers},
		{name: "competing owner during retirement", receipt: "deleted", retirement: "idle", competing: true, want: errCompetingManagers},
		{name: "old owner still visible", receipt: "deleted", retirement: "idle", ownerListed: true, want: ErrControllerUncertain},
		{name: "unverified audience", receipt: "deleted", noAudience: true, want: audienceLost},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, store, native, b := newControllerFixture(t)
			r := &deletedActiveManagerRuntime{
				discussionTestRuntime: discussionTestRuntime{controllerRuntimeFixture: native, managerErr: managerproof.ErrUnavailable},
				receipt:               tc.receipt, wrongID: tc.wrongID,
			}
			cfg := testConfig()
			cfg.ConversationID = b.ConversationID
			d := &DiscussionController{controller: c, store: store, runtime: r, cfg: cfg, now: time.Now}
			c.discussion = &DiscussionScope{InstallationID: "install", GuildID: "guild", ThreadID: "thread"}
			c.runtime = r
			if tc.noAudience {
				d.scope.VerifyAudience = func(context.Context) error { return audienceLost }
			}
			a, err := c.submitDiscussion(discussionFixtureOrigin("pending"), "Check the original work", "", "")
			if err != nil {
				t.Fatal(err)
			}
			var oldRetirement *protocol.ManagerRetireReq
			if tc.retirement != "" {
				req := protocol.ManagerRetireReq{Binding: runtimeBinding(b), Generation: 1, Checkpoint: hashValue("existing"), ExpectedTurn: protocol.ManagerNativeTurn{State: "idle"}}
				if tc.retirement != "idle" {
					req.Replacement, req.ExpectedTurn.State = tc.retirement, "unknown"
				}
				oldRetirement = &req
			}
			worker := WorkerAssociation{ConversationID: b.ConversationID, Session: "machine/worker", SessionInstance: "incarnation", JobID: "job", AttemptID: "attempt", Admission: a.Sequence}
			action := Action{ConversationID: b.ConversationID, ID: "effect", Admission: a.Sequence, State: "executing", OperationID: "existing-operation", Request: json.RawMessage(`{"tool":"swarm_create"}`)}
			if err := store.update(func(st *State) error {
				conv := st.Conversations[b.ConversationID]
				conv.Retirement = oldRetirement
				putRecord(st, "conversations", &st.Conversations, b.ConversationID, conv)
				putRecord(st, "workers", &st.Workers, key(b.ConversationID, key(worker.Session, worker.SessionInstance)), worker)
				putRecord(st, "actions", &st.Actions, key(b.ConversationID, action.ID), action)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if tc.competing {
				r.rows = []protocol.SessionView{{ID: "machine/other", Role: "manager", ManagerConversationID: b.ConversationID, Status: status.Status{Process: status.ProcessRunning}}}
			}
			if tc.ownerListed {
				r.rows = []protocol.SessionView{{ID: b.SessionID, Role: "manager", ManagerConversationID: b.ConversationID, Status: status.Status{Process: status.ProcessRunning}}}
			}
			if oldRetirement == nil {
				if tc.want == nil {
					err = d.Sync(t.Context(), func(DiscussionOutput) error { return nil })
				} else {
					err = d.recoverActive(t.Context(), managerproof.ErrUnavailable)
				}
			} else {
				d.controller = nil
				err = d.finishRetirement(t.Context(), *oldRetirement, true)
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			st, err := store.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			conv := st.Conversations[b.ConversationID]
			if tc.want == nil {
				if !conv.Parked || conv.Retirement == nil || conv.Retirement.Replacement != schema.ManagerReplaceExited || conv.Retirement.Binding != runtimeBinding(b) || len(r.retirements) != 1 || !r.retirements[0] {
					t.Fatalf("deleted owner not parked by the exact status proof: %+v, calls %v", conv, r.retirements)
				}
			} else if conv.Parked || (conv.Retirement == nil) != (oldRetirement == nil) || conv.Retirement != nil && *conv.Retirement != *oldRetirement {
				t.Fatalf("unproven or competing owner changed the durable claim: %+v", conv)
			}
			retained := st.Admissions[admissionKey(b.ConversationID, a.Sequence)]
			if retained.Origin == nil || retained.Origin.RequestID != "pending" || retained.Phase != "" || !retained.SettledAt.IsZero() || len(native.sent) != 0 ||
				st.Workers[key(b.ConversationID, key(worker.Session, worker.SessionInstance))] != worker || st.Actions[key(b.ConversationID, action.ID)].OperationID != action.OperationID {
				t.Fatal("recovery changed or replayed the queued request, worker, or outgoing effect")
			}
		})
	}
}

func TestMissingManagerWireProofRecoversPausedDiscussion(t *testing.T) {
	c, store, _, b := newControllerFixture(t)
	socket := controllerWireServer(t, "machine", func(req protocol.Control) protocol.Control {
		switch req.Op {
		case protocol.OpManagerRuntime:
			return protocol.Control{Op: protocol.OpError, ErrorCode: protocol.CodeUnavailable, Error: "manager runtime proof is unavailable"}
		case protocol.OpList:
			return protocol.Control{Op: protocol.OpList}
		case protocol.OpManagerRetireStatus:
			if req.ManagerRetireRequest == nil || req.ManagerRetireRequest.Binding != runtimeBinding(b) || req.ManagerRetireRequest.Replacement != schema.ManagerReplaceExited {
				t.Errorf("unexpected proof request: %+v", req.ManagerRetireRequest)
			}
			return protocol.Control{Op: req.Op, SessionID: req.SessionID, OperationID: req.OperationID, ManagerRetire: &protocol.ManagerRetireReceipt{OperationID: req.OperationID, State: "deleted"}}
		default:
			t.Errorf("unexpected supervisor request: %s", req.Op)
			return protocol.Control{Op: protocol.OpError, ErrorCode: protocol.CodeUnavailable}
		}
	})
	rt, err := NewProtocolControllerClient(t.Context(), socket, "machine")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = rt.ManagerRuntime(b.SessionID, b.SessionInstance); !errors.Is(err, managerproof.ErrUnavailable) {
		t.Fatalf("wire missing-manager result: %v", err)
	}
	c.runtime = rt
	c.discussion = &DiscussionScope{InstallationID: "install", GuildID: "guild", ThreadID: "thread"}
	a, err := c.submitDiscussion(discussionFixtureOrigin("pending"), "Check the original work", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.update(func(st *State) error {
		conv := st.Conversations[b.ConversationID]
		conv.SchedulerPaused = true
		putRecord(st, "conversations", &st.Conversations, b.ConversationID, conv)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig()
	cfg.ConversationID = b.ConversationID
	d := &DiscussionController{controller: c, store: store, runtime: rt, cfg: cfg, now: time.Now}
	if err = d.Sync(t.Context(), func(DiscussionOutput) error { return nil }); err != nil {
		t.Fatal(err)
	}
	st, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	conv := st.Conversations[b.ConversationID]
	if !conv.Parked || conv.Retirement == nil || conv.Retirement.Replacement != schema.ManagerReplaceExited || st.Admissions[admissionKey(b.ConversationID, a.Sequence)].Phase != "" {
		t.Fatalf("missing-owner check did not preserve the held request and park the deleted owner: %+v", conv)
	}
}
