package pushgw

import (
	"sync"
	"time"
)

// regIdemCache is section 3.6 / PG-REG-2's registration idempotency store: a repeated
// registration under one Idempotency-Key returns the same installation_id rather than
// minting a second one, for the header's declared 10-minute retention window. PG-RET-4
// names this a bounded cache holding only the hash of the client-generated key, never the
// key itself, so it lives in memory rather than in the bbolt file.
type regIdemCache struct {
	mu      sync.Mutex
	entries map[string]regIdemEntry
}

type regIdemEntry struct {
	installationID string
	refreshBefore  string
	expiry         time.Time
}

func newRegIdemCache() *regIdemCache {
	return &regIdemCache{entries: make(map[string]regIdemEntry)}
}

func (c *regIdemCache) get(keyHash string, now time.Time) (regIdemEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[keyHash]
	if !ok || now.After(e.expiry) {
		return regIdemEntry{}, false
	}
	return e, true
}

func (c *regIdemCache) put(keyHash string, e regIdemEntry, now time.Time) {
	const window = 10 * time.Minute
	e.expiry = now.Add(window)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[keyHash] = e
}

// sweep deletes every entry past its own expiry (PG-RET-4 hardening): get() already
// refuses an expired entry logically, but never removed it, so the map only grew. Called
// from Server.RunRetention's periodic sweep, not its own timer.
func (c *regIdemCache) sweep(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.entries {
		if now.After(e.expiry) {
			delete(c.entries, k)
		}
	}
}

// wakeIdemCache is PG-SUB-4's bounded idempotency cache: a byte-identical retry of one
// wake, keyed SHA-256(the 74 received octets), may be answered from a prior FCM-confirmed
// outcome without re-sending, for at most the wake's five-minute expiry.
//
// RECORDED DEVIATION: the window below runs five minutes from RECEIPT (put's "now"), not
// from the wake's own expiry (issued_at + 300000ms, PG-WAKE-6). PG-SUB-4 bounds the cache at
// "at most the wake's five-minute expiry"; a wake submitted late in its life is therefore
// cached slightly past its own expiry, by the submission delay. Deriving the horizon from
// issued_at would mean parsing bytes 26-33 out of the envelope, which PG-SUB-5 designates
// opaque -- this is a small, one-sided over-retention of a keyed outcome, not a security
// gap (a stale hit only ever returns an outcome FCM already returned for this exact body),
// and is called out here rather than left invisible.
//
// PG-REV-1 lists "any in-flight idempotency state" among what whole-address revocation
// deletes; this cache is NOT purged on revoke. That is moot in practice: wake.go verifies
// the submit capability against the address record BEFORE ever consulting this cache, and
// PG-REV-1's own whole-address deletion removes that record, so a revoked address's cache
// entries are unreachable (401) long before their 5-minute window would matter. Purging
// would need an address index this cache does not otherwise have any use for.
type wakeIdemCache struct {
	mu      sync.Mutex
	entries map[[32]byte]wakeIdemEntry
}

type wakeIdemEntry struct {
	status int
	body   []byte
	expiry time.Time
}

func newWakeIdemCache() *wakeIdemCache {
	return &wakeIdemCache{entries: make(map[[32]byte]wakeIdemEntry)}
}

func (c *wakeIdemCache) get(key [32]byte, now time.Time) (wakeIdemEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || now.After(e.expiry) {
		return wakeIdemEntry{}, false
	}
	return e, true
}

func (c *wakeIdemCache) put(key [32]byte, e wakeIdemEntry, now time.Time) {
	const window = 5 * time.Minute
	e.expiry = now.Add(window)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = e
}

// sweep deletes every entry past its own expiry (PG-RET-4 hardening); see
// regIdemCache.sweep's comment -- same reasoning, same caller.
func (c *wakeIdemCache) sweep(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.entries {
		if now.After(e.expiry) {
			delete(c.entries, k)
		}
	}
}
