package protocol

import (
	"context"
	"errors"
	"github.com/Nathandela/swarm/internal/wire"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDialContextCancelsStalledHello(t *testing.T) {
	dir, e := os.MkdirTemp("", "pdc-")
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	socket := filepath.Join(dir, "s")
	listener, e := net.Listen("unix", socket)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = listener.Close() }()
	hello := make(chan struct{})
	closed := make(chan bool, 1)
	go func() {
		conn, e := listener.Accept()
		if e != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		if _, _, e = wire.ReadFrame(conn); e != nil {
			return
		}
		close(hello)
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		_, _, e = wire.ReadFrame(conn)
		closed <- e != nil
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		c, e := DialContext(ctx, socket, nil)
		if c != nil {
			_ = c.Close()
		}
		done <- e
	}()
	<-hello
	cancel()
	select {
	case e = <-done:
		if !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not stop handshake")
	}
	if !<-closed {
		t.Fatal("connection survived canceled handshake")
	}
}

func TestDialContextCancellationClosesConnectedClient(t *testing.T) {
	dir, e := os.MkdirTemp("", "pdc-")
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	socket := filepath.Join(dir, "s")
	listener, e := net.Listen("unix", socket)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = listener.Close() }()
	request := make(chan struct{})
	closed := make(chan bool, 1)
	go func() {
		conn, e := listener.Accept()
		if e != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		if _, _, e = wire.ReadFrame(conn); e != nil {
			return
		}
		body, _ := EncodeControl(Control{Op: OpHello, ProtocolVersion: Version, EndpointID: "local"})
		if wire.WriteFrame(conn, wire.TControl, body) != nil {
			return
		}
		if _, _, e = wire.ReadFrame(conn); e != nil {
			return
		}
		close(request)
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		_, _, e = wire.ReadFrame(conn)
		closed <- e != nil
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client, e := DialContext(ctx, socket, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = client.Close() }()
	done := make(chan error, 1)
	go func() { _, e := client.List(); done <- e }()
	<-request
	cancel()
	select {
	case e = <-done:
		if e == nil {
			t.Fatal("canceled request succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("connected request did not cancel")
	}
	if !<-closed {
		t.Fatal("peer connection remained live")
	}
}
