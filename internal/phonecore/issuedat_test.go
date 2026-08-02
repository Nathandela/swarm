package phonecore

// FAILING-FIRST (TDD RED, GG-5) test for PB-GW-6: every phone -> machine seal must stamp
// a non-zero IssuedAt.
//
// Today every phone-side seal sets only {Version, EpochID, Seq} (input.go:59,
// command.go:100,121,143); the ONLY non-test producer of IssuedAt anywhere is the
// machine's OUTBOUND journal path (remotegw/relaysink.go:166). So inbound IssuedAt is 0,
// and the header field is AAD-covered -- authenticated as zero. PB-GW-2 turns on the
// bounded-age check that NewMailboxReceiver leaves disabled (maxAge == 0,
// envelope.go:219-221) with a 10-minute bound; against IssuedAt == 0 that computes an age
// of ~56 years and REJECTS EVERY LEGITIMATE COMMAND AND KEYSTROKE. This is the ordering
// trap PB-GW-6 exists to remove: the phone stamps first, the toggle is enabled after.
//
// SEAM: none new. The seal functions keep their signatures verbatim -- phonesim and the
// A7 tests call all five and are not in this slice -- so IssuedAt is stamped INSIDE
// sealInputFrame / SealCommandEnvelope / SealTakeControlEnvelope / SealLaunchEnvelope
// from the wall clock, exactly as relaysink.go does on the outbound side.
//
// The bounded-age check is asserted by RE-IMPLEMENTING crypto's own arithmetic here
// rather than by constructing a max-age receiver: internal/remote/crypto is FROZEN,
// MailboxReceiver.maxAge is unexported and has no setter, and PB-GW-2's toggle is another
// slice's work. What this slice owes is the phone's half -- frames whose authenticated
// age is small -- and that is exactly what is asserted.

import (
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// inboundMaxAge is PB-GW-2's binding value from §6.0.
const inboundMaxAge = 10 * time.Minute

// envelopeAge is crypto.MailboxReceiver.Accept's own age arithmetic
// (envelope.go:263), applied to a parsed envelope.
func envelopeAge(t *testing.T, now time.Time, raw []byte) time.Duration {
	t.Helper()
	env, err := crypto.ParseEnvelope(raw)
	if err != nil {
		t.Fatalf("parse envelope: %v", err)
	}
	return now.Sub(time.UnixMilli(env.Header.IssuedAt))
}

// phoneSeals returns one sealed envelope per phone -> machine seal function. All five are
// covered because PB-GW-2's toggle applies to the whole inbound stream: one unstamped
// producer is enough to make the toggle reject that class forever.
func phoneSeals(t *testing.T, key crypto.ContentKey, epoch uint32, seq *Sequencer) map[string][]byte {
	t.Helper()
	auth := takeControlAuth()
	launch := &protocol.LaunchReq{}

	data, err := SealInputData(key, epoch, seq.Next(), "m1/s1", []byte("ls\r"))
	if err != nil {
		t.Fatalf("SealInputData: %v", err)
	}
	resize, err := SealInputResize(key, epoch, seq.Next(), "m1/s1", 120, 40)
	if err != nil {
		t.Fatalf("SealInputResize: %v", err)
	}
	cmd, err := SealCommandEnvelope(key, epoch, seq.Next(), auth)
	if err != nil {
		t.Fatalf("SealCommandEnvelope: %v", err)
	}
	take, err := SealTakeControlEnvelope(key, epoch, seq.Next(), auth, "gate-token", 3600)
	if err != nil {
		t.Fatalf("SealTakeControlEnvelope: %v", err)
	}
	lau, err := SealLaunchEnvelope(key, epoch, seq.Next(), auth, launch)
	if err != nil {
		t.Fatalf("SealLaunchEnvelope: %v", err)
	}
	resync, err := SealResyncEnvelope(key, epoch, seq.Next(), auth, 12)
	if err != nil {
		t.Fatalf("SealResyncEnvelope: %v", err)
	}
	prefs, err := SealPushPrefsEnvelope(key, epoch, seq.Next(), auth, schema.PushPrefs{Version: 1})
	if err != nil {
		t.Fatalf("SealPushPrefsEnvelope: %v", err)
	}
	return map[string][]byte{
		"SealInputData":           data,
		"SealInputResize":         resize,
		"SealCommandEnvelope":     cmd,
		"SealTakeControlEnvelope": take,
		"SealLaunchEnvelope":      lau,
		"SealResyncEnvelope":      resync,
		"SealPushPrefsEnvelope":   prefs,
	}
}

// phoneSealSources are the files every phone -> machine seal producer lives in. The sweep
// below reads them, so the covered set cannot silently fall behind the package.
var phoneSealSources = []string{"command.go", "input.go"}

// TestPhoneSeals_TheSweepCoversEveryProducerInThePackage is PB-GW-6's completeness half, and
// it exists because the list above is HAND-KEPT. When S7 wrote it there were five producers;
// SealResyncEnvelope (PB-SYNC-2) and SealPushPrefsEnvelope (PB-PUSH-8) were added later and
// the list did not follow, so for two verbs "every phone -> machine seal stamps IssuedAt" was
// asserted about nothing. Both were measured: rewritten to seal inline without IssuedAt, they
// left internal/phonecore, internal/remotegw, mobile and internal/skeleton entirely GREEN --
// the only thing that caught either was another requirement's end-to-end conformance test.
//
// The producer set is DERIVED FROM THE SOURCE rather than restated, so a new Seal* that
// forgets the sealPhoneFrame funnel fails HERE, at PB-GW-6's own fence, instead of waiting for
// some unrelated e2e test to notice. The failure mode it guards is silent and permanent:
// PB-GW-2's 10-minute bound refuses an unstamped verb forever, with nothing logged.
func TestPhoneSeals_TheSweepCoversEveryProducerInThePackage(t *testing.T) {
	key := testContentKey()
	var seq Sequencer
	covered := phoneSeals(t, key, 7, &seq)

	// `func SealX(` at the start of a line: the package's own declaration syntax.
	decl := regexp.MustCompile(`(?m)^func (Seal[A-Za-z0-9_]*)\(`)
	found := 0
	for _, file := range phoneSealSources {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		for _, m := range decl.FindAllStringSubmatch(string(src), -1) {
			found++
			if _, ok := covered[m[1]]; !ok {
				t.Errorf("%s declares %s, which the IssuedAt sweep does not cover: PB-GW-6 says EVERY "+
					"phone -> machine seal stamps IssuedAt, and an uncovered producer that forgets the "+
					"stamp is refused by PB-GW-2's 10-minute bound forever, silently", file, m[1])
			}
		}
	}
	// Vacuity control: a regexp that matched nothing would make the loop above a no-op and
	// this fence would pass for a package with no producers at all.
	if found < len(covered) {
		t.Fatalf("the producer scan found %d declarations across %v but the sweep seals %d; the scan is "+
			"not reading the package", found, phoneSealSources, len(covered))
	}
}

// TestPhoneSeals_StampANonZeroIssuedAt is PB-GW-6's first acceptance criterion. The
// bracket is taken around the seals, so the stamp must be a real clock reading, not a
// constant that happens to be non-zero.
func TestPhoneSeals_StampANonZeroIssuedAt(t *testing.T) {
	key := testContentKey()
	var seq Sequencer

	before := time.Now().Add(-time.Second).UnixMilli()
	seals := phoneSeals(t, key, 7, &seq)
	after := time.Now().Add(time.Second).UnixMilli()

	for name, raw := range seals {
		env, err := crypto.ParseEnvelope(raw)
		if err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		if env.Header.IssuedAt == 0 {
			t.Errorf("%s seals IssuedAt = 0; PB-GW-2's bounded-age check computes an age of ~56 years and rejects every legitimate frame of this class", name)
			continue
		}
		if env.Header.IssuedAt < before || env.Header.IssuedAt > after {
			t.Errorf("%s seals IssuedAt = %d, outside [%d, %d]; the stamp must be the wall clock, not a placeholder", name, env.Header.IssuedAt, before, after)
		}
	}
}

// TestPhoneSeals_PassTheBoundedAgeCheckThatWouldRejectTodaysFrames is PB-GW-6's second
// acceptance criterion -- "a second test asserts PB-GW-2's toggle with real phone-sealed
// frames still passes traffic". The assertion is what makes the requirement honest: a
// stamp that satisfied the first test but sat outside the window would still brick the
// toggle.
//
// The control case is today's tree: an unstamped header, whose age against the same bound
// is ~56 years. It proves the check being applied is not vacuous.
func TestPhoneSeals_PassTheBoundedAgeCheckThatWouldRejectTodaysFrames(t *testing.T) {
	key := testContentKey()
	var seq Sequencer
	now := time.Now()

	for name, raw := range phoneSeals(t, key, 7, &seq) {
		if age := envelopeAge(t, now, raw); age > inboundMaxAge {
			t.Errorf("%s: authenticated age %v exceeds PB-GW-2's %v bound; enabling the toggle would refuse this frame with crypto.ErrStaleAge", name, age, inboundMaxAge)
		}
	}

	// Control: an IssuedAt of 0 -- exactly what every phone seal produces today -- is
	// far outside the bound, so the check above is not vacuously satisfiable.
	unstamped, err := crypto.SealMailbox(key, crypto.EnvelopeHeader{Version: crypto.VersionV1, EpochID: 7, Seq: 99}, []byte(`{"t":"data"}`))
	if err != nil {
		t.Fatalf("seal unstamped control: %v", err)
	}
	if age := envelopeAge(t, now, unstamped.Marshal()); age <= inboundMaxAge {
		t.Fatalf("control: an unstamped envelope has age %v, within the %v bound; the bounded-age assertion above proves nothing", age, inboundMaxAge)
	}
}
