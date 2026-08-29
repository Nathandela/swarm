package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/grant"
	"github.com/Nathandela/swarm/internal/remote/relay"
	"github.com/Nathandela/swarm/internal/remotegw"
)

// grantRetryConn is one complete generation's relay seam. Its command wait is the proof
// that remotegw.Service actually started; its append script drives bootstrap outcomes.
type grantRetryConn struct {
	mu sync.Mutex

	authorizeErr   error
	authorizeCalls int
	appendResults  []error
	appendDefault  error
	appendCalls    int
	appendFrames   [][]byte

	waitStarted  chan struct{}
	waitOnce     sync.Once
	appendCalled chan int
	done         chan struct{}
	closeOnce    sync.Once
}

func newGrantRetryConn(results ...error) *grantRetryConn {
	return &grantRetryConn{
		appendResults: results,
		waitStarted:   make(chan struct{}),
		appendCalled:  make(chan int, 16),
		done:          make(chan struct{}),
	}
}

func (c *grantRetryConn) AuthorizeDevice(context.Context, ed25519.PublicKey, []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.authorizeCalls++
	return c.authorizeErr
}

func (c *grantRetryConn) MailboxAppend(_ context.Context, _ string, env []byte) (uint64, error) {
	c.mu.Lock()
	c.appendCalls++
	call := c.appendCalls
	c.appendFrames = append(c.appendFrames, append([]byte(nil), env...))
	err := c.appendDefault
	if call <= len(c.appendResults) {
		err = c.appendResults[call-1]
	}
	c.mu.Unlock()
	c.appendCalled <- call
	return uint64(call), err
}

func (c *grantRetryConn) MailboxRead(ctx context.Context, _ uint64) ([]relay.Item, error) {
	c.markWaitStarted()
	<-ctx.Done()
	return nil, ctx.Err()
}

func (c *grantRetryConn) MailboxWait(ctx context.Context, _ uint64) ([]relay.Item, bool, error) {
	c.markWaitStarted()
	<-ctx.Done()
	return nil, false, ctx.Err()
}

func (c *grantRetryConn) MailboxAck(context.Context, uint64) error { return nil }

func (c *grantRetryConn) Done() <-chan struct{} { return c.done }

func (c *grantRetryConn) Close() error {
	c.closeOnce.Do(func() { close(c.done) })
	return nil
}

func (c *grantRetryConn) markWaitStarted() {
	c.waitOnce.Do(func() { close(c.waitStarted) })
}

func (c *grantRetryConn) counts() (authorize, append int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.authorizeCalls, c.appendCalls
}

func testGatewayGrant() *crypto.EpochGrant {
	return &crypto.EpochGrant{EpochID: 7, GrantSeq: 9, Sealed: []byte("sealed"), Sig: []byte("sig")}
}

func waitGenerationResult(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("connected gateway generation did not stop")
		return nil
	}
}

func TestRunConnectedGeneration_GrantDepthQuotaStartsCommandDrainAndRetries(t *testing.T) {
	conn := newGrantRetryConn(relay.ErrQuotaExceeded, nil)
	p := gatewayParams{
		DaemonSocket: "/nonexistent/swarm-remote.sock",
		PhoneTarget:  "phone",
		Grant:        testGatewayGrant(),
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	progressedResult := make(chan bool, 1)
	go func() {
		progressed, err := runConnectedGeneration(ctx, p, conn)
		progressedResult <- progressed
		done <- err
	}()

	select {
	case call := <-conn.appendCalled:
		if call != 1 {
			t.Fatalf("first append notification = %d, want 1", call)
		}
	case <-time.After(time.Second):
		t.Fatal("initial epoch-grant append was not attempted")
	}
	select {
	case <-conn.waitStarted:
		// The command-IN plane is live despite the full phone mailbox.
	case <-time.After(time.Second):
		t.Fatal("grant quota refusal prevented Service command drain from starting")
	}
	select {
	case call := <-conn.appendCalled:
		t.Fatalf("grant retry spun immediately (unexpected append call %d)", call)
	case <-time.After(200 * time.Millisecond):
	}
	select {
	case call := <-conn.appendCalled:
		if call != 2 {
			t.Fatalf("retry append notification = %d, want 2", call)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("epoch grant was not retried after the quota could clear")
	}

	conn.mu.Lock()
	frames := append([][]byte(nil), conn.appendFrames...)
	conn.mu.Unlock()
	if len(frames) != 2 || string(frames[0]) != string(frames[1]) {
		t.Fatalf("grant retry did not preserve the exact bootstrap bytes: %q", frames)
	}
	got, ok := grant.ParseBootstrap(frames[1])
	if !ok || got.EpochID != p.Grant.EpochID || got.GrantSeq != p.Grant.GrantSeq {
		t.Fatalf("eventually appended frame is not the persisted grant: %#v, ok=%v", got, ok)
	}

	cancel()
	if err := waitGenerationResult(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("generation returned %v, want context.Canceled", err)
	}
	if progressed := <-progressedResult; progressed {
		t.Fatal("bootstrap grant retry counted as relay progress and would reset the reconnect backoff")
	}
	if auth, appends := conn.counts(); auth != 1 || appends != 2 {
		t.Fatalf("calls after successful retry = authorize %d, append %d; want 1, 2", auth, appends)
	}
}

func TestRunConnectedGeneration_AuthorizationFailuresStayFailClosed(t *testing.T) {
	for _, refusal := range []error{
		// The phase marker matters: a quota refusal from AuthorizeDevice must not be
		// mistaken for the recoverable mailbox-append depth refusal.
		relay.ErrQuotaExceeded,
		relay.ErrConsentRetired,
		relay.ErrConsentMalformed,
		relay.ErrRevoked,
	} {
		t.Run(refusal.Error(), func(t *testing.T) {
			conn := newGrantRetryConn()
			conn.authorizeErr = refusal
			_, err := runConnectedGeneration(context.Background(), gatewayParams{
				DaemonSocket: "/nonexistent/swarm-remote.sock",
				PhoneTarget:  "phone",
				Grant:        testGatewayGrant(),
			}, conn)
			if !errors.Is(err, refusal) {
				t.Fatalf("generation error = %v, want %v", err, refusal)
			}
			if _, appends := conn.counts(); appends != 0 {
				t.Fatalf("authorization refusal still appended %d grant(s)", appends)
			}
			select {
			case <-conn.waitStarted:
				t.Fatal("authorization refusal still started the Service")
			default:
			}
		})
	}
}

func TestRunConnectedGeneration_NonQuotaGrantAppendFailuresStayFailClosed(t *testing.T) {
	for _, refusal := range []error{relay.ErrRevoked, relay.ErrNotAuthorized} {
		t.Run(refusal.Error(), func(t *testing.T) {
			conn := newGrantRetryConn(refusal)
			_, err := runConnectedGeneration(context.Background(), gatewayParams{
				DaemonSocket: "/nonexistent/swarm-remote.sock",
				PhoneTarget:  "phone",
				Grant:        testGatewayGrant(),
			}, conn)
			if !errors.Is(err, refusal) {
				t.Fatalf("generation error = %v, want %v", err, refusal)
			}
			select {
			case <-conn.waitStarted:
				t.Fatal("terminal grant append refusal still started the Service")
			default:
			}
		})
	}
}

func TestRunConnectedGeneration_RevocationDuringGrantRetryStopsTheService(t *testing.T) {
	conn := newGrantRetryConn(relay.ErrQuotaExceeded, relay.ErrRevoked)
	p := gatewayParams{
		DaemonSocket: "/nonexistent/swarm-remote.sock",
		PhoneTarget:  "phone",
		Grant:        testGatewayGrant(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := runConnectedGeneration(ctx, p, conn)
		done <- err
	}()
	select {
	case <-conn.waitStarted:
	case <-time.After(time.Second):
		t.Fatal("Service did not start during quota recovery")
	}
	if err := waitGenerationResult(t, done); !errors.Is(err, relay.ErrRevoked) {
		t.Fatalf("generation returned %v, want the terminal retry revocation", err)
	}
}

func TestRunConnectedGeneration_CancelLeavesNoGrantRetryGoroutine(t *testing.T) {
	conn := newGrantRetryConn(relay.ErrQuotaExceeded)
	conn.appendDefault = relay.ErrQuotaExceeded
	p := gatewayParams{
		DaemonSocket: "/nonexistent/swarm-remote.sock",
		PhoneTarget:  "phone",
		Grant:        testGatewayGrant(),
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := runConnectedGeneration(ctx, p, conn)
		done <- err
	}()
	select {
	case <-conn.waitStarted:
	case <-time.After(time.Second):
		t.Fatal("Service did not start during quota recovery")
	}
	cancel()
	if err := waitGenerationResult(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("generation returned %v, want context.Canceled", err)
	}
	_, before := conn.counts()
	time.Sleep(650 * time.Millisecond) // longer than the fastest jittered first retry
	_, after := conn.counts()
	if after != before {
		t.Fatalf("grant retry outlived its generation: append calls grew from %d to %d", before, after)
	}
}

func TestRunConnectedGeneration_RelayLossJoinsGrantRetry(t *testing.T) {
	conn := newGrantRetryConn(relay.ErrQuotaExceeded)
	conn.appendDefault = relay.ErrQuotaExceeded
	p := gatewayParams{
		DaemonSocket: "/nonexistent/swarm-remote.sock",
		PhoneTarget:  "phone",
		Grant:        testGatewayGrant(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := runConnectedGeneration(ctx, p, conn)
		done <- err
	}()
	select {
	case <-conn.waitStarted:
	case <-time.After(time.Second):
		t.Fatal("Service did not start during quota recovery")
	}

	// A generation ends when its relay connection dies even while the grant retry is
	// parked in backoff. The retry must be cancelled and joined before the caller can
	// close this client and construct the next generation around a fresh one.
	if err := conn.Close(); err != nil {
		t.Fatalf("close relay connection: %v", err)
	}
	if err := waitGenerationResult(t, done); !errors.Is(err, remotegw.ErrRelayGone) {
		t.Fatalf("generation returned %v, want remotegw.ErrRelayGone", err)
	}
	_, before := conn.counts()
	time.Sleep(650 * time.Millisecond) // longer than the slowest jittered first retry
	_, after := conn.counts()
	if after != before {
		t.Fatalf("grant retry outlived relay loss: append calls grew from %d to %d", before, after)
	}
}
