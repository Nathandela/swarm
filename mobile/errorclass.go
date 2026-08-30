package swarmmobile

// PB-APP-9's error taxonomy: every error this facade returns names the state the user is
// shown, and the name survives the JNI boundary.
//
// WHY THE CLASS RIDES THE MESSAGE. gomobile turns a Go error into a Java exception carrying
// its MESSAGE and nothing else -- no type, no wrapped chain, no code. So a class that is only
// a Go identity is a class the Android side cannot read. keycustody.go established the shape
// for PB-KEY-6's two custody verdicts (a stable token, stamped centrally in barrier); this
// generalises it to the whole surface and adds the verb that reads it back out, App.ErrorClass.
//
// THE ALTERNATIVES WERE CONSIDERED AND ARE BOTH WORSE. A classifier that matched on the
// message text would be prose matching: every reword of an error becomes a silent misroute,
// and this file's whole point is that a misrouted custody verdict is a prompt the user can
// never satisfy. A classifier that DEFAULTED at the construction site would put every future
// error in one bucket, which is the unmapped class PB-APP-9 forbids wearing a default's
// clothes. So the class is named AT THE SITE, and mobile/s16_taxonomy_test.go fences that
// syntactically over every construction site in this package -- this file excepted, because
// this is where the constructors live.
//
// THE CLOSED SET IS THE GOLDEN. A class exists only as an exported const, so it appears in
// mobile/testdata/exported_surface.golden, which moves only as a reviewed change (PB-BIND-7);
// mobile/error_taxonomy.tsv is the JOIN between that set and the Kotlin ErrorState enum, and
// both directions are set equality (here and in android/gate/s16_ui_test.go). A class with no
// row fails; a row for no class fails; a row naming a state no screen declares fails; a state
// no class produces fails.

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// The error classes. Each VALUE is the token that crosses JNI and that the Android side
// branches on; each NAME is what the construction site must lexically mention.
//
// ONE OF THE VALUES IS DELIBERATELY A STRING PB-KEY-6 ALREADY SHIPPED
// (KeyCustodyKeyInvalidated). It is not renamed and not aliased: the Kotlin side has matched on
// that exact token since S14 (dev.swarm.phone.keys.GoCustodyFailure), and a third spelling would
// be a third thing to keep in step. It is re-declared as a literal rather than as
// `= KeyCustodyKeyInvalidated` because the taxonomy fence reads each class's value from SOURCE
// as a plain string literal -- a const that referred to another const would be unreadable to it,
// and the point of reading from source is that a drifted table cannot lie about what crosses.
// mobile/s14_custody_test.go pins the spellings to the Kotlin side so the duplication cannot
// become a divergence.
//
// THERE WERE TWO UNTIL ADR-007 B133. The second was ErrClassReauthRequired, carrying
// KeyCustodyAuthRequired's string, and it is gone -- see ErrClassRepairRequired, which is where
// its sentinel lands now. KeyCustodyAuthRequired itself stays: it is the token Kotlin STAMPS
// (dev.swarm.phone.keys.GoCustodyFailure.AUTH_REQUIRED_TOKEN), so it still crosses inbound and
// keycustody.go still reads it. What no longer exists is a class of its own for it.
const (
	// ErrClassUnknown is RESERVED: App.ErrorClass answers it for a message this facade did
	// not produce, and for nothing else. It is what makes the exhaustiveness sweep a real
	// assertion rather than a tautology -- a classifier that could not say "I do not know
	// this" would satisfy every other check by answering one class for every input.
	ErrClassUnknown = "swarm/unknown"

	// ErrClassInternal is a bug in the app: a panic caught at the JNI boundary, or a failure
	// with no identity this facade recognises. It is never a user's fault and never has a
	// user action.
	ErrClassInternal = "swarm/internal"

	// ErrClassInvalidRequest is a screen passing something the facade cannot use -- an empty
	// session id, a nil spec, a stream name that is not a repair channel. Also a bug, but a
	// distinguishable one: the app is running and healthy and one call was wrong.
	ErrClassInvalidRequest = "swarm/invalid-request"

	// ErrClassNotFound is a lookup that legitimately misses: a session no longer in the
	// roster, a snapshot never received, an index past the end of a handle.
	ErrClassNotFound = "swarm/not-found"

	// ErrClassClosed is a call on an App the Android lifecycle has already closed. It is its
	// own class because it is the one failure a screen fixes by doing nothing.
	ErrClassClosed = "swarm/closed"

	// ErrClassOffline is the transport: not started, no connection yet, or the relay refused
	// or dropped the call. Recoverable by waiting, which is the one case where a spinner is
	// the honest thing to show.
	ErrClassOffline = "swarm/offline"

	// ErrClassNotPaired is a phone that holds no machine destination. The remedy is pairing,
	// and it is NOT the same as ErrClassRepairRequired: nothing is broken, this handset has
	// simply never been paired (or its pairing left no destination behind).
	ErrClassNotPaired = "swarm/not-paired"

	// ErrClassStateCorrupt is phonecore.ErrCorruptState: the durable blob will not load, so
	// PB-STATE-4 fails closed and Resume refuses. It is the OWNER-RECOVERABLE fail-closed
	// state (PB-STATE-10) and it had no class of its own until S18b, which put it in
	// ErrClassInternal -- whose remedy is report_bug and whose own definition is "never the
	// user's fault and never has a user action". A recoverable state routed to "report a bug"
	// is the brick expressed as a screen: the one thing the user is told to do is the one
	// thing that cannot help.
	ErrClassStateCorrupt = "swarm/state-corrupt"

	// ErrClassDeviceUnsupported is PB-KEY-8's two refusals: the handset does not provide a
	// capability a key role needs, or it generated a key WEAKER than was requested. Nothing
	// the user does fixes either, and re-pairing least of all -- a re-pair re-provisions the
	// same key on the same platform and lands on the same screen, which is the failure LOOP
	// PB-APP-10 forbids, reached through the remedy. It shares ErrClassInternal's remedy
	// (report_bug: the maintainers must hear about a platform that downgrades silently) and
	// not its state, because the app is not at fault and its message must not say so.
	//
	// It is produced on the ANDROID side, where those refusals are raised
	// (dev.swarm.phone.keys.KeyCustodyException, routed by PhoneRuntime): the Go facade has
	// no sentinel for a Keystore read-back. The constant lives here because the taxonomy is a
	// closed set derived from this package's exported surface -- a token Kotlin routes on and
	// Go does not declare is a token nothing checks.
	ErrClassDeviceUnsupported = "swarm/device-unsupported"

	// ErrClassUnreconciled is PB-SYNC-7's fail-closed refusal of mutating ops until the
	// machine publishes its rollback authorities. The phone is fine, the machine has not
	// spoken yet, and reads keep working throughout.
	ErrClassUnreconciled = "swarm/unreconciled"

	// ErrClassAwaitingKey is a phone with no epoch content key that has NOT proved the grant
	// is lost. It is the ordinary first-launch window, so it must not be rendered as the
	// terminal ErrClassGrantLost -- that would send a healthy user to the machine.
	ErrClassAwaitingKey = "swarm/awaiting-key"

	// ErrClassGrantLost is PB-KEY-3's terminal state: custody is fine and the MACHINE's grant
	// is gone. It is the one remedy the user cannot perform, which is why it may never
	// collapse into ErrClassRepairRequired -- BeginPairing fail-fasts while this device is
	// registered (PB-STATE-10), so "pair again" is advice that cannot be carried out.
	ErrClassGrantLost = "swarm/grant-lost"

	// ErrClassRepairRequired is PERMANENT: the Keystore key cannot be used and nothing done on
	// this handset brings it back. Same token as KeyCustodyKeyInvalidated.
	//
	// IT CLASSIFIES BOTH CRYPTO SENTINELS AFTER ADR-007 B133. crypto.ErrKeyInvalidated is the
	// destroyed key it has always meant. crypto.ErrKeyAuthRequired had a class of its own,
	// ErrClassReauthRequired, whose remedy was "prompt for the biometric" -- and B133 removes
	// every phone-side user authentication, so that remedy names an act the product can no
	// longer offer. The refusal still HAPPENS: an install provisioned BEFORE B133 keeps its
	// AUTH_BIOMETRIC_STRONG content KEK, because KeystoreCustodyBootstrap.ensure returns early
	// when the alias exists and does not re-request the spec on upgrade. For that handset the
	// key really is unusable and pairing again really is the fix -- a re-pair discards the alias
	// and the next provision writes one that asks for no authenticator.
	//
	// This is the arm dev.swarm.phone.PhoneRuntime.routeCustodyVerdict already puts
	// KeyCustodyException.UserAuthenticationRequired in, and the one mobile/relay.go's dial
	// switch already collapsed the two sentinels onto (connRepairRequired). This file was the
	// last of the three still splitting them, which is how a class the taxonomy has no row for
	// survived: it reaches the screen as an opaque exception, which is what PB-APP-9 stops.
	ErrClassRepairRequired = "swarm-custody/key-invalidated"

	// ErrClassRevoked is relay.ErrRevoked: the OWNER removed this device. The remedy
	// coincides with ErrClassRepairRequired's -- pair again -- and the CAUSE does not, which
	// is why it is a separate class: the machine still holds a registration the owner has to
	// clear before a re-pair can succeed, and the phone must say so rather than sending the
	// user round a loop.
	ErrClassRevoked = "swarm/revoked"

	// ErrClassNoLease is PB-INPUT-2: the machine has not confirmed a control lease, so every
	// keystroke is refused. The screen's remedy is to offer take-control, never to retry.
	ErrClassNoLease = "swarm/no-lease"

	// ErrClassStaleTurn is the daemon's stale_turn refusal (Wave R6, IS-LIFE-5). Stop
	// remains strictly bound to the rendered turn; an older queued composer send may also
	// be invalidated by a successful Stop barrier. Ordinary concurrency, not a bug report:
	// refresh, with any unsent draft retained.
	ErrClassStaleTurn = "swarm/stale-turn"

	// ErrClassInputBusy is the shim's input_busy refusal of a composer send (Slice 0,
	// agents-tracker-bzfe): the PTY's only serialized writer found bytes written since the
	// last submit, so it refused HAVING WRITTEN NOTHING rather than joining this message to
	// somebody's half-typed line. Its own class for the reason ErrClassStaleTurn is: the
	// remedy is specific and mild and belongs to nobody's mistake -- the terminal's line
	// simply was not clean, and it will be the moment whoever is typing presses enter. It
	// errs safe by construction: a draft typed and deleted back to empty still refuses,
	// because the count measures bytes written and not what survives on the line.
	ErrClassInputBusy = "swarm/input-busy"

	// ErrClassRateLimited is a bound this phone enforces on itself (the §6.0 resync budget).
	// Retryable, but only after waiting -- "asking harder" is what the bound exists to stop.
	ErrClassRateLimited = "swarm/rate-limited"

	// ErrClassPairingFailed is a pairing attempt that ended without pinning anything. The
	// terminal state is on the Pairing handle (PB-PAIR-5); this is the class for the errors
	// the pairing verbs themselves return.
	ErrClassPairingFailed = "swarm/pairing-failed"

	// THE THREE BELOW SPLIT OFF ErrClassPairingFailed (agents-tracker-ksvb.5), and the split
	// is the point: that class means "the pairing CALL failed", and its routed sentence sends
	// the user back to their machine for a new code. None of the three ever reached a call.
	// They are the three ways the ENTRY can be wrong, they are refused before anything is
	// dialled, and each has a different next act -- retype ten characters, give the phone an
	// address, or fix the shape of the one that was typed. Collapsed onto one row the user is
	// told to restart a ceremony that never started, which is the specific advice that cannot
	// work for any of them.

	// ErrClassPairingCodeInvalid is pairing.ErrShortCodeMalformed: what was typed is not ten
	// Crockford characters. The code is still on the machine's screen, so the act is to read
	// it again -- nothing about the pairing needs restarting.
	ErrClassPairingCodeInvalid = "swarm/pairing-code"

	// ErrClassRelayUnknown is a typed code with no relay to complete it: this handset has
	// never scanned a QR and none was pasted beside the code. It is the phone MISSING a fact,
	// not a failure of anything -- and the two ways to supply it are the scan and the paste.
	ErrClassRelayUnknown = "swarm/relay-unknown"

	// ErrClassRelayAddressInvalid is a relay address in the wrong SHAPE (relayAddress). It is
	// separate from ErrClassRelayUnknown because the remedies differ by exactly the fact the
	// user has: one person has no address and the other has typed one wrong, and telling the
	// second to scan a QR ignores what they can see on their own screen.
	ErrClassRelayAddressInvalid = "swarm/relay-address"
)

// errClasses is the closed set, longest token first so a scan that finds two overlapping
// tokens at the same index reports the more specific one. It is the only list of classes in
// this package; the taxonomy TSV is checked against the GOLDEN, never against this slice.
var errClasses = []string{
	ErrClassDeviceUnsupported,
	ErrClassRepairRequired,
	ErrClassInvalidRequest,
	ErrClassUnreconciled,
	ErrClassPairingFailed,
	ErrClassStateCorrupt,
	ErrClassRelayAddressInvalid,
	ErrClassRelayUnknown,
	ErrClassPairingCodeInvalid,
	ErrClassAwaitingKey,
	ErrClassRateLimited,
	ErrClassNotPaired,
	ErrClassGrantLost,
	ErrClassNotFound,
	ErrClassInternal,
	ErrClassStaleTurn,
	ErrClassInputBusy,
	ErrClassNoLease,
	ErrClassOffline,
	ErrClassRevoked,
	ErrClassUnknown,
	ErrClassClosed,
}

// classedError is one error carrying its rendered state.
//
// The class is a PREFIX of the message rather than a field on a type, and that is the whole
// design: the type does not survive gomobile, so anything not in the message is not there at
// all by the time Kotlin sees it. Unwrap is kept so errors.Is still reaches the sentinel
// underneath -- the Go side of this repository routes on identity and must go on doing so.
type classedError struct {
	class string
	err   error
}

func (e classedError) Error() string { return e.class + ": " + e.err.Error() }

func (e classedError) Unwrap() error { return e.err }

// Class is the token this error carries.
func (e classedError) Class() string { return e.class }

// classed stamps err with the rendered state it reaches the user as.
//
// It is idempotent per class and cheap to nest: a message that already begins with this class
// is returned unchanged, and a message carrying a DIFFERENT class inside it keeps both, with
// the outermost winning at classification time (classifyMessage takes the earliest token).
// That is the right precedence -- the outermost site is the one that knows what the caller
// was trying to do.
func classed(class string, err error) error {
	if err == nil {
		return nil
	}
	if strings.HasPrefix(err.Error(), class+": ") {
		return err
	}
	return classedError{class: class, err: err}
}

// classifyMessage reads a class back out of a flattened error message.
//
// EARLIEST WINS, then longest. The earliest token is the outermost stamp, which is the class
// the site closest to the caller chose; a fixed order over the token set would instead make
// the answer depend on which class happens to sort first, so two wrappings of the same error
// would classify differently depending on an alphabet.
func classifyMessage(msg string) string {
	best, bestAt := ErrClassUnknown, -1
	for _, class := range errClasses {
		at := strings.Index(msg, class+": ")
		if at < 0 {
			continue
		}
		if bestAt < 0 || at < bestAt || (at == bestAt && len(class) > len(best)) {
			best, bestAt = class, at
		}
	}
	return best
}

// stateCorruptRecovery is PB-STATE-10's owner-side recovery, spelled as the commands that
// perform it.
//
// IT RIDES THE ERROR because for this one class there is nowhere else to put it. A corrupt
// blob fails Resume, so NewApp returns and there is no App: no screen, no App.ErrorClass, no
// remedy string -- the message is the entire product for a user in this state, and on Android
// it is what PhoneStartup.Unavailable carries into the log and the bug report.
//
// IT NAMES THE MACHINE-SIDE STEPS because the user's own act is not sufficient. Clearing the
// app's data removes the blob that will not load; `swarm remote pair` is then still REFUSED,
// because BeginPairing fail-fasts while this device is registered (single-device v1). A
// remedy that stopped at "pair again" is advice that cannot be carried out, which is the
// brick this requirement is named for.
const stateCorruptRecovery = "recovery: clear this app's data, then on the machine run " +
	"`swarm remote devices` to find this device, `swarm remote revoke <device-id>` to " +
	"unregister it, and `swarm remote pair` to pair again"

// stampErrorClass is the OUTBOUND totality guarantee: no error leaves this facade without a
// class, whether or not this package constructed it.
//
// It runs in barrier, which every exported entry point installs as its first statement
// (PB-BIND-5), so it is total by construction -- a verb cannot forget to classify and a verb
// added later inherits it. The syntax fence covers what this package AUTHORS; this covers what
// merely travels through, which is the half source can never see: crypto.ErrKeyInvalidated,
// relay.ErrRevoked and phonecore.ErrGrantLost are produced three packages away.
//
// THE RESIDUAL DEFAULT IS ErrClassInternal AND IT IS NOT A CLASSIFICATION SHORTCUT. It applies
// only to an error identity from OUTSIDE this package that none of the arms below recognise --
// which is, correctly, a bug: some dependency grew a failure mode nobody routed. Defaulting at
// a construction site is the thing PB-APP-9 forbids and the syntax fence prevents; defaulting
// at the boundary for a foreign identity is the only alternative to returning something
// unclassified, which is the failure the requirement exists to remove.
func stampErrorClass(err error) error {
	if err == nil {
		return nil
	}
	if classifyMessage(err.Error()) != ErrClassUnknown {
		return err
	}
	switch {
	case errors.Is(err, crypto.ErrKeyInvalidated), errors.Is(err, crypto.ErrKeyAuthRequired),
		errors.Is(err, phonecore.ErrContentTierLocked):
		// THE THREE IDENTITIES OF ONE UNUSABLE KEY (ADR-007 B133). ErrKeyAuthRequired kept a
		// class of its own while there was a prompt to offer; there is none left anywhere in
		// the product, so the remedy that remains is the permanent one -- see
		// ErrClassRepairRequired for which population still raises it and why re-pairing is
		// genuinely the fix for them.
		//
		// ErrContentTierLocked is not a custody sentinel: it is the Save that refuses because
		// THIS process could not open the content tier. It shared a class with
		// ErrKeyAuthRequired before B133 and still does, because the reason a content KEK does
		// not open on a handset with no authentication left IS one of the two above.
		return classed(ErrClassRepairRequired, err)
	case errors.Is(err, phonecore.ErrCorruptState):
		return classed(ErrClassStateCorrupt, fmt.Errorf("%w. %s", err, stateCorruptRecovery))
	case errors.Is(err, relay.ErrRevoked), errors.Is(err, relay.ErrConsentRetired):
		// ErrConsentRetired IS A REVOCATION IN THE RELAY'S OWN WORDS (agents-tracker-ksvb.5).
		// Its sentence ends "pair the device again", which is this class's remedy exactly:
		// the route consent behind a live pairing has been superseded or withdrawn, so the
		// registration on the machine is what has to move before anything the phone does can
		// succeed. It fell to the default arm before, so a consent the owner retired reached
		// the user as "The app hit an internal fault ... please report it" -- report_bug over
		// a state the owner themselves created and can undo.
		return classed(ErrClassRevoked, err)
	case errors.Is(err, relay.ErrRendezvousExpired), errors.Is(err, relay.ErrRendezvousBurned):
		// THE TWO ORDINARY ENDINGS OF A PAIRING MAILBOX, and the reason this arm exists.
		// Neither is a fault: a rendezvous outlives its TTL whenever the person typing was
		// slow, and a single-use one is burned the second time the button is pressed. Both
		// defaulted to ErrClassInternal, so an EXPIRED PAIRING CODE -- the most common thing
		// that goes wrong on the most-used screen in the product -- read "The app hit an
		// internal fault. Nothing you did caused it; please report it."
		//
		// They are ErrClassPairingFailed and not one of the three entry classes below: those
		// three refuse before anything is dialled, and these two are what the RELAY answered
		// a claim with. The attempt really did fail and really is worth restarting from a
		// fresh code, which is the pairing-failed row's advice.
		return classed(ErrClassPairingFailed, err)
	case errors.Is(err, phonecore.ErrGrantLost):
		return classed(ErrClassGrantLost, err)
	case errors.Is(err, phonecore.ErrUnreconciled):
		return classed(ErrClassUnreconciled, err)
	case errors.Is(err, phonecore.ErrNoLease), errors.Is(err, phonecore.ErrLeaseExpired),
		errors.Is(err, phonecore.ErrGapPending):
		// ErrGapPending belongs here rather than with the transport errors: PB-STATE-8's
		// burned reservation block is absorbed by a COMMAND frame, so the remedy a screen
		// offers is take-control -- the same one a missing lease gets.
		return classed(ErrClassNoLease, err)
	case errors.Is(err, relay.ErrQuotaExceeded):
		return classed(ErrClassRateLimited, err)
	case errors.Is(err, crypto.ErrStaleSeq), errors.Is(err, relay.ErrNotAuthorized),
		errors.Is(err, relay.ErrDuplicateConnection),
		errors.Is(err, relay.ErrTimeout), errors.Is(err, relay.ErrConnClosed),
		errors.Is(err, relay.ErrPeerCapabilityUnavailable):
		// THE TWO ENDINGS OF ONE OUTAGE, and they must not reach different screens. A relay
		// that answers nothing fails the call with relay.ErrTimeout (relay.DefaultCallTimeout);
		// the same outage noticed a moment later, once the socket is torn down, fails it with
		// relay.ErrConnClosed. Which one a given call gets is a race between its own deadline
		// and the reconnect loop dropping the client. Neither is the app's fault and neither
		// has a user action beyond waiting, so both are the transport class -- ErrClassInternal
		// would tell a user with a bad link to file a bug report, and would do it
		// intermittently.
		return classed(ErrClassOffline, err)
	}
	return classed(ErrClassInternal, err)
}

// barrier is the panic barrier every exported entry point installs as its FIRST statement
// (PB-BIND-5). A Go panic that reaches the JNI frame kills the app process -- there is no Java
// frame to catch it -- so it is converted into the entry point's error result, which is why
// every entry point has one.
//
// It also carries PB-APP-9's outbound half (see stampErrorClass) and, within it, PB-KEY-6's:
// the two custody sentinels are stamped with the tokens the Android side has matched on since
// S14. Central is the point -- an enumeration of the verbs that classify is a list somebody
// has to keep correct as verbs are added.
func barrier(err *error) {
	if r := recover(); r != nil {
		*err = classed(ErrClassInternal,
			fmt.Errorf("swarmmobile: recovered panic at the JNI boundary: %v", r))
		return
	}
	*err = stampErrorClass(*err)
}

// ErrorClass is the classifier the Android side routes on (PB-APP-9).
//
// It takes the MESSAGE because that is all gomobile leaves of a Go error at the JNI boundary,
// and it returns the token of the class the error was stamped with -- ErrClassUnknown, and
// only that, for a string this facade did not produce.
//
// IT IS GATED ON A USABLE RECEIVER LIKE EVERY OTHER ENTRY POINT (PB-BIND-5), even though the
// answer is a pure function of the argument. The rule that a receiver this package never
// constructed must ERROR is not a per-verb judgement: a bound object whose Go peer is gone
// looks identical to a working one from Kotlin, and a verb that answered anyway would report a
// class for an app that cannot produce errors at all. The Android side is not left without a
// classifier when the app is closed -- it holds the tokens as literals for exactly that reason
// (dev.swarm.phone.ui.SwarmErrorTokens).
//
// It returns no []byte, so ADR-007 B8 is untouched: the key crossing stays single and inbound
// and this widens the surface by one string verb.
func (a *App) ErrorClass(message string) (class string, err error) {
	defer barrier(&err)
	if _, err = a.ready(); err != nil {
		return "", err
	}
	return classifyMessage(message), nil
}
