package pushgw

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	registrationWindow = 10 * time.Minute
	registrationLease  = 30 * time.Second
	wakeWindow         = 5 * time.Minute
	wakeLease          = 15 * time.Second
	maxWakeAttempts    = 3
	gcBatchSize        = 100
	installationWindow = 180 * 24 * time.Hour
)

func installationExpired(lastActiveMs int64, now time.Time) bool {
	return now.UnixMilli()-lastActiveMs >= installationWindow.Milliseconds()
}

type registrationRecord struct {
	BodyDigest     string `firestore:"body_digest"`
	InstallationID string `firestore:"installation_id"`
	RefreshBefore  string `firestore:"refresh_before"`
	ExpiresAtMs    int64  `firestore:"expires_at_ms"`
	State          string `firestore:"state"`
	LeaseID        string `firestore:"lease_id"`
	LeaseUntilMs   int64  `firestore:"lease_until_ms"`
}

type wakeAttemptRecord struct {
	InstallationID  string `firestore:"installation_id"`
	Address         string `firestore:"address"`
	TokenGeneration int64  `firestore:"token_generation"`
	State           string `firestore:"state"`
	Attempts        int64  `firestore:"attempts"`
	LeaseUntilMs    int64  `firestore:"lease_until_ms"`
	LeaseID         string `firestore:"lease_id"`
	ExpiresAtMs     int64  `firestore:"expires_at_ms"`
	Status          int    `firestore:"status"`
	Body            []byte `firestore:"body"`
}

type rateWindowRecord struct {
	Count       int64 `firestore:"count"`
	ExpiresAtMs int64 `firestore:"expires_at_ms"`
}

type registrationResult struct {
	InstallationID string
	RefreshBefore  string
}

type wakeClaim struct {
	TokenEnc        []byte
	KeyVersion      string
	TokenGeneration int64
	Completed       bool
	Status          int
	Body            []byte
	Denied          string
	RetryAfter      int
}

// Repository is the transaction-level domain seam. Firestore is the sole production
// implementation; NewMemoryRepository is an explicitly injected test fake.
type Repository interface {
	close() error
	healthCheck(context.Context) error
	putInstallation(context.Context, string, installationRecord) error
	getInstallation(context.Context, string) (installationRecord, bool, error)
	claimNonceAndTouch(context.Context, string, []byte, string, time.Time, time.Time) (bool, error)
	lookupRegistration(context.Context, string, string, time.Time) (registrationResult, bool, bool, error)
	claimRegistration(context.Context, string, string, string, string, time.Time) (registrationResult, bool, bool, bool, error)
	completeRegistration(context.Context, string, string, string, installationRecord, time.Time) (registrationResult, bool, error)
	releaseRegistration(context.Context, string, string, time.Time) error
	registerOrReturn(context.Context, string, string, string, installationRecord, time.Time) (registrationResult, bool, bool, error)
	rotateToken(context.Context, string, []byte, string, time.Time) (bool, error)
	putAddressIfBelowLimit(context.Context, string, string, addressRecord, int, time.Time) (bool, error)
	getAddress(context.Context, string) (addressRecord, bool, error)
	deleteAddressAndTombstone(context.Context, string, addressRecord, time.Time) error
	getTombstone(context.Context, string) (tombstoneRecord, bool, error)
	allow(context.Context, string, RateLimit, time.Time) (bool, int, error)
	claimWake(context.Context, string, string, string, string, time.Time, time.Time, RateLimit, string, RateLimit) (wakeClaim, bool, error)
	completeWake(context.Context, string, string, int64, int, []byte, bool, time.Time) (bool, error)
	runRetention(context.Context, time.Time) error
}

type v2Store struct {
	p      Repository
	keys   map[string][32]byte
	active string
	idem   [32]byte
}

func newV2Store(repo Repository, active string, rawKeys map[string][]byte, digestKey []byte) (*v2Store, error) {
	if repo == nil {
		return nil, errors.New("pushgw: repository is required")
	}
	if active == "" {
		return nil, errors.New("pushgw: active token key version is required")
	}
	keys := make(map[string][32]byte, len(rawKeys))
	for version, raw := range rawKeys {
		if version == "" || len(raw) != 32 {
			return nil, fmt.Errorf("pushgw: token key %q must be exactly 32 bytes", version)
		}
		var key [32]byte
		copy(key[:], raw)
		keys[version] = key
	}
	if _, ok := keys[active]; !ok {
		return nil, fmt.Errorf("pushgw: active token key version %q is unavailable", active)
	}
	if len(digestKey) != 32 {
		return nil, errors.New("pushgw: registration digest key must be exactly 32 bytes")
	}
	var idem [32]byte
	copy(idem[:], digestKey)
	return &v2Store{p: repo, keys: keys, active: active, idem: idem}, nil
}

func (s *v2Store) close() error { return s.p.close() }

func (s *v2Store) encrypt(plaintext string) ([]byte, string, error) {
	key := s.keys[s.active]
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, "", err
	}
	return gcm.Seal(nonce, nonce, []byte(plaintext), nil), s.active, nil
}

func (s *v2Store) decrypt(ciphertext []byte, version string) (string, error) {
	key, ok := s.keys[version]
	if !ok {
		return "", fmt.Errorf("pushgw: token key version %q unavailable", version)
	}
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return "", errors.New("pushgw: encrypted token truncated")
	}
	plain, err := gcm.Open(nil, ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():], nil)
	return string(plain), err
}

func (s *v2Store) idempotencyID(key string) string {
	h := hmac.New(sha256.New, s.idem[:])
	_, _ = h.Write([]byte("swarm-pushgw-registration/v2\x00" + key))
	return hex.EncodeToString(h.Sum(nil))
}

func NewFirestoreRepository(client *firestore.Client, namespace string) (Repository, error) {
	if client == nil {
		return nil, errors.New("pushgw: Firestore client is required")
	}
	if namespace == "" || strings.Contains(namespace, "/") {
		return nil, errors.New("pushgw: Firestore namespace is required and must not contain '/'")
	}
	return newFirestorePersistence(client, namespace), nil
}

type firestorePersistence struct {
	client *firestore.Client
	prefix string
}

func newFirestorePersistence(client *firestore.Client, namespace string) *firestorePersistence {
	return &firestorePersistence{client: client, prefix: namespace + "_"}
}
func (f *firestorePersistence) col(name string) *firestore.CollectionRef {
	return f.client.Collection(f.prefix + name)
}
func (f *firestorePersistence) close() error { return nil }
func (f *firestorePersistence) healthCheck(ctx context.Context) error {
	_, err := f.col("metadata").Doc("health").Get(ctx)
	if status.Code(err) == codes.NotFound {
		return nil
	}
	return err
}

func (f *firestorePersistence) putInstallation(ctx context.Context, id string, rec installationRecord) error {
	_, err := f.col("installations").Doc(id).Create(ctx, rec)
	return err
}
func decodeSnapshot[T any](snap *firestore.DocumentSnapshot) (T, bool, error) {
	var out T
	if snap == nil || !snap.Exists() {
		return out, false, nil
	}
	if err := snap.DataTo(&out); err != nil {
		return out, false, err
	}
	return out, true, nil
}
func (f *firestorePersistence) getInstallation(ctx context.Context, id string) (installationRecord, bool, error) {
	snap, err := f.col("installations").Doc(id).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return installationRecord{}, false, nil
	}
	if err != nil {
		return installationRecord{}, false, err
	}
	return decodeSnapshot[installationRecord](snap)
}
func (f *firestorePersistence) claimNonceAndTouch(ctx context.Context, id string, expectedPublicKey []byte, nonce string, now, expiry time.Time) (accepted bool, err error) {
	nonceRef := f.col("nonce_claims").Doc(hashSecret(id + "\x00" + nonce))
	instRef := f.col("installations").Doc(id)
	err = f.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		accepted = false
		inst, txErr := tx.Get(instRef)
		if txErr != nil {
			return txErr
		}
		if !inst.Exists() {
			return nil
		}
		var installation installationRecord
		if txErr = inst.DataTo(&installation); txErr != nil {
			return txErr
		}
		if !bytes.Equal(installation.PublicKey, expectedPublicKey) || installationExpired(installation.LastActiveMs, now) {
			return nil
		}
		claim, txErr := tx.Get(nonceRef)
		if txErr == nil && claim.Exists() {
			var old struct {
				ExpiresAtMs int64 `firestore:"expires_at_ms"`
			}
			if txErr = claim.DataTo(&old); txErr != nil {
				return txErr
			}
			if now.UnixMilli() < old.ExpiresAtMs {
				return nil
			}
		} else if txErr != nil && status.Code(txErr) != codes.NotFound {
			return txErr
		}
		horizon := expiry
		if ceiling := now.Add(expiryHorizon); horizon.Before(ceiling) {
			horizon = ceiling
		}
		if txErr = tx.Set(nonceRef, map[string]any{"expires_at_ms": horizon.UnixMilli()}); txErr != nil {
			return txErr
		}
		if txErr = tx.Update(instRef, []firestore.Update{{Path: "last_active_ms", Value: now.UnixMilli()}}); txErr != nil {
			return txErr
		}
		accepted = true
		return nil
	})
	if status.Code(err) == codes.NotFound {
		return false, nil
	}
	return accepted, err
}
func (f *firestorePersistence) lookupRegistration(ctx context.Context, key, digest string, now time.Time) (registrationResult, bool, bool, error) {
	snap, err := f.col("registration_attempts").Doc(key).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return registrationResult{}, false, false, nil
	}
	if err != nil {
		return registrationResult{}, false, false, err
	}
	var rec registrationRecord
	if err := snap.DataTo(&rec); err != nil {
		return registrationResult{}, false, false, err
	}
	if now.UnixMilli() >= rec.ExpiresAtMs {
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
func (f *firestorePersistence) claimRegistration(ctx context.Context, key, digest, candidate, leaseID string, now time.Time) (out registrationResult, won, busy, mismatch bool, err error) {
	ref := f.col("registration_attempts").Doc(key)
	err = f.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		out, won, busy, mismatch = registrationResult{}, false, false, false
		snap, txErr := tx.Get(ref)
		var rec registrationRecord
		if txErr == nil && snap.Exists() {
			if txErr = snap.DataTo(&rec); txErr != nil {
				return txErr
			}
			if now.UnixMilli() < rec.ExpiresAtMs {
				if rec.BodyDigest != digest {
					mismatch = true
					return nil
				}
				if rec.State == "completed" {
					out = registrationResult{rec.InstallationID, rec.RefreshBefore}
					return nil
				}
				if now.UnixMilli() < rec.LeaseUntilMs {
					busy = true
					return nil
				}
			}
		} else if txErr != nil && status.Code(txErr) != codes.NotFound {
			return txErr
		}
		expires := rec.ExpiresAtMs
		if expires <= now.UnixMilli() {
			expires = now.Add(registrationWindow).UnixMilli()
		}
		rec = registrationRecord{BodyDigest: digest, InstallationID: candidate, ExpiresAtMs: expires, State: "pending", LeaseID: leaseID, LeaseUntilMs: now.Add(registrationLease).UnixMilli()}
		if txErr = tx.Set(ref, rec); txErr == nil {
			won = true
		}
		return txErr
	})
	return
}
func (f *firestorePersistence) completeRegistration(ctx context.Context, key, digest, leaseID string, installation installationRecord, now time.Time) (out registrationResult, completed bool, err error) {
	ref := f.col("registration_attempts").Doc(key)
	err = f.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		out, completed = registrationResult{}, false
		snap, txErr := tx.Get(ref)
		if txErr != nil {
			return txErr
		}
		var rec registrationRecord
		if txErr = snap.DataTo(&rec); txErr != nil {
			return txErr
		}
		if rec.BodyDigest != digest || rec.LeaseID != leaseID || rec.State != "pending" || now.UnixMilli() >= rec.ExpiresAtMs {
			return nil
		}
		refresh := now.Add(installationWindow).UTC().Format(time.RFC3339)
		if txErr = tx.Create(f.col("installations").Doc(rec.InstallationID), installation); txErr != nil {
			return txErr
		}
		rec.State, rec.RefreshBefore, rec.LeaseUntilMs = "completed", refresh, 0
		if txErr = tx.Set(ref, rec); txErr == nil {
			out, completed = registrationResult{rec.InstallationID, refresh}, true
		}
		return txErr
	})
	return
}
func (f *firestorePersistence) releaseRegistration(ctx context.Context, key, leaseID string, now time.Time) error {
	ref := f.col("registration_attempts").Doc(key)
	return f.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snap, err := tx.Get(ref)
		if status.Code(err) == codes.NotFound {
			return nil
		}
		if err != nil {
			return err
		}
		var rec registrationRecord
		if err = snap.DataTo(&rec); err != nil {
			return err
		}
		if rec.State == "pending" && rec.LeaseID == leaseID {
			rec.LeaseUntilMs = now.UnixMilli()
			return tx.Set(ref, rec)
		}
		return nil
	})
}
func (f *firestorePersistence) registerOrReturn(ctx context.Context, key, digest, candidate string, rec installationRecord, now time.Time) (out registrationResult, created, mismatch bool, err error) {
	ref := f.col("registration_attempts").Doc(key)
	inst := f.col("installations").Doc(candidate)
	err = f.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		out, created, mismatch = registrationResult{}, false, false
		snap, txErr := tx.Get(ref)
		if txErr == nil && snap.Exists() {
			var old registrationRecord
			if txErr = snap.DataTo(&old); txErr != nil {
				return txErr
			}
			if now.UnixMilli() < old.ExpiresAtMs {
				if old.BodyDigest != digest {
					mismatch = true
					return nil
				}
				out = registrationResult{old.InstallationID, old.RefreshBefore}
				return nil
			}
		} else if txErr != nil && status.Code(txErr) != codes.NotFound {
			return txErr
		}
		refresh := now.Add(installationWindow).UTC().Format(time.RFC3339)
		if txErr = tx.Create(inst, rec); txErr != nil {
			return txErr
		}
		if txErr = tx.Set(ref, registrationRecord{BodyDigest: digest, InstallationID: candidate, RefreshBefore: refresh, ExpiresAtMs: now.Add(registrationWindow).UnixMilli(), State: "completed"}); txErr != nil {
			return txErr
		}
		out, created = registrationResult{candidate, refresh}, true
		return nil
	})
	return
}
func (f *firestorePersistence) rotateToken(ctx context.Context, id string, enc []byte, version string, now time.Time) (updated bool, err error) {
	ref := f.col("installations").Doc(id)
	err = f.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		updated = false
		snap, txErr := tx.Get(ref)
		if status.Code(txErr) == codes.NotFound {
			return nil
		}
		if txErr != nil {
			return txErr
		}
		var rec installationRecord
		if txErr = snap.DataTo(&rec); txErr != nil {
			return txErr
		}
		if installationExpired(rec.LastActiveMs, now) {
			return nil
		}
		if rec.TokenGeneration == math.MaxInt64 {
			return errors.New("pushgw: token generation exhausted")
		}
		rec.FCMTokenEnc, rec.TokenKeyVersion, rec.TokenDead = enc, version, false
		rec.TokenGeneration++
		rec.LastActiveMs = now.UnixMilli()
		if txErr = tx.Set(ref, rec); txErr == nil {
			updated = true
		}
		return txErr
	})
	return
}
func (f *firestorePersistence) putAddressIfBelowLimit(ctx context.Context, addr, installationID string, rec addressRecord, limit int, now time.Time) (created bool, err error) {
	instRef := f.col("installations").Doc(installationID)
	addrRef := f.col("addresses").Doc(addr)
	err = f.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		created = false
		snap, txErr := tx.Get(instRef)
		if txErr != nil {
			return txErr
		}
		var inst installationRecord
		if txErr = snap.DataTo(&inst); txErr != nil {
			return txErr
		}
		if installationExpired(inst.LastActiveMs, now) || inst.AddressCount >= int64(limit) {
			return nil
		}
		if txErr = tx.Create(addrRef, rec); txErr != nil {
			return txErr
		}
		if txErr = tx.Update(instRef, []firestore.Update{{Path: "address_count", Value: inst.AddressCount + 1}}); txErr == nil {
			created = true
		}
		return txErr
	})
	if status.Code(err) == codes.NotFound {
		return false, nil
	}
	return
}
func (f *firestorePersistence) getAddress(ctx context.Context, addr string) (addressRecord, bool, error) {
	snap, err := f.col("addresses").Doc(addr).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return addressRecord{}, false, nil
	}
	if err != nil {
		return addressRecord{}, false, err
	}
	return decodeSnapshot[addressRecord](snap)
}
func (f *firestorePersistence) deleteAddressAndTombstone(ctx context.Context, addr string, rec addressRecord, now time.Time) error {
	addrRef := f.col("addresses").Doc(addr)
	tombRef := f.col("revocation_tombstones").Doc(rec.MachineRevokeHash)
	instRef := f.col("installations").Doc(rec.InstallationID)
	return f.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		snap, txErr := tx.Get(addrRef)
		if status.Code(txErr) == codes.NotFound {
			return nil
		}
		if txErr != nil {
			return txErr
		}
		var current addressRecord
		if txErr = snap.DataTo(&current); txErr != nil {
			return txErr
		}
		if current.MachineRevokeHash != rec.MachineRevokeHash {
			return nil
		}
		inst, instErr := tx.Get(instRef)
		if instErr != nil && status.Code(instErr) != codes.NotFound {
			return instErr
		}
		if txErr = tx.Delete(addrRef); txErr != nil {
			return txErr
		}
		if txErr = tx.Set(tombRef, tombstoneRecord{RevokedAtMs: now.UnixMilli()}); txErr != nil {
			return txErr
		}
		if inst != nil && inst.Exists() {
			var ir installationRecord
			if txErr = inst.DataTo(&ir); txErr != nil {
				return txErr
			}
			if ir.AddressCount > 0 {
				return tx.Update(instRef, []firestore.Update{{Path: "address_count", Value: ir.AddressCount - 1}})
			}
		}
		return nil
	})
}
func (f *firestorePersistence) getTombstone(ctx context.Context, key string) (tombstoneRecord, bool, error) {
	snap, err := f.col("revocation_tombstones").Doc(key).Get(ctx)
	if status.Code(err) == codes.NotFound {
		return tombstoneRecord{}, false, nil
	}
	if err != nil {
		return tombstoneRecord{}, false, err
	}
	return decodeSnapshot[tombstoneRecord](snap)
}
func retrySeconds(expiryMs int64, now time.Time) int {
	n := int(math.Ceil(float64(expiryMs-now.UnixMilli()) / 1000))
	if n < 1 {
		return 1
	}
	return n
}
func rateDocID(key string) string { return hashSecret(key) }
func (f *firestorePersistence) allow(ctx context.Context, key string, rl RateLimit, now time.Time) (ok bool, retry int, err error) {
	ref := f.col("rate_windows").Doc(rateDocID(key))
	err = f.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		ok, retry = false, 0
		snap, txErr := tx.Get(ref)
		var window rateWindowRecord
		if txErr == nil && snap.Exists() {
			if txErr = snap.DataTo(&window); txErr != nil {
				return txErr
			}
		} else if txErr != nil && status.Code(txErr) != codes.NotFound {
			return txErr
		}
		if now.UnixMilli() >= window.ExpiresAtMs {
			window = rateWindowRecord{ExpiresAtMs: now.Add(rl.Window).UnixMilli()}
		}
		if window.Count >= int64(rl.Max) {
			retry = retrySeconds(window.ExpiresAtMs, now)
			return nil
		}
		window.Count++
		ok = true
		return tx.Set(ref, window)
	})
	return
}
func loadRate(tx *firestore.Transaction, ref *firestore.DocumentRef, rl RateLimit, now time.Time) (rateWindowRecord, bool, error) {
	snap, err := tx.Get(ref)
	var window rateWindowRecord
	if err == nil && snap.Exists() {
		if err = snap.DataTo(&window); err != nil {
			return window, false, err
		}
	} else if err != nil && status.Code(err) != codes.NotFound {
		return window, false, err
	}
	if now.UnixMilli() >= window.ExpiresAtMs {
		window = rateWindowRecord{ExpiresAtMs: now.Add(rl.Window).UnixMilli()}
	}
	if window.Count >= int64(rl.Max) {
		return window, false, nil
	}
	window.Count++
	return window, true, nil
}
func (f *firestorePersistence) claimWake(ctx context.Context, id, leaseID, addr, capHash string, now, deadline time.Time, addrLimit RateLimit, sourceKey string, sourceLimit RateLimit) (out wakeClaim, claimed bool, err error) {
	if !now.Before(deadline) {
		return wakeClaim{Denied: "exhausted"}, false, nil
	}
	addrRef := f.col("addresses").Doc(addr)
	wakeRef := f.col("wake_attempts").Doc(id)
	addrQuota := f.col("rate_windows").Doc(rateDocID("wake-addr:" + addr))
	sourceQuota := f.col("rate_windows").Doc(rateDocID(sourceKey))
	err = f.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		out, claimed = wakeClaim{}, false
		addrSnap, txErr := tx.Get(addrRef)
		if txErr != nil {
			return txErr
		}
		var address addressRecord
		if txErr = addrSnap.DataTo(&address); txErr != nil {
			return txErr
		}
		if !verifierEquals(address.SubmitCapHash, capHash) || (!address.Bound && now.UnixMilli() >= address.UnboundExpiresMs) {
			out.Denied = "unauthorized"
			return nil
		}
		wakeSnap, txErr := tx.Get(wakeRef)
		var attempt wakeAttemptRecord
		if txErr == nil && wakeSnap.Exists() {
			if txErr = wakeSnap.DataTo(&attempt); txErr != nil {
				return txErr
			}
			if now.UnixMilli() >= attempt.ExpiresAtMs {
				out.Denied = "exhausted"
				return nil
			}
			if attempt.State == "completed" {
				out = wakeClaim{Completed: true, Status: attempt.Status, Body: attempt.Body}
				return nil
			}
			if attempt.Attempts >= maxWakeAttempts {
				out.Denied = "exhausted"
				return nil
			}
			if now.UnixMilli() < attempt.LeaseUntilMs {
				out.Denied = "busy"
				return nil
			}
			deadline = time.UnixMilli(attempt.ExpiresAtMs)
		} else if txErr != nil && status.Code(txErr) != codes.NotFound {
			return txErr
		}
		instRef := f.col("installations").Doc(address.InstallationID)
		instSnap, txErr := tx.Get(instRef)
		if txErr != nil {
			return txErr
		}
		var installation installationRecord
		if txErr = instSnap.DataTo(&installation); txErr != nil {
			return txErr
		}
		if installationExpired(installation.LastActiveMs, now) {
			out.Denied = "unauthorized"
			return nil
		}
		if installation.TokenDead {
			out.Denied = "token_dead"
			return nil
		}
		addrWindow, ok, txErr := loadRate(tx, addrQuota, addrLimit, now)
		if txErr != nil {
			return txErr
		}
		if !ok {
			out.Denied, out.RetryAfter = "quota", retrySeconds(addrWindow.ExpiresAtMs, now)
			return nil
		}
		sourceWindow, ok, txErr := loadRate(tx, sourceQuota, sourceLimit, now)
		if txErr != nil {
			return txErr
		}
		if !ok {
			out.Denied, out.RetryAfter = "quota", retrySeconds(sourceWindow.ExpiresAtMs, now)
			return nil
		}
		if txErr = tx.Set(addrQuota, addrWindow); txErr != nil {
			return txErr
		}
		if txErr = tx.Set(sourceQuota, sourceWindow); txErr != nil {
			return txErr
		}
		attempt = wakeAttemptRecord{InstallationID: address.InstallationID, Address: addr, TokenGeneration: installation.TokenGeneration, State: "claimed", Attempts: attempt.Attempts + 1, LeaseUntilMs: now.Add(wakeLease).UnixMilli(), LeaseID: leaseID, ExpiresAtMs: deadline.UnixMilli()}
		if txErr = tx.Set(wakeRef, attempt); txErr != nil {
			return txErr
		}
		out = wakeClaim{TokenEnc: installation.FCMTokenEnc, KeyVersion: installation.TokenKeyVersion, TokenGeneration: installation.TokenGeneration}
		claimed = true
		return nil
	})
	if status.Code(err) == codes.NotFound {
		return wakeClaim{Denied: "unauthorized"}, false, nil
	}
	return
}
func (f *firestorePersistence) completeWake(ctx context.Context, id, leaseID string, generation int64, httpStatus int, body []byte, unregistered bool, now time.Time) (staleUnregistered bool, err error) {
	wakeRef := f.col("wake_attempts").Doc(id)
	err = f.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		staleUnregistered = false
		snap, txErr := tx.Get(wakeRef)
		if txErr != nil {
			return txErr
		}
		var attempt wakeAttemptRecord
		if txErr = snap.DataTo(&attempt); txErr != nil {
			return txErr
		}
		// A provider response is not authority to extend the original obligation.
		// Late acceptance may have happened externally, but cannot bind an expired
		// allocation, mutate a token, or reopen retries after this deadline.
		if attempt.TokenGeneration != generation || attempt.LeaseID != leaseID || attempt.State != "claimed" || now.UnixMilli() >= attempt.ExpiresAtMs {
			return nil
		}
		var (
			instRef           *firestore.DocumentRef
			markTokenDead     bool
			staleTokenVerdict bool
			addressRef        *firestore.DocumentRef
			markAddressBound  bool
		)
		if unregistered {
			instRef = f.col("installations").Doc(attempt.InstallationID)
			instSnap, instErr := tx.Get(instRef)
			if instErr == nil {
				var installation installationRecord
				if instErr = instSnap.DataTo(&installation); instErr != nil {
					return instErr
				}
				if installation.TokenGeneration == generation {
					markTokenDead = true
				} else {
					staleTokenVerdict = true
				}
			} else if status.Code(instErr) != codes.NotFound {
				return instErr
			}
		}
		if httpStatus == 200 {
			addressRef = f.col("addresses").Doc(attempt.Address)
			addressSnap, addressErr := tx.Get(addressRef)
			if addressErr == nil && addressSnap.Exists() {
				var address addressRecord
				if addressErr = addressSnap.DataTo(&address); addressErr != nil {
					return addressErr
				}
				markAddressBound = address.Bound || now.UnixMilli() < address.UnboundExpiresMs
			} else if addressErr != nil && status.Code(addressErr) != codes.NotFound {
				return addressErr
			}
		}
		if markTokenDead {
			if txErr = tx.Update(instRef, []firestore.Update{{Path: "fcm_token_enc", Value: nil}, {Path: "token_dead", Value: true}}); txErr != nil {
				return txErr
			}
		}
		if markAddressBound {
			if txErr = tx.Update(addressRef, []firestore.Update{{Path: "bound", Value: true}}); txErr != nil {
				return txErr
			}
		}
		if staleTokenVerdict {
			attempt.LeaseUntilMs = now.UnixMilli()
			staleUnregistered = true
			return tx.Set(wakeRef, attempt)
		}
		if httpStatus == 503 {
			attempt.LeaseUntilMs = now.UnixMilli()
			return tx.Set(wakeRef, attempt)
		}
		attempt.State, attempt.Status, attempt.Body = "completed", httpStatus, body
		return tx.Set(wakeRef, attempt)
	})
	return staleUnregistered, err
}
func (f *firestorePersistence) runRetention(ctx context.Context, now time.Time) error {
	for _, name := range []string{"nonce_claims", "registration_attempts", "wake_attempts", "rate_windows"} {
		if err := f.deleteExpired(ctx, name, "expires_at_ms", now.UnixMilli()); err != nil {
			return err
		}
	}
	if err := f.deleteExpired(ctx, "revocation_tombstones", "revoked_at_ms", now.Add(-tombstoneWindow).UnixMilli()); err != nil {
		return err
	}
	if err := f.deleteExpiredAddresses(ctx, now); err != nil {
		return err
	}
	return f.deleteExpiredInstallations(ctx, now)
}
func (f *firestorePersistence) deleteExpired(ctx context.Context, collection, field string, before int64) error {
	it := f.col(collection).Where(field, "<=", before).Limit(gcBatchSize).Documents(ctx)
	defer it.Stop()
	snapshots, err := it.GetAll()
	if err != nil {
		return err
	}
	refs := make([]*firestore.DocumentRef, 0, len(snapshots))
	for _, snap := range snapshots {
		refs = append(refs, snap.Ref)
	}
	for _, ref := range refs {
		if err := f.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
			current, err := tx.Get(ref)
			if status.Code(err) == codes.NotFound {
				return nil
			}
			if err != nil {
				return err
			}
			value, err := current.DataAt(field)
			if err != nil {
				return err
			}
			expires, ok := value.(int64)
			if ok && expires <= before {
				return tx.Delete(ref)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func (f *firestorePersistence) deleteExpiredAddresses(ctx context.Context, now time.Time) error {
	it := f.col("addresses").Where("bound", "==", false).Where("unbound_expires_ms", "<=", now.UnixMilli()).Limit(gcBatchSize).Documents(ctx)
	defer it.Stop()
	snapshots, err := it.GetAll()
	if err != nil {
		return err
	}
	refs := make([]*firestore.DocumentRef, 0, len(snapshots))
	for _, snap := range snapshots {
		refs = append(refs, snap.Ref)
	}
	for _, ref := range refs {
		err := f.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
			snap, err := tx.Get(ref)
			if status.Code(err) == codes.NotFound {
				return nil
			}
			if err != nil {
				return err
			}
			var address addressRecord
			if err = snap.DataTo(&address); err != nil {
				return err
			}
			if address.Bound || address.UnboundExpiresMs > now.UnixMilli() {
				return nil
			}
			instRef := f.col("installations").Doc(address.InstallationID)
			instSnap, instErr := tx.Get(instRef)
			if instErr != nil && status.Code(instErr) != codes.NotFound {
				return instErr
			}
			if err = tx.Delete(ref); err != nil {
				return err
			}
			if err = tx.Set(f.col("revocation_tombstones").Doc(address.MachineRevokeHash), tombstoneRecord{RevokedAtMs: now.UnixMilli()}); err != nil {
				return err
			}
			if instSnap != nil && instSnap.Exists() {
				var installation installationRecord
				if err = instSnap.DataTo(&installation); err != nil {
					return err
				}
				if installation.AddressCount > 0 {
					return tx.Update(instRef, []firestore.Update{{Path: "address_count", Value: installation.AddressCount - 1}})
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (f *firestorePersistence) deleteExpiredInstallations(ctx context.Context, now time.Time) error {
	cutoff := now.Add(-installationWindow).UnixMilli()
	it := f.col("installations").Where("last_active_ms", "<=", cutoff).Limit(gcBatchSize).Documents(ctx)
	defer it.Stop()
	snapshots, err := it.GetAll()
	if err != nil {
		return err
	}
	refs := make([]*firestore.DocumentRef, 0, len(snapshots))
	for _, snap := range snapshots {
		refs = append(refs, snap.Ref)
	}
	for _, instRef := range refs {
		addresses, err := f.col("addresses").Where("installation_id", "==", instRef.ID).Limit(maxAddressesPerInstallation).Documents(ctx).GetAll()
		if err != nil {
			return err
		}
		err = f.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
			instSnap, err := tx.Get(instRef)
			if status.Code(err) == codes.NotFound {
				return nil
			}
			if err != nil {
				return err
			}
			var installation installationRecord
			if err = instSnap.DataTo(&installation); err != nil {
				return err
			}
			if installation.LastActiveMs > cutoff {
				return nil
			}
			current := make([]addressRecord, len(addresses))
			for i, address := range addresses {
				snap, e := tx.Get(address.Ref)
				if status.Code(e) == codes.NotFound {
					continue
				}
				if e != nil {
					return e
				}
				if e = snap.DataTo(&current[i]); e != nil {
					return e
				}
			}
			if err = tx.Delete(instRef); err != nil {
				return err
			}
			for i, address := range addresses {
				if current[i].InstallationID != "" {
					if err = tx.Delete(address.Ref); err != nil {
						return err
					}
					if err = tx.Set(f.col("revocation_tombstones").Doc(current[i].MachineRevokeHash), tombstoneRecord{RevokedAtMs: now.UnixMilli()}); err != nil {
						return err
					}
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// memoryRepository is a concurrency-correct fake, not a production backend.
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
		return wakeClaim{Denied: "exhausted"}, false, nil
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
	attempt = wakeAttemptRecord{InstallationID: address.InstallationID, Address: addr, TokenGeneration: installation.TokenGeneration, State: "claimed", Attempts: attempt.Attempts + 1, LeaseUntilMs: now.Add(wakeLease).UnixMilli(), LeaseID: leaseID, ExpiresAtMs: deadline.UnixMilli()}
	m.wakes[id] = attempt
	return wakeClaim{TokenEnc: installation.FCMTokenEnc, KeyVersion: installation.TokenKeyVersion, TokenGeneration: installation.TokenGeneration}, true, nil
}
func (m *memoryRepository) completeWake(_ context.Context, id, leaseID string, generation int64, httpStatus int, body []byte, unregistered bool, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	attempt, ok := m.wakes[id]
	if !ok || attempt.TokenGeneration != generation || attempt.LeaseID != leaseID || attempt.State != "claimed" || now.UnixMilli() >= attempt.ExpiresAtMs {
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
