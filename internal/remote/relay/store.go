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
	bucketItems = []byte("items") // nested: rid -> (cursor8 -> record)
	bucketSeq   = []byte("seq")   // rid -> next storage cursor (8 bytes)
	bucketPairs = []byte("pairs") // "a\x00b" -> {1}, stored both directions
	// bucketRevoked is rid -> the routing id that BANNED it (ADR-007 B24). The value is
	// load-bearing, not a marker: it is what authorizePair matches the pairer against, and
	// it is the whole of "the owner's machine lifts the ban it placed".
	bucketRevoked = []byte("revoked")
	// bucketTokens holds rid -> push token (PB-PUSH-6). It is a NEW durable device
	// identifier at rest in the untrusted relay's store, and it has its own named bucket
	// for exactly that reason: the token is not opaque to the relay (the relay must hand
	// it to the provider, which can also see it), so the honest mitigation is not
	// encryption but AUDITABILITY — an operator inspecting this store finds every device
	// identifier in one place instead of discovering one smuggled into the item log.
	bucketTokens = []byte("tokens")
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
		for _, b := range [][]byte{bucketItems, bucketSeq, bucketPairs, bucketRevoked, bucketTokens} {
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

// authorizePair records an undirected pairing (both directions) so an
// authorization check is a single point lookup either way, AND lifts any ban
// standing against device — in ONE transaction, so a crash between the two can
// never leave a routing id paired-but-banned (authorized on paper and refused at
// the handshake).
//
// CLEARING THE BAN IS ADR-007 B22 AND IT IS NOT A WEAKENING. revokeAndPurge is
// the only writer of bucketRevoked and nothing else ever cleared it, while the
// phone's relay-auth key is minted once per install — so a recovered handset
// returned on the same routing id and was locked out of the relay for good.
// Revoke and re-pair were mutually exclusive, and PB-STATE-10 ("fail closed must
// not mean bricked") was unsatisfiable.
//
// THE BAN IS LIFTED ONLY BY ITS PLACER, WHICH IS ADR-007 B24 CORRECTING B22.
// B22 argued the clear was safe because the verb is reached only from an
// AUTHENTICATED connection and a revoked routing id cannot authenticate. The
// second half is true and the conclusion does not follow: relay auth is OPEN
// REGISTRATION and handleAuthorizeDevice has no ownership or role check, so a
// revoked device could mint a throwaway identity, authenticate as it, and
// authorize its OWN revoked routing id back in. Authentication proves identity,
// not authority.
//
// So the ban carries its banner and only that banner clears it. That keeps B22's
// semantics exactly — the owner's machine lifts the ban it placed, over the same
// relay identity `swarm remote revoke` used to place it — and makes its argument
// true rather than aspirational. Fenced in BOTH directions by
// TestRelay_ABanIsLiftedOnlyByTheIdentityThatPlacedIt and
// TestRelay_TheBanningMachineCanLiftItsOwnBan.
//
// WHAT THIS POLICY MAKES WORSE, recorded rather than left to be found a third
// time: handleDeviceRevoke has the SAME missing check — any authenticated
// identity may authorize_device itself into a pairing with any routing id and
// then revoke it, including the MACHINE's. That hole pre-dates this work and is
// out of scope here (ADR-007 B24 records it), but the fix above changes its
// severity rather than leaving it alone. Before, a ban an attacker placed on the
// machine was cleared by the phone's very next reconnect — mobile/relay.go
// onConnected authorizes the machine on every authenticated connect, and any
// authorize cleared any ban. Now only the banner clears, so an attacker-placed
// ban on the machine STICKS: a transient denial of service became a durable
// lockout of that routing id. The rule is still the right one — no weaker rule
// stops a revoked device un-banning itself through a throwaway identity, since a
// throwaway is by construction not the banned party — so what this note buys is
// that the mirror hole is now the load-bearing one and should be prioritised as
// such.
func (s *store) authorizePair(pairer, device string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		pb := tx.Bucket(bucketPairs)
		if err := pb.Put(pairKey(pairer, device), []byte{1}); err != nil {
			return err
		}
		if err := pb.Put(pairKey(device, pairer), []byte{1}); err != nil {
			return err
		}
		rb := tx.Bucket(bucketRevoked)
		if !bytes.Equal(rb.Get([]byte(device)), []byte(pairer)) {
			return nil // not banned, or banned by somebody else: the pairing still stands.
		}
		return rb.Delete([]byte(device))
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

// putToken records rid's push token durably (PB-PUSH-6). Registration is
// last-write-wins, matching the in-memory map it mirrors: one token per routing
// id, and a routing id is one paired device, so this is the single-device v1
// limitation expressed as storage. It is also what makes PB-PUSH-9's
// re-registration on every reconnect converge — a rotated token REPLACES the
// stale one instead of sitting beside it and getting every wake delivered twice.
func (s *store) putToken(rid, token string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketTokens).Put([]byte(rid), []byte(token))
	})
}

// deleteToken durably forgets rid's push token. Deletion MUST be as durable as
// registration: a relay that persisted only the register would resurrect, on the
// next restart, a token the device revoked or the owner killed — resuming wakes
// to a handset that was deliberately silenced, and handing the provider a token
// that should be gone.
func (s *store) deleteToken(rid string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketTokens).Delete([]byte(rid))
	})
}

// loadTokens reads the whole rid -> token map, which the Server hydrates its
// in-memory cache from at construction so a restart resumes with the tokens it
// had. Backgrounding DISCONNECTS the phone (ADR-007 B16), so a token the relay
// forgot cannot be re-registered until the user next opens the app — which is
// exactly what the lost push was meant to prompt.
func (s *store) loadTokens() (map[string]string, error) {
	out := map[string]string{}
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketTokens).ForEach(func(k, v []byte) error {
			out[string(k)] = string(v)
			return nil
		})
	})
	return out, err
}

// revokeAndPurge atomically unpairs pairer<->rid, marks rid revoked BY pairer,
// drops rid's push token, and drops rid's mailbox — in ONE transaction (ME-1),
// so a crash/read mid-revoke can never observe rid as still-paired,
// not-yet-revoked, still-pushable, or holding a pre-revoke backlog.
//
// Recording the banner is ADR-007 B24: it is the only thing that lets
// authorizePair tell the owner's machine lifting its own ban from a stranger
// lifting it. It is also why there is no second, banner-less writer of this
// bucket — one would place a ban nobody could ever lift.
func (s *store) revokeAndPurge(pairer, rid string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		pb := tx.Bucket(bucketPairs)
		_ = pb.Delete(pairKey(pairer, rid))
		_ = pb.Delete(pairKey(rid, pairer))
		if err := tx.Bucket(bucketRevoked).Put([]byte(rid), []byte(pairer)); err != nil {
			return err
		}
		// In the SAME transaction as the revocation: a token purged separately could
		// survive a crash between the two writes and be resurrected by the next restart,
		// resuming pushes to a handset whose access the owner withdrew.
		if err := tx.Bucket(bucketTokens).Delete([]byte(rid)); err != nil {
			return err
		}
		root := tx.Bucket(bucketItems)
		if root.Bucket([]byte(rid)) != nil {
			return root.DeleteBucket([]byte(rid))
		}
		return nil
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
