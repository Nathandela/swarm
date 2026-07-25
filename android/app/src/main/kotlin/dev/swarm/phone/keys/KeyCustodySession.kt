package dev.swarm.phone.keys

/**
 * The at-rest store and the custody lifecycle. SCAFFOLDING ONLY.
 *
 * PB-SEC-1 (at rest), PB-KEY-2 (the tiers in use), PB-KEY-7 (lock purges live memory).
 */

/**
 * PB-SEC-1: everything this holds is sealed under the Keystore KEK of its tier. `rawBytes` is
 * what a `run-as`/backup extraction would see, and the test asserts it is not the key.
 */
class SealedStore(private val kek: KekProvider) {

    fun put(name: String, tier: KeyTier, plaintext: ByteArray) {
        TODO("PB-SEC-1: seal $name under the $tier KEK")
    }

    /** @throws KeyCustodyException when the tier's KEK will not authorize the unwrap. */
    fun open(name: String): ByteArray = TODO("PB-SEC-1: unseal $name")

    fun tierOf(name: String): KeyTier = TODO("PB-SEC-1: which tier seals $name")

    /** The persisted bytes, exactly as they sit on disk. */
    fun rawBytes(name: String): ByteArray = TODO("PB-SEC-1: persisted bytes for $name")

    fun names(): Set<String> = TODO("PB-SEC-1: the sealed inventory")

    fun remove(name: String) {
        TODO("PB-KEY-5: drop $name")
    }
}

/**
 * Owns the one inbound crossing over its whole lifetime: unwrap under the tier KEK, install
 * into the Go core, zeroize, and purge on lock.
 */
class KeyCustodySession(
    private val store: SealedStore,
    private val core: CoreKeyCustody,
) {

    /**
     * Unwrap the tier's epoch key and install it. The unwrapped array is zeroized before this
     * returns -- the facade package doc makes that the caller's obligation, and here the
     * caller is this class.
     *
     * @throws KeyCustodyException.UserAuthenticationRequired for CONTENT while locked.
     */
    fun installTier(tier: KeyTier) {
        TODO("PB-KEY-1: unwrap $tier, install inbound, zeroize")
    }

    /** PB-KEY-7. Every event in [InvalidationEvent] routes here. */
    fun invalidate(event: InvalidationEvent) {
        TODO("PB-KEY-7: purge live content custody on $event")
    }

    fun contentAvailable(): Boolean = TODO("PB-KEY-7: is content custody live?")

    fun wakeAvailable(): Boolean = TODO("PB-KEY-2: is wake custody live?")
}

/**
 * ADR-007 B9/B16: FirebaseMessagingService runs IN the app process, so the split is not
 * enforced by isolation. This is the code-discipline half -- the push path declares the tiers
 * it may touch, and a test holds it to exactly one.
 */
object PushWakePath {

    val requiredTiers: Set<KeyTier>
        get() = TODO("PB-KEY-2: which tiers may the push path touch?")

    /**
     * Prepare custody for a push arriving on a locked handset. Must succeed with the CONTENT
     * tier refusing, or B16's sole background wake path is dead whenever it matters.
     */
    fun prepare(session: KeyCustodySession) {
        TODO("PB-KEY-2: install only what a content-free wake needs")
    }
}
