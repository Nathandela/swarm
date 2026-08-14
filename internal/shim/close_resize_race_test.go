package shim

// Wave R0, work package "shim-race": pins the close-vs-resize race. shim.go's
// finalization calls ptmx.Close() (shim.go:212) BEFORE srv.shutdown joins every
// serveConn handler, so a live client resize still in flight (server.go:249,
// s.resize -> pty.Setsize -> os.File.Fd) can run concurrently with that Close.
// CI observed this as a WARNING: DATA RACE (write os.File.Close at shim.go:212;
// read os.File.Fd via resize at server.go:249) on TestF5_OutOfRangeResizeIgnored.
// This test forces the same two operations to overlap deterministically, on the
// real server.resize code path, with no synchronization between them — exactly
// the ordering gap production is missing.

import (
	"sync"
	"testing"

	"github.com/Nathandela/swarm/internal/vt"
	"github.com/creack/pty"
)

func TestCloseVsResizeRace(t *testing.T) {
	emu := vt.NewEmulator(80, 24)
	t.Cleanup(func() { _ = emu.Close() })
	h := &hub{emu: emu, tr: newHubTranscript(t), metrics: &Metrics{}}
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	t.Cleanup(func() { _ = tty.Close() })
	s := &server{hub: h, ptmx: ptmx}

	// Release both goroutines from the same starting line, with nothing linking
	// the resize's Fd read to the Close's write: this is the same absence of
	// ordering that lets shim.go's teardown race a live client resize in
	// production (shim.go calls ptmx.Close() without first joining serveConn).
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		s.resize(100, 30) // server.go:249 -> pty.Setsize -> ptmx.Fd()
	}()
	go func() {
		defer wg.Done()
		<-start
		_ = ptmx.Close() // mirrors shim.go:212, run before any handler join
	}()
	close(start)
	wg.Wait()
}
