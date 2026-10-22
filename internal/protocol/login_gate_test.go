package protocol

import (
	"errors"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/status"
)

func TestNewTurnGatePreservesCancellationAndControllerAccess(t *testing.T) {
	d := newSendInputDaemon()
	d.setMetas(statusMeta("sess1", status.TurnIdle, status.InteractionNone))
	sock := tmpSock(t)
	srv, err := Serve(d, sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	srv.SetNewTurnGateFunc(func(string) error { return errors.New("login switch waiting") })
	if err := srv.SendInput("sess1", SendInputReq{Text: "new work", Submit: true}); err == nil || !strings.Contains(err.Error(), "waiting") {
		t.Fatalf("new turn crossed provider barrier: %v", err)
	}
	if d.attachCount() != 0 {
		t.Fatal("refused turn opened a stream")
	}
	for _, key := range []string{"ctrl-c", "esc"} {
		if err := srv.SendInput("sess1", SendInputReq{Key: key}); err != nil {
			t.Fatalf("cancel %s blocked: %v", key, err)
		}
	}
	c, err := Dial(sock, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if _, err := c.Attach(c.endpointID + "/sess1"); err != nil {
		t.Fatalf("controller cannot resolve a held interaction: %v", err)
	}
}
