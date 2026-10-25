package skeleton

import (
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/charmbracelet/x/term"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/vt"
)

func launchTrustSnapshot(t *testing.T) *vt.Snap {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("../adapter/claude/testdata/trustdialog", "trust-dialog-2.1.310.snap.json"))
	if err != nil {
		t.Fatal(err)
	}
	snap, err := vt.DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func TestLaunchGateFirstObservationDoesNotWrite(t *testing.T) {
	sk := assemble(t)
	m := launchFakeSized(t, sk, "idle 600s\n", 150, 44)
	r := newGateRig(t, sk.core, m, launchTrustSnapshot(t))
	r.poll(t)
	if len(r.up.attempts) != 0 || r.phase() != launchGateInitial {
		t.Fatalf("first valid trust observation wrote %q; phase=%v", r.up.attempts, r.phase())
	}
}

// This child is a raw PTY fake CLI, never an installed provider.
func TestLaunchGateRawCLIHelper(t *testing.T) {
	if len(os.Args) < 3 || os.Args[len(os.Args)-2] != "--launchgate-raw-helper" {
		return
	}
	state, err := term.MakeRaw(os.Stdin.Fd())
	if err != nil {
		os.Exit(2)
	}
	defer func() { _ = term.Restore(os.Stdin.Fd(), state) }()
	raw, err := os.ReadFile(os.Args[len(os.Args)-1])
	if err != nil {
		os.Exit(3)
	}
	snap, err := vt.DecodeSnapshot(raw)
	if err != nil {
		os.Exit(4)
	}
	_, _ = os.Stdout.Write(vt.RenderSnapshotClipped(snap, 0, 0))
	painted := time.Now()
	buffer := make([]byte, 32)
	n, err := os.Stdin.Read(buffer)
	if err != nil || string(buffer[:n]) != "\x1b[B" || time.Since(painted) < 150*time.Millisecond {
		_, _ = os.Stdout.Write([]byte("\x1b[HRAW_GATE_REJECTED_SELECTION"))
		return
	}
	_, _ = os.Stdout.Write(vt.RenderSnapshotClipped(trustSelected(snap), 0, 0))
	selected := time.Now()
	n, err = os.Stdin.Read(buffer)
	if err != nil || string(buffer[:n]) != "\r" || time.Since(selected) < 150*time.Millisecond {
		_, _ = os.Stdout.Write([]byte("\x1b[HRAW_GATE_REJECTED_CONFIRMATION"))
		return
	}
	_, _ = os.Stdout.Write([]byte("\x1b[2J\x1b[HRAW_GATE_ACCEPTED"))
	time.Sleep(15 * time.Second)
}

func TestLaunchGateRawCLITimingThroughSharedTap(t *testing.T) {
	sk := assemble(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := filepath.Abs("../adapter/claude/testdata/trustdialog/trust-dialog-2.1.310.snap.json")
	if err != nil {
		t.Fatal(err)
	}
	meta, err := sk.core.Launch(daemon.LaunchSpec{AgentType: "fake", Argv: []string{executable, "-test.run=^TestLaunchGateRawCLIHelper$", "--", "--launchgate-raw-helper", fixture}, Cwd: t.TempDir(), ClientEnv: []string{"PATH=" + os.Getenv("PATH")}, Cols: 150, Rows: 44})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sk.core.Kill(meta.ID) })
	d := &Daemon{core: sk.core, closing: make(chan struct{})}
	d.initInteractions()
	d.setAdapterForTest(claudeResolver)
	d.capStore.instances = map[string]string{meta.ID: "raw-instance"}
	d.capStore.incarnations = map[string]sessionIncarnation{meta.ID: {ShimPID: meta.ShimPID, ShimStartTime: meta.ShimStartTime}}
	mgr := newTapManager(func(id string) (protocol.SessionStream, error) {
		conn, caps, e := sk.core.DialSession(id)
		if e != nil {
			return nil, e
		}
		return protocol.NewShimStream(conn, caps)
	})
	mgr.gateIdentity = d.cachedLaunchGateIdentity
	mgr.foreignWrite = d.abandonLaunchGateBinding
	mgr.gateFault = d.abandonLaunchGateBinding
	d.api = &coreAPI{core: sk.core, tap: mgr}
	anchor, err := mgr.subscribe(meta.ID, readOnly)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = anchor.Close() }()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		raw, e := d.api.SampleSnapshot(meta.ID)
		if e != nil {
			t.Fatal(e)
		}
		snap, e := vt.DecodeSnapshot(raw)
		if e != nil {
			t.Fatal(e)
		}
		var screen strings.Builder
		for _, line := range snap.Lines {
			for _, run := range line.Runs {
				screen.WriteString(run.Text)
			}
		}
		if strings.Contains(screen.String(), "RAW_GATE_REJECTED") {
			t.Fatal(screen.String())
		}
		if strings.Contains(screen.String(), "RAW_GATE_ACCEPTED") {
			return
		}
		identity, _ := d.cachedLaunchGateIdentity(meta.ID)
		d.answerLaunchGate(meta.ID, snap, identity)
		time.Sleep(gridPoll)
	}
	t.Fatal("raw fake CLI did not accept staged trust input")
}
