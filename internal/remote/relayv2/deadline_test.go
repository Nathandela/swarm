package relayv2

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/coder/websocket"
)

// deadlineTestCeiling is deliberately independent of the production constants. A
// background-context operation still running here is not bounded usefully, while a
// widened production timeout cannot make this test bless its own widening.
const deadlineTestCeiling = 20 * time.Second

func TestRelayV2DialHonorsCallerCancellation(t *testing.T) {
	server, entered := silentUpgradeServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		transport, err := DialPair(ctx, testPairProfile(server.URL), strings.Repeat("a", 32))
		if transport != nil {
			transport.Close()
		}
		done <- err
	}()

	awaitSignal(t, entered, "pairing dial did not reach the silent HTTP peer")
	cancel()
	assertPromptContextError(t, done, context.Canceled)
}

func TestRelayV2CallHonorsCallerCancellation(t *testing.T) {
	conn, received := silentCallConn(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := appendAsync(conn, ctx)

	awaitSignal(t, received, "append did not reach the silent WebSocket peer")
	cancel()
	assertPromptContextError(t, done, context.Canceled)
	assertConnectionClosed(t, conn)
}

func TestRelayV2DialHasInternalDeadline(t *testing.T) {
	t.Parallel()
	server, entered := silentUpgradeServer(t)
	done := make(chan error, 1)
	go func() {
		transport, err := DialPair(context.Background(), testPairProfile(server.URL), strings.Repeat("b", 32))
		if transport != nil {
			transport.Close()
		}
		done <- err
	}()

	awaitSignal(t, entered, "background-context dial did not reach the silent HTTP peer")
	assertInternalDeadline(t, done)
}

func TestRelayV2CallHasInternalDeadline(t *testing.T) {
	t.Parallel()
	conn, received := silentCallConn(t)
	done := appendAsync(conn, context.Background())

	awaitSignal(t, received, "background-context append did not reach the silent WebSocket peer")
	assertInternalDeadline(t, done)
	assertConnectionClosed(t, conn)
}

func silentUpgradeServer(t *testing.T) (*httptest.Server, <-chan struct{}) {
	t.Helper()
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		once.Do(func() { close(entered) })
		select {
		case <-request.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })
	return server, entered
}

func testPairProfile(rawURL string) Profile {
	return Profile{RelayURL: rawURL, Security: relay.Security{AllowLoopbackCleartext: true}}
}

func silentCallConn(t *testing.T) (*Conn, <-chan struct{}) {
	t.Helper()
	received := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		ws, err := websocket.Accept(w, request, nil)
		if err != nil {
			return
		}
		defer func() { _ = ws.CloseNow() }()
		if _, _, err := ws.Read(request.Context()); err == nil {
			close(received)
			select {
			case <-request.Context().Done():
			case <-release:
			}
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := dialRaw(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial silent WebSocket peer: %v", err)
	}
	t.Cleanup(conn.Close)
	conn.role = RoleMachine
	conn.purpose = PurposeStream
	conn.machineRID = testMachineRID
	conn.rid = testMachineRID
	return conn, received
}

func appendAsync(conn *Conn, ctx context.Context) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := conn.Append(ctx, Binding{
			MachineRID: testMachineRID,
			PeerRID:    testPhoneRID,
			Generation: 7,
		}, "deadline-probe", []byte("opaque"))
		done <- err
	}()
	return done
}

func awaitSignal(t *testing.T, signal <-chan struct{}, failure string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal(failure)
	}
}

func assertPromptContextError(t *testing.T, done <-chan error, want error) {
	t.Helper()
	select {
	case err := <-done:
		if !errors.Is(err, want) {
			t.Fatalf("operation returned %v, want %v", err, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("operation did not return promptly after %v", want)
	}
}

func assertInternalDeadline(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("background-context operation returned %v, want an internal deadline", err)
		}
	case <-time.After(deadlineTestCeiling):
		t.Fatalf("background-context operation exceeded the independent %v ceiling", deadlineTestCeiling)
	}
}

func assertConnectionClosed(t *testing.T, conn *Conn) {
	t.Helper()
	select {
	case <-conn.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("timed-out call left its WebSocket connection alive")
	}
}
