package conformance_test

// Slice S16 -- PB-APP-9's RUNTIME half and PB-APP-10, over the real relay and real custody.
//
// The SOURCE half is ../s16_taxonomy_test.go and it covers every path, reached or not. This
// file covers the half source cannot see: errors the facade does not construct at all and
// merely lets through. crypto.ErrKeyInvalidated is produced in internal/remote/crypto,
// relay.ErrRevoked in the relay handshake, phonecore.ErrGrantLost in the core -- three
// packages, none of which the syntax fence reads, and all three are exactly the identities
// PB-APP-10 says must reach the user as DIFFERENT screens.
//
// THE SWEEP IS ENUMERATED FROM THE GOLDEN, not from a list here. That is what makes "a new
// error class without a mapping fails the test" mechanical rather than aspirational: a verb
// cannot reach the Android app without moving mobile/testdata/exported_surface.golden, and
// the moment it does, this sweep calls it and demands its errors classify.

import (
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/remote/grant"
	swarmmobile "github.com/Nathandela/swarm/mobile"
)

// s16LifecycleVerbs are held out of the sweep everywhere except the closed-app state,
// because they MUTATE the state the sweep is measuring: a Start halfway down an alphabetical
// walk means the verbs after S are being asked a different question from the ones before it.
// On a closed App they are safe -- every one of them refuses -- so that state runs the full
// surface with nothing held back.
var s16LifecycleVerbs = map[string]bool{"Start": true, "Stop": true, "Close": true}

// TestPBAPP9_EveryErrorTheFacadeReturnsCarriesAKnownClass is the sweep.
//
// It is deliberately built from ADVERSARIAL STATES rather than from happy paths: an error
// taxonomy is only ever read when something has gone wrong, so a sweep over a healthy app
// would classify almost nothing and pass.
func TestPBAPP9_EveryErrorTheFacadeReturnsCarriesAKnownClass(t *testing.T) {
	tokens, unknown := s16TaxonomyTokens(t)
	verbs := s16GoldenMethods(t)
	sort.Strings(verbs)

	h := newHarness(t)
	classify := s16Lookup(t, h.App, "ErrorClass", "(string) (string, error)", "PB-APP-9",
		"The Android side gets a Go error as an exception carrying only its MESSAGE, so the "+
			"class has to ride the message and be read back out of it. keycustody.go already "+
			"established the shape for the two custody verdicts; this generalises it.")

	// NON-VACUITY, FIRST. A classifier that answered one class for every input would satisfy
	// every other assertion in this file. It must be able to say "I do not know this".
	foreign := "java.lang.IllegalStateException: this string was never produced by swarmmobile"
	got, err := s16StringErr(t, classify.Call([]reflect.Value{reflect.ValueOf(foreign)}))
	if err != nil {
		t.Fatalf("App.ErrorClass on an unrelated message returned an error: %v", err)
	}
	if got != unknown {
		t.Fatalf("PB-APP-9: App.ErrorClass(%q) = %q, want the reserved unknown token %q.\n"+
			"A classifier that maps an arbitrary string to a real class cannot distinguish a "+
			"facade error from anything else, and the sweep below -- whose whole assertion is "+
			"that no facade error lands in unknown -- would pass against a constant function.",
			foreign, got, unknown)
	}

	states := s16ErrorStates(t, h)
	seen := map[string]bool{}
	for _, st := range states {
		for _, verb := range verbs {
			if s16LifecycleVerbs[verb] && !st.closed {
				continue
			}
			m := reflect.ValueOf(st.app).MethodByName(verb)
			if !m.IsValid() {
				// Reported by the source-level PB-BIND-7 guard; the sweep is about errors.
				continue
			}
			err := s16CallZero(t, m)
			if err == nil {
				continue
			}
			class, cerr := s16StringErr(t, classify.Call([]reflect.Value{reflect.ValueOf(err.Error())}))
			if cerr != nil {
				t.Fatalf("App.ErrorClass: %v", cerr)
			}
			switch {
			case class == "" || class == unknown:
				t.Errorf("PB-APP-9 [%s] App.%s returned an UNCLASSIFIED error:\n\t%v\n\t"+
					"classified as %q. Every error this facade returns must name the rendered state "+
					"the user sees; unclassified, it reaches the screen as an exception message and "+
					"the user is told nothing they can act on.", st.name, verb, err, class)
			case tokens[class] == "":
				t.Errorf("PB-APP-9 [%s] App.%s classified as %q, which has no row in "+
					"mobile/error_taxonomy.tsv:\n\t%v", st.name, verb, class, err)
			default:
				seen[class] = true
			}
		}
	}

	// The sweep must have EXERCISED the taxonomy, not merely failed to contradict it. A
	// facade whose every error came back as one class would pass everything above.
	if len(seen) < 4 {
		t.Errorf("PB-APP-9: the whole adversarial sweep produced only %d distinct error class(es) "+
			"(%v) across %d verbs and %d states. The taxonomy exists so the UI can route; one or "+
			"two classes means it cannot.", len(seen), s16Keys(seen), len(verbs), len(states))
	}
}

// s16ErrorState is one adversarial condition and the App standing in it.
type s16ErrorState struct {
	name   string
	app    *swarmmobile.App
	closed bool
}

// s16ErrorStates builds the matrix. Each is a condition a real handset reaches, named with
// how it gets there -- a state the app can never be in would prove nothing (standing defect
// class (v): a fence guarding a path production does not take).
func s16ErrorStates(t *testing.T, h *harness) []s16ErrorState {
	t.Helper()

	// 1. NEVER STARTED, NEVER PAIRED: the first launch after install.
	fresh := s16FreshApp(t, "ws://127.0.0.1:1")

	// 2. STARTED BUT UNPAIRED: the same install with the relay unreachable. Every send verb
	//    must refuse on the missing destination before it touches the wire.
	unpaired := s16FreshApp(t, "ws://127.0.0.1:1")
	if err := unpaired.Start(); err != nil {
		t.Fatalf("Start on a fresh install: %v", err)
	}

	// 3. CLOSED: Android's lifecycle callback ran and a UI thread is still mid-call.
	closed := s16FreshApp(t, "ws://127.0.0.1:1")
	if err := closed.Close(); err != nil {
		t.Fatalf("Close on a fresh install: %v", err)
	}

	// 4. SCREEN LOCKED: PB-KEY-7's purge. The content tier is gone and the wake tier is not.
	locked := h.App
	if err := locked.PurgeKeys(); err != nil {
		t.Fatalf("PurgeKeys: %v", err)
	}

	// 5. CUSTODY REFUSING, both verdicts, on a phone that is otherwise complete. These are
	//    the two identities PB-KEY-6 made distinguishable and PB-APP-10 must keep apart.
	authRefused := s16CustodyApp(t, swarmmobile.KeyCustodyAuthRequired)
	invalidated := s16CustodyApp(t, swarmmobile.KeyCustodyKeyInvalidated)

	return []s16ErrorState{
		{name: "fresh install, never started", app: fresh},
		{name: "started, never paired", app: unpaired},
		{name: "closed by the Android lifecycle", app: closed, closed: true},
		{name: "screen locked (PB-KEY-7 purge)", app: locked},
		{name: "custody refuses: authentication required", app: authRefused},
		{name: "custody refuses: key invalidated", app: invalidated},
	}
}

func s16FreshApp(t *testing.T, relayURL string) *swarmmobile.App {
	t.Helper()
	app, err := swarmmobile.NewApp(&swarmmobile.Config{
		StateDir: t.TempDir(), RelayURL: relayURL, MachineID: testMachineID,
	}, newTestCustody(t))
	if err != nil {
		t.Fatalf("swarmmobile.NewApp: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	return app
}

// s16CustodyApp is a phone whose Keystore refuses the CONTENT tier with the given verdict.
// The wake tier answers, because that is the real asymmetry: a locked handset still receives
// pushes (ADR-007 B9), so a refusal that took both tiers down would model nothing.
func s16CustodyApp(t *testing.T, verdict string) *swarmmobile.App {
	t.Helper()
	custody := newTestCustody(t)
	app, err := swarmmobile.NewApp(&swarmmobile.Config{
		StateDir: t.TempDir(), RelayURL: "ws://127.0.0.1:1", MachineID: testMachineID,
	}, custody)
	if err != nil {
		t.Fatalf("swarmmobile.NewApp: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	custody.Refuse("content", verdict)
	return app
}

// s16CallZero invokes a bound method with the zero value of every parameter and returns its
// error result. Zero arguments are the adversarial ones on this surface: an empty session id,
// a nil LaunchSpec, a nil PushPreference and a zero cursor are all things a screen can pass.
func s16CallZero(t *testing.T, m reflect.Value) error {
	t.Helper()
	mt := m.Type()
	in := make([]reflect.Value, mt.NumIn())
	for i := range in {
		in[i] = reflect.Zero(mt.In(i))
	}
	out := m.Call(in)
	if len(out) == 0 {
		return nil
	}
	return s16AsError(t, out[len(out)-1])
}

func s16Keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---- PB-APP-10 -----------------------------------------------------------------

// TestPBAPP10_ARevokedDeviceIsToldToRePairInsteadOfLoopingForever.
//
// THE DEFECT THIS IS WRITTEN AGAINST, verified in source before the test was written.
// mobile/relay.go's dial-error switch has exactly two arms -- crypto.ErrKeyInvalidated and
// crypto.ErrKeyAuthRequired -- and everything else falls through to `continue`. A revoked
// device gets neither: the relay answers the AUTH frame with codeRevoked, which
// relay.codeToErr maps to relay.ErrRevoked, so the phone redials every reconnectDelay
// (250 ms) for the life of the process, showing "reconnecting" behind a spinner.
//
// That is the "failure loop" PB-APP-10 names in as many words, and it is the state a user
// reaches by doing the one thing the product tells them to do when they lose the handset.
func TestPBAPP10_ARevokedDeviceIsToldToRePairInsteadOfLoopingForever(t *testing.T) {
	h := newHarness(t)

	if got, ok := awaitConnState(t, h.App, "online", 5*time.Second); !ok {
		t.Fatalf("precondition: the phone never connected (state %q); a revoke assertion against "+
			"a phone that was never online proves nothing", got)
	}

	// The OWNER revokes the phone, through the same verb the machine side uses. Nothing here
	// is simulated: relay.Client.DeviceRevoke unpairs and marks the routing id revoked, and
	// the relay severs the live socket.
	if err := h.machineRelay.DeviceRevoke(h.ctx, h.phoneTarget); err != nil {
		t.Fatalf("machine DeviceRevoke: %v", err)
	}

	got, ok := awaitConnState(t, h.App, "revoked", 10*time.Second)
	if !ok {
		t.Fatalf("PB-APP-10: a revoked phone reports %q.\n"+
			"relay.ErrRevoked is the ONLY signal it ever gets and mobile/relay.go's dial switch "+
			"does not name it, so it redials every 250 ms forever behind a spinner -- the failure "+
			"LOOP this requirement forbids, reached by the owner doing exactly what the product "+
			"tells them to do when a handset is lost.\n"+
			"The state must be a seventh literal beside offline/connecting/online/reconnecting/"+
			"reauth_required/repair_required -- \"revoked\" -- carrying the re-pair remedy, and it "+
			"needs a row in mobile/error_taxonomy.tsv for %s.", got, "relay.ErrRevoked")
	}

	// TERMINAL, for the same reason repair_required is: nothing on this device can un-revoke
	// itself, so every retry is a websocket handshake spent re-proving that, on a battery,
	// against the relay's per-source budget. Two windows, so a dial already in flight when the
	// state flipped is not counted as a steady-state retry.
	time.Sleep(300 * time.Millisecond)
	before := h.Custody.Unwraps("wake")
	time.Sleep(1 * time.Second)
	if after := h.Custody.Unwraps("wake"); after != before {
		t.Errorf("PB-APP-10: a revoked phone made %d further dial attempts. Revocation is not "+
			"recoverable on-device: the loop must stop, exactly as it does for a permanently "+
			"invalidated key", after-before)
	}

	// And it must NOT be reported as a custody failure. The two look identical to a user
	// ("pair again") and are not the same event: repair_required means this handset's Keystore
	// key is gone, revoked means the OWNER removed it, and the machine-side state differs.
	state, err := h.App.ConnectionState()
	if err != nil {
		t.Fatalf("ConnectionState: %v", err)
	}
	if state == "repair_required" {
		t.Errorf("PB-APP-10: a revoked phone reports repair_required, the CUSTODY state. The " +
			"remedy happens to coincide; the cause does not, and the machine-side record of a " +
			"revoked device is what the owner has to clear before a re-pair can succeed")
	}
	// Nor may the verdict DECAY. The transport loop's normal bottom runs setConn("offline"),
	// so an implementation that `break`s out of the loop rather than returning erases the one
	// state that told the user what to do -- the hazard S14 recorded for repair_required, which
	// this state inherits verbatim. A full second past the transition is well past the 250 ms
	// reconnect cadence, so a loop still running would have overwritten it by now.
	if state == "offline" || state == "reconnecting" || state == "connecting" {
		t.Errorf("PB-APP-10: the revoked verdict decayed into %q", state)
	}
}

// TestPBAPP10_AGrantLossDeviceShowsPBKEY3sStateAndIsNeverSentToRePair.
//
// The third remedy, and the one that is a BRICK when misrouted. phonecore.ErrGrantLost was
// given its own identity for this: custody is fine, the MACHINE's grant never arrived. A
// phone shown "pair this device again" cannot act on it -- BeginPairing fail-fasts while a
// device is registered (PB-STATE-10) -- so the only exit is physical access to the machine.
//
// THE SCENARIO IS THE ONE PRODUCTION DETECTS, and getting that right took a correction from
// the S16 implementer, recorded here because the first version of this test was the exact
// defect class the brief warns about. It seeded a state directory, stripped the keys by hand
// and delivered no grant at all -- a condition phonecore.grantLossDetected can NEVER see,
// because its two conditions are `keyless` AND crypto.ErrGrantReplay. Nothing in that setup
// produces a replay, so the test demanded a detection rule the core does not have and the
// core's own doc argues against: the phone cannot measure "drained, no grant, retention cap
// passed" -- it holds no pairing timestamp, and the retention cap is RELAY configuration
// asserted by the party this design treats as hostile.
//
// So the setup below reaches the real condition, and every input comes from a production
// path with nothing supplied by hand:
//
//  1. a real pairing and a real enroll.Enroll bootstrap grant -- the only thing that ever
//     puts an epoch key where a phone can reach it (cmd/swarm-remote/deliver.go);
//  2. PurgeKeys, which is PB-KEY-7's screen lock and the production way a phone becomes
//     keyless WITHOUT losing its grant watermark;
//  3. the machine's own bootstrap frame re-appended -- exactly what the gateway does once
//     per session from its persistent sidecar.
//
// Keyless plus a replay of coordinates already consumed is PROOF that re-delivery can never
// help, which is what makes it terminal rather than a wait. The core reaches that verdict
// today. What S16 owes is the JOIN: the facade must surface it as phonecore.ErrGrantLost
// rather than as errNoContentKey, whose remedy -- "call InstallContentKey after unlocking" --
// is advice nothing in production can act on, because InstallContentKey is called from Kotlin
// and Kotlin has no source for the bytes (that was PB-KEY-10).
func TestPBAPP10_AGrantLossDeviceShowsPBKEY3sStateAndIsNeverSentToRePair(t *testing.T) {
	ctx, relayURL, _, open := s10FreshInstall(t)
	m := newS10Machine(t, ctx, relayURL)

	app := open()
	runPairing(t, app, m)
	m.enrollAndDeliver()

	// Waited on a coordinate the GRANT moves, not one the PIN moves. State.EpochID comes from
	// the machine's payload at pin() time, so `EpochID != 0` is satisfied by the handshake and
	// says nothing about whether a key arrived -- the same proxy that made the PB-PAIR-5
	// different-machine subtest run against a keyless phone. RelayCursor advances only when the
	// phone commits an inbound frame, and the bootstrap grant is the only frame published here.
	eventually(t, "the phone never obtained an epoch key through the bootstrap grant", func() bool {
		sum, err := app.StateSummary()
		return err == nil && sum.EpochID == int64(s10BootstrapEpoch) && sum.RelayCursor > 0
	})

	// Reconciled FIRST. Without it every mutating verb refuses on PB-SYNC-7's fail-closed gate,
	// which arrives BEFORE the grant is consulted -- so the probe below would be measuring a
	// different requirement entirely.
	if err := m.sink.Reconcile(); err != nil {
		t.Fatalf("sink.Reconcile: %v", err)
	}
	eventually(t, "the phone never adopted the machine's rollback authorities", func() bool {
		sum, err := app.StateSummary()
		return err == nil && sum.Reconciled
	})

	// THE PHONE LANDS IN AN EPOCH IT HOLDS NO KEY FOR. This replaced PurgeKeys as the setup
	// when ADR-007 B35 established that a screen lock must NOT leave the phone keyless: the
	// lock now keeps the sealed key at rest and one fresh unwrap restores it, so it proves
	// nothing about delivery and is fenced the other way by
	// TestPBKEY7_AScreenLockIsNotGrantLossAndRecoversLocally below.
	//
	// What remains is PB-KEY-3's own scenario, and it is still a production path with nothing
	// supplied by hand: mobile.App.pin zeroes State.Keys when a pairing lands in a DIFFERENT
	// epoch -- the tier keys belong to the old one -- and phonecore.resealTier carries a sealed
	// key only into the epoch it was sealed for, so the blob goes with it. The phone is then
	// genuinely keyless, not merely locked, with its grant watermark standing at the sidecar's
	// own coordinates.
	m.pairEpoch = s10BootstrapEpoch + 1
	runPairing(t, app, m)
	eventually(t, "the phone never moved to the epoch the second pairing published", func() bool {
		sum, err := app.StateSummary()
		return err == nil && sum.EpochID == int64(s10BootstrapEpoch+1)
	})
	if err := app.UnlockContent(); err != nil {
		t.Fatalf("UnlockContent on the rotated phone: %v", err)
	}

	// The gateway re-appends the same sidecar frame every session. On a keyless phone whose
	// watermark has already consumed these coordinates, that is crypto.ErrGrantReplay.
	frame, err := grant.MarshalBootstrap(m.Grant)
	if err != nil {
		t.Fatalf("grant.MarshalBootstrap: %v", err)
	}
	if _, err := m.conn.MailboxAppend(ctx, m.phoneTarget(), frame); err != nil {
		t.Fatalf("re-append the bootstrap grant: %v", err)
	}
	eventually(t, "the core never reached PB-KEY-3's terminal state; the scenario did not "+
		"produce the replay grantLossDetected requires, so everything below is vacuous", func() bool {
		state, serr := app.StreamState(phonecore.StreamGrant)
		return serr == nil && state == "stale"
	})

	// PB-KEY-3 forbids an "indefinite decrypt-failure loop", so the identity must reach the
	// caller with no user action and no verb the UI has to know to call.
	// REVOKE is the probe, and it is the right one rather than a convenient one. A rotated
	// phone is by construction UNRECONCILED -- ReconciledEpoch belongs to the epoch it left,
	// and the machine's reconcile record for the new one is sealed under a key this phone does
	// not have -- so every target-selecting verb refuses on PB-SYNC-7's gate before the grant
	// verdict is ever consulted. device_revoke is exempt from that gate by PB-STATE-4's
	// amendment precisely because it selects no target, which makes it the one verb that can
	// reach resolveSend here. It is also the verb that matters: the panic button on a handset
	// whose grant is gone must say so, not report a sync problem the user cannot fix.
	var lastErr error
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, lastErr = app.RevokeThisDevice(); lastErr != nil &&
			errors.Is(lastErr, phonecore.ErrGrantLost) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if lastErr == nil || !errors.Is(lastErr, phonecore.ErrGrantLost) {
		t.Fatalf("PB-APP-10/PB-KEY-3: a phone the CORE has already marked grant-lost reports:\n"+
			"\t%v\nwant an error wrapping phonecore.ErrGrantLost.\n"+
			"The core reached the verdict (Core.MarkGrantLost, via installGrant's replay arm) and "+
			"the facade discards it: resolveSend answers errNoContentKey -- \"call "+
			"InstallContentKey after unlocking\" -- which is advice NOTHING in production can act "+
			"on, since InstallContentKey is called from Kotlin and Kotlin has no source for the "+
			"bytes. The join between the core's durable verdict and the user's screen is what "+
			"S16 owes.", lastErr)
	}

	// The identity must survive to the UI as a class of its own. This is the assertion the
	// whole three-remedy design exists for.
	classify := s16Lookup(t, app, "ErrorClass", "(string) (string, error)", "PB-APP-10",
		"The three remedies must be distinguishable from Kotlin, not merely inside Go.")
	class, cerr := s16StringErr(t, classify.Call([]reflect.Value{reflect.ValueOf(lastErr.Error())}))
	if cerr != nil {
		t.Fatalf("App.ErrorClass: %v", cerr)
	}
	tokens, unknown := s16TaxonomyTokens(t)
	if class == unknown || tokens[class] == "" {
		t.Fatalf("PB-APP-10: grant loss classified as %q, which is unknown or unmapped", class)
	}

	// NOT the re-pair class, and not the re-authenticate one. Asserted against the classes the
	// two custody sentinels resolve to rather than against a literal, so a rename cannot
	// silently satisfy it.
	for _, wrong := range []struct{ msg, what string }{
		{swarmmobile.KeyCustodyKeyInvalidated + ": key gone", "the PERMANENT custody refusal (re-pair)"},
		{swarmmobile.KeyCustodyAuthRequired + ": authenticate", "the RECOVERABLE custody refusal (biometric)"},
	} {
		other, oerr := s16StringErr(t, classify.Call([]reflect.Value{reflect.ValueOf(wrong.msg)}))
		if oerr != nil {
			t.Fatalf("App.ErrorClass: %v", oerr)
		}
		if other == class {
			t.Errorf("PB-APP-10: grant loss and %s both classify as %q. Grant loss is the one "+
				"remedy the USER cannot perform: routing it to re-pair sends them to a "+
				"BeginPairing that fail-fasts while this device is still registered, and the only "+
				"exit left is physical access to the machine (PB-STATE-10)", wrong.what, class)
		}
	}
}
