package voicessh

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/voicepanel"
	"github.com/Nathandela/swarm/internal/voicewire"
	"golang.org/x/crypto/ssh"
)

func TestAliasAndForwardDestinationRejectInjection(t *testing.T) {
	for _, bad := range []string{"", "-oProxyCommand=internal", "user@alias", "host;next", "a b", "host\nnext", "../alias"} {
		if ValidAlias(bad) {
			t.Errorf("alias accepted %q", bad)
		}
	}
	for _, bad := range []string{"example.test:1234", "0.0.0.0:80", "127.0.0.2:55", "127.0.0.1:0", "[::]:55", "127.0.0.1:80 -v"} {
		if validEndpoint(bad) {
			t.Errorf("destination accepted %q", bad)
		}
	}
	if !ValidAlias("worker-1.local_alias") || !validEndpoint("127.0.0.1:1234") || !validEndpoint("[::1]:1234") {
		t.Fatal("ordinary alias or loopback endpoint refused")
	}
}

type sshFixture struct {
	listener net.Listener
	signer   ssh.Signer
}

func startSSHFixture(t *testing.T, authorized ssh.PublicKey, allowedTarget string, command func(string) ([]byte, bool)) sshFixture {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	host, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, offered ssh.PublicKey) (*ssh.Permissions, error) {
		if !bytes.Equal(authorized.Marshal(), offered.Marshal()) {
			return nil, errors.New("wrong fixture key")
		}
		return nil, nil
	}}
	cfg.AddHostKey(host)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var conns sync.Map
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			conns.Store(raw, struct{}{})
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { _ = raw.Close(); conns.Delete(raw) }()
				_ = raw.SetDeadline(time.Now().Add(30 * time.Second))
				conn, channels, requests, err := ssh.NewServerConn(raw, cfg)
				if err != nil {
					return
				}
				defer func() { _ = conn.Close() }()
				go ssh.DiscardRequests(requests)
				for incoming := range channels {
					switch incoming.ChannelType() {
					case "direct-tcpip":
						go fixtureForward(incoming, allowedTarget)
					case "session":
						if command == nil {
							_ = incoming.Reject(ssh.Prohibited, "session not allowed")
						} else {
							go fixtureCommand(incoming, command)
						}
					default:
						_ = incoming.Reject(ssh.UnknownChannelType, "unknown fixture channel")
					}
				}
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-done
		conns.Range(func(raw, _ any) bool { _ = raw.(net.Conn).Close(); return true })
		wg.Wait()
	})
	return sshFixture{listener, host}
}

func fixtureCommand(incoming ssh.NewChannel, handle func(string) ([]byte, bool)) {
	stream, requests, err := incoming.Accept()
	if err != nil {
		return
	}
	defer func() { _ = stream.Close() }()
	for req := range requests {
		var body struct{ Command string }
		if req.Type != "exec" || ssh.Unmarshal(req.Payload, &body) != nil {
			_ = req.Reply(false, nil)
			continue
		}
		raw, allowed := handle(body.Command)
		_ = req.Reply(allowed, nil)
		if allowed {
			_, _ = stream.Write(raw)
			_, _ = stream.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{0}))
		}
		return
	}
}

func fixtureForward(incoming ssh.NewChannel, allowed string) {
	var body struct {
		Address  string
		Port     uint32
		From     string
		FromPort uint32
	}
	if ssh.Unmarshal(incoming.ExtraData(), &body) != nil || net.JoinHostPort(body.Address, strconv.FormatUint(uint64(body.Port), 10)) != allowed {
		_ = incoming.Reject(ssh.Prohibited, "wrong fixture destination")
		return
	}
	upstream, err := net.DialTimeout("tcp", allowed, time.Second)
	if err != nil {
		_ = incoming.Reject(ssh.ConnectionFailed, "fixture endpoint unavailable")
		return
	}
	defer func() { _ = upstream.Close() }()
	stream, requests, err := incoming.Accept()
	if err != nil {
		return
	}
	defer func() { _ = stream.Close() }()
	go ssh.DiscardRequests(requests)
	copied := make(chan struct{})
	go func() { defer close(copied); _, _ = io.Copy(upstream, stream); _ = upstream.Close() }()
	_, _ = io.Copy(stream, upstream)
	_ = stream.Close()
	<-copied
}

type deviceFixture struct {
	mu                 sync.Mutex
	target             string
	public             ed25519.PublicKey
	challenge          string
	counter            uint64
	enrollmentCommands int
	statusFailure      int
	invitation         voicewire.DeviceCredential
	grant              voicewire.DeviceCredential
	admitted           map[string]string
}

func (f *deviceFixture) bootstrap(command string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if command == "swarm voice device-endpoint" {
		raw, _ := json.Marshal(f.target)
		return raw, true
	}
	if public, ok := strings.CutPrefix(command, "swarm voice enroll --device-key "); ok {
		key, err := base64.RawURLEncoding.Strict().DecodeString(public)
		if err == nil && len(key) == ed25519.PublicKeySize && base64.RawURLEncoding.EncodeToString(key) == public {
			f.public = key
			f.enrollmentCommands++
			raw, _ := json.Marshal(voicewire.DeviceInvitation{PlaceID: f.invitation.PlaceID,
				Reference: f.invitation.Token, ExpiresAt: time.Now().Add(time.Minute), Endpoint: f.target})
			return raw, true
		}
	}
	return nil, false
}

func (f *deviceFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	action := strings.TrimPrefix(r.URL.Path, "/v1/device/")
	var proof voicewire.DeviceProof
	var body []byte
	utterance := r.Header.Get("Swarm-Utterance-ID")
	decoder := json.NewDecoder(io.LimitReader(r.Body, 128<<10))
	switch action {
	case "challenge":
		var credential voicewire.DeviceCredential
		if decoder.Decode(&credential) != nil || (credential != f.invitation && credential != f.grant) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		f.counter++
		f.challenge = fmt.Sprintf("%064x", f.counter)
		_ = json.NewEncoder(w).Encode(voicewire.DeviceChallenge{Challenge: f.challenge})
		return
	case "ptt/finalize":
		encoded, err := base64.RawURLEncoding.Strict().DecodeString(r.Header.Get("Swarm-Device-Proof"))
		if err != nil || json.Unmarshal(encoded, &proof) != nil || utterance == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		body, err = io.ReadAll(io.LimitReader(r.Body, voicewire.MaxDevicePCMBytes+1))
		if err != nil || len(body) != 4 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		action = voicewire.DeviceFinalizeAction(utterance, r.Header.Get("Swarm-Capture-Session"))
	case "enroll":
		if decoder.Decode(&proof) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	default:
		var req voicewire.DeviceRequest
		if decoder.Decode(&req) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		proof, body = req.Proof, req.Body
	}
	expected := f.grant
	if action == "enroll" {
		expected = f.invitation
	}
	if proof.Credential != expected || proof.Challenge == "" || proof.Challenge != f.challenge || len(f.public) != ed25519.PublicKeySize ||
		!ed25519.Verify(f.public, voicewire.DeviceSignedBytes(proof, action, body), proof.Signature) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	f.challenge = ""
	encode := json.NewEncoder(w)
	switch action {
	case "enroll":
		_ = encode.Encode(f.grant)
	case "session":
		if f.statusFailure != 0 {
			w.WriteHeader(f.statusFailure)
			return
		}
		msg, err := voicewire.DecodeClientMessage(body)
		if err != nil || !strings.HasPrefix(msg.SessionID, f.grant.DeviceID+".") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		cursor := voicewire.Cursor{StreamID: "fixture", Sequence: 1}
		page := voicewire.DeviceStatusPage{Version: 1, Head: cursor}
		if msg.After == nil || *msg.After != cursor {
			data, _ := json.Marshal(voicewire.DeviceStatus{PlaceID: f.grant.PlaceID, ConversationID: "actual-place", Provider: "codex", Ready: true, Generation: 1})
			page.Reset = true
			page.Events = []voicewire.ServerEvent{{Type: "snapshot", SessionID: msg.SessionID, Cursor: cursor, Data: data}}
		}
		_ = encode.Encode(page)
	case "ptt/begin":
		var msg voicewire.DeviceText
		_ = json.Unmarshal(body, &msg)
		_ = encode.Encode(voicewire.DeviceCapture{ID: msg.ID, Phase: "capturing"})
	case voicewire.DeviceFinalizeAction(utterance, r.Header.Get("Swarm-Capture-Session")):
		f.admitted[utterance] = "PTT text"
		_ = encode.Encode(voicewire.DeviceAdmission{ID: utterance, Sequence: 2, Phase: "admitted"})
	case "text", "receipt":
		var msg voicewire.DeviceText
		_ = json.Unmarshal(body, &msg)
		old, exists := f.admitted[msg.ID]
		if action == "receipt" && !exists {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if action == "text" && exists && old != msg.Text {
			w.WriteHeader(http.StatusConflict)
			return
		}
		if action == "text" {
			f.admitted[msg.ID] = msg.Text
		}
		_ = encode.Encode(voicewire.DeviceAdmission{ID: msg.ID, Sequence: 1, Phase: "completed"})
	case "publications":
		var query voicewire.DevicePublicationQuery
		_ = json.Unmarshal(body, &query)
		page := voicewire.DevicePublicationPage{}
		if query.After == 0 {
			page.Item = &voicewire.DevicePublication{ID: "output", Sequence: 1, Final: true}
			page.Text = []byte("Observed worker result")
		}
		_ = encode.Encode(page)
	case "preview":
		var request voicewire.DevicePreviewRequest
		if json.Unmarshal(body, &request) != nil || request.PublicationID != "output" ||
			f.admitted[request.UtteranceID] == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", "4")
		_, _ = w.Write([]byte{0, 4, 0, 7})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestOpenSSHDesktopPanelThroughKnownJump(t *testing.T) {
	binary, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("installed OpenSSH is unavailable")
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshKey, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	private, err := ssh.MarshalPrivateKey(key, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	identity := filepath.Join(dir, "client-key")
	if err = os.WriteFile(identity, pem.EncodeToMemory(private), 0600); err != nil {
		t.Fatal(err)
	}
	device := &deviceFixture{
		invitation: voicewire.DeviceCredential{PlaceID: "fixture-place", Token: strings.Repeat("a", 64)},
		grant:      voicewire.DeviceCredential{PlaceID: "fixture-place", DeviceID: strings.Repeat("b", 32), Generation: 1, Token: strings.Repeat("c", 64)},
		admitted:   make(map[string]string),
	}
	httpServer := httptest.NewServer(device)
	defer httpServer.Close()
	device.target = httpServer.Listener.Addr().String()
	target := startSSHFixture(t, sshKey.PublicKey(), device.target, device.bootstrap)
	jump := startSSHFixture(t, sshKey.PublicKey(), target.listener.Addr().String(), nil)
	known := filepath.Join(dir, "known_hosts")
	writeKnown := func(targetKey ssh.PublicKey) {
		t.Helper()
		var entries strings.Builder
		for _, entry := range []struct {
			alias string
			key   ssh.PublicKey
		}{{"target-test-key", targetKey}, {"jump-test-key", jump.signer.PublicKey()}} {
			entries.WriteString(entry.alias + " " + string(ssh.MarshalAuthorizedKey(entry.key)))
		}
		if err := os.WriteFile(known, []byte(entries.String()), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeKnown(target.signer.PublicKey())
	_, targetPort, _ := net.SplitHostPort(target.listener.Addr().String())
	_, jumpPort, _ := net.SplitHostPort(jump.listener.Addr().String())
	config := filepath.Join(dir, "ssh_config")
	text := fmt.Sprintf("Host voice-test\n HostName 127.0.0.1\n Port %s\n HostKeyAlias target-test-key\n ProxyJump fixture-jump\nHost fixture-jump\n HostName 127.0.0.1\n Port %s\n HostKeyAlias jump-test-key\nHost *\n User tester\n IdentityFile \"%s\"\n UserKnownHostsFile \"%s\"\n GlobalKnownHostsFile /dev/null\n IdentitiesOnly yes\n", targetPort, jumpPort, identity, known)
	if err = os.WriteFile(config, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	route := connector{binary: binary, config: config, alias: "voice-test"}
	profiles := filepath.Join(dir, "profiles")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err = open(ctx, profiles, route, false); !errors.Is(err, ErrEnrollmentRequired) {
		t.Fatalf("no-grant route attempted implicit enrollment: %v", err)
	}
	connection, err := open(ctx, profiles, route, true)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err = open(ctx, profiles, route, false); !errors.Is(err, ErrProfileBusy) {
		t.Fatalf("same grant allowed a competing challenge client: %v", err)
	}
	panelCtx, stopPanel := context.WithCancel(ctx)
	defer stopPanel()
	ready := make(chan string, 1)
	panelDone := make(chan error, 1)
	go func() {
		panelDone <- voicepanel.ServeDevice(panelCtx, connection.Client, connection.Credential, route.alias, func(url string) error { ready <- url; return nil })
	}()
	var origin, token string
	select {
	case launch := <-ready:
		url, secret, _ := strings.Cut(launch, "/#")
		origin, token = url, secret
	case err = <-panelDone:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	request := func(method, path string, value string, status int) []byte {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, origin+"/api/"+path, strings.NewReader(value))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if method == http.MethodPost {
			req.Header.Set("Origin", origin)
			req.Header.Set("Content-Type", "application/json")
		}
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		raw, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != status {
			t.Fatalf("panel %s status %d: %s (%v)", path, response.StatusCode, raw, err)
		}
		return raw
	}
	raw := request(http.MethodGet, "status", "", 200)
	if !bytes.Contains(raw, []byte("actual-place")) || bytes.Contains(raw, []byte(`"preview_unavailable":true`)) {
		t.Fatalf("panel status omitted exact signed place or hid signed preview: %s", raw)
	}
	const id = "00112233-4455-6677-8899-aabbccddeeff"
	const textBody = `{"id":"` + id + `","text":"Literal text; no SSH command"}`
	request(http.MethodPost, "receipt", `{"id":"`+id+`"}`, 404)
	request(http.MethodPost, "text", textBody, 200)
	request(http.MethodPost, "text", textBody, 200)
	request(http.MethodPost, "text", `{"id":"`+id+`","text":"different"}`, 409)
	request(http.MethodPost, "receipt", `{"id":"`+id+`"}`, 200)
	if raw = request(http.MethodPost, "publications", `{"id":"`+id+`"}`, 200); !bytes.Contains(raw, []byte("Observed worker result")) {
		t.Fatalf("missing publication: %s", raw)
	}
	if raw = request(http.MethodPost, "read-aloud", `{"id":"`+id+`","publication_id":"output"}`, 200); !bytes.Equal(raw, []byte{0, 4, 0, 7}) {
		t.Fatal("signed desktop preview did not return renderable PCM")
	}
	request(http.MethodPost, "read-aloud", `{"id":"`+id+`","publication_id":"other"}`, 404)
	const pttID = "00112233-4455-6677-8899-aabbccddeeee"
	request(http.MethodPost, "ptt/begin", `{"id":"`+pttID+`"}`, 200)
	ptt, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/api/ptt/finalize", bytes.NewReader([]byte{1, 2, 3, 4}))
	if err != nil {
		t.Fatal(err)
	}
	ptt.Header.Set("Authorization", "Bearer "+token)
	ptt.Header.Set("Origin", origin)
	ptt.Header.Set("Content-Type", "application/octet-stream")
	ptt.Header.Set("Swarm-Utterance-ID", pttID)
	res, err := http.DefaultClient.Do(ptt)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatal("signed PTT failed:", res.Status)
	}
	stopPanel()
	if err = <-panelDone; err != nil {
		t.Fatal(err)
	}
	connection.Close()
	reopened, err := open(ctx, profiles, route, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reopened.Client.Receipt(ctx, reopened.Credential, id); err != nil {
		t.Fatal(err)
	}
	reopened.Close()
	device.mu.Lock()
	calls, admitted := device.enrollmentCommands, len(device.admitted)
	device.mu.Unlock()
	if calls != 1 || admitted != 2 {
		t.Fatalf("reconnect re-enrolled or duplicated an admission: %d / %d", calls, admitted)
	}
	device.mu.Lock()
	device.statusFailure = http.StatusServiceUnavailable
	device.mu.Unlock()
	if _, err := open(ctx, profiles, route, true); err == nil {
		t.Fatal("temporary failure opened panel")
	}
	device.mu.Lock()
	device.statusFailure = 0
	device.grant.DeviceID = strings.Repeat("e", 32)
	device.mu.Unlock()
	if _, err := open(ctx, profiles, route, false); err == nil {
		t.Fatal("revoked grant opened panel")
	}
	renewed, err := open(ctx, profiles, route, true)
	if err != nil {
		t.Fatalf("explicit re-enrollment after revocation failed: %v", err)
	}
	if renewed.Credential.DeviceID == reopened.Credential.DeviceID {
		t.Fatal("re-enrollment retained revoked grant")
	}
	renewed.Close()
	device.mu.Lock()
	calls = device.enrollmentCommands
	device.mu.Unlock()
	if calls != 2 {
		t.Fatalf("expected original and explicit replacement enrollment only, got %d", calls)
	}
	streamCtx, cancelStream := context.WithCancel(ctx)
	stream, err := route.forward(streamCtx, device.target)
	if err != nil {
		t.Fatal(err)
	}
	blocked := make(chan error, 1)
	go func() {
		var b [1]byte
		_, err := stream.Read(b[:])
		blocked <- err
	}()
	cancelStream()
	select {
	case err := <-blocked:
		if err == nil {
			t.Fatal("canceled SSH stream remained readable")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not unblock SSH read")
	}
	if err = stream.Close(); err != nil {
		t.Fatal(err)
	}
	writeKnown(jump.signer.PublicKey())
	if _, err = open(ctx, profiles, route, false); !errors.Is(err, ErrSSH) {
		t.Fatalf("changed known key accepted: %v", err)
	}
	writeKnown(target.signer.PublicKey())
	retargeted := strings.Replace(text, " Port "+targetPort+"\n", " Port "+jumpPort+"\n", 1)
	if err = os.WriteFile(config, []byte(retargeted), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = open(ctx, profiles, route, false); !errors.Is(err, ErrTargetChanged) {
		t.Fatalf("alias retarget reused an existing device grant: %v", err)
	}
	if _, err = proxyDeniedFixture(ctx, route, text); !errors.Is(err, ErrHostConfig) {
		t.Fatalf("jump with unsafe host checking accepted: %v", err)
	}
}

func proxyDeniedFixture(ctx context.Context, route connector, prior string) (string, error) {
	text := strings.Replace(prior, "Host fixture-jump\n", "Host fixture-jump\n StrictHostKeyChecking no\n", 1)
	if err := os.WriteFile(route.config, []byte(text), 0600); err != nil {
		return "", err
	}
	return route.identity(ctx)
}
