package swarmmobile

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type platformPushCustody struct{}

func (platformPushCustody) WakeKEK() ([]byte, error) {
	sum := sha256.Sum256([]byte("platform-push-wake"))
	return sum[:], nil
}
func (platformPushCustody) ContentKEK() ([]byte, error) {
	sum := sha256.Sum256([]byte("platform-push-content"))
	return sum[:], nil
}

type testPushAttestor struct {
	hash []byte
}

func (a *testPushAttestor) Attest(hash []byte) (string, error) {
	a.hash = append([]byte(nil), hash...)
	return "real-play-token", nil
}

type testPlatformInstallationSigner struct {
	key        *ecdsa.PrivateKey
	signingKey *ecdsa.PrivateKey
	signErr    error
	public     []byte
	mu         sync.Mutex
	canonicals [][]byte
}

func newPlatformTestSigner(t *testing.T) *testPlatformInstallationSigner {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	public, err := key.PublicKey.ECDH()
	if err != nil {
		t.Fatal(err)
	}
	return &testPlatformInstallationSigner{key: key, public: public.Bytes()}
}

func (s *testPlatformInstallationSigner) PublicKey() []byte { return append([]byte(nil), s.public...) }
func (s *testPlatformInstallationSigner) Sign(canonical []byte) ([]byte, error) {
	s.mu.Lock()
	s.canonicals = append(s.canonicals, append([]byte(nil), canonical...))
	s.mu.Unlock()
	if s.signErr != nil {
		return nil, s.signErr
	}
	key := s.signingKey
	if key == nil {
		key = s.key
	}
	digest := sha256.Sum256(canonical)
	r, sig, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		return nil, err
	}
	half := new(big.Int).Rsh(key.Curve.Params().N, 1)
	if sig.Cmp(half) > 0 {
		sig = new(big.Int).Sub(key.Curve.Params().N, sig)
	}
	out := make([]byte, 64)
	r.FillBytes(out[:32])
	sig.FillBytes(out[32:])
	return out, nil
}

func (s *testPlatformInstallationSigner) recordedCanonicals() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]byte(nil), s.canonicals...)
}

func TestConfigurePushRegistration_UsesPlatformAuthorityAndLeavesNoGoPrivateKey(t *testing.T) {
	signer := newPlatformTestSigner(t)
	public := signer.PublicKey()
	var registrationBody struct {
		InstallationPublicKey string `json:"installation_public_key"`
		Attestation           struct {
			Token string `json:"token"`
		} `json:"attestation"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/installations":
			if err := json.NewDecoder(r.Body).Decode(&registrationBody); err != nil {
				t.Fatal(err)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"installation_id":"YWJjZGVmZ2hpamtsbW5vcA","refresh_before":"2030-01-01T00:00:00Z"}`))
		case r.Method == http.MethodPut && r.URL.Path == "/v1/installations/YWJjZGVmZ2hpamtsbW5vcA/token":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}))
	defer server.Close()
	app, err := NewApp(&Config{StateDir: t.TempDir(), PushGatewayURL: server.URL}, platformPushCustody{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	// Exercise migration from the old pre-production scalar before configuring Android.
	if _, err := app.core.InstallationSigner(); err != nil {
		t.Fatal(err)
	}
	attestor := &testPushAttestor{}
	if err := app.ConfigurePushRegistration(attestor, signer); err != nil {
		t.Fatal(err)
	}
	if err := app.EnsurePushRegistration("fcm-token"); err != nil {
		t.Fatal(err)
	}
	if len(attestor.hash) != 32 {
		t.Fatalf("attestor received %d hash bytes", len(attestor.hash))
	}
	if registrationBody.Attestation.Token != "real-play-token" ||
		registrationBody.InstallationPublicKey != base64.RawURLEncoding.EncodeToString(public) {
		t.Fatalf("registration body = %+v", registrationBody)
	}
	beforeRotation := len(signer.recordedCanonicals())
	if err := app.EnsurePushRegistration("fcm-token-2"); err != nil {
		t.Fatal(err)
	}
	canonicals := signer.recordedCanonicals()
	if len(canonicals) != beforeRotation+1 {
		t.Fatalf("platform signer calls after rotation = %d, want %d", len(canonicals), beforeRotation+1)
	}
	if got := string(canonicals[len(canonicals)-1]); !strings.HasPrefix(got,
		"swarm-pg-v1|PUT|/v1/installations/YWJjZGVmZ2hpamtsbW5vcA/token|") {
		t.Fatalf("rotation canonical = %q", got)
	}
}

func TestConfigurePushRegistration_RegistrationUsesPlatformSigner(t *testing.T) {
	signer := newPlatformTestSigner(t)
	proofs := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/installations" {
			http.Error(w, "unexpected", http.StatusNotFound)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			proofs <- err
			http.Error(w, "registration body unreadable", http.StatusBadRequest)
			return
		}
		proofErr := registrationProofError(signer, r.Header.Get("Idempotency-Key"), body,
			r.Header.Get("Swarm-Registration-Proof"))
		proofs <- proofErr
		if proofErr != nil {
			http.Error(w, "registration proof refused", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"installation_id":"YWJjZGVmZ2hpamtsbW5vcA","refresh_before":"2030-01-01T00:00:00Z"}`))
	}))
	defer server.Close()

	app, err := NewApp(&Config{StateDir: t.TempDir(), PushGatewayURL: server.URL}, platformPushCustody{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	if err := app.ConfigurePushRegistration(&testPushAttestor{}, signer); err != nil {
		t.Fatal(err)
	}
	ensureErr := app.EnsurePushRegistration("fcm-token")
	if ensureErr != nil {
		t.Fatal(ensureErr)
	}
	select {
	case proofErr := <-proofs:
		if proofErr != nil {
			t.Fatal(proofErr)
		}
	default:
		t.Fatal("registration returned successfully without a proof-checked POST")
	}
	if got := app.core.PushInstallationID(); got != "YWJjZGVmZ2hpamtsbW5vcA" {
		t.Fatalf("durable installation ID = %q", got)
	}
}

func TestConfigurePushRegistration_RegistrationProofFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mutate    func(*testing.T, *testPlatformInstallationSigner)
		wantPosts int
		verify    bool
	}{
		{
			name: "signer failure",
			mutate: func(_ *testing.T, signer *testPlatformInstallationSigner) {
				signer.signErr = errors.New("keystore refused to sign")
			},
			wantPosts: 0,
		},
		{
			name: "signature from another key",
			mutate: func(t *testing.T, signer *testPlatformInstallationSigner) {
				key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				signer.signingKey = key
			},
			wantPosts: 1,
			verify:    true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			signer := newPlatformTestSigner(t)
			tc.mutate(t, signer)
			var posts atomic.Int32
			proofs := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				posts.Add(1)
				if tc.verify {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						proofs <- err
					} else {
						proofs <- registrationProofError(signer, r.Header.Get("Idempotency-Key"), body,
							r.Header.Get("Swarm-Registration-Proof"))
					}
				}
				http.Error(w, "registration proof refused", http.StatusUnauthorized)
			}))
			defer server.Close()

			app, err := NewApp(&Config{StateDir: t.TempDir(), PushGatewayURL: server.URL}, platformPushCustody{})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = app.Close() }()
			if err := app.ConfigurePushRegistration(&testPushAttestor{}, signer); err != nil {
				t.Fatal(err)
			}
			if err := app.EnsurePushRegistration("fcm-token"); err == nil {
				t.Fatal("invalid platform signer registered an installation")
			}
			if got := int(posts.Load()); got != tc.wantPosts {
				t.Fatalf("registration POSTs = %d, want %d", got, tc.wantPosts)
			}
			if tc.verify {
				select {
				case proofErr := <-proofs:
					if proofErr == nil {
						t.Fatal("gateway accepted a proof from a different platform key")
					}
				default:
					t.Fatal("wrong-key registration POST was not proof-checked")
				}
			}
			if got := app.core.PushInstallationID(); got != "" {
				t.Fatalf("invalid platform signer persisted installation ID %q", got)
			}
		})
	}
}

func registrationProofError(signer *testPlatformInstallationSigner, idem string, body []byte, proof string) error {
	canonicals := signer.recordedCanonicals()
	if len(canonicals) != 1 {
		return fmt.Errorf("platform signer calls before first registration POST = %d, want 1", len(canonicals))
	}
	bodyHash := sha256.Sum256(body)
	wantCanonical := "swarm-pg-register-v1|" + idem + "|" + base64.RawURLEncoding.EncodeToString(bodyHash[:])
	if got := string(canonicals[0]); got != wantCanonical {
		return fmt.Errorf("platform signer canonical = %q, want %q", got, wantCanonical)
	}
	encoded, ok := strings.CutPrefix(proof, "p256-sha256 ")
	if !ok {
		return fmt.Errorf("registration proof header = %q", proof)
	}
	sig, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(sig) != 64 {
		return fmt.Errorf("registration proof = %q, decode error = %v", proof, err)
	}
	s := new(big.Int).SetBytes(sig[32:])
	if s.Cmp(new(big.Int).Rsh(signer.key.Curve.Params().N, 1)) > 0 {
		return errors.New("registration proof is not low-S P1363")
	}
	digest := sha256.Sum256([]byte(wantCanonical))
	if !ecdsa.Verify(&signer.key.PublicKey, digest[:], new(big.Int).SetBytes(sig[:32]), s) {
		return errors.New("registration proof does not verify against the platform public key")
	}
	return nil
}

func TestConfigurePushRegistration_FailsClosed(t *testing.T) {
	app, err := NewApp(&Config{StateDir: t.TempDir()}, platformPushCustody{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = app.Close() }()
	if err := app.ConfigurePushRegistration(nil, nil); err == nil {
		t.Fatal("nil production providers were accepted")
	}
	signer := newPlatformTestSigner(t)
	if err := app.ConfigurePushRegistration(&testPushAttestor{}, signer); err != nil {
		t.Fatal(err)
	}
	err = app.ConfigurePushRegistration(&testPushAttestor{}, signer)
	if err == nil || !strings.Contains(err.Error(), "already") {
		t.Fatalf("reconfiguration error = %v", err)
	}
}

var _ PushAttestor = (*testPushAttestor)(nil)
var _ PushInstallationSigner = (*testPlatformInstallationSigner)(nil)
