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
	// bucketPairs is "authorizer\x00authorized" -> {1}, DIRECTED (ADR-007 B27): one
	// key per authorize_device, naming who granted whom. The direction is the
	// authority check itself (see authorizePair and mayActOn), not a storage detail.
	bucketPairs = []byte("pairs")
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

// isRecord reports whether v is an item record this store wrote (see recordV1).
func isRecord(v []byte) bool { return len(v) >= recordHead && v[0] == recordV1 }

func u64(v uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	return b[:]
}

// The stored item record is [1 version][8 append time][32 sender rid][envelope].
//
// ridLen is fixed (a RoutingID is 16 bytes hex-encoded), which is what lets the
// record carry its sender without a length prefix. recordV1 is what makes the
// SENDER FIELD SAFE TO ADD: the previous layout had no version byte and began
// with a millisecond timestamp, whose leading byte is 0x00 for any date this
// millennium, so a record written by an older relay can never be mistaken for one
// of these and have 32 bytes of somebody's ciphertext read off as a routing id.
// Such a record is skipped rather than served — mis-serving it would hand the
// phone a truncated envelope that no key can open and that never drains.
const (
	ridLen     = 2 * 16
	recordV1   = 0x01
	recordHead = 1 + 8 + ridLen
)

// appendItem assigns the next monotonic storage cursor for rid (distinct from
// and never confused with the authenticated per-epoch seq inside the envelope),
// stores the opaque envelope verbatim alongside its append time AND ITS SENDER,
// and returns the assigned cursor. The seq counter never rewinds on compaction.
//
// THE SENDER IS STORED BECAUSE THE DEPTH CAP HAS TO BE CHARGED TO SOMEBODY.
// Charged to the mailbox alone — as it was — the cap is a shared resource with no
// owner, so whoever fills it first evicts everybody else from a mailbox that is
// not theirs. It is not read back out to any caller and never reaches the wire:
// the relay already knows it (it authenticated the sender), so this leaks nothing
// it did not have, and it is a routing id rather than a key (R-REL.11).
func (s *store) appendItem(rid, source string, env []byte, atMillis int64) (uint64, error) {
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
		rec := make([]byte, recordHead+len(env))
		rec[0] = recordV1
		binary.BigEndian.PutUint64(rec[1:9], uint64(atMillis))
		copy(rec[9:recordHead], source)
		copy(rec[recordHead:], env)
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
			if !isRecord(v) {
				continue // not a record this store wrote; never parsed as an envelope.
			}
			raw := v[recordHead:]
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
				if !isRecord(v) {
					continue
				}
				at := int64(binary.BigEndian.Uint64(v[1:9]))
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

// mailboxDepth reports how many items rid's mailbox currently holds, from every
// sender (ops and revocation visibility: a purge has to drop to zero).
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

// mailboxDepthFrom reports how many items SOURCE currently holds in rid's
// mailbox — the quantity the depth cap is charged against, so that one sender's
// backlog can never refuse another's append. The scan is bounded by the cap it
// serves.
func (s *store) mailboxDepthFrom(rid, source string) int {
	n := 0
	src := []byte(source)
	_ = s.db.View(func(tx *bolt.Tx) error {
		mb := tx.Bucket(bucketItems).Bucket([]byte(rid))
		if mb == nil {
			return nil
		}
		return mb.ForEach(func(_, v []byte) error {
			if isRecord(v) && bytes.Equal(v[9:recordHead], src) {
				n++
			}
			return nil
		})
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

// authorizePair records ONE DIRECTED authorization — pairer authorized device —
// AND lifts any ban standing against device, in ONE transaction, so a crash
// between the two can never leave a routing id authorized-but-banned (granted on
// paper and refused at the handshake).
//
// THE EDGE IS DIRECTED (ADR-007 B27), AND THAT IS ADR-007 B25's MISSING CHECK.
// It used to be written BOTH ways, so `authorize_device` — behind requireAuth
// alone, over open registration — manufactured a mutual pairing out of one side's
// say-so, and every verb meaning "act on somebody else's route" gated on exactly
// that. A keypair minted seconds ago could name the machine and thereby acquire
// it: append to its mailbox, push to it, revoke it. Stored directed, the same
// call records only what it actually is — the CALLER's intent. Consent to be
// acted upon is the OTHER direction, and only the named party can write it. See
// mayActOn for what that buys and for the one exception it cannot avoid.
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
// THE BANNER-SCOPED CLEAR SURVIVES THE DIRECTED EDGE, and it has to be checked
// rather than assumed, because B24's own note said this policy made the mirror
// hole permanent: the ban an attacker placed on the machine stuck, since only its
// placer could lift it and the placer was the attacker. That escalation is gone
// now — not by weakening this rule, but because mayActOn no longer lets a
// stranger reach device_revoke at all, so no ban of that shape can be placed.
// B24 and this line are unchanged and remain right: no weaker rule stops a
// revoked device un-banning itself through a throwaway identity, since a
// throwaway is by construction not the banned party.
func (s *store) authorizePair(pairer, device string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		pb := tx.Bucket(bucketPairs)
		if err := pb.Put(pairKey(pairer, device), []byte{1}); err != nil {
			return err
		}
		rb := tx.Bucket(bucketRevoked)
		if !bytes.Equal(rb.Get([]byte(device)), []byte(pairer)) {
			return nil // not banned, or banned by somebody else: the grant still stands.
		}
		return rb.Delete([]byte(device))
	})
}

// isPaired reports a MUTUAL pairing: each party has authorized the other. It is
// the honest reading of "these two are paired" now that the edge is directed.
//
// IT IS DELIBERATELY NOT THE GATE FOR ACTING ON A ROUTE, and reinstating it as
// one would put ADR-007 B25 back: a first pairing is genuine before the second
// leg exists, so a mutual gate refuses the epoch grant that makes the pairing
// usable. mayActOn is the authority decision; this is the fact, and its only
// consumer is the fence that pins it (b25_authority_test.go).
func (s *store) isPaired(a, b string) bool {
	paired := false
	_ = s.db.View(func(tx *bolt.Tx) error {
		pb := tx.Bucket(bucketPairs)
		paired = pb.Get(pairKey(a, b)) != nil && pb.Get(pairKey(b, a)) != nil
		return nil
	})
	return paired
}

// mayActOn is the relay's authority decision — may source append to, push to, or
// revoke target's route? THE RULE, WHICH IS ADR-007 B27: the target must have
// authorized the source, or have authorized nobody at all.
//
// The first clause is the property ADR-007 B25 found missing, and it is the only
// clause that matters once a device is paired. Authentication proves identity;
// authority is the TARGET's own authorize, which only the target can write since
// authorizePair stores the edge directed. A stranger naming the machine writes
// the other direction and gets nothing from it.
//
// THE SECOND CLAUSE IS A BOOTSTRAP EXCEPTION AND IT IS LOAD-BEARING: without it
// there is no first pairing at all. deliverEpochGrant (cmd/swarm-remote)
// authorizes the phone and IMMEDIATELY appends the sealed epoch grant — the
// append that delivers the ContentKey, i.e. what makes a pairing usable — and its
// failure is fatal. The phone need not have connected yet, so it cannot have
// consented at the relay yet, and it cannot consent before it holds the grant
// either. That circularity is what falsified the mutual-pairing direction ADR-007
// B25 recorded, measured at TestDeliverEpochGrant_AuthorizesAndAppendsBootstrap.
//
// The relay cannot witness the QR ceremony that DID convey the phone's consent,
// so at the relay "machine authorizes phone, then appends to phone" and "stranger
// authorizes machine, then appends to machine" are the same shape, and no
// predicate over the caller distinguishes them. The asymmetry that does survive
// is not about the caller: the stranger's target is an ESTABLISHED identity that
// has already authorized somebody, and a bootstrapping target has authorized
// nobody.
//
// THE RESIDUAL, accepted in ADR-007 B27 rather than smoothed over: this is trust
// on first use, and the entry records the complete fix it does not take. A
// party that knows a NEVER-PAIRED identity's relay-auth pubkey can act on it
// until it authorizes someone. That pubkey is disclosed at the relay handshake
// and over the SAS-authenticated pairing channel, so the window is reachable in
// practice by the RELAY OPERATOR, to whom the threat model already concedes
// availability — not by an anonymous party who can merely open a socket to the
// relay, which is the line B25 drew and the one this fix has to hold.
func (s *store) mayActOn(source, target string) bool {
	ok := false
	_ = s.db.View(func(tx *bolt.Tx) error {
		pb := tx.Bucket(bucketPairs)
		if pb.Get(pairKey(target, source)) != nil {
			ok = true
			return nil
		}
		if pb.Get(pairKey(source, target)) == nil {
			return nil // not even the caller's own intent: nothing to bootstrap.
		}
		ok = !hasActedAsAuthority(tx, target)
		return nil
	})
	return ok
}

// hasActedAsAuthority reports whether rid has ever authorized or banned another
// party — whether it is past its first use, and so whether mayActOn's bootstrap
// exception is closed for it.
//
// A BAN COUNTS, and leaving it out would be a hole rather than an omission:
// revokeAndPurge DELETES the authorization it severs, so counting live grants
// alone would RE-OPEN the bootstrap window of a machine that revoked its only
// device — handing a stranger the same permanent lockout ADR-007 B25 describes,
// one revoke later.
func hasActedAsAuthority(tx *bolt.Tx, rid string) bool {
	prefix := append([]byte(rid), 0)
	if k, _ := tx.Bucket(bucketPairs).Cursor().Seek(prefix); bytes.HasPrefix(k, prefix) {
		return true
	}
	banned := false
	_ = tx.Bucket(bucketRevoked).ForEach(func(_, banner []byte) error {
		if bytes.Equal(banner, []byte(rid)) {
			banned = true
		}
		return nil
	})
	return banned
}

// pairedPeers enumerates the routing ids rid has AUTHORIZED (used to fan a
// machine-went-silent push out to a machine's devices). Now that the edge is
// directed this is rid's own grants and nothing else — a stranger that authorized
// rid writes the opposite direction and is never woken by rid's silence.
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
