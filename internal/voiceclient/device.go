package voiceclient

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"time"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/voicewire"
	"golang.org/x/crypto/ssh"
)

// ErrPublicationCapacity means the reply exceeded the aggregate encoded limit.
var ErrPublicationCapacity = errors.New("voice: device publication byte limit reached")

// DeviceClient speaks the signed device HTTP protocol over an authenticated SSH transport.
type DeviceClient struct {
	http     *http.Client
	signer   crypto.Signer
	cancel   context.CancelFunc
	gate     chan struct{}
	endpoint string
}

// NewDeviceClient requires the lifetime of the app-owned transport and separate
// device and SSH signers. No key is loaded from disk.
func NewDeviceClient(ctx context.Context, cfg SSHConfig, sshSigner ssh.Signer, signer crypto.Signer) (*DeviceClient, error) {
	if sshSigner == nil || !validSSHConfig(cfg) {
		return nil, ErrSSHConfig
	}
	return NewDeviceClientWithDial(ctx, cfg.RemoteVoiceAddress, signer, func(lifetime context.Context) (net.Conn, error) {
		return DialSSH(lifetime, cfg, sshSigner)
	})
}

// NewDeviceClientWithDial reuses the device protocol with an authenticated SSH
// direct-forward dialer. Its context owns each stream for the client's lifetime.
func NewDeviceClientWithDial(ctx context.Context, endpoint string, signer crypto.Signer, dial func(context.Context) (net.Conn, error)) (*DeviceClient, error) {
	target, err := netip.ParseAddrPort(endpoint)
	if ctx == nil || signer == nil || dial == nil || err != nil || target.String() != endpoint || target.Port() == 0 ||
		(target.Addr() != netip.AddrFrom4([4]byte{127, 0, 0, 1}) && target.Addr() != netip.IPv6Loopback()) {
		return nil, ErrSSHConfig
	}
	if public, ok := signer.Public().(ed25519.PublicKey); !ok || len(public) != ed25519.PublicKeySize {
		return nil, errors.New("voice: an Ed25519 device signer is required")
	}
	lifetime, cancel := context.WithCancel(ctx)
	transport := &http.Transport{
		MaxConnsPerHost: 1, IdleConnTimeout: 10 * time.Second,
		DialContext: func(_ context.Context, network, address string) (net.Conn, error) {
			if network != "tcp" || address != "voice-device:80" {
				return nil, ErrSSHConfig
			}
			return dial(lifetime)
		},
	}
	return &DeviceClient{
		http: &http.Client{Transport: transport, Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
		signer: signer, cancel: cancel, gate: make(chan struct{}, 1), endpoint: endpoint,
	}, nil
}

func (c *DeviceClient) Close() {
	c.cancel()
	c.http.CloseIdleConnections()
}

// Enroll proves the prebound device identity and consumes a one-use invitation.
func (c *DeviceClient) Enroll(ctx context.Context, invitation voicewire.DeviceInvitation) (voicewire.DeviceCredential, error) {
	var grant voicewire.DeviceCredential
	if invitation.Endpoint != c.endpoint || invitation.PlaceID == "" || len(invitation.Reference) != 64 {
		return grant, errors.New("voice: invitation does not match the SSH voice endpoint")
	}
	err := c.signed(ctx, voicewire.DeviceCredential{PlaceID: invitation.PlaceID, Token: invitation.Reference}, "enroll", nil, &grant)
	if err == nil && (grant.PlaceID != invitation.PlaceID || grant.Generation == 0 || grant.DeviceID == "" || grant.Token == "") {
		err = errors.New("voice: invalid device enrollment receipt")
	}
	return grant, err
}

func (c *DeviceClient) Status(ctx context.Context, credential voicewire.DeviceCredential) (voicewire.DeviceStatus, error) {
	var status voicewire.DeviceStatus
	err := c.signed(ctx, credential, "status", nil, &status)
	if err == nil && (status.PlaceID != credential.PlaceID || status.ConversationID == "") {
		err = errors.New("voice: invalid device status")
	}
	return status, err
}

func (c *DeviceClient) SubmitText(ctx context.Context, credential voicewire.DeviceCredential, id, text string) (voicewire.DeviceAdmission, error) {
	return c.admission(ctx, credential, "text", voicewire.DeviceText{ID: id, Text: text})
}

func (c *DeviceClient) Receipt(ctx context.Context, credential voicewire.DeviceCredential, id string) (voicewire.DeviceAdmission, error) {
	return c.admission(ctx, credential, "receipt", voicewire.DeviceText{ID: id})
}

func (c *DeviceClient) admission(ctx context.Context, credential voicewire.DeviceCredential, action string, body voicewire.DeviceText) (voicewire.DeviceAdmission, error) {
	var admission voicewire.DeviceAdmission
	err := c.signed(ctx, credential, action, body, &admission)
	if err == nil && (admission.ID != body.ID || admission.Sequence == 0) {
		err = errors.New("voice: invalid device admission receipt")
	}
	return admission, err
}

func (c *DeviceClient) signed(ctx context.Context, credential voicewire.DeviceCredential, action string, value any, result any) error {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	select {
	case c.gate <- struct{}{}:
		defer func() { <-c.gate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	var body []byte
	if value != nil {
		var err error
		body, err = json.Marshal(value)
		if err != nil {
			return err
		}
	}
	proof, err := c.makeProof(ctx, credential, action, body)
	if err != nil {
		return err
	}
	if action == "enroll" {
		return c.call(ctx, action, proof, result)
	}
	return c.call(ctx, action, voicewire.DeviceRequest{Proof: proof, Body: body}, result)
}

func (c *DeviceClient) makeProof(ctx context.Context, credential voicewire.DeviceCredential, action string, body []byte) (voicewire.DeviceProof, error) {
	var challenge voicewire.DeviceChallenge
	if err := c.call(ctx, "challenge", credential, &challenge); err != nil {
		return voicewire.DeviceProof{}, err
	}
	if len(challenge.Challenge) != 64 {
		return voicewire.DeviceProof{}, errors.New("voice: invalid device challenge")
	}
	proof := voicewire.DeviceProof{Credential: credential, Challenge: challenge.Challenge}
	signature, err := c.signer.Sign(rand.Reader, voicewire.DeviceSignedBytes(proof, action, body), crypto.Hash(0))
	if err != nil {
		return voicewire.DeviceProof{}, errors.New("voice: device signer failed")
	}
	proof.Signature = signature
	return proof, nil
}

func (c *DeviceClient) BeginPTT(ctx context.Context, credential voicewire.DeviceCredential, id string) (voicewire.DeviceCapture, error) {
	var capture voicewire.DeviceCapture
	err := c.signed(ctx, credential, "ptt/begin", voicewire.DeviceText{ID: id}, &capture)
	if err == nil && (capture.ID != id || capture.Phase == "") {
		err = errors.New("voice: invalid device capture receipt")
	}
	return capture, err
}

// FinalizePTT sends one explicitly bounded PCM16LE utterance. Resolve an ambiguous
// reply with Receipt; no automatic retry starts a second transcription.
func (c *DeviceClient) FinalizePTT(ctx context.Context, credential voicewire.DeviceCredential, id string, pcm []byte) (voicewire.DeviceAdmission, error) {
	var result voicewire.DeviceAdmission
	if id == "" || len(id) > 128 || len(pcm) == 0 || len(pcm) > voicewire.MaxDevicePCMBytes || len(pcm)%2 != 0 {
		return result, errors.New("voice: invalid device utterance")
	}
	ctx, cancel := context.WithTimeout(ctx, 42*time.Second)
	defer cancel()
	select {
	case c.gate <- struct{}{}:
		defer func() { <-c.gate }()
	case <-ctx.Done():
		return result, ctx.Err()
	}
	proof, err := c.makeProof(ctx, credential, voicewire.DeviceFinalizeAction(id), pcm)
	if err != nil {
		return result, err
	}
	raw, err := json.Marshal(proof)
	if err != nil {
		return result, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://voice-device/v1/device/ptt/finalize", bytes.NewReader(pcm))
	if err != nil {
		return result, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Swarm-Utterance-ID", id)
	req.Header.Set("Swarm-Device-Proof", base64.RawURLEncoding.EncodeToString(raw))
	if err := c.perform(req, &result); err != nil {
		return voicewire.DeviceAdmission{}, err
	}
	if result.ID != id || result.Sequence == 0 {
		return voicewire.DeviceAdmission{}, errors.New("voice: invalid device admission receipt")
	}
	return result, nil
}

func (c *DeviceClient) Publications(ctx context.Context, credential voicewire.DeviceCredential, id string) ([]voicewire.DevicePublication, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	pages := make([]voicewire.DevicePublication, 0)
	const maxEncoded = 128 << 10
	encodedBytes := len("[]")
	query := voicewire.DevicePublicationQuery{ID: id}
	for len(pages) < 10_000 {
		var page voicewire.DevicePublicationPage
		if err := c.signed(ctx, credential, "publications", query, &page); err != nil {
			return nil, err
		}
		if page.Item == nil {
			return pages, nil
		}
		if page.Item.ID == "" || page.Item.Sequence <= query.After || len(page.Text) == 0 || len(page.Text) > 64<<10 || !utf8.Valid(page.Text) {
			return nil, errors.New("voice: invalid device publication page")
		}
		page.Item.Text = string(page.Text)
		encoded, err := json.Marshal(page.Item)
		if err != nil {
			return nil, err
		}
		if len(pages) != 0 {
			encodedBytes++ // JSON array separator
		}
		if len(encoded) > maxEncoded-encodedBytes {
			return nil, ErrPublicationCapacity
		}
		encodedBytes += len(encoded)
		pages = append(pages, *page.Item)
		query.After = page.Item.Sequence
	}
	return nil, errors.New("voice: device publication limit reached")
}

func (c *DeviceClient) call(ctx context.Context, action string, value, result any) error {
	raw, err := json.Marshal(value)
	const limit = 128 << 10
	if err != nil || len(raw) > limit {
		return errors.New("voice: invalid device request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://voice-device/v1/device/"+action, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.perform(req, result)
}

func (c *DeviceClient) perform(req *http.Request, result any) error {
	const limit = 128 << 10
	response, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return &DeviceHTTPError{StatusCode: response.StatusCode}
	}
	reply, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || len(reply) > limit || response.Header.Get("Content-Type") != "application/json" || json.Unmarshal(reply, result) != nil {
		return errors.New("voice: invalid device response")
	}
	return nil
}

// DeviceHTTPError carries a bounded device HTTP refusal without remote body text.
type DeviceHTTPError struct{ StatusCode int }

func (e *DeviceHTTPError) Error() string {
	return fmt.Sprintf("voice: device request failed (HTTP %d)", e.StatusCode)
}
