package voiceclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"
)

type testForward struct {
	Host       string
	Port       uint32
	OriginHost string
	OriginPort uint32
}

func TestDialSSHDirectLoopback(t *testing.T) {
	cfg, user, forwards := sshFixture(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := DialSSH(ctx, cfg, user)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("echo")); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 4)
	if _, err := io.ReadFull(conn, response); err != nil || string(response) != "echo" {
		t.Fatalf("echo response %q, %v", response, err)
	}
	forward := <-forwards
	if forward.Host != "127.0.0.1" || forward.Port != 8443 {
		t.Fatalf("unexpected remote destination %+v", forward)
	}
}

func TestDialSSHRefusesUntrustedHostsAndTargets(t *testing.T) {
	for _, tc := range []struct {
		name   string
		modify func(*SSHConfig, *ssh.Signer)
		want   error
	}{
		{"pin mismatch", func(c *SSHConfig, _ *ssh.Signer) {
			c.HostKeySHA256 = ssh.FingerprintSHA256(signerFixture(t).PublicKey())
		}, ErrHostKey},
		{"wrong user key", func(_ *SSHConfig, signer *ssh.Signer) { *signer = signerFixture(t) }, ErrSSHHandshake},
		{"wrong remote port", func(c *SSHConfig, _ *ssh.Signer) { c.RemoteVoiceAddress = "127.0.0.1:8444" }, ErrForward},
		{"hostname destination", func(c *SSHConfig, _ *ssh.Signer) { c.RemoteVoiceAddress = "localhost:8443" }, ErrSSHConfig},
		{"nonloopback destination", func(c *SSHConfig, _ *ssh.Signer) { c.RemoteVoiceAddress = "192.0.2.1:8443" }, ErrSSHConfig},
		{"mapped destination", func(c *SSHConfig, _ *ssh.Signer) { c.RemoteVoiceAddress = "[::ffff:127.0.0.1]:8443" }, ErrSSHConfig},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, user, _ := sshFixture(t, false)
			tc.modify(&cfg, &user)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if conn, err := DialSSH(ctx, cfg, user); !errors.Is(err, tc.want) {
				if conn != nil {
					_ = conn.Close()
				}
				t.Fatalf("DialSSH() = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestDialSSHCancelUnblocksStalledReadAndWrite(t *testing.T) {
	cfg, user, forwards := sshFixture(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn, err := DialSSH(ctx, cfg, user)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	<-forwards
	read := make(chan error, 1)
	written := make(chan error, 1)
	go func() { _, err := conn.Read(make([]byte, 2)); read <- err }()
	go func() { _, err := conn.Write(make([]byte, 8<<20)); written <- err }()
	select {
	case err := <-read:
		t.Fatalf("read was not stalled: %v", err)
	case err := <-written:
		t.Fatalf("write was not stalled: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	start := time.Now()
	cancel()
	for _, result := range []<-chan error{read, written} {
		select {
		case err := <-result:
			if err == nil {
				t.Fatal("stalled I/O succeeded after cancellation")
			}
		case <-time.After(time.Second):
			t.Fatal("owned SSH transport did not unblock")
		}
	}
	t.Logf("cancellation unblocked channel read/write in %s", time.Since(start))
}

func TestDialSSHCancelInterruptsHandshake(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	accepted := make(chan net.Conn, 1)
	go func() { conn, _ := listener.Accept(); accepted <- conn }()
	cfg := SSHConfig{
		Address: listener.Addr().String(), User: "swarm-test",
		HostKeySHA256: ssh.FingerprintSHA256(signerFixture(t).PublicKey()), RemoteVoiceAddress: "127.0.0.1:8443",
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	user := signerFixture(t)
	go func() { _, err := DialSSH(ctx, cfg, user); done <- err }()
	select {
	case peer := <-accepted:
		if peer == nil {
			t.Fatal("fixture did not accept")
		}
		defer func() { _ = peer.Close() }()
	case <-time.After(time.Second):
		t.Fatal("handshake never started")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("handshake cancellation = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("handshake ignored cancellation")
	}
}

func signerFixture(t *testing.T) ssh.Signer {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func sshFixture(t *testing.T, stall bool) (SSHConfig, ssh.Signer, <-chan testForward) {
	t.Helper()
	return sshRemoteFixture(t, stall, "", "127.0.0.1:8443")
}

func sshRemoteFixture(t *testing.T, stall bool, forwardAddr, allowed string) (SSHConfig, ssh.Signer, <-chan testForward) {
	t.Helper()
	endpoint, err := netip.ParseAddrPort(allowed)
	if err != nil {
		t.Fatal(err)
	}
	host, user := signerFixture(t), signerFixture(t)
	server := &ssh.ServerConfig{PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if meta.User() != "swarm-test" || !bytes.Equal(key.Marshal(), user.PublicKey().Marshal()) {
			return nil, errors.New("test identity refused")
		}
		return nil, nil
	}}
	server.AddHostKey(host)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	forwards := make(chan testForward, 1)
	go func() {
		defer close(done)
		raw, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = raw.Close() }()
		_ = raw.SetDeadline(time.Now().Add(5 * time.Second))
		conn, channels, requests, err := ssh.NewServerConn(raw, server)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		go ssh.DiscardRequests(requests)
		for request := range channels {
			var forward testForward
			if request.ChannelType() != "direct-tcpip" || ssh.Unmarshal(request.ExtraData(), &forward) != nil ||
				forward.Host != endpoint.Addr().String() || forward.Port != uint32(endpoint.Port()) {
				_ = request.Reject(ssh.Prohibited, "fixture destination refused")
				continue
			}
			channel, channelReqs, err := request.Accept()
			if err != nil {
				return
			}
			go ssh.DiscardRequests(channelReqs)
			forwards <- forward
			if stall {
				_ = conn.Wait()
				return
			}
			if forwardAddr == "" {
				_, _ = io.Copy(channel, channel)
			} else {
				forwardToFixture(channel, forwardAddr)
			}
			_ = channel.Close()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("SSH fixture did not finish")
		}
	})
	return SSHConfig{Address: listener.Addr().String(), User: "swarm-test", HostKeySHA256: ssh.FingerprintSHA256(host.PublicKey()), RemoteVoiceAddress: allowed}, user, forwards
}

func forwardToFixture(channel ssh.Channel, address string) {
	remote, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		return
	}
	copied := make(chan struct{})
	go func() {
		defer close(copied)
		_, _ = io.Copy(remote, channel)
		_ = remote.Close()
	}()
	_, _ = io.Copy(channel, remote)
	_ = remote.Close()
	_ = channel.Close()
	<-copied
}

func TestDialSSHDirectWebSocket(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/voice" {
			http.NotFound(w, r)
			return
		}
		socket, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = socket.CloseNow() }()
		kind, payload, err := socket.Read(ctx)
		if err == nil {
			_ = socket.Write(ctx, kind, payload)
			_, _, _ = socket.Read(ctx)
		}
	}))
	defer server.Close()
	address := strings.TrimPrefix(server.URL, "http://")
	cfg, user, forwards := sshRemoteFixture(t, false, address, address)
	transport := &http.Transport{
		DialContext: func(_ context.Context, network, address string) (net.Conn, error) {
			if network != "tcp" || address != "voice.invalid:80" {
				return nil, errors.New("unexpected WebSocket destination")
			}
			return DialSSH(ctx, cfg, user)
		},
	}
	defer transport.CloseIdleConnections()
	socket, _, err := websocket.Dial(ctx, "ws://voice.invalid/v1/voice", &websocket.DialOptions{HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = socket.CloseNow() }()
	payload := []byte{0, 1, 2, 3}
	if err := socket.Write(ctx, websocket.MessageBinary, payload); err != nil {
		t.Fatal(err)
	}
	kind, got, err := socket.Read(ctx)
	if err != nil || kind != websocket.MessageBinary || !bytes.Equal(payload, got) {
		t.Fatalf("forwarded WebSocket message = %v %v %v", kind, got, err)
	}
	target := netip.MustParseAddrPort(address)
	if forward := <-forwards; forward.Host != target.Addr().String() || forward.Port != uint32(target.Port()) {
		t.Fatalf("unexpected direct-tcpip destination: %+v", forward)
	}
}
