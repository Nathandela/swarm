package voice

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/voiceclient"
	"github.com/Nathandela/swarm/internal/voicewire"
	"golang.org/x/crypto/ssh"
)

func TestVoiceDeviceEndpointOverPinnedSSH(t *testing.T) {
	dir := privateServerDir(t)
	cfg := testConfig()
	cfg.DeviceListen = "127.0.0.1:0"
	refs := ManagerBridgeRefs{Socket: filepath.Join(dir, "bridge.sock"), AuthFile: filepath.Join(dir, managerBridgeAuth)}
	native := bootstrapNativeFixture(cfg, refs)
	native.runtime.ReadyForInput = true
	native.runtime.Turn.State = "idle"
	nativeSends := make(chan protocol.ComposerSendReq, 8)
	controller := &controllerRuntimeFixture{status: native.runtime, receipts: map[string]protocol.SemanticMessageReceipt{}}
	controller.beforeSend = func(_ string, req protocol.ComposerSendReq) { nativeSends <- req }
	runtime := &serviceRuntimeFixture{bootstrapFixture: native, controllerRuntimeFixture: controller}
	ctx, stop := context.WithTimeout(context.Background(), 20*time.Second)
	var providerCalls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	preparePTT := func() (pttTranscriber, error) {
		return func(ctx context.Context, _ []byte) (string, error) {
			if providerCalls.Add(1) == 2 {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return "", ctx.Err()
				}
			}
			return "Canonical device transcript", nil
		}, nil
	}
	done := make(chan error, 1)
	go func() { done <- serveManager(ctx, dir, "endpoint", cfg, runtime, preparePTT, nil) }()
	defer func() {
		stop()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	owner := NewManagerClient(dir)
	defer owner.Close()
	var address string
	for ctx.Err() == nil {
		var err error
		if address, err = owner.DeviceEndpoint(ctx); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !validDeviceEndpoint(address, false) {
		t.Fatal("private discovery did not return an actual loopback port")
	}
	sshCfg, sshKey := deviceSSHFixture(t, address)
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherPublic, otherPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	client, err := voiceclient.NewDeviceClient(ctx, sshCfg, sshKey, private)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	other, err := voiceclient.NewDeviceClient(ctx, sshCfg, sshKey, otherPrivate)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	invitation, err := owner.IssueDeviceEnrollment(ctx, public)
	if err != nil || invitation.PlaceID != cfg.PlaceID || invitation.Endpoint != address || invitation.Reference == "" {
		t.Fatalf("invalid invitation fields: %v", err)
	}
	if _, err := other.Enroll(ctx, invitation); err == nil {
		t.Fatal("wrong Ed25519 device enrolled")
	}
	grant, err := client.Enroll(ctx, invitation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Enroll(ctx, invitation); err == nil {
		t.Fatal("enrollment reference replayed")
	}
	wrongPlace := grant
	wrongPlace.PlaceID = "different"
	if _, err := client.Status(ctx, wrongPlace); err == nil {
		t.Fatal("grant accepted across places")
	}
	wrongPin := sshCfg
	wrongPin.HostKeySHA256 = ssh.FingerprintSHA256(testDeviceSSHSigner(t).PublicKey())
	foreign, err := voiceclient.NewDeviceClient(ctx, wrongPin, sshKey, private)
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	if _, err := foreign.Status(ctx, grant); !errors.Is(err, voiceclient.ErrHostKey) {
		t.Fatalf("wrong host pin = %v", err)
	}
	if state, err := client.Status(ctx, grant); err != nil || !state.Ready || state.PlaceID != cfg.PlaceID {
		t.Fatalf("authorized status = %+v, %v", state, err)
	}
	first, err := client.SubmitText(ctx, grant, "same-id", "device one")
	if err != nil {
		t.Fatal(err)
	}
	var send protocol.ComposerSendReq
	select {
	case send = <-nativeSends:
		if send.Text != "device one" || send.ManagerAdmission == nil {
			t.Fatalf("unexpected manager input: %v", send.Text)
		}
	case <-ctx.Done():
		t.Fatal("device text did not reach the manager")
	}
	for {
		admission, err := client.Receipt(ctx, grant, "same-id")
		if err != nil {
			t.Fatal(err)
		}
		if admission.Phase == "submitted" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	nonce := send.ManagerAdmission.Nonce
	controller.mu.Lock()
	controller.status.ReadyForInput = false
	controller.status.Turn = protocol.ManagerNativeTurn{State: "running", NativeInputID: nonce, NativeTurnID: "device-turn"}
	controller.proof = protocol.ManagerCallProof{Main: true, NativeInputID: nonce, NativeTurnID: "device-turn", NativeCallID: "device-call"}
	controller.mu.Unlock()
	bridge, err := NewManagerBridgeClient(refs.Socket, refs.AuthFile, cfg.ConversationID, "manager")
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	if _, err := bridge.Call(ctx, ManagerToolCall{Name: "publish", Arguments: json.RawMessage(`{"output_id":"device-publication","sequence":1,"text":"Exact manager text.","final":true}`)}); err != nil {
		t.Fatal(err)
	}
	if publications, err := client.Publications(ctx, grant, "same-id"); err != nil || len(publications) != 1 || publications[0].Text != "Exact manager text." {
		t.Fatalf("same-device publications = %+v, %v", publications, err)
	}
	invitation, err = owner.IssueDeviceEnrollment(ctx, otherPublic)
	if err != nil {
		t.Fatal(err)
	}
	otherGrant, err := other.Enroll(ctx, invitation)
	if err != nil {
		t.Fatal(err)
	}
	if status, err := other.Status(ctx, otherGrant); err != nil || status.AdmissionSequence != 0 {
		t.Fatalf("another device received the first admission: %+v (%v)", status, err)
	}
	if status, err := client.Status(ctx, grant); err != nil || status.AdmissionSequence != first.Sequence || status.Generation == 0 {
		t.Fatalf("own status replay missed durable admission: %+v (%v)", status, err)
	}
	if _, err := other.Receipt(ctx, otherGrant, "same-id"); err == nil {
		t.Fatal("second device saw first device's receipt")
	}
	if _, err := other.Publications(ctx, otherGrant, "same-id"); err == nil {
		t.Fatal("second device saw first device's publications")
	}
	second, err := other.SubmitText(ctx, otherGrant, "same-id", "device two")
	if err != nil || first.Sequence == second.Sequence {
		t.Fatalf("separate device ID namespace: %+v, %v", second, err)
	}

	client.Close()
	if _, err := client.Status(ctx, grant); err == nil {
		t.Fatal("closed device transport reopened")
	}
	client, err = voiceclient.NewDeviceClient(ctx, sshCfg, sshKey, private)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if result, err := client.Receipt(ctx, grant, "same-id"); err != nil || result.Sequence != first.Sequence {
		t.Fatalf("reconnected receipt = %+v, %v", result, err)
	}

	transport := &http.Transport{DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
		return voiceclient.DialSSH(ctx, sshCfg, sshKey)
	}}
	defer transport.CloseIdleConnections()
	raw := &http.Client{Transport: transport}
	var versionChallenge voicewire.DeviceChallenge
	if code := postDeviceFixture(t, ctx, raw, "/v1/device/challenge", grant, &versionChallenge); code != http.StatusOK {
		t.Fatal(code)
	}
	wrongMajor, _ := json.Marshal(voicewire.ClientMessage{Type: voicewire.TypeHandshake, Version: 2, Sequence: 1, SessionID: grant.DeviceID + ".00112233445566778899aabbccddeeff"})
	versionProof := voicewire.DeviceProof{Credential: grant, Challenge: versionChallenge.Challenge}
	versionProof.Signature = ed25519.Sign(private, voicewire.DeviceSignedBytes(versionProof, "session", wrongMajor))
	if code := postDeviceFixture(t, ctx, raw, "/v1/device/session", voicewire.DeviceRequest{Proof: versionProof, Body: wrongMajor}, nil); code != http.StatusBadRequest {
		t.Fatal("unsupported signed protocol accepted", code)
	}
	const audioID = "audio-stable"
	audio := []byte{1, 2, 3, 4}
	if capture, err := client.BeginPTT(ctx, grant, audioID); err != nil || capture.Phase != "capturing" {
		t.Fatalf("device PTT begin = %+v, %v", capture, err)
	}
	lease, err := owner.Capture(ctx)
	if err != nil || lease.ID == "" || lease.Source != "device:"+grant.DeviceID+":ptt" {
		t.Fatalf("owner cannot inspect lease: %+v %v", lease, err)
	}
	if _, err := owner.BeginPTT(ctx, "owner-busy"); !errors.Is(err, ErrCaptureBusy) {
		t.Fatalf("owner acquired without takeover: %v", err)
	}
	if _, err := other.BeginPTT(ctx, otherGrant, "other-busy"); err == nil {
		t.Fatal("other device bypassed capture lease")
	}
	if st, err := other.Status(ctx, otherGrant); err != nil || st.Capture != "busy" {
		t.Fatalf("passive status: %+v, %v", st, err)
	}
	passive, err := voiceclient.NewDeviceClient(ctx, sshCfg, sshKey, private)
	if err != nil {
		t.Fatal(err)
	}
	defer passive.Close()
	if _, err := passive.FinalizePTT(ctx, grant, audioID, audio); err == nil || providerCalls.Load() != 0 {
		t.Fatal("passive same-grant transport submitted audio", err)
	}
	if err := passive.EndPTT(ctx, grant, audioID); err == nil {
		t.Fatal("passive same-grant transport ended capture")
	}
	if _, err := other.SubmitText(ctx, otherGrant, "while-speaking", "text remains independent"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.SubmitText(ctx, grant, audioID, "text cannot overwrite capture"); err == nil {
		t.Fatal("device text overwrote an existing PTT capture")
	}
	if code := postDeviceAudioFixture(t, ctx, raw, grant, private, audioID, audio, []byte{3, 4}); code != http.StatusForbidden || providerCalls.Load() != 0 {
		t.Fatalf("changed signed PCM reached the provider (HTTP %d, %d calls)", code, providerCalls.Load())
	}
	audioReceipt, err := client.FinalizePTT(ctx, grant, audioID, audio)
	if err != nil {
		t.Fatal(err)
	}
	if repeat, err := client.FinalizePTT(ctx, grant, audioID, audio); err != nil || repeat.Sequence != audioReceipt.Sequence || providerCalls.Load() != 1 {
		t.Fatalf("exact device PTT retry = %+v, %v; calls=%d", repeat, err, providerCalls.Load())
	}
	if _, err := other.Receipt(ctx, otherGrant, audioID); err == nil {
		t.Fatal("a different device saw the PTT receipt")
	}
	if _, err := passive.BeginPTT(ctx, grant, "discarded"); err != nil {
		t.Fatal(err)
	}
	if err := passive.EndPTT(ctx, grant, "discarded"); err != nil {
		t.Fatal(err)
	}
	if _, err := passive.BeginPTT(ctx, grant, "close-discards"); err != nil {
		t.Fatal(err)
	}
	passive.Close()
	if current, err := owner.Capture(ctx); err != nil || current.ID != "" {
		t.Fatalf("close retained capture: %+v %v", current, err)
	}
	if _, err := other.BeginPTT(ctx, otherGrant, "owner-takeover"); err != nil {
		t.Fatal(err)
	}
	previous, err := owner.Capture(ctx)
	if err != nil || previous.ID == "" {
		t.Fatal("missing host lease", err)
	}
	if _, err := owner.TakeCapture(ctx, previous.ID, "local-takeover"); err != nil {
		t.Fatal("owner takeover", err)
	}
	if err := owner.EndCapture(ctx, previous.ID); !errors.Is(err, ErrStale) {
		t.Fatal("stale end released replacement", err)
	}
	if _, err := other.FinalizePTT(ctx, otherGrant, "owner-takeover", audio); err == nil || providerCalls.Load() != 1 {
		t.Fatal("superseded capture transmitted", err)
	}
	replacement, err := owner.Capture(ctx)
	if err != nil || replacement.ID == previous.ID || replacement.Source != localPTTDevice {
		t.Fatal("missing local takeover", err)
	}
	if err := owner.EndCapture(ctx, replacement.ID); err != nil {
		t.Fatal("end replacement", err)
	}
	if _, err := client.BeginPTT(ctx, grant, "blocked-audio"); err != nil {
		t.Fatal(err)
	}
	finalized := make(chan error, 1)
	go func() { _, err := client.FinalizePTT(ctx, grant, "blocked-audio", audio); finalized <- err }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("test provider did not begin")
	}
	var challenge voicewire.DeviceChallenge
	if code := postDeviceFixture(t, ctx, raw, "/v1/device/challenge", grant, &challenge); code != http.StatusOK {
		t.Fatalf("raw challenge status = %d", code)
	}
	body := json.RawMessage(`{"id":"after-revoke","text":"must never run"}`)
	proof := voicewire.DeviceProof{Credential: grant, Challenge: challenge.Challenge}
	proof.Signature = ed25519.Sign(private, voicewire.DeviceSignedBytes(proof, "text", body))
	revoked := make(chan error, 1)
	go func() { revoked <- owner.RevokeDevice(ctx, grant.DeviceID) }()
	select {
	case err := <-revoked:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("transcription held the registry lock across the external call")
	}
	close(release)
	grants, err := owner.ListDevices(ctx)
	if err != nil || len(grants) != 2 || grants[0].DeviceID >= grants[1].DeviceID {
		t.Fatalf("unexpected owner grant inventory: %v (%v)", grants, err)
	}
	want := map[string]DeviceGrantInfo{
		grant.DeviceID:      {grant.DeviceID, hashBytes(public), "revoked"},
		otherGrant.DeviceID: {otherGrant.DeviceID, hashBytes(otherPublic), "active"},
	}
	for _, item := range grants {
		if item != want[item.DeviceID] {
			t.Fatalf("unexpected projected grant: %+v", item)
		}
	}
	var wire []map[string]json.RawMessage
	if err := owner.callJSON(ctx, http.MethodGet, "/v1/devices/list", nil, &wire); err != nil || len(wire) != 2 {
		t.Fatal("owner inventory response", err)
	}
	for _, row := range wire {
		if len(row) != 3 || row["device_id"] == nil || row["signing_key_sha256"] == nil || row["status"] == nil {
			t.Fatal("owner inventory included unexpected fields")
		}
	}
	if code := postDeviceFixture(t, ctx, raw, "/v1/devices/list", otherGrant, nil); code != http.StatusNotFound {
		t.Fatalf("private inventory reachable from device endpoint: HTTP %d", code)
	}
	select {
	case err := <-finalized:
		if err == nil || providerCalls.Load() != 2 {
			t.Fatalf("revoked audio admitted: %v; calls=%d", err, providerCalls.Load())
		}
	case <-ctx.Done():
		t.Fatal("revoked transcription did not return")
	}
	_, blocked := deviceUtterance(grant.DeviceID, "blocked-audio")
	if _, err := owner.Receipt(ctx, blocked); !errors.Is(err, ErrPTTUnconfirmed) {
		t.Fatalf("revoked transcript affected the manager ledger: %v", err)
	}
	if code := postDeviceFixture(t, ctx, raw, "/v1/device/text", voicewire.DeviceRequest{Proof: proof, Body: body}, nil); code != http.StatusForbidden {
		t.Fatalf("pre-revocation proof still accepted (HTTP %d)", code)
	}
	if code := postDeviceFixture(t, ctx, raw, "/v1/recovery/status", grant, nil); code != http.StatusNotFound {
		t.Fatalf("device route exposed recovery authority (HTTP %d)", code)
	}
	hash := hashBytes([]byte("device:" + grant.DeviceID + "\x00after-revoke"))
	if _, err := owner.Receipt(ctx, hash); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked action reached the durable ledger: %v", err)
	}
}

func TestVoiceDeviceListenRefusesNonLoopback(t *testing.T) {
	for _, address := range []string{"0.0.0.0:0", "localhost:0", "[::]:0", "[::ffff:127.0.0.1]:0"} {
		cfg := testConfig()
		cfg.DeviceListen = address
		if cfg.Validate() == nil {
			t.Fatalf("accepted %q", address)
		}
	}
}

func postDeviceFixture(t *testing.T, ctx context.Context, client *http.Client, path string, body, result any) int {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://voice-device"+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if result != nil && response.StatusCode == http.StatusOK {
		if err := json.NewDecoder(response.Body).Decode(result); err != nil {
			t.Fatal(err)
		}
	}
	return response.StatusCode
}

func testDeviceSSHSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func deviceSSHFixture(t *testing.T, destination string) (voiceclient.SSHConfig, ssh.Signer) {
	t.Helper()
	host, user := testDeviceSSHSigner(t), testDeviceSSHSigner(t)
	server := &ssh.ServerConfig{PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if meta.User() != "fixture" || !bytes.Equal(user.PublicKey().Marshal(), key.Marshal()) {
			return nil, errors.New("fixture user denied")
		}
		return nil, nil
	}}
	server.AddHostKey(host)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	var sessions sync.WaitGroup
	go func() {
		defer close(done)
		defer sessions.Wait()
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			sessions.Go(func() {
				closeRaw := context.AfterFunc(ctx, func() { _ = raw.Close() })
				defer closeRaw()
				defer func() { _ = raw.Close() }()
				serverConn, channels, requests, err := ssh.NewServerConn(raw, server)
				if err != nil {
					return
				}
				defer func() { _ = serverConn.Close() }()
				go ssh.DiscardRequests(requests)
				remote := netip.MustParseAddrPort(destination)
				for request := range channels {
					var forward struct {
						Host       string
						Port       uint32
						Origin     string
						OriginPort uint32
					}
					if request.ChannelType() != "direct-tcpip" || ssh.Unmarshal(request.ExtraData(), &forward) != nil ||
						forward.Host != remote.Addr().String() || forward.Port != uint32(remote.Port()) {
						_ = request.Reject(ssh.Prohibited, "fixture denied destination")
						continue
					}
					channel, channelRequests, err := request.Accept()
					if err != nil {
						break
					}
					go ssh.DiscardRequests(channelRequests)
					copyDeviceSSH(channel, destination)
				}
			})
		}
	}()
	t.Cleanup(func() {
		stop()
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("SSH device fixture did not close")
		}
	})
	return voiceclient.SSHConfig{Address: listener.Addr().String(), User: "fixture", HostKeySHA256: ssh.FingerprintSHA256(host.PublicKey()), RemoteVoiceAddress: destination}, user
}

func copyDeviceSSH(channel ssh.Channel, destination string) {
	defer func() { _ = channel.Close() }()
	target, err := net.DialTimeout("tcp", destination, time.Second)
	if err != nil {
		return
	}
	defer func() { _ = target.Close() }()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(target, channel)
		_ = target.Close()
	}()
	_, _ = io.Copy(channel, target)
	_ = channel.Close()
	<-done
}

func postDeviceAudioFixture(t *testing.T, ctx context.Context, client *http.Client, credential voicewire.DeviceCredential, signer ed25519.PrivateKey, id string, signed, sent []byte) int {
	t.Helper()
	var challenge voicewire.DeviceChallenge
	if code := postDeviceFixture(t, ctx, client, "/v1/device/challenge", credential, &challenge); code != http.StatusOK {
		t.Fatalf("PTT proof challenge status = %d", code)
	}
	proof := voicewire.DeviceProof{Credential: credential, Challenge: challenge.Challenge}
	proof.Signature = ed25519.Sign(signer, voicewire.DeviceSignedBytes(proof, voicewire.DeviceFinalizeAction(id, "00112233445566778899aabbccddeeff"), signed))
	raw, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://voice-device/v1/device/ptt/finalize", bytes.NewReader(sent))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Swarm-Utterance-ID", id)
	req.Header.Set("Swarm-Capture-Session", "00112233445566778899aabbccddeeff")
	req.Header.Set("Swarm-Device-Proof", base64.RawURLEncoding.EncodeToString(raw))
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	return response.StatusCode
}
