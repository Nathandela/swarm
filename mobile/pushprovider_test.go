package swarmmobile

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
	public []byte
	signs  int
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
	return &testPlatformInstallationSigner{public: public.Bytes()}
}

func (s *testPlatformInstallationSigner) PublicKey() []byte { return append([]byte(nil), s.public...) }
func (s *testPlatformInstallationSigner) Sign([]byte) ([]byte, error) {
	s.signs++
	return make([]byte, 64), nil
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
			_, _ = w.Write([]byte(`{"installation_id":"abcdefghijklmnopqrstuv","refresh_before":"2030-01-01T00:00:00Z"}`))
		case r.Method == http.MethodPut && r.URL.Path == "/v1/installations/abcdefghijklmnopqrstuv/token":
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
	if err := app.EnsurePushRegistration("fcm-token-2"); err != nil {
		t.Fatal(err)
	}
	if signer.signs == 0 {
		t.Fatal("signed rotation did not use the platform signer")
	}
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
