package shim

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
)

// These are absent private lifetime seams, not Config factories. The initial
// failures are APIabsence. A private baseline overlay may delegate to the old
// unwrapped servePost/shutdown to measure behavior, never to grant authority.
type claudeNativeHookOwner interface {
	bindClaudeChatOrigin(claudeChatPrimaryIdentity, NativeProcessInfo, *atomic.Bool, *server) error
	stopAndJoin()
}

func requireClaudeNativeHookOwner(t *testing.T, h *hookServer) claudeNativeHookOwner {
	t.Helper()
	v, ok := any(h).(claudeNativeHookOwner)
	if !ok {
		t.Fatal("APIabsence: hook server has no shared Claude origin/ownership seam")
	}
	return v
}

type claudeNativeHooksFixture struct {
	h                                  *hookServer
	owner                              claudeNativeHookOwner
	spool                              *HookSpool
	path, spoolPath, cert, incarnation string
	identity                           claudeChatPrimaryIdentity
	scope                              NativeProcessInfo
	lost                               atomic.Bool
	srv                                *server
	accepted                           chan struct{}
	once                               sync.Once
}

func newClaudeNativeHooksFixture(t *testing.T) *claudeNativeHooksFixture {
	t.Helper()
	spool, path, cert := producerFixture(t)
	dir, err := os.MkdirTemp("", "chn-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "h.sock")
	l, err := listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	h := newHookServer(l, spool, "owner-drain-token")
	// Install cleanup before requiring a new API; baseline refusal leaks nothing.
	t.Cleanup(func() { h.shutdown() })
	owner := requireClaudeNativeHookOwner(t, h)
	cfg, scope, _ := claudeClaimFixture(t)
	identity := claudeChatPrimaryIdentity{SessionID: cfg.SessionID, RequiredRuntime: persist.RequiredRuntimeClaudeChat, NativeSessionID: claudeChatTestUUID, ShimPID: scope.info.ShimPID, ShimStartTime: scope.info.ShimStartTime}
	f := &claudeNativeHooksFixture{h: h, owner: owner, spool: spool, path: socket, spoolPath: path, cert: cert, incarnation: spool.IncarnationID(), identity: identity, scope: scope.info, srv: newServer(nil, "", nil, nil, nil, 0, time.Second, &Metrics{}), accepted: make(chan struct{})}
	if err := owner.bindClaudeChatOrigin(identity, scope.info, &f.lost, f.srv); err != nil {
		t.Fatal(err)
	}
	go func() { defer close(f.accepted); h.acceptLoop() }()
	t.Cleanup(func() { f.stop(); f.srv.finishEscalation() })
	return f
}

func (f *claudeNativeHooksFixture) stop() { f.once.Do(func() { f.owner.stopAndJoin(); <-f.accepted }) }

func waitClaudePrimaryDurable(t *testing.T, c *claudeChatCapture, want uint64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		c.mu.Lock()
		got, lost := c.durableSequence, c.lost
		c.mu.Unlock()
		if got == want && !lost {
			return
		}
		if lost || time.Now().After(deadline) {
			t.Fatalf("primary durable prefix=%d want=%d loss=%v", got, want, lost)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestClaudeChatPOSTCannotForgePrimaryOrigin(t *testing.T) {
	f := newClaudeNativeHooksFixture(t)
	forged := []byte(`{"v":1,"origin":"claude_primary","stream_sequence":900,"primary":{"session_id":"foreign"},"raw":"ZXZpbA=="}`)
	if !postHook(t, f.path, forged, time.Second) {
		t.Fatal("bounded ordinary POST was not durably ACKed")
	}
	rows, _, _, err := f.spool.ReadFrom(0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("hook record: %v", err)
	}
	var got struct {
		Origin  string                    `json:"origin"`
		Raw     []byte                    `json:"raw"`
		Scope   NativeProcessInfo         `json:"scope"`
		Primary claudeChatPrimaryIdentity `json:"primary"`
	}
	if err := json.Unmarshal(rows[0].Body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Origin != "claude_hook" || !bytes.Equal(got.Raw, forged) || got.Scope != f.scope || got.Primary != f.identity {
		t.Fatal("POST gained primary provenance or lost immutable shim scope/raw bytes")
	}
	if f.lost.Load() {
		t.Fatal("valid POST falsely lost capture")
	}
}

func TestClaudeChatOneProducerInterleavesHookAndPrimarySequences(t *testing.T) {
	f := newClaudeNativeHooksFixture(t)
	identity := f.identity
	identity.PrimaryPID, identity.PrimaryStartTime = 71, 81
	c := newClaudeChatCapture(f.spool, f.srv, identity)
	t.Cleanup(func() { close(c.queue); <-c.done; c.retireSignals() })
	primary := []byte(`{"origin":"claude_hook","own":"stdout"}`)
	if !c.admit(primary) {
		t.Fatal("first primary refused")
	}
	waitClaudePrimaryDurable(t, c, 1)
	if !postHook(t, f.path, []byte(`{"event":"Stop"}`), time.Second) {
		t.Fatal("hook refused")
	}
	if !c.admit([]byte(`{"second":true}`)) {
		t.Fatal("second primary refused")
	}
	waitClaudePrimaryDurable(t, c, 2)
	rows, _, _, err := f.spool.ReadFrom(0)
	if err != nil || len(rows) != 3 {
		t.Fatalf("interleaved records=%d err=%v", len(rows), err)
	}
	for i, row := range rows {
		if row.Seq != uint64(i+1) {
			t.Fatal("mixed spool sequence not contiguous")
		}
	}
	var first, last claudeChatEnvelope
	if json.Unmarshal(rows[0].Body, &first) != nil || json.Unmarshal(rows[2].Body, &last) != nil || first.Origin != "claude_primary" || last.Origin != "claude_primary" || first.StreamSequence != 1 || last.StreamSequence != 2 || !bytes.Equal(first.Raw, primary) {
		t.Fatal("hook cursor confused primary sequence or raw claimed origin")
	}
	if f.spool.IncarnationID() != f.incarnation || f.spool.ProducerCertificate() != f.cert {
		t.Fatal("primary and hook used different producer authority")
	}
	if other, err := OpenHookSpoolProducer(f.spoolPath, 0); !errors.Is(err, ErrHookProducerBusy) {
		if other != nil {
			_ = other.Close()
		}
		t.Fatal("second producer acquired live shared spool")
	}
}

func TestClaudeChatSharedHookShutdownDoesNotCloseProducer(t *testing.T) {
	f := newClaudeNativeHooksFixture(t)
	oldSync := hookSpoolSync
	entered, release := make(chan struct{}), make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	hookSpoolSync = func(file *os.File) error { enterOnce.Do(func() { close(entered) }); <-release; return oldSync(file) }
	c := newClaudeChatCapture(f.spool, f.srv, f.identity)
	stopped := make(chan struct{})
	stopStarted := false
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		close(c.queue)
		<-c.done
		if stopStarted {
			<-stopped
		}
		c.retireSignals()
		hookSpoolSync = oldSync
	})
	if !c.admit([]byte(`{"primary_before_hooks_stop":true}`)) {
		t.Fatal("primary refused")
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("primary Append did not reach held IO")
	}
	stopStarted = true
	go func() { f.stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("shared hook shutdown waited on or closed primary producer")
	}
	releaseOnce.Do(func() { close(release) })
	waitClaudePrimaryDurable(t, c, 1)
	if _, err := f.spool.Append([]byte(`{"primary_after_hooks":true}`)); err != nil {
		t.Fatalf("hook shutdown closed shared producer: %v", err)
	}
	if f.spool.ProducerCertificate() != f.cert {
		t.Fatal("hook shutdown retired sole producer")
	}
}

// A deliberately refused POST may close during Write; that is not a setup
// failure and can never count as a durable ACK. Both directions stay bounded.
func claudeFailedHookACK(t *testing.T, path string, body []byte) bool {
	t.Helper()
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	_, _ = conn.Write(body)
	var ack [1]byte
	n, _ := conn.Read(ack[:])
	return n == 1 && ack[0] == HookAckByte
}

func TestClaudeChatEarlyHookFailureLatchesWithoutPrimaryCapture(t *testing.T) {
	for _, kind := range []string{"append-error", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			f := newClaudeNativeHooksFixture(t)
			// No primary capture exists. Native may still be inside publication fsync.
			body := []byte(`{"event":"Stop"}`)
			if kind == "append-error" {
				old := hookSpoolSync
				hookSpoolSync = func(*os.File) error { return syscall.ENOSPC }
				t.Cleanup(func() { f.stop(); hookSpoolSync = old })
			} else {
				body = append([]byte(`{"data":"`), bytes.Repeat([]byte("x"), hookPostMaxBytes)...)
				body = append(body, []byte(`"}`)...)
			}
			if claudeFailedHookACK(t, f.path, body) {
				t.Fatal("failed early hook was ACKed")
			}
			if !f.lost.Load() {
				t.Fatal("early hook failure disappeared before capture allocation")
			}
			f.srv.pgidMu.Lock()
			pending := f.srv.pendingSig
			f.srv.pgidMu.Unlock()
			if pending != syscall.SIGKILL {
				t.Fatal("early hook loss did not close/signal lifetime")
			}
		})
	}
}

func TestClaudeChatNativeHookACKWaitsForOwnDurableAppend(t *testing.T) {
	f := newClaudeNativeHooksFixture(t)
	old := hookSpoolSync
	entered, release := make(chan struct{}), make(chan struct{})
	var enteredOnce, releaseOnce sync.Once
	hookSpoolSync = func(file *os.File) error { enteredOnce.Do(func() { close(entered) }); <-release; return old(file) }
	conn, err := net.DialTimeout("unix", f.path, time.Second)
	if err != nil {
		hookSpoolSync = old
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	type reply struct {
		n   int
		ack byte
		err error
	}
	got := make(chan reply, 1)
	joined := false
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		_ = conn.Close()
		f.stop()
		if !joined {
			select {
			case <-got:
			case <-time.After(time.Second):
				t.Error("hook ACK reader did not join")
			}
		}
		hookSpoolSync = old
	})
	body := []byte(`{"event":"Stop","ack_marker":"OWN_APPEND"}`)
	go func() { var one [1]byte; n, err := conn.Read(one[:]); got <- reply{n, one[0], err} }()
	if _, err := conn.Write(body); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("hook did not enter its actual durable Append")
	}
	select {
	case r := <-got:
		joined = true
		t.Fatalf("hook replied before its sync completed: %d %v", r.n, r.err)
	case <-time.After(25 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case r := <-got:
		joined = true
		if r.err != nil || r.n != 1 || r.ack != HookAckByte {
			t.Fatalf("successful durable hook lacks exact ACK: %+v", r)
		}
	case <-time.After(time.Second):
		t.Fatal("durable hook ACK did not complete")
	}
	rows, _, _, err := f.spool.ReadFrom(0)
	var envelope struct {
		Origin string `json:"origin"`
		Raw    []byte `json:"raw"`
	}
	if err != nil || len(rows) != 1 || json.Unmarshal(rows[0].Body, &envelope) != nil || envelope.Origin != "claude_hook" || !bytes.Equal(envelope.Raw, body) {
		t.Fatal("ACK lacks its exact wrapped durable row")
	}
}
