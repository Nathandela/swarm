package protocol

import (
	"errors"
	"sync"
	"testing"

	"github.com/Nathandela/swarm/internal/status"
)

func TestManualInputGateCannotAuthorizeDaemonSendInput(t *testing.T) {
	d := newSendInputDaemon()
	d.setMetas(statusMeta("sess1", status.TurnIdle, status.InteractionPermission))
	socket := tmpSock(t)
	srv, err := Serve(d, socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	srv.SetNewTurnGateFunc(func(string) error { return errors.New("typed turn refused") })
	srv.SetManualInputGateFunc(func(string) error { return nil })
	client, err := Dial(socket, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	for _, req := range []SendInputReq{{Text: "y"}, {Key: "enter"}} {
		if err := srv.SendInput("sess1", req); err == nil {
			t.Fatal("manual callback authorized daemon input")
		}
		if err := client.SendInput(client.endpointID+"/sess1", req); err != nil {
			t.Fatalf("authenticated owner input refused: %v", err)
		}
	}
}

func TestManualInputGateRechecksUnderWriteLocks(t *testing.T) {
	d := newSendInputDaemon()
	d.setMetas(statusMeta("sess1", status.TurnIdle, status.InteractionNone))
	socket := tmpSock(t)
	srv, err := Serve(d, socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	calls := 0
	srv.SetManualInputGateFunc(func(string) error {
		calls++
		if calls == 2 {
			return errors.New("custody changed")
		}
		return nil
	})
	client, err := Dial(socket, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if err := client.SendInput(client.endpointID+"/sess1", SendInputReq{Text: "y"}); err == nil {
		t.Fatal("manual origin skipped final prewrite gate")
	}
	if calls != 2 || d.attachCount() != 0 {
		t.Fatal("custody refusal occurred after opening input")
	}
}

func TestInputOriginGateRechecksBeforePaste(t *testing.T) {
	for _, owner := range []bool{false, true} {
		origin := "daemon"
		if owner {
			origin = "owner"
		}
		t.Run(origin, func(t *testing.T) {
			d := &pastingDaemon{stubDaemon: newStubDaemon()}
			d.setMetas(statusMeta("sess1", status.TurnIdle, status.InteractionNone))
			socket := tmpSock(t)
			srv, err := Serve(d, socket)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = srv.Close() })
			var mu sync.Mutex
			calls := 0
			gate := func(string) error {
				mu.Lock()
				defer mu.Unlock()
				calls++
				if calls == 2 {
					return errors.New("owner custody changed before write")
				}
				return nil
			}
			if owner {
				srv.SetManualInputGateFunc(gate)
			} else {
				srv.SetNewTurnGateFunc(gate)
			}
			req := SendInputReq{Text: "message", Submit: true}
			if owner {
				client, err := Dial(socket, nil)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = client.Close() })
				err = client.SendInput(client.endpointID+"/sess1", req)
				if err == nil {
					t.Fatal("owner paste skipped final custody gate")
				}
			} else if err := srv.SendInput("sess1", req); err == nil {
				t.Fatal("daemon paste skipped final custody gate")
			}
			mu.Lock()
			checked := calls
			mu.Unlock()
			d.mu.Lock()
			attached := len(d.opened)
			d.mu.Unlock()
			if checked != 2 || attached != 0 {
				t.Fatalf("stale custody reached input: checks=%d attaches=%d", checked, attached)
			}
		})
	}
}
