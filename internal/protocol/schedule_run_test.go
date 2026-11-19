package protocol

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

type scheduleRunStub struct {
	*stubDaemon
	requests chan [2]string
}

func (*scheduleRunStub) ScheduleTick() error { return nil }
func (*scheduleRunStub) ScheduleComplete(string, schema.ScheduleCompletion) error {
	return nil
}
func (s *scheduleRunStub) ScheduleRun(jobID, operationID string) (string, error) {
	s.requests <- [2]string{jobID, operationID}
	return "occurrence", nil
}

func TestScheduleRunOperationIDWireRetry(t *testing.T) {
	b := &scheduleRunStub{stubDaemon: newStubDaemon(), requests: make(chan [2]string, 3)}
	sock := serveOwnerAPI(t, b)
	legacy := rawDial(t, sock)
	hello := legacy.hello(Version, []string{CapSchedule})
	legacy.writeControl(Control{Op: OpScheduleRun, EndpointID: hello.EndpointID, ScheduleID: "job"})
	if resp := legacy.readControl(); resp.Op != OpError || len(b.requests) != 0 {
		t.Fatalf("legacy request reached Run now: %+v", resp)
	}
	legacyClient := dialClient(t, sock, []string{CapSchedule})
	if _, err := legacyClient.ScheduleRun("job", "01ARZ3NDEKTSV4RRFFQ69G5FAV"); err == nil || len(b.requests) != 0 {
		t.Fatalf("client used an unnegotiated Run now: %v", err)
	}

	caps := []string{CapSchedule, CapScheduleRunID}
	raw := rawDial(t, sock)
	hello = raw.hello(Version, caps)
	if !slices.Contains(hello.Capabilities, CapScheduleRunID) {
		t.Fatal("owner socket did not negotiate schedule operation IDs")
	}
	raw.writeControl(Control{Op: OpScheduleRun, EndpointID: hello.EndpointID, ScheduleID: "job"})
	if resp := raw.readControl(); resp.Op != OpError || len(b.requests) != 0 {
		t.Fatalf("request with missing ID reached Run now: %+v", resp)
	}
	const operationID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	want := [2]string{"job", operationID}
	raw.writeControl(Control{Op: OpScheduleRun, EndpointID: hello.EndpointID, ScheduleID: "job", OperationID: operationID})
	checkRequest := func() {
		t.Helper()
		select {
		case got := <-b.requests:
			if got != want {
				t.Fatalf("request = %q, want %q", got, want)
			}
		case <-time.After(recvTimeout):
			t.Fatal("request never reached backend")
		}
	}
	checkRequest()
	_ = raw.conn.Close() // Lose the original reply.
	reconnected := dialClient(t, sock, caps)
	if got, err := reconnected.ScheduleRun("job", operationID); err != nil || got != "occurrence" {
		t.Fatalf("replayed run = %q, %v", got, err)
	}
	checkRequest()
}

func TestScheduleCompletionSocketRejectsOwnerOperations(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "sw-receipt-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "receipt.sock")
	b := &scheduleRunStub{stubDaemon: newStubDaemon(), requests: make(chan [2]string, 1)}
	ready := make(chan struct{})
	close(ready)
	server, err := ServeScheduleCompletion(b, sock, "local", ready)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	stat, err := os.Stat(sock)
	if err != nil || stat.Mode().Perm() != 0600 {
		t.Fatalf("completion socket must be owner-only: %v, %v", stat, err)
	}
	for _, op := range []string{OpList, OpScheduleRun, OpScheduleTick, OpOwnerLaunch} {
		t.Run(op, func(t *testing.T) {
			raw := rawDial(t, sock)
			hello := raw.hello(Version, []string{CapSchedule, CapOwnerLaunch, CapScheduleRunID})
			if !slices.Equal(hello.Capabilities, []string{CapSchedule}) {
				t.Fatalf("unexpected completion capabilities: %v", hello.Capabilities)
			}
			raw.writeControl(Control{Op: op, EndpointID: hello.EndpointID})
			if resp := raw.readControl(); resp.Op != OpError {
				t.Fatalf("received response to forbidden operation %q: %+v", op, resp)
			}
		})
	}
	if len(b.requests) != 0 {
		t.Fatal("completion listener dispatched an owner operation")
	}
}
