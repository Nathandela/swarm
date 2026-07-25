package conformance_test

// FAILING-FIRST (TDD RED, GG-5) for slice S17 / PB-PUSH-4: what the phone renders for a push,
// and what it must not go and read in order to render it.
//
// THE REQUIREMENT: "The app receives a push and renders a content-free notification unless the
// user has authenticated; it never decrypts session content with a locked device (PB-KEY-2).
// Lock-screen redaction and notification-channel privacy are set."
//
// WHERE THE REACHABLE DEFECT ACTUALLY IS. It is not the payload. The wake is a constant 78
// bytes over an EMPTY plaintext with both key ids zeroed (ADR-007 B20), so there is nothing in
// it to render even if the app wanted to, and S12 pinned that. The reachable defect is an app
// that goes and FETCHES content to fill the notification in -- one roster read, one snapshot
// peek -- which is a decrypt with a locked device by any reading of PB-KEY-2, and which no
// assertion about the payload can see.
//
// So these tests measure the CUSTODY SEAM, not the string. The content tier is biometric-gated
// and a locked read returns crypto.ErrKeyAuthRequired, and the test double counts every unwrap
// per tier. A wake handled with the content tier locked must cost ZERO content unwraps. An
// implementation that started fetching would raise that count whether it succeeded or was
// refused, which is the property "assert a generic string was rendered" cannot express.
//
// LOCK-SCREEN REDACTION AND CHANNEL PRIVACY ARE NOT HERE. They are Android platform
// configuration -- VISIBILITY_SECRET on the notification and on the channel -- and they live in
// android/app/src/test/kotlin/dev/swarm/phone/push/ (Robolectric, which models POLICY) with a
// source-level gate in android/gate. Asserting them in Go would be asserting a Go constant.
//
// PB-E2E-5 REMAINS DEFERRED. No handset, no real FCM, no Doze, no biometric prompt. The
// custody double models a refusing auth-gated key and models no hardware property whatsoever.

import (
	"sort"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remotegw"
	swarmmobile "github.com/Nathandela/swarm/mobile"
)

// s17LoudSession is a session name with something recognisable to leak, in the spirit of
// S12's TestPBPUSH3_WakeEnvelopeIsExactlyTheFixedContentFreeSize.
const s17LoudSession = "build-box-17.local/refactor-the-auth-middleware"

// lockContent makes every content-tier unwrap refuse with the recoverable verdict, which is
// what an auth-gated Keystore key does when the user has not authenticated. It is the state
// EVERY push arrives in.
func s17LockContent(r *s17Rig) {
	r.Custody.Refuse("content", swarmmobile.KeyCustodyAuthRequired)
}

// s17RequireWakeWasProcessed is the non-vacuity control every RENDERING assertion in this file
// ends with, and it exists because the vacuous-pass probe found four tests without it.
//
// "The alert says the constant and reports the content tier closed" is satisfied PERFECTLY by a
// receiver that authenticates nothing, dedupes nothing and returns a canned struct for any input
// -- which is a receiver that renders a notification on the relay's say-so. Replaying the same
// payload is the cheapest observable proof that the wake was actually PROCESSED: a receiver that
// did the work refuses the second delivery, and a receiver that returned a constant accepts it.
func s17RequireWakeWasProcessed(t *testing.T, app *swarmmobile.App, payload string) {
	t.Helper()
	if _, err := app.HandlePushWake(payload); err == nil {
		t.Error("NON-VACUITY: the same payload was accepted a second time, so the assertions above " +
			"were made against a receiver that authenticated nothing and recorded nothing -- a " +
			"canned alert returned for any input satisfies every one of them")
	}
}

// ---------------------------------------------------------------------------
// The payload contract, measured where the phone reads it.
// ---------------------------------------------------------------------------

// PASSES TODAY, AND THAT IS NOT COVERAGE -- labelled so no evidence line counts it as earned.
// S12 shipped every property below and pinned each one AT THE SENDER. What is new here is the
// hop S12's tests stop short of: the FCM v1 request body, its data block, and the base64 the
// Android side actually reads. A rename or an extra key on that block breaks the phone and
// breaks nothing either side can see.
//
// TestS17_ThePayloadTheProviderCarriesIsTheFixedSizeContentFreeWake is the contract the
// FirebaseMessagingService is written against, stated once, at the receiving end.
func TestS17_ThePayloadTheProviderCarriesIsTheFixedSizeContentFreeWake(t *testing.T) {
	r := s17NewRig(t)
	r.RegisterThenBackground("fcm-token-payload")
	r.Wake(s17LoudSession)

	deliveries := r.FCM.Deliveries()
	if len(deliveries) != 1 {
		t.Fatalf("the provider received %d message(s), want 1", len(deliveries))
	}
	got := deliveries[0]

	// ONE key. Every additional key is metadata handed to the push provider, and PB-PUSH-3
	// concedes token, timing and size only.
	if len(got.Data) != 1 {
		var keys []string
		for k := range got.Data {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		t.Errorf("PB-PUSH-3: the FCM data block carries %d keys %v, want exactly one (\"e\"). "+
			"Every extra key is provider-visible metadata", len(got.Data), keys)
	}
	if got.Android["priority"] != "high" {
		t.Errorf("PB-RUN-4: the message priority is %v, want \"high\". A normal-priority message is "+
			"deferred until Doze ends, which is the exact state the wake exists to escape",
			got.Android["priority"])
	}

	raw := s17DecodePayload(t, r.LastPayload())
	if len(raw) != remotegw.PushWakeEnvelopeSize {
		t.Errorf("ADR-007 B20: the wake the provider carried is %d bytes, want the INVARIANT %d. "+
			"Size is the one property PB-PUSH-3 concedes the provider observes, and a conceded "+
			"disclosure is benign only while it is CONSTANT", len(raw), remotegw.PushWakeEnvelopeSize)
	}
	env, err := crypto.ParseEnvelope(raw)
	if err != nil {
		t.Fatalf("PB-PUSH-3: the payload the provider carried is not a parseable envelope: %v", err)
	}
	if env.Header.Type != crypto.TypePushWake {
		t.Errorf("A15: the payload is envelope type 0x%02x, want the push-wake type 0x%02x",
			env.Header.Type, crypto.TypePushWake)
	}
	if env.Header.RecipientKeyID != ([8]byte{}) || env.Header.SenderKeyID != ([8]byte{}) {
		t.Errorf("ADR-007 B20: the wake carries recipient key id %x and sender key id %x in the "+
			"CLEAR. Those are two stable identifiers linking every wake to one machine/device "+
			"pair for the life of the epoch, which is strictly more than PB-PUSH-3 promises",
			env.Header.RecipientKeyID, env.Header.SenderKeyID)
	}
}

// ---------------------------------------------------------------------------
// The locked device.
// ---------------------------------------------------------------------------

// TestS17_ALockedDeviceIsToldContentIsUnavailable is PB-PUSH-4's first half at the only seam
// Go owns: the phone reports that the user has not authenticated, so the app has nothing to
// render but the constant.
func TestS17_ALockedDeviceIsToldContentIsUnavailable(t *testing.T) {
	r := s17NewRig(t)

	// A paired phone that has registered its token and gone to the background: no connection,
	// screen locked. Every wake arrives in exactly this state (ADR-007 B16).
	r.RegisterThenBackground("fcm-token-locked")
	r.Wake(s17LoudSession)
	payload := r.LastPayload()

	s17LockContent(r)
	woken := r.OpenApp()

	alert, err := woken.HandlePushWake(payload)
	if err != nil {
		t.Fatalf("PB-PUSH-4: a genuine wake was refused on a LOCKED device: %v.\n"+
			"Locked is the only state a wake arrives in; a receiver that needs the content tier "+
			"has no path from the machine to a backgrounded phone at all", err)
	}
	if alert == nil {
		t.Fatal("PB-PUSH-4: HandlePushWake returned no alert and no error, so the app is told " +
			"nothing and renders nothing for a wake that was genuine")
	}
	if alert.ContentReady {
		t.Error("PB-PUSH-4/PB-KEY-2: the phone reported ContentReady on a device whose content " +
			"tier refuses every unwrap. That is the flag the app renders session content on")
	}
	if alert.Text != swarmmobile.WakeNotificationText {
		t.Errorf("PB-PUSH-4: the alert text is %q, want the constant %q",
			alert.Text, swarmmobile.WakeNotificationText)
	}
	s17RequireWakeWasProcessed(t, woken, payload)
}

// TestS17_HandlingAWakeCostsZeroContentUnwraps is the assertion that would fail if the app
// STARTED fetching content, which is the reachable defect and the one the requirement's own
// acceptance ("locked -> generic alert only") cannot see.
//
// It counts at the custody seam because that is where a fetch is visible whether it SUCCEEDS
// or is REFUSED. A test that asserted only "the notification text is generic" passes
// identically for an app that reads the roster, is refused, and renders the generic string
// anyway -- and that app decrypts session content the moment it runs on a handset the user
// unlocked five minutes ago.
func TestS17_HandlingAWakeCostsZeroContentUnwraps(t *testing.T) {
	r := s17NewRig(t)

	r.RegisterThenBackground("fcm-token-unwraps")
	r.Wake(s17LoudSession)
	payload := r.LastPayload()

	s17LockContent(r)
	woken := r.OpenApp()

	before := r.Custody.Unwraps("content")
	if _, err := woken.HandlePushWake(payload); err != nil {
		t.Fatalf("PB-PUSH-4: the wake was refused: %v", err)
	}
	if got := r.Custody.Unwraps("content") - before; got != 0 {
		t.Errorf("PB-KEY-2/PB-PUSH-4: handling ONE wake asked the content-tier KEK to unwrap %d "+
			"time(s), want 0.\nThe wake arrives with no user present. Every content-tier unwrap on "+
			"this path is either a decrypt of session content with a locked device, or a "+
			"dependency that refuses on a real handset and succeeds on one the user happened to "+
			"unlock a minute ago -- which is worse, because it works in every test", got)
	}

	// NON-VACUITY. "Asked for nothing" is satisfied by a receiver that DID nothing, and
	// counting wake-tier unwraps does NOT distinguish the two: the wake KEK is unwrapped once at
	// Resume, before this call, so that count is non-zero whatever HandlePushWake does. The
	// replay refusal is the observable that separates them.
	s17RequireWakeWasProcessed(t, woken, payload)
}

// TestS17_TheAlertCarriesNothingFromTheSession pins the notification's content-freeness against
// the values a leak would carry. It is deliberately assertion-by-sentinel rather than
// assertion-by-equality-with-a-constant: the constant is the implementer's to word, and what
// the requirement protects is that nothing about the session, the machine or the transition
// reaches the lock screen.
func TestS17_TheAlertCarriesNothingFromTheSession(t *testing.T) {
	r := s17NewRig(t)

	r.RegisterThenBackground("fcm-token-sentinels")
	r.Wake(s17LoudSession)
	payload := r.LastPayload()

	s17LockContent(r)
	woken := r.OpenApp()
	alert, err := woken.HandlePushWake(payload)
	if err != nil {
		t.Fatalf("PB-PUSH-4: the wake was refused: %v", err)
	}

	if leak := s17ContainsAny(alert.Text,
		s17LoudSession, "build-box-17", "refactor-the-auth-middleware",
		s17Machine, r.phoneTarget, "needs_input", "agent_stopped",
	); leak != "" {
		t.Errorf("PB-PUSH-4/PB-PUSH-3: the notification text contains %q. It is rendered on the "+
			"LOCK SCREEN of a device the owner may not be holding", leak)
	}
	if strings.TrimSpace(alert.Text) == "" {
		t.Error("PB-PUSH-4: the alert text is empty, so the check above passes for a notification " +
			"that says nothing and the user is woken by a blank line")
	}
	s17RequireWakeWasProcessed(t, woken, payload)
}

// TestS17_TheAlertIsTheSameForTwoDifferentSessions is the covert-channel half, and it is the
// same argument ADR-007 B20 makes about SIZE one layer down: a disclosure is benign only while
// it is CONSTANT. A notification whose wording varied with the session -- even "1 session" vs
// "3 sessions" -- is a channel from the machine to the lock screen of a locked phone.
func TestS17_TheAlertIsTheSameForTwoDifferentSessions(t *testing.T) {
	r := s17NewRig(t)

	r.RegisterThenBackground("fcm-token-constant")

	s17LockContent(r)
	woken := r.OpenApp()

	r.Wake(s17LoudSession)
	first, err := woken.HandlePushWake(r.LastPayload())
	if err != nil {
		t.Fatalf("PB-PUSH-4: the first wake was refused: %v", err)
	}
	r.Wake("m/a")
	second2 := r.LastPayload()
	second, err := woken.HandlePushWake(second2)
	if err != nil {
		t.Fatalf("PB-PUSH-4: the second wake was refused: %v", err)
	}
	if first.Text != second.Text {
		t.Errorf("PB-PUSH-4: a 46-character session produced %q and a 3-character session produced "+
			"%q. The text must not vary with anything the machine chose", first.Text, second.Text)
	}
	s17RequireWakeWasProcessed(t, woken, second2)
}

// TestS17_AnAuthenticatedDeviceIsToldContentIsAvailable is the requirement's second half and
// the non-vacuity control for the first: a receiver that reported ContentReady=false always
// would pass every test above and would leave the user with a generic notification forever,
// including while they are looking at the unlocked phone.
func TestS17_AnAuthenticatedDeviceIsToldContentIsAvailable(t *testing.T) {
	r := s17NewRig(t)

	app := r.StartApp()
	if err := app.RegisterPushToken("fcm-token-unlocked"); err != nil {
		t.Fatalf("App.RegisterPushToken: %v", err)
	}
	r.Wake(s17LoudSession)

	alert, err := app.HandlePushWake(r.LastPayload())
	if err != nil {
		t.Fatalf("PB-PUSH-4: a genuine wake was refused on an UNLOCKED device: %v", err)
	}
	if !alert.ContentReady {
		t.Error("PB-PUSH-4: the phone reports ContentReady=false while the content tier opens " +
			"normally. 'unless the user has authenticated' has to have an authenticated side, or " +
			"the notification is generic forever and the requirement is met by doing nothing")
	}
}

// ---------------------------------------------------------------------------
// A wake the phone should not have acted on renders nothing at all.
// ---------------------------------------------------------------------------

// TestS17_AReplayedWakeRendersNothing is PB-PUSH-3's replay window as the USER experiences it.
//
// The relay handles every wake and holds the envelope it was asked to deliver, so re-delivery
// is one line of code away for the party PB-SYNC-6 declares hostile. Without the window, that
// is a button which puts a notification on the owner's lock screen at a time of the relay's
// choosing, as often as it likes.
func TestS17_AReplayedWakeRendersNothing(t *testing.T) {
	r := s17NewRig(t)

	r.RegisterThenBackground("fcm-token-replay")
	r.Wake(s17LoudSession)
	payload := r.LastPayload()

	s17LockContent(r)
	woken := r.OpenApp()

	if _, err := woken.HandlePushWake(payload); err != nil {
		t.Fatalf("PB-PUSH-4: the FIRST delivery was refused: %v", err)
	}
	alert, err := woken.HandlePushWake(payload)
	if err == nil {
		t.Fatal("PB-PUSH-3/PB-PUSH-4: the same push payload was accepted TWICE")
	}
	if alert != nil {
		t.Error("PB-PUSH-4: a refused wake still returned an alert. A refusal that hands the app " +
			"something to render is not a refusal")
	}
}

// TestS17_TheReplayRefusalSurvivesAProcessDeath. Android SIGKILLs this process between one
// push and the next, so "the app has restarted since" is the cheapest way for a relay to make
// a replay land -- and a window held in memory would let it.
func TestS17_TheReplayRefusalSurvivesAProcessDeath(t *testing.T) {
	r := s17NewRig(t)

	r.RegisterThenBackground("fcm-token-processdeath")
	r.Wake(s17LoudSession)
	payload := r.LastPayload()

	s17LockContent(r)
	first := r.OpenApp()
	if _, err := first.HandlePushWake(payload); err != nil {
		t.Fatalf("PB-PUSH-4: the first delivery was refused: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("App.Close: %v", err)
	}

	// A new process over the same state directory: an Android app relaunch.
	restarted := r.OpenApp()
	if _, err := restarted.HandlePushWake(payload); err == nil {
		t.Error("PB-PUSH-3/PB-STATE-1: a push replayed after a process death was accepted. The " +
			"replay coordinate is persisted for exactly this, and until this slice nothing in " +
			"internal/ or mobile/ ever wrote it")
	}
}

// TestS17_AForgedWakeRendersNothingAndDoesNotSilenceThePhone is the class-(iv) fence at the
// facade: "a replay window with a persisted coordinate" is satisfiable by an implementation
// that hands the relay a one-packet permanent outage.
//
// If the coordinate moves before the AEAD is checked, one fabricated envelope with a huge seq
// pins the window at the top and every genuine wake for the rest of the epoch is refused as a
// replay -- on the device's only background path, silently. So the second half of this test is
// the load-bearing one.
func TestS17_AForgedWakeRendersNothingAndDoesNotSilenceThePhone(t *testing.T) {
	r := s17NewRig(t)

	r.RegisterThenBackground("fcm-token-forged")

	s17LockContent(r)
	woken := r.OpenApp()

	// The relay does not hold the wake key, so anything it fabricates fails the tag. The seq
	// it chooses is unbounded.
	forged := s17ForgedPayload(t)
	if _, err := woken.HandlePushWake(forged); err == nil {
		t.Fatal("PB-KEY-2: a payload the relay could have fabricated was accepted as a wake")
	}

	r.Wake(s17LoudSession)
	if _, err := woken.HandlePushWake(r.LastPayload()); err != nil {
		t.Errorf("PB-PUSH-3: after ONE forged payload, a genuine wake was refused with %v. The "+
			"relay just took this handset's only background path away with a single packet", err)
	}
}

// TestS17_AWakeAtAPairedButKeylessPhoneIsNotReportedAsTerminal is PB-APP-10's third state,
// which the requirement gained at e1ab559, met on the wake path.
//
// THE WAKE PATH IS WHERE THIS IS MOST REACHABLE AND HARDEST TO SEE. A phone paired minutes ago
// and backgrounded before its first grant landed has no epoch key, no user present and nothing
// on screen. The push arrives, the phone cannot open it -- and from inside HandlePushWake that
// is byte-for-byte indistinguishable from PB-KEY-3's terminal grant loss. The two have
// OPPOSITE remedies: this one self-heals, because the gateway re-appends its sidecar every
// session, and the remedy is waiting; ErrClassGrantLost's remedy is one the user cannot perform
// at all, and BeginPairing fail-fasts while the device is still registered (PB-STATE-10), so
// telling them to pair again sends them round a loop.
//
// S16 owns the rendered state. What S17 owes is not authoring the wrong class on the one path
// that reaches it with nobody watching.
func TestS17_AWakeAtAPairedButKeylessPhoneIsNotReportedAsTerminal(t *testing.T) {
	r := s17NewKeylessRig(t)

	r.RegisterThenBackground("fcm-token-keyless")
	r.Wake(s17LoudSession)
	payload := r.LastPayload()

	s17LockContent(r)
	woken := r.OpenApp()

	_, err := woken.HandlePushWake(payload)
	if err == nil {
		t.Fatal("PB-KEY-2: a phone with NO epoch wake key accepted a wake. It cannot have " +
			"authenticated it -- there is no key to authenticate it with")
	}
	class, cerr := woken.ErrorClass(err.Error())
	if cerr != nil {
		t.Fatalf("App.ErrorClass: %v", cerr)
	}
	if class == swarmmobile.ErrClassGrantLost {
		t.Errorf("PB-APP-10: a wake at a paired-but-keyless phone was classed %s, which is "+
			"TERMINAL. This phone is in the ordinary first-pairing window: the gateway re-appends "+
			"its sidecar every session, so the state clears itself and the remedy is waiting. "+
			"Rendering it as grant loss sends a healthy user to their machine for a fault that "+
			"does not exist, and BeginPairing fail-fasts while this device is registered, so the "+
			"advice cannot even be followed", class)
	}
	if class != swarmmobile.ErrClassAwaitingKey {
		t.Errorf("PB-APP-10: a wake at a paired-but-keyless phone was classed %q, want %q. The "+
			"third state exists because a keyless phone that is WAITING and one whose grant is "+
			"GONE are otherwise indistinguishable on screen", class, swarmmobile.ErrClassAwaitingKey)
	}
}

// TestS17_GarbageFromTheProviderIsRefusedWithoutCrashing. The payload comes from a push
// provider by way of Kotlin: a truncated data field, a stale project id, a message from a
// previous install. A panic here is a crash loop on a background wake, which the user sees as
// an app that dies whenever an agent stops.
func TestS17_GarbageFromTheProviderIsRefusedWithoutCrashing(t *testing.T) {
	r := s17NewRig(t)
	r.RegisterThenBackground("fcm-token-garbage")

	s17LockContent(r)
	woken := r.OpenApp()

	for name, payload := range map[string]string{
		"empty":          "",
		"not base64":     "!!!!not base64 at all!!!!",
		"base64 garbage": "aGVsbG8gd29ybGQ=",
		"truncated":      "AQI=",
	} {
		t.Run(name, func(t *testing.T) {
			alert, err := woken.HandlePushWake(payload)
			if err == nil {
				t.Errorf("PB-PUSH-4: %q was accepted as a wake", name)
			}
			if alert != nil {
				t.Errorf("PB-PUSH-4: %q was refused and still produced an alert", name)
			}
		})
	}

	// NON-VACUITY. Everything above is a refusal, and a receiver that refuses EVERYTHING
	// passes all of it.
	r.Wake(s17LoudSession)
	if _, err := woken.HandlePushWake(r.LastPayload()); err != nil {
		t.Errorf("NON-VACUITY: a genuine wake was refused with %v, so the refusals above prove "+
			"nothing", err)
	}
}
