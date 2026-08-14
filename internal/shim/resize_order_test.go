package shim

import (
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/vt"
	"github.com/creack/pty"
)

func TestServerResizeSerializesPTYNotificationAndEmulatorWithFeed(t *testing.T) {
	emu := vt.NewEmulator(80, 94)
	t.Cleanup(func() { _ = emu.Close() })
	metrics := &Metrics{}
	h := &hub{emu: emu, tr: newHubTranscript(t), metrics: metrics}
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatalf("pty.Open: %v", err)
	}
	t.Cleanup(func() {
		_ = ptmx.Close()
		_ = tty.Close()
	})
	s := &server{hub: h, ptmx: ptmx}

	hookEntered := make(chan struct{})
	releaseHook := make(chan struct{})
	testHookAfterPTYResize = func() {
		close(hookEntered)
		<-releaseHook
	}
	t.Cleanup(func() { testHookAfterPTYResize = nil })

	resizeDone := make(chan struct{})
	go func() {
		s.resize(80, 45)
		close(resizeDone)
	}()
	select {
	case <-hookEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("resize did not reach post-PTY test seam")
	}

	feedDone := make(chan struct{})
	go func() {
		h.feed([]byte("BLOCKED-UNTIL-EMULATOR-RESIZE"))
		close(feedDone)
	}()
	select {
	case <-feedDone:
		t.Fatal("hub.feed interleaved between PTY notification and emulator resize")
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseHook)
	select {
	case <-resizeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("resize did not complete after releasing test seam")
	}
	select {
	case <-feedDone:
	case <-time.After(2 * time.Second):
		t.Fatal("hub.feed remained blocked after resize completed")
	}

	snap, err := emu.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	decoded, err := vt.DecodeSnapshot(snap)
	if err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if decoded.Cols != 80 || decoded.Rows != 45 {
		t.Fatalf("emulator dims = %dx%d, want 80x45", decoded.Cols, decoded.Rows)
	}
}

func TestHubFeedContainsAndCountsParserFault(t *testing.T) {
	emu := vt.NewEmulator(80, 45)
	t.Cleanup(func() { _ = emu.Close() })
	metrics := &Metrics{}
	h := &hub{emu: emu, tr: newHubTranscript(t), metrics: metrics}

	h.feed([]byte("\x1b[1;94r\x1bM"))
	if got := metrics.VTParserFaults.Load(); got != 1 {
		t.Fatalf("VTParserFaults = %d, want 1", got)
	}
	h.feed([]byte("\x1b[r\x1b[HRECOVERED"))

	snap, err := emu.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	decoded, err := vt.DecodeSnapshot(snap)
	if err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if got := strings.Join(vt.SnapText(decoded), "\n"); !strings.Contains(got, "RECOVERED") {
		t.Fatalf("shim emulator did not recover after parser fault:\n%s", got)
	}
}
