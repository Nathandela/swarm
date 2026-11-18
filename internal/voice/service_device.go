package voice

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/voicewire"
)

func validDeviceEndpoint(address string, allowZero bool) bool {
	target, err := netip.ParseAddrPort(address)
	return err == nil && target.String() == address && (allowZero || target.Port() != 0) &&
		(target.Addr() == netip.AddrFrom4([4]byte{127, 0, 0, 1}) || target.Addr() == netip.IPv6Loopback())
}

func (c *managerControl) startDeviceEndpoint(ctx context.Context, address string) (func(), error) {
	if !validDeviceEndpoint(address, true) || c.devices == nil {
		return nil, ErrInvalid
	}
	var listen net.ListenConfig
	listener, err := listen.Listen(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	c.deviceEndpoint = listener.Addr().String()
	if !validDeviceEndpoint(c.deviceEndpoint, false) {
		_ = listener.Close()
		return nil, ErrInvalid
	}
	server := &http.Server{
		Handler: http.HandlerFunc(c.serveDevice), ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 45 * time.Second, IdleTimeout: 10 * time.Second,
		MaxHeaderBytes: 4096, BaseContext: func(net.Listener) context.Context { return ctx },
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = server.Serve(listener)
	}()
	return func() { _ = server.Close(); <-done }, nil
}

func (c *managerControl) serveDevice(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	contentType := "application/json"
	if r.URL.Path == "/v1/device/ptt/finalize" {
		contentType = "application/octet-stream"
	}
	if r.Host != "voice-device" || r.Method != http.MethodPost || r.URL.RawQuery != "" ||
		r.Header.Get("Origin") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Content-Type") != contentType {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	var data any
	var err error
	switch r.URL.Path {
	case "/v1/device/challenge":
		var credential voicewire.DeviceCredential
		if err = readDeviceJSON(r, &credential, "place_id", "device_id", "generation", "token"); err == nil {
			var challenge string
			challenge, err = c.devices.challenge(credential)
			data = voicewire.DeviceChallenge{Challenge: challenge}
		}
	case "/v1/device/enroll":
		var proof voicewire.DeviceProof
		if err = readDeviceJSON(r, &proof, "credential", "challenge", "signature"); err == nil {
			if err = r.Context().Err(); err == nil {
				if c.store.checkControllerBinding(c.binding) != nil {
					err = ErrStale
				} else {
					data, err = c.devices.enroll(proof)
				}
			}
		}
	case "/v1/device/preview":
		c.serveDevicePreview(w, r)
		return
	case "/v1/device/ptt/finalize":
		data, err = c.deviceFinalize(r)
	case "/v1/device/session", "/v1/device/status", "/v1/device/text", "/v1/device/receipt", "/v1/device/publications", "/v1/device/ptt/begin", "/v1/device/ptt/end":
		var request voicewire.DeviceRequest
		if err = readDeviceJSON(r, &request, "proof", "body"); err != nil {
			break
		}
		action := strings.TrimPrefix(r.URL.Path, "/v1/device/")
		err = c.devices.authorize(request.Proof, action, request.Body, func(device string) error {
			if err := r.Context().Err(); err != nil {
				return err
			}
			data, err = c.deviceAction(action, request.Proof.Credential.PlaceID, device, request.Body)
			return err
		})
	default:
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if errors.Is(err, errDeviceDenied) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if err != nil {
		w.WriteHeader(managerControlErrorStatus(err))
		return
	}
	_ = json.NewEncoder(w).Encode(data)
}

func readDeviceJSON(r *http.Request, output any, fields ...string) error {
	raw, err := io.ReadAll(io.LimitReader(r.Body, MaxControlFrameBytes+1))
	if err != nil || len(raw) > MaxControlFrameBytes || validateObjectMembers(raw, fields...) != nil || json.Unmarshal(raw, output) != nil {
		return ErrInvalid
	}
	return nil
}

func (c *managerControl) deviceAction(action, place, device string, body []byte) (any, error) {
	if action == "session" {
		return c.deviceStatusPage(device, place, body)
	}
	if action == "status" {
		if len(body) != 0 {
			return nil, ErrInvalid
		}
		status, err := c.status()
		return voicewire.DeviceStatus{PlaceID: place, ConversationID: status.Binding.ConversationID, Provider: status.Binding.Provider, Ready: status.Ready}, err
	}
	if action == "publications" {
		var req voicewire.DevicePublicationQuery
		if validateObjectMembers(body, "id", "after") != nil || json.Unmarshal(body, &req) != nil || !configText(req.ID, 128) {
			return nil, ErrInvalid
		}
		receipt, state, err := c.deviceReceipt(device, req.ID)
		if err != nil {
			return nil, err
		}
		p := nextPublication(state, receipt, req.After)
		if p == nil {
			return voicewire.DevicePublicationPage{}, nil
		}
		return voicewire.DevicePublicationPage{
			Item: &voicewire.DevicePublication{ID: p.ID, Sequence: p.Sequence, Final: p.Final}, Text: []byte(p.Text),
		}, nil
	}
	if action == "ptt/begin" || action == "ptt/end" {
		var intent voicewire.DeviceCaptureIntent
		if validateObjectMembers(body, "id", "session_id", "version") != nil || json.Unmarshal(body, &intent) != nil || !configText(intent.ID, 128) || !deviceHex(intent.SessionID, 16) || intent.Version != voicewire.ProtocolVersion {
			return nil, ErrInvalid
		}
		source, keyID := deviceUtterance(device, intent.ID)
		source += ":ptt"
		if action == "ptt/end" {
			return struct{}{}, c.capture.end(keyID, source, intent.SessionID)
		}
		captured, err := c.beginCapture(keyID, source, intent.SessionID, "")
		return voicewire.DeviceCapture{ID: intent.ID, Phase: captured.Phase, Admission: captured.Admission}, err
	}
	var req voicewire.DeviceText
	fields := []string{"id"}
	if action == "text" {
		fields = append(fields, "text")
	}
	if validateObjectMembers(body, fields...) != nil || json.Unmarshal(body, &req) != nil || !configText(req.ID, 128) {
		return nil, ErrInvalid
	}
	source, keyID := deviceUtterance(device, req.ID)

	var receipt Admission
	var err error
	if action == "text" {
		if c.controller == nil {
			return nil, ErrStale
		}
		if strings.TrimSpace(req.Text) == "" || len(req.Text) > MaxUtteranceBytes || !utf8.ValidString(req.Text) {
			return nil, ErrInvalid
		}
		if err := c.store.checkControllerBinding(c.binding); err != nil {
			return nil, ErrStale
		}
		receipt, err = c.controller.SubmitText(Utterance{ID: keyID, DeviceID: source, VoiceSessionID: keyID, MediaEpoch: 1}, req.Text)
		if errors.Is(err, ErrExpired) {
			receipt, _, err = c.deviceReceipt(device, req.ID)
		}
		if err == nil {
			if err := c.syncDeviceStatuses(); err != nil {
				return nil, err
			}
			select {
			case c.wake <- struct{}{}:
			default:
			}
		}
	} else {
		receipt, _, err = c.deviceReceipt(device, req.ID)
	}
	return voicewire.DeviceAdmission{ID: req.ID, Sequence: receipt.Sequence, Phase: receipt.Phase}, err
}

func deviceUtterance(device, id string) (source, keyID string) {
	source = "device:" + device
	return source, hashBytes([]byte(source + "\x00" + id))
}

func (c *managerControl) deviceReceipt(device, id string) (Admission, State, error) {
	source, keyID := deviceUtterance(device, id)
	state, err := c.store.Snapshot()
	if err != nil {
		return Admission{}, State{}, err
	}
	owner := state.Utterances[key(c.binding.ConversationID, keyID)].DeviceID
	if owner != source && owner != source+":ptt" {
		return Admission{}, State{}, ErrNotFound
	}
	receipt, err := receiptForUtterance(state, c.binding.ConversationID, keyID)
	return receipt, state, err
}

func (c *managerControl) deviceFinalize(r *http.Request) (voicewire.DeviceAdmission, error) {
	var empty voicewire.DeviceAdmission
	if c.controller == nil {
		return empty, ErrStale
	}
	ids, proofHeaders := r.Header.Values("Swarm-Utterance-ID"), r.Header.Values("Swarm-Device-Proof")
	if len(ids) != 1 || !configText(ids[0], 128) || len(proofHeaders) != 1 ||
		len(proofHeaders[0]) > 2048 || r.ContentLength > MaxPTTBytes {
		return empty, ErrInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(proofHeaders[0])
	var proof voicewire.DeviceProof
	if err != nil || validateObjectMembers(raw, "credential", "challenge", "signature") != nil || json.Unmarshal(raw, &proof) != nil {
		return empty, ErrInvalid
	}
	if err := c.devices.commitIfGranted(proof.Credential, func() error { return nil }); err != nil {
		return empty, err
	}
	sessions := r.Header.Values("Swarm-Capture-Session")
	if len(sessions) != 1 || !deviceHex(sessions[0], 16) {
		return empty, ErrInvalid
	}
	session := sessions[0]
	id := ids[0]
	source, keyID := deviceUtterance(proof.Credential.DeviceID, id)
	select {
	case c.pttReads <- struct{}{}:
		defer func() { <-c.pttReads }()
	default:
		return empty, ErrCapacity
	}
	pcm, err := io.ReadAll(io.LimitReader(r.Body, MaxPTTBytes+1))
	if err != nil || !validPTTAudio(pcm) {
		return empty, ErrInvalid
	}
	receipt, err := c.finalizeScopedPTT(r.Context(), keyID, source+":ptt", session, pcm, func(claim func() error) error {
		return c.devices.authorize(proof, voicewire.DeviceFinalizeAction(id, session), pcm, func(string) error { return claim() })
	}, func(commit func() error) error {
		return c.devices.commitIfGranted(proof.Credential, func() error {
			if err := commit(); err != nil {
				return err
			}
			return c.syncDeviceStatuses()
		})
	})
	return voicewire.DeviceAdmission{ID: id, Sequence: receipt.Sequence, Phase: receipt.Phase}, err
}

func (c *managerControl) ownerDeviceAction(r *http.Request) (any, error) {
	if c.devices == nil {
		return nil, ErrNotFound
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/devices/list":
		return c.devices.list()
	case r.Method == http.MethodGet && r.URL.Path == "/v1/devices/endpoint":
		if c.deviceEndpoint == "" {
			return nil, ErrNotFound
		}
		return map[string]string{"endpoint": c.deviceEndpoint}, nil
	case r.Method == http.MethodPost && r.URL.Path == "/v1/devices/enrollment":
		var req struct {
			PublicKey []byte `json:"public_key"`
		}
		if err := readDeviceJSON(r, &req, "public_key"); err != nil {
			return nil, err
		}
		if c.controller == nil || c.deviceEndpoint == "" || c.store.checkControllerBinding(c.binding) != nil {
			return nil, ErrStale
		}
		invitation, err := c.devices.issue(req.PublicKey)
		invitation.Endpoint = c.deviceEndpoint
		return invitation, err
	case r.Method == http.MethodPost && r.URL.Path == "/v1/devices/revoke":
		var req struct {
			DeviceID string `json:"device_id"`
		}
		if err := readDeviceJSON(r, &req, "device_id"); err != nil || !deviceHex(req.DeviceID, 16) {
			return nil, ErrInvalid
		}
		if err := c.devices.revoke(req.DeviceID); err != nil {
			return nil, err
		}
		c.capture.mu.Lock()
		if c.capture.current.Source == "device:"+req.DeviceID+":ptt" {
			c.capture.current = captureOwner{}
		}
		c.capture.mu.Unlock()
		c.statusStreams.mu.Lock()
		delete(c.statusStreams.devices, req.DeviceID)
		c.statusStreams.mu.Unlock()
		return struct{}{}, nil
	}
	return nil, ErrNotFound
}

// DeviceEndpoint returns the configured service's actual loopback address.
func (c *ManagerClient) DeviceEndpoint(ctx context.Context) (string, error) {
	var result struct {
		Endpoint string `json:"endpoint"`
	}
	if err := c.callJSON(ctx, http.MethodGet, "/v1/devices/endpoint", nil, &result); err != nil {
		return "", err
	}
	if !validDeviceEndpoint(result.Endpoint, false) {
		return "", ErrCorrupt
	}
	return result.Endpoint, nil
}

// ListDevices returns the device IDs, signing key fingerprints, and revocation status held by this place.
func (c *ManagerClient) ListDevices(ctx context.Context) ([]DeviceGrantInfo, error) {
	var result []DeviceGrantInfo
	if err := c.callJSON(ctx, http.MethodGet, "/v1/devices/list", nil, &result); err != nil {
		return nil, err
	}
	if result == nil || len(result) > maxDeviceRecords {
		return nil, ErrCorrupt
	}
	for i, item := range result {
		if !deviceHex(item.DeviceID, 16) || !deviceHex(item.SigningKeySHA256, 32) ||
			(item.Status != "active" && item.Status != "revoked") || (i > 0 && item.DeviceID <= result[i-1].DeviceID) {
			return nil, ErrCorrupt
		}
	}
	return result, nil
}

// IssueDeviceEnrollment binds a one-use token to the supplied Ed25519 public key.
func (c *ManagerClient) IssueDeviceEnrollment(ctx context.Context, publicKey []byte) (DeviceInvitation, error) {
	var result DeviceInvitation
	if err := c.callJSON(ctx, http.MethodPost, "/v1/devices/enrollment", map[string][]byte{"public_key": publicKey}, &result); err != nil {
		return DeviceInvitation{}, err
	}
	if !configText(result.PlaceID, 128) || !deviceHex(result.Reference, 32) ||
		!validDeviceEndpoint(result.Endpoint, false) || result.ExpiresAt.IsZero() {
		return DeviceInvitation{}, ErrCorrupt
	}
	return result, nil
}

// RevokeDevice retires the scoped voice grant, leaving any SSH key untouched.
func (c *ManagerClient) RevokeDevice(ctx context.Context, deviceID string) error {
	var result struct{}
	return c.callJSON(ctx, http.MethodPost, "/v1/devices/revoke", map[string]string{"device_id": deviceID}, &result)
}
