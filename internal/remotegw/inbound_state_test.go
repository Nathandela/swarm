// FAILING-FIRST (TDD RED, GG-5) tests for PB-GW-1 (§4.6 / §6.0b of
// docs/specifications/remote-phaseB-requirements.md): the gateway's INBOUND replay
// high-water and mailbox read cursor must be DURABLE and SEEDED on start.
//
// THE DEFECT, verified in the tree (not assumed):
//   - NewCommandBridge builds a fresh crypto.NewMailboxReceiver() on every start
//     (command_loop.go:106) and the read cursor starts at 0. Its own doc says a caller
//     "resuming across a restart should seed it via SetCursor from durable state" -- and
//     SetCursor is NEVER called from production startup; its only call site (PollOnce)
//     advances within one run.
//   - cmd/swarm-remote/config.go opens OpenSeqSource for outbound-journal.seq and
//     outbound-reply.seq only. NO inbound state is persisted at all.
//   - crypto.MailboxReceiver.Accept tests staleness as `hi, seen := r.highest[mk]; if
//     seen && e.Header.Seq <= hi` (envelope.go:255-257). On a fresh receiver seen ==
//     false, so the check is SKIPPED ENTIRELY and gap is false -- the first retained
//     frame at any seq is accepted, and so is every contiguous frame after it.
//
// THE SEAM these tests pin (undefined symbols -> compile-fail RED). CommandBridge.recv
// is private and exposes no high-water seeding method, so the earlier claim that
// production could seed "via the existing seams" is false. The bridge needs its own:
//
//	type InboundStream struct { Sender [8]byte; Epoch uint32 }
//	type InboundCheckpoint struct { Cursor uint64; Highest map[InboundStream]uint64 }
//	type InboundState interface { Load() InboundCheckpoint; Save(InboundCheckpoint) error }
//	func OpenInboundState(path string) (InboundState, error) // "" => in-memory, no durability
//	CommandBridgeConfig gains: Inbound InboundState
//
// NewCommandBridge must Load() at construction and seed BOTH halves -- every
// (sender, epoch) high-water into its receiver (crypto.MailboxReceiver.SeedHighWater
// ALREADY EXISTS, so internal/remote/crypto stays FROZEN and is not touched by this
// slice) and Cursor into the read cursor -- and PollOnce must Save() as it consumes.
package remotegw

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// retainingRelay is the ADVERSARIAL relay of §4.6: it serves the machine's inbox and
// RECORDS acks but NEVER deletes the acked items, so everything it ever held stays
// replayable across a gateway restart. It is the exact opposite of ackPurgingMailbox
// (command_ack_test.go), which models an honest relay whose ack-deletion is what today's
// restart safety silently rests on.
type retainingRelay struct {
	mu      sync.Mutex
	inbox   []relay.Item
	acked   []uint64
	replies [][]byte
}

func (r *retainingRelay) add(cursor uint64, env []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inbox = append(r.inbox, relay.Item{Cursor: cursor, Envelope: env})
}

func (r *retainingRelay) MailboxRead(_ context.Context, cursor uint64) ([]relay.Item, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []relay.Item
	for _, it := range r.inbox {
		if it.Cursor > cursor {
			out = append(out, it)
		}
	}
	return out, nil
}

func (r *retainingRelay) MailboxAppend(_ context.Context, _ string, env []byte) (uint64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.replies = append(r.replies, env)
	return uint64(len(r.replies)), nil
}

// MailboxAck records the ack and DELIBERATELY does not purge: an ack the gateway cannot
// verify is exactly the guarantee a hostile or buggy relay withholds.
func (r *retainingRelay) MailboxAck(_ context.Context, cursor uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.acked = append(r.acked, cursor)
	return nil
}

// memInboundState is an in-memory InboundState that SURVIVES a simulated restart (the
// same instance is handed to both bridges), standing in for the file a production
// gateway would keep under <state>/remote. failSave models a crash at the persist
// boundary: the process dies with nothing durable written (see the PB-GW-3 matrix).
type memInboundState struct {
	mu       sync.Mutex
	ck       InboundCheckpoint
	saves    int
	failSave error
}

func (s *memInboundState) Load() InboundCheckpoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := InboundCheckpoint{Cursor: s.ck.Cursor, Highest: make(map[InboundStream]uint64, len(s.ck.Highest))}
	for k, v := range s.ck.Highest {
		out.Highest[k] = v
	}
	return out
}

func (s *memInboundState) Save(ck InboundCheckpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saves++
	if s.failSave != nil {
		return s.failSave
	}
	s.ck.Cursor = ck.Cursor
	if s.ck.Highest == nil {
		s.ck.Highest = make(map[InboundStream]uint64)
	}
	for k, v := range ck.Highest {
		if v > s.ck.Highest[k] {
			s.ck.Highest[k] = v
		}
	}
	return nil
}

// sealAt seals any phone -> machine plaintext (a RemoteCommand or an inputFrameWire)
// under an explicit (epoch, seq), which the existing sealedCmd/sealRemoteCmd/sealInputEnv
// helpers cannot do -- they hardcode EpochID 1, and PB-GW-1's per-stream keying needs two
// epochs. Mirrors the phone's seals: no SenderKeyID and no IssuedAt (§4.6, PB-GW-6).
func sealAt(t *testing.T, key crypto.ContentKey, epoch uint32, seq uint64, v any) []byte {
	t.Helper()
	plain, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal plaintext: %v", err)
	}
	env, err := crypto.SealMailbox(key, crypto.EnvelopeHeader{Version: crypto.VersionV1, EpochID: epoch, Seq: seq}, plain)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	return env.Marshal()
}

func inboundKey(n byte) crypto.ContentKey {
	var key crypto.ContentKey
	for i := range key {
		key[i] = byte(i) + n
	}
	return key
}

func killCmd(session, op string) protocol.RemoteCommand {
	return protocol.RemoteCommand{DeviceCommandAuth: protocol.DeviceCommandAuth{
		Action: protocol.ActionKill, Session: session, OperationID: op, DeviceID: "d1", Sig: "sig-" + op,
	}}
}

// TestCommandBridge_InboundHighWaterSeededAcrossRestart is the PB-GW-1 acceptance test,
// asserted AT THE REPLAY GUARD: after a restart the retained frames must be refused with
// crypto.ErrStaleSeq, not merely be harmless because some downstream mechanism drops them.
//
// The relay re-appends the IDENTICAL sealed envelopes at NEW storage cursors, so a seeded
// read cursor cannot help -- only the seeded per-(sender, epoch) high-water can.
func TestCommandBridge_InboundHighWaterSeededAcrossRestart(t *testing.T) {
	key := inboundKey(11)
	const epoch uint32 = 7
	st := &memInboundState{}
	rl := &retainingRelay{}

	envs := make([][]byte, 3)
	for i := range envs {
		seq := uint64(i + 1)
		envs[i] = sealAt(t, key, epoch, seq, killCmd("m/s1", fmt.Sprintf("op-%d", seq)))
		rl.add(seq, envs[i])
	}

	fwd1 := &fakeForwarder{}
	b1 := NewCommandBridge(CommandBridgeConfig{
		Mailbox: rl, Forwarder: fwd1, Key: key, EpochID: epoch, ReplyTarget: "phone", Inbound: st,
	})
	if _, err := b1.PollOnce(context.Background()); err != nil {
		t.Fatalf("run 1 PollOnce: %v", err)
	}
	if len(fwd1.seen) != 3 {
		t.Fatalf("run 1 forwarded %d commands, want 3", len(fwd1.seen))
	}

	// RESTART. The retaining relay re-serves the same three sealed envelopes at fresh
	// storage cursors 4..6 (its cursor is its own untrusted bookkeeping, ADR-007 D7).
	for i, env := range envs {
		rl.add(uint64(4+i), env)
	}
	fwd2 := &fakeForwarder{}
	b2 := NewCommandBridge(CommandBridgeConfig{
		Mailbox: rl, Forwarder: fwd2, Key: key, EpochID: epoch, ReplyTarget: "phone", Inbound: st,
	})
	n, err := b2.PollOnce(context.Background())

	if n != 0 || len(fwd2.seen) != 0 {
		t.Fatalf("post-restart processed=%d forwarded=%d, want 0/0: retained frames at seqs already "+
			"consumed before the restart must be refused at the replay guard", n, len(fwd2.seen))
	}
	if !errors.Is(err, crypto.ErrStaleSeq) {
		t.Fatalf("post-restart PollOnce err = %v, want it to wrap crypto.ErrStaleSeq: the inbound "+
			"per-(sender,epoch) high-water is not persisted or not seeded, so the restarted bridge's "+
			"FRESH crypto.MailboxReceiver has seen == false and skips the staleness check entirely, "+
			"accepting every retained frame the relay chose not to delete", err)
	}
}

// TestCommandBridge_MailboxCursorSeededOnStart pins the OTHER half of PB-GW-1: the read
// cursor is durable and seeded at construction, so a bridge resuming against a relay that
// never purged does not re-read what a previous run already consumed. Asserted on
// Cursor() BEFORE any poll, because "seeds them on start" is the requirement.
func TestCommandBridge_MailboxCursorSeededOnStart(t *testing.T) {
	key := inboundKey(23)
	const epoch uint32 = 7
	st := &memInboundState{}
	rl := &retainingRelay{}
	for seq := uint64(1); seq <= 3; seq++ {
		rl.add(seq, sealAt(t, key, epoch, seq, killCmd("m/s1", fmt.Sprintf("op-%d", seq))))
	}

	b1 := NewCommandBridge(CommandBridgeConfig{
		Mailbox: rl, Forwarder: &fakeForwarder{}, Key: key, EpochID: epoch, ReplyTarget: "phone", Inbound: st,
	})
	if _, err := b1.PollOnce(context.Background()); err != nil {
		t.Fatalf("run 1 PollOnce: %v", err)
	}
	if got := b1.Cursor(); got != 3 {
		t.Fatalf("run 1 Cursor() = %d, want 3", got)
	}

	// RESTART: a fresh bridge over the SAME durable state and the SAME un-purged relay.
	b2 := NewCommandBridge(CommandBridgeConfig{
		Mailbox: rl, Forwarder: &fakeForwarder{}, Key: key, EpochID: epoch, ReplyTarget: "phone", Inbound: st,
	})
	if got := b2.Cursor(); got != 3 {
		t.Fatalf("post-restart Cursor() = %d before any poll, want 3: the mailbox read cursor is not "+
			"persisted or not seeded on start, so the bridge re-reads from 0 and re-consumes every item "+
			"still sitting in the relay's store (up to its full retention window)", got)
	}
}

// TestCommandBridge_InboundHighWaterKeyedPerEpoch pins the SHAPE of the persisted state:
// it is keyed per (sender, epoch), exactly like crypto.MailboxReceiver's own high-water
// map. Two assertions, in order:
//
//  1. the retained old-epoch frames are refused at the guard (this is the half that fails
//     against unfixed code), and
//  2. after an epoch rotation (RevokeDevice rotates the epoch key, skeleton/api.go:231) a
//     brand-new epoch's seq 1 is still ACCEPTED -- a single scalar high-water seeded into
//     every stream would refuse it and brick the phone after every revoke.
func TestCommandBridge_InboundHighWaterKeyedPerEpoch(t *testing.T) {
	key7 := inboundKey(31)
	key8 := inboundKey(97)
	st := &memInboundState{}
	rl := &retainingRelay{}

	envs := make([][]byte, 3)
	for i := range envs {
		seq := uint64(i + 1)
		envs[i] = sealAt(t, key7, 7, seq, killCmd("m/s1", fmt.Sprintf("op-e7-%d", seq)))
		rl.add(seq, envs[i])
	}
	b1 := NewCommandBridge(CommandBridgeConfig{
		Mailbox: rl, Forwarder: &fakeForwarder{}, Key: key7, EpochID: 7, ReplyTarget: "phone", Inbound: st,
	})
	if _, err := b1.PollOnce(context.Background()); err != nil {
		t.Fatalf("epoch-7 run PollOnce: %v", err)
	}

	// Restart in the SAME epoch: the retained epoch-7 frames come back at new cursors.
	for i, env := range envs {
		rl.add(uint64(4+i), env)
	}
	fwd2 := &fakeForwarder{}
	b2 := NewCommandBridge(CommandBridgeConfig{
		Mailbox: rl, Forwarder: fwd2, Key: key7, EpochID: 7, ReplyTarget: "phone", Inbound: st,
	})
	_, err := b2.PollOnce(context.Background())
	if len(fwd2.seen) != 0 {
		t.Fatalf("same-epoch restart forwarded %d retained epoch-7 commands, want 0", len(fwd2.seen))
	}
	if !errors.Is(err, crypto.ErrStaleSeq) {
		t.Fatalf("same-epoch restart PollOnce err = %v, want crypto.ErrStaleSeq for the retained "+
			"epoch-7 frames", err)
	}

	// Epoch rotation: a NEW epoch key and id over the SAME durable state. Its seq 1 is the
	// first frame of a new stream and must be accepted.
	rl.add(10, sealAt(t, key8, 8, 1, killCmd("m/s1", "op-e8-1")))
	fwd3 := &fakeForwarder{}
	b3 := NewCommandBridge(CommandBridgeConfig{
		Mailbox: rl, Forwarder: fwd3, Key: key8, EpochID: 8, ReplyTarget: "phone", Inbound: st,
	})
	if _, err := b3.PollOnce(context.Background()); err != nil {
		t.Fatalf("post-rotation PollOnce: %v", err)
	}
	if len(fwd3.seen) != 1 || fwd3.seen[0].OperationID != "op-e8-1" {
		t.Fatalf("post-rotation forwarded %+v, want exactly [op-e8-1]: the persisted high-water must be "+
			"keyed per (sender, epoch) like crypto.MailboxReceiver's own map -- a single scalar seeded "+
			"into the new epoch's stream stale-drops the phone's first post-revoke command", fwd3.seen)
	}
}
