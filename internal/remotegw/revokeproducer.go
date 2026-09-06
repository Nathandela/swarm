package remotegw

// The machine-side HTTP revoke helper. Durable custody and restart recovery live in
// internal/skeleton's pairing-push custody, which preserves the complete registry binding
// before rotation or deletion and invokes this helper to present it.

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// HTTPAddressRevoker presents DELETE /v1/addresses/{addr}
// bearing "Swarm-Revoke <capability>", no body -- push-gateway-api.md 3.4's machine
// arm, the exact request internal/pushgw's handleRevoke verifies. It must never present
// the submit capability's "Swarm-Capability" header: that crossover is PG-AUTH-8's
// forbidden one.
type HTTPAddressRevoker struct {
	BaseURL                 string
	MachineRevokeCapability string
	Client                  *http.Client // nil => http.DefaultClient
}

// RevokeAddress presents the machine-revoke capability once. A 2xx -- including the
// PG-REV-2 tombstone's idempotent 204 on a re-presented delete -- is success. A 5xx or
// 429 and any transport failure are retryable; every other status is a terminal
// refusal (a dead capability cannot come alive by retrying).
func (r *HTTPAddressRevoker) RevokeAddress(ctx context.Context, addr PushAddress) error {
	base, err := url.Parse(r.BaseURL)
	if err != nil {
		return fmt.Errorf("remotegw: invalid gateway BaseURL: %w", err)
	}
	endpoint := strings.TrimRight(base.String(), "/") + "/v1/addresses/" +
		base64.RawURLEncoding.EncodeToString(addr[:])
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, http.NoBody)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Swarm-Revoke "+r.MachineRevokeCapability)
	client := r.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("remotegw: revoke transport failure: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests:
		return fmt.Errorf("remotegw: gateway answered %d to the revoke; retryable", resp.StatusCode)
	default:
		return fmt.Errorf("remotegw: gateway refused the revoke with %d", resp.StatusCode)
	}
}
