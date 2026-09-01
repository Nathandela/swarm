package pushgw

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// ProductionAndroidPackage is the application id enrolled in Google Play.
	ProductionAndroidPackage = "dev.swarm.phone"
	// ProductionCloudProjectNumber is the Cloud project linked to the Play app. Android
	// binds Standard API tokens to this project and the decode client's credentials must
	// belong to the same project.
	ProductionCloudProjectNumber int64 = 733314021126
	googlePlayIntegrityBaseURL         = "https://playintegrity.googleapis.com"
	playIntegrityResponseMax           = 64 * 1024
)

var errPlayIntegrityVerdict = errors.New("pushgw: invalid Play Integrity verdict")

// PlayIntegrityDecodeClient is the authenticated Google boundary. Production supplies an
// OAuth-authorized HTTP client; tests inject decoded payloads without dialing Google.
type PlayIntegrityDecodeClient interface {
	Decode(ctx context.Context, packageName, verdictToken string) (PlayIntegrityPayload, error)
}

// PlayIntegrityPayload is tokenPayloadExternal from applications:decodeIntegrityToken.
// Only authority-bearing fields used by registration are represented.
type PlayIntegrityPayload struct {
	RequestDetails PlayIntegrityRequestDetails `json:"requestDetails"`
	AppIntegrity   PlayIntegrityAppIntegrity   `json:"appIntegrity"`
	AccountDetails PlayIntegrityAccountDetails `json:"accountDetails"`
}

type PlayIntegrityRequestDetails struct {
	RequestPackageName string `json:"requestPackageName"`
	RequestHash        string `json:"requestHash"`
	TimestampMillis    int64  `json:"timestampMillis,string"`
}

type PlayIntegrityAppIntegrity struct {
	AppRecognitionVerdict   string   `json:"appRecognitionVerdict"`
	PackageName             string   `json:"packageName"`
	CertificateSHA256Digest []string `json:"certificateSha256Digest"`
}

type PlayIntegrityAccountDetails struct {
	AppLicensingVerdict string `json:"appLicensingVerdict"`
}

// PlayIntegrityConfig is the complete, fail-closed decoded-verdict policy. There are no
// defaults: omitting a package, certificate allowlist or freshness bound is a construction
// error rather than a weakened production verifier. Cloud project identity is deliberately
// absent because Google does not echo it in tokenPayloadExternal; Android token preparation,
// the decoder's ADC construction and release provenance enforce that deployment coordinate.
type PlayIntegrityConfig struct {
	PackageName              string
	AllowedCertificateSHA256 []string
	MaxVerdictAge            time.Duration
	MaxFutureSkew            time.Duration
	Now                      func() time.Time
	Decode                   PlayIntegrityDecodeClient
}

type PlayIntegrityVerifier struct {
	packageName   string
	certificates  [][32]byte
	maxAge        time.Duration
	maxFutureSkew time.Duration
	now           func() time.Time
	decode        PlayIntegrityDecodeClient
}

func NewPlayIntegrityVerifier(cfg PlayIntegrityConfig) (*PlayIntegrityVerifier, error) {
	if strings.TrimSpace(cfg.PackageName) == "" {
		return nil, errors.New("pushgw: Play Integrity package name is required")
	}
	if cfg.Decode == nil {
		return nil, errors.New("pushgw: Play Integrity decode client is required")
	}
	if cfg.MaxVerdictAge <= 0 || cfg.MaxFutureSkew <= 0 {
		return nil, errors.New("pushgw: positive Play Integrity freshness bounds are required")
	}
	if cfg.Now == nil {
		return nil, errors.New("pushgw: Play Integrity clock is required")
	}
	if len(cfg.AllowedCertificateSHA256) == 0 {
		return nil, errors.New("pushgw: at least one Play signing certificate is required")
	}
	certs := make([][32]byte, 0, len(cfg.AllowedCertificateSHA256))
	for _, encoded := range cfg.AllowedCertificateSHA256 {
		raw, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != encoded {
			return nil, fmt.Errorf("pushgw: Play signing certificate %q is not canonical base64url SHA-256", encoded)
		}
		var cert [32]byte
		copy(cert[:], raw)
		certs = append(certs, cert)
	}
	return &PlayIntegrityVerifier{
		packageName:   cfg.PackageName,
		certificates:  certs,
		maxAge:        cfg.MaxVerdictAge,
		maxFutureSkew: cfg.MaxFutureSkew,
		now:           cfg.Now,
		decode:        cfg.Decode,
	}, nil
}

func (v *PlayIntegrityVerifier) Verify(ctx context.Context, verdictToken string) (VerdictBinding, error) {
	if strings.TrimSpace(verdictToken) == "" {
		return VerdictBinding{}, fmt.Errorf("%w: empty token", errPlayIntegrityVerdict)
	}
	payload, err := v.decode.Decode(ctx, v.packageName, verdictToken)
	if err != nil {
		if errors.Is(err, ErrAttestationUnavailable) {
			return VerdictBinding{}, err
		}
		return VerdictBinding{}, fmt.Errorf("%w: decode: %v", errPlayIntegrityVerdict, err)
	}
	if payload.RequestDetails.RequestPackageName != v.packageName ||
		payload.AppIntegrity.PackageName != v.packageName ||
		payload.AppIntegrity.AppRecognitionVerdict != "PLAY_RECOGNIZED" ||
		payload.AccountDetails.AppLicensingVerdict != "LICENSED" {
		return VerdictBinding{}, fmt.Errorf("%w: package, recognition, or licensing mismatch", errPlayIntegrityVerdict)
	}
	hashRaw, err := decodeCanonicalBase64URL32(payload.RequestDetails.RequestHash)
	if err != nil {
		return VerdictBinding{}, fmt.Errorf("%w: request hash: %v", errPlayIntegrityVerdict, err)
	}
	if !v.allowsCertificate(payload.AppIntegrity.CertificateSHA256Digest) {
		return VerdictBinding{}, fmt.Errorf("%w: Play signing certificate is not allowed", errPlayIntegrityVerdict)
	}
	issued := time.UnixMilli(payload.RequestDetails.TimestampMillis)
	now := v.now()
	if payload.RequestDetails.TimestampMillis <= 0 || now.Sub(issued) > v.maxAge || issued.Sub(now) > v.maxFutureSkew {
		return VerdictBinding{}, fmt.Errorf("%w: verdict is outside the freshness window", errPlayIntegrityVerdict)
	}
	var hash [32]byte
	copy(hash[:], hashRaw)
	return VerdictBinding{RequestHash: hash, LicensedBuild: true}, nil
}

func (v *PlayIntegrityVerifier) allowsCertificate(encoded []string) bool {
	matched := 0
	for _, candidate := range encoded {
		raw, err := decodeCanonicalBase64URL32(candidate)
		if err != nil {
			continue
		}
		for _, allowed := range v.certificates {
			matched |= subtle.ConstantTimeCompare(raw, allowed[:])
		}
	}
	return matched == 1
}

func decodeCanonicalBase64URL32(encoded string) ([]byte, error) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != encoded {
		return nil, errors.New("not canonical base64url SHA-256")
	}
	return raw, nil
}

// GooglePlayIntegrityDecodeClient calls Google's REST decode endpoint through an
// OAuth-authorized HTTP client. Authentication is deliberately supplied by the command so
// credential loading and readiness remain an operational construction concern.
type GooglePlayIntegrityDecodeClient struct {
	hc      *http.Client
	baseURL string
}

func NewGooglePlayIntegrityDecodeClient(hc *http.Client) (*GooglePlayIntegrityDecodeClient, error) {
	if hc == nil {
		return nil, errors.New("pushgw: authenticated Play Integrity HTTP client is required")
	}
	return &GooglePlayIntegrityDecodeClient{hc: hc, baseURL: googlePlayIntegrityBaseURL}, nil
}

func (c *GooglePlayIntegrityDecodeClient) Decode(ctx context.Context, packageName, verdictToken string) (PlayIntegrityPayload, error) {
	if strings.TrimSpace(packageName) == "" || strings.TrimSpace(verdictToken) == "" {
		return PlayIntegrityPayload{}, fmt.Errorf("%w: package and token are required", errPlayIntegrityVerdict)
	}
	body, err := json.Marshal(struct {
		IntegrityToken string `json:"integrity_token"`
	}{verdictToken})
	if err != nil {
		return PlayIntegrityPayload{}, fmt.Errorf("%w: encode decode request: %v", errPlayIntegrityVerdict, err)
	}
	endpoint := strings.TrimRight(c.baseURL, "/") + "/v1/" + url.PathEscape(packageName) + ":decodeIntegrityToken"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return PlayIntegrityPayload{}, fmt.Errorf("%w: build decode request: %v", ErrAttestationUnavailable, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return PlayIntegrityPayload{}, fmt.Errorf("%w: decode request: %v", ErrAttestationUnavailable, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, playIntegrityResponseMax+1))
	if err != nil {
		return PlayIntegrityPayload{}, fmt.Errorf("%w: read decode response: %v", ErrAttestationUnavailable, err)
	}
	if len(raw) > playIntegrityResponseMax {
		return PlayIntegrityPayload{}, fmt.Errorf("%w: decode response exceeds %d bytes", errPlayIntegrityVerdict, playIntegrityResponseMax)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 ||
			resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return PlayIntegrityPayload{}, fmt.Errorf("%w: Google decode returned HTTP %d", ErrAttestationUnavailable, resp.StatusCode)
		}
		return PlayIntegrityPayload{}, fmt.Errorf("%w: Google decode returned HTTP %d", errPlayIntegrityVerdict, resp.StatusCode)
	}
	var envelope struct {
		TokenPayloadExternal PlayIntegrityPayload `json:"tokenPayloadExternal"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&envelope); err != nil {
		return PlayIntegrityPayload{}, fmt.Errorf("%w: malformed decode response: %v", errPlayIntegrityVerdict, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return PlayIntegrityPayload{}, fmt.Errorf("%w: trailing decode response data", errPlayIntegrityVerdict)
	}
	return envelope.TokenPayloadExternal, nil
}

var _ AttestationVerifier = (*PlayIntegrityVerifier)(nil)
var _ PlayIntegrityDecodeClient = (*GooglePlayIntegrityDecodeClient)(nil)
