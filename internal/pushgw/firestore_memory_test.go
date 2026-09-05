package pushgw

import (
	"bytes"
	"context"
	"errors"
	"math"
	"sync"
	"time"
)

// memoryRepository is a concurrency-correct test fake.
type memoryRepository struct {
	mu                  sync.Mutex
	installations       map[string]installationRecord
	addresses           map[string]addressRecord
	tombs               map[string]tombstoneRecord
	regs                map[string]registrationRecord
	nonces              map[string]int64
	wakes               map[string]wakeAttemptRecord
	rates               map[string]rateWindowRecord
	registrationLookups int
}

func NewMemoryRepository() Repository {
	return &memoryRepository{installations: map[string]installationRecord{}, addresses: map[string]addressRecord{}, tombs: map[string]tombstoneRecord{}, regs: map[string]registrationRecord{}, nonces: map[string]int64{}, wakes: map[string]wakeAttemptRecord{}, rates: map[string]rateWindowRecord{}}
}
func (m *memoryRepository) close() error                      { return nil }
func (m *memoryRepository) healthCheck(context.Context) error { return nil }
func (m *memoryRepository) putInstallation(_ context.Context, id string, rec installationRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.installations[id]; exists {
		return errors.New("installation exists")
	}
	m.installations[id] = rec
	return nil
}
func (m *memoryRepository) getInstallation(_ context.Context, id string) (installationRecord, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.installations[id]
	return rec, ok, nil
}
func (m *memoryRepository) claimNonceAndTouch(_ context.Context, id string, expectedPublicKey []byte, nonce string, now, expiry time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.installations[id]
	if !ok || !bytes.Equal(rec.PublicKey, expectedPublicKey) || installationExpired(rec.LastActiveMs, now) {
		return false, nil
	}
	key := id + "\x00" + nonce
	if old, found := m.nonces[key]; found && now.UnixMilli() < old {
		return false, nil
	}
	if ceiling := now.Add(expiryHorizon); expiry.Before(ceiling) {
		expiry = ceiling
	}
	m.nonces[key] = expiry.UnixMilli()
	rec.LastActiveMs = now.UnixMilli()
	m.installations[id] = rec
	return true, nil
}
func (m *memoryRepository) lookupRegistration(_ context.Context, key, digest string, now time.Time) (registrationResult, bool, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.registrationLookups++
	rec, ok := m.regs[key]
	if !ok || now.UnixMilli() >= rec.ExpiresAtMs {
		return registrationResult{}, false, false, nil
	}
	if rec.BodyDigest != digest {
		return registrationResult{}, false, true, nil
	}
	if rec.State != "completed" {
		return registrationResult{}, false, false, nil
	}
	return registrationResult{rec.InstallationID, rec.RefreshBefore}, true, false, nil
}
func (m *memoryRepository) claimRegistration(_ context.Context, key, digest, candidate, leaseID string, now time.Time) (registrationResult, bool, bool, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.regs[key]
	if ok && now.UnixMilli() < rec.ExpiresAtMs {
		if rec.BodyDigest != digest {
			return registrationResult{}, false, false, true, nil
		}
		if rec.State == "completed" {
			return registrationResult{rec.InstallationID, rec.RefreshBefore}, false, false, false, nil
		}
		if now.UnixMilli() < rec.LeaseUntilMs {
			return registrationResult{}, false, true, false, nil
		}
	}
	expires := rec.ExpiresAtMs
	if expires <= now.UnixMilli() {
		expires = now.Add(registrationWindow).UnixMilli()
	}
	m.regs[key] = registrationRecord{BodyDigest: digest, InstallationID: candidate, ExpiresAtMs: expires, State: "pending", LeaseID: leaseID, LeaseUntilMs: now.Add(registrationLease).UnixMilli()}
	return registrationResult{}, true, false, false, nil
}
func (m *memoryRepository) completeRegistration(_ context.Context, key, digest, leaseID string, installation installationRecord, now time.Time) (registrationResult, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.regs[key]
	if !ok || rec.BodyDigest != digest || rec.LeaseID != leaseID || rec.State != "pending" || now.UnixMilli() >= rec.ExpiresAtMs {
		return registrationResult{}, false, nil
	}
	if _, exists := m.installations[rec.InstallationID]; exists {
		return registrationResult{}, false, errors.New("installation exists")
	}
	refresh := now.Add(installationWindow).UTC().Format(time.RFC3339)
	m.installations[rec.InstallationID] = installation
	rec.State, rec.RefreshBefore, rec.LeaseUntilMs = "completed", refresh, 0
	m.regs[key] = rec
	return registrationResult{rec.InstallationID, refresh}, true, nil
}
func (m *memoryRepository) releaseRegistration(_ context.Context, key, leaseID string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.regs[key]
	if ok && rec.State == "pending" && rec.LeaseID == leaseID {
		rec.LeaseUntilMs = now.UnixMilli()
		m.regs[key] = rec
	}
	return nil
}
func (m *memoryRepository) registerOrReturn(_ context.Context, key, digest, candidate string, rec installationRecord, now time.Time) (registrationResult, bool, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if old, ok := m.regs[key]; ok && now.UnixMilli() < old.ExpiresAtMs {
		if old.BodyDigest != digest {
			return registrationResult{}, false, true, nil
		}
		return registrationResult{old.InstallationID, old.RefreshBefore}, false, false, nil
	}
	refresh := now.Add(installationWindow).UTC().Format(time.RFC3339)
	m.installations[candidate] = rec
	m.regs[key] = registrationRecord{BodyDigest: digest, InstallationID: candidate, RefreshBefore: refresh, ExpiresAtMs: now.Add(registrationWindow).UnixMilli(), State: "completed"}
	return registrationResult{candidate, refresh}, true, false, nil
}
func (m *memoryRepository) rotateToken(_ context.Context, id string, enc []byte, version string, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.installations[id]
	if !ok {
		return false, nil
	}
	if installationExpired(rec.LastActiveMs, now) {
		return false, nil
	}
	if rec.TokenGeneration == math.MaxInt64 {
		return false, errors.New("pushgw: token generation exhausted")
	}
	rec.FCMTokenEnc, rec.TokenKeyVersion, rec.TokenDead = enc, version, false
	rec.TokenGeneration++
	rec.LastActiveMs = now.UnixMilli()
	m.installations[id] = rec
	return true, nil
}
func (m *memoryRepository) putAddressIfBelowLimit(_ context.Context, addr, id string, rec addressRecord, limit int, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	installation, ok := m.installations[id]
	if !ok || installationExpired(installation.LastActiveMs, now) || installation.AddressCount >= int64(limit) {
		return false, nil
	}
	m.addresses[addr] = rec
	installation.AddressCount++
	m.installations[id] = installation
	return true, nil
}
func (m *memoryRepository) getAddress(_ context.Context, id string) (addressRecord, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.addresses[id]
	return rec, ok, nil
}
func (m *memoryRepository) deleteAddressAndTombstone(_ context.Context, addr string, rec addressRecord, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.addresses[addr]
	if !ok || current.MachineRevokeHash != rec.MachineRevokeHash {
		return nil
	}
	delete(m.addresses, addr)
	m.tombs[rec.MachineRevokeHash] = tombstoneRecord{RevokedAtMs: now.UnixMilli()}
	installation := m.installations[rec.InstallationID]
	if installation.AddressCount > 0 {
		installation.AddressCount--
		m.installations[rec.InstallationID] = installation
	}
	return nil
}
func (m *memoryRepository) getTombstone(_ context.Context, key string) (tombstoneRecord, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.tombs[key]
	return rec, ok, nil
}
func (m *memoryRepository) allow(_ context.Context, key string, rl RateLimit, now time.Time) (bool, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	window := m.rates[key]
	if now.UnixMilli() >= window.ExpiresAtMs {
		window = rateWindowRecord{ExpiresAtMs: now.Add(rl.Window).UnixMilli()}
	}
	if window.Count >= int64(rl.Max) {
		return false, retrySeconds(window.ExpiresAtMs, now), nil
	}
	window.Count++
	m.rates[key] = window
	return true, 0, nil
}
func (m *memoryRepository) claimWake(_ context.Context, id, leaseID, addr, capHash string, now, deadline time.Time, addrLimit RateLimit, sourceKey string, sourceLimit RateLimit) (wakeClaim, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !now.Before(deadline) {
		return wakeClaim{Denied: "malformed"}, false, nil
	}
	address, ok := m.addresses[addr]
	if !ok || !verifierEquals(address.SubmitCapHash, capHash) || (!address.Bound && now.UnixMilli() >= address.UnboundExpiresMs) {
		return wakeClaim{Denied: "unauthorized"}, false, nil
	}
	if attempt, ok := m.wakes[id]; ok {
		if now.UnixMilli() >= attempt.ExpiresAtMs {
			return wakeClaim{Denied: "exhausted"}, false, nil
		}
		if attempt.State == "completed" {
			return wakeClaim{Completed: true, Status: attempt.Status, Body: attempt.Body}, false, nil
		}
		if attempt.Attempts >= maxWakeAttempts {
			return wakeClaim{Denied: "exhausted"}, false, nil
		}
		if now.UnixMilli() < attempt.LeaseUntilMs {
			return wakeClaim{Denied: "busy"}, false, nil
		}
		deadline = time.UnixMilli(attempt.ExpiresAtMs)
	}
	installation, ok := m.installations[address.InstallationID]
	if !ok {
		return wakeClaim{Denied: "unauthorized"}, false, nil
	}
	if installationExpired(installation.LastActiveMs, now) {
		return wakeClaim{Denied: "unauthorized"}, false, nil
	}
	if installation.TokenDead {
		return wakeClaim{Denied: "token_dead"}, false, nil
	}
	type quotaUpdate struct {
		key    string
		window rateWindowRecord
	}
	updates := make([]quotaUpdate, 0, 2)
	for _, quota := range []struct {
		key  string
		rate RateLimit
	}{{"wake-addr:" + addr, addrLimit}, {sourceKey, sourceLimit}} {
		window := m.rates[quota.key]
		if now.UnixMilli() >= window.ExpiresAtMs {
			window = rateWindowRecord{ExpiresAtMs: now.Add(quota.rate.Window).UnixMilli()}
		}
		if window.Count >= int64(quota.rate.Max) {
			return wakeClaim{Denied: "quota", RetryAfter: retrySeconds(window.ExpiresAtMs, now)}, false, nil
		}
		window.Count++
		updates = append(updates, quotaUpdate{quota.key, window})
	}
	for _, update := range updates {
		m.rates[update.key] = update.window
	}
	attempt := m.wakes[id]
	leaseUntil := now.Add(wakeLease).UnixMilli()
	attempt = wakeAttemptRecord{InstallationID: address.InstallationID, Address: addr, TokenGeneration: installation.TokenGeneration, State: "claimed", Attempts: attempt.Attempts + 1, LeaseUntilMs: leaseUntil, LeaseID: leaseID, ExpiresAtMs: deadline.UnixMilli()}
	m.wakes[id] = attempt
	return wakeClaim{
		TokenEnc:        installation.FCMTokenEnc,
		KeyVersion:      installation.TokenKeyVersion,
		TokenGeneration: installation.TokenGeneration,
		LeaseUntilMs:    leaseUntil,
		ProviderBudget:  min(time.UnixMilli(leaseUntil).Sub(now), deadline.Sub(now)),
	}, true, nil
}
func (m *memoryRepository) completeWake(_ context.Context, id, leaseID string, generation int64, httpStatus int, body []byte, unregistered bool, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	attempt, ok := m.wakes[id]
	if !ok || attempt.TokenGeneration != generation || attempt.LeaseID != leaseID || attempt.State != "claimed" || now.UnixMilli() >= attempt.ExpiresAtMs || now.UnixMilli() >= attempt.LeaseUntilMs {
		return false, nil
	}
	if unregistered {
		installation, found := m.installations[attempt.InstallationID]
		if found && installation.TokenGeneration == generation {
			installation.FCMTokenEnc = nil
			installation.TokenDead = true
			m.installations[attempt.InstallationID] = installation
		} else if found {
			attempt.LeaseUntilMs = now.UnixMilli()
			m.wakes[id] = attempt
			return true, nil
		}
	}
	if httpStatus == 200 {
		if address, found := m.addresses[attempt.Address]; found && (address.Bound || now.UnixMilli() < address.UnboundExpiresMs) {
			address.Bound = true
			m.addresses[attempt.Address] = address
		}
	}
	if httpStatus == 503 {
		attempt.LeaseUntilMs = now.UnixMilli()
		m.wakes[id] = attempt
		return false, nil
	}
	attempt.State, attempt.Status, attempt.Body = "completed", httpStatus, append([]byte(nil), body...)
	m.wakes[id] = attempt
	return false, nil
}
func (m *memoryRepository) runRetention(_ context.Context, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	nowMs := now.UnixMilli()
	for key, expiry := range m.nonces {
		if nowMs >= expiry {
			delete(m.nonces, key)
		}
	}
	for key, rec := range m.regs {
		if nowMs >= rec.ExpiresAtMs {
			delete(m.regs, key)
		}
	}
	for key, rec := range m.wakes {
		if nowMs >= rec.ExpiresAtMs {
			delete(m.wakes, key)
		}
	}
	for key, rec := range m.rates {
		if nowMs >= rec.ExpiresAtMs {
			delete(m.rates, key)
		}
	}
	for key, rec := range m.tombs {
		if nowMs-rec.RevokedAtMs > tombstoneWindow.Milliseconds() {
			delete(m.tombs, key)
		}
	}
	for key, rec := range m.addresses {
		if !rec.Bound && nowMs >= rec.UnboundExpiresMs {
			delete(m.addresses, key)
			m.tombs[rec.MachineRevokeHash] = tombstoneRecord{RevokedAtMs: nowMs}
		}
	}
	for id, rec := range m.installations {
		if installationExpired(rec.LastActiveMs, now) {
			delete(m.installations, id)
			for address, binding := range m.addresses {
				if binding.InstallationID == id {
					delete(m.addresses, address)
					m.tombs[binding.MachineRevokeHash] = tombstoneRecord{RevokedAtMs: nowMs}
				}
			}
		}
	}
	return nil
}
