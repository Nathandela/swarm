package protocol

import (
	"net"
	"slices"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

type openCodeObservationStub struct{ *stubDaemon }

func (*openCodeObservationStub) OpenCodeObservation(string) (OpenCodeObservationView, bool) {
	return OpenCodeObservationView{Version: 1, RootID: "ses_native", Generation: "generation", Busy: true, BusyKnown: true, Attention: true}, true
}

func TestOpenCodeObservationRequiresOwnerNegotiationForListAndEvents(t *testing.T) {
	backend := &openCodeObservationStub{newStubDaemon()}
	meta := persist.Meta{ID: "native", AgentType: "opencode", Status: status.Status{Process: status.ProcessRunning, Turn: status.TurnActive, Interaction: status.InteractionPermission, Attention: true}}
	backend.setMetas(meta)
	socket, _ := serveFix(t, backend)
	modern := dialClient(t, socket, []string{CapSubscribe, CapOpenCodeObservation})
	legacy := dialClient(t, socket, []string{CapSubscribe})
	for _, test := range []struct {
		client *Client
		want   bool
	}{{modern, true}, {legacy, false}} {
		rows, err := test.client.List()
		if err != nil || len(rows) != 1 {
			t.Fatalf("list: %v %d", err, len(rows))
		}
		if (rows[0].OpenCode != nil) != test.want {
			t.Fatalf("negotiated native list presence=%v want=%v", rows[0].OpenCode != nil, test.want)
		}
	}
	newEvents, err := modern.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	oldEvents, err := legacy.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	backend.pushStatus(meta)
	for _, test := range []struct {
		events <-chan Event
		want   bool
	}{{newEvents, true}, {oldEvents, false}} {
		event, ok := recvEvent(t, test.events, time.Second)
		if !ok {
			t.Fatal("native event missing")
		}
		if (event.Session.OpenCode != nil) != test.want {
			t.Fatal("shared fanout leaked or dropped negotiated native view")
		}
	}
}

func TestOpenCodeObservationNeverAdvertisedOrPublishedRemotely(t *testing.T) {
	backend := &openCodeObservationStub{newStubDaemon()}
	server := &Server{d: backend, remoteTier: true}
	if slices.Contains(server.supportedCaps(), CapOpenCodeObservation) {
		t.Fatal("remote tier advertised owner native observation")
	}
	row := server.stampView("ep", persist.Meta{ID: "native"}, status.GroupWorking, false, false, time.Time{})
	if row.OpenCode != nil {
		t.Fatal("remote tier received owner-only native observations")
	}
	if slices.Contains((&Server{d: newStubDaemon()}).supportedCaps(), CapOpenCodeObservation) {
		t.Fatal("missing native source advertised observations")
	}
}

func TestOpenCodeObservationSharedFanoutSeparatesNegotiatedClients(t *testing.T) {
	server := NewServer(&openCodeObservationStub{newStubDaemon()}, "stable")
	defer server.Close()
	var clients []*clientConn
	for _, caps := range [][]string{{CapOpenCodeObservation}, nil} {
		a, b := net.Pipe()
		t.Cleanup(func() { a.Close(); b.Close() })
		cc := &clientConn{endpointID: "stable", caps: caps, eventQ: make(chan []byte, 1), conn: a, done: make(chan struct{})}
		clients = append(clients, cc)
		server.subMu.Lock()
		server.subs[cc] = struct{}{}
		server.subMu.Unlock()
	}
	server.distribute(persist.Meta{ID: "native"})
	for i, cc := range clients {
		frame, err := DecodeControl(<-cc.eventQ)
		if err != nil {
			t.Fatal(err)
		}
		if frame.Session == nil || (frame.Session.OpenCode != nil) != (i == 0) {
			t.Fatalf("shared native observation leaked or lost: client %d", i)
		}
	}
}
