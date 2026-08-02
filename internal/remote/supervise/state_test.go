package supervise

// FAILING-FIRST tests for slice S4 — PB-LIFE-2 and PB-LIFE-3: the three supervision
// states, and the reason a naive "restart on exit" unit would be a permanent crash loop.
//
// THE TRAP THIS SLICE EXISTS TO AVOID. cmd/swarm-remote/config.go's resolveGatewayParams
// fails unless EXACTLY ONE device is paired ("want exactly one paired device, got %d").
// After `swarm remote revoke` there are ZERO, so the gateway can never start again. A
// unit that restarts the process on every exit therefore restarts it forever, every
// interval, for as long as the machine is up -- burning a process spawn per tick and
// filling logs with a failure that is not a failure at all. PB-LIFE-3 names the three
// states that resolve it:
//
//	(a) NO PAIRED DEVICE  -> quiescent. NOT an error. The gateway exits ExitQuiescent and
//	                         neither unit restarts it.
//	(b) PAIRED            -> active. The gateway runs and grant delivery completes.
//	(c) REVOKED           -> the running gateway exits (remotegw.ErrDeviceRevoked), the
//	                         unit returns to quiescent, and ONLY a later successful
//	                         re-pair starts a gateway again -- necessarily a fresh
//	                         process, which re-resolves params and so runs under the NEW
//	                         epoch, never the pre-revoke one.
//
// INTENDED PRODUCTION SURFACE (RED — none of it exists yet; GREEN implements it):
//
//	type State int
//	const (
//		StateQuiescent State = iota // installed, not running, and NOT a failure
//		StateActive                 // exactly one device paired; the gateway runs
//		StateFailed                 // exited for a reason that is not quiescence
//	)
//	func (s State) String() string
//
//	// Desired reports the state the unit must be in for a given paired-device count.
//	// cmd/swarm-remote's zero-device path consults it so the CLI, the unit and the
//	// gateway share ONE definition of quiescence.
//	func Desired(deviceCount int) State
//
//	const (
//		ExitQuiescent = 0 // "nothing to serve"; deliberately a SUCCESS status
//		ExitFailure   = 1
//	)
//
//	// ErrQuiescent marks a gateway outcome that is not a failure. cmd/swarm-remote wraps
//	// BOTH quiescent outcomes in it: the zero-paired-device provisioning failure at
//	// startup and remotegw.ErrDeviceRevoked at runtime.
//	var ErrQuiescent = errors.New("supervise: nothing to serve; gateway quiescent")
//
//	func ExitCodeFor(err error) int
//	func ShouldRestart(exitCode int) bool
//
//	// Supervisor is the seam between the owner-invoked CLI and the host init system.
//	// ADR-007 D5 forbids the daemon spawning the gateway, so the only caller is
//	// cmd/swarm. Tests substitute a fake and never touch launchd/systemd.
//	type Supervisor interface {
//		Install(Spec) error // write/refresh the unit; idempotent; files only
//		Ensure() error      // make the gateway running now; idempotent, never a restart
//		Stop() error        // return the unit to quiescent
//	}
//
//	// ErrNotInstalled is returned by Ensure/Stop when no unit has been installed for
//	// this state dir. It is a HINT, not a failure: `swarm remote pair` reports it and
//	// still exits 0.
//	var ErrNotInstalled = errors.New("supervise: no gateway unit installed")
//
//	func Host(stateDir string) (Supervisor, error)

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/remote/machineid"
)

// TestDesired_ThreeStates pins PB-LIFE-3's mapping from paired-device count to unit
// state. Zero devices is quiescent and is NOT an error -- that is the whole correction.
// More than one device is quiescent too: resolveGatewayParams refuses that count as
// hard as it refuses zero, so a unit that tried to run would loop just the same.
func TestDesired_ThreeStates(t *testing.T) {
	cases := []struct {
		devices int
		want    State
	}{
		{0, StateQuiescent},
		{1, StateActive},
		{2, StateQuiescent},
		{7, StateQuiescent},
	}
	for _, tc := range cases {
		if got := Desired(tc.devices); got != tc.want {
			t.Errorf("Desired(%d) = %v, want %v", tc.devices, got, tc.want)
		}
	}
}

// TestExitCodeFor_QuiescenceIsNotAFailure pins the exit-status contract the units'
// restart policy is written against.
//
// ExitQuiescent is 0 ON PURPOSE. launchd's KeepAlive has no per-exit-code list -- the
// only status that stops it restarting is a successful one -- so a nonzero "quiescent"
// code could be expressed in systemd (SuccessExitStatus=) and NOT in launchd, and the
// two unit types would then disagree about the single most important case. A zero status
// makes ONE policy (restart on failure only) correct on both.
func TestExitCodeFor_QuiescenceIsNotAFailure(t *testing.T) {
	if ExitQuiescent != 0 {
		t.Errorf("ExitQuiescent = %d, want 0: launchd cannot express a non-restarting nonzero exit", ExitQuiescent)
	}
	if ExitFailure == ExitQuiescent {
		t.Fatalf("ExitFailure = ExitQuiescent = %d; a real failure must be distinguishable", ExitFailure)
	}

	if got := ExitCodeFor(nil); got != ExitQuiescent {
		t.Errorf("ExitCodeFor(nil) = %d, want %d", got, ExitQuiescent)
	}
	// Both quiescent outcomes reach ExitCodeFor WRAPPED, exactly as the gateway will
	// wrap them -- the zero-device provisioning refusal and the runtime revoke.
	noDevice := fmt.Errorf("resolveGatewayParams: want exactly one paired device, got 0: %w", ErrQuiescent)
	if got := ExitCodeFor(noDevice); got != ExitQuiescent {
		t.Errorf("ExitCodeFor(no paired device) = %d, want %d (PB-LIFE-3(a) is not a failure)", got, ExitQuiescent)
	}
	revoked := fmt.Errorf("remotegw: paired device revoked; gateway exiting: %w", ErrQuiescent)
	if got := ExitCodeFor(revoked); got != ExitQuiescent {
		t.Errorf("ExitCodeFor(revoked) = %d, want %d (PB-LIFE-3(c) returns to quiescent)", got, ExitQuiescent)
	}
	// A genuine failure must stay a failure, or restart-on-failure protects nothing.
	if got := ExitCodeFor(errors.New("dial relay: connection refused")); got != ExitFailure {
		t.Errorf("ExitCodeFor(relay dial failure) = %d, want %d", got, ExitFailure)
	}

	if ShouldRestart(ExitQuiescent) {
		t.Error("ShouldRestart(ExitQuiescent) = true; this is the crash loop PB-LIFE-3 forbids")
	}
	if !ShouldRestart(ExitFailure) {
		t.Error("ShouldRestart(ExitFailure) = false; a crashed gateway must come back (PB-LIFE-1)")
	}
}

// fakeSupervisor is the test double for the Supervisor seam. It records an ORDERED call
// log, because the ordering is the assertion that matters in the revoke sequence: a Stop
// must land between the two Ensures, or a gateway from before the revoke could still be
// serving the phone under the pre-rotation epoch.
type fakeSupervisor struct {
	calls     []string
	installed bool
	running   bool
	ensureErr error
}

func (f *fakeSupervisor) Install(Spec) error {
	f.calls = append(f.calls, "install")
	f.installed = true
	return nil
}

func (f *fakeSupervisor) Ensure() error {
	f.calls = append(f.calls, "ensure")
	if f.ensureErr != nil {
		return f.ensureErr
	}
	f.running = true
	return nil
}

func (f *fakeSupervisor) Stop() error {
	f.calls = append(f.calls, "stop")
	f.running = false
	return nil
}

func (f *fakeSupervisor) starts() int {
	n := 0
	for _, c := range f.calls {
		if c == "ensure" {
			n++
		}
	}
	return n
}

// superviseTick models ONE supervisor decision point: given the paired-device count and
// the status the gateway last exited with, decide whether a gateway should be running
// now. It is the documented behavior of both unit types expressed in Go -- launchd's
// KeepAlive{SuccessfulExit:false} and systemd's Restart=on-failure -- so the sequence
// test below can drive many ticks without a real init system.
//
// EITHER clause starts a gateway: an active desired state does (RunAtLoad at login, the
// CLI's Ensure on a successful pair), and so does a FAILURE exit, which both unit types
// restart without consulting the device count at all. That second clause is the whole
// reason quiescence has to be a SUCCESS status: were it not, each zero-device tick below
// would spawn a gateway.
func superviseTick(sup Supervisor, deviceCount int, lastExit int) error {
	if Desired(deviceCount) == StateActive || ShouldRestart(lastExit) {
		return sup.Ensure()
	}
	return sup.Stop()
}

// TestSupervisionSequence_RevokeQuiescenceRepair_NoCrashLoop drives PB-LIFE-3 end to end
// through the seam: paired -> revoked -> zero-device quiescence held across many
// supervisor ticks -> re-pair. It asserts the two things that make the model correct:
//
//  1. NO CRASH LOOP. Once the device is revoked, a hundred supervisor ticks produce zero
//     further starts. This is the assertion that fails today: with restart-on-any-exit
//     and a gateway that exits 1 on zero devices, starts would grow without bound.
//  2. THE NEW EPOCH. The re-pair start is a FRESH process -- a Stop separates it from the
//     pre-revoke Ensure -- so it re-resolves gatewayParams and picks up the rotated epoch
//     key rather than continuing to seal frames under the revoked device's epoch.
func TestSupervisionSequence_RevokeQuiescenceRepair_NoCrashLoop(t *testing.T) {
	f := &fakeSupervisor{}
	if err := f.Install(Spec{}); err != nil {
		t.Fatalf("Install error = %v", err)
	}

	// (b) PAIRED: one device, the gateway has never exited. It must be running.
	if err := superviseTick(f, 1, ExitQuiescent); err != nil {
		t.Fatalf("tick(paired) error = %v", err)
	}
	if !f.running {
		t.Fatal("with one paired device the gateway is not running; PB-LIFE-3(b) wants it active")
	}
	startsWhilePaired := f.starts()

	// (c) REVOKED: the running gateway sees its device gone and exits. That exit is
	// quiescence, not a failure, so nothing restarts it -- and the unit stops.
	revokeExit := ExitCodeFor(fmt.Errorf("remotegw: paired device revoked: %w", ErrQuiescent))
	if err := superviseTick(f, 0, revokeExit); err != nil {
		t.Fatalf("tick(revoked) error = %v", err)
	}
	if f.running {
		t.Error("gateway still running after a revoke; a stale process would keep serving under the pre-rotation epoch")
	}

	// (a) ZERO DEVICES: hold. Every subsequent supervisor tick must be a no-op. This is
	// the crash-loop assertion.
	for i := 0; i < 100; i++ {
		if err := superviseTick(f, 0, ExitQuiescent); err != nil {
			t.Fatalf("tick(quiescent, i=%d) error = %v; zero devices is NOT a failure", i, err)
		}
	}
	if got := f.starts(); got != startsWhilePaired {
		t.Fatalf("gateway started %d times across 100 zero-device ticks (was %d before the revoke); PB-LIFE-3(a) requires a stable quiescent state, not a crash loop", got-startsWhilePaired, startsWhilePaired)
	}
	if f.running {
		t.Error("gateway running with zero paired devices; resolveGatewayParams would refuse to start it anyway")
	}

	// RE-PAIR: a successful pair is the ONLY thing that activates a gateway again.
	if err := superviseTick(f, 1, ExitQuiescent); err != nil {
		t.Fatalf("tick(re-paired) error = %v", err)
	}
	if !f.running {
		t.Fatal("gateway not running after a re-pair; PB-LIFE-3(c) requires the new pairing to activate it")
	}
	if got := f.starts(); got != startsWhilePaired+1 {
		t.Errorf("starts after re-pair = %d, want %d (exactly one new gateway)", got, startsWhilePaired+1)
	}

	// The ordering that proves the re-paired gateway is a NEW process under the NEW epoch.
	got := strings.Join(f.calls, ",")
	first := strings.Index(got, "ensure")
	stop := strings.Index(got, "stop")
	last := strings.LastIndex(got, "ensure")
	if !(first < stop && stop < last) {
		t.Errorf("call order = %q; want ensure ... stop ... ensure, so no pre-revoke gateway survives into the new epoch", got)
	}
}

// TestHostEnsure_WithoutAnInstalledUnitDoesNotTouchTheInitSystem is the safety property
// that keeps every OTHER test in this tree off the real machine. `swarm remote pair` will
// call Ensure on every successful pair, including in tests that never ran `swarm remote
// init`. With no unit installed for the given state dir, Ensure must return
// ErrNotInstalled WITHOUT shelling out to launchctl/systemctl -- which is also the right
// behavior for a real operator who paired before installing (they get a hint, not a
// mysterious launchctl error).
func TestHostEnsure_WithoutAnInstalledUnitDoesNotTouchTheInitSystem(t *testing.T) {
	dir := t.TempDir()
	sup, err := Host(dir)
	if err != nil {
		t.Fatalf("Host(%q) error = %v", dir, err)
	}
	if err := sup.Ensure(); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("Ensure() with no installed unit = %v, want ErrNotInstalled", err)
	}
	if err := sup.Stop(); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("Stop() with no installed unit = %v, want ErrNotInstalled", err)
	}
}

// TestGateway_ExitsQuiescentWithNoPairedDevice is PB-LIFE-3(a) against the REAL binary,
// not a model: it builds cmd/swarm-remote, points it at a fully provisioned state dir
// with an EMPTY device registry, and asserts the process exits ExitQuiescent.
//
// Today it exits 1 (resolveGatewayParams refuses the zero count), which under any
// restart-on-failure unit is the permanent crash loop. The exit status must become
// quiescent while STILL saying why on stderr -- silent success would hide a machine whose
// gateway is doing nothing.
//
// Hermetic by construction: a temp state dir, no relay, no daemon socket. The binary
// fails to resolve params long before it dials anything.
func TestGateway_ExitsQuiescentWithNoPairedDevice(t *testing.T) {
	bin := buildGateway(t)
	dir := provisionZeroDeviceStateDir(t)

	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), "SWARM_DAEMON_STATE="+dir)
	out, err := cmd.CombinedOutput()

	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run swarm-remote: %v\n%s", err, out)
	}
	if code != ExitQuiescent {
		t.Errorf("swarm-remote with zero paired devices exit = %d, want ExitQuiescent (%d).\n"+
			"A nonzero status makes the unit restart forever, because a revoke leaves exactly this state.\n%s",
			code, ExitQuiescent, out)
	}
	if !strings.Contains(strings.ToLower(string(out)), "paired device") {
		t.Errorf("swarm-remote said nothing about the missing pairing; a quiescent exit must still be legible.\ngot: %s", out)
	}
}

// buildGateway compiles cmd/swarm-remote into a temp dir. CGO_ENABLED=0 mirrors the
// release build (PB-OPS-4), so this also catches a gateway that stops being statically
// buildable.
func buildGateway(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "swarm-remote")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/Nathandela/swarm/cmd/swarm-remote")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build cmd/swarm-remote: %v\n%s", err, out)
	}
	return bin
}

// provisionZeroDeviceStateDir builds the state dir of a machine that ran `swarm remote
// init` and then revoked its only device: a real machine identity, a relay URL, and no
// devices file. Every check in resolveGatewayParams before the device count therefore
// passes, so the exit status under test is unambiguously about the zero count.
func provisionZeroDeviceStateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	remoteDir := filepath.Join(dir, "remote")
	if err := os.MkdirAll(remoteDir, 0o700); err != nil {
		t.Fatalf("mkdir remote dir: %v", err)
	}
	id, err := machineid.Generate("supervise-test-host")
	if err != nil {
		t.Fatalf("generate machine identity: %v", err)
	}
	if err := id.Save(filepath.Join(remoteDir, "machine.key")); err != nil {
		t.Fatalf("save machine identity: %v", err)
	}
	if err := os.WriteFile(filepath.Join(remoteDir, "relay.json"), []byte(`{"relay_url":"wss://relay.invalid/ws"}`), 0o600); err != nil {
		t.Fatalf("write relay.json: %v", err)
	}
	return dir
}
