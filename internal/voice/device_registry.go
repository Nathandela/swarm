package voice

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/voicewire"
)

const (
	deviceRegistryFile       = "voice-devices.json"
	maxDeviceRecords         = 128
	maxDeviceLedger          = 128 << 10
	deviceEnrollmentLifetime = 10 * time.Minute
	deviceChallengeLifetime  = 30 * time.Second
)

var errDeviceDenied = errors.New("voice: device authorization denied")

type DeviceInvitation = voicewire.DeviceInvitation

// DeviceGrantInfo identifies a retained enrollment by its public Ed25519 signing key.
type DeviceGrantInfo struct {
	DeviceID         string `json:"device_id"`
	SigningKeySHA256 string `json:"signing_key_sha256"`
	Status           string `json:"status"`
}

type deviceInvitationRecord struct {
	PublicKey []byte    `json:"public_key"`
	ExpiresAt time.Time `json:"expires_at"`
}
type deviceGrantRecord struct {
	PublicKey  []byte `json:"public_key"`
	Generation uint64 `json:"generation"`
	TokenHash  string `json:"token_hash"`
}
type deviceRegistryState struct {
	Version     int                               `json:"version"`
	PlaceID     string                            `json:"place_id"`
	Invitations map[string]deviceInvitationRecord `json:"invitations"`
	Devices     map[string]deviceGrantRecord      `json:"devices"`
}
type deviceChallenge struct {
	Subject   string
	ExpiresAt time.Time
}
type deviceRegistry struct {
	mu         sync.Mutex
	dir        string
	file       string
	now        func() time.Time
	state      deviceRegistryState
	challenges map[string]deviceChallenge
	previews   map[string]*devicePreviewSlot
}

func openDeviceRegistry(dir, file, place string, now func() time.Time) (*deviceRegistry, error) {
	if !configText(place, 128) {
		return nil, ErrInvalid
	}
	state := deviceRegistryState{
		Version: 1, PlaceID: place, Invitations: map[string]deviceInvitationRecord{}, Devices: map[string]deviceGrantRecord{},
	}
	raw, err := readVoiceFile(dir, file, maxDeviceLedger)
	if err == nil {
		if validateObjectMembers(raw, "version", "place_id", "invitations", "devices") != nil || json.Unmarshal(raw, &state) != nil ||
			state.Version != 1 || state.PlaceID != place || state.Invitations == nil || state.Devices == nil ||
			len(state.Invitations) > maxDeviceRecords || len(state.Devices) > maxDeviceRecords {
			return nil, ErrCorrupt
		}
		for reference, item := range state.Invitations {
			if !deviceHex(reference, 32) || len(item.PublicKey) != ed25519.PublicKeySize || item.ExpiresAt.IsZero() {
				return nil, ErrCorrupt
			}
		}
		for id, item := range state.Devices {
			if !deviceHex(id, 16) || len(item.PublicKey) != ed25519.PublicKeySize || item.Generation == 0 ||
				(item.TokenHash != "" && !deviceHex(item.TokenHash, 32)) {
				return nil, ErrCorrupt
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return &deviceRegistry{dir: dir, file: file, now: now, state: state, challenges: map[string]deviceChallenge{}}, nil
}

func (r *deviceRegistry) save(next deviceRegistryState) error {
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if len(raw) > maxDeviceLedger {
		return ErrCapacity
	}
	if err := writeVoiceFile(r.dir, r.file, raw); err != nil {
		// A failed directory sync can follow a successful atomic rename. Refuse
		// further authorization until service restart reloads the durable file.
		r.invalidatePreviews(deviceRegistryState{})
		r.state = deviceRegistryState{}
		clear(r.challenges)
		return err
	}
	r.invalidatePreviews(next)
	r.state = next
	return nil
}

func (r *deviceRegistry) issue(public []byte) (DeviceInvitation, error) {
	if len(public) != ed25519.PublicKeySize {
		return DeviceInvitation{}, ErrInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state.Version == 0 {
		return DeviceInvitation{}, ErrCorrupt
	}
	now := r.now()
	next := r.state
	next.Invitations = maps.Clone(next.Invitations)
	maps.DeleteFunc(next.Invitations, func(_ string, item deviceInvitationRecord) bool { return !now.Before(item.ExpiresAt) })
	if len(next.Invitations) >= maxDeviceRecords {
		return DeviceInvitation{}, ErrCapacity
	}
	ref, err := deviceRandom(32)
	if err != nil {
		return DeviceInvitation{}, err
	}
	expires := now.Add(deviceEnrollmentLifetime)
	next.Invitations[hashBytes([]byte(ref))] = deviceInvitationRecord{PublicKey: append([]byte(nil), public...), ExpiresAt: expires}
	if err := r.save(next); err != nil {
		return DeviceInvitation{}, err
	}
	return DeviceInvitation{PlaceID: next.PlaceID, Reference: ref, ExpiresAt: expires}, nil
}

func (r *deviceRegistry) challenge(credential voicewire.DeviceCredential) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	_, subject, err := r.identity(credential, now)
	if err != nil {
		return "", err
	}
	maps.DeleteFunc(r.challenges, func(_ string, c deviceChallenge) bool { return !now.Before(c.ExpiresAt) })
	active := 0
	for _, challenge := range r.challenges {
		if challenge.Subject == subject {
			active++
		}
	}
	if active >= 4 || len(r.challenges) >= maxDeviceRecords {
		return "", ErrCapacity
	}
	nonce, err := deviceRandom(32)
	if err != nil {
		return "", err
	}
	r.challenges[nonce] = deviceChallenge{Subject: subject, ExpiresAt: now.Add(deviceChallengeLifetime)}
	return nonce, nil
}

func (r *deviceRegistry) identity(credential voicewire.DeviceCredential, now time.Time) ([]byte, string, error) {
	if r.state.Version != 1 || credential.PlaceID != r.state.PlaceID || !deviceHex(credential.Token, 32) {
		return nil, "", errDeviceDenied
	}
	tokenHash := hashBytes([]byte(credential.Token))
	if credential.DeviceID == "" && credential.Generation == 0 {
		record, ok := r.state.Invitations[tokenHash]
		if ok && now.Before(record.ExpiresAt) {
			return record.PublicKey, "invite:" + tokenHash, nil
		}
	} else if deviceHex(credential.DeviceID, 16) {
		record, ok := r.state.Devices[credential.DeviceID]
		if ok && record.Generation == credential.Generation && record.TokenHash == tokenHash {
			return record.PublicKey, "device:" + credential.DeviceID, nil
		}
	}
	return nil, "", errDeviceDenied
}

func (r *deviceRegistry) verify(proof voicewire.DeviceProof, action string, body []byte) error {
	now := r.now()
	public, subject, err := r.identity(proof.Credential, now)
	if err != nil {
		return err
	}
	challenge, ok := r.challenges[proof.Challenge]
	if !ok || challenge.Subject != subject {
		return errDeviceDenied
	}
	delete(r.challenges, proof.Challenge)
	if !now.Before(challenge.ExpiresAt) || !ed25519.Verify(public, voicewire.DeviceSignedBytes(proof, action, body), proof.Signature) {
		return errDeviceDenied
	}
	return nil
}

func (r *deviceRegistry) enroll(proof voicewire.DeviceProof) (voicewire.DeviceCredential, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if proof.Credential.DeviceID != "" || proof.Credential.Generation != 0 {
		return voicewire.DeviceCredential{}, errDeviceDenied
	}
	if err := r.verify(proof, "enroll", nil); err != nil {
		return voicewire.DeviceCredential{}, err
	}
	next := r.state
	next.Devices = maps.Clone(next.Devices)
	maps.DeleteFunc(next.Devices, func(_ string, item deviceGrantRecord) bool { return item.TokenHash == "" })
	if len(next.Devices) >= maxDeviceRecords {
		return voicewire.DeviceCredential{}, ErrCapacity
	}
	id, err := deviceRandom(16)
	if err != nil {
		return voicewire.DeviceCredential{}, err
	}
	token, err := deviceRandom(32)
	if err != nil {
		return voicewire.DeviceCredential{}, err
	}
	next.Invitations = maps.Clone(next.Invitations)
	ref := hashBytes([]byte(proof.Credential.Token))
	public := next.Invitations[ref].PublicKey
	delete(next.Invitations, ref)
	next.Devices[id] = deviceGrantRecord{PublicKey: public, TokenHash: hashBytes([]byte(token)), Generation: 1}
	if err := r.save(next); err != nil {
		return voicewire.DeviceCredential{}, err
	}
	return voicewire.DeviceCredential{PlaceID: next.PlaceID, DeviceID: id, Token: token, Generation: 1}, nil
}

// authorize orders revocation against a local action's durable admission.
func (r *deviceRegistry) authorize(proof voicewire.DeviceProof, action string, body []byte, perform func(string) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if proof.Credential.DeviceID == "" {
		return errDeviceDenied
	}
	if err := r.verify(proof, action, body); err != nil {
		return err
	}
	return perform(proof.Credential.DeviceID)
}

// commitIfGranted rechecks the exact credential after an external operation.
// The callback is limited to a local durable commit.
func (r *deviceRegistry) commitIfGranted(credential voicewire.DeviceCredential, perform func() error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if credential.DeviceID == "" {
		return errDeviceDenied
	}
	if _, _, err := r.identity(credential, r.now()); err != nil {
		return err
	}
	return perform()
}

func (r *deviceRegistry) revoke(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state.Version == 0 {
		return ErrCorrupt
	}
	record, ok := r.state.Devices[id]
	if !ok {
		return ErrNotFound
	}
	if record.TokenHash == "" {
		return nil
	}
	next := r.state
	next.Devices = maps.Clone(next.Devices)
	record.TokenHash = ""
	record.Generation++
	next.Devices[id] = record
	if err := r.save(next); err != nil {
		return err
	}
	maps.DeleteFunc(r.challenges, func(_ string, c deviceChallenge) bool { return c.Subject == "device:"+id })
	return nil
}

func (r *deviceRegistry) list() ([]DeviceGrantInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state.Version != 1 {
		return nil, ErrCorrupt
	}
	result := make([]DeviceGrantInfo, 0, len(r.state.Devices))
	for _, id := range slices.Sorted(maps.Keys(r.state.Devices)) {
		record := r.state.Devices[id]
		status := "active"
		if record.TokenHash == "" {
			status = "revoked"
		}
		result = append(result, DeviceGrantInfo{DeviceID: id, SigningKeySHA256: hashBytes(record.PublicKey), Status: status})
	}
	return result, nil
}

func deviceRandom(size int) (string, error) {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func deviceHex(text string, size int) bool {
	if len(text) != size*2 {
		return false
	}
	raw, err := hex.DecodeString(text)
	return err == nil && hex.EncodeToString(raw) == text
}
