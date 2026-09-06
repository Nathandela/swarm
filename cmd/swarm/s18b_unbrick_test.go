package main

// FAILING-FIRST tests for slice S18b -- PB-STATE-10: fail-closed must not mean bricked.
//
// THE REQUIREMENT, and why it is one requirement rather than three bugs. PB-STATE-4 fails
// closed on corrupt durable state and the phone tells its user to pair again; PB-KEY-3
// establishes that a re-pair is REFUSED while a device is registered, because BeginPairing
// fail-fasts on a non-empty registry (internal/skeleton/pairing.go, single-device policy). So
// the phone advises the one act the machine will not permit, and the handset's only exit is
// physical access to the machine. Three distinct on-device states arrive at that same wall:
//
//   - corrupt durable state (phonecore.ErrCorruptState) -- and this one is worse than the
//     others, because the error fails Resume, so the app never constructs and never OFFERS
//     the re-pair it advises;
//   - a permanently invalidated Keystore key (crypto.ErrKeyInvalidated -> REPAIR_REQUIRED,
//     remedy re_pair) -- refused while registered;
//   - a lost epoch grant (phonecore.ErrGrantLost -> GRANT_LOST, remedy machine_regrant) --
//     a DIFFERENT remedy, and the requirement says in as many words that re-grant "cannot
//     recover a phone whose local state is corrupt and fail-closed".
//
// The requirement therefore demands its own OWNER-SIDE flow, unconditional and not inherited
// from PB-KEY-3's optional re-grant branch: list/identify the stranded device, revoke or
// unregister it, purge machine AND relay state, re-pair.
//
// ACCEPTANCE: "Test drives the exact CLI-visible path: corruption -> fail-closed ->
// owner-side recovery -> working re-pair, with NO STEP REQUIRING UNDOCUMENTED KNOWLEDGE."
//
// THE LAST CLAUSE IS THE SLICE. A recovery that works only for an operator who already knows
// to run `swarm remote revoke` -- or to delete a particular file -- satisfies "reachable" and
// fails "discoverable", and discoverability is what the requirement asks for. So these tests
// assert a CLOSURE PROPERTY rather than a sequence: every command the operator runs must have
// been NAMED by the output of the step before it, starting from the only text a stranded user
// has, which is what their phone printed. TestPBSTATE10_TheRecoveryChainIsClosedUnderWhatThe
// OperatorWasTold is that assertion; the tests above it pin each link separately so a RED run
// says which link is missing rather than only that the chain is open.
//
// WHY THIS FILE IS IN package main AND DRIVES A REAL PHONE. The path the requirement names
// starts on the handset and ends on the handset, and only its middle is CLI. cmd/swarm is
// the one package that can run the real verbs (runRemote/runRemotePair are unexported), and
// swarmmobile is importable from a test in it -- so the whole path fits in one process:
// a real relay, a real skeleton daemon over a real machine identity, and a real
// swarmmobile.App as the phone. Nothing here is scripted or faked except the gateway
// supervisor (the pre-existing installFakeSupervisor seam, so no test touches launchd).
//
// INTENDED PRODUCTION (RED -- none of this exists yet; GREEN implements it):
//
//   - the phone's corrupt-state refusal names the owner-side flow by the commands that
//     perform it, not merely "pair again";
//   - `swarm remote pair`'s already-paired refusal names `swarm remote devices` (how to
//     learn the id) and `swarm remote revoke <device-id>` (the step that unblocks it);
//   - `swarm remote revoke` names `swarm remote pair` as the next step;
//   - the revoke PURGES the stranded device's relay-v2 state;
//   - and the purge does NOT permanently ban the handset's relay-auth key, or the flow
//     trades one brick for another.

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remote/pairing"
	"github.com/Nathandela/swarm/internal/remotegw"
	"github.com/Nathandela/swarm/internal/skeleton"
	swarmmobile "github.com/Nathandela/swarm/mobile"
)

// s18bCustody is the Android Keystore stand-in: two fixed 32-byte tier KEKs. It survives a
// "clear the app's data" (which deletes files, not the Keystore) and that is exactly what
// the recovery test needs -- a factory-reset custody would model a different failure.
type s18bCustody struct{ wake, content []byte }

func newS18bCustody(t *testing.T) *s18bCustody {
	t.Helper()
	c := &s18bCustody{wake: make([]byte, 32), content: make([]byte, 32)}
	for _, k := range [][]byte{c.wake, c.content} {
		if _, err := rand.Read(k); err != nil {
			t.Fatalf("generate a Keystore stand-in KEK: %v", err)
		}
	}
	return c
}

func (c *s18bCustody) WakeKEK() ([]byte, error)    { return append([]byte(nil), c.wake...), nil }
func (c *s18bCustody) ContentKEK() ([]byte, error) { return append([]byte(nil), c.content...), nil }

// s18bRig is one machine (provisioned identity + relay + live daemon) and one phone, wired
// through the real relay -- the fixture every test below starts from.
type s18bRig struct {
	relayURL string
	stateDir string // the MACHINE's state dir
	phoneDir string // the PHONE's state dir ("app data")
	custody  *s18bCustody
	deviceID string // the paired device's registry id
	phoneRID string // the paired device's relay routing id
}

// s18bNewRig provisions a machine for remote control the way an owner does -- `swarm remote
// init --relay-url` against a real relay -- and stands up the real assembled daemon over it.
// No device is paired yet; s18bPairPhone does that.
func s18bNewRig(t *testing.T) *s18bRig {
	t.Helper()
	relayURL := os.Getenv("OPERATOR_RELAY_V2_HTTP")
	if relayURL == "" {
		t.Skip("OPERATOR_RELAY_V2_HTTP is set by the fresh-workerd operator gate")
	}
	relayURL = "ws" + strings.TrimPrefix(relayURL, "http")

	// Off the real system: the CLI's supervisor seam is faked and swarm-remote is resolved
	// from a temp dir, so `remote init`/`pair`/`revoke` never reach launchd or systemd.
	installFakeSupervisor(t)
	fakeGatewayBinaryOnPath(t)

	rig := &s18bRig{
		relayURL: relayURL,
		stateDir: shortStateDir(t),
		phoneDir: t.TempDir(),
		custody:  newS18bCustody(t),
	}
	operatorV2Identity(t, rig.stateDir)
	// The environment dialClient/EnsureDaemon and every verb read. Set BEFORE `remote init`,
	// which resolves its state dir the same way.
	sock := filepath.Join(rig.stateDir, "daemon.sock")
	t.Setenv(daemon.EnvStateDir, rig.stateDir)
	t.Setenv(daemon.EnvSocket, sock)
	t.Setenv(daemon.EnvLock, filepath.Join(rig.stateDir, "daemon.lock"))
	t.Setenv(daemon.EnvLog, filepath.Join(rig.stateDir, "daemon.log"))
	// TERM=dumb makes printPairingQR fall back to the unwrapped manual code on one line,
	// which is what s18bQRFromPairOutput reads back. The QR drawing is PB-PAIR-1's subject,
	// not this slice's.
	t.Setenv("TERM", "dumb")

	var out, errOut bytes.Buffer
	if exit := runRemote([]string{"init", "--relay-url", rig.relayURL, "--relay-namespace", "local-test"}, &out, &errOut); exit != 0 {
		t.Fatalf("swarm remote init exit = %d, want 0; stderr=%q", exit, errOut.String())
	}

	sk, err := skeleton.Serve(skeleton.Config{
		StateDir:    rig.stateDir,
		SocketPath:  sock,
		LockPath:    filepath.Join(rig.stateDir, "daemon.lock"),
		LogPath:     filepath.Join(rig.stateDir, "daemon.log"),
		MaxSessions: 4,
	})
	if err != nil {
		t.Fatalf("skeleton.Serve: %v", err)
	}
	t.Cleanup(func() { _ = sk.Close() })
	return rig
}

// s18bApp opens the phone over its state directory without connecting. A fresh v2 registry
// is intentionally unpaired; s18bPairPhone starts only after authenticated pairing commits.
func (r *s18bRig) s18bApp(t *testing.T) (*swarmmobile.App, error) {
	t.Helper()
	app, err := swarmmobile.NewApp(&swarmmobile.Config{
		StateDir: r.phoneDir, RelayURL: r.relayURL,
	}, r.custody)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = app.Close() })
	return app, nil
}

// syncBuf is a bytes.Buffer safe to read while the pairing goroutine writes it.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// s18bPairResult is one completed run of the OWNER's `swarm remote pair` verb.
type s18bPairResult struct {
	exit   int
	stdout string
	stderr string
}

// s18bPairPhone drives a COMPLETE pairing: the owner's `swarm remote pair` on one side
// (runRemotePair, with the operator's "y" on an injected stdin exactly as the pre-existing
// pair tests do) and the real swarmmobile phone on the other, over the real relay.
//
// It is the "working re-pair" half of the acceptance criterion, and it is also how the rig
// reaches the STRANDED state in the first place -- a phone can only be bricked by a fail-
// closed state if it was genuinely paired before.
func (r *s18bRig) s18bPairPhone(t *testing.T) (*swarmmobile.App, s18bPairResult) {
	t.Helper()

	var stdout, stderr syncBuf
	done := make(chan int, 1)
	go func() {
		done <- runRemotePair(nil, strings.NewReader("y\n"), &stdout, &stderr)
	}()

	qr := s18bAwaitQR(t, &stdout, &stderr)

	app, err := r.s18bApp(t)
	if err != nil {
		t.Fatalf("the phone would not construct before a pairing: %v", err)
	}
	p, err := app.BeginPairing(qr)
	if err != nil {
		t.Fatalf("App.BeginPairing: %v", err)
	}
	origin, err := p.Origin()
	if err != nil {
		t.Fatalf("Pairing.Origin: %v", err)
	}
	if err := p.ConfirmOrigin(origin); err != nil {
		t.Fatalf("Pairing.ConfirmOrigin(%q): %v", origin, err)
	}
	s18bAwaitSAS(t, p)
	if err := p.Confirm(); err != nil {
		t.Fatalf("Pairing.Confirm: %v", err)
	}

	var exit int
	select {
	case exit = <-done:
	case <-time.After(30 * time.Second):
		t.Fatalf("`swarm remote pair` never returned; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	res := s18bPairResult{exit: exit, stdout: stdout.String(), stderr: stderr.String()}
	if exit != 0 {
		t.Fatalf("`swarm remote pair` exit = %d, want 0; stdout=%q stderr=%q", exit, res.stdout, res.stderr)
	}

	// The two ends finish INDEPENDENTLY: `swarm remote pair` returns when the machine has
	// enrolled, while the phone persists its half on its own goroutine. Wait for the phone's
	// durable state before calling the pairing done, or a test that corrupts that state races
	// the write that creates it.
	s18bAwaitRestored(t, app)
	s18bAwaitPaired(t, p)
	if err := app.Start(); err != nil {
		t.Fatalf("App.Start after authenticated pairing: %v", err)
	}

	// Record the coordinates the recovery has to act on: the registry id the owner will type
	// and the relay routing id whose relay-side state has to be purged.
	r.deviceID = s18bOnlyDeviceID(t)
	r.phoneRID = r.s18bPhoneRoutingID(t, r.deviceID)
	return app, res
}

func s18bAwaitPaired(t *testing.T, p *swarmmobile.Pairing) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		state, err := p.State()
		if err != nil {
			t.Fatalf("Pairing.State while waiting for authenticated completion: %v", err)
		}
		if state == "paired" {
			return
		}
		if state != "pairing" && state != "confirming" && state != "confirm_destination" {
			t.Fatalf("pairing reached terminal state %q before authenticated completion", state)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the phone never reached authenticated pairing completion within 20s")
}

// s18bAwaitQR reads the pairing payload back off `swarm remote pair`'s own stdout -- the
// operator's channel, not a test seam -- by finding the first line the QR decoder accepts.
func s18bAwaitQR(t *testing.T, stdout, stderr *syncBuf) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		for _, line := range strings.Split(stdout.String(), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if _, err := pairing.DecodeQR(line); err == nil {
				return line
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("`swarm remote pair` printed no decodable pairing code within 20s; stdout=%q stderr=%q",
		stdout.String(), stderr.String())
	return ""
}

// s18bAwaitSAS waits for the phone to derive its six emoji, failing fast on a terminal
// pairing state so a dead handshake reports itself as what it was.
func s18bAwaitSAS(t *testing.T, p *swarmmobile.Pairing) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		sas, serr := p.SAS()
		state, sterr := p.State()
		switch {
		case serr == nil && sas != "":
			return sas
		case sterr != nil:
			t.Fatalf("Pairing.State: %v", sterr)
		case state != "pairing" && state != "confirming" && state != "confirm_destination":
			t.Fatalf("the pairing reached terminal state %q without deriving a SAS: %v", state, serr)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the phone never derived a SAS within 20s")
	return ""
}

// s18bAwaitRestored blocks until the phone's pairing is DURABLE -- StateSummary.Restored is
// derived from the pinned machine destination, so it is true only once the blob is written.
func s18bAwaitRestored(t *testing.T, app *swarmmobile.App) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		sum, err := app.StateSummary()
		if err != nil {
			t.Fatalf("StateSummary while waiting for the pairing to become durable: %v", err)
		}
		if sum.Restored && sum.EpochID != 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the phone never persisted its half of the pairing within 20s")
}

// s18bOnlyDeviceID reads the single registered device's id out of `swarm remote devices` --
// the operator's own listing, so the test identifies the stranded device exactly as the
// requirement's first step says an owner must.
func s18bOnlyDeviceID(t *testing.T) string {
	t.Helper()
	var out, errOut bytes.Buffer
	if exit := runRemote([]string{"devices"}, &out, &errOut); exit != 0 {
		t.Fatalf("`swarm remote devices` exit = %d, want 0; stderr=%q", exit, errOut.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("`swarm remote devices` listed no device; got:\n%s", out.String())
	}
	if len(lines) > 2 {
		t.Fatalf("`swarm remote devices` listed %d devices, want exactly 1; got:\n%s",
			len(lines)-1, out.String())
	}
	id := strings.Fields(lines[1])
	if len(id) == 0 {
		t.Fatalf("`swarm remote devices` row carries no device id; got:\n%s", out.String())
	}
	return id[0]
}

// s18bPhoneRoutingID is the relay mailbox the machine appends to for this handset, read from
// the OWNER's own device registry -- the machine-side view, which is the only one the
// recovery flow has once the handset is fail-closed.
func (r *s18bRig) s18bPhoneRoutingID(t *testing.T, deviceID string) string {
	t.Helper()
	rec := r.s18bDeviceRecord(t, deviceID)
	rid := string(rec.RoutingID)
	if rid == "" {
		t.Fatalf("device %s carries no relay routing id in the registry", deviceID)
	}
	return rid
}

// s18bDeviceRecord reads one registry record straight off disk. The registry is opened fresh
// each time: the daemon's own handle does not hot-reload, and a revoke run through the CLI
// mutates the file underneath it.
func (r *s18bRig) s18bDeviceRecord(t *testing.T, deviceID string) device.Record {
	t.Helper()
	reg, err := device.Open(filepath.Join(r.stateDir, "devices"))
	if err != nil {
		t.Fatalf("device.Open: %v", err)
	}
	rec, ok := reg.Get(deviceID)
	if !ok {
		t.Fatalf("device %s is not in the registry at %s", deviceID, r.stateDir)
	}
	return rec
}

// s18bCorruptPhoneState makes the phone's durable blob unreadable, which is PB-STATE-4's
// fail-closed trigger and PB-STATE-10's first step. The bytes are deliberately garbage
// rather than a truncation: an unparseable blob is the ErrCorruptState branch that fails
// Resume outright, which is the case whose advised remedy ("re-pair") is unreachable
// BECAUSE the app never constructs to offer it.
func (r *s18bRig) s18bCorruptPhoneState(t *testing.T) {
	t.Helper()
	legacyPath := filepath.Join(r.phoneDir, phonecore.StateFileName)
	if _, err := os.Lstat(legacyPath); err == nil {
		t.Fatalf("v2 fixture unexpectedly wrote state at legacy root %s", legacyPath)
	} else if !os.IsNotExist(err) {
		t.Fatalf("inspect legacy root state path %s: %v", legacyPath, err)
	}
	reg, err := phonecore.OpenMachineRegistry(r.phoneDir)
	if err != nil {
		t.Fatalf("open v2 machine registry: %v", err)
	}
	entries := reg.Entries()
	if len(entries) != 1 {
		t.Fatalf("registry entries = %+v; want exactly the paired machine", entries)
	}
	path := filepath.Join(reg.MachineDir(entries[0].ID), phonecore.StateFileName)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the phone wrote no durable state to corrupt at %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte("{not json at all"), 0o600); err != nil {
		t.Fatalf("corrupt the phone state blob: %v", err)
	}
}

// s18bClearAppData is "clear the app's data": the state directory the phone owns, gone. It
// is the ONLY act available to a user whose blob refuses to load, and the requirement's
// point is that it is not sufficient on its own -- the machine still holds the registration.
func (r *s18bRig) s18bClearAppData(t *testing.T) {
	t.Helper()
	if err := os.RemoveAll(r.phoneDir); err != nil {
		t.Fatalf("clear the phone's app data: %v", err)
	}
	if err := os.MkdirAll(r.phoneDir, 0o700); err != nil {
		t.Fatalf("recreate the phone's app data dir: %v", err)
	}
}

// s18bCommandsNamed extracts every `swarm remote <verb>` an operator could read out of a
// piece of output. It is what makes "no step requiring undocumented knowledge" an EXECUTABLE
// property: a step is discoverable only if the previous step's text names it.
//
// It is deliberately literal. A message that says "revoke it first" without naming the verb
// is not matched, because an operator cannot type it -- and that is precisely the gap this
// slice exists to close.
var s18bVerbRe = regexp.MustCompile(`swarm\s+remote\s+([a-z]+)`)

func s18bCommandsNamed(text string) map[string]bool {
	out := map[string]bool{}
	for _, m := range s18bVerbRe.FindAllStringSubmatch(text, -1) {
		out[m[1]] = true
	}
	return out
}

// s18bRequireNames fails unless text names every verb, quoting what the operator was
// actually shown -- the failure message has to be readable as "here is what they were told,
// and here is the step it does not mention".
func s18bRequireNames(t *testing.T, what, text string, verbs ...string) {
	t.Helper()
	named := s18bCommandsNamed(text)
	var missing []string
	for _, v := range verbs {
		if !named[v] {
			missing = append(missing, "swarm remote "+v)
		}
	}
	if len(missing) > 0 {
		t.Errorf("PB-STATE-10: %s does not name %s, so the operator can only reach the next step "+
			"by knowing it already. What they were shown:\n%s",
			what, strings.Join(missing, " or "), text)
	}
}

// ---- step 1: corruption -> fail closed, with a remedy the user can act on ----------

// TestPBSTATE10_CorruptStateFailsClosedAndNamesTheOwnerSideRecovery.
//
// The phone's blob is unreadable, so phonecore refuses and NewApp returns the refusal. That
// half already holds and is asserted first, because a corrupt blob that LOADED would be a
// PB-STATE-4 regression and this test would otherwise pass through it silently.
//
// The half that does not hold is the message. ErrCorruptState's text is the ONLY thing a
// stranded user has: the app did not construct, so there is no screen, no error class and no
// remedy string -- App.ErrorClass is a method on an App that does not exist. The user is left
// with "clear the app's data, then pair again", and pairing again is refused by the machine
// while this device is registered. The remedy has to name the owner-side flow that makes the
// re-pair possible, by the commands that perform it.
func TestPBSTATE10_CorruptStateFailsClosedAndNamesTheOwnerSideRecovery(t *testing.T) {
	rig := s18bNewRig(t)
	app, _ := rig.s18bPairPhone(t)
	if err := app.Close(); err != nil {
		t.Fatalf("App.Close before the corruption: %v", err)
	}
	rig.s18bCorruptPhoneState(t)

	_, err := rig.s18bApp(t)
	if err == nil {
		t.Fatal("PB-STATE-4 regression: the phone constructed over a corrupt durable blob")
	}
	msg := err.Error()

	// THE CLASS COMES FIRST, because it decides which screen the user ever sees. gomobile
	// flattens this into a message and Kotlin routes on the class token it carries; today that
	// token is ErrClassInternal, whose row in mobile/error_taxonomy.tsv reads "Never the user's
	// fault and never has a user action" with remedy report_bug. A corrupt durable blob is a
	// state the owner CAN recover from, so routing it to "report a bug" is the brick expressed
	// as a screen: the one thing the user is told to do is the one thing that cannot help.
	// ErrClassUnknown (remedy none) is the same failure with a different label.
	for _, dead := range []string{swarmmobile.ErrClassInternal, swarmmobile.ErrClassUnknown} {
		if strings.HasPrefix(msg, dead+":") {
			t.Errorf("PB-STATE-10: the corrupt-state refusal is classed %q, whose remedy in "+
				"mobile/error_taxonomy.tsv is not something a user can act on. A fail-closed state "+
				"with an owner-side recovery needs a class that carries that recovery. Got: %s", dead, msg)
		}
	}

	// The user's own act, and the only one available to them.
	if !strings.Contains(strings.ToLower(msg), "clear") {
		t.Errorf("PB-STATE-10: the corrupt-state refusal does not tell the user to clear the app's "+
			"data, which is the only act that removes the blob refusing to load. Got: %s", msg)
	}
	// And the act that is NOT theirs. Without it the advice is "pair again" against a machine
	// that will refuse, which is the brick this requirement is named for.
	s18bRequireNames(t, "the phone's corrupt-state refusal", msg, "devices", "revoke", "pair")
}

// ---- step 2: the refusal that blocks the re-pair must name its own remedy ----------

// TestPBSTATE10_ThePairRefusalNamesHowToFindAndRevokeTheStrandedDevice.
//
// This is the wall. The owner, told by the handset to pair again, runs `swarm remote pair`
// and is refused -- correctly, per PB-KEY-3 and the single-device policy. What they are told is
// "a device is already paired; revoke it first", which names no command
// and no way to learn the device id the command needs. An operator who does not already know
// the verb has nowhere to go, and the requirement's acceptance clause forbids exactly that.
func TestPBSTATE10_ThePairRefusalNamesHowToFindAndRevokeTheStrandedDevice(t *testing.T) {
	rig := s18bNewRig(t)
	app, _ := rig.s18bPairPhone(t)
	if err := app.Close(); err != nil {
		t.Fatalf("App.Close: %v", err)
	}
	rig.s18bCorruptPhoneState(t)

	var stdout, stderr syncBuf
	exit := runRemotePair(nil, strings.NewReader("y\n"), &stdout, &stderr)
	if exit == 0 {
		t.Fatal("`swarm remote pair` succeeded while a device was still registered; " +
			"the single-device policy requires it to be refused (PB-KEY-3)")
	}
	shown := stdout.String() + stderr.String()
	s18bRequireNames(t, "the already-paired pair refusal", shown, "devices", "revoke")
}

// ---- step 3: the revoke must name the step that finishes the recovery --------------

// TestPBSTATE10_RevokeNamesTheRePairThatFinishesTheRecovery.
//
// `swarm remote revoke <id>` prints "revoked device <id>" and stops. In every other context
// that is the whole job; in THIS one the revoke is the middle of a four-step recovery, and an
// operator who stops here has a machine with no device and a handset that still cannot pair
// because nobody told them the flow had another step.
func TestPBSTATE10_RevokeNamesTheRePairThatFinishesTheRecovery(t *testing.T) {
	rig := s18bNewRig(t)
	app, _ := rig.s18bPairPhone(t)
	if err := app.Close(); err != nil {
		t.Fatalf("App.Close: %v", err)
	}
	rig.s18bCorruptPhoneState(t)

	var out, errOut bytes.Buffer
	if exit := runRemote([]string{"revoke", rig.deviceID}, &out, &errOut); exit != 0 {
		t.Fatalf("`swarm remote revoke %s` exit = %d, want 0; stderr=%q", rig.deviceID, exit, errOut.String())
	}
	s18bRequireNames(t, "the revoke confirmation", out.String()+errOut.String(), "pair")

	// The message is not the only subject: a revoke that ADVERTISED the next step without
	// performing its own would satisfy the line above and leave the machine exactly as blocked.
	var listOut, listErr bytes.Buffer
	if exit := runRemote([]string{"devices"}, &listOut, &listErr); exit != 0 {
		t.Fatalf("`swarm remote devices` after the revoke exit = %d, want 0; stderr=%q", exit, listErr.String())
	}
	if strings.Contains(listOut.String(), rig.deviceID) {
		t.Errorf("PB-STATE-10: `swarm remote revoke %s` reported success and the device is still "+
			"registered, so the re-pair it points at is still refused. Listing:\n%s",
			rig.deviceID, listOut.String())
	}
}

// ---- step 3b: "purge machine and relay state" --------------------------------------

// TestPBSTATE10_RevokePurgesTheMachineSideOutboundCustody.
//
// The MACHINE half of "purge machine and relay state", at the one piece of durable machine
// state a revoke provably leaves behind and that the next pairing then acts on.
//
// PB-GW-8's outbox (<stateDir>/remote/outbound-journal.outbox) holds RESERVED-BUT-UNCOMMITTED
// entries as the EXACT SEALED BYTES, by contract: "a replay re-appends Envelope verbatim;
// re-sealing would mint a fresh nonce". A stranded phone stops acking, so entries accumulate.
// The revoke rotates the machine epoch, so every one of those envelopes is now sealed under a
// key no future device will ever hold -- and the gateway that comes up for the RE-PAIRED phone
// replays them verbatim into its mailbox, where they can never be opened. `swarm remote
// revoke` today touches the registry, the epoch and the grant sidecar, and nothing under
// <stateDir>/remote at all.
//
// Asserted at the outbox itself rather than at a phone-visible symptom, because the phone has
// several independent reasons to survive a few unopenable frames and the requirement is about
// the state, not about which of those masks it.
func TestPBSTATE10_RevokePurgesTheMachineSideOutboundCustody(t *testing.T) {
	rig := s18bNewRig(t)
	app, _ := rig.s18bPairPhone(t)
	if err := app.Close(); err != nil {
		t.Fatalf("App.Close: %v", err)
	}

	// The gateway's own file, at the production path cmd/swarm-remote resolves, carrying the
	// undelivered custody a stranded phone leaves behind.
	outboxPath := filepath.Join(rig.stateDir, "remote", "outbound-journal.outbox")
	ob, err := remotegw.OpenOutbox(outboxPath)
	if err != nil {
		t.Fatalf("remotegw.OpenOutbox: %v", err)
	}
	if err := ob.Reserve(1, []byte("sealed-under-the-revoked-epoch")); err != nil {
		t.Fatalf("Outbox.Reserve: %v", err)
	}
	if pending, perr := ob.Pending(); perr != nil || len(pending) == 0 {
		t.Fatalf("the fixture left no pending outbox entry to purge (pending=%d err=%v); a purge "+
			"assertion over an empty outbox would pass no matter what production does", len(pending), perr)
	}
	rig.s18bCorruptPhoneState(t)

	var out, errOut bytes.Buffer
	if exit := runRemote([]string{"revoke", rig.deviceID}, &out, &errOut); exit != 0 {
		t.Fatalf("`swarm remote revoke %s` exit = %d, want 0; stderr=%q", rig.deviceID, exit, errOut.String())
	}

	// Re-opened from disk: the assertion is about what the NEXT gateway process reads, not
	// about the handle this test happens to hold.
	after, err := remotegw.OpenOutbox(outboxPath)
	if err != nil {
		t.Fatalf("remotegw.OpenOutbox after the revoke: %v", err)
	}
	pending, err := after.Pending()
	if err != nil {
		t.Fatalf("Outbox.Pending after the revoke: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("PB-STATE-10: after `swarm remote revoke` the machine still holds %d undelivered "+
			"outbound entr(ies) sealed under the epoch the revoke rotated away. The gateway that "+
			"serves the RE-PAIRED phone replays them verbatim into its mailbox, where nothing can "+
			"open them. \"Purge machine state\" is the requirement's third step and no CLI path "+
			"touches %s", len(pending), outboxPath)
	}
}

// ---- step 3c: the purge must not become a permanent ban ----------------------------

// TestPBSTATE10_TheSameHandsetRecoversWithoutAFactoryReset.
//
// The relay-v2 cleanup retires one pairing generation, not the phone's permanent key. This
// fence proves a handset whose local key survives can receive a fresh generation.
//
// The pairing here reuses the phone's EXISTING state directory (no clear), so device.key and
// therefore the relay-auth key are the ones the revoke acted on.
func TestPBSTATE10_TheSameHandsetRecoversWithoutAFactoryReset(t *testing.T) {
	rig := s18bNewRig(t)
	app, _ := rig.s18bPairPhone(t)
	strandedID, strandedRID := rig.deviceID, rig.phoneRID
	if err := app.Close(); err != nil {
		t.Fatalf("App.Close: %v", err)
	}

	var out, errOut bytes.Buffer
	if exit := runRemote([]string{"revoke", strandedID}, &out, &errOut); exit != 0 {
		t.Fatalf("`swarm remote revoke %s` exit = %d, want 0; stderr=%q", strandedID, exit, errOut.String())
	}

	// The same handset, same device.key, same relay routing id, pairing again.
	rig.deviceID, rig.phoneRID = "", ""
	recovered, _ := rig.s18bPairPhone(t)
	if rig.phoneRID != strandedRID {
		t.Fatalf("the fixture re-paired a DIFFERENT handset (routing id %s -> %s), so this test "+
			"would pass whatever the relay does to the original one", strandedRID, rig.phoneRID)
	}
	sum, err := recovered.StateSummary()
	if err != nil {
		t.Fatalf("StateSummary after the same-handset recovery: %v", err)
	}
	if !sum.Restored || sum.EpochID == 0 {
		t.Errorf("PB-STATE-10: the same handset re-paired but is not usable: restored=%v epoch=%d. "+
			"Leftover machine state the new session cannot get past lands here", sum.Restored, sum.EpochID)
	}

	// Pairing completion alone is insufficient: the replacement generation must authenticate
	// and reach the relay-v2 stream.
	s18bAwaitOnline(t, recovered)
}

// s18bRevokedIsTerminalAfter is how long "revoked" has to PERSIST before this helper is
// entitled to call it terminal. It mirrors swarmmobile's unexported pairingRevokeGrace (30 s)
// plus a margin for the 250 ms redial and a loaded machine.
//
// IT IS NOT A TUNING KNOB, it is the correction of a wrong assumption. This helper used to
// fail-fast the instant it observed "revoked", which reads as fail-fast discipline and is
// actually a race: ADR-007 B23(b) DELIBERATELY HOLDS that state between retries while the
// re-armed generation waits for the machine's authorize, precisely so nothing hides behind a
// spinner. So "revoked" is a legitimate transient during the recovery this test drives, and an
// independent reviewer measured the old helper failing 2 runs in 10 against CORRECT code --
// with a message that then misdiagnosed it ("the revoked bucket is never cleared": it had been
// cleared, the poll caught the held state). A test that is wrong 20% of the time about the
// thing it exists to prove is worse than no test, because its failures get explained away.
//
// WHAT WAITING THE WINDOW OUT DOES NOT FIX, said plainly. The reviewer also measured the
// DELETION of the grace window escaping this test 2 runs in 5, and it still does -- re-measured
// at 3 failures in 5 after this change. That is not a defect in the helper: when the machine's
// authorize happens to land before the phone's first post-pairing dial, the phone never needs
// the grace and the recovery genuinely succeeds without it. An end-to-end test that drives both
// ends through their real verbs cannot order that race, so it can only ever SAMPLE it. The
// deterministic half is mobile/conformance's
// TestPBSTATE10_ThePostPairingGraceWindowSurvivesADialThatLosesTheRace, which HOLDS the
// authorize until the phone has already been refused and so catches the deletion every run.
// This helper's job is only to stop lying in the other direction.
const s18bRevokedIsTerminalAfter = 45 * time.Second

// s18bAwaitOnline polls the phone's PB-APP-8 connection state until it is online. "revoked" is
// terminal only once it has PERSISTED past the post-pairing grace window (see above).
func s18bAwaitOnline(t *testing.T, app *swarmmobile.App) {
	t.Helper()
	deadline := time.Now().Add(s18bRevokedIsTerminalAfter + 15*time.Second)
	var last string
	var revokedSince time.Time
	for time.Now().Before(deadline) {
		state, err := app.ConnectionState()
		if err != nil {
			t.Fatalf("App.ConnectionState: %v", err)
		}
		last = state
		switch state {
		case "online":
			return
		case "revoked":
			if revokedSince.IsZero() {
				revokedSince = time.Now()
			}
			if held := time.Since(revokedSince); held > s18bRevokedIsTerminalAfter {
				t.Fatalf("PB-STATE-10: the recovered handset has reported REVOKED continuously for "+
					"%s -- past the post-pairing grace window, so this is the relay's settled answer "+
					"and not the race B23(b) covers.\nEither the `revoked` bucket store.revokeAndPurge "+
					"writes was never cleared (nothing cleared it before ADR-007 B22, and after B24 "+
					"only the routing id that PLACED the ban can clear it -- so a pair issued over a "+
					"different relay identity than the revoke leaves it standing), or the pairing did "+
					"not re-arm the transport at all. Either way this handset can never reach the "+
					"relay again and the recovery has traded one brick for another", held.Round(time.Second))
			}
		default:
			revokedSince = time.Time{}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the recovered handset never reached the relay within %s (last connection state %q)",
		s18bRevokedIsTerminalAfter+15*time.Second, last)
}

// ---- the acceptance criterion: the whole chain, closed under what was shown --------

// TestPBSTATE10_TheRecoveryChainIsClosedUnderWhatTheOperatorWasTold.
//
// The requirement's acceptance criterion, driven end to end and asserted as a CLOSURE:
// corruption -> fail-closed -> owner-side recovery -> working re-pair, where every command
// run is one the previous step NAMED. The operator in this test is given nothing but the
// text the product printed; each step's assertion is "the next command appears in what I was
// just shown", and the run ends with a phone that is actually paired again.
//
// Written as one test on purpose. Each link is pinned separately above so a RED run says
// which link is missing; this one says whether the chain exists at all, which is the thing
// the acceptance criterion asks and which no per-link test can answer.
//
// ITS SCOPE, STATED SO NOTHING ELSE IS READ INTO IT. This test would pass against a change
// that only rewrote the messages and purged nothing: it clears the phone's app data, so the
// recovered handset comes back on a NEW relay routing id and cannot observe the stranded
// one's leftover state at all. That is not a hole in the chain, it is the boundary of what a
// wiped handset can see -- the two purges are owned by
// services/relay/test/protocol.mjs (unacknowledged custody and on-disk purge) and
// TestPBSTATE10_RevokePurgesTheMachineSideOutboundCustody, and the same-routing-id case by
// TestPBSTATE10_TheSameHandsetRecoversWithoutAFactoryReset. All four have to be green.
func TestPBSTATE10_TheRecoveryChainIsClosedUnderWhatTheOperatorWasTold(t *testing.T) {
	rig := s18bNewRig(t)
	app, _ := rig.s18bPairPhone(t)
	strandedID := rig.deviceID
	if err := app.Close(); err != nil {
		t.Fatalf("App.Close: %v", err)
	}

	// 1. CORRUPTION -> FAIL CLOSED. Everything the operator knows starts here.
	rig.s18bCorruptPhoneState(t)
	_, err := rig.s18bApp(t)
	if err == nil {
		t.Fatal("PB-STATE-4 regression: the phone constructed over a corrupt durable blob")
	}
	told := err.Error()

	// 2. LIST / IDENTIFY. The operator may only run a command their phone named.
	s18bRequireNames(t, "the phone's fail-closed refusal", told, "devices")
	var listOut, listErr bytes.Buffer
	if exit := runRemote([]string{"devices"}, &listOut, &listErr); exit != 0 {
		t.Fatalf("`swarm remote devices` exit = %d, want 0; stderr=%q", exit, listErr.String())
	}
	if !strings.Contains(listOut.String(), strandedID) {
		t.Fatalf("`swarm remote devices` does not identify the stranded device %s; got:\n%s",
			strandedID, listOut.String())
	}
	told = listOut.String() + listErr.String()
	outboxPath := filepath.Join(rig.stateDir, "remote", "outbound-journal.outbox")
	outbox, err := remotegw.OpenOutbox(outboxPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := outbox.Reserve(1, []byte("sealed-under-the-revoked-epoch")); err != nil {
		t.Fatal(err)
	}

	// 3. REVOKE / UNREGISTER, named by the listing the operator just read.
	s18bRequireNames(t, "the device listing", told, "revoke")
	var revOut, revErr bytes.Buffer
	if exit := runRemote([]string{"revoke", strandedID}, &revOut, &revErr); exit != 0 {
		t.Fatalf("`swarm remote revoke %s` exit = %d, want 0; stderr=%q", strandedID, exit, revErr.String())
	}
	told = revOut.String() + revErr.String()
	after, err := remotegw.OpenOutbox(outboxPath)
	if err != nil {
		t.Fatal(err)
	}
	if pending, err := after.Pending(); err != nil || len(pending) != 0 {
		t.Fatalf("outbound custody after revoke = %d pending, %v", len(pending), err)
	}

	// 4. RE-PAIR, named by the revoke. The phone's app data is cleared first because that is
	// what a corrupt blob leaves the user -- and clearing it is NOT what unblocks the machine,
	// which is the whole point of the three steps above.
	s18bRequireNames(t, "the revoke confirmation", told, "pair")
	rig.s18bClearAppData(t)
	rig.deviceID, rig.phoneRID = "", ""
	recovered, _ := rig.s18bPairPhone(t)

	// AND THE PHONE WORKS. A pairing that reported success but left no durable destination is
	// the half-paired state PB-PAIR-4 forbids, and it would satisfy every assertion above.
	sum, err := recovered.StateSummary()
	if err != nil {
		t.Fatalf("StateSummary after the recovery: %v", err)
	}
	if !sum.Restored || sum.EpochID == 0 {
		t.Errorf("PB-STATE-10: the recovery finished but the phone is not usable: restored=%v "+
			"epoch=%d. The chain has to end in a WORKING re-pair, not merely an accepted one",
			sum.Restored, sum.EpochID)
	}
	s18bAwaitOnline(t, recovered)
}
