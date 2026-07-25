package dev.swarm.phone.keys

/**
 * The at-rest store and the custody lifecycle.
 *
 * PB-SEC-1 (at rest), PB-KEY-2 (the tiers in use), PB-KEY-7 (lock purges live memory).
 */

/**
 * PB-SEC-1: everything this holds is sealed under the Keystore KEK of its tier. `rawBytes` is
 * what a `run-as`/backup extraction would see, and the test asserts it is not the key.
 *
 * NOTHING IS MEMOIZED. `open` goes back to the KEK every time, deliberately: an auth-gated
 * key re-checks authorisation on every use, and a store that unwrapped once and cached would
 * keep serving the content key after the screen locked (PB-KEY-7) while every restart-based
 * test still passed. The cost is a Keystore round trip per read, which is the point.
 */
class SealedStore(private val kek: KekProvider) {

    private class Entry(val tier: KeyTier, val blob: ByteArray)

    private val entries = LinkedHashMap<String, Entry>()

    /**
     * Seals [plaintext] under [tier]'s KEK and keeps only the blob. The caller's array is
     * neither retained nor cleared -- it belongs to the caller, and clearing it here would
     * make `put` surprising for a caller that still needs the value.
     */
    fun put(name: String, tier: KeyTier, plaintext: ByteArray) {
        entries[name] = Entry(tier, kek.wrap(tier, plaintext))
    }

    /** @throws KeyCustodyException when the tier's KEK will not authorize the unwrap. */
    fun open(name: String): ByteArray {
        val entry = entry(name)
        return kek.unwrap(entry.tier, entry.blob)
    }

    fun tierOf(name: String): KeyTier = entry(name).tier

    /** The persisted bytes, exactly as they sit on disk. */
    fun rawBytes(name: String): ByteArray = entry(name).blob.copyOf()

    fun names(): Set<String> = entries.keys.toSet()

    fun remove(name: String) {
        entries.remove(name)
    }

    private fun entry(name: String): Entry =
        entries[name] ?: throw KeyCustodyException.KeystoreKeyMissing(name)
}

/**
 * Owns the one inbound crossing over its whole lifetime: unwrap under the tier KEK, install
 * into the Go core, zeroize, and purge on lock.
 *
 * IT HOLDS NO KEY MATERIAL IN A FIELD. Only two booleans recording what the Go core currently
 * has installed, which is why `LockPurgeTest.a_purge_leaves_no_key_material_live_in_the_
 * custody_layer` can sweep this object's fields for a non-zero ByteArray and find none. The
 * shortcut that would break it is caching the unwrapped key so the user is not re-prompted --
 * which is also the shortcut that makes the whole purge cosmetic.
 */
class KeyCustodySession(
    private val store: SealedStore,
    private val core: CoreKeyCustody,
) {

    private var wakeInstalled = false
    private var contentInstalled = false
    private var lastInvalidation: InvalidationEvent? = null

    /**
     * Unwrap the tier's epoch key and install it. The unwrapped array is zeroized before this
     * returns -- the facade package doc makes that the caller's obligation, and here the
     * caller is this class.
     *
     * The zeroize is in a `finally` around the install, not after it: an install that throws
     * would otherwise leave the plaintext live on the Java heap for the GC to get to
     * eventually, which is the same defect with a tidier diff.
     *
     * @throws KeyCustodyException.UserAuthenticationRequired for CONTENT while locked.
     */
    fun installTier(tier: KeyTier) {
        val key = store.open(CustodyBlobs.tierKey(tier))
        try {
            when (tier) {
                KeyTier.WAKE -> core.installWakeKey(key)
                KeyTier.CONTENT -> core.installContentKey(key)
            }
        } finally {
            key.fill(0)
        }
        when (tier) {
            KeyTier.WAKE -> wakeInstalled = true
            KeyTier.CONTENT -> contentInstalled = true
        }
    }

    /**
     * PB-KEY-7. Every event in [InvalidationEvent] routes here.
     *
     * BOTH tiers go, not just content, and that is not over-caution: `App.PurgeKeys` clears
     * `st.Keys` wholesale, so after this call the Go core holds no wake key either
     * (ADR-007 B17(b)). A session that went on believing the wake tier survived would produce
     * a push path that works until the first screen lock and then goes quiet.
     *
     * It is unconditional and idempotent: lock and background arrive together all the time,
     * and a purge that skipped because it thought nothing was installed would be a guard that
     * cannot fail.
     */
    fun invalidate(event: InvalidationEvent) {
        lastInvalidation = event
        wakeInstalled = false
        contentInstalled = false
        core.purgeKeys()
    }

    fun contentAvailable(): Boolean = contentInstalled

    fun wakeAvailable(): Boolean = wakeInstalled

    /**
     * How content custody is recovered after the last invalidation, or null if none has
     * happened. The event matters: four of the five are a fresh prompt away, and the fifth --
     * a biometric enrollment change -- destroyed the content KEK, so prompting produces an
     * authentication the user can satisfy that changes nothing.
     */
    fun recovery(): Recovery? = lastInvalidation?.let { GateInvalidation.recoveryFor(it) }
}

/**
 * ADR-007 B9/B16: FirebaseMessagingService runs IN the app process, so the split is not
 * enforced by isolation. This is the code-discipline half -- the push path declares the tiers
 * it may touch, and a test holds it to exactly one.
 */
object PushWakePath {

    /**
     * Declared, not inferred from whatever `prepare` happened to call. A set the test reads
     * and a set the code obeys are the same set here; inferring it from behaviour would make
     * the assertion a tautology.
     */
    val requiredTiers: Set<KeyTier> = setOf(KeyTier.WAKE)

    /**
     * Prepare custody for a push arriving on a locked handset. Must succeed with the CONTENT
     * tier refusing, or B16's sole background wake path is dead whenever it matters.
     *
     * It re-installs unconditionally rather than checking `wakeAvailable()` first, because
     * after a lock purge the CORE holds no wake key even though this object might believe it
     * does -- and the whole point of the path is that it works with nobody there to prompt.
     */
    fun prepare(session: KeyCustodySession) {
        for (tier in requiredTiers) {
            session.installTier(tier)
        }
    }
}
