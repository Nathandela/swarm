package remotegw

// Slice S11 -- FAILING-FIRST (TDD RED, GG-5) tests for the GATEWAY half of PB-INPUT-2 and
// for PB-TIME-2's producer gap.
//
// PB-INPUT-2 requires that "input is suppressed until a new lease is visibly confirmed",
// across gateway restart, daemon restart, session exit under the user, app backgrounding
// and process death. Three of those five are events the PHONE cannot observe on its own:
// a daemon restart, a session exit and a daemon-side lease expiry all kill the lease conn
// while the phone's relay connection stays perfectly healthy. Today nothing tells it.
// LeaseManager.watch (leasemanager.go:132-139) silently deletes the dead conn from its map
// and LeaseManager.Input then DROPS every subsequent keystroke returning nil
// (leasemanager.go:72-87) -- so the phone types into a void with no error, no reply and no
// signal of any kind. PB-SYNC-7 closed exactly this hole for the take_control REPLY
// ("silence is indistinguishable from a slow grant, which is how a keystroke gets sent
// against a lease that does not exist"); the same argument applies verbatim to a lease that
// is granted and then dies.
//
// THE CONTRACT these tests freeze:
//
//	type SeveredLease struct {
//		Session     string  // namespaced session id whose lease died
//		OperationID string  // the take_control that opened it, so the notice is attributable
//		Generation  uint64  // the DEAD generation, so a supersede cannot kill its replacement
//		Reason      string  // legible cause, for PB-INPUT-2's "visibly"
//	}
//
//	LeaseRouter gains: OnSever(fn func(SeveredLease))
//	*LeaseManager implements it and fires once per lease death.
//	NewCommandBridge registers a handler that seals an OpDetach control reply to the phone.
//
// WHY OnSever IS ON THE INTERFACE and not a method a call site type-asserts for. The
// Generation method carries the same note (command_loop.go:55-59): an optional hook is a
// hook a future refactor drops, and the failure is silent -- the gateway simply stops
// telling the phone, and PB-INPUT-2 regresses to exactly today's behaviour with every test
// still green. On the interface, a router without it does not compile.
//
// DECLARED BLAST RADIUS: adding a method to LeaseRouter turns fakeLeaseRouter
// (mailbox_route_test.go:45) red at every use site until the implementer adds the one
// method. That is the whole of it; no assertion in those files changes.
//
// This file contains NO implementation.

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/wire"
)

// s11Key is the epoch content key every frame in this file is sealed under.
func s11Key() crypto.ContentKey {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 11)
	}
	return key
}

const s11Epoch uint32 = 7

// s11SeverRouter is a LeaseRouter that grants nothing and lets a test fire a severance,
// so the BRIDGE's half can be tested without a daemon.
type s11SeverRouter struct {
	fakeLeaseRouter
	mu   sync.Mutex
	sink func(SeveredLease)
}

func (r *s11SeverRouter) OnSever(fn func(SeveredLease)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sink = fn
}

func (r *s11SeverRouter) fire(s SeveredLease) {
	r.mu.Lock()
	fn := r.sink
	r.mu.Unlock()
	if fn == nil {
		return
	}
	fn(s)
}

// s11OpenReply opens one sealed machine -> phone reply back to its Control.
func s11OpenReply(t *testing.T, key crypto.ContentKey, raw []byte) protocol.Control {
	t.Helper()
	env, err := crypto.ParseEnvelope(raw)
	if err != nil {
		t.Fatalf("parse reply envelope: %v", err)
	}
	plain, err := crypto.OpenMailbox(key, env)
	if err != nil {
		t.Fatalf("open reply envelope: %v", err)
	}
	var rf replyFrame
	if err := json.Unmarshal(plain, &rf); err != nil {
		t.Fatalf("decode reply frame: %v", err)
	}
	if rf.Kind != kindCommandReply {
		t.Fatalf("reply frame kind = %q, want %q -- the phone demuxes on this tag and would swallow it into the session cache (C8)", rf.Kind, kindCommandReply)
	}
	return rf.Control
}

// ---------------------------------------------------------------------------
// PB-TIME-2 -- the producer gap, at the producer
// ---------------------------------------------------------------------------

// TestS11ReplyStamp_SealControlReplyStampsIssuedAt is PB-TIME-2's named defect, asserted
// where it lives. The cross-package proof that this makes REAL machine traffic survive an
// enabled bound is in internal/phonecore/s11_replyage_test.go, for the same reason PB-GW-6's
// was: remotegw's own fixtures cannot prove anything about the real producer.
//
// The control at the end is today's header. Without it, an implementation that stamped a
// constant would pass.
func TestS11ReplyStamp_SealControlReplyStampsIssuedAt(t *testing.T) {
	key := s11Key()

	before := time.Now().Add(-time.Second).UnixMilli()
	raw, err := SealControlReply(key, s11Epoch, 1, protocol.Control{Op: protocol.OpOK, OperationID: "op-1"})
	if err != nil {
		t.Fatalf("SealControlReply: %v", err)
	}
	after := time.Now().Add(time.Second).UnixMilli()

	env, err := crypto.ParseEnvelope(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if env.Header.IssuedAt == 0 {
		t.Fatalf("SealControlReply seals IssuedAt = 0 while relaysink.go:432 stamps every other machine -> phone frame. IssuedAt is AAD-covered, so this is AUTHENTICATED as zero: a bounded-age check on the phone computes ~56 years and refuses every command reply, including the lease confirmation PB-INPUT-2 gates typing on")
	}
	if env.Header.IssuedAt < before || env.Header.IssuedAt > after {
		t.Fatalf("SealControlReply seals IssuedAt = %d, outside [%d, %d]; the stamp must be a wall-clock reading, not a placeholder", env.Header.IssuedAt, before, after)
	}

	// The deliberate SenderKeyID split must survive the change: command replies ride the
	// sender-zero bucket so they do not collide with the journal/terminal seq stream
	// (command_in.go:104-111). A stamp is not a licence to touch the header otherwise.
	if env.Header.SenderKeyID != ([8]byte{}) {
		t.Fatalf("SealControlReply now stamps SenderKeyID %v; the zero value is what keeps command replies in their own seq bucket, driven by an independent durable counter (C2b)", env.Header.SenderKeyID)
	}
}

// ---------------------------------------------------------------------------
// PB-INPUT-2 -- the gateway tells the phone when a lease dies
// ---------------------------------------------------------------------------

// TestS11Sever_ADyingLeaseIsSealedToThePhone is the bridge half. The notice must be a
// command_reply on the phone's mailbox, tagged with the take_control's operation id (so
// ReplyCache.TakeFor can attribute it -- an untagged reply matches NOTHING, snapshot.go:110)
// and carrying the dead generation.
func TestS11Sever_ADyingLeaseIsSealedToThePhone(t *testing.T) {
	key := s11Key()
	mb := &fakeMailbox{}
	lr := &s11SeverRouter{}

	NewCommandBridge(CommandBridgeConfig{
		Mailbox:     mb,
		Forwarder:   &fakeForwarder{},
		Leases:      lr,
		Key:         key,
		EpochID:     s11Epoch,
		ReplyTarget: "phone-routing-id",
	})

	lr.fire(SeveredLease{
		Session:     "m1/s1",
		OperationID: "op-take-1",
		Generation:  42,
		Reason:      "daemon connection closed",
	})

	mb.mu.Lock()
	replies := append([][]byte(nil), mb.replies...)
	target := mb.target
	mb.mu.Unlock()

	if len(replies) != 1 {
		t.Fatalf("a dying lease produced %d sealed replies, want 1 -- with none, LeaseManager.Input silently drops every later keystroke (leasemanager.go:72-87) and the phone types into a void with no signal at all", len(replies))
	}
	if target != "phone-routing-id" {
		t.Fatalf("the notice was appended to %q, want the phone's ReplyTarget", target)
	}

	ctrl := s11OpenReply(t, key, replies[0])
	if ctrl.Op != protocol.OpDetach {
		t.Errorf("notice Op = %q, want %q -- the phone routes the lease lifecycle on this verb", ctrl.Op, protocol.OpDetach)
	}
	if ctrl.SessionID != "m1/s1" {
		t.Errorf("notice SessionID = %q, want %q", ctrl.SessionID, "m1/s1")
	}
	if ctrl.OperationID != "op-take-1" {
		t.Errorf("notice OperationID = %q, want the take_control's %q -- an untagged reply matches nothing in ReplyCache.TakeFor and cannot be attributed to the lease it ends", ctrl.OperationID, "op-take-1")
	}
	if ctrl.Generation != 42 {
		t.Errorf("notice Generation = %d, want the DEAD generation 42 -- without it a supersede's notice cannot be told from one that ends the live lease", ctrl.Generation)
	}
	if ctrl.Error == "" {
		t.Error("notice carries no reason; PB-INPUT-2 requires the suppression be VISIBLE, and a silent one is a dead keyboard with no explanation")
	}
}

// TestS11Sever_TheNoticeRidesTheReplySeqStream. Two deaths must not collide, and they must
// not collide with the journal/terminal stream either. Both properties come free from
// sealing through the bridge's own reply seq source -- and both break the moment an
// implementation seals the notice with a hand-rolled seq.
func TestS11Sever_TheNoticeRidesTheReplySeqStream(t *testing.T) {
	key := s11Key()
	mb := &fakeMailbox{}
	lr := &s11SeverRouter{}
	NewCommandBridge(CommandBridgeConfig{
		Mailbox: mb, Forwarder: &fakeForwarder{}, Leases: lr,
		Key: key, EpochID: s11Epoch, ReplyTarget: "phone-routing-id",
	})

	lr.fire(SeveredLease{Session: "m1/s1", OperationID: "op-1", Generation: 1, Reason: "session exited"})
	lr.fire(SeveredLease{Session: "m1/s2", OperationID: "op-2", Generation: 2, Reason: "session exited"})

	mb.mu.Lock()
	replies := append([][]byte(nil), mb.replies...)
	mb.mu.Unlock()
	if len(replies) != 2 {
		t.Fatalf("two lease deaths produced %d notices, want 2", len(replies))
	}

	seqs := make([]uint64, 0, 2)
	for i, raw := range replies {
		env, err := crypto.ParseEnvelope(raw)
		if err != nil {
			t.Fatalf("parse notice %d: %v", i, err)
		}
		seqs = append(seqs, env.Header.Seq)
	}
	if seqs[0] == seqs[1] {
		t.Fatalf("both notices carry seq %d; the phone's MailboxReceiver stale-drops the second and one lease death is invisible", seqs[0])
	}
}

// TestS11Sever_ABridgeWithNoLeasePlaneDoesNotPanic. cfg.Leases is documented as nilable
// ("nil => input plane disabled"), and every other lease call site guards for it. A
// registration that dereferenced it would turn a supported configuration into a crash at
// construction.
func TestS11Sever_ABridgeWithNoLeasePlaneDoesNotPanic(t *testing.T) {
	NewCommandBridge(CommandBridgeConfig{
		Mailbox: &fakeMailbox{}, Forwarder: &fakeForwarder{},
		Key: s11Key(), EpochID: s11Epoch, ReplyTarget: "phone-routing-id",
	})
}

// ---------------------------------------------------------------------------
// PB-INPUT-2 -- the LeaseManager actually fires it
// ---------------------------------------------------------------------------

// s11LeaseDaemon is the smallest daemon that grants a lease: hello, then an OpLease
// carrying gen in answer to the take_control. It keeps the accepted connection so a test
// can KILL it, which is the daemon-restart / session-exit event PB-INPUT-2 enumerates.
type s11LeaseDaemon struct {
	mu       sync.Mutex
	accepted []net.Conn
}

func (d *s11LeaseDaemon) serve(ln net.Listener, gen uint64) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		d.mu.Lock()
		d.accepted = append(d.accepted, c)
		d.mu.Unlock()
		go d.session(c, gen)
	}
}

func (d *s11LeaseDaemon) session(c net.Conn, gen uint64) {
	write := func(ctrl protocol.Control) bool {
		body, err := protocol.EncodeControl(ctrl)
		if err != nil {
			return false
		}
		return wire.WriteFrame(c, wire.TControl, body) == nil
	}
	for {
		typ, payload, err := wire.ReadFrame(c)
		if err != nil {
			return
		}
		if typ != wire.TControl {
			continue
		}
		ctrl, err := protocol.DecodeControl(payload)
		if err != nil {
			continue
		}
		switch ctrl.Op {
		case protocol.OpHello:
			if !write(protocol.Control{Op: protocol.OpHello, EndpointID: "m1", ProtocolVersion: protocol.Version}) {
				return
			}
		case protocol.OpTakeControl:
			if !write(protocol.Control{Op: protocol.OpLease, EndpointID: "m1", SessionID: ctrl.SessionID, Generation: gen}) {
				return
			}
		}
	}
}

// kill drops every accepted connection: the daemon restarting under a live gateway.
func (d *s11LeaseDaemon) kill() {
	d.mu.Lock()
	conns := d.accepted
	d.accepted = nil
	d.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

// TestS11Sever_LeaseManagerFiresWhenTheDaemonDies is the assertion that keeps every test
// above from fencing an unreachable subject. The bridge can seal a perfect notice and the
// phone can consume it perfectly, and PB-INPUT-2 still ships broken if nothing ever calls
// the hook. This drives the REAL LeaseManager over a REAL unix socket and kills the
// daemon under it.
func TestS11Sever_LeaseManagerFiresWhenTheDaemonDies(t *testing.T) {
	// /tmp keeps the socket under the 104-byte sun_path limit (macOS $TMPDIR is long).
	dir, err := os.MkdirTemp("/tmp", "s11lease")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	const gen uint64 = 9
	d := &s11LeaseDaemon{}
	go d.serve(ln, gen)

	m := NewLeaseManager(sock, 3*time.Second)
	defer m.Close()

	severed := make(chan SeveredLease, 4)
	m.OnSever(func(s SeveredLease) { severed <- s })

	if err := m.Begin(protocol.RemoteCommand{
		DeviceCommandAuth: protocol.DeviceCommandAuth{
			Action: protocol.ActionTakeControl, Machine: "m1",
			Session: "m1/s1", OperationID: "op-take-1",
		},
	}); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if got := m.Generation("m1/s1"); got != gen {
		t.Fatalf("setup: Generation = %d, want %d", got, gen)
	}

	// The daemon restarts under a live gateway.
	d.kill()

	select {
	case s := <-severed:
		if s.Session != "m1/s1" {
			t.Errorf("SeveredLease.Session = %q, want %q", s.Session, "m1/s1")
		}
		if s.OperationID != "op-take-1" {
			t.Errorf("SeveredLease.OperationID = %q, want the take_control's %q", s.OperationID, "op-take-1")
		}
		if s.Generation != gen {
			t.Errorf("SeveredLease.Generation = %d, want the dead %d", s.Generation, gen)
		}
		if s.Reason == "" {
			t.Error("SeveredLease carries no Reason")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the lease died and nothing fired: LeaseManager.watch evicts the conn and tells no one, so every later keystroke is dropped by LeaseManager.Input returning nil and the phone never learns it lost control (PB-INPUT-2)")
	}
}

// TestS11Sever_FiresOnceAndOnlyOnce. The watcher, End, Close and a supersede all close the
// same conn. A notice per path would tell the phone the lease died two or three times,
// and -- with the generation check on the phone side -- a duplicate carrying a superseded
// generation is exactly the frame that could kill a live replacement.
func TestS11Sever_FiresOnceAndOnlyOnce(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "s11lease2")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	d := &s11LeaseDaemon{}
	go d.serve(ln, 5)

	m := NewLeaseManager(sock, 3*time.Second)
	var mu sync.Mutex
	var got []SeveredLease
	m.OnSever(func(s SeveredLease) {
		mu.Lock()
		got = append(got, s)
		mu.Unlock()
	})

	if err := m.Begin(protocol.RemoteCommand{
		DeviceCommandAuth: protocol.DeviceCommandAuth{
			Action: protocol.ActionTakeControl, Machine: "m1",
			Session: "m1/s1", OperationID: "op-take-1",
		},
	}); err != nil {
		t.Fatalf("Begin: %v", err)
	}

	// The user releases control, and then the manager shuts down.
	m.End("m1/s1")
	_ = m.Close()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // let any duplicate arrive

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("one lease death fired %d notices, want exactly 1 (%+v) -- End, Close and the watcher all close the same conn, and a duplicate carrying a dead generation is the frame that can kill a live replacement lease", len(got), got)
	}
}

// TestS11Sever_ContextIsNotTheCallersRequestContext. The seal happens on a lease death,
// which is asynchronous to every request. If the bridge sealed it under a context that a
// finished poll had cancelled, the notice would fail to append and the phone would be told
// nothing -- silently, since nobody is waiting on the result.
func TestS11Sever_ContextIsNotTheCallersRequestContext(t *testing.T) {
	key := s11Key()
	mb := &fakeMailbox{}
	lr := &s11SeverRouter{}
	b := NewCommandBridge(CommandBridgeConfig{
		Mailbox: mb, Forwarder: &fakeForwarder{}, Leases: lr,
		Key: key, EpochID: s11Epoch, ReplyTarget: "phone-routing-id",
	})

	// Drive and cancel a poll, exactly as the Run loop does per batch.
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := b.PollOnce(ctx); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	cancel()

	lr.fire(SeveredLease{Session: "m1/s1", OperationID: "op-take-1", Generation: 1, Reason: "session exited"})

	mb.mu.Lock()
	n := len(mb.replies)
	mb.mu.Unlock()
	if n != 1 {
		t.Fatalf("after the poll context was cancelled a lease death produced %d notices, want 1 -- the notice must not be scoped to a request that has already finished", n)
	}
}
