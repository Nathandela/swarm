package voicewire

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"
)

type DeviceCredential struct {
	PlaceID    string `json:"place_id"`
	DeviceID   string `json:"device_id,omitempty"`
	Generation uint64 `json:"generation,omitempty"`
	Token      string `json:"token"`
}

type DeviceProof struct {
	Credential DeviceCredential `json:"credential"`
	Challenge  string           `json:"challenge"`
	Signature  []byte           `json:"signature"`
}

type DeviceInvitation struct {
	PlaceID   string    `json:"place_id"`
	Reference string    `json:"reference"`
	ExpiresAt time.Time `json:"expires_at"`
	Endpoint  string    `json:"endpoint"`
}

// DeviceSignedBytes is the versioned Ed25519 domain for one authorized request.
func DeviceSignedBytes(proof DeviceProof, action string, body []byte) []byte {
	credential := proof.Credential
	digest := sha256.Sum256(body)
	encoded, _ := json.Marshal([8]string{
		"swarm-voice-device-v1", credential.PlaceID, credential.DeviceID,
		strconv.FormatUint(credential.Generation, 10), credential.Token,
		proof.Challenge, action, hex.EncodeToString(digest[:]),
	})
	return encoded
}

type DeviceChallenge struct {
	Challenge string `json:"challenge"`
}

type DeviceRequest struct {
	Proof DeviceProof     `json:"proof"`
	Body  json.RawMessage `json:"body,omitempty"`
}

type DeviceText struct {
	ID   string `json:"id"`
	Text string `json:"text,omitempty"`
}

type DeviceStatus struct {
	PlaceID        string `json:"place_id"`
	ConversationID string `json:"conversation_id"`
	Provider       string `json:"provider"`
	Ready          bool   `json:"ready"`
}

type DeviceAdmission struct {
	ID       string `json:"id"`
	Sequence uint64 `json:"sequence"`
	Phase    string `json:"phase,omitempty"`
}

const MaxDevicePCMBytes = 24_000 * 2 * 120

type DeviceCapture struct {
	ID        string `json:"id"`
	Phase     string `json:"phase"`
	Admission uint64 `json:"admission,omitempty"`
}

type DevicePublication struct {
	ID       string `json:"id"`
	Sequence uint64 `json:"sequence"`
	Text     string `json:"text"`
	Final    bool   `json:"final"`
}

type DevicePublicationPage struct {
	Item *DevicePublication `json:"item,omitempty"`
	Text []byte             `json:"text,omitempty"`
}

type DevicePublicationQuery struct {
	ID    string `json:"id"`
	After uint64 `json:"after,omitempty"`
}

// DeviceFinalizeAction binds the stable utterance ID as well as the signed PCM body.
func DeviceFinalizeAction(id string) string { return "ptt/finalize:" + id }
