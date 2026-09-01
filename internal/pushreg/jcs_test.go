package pushreg

import (
	"encoding/hex"
	"testing"
)

func TestRequestHash_RFC8785GoldenVectors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, publicKey, fcmToken, wantCanonical, wantSHA256 string
	}{
		{
			name:          "ascii",
			publicKey:     "key",
			fcmToken:      "token",
			wantCanonical: `{"attestation":{"kind":"play_integrity","token":""},"fcm_token":"token","installation_public_key":"key"}`,
			wantSHA256:    "0260b76950a29c2b2e02327e9c8c5d684032d53fd53193c1170b66423de94d76",
		},
		{
			name:          "jcs preserves line separators and does not html escape",
			publicKey:     "<>&",
			fcmToken:      "a\u2028b\u2029c",
			wantCanonical: "{\"attestation\":{\"kind\":\"play_integrity\",\"token\":\"\"},\"fcm_token\":\"a\u2028b\u2029c\",\"installation_public_key\":\"<>&\"}",
			wantSHA256:    "464fc543171821648da4bedfec321488d9fede4d0331175f0a1e26397e6eaec5",
		},
		{
			name:          "jcs string escapes",
			publicKey:     "\"\\",
			fcmToken:      "a\b\t\n\f\r\x00\x1f",
			wantCanonical: `{"attestation":{"kind":"play_integrity","token":""},"fcm_token":"a\b\t\n\f\r\u0000\u001f","installation_public_key":"\"\\"}`,
			wantSHA256:    "fcca4b4778fc3d5524a9b9d518b19d5fccb846bb429f1a84652ec47bf8c5247c",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			canonical, err := CanonicalRegistration(tt.publicKey, tt.fcmToken)
			if err != nil {
				t.Fatal(err)
			}
			if string(canonical) != tt.wantCanonical {
				t.Fatalf("canonical = %q\nwant      = %q", canonical, tt.wantCanonical)
			}
			got, err := RequestHash(tt.publicKey, tt.fcmToken)
			if err != nil {
				t.Fatal(err)
			}
			if hex.EncodeToString(got[:]) != tt.wantSHA256 {
				t.Fatalf("SHA-256 = %x, want %s", got, tt.wantSHA256)
			}
		})
	}
}

func TestCanonicalRegistration_RejectsNonIJSONStrings(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"bad\xff", "bad\xc0\xaf"} {
		if _, err := CanonicalRegistration(value, "token"); err == nil {
			t.Fatalf("CanonicalRegistration(%q) succeeded, want fail-closed rejection", value)
		}
	}
}
