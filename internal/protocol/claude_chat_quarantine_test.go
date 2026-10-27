package protocol

import (
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

// Protocol consumes an already-validated metadata row; structural validation is
// tested separately by the metadata owner, never supplied by this fake API.
func TestClaudeChatRawOwnerRemoteAndDirectPathsRefuseBeforeEffects(t *testing.T) {
	for _, tier := range []string{"owner", "remote"} {
		t.Run(tier, func(t *testing.T) {
			d := newSendInputDaemon()
			m := statusMeta("sess1", status.TurnIdle, status.InteractionNone)
			m.SchemaVersion = 6
			m.RequiredRuntime = "claude-chat"
			d.setMetas(m)
			var socket string
			if tier == "owner" {
				socket = serveOwnerAPI(t, d)
			} else {
				socket = serveRemoteAPI(t, d)
			}
			rc := rawDial(t, socket)
			hello := rc.hello(Version, nil)
			sid := hello.EndpointID + "/sess1"
			for _, op := range []string{OpAttach, OpSendInput} {
				control := Control{Op: op, EndpointID: hello.EndpointID, SessionID: sid}
				if op == OpSendInput {
					control.SendInput = &SendInputReq{Text: "exact blocked input", Submit: true}
				}
				rc.writeControl(control)
				reply := nextControl(t, rc)
				if reply.Op != OpError {
					t.Errorf("%s %s accepted unsupported runtime: %s", tier, op, reply.Op)
				}
			}
			if d.attachCount() != 0 {
				t.Errorf("%s opened %d raw streams", tier, d.attachCount())
			}
		})
	}
}

type a2ReadOnlyQuarantineAPI struct {
	*sendInputDaemon
	retained string
}

func (d *a2ReadOnlyQuarantineAPI) ClaudeChatQuarantined(local string) bool {
	return d.retained != "" && d.retained == local
}

func TestClaudeChatCachedLeaseFinalInputAndResizeRefuse(t *testing.T) {
	d := &a2ReadOnlyQuarantineAPI{sendInputDaemon: newSendInputDaemon()}
	m := statusMeta("sess1", status.TurnIdle, status.InteractionNone)
	d.setMetas(m)
	srv := NewServer(d, "a2")
	t.Cleanup(func() { _ = srv.Close() })
	stream := newTimedStream()
	cc := &clientConn{}
	lease := &sessionLease{controller: cc, genCounter: 1, stream: stream}
	srv.mu.Lock()
	srv.leases[m.ID] = lease
	srv.mu.Unlock()
	// Establish the same captured lease genuinely writes for ordinary metadata.
	srv.forwardInput(cc, m.ID, 1, []byte("ordinary"))
	genericCalls := 0
	srv.SetInputGateFunc(func(string) error { genericCalls++; return errors.New("unrelated input fence") })
	srv.forwardResize(cc, m.ID, 1, 90, 30)
	if genericCalls != 0 {
		t.Error("ordinary resize invoked the side-effecting generic input gate")
	}
	srv.SetInputGateFunc(nil)
	if len(stream.written()) != 1 {
		t.Fatal("ordinary lease input control did not write")
	}
	stream.mu.Lock()
	resizes := len(stream.resizes)
	stream.mu.Unlock()
	if resizes != 1 {
		t.Fatal("ordinary resize control did not write")
	}
	m.SchemaVersion = 6
	m.RequiredRuntime = "claude-chat"
	d.setMetas(m)
	srv.forwardInput(cc, m.ID, 1, []byte("blocked"))
	srv.forwardResize(cc, m.ID, 1, 91, 31)
	stream.mu.Lock()
	writes, afterResize := len(stream.writes), len(stream.resizes)
	stream.mu.Unlock()
	if writes != 1 || afterResize != 1 {
		t.Errorf("cached lease bypassed mode: writes=%d resizes=%d", writes, afterResize)
	}
	// Assembly remembers recognized mode independently of a later missing row.
	if !persist.IsClaudeChatRuntime(m) {
		t.Fatal("sticky-query fixture did not capture chat mode")
	}
	d.retained = m.ID
	d.setMetas()
	srv.forwardInput(cc, m.ID, 1, []byte("missing-row blocked"))
	srv.forwardResize(cc, m.ID, 1, 92, 32)
	stream.mu.Lock()
	writes, afterResize = len(stream.writes), len(stream.resizes)
	stream.mu.Unlock()
	if writes != 1 || afterResize != 1 {
		t.Errorf("missing row bypassed read-only sticky query: writes=%d resizes=%d", writes, afterResize)
	}
}

func TestClaudeChatSendInputRechecksModeAfterInputLock(t *testing.T) {
	d := &pastingDaemon{stubDaemon: newStubDaemon()}
	m := statusMeta("sess1", status.TurnIdle, status.InteractionNone)
	d.setMetas(m)
	srv := NewServer(d, "a2")
	lease := &sessionLease{}
	srv.mu.Lock()
	srv.leases[m.ID] = lease
	srv.mu.Unlock()
	lease.inMu.Lock()
	locked, joined := true, false
	done := make(chan error, 1)
	go func() { done <- srv.SendInput(m.ID, SendInputReq{Text: "blocked paste", Submit: true}) }()
	t.Cleanup(func() {
		if locked {
			lease.inMu.Unlock()
		}
		if !joined {
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Error("sender did not join")
			}
		}
		_ = srv.Close()
	})
	deadline := time.Now().Add(2 * time.Second)
	for lease.attachMu.TryLock() {
		lease.attachMu.Unlock()
		select {
		case err := <-done:
			joined = true
			t.Fatalf("sender did not reach captured lease: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("sender never acquired attach lock")
		}
		runtime.Gosched()
	}
	m.SchemaVersion = 6
	m.RequiredRuntime = "claude-chat"
	d.setMetas(m)
	lease.inMu.Unlock()
	locked = false
	select {
	case err := <-done:
		joined = true
		if err == nil {
			t.Error("mode changed under input lock but paste accepted")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("sender did not finish")
	}
	d.mu.Lock()
	opened := len(d.opened)
	d.mu.Unlock()
	if opened != 0 {
		t.Errorf("final mode fence opened %d streams", opened)
	}
}

func TestClaudeChatPerFrameRuntimeQueryDoesNotRepeatInputGateEffects(t *testing.T) {
	d := newSendInputDaemon()
	d.setMetas(statusMeta("sess1", status.TurnIdle, status.InteractionNone))
	srv := NewServer(d, "a2")
	t.Cleanup(func() { _ = srv.Close() })
	calls := 0
	srv.SetInputGateFunc(func(string) error { calls++; return nil })
	if err := srv.SendInput("sess1", SendInputReq{Text: "ordinary"}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("per-frame runtime query repeated generic input effects: calls=%d want optimistic+locked checks", calls)
	}
	if len(d.onlyStream(t).written()) != 1 {
		t.Error("ordinary message was not written exactly once")
	}
}

// Models quarantine recognition exactly as Attach returns, not a supported
// mutation of immutable persisted runtime metadata.
type a2QuarantineAtAttachAPI struct {
	*sendInputDaemon
	capture  bool
	retained string
}

func (d *a2QuarantineAtAttachAPI) Attach(local string) (SessionStream, error) {
	stream, err := d.sendInputDaemon.Attach(local)
	if err == nil && d.capture {
		d.retained = local
	}
	return stream, err
}

func (d *a2QuarantineAtAttachAPI) ClaudeChatQuarantined(local string) bool {
	return d.retained != "" && d.retained == local
}

func TestClaudeChatFinalQueryPrecedesDraftMarkerAfterAttach(t *testing.T) {
	for _, capture := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "quarantine-at-attach-return"}[capture], func(t *testing.T) {
			d := &a2QuarantineAtAttachAPI{sendInputDaemon: newSendInputDaemon(), capture: capture}
			d.setMetas(statusMeta("sess1", status.TurnIdle, status.InteractionNone))
			srv := NewServer(d, "a2")
			t.Cleanup(func() { _ = srv.Close() })
			markers := 0
			srv.SetDirectInputObserver(func(string, DirectInputClass) error {
				if len(d.onlyStream(t).written()) != 0 {
					t.Error("draft marker ran after native input write")
				}
				markers++
				return nil
			})
			err := srv.SendInput("sess1", SendInputReq{Text: "exact ordinary message"})
			stream := d.onlyStream(t)
			writes := len(stream.written())
			if capture {
				if !errors.Is(err, errClaudeChatRaw) || markers != 0 || writes != 0 {
					t.Errorf("late quarantine caused effects: err=%v markers=%d writes=%d", err, markers, writes)
				}
			} else if err != nil || markers != 1 || writes != 1 {
				t.Errorf("ordinary control failed: err=%v markers=%d writes=%d", err, markers, writes)
			}
			stream.mu.Lock()
			closed := stream.closed
			stream.mu.Unlock()
			if !closed {
				t.Error("temporary input stream was not closed")
			}
		})
	}
}
