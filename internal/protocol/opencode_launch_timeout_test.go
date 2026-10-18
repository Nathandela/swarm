package protocol

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
)

type slowNativeLaunch struct { *stubDaemon }

func (s *slowNativeLaunch) Launch(spec daemon.LaunchSpec) (persist.Meta, error) {
	time.Sleep(clientTimeout + 100*time.Millisecond)
	return s.stubDaemon.Launch(spec)
}

func TestOpenCodeLaunchWaitsForNativeBindingBeyondOrdinaryRequestDeadline(t *testing.T) {
	if testing.Short() { t.Skip("qualifies the real client deadline") }
	s := &slowNativeLaunch{newStubDaemon()}
	c, err := Dial(serveOwnerAPI(t, s), nil)
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = c.Close() })
	req := validLaunch(t)
	req.Agent = "opencode"
	if id, _, err := c.Launch(req); err != nil || id == "" {
		t.Fatalf("native launch lost its binding reply during startup: id%q err%v", id, err)
	}
}
