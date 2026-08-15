package pushgw_test

// PG-TEST-9 / §8.2: a negative control asserting no token, capability, envelope,
// signature, nonce, or attestation token appears in any emitted log line. Every operation
// is driven once, with a known secret value in hand for each, then the captured log
// output is grepped for every one of them.
//
// RED: package pushgw has no implementation yet; every pushgw.* reference below is an
// undefined symbol.

import (
	"bytes"
	"encoding/hex"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/pushgw"
)

func TestLogs_NeverContainTokensCapabilitiesEnvelopesOrSignatures(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	h := newHarness(t, func(cfg *pushgw.Config) { cfg.Logger = logger })

	r := registerInstallation(t, h, "fcm-token-logsecret-9f2a")
	a := allocateAddress(t, h, r)

	path := "/v1/installations/" + r.installationID + "/token"
	rotateReqBody := []byte(`{"fcm_token":"fcm-token-logsecret-rotated-77b3"}`)
	headers, nonce, _ := sign(t, r.priv, h.clock.Now(), signParams{method: "PUT", path: path, body: rotateReqBody})
	requireStatus(t, h.doJSON("PUT", path, rotateReqBody, headers), http.StatusNoContent)

	env := buildWakeV1(t, decodeAddr(t, a.pushAddress), 1, h.clock.Now())
	requireStatus(t, submitTestWake(h, a.submitCapability, env), http.StatusOK)

	revokeResp := revokeByMachine(h, a.pushAddress, a.machineRevokeCapability)
	requireStatus(t, revokeResp, http.StatusNoContent)

	logged := logBuf.String()
	secrets := map[string]string{
		"fcm token (registered)":     r.fcmToken,
		"fcm token (rotated)":        "fcm-token-logsecret-rotated-77b3",
		"submit capability":          a.submitCapability,
		"machine-revoke capability":  a.machineRevokeCapability,
		"installation-key nonce":     nonce,
		"installation-key signature": headers["Swarm-Signature"],
		"wake envelope (hex)":        hex.EncodeToString(env[:]),
	}
	for label, secret := range secrets {
		if secret == "" {
			continue
		}
		if strings.Contains(logged, secret) {
			t.Fatalf("log output contains the %s: logs=%q", label, logged)
		}
	}
}
