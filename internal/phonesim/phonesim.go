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
	"errors"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/grant"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// errNoBootstrap is returned when NewFromMailbox scans the mailbox and finds no
// epoch_grant_bootstrap frame: with no ContentKey the phone can do nothing, so it fails closed.
var errNoBootstrap = errors.New("phonesim: no epoch_grant_bootstrap frame in mailbox")

// errStuckPage is returned by NewFromMailbox and drain when a relay page reports
// has_more=true but no item in it advanced past the cursor (an empty page, or every item
// at/behind it). Trusting has_more in that case spins the scan forever against a hostile
// relay (codex#7); a non-advancing page terminates it instead.
var errStuckPage = errors.New("phonesim: relay page did not advance past cursor")

// mailbox is the slice of *relay.Client the phone consumes: PAGED reads to drain its
// mailbox, appends to reach the machine, and acks to compact what it has drained. Taking
// an interface (not the concrete client) lets a test drive the phone with a scripted relay
// -- the untrusted adversary controlling what each read returns.
type mailbox interface {
	MailboxReadPage(ctx context.Context, cursor uint64, limit int) ([]relay.Item, bool, error)
	MailboxAppend(ctx context.Context, target string, env []byte) (uint64, error)
	MailboxAck(ctx context.Context, cursor uint64) error
}

// Config wires a simulated phone to the pairing outcome and the relay. It mirrors what
// a real device holds after pairing + enrollment: its key custody, the pinned machine
// grant-signing pub, the sealed epoch grant, its relay connection, and the machine's
// routing/endpoint targets.
type Config struct {
	KeyStore       crypto.KeyStore    // phone key custody (Noise/relay-auth/command-signing)
	MachineSignPub []byte             // machine Ed25519 grant-signing pub pinned at pairing
	Grant          *crypto.EpochGrant // sealed initial epoch grant delivered by the machine
	Relay          mailbox            // the phone's authenticated relay connection (*relay.Client in production)
	MachineTarget  string             // machine mailbox routing id (where commands are appended)
	Machine        string             // machine endpoint id, signed into each command tuple
}

// Phone is a simulated device driving phonecore over the relay. It recovers the epoch
// ContentKey from the grant (custody: unexported), and holds a MailboxRouter for OBSERVE
// (demuxing journal records and server-rendered terminal snapshots off the ONE shared
// mailbox seq stream into their respective caches), plus a monotonic command seq for DRIVE.
type Phone struct {
	ks            crypto.KeyStore
	relay         mailbox
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

	// drainMu serializes the WHOLE drain sweep (read-page-then-Accept loop, not just the
	// cursor read/write) across concurrent Observe/ReadReply callers, so two sweeps never
	// interleave their router.Accept + cache-apply calls (Finding 3 / sonnet#2).
	drainMu sync.Mutex

	mu     sync.Mutex
	cursor uint64 // phone mailbox read cursor (drained forward by Observe + ReadReply)
	stale  bool   // sticky: set once drain observes a mailbox gap (Finding 1 / codex#5+sonnet#4)
}

// New bootstraps the phone from an IN-PROCESS sealed grant (cfg.Grant): it AcceptGrants
// (verifying the grant against the pinned machine sign pub and opening it with the phone
// KeyStore) to recover the epoch ContentKey/EpochID, then builds a MailboxRouter bound to
// that key (its one seq guard demuxes journal records and terminal snapshots off the shared
// mailbox stream). It fails closed on any grant that does not open under the pinned key.
// NewFromMailbox is the production path that recovers the grant off the relay instead.
func New(cfg Config) (*Phone, error) {
	return newPhone(cfg, cfg.Grant, 0)
}

// NewFromMailbox bootstraps the phone by READING the sealed grant off its relay mailbox --
// the production topology (the gateway delivered it as a tagged plaintext bootstrap frame),
// NOT in-process injection. It scans forward across EVERY bounded page (MailboxReadPage until
// has_more is false), running grant.ParseBootstrap over each item -- a ContentKey-sealed
// router/journal envelope parses ok=false and is cleanly skipped. grant.ParseBootstrap is a
// JSON-shape check only (no crypto), so a hostile relay can plant a well-formed-but-unopenable
// POISON frame; on any frame that shape-accepts but fails to open (newPhone/AcceptGrant), the
// scan CONTINUES rather than failing, so a single poison frame cannot permanently block pairing.
// The FIRST grant that actually opens (verified against the pinned machine sign pub, opened with
// the phone KeyStore) recovers the epoch ContentKey and builds the router, exactly as New does.
// The read cursor is seeded to the CONSUMED frame's cursor, so a later Observe neither reprocesses
// the bootstrap nor skips any real frame that followed it (frames read strictly-after that cursor).
// It fails closed with errNoBootstrap only when the FULL paged scan opens nothing -- with no
// ContentKey the phone can do nothing. It does NOT simply trust has_more: a page that returns no
// item past the cursor terminates the scan with errStuckPage instead of looping forever against a
// hostile relay (codex#7). The bootstrap is one-shot (a single consume the cursor never returns
// to), so it needs no GrantReceiver: this IS the phone's first grant, the same non-monotonic
// AcceptGrant New runs.
func NewFromMailbox(ctx context.Context, cfg Config) (*Phone, error) {
	var cursor uint64
	for {
		items, hasMore, err := cfg.Relay.MailboxReadPage(ctx, cursor, 0)
		if err != nil {
			return nil, err
		}
		advanced := false
		for _, it := range items {
			if it.Cursor > cursor {
				cursor = it.Cursor
				advanced = true
			}
			g, ok := grant.ParseBootstrap(it.Envelope)
			if !ok {
				continue // ContentKey-sealed router/journal envelope -- not a bootstrap frame
			}
			phone, err := newPhone(cfg, g, it.Cursor)
			if err != nil {
				continue // a poison bootstrap (well-formed shape, unopenable) -- keep scanning
			}
			return phone, nil
		}
		if !hasMore {
			return nil, errNoBootstrap
		}
		if !advanced {
			return nil, errStuckPage // has_more=true but no item advanced past cursor
		}
	}
}

// newPhone recovers the epoch ContentKey/EpochID from a sealed grant (verifying it against the
// pinned machine sign pub, opening it with the phone KeyStore) and assembles the Phone with a
// MailboxRouter bound to that key and its mailbox read cursor seeded to startCursor. It fails
// closed on any grant that does not open. Shared by New (grant injected in-process, cursor 0)
// and NewFromMailbox (grant read off the mailbox, cursor at the consumed bootstrap frame).
func newPhone(cfg Config, g *crypto.EpochGrant, startCursor uint64) (*Phone, error) {
	epochID, _, keys, err := phonecore.AcceptGrant(cfg.KeyStore, cfg.MachineSignPub, g)
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
		cursor:        startCursor,
	}, nil
}

// drain performs ONE forward sweep of the phone's relay mailbox and is the SINGLE consumer
// Observe and ReadReply share (C8). drainMu serializes the ENTIRE sweep -- read + Accept loop
// + cursor advance -- across concurrent callers (Finding 3 / sonnet#2), so two sweeps never
// interleave their router.Accept + cache-apply calls (which would let a later frame's Apply
// execute before an earlier frame's, inverting freshness). It reads every bounded page past
// the shared cursor and routes EACH item through the phonecore MailboxRouter EXACTLY ONCE --
// the router authenticates + seq-guards it, then demuxes it by kind: journal records into the
// session cache, terminal snapshots into the snapshot cache, command replies into the reply
// cache (drained by ReadReply). Because every item goes through Accept once, a reply lands in
// the reply cache and a journal frame in the session cache no matter WHICH caller drove the
// read -- neither consumer advances the cursor past the other's frames. Items the router
// rejects (not a router frame, or a replay/reorder) are skipped, and their cursor is still
// consumed so the sweep does not re-read them forever.
//
// It does NOT simply trust has_more: a page that returns no item past the cursor terminates
// the sweep with errStuckPage rather than looping forever against a hostile relay (Finding 2 /
// codex#7).
//
// router.Accept also reports a seq GAP -- a dropped or reordered relay frame (Finding 1 /
// codex#5+sonnet#4). On a gap, drain marks the phone Stale() (sticky: Phase A has no resync to
// clear it) and stops the sweep AT the gap, advancing/acking only the confirmed-good prefix
// before it -- it never tells the relay it may compact past a point known to have a missing
// frame. gap is checked BEFORE err: the seq gap is authenticated the moment the receiver's seq
// guard sees it, even when the SAME frame also fails its kind-specific decode (an unrecognised
// kind, or a malformed frame under a future protocol version) -- a decode failure must never
// silently erase a real gap (round-4 re-audit, codex#3+sonnet#2). The gapped frame itself was
// already authenticated (and applied, if it also decoded) by router.Accept before the gap bool
// is even returned, so a later sweep resumes past it once its now-duplicate re-read is rejected
// as a stale seq by the crypto guard.
func (p *Phone) drain(ctx context.Context) error {
	p.drainMu.Lock()
	defer p.drainMu.Unlock()

	p.mu.Lock()
	cursor := p.cursor
	p.mu.Unlock()

	for {
		items, hasMore, err := p.relay.MailboxReadPage(ctx, cursor, 0)
		if err != nil {
			return err
		}
		advanced := false
		for _, it := range items {
			gap, err := p.router.Accept(it.Envelope)
			// A gap is authenticated the moment the seq guard sees it, even when the SAME
			// frame also fails its kind-specific decode (an unrecognised kind, or a malformed
			// frame under a future protocol version) -- honor it regardless of err, or a
			// decode failure would silently erase a real gap (round-4 re-audit, codex#3 +
			// sonnet#2).
			if gap {
				p.markStale()
				p.advanceCursor(cursor)
				return p.relay.MailboxAck(ctx, cursor) // stop AT the gap: never ack past it
			}
			if err != nil {
				if it.Cursor > cursor {
					cursor = it.Cursor
					advanced = true
				}
				continue // not a router frame, or a replay/reorder the receiver rejected
			}
			if it.Cursor > cursor {
				cursor = it.Cursor
				advanced = true
			}
		}
		if !hasMore {
			break
		}
		if !advanced {
			return errStuckPage // has_more=true but no item advanced past cursor
		}
	}
	p.advanceCursor(cursor)
	return p.relay.MailboxAck(ctx, cursor)
}

// Observe drains the phone's relay mailbox (routing journal records into the session cache
// and terminal snapshots into the snapshot cache via the shared drain) and returns the
// session cache's current sessions. Callers poll it until the cache holds the session (or
// Snapshot the peek) they expect. Draining populates BOTH caches, so a Snapshot getter reads
// whatever the latest Observe pulled off the shared mailbox.
func (p *Phone) Observe(ctx context.Context) ([]phonecore.CachedSession, error) {
	if err := p.drain(ctx); err != nil {
		return nil, err
	}
	return p.router.Sessions().List(), nil
}

// Session returns the phone's cached view of one namespaced session id.
func (p *Phone) Session(id string) (phonecore.CachedSession, bool) {
	return p.router.Sessions().Get(id)
}

// Stale reports whether drain has ever observed a mailbox gap (router.Accept's gap=true --
// a seq the relay skipped, i.e. a dropped or reordered frame). It is STICKY: once set it
// never clears itself, because Phase A has no resync path to repair the missing update
// (Phase B). A caller that cares about cache freshness (e.g. before acting on a Snapshot
// or Session) should check this alongside Observe/ReadReply.
func (p *Phone) Stale() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stale
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

// DriveLaunch signs a launch with the phone command-signing key over the reserved
// LaunchSessionSentinel (a launch has no target session yet), binding the LaunchReq spec
// into the signature via ContentHash = LaunchContentHash(req) so a relay/gateway cannot
// alter the agent/cwd/options/prompt of a validly-signed launch. It seals command AND spec
// into one envelope under the epoch content key with the SHARED sequencer and appends it to
// the machine mailbox, where the gateway forwards it (OpLaunch) and the daemon verifies the
// signature + capability + launch policy and spawns the session. It mirrors DriveKill; seq
// is the shared per-epoch counter SealLaunchEnvelope requires to be unique.
func (p *Phone) DriveLaunch(ctx context.Context, req *protocol.LaunchReq, operationID string) error {
	cmd, err := phonecore.SignCommand(p.ks, phonecore.CommandInput{
		Action:      protocol.ActionLaunch,
		Machine:     p.machine,
		Session:     protocol.LaunchSessionSentinel,
		OperationID: operationID,
		ExpiresAt:   time.Now().Add(time.Minute),
		ContentHash: protocol.LaunchContentHash(req),
	})
	if err != nil {
		return err
	}

	env, err := phonecore.SealLaunchEnvelope(p.content, p.epochID, p.seq.Next(), cmd, req)
	if err != nil {
		return err
	}
	if _, err := p.relay.MailboxAppend(ctx, p.machineTarget, env); err != nil {
		return err
	}
	return nil
}

// ReadReply returns the sealed control reply the gateway returns after executing a command.
// It drains the mailbox through the SHARED drain -- routing every item through the router
// exactly once, so a journal frame that arrives alongside the reply lands in the session
// cache instead of being skipped past -- then pops the oldest demuxed reply off the router's
// reply cache (found=false when none is pending). It does NOT re-scan raw items via
// OpenControlReply: the drain is the one consumer that advances the shared cursor, so a reply
// an earlier Observe already demuxed is still returned here (C8).
func (p *Phone) ReadReply(ctx context.Context) (protocol.Control, bool, error) {
	if err := p.drain(ctx); err != nil {
		return protocol.Control{}, false, err
	}
	ctrl, ok := p.router.Replies().Take()
	return ctrl, ok, nil
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

// markStale sets the sticky gap flag (see Stale).
func (p *Phone) markStale() {
	p.mu.Lock()
	p.stale = true
	p.mu.Unlock()
}
