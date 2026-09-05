package pushgw

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestV2StoreRegistrationIdentityDoesNotChangeWithTokenKeyRotation(t *testing.T) {
	repository := NewMemoryRepository()
	digestKey := bytes.Repeat([]byte{3}, 32)
	keys := map[string][]byte{"old": bytes.Repeat([]byte{1}, 32), "new": bytes.Repeat([]byte{2}, 32)}
	oldStore, err := newV2Store(repository, "old", keys, digestKey)
	if err != nil {
		t.Fatal(err)
	}
	newStore, err := newV2Store(repository, "new", keys, digestKey)
	if err != nil {
		t.Fatal(err)
	}
	if oldStore.idempotencyID("retry-key") != newStore.idempotencyID("retry-key") {
		t.Fatal("registration identity changed with the active token-encryption key")
	}
	oldCiphertext, oldVersion, err := oldStore.encrypt("old-token")
	if err != nil {
		t.Fatal(err)
	}
	if plaintext, err := newStore.decrypt(oldCiphertext, oldVersion); err != nil || plaintext != "old-token" {
		t.Fatalf("rotated keyring failed old decrypt: plaintext=%q err=%v", plaintext, err)
	}
	newCiphertext, newVersion, err := newStore.encrypt("new-token")
	if err != nil {
		t.Fatal(err)
	}
	if newVersion != "new" {
		t.Fatalf("new encryption version=%q, want new", newVersion)
	}
	if plaintext, err := newStore.decrypt(newCiphertext, newVersion); err != nil || plaintext != "new-token" {
		t.Fatalf("rotated keyring failed new decrypt: plaintext=%q err=%v", plaintext, err)
	}
}

func TestWakeAttemptTakeoverKeepsDeadlineAndRejectsStaleCompletion(t *testing.T) {
	ctx := context.Background()
	repository := NewMemoryRepository().(*memoryRepository)
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	installation := installationRecord{FCMTokenEnc: []byte("cipher"), TokenKeyVersion: "1", TokenGeneration: 1, LastActiveMs: now.UnixMilli()}
	if err := repository.putInstallation(ctx, "installation", installation); err != nil {
		t.Fatal(err)
	}
	address := addressRecord{InstallationID: "installation", SubmitCapHash: hashSecret("cap"), MachineRevokeHash: hashSecret("revoke"), Bound: true}
	if created, err := repository.putAddressIfBelowLimit(ctx, "address", "installation", address, 10, now); err != nil || !created {
		t.Fatalf("address created=%v err=%v", created, err)
	}
	limit := RateLimit{Max: 10, Window: time.Hour}
	deadline := now.Add(wakeWindow)
	first, claimed, err := repository.claimWake(ctx, "wake", "lease-one", "address", hashSecret("cap"), now, deadline, limit, "source", limit)
	if err != nil || !claimed {
		t.Fatalf("first claim=%+v claimed=%v err=%v", first, claimed, err)
	}
	later := now.Add(wakeLease + time.Second)
	second, claimed, err := repository.claimWake(ctx, "wake", "lease-two", "address", hashSecret("cap"), later, later.Add(wakeWindow), limit, "source", limit)
	if err != nil || !claimed {
		t.Fatalf("takeover=%+v claimed=%v err=%v", second, claimed, err)
	}
	if got := repository.wakes["wake"].ExpiresAtMs; got != deadline.UnixMilli() {
		t.Fatalf("takeover deadline=%d, want original %d", got, deadline.UnixMilli())
	}
	if _, err := repository.completeWake(ctx, "wake", "lease-one", first.TokenGeneration, 200, []byte("stale"), false, later); err != nil {
		t.Fatal(err)
	}
	if repository.wakes["wake"].State == "completed" {
		t.Fatal("stale lease completed a replacement attempt")
	}
	if _, err := repository.completeWake(ctx, "wake", "lease-two", second.TokenGeneration, 200, []byte("ok"), false, later); err != nil {
		t.Fatal(err)
	}
	record := repository.wakes["wake"]
	record.Attempts = maxWakeAttempts
	repository.wakes["wake"] = record
	cached, claimed, err := repository.claimWake(ctx, "wake", "lease-three", "address", hashSecret("cap"), later, deadline, limit, "source", limit)
	if err != nil || claimed || !cached.Completed || string(cached.Body) != "ok" {
		t.Fatalf("completed-at-max cached=%+v claimed=%v err=%v", cached, claimed, err)
	}
}

func TestWakeExpiryFencesProviderCompletion(t *testing.T) {
	testWakeExpiryFencesProviderCompletion(t, NewMemoryRepository())
}

type deadlineWakeSender func(context.Context, string, []byte) error

func (f deadlineWakeSender) Send(ctx context.Context, token string, envelope []byte) error {
	return f(ctx, token, envelope)
}

func TestWakeProviderDeadlineAndLateAcceptance(t *testing.T) {
	testWakeProviderDeadlineAndLateAcceptance(t, NewMemoryRepository())
}

func testWakeProviderDeadlineAndLateAcceptance(t *testing.T, repo Repository) {
	t.Helper()
	for i, remaining := range []time.Duration{time.Second, wakeWindow} {
		t.Run(remaining.String(), func(t *testing.T) {
			now := time.Now().UTC()
			deadline := now.Add(remaining)
			calls := 0
			sender := deadlineWakeSender(func(ctx context.Context, _ string, _ []byte) error {
				calls++
				if end, ok := ctx.Deadline(); !ok || time.Until(end) > min(remaining, wakeLease) {
					t.Errorf("provider context exceeds wake/lease deadline: deadline=%v present=%v", end, ok)
				}
				// Model a provider that accepted before the connection died, but whose
				// response arrived after the obligation expired. No sleeping or real FCM.
				now = deadline
				return nil
			})
			srv, err := NewFirestoreServer(Config{Repository: repo, TokenKeys: map[string][]byte{"v1": bytes.Repeat([]byte{1}, 32)}, ActiveTokenKeyVersion: "v1", RegistrationDigestKey: bytes.Repeat([]byte{2}, 32), RegistrationAdmission: func(string) bool { return true }, Sender: sender, Attest: firestoreTestAttestor{}, Now: func() time.Time { return now }})
			if err != nil {
				t.Fatal(err)
			}
			enc, version, err := srv.v2store.encrypt("fcm-token")
			if err != nil {
				t.Fatal(err)
			}
			rawAddress := bytes.Repeat([]byte{byte(i + 1)}, 16)
			address := encodeAddress(rawAddress)
			if err := repo.putInstallation(context.Background(), address, installationRecord{FCMTokenEnc: enc, TokenKeyVersion: version, TokenGeneration: 1, LastActiveMs: now.UnixMilli()}); err != nil {
				t.Fatal(err)
			}
			if created, err := repo.putAddressIfBelowLimit(context.Background(), address, address, addressRecord{InstallationID: address, SubmitCapHash: hashSecret("cap"), UnboundExpiresMs: now.Add(10 * time.Minute).UnixMilli()}, 20, now); err != nil || !created {
				t.Fatalf("create=%v err=%v", created, err)
			}
			envelope := make([]byte, wakeSize)
			envelope[0], envelope[1] = wakeVersion, wakeType
			copy(envelope[2:18], rawAddress)
			binary.BigEndian.PutUint64(envelope[26:34], uint64(deadline.Add(-wakeWindow).UnixMilli()))
			post := func() *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodPost, "/v1/wakes", bytes.NewReader(envelope))
				req.Header.Set("Content-Type", "application/octet-stream")
				req.Header.Set("Authorization", "Swarm-Capability cap")
				response := httptest.NewRecorder()
				srv.ServeHTTP(response, req)
				return response
			}
			if response := post(); response.Code != http.StatusOK {
				t.Fatalf("late provider acceptance status=%d body=%s", response.Code, response.Body.String())
			}
			got, found, err := repo.getAddress(context.Background(), address)
			if err != nil || !found || got.Bound {
				t.Errorf("late provider acceptance bound address: found=%v bound=%v err=%v", found, got.Bound, err)
			}
			if response := post(); response.Code != http.StatusBadRequest {
				t.Errorf("expired retry status=%d body=%s", response.Code, response.Body.String())
			}
			if calls != 1 {
				t.Errorf("expired retry repeated provider call: %d", calls)
			}
		})
	}
}

// The same contract runs through the real SDK/emulator, without depending on GC.
func testWakeExpiryFencesProviderCompletion(t *testing.T, repo Repository) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	limit := RateLimit{Max: 100, Window: time.Hour}
	for _, tc := range []struct {
		name            string
		status          int
		addressLifetime time.Duration
		completionAfter time.Duration
	}{
		{"late_acceptance", http.StatusOK, 10 * time.Minute, wakeWindow},
		{"late_unregistered", http.StatusGone, 10 * time.Minute, wakeWindow},
		{"expired_allocation", http.StatusOK, time.Minute, time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := tc.name
			if err := repo.putInstallation(ctx, id, installationRecord{FCMTokenEnc: []byte("token"), TokenGeneration: 1, LastActiveMs: now.UnixMilli()}); err != nil {
				t.Fatal(err)
			}
			address := addressRecord{InstallationID: id, SubmitCapHash: hashSecret("cap"), UnboundExpiresMs: now.Add(tc.addressLifetime).UnixMilli()}
			if created, err := repo.putAddressIfBelowLimit(ctx, id, id, address, 20, now); err != nil || !created {
				t.Fatalf("create=%v err=%v", created, err)
			}
			claim, claimed, err := repo.claimWake(ctx, id, "lease", id, hashSecret("cap"), now, now.Add(wakeWindow), limit, id, limit)
			if err != nil || !claimed {
				t.Fatalf("claim=%+v claimed=%v err=%v", claim, claimed, err)
			}
			if _, err := repo.completeWake(ctx, id, "lease", claim.TokenGeneration, tc.status, []byte("response"), tc.status == http.StatusGone, now.Add(tc.completionAfter)); err != nil {
				t.Fatal(err)
			}
			got, found, err := repo.getAddress(ctx, id)
			if err != nil || !found || got.Bound {
				t.Errorf("late completion bound allocation: found=%v bound=%v err=%v", found, got.Bound, err)
			}
			inst, found, err := repo.getInstallation(ctx, id)
			if err != nil || !found || inst.TokenDead || string(inst.FCMTokenEnc) != "token" {
				t.Errorf("late completion changed token: found=%v dead=%v err=%v", found, inst.TokenDead, err)
			}
		})
	}
	const id = "completed_expiry"
	if err := repo.putInstallation(ctx, id, installationRecord{FCMTokenEnc: []byte("token"), TokenGeneration: 1, LastActiveMs: now.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if created, err := repo.putAddressIfBelowLimit(ctx, id, id, addressRecord{InstallationID: id, SubmitCapHash: hashSecret("cap"), Bound: true}, 20, now); err != nil || !created {
		t.Fatalf("create=%v err=%v", created, err)
	}
	deadline := now.Add(wakeWindow)
	claim, claimed, err := repo.claimWake(ctx, id, "lease", id, hashSecret("cap"), now, deadline, limit, id, limit)
	if err != nil || !claimed {
		t.Fatalf("claim=%+v claimed=%v err=%v", claim, claimed, err)
	}
	if _, err := repo.completeWake(ctx, id, "lease", claim.TokenGeneration, http.StatusOK, []byte("response"), false, now); err != nil {
		t.Fatal(err)
	}
	for _, attemptID := range []string{id, "never_claimed"} {
		claim, claimed, err := repo.claimWake(ctx, attemptID, "retry", id, hashSecret("cap"), deadline, deadline, limit, id, limit)
		if err != nil || claimed || claim.Completed || claim.Denied != "exhausted" {
			t.Errorf("expired attempt %s: claim=%+v claimed=%v err=%v", attemptID, claim, claimed, err)
		}
	}
}

func TestStaleUnregisteredCannotEraseRotatedToken(t *testing.T) {
	ctx := context.Background()
	repository := NewMemoryRepository().(*memoryRepository)
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	if err := repository.putInstallation(ctx, "installation", installationRecord{FCMTokenEnc: []byte("old"), TokenGeneration: 1, LastActiveMs: now.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	address := addressRecord{InstallationID: "installation", SubmitCapHash: hashSecret("cap"), Bound: true}
	if created, err := repository.putAddressIfBelowLimit(ctx, "address", "installation", address, 10, now); err != nil || !created {
		t.Fatal(err)
	}
	limit := RateLimit{Max: 10, Window: time.Hour}
	claim, claimed, err := repository.claimWake(ctx, "wake", "lease", "address", hashSecret("cap"), now, now.Add(wakeWindow), limit, "source", limit)
	if err != nil || !claimed {
		t.Fatalf("claim=%+v claimed=%v err=%v", claim, claimed, err)
	}
	if updated, err := repository.rotateToken(ctx, "installation", []byte("new"), "2", now); err != nil || !updated {
		t.Fatalf("rotate updated=%v err=%v", updated, err)
	}
	if _, err := repository.completeWake(ctx, "wake", "lease", claim.TokenGeneration, 410, nil, true, now); err != nil {
		t.Fatal(err)
	}
	got, _, _ := repository.getInstallation(ctx, "installation")
	if got.TokenDead || string(got.FCMTokenEnc) != "new" || got.TokenGeneration != 2 {
		t.Fatalf("stale UNREGISTERED changed rotated token: %+v", got)
	}
}

func TestLogicalExpiryIsClosedAtTheBoundary(t *testing.T) {
	ctx := context.Background()
	repository := NewMemoryRepository().(*memoryRepository)
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	publicKey := []byte("public-key")
	if err := repository.putInstallation(ctx, "expired", installationRecord{PublicKey: publicKey, LastActiveMs: now.Add(-180 * 24 * time.Hour).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if accepted, err := repository.claimNonceAndTouch(ctx, "expired", publicKey, "nonce", now, now.Add(time.Minute)); err != nil || accepted {
		t.Fatalf("expiry-boundary nonce accepted=%v err=%v", accepted, err)
	}
	if updated, err := repository.rotateToken(ctx, "expired", []byte("new"), "v2", now); err != nil || updated {
		t.Fatalf("expiry-boundary rotation updated=%v err=%v", updated, err)
	}
	repository.addresses["expired-address"] = addressRecord{InstallationID: "expired", SubmitCapHash: hashSecret("expired-cap"), Bound: true}
	limit := RateLimit{Max: 10, Window: time.Hour}
	if claim, claimed, err := repository.claimWake(ctx, "expired-wake", "lease", "expired-address", hashSecret("expired-cap"), now, now.Add(wakeWindow), limit, "expired-source", limit); err != nil || claimed || claim.Denied != "unauthorized" {
		t.Fatalf("expiry-boundary installation wake=%+v claimed=%v err=%v", claim, claimed, err)
	}
	if err := repository.putInstallation(ctx, "active", installationRecord{LastActiveMs: now.UnixMilli(), FCMTokenEnc: []byte("token"), TokenGeneration: 1}); err != nil {
		t.Fatal(err)
	}
	address := addressRecord{InstallationID: "active", SubmitCapHash: hashSecret("cap"), Bound: false, UnboundExpiresMs: now.UnixMilli()}
	if created, err := repository.putAddressIfBelowLimit(ctx, "address", "active", address, 20, now); err != nil || !created {
		t.Fatalf("address created=%v err=%v", created, err)
	}
	claim, claimed, err := repository.claimWake(ctx, "wake", "lease", "address", hashSecret("cap"), now, now.Add(wakeWindow), limit, "source", limit)
	if err != nil || claimed || claim.Denied != "unauthorized" {
		t.Fatalf("expiry-boundary claim=%+v claimed=%v err=%v", claim, claimed, err)
	}
	repository.installations["active"] = installationRecord{LastActiveMs: now.UnixMilli(), TokenGeneration: int64(^uint64(0) >> 1)}
	if updated, err := repository.rotateToken(ctx, "active", []byte("overflow"), "v2", now); err == nil || updated {
		t.Fatalf("generation-overflow rotation updated=%v err=%v", updated, err)
	}
}

func TestClosedBetaAdmissionPrecedesRepositoryRead(t *testing.T) {
	repository := NewMemoryRepository().(*memoryRepository)
	srv, err := NewFirestoreServer(Config{
		Repository: repository, TokenKeys: map[string][]byte{"v1": bytes.Repeat([]byte{1}, 32)},
		ActiveTokenKeyVersion: "v1", RegistrationDigestKey: bytes.Repeat([]byte{2}, 32),
		RegistrationAdmission: func(string) bool { return false }, Sender: firestoreTestSender{}, Attest: firestoreTestAttestor{},
	})
	if err != nil {
		t.Fatal(err)
	}
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := firestoreTestPublicKey(t, privateKey)
	body, _ := json.Marshal(map[string]any{"installation_public_key": publicKey, "fcm_token": "token", "attestation": map[string]any{"kind": "play_integrity", "token": "verdict"}})
	request := httptest.NewRequest(http.MethodPost, "/v1/installations", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "CCCCCCCCCCCCCCCCCCCCCC")
	response := httptest.NewRecorder()
	srv.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if repository.registrationLookups != 0 {
		t.Fatalf("repository registration lookups=%d, want 0 for denied key", repository.registrationLookups)
	}
}
