package dev.swarm.phone.keys

/**
 * Phase B slice S14 -- the phone's key custody.
 *
 * SCAFFOLDING ONLY. Every declaration whose value is a DECISION is `TODO()`, so the RED run
 * fails with kotlin.NotImplementedError rather than passing over a placeholder. The types,
 * the enum members and the totality they force are not decisions -- they are what the
 * requirements enumerate, and an enum with all its members is the mechanism that stops a
 * matrix test from passing on an empty set.
 *
 * The authorities:
 *   - PB-KEY-1/2/5/7/8, PB-SEC-1/2 in docs/specifications/remote-phaseB-requirements.md
 *   - ADR-007 B8 (the single inbound crossing), B9 (tiers per role), B16 (minSdk 33)
 */

/**
 * The four device roles `crypto.KeyStore` is an interface over
 * (`internal/remote/crypto/keystore.go:47-56`). PB-KEY-5 exists because "one core key" is
 * wrong: background reconnect needs RELAY_AUTH while the handset is locked, and RECIPIENT
 * recovers BOTH epoch keys from a grant.
 */
enum class KeyRole { NOISE_STATIC, RECIPIENT, COMMAND_SIGN, RELAY_AUTH }

/**
 * ADR-007 D2's two epoch keys. WAKE is content-free and available after first unlock so a
 * push can wake a locked handset; CONTENT is user-authentication-gated and opens session
 * content. Conflating them is the defect PB-KEY-2 exists to prevent.
 */
enum class KeyTier { WAKE, CONTENT }

/** PB-KEY-8's three admissible backings for a role. */
enum class KeyBacking {
    /** Generated and used inside Keystore; the private key never exists outside it. */
    KEYSTORE_NATIVE,

    /** An app-format key held wrapped by an authenticated Keystore AES KEK (ADR-007 B8). */
    KEYSTORE_WRAPPED,

    /** Neither; requires a recorded residual. */
    SOFTWARE_ONLY,
}

/**
 * Where the operation for a role is actually PERFORMED. KEYSTORE_NATIVE backing implies
 * ANDROID_KEYSTORE, because a key that never leaves Keystore cannot be used anywhere else --
 * which in turn implies a reverse Java -> Go seam the frozen facade does not have.
 */
enum class CustodyBoundary { GO_CORE, ANDROID_KEYSTORE }

/** The algorithm a role needs on the wire. Curve25519 entered KeyMint only at API 33. */
enum class KeyAlgorithm { X25519, ED25519, AES_GCM }

/** One row of PB-KEY-8's platform capability matrix. */
data class CustodyRow(
    val role: KeyRole,
    val tier: KeyTier,
    val backing: KeyBacking,
    val algorithm: KeyAlgorithm,
    /** The lowest API level at which this row is achievable. Bound to PB-RUN-1's minSdk. */
    val requiresApi: Int,
    val boundary: CustodyBoundary,
    /** Why this backing and not a stronger one. Never blank. */
    val rationale: String,
    /** Required when backing is SOFTWARE_ONLY; null otherwise. */
    val residual: String?,
)

/** PB-KEY-8. Implementer supplies one row per role. */
object KeyCustodyMatrix {
    val rows: Map<KeyRole, CustodyRow>
        get() = TODO("PB-KEY-8: assign backing, algorithm, required API and rationale per role")

    fun row(role: KeyRole): CustodyRow =
        rows[role] ?: error("PB-KEY-8: the custody matrix has no row for $role")
}

/**
 * ADR-007 B9's tier assignment, plus the enforcement mechanism PB-KEY-2 requires be STATED
 * rather than assumed.
 */
object KeyTierPolicy {
    fun tierOf(role: KeyRole): KeyTier =
        TODO("PB-KEY-5 / ADR-007 B9: assign a custody tier to $role")

    val enforcement: EnforcementMechanism
        get() = TODO("PB-KEY-2: state how the split is enforced on Android")
}

/**
 * PB-KEY-2: "The enforcement mechanism must be stated". On iOS the split leans on the
 * Notification Service Extension being a separate process; on Android
 * FirebaseMessagingService runs IN the app process, so that argument does not transfer.
 */
enum class EnforcementMechanism { OS_PROCESS_ISOLATION, KEYSTORE_AUTH_GATING }

/** PB-KEY-5: the sealed grant blob opens BOTH tiers, so its fate must be stated. */
enum class GrantRetention { DISCARDED_AFTER_OPEN, RETAINED_UNDER_CONTENT_TIER }

object SealedGrantPolicy {
    val retention: GrantRetention
        get() = TODO("PB-KEY-5: discard the sealed grant after opening, or retain it under CONTENT")
}

/** Keystore alias per tier. Distinct aliases are what makes the tiers separately gated. */
object KeystoreAliases {
    fun forTier(tier: KeyTier): String = TODO("PB-KEY-2: alias for $tier")
}

/** Blob names in the sealed store. Naming, not a decision -- stated so tests and the
 *  implementation address the same entries. */
object CustodyBlobs {
    fun tierKey(tier: KeyTier): String = "epoch-${tier.name.lowercase()}-key"

    fun deviceRole(role: KeyRole): String = "device-${role.name.lowercase()}"

    const val SEALED_GRANT: String = "sealed-epoch-grant"
}

// ---------------------------------------------------------------------------
// PB-SEC-1: what key material exists at rest, where, and under whose seal.
// ---------------------------------------------------------------------------

enum class AtRestArtifact {
    DEVICE_NOISE_STATIC,
    DEVICE_RECIPIENT,
    DEVICE_COMMAND_SIGN,
    DEVICE_RELAY_AUTH,
    EPOCH_WAKE_KEY,
    EPOCH_CONTENT_KEY,
    SEALED_EPOCH_GRANT,
}

/**
 * GO_STATE_DIR is the phone core's state directory: `device.key` (128 bytes of raw private
 * scalars, `crypto.NewFileKeyStore`) and `phone-state.json` (which carries `wake_key` and
 * `content_key` as base64 fields, `internal/phonecore/state.go:176-177`). Both are written
 * by Go, 0600, with no Keystore involvement today -- see the S14 RED report.
 */
enum class CustodyLocation { KOTLIN_SEALED_STORE, GO_STATE_DIR }

data class AtRestRecord(
    val artifact: AtRestArtifact,
    val tier: KeyTier,
    val location: CustodyLocation,
    /** PB-SEC-1's property: the persisted bytes are not the raw key. */
    val sealedByKeystore: Boolean,
    val note: String,
)

object AtRestInventory {
    /** Null only for an artifact that is genuinely never persisted (see PB-KEY-5's grant). */
    fun record(artifact: AtRestArtifact): AtRestRecord? =
        TODO("PB-SEC-1: declare where $artifact lives at rest and what seals it")
}

// ---------------------------------------------------------------------------
// Failures. Typed, because PB-KEY-6's whole point is that every custody operation can fail
// and none of them may return a default.
// ---------------------------------------------------------------------------

sealed class KeyCustodyException(message: String) : Exception(message) {

    /** The platform refused because the user is not authenticated (or the window lapsed). */
    class UserAuthenticationRequired(val tier: KeyTier) :
        KeyCustodyException("$tier custody requires a fresh user authentication")

    /**
     * Permanent. A biometric enrollment change destroys the key; re-authenticating cannot
     * recover it. PB-SEC-2 requires this be handled distinctly.
     */
    class KeyPermanentlyInvalidated(val tier: KeyTier) :
        KeyCustodyException("$tier custody key was permanently invalidated")

    /** The Keystore entry is gone, or the blob does not authenticate under it. */
    class KeystoreKeyMissing(val alias: String) :
        KeyCustodyException("no usable Keystore key for alias $alias")

    /** The handset cannot do what a role needs. PB-KEY-8's defined refusal. */
    class PlatformCapabilityMissing(val role: KeyRole, val capability: PlatformCapability) :
        KeyCustodyException("$role needs $capability, which this handset does not provide")
}

// ---------------------------------------------------------------------------
// The Keystore seam and the ONE inbound crossing.
// ---------------------------------------------------------------------------

/**
 * The authenticated Keystore AES KEK, one per tier (ADR-007 B8). Unwrapping the CONTENT tier
 * fails while the device is locked -- that failure IS the gate, not a boolean beside it.
 */
interface KekProvider {
    fun wrap(tier: KeyTier, plaintext: ByteArray): ByteArray

    /** @throws KeyCustodyException on any refusal. Never returns a partial or empty result. */
    fun unwrap(tier: KeyTier, blob: ByteArray): ByteArray
}

/**
 * ADR-007 B8's single deliberate crossing: a transient per-tier data key, unwrapped on the
 * Java side and passed Java -> Go. INBOUND ONLY. No method here may return key material, and
 * no method beyond the two installs may take it -- that is what makes the crossing pinnable.
 *
 * Implemented over the gomobile facade (`App.InstallWakeKey`, `App.InstallContentKey`,
 * `App.PurgeKeys`); declared as an interface so the custody layer is testable on the JVM
 * without loading the native library.
 */
interface CoreKeyCustody {
    fun installWakeKey(key: ByteArray)

    fun installContentKey(key: ByteArray)

    /** PB-KEY-7's lock purge. */
    fun purgeKeys()
}
