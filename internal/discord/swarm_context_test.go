package discord

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/wire"
)

func TestSwarmCanceledHandshakeWritesNoNativeOperation(t *testing.T) {
	dir, e := os.MkdirTemp("", "dsc-")
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
	release := make(chan struct{})
	observed := make(chan string, 1)
	go func() {
		defer close(observed)
		conn, e := listener.Accept()
		if e != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		if _, _, e = wire.ReadFrame(conn); e != nil {
			return
		}
		close(hello)
		<-release
		body, _ := protocol.EncodeControl(protocol.Control{Op: protocol.OpHello, ProtocolVersion: protocol.Version, EndpointID: "local", Capabilities: []string{protocol.CapSemanticSend, protocol.CapMessageOrigin}})
		if wire.WriteFrame(conn, wire.TControl, body) != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		_, raw, e := wire.ReadFrame(conn)
		if e == nil {
			frame, _ := protocol.DecodeControl(raw)
			observed <- frame.Op
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	operation, _ := protocol.NewSemanticOperationID()
	go func() {
		defer close(done)
		_, _ = (SwarmClient{Socket: socket}).Submit(ctx, operation, protocol.ComposerSendReq{Session: "local/one", SessionInstance: "instance", Text: "message"})
	}()
	<-hello
	cancel()
	close(release)
	select {
	case op := <-observed:
		if op == protocol.OpSemanticSend {
			t.Fatal("native mutation written after handshake cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled connection remained live")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Submit did not stop")
	}
}
