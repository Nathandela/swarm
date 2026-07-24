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
	KeyStore       crypto.KeyStore    // phone key custody (Noise/relay-auth/command-signing)
	MachineSignPub []byte             // machine Ed25519 grant-signing pub pinned at pairing
	Grant          *crypto.EpochGrant // sealed initial epoch grant delivered by the machine
	Relay          *relay.Client      // the phone's authenticated relay connection
	MachineTarget  string             // machine mailbox routing id (where commands are appended)
	Machine        string             // machine endpoint id, signed into each command tuple
}

// Phone is a simulated device driving phonecore over the relay. It recovers the epoch
// ContentKey from the grant (custody: unexported), and holds a MailboxRouter for OBSERVE
// (demuxing journal records and server-rendered terminal snapshots off the ONE shared
// mailbox seq stream into their respective caches), plus a monotonic command seq for DRIVE.
type Phone struct {
	ks            crypto.KeyStore
	relay         *relay.Client
	router        *phonecore.MailboxRouter
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
// the epoch ContentKey/EpochID, then builds a MailboxRouter bound to that key (its one
// seq guard demuxes journal records and terminal snapshots off the shared mailbox stream).
// It fails closed on any grant that does not open under the pinned key.
func New(cfg Config) (*Phone, error) {
	epochID, _, keys, err := phonecore.AcceptGrant(cfg.KeyStore, cfg.MachineSignPub, cfg.Grant)
	if err != nil {
		return nil, err
	}
	return &Phone{
		ks:            cfg.KeyStore,
		relay:         cfg.Relay,
		router:        phonecore.NewMailboxRouter(keys.ContentKey),
		content:       keys.ContentKey,
		epochID:       epochID,
		machineTarget: cfg.MachineTarget,
		machine:       cfg.Machine,
	}, nil
}

// Observe performs ONE forward scan of the phone's relay mailbox: it reads items past
// the phone's cursor, runs each through the phonecore MailboxRouter (which authenticates
// and seq-guards it, then demuxes journal records into the session cache and terminal
// snapshots into the snapshot cache), advances the cursor, and returns the session cache's
// current sessions. Items the router rejects (not a router frame, or a replay/reorder) are
// skipped. Callers poll it until the cache holds the session (or Snapshot the peek) they
// expect. Draining here populates BOTH caches, so a Snapshot getter reads whatever the
// latest Observe pulled off the shared mailbox.
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
		if _, err := p.router.Accept(it.Envelope); err != nil {
			continue // not a router frame, or a replay/reorder the receiver rejected
		}
	}
	p.advanceCursor(maxCursor)
	return p.router.Sessions().List(), nil
}

// Session returns the phone's cached view of one namespaced session id.
func (p *Phone) Session(id string) (phonecore.CachedSession, bool) {
	return p.router.Sessions().Get(id)
}

// Snapshot returns the phone's latest server-rendered terminal snapshot lines for a
// namespaced session id (found=false until a snapshot has been Observed). The phone is
// THIN: it holds only the sanitized text the daemon rendered, never a VT emulator. Observe
// is what drains snapshots into this cache off the shared mailbox, so callers poll Observe
// then read Snapshot.
func (p *Phone) Snapshot(session string) (lines []string, found bool) {
	snap, ok := p.router.Snapshots().Get(session)
	if !ok {
		return nil, false
	}
	return snap.Lines, true
}

// Watch asks the machine to open a server-rendered terminal peek for a session: it seals
// an UNSIGNED terminal_watch RemoteCommand under the epoch content key with the SHARED
// sequencer and appends it to the machine mailbox, where the gateway routes it to its
// TerminalWatcher (which runs a read-only terminal_subscribe against the daemon and seals
// each snapshot back to this phone's mailbox). A peek is a READ, so the command carries no
// device signature; the daemon gates the peek itself (capability + kill switch). session is
// the target namespaced session id.
func (p *Phone) Watch(ctx context.Context, session string) error {
	return p.appendWatch(ctx, protocol.ActionTerminalWatch, session)
}

// Unwatch stops a terminal peek previously started with Watch, mirroring Watch with the
// terminal_unwatch action.
func (p *Phone) Unwatch(ctx context.Context, session string) error {
	return p.appendWatch(ctx, protocol.ActionTerminalUnwatch, session)
}

// appendWatch seals an unsigned watch/unwatch command and appends it to the machine mailbox.
func (p *Phone) appendWatch(ctx context.Context, action, session string) error {
	env, err := phonecore.SealCommandEnvelope(p.content, p.epochID, p.seq.Next(), protocol.DeviceCommandAuth{
		Action:  action,
		Machine: p.machine,
		Session: session,
	})
	if err != nil {
		return err
	}
	if _, err := p.relay.MailboxAppend(ctx, p.machineTarget, env); err != nil {
		return err
	}
	return nil
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
// routes it -- by the target session id bound INSIDE the sealed frame -- onto that
// session's lease conn -> the daemon -> the session's PTY. It returns the raw wire bytes
// so a caller can Replay them (an adversarial redelivery).
func (p *Phone) Type(ctx context.Context, session string, data []byte) ([]byte, error) {
	env, err := phonecore.SealInputData(p.content, p.epochID, p.seq.Next(), session, data)
	if err != nil {
		return nil, err
	}
	if _, err := p.relay.MailboxAppend(ctx, p.machineTarget, env); err != nil {
		return nil, err
	}
	return env, nil
}

// Resize seals a terminal resize as an input-frame envelope with the SHARED sequencer and
// appends it to the machine mailbox, mirroring Type. session is the target session id.
func (p *Phone) Resize(ctx context.Context, session string, cols, rows int) error {
	env, err := phonecore.SealInputResize(p.content, p.epochID, p.seq.Next(), session, cols, rows)
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
