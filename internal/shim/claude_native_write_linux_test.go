package shim

import (
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/shimwire"
	"golang.org/x/sys/unix"
)

func TestClaudeNativeA2StopBetweenActualPartialWrites(t *testing.T) {
	f := claudeA2PipeFixture(t)
	f.ready(t, "idle")
	// Private pipe only: guarantee a real partial owner write without making the
	// request/operation larger or extending its unchanged three-second deadline.
	fd, err := f.input.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var capErr error
	if err := fd.Control(func(n uintptr) { _, capErr = unix.FcntlInt(n, unix.F_SETPIPE_SZ, 4096) }); err != nil || capErr != nil {
		t.Fatalf("partial-write setup: %v %v", err, capErr)
	}
	req := f.request(t, 0, 5)
	req.Text = strings.Repeat("\x01", 16<<10)
	done := make(chan shimwire.ClaudeMessageResult, 1)
	joined := false
	go func() { done <- f.c.writeMessage(req) }()
	t.Cleanup(func() {
		if !joined {
			f.c.server.onSignal(shimwire.SigKill)
			_ = f.input.Close()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Error("partial writer did not join")
			}
		}
	})
	ready, err := claudePipePoll(f.input, unix.POLLIN, 1000)
	if err != nil || ready&unix.POLLIN == 0 {
		t.Fatalf("no positive actual partial input witness: %d %v", ready, err)
	}
	// Do not drain/free pipe space before the Stop latch. Writer still owns
	// admission and is waiting for POLLOUT; independent Stop must not borrow it.
	stopped := make(chan struct{})
	go func() { f.c.server.onSignal(shimwire.SigKill); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop waited for write admission")
	}
	select {
	case got := <-done:
		joined = true
		if got.Status != "uncertain" {
			t.Fatalf("partial Stop outcome=%s", got.Status)
		}
	case <-time.After(time.Second):
		t.Fatal("partial writer ignored accepted Stop")
	}
	prefix := f.available(t)
	if len(prefix) == 0 || len(prefix) > 4096 {
		t.Fatal("partial byte witness was absent or grew after Stop")
	}
	if p := f.c.primarySnapshot(); p == nil || p.OwnerRevision != 0 || p.Healthy {
		t.Fatal("uncertain prefix gained delivered credit or remained writable")
	}
	if got := f.c.writeMessage(req); got.Status != "refused" || len(f.available(t)) != 0 {
		t.Fatal("stopped partial owner input was resent")
	}
}
