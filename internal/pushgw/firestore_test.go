package pushgw

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/Nathandela/swarm/internal/pushreg"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// These tests use the pinned emulator runner documented under
// docs/verification/remote-scale-to-zero/push-probe. They intentionally exercise two
// independent clients: process-local replay and quota state would make them fail.
func TestFirestoreRepositorySharedClaims(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("requires Firestore emulator")
	}
	ctx := context.Background()
	clientA, err := firestore.NewClient(ctx, "demo-swarm-push-probe")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = clientA.Close() }()
	clientB, err := firestore.NewClient(ctx, "demo-swarm-push-probe")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = clientB.Close() }()

	namespace := "go-shared-claims-" + time.Now().Format("150405.000000000")
	a := newFirestorePersistence(clientA, namespace)
	b := newFirestorePersistence(clientB, namespace)
	now := time.Now().UTC()
	const installationID = "installation"
	if err := a.putInstallation(ctx, installationID, installationRecord{LastActiveMs: now.UnixMilli()}); err != nil {
		t.Fatal(err)
	}

	results := make(chan bool, 2)
	for _, repo := range []Repository{a, b} {
		go func(repo Repository) {
			ok, err := repo.claimNonceAndTouch(ctx, installationID, nil, "nonce", now, now.Add(2*time.Minute))
			if err != nil {
				t.Errorf("claim nonce: %v", err)
			}
			results <- ok
		}(repo)
	}
	accepted := 0
	for range 2 {
		if <-results {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted nonce claims = %d, want exactly 1", accepted)
	}

	limit := RateLimit{Max: 1, Window: time.Minute}
	if ok, _, err := a.allow(ctx, "shared", limit, now); err != nil || !ok {
		t.Fatalf("first quota claim ok=%v err=%v", ok, err)
	}
	if ok, _, err := b.allow(ctx, "shared", limit, now); err != nil || ok {
		t.Fatalf("second quota claim ok=%v err=%v, want shared refusal", ok, err)
	}
}

func TestFirestoreHealthCheckIsReadOnly(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("requires Firestore emulator")
	}
	ctx := context.Background()
	client, err := firestore.NewClient(ctx, "demo-swarm-push-probe")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	repo := newFirestorePersistence(client, "go-health-"+time.Now().Format("150405000000000"))
	if err := repo.healthCheck(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.col("metadata").Doc("health").Get(ctx); err == nil {
		t.Fatal("health check persisted its probe document")
	}
}

func TestFirestoreMissingTransactionSnapshotHasServerReadTime(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("requires Firestore emulator")
	}
	ctx := context.Background()
	client, err := firestore.NewClient(ctx, "demo-swarm-push-probe")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	ref := client.Collection("go-read-time-" + time.Now().Format("150405000000000")).Doc("missing")
	var readTime time.Time
	if err := client.RunTransaction(ctx, func(_ context.Context, tx *firestore.Transaction) error {
		snap, getErr := tx.Get(ref)
		if status.Code(getErr) != codes.NotFound {
			return fmt.Errorf("missing transaction get: %w", getErr)
		}
		if snap == nil || snap.Exists() {
			return errors.New("missing transaction get did not return a missing snapshot")
		}
		readTime = snap.ReadTime
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if readTime.IsZero() {
		t.Fatal("missing transaction snapshot has zero server ReadTime")
	}
}

type firestoreTestSender struct{}

func firestoreTestPublicKey(t *testing.T, private *ecdsa.PrivateKey) string {
	t.Helper()
	public, err := private.PublicKey.ECDH()
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(public.Bytes())
}

func firestoreTestRegistrationProof(t *testing.T, private *ecdsa.PrivateKey, idempotencyKey string, body []byte) string {
	t.Helper()
	digest := sha256.Sum256(pushreg.RegistrationProofMessage(idempotencyKey, body))
	r, s, err := ecdsa.Sign(rand.Reader, private, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	n := private.Curve.Params().N
	if half := new(big.Int).Rsh(n, 1); s.Cmp(half) > 0 {
		s.Sub(n, s)
	}
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	return "p256-sha256 " + base64.RawURLEncoding.EncodeToString(signature)
}

func (firestoreTestSender) Send(context.Context, string, []byte) error { return nil }

type countingFirestoreSender struct {
	calls        atomic.Int32
	firstStarted chan struct{}
}

func (s *countingFirestoreSender) Send(ctx context.Context, _ string, _ []byte) error {
	if s.calls.Add(1) == 1 && s.firstStarted != nil {
		close(s.firstStarted)
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

type firestoreTestAttestor struct {
	bindings map[string]VerdictBinding
	calls    *atomic.Int32
}

type singleUseFirestoreAttestor struct {
	binding VerdictBinding
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (a *singleUseFirestoreAttestor) Verify(context.Context, string) (VerdictBinding, error) {
	if a.calls.Add(1) != 1 {
		return VerdictBinding{}, ErrAttestationUnavailable
	}
	close(a.started)
	<-a.release
	return a.binding, nil
}

func (a firestoreTestAttestor) Verify(_ context.Context, token string) (VerdictBinding, error) {
	if a.calls != nil {
		a.calls.Add(1)
	}
	return a.bindings[token], nil
}

func TestFirestoreServerRegistrationIsSharedAndRejectsBodyMismatch(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("requires Firestore emulator")
	}
	ctx := context.Background()
	client, err := firestore.NewClient(ctx, "demo-swarm-push-probe")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	repository, err := NewFirestoreRepository(client, "go-handler-"+time.Now().Format("150405000000000"))
	if err != nil {
		t.Fatal(err)
	}
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := firestoreTestPublicKey(t, privateKey)
	request := registerRequest{InstallationPublicKey: publicKey, FCMToken: "fcm-one"}
	request.Attestation.Kind, request.Attestation.Token = "play_integrity", "one"
	body, _ := json.Marshal(request)
	wantHash, err := pushreg.RequestHash(publicKey, request.FCMToken)
	if err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{7}, 32)
	attestor := &singleUseFirestoreAttestor{binding: VerdictBinding{RequestHash: wantHash, LicensedBuild: true}, started: make(chan struct{}), release: make(chan struct{})}
	newServer := func() *httptest.Server {
		srv, err := NewFirestoreServer(Config{Repository: repository, TokenKeys: map[string][]byte{"1": key}, ActiveTokenKeyVersion: "1", RegistrationDigestKey: bytes.Repeat([]byte{9}, 32), RegistrationAdmission: func(string) bool { return true }, Sender: firestoreTestSender{}, Attest: attestor})
		if err != nil {
			t.Fatal(err)
		}
		ts := httptest.NewServer(srv)
		t.Cleanup(ts.Close)
		return ts
	}
	a, b := newServer(), newServer()
	post := func(url string, body []byte) *http.Response {
		req, _ := http.NewRequest(http.MethodPost, url+"/v1/installations", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		const idem = "AAAAAAAAAAAAAAAAAAAAAA"
		req.Header.Set("Idempotency-Key", idem)
		req.Header.Set("Swarm-Registration-Proof", firestoreTestRegistrationProof(t, privateKey, idem, body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	firstResult := make(chan *http.Response, 1)
	go func() { firstResult <- post(a.URL, body) }()
	<-attestor.started
	concurrent := post(b.URL, body)
	defer func() { _ = concurrent.Body.Close() }()
	if concurrent.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("concurrent registration status=%d, want 503 while the sole attestation owner is active", concurrent.StatusCode)
	}
	close(attestor.release)
	first := <-firstResult
	defer func() { _ = first.Body.Close() }()
	var one registerResponse
	if err := json.NewDecoder(first.Body).Decode(&one); err != nil || first.StatusCode != http.StatusCreated {
		t.Fatalf("first status=%d result=%+v err=%v", first.StatusCode, one, err)
	}
	second := post(b.URL, body)
	defer func() { _ = second.Body.Close() }()
	var two registerResponse
	if err := json.NewDecoder(second.Body).Decode(&two); err != nil || second.StatusCode != http.StatusCreated || two.InstallationID != one.InstallationID {
		t.Fatalf("retry status=%d first=%+v second=%+v err=%v", second.StatusCode, one, two, err)
	}
	request.FCMToken = "fcm-two"
	request.Attestation.Token = "two"
	mismatchBody, _ := json.Marshal(request)
	mismatch := post(b.URL, mismatchBody)
	defer func() { _ = mismatch.Body.Close() }()
	_, _ = io.Copy(io.Discard, mismatch.Body)
	if mismatch.StatusCode != http.StatusConflict {
		t.Fatalf("body mismatch status=%d, want 409", mismatch.StatusCode)
	}
	if got := attestor.calls.Load(); got != 1 {
		t.Fatalf("attestation calls = %d, want 1; retries and body conflicts must resolve from shared idempotency state first", got)
	}
}

func TestFirestoreRegistrationProofRejectsNonHolderWithoutStateAndReplayIsFree(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("requires Firestore emulator")
	}
	ctx := context.Background()
	client, err := firestore.NewClient(ctx, "demo-swarm-push-probe")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	namespace := fmt.Sprintf("go-register-proof-%d", time.Now().UnixNano())
	repository, err := NewFirestoreRepository(client, namespace)
	if err != nil {
		t.Fatal(err)
	}
	persistence := repository.(*firestorePersistence)
	holder, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	nonholder, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := firestoreTestPublicKey(t, holder)
	const (
		idem        = "EEEEEEEEEEEEEEEEEEEEEE"
		attestation = "proof-verdict"
	)
	body, _ := json.Marshal(map[string]any{
		"installation_public_key": publicKey,
		"fcm_token":               "proof-fcm-token",
		"attestation":             map[string]any{"kind": "play_integrity", "token": attestation},
	})
	wantHash, err := pushreg.RequestHash(publicKey, "proof-fcm-token")
	if err != nil {
		t.Fatal(err)
	}
	var attestationCalls atomic.Int32
	srv, err := NewFirestoreServer(Config{
		Repository: repository, TokenKeys: map[string][]byte{"1": bytes.Repeat([]byte{7}, 32)},
		ActiveTokenKeyVersion: "1", RegistrationDigestKey: bytes.Repeat([]byte{9}, 32),
		RegistrationAdmission: func(candidate string) bool { return candidate == publicKey },
		Sender:                firestoreTestSender{},
		Attest:                firestoreTestAttestor{bindings: map[string]VerdictBinding{attestation: {RequestHash: wantHash, LicensedBuild: true}}, calls: &attestationCalls},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	post := func(signer *ecdsa.PrivateKey) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/v1/installations", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", idem)
		request.Header.Set("Swarm-Registration-Proof", firestoreTestRegistrationProof(t, signer, idem, body))
		response := httptest.NewRecorder()
		srv.ServeHTTP(response, request)
		return response
	}
	collectionCount := func(name string) int {
		snapshots, err := persistence.col(name).Documents(ctx).GetAll()
		if err != nil {
			t.Fatal(err)
		}
		return len(snapshots)
	}
	quotaState := func() (int, int64) {
		snapshots, err := persistence.col("rate_windows").Documents(ctx).GetAll()
		if err != nil {
			t.Fatal(err)
		}
		var total int64
		for _, snapshot := range snapshots {
			var record rateWindowRecord
			if err := snapshot.DataTo(&record); err != nil {
				t.Fatal(err)
			}
			total += record.Count
		}
		return len(snapshots), total
	}

	if response := post(nonholder); response.Code != http.StatusUnauthorized {
		t.Fatalf("nonholder status=%d body=%s", response.Code, response.Body.String())
	}
	for _, collection := range []string{"installations", "registration_attempts", "rate_windows"} {
		if got := collectionCount(collection); got != 0 {
			t.Fatalf("nonholder created %d documents in %s, want 0", got, collection)
		}
	}
	if got := attestationCalls.Load(); got != 0 {
		t.Fatalf("nonholder attestation calls=%d, want 0", got)
	}

	first := post(holder)
	if first.Code != http.StatusCreated {
		t.Fatalf("holder status=%d body=%s", first.Code, first.Body.String())
	}
	var firstResult registerResponse
	if err := json.Unmarshal(first.Body.Bytes(), &firstResult); err != nil {
		t.Fatal(err)
	}
	if firstResult.InstallationID == "" {
		t.Fatal("holder returned an empty installation id")
	}
	quotaDocs, quotaTotal := quotaState()
	if quotaDocs == 0 || quotaTotal == 0 {
		t.Fatalf("successful registration did not charge durable quota: documents=%d total=%d", quotaDocs, quotaTotal)
	}
	replay := post(holder)
	if replay.Code != http.StatusCreated {
		t.Fatalf("signed replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	var replayResult registerResponse
	if err := json.Unmarshal(replay.Body.Bytes(), &replayResult); err != nil {
		t.Fatal(err)
	}
	if replayResult.InstallationID != firstResult.InstallationID {
		t.Fatalf("signed replay installation id=%q, want %q", replayResult.InstallationID, firstResult.InstallationID)
	}
	if got := attestationCalls.Load(); got != 1 {
		t.Fatalf("attestation calls after replay=%d, want 1", got)
	}
	if gotDocs, gotTotal := quotaState(); gotDocs != quotaDocs || gotTotal != quotaTotal {
		t.Fatalf("signed replay changed quota: before=(%d,%d) after=(%d,%d)", quotaDocs, quotaTotal, gotDocs, gotTotal)
	}
	if got := collectionCount("installations"); got != 1 {
		t.Fatalf("installations after replay=%d, want 1", got)
	}
}

func TestFirestoreSecretsAreNotPersistedInPlaintext(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("requires Firestore emulator")
	}
	ctx := context.Background()
	client, err := firestore.NewClient(ctx, "demo-swarm-push-probe")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	namespace := "go-secrets-" + time.Now().Format("150405000000000")
	repository, err := NewFirestoreRepository(client, namespace)
	if err != nil {
		t.Fatal(err)
	}
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := firestoreTestPublicKey(t, privateKey)
	const (
		fcmToken    = "fcm-secret-never-plaintext"
		attestation = "attestation-secret-never-persisted"
		submitCap   = "submit-secret-never-persisted"
		machineCap  = "revoke-secret-never-persisted"
		pushAddress = "address-must-not-survive-revoke"
	)
	wantHash, err := pushreg.RequestHash(publicKey, fcmToken)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewFirestoreServer(Config{
		Repository:            repository,
		TokenKeys:             map[string][]byte{"1": bytes.Repeat([]byte{7}, 32)},
		ActiveTokenKeyVersion: "1",
		RegistrationDigestKey: bytes.Repeat([]byte{9}, 32),
		RegistrationAdmission: func(string) bool { return true },
		Sender:                firestoreTestSender{},
		Attest:                firestoreTestAttestor{bindings: map[string]VerdictBinding{attestation: {RequestHash: wantHash, LicensedBuild: true}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	ts := httptest.NewServer(srv)
	defer ts.Close()
	body, _ := json.Marshal(map[string]any{
		"installation_public_key": publicKey,
		"fcm_token":               fcmToken,
		"attestation":             map[string]any{"kind": "play_integrity", "token": attestation},
	})
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/installations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	const idem = "BBBBBBBBBBBBBBBBBBBBBB"
	registrationProof := firestoreTestRegistrationProof(t, privateKey, idem, body)
	req.Header.Set("Idempotency-Key", idem)
	req.Header.Set("Swarm-Registration-Proof", registrationProof)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var registered registerResponse
	if err := json.NewDecoder(resp.Body).Decode(&registered); err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("register status=%d result=%+v err=%v", resp.StatusCode, registered, err)
	}

	repo := newFirestorePersistence(client, namespace)
	address := addressRecord{InstallationID: registered.InstallationID, SubmitCapHash: hashSecret(submitCap), MachineRevokeHash: hashSecret(machineCap), Bound: true}
	if created, err := repo.putAddressIfBelowLimit(ctx, pushAddress, registered.InstallationID, address, 20, time.Now()); err != nil || !created {
		t.Fatalf("create address=%v err=%v", created, err)
	}
	if err := repo.deleteAddressAndTombstone(ctx, pushAddress, address, time.Now()); err != nil {
		t.Fatal(err)
	}
	var persisted bytes.Buffer
	for _, collection := range []string{"installations", "addresses", "revocation_tombstones", "registration_attempts", "nonce_claims", "wake_attempts", "rate_windows", "metadata"} {
		snapshots, err := repo.col(collection).Documents(ctx).GetAll()
		if err != nil {
			t.Fatal(err)
		}
		for _, snapshot := range snapshots {
			persisted.WriteString(snapshot.Ref.ID)
			encoded, err := json.Marshal(snapshot.Data())
			if err != nil {
				t.Fatal(err)
			}
			persisted.Write(encoded)
		}
	}
	for label, secret := range map[string]string{
		"FCM token": fcmToken, "attestation token": attestation,
		"submit capability": submitCap, "machine-revoke capability": machineCap,
		"revoked address": pushAddress, "registration proof": registrationProof,
		"raw idempotency key": idem,
	} {
		if bytes.Contains(persisted.Bytes(), []byte(secret)) {
			t.Fatalf("Firestore documents contain raw %s", label)
		}
	}
}

func TestFirestoreRegistrationIdempotencyRejectsBodyMismatch(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("requires Firestore emulator")
	}
	ctx := context.Background()
	client, err := firestore.NewClient(ctx, "demo-swarm-push-probe")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	repo := newFirestorePersistence(client, "go-idempotency-"+time.Now().Format("150405.000000000"))
	now := time.Now().UTC()
	first, created, mismatch, err := repo.registerOrReturn(ctx, "key", "body-a", "one", installationRecord{}, now)
	if err != nil || !created || mismatch || first.InstallationID != "one" {
		t.Fatalf("first=%+v created=%v mismatch=%v err=%v", first, created, mismatch, err)
	}
	got, created, mismatch, err := repo.registerOrReturn(ctx, "key", "body-b", "two", installationRecord{}, now)
	if err != nil || created || !mismatch || got.InstallationID != "" {
		t.Fatalf("mismatch got=%+v created=%v mismatch=%v err=%v", got, created, mismatch, err)
	}
}

func TestFirestoreWakeLeaseCASAndTokenGeneration(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("requires Firestore emulator")
	}
	ctx := context.Background()
	client, err := firestore.NewClient(ctx, "demo-swarm-push-probe")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	repo := newFirestorePersistence(client, "go-wake-"+time.Now().Format("150405000000000"))
	now := time.Now().UTC()
	if err := repo.putInstallation(ctx, "installation", installationRecord{FCMTokenEnc: []byte("old"), TokenKeyVersion: "1", TokenGeneration: 1, LastActiveMs: now.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	address := addressRecord{InstallationID: "installation", SubmitCapHash: hashSecret("cap"), MachineRevokeHash: hashSecret("revoke"), Bound: true}
	if created, err := repo.putAddressIfBelowLimit(ctx, "address", "installation", address, 10, now); err != nil || !created {
		t.Fatalf("create address=%v err=%v", created, err)
	}
	limit := RateLimit{Max: 10, Window: time.Hour}
	deadline := now.Add(wakeWindow)
	first, claimed, err := repo.claimWake(ctx, "wake", "lease-one", "address", hashSecret("cap"), now, deadline, limit, "source", limit)
	if err != nil || !claimed {
		t.Fatalf("first=%+v claimed=%v err=%v", first, claimed, err)
	}
	if updated, err := repo.rotateToken(ctx, "installation", []byte("new"), "2", now); err != nil || !updated {
		t.Fatalf("rotate=%v err=%v", updated, err)
	}
	if stale, err := repo.completeWake(ctx, "wake", "lease-one", first.TokenGeneration, http.StatusGone, nil, true, now); err != nil || !stale {
		t.Fatal(err)
	}
	installation, _, err := repo.getInstallation(ctx, "installation")
	if err != nil {
		t.Fatal(err)
	}
	if installation.TokenDead || string(installation.FCMTokenEnc) != "new" || installation.TokenGeneration != 2 {
		t.Fatalf("stale completion changed token: %+v", installation)
	}

	later := now.Add(wakeLease + time.Second)
	second, claimed, err := repo.claimWake(ctx, "wake", "lease-two", "address", hashSecret("cap"), later, later.Add(wakeWindow), limit, "source", limit)
	if err != nil || !claimed || second.TokenGeneration != 2 {
		t.Fatalf("takeover=%+v claimed=%v err=%v", second, claimed, err)
	}
	if _, err := repo.completeWake(ctx, "wake", "lease-one", second.TokenGeneration, http.StatusOK, []byte("stale"), false, later); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.completeWake(ctx, "wake", "lease-two", second.TokenGeneration, http.StatusOK, []byte("ok"), false, later); err != nil {
		t.Fatal(err)
	}
	cached, claimed, err := repo.claimWake(ctx, "wake", "lease-three", "address", hashSecret("cap"), later, deadline, limit, "source", limit)
	if err != nil || claimed || !cached.Completed || string(cached.Body) != "ok" {
		t.Fatalf("cached=%+v claimed=%v err=%v", cached, claimed, err)
	}

	revokedAddress := addressRecord{InstallationID: "installation", SubmitCapHash: hashSecret("revoked-cap"), MachineRevokeHash: hashSecret("revoked-revoke"), Bound: true}
	if created, err := repo.putAddressIfBelowLimit(ctx, "revoked-address", "installation", revokedAddress, 10, later); err != nil || !created {
		t.Fatalf("revoke-race address=%v err=%v", created, err)
	}
	revokedClaim, claimed, err := repo.claimWake(ctx, "revoked-wake", "revoked-lease", "revoked-address", hashSecret("revoked-cap"), later, deadline, limit, "revoked-source", limit)
	if err != nil || !claimed {
		t.Fatalf("revoke-race claim=%+v claimed=%v err=%v", revokedClaim, claimed, err)
	}
	if err := repo.deleteAddressAndTombstone(ctx, "revoked-address", revokedAddress, later); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.completeWake(ctx, "revoked-wake", "revoked-lease", revokedClaim.TokenGeneration, http.StatusOK, []byte("ok"), false, later); err != nil {
		t.Fatal(err)
	}
	if _, found, err := repo.getAddress(ctx, "revoked-address"); err != nil || found {
		t.Fatalf("wake completion recreated concurrently revoked address: found=%v err=%v", found, err)
	}
}

func TestFirestoreWakeClaimUsesServerReadTime(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("requires Firestore emulator")
	}
	ctx := context.Background()
	client, err := firestore.NewClient(ctx, "demo-swarm-push-probe")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	repo := newFirestorePersistence(client, "go-wake-clock-"+time.Now().Format("150405000000000"))
	clockRef := repo.col("metadata").Doc("clock")
	if _, err := clockRef.Set(ctx, map[string]any{"marker": true}); err != nil {
		t.Fatal(err)
	}
	clockSnap, err := clockRef.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	serverNow := clockSnap.ReadTime
	if err := repo.putInstallation(ctx, "installation", installationRecord{FCMTokenEnc: []byte("token"), TokenGeneration: 1, LastActiveMs: serverNow.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	address := addressRecord{InstallationID: "installation", SubmitCapHash: hashSecret("cap"), Bound: true}
	if created, err := repo.putAddressIfBelowLimit(ctx, "address", "installation", address, 10, serverNow); err != nil || !created {
		t.Fatalf("create address=%v err=%v", created, err)
	}
	callerNow := serverNow.Add(-24 * time.Hour)
	limit := RateLimit{Max: 100, Window: time.Hour}
	deadline := serverNow.Add(time.Minute)
	if _, claimed, err := repo.claimWake(ctx, "live", "lease", "address", hashSecret("cap"), callerNow, deadline, limit, "source-live", limit); err != nil || !claimed {
		t.Fatalf("live claim claimed=%v err=%v", claimed, err)
	}
	wakeSnap, err := repo.col("wake_attempts").Doc("live").Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var wake wakeAttemptRecord
	if err := wakeSnap.DataTo(&wake); err != nil {
		t.Fatal(err)
	}
	if leaseRemaining := time.UnixMilli(wake.LeaseUntilMs).Sub(wakeSnap.ReadTime); leaseRemaining < wakeLease-2*time.Second || leaseRemaining > wakeLease+time.Second {
		t.Fatalf("lease is not anchored to Firestore time: remaining=%v", leaseRemaining)
	}
	expiredDeadline := serverNow.Add(-time.Second)
	claim, claimed, err := repo.claimWake(ctx, "expired", "lease", "address", hashSecret("cap"), callerNow, expiredDeadline, limit, "source-expired", limit)
	if err != nil || claimed || claim.Denied != "malformed" {
		t.Fatalf("expired server-time claim=%+v claimed=%v err=%v", claim, claimed, err)
	}
}

func TestFirestoreWakeCompletionUsesServerReadTime(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("requires Firestore emulator")
	}
	ctx := context.Background()
	client, err := firestore.NewClient(ctx, "demo-swarm-push-probe")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	repo := newFirestorePersistence(client, "go-wake-complete-clock-"+time.Now().Format("150405000000000"))
	clockRef := repo.col("metadata").Doc("clock")
	if _, err := clockRef.Set(ctx, map[string]any{"marker": true}); err != nil {
		t.Fatal(err)
	}
	clockSnap, err := clockRef.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	serverNow := clockSnap.ReadTime
	if err := repo.putInstallation(ctx, "installation", installationRecord{FCMTokenEnc: []byte("token"), TokenGeneration: 1, LastActiveMs: serverNow.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.col("addresses").Doc("address").Set(ctx, addressRecord{InstallationID: "installation", SubmitCapHash: hashSecret("cap"), UnboundExpiresMs: serverNow.Add(time.Minute).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	expired := wakeAttemptRecord{InstallationID: "installation", Address: "address", TokenGeneration: 1, State: "claimed", LeaseID: "lease", LeaseUntilMs: serverNow.Add(time.Minute).UnixMilli(), ExpiresAtMs: serverNow.Add(-time.Second).UnixMilli()}
	if _, err := repo.col("wake_attempts").Doc("expired-accept").Set(ctx, expired); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.completeWake(ctx, "expired-accept", "lease", 1, http.StatusOK, []byte("late"), false, serverNow.Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	gotAddress, found, err := repo.getAddress(ctx, "address")
	if err != nil || !found || gotAddress.Bound {
		t.Fatalf("expired completion bound address: found=%v address=%+v err=%v", found, gotAddress, err)
	}
	wakeSnap, err := repo.col("wake_attempts").Doc("expired-accept").Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := wakeSnap.DataTo(&expired); err != nil {
		t.Fatal(err)
	}
	if expired.State != "claimed" || string(expired.Body) != "" {
		t.Fatalf("expired completion changed attempt: %+v", expired)
	}

	if _, err := repo.col("wake_attempts").Doc("expired-lease-unregistered").Set(ctx, wakeAttemptRecord{InstallationID: "installation", TokenGeneration: 1, State: "claimed", LeaseID: "lease", LeaseUntilMs: serverNow.Add(-time.Second).UnixMilli(), ExpiresAtMs: serverNow.Add(time.Minute).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.completeWake(ctx, "expired-lease-unregistered", "lease", 1, http.StatusGone, nil, true, serverNow.Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	installation, found, err := repo.getInstallation(ctx, "installation")
	if err != nil || !found || installation.TokenDead || string(installation.FCMTokenEnc) != "token" {
		t.Fatalf("expired completion changed token: found=%v installation=%+v err=%v", found, installation, err)
	}

	if _, err := repo.col("addresses").Doc("expired-address").Set(ctx, addressRecord{InstallationID: "installation", SubmitCapHash: hashSecret("cap"), UnboundExpiresMs: serverNow.Add(-time.Second).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.col("wake_attempts").Doc("expired-allocation").Set(ctx, wakeAttemptRecord{InstallationID: "installation", Address: "expired-address", TokenGeneration: 1, State: "claimed", LeaseID: "lease", LeaseUntilMs: serverNow.Add(time.Minute).UnixMilli(), ExpiresAtMs: serverNow.Add(time.Minute).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.completeWake(ctx, "expired-allocation", "lease", 1, http.StatusOK, []byte("accepted"), false, serverNow.Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	gotAddress, found, err = repo.getAddress(ctx, "expired-address")
	if err != nil || !found || gotAddress.Bound {
		t.Fatalf("expired allocation was bound: found=%v address=%+v err=%v", found, gotAddress, err)
	}
}

func TestFirestoreRegistrationClaimHasOneProviderOwner(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("requires Firestore emulator")
	}
	ctx := context.Background()
	aClient, err := firestore.NewClient(ctx, "demo-swarm-push-probe")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = aClient.Close() }()
	bClient, err := firestore.NewClient(ctx, "demo-swarm-push-probe")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bClient.Close() }()
	namespace := "go-reg-claim-" + time.Now().Format("150405000000000")
	a, b := newFirestorePersistence(aClient, namespace), newFirestorePersistence(bClient, namespace)
	now := time.Now().UTC()
	type outcome struct {
		won, busy, mismatch bool
		err                 error
	}
	results := make(chan outcome, 2)
	for i, repo := range []*firestorePersistence{a, b} {
		go func(i int, repo *firestorePersistence) {
			_, won, busy, mismatch, err := repo.claimRegistration(ctx, "key", "body", fmt.Sprintf("candidate-%d", i), fmt.Sprintf("lease-%d", i), now)
			results <- outcome{won, busy, mismatch, err}
		}(i, repo)
	}
	wins, busy := 0, 0
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.won {
			wins++
		}
		if result.busy {
			busy++
		}
		if result.mismatch {
			t.Fatal("identical concurrent body mismatched")
		}
	}
	if wins != 1 || busy != 1 {
		t.Fatalf("wins=%d busy=%d, want one provider owner and one retry", wins, busy)
	}
}

func TestFirestoreRetentionRechecksAndCascadesBoundedly(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("requires Firestore emulator")
	}
	ctx := context.Background()
	client, err := firestore.NewClient(ctx, "demo-swarm-push-probe")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	repo := newFirestorePersistence(client, "go-gc-"+time.Now().Format("150405000000000"))
	now := time.Now().UTC()
	old := now.Add(-181 * 24 * time.Hour)
	if err := repo.putInstallation(ctx, "inactive", installationRecord{LastActiveMs: old.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	bound := addressRecord{InstallationID: "inactive", SubmitCapHash: hashSecret("bound"), MachineRevokeHash: hashSecret("bound-revoke"), Bound: true, UnboundExpiresMs: now.Add(-time.Hour).UnixMilli()}
	// Seed through Firestore because allocation correctly refuses an already-expired installation.
	if _, err := repo.col("addresses").Doc("bound").Set(ctx, bound); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.col("installations").Doc("inactive").Update(ctx, []firestore.Update{{Path: "address_count", Value: int64(1)}}); err != nil {
		t.Fatal(err)
	}
	if err := repo.putInstallation(ctx, "active", installationRecord{LastActiveMs: now.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	unbound := addressRecord{InstallationID: "active", SubmitCapHash: hashSecret("unbound"), MachineRevokeHash: hashSecret("unbound-revoke"), Bound: false, UnboundExpiresMs: now.Add(-time.Second).UnixMilli()}
	if _, err := repo.col("addresses").Doc("unbound").Set(ctx, unbound); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.col("installations").Doc("active").Update(ctx, []firestore.Update{{Path: "address_count", Value: int64(1)}}); err != nil {
		t.Fatal(err)
	}
	if err := repo.runRetention(ctx, now); err != nil {
		t.Fatal(err)
	}
	if _, found, err := repo.getInstallation(ctx, "inactive"); err != nil || found {
		t.Fatalf("inactive installation found=%v err=%v", found, err)
	}
	if _, found, err := repo.getAddress(ctx, "bound"); err != nil || found {
		t.Fatalf("bound child of inactive installation found=%v err=%v", found, err)
	}
	if _, found, err := repo.getAddress(ctx, "unbound"); err != nil || found {
		t.Fatalf("expired unbound address found=%v err=%v", found, err)
	}
	active, found, err := repo.getInstallation(ctx, "active")
	if err != nil || !found || active.AddressCount != 0 {
		t.Fatalf("active installation=%+v found=%v err=%v", active, found, err)
	}
	for _, key := range []string{hashSecret("bound-revoke"), hashSecret("unbound-revoke")} {
		tomb, found, err := repo.getTombstone(ctx, key)
		if err != nil || !found || tomb.RevokedAtMs != now.UnixMilli() {
			t.Fatalf("tombstone %s=%+v found=%v err=%v", key, tomb, found, err)
		}
	}
}

func TestFirestoreServerWakeCancellationKeepsBoundedClaim(t *testing.T) {
	if os.Getenv("FIRESTORE_EMULATOR_HOST") == "" {
		t.Skip("requires Firestore emulator")
	}
	ctx := context.Background()
	client, err := firestore.NewClient(ctx, "demo-swarm-push-probe")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	persistence := newFirestorePersistence(client, "go-handler-wake-"+time.Now().Format("150405000000000"))
	var repository Repository = persistence
	key := bytes.Repeat([]byte{4}, 32)
	digestKey := bytes.Repeat([]byte{5}, 32)
	sender := &countingFirestoreSender{firstStarted: make(chan struct{})}
	now := time.Now().UTC()
	newServer := func(clock time.Time) *Server {
		srv, err := NewFirestoreServer(Config{Repository: repository, TokenKeys: map[string][]byte{"v1": key}, ActiveTokenKeyVersion: "v1", RegistrationDigestKey: digestKey, RegistrationAdmission: func(string) bool { return true }, Sender: sender, Attest: firestoreTestAttestor{}, Now: func() time.Time { return clock }})
		if err != nil {
			t.Fatal(err)
		}
		return srv
	}
	first := newServer(now)
	tokenEnc, version, err := first.v2store.encrypt("fcm-token")
	if err != nil {
		t.Fatal(err)
	}
	repo := first.v2store.p
	if err := repo.putInstallation(ctx, "installation", installationRecord{FCMTokenEnc: tokenEnc, TokenKeyVersion: version, TokenGeneration: 1, LastActiveMs: now.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	rawAddress := bytes.Repeat([]byte{8}, 16)
	address := base64.RawURLEncoding.EncodeToString(rawAddress)
	capability := "capability"
	if created, err := repo.putAddressIfBelowLimit(ctx, address, "installation", addressRecord{InstallationID: "installation", SubmitCapHash: hashSecret(capability), MachineRevokeHash: hashSecret("revoke"), Bound: true}, 20, now); err != nil || !created {
		t.Fatalf("address=%v err=%v", created, err)
	}
	envelope := make([]byte, wakeSize)
	envelope[0], envelope[1] = wakeVersion, wakeType
	copy(envelope[2:18], rawAddress)
	binary.BigEndian.PutUint64(envelope[26:34], uint64(now.UnixMilli()))
	post := func(ctx context.Context, srv *Server) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/v1/wakes", bytes.NewReader(envelope))
		request = request.WithContext(ctx)
		request.Header.Set("Content-Type", "application/octet-stream")
		request.Header.Set("Authorization", "Swarm-Capability "+capability)
		response := httptest.NewRecorder()
		srv.ServeHTTP(response, request)
		return response
	}
	requestCtx, cancel := context.WithCancel(ctx)
	firstResponse := make(chan *httptest.ResponseRecorder, 1)
	go func() { firstResponse <- post(requestCtx, first) }()
	<-sender.firstStarted
	cancel()
	if response := <-firstResponse; response.Code != http.StatusInternalServerError {
		t.Fatalf("cancelled wake status=%d body=%s", response.Code, response.Body.String())
	}
	second := newServer(now)
	if response := post(ctx, second); response.Code != http.StatusBadGateway {
		t.Fatalf("wake during lease status=%d body=%s", response.Code, response.Body.String())
	}
	attempts, err := persistence.col("wake_attempts").Documents(ctx).GetAll()
	if err != nil || len(attempts) != 1 {
		t.Fatalf("wake attempts=%d err=%v", len(attempts), err)
	}
	if _, err := attempts[0].Ref.Update(ctx, []firestore.Update{{Path: "lease_until_ms", Value: attempts[0].ReadTime.Add(-time.Second).UnixMilli()}}); err != nil {
		t.Fatal(err)
	}
	third := newServer(now)
	if response := post(ctx, third); response.Code != http.StatusOK {
		t.Fatalf("takeover wake status=%d body=%s", response.Code, response.Body.String())
	}
	fourth := newServer(now)
	if response := post(ctx, fourth); response.Code != http.StatusOK {
		t.Fatalf("completed retry status=%d body=%s", response.Code, response.Body.String())
	}
	if calls := sender.calls.Load(); calls != 2 {
		t.Fatalf("provider calls=%d, want cancelled attempt plus one bounded takeover", calls)
	}
}
