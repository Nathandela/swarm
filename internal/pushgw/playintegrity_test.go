package pushgw

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakePlayIntegrityDecoder struct {
	payload     PlayIntegrityPayload
	err         error
	packageName string
	token       string
}

func TestGooglePlayIntegrityDecodeClient_UsesExactRESTContract(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/dev.swarm.phone:decodeIntegrityToken" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("content-type = %q", r.Header.Get("Content-Type"))
		}
		raw, _ := io.ReadAll(r.Body)
		if string(raw) != `{"integrity_token":"opaque-token"}` {
			t.Fatalf("body = %s", raw)
		}
		_, _ = io.WriteString(w, `{"tokenPayloadExternal":{"requestDetails":{"requestPackageName":"dev.swarm.phone","requestHash":"hash","timestampMillis":"1800000000000"}}}`)
	}))
	defer server.Close()
	client, err := NewGooglePlayIntegrityDecodeClient(server.Client())
	if err != nil {
		t.Fatal(err)
	}
	client.baseURL = server.URL
	payload, err := client.Decode(context.Background(), ProductionAndroidPackage, "opaque-token")
	if err != nil {
		t.Fatal(err)
	}
	if payload.RequestDetails.TimestampMillis != 1_800_000_000_000 {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestGooglePlayIntegrityDecodeClient_ClassifiesAvailability(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
			defer server.Close()
			client, _ := NewGooglePlayIntegrityDecodeClient(server.Client())
			client.baseURL = server.URL
			_, err := client.Decode(context.Background(), ProductionAndroidPackage, "token")
			if !errors.Is(err, ErrAttestationUnavailable) {
				t.Fatalf("HTTP %d error = %v", status, err)
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, strings.Repeat("x", 10))
	}))
	defer server.Close()
	client, _ := NewGooglePlayIntegrityDecodeClient(server.Client())
	client.baseURL = server.URL
	if _, err := client.Decode(context.Background(), ProductionAndroidPackage, "bad-token"); err == nil || errors.Is(err, ErrAttestationUnavailable) {
		t.Fatalf("HTTP 400 error = %v, want definitive invalid", err)
	}
}

func (f *fakePlayIntegrityDecoder) Decode(_ context.Context, packageName, token string) (PlayIntegrityPayload, error) {
	f.packageName, f.token = packageName, token
	return f.payload, f.err
}

func validPlayIntegrityPayload(now time.Time, hash, cert string) PlayIntegrityPayload {
	return PlayIntegrityPayload{
		RequestDetails: PlayIntegrityRequestDetails{
			RequestPackageName: ProductionAndroidPackage,
			RequestHash:        hash,
			TimestampMillis:    now.UnixMilli(),
		},
		AppIntegrity: PlayIntegrityAppIntegrity{
			AppRecognitionVerdict:   "PLAY_RECOGNIZED",
			PackageName:             ProductionAndroidPackage,
			CertificateSHA256Digest: []string{cert},
		},
		AccountDetails: PlayIntegrityAccountDetails{AppLicensingVerdict: "LICENSED"},
	}
}

func newValidPlayIntegrityVerifier(t *testing.T) (*PlayIntegrityVerifier, *fakePlayIntegrityDecoder, time.Time, [32]byte, string) {
	t.Helper()
	now := time.Unix(1_800_000_000, 0)
	hash := sha256.Sum256([]byte("registration"))
	certRaw := sha256.Sum256([]byte("play signing certificate"))
	cert := base64.RawURLEncoding.EncodeToString(certRaw[:])
	decoder := &fakePlayIntegrityDecoder{payload: validPlayIntegrityPayload(now, base64.RawURLEncoding.EncodeToString(hash[:]), cert)}
	verifier, err := NewPlayIntegrityVerifier(PlayIntegrityConfig{
		PackageName:              ProductionAndroidPackage,
		CloudProjectNumber:       ProductionCloudProjectNumber,
		AllowedCertificateSHA256: []string{cert},
		MaxVerdictAge:            2 * time.Minute,
		MaxFutureSkew:            30 * time.Second,
		Now:                      func() time.Time { return now },
		Decode:                   decoder,
	})
	if err != nil {
		t.Fatal(err)
	}
	return verifier, decoder, now, hash, cert
}

func TestPlayIntegrityVerifier_StrictLicensedPlayBuild(t *testing.T) {
	t.Parallel()
	verifier, decoder, _, wantHash, _ := newValidPlayIntegrityVerifier(t)
	got, err := verifier.Verify(context.Background(), "google-token")
	if err != nil {
		t.Fatal(err)
	}
	if got.RequestHash != wantHash || !got.LicensedBuild {
		t.Fatalf("binding = %+v", got)
	}
	if decoder.packageName != ProductionAndroidPackage || decoder.token != "google-token" {
		t.Fatalf("decode call = package %q token %q", decoder.packageName, decoder.token)
	}
}

func TestPlayIntegrityVerifier_RejectsEveryAuthorityMismatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*PlayIntegrityPayload, time.Time, string)
	}{
		{"request package", func(p *PlayIntegrityPayload, _ time.Time, _ string) { p.RequestDetails.RequestPackageName = "evil.app" }},
		{"app package", func(p *PlayIntegrityPayload, _ time.Time, _ string) { p.AppIntegrity.PackageName = "evil.app" }},
		{"recognition", func(p *PlayIntegrityPayload, _ time.Time, _ string) {
			p.AppIntegrity.AppRecognitionVerdict = "UNRECOGNIZED_VERSION"
		}},
		{"licensing", func(p *PlayIntegrityPayload, _ time.Time, _ string) {
			p.AccountDetails.AppLicensingVerdict = "UNLICENSED"
		}},
		{"certificate", func(p *PlayIntegrityPayload, _ time.Time, _ string) {
			p.AppIntegrity.CertificateSHA256Digest = []string{base64.RawURLEncoding.EncodeToString(make([]byte, 32))}
		}},
		{"malformed request hash", func(p *PlayIntegrityPayload, _ time.Time, _ string) { p.RequestDetails.RequestHash = "not-base64" }},
		{"old verdict", func(p *PlayIntegrityPayload, now time.Time, _ string) {
			p.RequestDetails.TimestampMillis = now.Add(-2*time.Minute - time.Millisecond).UnixMilli()
		}},
		{"future verdict", func(p *PlayIntegrityPayload, now time.Time, _ string) {
			p.RequestDetails.TimestampMillis = now.Add(30*time.Second + time.Millisecond).UnixMilli()
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verifier, decoder, now, _, cert := newValidPlayIntegrityVerifier(t)
			p := decoder.payload
			tt.mutate(&p, now, cert)
			decoder.payload = p
			if _, err := verifier.Verify(context.Background(), "token"); err == nil || errors.Is(err, ErrAttestationUnavailable) {
				t.Fatalf("Verify error = %v, want definitive invalid verdict", err)
			}
		})
	}
}

func TestPlayIntegrityVerifier_DecodeUnavailableRemainsRetryable(t *testing.T) {
	t.Parallel()
	verifier, decoder, _, _, _ := newValidPlayIntegrityVerifier(t)
	decoder.err = ErrAttestationUnavailable
	if _, err := verifier.Verify(context.Background(), "token"); !errors.Is(err, ErrAttestationUnavailable) {
		t.Fatalf("Verify error = %v, want ErrAttestationUnavailable", err)
	}
}

func TestPlayIntegrityVerifier_ConfigurationFailsClosed(t *testing.T) {
	t.Parallel()
	base := PlayIntegrityConfig{
		PackageName:              ProductionAndroidPackage,
		CloudProjectNumber:       ProductionCloudProjectNumber,
		AllowedCertificateSHA256: []string{base64.RawURLEncoding.EncodeToString(make([]byte, 32))},
		MaxVerdictAge:            time.Minute,
		MaxFutureSkew:            30 * time.Second,
		Now:                      time.Now,
		Decode:                   &fakePlayIntegrityDecoder{},
	}
	tests := []struct {
		name   string
		mutate func(*PlayIntegrityConfig)
	}{
		{"missing decoder", func(c *PlayIntegrityConfig) { c.Decode = nil }},
		{"wrong package", func(c *PlayIntegrityConfig) { c.PackageName = "" }},
		{"missing project", func(c *PlayIntegrityConfig) { c.CloudProjectNumber = 0 }},
		{"missing certs", func(c *PlayIntegrityConfig) { c.AllowedCertificateSHA256 = nil }},
		{"malformed cert", func(c *PlayIntegrityConfig) { c.AllowedCertificateSHA256 = []string{"abcd"} }},
		{"missing age", func(c *PlayIntegrityConfig) { c.MaxVerdictAge = 0 }},
		{"missing skew", func(c *PlayIntegrityConfig) { c.MaxFutureSkew = 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base
			tt.mutate(&cfg)
			if _, err := NewPlayIntegrityVerifier(cfg); err == nil {
				t.Fatal("NewPlayIntegrityVerifier succeeded")
			}
		})
	}
}
