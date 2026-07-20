package relay

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"

	bolt "go.etcd.io/bbolt"
)

// The persistence store (R-REL.7): an embedded transactional bbolt file holding
// the per-device mailbox log, its monotonic storage cursors, the pairing graph,
// and the revocation set. It stores ONLY opaque ciphertext + routing metadata —
// never plaintext, identity keys, or the pairing secret. Routing ids are HKDF
// handles; the relay-auth pubkeys they derive from are never persisted.
var (
	bucketItems   = []byte("items")   // nested: rid -> (cursor8 -> record)
	bucketSeq     = []byte("seq")     // rid -> next storage cursor (8 bytes)
	bucketPairs   = []byte("pairs")   // "a\x00b" -> {1}, stored both directions
	bucketRevoked = []byte("revoked") // rid -> {1}
)

type store struct {
	db *bolt.DB
}

func openStore(path string) (*store, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 0})
	if err != nil {
		return nil, err
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{bucketItems, bucketSeq, bucketPairs, bucketRevoked} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &store{db: db}, nil
}

func (s *store) close() error { return s.db.Close() }

func u64(v uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	return b[:]
}

// appendItem assigns the next monotonic storage cursor for rid (distinct from
// and never confused with the authenticated per-epoch seq inside the envelope),
// stores the opaque envelope verbatim alongside its append time, and returns the
// assigned cursor. The seq counter never rewinds on compaction.
func (s *store) appendItem(rid string, env []byte, atMillis int64) (uint64, error) {
	var cursor uint64
	err := s.db.Update(func(tx *bolt.Tx) error {
		seqB := tx.Bucket(bucketSeq)
		next := uint64(1)
		if v := seqB.Get([]byte(rid)); v != nil {
			next = binary.BigEndian.Uint64(v)
		}
		cursor = next
		if err := seqB.Put([]byte(rid), u64(next+1)); err != nil {
			return err
		}
		mb, err := tx.Bucket(bucketItems).CreateBucketIfNotExists([]byte(rid))
		if err != nil {
			return err
		}
		rec := make([]byte, 8+len(env))
		binary.BigEndian.PutUint64(rec[:8], uint64(atMillis))
		copy(rec[8:], env)
		return mb.Put(u64(cursor), rec)
	})
	return cursor, err
}

// mailboxItemJSONOverhead is a conservative upper bound on the JSON framing an
// Item costs beyond its base64 envelope: the object braces, the "cursor"/
// "envelope" keys, a 20-digit cursor, the string quotes, and the array comma.
// The real cost is ~46 bytes; 64 keeps the page-size estimate an over-estimate
// so the serialized reply can never exceed the byte budget (CR-4).
const mailboxItemJSONOverhead = 64

// readItemsPage returns at most maxItems items whose storage cursor is strictly
// greater than afterCursor, in ascending cursor order, bounded so that the
// items' estimated serialized size stays within byteBudget. It reports hasMore
// true iff at least one further item remains past the returned page.
//
// At least one item is always returned when the mailbox holds any item past the
// cursor (progress guarantee): a page is never empty-with-more, so a paginated
// drain cannot spin. The byte accounting uses the base64-encoded envelope length
// plus a conservative per-item JSON overhead, so a caller can size byteBudget to
// keep the whole JSON reply under MaxFrame without ever leaking plaintext (CR-4).
func (s *store) readItemsPage(rid string, afterCursor uint64, maxItems, byteBudget int) ([]Item, bool, error) {
	var out []Item
	hasMore := false
	err := s.db.View(func(tx *bolt.Tx) error {
		mb := tx.Bucket(bucketItems).Bucket([]byte(rid))
		if mb == nil {
			return nil
		}
		c := mb.Cursor()
		start := u64(afterCursor + 1)
		used := 0
		for k, v := c.Seek(start); k != nil; k, v = c.Next() {
			raw := v[8:]
			cost := base64.StdEncoding.EncodedLen(len(raw)) + mailboxItemJSONOverhead
			// Once the page holds at least one item, stop before either the item
			// count cap or the byte budget would be exceeded; the current item then
			// remains for a later page, so more items remain (hasMore).
			if len(out) > 0 && (len(out) >= maxItems || used+cost > byteBudget) {
				hasMore = true
				break
			}
			env := append([]byte(nil), raw...)
			out = append(out, Item{Cursor: binary.BigEndian.Uint64(k), Envelope: env})
			used += cost
		}
		return nil
	})
	return out, hasMore, err
}

// ackItems compacts away every item whose storage cursor is at or below
// throughCursor (the durable consumed watermark).
func (s *store) ackItems(rid string, throughCursor uint64) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		mb := tx.Bucket(bucketItems).Bucket([]byte(rid))
		if mb == nil {
			return nil
		}
		c := mb.Cursor()
		limit := u64(throughCursor)
		for k, _ := c.First(); k != nil && bytes.Compare(k, limit) <= 0; k, _ = c.Next() {
			if err := c.Delete(); err != nil {
				return err
			}
		}
		return nil
	})
}

// purgeOlderThan deletes every item (across all mailboxes) whose append time is
// at or before cutoffMillis — the retention cap, even for never-acked items.
func (s *store) purgeOlderThan(cutoffMillis int64) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		root := tx.Bucket(bucketItems)
		return root.ForEachBucket(func(rid []byte) error {
			mb := root.Bucket(rid)
			c := mb.Cursor()
			for k, v := c.First(); k != nil; k, v = c.Next() {
				at := int64(binary.BigEndian.Uint64(v[:8]))
				if at <= cutoffMillis {
					if err := c.Delete(); err != nil {
						return err
					}
				}
			}
			return nil
		})
	})
}

// purgeMailbox drops every item for rid (device de-authorization, R-REL.13).
func (s *store) purgeMailbox(rid string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		root := tx.Bucket(bucketItems)
		if root.Bucket([]byte(rid)) == nil {
			return nil
		}
		return root.DeleteBucket([]byte(rid))
	})
}

// mailboxDepth reports how many items rid's mailbox currently holds.
func (s *store) mailboxDepth(rid string) int {
	n := 0
	_ = s.db.View(func(tx *bolt.Tx) error {
		mb := tx.Bucket(bucketItems).Bucket([]byte(rid))
		if mb == nil {
			return nil
		}
		n = mb.Stats().KeyN
		return nil
	})
	return n
}

func pairKey(a, b string) []byte {
	k := make([]byte, 0, len(a)+1+len(b))
	k = append(k, a...)
	k = append(k, 0)
	k = append(k, b...)
	return k
}

// addPair records an undirected pairing (both directions) so an authorization
// check is a single point lookup either way.
func (s *store) addPair(a, b string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		pb := tx.Bucket(bucketPairs)
		if err := pb.Put(pairKey(a, b), []byte{1}); err != nil {
			return err
		}
		return pb.Put(pairKey(b, a), []byte{1})
	})
}

func (s *store) removePair(a, b string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		pb := tx.Bucket(bucketPairs)
		if err := pb.Delete(pairKey(a, b)); err != nil {
			return err
		}
		return pb.Delete(pairKey(b, a))
	})
}

func (s *store) isPaired(a, b string) bool {
	paired := false
	_ = s.db.View(func(tx *bolt.Tx) error {
		paired = tx.Bucket(bucketPairs).Get(pairKey(a, b)) != nil
		return nil
	})
	return paired
}

// pairedPeers enumerates every routing id paired with rid (used to fan a
// machine-went-silent push out to its paired devices).
func (s *store) pairedPeers(rid string) []string {
	var peers []string
	prefix := append([]byte(rid), 0)
	_ = s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(bucketPairs).Cursor()
		for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = c.Next() {
			peers = append(peers, string(k[len(prefix):]))
		}
		return nil
	})
	return peers
}

func (s *store) revoke(rid string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketRevoked).Put([]byte(rid), []byte{1})
	})
}

func (s *store) isRevoked(rid string) bool {
	revoked := false
	_ = s.db.View(func(tx *bolt.Tx) error {
		revoked = tx.Bucket(bucketRevoked).Get([]byte(rid)) != nil
		return nil
	})
	return revoked
}
