package protocol

// Item 1.2 (agents-tracker-mlm), T1.2.b — the full real writer<->reader pair: a real
// shim serving a 200x50 heavily-styled grid (>1 MiB snapshot) attached through the
// real daemon reader. Before the fix the shim's single oversized TSnapshot frame is
// rejected by wire.WriteFrame and the daemon's readSnapshot hangs until its deadline,
// so Attach fails; after the fix the snapshot is chunked shim->daemon, reassembled
// under the size cap, and the attach completes quickly with a byte-identical snapshot.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/vt"
	"github.com/Nathandela/swarm/internal/wire"
)

// styledScript builds a fakeagent script that paints every cell of a rows x cols grid
// with rich attributes (bold/italic/underline/reverse + a truecolor bg, set once per
// line) plus a truecolor fg DISTINCT from its neighbors, so the emulator snapshot
// exceeds wire.MaxFrame and must be chunked. Per-cell-distinct styling is required
// post item 4.3 run-merging: a uniform-per-row grid would coalesce to one run per row
// and drop below MaxFrame, whereas a per-cell-varying grid is merging's worst case and
// keeps the snapshot oversized. Only the fg varies per cell (attrs+bg persist), which
// keeps the script small so the grid paints quickly — the attach loop below reads live
// frames only implicitly, so a slow paint would flood an oversized styled grid's
// output and trip the S9 wedged-subscriber eviction before the grid finishes. It then
// idles so the grid stays populated for the attach.
func styledScript(rows, cols int) string {
	var b strings.Builder
	for y := 0; y < rows; y++ {
		b.WriteString("print \x1b[1;3;4;7;48;2;40;64;120m") // rich attrs + bg once per line
		for x := 0; x < cols; x++ {
			fg := (x*3 + y*5) % 256 // odd x-step -> adjacent cells never share fg
			fmt.Fprintf(&b, "\x1b[38;2;%d;%d;%dm", fg, 128, 255-fg)
			b.WriteByte(byte('A' + (x+y)%26))
		}
		b.WriteByte('\n')
	}
	b.WriteString("idle 60s\n")
	return b.String()
}

// launchStyledSession launches a fake agent that paints a cols x rows styled grid.
func launchStyledSession(t *testing.T, d *daemon.Daemon, cols, rows int) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "styled.txt")
	if err := os.WriteFile(script, []byte(styledScript(rows, cols)), 0o600); err != nil {
		t.Fatalf("write styled script: %v", err)
	}
	m, err := d.Launch(daemon.LaunchSpec{
		AgentType: "fake",
		Argv:      []string{fakeAgentBin, script},
		Cwd:       t.TempDir(),
		ClientEnv: []string{"PATH=" + os.Getenv("PATH")},
		Cols:      cols,
		Rows:      rows,
	})
	if err != nil {
		t.Fatalf("daemon.Launch: %v", err)
	}
	t.Cleanup(func() {
		if m.ShimPID > 0 {
			_ = syscall.Kill(m.ShimPID, syscall.SIGTERM)
		}
	})
}

func TestIntegration_ChunkedOversizedSnapshot(t *testing.T) {
	// Shorten the total snapshot read deadline so the pre-fix failure (the daemon
	// waiting on a TSnapshot the shim can never send in one frame) surfaces fast
	// instead of taking the full 10s.
	old := shimAttachTimeout
	shimAttachTimeout = 3 * time.Second
	t.Cleanup(func() { shimAttachTimeout = old })

	d := realDaemon(t)
	launchStyledSession(t, d, 200, 50)

	sock := tmpSock(t)
	srv, err := Serve(FromDaemon(d), sock)
	if err != nil {
		t.Fatalf("Serve(FromDaemon): %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	c := dialClient(t, sock, []string{"attach"})
	var id string
	deadline := time.Now().Add(launchTimeout)
	for time.Now().Before(deadline) {
		views, err := c.List()
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(views) == 1 {
			id = views[0].ID
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if id == "" {
		t.Fatalf("session never appeared within %s", launchTimeout)
	}

	// Poll until the grid is fully painted (snapshot exceeds one frame), then time a
	// fresh attach: it must reassemble the chunked snapshot well under 2s (T1.2.b).
	var snap []byte
	var attachDur time.Duration
	gridDeadline := time.Now().Add(8 * time.Second)
	for {
		start := time.Now()
		a, err := c.Attach(id)
		if err != nil {
			if time.Now().After(gridDeadline) {
				t.Fatalf("Attach of oversized-grid session never succeeded: %v", err)
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		attachDur = time.Since(start)
		snap = a.Snapshot()
		if len(snap) > wire.MaxFrame-1 {
			break
		}
		if time.Now().After(gridDeadline) {
			t.Fatalf("grid never grew past one frame (last snapshot %d bytes)", len(snap))
		}
		time.Sleep(100 * time.Millisecond)
	}

	if len(snap) <= wire.MaxFrame-1 {
		t.Fatalf("snapshot %d bytes did not exceed MaxFrame-1 (%d); chunking not exercised", len(snap), wire.MaxFrame-1)
	}
	if attachDur > 2*time.Second {
		t.Errorf("attach of a %d-byte snapshot took %s, want < 2s (T1.2.b)", len(snap), attachDur)
	}
	sn, err := vt.DecodeSnapshot(snap)
	if err != nil {
		t.Fatalf("reassembled snapshot does not decode: %v", err)
	}
	if sn.Cols != 200 || sn.Rows != 50 {
		t.Errorf("reassembled grid is %dx%d, want 200x50", sn.Cols, sn.Rows)
	}

	// Byte-identical across attaches: the grid is idle, so a re-attach reassembles the
	// exact same bytes (no chunk boundary corruption).
	b, err := c.Attach(id)
	if err != nil {
		t.Fatalf("re-attach: %v", err)
	}
	if string(b.Snapshot()) != string(snap) {
		t.Errorf("re-attach snapshot (%d bytes) not byte-identical to first (%d bytes)", len(b.Snapshot()), len(snap))
	}
}
