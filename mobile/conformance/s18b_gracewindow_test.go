package conformance_test

// The DETERMINISTIC fence over ADR-007 B23(b): the post-pairing revoke grace window.
//
// WHY IT IS OWED. B23(b) is the phone half of PB-STATE-10. `relay.ErrRevoked` is terminal --
// the dial loop RETURNS rather than retrying -- so a handset that latches it before the owner's
// machine gets round to lifting the ban stays on that screen until the Android process is
// rebuilt, which on a phone can be hours. The grace window is what lets the re-armed generation
// retry across the genuinely unorderable race between the phone's first post-pairing dial and
// the machine's authorize.
//
// WHAT WAS COVERING IT BEFORE THIS FILE: nothing, and the one test that touched the path was
// racy in BOTH directions. cmd/swarm's TestPBSTATE10_TheSameHandsetRecoversWithoutAFactoryReset
// drives the whole recovery end to end and therefore only ever observes whichever side of the
// race won on the day -- an independent reviewer measured it failing 2 runs in 10 on CORRECT
// code (its poll caught the state the grace window deliberately HOLDS between retries and
// called it terminal), while deleting the grace window entirely was caught in only 3 runs of 5.
// A regression removing B23(b) survived a single run 40% of the time.
//
// SO THIS TEST DRIVES THE RACE INSTEAD OF SAMPLING IT. The machine's authorize is HELD until
// the phone's first post-pairing dial has been refused -- observed on the event plane, not
// guessed at with a sleep -- which is the losing side of the race made unconditional. Without
// the grace window the generation is over by then and no later authorize can reach it, so the
// mutation is caught every run rather than three times in five.
//
// IT ALSO PINS THE STATE SEQUENCE, because B23's prose ("the state stays revoked throughout")
// is imprecise: rearmAfterPairing starts a FRESH generation, and a fresh generation runs
// `run` with first=true, which sets "connecting" until the first dial resolves. The sequence
// is connecting -> revoked -> online, and what the requirement actually forbids -- the
// "reconnecting" spinner PB-APP-10 calls a failure loop -- never appears.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/remote/relay"
	swarmmobile "github.com/Nathandela/swarm/mobile"
)

// s18bConnLog records the connection states the facade emits, in order. Events are queued by
// the dispatcher before a listener is installed, so a listener set between NewApp and Start
// still sees the first generation's states.
type s18bConnLog struct {
	mu     sync.Mutex
	states []string
}

func (l *s18bConnLog) OnEvent(e *swarmmobile.Event) {
	if e == nil || e.Kind != "connection" || e.Stream != "" {
		return // "connection"+Stream:"reconcile" is the adoption event, not a transport state.
	}
	l.mu.Lock()
	l.states = append(l.states, e.State)
	l.mu.Unlock()
}

func (l *s18bConnLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.states...)
}

// await polls the recorded sequence until want holds, and reports what it saw if it never does.
func (l *s18bConnLog) await(t *testing.T, why string, want func([]string) bool) []string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		got := l.snapshot()
		if want(got) {
			return got
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("%s\nconnection states seen, in order: %v", why, got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestPBSTATE10_ThePostPairingGraceWindowSurvivesADialThatLosesTheRace.
//
// The race, made unconditional: the phone dials first and is refused, and only THEN does the
// machine authorize. That ordering is legitimate in production -- `swarm remote pair`'s
// authorizeAtRelay runs over a connection of the machine's own, and the gateway's idempotent
// call runs whenever a supervised process happens to boot -- so a recovery that only worked
// when the phone lost is PB-STATE-10's brick one layer down.
func TestPBSTATE10_ThePostPairingGraceWindowSurvivesADialThatLosesTheRace(t *testing.T) {
	_, relayURL := s16FreshRelay(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	dir := t.TempDir()
	custody := newTestCustody(t)
	coreDir := pairedNamespace(t, dir, testMachineID)

	// Provision the phone's device keys first, so the machine can name its routing id before
	// the facade ever opens the directory. This is the same order newHarness uses.
	provision, err := phonecore.Resume(phonecore.Config{
		Dir: coreDir, Machine: testMachineID,
		WakeSealer: custody.wakeSealer(), ContentSealer: custody.contentSealer(),
	})
	if err != nil {
		t.Fatalf("provision the phone's keystore: %v", err)
	}
	phonePub := provision.KeyStore().RelayAuthPublic()
	phoneRID := relay.RoutingID(phonePub)

	// The OWNER'S MACHINE, and it is one identity for both halves on purpose: the ban records
	// its placer (ADR-007 B24) and `swarm remote revoke` / `swarm remote pair` both act over
	// withMachineRelay, so the authorize that lifts it comes from the connection that placed it.
	mPub, mPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("machine relay-auth key: %v", err)
	}
	machine, err := relay.Dial(ctx, relayURL, relay.ClientAuth{
		RelayAuthPub: mPub,
		Sign:         func(c []byte) ([]byte, error) { return ed25519.Sign(mPriv, c), nil },
	})
	if err != nil {
		t.Fatalf("machine dial: %v", err)
	}
	t.Cleanup(func() { _ = machine.Close() })
	// The phone's relay-route consent, signed through its own custody exactly as
	// mobile/pairing.go signs it into msg3 (ADR-007 B27/B38). Without it the machine's
	// authorize is refused and the ban below could never be lifted -- which is the
	// recovery PB-STATE-10 is about.
	if err := machine.AuthorizeDevice(ctx, phonePub, consentFrom(t, provision.KeyStore(), relay.RoutingID(mPub))); err != nil {
		t.Fatalf("machine authorize phone: %v", err)
	}
	if err := machine.DeviceRevoke(ctx, phoneRID); err != nil {
		t.Fatalf("machine revoke phone: %v", err)
	}

	// The stranded handset: same device.key, same routing id, and THE PAIRING RECORD IT WOULD
	// ACTUALLY HOLD -- which is why a handset recovers into this state without a full app-data
	// wipe rather than in spite of one.
	//
	// THE RECORD USED TO BE OMITTED, and the omission was invisible while the ban was global:
	// a ban keyed by relay-auth routing id alone answered every dial, so a handset that knew
	// nothing about its machine was still refused. The ban is now scoped to the relationship it
	// ended (ADR-007 B49, because a global one made every revoke mutual assured destruction),
	// and a scoped verdict is asked for by naming the peer -- so the fixture has to hold the
	// coordinate a real stranded handset holds. MachineRelayAuthPub is wake-tier durable state
	// written at pairing; it is how the phone reaches its machine after any restart, which is
	// the same restart this test is simulating. The fixture was modelling something LESS than
	// the handset the requirement is about.
	store, err := phonecore.OpenStore(filepath.Join(coreDir, phonecore.StateFileName), testMachineID,
		custody.wakeSealer(), custody.contentSealer())
	if err != nil {
		t.Fatalf("open the stranded phone's state: %v", err)
	}
	if err := store.Save(phonecore.State{
		Machine:             testMachineID,
		MachineRelayAuthPub: mPub,
		OperatorNamespace:   "owner",
		RoutingID:           phoneRID,
	}); err != nil {
		t.Fatalf("seed the stranded phone's pairing record: %v", err)
	}

	log := &s18bConnLog{}
	app, err := swarmmobile.NewApp(&swarmmobile.Config{
		StateDir: dir, RelayURL: relayURL, MachineID: testMachineID,
	}, custody)
	if err != nil {
		t.Fatalf("swarmmobile.NewApp: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	if err := app.SetEventListener(log); err != nil {
		t.Fatalf("SetEventListener: %v", err)
	}
	if err := app.Start(); err != nil {
		t.Fatalf("App.Start: %v", err)
	}

	// The terminal state is LATCHED before the pairing that recovers it -- which is the whole
	// reason a re-arm is owed at all.
	log.await(t, "PB-APP-10: the banned handset never reported \"revoked\". Without that state "+
		"latched there is nothing for the pairing to make stale and this test measures nothing",
		func(s []string) bool { return len(s) >= 1 && s[len(s)-1] == "revoked" })
	stranded := len(log.snapshot())

	// THE OWNER ACTS: a real pairing over the real rendezvous, which is unauthenticated
	// (relay.DialRaw) and therefore completes perfectly for a handset the relay has banned.
	mSignPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("machine sign key: %v", err)
	}
	// UNDER THE MACHINE'S OWN RELAY IDENTITY -- the one that placed the ban. A machine's
	// relay-auth key is durable state and `swarm remote pair` re-pairs under it; a ceremony
	// that minted a fresh one would re-pin the handset onto a machine that has revoked
	// nobody, so the refusal this test holds the race open for could not happen and the
	// recovery below would prove nothing about lifting a ban.
	mp := s16MachinePairingAs(t, relayURL, mSignPub, mPub, testEpochID, true)
	p := s16BeginConfirmed(t, app, mp.QR)
	s16AwaitSAS(t, p)
	if err := p.Confirm(); err != nil {
		t.Fatalf("Pairing.Confirm: %v", err)
	}

	// THE HOLD. The authorize is withheld until the re-armed generation has dialled and been
	// refused: "connecting" then "revoked" AFTER the pairing. This is the phone LOSING the race
	// on every run, which is what the end-to-end test can only sample -- there the machine's
	// authorize wins often enough that deleting the grace window escapes 2 runs in 5.
	log.await(t, "PB-STATE-10: the pairing did not re-arm the phone's transport. "+
		"rearmAfterPairing is the phone half of the recovery: connRevoked RETURNS from the dial "+
		"loop, so the generation is over, and a completed pairing is the one event that can make "+
		"that verdict stale. Without it the handset shows \"revoked\" until the Android process "+
		"is rebuilt -- the brick reached through the remedy",
		func(s []string) bool {
			return len(s) >= stranded+2 && s[stranded] == "connecting" && s[stranded+1] == "revoked"
		})
	// A SECOND consent, over a fresh ceremony, because that is what the pairing above
	// actually produced (ADR-007 B47): the phone signs a new one in front of the owner,
	// and the credential its revoke retired authorizes nothing ever again. Replaying the
	// pre-revoke bytes here would be B47's attack, not PB-STATE-10's remedy.
	if err := machine.AuthorizeDevice(ctx, phonePub, consentFrom(t, provision.KeyStore(), relay.RoutingID(mPub))); err != nil {
		t.Fatalf("machine authorize after the refused dial: %v", err)
	}

	// RECOVERY, and it can only happen inside the window: the generation that was going to
	// notice this authorize is the one the grace window kept alive. Ten seconds is comfortably
	// inside the 30-second grace and comfortably outside the 250 ms reconnect delay.
	final := log.await(t, "PB-STATE-10/ADR-007 B23(b): the machine lifted the ban and the phone "+
		"never came back online.\nThe grace window is what makes the losing side of this race "+
		"survivable: without it the first refused dial after the pairing RETURNS from the loop, "+
		"the generation is over, and no later authorize can be noticed -- so the recovery works "+
		"only when the phone happens to dial second. That is the same brick PB-STATE-10 forbids, "+
		"reached through the remedy the product told the owner to use",
		func(s []string) bool { return len(s) > 0 && s[len(s)-1] == "online" })

	// AND NOTHING HID BEHIND A SPINNER. PB-APP-10 forbids the failure LOOP, and "reconnecting"
	// is what a failure loop looks like on screen: the retry that the grace window permits must
	// not be dressed up as an ordinary reconnect. The one transient is the "connecting" that
	// opens any fresh generation, which is B23's claim stated precisely.
	post := final[stranded:]
	want := []string{"connecting", "revoked", "online"}
	if strings.Join(post, ",") != strings.Join(want, ",") {
		t.Errorf("ADR-007 B23(b): the post-pairing connection states were %v, want %v.\n"+
			"\"reconnecting\" anywhere here is the spinner PB-APP-10 calls a failure loop -- the "+
			"user is being shown progress while the app re-proves a revocation. Anything else is a "+
			"state sequence the ADR does not describe", post, want)
	}
}
