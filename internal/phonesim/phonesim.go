// Package phonesim is a simulated phone that drives the REAL phonecore over a REAL
// relay, with no mobile app in the loop. It exists to exercise the full remote wire
// on the build machine (ADR-007 D12): pair -> observe the daemon journal -> sign and
// drive a command -> read the sealed reply. It is a THIN composition over phonecore --
// it invents no crypto and duplicates no receiver/seal logic; every step delegates to
// the same phonecore functions the compiled SwiftUI shell will call.
//
// Custody: the epoch content key recovered from the grant is held UNEXPORTED and never
// leaves the Phone. The simulator is a test-harness stand-in for the phone, not a key
// escrow.
package phonesim

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// Config wires a simulated phone to the pairing outcome and the relay. It mirrors what
// a real device holds after pairing + enrollment: its key custody, the pinned machine
// grant-signing pub, the sealed epoch grant, its relay connection, and the machine's
// routing/endpoint targets.
type Config struct {
	KeyStore       crypto.KeyStore   // phone key custody (Noise/relay-auth/command-signing)
	MachineSignPub []byte            // machine Ed25519 grant-signing pub pinned at pairing
	Grant          *crypto.EpochGrant // sealed initial epoch grant delivered by the machine
	Relay          *relay.Client     // the phone's authenticated relay connection
	MachineTarget  string            // machine mailbox routing id (where commands are appended)
	Machine        string            // machine endpoint id, signed into each command tuple
}

// Phone is a simulated device driving phonecore over the relay. It recovers the epoch
// ContentKey from the grant (custody: unexported), and holds a journal receiver + a
// merged session cache for OBSERVE, plus a monotonic command seq for DRIVE.
type Phone struct {
	ks            crypto.KeyStore
	relay         *relay.Client
	receiver      *phonecore.JournalReceiver
	cache         *phonecore.SessionCache
	content       crypto.ContentKey // custody: recovered from the grant, never exported
	epochID       uint32
	machineTarget string
	machine       string

	// seq stamps EVERY phone -> machine mailbox envelope -- kill/take_control commands AND
	// input frames alike -- from ONE per-epoch allocator, because the gateway opens them
	// all through a single (sender, epoch) MailboxReceiver: a per-kind counter would
	// collide and the receiver would reject the second stream as a replayed seq.
	seq phonecore.Sequencer

	mu     sync.Mutex
	cursor uint64 // phone mailbox read cursor (drained forward by Observe + ReadReply)
}

// New bootstraps the phone from the sealed grant: it AcceptGrants (verifying the grant
// against the pinned machine sign pub and opening it with the phone KeyStore) to recover
// the epoch ContentKey/EpochID, then builds a journal receiver + session cache bound to
// that key. It fails closed on any grant that does not open under the pinned key.
func New(cfg Config) (*Phone, error) {
	epochID, _, keys, err := phonecore.AcceptGrant(cfg.KeyStore, cfg.MachineSignPub, cfg.Grant)
	if err != nil {
		return nil, err
	}
	return &Phone{
		ks:            cfg.KeyStore,
		relay:         cfg.Relay,
		receiver:      phonecore.NewJournalReceiver(keys.ContentKey),
		cache:         phonecore.NewSessionCache(),
		content:       keys.ContentKey,
		epochID:       epochID,
		machineTarget: cfg.MachineTarget,
		machine:       cfg.Machine,
	}, nil
}

// Observe performs ONE forward scan of the phone's relay mailbox: it reads items past
// the phone's cursor, runs each through the phonecore JournalReceiver (which authenticates
// and seq-guards it), applies every decoded record to the session cache, advances the
// cursor, and returns the cache's current sessions. Items that are not journal records
// (or replays/reorders the receiver rejects) are skipped. Callers poll it until the cache
// holds the session they expect.
func (p *Phone) Observe(ctx context.Context) ([]phonecore.CachedSession, error) {
	p.mu.Lock()
	cursor := p.cursor
	p.mu.Unlock()

	items, err := p.relay.MailboxRead(ctx, cursor)
	if err != nil {
		return nil, err
	}
	maxCursor := cursor
	for _, it := range items {
		if it.Cursor > maxCursor {
			maxCursor = it.Cursor
		}
		rec, _, err := p.receiver.Accept(it.Envelope)
		if err != nil {
			continue // not a journal record, or a replay/reorder the receiver rejected
		}
		p.cache.Apply(rec)
	}
	p.advanceCursor(maxCursor)
	return p.cache.List(), nil
}

// Session returns the phone's cached view of one namespaced session id.
func (p *Phone) Session(id string) (phonecore.CachedSession, bool) {
	return p.cache.Get(id)
}

// DriveKill signs a kill for the session with the phone command-signing key, seals it
// under the epoch content key, and appends it to the machine's relay mailbox. The
// gateway opens the envelope, the daemon verifies the device signature + capability and
// executes. seq is an internal monotonic counter (unique per epoch, as SealCommandEnvelope
// requires).
func (p *Phone) DriveKill(ctx context.Context, session, operationID string) error {
	cmd, err := phonecore.SignCommand(p.ks, phonecore.CommandInput{
		Action:      protocol.ActionKill,
		Machine:     p.machine,
		Session:     session,
		OperationID: operationID,
		ExpiresAt:   time.Now().Add(time.Minute),
	})
	if err != nil {
		return err
	}

	env, err := phonecore.SealCommandEnvelope(p.content, p.epochID, p.seq.Next(), cmd)
	if err != nil {
		return err
	}
	if _, err := p.relay.MailboxAppend(ctx, p.machineTarget, env); err != nil {
		return err
	}
	return nil
}

// ReadReply performs ONE forward scan of the phone's mailbox for the sealed control reply
// the gateway returns after executing a command. It opens each item as a control reply and
// returns the first OK/Error Control it finds (found=true). Journal envelopes and other
// non-control items are skipped. The cursor is drained forward so a large journal backlog
// cannot bury the reply behind a bounded page.
func (p *Phone) ReadReply(ctx context.Context) (protocol.Control, bool, error) {
	p.mu.Lock()
	cursor := p.cursor
	p.mu.Unlock()

	items, err := p.relay.MailboxRead(ctx, cursor)
	if err != nil {
		return protocol.Control{}, false, err
	}
	maxCursor := cursor
	for _, it := range items {
		if it.Cursor > maxCursor {
			maxCursor = it.Cursor
		}
		ctrl, err := phonecore.OpenControlReply(p.content, it.Envelope)
		if err != nil {
			continue // journal envelope or unrelated item -> not a control reply
		}
		if ctrl.Op == protocol.OpOK || ctrl.Op == protocol.OpError {
			p.advanceCursor(maxCursor)
			return ctrl, true, nil
		}
	}
	p.advanceCursor(maxCursor)
	return protocol.Control{}, false, nil
}

// TakeControl acquires the controller lease for a session (A7 input Slice 7). It signs a
// take_control with the phone command-signing key (binding a freshly-minted one-shot gate
// token into the signature via ContentHash = SHA256(token)), seals it under the epoch
// content key with the SHARED sequencer, and appends it to the machine mailbox. The
// gateway routes it to LeaseManager.Begin, which opens a persistent lease conn on the
// daemon; the daemon verifies the signature + capability + kill switch and grants the
// lease. Subsequent Type/Resize frames ride THAT lease conn.
func (p *Phone) TakeControl(ctx context.Context, session, operationID string) error {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	gateToken := hex.EncodeToString(raw)

	cmd, err := phonecore.SignTakeControl(p.ks, phonecore.TakeControlInput{
		Machine:     p.machine,
		Session:     session,
		OperationID: operationID,
		ExpiresAt:   time.Now().Add(time.Minute),
		GateToken:   gateToken,
	})
	if err != nil {
		return err
	}
	env, err := phonecore.SealTakeControlEnvelope(p.content, p.epochID, p.seq.Next(), cmd, gateToken, 3600)
	if err != nil {
		return err
	}
	if _, err := p.relay.MailboxAppend(ctx, p.machineTarget, env); err != nil {
		return err
	}
	return nil
}

// Type seals a keystroke burst as an input-frame envelope under the epoch content key
// with the SHARED sequencer and appends it to the machine mailbox, where the gateway
// routes it onto the focused session's lease conn -> the daemon -> the session's PTY. It
// returns the raw wire bytes so a caller can Replay them (an adversarial redelivery).
func (p *Phone) Type(ctx context.Context, data []byte) ([]byte, error) {
	env, err := phonecore.SealInputData(p.content, p.epochID, p.seq.Next(), data)
	if err != nil {
		return nil, err
	}
	if _, err := p.relay.MailboxAppend(ctx, p.machineTarget, env); err != nil {
		return nil, err
	}
	return env, nil
}

// Resize seals a terminal resize as an input-frame envelope with the SHARED sequencer and
// appends it to the machine mailbox, mirroring Type.
func (p *Phone) Resize(ctx context.Context, cols, rows int) error {
	env, err := phonecore.SealInputResize(p.content, p.epochID, p.seq.Next(), cols, rows)
	if err != nil {
		return err
	}
	if _, err := p.relay.MailboxAppend(ctx, p.machineTarget, env); err != nil {
		return err
	}
	return nil
}

// Replay re-appends an already-sealed envelope to the machine mailbox, simulating a relay
// that redelivers a captured ciphertext. It reuses the exact wire bytes (same epoch + seq),
// so the gateway's single MailboxReceiver.Accept must reject it as a stale/replayed seq.
func (p *Phone) Replay(ctx context.Context, env []byte) error {
	_, err := p.relay.MailboxAppend(ctx, p.machineTarget, env)
	return err
}

// advanceCursor moves the shared mailbox read cursor forward, never backward.
func (p *Phone) advanceCursor(to uint64) {
	p.mu.Lock()
	if to > p.cursor {
		p.cursor = to
	}
	p.mu.Unlock()
}
