package pushgw

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	crand "crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	bolt "go.etcd.io/bbolt"
)

// bbolt buckets. Field content per bucket is exactly PG-RET-10's closed stored-field set:
// token mapping, installation public key, opaque address, hashed capability verifiers,
// creation/last-use timestamps, an attestation verdict class, and the minimal delivery
// diagnostic PG-RET-3 places here (the machine-revoke tombstone). Nothing else is ever
// written to disk by this package.
var (
	bucketInstallations = []byte("installations")
	bucketAddresses     = []byte("addresses")
	bucketTombstones    = []byte("tombstones")
)

// installationRecord is one row of the "installations" bucket, keyed by installation_id.
// FCMTokenEnc is AEAD ciphertext (PG-RET-5); the plaintext token never touches disk.
// AttestationVerdictToken is deliberately absent -- PG-AUTH-12 forbids persisting it in
// any form, hashed or otherwise.
type installationRecord struct {
	PublicKey     []byte `json:"public_key"`
	FCMTokenEnc   []byte `json:"fcm_token_enc"`
	CreatedAtMs   int64  `json:"created_at_ms"`
	LastActiveMs  int64  `json:"last_active_ms"`
	LicensedBuild bool   `json:"licensed_build"`
	// TokenDead is PG-ROT-2's dead-mapping marker: set (and FCMTokenEnc cleared) when FCM
	// reports the stored token UNREGISTERED. It is part of the closed "token mapping"
	// stored field (PG-RET-10) -- it describes that mapping's own liveness, not a new
	// category of datum. Cleared by the next successful PUT .../token (§3.2).
	TokenDead bool `json:"token_dead"`
}

// addressRecord is one row of the "addresses" bucket, keyed by push_address. Only the
// SHA-256 verifiers of the two capabilities are stored (PG-AUTH-7); the raw 32 CSPRNG
// bytes a client presents never touch disk.
type addressRecord struct {
	InstallationID    string `json:"installation_id"`
	SubmitCapHash     string `json:"submit_cap_hash"`
	MachineRevokeHash string `json:"machine_revoke_hash"`
	CreatedAtMs       int64  `json:"created_at_ms"`
	Bound             bool   `json:"bound"`
	UnboundExpiresMs  int64  `json:"unbound_expires_ms"`
}

// tombstoneRecord is PG-REV-2 / PG-RET-3's bounded revocation tombstone. The bucket is
// KEYED by the hashed machine-revoke verifier itself (hex SHA-256, the same hashSecret
// value addressRecord.MachineRevokeHash carries) rather than by push_address: key plus
// value together are exactly "the hashed machine-revoke verifier plus a revoked-at
// timestamp, no address content" -- PG-REV-2's and PG-RET-3's wording, taken literally.
// Keying by address (an earlier revision of this file did) let the revoked address survive
// on disk for up to 7 days after every deletion path, including the 180-day inactivity
// sweep tombstoning a whole installation's addresses at once -- exactly the disclosure the
// tombstone's bound exists to prevent. A durable retry already presents the capability
// whose hash is the key, so no address lookup is needed to find its own tombstone.
type tombstoneRecord struct {
	RevokedAtMs int64 `json:"revoked_at_ms"`
}

// store wraps the single bbolt file plus the gateway-local at-rest encryption key.
type store struct {
	db      *bolt.DB
	key     [32]byte
	dbPath  string
	keyPath string
}

func openStore(dbPath, keyPath string) (*store, error) {
	if _, err := os.Stat(restoreMarkerPath(dbPath)); err == nil {
		return nil, fmt.Errorf("pushgw: interrupted restore for %s; rerun the restore command before opening the gateway", dbPath)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("pushgw: inspect restore marker: %w", err)
	}
	db, err := bolt.Open(dbPath, 0o600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("pushgw: open %s: %w", dbPath, err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{bucketInstallations, bucketAddresses, bucketTombstones} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("pushgw: init buckets: %w", err)
	}
	key, err := loadOrCreateKey(keyPath)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &store{db: db, key: key, dbPath: dbPath, keyPath: keyPath}, nil
}

func (s *store) close() error { return s.db.Close() }

func (s *store) healthCheck() error {
	if err := s.db.Update(func(*bolt.Tx) error { return nil }); err != nil {
		return fmt.Errorf("database not writable: %w", err)
	}
	onDiskKey, err := os.ReadFile(s.keyPath)
	if err != nil {
		return fmt.Errorf("AEAD key unavailable: %w", err)
	}
	if len(onDiskKey) != len(s.key) {
		return fmt.Errorf("AEAD key has size %d, want %d", len(onDiskKey), len(s.key))
	}
	if !bytes.Equal(onDiskKey, s.key[:]) {
		return errors.New("AEAD key on disk does not match the key loaded by this process")
	}
	return nil
}

type storeMetrics struct {
	DBBytes       int64
	Installations int
	Addresses     int
	Tombstones    int
}

func (s *store) metrics() storeMetrics {
	var out storeMetrics
	if info, err := os.Stat(s.dbPath); err == nil {
		out.DBBytes = info.Size()
	}
	_ = s.db.View(func(tx *bolt.Tx) error {
		out.Installations = tx.Bucket(bucketInstallations).Stats().KeyN
		out.Addresses = tx.Bucket(bucketAddresses).Stats().KeyN
		out.Tombstones = tx.Bucket(bucketTombstones).Stats().KeyN
		return nil
	})
	return out
}

// --- at-rest encryption (PG-RET-5) -----------------------------------------------------

// loadOrCreateKey reads the gateway-local AEAD key from path, generating and persisting a
// fresh one at 0600 on first boot. The key never leaves this file; losing it makes every
// stored fcm_token unrecoverable, which is the correct failure mode for a secret with no
// other backing store.
func loadOrCreateKey(path string) ([32]byte, error) {
	var key [32]byte
	data, err := os.ReadFile(path)
	if err == nil {
		if len(data) != len(key) {
			return key, fmt.Errorf("pushgw: key file %s has wrong length %d, want %d", path, len(data), len(key))
		}
		copy(key[:], data)
		return key, nil
	}
	if !os.IsNotExist(err) {
		return key, fmt.Errorf("pushgw: read key file %s: %w", path, err)
	}
	if _, err := crand.Read(key[:]); err != nil {
		return key, err
	}
	if err := os.WriteFile(path, key[:], 0o600); err != nil {
		return key, fmt.Errorf("pushgw: write key file %s: %w", path, err)
	}
	return key, nil
}

func (s *store) encrypt(plaintext string) ([]byte, error) {
	block, err := aes.NewCipher(s.key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := crand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, []byte(plaintext), nil), nil
}

func (s *store) decrypt(ciphertext []byte) (string, error) {
	block, err := aes.NewCipher(s.key[:])
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
	nonce, ct := ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():]
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

// --- capability verifiers (PG-AUTH-7) ---------------------------------------------------

func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// verifierEquals compares two hex-encoded SHA-256 verifiers in constant time (PG-AUTH-7).
func verifierEquals(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// --- installations ------------------------------------------------------------------

func (s *store) putInstallation(id string, rec installationRecord) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		return tx.Bucket(bucketInstallations).Put([]byte(id), b)
	})
}

func (s *store) getInstallation(id string) (installationRecord, bool, error) {
	var rec installationRecord
	found := false
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketInstallations).Get([]byte(id))
		if v == nil {
			return nil
		}
		found = true
		return json.Unmarshal(v, &rec)
	})
	return rec, found, err
}

// touchInstallation resets the 180-day inactivity clock (PG-AUTH-5). It is a no-op,
// rather than an error, for an installation that no longer exists (a caller that already
// checked existence for auth purposes need not check twice).
func (s *store) touchInstallation(id string, nowMs int64) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketInstallations)
		v := b.Get([]byte(id))
		if v == nil {
			return nil
		}
		var rec installationRecord
		if err := json.Unmarshal(v, &rec); err != nil {
			return err
		}
		rec.LastActiveMs = nowMs
		nv, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		return b.Put([]byte(id), nv)
	})
}

// updateInstallationIfPresent reads, mutates via fn, and writes back installationID's record
// inside ONE bbolt transaction, and is a no-op (fn never runs, updated is false) if the
// installation does not exist. This closes a read-modify-write race in rotate.go: reading
// the record at the top of the handler and blind-writing the WHOLE thing back at the end
// let the 180-day inactivity sweep (runRetention) delete the row in between, and rotate's
// final write then silently RESURRECTED it -- token and all, minus its addresses, undoing
// §8.1 row 3's deletion. Mirrors markInstallationTokenDeadIfCurrent's own single-transaction
// CAS shape, generalized to an arbitrary mutation.
func (s *store) updateInstallationIfPresent(id string, fn func(rec *installationRecord)) (updated bool, err error) {
	err = s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketInstallations)
		v := b.Get([]byte(id))
		if v == nil {
			return nil
		}
		var rec installationRecord
		if unmarshalErr := json.Unmarshal(v, &rec); unmarshalErr != nil {
			return unmarshalErr
		}
		fn(&rec)
		updated = true
		nv, marshalErr := json.Marshal(rec)
		if marshalErr != nil {
			return marshalErr
		}
		return b.Put([]byte(id), nv)
	})
	return updated, err
}

// markInstallationTokenDeadIfCurrent implements PG-ROT-2: on an UNREGISTERED verdict from
// FCM, the token bytes are deleted (PG-RET-2 -- actual deletion, never a tombstone that
// retains them) and the mapping is marked dead, so wake.go refuses subsequent submits with
// push_token_unregistered before ever calling the provider again.
//
// It is a compare-and-set on sentTokenEnc, the exact ciphertext the send was attempted
// with, rather than an unconditional write. Without the guard: a wake dispatches against
// token T1, the handset rotates to T2 mid-flight (clearing TokenDead), and the in-flight
// send's stale UNREGISTERED verdict for T1 arrives after -- an unconditional write would
// delete T2 and mark the mapping dead even though T2 was never sent and may be perfectly
// live, killing push for that installation until it rotates again with nothing to prompt
// it. Each bbolt write is atomic, so the race is a stale DECISION, not a torn write, and a
// compare-and-set on the record actually sent is what closes it. A no-op, not an error, for
// an installation that no longer exists OR whose stored token has already changed since.
func (s *store) markInstallationTokenDeadIfCurrent(id string, sentTokenEnc []byte) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketInstallations)
		v := b.Get([]byte(id))
		if v == nil {
			return nil
		}
		var rec installationRecord
		if err := json.Unmarshal(v, &rec); err != nil {
			return err
		}
		if !bytes.Equal(rec.FCMTokenEnc, sentTokenEnc) {
			return nil
		}
		rec.FCMTokenEnc = nil
		rec.TokenDead = true
		nv, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		return b.Put([]byte(id), nv)
	})
}

// --- addresses -----------------------------------------------------------------------

func (s *store) putAddress(addr string, rec addressRecord) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		return tx.Bucket(bucketAddresses).Put([]byte(addr), b)
	})
}

func (s *store) getAddress(addr string) (addressRecord, bool, error) {
	var rec addressRecord
	found := false
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketAddresses).Get([]byte(addr))
		if v == nil {
			return nil
		}
		found = true
		return json.Unmarshal(v, &rec)
	})
	return rec, found, err
}

func (s *store) markAddressBound(addr string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucketAddresses)
		v := b.Get([]byte(addr))
		if v == nil {
			return nil
		}
		var rec addressRecord
		if err := json.Unmarshal(v, &rec); err != nil {
			return err
		}
		if rec.Bound {
			return nil
		}
		rec.Bound = true
		nv, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		return b.Put([]byte(addr), nv)
	})
}

// deleteAddressAndTombstone deletes addr and writes its PG-REV-2 tombstone (keyed by
// rec.MachineRevokeHash) in ONE bbolt transaction -- the single-address form of
// tombstoneAndDeleteAll, used by both revoke.go credential arms so neither can leave the
// address deleted with no tombstone if a crash or a write error lands between the two
// effects. Before this, revokeByMachineCapability performed the delete and the putTombstone
// as two separate db.Update calls: a process exit or a putTombstone error in between left
// the address gone with no tombstone, and every later durable retry (PG-REV-2) then saw 401
// forever instead of the idempotent 204 the tombstone exists to guarantee.
func (s *store) deleteAddressAndTombstone(addr string, rec addressRecord, nowMs int64) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		addrB := tx.Bucket(bucketAddresses)
		tombB := tx.Bucket(bucketTombstones)
		return tombstoneAndDeleteAll(addrB, tombB, []deadAddress{{key: []byte(addr), rec: rec}}, nowMs)
	})
}

// countAddresses returns how many live addresses belong to installationID (PG-ALLOC-4).
func (s *store) countAddresses(installationID string) (int, error) {
	n := 0
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketAddresses).ForEach(func(_, v []byte) error {
			var rec addressRecord
			if err := json.Unmarshal(v, &rec); err != nil {
				return err
			}
			if rec.InstallationID == installationID {
				n++
			}
			return nil
		})
	})
	return n, err
}

// deadAddress is one address bound for deletion inside a retention transaction, carrying
// enough of its own record (the machine-revoke verifier) to tombstone it on the way out.
type deadAddress struct {
	key []byte
	rec addressRecord
}

// deleteAddressesForInstallation removes every address belonging to installationID (used
// by the 180-day inactivity sweep, section 8.1 row 3: "Delete FCM token mapping and
// addresses") and tombstones each one (PG-REV-2): a machine's durable revoke retry must
// see 204, not 401, regardless of WHICH path destroyed the address first -- the tombstone
// has to be written on every path that destroys one, not only an explicit revoke.
func (s *store) deleteAddressesForInstallation(tx *bolt.Tx, installationID string, nowMs int64) error {
	addrB := tx.Bucket(bucketAddresses)
	tombB := tx.Bucket(bucketTombstones)
	var dead []deadAddress
	err := addrB.ForEach(func(k, v []byte) error {
		var rec addressRecord
		if err := json.Unmarshal(v, &rec); err != nil {
			return err
		}
		if rec.InstallationID == installationID {
			dead = append(dead, deadAddress{key: append([]byte(nil), k...), rec: rec})
		}
		return nil
	})
	if err != nil {
		return err
	}
	return tombstoneAndDeleteAll(addrB, tombB, dead, nowMs)
}

// tombstoneAndDeleteAll deletes every address in dead and writes its PG-REV-2 tombstone,
// keyed by the hashed machine-revoke verifier (not the address -- see tombstoneRecord's
// comment), inside the caller's own bbolt transaction. Shared by every deletion path so
// none of them can forget the tombstone -- the property PG-REV-2 needs is "written on every
// path that destroys an address," not "written by whichever handler happens to remember."
func tombstoneAndDeleteAll(addrB, tombB *bolt.Bucket, dead []deadAddress, nowMs int64) error {
	for _, d := range dead {
		if err := addrB.Delete(d.key); err != nil {
			return err
		}
		tb, err := json.Marshal(tombstoneRecord{RevokedAtMs: nowMs})
		if err != nil {
			return err
		}
		if err := tombB.Put([]byte(d.rec.MachineRevokeHash), tb); err != nil {
			return err
		}
	}
	return nil
}

// --- tombstones (PG-REV-2, PG-RET-3) --------------------------------------------------
//
// Writing a tombstone always happens together with deleting the address it revokes (see
// deleteAddressAndTombstone and tombstoneAndDeleteAll above), in one bbolt transaction, so
// there is no standalone putTombstone here -- only the read side. getTombstone keys on
// machineRevokeHash -- hex SHA-256 of the presented machine-revoke capability, i.e. exactly
// what a durable retry can compute for itself without ever knowing the address again. See
// tombstoneRecord's comment for why.

func (s *store) getTombstone(machineRevokeHash string) (tombstoneRecord, bool, error) {
	var rec tombstoneRecord
	found := false
	err := s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(bucketTombstones).Get([]byte(machineRevokeHash))
		if v == nil {
			return nil
		}
		found = true
		return json.Unmarshal(v, &rec)
	})
	return rec, found, err
}

// --- retention sweep (section 8.1) ----------------------------------------------------

// runRetention applies all three of section 8.1's rows in one bbolt transaction, driven by
// nowMs so the caller's injected clock governs entirely -- nothing here sleeps or reads the
// wall clock.
func (s *store) runRetention(nowMs int64) error {
	const (
		inactivityWindowMs = 180 * 24 * 60 * 60 * 1000
		tombstoneWindowMs  = 7 * 24 * 60 * 60 * 1000
	)
	return s.db.Update(func(tx *bolt.Tx) error {
		addrB := tx.Bucket(bucketAddresses)
		tombB := tx.Bucket(bucketTombstones)
		var deadAddrs []deadAddress
		if err := addrB.ForEach(func(k, v []byte) error {
			var rec addressRecord
			if err := json.Unmarshal(v, &rec); err != nil {
				return err
			}
			if !rec.Bound && nowMs > rec.UnboundExpiresMs {
				deadAddrs = append(deadAddrs, deadAddress{key: append([]byte(nil), k...), rec: rec})
			}
			return nil
		}); err != nil {
			return err
		}
		// PG-REV-2: the unbound sweep destroys an address exactly as a revoke does, so a
		// machine's durable revoke retry landing after the sweep must see the same
		// idempotent 204 -- tombstone it here too.
		if err := tombstoneAndDeleteAll(addrB, tombB, deadAddrs, nowMs); err != nil {
			return err
		}

		instB := tx.Bucket(bucketInstallations)
		var deadInst []string
		if err := instB.ForEach(func(k, v []byte) error {
			var rec installationRecord
			if err := json.Unmarshal(v, &rec); err != nil {
				return err
			}
			if nowMs-rec.LastActiveMs > inactivityWindowMs {
				deadInst = append(deadInst, string(k))
			}
			return nil
		}); err != nil {
			return err
		}
		for _, id := range deadInst {
			if err := instB.Delete([]byte(id)); err != nil {
				return err
			}
			if err := s.deleteAddressesForInstallation(tx, id, nowMs); err != nil {
				return err
			}
		}

		var deadTombs [][]byte
		if err := tombB.ForEach(func(k, v []byte) error {
			var rec tombstoneRecord
			if err := json.Unmarshal(v, &rec); err != nil {
				return err
			}
			if nowMs-rec.RevokedAtMs > tombstoneWindowMs {
				deadTombs = append(deadTombs, append([]byte(nil), k...))
			}
			return nil
		}); err != nil {
			return err
		}
		for _, k := range deadTombs {
			if err := tombB.Delete(k); err != nil {
				return err
			}
		}
		return nil
	})
}
