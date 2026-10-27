package protocol

import (
	"errors"
	"strings"
	"testing"
	"time"
)

type slowNativeStop struct{ *stubDaemon }

func (s *slowNativeStop) Kill(string) error {
	time.Sleep(clientTimeout + 100*time.Millisecond)
	return errors.New("native stop requested; completion remains unknown")
}

func (s *slowNativeStop) Delete(id string) error { return s.Kill(id) }

func TestNativeStopWaitsForUncertainReplyAndKeepsConnectionUsable(t *testing.T) {
	if testing.Short() {
		t.Skip("qualifies the real client deadline")
	}
	for _, op := range []string{OpKill, OpDelete} {
		t.Run(op, func(t *testing.T) {
			t.Parallel()
			c, err := Dial(serveOwnerAPI(t, &slowNativeStop{newStubDaemon()}), nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = c.Close() })
			id, _, err := c.Launch(validLaunch(t))
			if err != nil {
				t.Fatal(err)
			}
			stop := c.Kill
			if op == OpDelete {
				stop = c.Delete
			}
			if err = stop(id); err == nil || !strings.Contains(err.Error(), "completion remains unknown") {
				t.Fatalf("lost native stop result: %v", err)
			}
			if _, err = c.List(); err != nil {
				t.Fatalf("native stop closed the public control connection: %v", err)
			}
		})
	}
}
