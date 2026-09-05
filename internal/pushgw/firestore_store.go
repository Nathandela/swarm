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
	LeaseUntilMs    int64
	ProviderBudget  time.Duration
	Completed       bool
	Status          int
	Body            []byte
	Denied          string
	RetryAfter      int
}

// Repository is the transaction-level domain seam. Firestore is its sole production
// implementation; tests inject a package-local fake.
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
	_, err := f.col("metadata").Limit(1).Documents(ctx).GetAll()
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

func latestReadTime(snaps ...*firestore.DocumentSnapshot) (time.Time, error) {
	var latest time.Time
	for _, snap := range snaps {
		if snap == nil || snap.ReadTime.IsZero() {
			return time.Time{}, errors.New("pushgw: Firestore snapshot has no server read time")
		}
		if snap.ReadTime.After(latest) {
			latest = snap.ReadTime
		}
	}
	return latest, nil
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
func takeRate(window rateWindowRecord, rl RateLimit, now time.Time) (rateWindowRecord, bool) {
	if now.UnixMilli() >= window.ExpiresAtMs {
		window = rateWindowRecord{ExpiresAtMs: now.Add(rl.Window).UnixMilli()}
	}
	if window.Count >= int64(rl.Max) {
		return window, false
	}
	window.Count++
	return window, true
}
func (f *firestorePersistence) claimWake(ctx context.Context, id, leaseID, addr, capHash string, now, deadline time.Time, addrLimit RateLimit, sourceKey string, sourceLimit RateLimit) (out wakeClaim, claimed bool, err error) {
	addrRef := f.col("addresses").Doc(addr)
	wakeRef := f.col("wake_attempts").Doc(id)
	addrQuota := f.col("rate_windows").Doc(rateDocID("wake-addr:" + addr))
	sourceQuota := f.col("rate_windows").Doc(rateDocID(sourceKey))
	var committedDeadline time.Time
	var commit firestore.CommitResponse
	err = f.client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		out, claimed = wakeClaim{}, false
		attemptDeadline := deadline
		addrSnap, txErr := tx.Get(addrRef)
		if txErr != nil {
			return txErr
		}
		var address addressRecord
		if txErr = addrSnap.DataTo(&address); txErr != nil {
			return txErr
		}
		// Capability mismatch needs no server-clock decision. Reject it before
		// fetching durable attempt, installation, or quota state.
		if !verifierEquals(address.SubmitCapHash, capHash) {
			out.Denied = "unauthorized"
			return nil
		}
		wakeSnap, txErr := tx.Get(wakeRef)
		var attempt wakeAttemptRecord
		if txErr == nil && wakeSnap.Exists() {
			if txErr = wakeSnap.DataTo(&attempt); txErr != nil {
				return txErr
			}
			attemptDeadline = time.UnixMilli(attempt.ExpiresAtMs)
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
		quotaSnaps, txErr := tx.GetAll([]*firestore.DocumentRef{addrQuota, sourceQuota})
		if txErr != nil {
			return txErr
		}
		txNow, txErr := latestReadTime(addrSnap, wakeSnap, instSnap, quotaSnaps[0], quotaSnaps[1])
		if txErr != nil {
			return txErr
		}
		if !txNow.Before(deadline) || deadline.Add(-wakeWindow).After(txNow.Add(expiryHorizon)) {
			out.Denied = "malformed"
			return nil
		}
		if !txNow.Before(attemptDeadline) {
			out.Denied = "exhausted"
			return nil
		}
		if !address.Bound && txNow.UnixMilli() >= address.UnboundExpiresMs {
			out.Denied = "unauthorized"
			return nil
		}
		if wakeSnap.Exists() {
			if attempt.State == "completed" {
				out = wakeClaim{Completed: true, Status: attempt.Status, Body: attempt.Body}
				return nil
			}
			if attempt.Attempts >= maxWakeAttempts {
				out.Denied = "exhausted"
				return nil
			}
			if txNow.UnixMilli() < attempt.LeaseUntilMs {
				out.Denied = "busy"
				return nil
			}
		}
		if installationExpired(installation.LastActiveMs, txNow) {
			out.Denied = "unauthorized"
			return nil
		}
		if installation.TokenDead {
			out.Denied = "token_dead"
			return nil
		}
		var addrWindow, sourceWindow rateWindowRecord
		if quotaSnaps[0].Exists() {
			if txErr = quotaSnaps[0].DataTo(&addrWindow); txErr != nil {
				return txErr
			}
		}
		addrWindow, ok := takeRate(addrWindow, addrLimit, txNow)
		if !ok {
			out.Denied, out.RetryAfter = "quota", retrySeconds(addrWindow.ExpiresAtMs, txNow)
			return nil
		}
		if quotaSnaps[1].Exists() {
			if txErr = quotaSnaps[1].DataTo(&sourceWindow); txErr != nil {
				return txErr
			}
		}
		sourceWindow, ok = takeRate(sourceWindow, sourceLimit, txNow)
		if !ok {
			out.Denied, out.RetryAfter = "quota", retrySeconds(sourceWindow.ExpiresAtMs, txNow)
			return nil
		}
		if txErr = tx.Set(addrQuota, addrWindow); txErr != nil {
			return txErr
		}
		if txErr = tx.Set(sourceQuota, sourceWindow); txErr != nil {
			return txErr
		}
		leaseUntil := txNow.Add(wakeLease).UnixMilli()
		attempt = wakeAttemptRecord{InstallationID: address.InstallationID, Address: addr, TokenGeneration: installation.TokenGeneration, State: "claimed", Attempts: attempt.Attempts + 1, LeaseUntilMs: leaseUntil, LeaseID: leaseID, ExpiresAtMs: attemptDeadline.UnixMilli()}
		if txErr = tx.Set(wakeRef, attempt); txErr != nil {
			return txErr
		}
		out = wakeClaim{
			TokenEnc:        installation.FCMTokenEnc,
			KeyVersion:      installation.TokenKeyVersion,
			TokenGeneration: installation.TokenGeneration,
			LeaseUntilMs:    leaseUntil,
			ProviderBudget:  min(time.UnixMilli(leaseUntil).Sub(txNow), attemptDeadline.Sub(txNow)),
		}
		committedDeadline = attemptDeadline
		claimed = true
		return nil
	}, firestore.WithCommitResponseTo(&commit))
	if status.Code(err) == codes.NotFound {
		return wakeClaim{Denied: "unauthorized"}, false, nil
	}
	if err == nil && claimed {
		commitTime := commit.CommitTime()
		if commitTime.IsZero() {
			return wakeClaim{}, false, errors.New("pushgw: Firestore claim commit has no server time")
		}
		if !commitTime.Before(committedDeadline) {
			return wakeClaim{Denied: "exhausted"}, false, nil
		}
		if !commitTime.Before(time.UnixMilli(out.LeaseUntilMs)) {
			return wakeClaim{Denied: "busy"}, false, nil
		}
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
		if attempt.TokenGeneration != generation || attempt.LeaseID != leaseID || attempt.State != "claimed" {
			return nil
		}
		var (
			instRef           *firestore.DocumentRef
			markTokenDead     bool
			staleTokenVerdict bool
			addressRef        *firestore.DocumentRef
			markAddressBound  bool
			instSnap          *firestore.DocumentSnapshot
			addressSnap       *firestore.DocumentSnapshot
		)
		if unregistered {
			instRef = f.col("installations").Doc(attempt.InstallationID)
			instSnap, txErr = tx.Get(instRef)
			if txErr == nil {
				var installation installationRecord
				if txErr = instSnap.DataTo(&installation); txErr != nil {
					return txErr
				}
				if installation.TokenGeneration == generation {
					markTokenDead = true
				} else {
					staleTokenVerdict = true
				}
			} else if status.Code(txErr) != codes.NotFound {
				return txErr
			}
		}
		if httpStatus == 200 {
			addressRef = f.col("addresses").Doc(attempt.Address)
			addressSnap, txErr = tx.Get(addressRef)
			if txErr == nil && addressSnap.Exists() {
				var address addressRecord
				if txErr = addressSnap.DataTo(&address); txErr != nil {
					return txErr
				}
				markAddressBound = address.InstallationID == attempt.InstallationID
			} else if txErr != nil && status.Code(txErr) != codes.NotFound {
				return txErr
			}
		}
		readSnaps := []*firestore.DocumentSnapshot{snap}
		if instSnap != nil {
			readSnaps = append(readSnaps, instSnap)
		}
		if addressSnap != nil {
			readSnaps = append(readSnaps, addressSnap)
		}
		txNow, txErr := latestReadTime(readSnaps...)
		if txErr != nil {
			return txErr
		}
		// The latest transactional server read is the exact authority boundary.
		// Firestore exposes commit time only after writes, so this deliberately does
		// not claim an impossible commit-time predicate for completion side effects.
		if txNow.UnixMilli() >= attempt.ExpiresAtMs || txNow.UnixMilli() >= attempt.LeaseUntilMs {
			return nil
		}
		if markAddressBound {
			var address addressRecord
			if txErr = addressSnap.DataTo(&address); txErr != nil {
				return txErr
			}
			markAddressBound = address.Bound || txNow.UnixMilli() < address.UnboundExpiresMs
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
			attempt.LeaseUntilMs = txNow.UnixMilli()
			staleUnregistered = true
			return tx.Set(wakeRef, attempt)
		}
		if httpStatus == 503 {
			attempt.LeaseUntilMs = txNow.UnixMilli()
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
