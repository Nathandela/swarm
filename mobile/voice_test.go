package swarmmobile

import (
	"bytes"
	"crypto"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/voiceclient"
	"github.com/Nathandela/swarm/internal/voicewire"
	"golang.org/x/crypto/ssh"
)

type voiceTestKey struct {
	key   ed25519.PrivateKey
	calls atomic.Int64
}

func newVoiceTestKey(t *testing.T) *voiceTestKey {
	t.Helper()
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &voiceTestKey{key: key}
}

func (k *voiceTestKey) VoiceSSHPublicKey() string {
	return base64.StdEncoding.EncodeToString(k.key.Public().(ed25519.PublicKey))
}
func (k *voiceTestKey) VoiceEnrollmentPublicKey() string { return k.VoiceSSHPublicKey() }
func (k *voiceTestKey) SignVoiceSSH(value string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return "", err
	}
	k.calls.Add(1)
	return base64.StdEncoding.EncodeToString(ed25519.Sign(k.key, data)), nil
}
func (k *voiceTestKey) SignVoiceEnrollment(value string) (string, error) {
	return k.SignVoiceSSH(value)
}

func voiceTestTunnel(t *testing.T, caller ssh.PublicKey, host ssh.Signer, target string) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	config := &ssh.ServerConfig{PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if meta.User() != "fixture" || !bytes.Equal(caller.Marshal(), key.Marshal()) {
			return nil, errors.New("invalid test SSH identity")
		}
		return nil, nil
	}}
	config.AddHostKey(host)
	go func() {
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = raw.Close() }()
				conn, channels, requests, err := ssh.NewServerConn(raw, config)
				if err != nil {
					return
				}
				defer conn.Close()
				go ssh.DiscardRequests(requests)
				for incoming := range channels {
					if incoming.ChannelType() != "direct-tcpip" {
						_ = incoming.Reject(ssh.UnknownChannelType, "direct only")
						continue
					}
					var forward struct {
						Host       string
						Port       uint32
						Source     string
						SourcePort uint32
					}
					if err := ssh.Unmarshal(incoming.ExtraData(), &forward); err != nil || forward.Host != "127.0.0.1" || forward.Port != voiceTestPort(target) {
						_ = incoming.Reject(ssh.Prohibited, "wrong endpoint")
						continue
					}
					peer, err := net.DialTimeout("tcp", target, time.Second)
					if err != nil {
						_ = incoming.Reject(ssh.ConnectionFailed, "fixture endpoint unavailable")
						continue
					}
					stream, reqs, err := incoming.Accept()
					if err != nil {
						_ = peer.Close()
						continue
					}
					go ssh.DiscardRequests(reqs)
					go func() {
						defer func() { _ = peer.Close() }()
						defer func() { _ = stream.Close() }()
						go func() {
							_, _ = io.Copy(peer, stream)
							_ = peer.Close()
						}()
						_, _ = io.Copy(stream, peer)
					}()
				}
			}()
		}
	}()
	return listener.Addr().String()
}

func voiceTestPort(address string) uint32 {
	_, port, _ := net.SplitHostPort(address)
	n, _ := net.LookupPort("tcp", port)
	return uint32(n)
}

func TestVoiceDeviceSeparatesSSHAndGrantAndEndsStalledForward(t *testing.T) {
	sshKey, grantKey, hostKey := newVoiceTestKey(t), newVoiceTestKey(t), newVoiceTestKey(t)
	userPublic, _ := ssh.NewPublicKey(sshKey.key.Public())
	host, _ := ssh.NewSignerFromKey(hostKey.key)
	grant := voicewire.DeviceCredential{PlaceID: "fixture", DeviceID: strings.Repeat("a", 32), Generation: 1, Token: strings.Repeat("b", 64)}
	invite := strings.Repeat("c", 64)
	challenge := strings.Repeat("d", 64)
	var enrolled, revoked, stall, oversized atomic.Bool
	entered := make(chan struct{}, 1)
	confirmed := make(chan struct{}, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		action := strings.TrimPrefix(r.URL.Path, "/v1/device/")
		respond := func(value any) { _ = json.NewEncoder(w).Encode(value) }
		deny := func() { w.WriteHeader(http.StatusUnauthorized) }
		switch action {
		case "challenge":
			var c voicewire.DeviceCredential
			if json.NewDecoder(r.Body).Decode(&c) != nil || revoked.Load() || (c != grant && (c != (voicewire.DeviceCredential{PlaceID: grant.PlaceID, Token: invite}) || enrolled.Load())) {
				deny()
				return
			}
			respond(voicewire.DeviceChallenge{Challenge: challenge})
		case "enroll":
			var proof voicewire.DeviceProof
			if json.NewDecoder(r.Body).Decode(&proof) != nil || proof.Challenge != challenge || proof.Credential.Token != invite || !ed25519.Verify(grantKey.key.Public().(ed25519.PublicKey), voicewire.DeviceSignedBytes(proof, action, nil), proof.Signature) || enrolled.Swap(true) {
				deny()
				return
			}
			respond(grant)
		case "ptt/finalize":
			pcm, readErr := io.ReadAll(io.LimitReader(r.Body, 9))
			raw, decodeErr := base64.RawURLEncoding.DecodeString(r.Header.Get("Swarm-Device-Proof"))
			var proof voicewire.DeviceProof
			id := r.Header.Get("Swarm-Utterance-ID")
			if readErr != nil || decodeErr != nil || json.Unmarshal(raw, &proof) != nil || id != "operation" || !bytes.Equal(pcm, []byte{1, 2}) || proof.Credential != grant || proof.Challenge != challenge || !ed25519.Verify(grantKey.key.Public().(ed25519.PublicKey), voicewire.DeviceSignedBytes(proof, voicewire.DeviceFinalizeAction(id), pcm), proof.Signature) {
				deny()
				return
			}
			respond(voicewire.DeviceAdmission{ID: id, Sequence: 1, Phase: "queued"})
		case "status", "text", "receipt", "ptt/begin", "publications":
			var req voicewire.DeviceRequest
			if json.NewDecoder(r.Body).Decode(&req) != nil || req.Proof.Challenge != challenge || req.Proof.Credential != grant || revoked.Load() || !ed25519.Verify(grantKey.key.Public().(ed25519.PublicKey), voicewire.DeviceSignedBytes(req.Proof, action, req.Body), req.Proof.Signature) {
				deny()
				return
			}
			if action == "status" {
				if stall.Load() {
					entered <- struct{}{}
					<-r.Context().Done()
					confirmed <- struct{}{}
					return
				}
				respond(voicewire.DeviceStatus{PlaceID: grant.PlaceID, ConversationID: "conversation", Ready: true})
				return
			}
			if action == "publications" {
				var q voicewire.DevicePublicationQuery
				if json.Unmarshal(req.Body, &q) != nil || q.ID != "operation" {
					deny()
					return
				}
				if oversized.Load() {
					respond(voicewire.DevicePublicationPage{Item: &voicewire.DevicePublication{ID: "large", Sequence: q.After + 1}, Text: bytes.Repeat([]byte{'x'}, 64<<10)})
					return
				}
				if q.After == 0 {
					respond(voicewire.DevicePublicationPage{Item: &voicewire.DevicePublication{ID: "pub", Sequence: 1, Final: true}, Text: []byte("Canonical result")})
				} else {
					respond(voicewire.DevicePublicationPage{})
				}
				return
			}
			var body voicewire.DeviceText
			if json.Unmarshal(req.Body, &body) != nil || body.ID != "operation" || action == "text" && body.Text != "neutral text" {
				deny()
				return
			}
			if action == "ptt/begin" {
				respond(voicewire.DeviceCapture{ID: body.ID, Phase: "capturing"})
				return
			}
			respond(voicewire.DeviceAdmission{ID: body.ID, Sequence: 1, Phase: "queued"})
		default:
			deny()
		}
	})
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	target := httpServer.Listener.Addr().String()
	address := voiceTestTunnel(t, userPublic, host, target)
	pin := ssh.FingerprintSHA256(host.PublicKey())
	if _, err := NewVoiceDevice(address, "fixture", pin, target, sshKey, sshKey); !errors.Is(err, errVoiceInput) {
		t.Fatalf("same key accepted: %v", err)
	}
	if _, err := NewVoiceDevice(address, "fixture", pin, "192.0.2.1:9999", sshKey, grantKey); !errors.Is(err, errVoiceInput) {
		t.Fatalf("non-loopback forward accepted: %v", err)
	}
	wrong, err := NewVoiceDevice(address, "fixture", ssh.FingerprintSHA256(userPublic), target, sshKey, grantKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = wrong.Enroll("fixture", invite, target); !errors.Is(err, voiceclient.ErrHostKey) {
		t.Fatalf("wrong pin: %v", err)
	}
	_ = wrong.End()
	device, err := NewVoiceDevice(address, "fixture", pin, target, sshKey, grantKey)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = device.End() }()
	raw, err := device.Enroll("fixture", invite, target)
	if err != nil {
		t.Fatal(err)
	}
	var returned voiceGrant
	if json.Unmarshal([]byte(raw), &returned) != nil || returned.Credential != grant {
		t.Fatal("wrong scoped enrollment receipt")
	}
	if sshKey.calls.Load() == 0 || grantKey.calls.Load() == 0 {
		t.Fatal("both native signing paths should be exercised")
	}
	if _, err = wrong.Status(raw); !errors.Is(err, errVoiceInput) {
		t.Fatalf("other host profile accepted grant: %v", err)
	}
	if _, err = device.Enroll("fixture", invite, "127.0.0.1:1"); !errors.Is(err, errVoiceInput) {
		t.Fatalf("mismatched invitation endpoint: %v", err)
	}
	if _, err = device.Status(raw); err != nil {
		t.Fatal(err)
	}
	if _, err = device.Status(raw + " "); !errors.Is(err, errVoiceInput) {
		t.Fatalf("ambiguous grant JSON: %v", err)
	}
	if _, err = device.SubmitText(raw, "operation", "neutral text"); err != nil {
		t.Fatal(err)
	}
	if _, err = device.Receipt(raw, "operation"); err != nil {
		t.Fatal(err)
	}
	if _, err = device.BeginPTT(raw, "operation"); err != nil {
		t.Fatal(err)
	}
	if _, err = device.FinalizePTT(raw, "operation", "AQ=="); !errors.Is(err, errVoiceInput) {
		t.Fatalf("partial PCM passed binding: %v", err)
	}
	if _, err = device.FinalizePTT(raw, "operation", "AQI="); err != nil {
		t.Fatal(err)
	}
	if publications, err := device.Publications(raw, "operation"); err != nil || !strings.Contains(publications, "Canonical result") {
		t.Fatalf("missing canonical publication: %v", err)
	}
	oversized.Store(true)
	if _, err := device.Publications(raw, "operation"); err == nil || !strings.Contains(err.Error(), "voice publication history exceeds handset capacity") {
		t.Fatalf("missing explicit handset capacity error: %v", err)
	}
	oversized.Store(false)
	revoked.Store(true)
	if _, err = device.Status(raw); !errors.Is(err, errVoiceUnavailable) {
		t.Fatalf("revoked grant: %v", err)
	}
	revoked.Store(false)
	stall.Store(true)
	done := make(chan error, 1)
	go func() { _, err := device.Status(raw); done <- err }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("status did not reach stalled handler")
	}
	if err = device.End(); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if !errors.Is(err, errVoiceEnded) {
			t.Fatalf("ended status: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("End did not unblock status")
	}
	select {
	case <-confirmed:
	case <-time.After(3 * time.Second):
		t.Fatal("End did not close remote forward")
	}
	if _, err = device.Status(raw); !errors.Is(err, errVoiceEnded) {
		t.Fatalf("ended device reused: %v", err)
	}
	stall.Store(false)
	restored, err := NewVoiceDevice(address, "fixture", pin, target, sshKey, grantKey)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.End() }()
	if _, err := restored.Status(raw); err != nil {
		t.Fatalf("exact profile could not restore its grant: %v", err)
	}

}

func TestVoiceDeviceSigningBridgeRefusesInvalidOrPanickingCallback(t *testing.T) {
	key := newVoiceTestKey(t)
	public := key.key.Public().(ed25519.PublicKey)
	for _, callback := range []func(string) (string, error){
		func(string) (string, error) { return strings.Repeat("A", 88), nil },
		func(string) (string, error) { panic("synthetic protected payload") },
	} {
		signer := voiceSigner{key: public, sign: callback}
		if _, err := signer.Sign(nil, []byte("signed message"), crypto.Hash(0)); !errors.Is(err, errVoiceSign) {
			t.Fatalf("invalid signing result did not fail closed: %v", err)
		}
	}
}
