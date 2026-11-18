package voice

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/voicewire"
)

func TestVoiceDeviceRegistryEnrollmentChallengeRevocation(t *testing.T) {
	dir := privateServerDir(t)
	now := time.Now
	registry, err := openDeviceRegistry(dir, deviceRegistryFile, "place", now)
	if err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, wrong, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	invitation, err := registry.issue(public)
	if err != nil {
		t.Fatal(err)
	}
	identity := voicewire.DeviceCredential{PlaceID: "place", Token: invitation.Reference}
	enroll := func(signer ed25519.PrivateKey) (voicewire.DeviceCredential, error) {
		challenge, err := registry.challenge(identity)
		if err != nil {
			return voicewire.DeviceCredential{}, err
		}
		proof := voicewire.DeviceProof{Credential: identity, Challenge: challenge}
		proof.Signature = ed25519.Sign(signer, voicewire.DeviceSignedBytes(proof, "enroll", nil))
		return registry.enroll(proof)
	}
	if _, err := enroll(wrong); !errors.Is(err, errDeviceDenied) {
		t.Fatalf("wrong identity enrollment: %v", err)
	}
	grant, err := enroll(private)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := enroll(private); !errors.Is(err, errDeviceDenied) {
		t.Fatalf("used reference accepted: %v", err)
	}
	challenge, err := registry.challenge(grant)
	if err != nil {
		t.Fatal(err)
	}
	secondNonce, err := registry.challenge(grant)
	if err != nil || secondNonce == challenge {
		t.Fatal("concurrent grant challenge", err)
	}
	concurrent := voicewire.DeviceProof{Credential: grant, Challenge: secondNonce}
	concurrent.Signature = ed25519.Sign(private, voicewire.DeviceSignedBytes(concurrent, "status", nil))
	if err := registry.authorize(concurrent, "status", nil, func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	proof := voicewire.DeviceProof{Credential: grant, Challenge: challenge}
	body := []byte("exact body")
	proof.Signature = ed25519.Sign(private, voicewire.DeviceSignedBytes(proof, "status", nil))
	if err := registry.authorize(proof, "status", nil, func(string) error { return nil }); err != nil {
		t.Fatal("polling invalidated unfinished request", err)
	}
	proof.Challenge, err = registry.challenge(grant)
	if err != nil {
		t.Fatal(err)
	}
	proof.Signature = ed25519.Sign(private, voicewire.DeviceSignedBytes(proof, "text", body))
	calls := 0
	action := func(_ string) error { calls++; return nil }
	if err := registry.authorize(proof, "text", []byte("modified body"), action); !errors.Is(err, errDeviceDenied) || calls != 0 {
		t.Fatalf("changed signed body reached action: %v, calls=%d", err, calls)
	}
	proof.Challenge, err = registry.challenge(grant)
	if err != nil {
		t.Fatal(err)
	}
	proof.Signature = ed25519.Sign(private, voicewire.DeviceSignedBytes(proof, "text", body))
	if err := registry.authorize(proof, "text", body, action); err != nil {
		t.Fatal(err)
	}
	if err := registry.authorize(proof, "text", body, action); !errors.Is(err, errDeviceDenied) || calls != 1 {
		t.Fatalf("replayed challenge: %v, calls=%d", err, calls)
	}
	proof.Challenge, err = registry.challenge(grant)
	if err != nil {
		t.Fatal(err)
	}
	proof.Signature = ed25519.Sign(private, voicewire.DeviceSignedBytes(proof, "text", body))
	if err := registry.revoke(grant.DeviceID); err != nil {
		t.Fatal(err)
	}
	if err := registry.authorize(proof, "text", body, action); !errors.Is(err, errDeviceDenied) || calls != 1 {
		t.Fatalf("revoked signature retained authority: %v, calls=%d", err, calls)
	}
	registry, err = openDeviceRegistry(dir, deviceRegistryFile, "place", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.challenge(grant); !errors.Is(err, errDeviceDenied) {
		t.Fatalf("revoked grant after restart: %v", err)
	}
}

func TestVoiceDeviceRegistryReferenceExpiresAtTenMinutes(t *testing.T) {
	dir := privateServerDir(t)
	at := time.Date(2031, time.January, 1, 12, 0, 0, 0, time.UTC)
	registry, err := openDeviceRegistry(dir, deviceRegistryFile, "place", func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	result, err := registry.issue(public)
	if err != nil || !result.ExpiresAt.Equal(at.Add(10*time.Minute)) {
		t.Fatalf("enrollment window: %+v, %v", result, err)
	}
	at = at.Add(10 * time.Minute)
	if _, err := registry.challenge(voicewire.DeviceCredential{PlaceID: "place", Token: result.Reference}); !errors.Is(err, errDeviceDenied) {
		t.Fatalf("expired reference accepted: %v", err)
	}
}
