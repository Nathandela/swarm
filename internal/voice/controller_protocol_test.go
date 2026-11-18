package voice

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/wire"
)

func controllerWireServer(t *testing.T, endpoint string, handle func(protocol.Control) protocol.Control) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "vc")
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "owner.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	done := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { _ = conn.Close() }()
				_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
				_, raw, err := wire.ReadFrame(conn)
				if err != nil {
					return
				}
				hello, err := protocol.DecodeControl(raw)
				if err != nil {
					return
				}
				body, err := protocol.EncodeControl(protocol.Control{Op: protocol.OpHello, ProtocolVersion: protocol.Version, EndpointID: endpoint, Capabilities: hello.Capabilities})
				if err != nil {
					return
				}
				if wire.WriteFrame(conn, wire.TControl, body) != nil {
					return
				}
				_, raw, err = wire.ReadFrame(conn)
				if err != nil {
					return
				}
				request, err := protocol.DecodeControl(raw)
				if err != nil {
					return
				}
				reply := handle(request)
				body, err = protocol.EncodeControl(reply)
				if err == nil {
					_ = wire.WriteFrame(conn, wire.TControl, body)
				}
			}()
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done; wg.Wait(); _ = os.RemoveAll(dir) })
	return socket
}
func TestProtocolControllerLostInputReplyDoesNotBlockNativeProofConnection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	binding := protocol.ManagerRuntimeBinding{Session: "machine/manager", SessionInstance: "instance", ConversationID: "conversation", Provider: "codex", NativeSessionID: "thread", NativeGeneration: "generation", ObservationGeneration: "observation", LoginEpoch: "account"}
	socket := controllerWireServer(t, "machine", func(req protocol.Control) protocol.Control {
		if req.Op == protocol.OpSemanticSend {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
			}
			return protocol.Control{Op: protocol.OpOK, OperationID: req.OperationID, SessionID: req.SessionID}
		}
		return protocol.Control{Op: protocol.OpManagerRuntime, ManagerRuntime: &protocol.ManagerRuntimeStatus{Binding: binding, ReadyForInput: true, Turn: protocol.ManagerNativeTurn{State: "idle"}}}
	})
	client, err := NewProtocolControllerClient(ctx, socket, "machine")
	if err != nil {
		t.Fatal(err)
	}
	operation, err := protocol.NewSemanticOperationID()
	if err != nil {
		t.Fatal(err)
	}
	sent := make(chan error, 1)
	go func() {
		sent <- client.SemanticSend(operation, protocol.ComposerSendReq{Session: binding.Session, SessionInstance: binding.SessionInstance, Text: "exact", ManagerAdmission: &protocol.ManagerAdmission{Binding: binding, Nonce: strings.Repeat("a", 64)}})
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("native send did not start")
	}
	status, err := client.ManagerRuntime(binding.Session, binding.SessionInstance)
	if err != nil || !status.ReadyForInput {
		t.Fatalf("native proof read waited for the send ACK: %+v %v", status, err)
	}
	once.Do(func() { close(release) })
	select {
	case err := <-sent:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("accepted input did not receive its own reply")
	}
}
func TestProtocolControllerPinsEndpointAndHonorsCancelledProofContext(t *testing.T) {
	var requests atomic.Int32
	socket := controllerWireServer(t, "foreign", func(req protocol.Control) protocol.Control {
		requests.Add(1)
		return protocol.Control{Op: protocol.OpOK}
	})
	client, err := NewProtocolControllerClient(context.Background(), socket, "machine")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.List(); !errors.Is(err, ErrStale) {
		t.Fatalf("adopted foreign endpoint: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = client.ResolveManagerCall(ctx, protocol.ManagerCallReq{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if requests.Load() != 0 {
		t.Fatal("foreign/cancelled source received an application request")
	}
}
