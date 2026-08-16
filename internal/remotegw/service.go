package remotegw

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
)

// ServiceConfig configures the gateway runtime. The Service depends only on the
// daemon remote socket path and the relay Mailbox seam, so it runs against a real
// relay in production and a fake one in tests.
type ServiceConfig struct {
	DaemonSocket string           // the daemon remote.sock the journal bridge dials
	Relay        Mailbox          // the relay client (machine mailbox read + phone mailbox append)
	Forwarder    CommandForwarder // optional override; nil => the built-in Gateway forwards commands
	PhoneTarget  string           // the phone's relay routing id (journal + reply target)
	// Machine is this machine's endpoint id, stamped on every reconcile record. A phone
	// REFUSES an authority stamped with a machine that is not the one it paired with, so
	// leaving this empty publishes a record no paired phone will adopt -- and an unadopted
	// authority is the same fail-closed refusal of mutating ops as no record at all. Callers
	// that assemble a Service for real traffic MUST set it (and GrantSeq).
	Machine string
	// Profile is the machine's sealed RemoteProfileV1 (ADR-017 T5), threaded straight into
	// RelayConfig.Profile so it reaches every reconcile record this runtime seals. Before
	// this field existed RelayConfig.Profile was reachable from nowhere but a test --
	// exactly B34's "a fence guarding a path production did not take" shape, one layer up
	// (ADR-016 "profile" wiring gap).
	Profile protocol.RemoteProfileV1
	Key     crypto.ContentKey // the epoch content key shared with the phone
	// WakeKey is the CONTENT-FREE key the push trigger seals its wakes under (PB-KEY-2,
	// PB-PUSH-0). It reaches the sidecar either way -- machineid.unmarshal already reads
	// it into this process at startup alongside the content key and both private signing
	// keys, and resolveGatewayParams merely dropped it -- so naming it here widens the
	// package's key inventory, not the process's exposure (ADR-007 B19). It is handed ONLY
	// to the PushConfig; nothing else in the gateway may see it.
	WakeKey        crypto.WakeKey
	EpochID        uint32  // the epoch the content key belongs to
	GrantSeq       uint64  // the machine identity's grant-issuance seq (authority (c), see gatewayAuthorities)
	RecipientKeyID [8]byte // phone routing key id stamped on sealed journal envelopes
	SenderKeyID    [8]byte // this machine's routing key id
	// NOTE: there is deliberately NO command-IN poll cadence here. PB-NET-5 requires
	// the old 500 ms poll to be DROPPED, not tuned: the command loop is driven by the
	// relay's bounded server-side wait (CommandBridge.Run), and re-introducing an
	// interval field would re-introduce the failure ADR-007:461 calls "unusable for
	// live typing".
	ReconnectDelay time.Duration    // journal reconnect backoff (default 1s)
	LeaseAwait     time.Duration    // how long take_control waits for the lease grant (default 5s)
	Now            func() time.Time // envelope issued-at clock (nil => time.Now)
	JournalSeq     SeqSource        // durable outbound seq for journal + terminal frames (nil => in-memory)
	ReplySeq       SeqSource        // durable outbound seq for command replies (nil => in-memory)
	// PushSeq is the durable replay coordinate stamped on every wake (PB-PUSH-3). It is a
	// THIRD stream, separate from the journal and reply seqs, because a wake is sealed
	// under a different key and opened by a different receiver on the phone. Nil =>
	// in-memory, which restarts at 1 and would have the phone's persisted coordinate
	// (PB-STATE-1) reject every wake after a gateway restart; production wires the file.
	PushSeq SeqSource
	// PushPrefs is the durable push preference (PB-PUSH-8, PB-PUSH-10). ONE object serves
	// both directions: the command bridge writes it when the daemon authorizes a change,
	// the notifier reads it before every wake. Nil leaves the verb refused and every wake
	// suppressed -- fail closed, since a misassembled gateway must not be the one
	// configuration in which every push goes out unfiltered.
	PushPrefs PushPrefsSource
	// Durable OUTBOUND journal outbox (PB-GW-8): {journal cursor, sealed envelope, relay
	// outcome}, which is what makes the resume point survive a restart AND makes a
	// delivery-unknown append recoverable by re-appending the identical envelope. Nil =>
	// in-memory, i.e. every restart re-reads from 0 and re-appends the whole journal.
	Outbox Outbox
	// Durable INBOUND checkpoint (PB-GW-1): the mailbox read cursor and the
	// per-(sender,epoch) replay high-water, seeded into the command bridge at
	// construction. Nil => in-memory (resets on restart), which leaves the replay guard
	// blind after a restart -- production always wires the file.
	Inbound InboundState
	// PushGateway configures the ADR-015 P9 wake-obligation machine as an alternative to
	// the legacy relay push_trigger transport (ADR-015 P12). Nil (the default) means this
	// pairing has not migrated: the push path is exactly what it is today, byte-for-byte
	// unchanged -- Pusher is the relay client discovered from cfg.Relay, with no
	// TransportRouter in front of it at all.
	PushGateway *PushGatewayConfig
	// Post-revocation confidentiality (codex#1): the epoch key + phone target are fixed for
	// this process's lifetime, so after the owner revokes the paired device (rotating the
	// epoch key) a still-running gateway would reconnect and reseal epoch frames to the
	// revoked mailbox under the STALE key. On each journal reconnect the runtime re-reads
	// <StateDir>/devices and, if DeviceID is gone, exits instead. Both empty disables the
	// check (unit tests that do not provision a registry).
	StateDir string // state dir whose <StateDir>/devices registry is re-read on reconnect
	DeviceID string // this gateway's paired device; its removal triggers a graceful exit
}

// PushGatewayConfig configures the ADR-015 P9 wake-obligation machine for one pairing.
//
// TODO(pairing-conveyance): GatewayURL, SubmitCapability and Address are, for this wave,
// sourced from static machine configuration (cmd/swarm-remote/config.go's optional
// push-gateway.json) rather than the real PG-MIG-2 per-pairing conveyance -- the phone
// allocating an address and handing it to this machine over an authenticated
// pairing-update. THE SAME IS TRUE OF THE WAKE KEY: ServiceConfig.WakeKey (fed into
// WakeObligationConfig below, not a field on this struct) is sourced from
// id.EpochKeys().WakeKey, epoch material, rather than ADR-015 P7's phone-generated,
// per-pairing key conveyed inside that same pairing transcript and DELIBERATELY not
// epoch material -- precisely so an ADR-011 M5 epoch rotation does not invalidate a push
// binding. Left sourced from epoch material, an epoch rotation silently breaks every
// WakeV1 tag on this pairing with nothing failing on the machine: the mirror image of
// the half-migration P8 delta 2 exists to forbid. That conveyance, and ADR-018's
// eventual N-pairings widening, are a later slice; see pushtransport.go's own
// TODO(pairing-conveyance) on TransportStore, which this config feeds. This wave's
// contract does not change when that lands.
type PushGatewayConfig struct {
	GatewayURL       string // the push gateway's base URL, e.g. https://push.example.com
	SubmitCapability string // this pairing's submit capability (spec §2.2)
	// MachineRevokeCapability is the pairing's machine-revoke capability (spec §2.2/3.4,
	// PG-AUTH-9: DISTINCT from submit), carried verbatim from push-gateway.json for the
	// revoke producer (revokeproducer.go, bead agents-tracker-u37c). Empty on every
	// pre-producer provisioning: the producer then cannot run and must say so --
	// degraded and disclosed, never silently required.
	MachineRevokeCapability string
	Address                 PushAddress
	// Transport is the durable push_transport selection (PG-MIG-1). Nil => in-memory,
	// which defaults to legacy_relay (PG-MIG-1/2's starting state) and is not durable
	// across a restart -- production wires the file.
	Transport TransportStore
	// Obligations is the durable wake-obligation custody (PG-OBL-1). Nil => in-memory,
	// which loses every non-terminal obligation across a restart -- production wires the
	// file.
	Obligations ObligationStore
	// WakeSeq is the durable, per-pairing wake_seq coordinate (PG-WAKE-16). It is
	// DELIBERATELY separate from ServiceConfig.PushSeq (the legacy 78-byte wake's seq):
	// the two are different wire objects with different receivers, and sharing a counter
	// would have them stale-drop each other exactly as PushSeq's own doc comment already
	// argues for JournalSeq vs PushSeq. Nil => in-memory, which restarts at 1 and would
	// have the phone's persisted high-water reject every wake after a restart.
	WakeSeq SeqSource
}

// ErrDeviceRevoked is returned by Run when the gateway's paired device is no longer in the
// registry: the owner revoked it (rotating the epoch key), so the gateway shuts down rather
// than reconnecting and resealing epoch journal/snapshot frames to the revoked device's
// mailbox under the now-stale key (codex#1 / post-revocation confidentiality).
var ErrDeviceRevoked = errors.New("remotegw: paired device revoked; gateway exiting")

// ErrRelayGone is returned by Run when the relay connection it was given DIED. It is the
// signal the process that owns the dial redials on (PB-NET-4).
//
// It has to be an identity distinct from ctx.Err(), because those are opposite
// instructions: a cancelled context is the operator stopping the sidecar and must not be
// redialled, while a dead link is an outage and must be.
var ErrRelayGone = errors.New("remotegw: relay connection lost")

// LinkWatcher is the relay client's LIVENESS seam: a channel closed when the connection
// dies underneath its holder. *relay.Client satisfies it (its read pump closes Done when
// the socket breaks), and it is the ONLY way to learn of a drop while idle -- a loop that
// notices only when a request fails cannot see a link that dies with nothing outstanding.
//
// It is optional, like PushTriggerer: a Mailbox that cannot report liveness (every
// unit-test fake) simply gets no observation. The production wiring is pinned at compile
// time in cmd/swarm-remote.
type LinkWatcher interface {
	Done() <-chan struct{}
}

// Service is the supervised gateway runtime (R-GW.1): it composes the journal-OUT
// bridge (Gateway.RunJournal delivering to a RelaySink that seals and appends to the
// phone's mailbox) and the command-IN loop (CommandBridge polling the machine's
// mailbox) over one relay connection. It is the body of the cmd/swarm-remote sidecar
// process; a crash leaves the daemon and its sessions untouched (S1) and the runtime
// resumes journal delivery from its last durable cursor.
type Service struct {
	cfg      ServiceConfig
	gw       *Gateway
	sink     *RelaySink
	notifier *PushNotifier
	bridge   *CommandBridge
	leases   *LeaseManager
	watchers *TerminalWatcher
	// wakeMachine/wakeObligations are set only when cfg.PushGateway is configured, and
	// exist so RedrivePendingWakeObligations (PG-OBL-8) has something to re-drive at
	// startup without reaching back into cfg. wakeRetry (PG-OBL-9, set under the same
	// condition) is the retry scheduler wrapped around wakeMachine; it is ALSO the
	// TransportRouter's gateway arm, so every Drive -- live trigger or startup redrive --
	// arms the timer-driven backoff on a retryable outcome.
	wakeMachine     *WakeObligationMachine
	wakeObligations ObligationStore
	wakeRetry       *WakeRetryScheduler
}

// NewService builds a runtime over cfg. It wires a RelaySink onto a Gateway for the
// journal-OUT direction and a CommandBridge for the command-IN direction, both bound
// to the same content key and phone target.
func NewService(cfg ServiceConfig) *Service {
	if cfg.ReconnectDelay <= 0 {
		cfg.ReconnectDelay = time.Second
	}
	if cfg.LeaseAwait <= 0 {
		cfg.LeaseAwait = 5 * time.Second
	}
	// The inbound checkpoint and the reply seq are shared between the command bridge that
	// ADVANCES them and the authorities that PUBLISH them, so the record the phone adopts
	// describes this runtime's live coordinates. Defaulted here rather than inside
	// NewCommandBridge, which would leave the authorities reading a different object.
	inbound := cfg.Inbound
	if inbound == nil {
		inbound, _ = OpenInboundState("", "") // in-memory, cannot error
	}
	replySeq := cfg.ReplySeq
	if replySeq == nil {
		replySeq, _ = OpenSeqSource("") // in-memory, cannot error
	}
	sink := NewRelaySink(RelayConfig{
		Appender:       cfg.Relay,
		Target:         cfg.PhoneTarget,
		Machine:        cfg.Machine,
		EpochID:        cfg.EpochID,
		Key:            cfg.Key,
		RecipientKeyID: cfg.RecipientKeyID,
		SenderKeyID:    cfg.SenderKeyID,
		Now:            cfg.Now,
		Seq:            cfg.JournalSeq,
		Outbox:         cfg.Outbox,
		Profile:        cfg.Profile,
		// PB-SYNC-7 wired for real: WITHOUT an authority source the sink publishes no
		// reconcile record, and a phone that fails closed on RequireReconciled then refuses
		// every mutating op FOREVER with nothing in the tree failing -- the permanent brick
		// PB-SYNC-7 exists to prevent, re-created at the seam.
		Authorities: gatewayAuthorities{
			inbound:  inbound,
			reply:    replySeq,
			epoch:    cfg.EpochID,
			grantSeq: cfg.GrantSeq,
		},
	})
	// PB-PUSH-0: the push trigger sits BETWEEN the coalescer and the sealing sink, so the
	// journal it watches is the one the gateway actually delivers. Outside the coalescer it
	// would hide CoalescingSink.Flush from RunTerminal's idle wake and PB-GW-7's trailing
	// terminal flush would die silently; the notifier forwards SetMachine and
	// DeliveredCursor so the coalescer still reaches the sink through it.
	//
	// The relay client is BOTH the mailbox and the push transport, so the pusher is
	// discovered from cfg.Relay rather than configured separately. A Mailbox that cannot
	// push (every unit-test fake) leaves it nil, which is the supported no-push
	// configuration -- not an error.
	var pusher PushTriggerer
	if pt, ok := cfg.Relay.(PushTriggerer); ok {
		pusher = pt
	}
	// ADR-015 P9/P12: when this pairing has migrated (cfg.PushGateway set), the push path
	// is a TransportRouter in front of the legacy relay pusher discovered above, so
	// selection stays EXCLUSIVE (P12) and legacy_relay keeps working byte-for-byte should
	// the pairing roll back. cfg.PushGateway == nil (every pairing until it migrates)
	// leaves `pusher` exactly as it always has been -- no router, no obligation machine.
	var wakeMachine *WakeObligationMachine
	var wakeObligations ObligationStore
	var wakeRetry *WakeRetryScheduler
	if cfg.PushGateway != nil {
		wakeObligations = cfg.PushGateway.Obligations
		if wakeObligations == nil {
			wakeObligations, _ = OpenObligationStore("") // in-memory, cannot error
		}
		transport := cfg.PushGateway.Transport
		if transport == nil {
			transport, _ = OpenTransportStore("") // in-memory, cannot error; defaults legacy_relay
		}
		wakeSeq := cfg.PushGateway.WakeSeq
		if wakeSeq == nil {
			wakeSeq, _ = OpenSeqSource("") // in-memory, cannot error
		}
		wakeMachine = NewWakeObligationMachine(WakeObligationConfig{
			Store: wakeObligations,
			Submitter: &HTTPWakeSubmitter{
				BaseURL:          cfg.PushGateway.GatewayURL,
				SubmitCapability: cfg.PushGateway.SubmitCapability,
			},
			WakeKey: cfg.WakeKey,
			Address: cfg.PushGateway.Address,
			Seq:     wakeSeq,
			Now:     cfg.Now,
		})
		// PG-OBL-9: the retry scheduler wraps the machine and stands in the router's
		// gateway arm, so a retryable submit failure on ANY drive -- a live trigger as
		// much as the startup redrive -- arms a timer-driven backoff bounded by the
		// obligation's own expiry, instead of waiting for an unrelated trigger or redial
		// to happen along before it. Trigger/Drive/Supersede all delegate to the machine,
		// so the router's ordering contract (push_obligation_order_test.go) is unchanged.
		wakeRetry = NewWakeRetryScheduler(WakeRetryConfig{
			Machine: wakeMachine,
			Store:   wakeObligations,
			Address: cfg.PushGateway.Address,
			Now:     cfg.Now,
			// PB-PUSH-8 on the redrive/retry paths: the scheduler re-reads the SAME
			// durable preference the notifier gates live wakes on, so an obligation that
			// survived a crash cannot carry a trigger-time authorization past a user who
			// has since turned push off.
			Prefs: cfg.PushPrefs,
		})
		pusher = &TransportRouter{Transport: transport, Legacy: pusher, Gateway: wakeRetry}
	}
	notifier := NewPushNotifier(sink, PushConfig{
		Pusher:  pusher,
		Target:  cfg.PhoneTarget,
		WakeKey: cfg.WakeKey,
		EpochID: cfg.EpochID,
		Now:     cfg.Now,
		Seq:     cfg.PushSeq,
		Prefs:   cfg.PushPrefs,
	})
	// PB-GW-7: the journal, the terminal peek and the reconcile record share ONE sink, ONE
	// relay target and ONE per-target append quota, so the peek's ~62 snapshots/s must be
	// coalesced to the §6.0 budget or it exhausts the target's tumbling minute and starves
	// the journal alongside it. Admission policy is a WRAPPER, not a change inside the sink.
	gw := New(cfg.DaemonSocket, NewCoalescingSink(CoalesceConfig{Inner: notifier, Now: cfg.Now}))
	forwarder := cfg.Forwarder
	if forwarder == nil {
		forwarder = gw
	}
	// The input plane: take_control opens a persistent lease conn on the daemon
	// remote.sock, and every keystroke/resize for that session rides THAT conn.
	leases := NewLeaseManager(cfg.DaemonSocket, cfg.LeaseAwait)
	// The peek plane: terminal_watch runs a read-only terminal_subscribe against the daemon
	// (via the SAME Gateway/RelaySink as the journal), sealing each rendered snapshot to the
	// phone. It reconnects on the journal backoff cadence.
	watchers := NewTerminalWatcher(gw, cfg.ReconnectDelay)
	bridge := NewCommandBridge(CommandBridgeConfig{
		Mailbox:     cfg.Relay,
		Forwarder:   forwarder,
		Leases:      leases,
		Watchers:    watchers,
		Key:         cfg.Key,
		EpochID:     cfg.EpochID,
		ReplyTarget: cfg.PhoneTarget,
		ReplySeq:    replySeq,
		Inbound:     inbound,
		Prefs:       cfg.PushPrefs,
		Resync:      gw,
	})
	return &Service{
		cfg: cfg, gw: gw, sink: sink, notifier: notifier, bridge: bridge, leases: leases, watchers: watchers,
		wakeMachine: wakeMachine, wakeObligations: wakeObligations, wakeRetry: wakeRetry,
	}
}

// RedrivePendingWakeObligations re-drives every non-terminal wake obligation this process
// finds durable at startup (PG-OBL-8): an obligation left in_flight or pending by a crash
// -- or by a Trigger that coalesced into a live obligation Drive never got to run for --
// is retried here rather than waiting for an unrelated future trigger to happen to land on
// the same address before the obligation's five-minute expiry. It is a no-op when this
// pairing has not migrated off legacy_relay (cfg.PushGateway unset).
//
// The one pass over Pending() at startup is PG-OBL-8's shape; the drive itself goes
// through the PG-OBL-9 retry scheduler (bd agents-tracker-hggx.4.3), so a redrive whose
// submit fails RETRYABLY arms the timer-driven, expiry-bounded backoff rather than
// waiting for an unrelated trigger or redial to land before the obligation's five
// minutes run out.
func (s *Service) RedrivePendingWakeObligations(ctx context.Context) error {
	if s.wakeRetry == nil || s.wakeObligations == nil {
		return nil
	}
	pending, err := s.wakeObligations.Pending()
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}
	// This wave's scope is one pairing (one address) per Service (see PushGatewayConfig's
	// TODO), so one Drive against the configured Address is the whole redrive regardless
	// of how many non-terminal records Pending() reports; a future ADR-018 N-pairings
	// widening is what would make this iterate distinct addresses. A redrive failure here
	// must not stop the rest of gateway startup (PG-OBL-3's failure-isolation applies at
	// boot exactly as it does live).
	return s.wakeRetry.Drive(ctx)
}

// gatewayAuthorities is the PRODUCTION ReconcileSource: each authority is read from the
// gateway's own durable state at the moment the record is sealed, never cached at process
// start, so a reconnect republishes what is true now.
//
// (a) is the durable INBOUND accepted high-water (PB-GW-1) for the sender-zero stream --
// every phone -> machine seal leaves SenderKeyID unset (phonecore input.go / command.go), so
// that is the only stream the phone sends on. (b)'s reply half is the reply SeqSource's
// Issued watermark, which is the highest seq ISSUED and never the durable reservation
// ceiling (a phone seeded at the ceiling would stale-drop a whole block of live replies);
// (b)'s journal half is the sink's own seq and is not in this interface. (c) is the machine
// identity's grant-issuance coordinate.
//
// No method fabricates a zero on failure -- the interface returns errors for exactly that
// reason -- and none can fail here: InboundState.Load is infallible by construction
// (custody was validated at OpenInboundState) and Issued is a memory read.
type gatewayAuthorities struct {
	inbound  InboundState
	reply    SeqSource
	epoch    uint32
	grantSeq uint64
}

func (a gatewayAuthorities) InboundHighWater() (uint64, error) {
	return a.inbound.Load().Highest[InboundStream{Sender: [8]byte{}, Epoch: a.epoch}], nil
}

func (a gatewayAuthorities) ReplyCeiling() (uint64, error) { return a.reply.Issued(), nil }

func (a gatewayAuthorities) GrantWatermark() (uint32, uint64, error) {
	return a.epoch, a.grantSeq, nil
}

// Gateway exposes the underlying journal bridge (e.g. to seed or read its cursor).
func (s *Service) Gateway() *Gateway { return s.gw }

// CommandBridge exposes the underlying command loop (e.g. to seed its cursor).
func (s *Service) CommandBridge() *CommandBridge { return s.bridge }

// PushNotifier exposes the push trigger, whose Err() is the ONLY signal that the wake path
// is degraded: a push failure never fails a journal record, so without reading this a
// gateway that has stopped waking the phone entirely is indistinguishable from one that
// simply has nothing to say.
func (s *Service) PushNotifier() *PushNotifier { return s.notifier }

// Err is the runtime's DEGRADED STATE: the first error each of the three components that
// store one has seen, joined, or nil when none has.
//
// IT EXISTS BECAUSE NOTHING READ ANY OF THEM. CommandBridge.Err, RelaySink.Err and
// PushNotifier.Err each stash a first error precisely so a condition that must not fail a
// record is still observable -- a state dir that fails every checkpoint persist and so
// drops every keystroke, a relay that answers no mailbox wait, a wake path that has stopped
// ringing the phone. The tree contained no non-test caller of any of the three
// (ADR-007 B114), so all three were writes to a channel with no reader and an operator
// learned nothing. This is the reader; cmd/swarm-remote prints it to the unit's log.
//
// It is deliberately a SNAPSHOT of first errors rather than a stream: each component's
// stored error is the root cause it saw, not the latest symptom, and a gateway that has
// recovered still owes the operator the reason it was degraded.
//
// The fourth member is PG-OBL-10 (bd agents-tracker-hggx.4.5): the wake-obligation
// store's CURRENT record for the configured push address, read live rather than stashed,
// because the pairing's push health IS its last obligation's outcome -- a later
// obligation that delivers replaces the record and clears the state, exactly as
// PG-OBL-10's "last obligation" wording intends.
func (s *Service) Err() error {
	return errors.Join(s.bridge.Err(), s.sink.Err(), s.notifier.Err(), s.wakeObligationErr())
}

// wakeObligationErr surfaces PG-OBL-10's degraded push state: a migrated pairing whose
// LAST wake obligation reached `expired` or `abandoned` has a phone that has stopped
// receiving wakes, and the one operator-visible surface (this Err, printed to the unit's
// log by cmd/swarm-remote) must say so -- naming the terminal state AND the last outcome
// code, because the code is the repair path (address_revoked means re-pair;
// push_token_unregistered means the handset must rotate its token). `delivered` is a
// working push path, `pending`/`in_flight` are still inside their retry horizon, and
// `superseded` is the user's own preference honoured -- none of the three is degraded ON
// ITS OWN, though the latter two still report a degraded PREDECESSOR they carry (below).
// An unmigrated service (no obligation custody at all) reports nothing here.
func (s *Service) wakeObligationErr() error {
	if s.wakeObligations == nil || s.cfg.PushGateway == nil {
		return nil
	}
	ob, ok, err := s.wakeObligations.Get(s.cfg.PushGateway.Address)
	if err != nil {
		return fmt.Errorf("remotegw: push degraded: wake-obligation store unreadable: %w", err)
	}
	if !ok {
		return nil
	}
	switch ob.State {
	case ObligationExpired, ObligationAbandoned:
		return fmt.Errorf("remotegw: push degraded (PG-OBL-10): this pairing's last wake obligation reached %q (last outcome %q) -- the phone is not receiving wakes", ob.State, ob.LastOutcome)
	case ObligationPending, ObligationInFlight, ObligationSuperseded:
		// A record whose PREDECESSOR ended degraded is still a degraded pairing:
		// PG-OBL-6's re-mint replaces an expired-with-coalesced-triggers record inside
		// the very Drive call that expires it, and without reading the carried-over
		// prior state this surface would report the busiest failing pairing -- the one
		// whose wakes keep expiring with more triggers always waiting -- as healthy.
		// A DELIVERY is what clears it, which is why `superseded` is read here rather
		// than in the healthy default: a supersede puts nothing on the wire, so it
		// cannot prove a revoked address or a dead token repaired -- it would merely
		// have written over the evidence and reported healthy until the next real wake
		// attempt re-derived the same failure.
		if ob.PriorTerminalState == ObligationExpired || ob.PriorTerminalState == ObligationAbandoned {
			return fmt.Errorf("remotegw: push degraded (PG-OBL-10): this pairing's previous wake obligation reached %q (last outcome %q) and no wake has delivered since -- the phone is not receiving wakes", ob.PriorTerminalState, ob.PriorTerminalOutcome)
		}
		return nil
	default:
		return nil
	}
}

// Progressed reports whether traffic actually crossed the relay link during this runtime's
// life: the command loop's bounded wait completed at least once, which takes an answer from
// the relay and cannot be faked by a socket that is merely up.
//
// IT IS THE RECONNECT BACKOFF'S RESET CONDITION, and that is the whole reason it is not
// simply "did the dial succeed". A relay that completes the websocket handshake and then
// answers nothing keeps every connection alive while every call reaches its deadline; a
// backoff reset by connection would turn that into a fixed-rate redial cycle an adversary
// drives for free, with the relay never having to send a byte. Evidence of progress is the
// property that costs the far end something.
func (s *Service) Progressed() bool { return s.bridge.RelayReplies() > 0 }

// Run drives both loops until ctx is cancelled, then returns ctx.Err(). The two
// loops are independent: a failing journal connection (retried with ReconnectDelay)
// does not stall the command loop, and vice versa.
//
// IT ALSO ENDS WHEN THE RELAY CONNECTION DIES, returning ErrRelayGone. The Service is
// given ONE already-dialled Mailbox and cannot redial it; the process that owns the dial
// can, and it can only act on a Run that returns. Before this, a link that dropped left
// both loops running against a dead client forever -- the command loop retrying a wait
// that could never answer, the journal loop happily reconnecting to the DAEMON and sealing
// frames into an append that could never leave -- so the sidecar never exited, no
// supervision policy ever fired, and remote control was over until a human intervened.
func (s *Service) Run(ctx context.Context) error {
	// Tear down every live lease conn AND every terminal peek on shutdown so no daemon
	// connection (control gate or read-only tap) is left behind after the sidecar exits.
	defer func() { _ = s.leases.Close() }()
	defer func() { _ = s.watchers.Close() }()
	// End the PG-OBL-9 retry tail with the generation that armed it: a Service is one
	// relay generation (cmd/swarm-remote builds a fresh one per redial), and an armed
	// backoff chain left running would keep submitting wakes -- concurrently with the
	// NEXT generation's scheduler -- for up to WakeV1Expiry after this Run returned.
	// The next generation's RedrivePendingWakeObligations picks the live obligation up.
	if s.wakeRetry != nil {
		defer s.wakeRetry.Stop()
	}
	// PB-GW-8: re-append whatever the outbox reserved but never saw acked BEFORE any new
	// frame goes out, so a delivery-unknown record is recovered by its IDENTICAL sealed
	// envelope (which the phone stale-drops for free) rather than re-sealed at a fresh seq.
	// A replay that fails is stashed on the sink (Err) and its entries stay pending for the
	// next start; it must not stop the bridge from running.
	_ = s.sink.Replay()
	// Derive a cancelable context so the journal loop can tear the WHOLE Service down (both
	// loops) the moment it detects the paired device was revoked (codex#1) -- and so the
	// link watcher below can do the same the moment the relay connection dies. parent is
	// kept because the two are opposite instructions to the caller: a cancelled parent must
	// NOT be redialled, a dead link must.
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Parent the peek watchers to the Service ctx so a revoke (cancel below) stops every peek
	// reconnecting IMMEDIATELY and structurally -- not incidentally via the kill switch, and not
	// only when the deferred watchers.Close runs after wg.Wait returns (opus#2).
	s.watchers.bindParent(ctx)
	var revoked, linkGone atomic.Bool
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if s.runJournal(ctx) {
			revoked.Store(true)
			cancel() // stop the command loop too, so Run returns promptly
		}
	}()
	go func() { defer wg.Done(); _ = s.bridge.Run(ctx) }()
	// PB-NET-4: watch the RELAY connection itself. This is the only observer of a link that
	// dies while nothing is outstanding, and it is in the WaitGroup rather than left to the
	// deferred cancel so it is joined here rather than merely expected to end.
	if w, ok := s.cfg.Relay.(LinkWatcher); ok {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-ctx.Done():
			case <-w.Done():
				linkGone.Store(true)
				cancel() // stop both loops: neither can do anything over a dead connection
			}
		}()
	}
	wg.Wait()
	if revoked.Load() {
		return ErrDeviceRevoked
	}
	// The parent is asked BEFORE the link, so a connection that dies as the sidecar is being
	// stopped is reported as the shutdown it is and does not provoke a redial.
	if err := parent.Err(); err != nil {
		return err
	}
	if linkGone.Load() {
		return ErrRelayGone
	}
	return ctx.Err()
}

// runJournal runs the journal bridge, reconnecting after ReconnectDelay whenever the
// connection drops, until ctx is cancelled. RunJournal resumes from the last delivered
// cursor, so a reconnect loses no events. It returns true when it stopped because the
// paired device was definitively revoked (deviceRevoked) so the caller tears the whole
// Service down.
func (s *Service) runJournal(ctx context.Context) (revoked bool) {
	for {
		if ctx.Err() != nil {
			return false
		}
		_ = s.gw.RunJournal(ctx)
		if ctx.Err() != nil {
			return false
		}
		// The daemon severed the journal connection. A device REVOKE severs it (C2a) and
		// rotates the epoch key, so before reconnecting re-read the registry: if our paired
		// device is CONFIRMED gone we must NOT resume sealing epoch frames to its mailbox under
		// the now-stale key (codex#1). A device still present -- or a registry we cannot read
		// right now -- is an ordinary reconnect: back off and re-check next cycle.
		if s.deviceRevoked() {
			return true
		}
		// Back off before reconnecting, but wake immediately on cancel.
		t := time.NewTimer(s.cfg.ReconnectDelay)
		select {
		case <-ctx.Done():
			t.Stop()
			return false
		case <-t.C:
		}
	}
}

// deviceRevoked reports whether this gateway's paired device is DEFINITIVELY revoked: the
// on-disk registry read SUCCEEDED and this gateway's DeviceID is ABSENT (the owner revoked it,
// rotating the epoch key). It re-reads <StateDir>/devices FRESH on each call so a revocation is
// observed on the next journal reconnect.
//
// It deliberately distinguishes "definitively gone" from "cannot read right now": a device.Open
// error (a torn read, a transiently-unavailable/network-mounted stateDir, a MkdirAll/ReadFile
// hiccup) is NOT a confirmed revocation, so it returns false and the caller keeps reconnecting
// and re-checks next cycle. This check runs on EVERY routine daemon reconnect, so treating a
// transient FS error as a revocation (the prior behavior) would let one coincidental hiccup
// silently and permanently kill remote control until a human restarts the sidecar (Finding 1,
// codex#6 / sonnet#3 / opus#1). The fail-closed intent is preserved for the case we can actually
// confirm -- a successful read showing the device gone still exits promptly. An empty StateDir or
// DeviceID disables the check (returns false) -- used by unit tests that do not provision a
// registry.
//
// Follow-up: device.Open does MkdirAll/Chmod on the registry dir on this read path; a read-only
// registry accessor would avoid writing during the liveness check, but adding one means editing
// device/registry.go (owned elsewhere), so it is deferred.
func (s *Service) deviceRevoked() bool {
	if s.cfg.StateDir == "" || s.cfg.DeviceID == "" {
		return false
	}
	reg, err := device.Open(filepath.Join(s.cfg.StateDir, "devices"))
	if err != nil {
		return false // cannot read the registry right now: not a confirmed revocation -- retry next cycle
	}
	_, ok := reg.Get(s.cfg.DeviceID)
	return !ok
}
