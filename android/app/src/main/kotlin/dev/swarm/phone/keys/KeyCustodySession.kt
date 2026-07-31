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
 * NOTHING IS MEMOIZED. `open` goes back to the KEK every time, deliberately: a store that
 * unwrapped once and cached would keep serving the content key after PB-KEY-7's purge, while
 * every restart-based test still passed. The cost is a Keystore round trip per read, which is
 * the point.
 *
 * WHAT IT KEEPS IN MEMORY IS CIPHERTEXT. The blobs are read from [PersistentCustodyBacking]
 * once, at construction, and re-read only when the object is built again -- which on Android is
 * every process. Caching sealed bytes costs nothing, because opening them still needs the KEK.
 *
 * THE BACKING IS A CONSTRUCTOR ARGUMENT AND HAS NO DEFAULT. It used to be a `LinkedHashMap`
 * with no way to say otherwise, so the sealed material lived for exactly one process and the
 * second launch could not open its own state. [openOver] is the production factory; the
 * one-argument constructor is the volatile store the pure-JVM policy tests want, and it is
 * named for what it is at the point of use.
 */
class SealedStore private constructor(
    private val kek: KekProvider,
    private val backing: PersistentCustodyBacking,
) {

    /**
     * A store over bytes that die with the process. It is what the policy tests construct --
     * they assert about tiers and refusals, not about durability -- and it is never what the
     * app constructs.
     */
    constructor(kek: KekProvider) : this(kek, PersistentCustodyBacking.inMemoryForTest())

    private val entries = LinkedHashMap(backing.load())

    /**
     * Seals [plaintext] under [tier]'s KEK and keeps only the blob. The caller's array is
     * neither retained nor cleared -- it belongs to the caller, and clearing it here would
     * make `put` surprising for a caller that still needs the value.
     *
     * THE BACKING IS WRITTEN FIRST. An entry this object believes it holds and the backing
     * does not is the whole defect in miniature: it works until the process dies.
     */
    fun put(name: String, tier: KeyTier, plaintext: ByteArray) {
        val record = SealedRecord(tier, kek.wrap(tier, plaintext))
        backing.save(name, record)
        entries[name] = record
    }

    /** @throws KeyCustodyException when the tier's KEK will not authorize the unwrap. */
    fun open(name: String): ByteArray {
        val entry = entry(name)
        return kek.unwrap(entry.tier, entry.blob)
    }

    fun tierOf(name: String): KeyTier = entry(name).tier

    /** The persisted bytes, exactly as the backing holds them. */
    fun rawBytes(name: String): ByteArray = entry(name).blob.copyOf()

    fun names(): Set<String> = entries.keys.toSet()

    fun remove(name: String) {
        entries.remove(name)
        backing.delete(name)
    }

    private fun entry(name: String): SealedRecord =
        entries[name] ?: throw KeyCustodyException.KeystoreKeyMissing(name)

    companion object {
        /**
         * The production factory: a store over a backing that outlives the process.
         *
         * It is `openOver` rather than a second `open` because the instance method of that
         * name opens an ENTRY. Two `open`s one line apart, meaning "open the store" and "open
         * this blob", is a name that gets misread once by everyone.
         */
        fun openOver(kek: KekProvider, backing: PersistentCustodyBacking): SealedStore =
            SealedStore(kek, backing)
    }
}

/**
 * Owns the one inbound crossing over its whole lifetime: unwrap under the tier KEK, install
 * into the Go core, zeroize, and purge on revoke.
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

    /**
     * Unwrap the tier's epoch key and install it. The unwrapped array is zeroized before this
     * returns -- the facade package doc makes that the caller's obligation, and here the
     * caller is this class.
     *
     * The zeroize is in a `finally` around the install, not after it: an install that throws
     * would otherwise leave the plaintext live on the Java heap for the GC to get to
     * eventually, which is the same defect with a tidier diff.
     *
     * @throws KeyCustodyException when the tier's KEK will not answer -- a destroyed key, a
     *  missing entry, or a platform fault. It is no longer a state a user can be in: the KEKs
     *  ask for no authentication (ADR-007 B133).
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
     * PB-KEY-7's purge, whose TRIGGER is now REVOKE or UNPAIR (ADR-007 B133 decision 3).
     *
     * It used to be `invalidate(event)`, taking one of five events -- a screen lock, a
     * backgrounding, an enrollment change -- because the content tier was returned to locked
     * whenever the user's authentication lapsed. There is no such event any more, and there is
     * no `recovery()` beside this either: recovery meant "which of these is a fresh prompt
     * away", and no prompt exists.
     *
     * BOTH TIERS GO, which is the change of substance rather than of name. The old lock purge
     * deliberately spared the wake tier (ADR-007 B35), because a high-priority FCM push is the
     * sole background wake path and arrives on a locked handset with nobody there -- a screen
     * lock is a state the phone comes BACK from. A revoke is not: the device's registration is
     * gone, so a wake it could still answer would be a wake it has no business answering, and
     * the honest state is holding neither key. It is not recoverable without pairing again.
     *
     * WHAT THIS OBJECT CAN DO IS DROP ITS OWN RECORD AND ASK THE CORE. Whether `App.PurgeKeys`
     * clears the wake tier on the Go side is that verb's business and is asserted there; a
     * session reporting a tier it no longer holds is the half this class owns.
     *
     * It is unconditional and idempotent: a purge that skipped because it thought nothing was
     * installed would be a guard that cannot fail.
     */
    fun purge() {
        contentInstalled = false
        wakeInstalled = false
        core.purgeKeys()
    }

    fun contentAvailable(): Boolean = contentInstalled

    fun wakeAvailable(): Boolean = wakeInstalled
}

/**
 * ADR-007 B9/B16: FirebaseMessagingService runs IN the app process, so the split is not
 * enforced by isolation. This is the code-discipline half -- the push path declares the tiers
 * it may touch, and a test holds it to exactly one.
 *
 * IT IS NOW THE ONLY PHONE-SIDE ENFORCEMENT OF THE TIER BOUNDARY (ADR-007 B133). It used to sit
 * behind the content KEK's auth gate, which refused the unwrap outright while the user was not
 * authenticated; that gate is gone, so this declaration is what is left on this side. B133
 * records the reduction and accepts it, because the property the split exists to buy -- that the
 * CARRIER of the push cannot read session content -- is enforced at the sender (PB-PUSH-0), and
 * the receiver-side half was always the weaker one on Android.
 */
object PushWakePath {

    /**
     * Declared, not inferred from whatever `prepare` happened to call. A set the test reads
     * and a set the code obeys are the same set here; inferring it from behaviour would make
     * the assertion a tautology.
     */
    val requiredTiers: Set<KeyTier> = setOf(KeyTier.WAKE)

    /**
     * Prepare custody for a push arriving on a locked handset. It touches the WAKE tier and no
     * other, which is the discipline; a push path that reached for the content tier would be
     * decrypting session content in a process FCM woke.
     *
     * It re-installs unconditionally rather than checking `wakeAvailable()` first, because this
     * runs in a process that may have been started BY the push and may hold nothing at all.
     */
    fun prepare(session: KeyCustodySession) {
        for (tier in requiredTiers) {
            session.installTier(tier)
        }
    }
}
