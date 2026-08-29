package remotegw

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// cursorResetMailbox models the honest relay after its durable mailbox sequence was
// reinitialised while the gateway retained its prior read cursor. The first read exposes
// the discontinuity; the retry from zero serves the new store's first command.
type cursorResetMailbox struct {
	fakeMailbox
	reads []uint64
	acked []uint64
}

type waitCursorResetMailbox struct {
	cursorResetMailbox
	mu        sync.Mutex
	waitReads []uint64
	calls     int
}

// legacyIncarnationMailbox models an upgraded gateway resuming a pre-incarnation
// checkpoint whose numeric cursor happens to equal the replacement store's high-water.
// Only the explicit empty incarnation can expose that otherwise ambiguous shape.
type legacyIncarnationMailbox struct {
	fakeMailbox
	mu          sync.Mutex
	incarnation string
	reads       []uint64
}

func (m *legacyIncarnationMailbox) SetMailboxIncarnation(incarnation string) {
	m.mu.Lock()
	m.incarnation = incarnation
	m.mu.Unlock()
}

func (m *legacyIncarnationMailbox) ResetMailboxIncarnation() {
	m.SetMailboxIncarnation("")
}

func (m *legacyIncarnationMailbox) MailboxIncarnation() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.incarnation
}

func (m *legacyIncarnationMailbox) MailboxRead(ctx context.Context, cursor uint64) ([]relay.Item, error) {
	m.mu.Lock()
	m.reads = append(m.reads, cursor)
	incarnation := m.incarnation
	m.mu.Unlock()
	if cursor > 0 && incarnation == "" {
		return nil, relay.ErrMailboxCursorResetRequired
	}
	if cursor == 0 {
		m.SetMailboxIncarnation("replacement-mailbox-incarnation")
	}
	return m.fakeMailbox.MailboxRead(ctx, cursor)
}

func (m *waitCursorResetMailbox) MailboxWait(ctx context.Context, cursor uint64) ([]relay.Item, bool, error) {
	m.mu.Lock()
	m.calls++
	call := m.calls
	m.waitReads = append(m.waitReads, cursor)
	m.mu.Unlock()
	switch call {
	case 1:
		return nil, false, relay.ErrMailboxCursorResetRequired
	case 2:
		items, err := m.fakeMailbox.MailboxRead(ctx, cursor)
		return items, false, err
	default:
		<-ctx.Done()
		return nil, false, ctx.Err()
	}
}

func (m *cursorResetMailbox) MailboxAck(_ context.Context, cursor uint64) error {
	m.acked = append(m.acked, cursor)
	return nil
}

func (m *cursorResetMailbox) MailboxRead(ctx context.Context, cursor uint64) ([]relay.Item, error) {
	m.reads = append(m.reads, cursor)
	if cursor > 1 {
		return nil, relay.ErrMailboxCursorResetRequired
	}
	return m.fakeMailbox.MailboxRead(ctx, cursor)
}

func TestCommandBridge_AuthenticatesAndCompactsReplaysAfterRewind(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 19)
	}
	stream := InboundStream{Epoch: 1}
	inbound := &memInboundState{ck: InboundCheckpoint{
		Cursor: 53, Highest: map[InboundStream]uint64{stream: 1},
	}}
	mb := &cursorResetMailbox{fakeMailbox: fakeMailbox{inbox: []relay.Item{{
		Cursor: 1,
		Envelope: sealedCmd(t, key, 1, protocol.DeviceCommandAuth{
			Action: protocol.ActionKill, Session: "m/old", OperationID: "old", DeviceID: "d1", Sig: "s1",
		}),
	}}}}
	fwd := &fakeForwarder{}
	b := NewCommandBridge(CommandBridgeConfig{
		Mailbox: mb, Forwarder: fwd, Key: key, EpochID: 1, ReplyTarget: "phone", Inbound: inbound,
	})

	processed, err := b.PollOnce(context.Background())
	if !errors.Is(err, crypto.ErrStaleSeq) {
		t.Fatalf("PollOnce replay cleanup = %v, want the authenticated replay refusal surfaced", err)
	}
	if processed != 0 || len(fwd.seen) != 0 {
		t.Fatalf("replayed command processed=%d forwarded=%d, want no repeated side effect", processed, len(fwd.seen))
	}
	if len(mb.acked) != 1 || mb.acked[0] != 1 {
		t.Fatalf("replayed item acks = %v, want cursor 1 compacted", mb.acked)
	}
	ck := inbound.Load()
	if ck.Cursor != 1 || ck.Highest[stream] != 1 {
		t.Fatalf("checkpoint after replay cleanup = %+v, want cursor 1 and unchanged replay high-water 1", ck)
	}
}

func TestCommandBridge_PollOnceRecoversAResetRelayMailbox(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 11)
	}
	stream := InboundStream{Epoch: 1}
	inbound := &memInboundState{ck: InboundCheckpoint{
		Cursor:  53,
		Highest: map[InboundStream]uint64{stream: 1},
	}}
	mb := &cursorResetMailbox{fakeMailbox: fakeMailbox{inbox: []relay.Item{{
		Cursor: 1,
		Envelope: sealedCmd(t, key, 2, protocol.DeviceCommandAuth{
			Action: protocol.ActionKill, Session: "m/s1", OperationID: "op-2", DeviceID: "d1", Sig: "s2",
		}),
	}}}}
	fwd := &fakeForwarder{}
	b := NewCommandBridge(CommandBridgeConfig{
		Mailbox: mb, Forwarder: fwd, Key: key, EpochID: 1, ReplyTarget: "phone", Inbound: inbound,
	})

	processed, err := b.PollOnce(context.Background())
	if err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if processed != 1 || len(fwd.seen) != 1 {
		t.Fatalf("processed=%d forwarded=%d, want one command after automatic rewind", processed, len(fwd.seen))
	}
	if len(mb.reads) != 2 || mb.reads[0] != 53 || mb.reads[1] != 0 {
		t.Fatalf("mailbox reads = %v, want [53 0]", mb.reads)
	}
	ck := inbound.Load()
	if ck.Cursor != 1 || ck.Highest[stream] != 2 {
		t.Fatalf("checkpoint after recovery = %+v, want cursor 1 and replay high-water 2", ck)
	}
}

func TestCommandBridge_LegacyEqualHighWaterCheckpointRewindsBeforeIncarnationAdoption(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 61)
	}
	inbound := &memInboundState{ck: InboundCheckpoint{
		Cursor:  3, // numerically equal to the modeled replacement mailbox high-water
		Highest: map[InboundStream]uint64{},
	}}
	mb := &legacyIncarnationMailbox{fakeMailbox: fakeMailbox{inbox: []relay.Item{{
		Cursor: 1,
		Envelope: sealedCmd(t, key, 1, protocol.DeviceCommandAuth{
			Action: protocol.ActionKill, Session: "m/migrated", OperationID: "migrated", DeviceID: "d1", Sig: "s1",
		}),
	}}}}
	b := NewCommandBridge(CommandBridgeConfig{
		Mailbox: mb, Forwarder: &fakeForwarder{}, Key: key, EpochID: 1, ReplyTarget: "phone", Inbound: inbound,
	})

	processed, err := b.PollOnce(context.Background())
	if err != nil || processed != 1 {
		t.Fatalf("legacy equal-high-water recovery processed=%d err=%v, want one replacement item", processed, err)
	}
	mb.mu.Lock()
	reads := append([]uint64(nil), mb.reads...)
	mb.mu.Unlock()
	if len(reads) != 2 || reads[0] != 3 || reads[1] != 0 {
		t.Fatalf("legacy migration reads = %v, want [3 0]", reads)
	}
	ck := inbound.Load()
	if ck.Cursor != 1 || ck.Incarnation != "replacement-mailbox-incarnation" {
		t.Fatalf("migrated checkpoint = %+v, want cursor 1 bound to replacement incarnation", ck)
	}
}

func TestCommandBridge_RunRecoversAResetRelayMailboxWait(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 29)
	}
	stream := InboundStream{Epoch: 1}
	inbound := &memInboundState{ck: InboundCheckpoint{
		Cursor: 53, Highest: map[InboundStream]uint64{stream: 1},
	}}
	mb := &waitCursorResetMailbox{cursorResetMailbox: cursorResetMailbox{fakeMailbox: fakeMailbox{inbox: []relay.Item{{
		Cursor: 1,
		Envelope: sealedCmd(t, key, 2, protocol.DeviceCommandAuth{
			Action: protocol.ActionKill, Session: "m/wait", OperationID: "op-wait", DeviceID: "d1", Sig: "s2",
		}),
	}}}}}
	fwd := &fakeForwarder{}
	b := NewCommandBridge(CommandBridgeConfig{
		Mailbox: mb, Forwarder: fwd, Key: key, EpochID: 1, ReplyTarget: "phone", Inbound: inbound,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()
	waitFor(t, func() bool {
		fwd.mu.Lock()
		defer fwd.mu.Unlock()
		return len(fwd.seen) == 1 && b.Cursor() == 1
	}, 2*time.Second, "wait drain did not recover and forward the replacement mailbox command")
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after recovery test cancellation")
	}
	mb.mu.Lock()
	reads := append([]uint64(nil), mb.waitReads...)
	mb.mu.Unlock()
	if len(reads) < 2 || reads[0] != 53 || reads[1] != 0 {
		t.Fatalf("mailbox wait reads = %v, want reset retry [53 0]", reads)
	}
}

func TestCommandBridge_RecoverySurvivesMixedStaleFreshStalePage(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 41)
	}
	stream := InboundStream{Epoch: 1}
	inbound := &memInboundState{ck: InboundCheckpoint{
		Cursor: 53, Highest: map[InboundStream]uint64{stream: 2},
	}}
	cmd := func(seq uint64, op string) []byte {
		return sealedCmd(t, key, seq, protocol.DeviceCommandAuth{
			Action: protocol.ActionKill, Session: "m/" + op, OperationID: op, DeviceID: "d1", Sig: op,
		})
	}
	mb := &cursorResetMailbox{fakeMailbox: fakeMailbox{inbox: []relay.Item{
		{Cursor: 1, Envelope: cmd(1, "old-1")},
		{Cursor: 2, Envelope: cmd(3, "fresh")},
		{Cursor: 3, Envelope: cmd(2, "old-2")},
	}}}
	fwd := &fakeForwarder{}
	b := NewCommandBridge(CommandBridgeConfig{
		Mailbox: mb, Forwarder: fwd, Key: key, EpochID: 1, ReplyTarget: "phone", Inbound: inbound,
	})

	processed, err := b.PollOnce(context.Background())
	if processed != 1 || len(fwd.seen) != 1 {
		t.Fatalf("mixed recovery processed=%d forwarded=%d, want only fresh command", processed, len(fwd.seen))
	}
	if !errors.Is(err, crypto.ErrStaleSeq) {
		t.Fatalf("mixed recovery error = %v, want stale replay refusals surfaced", err)
	}
	if got := inbound.Load().Cursor; got != 3 {
		t.Fatalf("mixed recovery cursor = %d, want stale tail compacted through 3", got)
	}
	if len(mb.acked) != 1 || mb.acked[0] != 3 {
		t.Fatalf("mixed recovery acks = %v, want [3]", mb.acked)
	}
}

func TestCommandBridge_RecoveryCompactsAuthenticatedOldReplayDespiteAge(t *testing.T) {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i + 51)
	}
	stream := InboundStream{Epoch: 1}
	inbound := &memInboundState{ck: InboundCheckpoint{
		Cursor: 53, Highest: map[InboundStream]uint64{stream: 1},
	}}
	plain, err := json.Marshal(protocol.DeviceCommandAuth{
		Action: protocol.ActionKill, Session: "m/old", OperationID: "old", DeviceID: "d1", Sig: "s1",
	})
	if err != nil {
		t.Fatal(err)
	}
	env, err := crypto.SealMailbox(key, crypto.EnvelopeHeader{
		Version: crypto.VersionV1, EpochID: 1, Seq: 1,
		IssuedAt: time.Now().Add(-InboundMaxAge - time.Minute).UnixMilli(),
	}, plain)
	if err != nil {
		t.Fatal(err)
	}
	mb := &cursorResetMailbox{fakeMailbox: fakeMailbox{inbox: []relay.Item{{Cursor: 1, Envelope: env.Marshal()}}}}
	b := NewCommandBridge(CommandBridgeConfig{
		Mailbox: mb, Forwarder: &fakeForwarder{}, Key: key, EpochID: 1, ReplyTarget: "phone", Inbound: inbound,
	})

	processed, err := b.PollOnce(context.Background())
	if processed != 0 || !errors.Is(err, crypto.ErrStaleAge) {
		t.Fatalf("old replay recovery processed=%d err=%v, want 0 and ErrStaleAge", processed, err)
	}
	if got := inbound.Load().Cursor; got != 1 {
		t.Fatalf("old authenticated replay left recovery cursor at %d, want 1", got)
	}
	if len(mb.acked) != 1 || mb.acked[0] != 1 {
		t.Fatalf("old authenticated replay acks = %v, want [1]", mb.acked)
	}
}
