package conformance_test

// FAILING-FIRST (TDD RED, GG-5) test for slice S19's second production hole: PB-APP-6's
// remote launch carried NO TERMINAL GEOMETRY, and the daemon refuses one without it
// ("launch: cols/rows out of range", internal/protocol/server.go handleLaunch, which rejects
// Cols < 1). swarmmobile.LaunchSpec had no Cols/Rows at all, so App.Launch built a
// schema.LaunchReq with Cols=0, Rows=0 and every remote launch was refused at the machine.
//
// WHY THE PHONE SUPPLIES IT AND NOT THE GATEWAY. Not because the signature forces it --
// schema.LaunchContentHash deliberately EXCLUDES Cols/Rows as "cosmetic terminal dimensions",
// so a gateway could legally fill them without breaking the device signature. The reason is
// that only the phone knows the grid: the geometry is the size of the terminal view the user
// is about to watch this session in, and a gateway-side default would render every remote
// launch at a width nobody chose and re-wrap the first screen the moment the phone resized.
//
// WHY NO SHIPPED TEST COULD HAVE CAUGHT IT. PB-APP-6's evidence is facade-level: this suite's
// machine is a mailbox reader, and remotegw's launch tests forward a LaunchReq built in-test.
// Neither ever reaches handleLaunch's range check, so a launch spec that no daemon would
// accept satisfied both. It is the class the S19 brief names (v): a fence guarding a path
// production does not take.

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	swarmmobile "github.com/Nathandela/swarm/mobile"
)

// s19MaxDim mirrors internal/protocol's unexported maxDim (server.go), the daemon's upper
// bound on a launch grid. Restated because package protocol does not export it and a geometry
// the daemon refuses at either end is the same refused launch.
const s19MaxDim = 1000

// TestS19_ARemoteLaunchCarriesATerminalGeometryTheMachineAccepts.
//
// Both halves matter. An unset geometry must still produce a launch the daemon will run --
// the Android launch sheet has no terminal view to measure when the session does not exist
// yet -- and an explicit one must ride VERBATIM, or the field is decoration and the caller's
// grid is silently discarded.
func TestS19_ARemoteLaunchCarriesATerminalGeometryTheMachineAccepts(t *testing.T) {
	h := newHarness(t)
	h.PushReconcile()
	eventually(t, "reconcile never adopted", func() bool {
		s, err := h.App.StateSummary()
		return err == nil && s.Reconciled
	})

	if _, err := h.App.Launch(&swarmmobile.LaunchSpec{Agent: "fake", Cwd: t.TempDir()}); err != nil {
		t.Fatalf("App.Launch: %v", err)
	}
	cmd := h.AwaitCommand(protocol.ActionLaunch)
	if cmd.Launch == nil {
		t.Fatal("the sealed launch carries no spec in-envelope")
	}
	if cmd.Launch.Cols < 1 || cmd.Launch.Cols > s19MaxDim || cmd.Launch.Rows < 1 || cmd.Launch.Rows > s19MaxDim {
		t.Fatalf("a launch spec with no geometry sealed cols=%d rows=%d, which the daemon refuses "+
			"(handleLaunch: cols/rows out of range). PB-APP-6's launch never reaches a PTY",
			cmd.Launch.Cols, cmd.Launch.Rows)
	}

	const wantCols, wantRows = 132, 43
	if _, err := h.App.Launch(&swarmmobile.LaunchSpec{
		Agent: "fake", Cwd: t.TempDir(), Cols: wantCols, Rows: wantRows,
	}); err != nil {
		t.Fatalf("App.Launch with an explicit geometry: %v", err)
	}
	explicit := s19AwaitNth(t, h, protocol.ActionLaunch, 2).Launch
	if explicit == nil {
		t.Fatal("the second sealed launch carries no spec in-envelope")
	}
	if explicit.Cols == cmd.Launch.Cols && explicit.Rows == cmd.Launch.Rows {
		t.Fatalf("the second launch sealed the same geometry as the defaulted one (cols=%d rows=%d), "+
			"so LaunchSpec.Cols/Rows are ignored and the caller's grid is discarded",
			cmd.Launch.Cols, cmd.Launch.Rows)
	}
	if explicit.Cols != wantCols || explicit.Rows != wantRows {
		t.Fatalf("an explicit geometry sealed as cols=%d rows=%d, want %dx%d",
			explicit.Cols, explicit.Rows, wantCols, wantRows)
	}
}

// s19AwaitNth returns the nth (1-based) command of an action the machine received.
// harness.AwaitCommand returns the FIRST match, which is the wrong frame for any assertion
// about how two successive commands of the same verb differ.
func s19AwaitNth(t *testing.T, h *harness, action string, n int) schema.RemoteCommand {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.Drain()
		var seen []schema.RemoteCommand
		h.mu.Lock()
		for _, c := range h.Commands {
			if c.Action == action {
				seen = append(seen, c)
			}
		}
		h.mu.Unlock()
		if len(seen) >= n {
			return seen[n-1]
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("only saw fewer than %d %q commands within 5s", n, action)
	return schema.RemoteCommand{}
}
