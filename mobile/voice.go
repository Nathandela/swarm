package swarmmobile

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/voiceclient"
	"github.com/Nathandela/swarm/internal/voicewire"
	"golang.org/x/crypto/ssh"
)

// VoiceSSHSigner supplies one app-owned Ed25519 SSH public key and signatures.
// The public key and raw signatures are standard padded Base64. The callback
// signs the decoded bytes verbatim. Private keys do not cross the binding.
type VoiceSSHSigner interface {
	VoiceSSHPublicKey() string
	SignVoiceSSH(messageBase64 string) (string, error)
}

// VoiceEnrollmentSigner supplies a different app-owned Ed25519 device key.
// It signs only the voice device protocol's canonical Base64-decoded bytes.
// The message includes a short-lived challenge and scoped credential. Do not log it.
type VoiceEnrollmentSigner interface {
	VoiceEnrollmentPublicKey() string
	SignVoiceEnrollment(messageBase64 string) (string, error)
}

// VoiceDevice owns one SSH profile's transport lifetime. It has no dependency
// on App, dashboard identity, or push registration.
type VoiceDevice struct {
	client   *voiceclient.DeviceClient
	lifetime context.Context
	cancel   context.CancelFunc
	endpoint string
	profile  string
}

type voiceSigner struct {
	key  ed25519.PublicKey
	sign func(string) (string, error)
}

func (s voiceSigner) Public() crypto.PublicKey { return ed25519.PublicKey(bytes.Clone(s.key)) }
func (s voiceSigner) Sign(_ io.Reader, message []byte, opts crypto.SignerOpts) (sig []byte, err error) {
	defer func() {
		if recover() != nil {
			sig, err = nil, errVoiceSign
		}
	}()
	if opts == nil || opts.HashFunc() != 0 || len(message) == 0 || len(message) > voicewire.MaxMessageBytes {
		return nil, errVoiceSign
	}
	raw, err := s.sign(base64.StdEncoding.EncodeToString(message))
	if err != nil {
		return nil, errVoiceSign
	}
	signature := voiceBase64(raw, ed25519.SignatureSize)
	if signature == nil || !ed25519.Verify(s.key, message, signature) {
		return nil, errVoiceSign
	}
	return signature, nil
}

var (
	errVoiceInput       = classed(ErrClassInvalidRequest, errors.New("swarmmobile: invalid voice profile, grant, or request"))
	errVoiceSign        = classed(ErrClassUnreconciled, errors.New("swarmmobile: voice signing failed"))
	errVoiceEnded       = classed(ErrClassClosed, errors.New("swarmmobile: voice device ended"))
	errVoiceUnavailable = classed(ErrClassUnreconciled, errors.New("swarmmobile: voice device request unavailable or refused"))
)

func voiceBase64(value string, size int) []byte {
	if len(value) != base64.StdEncoding.EncodedLen(size) {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(raw) != size || base64.StdEncoding.EncodeToString(raw) != value {
		return nil
	}
	return raw
}

func voicePublicKey(public func() string) (key ed25519.PublicKey) {
	defer func() { _ = recover() }()
	return voiceBase64(public(), ed25519.PublicKeySize)
}

// NewVoiceDevice accepts a host:port, SSH username, OpenSSH SHA256 host
// fingerprint and exact remote loopback endpoint. Network calls are synchronous
// and belong off the UI thread. Signers must be safe to call from worker threads.
func NewVoiceDevice(address, user, hostKeySHA256, remoteVoiceAddress string, sshKey VoiceSSHSigner, enrollment VoiceEnrollmentSigner) (device *VoiceDevice, err error) {
	defer barrier(&err)
	if sshKey == nil || enrollment == nil {
		return nil, errVoiceInput
	}
	sshPublic := voicePublicKey(sshKey.VoiceSSHPublicKey)
	devicePublic := voicePublicKey(enrollment.VoiceEnrollmentPublicKey)
	if sshPublic == nil || devicePublic == nil || bytes.Equal(sshPublic, devicePublic) {
		return nil, errVoiceInput
	}
	sshSigner, err := ssh.NewSignerFromSigner(voiceSigner{sshPublic, sshKey.SignVoiceSSH})
	if err != nil {
		return nil, errVoiceInput
	}
	ctx, cancel := context.WithCancel(context.Background())
	client, err := voiceclient.NewDeviceClient(ctx, voiceclient.SSHConfig{
		Address: address, User: user, HostKeySHA256: hostKeySHA256, RemoteVoiceAddress: remoteVoiceAddress,
	}, sshSigner, voiceSigner{devicePublic, enrollment.SignVoiceEnrollment})
	if err != nil {
		cancel()
		return nil, errVoiceInput
	}
	profile, _ := json.Marshal([]string{address, user, hostKeySHA256, remoteVoiceAddress, base64.StdEncoding.EncodeToString(sshPublic), base64.StdEncoding.EncodeToString(devicePublic)})
	sum := sha256.Sum256(profile)
	return &VoiceDevice{client: client, lifetime: ctx, cancel: cancel, endpoint: remoteVoiceAddress, profile: hex.EncodeToString(sum[:])}, nil
}

// Enroll consumes a one-use invitation. Store the returned scoped grant in
// app-protected storage and pass its exact bytes back to subsequent methods.
func (d *VoiceDevice) Enroll(placeID, invitationRef, invitationEndpoint string) (grantJSON string, err error) {
	defer barrier(&err)
	if d == nil || invitationEndpoint != d.endpoint || !voiceIdentity(placeID) || !voiceHex(invitationRef, 32) {
		return "", errVoiceInput
	}
	return voiceCall(d, 15*time.Second, func(ctx context.Context) (voiceGrant, error) {
		grant, err := d.client.Enroll(ctx, voicewire.DeviceInvitation{PlaceID: placeID, Reference: invitationRef, Endpoint: invitationEndpoint})
		if err == nil && !validVoiceGrant(grant) {
			return voiceGrant{}, errVoiceUnavailable
		}
		return voiceGrant{Profile: d.profile, Credential: grant}, err
	})
}

// Status returns the current readiness and conversation from the scoped grant.
func (d *VoiceDevice) Status(grantJSON string) (statusJSON string, err error) {
	defer barrier(&err)
	grant, err := d.decodeGrant(grantJSON)
	if err != nil {
		return "", err
	}
	return voiceCall(d, 15*time.Second, func(ctx context.Context) (voicewire.DeviceStatus, error) {
		return d.client.Status(ctx, grant)
	})
}

// SubmitText uses the caller's stable operation ID. After an ambiguous response
// check Receipt with that same ID before choosing to retry.
func (d *VoiceDevice) SubmitText(grantJSON, id, text string) (receiptJSON string, err error) {
	defer barrier(&err)
	grant, err := d.decodeGrant(grantJSON)
	if err != nil || !voiceIdentity(id) || len(text) > voicewire.MaxTextBytes || !utf8.ValidString(text) || strings.TrimSpace(text) == "" {
		return "", errVoiceInput
	}
	return voiceCall(d, 15*time.Second, func(ctx context.Context) (voicewire.DeviceAdmission, error) {
		return d.client.SubmitText(ctx, grant, id, text)
	})
}

// Receipt reads the admission for the same exact grant and operation ID.
func (d *VoiceDevice) Receipt(grantJSON, id string) (receiptJSON string, err error) {
	defer barrier(&err)
	grant, err := d.decodeGrant(grantJSON)
	if err != nil || !voiceIdentity(id) {
		return "", errVoiceInput
	}
	return voiceCall(d, 15*time.Second, func(ctx context.Context) (voicewire.DeviceAdmission, error) {
		return d.client.Receipt(ctx, grant, id)
	})
}

// End cancels outstanding work and closes all owned SSH transport. It does not
// revoke the grant on the host. A new handle is required to connect again.
func (d *VoiceDevice) End() (err error) {
	defer barrier(&err)
	if d == nil || d.client == nil {
		return errVoiceEnded
	}
	d.cancel()
	d.client.Close()
	return nil
}

func voiceCall[T any](d *VoiceDevice, timeout time.Duration, run func(context.Context) (T, error)) (string, error) {
	if d == nil || d.client == nil || d.lifetime.Err() != nil {
		return "", errVoiceEnded
	}
	ctx, cancel := context.WithTimeout(d.lifetime, timeout)
	defer cancel()
	reply, err := run(ctx)
	switch {
	case d.lifetime.Err() != nil:
		return "", errVoiceEnded
	case errors.Is(err, voiceclient.ErrPublicationCapacity):
		return "", classed(ErrClassUnreconciled, errors.New("swarmmobile: voice publication history exceeds handset capacity"))
	case errors.Is(err, voiceclient.ErrHostKey):
		return "", classed(ErrClassInvalidRequest, voiceclient.ErrHostKey)
	case err != nil:
		return "", errVoiceUnavailable
	}
	raw, err := json.Marshal(reply)
	if err != nil || len(raw) > voicewire.MaxMessageBytes {
		return "", errVoiceUnavailable
	}
	return string(raw), nil
}

func voiceIdentity(s string) bool {
	return s != "" && len(s) <= 128 && utf8.ValidString(s) && s == strings.TrimSpace(s) && strings.IndexFunc(s, unicode.IsControl) < 0
}

func voiceHex(s string, size int) bool {
	if len(s) != 2*size || strings.ToLower(s) != s {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func validVoiceGrant(grant voicewire.DeviceCredential) bool {
	return voiceIdentity(grant.PlaceID) && voiceHex(grant.DeviceID, 16) && grant.Generation != 0 && voiceHex(grant.Token, 32)
}

type voiceGrant struct {
	Profile    string                     `json:"profile"`
	Credential voicewire.DeviceCredential `json:"credential"`
}

func (d *VoiceDevice) decodeGrant(raw string) (voicewire.DeviceCredential, error) {
	var grant voiceGrant
	if d == nil || len(raw) > 2048 || json.Unmarshal([]byte(raw), &grant) != nil || grant.Profile != d.profile || !validVoiceGrant(grant.Credential) {
		return voicewire.DeviceCredential{}, errVoiceInput
	}
	exact, err := json.Marshal(grant)
	if err != nil || raw != string(exact) {
		return voicewire.DeviceCredential{}, errVoiceInput
	}
	return grant.Credential, nil
}

// BeginPTT reserves the caller's utterance ID before foreground recording starts.
func (d *VoiceDevice) BeginPTT(grantJSON, id string) (captureJSON string, err error) {
	defer barrier(&err)
	grant, err := d.decodeGrant(grantJSON)
	if err != nil || !voiceIdentity(id) {
		return "", errVoiceInput
	}
	return voiceCall(d, 15*time.Second, func(ctx context.Context) (voicewire.DeviceCapture, error) {
		return d.client.BeginPTT(ctx, grant, id)
	})
}

// FinalizePTT submits bounded mono 24 kHz PCM16LE. On an ambiguous result use
// Receipt with the same ID. The facade never replays the recording.
func (d *VoiceDevice) FinalizePTT(grantJSON, id, pcmBase64 string) (receiptJSON string, err error) {
	defer barrier(&err)
	grant, err := d.decodeGrant(grantJSON)
	if err != nil || !voiceIdentity(id) || len(pcmBase64) == 0 || len(pcmBase64) > base64.StdEncoding.EncodedLen(voicewire.MaxDevicePCMBytes) {
		return "", errVoiceInput
	}
	pcm, err := base64.StdEncoding.Strict().DecodeString(pcmBase64)
	if err != nil || len(pcm) == 0 || len(pcm)%2 != 0 || len(pcm) > voicewire.MaxDevicePCMBytes {
		return "", errVoiceInput
	}
	defer clear(pcm)
	return voiceCall(d, 45*time.Second, func(ctx context.Context) (voicewire.DeviceAdmission, error) {
		return d.client.FinalizePTT(ctx, grant, id, pcm)
	})
}

// Publications returns the canonical manager text for one submitted operation.
func (d *VoiceDevice) Publications(grantJSON, id string) (publicationsJSON string, err error) {
	defer barrier(&err)
	grant, err := d.decodeGrant(grantJSON)
	if err != nil || !voiceIdentity(id) {
		return "", errVoiceInput
	}
	return voiceCall(d, 25*time.Second, func(ctx context.Context) ([]voicewire.DevicePublication, error) {
		return d.client.Publications(ctx, grant, id)
	})
}
