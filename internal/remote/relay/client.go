package relay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Item is one stored mailbox entry as the relay serves it: the relay's own
// monotonic storage cursor (untrusted ordering, DISTINCT from the authenticated
// per-epoch seq inside the envelope) and the opaque ciphertext envelope.
type Item struct {
	Cursor   uint64 `json:"cursor"`
	Envelope []byte `json:"envelope"`
}

// PresenceState is a party's coarse reachability as the relay sees it.
type PresenceState string

const (
	// PresenceUnknown means the relay has no live record (e.g. after restart —
	// presence is never persisted).
	PresenceUnknown PresenceState = "unknown"
	// PresenceOffline means the gateway dropped and the silent-push bound elapsed.
	PresenceOffline PresenceState = "offline"
	// PresenceOnline means a live authenticated connection is bound.
	PresenceOnline PresenceState = "online"
)

// PresenceInfo is the presence answer for a routing id.
type PresenceInfo struct {
	State PresenceState `json:"state"`
}

// ClientAuth carries the only key a party ever discloses to the untrusted relay:
// its Ed25519 relay-auth public key, plus a signer over the relay's challenge.
// The signer is a closure so a hardware-gated key never leaves its boundary.
//
// Sign can FAIL (ADR-007 B18(a)): the only production phone-side implementation is
// crypto.KeyStore.SignRelayAuth, which a hardware-gated custody refuses. Swallowing
// that refusal here — or signing nil and letting the relay reject opaquely — would
// re-create one layer up exactly the errorless interface B14 removed.
type ClientAuth struct {
	RelayAuthPub ed25519.PublicKey
	Sign         func(challenge []byte) ([]byte, error)

	// Peer is the routing id of the counterparty whose revocation of THIS identity the
	// dialer wants to be told about, and it is optional (ADR-007 B49).
	Peer string
}

// Conn is a raw, unauthenticated framed connection to the relay over a single
// websocket. Pairing rendezvous rides it (pairing peers are not yet relay-
// registered); authenticated clients wrap it (see Dial).
type Conn struct {
	ws     *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex // serialises one request/response exchange
	wmu    sync.Mutex // serialises socket writes: a parked wait writes OUTSIDE mu

	// frames is non-nil on a PUMPED connection: a background reader owns every
	// socket read, so the connection's death is observed while the caller is
	// idle (see Done). A raw Conn reads inline, which is what the adversarial
	// framing paths want.
	frames chan pumpedFrame
	// peerSPKI records what the peer's certificate was on a DialRawSecure dial (ADR-007
	// B48). Nil on every other constructor, which reads as "nothing observed".
	peerSPKI *spkiObserver
	// owed counts requests written whose reply frame the PUMP has not yet routed:
	// roundtrip raises it (under owedMu) BEFORE its request write reaches the
	// socket, and the pump ALONE lowers it, atomically with claiming the frames
	// slot for an arriving frame. A clean ordinary frame that finds owed == 0
	// therefore provably answers no written request -- it is owed to NOBODY, and
	// queueing it would hand it to the next exchange as its answer, shifting every
	// reply on the connection one question back for the connection's life
	// (committee finding H1). The pump drops such frames instead.
	//
	// The accounting lives WHOLLY in the pump (committee round 3, codex finding 3)
	// because the previous shape -- a shared atomic the consumer decremented on
	// read -- gave the drop rule a TOCTOU: the pump sampled the count and then
	// performed a potentially BLOCKING send into the capacity-1 channel, so a
	// legitimate reply could pass the non-zero check while a stray occupied the
	// slot, park, and enter the queue AFTER the count had reached zero -- the
	// exact shift the rule exists to prevent. With the check, the decrement and
	// the slot claim fused under owedMu, and the pump the only decrementer, the
	// count a frame is judged against cannot go stale: a full channel is entered
	// blocking only AFTER the decrement, and the pump is one goroutine, so the
	// next frame it reads still sees a truthful count however long the consumer
	// takes to drain the slot.
	owedMu sync.Mutex
	owed   int
	// discard counts replies owed to exchanges that ABANDONED them (timeout/cancel
	// after a successful write). It is the ROUND-4 UNIFICATION of what used to be two
	// independent ledgers -- the abandoned credit left parked in `owed` (pump-side)
	// and a roundtrip-side `skip` the next caller spent on whatever it happened to
	// read -- which could spend on DIFFERENT frames: an idle-time stray spent the
	// abandoned owed credit (the pump enqueued the stray), the skip then spent itself
	// on that same stray, and the abandoned exchange's late reply arrived against the
	// next LIVE exchange's fresh credit and was adopted as its answer -- wrong data,
	// nil error (Opus round-4 finding F3, reproduced 5/5;
	// TestCommitteeR4_AnIdleStrayCannotRedirectAnAbandonedLateReplyOntoALiveExchange
	// is the permanent probe). Now an abandonment moves the credit OUT of owed and
	// into this counter, owned like owed by the pump's critical section, so one
	// abandonment is exactly one credit and it can only ever be spent once.
	//
	// HOW THE PUMP SPENDS IT: a clean frame that arrives while a live exchange is
	// waiting (owed > 0) is judged against discard FIRST -- the stream is in-order
	// and every abandoned request was written before the live one, so the abandoned
	// stragglers arrive AHEAD of the live reply; dropping the next `discard` such
	// frames before delivering one is exactly the FIFO attribution. A clean frame
	// that arrives while NOTHING is owed stays the round-3 unsolicited free drop and
	// spends NO credit: the committee's probe is precisely the world where that frame
	// is a hostile stray and the credit must survive it for the real straggler still
	// in flight.
	//
	// THE PRICE OF THAT CHOICE, and its bound. The idle-time frame is unattributable
	// -- an honest straggler landing at idle is free-dropped too, and its surviving
	// credit will be spent on the NEXT live exchange's own reply, timing that
	// exchange out spuriously. Unmitigated this CASCADES: the timed-out exchange
	// re-mints a credit that eats its successor, forever. abandonReply therefore
	// SUPPRESSES the re-mint when a discard credit was already spent inside the
	// abandoning exchange's own window (`spent` below) AND that credit's lifetime
	// was marked by an observed idle free drop (`idleLeak`/`spentLeaked` below):
	// each leaked credit is consumed by at most ONE bounded casualty and the
	// connection recovers
	// (TestCommitteeR4_AnIdleArrivingLateReplyNeverWedgesTheConnection pins the
	// recovery).
	//
	// WHY THE SUPPRESSION IS CONDITIONED, not unconditional (round-4 fix wave). The
	// two worlds a spent-then-abandoned window can sit in are distinguishable after
	// all, by evidence this side of the wire already holds: the idle-leak world
	// (where suppression is correct -- the abandoned exchange's own reply was
	// ALREADY consumed by the leaked credit, so minting would start the cascade)
	// necessarily contains a clean-frame FREE DROP at owed == 0 while a discard
	// credit was outstanding, and the honest double-slow world (two back-to-back
	// timeouts against one stall) contains none -- there the credit spent inside
	// the window paid for the PREDECESSOR's straggler, the abandoning exchange's
	// own reply is still genuinely in flight, and an unconditional suppression
	// would leave it uncredited to be adopted by the successor as wrong data with
	// a nil error, a one-back shift persisting until the first idle gap
	// (TestCommitteeR4_AnHonestDoubleTimeoutKeepsFIFOAttributionForTheSuccessor
	// pins that corner). What still cannot be told apart without the wire-format
	// correlation ids the committee has twice declined is WHICH outstanding credit
	// an idle drop leaked when several coexist -- idleLeak taints the whole
	// outstanding-discard epoch, one boolean, not one flag per credit.
	discard int
	// idleLeak records that a clean frame was free-dropped at owed == 0 while at
	// least one discard credit was outstanding -- the observable signature of the
	// idle-leak world, and the sole license for abandonReply's suppressed re-mint.
	// Set by the pump at the free drop; cleared when the outstanding credits are
	// fully spent (discard back to 0), having first transferred onto the spending
	// window as spentLeaked.
	idleLeak bool
	// spent counts discard credits the pump spent inside the CURRENT live exchange's
	// window, and spentLeaked whether any of them carried the idleLeak taint (both
	// reset when roundtrip raises owed for its write). They exist solely for
	// abandonReply's suppressed re-mint, above: suppression demands a credit spent
	// inside THIS window AND the observed idle leak that marks it as the abandoned
	// predecessor's own reply rather than an honest straggler of a slow stall.
	spent       int
	spentLeaked bool
	// dropped/lastDropLog throttle the unsolicited-frame log line (round 3, Opus
	// finding 5): the drop path is driven entirely by peer-sent frames, so an
	// unthrottled print is a log-amplification lever the peer pulls for free.
	// Touched only by the pump goroutine, hence unlocked.
	dropped     uint64
	lastDropLog time.Time

	// The bounded server-side wait's client half (ADR-007 B7). A wait is the one
	// exchange that does NOT hold mu across write-then-read: it registers a
	// correlated waiter, writes its request, and parks on waitCh while ordinary
	// requests keep flowing through roundtrip. pump routes MsgWaitReply frames
	// straight here, which is the demux that stops a parked wait from
	// head-of-line-blocking the keystrokes it exists to accelerate. §6.0 caps
	// pending waits at one per client, so a single slot is the whole structure.
	waitMu  sync.Mutex
	waitSeq uint64
	waitID  uint64
	waitCh  chan waitReplyBody

	// callTimeout bounds ONE request/reply exchange (see roundtrip). Zero means
	// unbounded and is deliberate on a raw connection: rendezvous_recv is a long
	// poll for a human-paced ceremony, and the pairing caller declares its own
	// deadline (mobile/pairing.go's pairingTTL).
	callTimeout time.Duration

	done      chan struct{}
	doneOnce  sync.Once
	closeOnce sync.Once
	closeErr  error
}

// pumpedFrame is one decoded frame, or the decode failure that replaced it.
type pumpedFrame struct {
	tag     MsgType
	payload []byte
	err     error
}

// DefaultCallTimeout bounds one authenticated request/reply exchange.
//
// IT EXISTS BECAUSE SILENCE IS THE RELAY'S CHEAPEST ATTACK. The relay is the declared
// adversary and it need not misbehave to wedge a client: it can complete the websocket
// handshake, accept every frame, and reply to none. roundtrip holds c.mu across
// write-then-read and readFrame waits on the socket, so ONE unanswered exchange used to park
// every producer on the connection -- with no error, no state change and nothing to retry.
// A half-open TCP after a WiFi -> cellular handoff presents identically, so this is reached
// benignly as well as adversarially.
//
// THE SAME BOUND THE GATEWAY ALREADY APPLIES TO THIS CLIENT, and for the same argument:
// remotegw's defaultAppendTimeout ("an UNBOUNDED append against a hung relay would pin that
// lock forever and wedge every producer AND Err()", Blocker 2). What that fixed was one
// caller; this fixes the client, so a call site nobody audited -- and every call site the
// phone has, which all pass context.Background() -- inherits the bound rather than the wedge.
//
// TEN SECONDS IS §6.0'S, NOT THIS AUTHOR'S. The budget table binds "Non-wait request timeout |
// 10 s" to PB-NET-7 under a preamble that reads "Changing any value requires committee
// agreement, not implementer discretion"
// (docs/specifications/remote-phaseB-requirements.md). This constant first shipped at five
// seconds on latency grounds -- generous against PB-NET-5's 150 ms p50 and against
// push_trigger's one-second verdict wait, and matching App.awaitConn's five-second patience.
// Every word of that argument may still be true and none of it is this file's to make: a
// budget an implementer may re-derive locally is not a budget, and the row was NOT MET while
// the two disagreed (ADR-007 B99).
//
// The 10 s used to live only as transport.RequestTimeout, in a package the shipped phone does
// not use -- so the value bound to a requirement existed exclusively in code nothing ran,
// which is the same defect as the fence-guarding-a-dead-path this constant was written to
// close. That package has since been deleted (B98), so this is now the ONLY place the budget
// exists at all, and TestCallDeadline_TheNonWaitRequestTimeoutIsTheCommitteeBudget pins it
// here, on the live path, so the next divergence is a failing test rather than a review round.
const DefaultCallTimeout = 10 * time.Second

// DefaultDialTimeout bounds the CONNECT phase of every dial: the TCP handshake, the TLS
// handshake, the HTTP response and the websocket upgrade -- every stage before the first
// frame.
//
// IT EXISTS BECAUSE THE DIAL HAPPENS BEFORE EVERY OTHER BOUND. DefaultCallTimeout bounds an
// exchange on an open connection and the gateway bounds its own MailboxWait, and NEITHER IS
// EVER REACHED by a caller still inside the dial. A relay that accepts the TCP connection and
// then stalls -- no ServerHello, no response head, a half-written 101 -- costs itself one file
// descriptor and parks its caller for as long as it cares to hold the socket. A half-open TCP
// after a WiFi -> cellular handoff presents identically, so this is benign as well as
// adversarial.
//
// WHAT IT COSTS EACH SHIPPED CALLER, differently, and NOT ONE OF THE THREE declares a deadline:
//
//	THE PHONE never enters backoff. mobile/app.go passes context.WithCancel(context.Background()),
//	and App.run's reconnect schedule runs BETWEEN dial attempts -- a dial that never returns is a
//	dial that never fails, so PB-NET-4's backoff never runs once.
//
//	THE GATEWAY, cmd/swarm-remote, passes signal.NotifyContext(context.Background(), ...) and
//	starts by dialling, so a stalled dial is a sidecar that never starts and never says why.
//
//	THE PAIRING RENDEZVOUS burns the owner connection's pairing SLOT. internal/skeleton's
//	relayRendezvousFactory dials inside the closure BeginPairing calls, on the owner connection's
//	lifetime context -- and BEFORE pairing.go builds pairCtx, so ADR-007 B64's window is not yet
//	in force. The slot is already claimed and is released only by `result` or by BeginPairing's
//	error return; a dial that never returns takes neither, there is no pair_cancel op, and
//	dropping the owner connection is the only exit.
//
// THE BOUND IS HERE, at the one seam every dial passes, and not at the callers. THREE callers
// exist and all three got it wrong independently -- and only two of them were known when this
// was written, which is the argument rather than a prediction about it: a per-caller fix would
// have been applied to the set we happened to know, and that set was wrong. It is also the
// quantifier B115 left open at the sibling defect ("fixing the one instance does not close a
// quantifier").
//
// AND THE THIRD SITE SHOWS WHY THE CALLER IS THE WRONG PLACE even when it is remembered. The ctx
// relayRendezvousFactory receives has DUAL DUTY -- it bounds the dial AND owns the connection's
// lifetime, via the `go func(){ <-ctx.Done(); _ = conn.Close() }()` watcher three lines down --
// so a caller-side `defer cancel()` there closes the connection it just returned. Bounding at
// this seam has no such hazard at any of the three.
//
// TEN SECONDS IS SECTION 6.0'S, REUSED RATHER THAN RE-DERIVED. The connect phase IS one
// non-wait request/reply -- an HTTP GET carrying the upgrade, answered by a 101 -- and the
// budget table binds "Non-wait request timeout | 10 s" to PB-NET-7. Minting a second, local
// dial budget is what ADR-007 B99 refused.
//
// AND THE COMPOSITION LANDS EXACTLY ON THE RELAY'S OWN PRE-AUTH WINDOW, which is the
// corroboration TestDialDeadline_TheWholeDialFitsTheRelaysOwnPreAuthWindow pins. A whole dial
// is this connect phase plus two non-wait requests (auth_init, auth_resp, each bounded at the
// same 10 s by DefaultCallTimeout) = 30 s; the relay bounds the SAME window from its own side
// at Config.HandshakeTimeout -- 30 s, "CUMULATIVE time-to-authenticate, anchored at accept
// time" (preAuthDeadline), one of the Phase A constants section 6.0's preamble names as the
// values its table is chosen to be consistent with. Past that instant an honest peer has
// already hung up.
//
// ONLY THAT NUMBER IS BORROWED FROM THE ADVERSARY, never its enforcement. B112 recorded that
// exact error -- "MailboxWait bypasses it by construction under the relay's own 25 s ceiling",
// measured 2.8x past that ceiling -- and the deadline below is declared and enforced on this
// side, whatever the peer does.
//
// WHAT IS STILL THE COMMITTEE'S TO SAY, recorded rather than decided here: section 6.0 has no
// row for a dial or a connect, so "the upgrade is one non-wait request" is a READING of an
// existing row, not a row. It is the narrowest reading available -- it mints nothing and it
// composes onto a constant already in the table -- but a committee that wanted a distinct dial
// budget would be setting it, not confirming this.
const DefaultDialTimeout = 10 * time.Second

// dialConn opens one websocket. hc is the dial client a security policy built
// (nil for the policy-free paths, which take the websocket package's default).
func dialConn(ctx context.Context, url string, hc *http.Client, pumped bool) (*Conn, error) {
	var opts *websocket.DialOptions
	if hc != nil {
		opts = &websocket.DialOptions{HTTPClient: hc}
	}
	// THE DEADLINE ENDS AT THE DIAL, not at the connection: cancelling it once the handshake
	// has returned must not disturb the socket, and does not. A 101 leaves net/http holding
	// errCallerOwnsConn and cancelling the request that produced it (transport.go), so the
	// connection is the caller's; coder/websocket ships the same pattern for its own
	// HTTPClient.Timeout handling (dial.go's cloneWithDefaults + deferred cancel).
	//
	// The connection's OWN context, below, is rooted at Background for the same reason: a
	// connection must outlive the dial that opened it.
	dctx, dcancel := context.WithTimeout(ctx, DefaultDialTimeout)
	defer dcancel()
	ws, _, err := websocket.Dial(dctx, url, opts)
	if err != nil {
		return nil, err
	}
	ws.SetReadLimit(MaxFrame + 64)
	cctx, cancel := context.WithCancel(context.Background())
	c := &Conn{ws: ws, ctx: cctx, cancel: cancel, done: make(chan struct{})}
	if pumped {
		// PUMPED IS THE AUTHENTICATED DIAL (Dial, DialSecure), and it is the whole of the
		// bounded surface: every exchange on it is a short request/reply, and the one
		// operation that legitimately parks -- MailboxWait -- does not go through roundtrip
		// at all. A RAW connection is left unbounded because its rendezvous_recv is a
		// deliberate long poll (see the callTimeout field).
		c.callTimeout = DefaultCallTimeout
		c.frames = make(chan pumpedFrame, 1)
		go c.pump()
	}
	return c, nil
}

// pump owns every read on a pumped connection and exits (closing Done) as soon
// as the socket dies, which is what makes an idle drop observable.
func (c *Conn) pump() {
	defer c.markDone()
	for {
		mt, data, err := c.ws.Read(c.ctx)
		if err != nil {
			return
		}
		var f pumpedFrame
		if mt != websocket.MessageBinary {
			f.err = fmt.Errorf("relay: unexpected websocket message type %v", mt)
		} else {
			f.tag, f.payload, f.err = ReadFrame(bytes.NewReader(data))
		}
		// A wait reply is CORRELATED by its request id and handed straight to the
		// parked waiter, so it neither queues behind nor jumps ahead of the
		// serialised request/reply exchanges (ADR-007 B7). It also never blocks the
		// pump, which is what keeps an outstanding wait from stalling the socket.
		if f.err == nil && f.tag == MsgWaitReply {
			c.deliverWait(f.payload)
			continue
		}
		// A clean frame that answers NO outstanding request is a protocol violation by
		// the peer -- the relay's contract is one in-order reply per request, plus the
		// correlated MsgWaitReply handled above. Queueing it would hand it to the NEXT
		// exchange as its answer and shift every reply on this connection one question
		// back, permanently (committee finding H1; the shape a pre-wait relay's MsgError
		// refusal of a blindly-probed mailbox_wait arrives in). Dropped, loudly but
		// rate-limited. Decode FAILURES are deliberately still forwarded: they end the
		// connection below, and a waiting reader learns why instead of blocking until
		// Done.
		//
		// The owed/discard checks, the decrement and the claim on the queue slot are
		// ONE critical section (see the owed field): deciding to enqueue and only
		// then blocking into a full channel is the round-2 TOCTOU. When the slot is
		// taken, the blocking send below is entered with owed ALREADY lowered, so
		// the next frame this loop reads -- necessarily after the send completes --
		// is judged against a truthful count.
		//
		// The judgment order is the round-4 single ledger (see the discard field):
		// a frame nobody live is waiting for is an unsolicited FREE drop, spending
		// no credit; a frame arriving inside a live window pays any pending discard
		// BEFORE it may be delivered, because the in-order stream puts every
		// abandoned straggler ahead of the live reply.
		//
		// The frame is DROPPED rather than the connection torn down, on two grounds
		// the committee's round-2 fence pins (SHAPE 2 of
		// TestCommittee_AnUnsolicitedFrameDoesNotShiftReplyPairing): a stray consumed
		// mid-flight costs exactly one bounded casualty and the connection survives,
		// and a teardown here would hand any peer able to volunteer one frame a
		// reconnect lever. The drop is provably safe now that the pump owns the
		// count: a frame judged against owed == 0 answers nothing that was ever
		// written, so dropping it can displace no real reply.
		if f.err == nil {
			c.owedMu.Lock()
			if c.owed == 0 {
				if c.discard > 0 {
					// A free drop while a discard credit is outstanding is the
					// idle-leak world's signature (see the idleLeak field): if
					// this frame was an honest straggler, its surviving credit
					// will eat a later live reply, and only this observation
					// licenses that exchange's suppressed re-mint.
					c.idleLeak = true
				}
				c.owedMu.Unlock()
				c.noteUnsolicitedDrop(f.tag)
				continue
			}
			if c.discard > 0 {
				c.discard--
				c.spent++
				if c.idleLeak {
					// The credit this window just spent belongs to a tainted
					// epoch: an idle free drop was observed while it was
					// outstanding, so the frame consumed here may well be the
					// live exchange's OWN reply (the leak's casualty). Mark the
					// window; clear the epoch once its credits are gone.
					c.spentLeaked = true
					if c.discard == 0 {
						c.idleLeak = false
					}
				}
				c.owedMu.Unlock()
				continue // an abandoned exchange's straggler, ahead of the live reply
			}
			c.owed--
			select {
			case c.frames <- f:
				c.owedMu.Unlock()
				continue
			default:
				// Defensively deliver outside the lock. Under the round-4 ledger this
				// arm is unreachable for a clean frame -- the slot is claimed only at
				// owed == 1, after which owed == 0 free-drops everything until the
				// consumer or abandonReply empties it -- but a blocking send under
				// owedMu would deadlock the pump if that proof ever rotted.
				c.owedMu.Unlock()
			}
		}
		select {
		case c.frames <- f:
		case <-c.ctx.Done():
			return
		}
		if f.err != nil {
			return
		}
	}
}

func (c *Conn) markDone() { c.doneOnce.Do(func() { close(c.done) }) }

// noteUnsolicitedDrop records one dropped unsolicited frame and logs at most one
// line per second, carrying the running count so the suppressed drops stay visible
// in the line that does print (Opus round-2 finding 5: the drop path is driven by
// peer-sent frames, so an unthrottled print is peer-controlled log amplification).
// Pump-goroutine only, hence no lock.
func (c *Conn) noteUnsolicitedDrop(tag MsgType) {
	c.dropped++
	if time.Since(c.lastDropLog) < time.Second {
		return
	}
	c.lastDropLog = time.Now()
	log.Printf("relay: dropped an unsolicited %v frame no request is owed (%d dropped on this connection); the peer is violating the one-reply-per-request contract", tag, c.dropped)
}

// owedAdd adjusts the pump's owed-reply count. A raw (unpumped) connection has no
// pump, no queue and no drop rule, so there is nothing to account.
//
// Raising the count opens a new live window, so the per-window spend record resets
// with it (see the spent field): what abandonReply must know is whether a discard was
// spent inside ITS exchange's window, never a predecessor's.
func (c *Conn) owedAdd(d int) {
	if c.frames == nil {
		return
	}
	c.owedMu.Lock()
	c.owed += d
	if d > 0 {
		c.spent = 0
		c.spentLeaked = false
	}
	c.owedMu.Unlock()
}

// abandonReply is roundtrip's exit for an exchange that gave up on its reply (timeout
// or cancellation after a successful write): the exchange's credit must not stay live,
// or the next arriving frame -- owed to THIS abandoned request -- would be delivered to
// the next caller as its answer.
//
// Three cases, decided in one critical section with the pump's own accounting:
//
//  1. The reply was ALREADY ROUTED (it sits in the queue): consume it here, which
//     retires the exchange completely -- the pump lowered owed when it enqueued, and
//     nothing of this exchange remains in flight. Without this drain the frame would
//     sit in the capacity-1 queue and be read by the next exchange as its own answer.
//  2. The reply is STILL IN FLIGHT: move the credit from owed to discard, so the pump
//     drops it on arrival instead of delivering it to the next caller (the round-4
//     single ledger; see the discard field).
//  3. The reply is still in flight BUT a discard credit was already spent inside this
//     exchange's own window AND that credit carried the observed-idle-leak taint
//     (spentLeaked): lower owed and mint NOTHING. This is the suppressed re-mint that
//     bounds the unattributable-idle-frame leak -- without it, one honest straggler
//     landing while the connection is idle turns into a permanent cascade of spurious
//     timeouts (each eaten reply re-minting the credit that eats the next). The taint
//     condition keeps the suppression out of the honest double-slow world, where the
//     spent credit paid for a PREDECESSOR's straggler and this exchange's own reply is
//     still genuinely in flight: there the mint is exactly the FIFO discard the
//     successor needs, and suppressing it hands the successor this exchange's answer
//     as wrong data with a nil error (see the discard field's WHY paragraph; both
//     worlds are pinned in committee_r4_ledger_test.go).
func (c *Conn) abandonReply() {
	if c.frames == nil {
		return
	}
	c.owedMu.Lock()
	defer c.owedMu.Unlock()
	select {
	case <-c.frames:
		return // case 1: routed already; consuming it retires the exchange whole
	default:
	}
	c.owed--
	if c.spent > 0 && c.spentLeaked {
		return // case 3: the suppressed re-mint, licensed by the observed idle leak
	}
	c.discard++ // case 2
}

// Done is closed when the connection is no longer usable. On a pumped
// connection (Dial, DialSecure) that happens as soon as the peer or the network
// drops it; on a raw one it happens at Close.
func (c *Conn) Done() <-chan struct{} { return c.done }

// DialRaw opens an unauthenticated framed connection (rendezvous + adversarial
// framing use it).
//
// It applies NO transport-security policy: the URL is dialed as given. Production
// callers use DialRawSecure -- see the note on Dial.
func DialRaw(ctx context.Context, url string) (*Conn, error) {
	return dialConn(ctx, url, nil, false)
}

// DialRawSecure is DialRaw under a transport-security policy (PB-NET-2). It is the
// pairing rendezvous's entry point: no relay-auth key is disclosed there, but it is the
// first packet either end sends to a URL a scanned QR chose, so the same refusal applies
// and it is decided before a socket is opened.
func DialRawSecure(ctx context.Context, url string, sec Security) (*Conn, error) {
	// One observer per dial (ADR-007 B48): what this connection's peer presented, not what
	// some other dial under a shared policy value presented.
	obs := &spkiObserver{}
	sec.observer = obs
	cfg, err := sec.resolve(url)
	if err != nil {
		return nil, err
	}
	c, err := dialConn(ctx, url, sec.httpClient(cfg), false)
	if err != nil {
		return nil, err
	}
	c.peerSPKI = obs
	return c, nil
}

// PeerSPKI is the SHA-256 SubjectPublicKeyInfo digest of the certificate this
// connection's peer presented, or nil if none was seen (a cleartext loopback dial, or a
// dial that did not go through DialRawSecure).
//
// IT EXISTS FOR EXACTLY ONE COMPARISON (ADR-007 B48). The pairing rendezvous dials
// UNVERIFIED, because it is the dial that fetches the pin that would verify it, and B45
// accepted that on the grounds that the certificate never protected the Noise payload.
// True of the payload, and B48 amends it: that ruling also lowered the cost of B46's
// consent harvest from "hold a certificate valid for the operator's relay" to "be on the
// path". So the certificate is recorded here and compared, when msg2 arrives, against the
// RelaySPKIPin the real machine authored. A network attacker terminating this TLS cannot
// make the two agree.
//
// WHAT IT DOES NOT COVER, stated so it is not assumed: a QR-holder is a legitimate party
// to the ceremony and presents the relay's real certificate. That case is the SAS gate's
// and the deferred consent's (B52), not this.
func (c *Conn) PeerSPKI() []byte { return c.peerSPKI.get() }

func (c *Conn) writeFrame(ctx context.Context, tag MsgType, payload []byte) error {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, tag, payload); err != nil {
		return err
	}
	// wmu, not mu: a parked wait writes its request and its cancellation without
	// holding the request/reply lock, so two writers can reach the socket at once.
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.ws.Write(ctx, websocket.MessageBinary, buf.Bytes())
}

func (c *Conn) readFrame(ctx context.Context) (MsgType, []byte, error) {
	if c.frames == nil {
		mt, data, err := c.ws.Read(ctx)
		if err != nil {
			return 0, nil, err
		}
		if mt != websocket.MessageBinary {
			return 0, nil, fmt.Errorf("relay: unexpected websocket message type %v", mt)
		}
		return ReadFrame(bytes.NewReader(data))
	}
	// A frame already delivered by the pump wins over a concurrently-observed
	// death, so the last reply before a drop is not discarded.
	select {
	case f := <-c.frames:
		return f.tag, f.payload, f.err
	default:
	}
	select {
	case f := <-c.frames:
		return f.tag, f.payload, f.err
	case <-c.done:
		return 0, nil, ErrConnClosed
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	}
}

// WriteMsg sends one raw framed message using the connection's own context.
func (c *Conn) WriteMsg(tag MsgType, payload []byte) error {
	return c.writeFrame(c.ctx, tag, payload)
}

// ReadMsg receives one raw framed message using the connection's own context.
func (c *Conn) ReadMsg() (MsgType, []byte, error) { return c.readFrame(c.ctx) }

// CloseNow severs the connection WITHOUT the websocket close handshake, for a caller
// that is ABANDONING the exchange rather than finishing it.
//
// Close below cancels the connection's own context and then performs the polite close,
// which waits up to five seconds for the peer's close frame -- and cannot observe one,
// because cancelling the context has already stopped the reader. So an aborted
// connection pays that timeout in full, every time. That was invisible while nothing
// waited on the teardown; it is five seconds on the caller's shutdown path as soon as
// something does.
//
// It shares Close's once, so whichever teardown runs first decides and the other is a
// no-op: a graceful Close already in flight is not turned into an abort by a late
// CloseNow, or the reverse.
func (c *Conn) CloseNow() error {
	c.closeOnce.Do(func() {
		c.cancel()
		c.closeErr = c.ws.CloseNow()
		c.markDone()
	})
	return c.closeErr
}

// Close severs the connection. It is idempotent.
func (c *Conn) Close() error {
	c.closeOnce.Do(func() {
		c.cancel()
		c.closeErr = c.ws.Close(websocket.StatusNormalClosure, "")
		c.markDone()
	})
	return c.closeErr
}

// roundtrip writes one request frame and reads exactly one reply, mapping an
// r_error reply to its sentinel error.
//
// It runs under the connection's call deadline (DefaultCallTimeout on an
// authenticated connection), so a relay that answers nothing fails the call
// instead of parking it.
//
// A caller that abandons its reply (context deadline or cancellation) leaves the
// exchange outstanding rather than tearing the connection down; abandonReply moves
// the exchange's credit into the pump's discard ledger (or consumes the reply if it
// already arrived), so a slow answer is dropped by the pump on arrival and never
// mistaken for the answer to a later question.
func (c *Conn) roundtrip(ctx context.Context, tag MsgType, req any) (json.RawMessage, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	// THE DEADLINE IS TAKEN BEFORE THE EXCHANGE LOCK, and that ordering is the difference
	// between bounding one call and bounding the plane. c.mu is held across write-then-read,
	// so a caller queued behind one that is waiting out its own deadline would otherwise
	// start its clock only once it acquired the lock -- and K queued callers would serialise
	// into K deadlines. Bounded from issue, every caller returns within one deadline of when
	// IT was issued, whatever is ahead of it: the caller that inherits an already-expired
	// context fails at the write, before it can spend a frame on a relay that is not
	// answering.
	ctx, cancel := c.bounded(ctx)
	defer cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	// Raised BEFORE the write reaches the socket, so the pump can never observe the
	// reply to this request while owed still reads zero and drop it as unsolicited.
	// The stream is in-order, so any frame that arrives before this write completes
	// belongs to an earlier request or to nobody -- never to this one.
	c.owedAdd(1)
	if err := c.writeFrame(ctx, tag, body); err != nil {
		// Nothing was sent; no reply is owed. Should the frame have reached the wire
		// anyway (a write that failed after the socket took it), its reply arrives
		// exactly as a peer-volunteered stray would and is bounded the same way.
		c.owedAdd(-1)
		return nil, c.callErr(err)
	}
	rtag, payload, err := c.readFrame(ctx)
	if err != nil {
		// This exchange's reply was never consumed: retire its credit through the
		// pump's own ledger (consume it if it already arrived, else leave one
		// discard for the pump to spend on it).
		c.abandonReply()
		return nil, c.callErr(err)
	}
	if rtag == MsgError {
		// THE STRAY QUARANTINE, the drop rule's other half (committee round 3).
		// The pump's owed count closes one race window -- a frame judged while a
		// stray occupies the queue -- but there is a second, complementary one
		// that NO client-side accounting can close: when a stray was consumed as
		// this exchange's answer, the reply it displaced is still in flight, and
		// if the next request is written before the pump reads it, the displaced
		// frame is indistinguishable from that request's own answer ("did this
		// frame arrive before my write" is unobservable through a lagging
		// reader). What the client CAN control is when the next request is
		// written. Every stray in the H1 family is an MsgError (a pre-wait
		// relay's refusal of an unknown op), so after adopting an error reply --
		// the one moment a violation may just have displaced a frame -- the
		// exchange lock is held for a beat before returning.
		//
		// WHAT HOLDS DURING THE WINDOW, stated exactly (Opus round-4 F4 corrected
		// the round-3 wording, which claimed this before it was true): owed is
		// zero for the whole window ONLY under the round-4 ledger, where an
		// abandoned exchange's credit moves out of owed at abandonment -- under
		// the round-3 ledger an abandoned credit still parked in owed could admit
		// the displaced frame mid-quarantine. Now this frame's decrement has
		// already happened, mu blocks any new writer, so owed reads zero; and a
		// frame arriving at owed == 0 is a FREE drop that spends no discard
		// credit either, so the quarantine cannot leak an abandoned exchange's
		// credit onto the displaced frame. A genuine refusal pays 5ms on a path
		// that already failed; a displaced reply lagging beyond the window is the
		// bounded residual the evidence file records. Raw connections read
		// inline, have no pump and no drop rule, so there is nothing to
		// quarantine.
		if c.frames != nil {
			time.Sleep(strayQuarantine)
		}
		return nil, decodeError(payload)
	}
	return json.RawMessage(payload), nil
}

// strayQuarantine is how long roundtrip keeps the exchange lock after adopting an
// MsgError reply, giving the pump time to judge -- against a provably-zero owed
// count -- any frame a protocol violation may have displaced. Loopback delivery
// and a netpoll wakeup are microseconds; five milliseconds is three orders of
// magnitude of margin without being noticeable on a path that is already a
// refusal.
const strayQuarantine = 5 * time.Millisecond

// bounded applies this connection's call deadline to ctx. A connection with no
// deadline of its own (a raw one) returns ctx untouched, so its long poll stays a
// long poll.
//
// The caller's own deadline still wins when it is EARLIER: context.WithTimeout
// never extends an existing one, so a caller that wants a tighter bound has one
// and a caller that passes context.Background() -- which is every phone call site
// -- gets this.
func (c *Conn) bounded(ctx context.Context) (context.Context, context.CancelFunc) {
	if c.callTimeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, c.callTimeout)
}

// callErr names a failed exchange in terms a caller can route on.
//
// IT MUST BE RECOGNISABLE, not merely non-nil. The phone routes errors by Go identity
// (mobile/errorclass.go) and shows the user the class it lands in; an identity nothing matches
// falls into the class whose remedy is "report a bug", which is the wrong thing to tell
// someone whose link is bad.
//
// ONE OUTAGE HAS THREE ENDINGS and a caller must not have to know which it got. A relay that
// stops answering ends the call as this deadline (ErrTimeout); the reconnect that follows
// closes the connection underneath any call still in flight, which surfaces either as
// readFrame's ErrConnClosed or as a RAW socket error from the write ("use of closed network
// connection"), depending on how far the teardown had got. The third was the one nobody had
// seen: it is a foreign identity, so it matched no arm anywhere and was reported to the user
// as an app bug -- intermittently, since which arm wins is a race. An exchange that failed
// while its own connection was being torn down is the connection being gone, so it is named
// that, matching what readFrame has always returned for the same condition.
//
// The wrapped chain keeps context.DeadlineExceeded reachable, so callers already testing for
// it are unaffected. Cancellation is deliberately NOT rewritten: a caller that cancelled its
// own call knows why, and calling that a timeout would report the app's own shutdown as a
// network fault.
func (c *Conn) callErr(err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%w: %w", ErrTimeout, err)
	case c.ctx.Err() != nil, errors.Is(err, net.ErrClosed):
		// The connection's own context is cancelled by Close/CloseNow BEFORE the socket is
		// torn down, so it covers the whole teardown window rather than the instant after it.
		// The underlying error is not propagated, for the reason ErrConnClosed states: every
		// caller's response to it is the same.
		return ErrConnClosed
	}
	return err
}

// control issues a generic MsgRelay control op with a JSON body.
func (c *Conn) control(ctx context.Context, op string, req map[string]any) (json.RawMessage, error) {
	if req == nil {
		req = map[string]any{}
	}
	req["op"] = op
	return c.roundtrip(ctx, MsgRelay, req)
}

func decodeError(payload []byte) error {
	var eb errorBody
	_ = json.Unmarshal(payload, &eb)
	if e, ok := codeToErr[eb.Code]; ok {
		return e
	}
	if eb.Message != "" {
		return fmt.Errorf("relay: %s", eb.Message)
	}
	if eb.Code != "" {
		return fmt.Errorf("relay: %s", eb.Code)
	}
	return errors.New("relay: server error")
}

// Hello negotiates the protocol version and the intersected capability set. An
// unsupported version is refused (returns a non-nil error), not downgraded.
func (c *Conn) Hello(ctx context.Context, version int, caps []string) (int, []string, error) {
	resp, err := c.control(ctx, "hello", map[string]any{"version": version, "caps": caps})
	if err != nil {
		return 0, nil, err
	}
	var r struct {
		Version int      `json:"version"`
		Caps    []string `json:"caps"`
	}
	if err := json.Unmarshal(resp, &r); err != nil {
		return 0, nil, err
	}
	return r.Version, r.Caps, nil
}

// RendezvousCreate opens a two-party pairing rendezvous keyed by id.
func (c *Conn) RendezvousCreate(ctx context.Context, id string) error {
	_, err := c.control(ctx, "rendezvous_create", map[string]any{"id": id})
	return err
}

// RendezvousClaim joins an existing rendezvous as its single second participant.
func (c *Conn) RendezvousClaim(ctx context.Context, id string) error {
	_, err := c.control(ctx, "rendezvous_claim", map[string]any{"id": id})
	return err
}

// RendezvousSend forwards opaque bytes to the other participant.
func (c *Conn) RendezvousSend(ctx context.Context, id string, msg []byte) error {
	_, err := c.control(ctx, "rendezvous_send", map[string]any{"id": id, "data": msg})
	return err
}

// RendezvousRecv blocks for the next opaque message from the other participant.
func (c *Conn) RendezvousRecv(ctx context.Context) ([]byte, error) {
	resp, err := c.control(ctx, "rendezvous_recv", nil)
	if err != nil {
		return nil, err
	}
	var r struct {
		Data []byte `json:"data"`
	}
	if err := json.Unmarshal(resp, &r); err != nil {
		return nil, err
	}
	return r.Data, nil
}

// RendezvousComplete burns the rendezvous id (single use).
func (c *Conn) RendezvousComplete(ctx context.Context, id string) error {
	_, err := c.control(ctx, "rendezvous_complete", map[string]any{"id": id})
	return err
}

// Client is an authenticated relay connection bound to RoutingID(relay-auth pub).
type Client struct {
	conn *Conn
	rid  string
}

// Dial opens a connection and completes the Ed25519 signed-challenge handshake,
// binding the connection to RoutingID(auth.RelayAuthPub). A revoked key, a rate
// refusal, or a bad signature returns a non-nil error and no Client.
//
// It applies no transport-security policy: the URL is dialed as given, so the
// auth_init frame -- which carries auth.RelayAuthPub in full -- is readable by any
// observer of a ws:// hop (ADR-007 B37). It remains exported for the test rigs that
// stand up an in-process relay and dial it deliberately; NO production caller may
// reach it, which internal/remote/transport's productiondial_test.go enforces at the
// call site. Production dials go through DialSecure, machine-side ones under
// MachineSecurity.
func Dial(ctx context.Context, url string, auth ClientAuth) (*Client, error) {
	conn, err := dialConn(ctx, url, nil, true)
	if err != nil {
		return nil, err
	}
	return authenticate(ctx, conn, auth)
}

// DialSecure is Dial under an explicit transport-security policy (PB-NET-2):
// TLS verified against the platform's stated trust roots by default, a pinned
// certificate as a per-connection opt-in for a self-hosted relay, and cleartext
// refused. The policy is decided before any packet is sent, so a refusal returns
// ErrCleartextRefused/ErrPinMismatch without a connection attempt and never
// yields a Client. It is re-decided on every redirect hop, so a relay cannot
// answer the upgrade with a 302 into cleartext.
func DialSecure(ctx context.Context, url string, auth ClientAuth, sec Security) (*Client, error) {
	cfg, err := sec.resolve(url)
	if err != nil {
		return nil, err
	}
	conn, err := dialConn(ctx, url, sec.httpClient(cfg), true)
	if err != nil {
		return nil, err
	}
	return authenticate(ctx, conn, auth)
}

// authenticate runs the signed-challenge handshake over an open connection.
func authenticate(ctx context.Context, conn *Conn, auth ClientAuth) (*Client, error) {
	rid := RoutingID(auth.RelayAuthPub)

	resp, err := conn.control(ctx, "auth_init", map[string]any{
		"relay_auth_pub": []byte(auth.RelayAuthPub),
		"peer":           auth.Peer,
	})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	var chal struct {
		Nonce []byte `json:"nonce"`
	}
	if err := json.Unmarshal(resp, &chal); err != nil {
		_ = conn.Close()
		return nil, err
	}

	sig, err := auth.Sign(AuthChallengeMessage(chal.Nonce, rid))
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	resp2, err := conn.control(ctx, "auth_resp", map[string]any{"signature": sig})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	var ok struct {
		RoutingID string `json:"routing_id"`
	}
	if err := json.Unmarshal(resp2, &ok); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &Client{conn: conn, rid: ok.RoutingID}, nil
}

// RoutingID returns the connection's bound routing id.
func (c *Client) RoutingID() string { return c.rid }

// Done is closed when the underlying connection dies, so a caller can notice a
// drop without issuing a request.
func (c *Client) Done() <-chan struct{} { return c.conn.Done() }

// Close severs the connection with the polite websocket close handshake. It is
// idempotent. Use it where an orderly goodbye is the point -- a finished exchange, a
// process exiting; a caller that is ABANDONING the connection uses CloseNow, because
// against a peer that has stopped answering the handshake waits its full five seconds
// with nothing to show for it (measured: the cost lands exactly when the pump has
// exited and the peer is silent -- the state a dead link leaves behind).
func (c *Client) Close() error { return c.conn.Close() }

// CloseNow severs the connection WITHOUT the close handshake -- the teardown for a
// caller that is abandoning the connection rather than finishing it (see Conn.CloseNow).
// It shares Close's once, so whichever teardown runs first decides.
func (c *Client) CloseNow() error { return c.conn.CloseNow() }

// Hello negotiates the protocol version and capability set on an authenticated
// connection -- the same r_hello a raw Conn speaks, surfaced here so a client can learn
// PER CONNECTION which optional ops this relay serves (the "wait" capability, committee
// finding M1) instead of probing an op blind and reading the verdict out of a timeout.
func (c *Client) Hello(ctx context.Context, version int, caps []string) (int, []string, error) {
	return c.conn.Hello(ctx, version, caps)
}

// AuthorizeDevice pairs this caller with the named device, authorizing
// mailbox/push routing between the two — in BOTH directions, which is why it
// takes the device's consent.
//
// consentSig is the device's own signature over ConsentMessage(c.RoutingID()),
// produced by the device's relay-auth key during the SAS-authenticated pairing
// ceremony and carried here by the caller (ADR-007 B27, made mandatory by B38).
// Without it the relay has no way to tell this call from a stranger naming a
// routing id it merely read off an unprotected auth_init or a photographed QR,
// because at the relay those two are the same shape. An absent, malformed, or
// non-matching signature is refused with ErrNotAuthorized.
func (c *Client) AuthorizeDevice(ctx context.Context, devicePub ed25519.PublicKey, consentSig []byte) error {
	_, err := c.conn.control(ctx, "authorize_device", map[string]any{
		"device_pub":  []byte(devicePub),
		"consent_sig": consentSig,
	})
	return err
}

// MailboxAppend stores an opaque envelope in target's mailbox and returns the
// relay's assigned storage cursor.
func (c *Client) MailboxAppend(ctx context.Context, target string, env []byte) (uint64, error) {
	resp, err := c.conn.roundtrip(ctx, MsgMailboxAppend, map[string]any{"target": target, "envelope": env})
	if err != nil {
		return 0, err
	}
	var r struct {
		Cursor uint64 `json:"cursor"`
	}
	if err := json.Unmarshal(resp, &r); err != nil {
		return 0, err
	}
	return r.Cursor, nil
}

// MailboxRead returns items whose storage cursor is strictly greater than cursor.
// The reply is a bounded first page (CR-4): on a large backlog it returns a
// subset that fits one frame rather than tearing the connection. Callers that
// need to drain a backlog and observe whether more remains use MailboxReadPage.
func (c *Client) MailboxRead(ctx context.Context, cursor uint64) ([]Item, error) {
	items, _, err := c.MailboxReadPage(ctx, cursor, 0)
	return items, err
}

// MailboxReadPage returns at most a bounded page of items whose storage cursor is
// strictly greater than cursor, plus has_more indicating whether further items
// remain past the page (CR-4). limit caps the page's item count; limit <= 0 asks
// for the server's own default page bound. A page always fits under MaxFrame, so
// draining an arbitrarily large backlog is a loop of MailboxReadPage + MailboxAck
// that never overflows a frame.
func (c *Client) MailboxReadPage(ctx context.Context, cursor uint64, limit int) ([]Item, bool, error) {
	resp, err := c.conn.control(ctx, "mailbox_read", map[string]any{"cursor": cursor, "limit": limit})
	if err != nil {
		return nil, false, err
	}
	var r struct {
		Items   []Item `json:"items"`
		HasMore bool   `json:"has_more"`
	}
	if err := json.Unmarshal(resp, &r); err != nil {
		return nil, false, err
	}
	if err := checkPageOrder(cursor, r.Items); err != nil {
		return nil, false, err
	}
	return r.Items, r.HasMore, nil
}

// errMailboxPageOrder is a page that breaks the relay's OWN read contract.
var errMailboxPageOrder = errors.New("relay: mailbox page cursors do not advance")

// checkPageOrder is the only thing a consumer can check about a value the RELAY MINTS.
//
// Item.Cursor is the relay's own coordinate -- its doc says so, and says "untrusted
// ordering" -- and both consumers adopt the highest one in a page as a DURABLE resume point.
// Nothing authenticates it, so its VALUE cannot be verified. What CAN be checked is that a
// page obeys the contract the relay states for its own store (store.readItemsPage): items
// whose storage cursor is STRICTLY greater than the requested cursor, in ASCENDING order. A
// page that repeats one value across its items, or hands back an item the caller already
// asked to skip past, is not a storage cursor -- it is a rewrite, and adopting it ends
// delivery permanently at whichever end read it.
//
// THIS IS HALF A FENCE AND THE OTHER HALF IS MISSING. A page of ONE item satisfies every
// clause here whatever cursor it carries, so a relay that rewrites a single delivered item's
// cursor is not caught. Catching that needs a bound on how far a cursor may advance per page,
// and every candidate bound is a number no requirement states: cursors are minted densely
// (store.appendItem) so a per-page delta bound would be exact, except that RetentionCap
// purges open legitimate holes whose size is governed by relay-side config the protocol does
// not carry. It is recorded as a residual rather than invented here.
//
// The page is REFUSED whole rather than truncated. A relay that serves such a page is either
// broken or hostile, and in both cases the honest outcome is a loud error the caller retries,
// not a partial page whose remainder is silently trusted.
func checkPageOrder(after uint64, items []Item) error {
	prev := after
	for i, it := range items {
		if it.Cursor <= prev {
			return fmt.Errorf("%w: item %d carries cursor %d, not past %d", errMailboxPageOrder, i, it.Cursor, prev)
		}
		prev = it.Cursor
	}
	return nil
}

// MailboxWait blocks SERVER-side until at least one item past cursor exists in
// this client's own mailbox, and returns that bounded page — the same
// {items, has_more} shape as MailboxReadPage. At Config.MaxServerWait it returns
// an empty page and a nil error, so a caller's loop is a wait, not a poll.
//
// It returns the ITEMS, not a bare signal: a signal a caller then had to read
// would cost two metered ops per batch, which §6.0's inbound drain budget cannot
// absorb. It meters exactly ONCE per call however many items come back, so
// batching under load actually buys something.
//
// §6.0 caps pending waits per client at one. A second concurrent wait is refused
// with ErrWaitInProgress, never queued — a queue would make cancellation
// ambiguous and let one connection pin unbounded server-side wait state.
func (c *Client) MailboxWait(ctx context.Context, cursor uint64) ([]Item, bool, error) {
	return c.conn.mailboxWait(ctx, cursor)
}

func (c *Conn) mailboxWait(ctx context.Context, cursor uint64) ([]Item, bool, error) {
	c.waitMu.Lock()
	if c.waitCh != nil {
		c.waitMu.Unlock()
		return nil, false, ErrWaitInProgress
	}
	c.waitSeq++
	id := c.waitSeq
	ch := make(chan waitReplyBody, 1)
	c.waitID, c.waitCh = id, ch
	c.waitMu.Unlock()

	defer func() {
		c.waitMu.Lock()
		if c.waitID == id {
			c.waitCh = nil
		}
		c.waitMu.Unlock()
	}()

	body, err := json.Marshal(map[string]any{"op": "mailbox_wait", "cursor": cursor, "wait_id": id})
	if err != nil {
		return nil, false, err
	}
	if err := c.writeFrame(ctx, MsgRelay, body); err != nil {
		return nil, false, err
	}
	select {
	case r := <-ch:
		if r.Code != "" {
			return nil, false, errForCode(r.Code)
		}
		// The wait carries the same page shape as a read, so it carries the same contract.
		if err := checkPageOrder(cursor, r.Items); err != nil {
			return nil, false, err
		}
		return r.Items, r.HasMore, nil
	case <-ctx.Done():
		// Release the SERVER's slot too. An orphaned wait would hold the single
		// pending-wait slot until its ceiling elapsed, so the next wait — the one a
		// reconnecting live tail parks — would be refused and typing would be dead
		// for the remainder of the ceiling.
		c.cancelWait(id)
		return nil, false, fmt.Errorf("relay: mailbox wait cancelled: %w", ctx.Err())
	case <-c.done:
		return nil, false, ErrConnClosed
	}
}

// cancelWait withdraws a parked wait. It is fire-and-forget and carries the wait
// id: the withdrawal and any later wait travel the same stream in order, so the
// server frees the slot before it sees the replacement, and the correlation id
// makes the abandoned wait's reply discardable rather than mis-delivered.
func (c *Conn) cancelWait(id uint64) {
	body, err := json.Marshal(map[string]any{"op": "mailbox_wait_cancel", "wait_id": id})
	if err != nil {
		return
	}
	_ = c.writeFrame(c.ctx, MsgRelay, body) // the caller's ctx is already done
}

// deliverWait routes one MsgWaitReply to the parked waiter it names. A reply for
// any other id is dropped: it belongs to a wait this client already withdrew.
func (c *Conn) deliverWait(payload []byte) {
	var r waitReplyBody
	if err := json.Unmarshal(payload, &r); err != nil {
		return
	}
	c.waitMu.Lock()
	defer c.waitMu.Unlock()
	if c.waitCh == nil || c.waitID != r.WaitID {
		return
	}
	// This send cannot block, which matters because it happens on the read pump
	// under waitMu: the channel is created per wait with capacity 1, and it is
	// cleared here under the same lock, so even a relay that replied twice to one
	// wait id finds waitCh nil on the second frame and is dropped above.
	c.waitCh <- r
	c.waitCh = nil
}

// MailboxAck compacts away every item at or below cursor.
func (c *Client) MailboxAck(ctx context.Context, cursor uint64) error {
	_, err := c.conn.control(ctx, "mailbox_ack", map[string]any{"cursor": cursor})
	return err
}

// TokenRegister registers (or refreshes) this device's APNs push token.
func (c *Client) TokenRegister(ctx context.Context, token string) error {
	_, err := c.conn.control(ctx, "token_register", map[string]any{"token": token})
	return err
}

// TokenDelete stops push delivery to this device.
func (c *Client) TokenDelete(ctx context.Context) error {
	_, err := c.conn.control(ctx, "token_delete", nil)
	return err
}

// Presence returns target's coarse reachability.
func (c *Client) Presence(ctx context.Context, target string) (PresenceInfo, error) {
	resp, err := c.conn.control(ctx, "presence", map[string]any{"target": target})
	if err != nil {
		return PresenceInfo{}, err
	}
	var p PresenceInfo
	if err := json.Unmarshal(resp, &p); err != nil {
		return PresenceInfo{}, err
	}
	return p, nil
}

// PushTrigger forwards an opaque wake envelope to target's registered push token.
func (c *Client) PushTrigger(ctx context.Context, target string, env []byte) error {
	_, err := c.conn.control(ctx, "push_trigger", map[string]any{"target": target, "envelope": env})
	return err
}

// DeviceRevoke de-authorizes target's relay-auth registration and purges its
// relay-side mailbox.
func (c *Client) DeviceRevoke(ctx context.Context, target string) error {
	_, err := c.conn.control(ctx, "device_revoke", map[string]any{"target": target})
	return err
}
