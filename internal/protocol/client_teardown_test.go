package protocol

// agents-tracker-1uq: Client.eventsCh is created once by Subscribe() and was never
// closed on teardown, so a caller blocked reading it (tui.waitForEvent) hung
// forever after ANY connection loss — explicit Close(), pump eviction, or a daemon
// crash/restart all look the same from here: the read loop dies and nothing ever
// sends on or closes eventsCh again. These tests pin both teardown paths.

import (
	"testing"
	"time"
)

// TestClientClose_ClosesEventsChannel proves an explicit Close() closes the
// channel Subscribe returned, so a caller blocked reading it observes a closed
// channel instead of hanging forever.
func TestClientClose_ClosesEventsChannel(t *testing.T) {
	sock := serveStub(t, newStubDaemon())
	c := dialClient(t, sock, nil)

	ch, err := c.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("channel delivered a value after Close(); want a closed channel")
		}
	case <-time.After(recvTimeout):
		t.Fatal("Events channel did not close within timeout after Close()")
	}
}

// TestClientTeardown_PeerCloseClosesEventsChannel proves the readLoop-death path
// closes eventsCh too, without the client ever calling Close() itself. This is
// what pump eviction and a daemon crash/restart look like from the client's
// perspective: the SERVER end of the connection dies.
func TestClientTeardown_PeerCloseClosesEventsChannel(t *testing.T) {
	stub := newStubDaemon()
	sock := tmpSock(t)
	srv, err := Serve(stub, sock)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	defer srv.Close()

	c, err := Dial(sock, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()

	ch, err := c.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if err := srv.Close(); err != nil {
		t.Fatalf("Server Close: %v", err)
	}

	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("channel delivered a value after peer close; want a closed channel")
		}
	case <-time.After(recvTimeout):
		t.Fatal("Events channel did not close within timeout after peer close")
	}
}
