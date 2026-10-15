package protocol

import (
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
)

// noticeDaemon launches like the stub and attaches a launch notice to the meta.
type noticeDaemon struct {
	*stubDaemon
	notice string
}

func (d noticeDaemon) Launch(spec daemon.LaunchSpec) (persist.Meta, error) {
	m, err := d.stubDaemon.Launch(spec)
	m.LaunchNotice = d.notice
	return m, err
}

// The launch reply carries the daemon's launch notice (an owner fallback) to the client.
func TestClient_LaunchWithNoticeCarriesTheDaemonNotice(t *testing.T) {
	sock := tmpSock(t)
	srv, err := Serve(noticeDaemon{stubDaemon: newStubDaemon(), notice: "launched on the owner login"}, sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	c := dialClient(t, sock, nil)
	id, _, err := c.Launch(validLaunch(t))
	if err != nil || id == "" {
		t.Fatalf("launch: id=%q err=%v", id, err)
	}
	if notice := c.TakeLaunchNotice(id); notice != "launched on the owner login" {
		t.Fatalf("notice = %q, want the daemon's launch notice", notice)
	}
	if again := c.TakeLaunchNotice(id); again != "" {
		t.Fatalf("notice taken twice: %q", again)
	}
}
