package relay

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"

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
	// bucketRevoked is "banned\x00banner" -> {1}: a ban is a fact about ONE RELATIONSHIP,
	// not about the banned identity (ADR-007 B49). It was rid -> banner, one row per banned
	// party, consulted by every dial that party ever made — and that shape is what made every
	// device_revoke MUTUAL ASSURED DESTRUCTION. Whoever fired first removed the other from
	// the relay entirely, only the banner could lift it (B24), and the banner was reachable
	// by nobody: lifting needs an authorize_device naming the victim, which since B38 demands
	// a signature under the VICTIM's own private relay-auth key that the banner never held.
	//
	// THE BAN ENFORCES NOTHING AND NEVER DID, which is the correction B26 diagnosed and this
	// key shape finally expresses: relay registration is OPEN, so a banned identity mints a
	// fresh keypair and returns — the ban stops only the identity an attacker trivially
	// changes and an honest owner never can. What severs access is the deleted pairs edge,
	// which is server-side and unforgeable. The ban's whole remaining job is to TELL the
	// banned party (PB-APP-10's re-pair prompt), and it was carrying that signalling job on a
	// global-authority mechanism. The mismatch was the vulnerability.
	//
	// A ROW WRITTEN BY THE OLD SHAPE IS DEAD RATHER THAN MISREAD, and that is the right
	// migration: the two key shapes cannot collide (a bare rid is 32 bytes and contains no
	// 0x00; a pair key is 65 bytes and contains exactly one), and nothing reads the bucket
	// except revokedBy, which asks for the pair shape. So an existing relay's stored bans
	// stop answering at the next start — which un-bricks every identity a global ban had
	// already destroyed, and costs a re-pair prompt that has by then served its purpose.
	bucketRevoked = []byte("revoked")
	// bucketTokens holds rid -> push token (PB-PUSH-6). It is a NEW durable device
	// identifier at rest in the untrusted relay's store, and it has its own named bucket
	// for exactly that reason: the token is not opaque to the relay (the relay must hand
	// it to the provider, which can also see it), so the honest mitigation is not
	// encryption but AUDITABILITY — an operator inspecting this store finds every device
	// identifier in one place instead of discovering one smuggled into the item log.
	bucketTokens = []byte("tokens")
	// bucketConsents is "pairer\x00device" -> the ceremony id of the ONE route consent
	// currently authorizing that pair, and bucketRetired is
	// "pairer\x00device\x00ceremony" -> {1} for every id that has stopped being it
	// (ADR-007 B47). Together they are what makes `swarm remote revoke` durable against a
	// grantee that still holds the signature: a retired id is refused forever, a fresh
	// ceremony is accepted exactly as before, and NOTHING is keyed on whether the pairing
	// was revoked — which is what leaves B22's ban lift and PB-STATE-10's recovery intact.
	bucketConsents = []byte("consents")
	bucketRetired  = []byte("retired_consents")
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
		for _, b := range [][]byte{bucketItems, bucketSeq, bucketPairs, bucketRevoked, bucketTokens, bucketConsents, bucketRetired} {
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

// retiredKey names one (pair, ceremony) tombstone. It is scoped to the PAIR, not global:
// two different machines running ceremonies that happened to share an id retire only
// their own, and a stranger cannot retire a pair it is not half of.
func retiredKey(pairer, device, ceremonyID string) []byte {
	k := pairKey(pairer, device)
	k = append(k, 0)
	return append(k, ceremonyID...)
}

// errConsentRetired is authorizePair's refusal for a consent whose ceremony has been
// superseded or revoked. It is mapped to ErrConsentRetired at the handler, which is where
// a wire code exists to carry it.
var errConsentRetired = errors.New("relay: route consent ceremony retired")

// errRetirementsFull is authorizePair's refusal once one pair has accumulated
// maxRetiredPerPair tombstones. It is mapped to quota_exceeded at the handler.
var errRetirementsFull = errors.New("relay: this pair has exhausted its retained ceremony retirements")

// maxRetiredPerPair bounds the tombstones bucketRetired keeps for ONE pair (ADR-007 B61).
//
// WHY A BOUND AT ALL: every supersession of a pair writes one row here and NOTHING in this
// package deletes from this bucket, so a single authenticated connection drove unbounded
// durable growth by re-pairing two keypairs it minted itself — no victim, no stolen key,
// no real pairing involved. bbolt never returns freed pages to the OS, so that growth is
// unreclaimable for the life of the file.
//
// WHY IT REFUSES INSTEAD OF EVICTING, which is the whole difficulty. ADR-007 B47 requires
// a retired ceremony to be refused FOREVER: that is the entire content of a durable revoke
// against a grantee who still holds the signed bytes. Any sweep — oldest-first, by
// timestamp, by key order — hands that grantee a laundering path: drive supersessions
// until its retirement falls out of the bucket, then replay the credential the revoke left
// behind and get its authority back. A bound and a forever-refusal are satisfiable
// together only by a mechanism that FORGETS NOTHING, so this one stops accepting new
// supersessions at the cap. Fenced by
// TestB61_ARetiredCeremonyIsStillRefusedAfterTheBoundIsReached.
//
// THE CAP IS LOAD-BEARING ONLY IN COMPANY WITH maxCeremonyIDLen, AND THE NEXT READER WILL
// NOT RECONSTRUCT THAT. This caps ROWS PER PAIR; it says nothing about what a row COSTS.
// Before the id was bounded a single row could carry a 32000-byte ceremony id, so 64 of
// them is ~2 MB for one pair and the attacker chose that number, not the relay — measured
// at ~84 KB of unreclaimable relay.db per call. Conversely the length bound alone caps
// only the row, leaving their number to the attacker. It is the PAIR of rules that makes
// the durable footprint of one authorize_device a constant the relay picked (~200 bytes,
// and nothing at all past the cap), which is what reduces this bucket's growth to the
// op-rate limit that already governs every other write. Removing either one re-opens the
// amplification.
//
// THE CAP DOES NOT APPLY TO revokeAndPurge, and that asymmetry is deliberate. A refusal
// there would be the very defect this closes, only reached by a different road: a pair at
// its cap would become unrevokable. Fail-closed means refusing to GRANT authority, never
// refusing to withdraw it.
//
// SO THE HONEST BOUND IS maxRetiredPerPair PLUS ONE ROW PER REVOKE, NOT A FLAT 64, and
// that is a consequence of the rule above rather than a leak in it. A revoke retires the
// live consent and DELETES it, so the pair's next authorize sees no live consent to
// supersede, takes no tombstone, and is accepted — measured: refusal at the 65th
// authorize with 64 rows, then revoke -> 65 rows, then a re-pair accepted, then one
// further row per revoke/re-pair cycle. Two things follow, and both are wanted. A capped
// pair is NOT bricked: `swarm remote revoke` is its recovery, which is PB-STATE-10's "fail
// closed must not mean bricked" answered by an act the owner already has. And a party that
// wants another row must spend a revoke to get it — destroying its own pairing for ~200
// bytes, at two metered ops per row. What this removes is the AMPLIFICATION, not every
// byte: durable growth is back under the op-rate limit that already governs every other
// write the relay accepts, instead of being a multiplier the caller chooses. Nothing
// weaker preserves B47, because the only way to make the count flat is to forget a
// retirement or to refuse a revoke.
//
// 64 IS A CEILING, NOT A BUDGET ANYONE SPENDS. A real pairing retires one ceremony per
// re-pairing and a device is re-paired a handful of times in its life; PB-STATE-10
// recovery is one such re-pairing.
const maxRetiredPerPair = 64

// countRetiredFor reports how many tombstones one pair holds, stopping as soon as the
// answer can only be "at the cap" — the caller's question is never "how many" beyond that.
func countRetiredFor(rb *bolt.Bucket, pairer, device string, stopAt int) int {
	prefix := append(pairKey(pairer, device), 0)
	n := 0
	c := rb.Cursor()
	for k, _ := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix) && n < stopAt; k, _ = c.Next() {
		n++
	}
	return n
}

// authorizePair records BOTH directed authorizations of one consented pairing —
// pairer grants device authority over the pairer's route, and device grants
// pairer authority over the device's — AND lifts any ban standing against device,
// in ONE transaction, so a crash between them can never leave a routing id
// authorized-but-banned (granted on paper and refused at the handshake).
//
// THE EDGES ARE STORED DIRECTED (ADR-007 B27) AND BOTH ARE EARNED, WHICH IS WHAT
// B25 WAS MISSING AND B38 MADE MANDATORY. Its caller, handleAuthorizeDevice,
// admits nothing here until the NAMED DEVICE's own relay-auth key has signed
// ConsentMessage(pairer) — so the device->pairer edge carries the device's proof,
// and the pairer->device edge is the caller's grant over its OWN route, which is
// the one statement a caller has always been entitled to make about itself.
//
// It used to be written both ways off ONE side's say-so with no proof at all, so
// `authorize_device` — behind requireAuth alone, over open registration —
// manufactured a mutual pairing out of a stranger's word, and every verb meaning
// "act on somebody else's route" gated on exactly that. A keypair minted seconds
// ago could name the machine and thereby acquire it: append to its mailbox, push
// to it, revoke it. The direction is preserved rather than collapsed because
// mayActOn reads exactly one of the two, and reading the caller's own edge as
// authority is the bug itself.
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
// THE CLEAR IS NOW A ONE-KEY DELETE RATHER THAN A COMPARE, and that is the same rule, not a
// weaker one: bucketRevoked is keyed by the PAIR (ADR-007 B49), so the row this deletes is by
// construction the ban THIS pairer placed on THIS device. No other party's ban is reachable
// from here, which is what the value comparison used to enforce by hand.
//
// THE BANNER-SCOPED CLEAR SURVIVES THE DIRECTED EDGE, and it has to be checked
// rather than assumed, because B24's own note said this policy made the mirror
// hole permanent: the ban an attacker placed on the machine stuck, since only its
// placer could lift it and the placer was the attacker. That escalation is gone
// now — not by weakening this rule, but because a stranger cannot reach
// device_revoke at all without the target's signed consent, so no ban of that
// shape can be placed. B24 and this line are unchanged and remain right: no weaker
// rule stops a revoked device un-banning itself through a throwaway identity,
// since a throwaway is by construction not the banned party.
//
// THE CEREMONY ID IS WHAT MAKES A REVOKE DURABLE (ADR-007 B47), and it is deliberately
// NOT a check on whether this pair was revoked. Restoring access and lifting the ban are
// the same act, and PB-STATE-10 requires that act, so a rule shaped "refuse a consent for
// a revoked pairing" would test green and re-brick the recovery flow. The rule here is
// about the CREDENTIAL instead:
//
//   - a retired ceremony id is refused forever. Replaying the bytes a revoke left behind
//     therefore restores nothing, which is the whole of B47.
//   - recording a new id retires the previous one. Retiring only at revoke would leave
//     every earlier ceremony's consent live, so a holder of two would spend one and keep
//     the spare.
//   - re-presenting the LIVE id is idempotent. cmd/swarm-remote's deliverEpochGrant
//     re-presents the same stored bytes on every gateway connect and its failure is fatal,
//     so a credential that were single-USE rather than single-CEREMONY would brick the
//     machine on its second boot.
//
// A re-pairing is a new ceremony, hence a new id, hence accepted — ban lift and all.
func (s *store) authorizePair(pairer, device, ceremonyID string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		cb := tx.Bucket(bucketConsents)
		rb2 := tx.Bucket(bucketRetired)
		key := pairKey(pairer, device)
		if rb2.Get(retiredKey(pairer, device, ceremonyID)) != nil {
			return errConsentRetired
		}
		if live := cb.Get(key); live != nil && !bytes.Equal(live, []byte(ceremonyID)) {
			// ADR-007 B61: the supersession is what GROWS the bucket, so the cap is charged
			// here and refuses the new ceremony rather than dropping an old retirement. The
			// retired check above has already run, so a tombstone this cap keeps is still
			// refusing its own credential — the bound never costs a retirement (B47).
			if countRetiredFor(rb2, pairer, device, maxRetiredPerPair) >= maxRetiredPerPair {
				return errRetirementsFull
			}
			if err := rb2.Put(retiredKey(pairer, device, string(live)), []byte{1}); err != nil {
				return err
			}
		}
		if err := cb.Put(key, []byte(ceremonyID)); err != nil {
			return err
		}

		pb := tx.Bucket(bucketPairs)
		if err := pb.Put(pairKey(pairer, device), []byte{1}); err != nil {
			return err
		}
		if err := pb.Put(pairKey(device, pairer), []byte{1}); err != nil {
			return err
		}
		return tx.Bucket(bucketRevoked).Delete(pairKey(device, pairer))
	})
}

// isPaired reports a MUTUAL pairing: each party has authorized the other.
//
// IT IS DELIBERATELY NOT THE GATE FOR ACTING ON A ROUTE. mayActOn is the
// authority decision and it reads ONE direction, because authority is asymmetric:
// "the phone may act on the machine" and "the machine may act on the phone" are
// different facts even when both hold. This is the fact, and its only consumers
// are the fences that pin it (b25_authority_test.go, b38_consent_test.go).
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
// revoke target's route? THE RULE, WHICH IS ADR-007 B27 WITH ITS EXCEPTION
// REMOVED (B38): the target must have authorized the source. There is no second
// clause.
//
// Authentication proves identity; authority is the TARGET's own authorize, which
// only the target can put here — either by calling authorize_device itself, or by
// signing ConsentMessage(source) during the SAS ceremony and letting source carry
// that proof to handleAuthorizeDevice. A stranger naming the machine writes the
// OTHER direction (its own grant over its own route) and gets nothing from it.
//
// THE DELETED CLAUSE WAS "OR THE TARGET HAS AUTHORIZED NOBODY AT ALL", and it was
// load-bearing until the consent signature existed. deliverEpochGrant
// (cmd/swarm-remote) authorizes the phone and IMMEDIATELY appends the sealed epoch
// grant — the append that delivers the ContentKey, i.e. what makes a pairing
// usable — and its failure is fatal, with the phone not yet connected and so
// unable to have consented at the relay. That circularity is what falsified ADR-007
// B25's mutual-pairing direction, measured at
// TestDeliverEpochGrant_AuthorizesAndAppendsBootstrap.
//
// It is not circular any more, and that is the whole of the fix: the phone's
// consent is obtained where it actually happens — in the pairing ceremony, over a
// channel the relay cannot see — and CARRIED to the relay by the machine. The
// bootstrap append is authorized by the phone, not by the phone's silence. Pinned
// from both sides by TestB38_ConsentedAuthorizeBootstraps (the grant still lands
// with the phone offline) and TestB38_ObserverOfThePubkeyCannotBanANeverPairedMachine
// (a never-connected machine cannot be banned by a party holding its pubkey).
//
// THE CLAUSE IS UNREACHABLE RATHER THAN MERELY UNNEEDED, WHICH IS WHY IT IS DELETED
// AND NOT KEPT AS BELT-AND-BRACES — and it is stated because it is the one part of
// this change no fence can catch. Under the consent rule authorizePair writes both
// edges or neither, so `granted(source, target)` can never hold while
// `granted(target, source)` does not: the second clause's own precondition is
// unsatisfiable. Restoring it changes no observable behaviour TODAY. It is removed
// so that it cannot come back to life silently if anyone ever makes consent
// optional again, which is exactly how it became a hole the first time.
func (s *store) mayActOn(source, target string) bool {
	granted := false
	_ = s.db.View(func(tx *bolt.Tx) error {
		granted = tx.Bucket(bucketPairs).Get(pairKey(target, source)) != nil
		return nil
	})
	return granted
}

// isPairer reports whether caller is the PAIRER of device — the party that carried
// device's signed route consent to the relay and thereby created this pairing.
//
// IT IS THE ONLY ASYMMETRIC DURABLE FACT ABOUT A PAIRING, which is why device_revoke asks
// its authority of this and not of bucketPairs (ADR-007 B60). authorizePair writes pairs in
// BOTH directions and revokeAndPurge deletes both, so nothing there tells the two parties
// apart — mayActOn answers true for a paired phone against its own machine. bucketConsents
// is written on ONE key, consents[pairer|device] (authorizePair), and deleted on that one
// key (revokeAndPurge); it has existed only since ADR-007 B52, which is why B50 concluded —
// correctly at the time — that no orientation-aware remedy was available.
//
// Presence of the key is the whole question: the value is a ceremony id and
// handleAuthorizeDevice refuses an empty one, so a row here is never a zero-length value
// that a caller could confuse with absence.
func (s *store) isPairer(caller, device string) bool {
	held := false
	_ = s.db.View(func(tx *bolt.Tx) error {
		held = tx.Bucket(bucketConsents).Get(pairKey(caller, device)) != nil
		return nil
	})
	return held
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

// revokeAndPurge atomically unpairs pairer<->rid, records the ban pairer places on
// rid, drops the frames PAIRER queued for rid, and — only if this severed rid's last
// relationship — drops rid's push token, in ONE transaction (ME-1), so a crash/read
// mid-revoke can never observe rid as still-paired, not-yet-revoked, still-pushable,
// or holding a pre-revoke backlog from this sender.
//
// Recording the banner is ADR-007 B24: it is the only thing that lets authorizePair
// tell the owner's machine lifting its own ban from a stranger lifting it. It is also
// why there is no second, banner-less writer of this bucket — one would place a ban
// nobody could ever lift.
//
// EVERYTHING THIS DESTROYS IS SCOPED TO THE PAIR, AND THAT IS ADR-007 B27's OBJECTION
// ANSWERED RATHER THAN ARGUED AROUND. B27 falsified pair-scoping the ban by pointing
// out that this function ALSO deleted the target's whole mailbox and its push token,
// both keyed per TARGET — so scoping the ban left an attacker able to destroy every
// undelivered frame and silence the handset, repeatedly, on demand. The anonymous reach
// that made that critical is gone (B38: nothing reaches device_revoke without the
// target's own signed consent), but the objection is structural and is met on its own
// terms: the stored record carries its SENDER (recordV1, what mailboxDepthFrom reads),
// so the purge deletes exactly the revoker's frames and leaves every other sender's
// alone.
//
// THE TOKEN IS THE ONE THING THAT CANNOT REPAIR ITSELF, which is why it is conditioned
// rather than simply left. Backgrounding DISCONNECTS the phone (ADR-007 B16), so the
// push token is what a backgrounded handset is woken BY: drop it and there is no wake,
// therefore no reconnect, therefore no re-registration to restore it — the silence is
// permanent, not a nuisance the next connect repairs. So it is forgotten only when no
// relationship remains that could ever wake this device, which is exactly when a
// retained token would be an unreachable provider-visible identifier for a device its
// owner disowned (PB-PUSH-6). One party to one relationship cannot silence a handset
// another relationship still depends on.
//
// It reports whether the token was forgotten, because the relay holds a WRITE-THROUGH
// CACHE of the token map and the two halves must not disagree: a cache cleared while the
// durable row stands resurrects the token on the next restart, and a durable row deleted
// while the cache stands keeps pushing until one. The condition is decided once, inside
// the transaction that decides everything else about this revoke.
func (s *store) revokeAndPurge(pairer, rid string) (bool, error) {
	forgotToken := false
	err := s.db.Update(func(tx *bolt.Tx) error {
		pb := tx.Bucket(bucketPairs)
		_ = pb.Delete(pairKey(pairer, rid))
		_ = pb.Delete(pairKey(rid, pairer))
		// ADR-007 B47, in the SAME transaction as the edges and the ban: retire the
		// ceremony that authorized this pair. The signature is a durable artifact the
		// grantee still holds, so without this the revoke is undone by re-presenting bytes
		// the machine already has on disk — and the phone is never asked. A later pairing
		// signs a new consent over a new ceremony and is unaffected.
		cb := tx.Bucket(bucketConsents)
		key := pairKey(pairer, rid)
		if live := cb.Get(key); live != nil {
			if err := tx.Bucket(bucketRetired).Put(retiredKey(pairer, rid, string(live)), []byte{1}); err != nil {
				return err
			}
			if err := cb.Delete(key); err != nil {
				return err
			}
		}
		// The ban is keyed by the PAIR (ADR-007 B49), and the retirement above is keyed by
		// the same pair. That is not a coincidence worth leaving implicit: the retirement is
		// what stops a replayed consent lifting this ban, and it is exactly as durable as
		// the ban it protects — a relay that loses one loses the other in the same instant,
		// so no surviving revocation is ever left for a replay to undo.
		if err := tx.Bucket(bucketRevoked).Put(pairKey(rid, pairer), []byte{1}); err != nil {
			return err
		}
		// In the SAME transaction as the revocation: a token purged separately could
		// survive a crash between the two writes and be resurrected by the next restart,
		// resuming pushes to a handset whose access the owner withdrew.
		if !grantsAnyone(pb, rid) {
			if err := tx.Bucket(bucketTokens).Delete([]byte(rid)); err != nil {
				return err
			}
			forgotToken = true
		}
		mb := tx.Bucket(bucketItems).Bucket([]byte(rid))
		if mb == nil {
			return nil
		}
		src := []byte(pairer)
		c := mb.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			if isRecord(v) && bytes.Equal(v[9:recordHead], src) {
				// c.Delete() leaves the cursor ON the deleted slot, so the loop's own
				// c.Next() is what advances: an extra advance here steps over the record
				// that shifted down and leaves it drainable. Fenced by
				// TestB49_ARevokeDoesNotDestroyAnotherSendersQueuedFrames, whose revoker
				// queues two ADJACENT frames for exactly this reason.
				if err := c.Delete(); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return forgotToken, err
}

// grantsAnyone reports whether rid still has an outbound grant — some party it has
// authorized to act on its route. It is the "is any relationship left" question the
// revoke asks before forgetting a device's push token, and it reads the same directed
// edge pairedPeers enumerates.
func grantsAnyone(pb *bolt.Bucket, rid string) bool {
	prefix := append([]byte(rid), 0)
	k, _ := pb.Cursor().Seek(prefix)
	return k != nil && bytes.HasPrefix(k, prefix)
}

// revokedBy reports whether banner has revoked banned. It is a PAIR lookup, and the
// relay's only consumer is the handshake verdict a dialer asks for by naming its peer
// (ADR-007 B49) — never a standing refusal of the banned identity itself.
func (s *store) revokedBy(banned, banner string) bool {
	revoked := false
	_ = s.db.View(func(tx *bolt.Tx) error {
		revoked = tx.Bucket(bucketRevoked).Get(pairKey(banned, banner)) != nil
		return nil
	})
	return revoked
}
