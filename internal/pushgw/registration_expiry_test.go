package pushgw

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/Nathandela/swarm/internal/pushreg"
)

type registrationExpiryDecoder struct {
	payload PlayIntegrityPayload
	calls   int
}

func (d *registrationExpiryDecoder) Decode(context.Context, string, string) (PlayIntegrityPayload, error) {
	d.calls++
	return d.payload, nil
}

func TestRegistrationExpiry_OldVerdictCannotMintSecondInstallation(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		repository := NewMemoryRepository().(*memoryRepository)
		testRegistrationExpiry(t, repository, func(t *testing.T, originalID string) {
			t.Helper()
			repository.mu.Lock()
			defer repository.mu.Unlock()
			if count := len(repository.installations); count != 1 {
				t.Fatalf("installations=%d, want 1", count)
			}
			if _, ok := repository.installations[originalID]; !ok {
				t.Fatal("original installation was removed")
			}
		})
	})

	t.Run("firestore", func(t *testing.T) {
		if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
			t.Skip("requires Firestore emulator")
		}
		ctx := context.Background()
		client, err := firestore.NewClient(ctx, "demo-swarm-push-probe")
		if err != nil {
			t.Fatal(err)
		}
		repository, err := NewFirestoreRepository(client, fmt.Sprintf("go-registration-expiry-%d", time.Now().UnixNano()))
		if err != nil {
			t.Fatal(err)
		}
		persistence := repository.(*firestorePersistence)
		testRegistrationExpiry(t, repository, func(t *testing.T, originalID string) {
			t.Helper()
			installations, err := persistence.col("installations").Documents(ctx).GetAll()
			if err != nil {
				t.Fatal(err)
			}
			if len(installations) != 1 {
				t.Fatalf("installations=%d, want 1", len(installations))
			}
			if installations[0].Ref.ID != originalID {
				t.Fatalf("remaining installation=%q, want original %q", installations[0].Ref.ID, originalID)
			}
		})
	})
}

func testRegistrationExpiry(t *testing.T, repository Repository, assertOriginal func(*testing.T, string)) {
	t.Helper()
	serverStart := time.Unix(1_800_000_000, 0).UTC()
	// Exercise the longest-lived verdict production accepts: one issued at the maximum
	// allowed future skew. The idempotency window must still outlive its full lifetime.
	verdictIssued := serverStart.Add(30 * time.Second)
	now := serverStart
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := firestoreTestPublicKey(t, privateKey)
	const (
		idempotencyKey = "registration-expiry000"
		fcmToken       = "registration-expiry-token"
		verdictToken   = "registration-expiry-verdict"
	)
	body, err := json.Marshal(registerRequest{
		InstallationPublicKey: publicKey,
		FCMToken:              fcmToken,
		Attestation: struct {
			Kind  string `json:"kind"`
			Token string `json:"token"`
		}{Kind: "play_integrity", Token: verdictToken},
	})
	if err != nil {
		t.Fatal(err)
	}
	requestHash, err := pushreg.RequestHash(publicKey, fcmToken)
	if err != nil {
		t.Fatal(err)
	}
	certificate := sha256.Sum256([]byte("registration-expiry-certificate"))
	certificateText := base64.RawURLEncoding.EncodeToString(certificate[:])
	decoder := &registrationExpiryDecoder{payload: validPlayIntegrityPayload(
		verdictIssued,
		base64.RawURLEncoding.EncodeToString(requestHash[:]),
		certificateText,
	)}
	verifier, err := NewPlayIntegrityVerifier(PlayIntegrityConfig{
		PackageName:              ProductionAndroidPackage,
		AllowedCertificateSHA256: []string{certificateText},
		MaxVerdictAge:            2 * time.Minute,
		MaxFutureSkew:            30 * time.Second,
		Now:                      func() time.Time { return now },
		Decode:                   decoder,
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewFirestoreServer(Config{
		Repository:            repository,
		TokenKeys:             map[string][]byte{"1": bytes.Repeat([]byte{1}, 32)},
		ActiveTokenKeyVersion: "1",
		RegistrationDigestKey: bytes.Repeat([]byte{2}, 32),
		RegistrationAdmission: func(candidate string) bool { return candidate == publicKey },
		Sender:                firestoreTestSender{},
		Attest:                verifier,
		Now:                   func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()

	post := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/v1/installations", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", idempotencyKey)
		request.Header.Set("Swarm-Registration-Proof", firestoreTestRegistrationProof(t, privateKey, idempotencyKey, body))
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		return response
	}
	decodeCreated := func(response *httptest.ResponseRecorder) registerResponse {
		t.Helper()
		if response.Code != http.StatusCreated {
			t.Fatalf("registration status=%d body=%s", response.Code, response.Body.String())
		}
		var result registerResponse
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}

	first := decodeCreated(post())
	if decoder.calls != 1 {
		t.Fatalf("first registration decode calls=%d, want 1", decoder.calls)
	}

	// A byte-identical retry remains recoverable for the full durable idempotency window,
	// even after the underlying Play verdict has become too old to verify again.
	now = serverStart.Add(registrationWindow - time.Millisecond)
	replayed := decodeCreated(post())
	if replayed.InstallationID != first.InstallationID {
		t.Fatalf("cached replay installation=%q, want original %q", replayed.InstallationID, first.InstallationID)
	}
	if decoder.calls != 1 {
		t.Fatalf("cached replay decode calls=%d, want 1", decoder.calls)
	}

	// Once the idempotency row expires, the exact saved body reaches attestation again.
	// Production's two-minute verdict-age policy is shorter than that ten-minute row, so
	// the old verdict cannot authorize a second installation.
	now = serverStart.Add(registrationWindow)
	expired := post()
	if expired.Code != http.StatusForbidden {
		t.Fatalf("expired replay status=%d body=%s, want 403", expired.Code, expired.Body.String())
	}
	var refusal wireError
	if err := json.Unmarshal(expired.Body.Bytes(), &refusal); err != nil {
		t.Fatal(err)
	}
	if refusal.Code != "attestation_invalid" {
		t.Fatalf("expired replay refusal=%q, want attestation_invalid", refusal.Code)
	}
	if decoder.calls != 2 {
		t.Fatalf("expired replay decode calls=%d, want 2", decoder.calls)
	}
	assertOriginal(t, first.InstallationID)
}
