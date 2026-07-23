package protocol

// FAILING-FIRST protocol tests for remote kill/delete idempotency (slice DHI-3): a
// REPLAYED signed remote kill/delete must execute the daemon side effect exactly ONCE
// and return the ORIGINAL attempt's cached outcome on the replay, so a captured-and-
// replayed remote command can never double-execute (double-fire OnSessionEnd, append a
// duplicate `deleted` tombstone, or re-signal a possibly-reused PID). Today handleKill/
// handleDelete call d.Kill/d.Delete directly with no operation_id dedup, so a replay
// double-fires — this pins the fix.
//
// RED is undefined-only: this file does not compile because the production symbols it
// pins do not exist yet.
//
// FROZEN API the GREEN implementer adds:
//
//	// internal/protocol/types.go — a NEW additive optional interface (NOT OperationClaimer,
//	// whose existed=>refuse is WRONG for kill/delete: a replay must return cached SUCCESS).
//	type IdempotentExecutor interface {
//	    // Fresh op: existed=false -> caller executes then CommitIdempotentOp.
//	    // Replayed op: existed=true, priorOK reports whether the ORIGINAL attempt
//	    // COMPLETED (true) or FAILED (false); the caller returns that cached outcome
//	    // and executes nothing.
//	    ClaimIdempotentOp(operationID, action, session string) (existed, priorOK bool, err error)
//	    CommitIdempotentOp(operationID string, ok bool) error
//	}
//
//	// handleKill/handleDelete: after requireRemoteAuthz, when the backend implements
//	// IdempotentExecutor, Claim the operation_id; a replay (existed) replies the CACHED
//	// outcome WITHOUT calling Kill/Delete; a fresh op executes then Commits(err==nil) and
//	// replies accordingly. Kill/Delete signatures are UNCHANGED (owner-tier calls untouched).

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

// idempotentStub is a remote-tier DaemonAPI (via the embedded *stubDaemon, so it is
// ALSO a DeviceAuthenticator that accepts by default) that additionally implements the
// expected IdempotentExecutor over an in-memory operation log. It models the durable
// store's cached-outcome contract: a fresh operation_id claims (existed=false) and is
// later committed with its terminal outcome; a replay of a COMMITTED op returns
// existed=true and the original attempt's ok. An uncommitted (in-flight) record replays
// as existed=false — a mid-op crash is self-idempotent to re-run for kill/delete,
// mirroring the daemon mapping of the prepared/executing phases.
type idempotentStub struct {
	*stubDaemon
	mu  sync.Mutex
	ops map[string]*idemRec
}

type idemRec struct {
	committed bool
	ok        bool
}

func newIdempotentStub() *idempotentStub {
	return &idempotentStub{stubDaemon: newStubDaemon(), ops: make(map[string]*idemRec)}
}

// Compile-time proof the stub satisfies the expected IdempotentExecutor (undefined
// until GREEN adds the interface; mirrors the DeviceRevoker compile-time proof in
// remote_devicerevoke_test.go).
var _ IdempotentExecutor = (*idempotentStub)(nil)

func (s *idempotentStub) ClaimIdempotentOp(operationID, action, session string) (existed, priorOK bool, err error) {
	// Mirror the durable store (internal/idempotency Prepare): an empty operation_id is
	// refused. This is what makes an OWNER-tier call that wrongly enters the idempotency
	// block surface the production "idempotency: empty operation_id" regression.
	if operationID == "" {
		return false, false, errors.New("idempotency: empty operation_id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec, ok := s.ops[operationID]; ok {
		if !rec.committed {
			return false, false, nil // in-flight (prepared/executing): safe to re-run
		}
		return true, rec.ok, nil // replay of a committed op: return its cached outcome
	}
	s.ops[operationID] = &idemRec{} // fresh: prepared, not yet committed
	return false, false, nil
}

func (s *idempotentStub) CommitIdempotentOp(operationID string, ok bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.ops[operationID]
	if rec == nil {
		rec = &idemRec{}
		s.ops[operationID] = rec
	}
	rec.committed = true
	rec.ok = ok
	return nil
}

// TestProtocol_ReplayedKillExecutesOnce: the SAME signed remote kill Control sent TWICE
// executes the daemon Kill exactly once; the replay returns the original's cached OK
// (same session id) without re-actioning the daemon.
func TestProtocol_ReplayedKillExecutesOnce(t *testing.T) {
	stub := newIdempotentStub()
	sock := serveRemoteAPI(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"

	exp := time.Now().Add(time.Minute)
	kill := Control{
		Op: OpKill, EndpointID: rep.EndpointID, SessionID: sid,
		OperationID: "devA:01JKILLDUP000000000000000",
		DeviceID:    "devA", DeviceSig: "sig", ExpiresAt: &exp,
	}

	// First send: fresh op -> executes and replies OK.
	rc.writeControl(kill)
	first := rc.readControl()
	if first.Op != OpOK {
		t.Fatalf("first kill = op %q code %q; want ok", first.Op, first.ErrorCode)
	}
	// Second send: replayed op (same operation_id) -> cached OK, no second execution.
	rc.writeControl(kill)
	second := rc.readControl()
	if second.Op != OpOK {
		t.Fatalf("replayed kill = op %q code %q; want cached ok (no double-execute)", second.Op, second.ErrorCode)
	}

	if first.SessionID != sid || second.SessionID != sid {
		t.Fatalf("kill reply session ids = %q then %q; want both %q", first.SessionID, second.SessionID, sid)
	}
	if killed := stub.killedIDs(); len(killed) != 1 {
		t.Fatalf("daemon executed %d kills for a replayed op; want exactly 1 (idempotent replay)", len(killed))
	}
}

// TestProtocol_ReplayedDeleteExecutesOnce: the SAME signed remote delete Control sent
// TWICE executes the daemon Delete exactly once; the replay returns the original's
// cached OK without re-actioning the daemon (no duplicate tombstone / OnSessionEnd).
func TestProtocol_ReplayedDeleteExecutesOnce(t *testing.T) {
	stub := newIdempotentStub()
	sock := serveRemoteAPI(t, stub)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})
	sid := rep.EndpointID + "/sess1"

	exp := time.Now().Add(time.Minute)
	del := Control{
		Op: OpDelete, EndpointID: rep.EndpointID, SessionID: sid,
		OperationID: "devA:01JDELETEDUP0000000000000",
		DeviceID:    "devA", DeviceSig: "sig", ExpiresAt: &exp,
	}

	// First send: fresh op -> executes and replies OK.
	rc.writeControl(del)
	first := rc.readControl()
	if first.Op != OpOK {
		t.Fatalf("first delete = op %q code %q; want ok", first.Op, first.ErrorCode)
	}
	// Second send: replayed op (same operation_id) -> cached OK, no second execution.
	rc.writeControl(del)
	second := rc.readControl()
	if second.Op != OpOK {
		t.Fatalf("replayed delete = op %q code %q; want cached ok (no double-execute)", second.Op, second.ErrorCode)
	}

	if first.SessionID != sid || second.SessionID != sid {
		t.Fatalf("delete reply session ids = %q then %q; want both %q", first.SessionID, second.SessionID, sid)
	}
	if deleted := stub.deletedIDs(); len(deleted) != 1 {
		t.Fatalf("daemon executed %d deletes for a replayed op; want exactly 1 (idempotent replay)", len(deleted))
	}
}

// TestProtocol_OwnerTierKillDeleteWithoutOperationID pins the OWNER-TIER property that a
// local kill/delete over the owner socket (which carries NO operation_id) SUCCEEDS even
// when the backend implements IdempotentExecutor (the production coreAPI does). DHI-3's
// replay-dedup is a REMOTE-tier concern: on the owner tier requireRemoteAuthz is a
// pass-through and local calls carry no operation_id, so the idempotency claim (which
// rejects an empty operation_id) must NOT run. Regression guard for the "empty
// operation_id" error surfacing on owner-tier c.Kill/c.Delete (a cross-slice regression
// that broke TestE2E_ResumeAsNewSession_R2 and TestE2E_Worktree_LaunchRunTeardown).
func TestProtocol_OwnerTierKillDeleteWithoutOperationID(t *testing.T) {
	stub := newIdempotentStub()
	stub.setMetas(persist.Meta{
		ID:        "sess1",
		AgentType: "claude",
		Cwd:       "/tmp",
		Status:    status.Status{Process: status.ProcessRunning, Turn: status.TurnActive, Interaction: status.InteractionNone},
	})
	sock := serveOwnerAPI(t, stub) // owner tier: remoteTier == false
	c := dialClient(t, sock, nil)
	id := onlyViewID(t, c)

	// Owner-tier kill: no operation_id. Must execute and reply OK, NOT error with
	// "empty operation_id" from the idempotency store.
	if err := c.Kill(id); err != nil {
		t.Fatalf("owner-tier Kill with empty operation_id: %v; want success (idempotency must not gate the owner tier)", err)
	}
	if got := stub.killedIDs(); len(got) != 1 || got[0] != "sess1" {
		t.Fatalf("daemon Kill received %v, want [sess1] exactly once", got)
	}

	// Owner-tier delete: same property.
	if err := c.Delete(id); err != nil {
		t.Fatalf("owner-tier Delete with empty operation_id: %v; want success", err)
	}
	if got := stub.deletedIDs(); len(got) != 1 || got[0] != "sess1" {
		t.Fatalf("daemon Delete received %v, want [sess1] exactly once", got)
	}
}
