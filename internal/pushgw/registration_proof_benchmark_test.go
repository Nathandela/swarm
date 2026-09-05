package pushgw

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Nathandela/swarm/internal/pushreg"
)

func BenchmarkVerifyRegistrationProof(b *testing.B) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		b.Fatal(err)
	}
	publicKey := &privateKey.PublicKey
	const idempotencyKey = "AAAAAAAAAAAAAAAAAAAAAA"
	body := []byte(`{"installation_public_key":"test","fcm_token":"token","attestation":{"kind":"play_integrity","token":"verdict"}}`)
	message := pushreg.RegistrationProofMessage(idempotencyKey, body)
	digest := sha256.Sum256(message)
	r, s, err := ecdsa.Sign(rand.Reader, privateKey, digest[:])
	if err != nil {
		b.Fatal(err)
	}
	n := privateKey.Curve.Params().N
	if half := new(big.Int).Rsh(n, 1); s.Cmp(half) > 0 {
		s.Sub(n, s)
	}
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	request := httptest.NewRequest(http.MethodPost, "/v1/installations", nil)
	request.Header.Set("Swarm-Registration-Proof", "p256-sha256 "+base64.RawURLEncoding.EncodeToString(signature))

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if !verifyRegistrationProof(request, publicKey, idempotencyKey, body) {
			b.Fatal("prepared valid proof was rejected")
		}
	}
}
