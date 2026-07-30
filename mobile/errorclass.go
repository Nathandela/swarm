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
// TWO OF THE VALUES ARE DELIBERATELY THE SAME STRINGS PB-KEY-6 ALREADY SHIPPED
// (KeyCustodyAuthRequired / KeyCustodyKeyInvalidated). They are not renamed and not aliased:
// the Kotlin side has matched on those exact tokens since S14 (dev.swarm.phone.keys
// .GoCustodyFailure), and a third spelling would be a third thing to keep in step. They are
// re-declared as literals rather than as `= KeyCustodyAuthRequired` because the taxonomy fence
// reads each class's value from SOURCE as a plain string literal -- a const that referred to
// another const would be unreadable to it, and the point of reading from source is that a
// drifted table cannot lie about what crosses. s16_taxonomy_agreement_test.go pins the two
// spellings to each other so the duplication cannot become a divergence.
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

	// ErrClassReauthRequired is crypto.ErrKeyAuthRequired: RECOVERABLE. Prompt for the
	// biometric; the operation is worth retrying afterwards. Same token as
	// KeyCustodyAuthRequired -- see the block comment above.
	ErrClassReauthRequired = "swarm-custody/auth-required"

	// ErrClassRepairRequired is crypto.ErrKeyInvalidated: PERMANENT. The Keystore key is
	// destroyed and no prompt brings it back. Same token as KeyCustodyKeyInvalidated.
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

	// ErrClassRateLimited is a bound this phone enforces on itself (the §6.0 resync budget).
	// Retryable, but only after waiting -- "asking harder" is what the bound exists to stop.
	ErrClassRateLimited = "swarm/rate-limited"

	// ErrClassPairingFailed is a pairing attempt that ended without pinning anything. The
	// terminal state is on the Pairing handle (PB-PAIR-5); this is the class for the errors
	// the pairing verbs themselves return.
	ErrClassPairingFailed = "swarm/pairing-failed"
)

// errClasses is the closed set, longest token first so a scan that finds two overlapping
// tokens at the same index reports the more specific one. It is the only list of classes in
// this package; the taxonomy TSV is checked against the GOLDEN, never against this slice.
var errClasses = []string{
	ErrClassDeviceUnsupported,
	ErrClassReauthRequired,
	ErrClassRepairRequired,
	ErrClassInvalidRequest,
	ErrClassUnreconciled,
	ErrClassPairingFailed,
	ErrClassStateCorrupt,
	ErrClassAwaitingKey,
	ErrClassRateLimited,
	ErrClassNotPaired,
	ErrClassGrantLost,
	ErrClassNotFound,
	ErrClassInternal,
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
	case errors.Is(err, crypto.ErrKeyInvalidated):
		return classed(ErrClassRepairRequired, err)
	case errors.Is(err, crypto.ErrKeyAuthRequired), errors.Is(err, phonecore.ErrContentTierLocked):
		// ErrContentTierLocked is not a custody sentinel and reaches the same screen: the
		// content KEK would not open, so the user must authenticate. Routing it anywhere else
		// tells a locked handset it is broken.
		return classed(ErrClassReauthRequired, err)
	case errors.Is(err, phonecore.ErrCorruptState):
		return classed(ErrClassStateCorrupt, fmt.Errorf("%w. %s", err, stateCorruptRecovery))
	case errors.Is(err, relay.ErrRevoked):
		return classed(ErrClassRevoked, err)
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
		errors.Is(err, relay.ErrTimeout), errors.Is(err, relay.ErrConnClosed):
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
