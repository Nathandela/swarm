package dev.swarm.phone.keys

/**
 * Phase B slice S14 -- the phone's key custody.
 *
 * The authorities:
 *   - PB-KEY-1/2/5/7/8, PB-SEC-1/2 in docs/specifications/remote-phaseB-requirements.md
 *   - ADR-007 B8 (the single inbound crossing), B9 (tiers per role), B16/B17 (minSdk 33)
 *
 * WHAT THE JVM AND ROBOLECTRIC TESTS AROUND THIS FILE COVER, stated once here because the
 * distinction is load-bearing everywhere below: they cover POLICY -- which tier a role is in,
 * which spec is REQUESTED of the platform, which failures are distinguished, what the code
 * does with each. They cover no hardware property whatsoever. That a real biometric prompt
 * gates the content KEK, that the key is really in a TEE or in StrongBox, that a real FCM
 * push arrives under Doze -- all of it is PB-E2E-5, the physical-handset gate, which is
 * DEFERRED and is not claimed anywhere in this slice.
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

/**
 * PB-KEY-8's platform capability matrix.
 *
 * NO ROW IS KEYSTORE_NATIVE, AT ANY API LEVEL, and that is ADR-007 B17(a) rather than a
 * limitation of this handset: KEYSTORE_NATIVE means the private key never leaves Keystore,
 * which means the OPERATION must run inside Keystore, which needs a Java -> Go reverse seam.
 * B8 pins the crossing to one INBOUND artifact and lets the matrix only narrow it, so the
 * reverse seam does not exist and may not be added here. Every role is therefore
 * KEYSTORE_WRAPPED: an app-format key whose private half lives in the Go core's `device.key`,
 * sealed at rest under the tier KEK that `swarmmobile.KeyCustody` supplies from Keystore.
 *
 * `requiresApi` is the level at which the ROW is achievable, not the app's floor. It is
 * below `SWARM_ANDROID_MIN_SDK` on purpose: B17 records that the 33 floor is a PRODUCT
 * choice, not a cryptographic necessity, and a row claiming to need exactly the floor would
 * re-assert the falsified rationale.
 */
object KeyCustodyMatrix {
    val rows: Map<KeyRole, CustodyRow> = mapOf(
        KeyRole.NOISE_STATIC to CustodyRow(
            role = KeyRole.NOISE_STATIC,
            tier = KeyTier.CONTENT,
            backing = KeyBacking.KEYSTORE_WRAPPED,
            algorithm = KeyAlgorithm.X25519,
            // setUserAuthenticationParameters(timeout, type) -- the call that expresses
            // per-use versus timed at all -- landed in API 30. Below it the content KEK
            // cannot state its own freshness tier, so the row is not achievable.
            requiresApi = 30,
            boundary = CustodyBoundary.GO_CORE,
            rationale = "The Noise handshake's DH runs in the Go core, so the private scalar " +
                "must exist outside Keystore; it is sealed at rest under the content-tier KEK. " +
                "Keystore-native is unavailable by ADR-007 B17(a), not by API level.",
            residual = null,
        ),
        KeyRole.RECIPIENT to CustodyRow(
            role = KeyRole.RECIPIENT,
            tier = KeyTier.CONTENT,
            backing = KeyBacking.KEYSTORE_WRAPPED,
            algorithm = KeyAlgorithm.X25519,
            requiresApi = 30,
            boundary = CustodyBoundary.GO_CORE,
            rationale = "OpenSealedBox recovers BOTH epoch keys from a grant " +
                "(internal/remote/crypto/keystore.go), so an after-first-unlock recipient key " +
                "IS a content key. Content tier, sealed under the user-authentication-gated KEK.",
            residual = null,
        ),
        KeyRole.COMMAND_SIGN to CustodyRow(
            role = KeyRole.COMMAND_SIGN,
            tier = KeyTier.CONTENT,
            backing = KeyBacking.KEYSTORE_WRAPPED,
            algorithm = KeyAlgorithm.ED25519,
            requiresApi = 30,
            boundary = CustodyBoundary.GO_CORE,
            rationale = "The daemon registry pins the device id to this public key (R-DEV.1) " +
                "and the Go core signs every command with it. Content tier: a stolen " +
                "after-first-unlock handset must not be able to sign a launch or a kill.",
            residual = null,
        ),
        KeyRole.RELAY_AUTH to CustodyRow(
            role = KeyRole.RELAY_AUTH,
            tier = KeyTier.WAKE,
            backing = KeyBacking.KEYSTORE_WRAPPED,
            algorithm = KeyAlgorithm.ED25519,
            // The wake KEK asks for nothing beyond an AES-GCM Keystore key, which is API 23.
            requiresApi = 23,
            boundary = CustodyBoundary.GO_CORE,
            rationale = "Background reconnect must work on a LOCKED handset (ADR-007 B9/B16), " +
                "so this seed is wake tier. Its KEK is a Keystore AES key that is deliberately " +
                "NOT user-authentication-gated and NOT invalidated by an enrollment change -- " +
                "gating it would kill the sole background wake path in exactly the state that " +
                "path exists for. The tier split, not the KEK's own gate, is what keeps this " +
                "key from reaching content material.",
            residual = null,
        ),
    )

    fun row(role: KeyRole): CustodyRow =
        rows[role] ?: error("PB-KEY-8: the custody matrix has no row for $role")
}

/**
 * ADR-007 B9's tier assignment, plus the enforcement mechanism PB-KEY-2 requires be STATED
 * rather than assumed.
 */
object KeyTierPolicy {
    fun tierOf(role: KeyRole): KeyTier = KeyCustodyMatrix.row(role).tier

    /**
     * PB-KEY-2 requires this be stated. It is KEYSTORE_AUTH_GATING and not OS_PROCESS_ISOLATION
     * because FirebaseMessagingService runs IN the app process (ADR-007 B9): the iOS argument,
     * which leans on the Notification Service Extension being a separate process, does not
     * transfer. What separates the tiers here is that the content KEK's unwrap REFUSES while
     * the user is not authenticated, plus the code discipline PushWakePath enforces.
     */
    val enforcement: EnforcementMechanism = EnforcementMechanism.KEYSTORE_AUTH_GATING
}

/**
 * PB-KEY-2: "The enforcement mechanism must be stated". On iOS the split leans on the
 * Notification Service Extension being a separate process; on Android
 * FirebaseMessagingService runs IN the app process, so that argument does not transfer.
 */
enum class EnforcementMechanism { OS_PROCESS_ISOLATION, KEYSTORE_AUTH_GATING }

/** PB-KEY-5: the sealed grant blob opens BOTH tiers, so its fate must be stated. */
enum class GrantRetention { DISCARDED_AFTER_OPEN, RETAINED_UNDER_CONTENT_TIER }

/**
 * DISCARDED. A grant blob opens BOTH epoch keys, so retaining it would create a second,
 * independent path to the content key whose custody would have to be argued separately --
 * and the blob buys nothing once opened, because the epoch keys it carries are themselves
 * installed and sealed. Retention would only be worth its cost if the phone had to re-open
 * the grant after a purge, and it does not: PB-KEY-7's recovery is a fresh unwrap of the
 * sealed tier key, not a re-open of the grant.
 */
object SealedGrantPolicy {
    val retention: GrantRetention = GrantRetention.DISCARDED_AFTER_OPEN
}

/**
 * Keystore alias per tier. Distinct aliases are what makes the tiers separately gated: two
 * tiers under one alias is one tier with two names, because whatever authorizes the unwrap
 * authorizes both.
 */
object KeystoreAliases {
    fun forTier(tier: KeyTier): String = when (tier) {
        KeyTier.WAKE -> "dev.swarm.phone.kek.wake"
        KeyTier.CONTENT -> "dev.swarm.phone.kek.content"
    }

    /**
     * Per-use operations get one entry EACH (PB-SEC-2: "no reuse of one authentication for a
     * different action"). A shared entry with a zero timeout still lets one CryptoObject
     * authorization be pointed at whichever operation the caller picks.
     *
     * The TIMED operations deliberately share ONE entry, because that is what
     * `BiometricPolicy.sharesAuthorizationWith` declares of them. Giving them separate
     * aliases would make the declaration false in the other direction: two keys means two
     * windows and two prompts, and a typing session would re-prompt on every take_control.
     */
    fun forOperation(operation: GatedOperation): String =
        if (BiometricPolicy.specFor(operation).requiresCryptoObject) {
            "dev.swarm.phone.gate.peruse." + operation.name.lowercase()
        } else {
            "dev.swarm.phone.gate.timed"
        }
}

/** Blob names in the sealed store. Naming, not a decision -- stated so tests and the
 *  implementation address the same entries. */
object CustodyBlobs {
    fun tierKey(tier: KeyTier): String = "epoch-${tier.name.lowercase()}-key"

    fun deviceRole(role: KeyRole): String = "device-${role.name.lowercase()}"

    const val SEALED_GRANT: String = "sealed-epoch-grant"

    /**
     * The key the GO CORE seals its own state directory with, per tier -- what
     * `swarmmobile.KeyCustody.wakeKEK`/`contentKEK` hand across the boundary.
     *
     * It is a SEPARATE artifact from [tierKey], and the difference is not cosmetic: the epoch
     * keys arrive in the machine's grant and rotate with the epoch, while this one must be
     * stable for the life of the install or `device.key` becomes unopenable. ADR-007 B8's
     * model is exactly this shape -- Keystore holds the authenticated AES KEK and never
     * exports it, the app stores this data key WRAPPED under it, and the unwrap is what
     * refuses on a locked handset.
     */
    fun stateKek(tier: KeyTier): String = "state-${tier.name.lowercase()}-kek"
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

    /**
     * The two per-tier data keys the Go core seals its state directory under -- what
     * `swarmmobile.KeyCustody` hands across the boundary (ADR-007 B8). They are key material
     * at rest like any other and are inventoried like any other; an artifact with no record is
     * key material nobody decided where to keep, which is how `device.key` came to sit in the
     * clear in the first place.
     */
    STATE_KEK_WAKE,
    STATE_KEK_CONTENT,
}

/**
 * GO_STATE_DIR is the phone core's state directory: `device.key` and `phone-state.json`.
 *
 * Both are written by Go. They are no longer written in the clear: `phonecore.Resume` takes
 * a `Sealer` per PB-KEY-2 tier, `swarmmobile.NewApp` requires a `KeyCustody` to supply the
 * two tier KEKs, and there is no constructor that can produce an App without one -- omitting
 * a sealer is `phonecore.ErrNoSealer`, not cleartext (ADR-007 B18(c)).
 *
 * A DECLARATION IS NOT A PROOF, and this enum is a declaration. What actually holds the
 * property is a pair of Go tests that open the two files and search their bytes for the key
 * material verbatim (`android/gate/keycustody_test.go`), plus the positive half -- that the
 * material went through an injected sealer at all -- in phonecore's own suite. The byte
 * search alone is not sufficient and its own comments say so: base64 alignment means a
 * needle buried mid-field is invisible to it.
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

    private const val DEVICE_KEY = "the phone core's device.key, a sealed container: the three " +
        "content-tier scalars in one blob under the content KEK, the relay-auth seed in another " +
        "under the wake KEK. The four PUBLIC halves travel in the clear -- they are public by " +
        "definition and the errorless accessors must answer while a tier is locked -- and are " +
        "re-derived from the sealed material and compared at every unseal, so a write to the app's " +
        "private data directory cannot substitute attacker-held publics."

    private val records: Map<AtRestArtifact, AtRestRecord> = listOf(
        AtRestRecord(
            AtRestArtifact.DEVICE_NOISE_STATIC, KeyTier.CONTENT, CustodyLocation.GO_STATE_DIR,
            sealedByKeystore = true, note = DEVICE_KEY,
        ),
        AtRestRecord(
            AtRestArtifact.DEVICE_RECIPIENT, KeyTier.CONTENT, CustodyLocation.GO_STATE_DIR,
            sealedByKeystore = true, note = DEVICE_KEY,
        ),
        AtRestRecord(
            AtRestArtifact.DEVICE_COMMAND_SIGN, KeyTier.CONTENT, CustodyLocation.GO_STATE_DIR,
            sealedByKeystore = true, note = DEVICE_KEY,
        ),
        AtRestRecord(
            AtRestArtifact.DEVICE_RELAY_AUTH, KeyTier.WAKE, CustodyLocation.GO_STATE_DIR,
            sealedByKeystore = true, note = DEVICE_KEY,
        ),
        AtRestRecord(
            AtRestArtifact.EPOCH_WAKE_KEY, KeyTier.WAKE, CustodyLocation.GO_STATE_DIR,
            sealedByKeystore = true,
            note = "phone-state.json's wake key, sealed under the wake KEK. It must open with no " +
                "user present: a push arrives with nobody there.",
        ),
        AtRestRecord(
            AtRestArtifact.EPOCH_CONTENT_KEY, KeyTier.CONTENT, CustodyLocation.GO_STATE_DIR,
            sealedByKeystore = true,
            note = "phone-state.json's content key, sealed under the content KEK. One file cannot " +
                "be gated two ways, so the two tiers are sealed SEPARATELY inside it -- a single " +
                "seal over the whole blob would collapse PB-KEY-2's split at rest.",
        ),
        AtRestRecord(
            AtRestArtifact.STATE_KEK_WAKE, KeyTier.WAKE, CustodyLocation.KOTLIN_SEALED_STORE,
            sealedByKeystore = true,
            note = "the data key the Go core seals its WAKE tier with, held wrapped under the " +
                "wake Keystore KEK. Its unwrap must succeed with no user present, or the sole " +
                "background wake path is dead in exactly the state it exists for.",
        ),
        AtRestRecord(
            AtRestArtifact.STATE_KEK_CONTENT, KeyTier.CONTENT, CustodyLocation.KOTLIN_SEALED_STORE,
            sealedByKeystore = true,
            note = "the data key the Go core seals its CONTENT tier with, held wrapped under the " +
                "user-authentication-gated Keystore KEK. THIS unwrap refusing is the gate: it is " +
                "what makes a locked handset unable to open device.key's content half, and it is " +
                "why the Go core fetches it per operation and never memoizes it.",
        ),
    ).associateBy { it.artifact }

    /**
     * Null only for an artifact that is genuinely never persisted. SEALED_EPOCH_GRANT is the
     * one: [SealedGrantPolicy] discards it after opening, so there is nothing at rest to
     * record and PerRoleCustodyTest holds the two statements to each other.
     */
    fun record(artifact: AtRestArtifact): AtRestRecord? = records[artifact]
}

// ---------------------------------------------------------------------------
// Failures. Typed, because PB-KEY-6's whole point is that every custody operation can fail
// and none of them may return a default.
// ---------------------------------------------------------------------------

sealed class KeyCustodyException(message: String) : Exception(message) {

    /**
     * The platform refused because the user is not authenticated (or the window lapsed).
     *
     * ITS MESSAGE CARRIES THE VERDICT TOKEN, and that is load-bearing rather than cosmetic.
     * When this is thrown out of a `swarmmobile.KeyCustody` method, gomobile flattens it into
     * a Go error carrying only the message -- so the token is the ONLY thing that lets the Go
     * core tell a recoverable refusal from a permanent one. Without it every refusal reads as
     * opaque, and `phonecore.openSealedDeviceKeys` refuses a Resume outright for any
     * content-tier error that is not one of the two sentinels: a locked handset would become
     * an app that cannot start.
     */
    class UserAuthenticationRequired(val tier: KeyTier) :
        KeyCustodyException(
            "${GoCustodyFailure.AUTH_REQUIRED_TOKEN}: $tier custody requires a fresh user authentication",
        )

    /**
     * Permanent. A biometric enrollment change destroys the key; re-authenticating cannot
     * recover it. PB-SEC-2 requires this be handled distinctly.
     *
     * Same token discipline as above, and the consequence of getting it wrong is worse in this
     * direction: a permanent invalidation that classified as recoverable produces a prompt the
     * user can satisfy and that changes nothing, forever.
     */
    class KeyPermanentlyInvalidated(val tier: KeyTier) :
        KeyCustodyException(
            "${GoCustodyFailure.KEY_INVALIDATED_TOKEN}: $tier custody key was permanently invalidated",
        )

    /** The Keystore entry is gone, or the blob does not authenticate under it. */
    class KeystoreKeyMissing(val alias: String) :
        KeyCustodyException("no usable Keystore key for alias $alias")

    /** The handset cannot do what a role needs. PB-KEY-8's defined refusal. */
    class PlatformCapabilityMissing(val role: KeyRole, val capability: PlatformCapability) :
        KeyCustodyException("$role needs $capability, which this handset does not provide")

    /**
     * PB-KEY-8's read-back refusal: the platform generated the key but delivered something
     * weaker than was requested. Distinct from every failure above because nothing the user
     * does fixes it -- the handset is not capable of what the design requires.
     */
    class KeystoreDowngrade(val alias: String, val detail: String) :
        KeyCustodyException("$alias was generated weaker than requested: $detail")

    /**
     * Anything else the platform threw. Deliberately NOT folded into
     * [UserAuthenticationRequired]: mapping an unknown failure onto "authenticate again"
     * produces a prompt loop over a bug, and the user can never satisfy it.
     */
    class Unexpected(val tier: KeyTier, val detail: String) :
        KeyCustodyException("$tier custody failed unexpectedly: $detail")
}

// ---------------------------------------------------------------------------
// PB-KEY-6: the Go core's two custody sentinels, made distinguishable in Kotlin.
// ---------------------------------------------------------------------------

/**
 * `crypto.ErrKeyAuthRequired` and `crypto.ErrKeyInvalidated` survive every Go hop distinctly
 * -- and then hit gomobile, which flattens every Go error into a Java exception carrying
 * only a MESSAGE. PB-KEY-6 wants the UI to act differently on each: one is a re-prompt, the
 * other means the key is gone and the device must re-pair. So the facade stamps each refusal
 * with a stable token before it crosses (`swarmmobile.KeyCustodyAuthRequired` /
 * `KeyCustodyKeyInvalidated`, applied centrally in the panic barrier every entry point
 * already installs, so no verb can forget) and this maps the token back onto a type.
 *
 * The tokens are read from the BOUND CONSTANTS rather than copied. A second copy of a
 * discriminator string is a second thing to get wrong, and the failure would be silent: an
 * unrecognised token falls through to [KeyCustodyException.Unexpected], which is a prompt
 * the user cannot satisfy.
 *
 * They are declared here as literals ONLY because the unit-test JVM does not load the AAR;
 * `mobile/s14_custody_test.go` asserts the Go constants and these literals agree, in the
 * direction that matters -- Go is authoritative and Kotlin is checked against it.
 */
object GoCustodyFailure {

    /** Recoverable: the Keystore refused for want of a fresh user authentication. */
    const val AUTH_REQUIRED_TOKEN = "swarm-custody/auth-required"

    /** Permanent: the key is gone. Re-prompting cannot bring it back. */
    const val KEY_INVALIDATED_TOKEN = "swarm-custody/key-invalidated"

    /**
     * Classify a message thrown out of any bound facade verb.
     *
     * Returns null for a message carrying neither token -- which is most of them. A caller
     * that turned every facade failure into a custody failure would report "authenticate
     * again" for a relay timeout.
     */
    fun classify(tier: KeyTier, message: String?): KeyCustodyException? {
        val text = message ?: return null
        return when {
            text.contains(KEY_INVALIDATED_TOKEN) -> KeyCustodyException.KeyPermanentlyInvalidated(tier)
            text.contains(AUTH_REQUIRED_TOKEN) -> KeyCustodyException.UserAuthenticationRequired(tier)
            else -> null
        }
    }

    /**
     * What the UI must do about it. The two answers differ in the one way that matters: a
     * recoverable refusal is worth prompting for, and a permanent one must NOT be, or the
     * user gets a prompt they can satisfy that changes nothing.
     */
    fun recoveryFor(failure: KeyCustodyException): Recovery = when (failure) {
        is KeyCustodyException.UserAuthenticationRequired -> Recovery.REAUTHENTICATE
        is KeyCustodyException.KeyPermanentlyInvalidated -> Recovery.REPAIR_DEVICE
        is KeyCustodyException.KeystoreKeyMissing -> Recovery.REPAIR_DEVICE
        is KeyCustodyException.KeystoreDowngrade -> Recovery.REPROVISION_KEK
        is KeyCustodyException.PlatformCapabilityMissing -> Recovery.REPAIR_DEVICE
        is KeyCustodyException.Unexpected -> Recovery.REPAIR_DEVICE
    }
}

/**
 * The transport-level states PB-KEY-6's two sentinels must reach the user as.
 *
 * `mobile/relay.go` used to discard its dial error with a bare `continue`, which was
 * unreachable while the shipped app ran on the software keystore and went LIVE the moment
 * the Keystore-backed KEK landed. Left alone, a recoverable refusal is an endless
 * "reconnecting" with no prompt, and a permanent one is the same loop against a key that no
 * longer exists.
 *
 * The strings are the ones `App.ConnectionState` reports, so the mapping is total in the
 * direction the UI reads it.
 */
enum class ConnectionState(val wire: String) {
    OFFLINE("offline"),
    CONNECTING("connecting"),
    ONLINE("online"),
    RECONNECTING("reconnecting"),

    /** RECOVERABLE. Prompt for the biometric; the connection resumes once it succeeds. */
    REAUTH_REQUIRED("reauth_required"),

    /** PERMANENT and TERMINAL. The relay-auth key is gone; nothing on-device recovers it. */
    REPAIR_REQUIRED("repair_required"),

    /**
     * PERMANENT and TERMINAL, and NOT a custody failure (PB-APP-10).
     *
     * `relay.ErrRevoked` is the only signal a revoked phone ever gets, and it comes back from
     * the relay handshake rather than from Keystore -- so it matches neither crypto sentinel.
     * Before this entry existed the transport loop fell through to a bare `continue` and the
     * phone redialled every 250 ms for the life of the process behind a "reconnecting"
     * spinner: the failure LOOP the requirement forbids, reached by the owner doing exactly
     * what the product tells them to do when a handset is lost.
     *
     * It shares REPAIR_REQUIRED's remedy and not its cause. The owner has to clear the
     * machine-side registration before a re-pair can succeed, so the two must read differently
     * on screen even though both say "pair again".
     */
    REVOKED("revoked"),
    ;

    /** True only for the state that must not sit behind a spinner. */
    val needsBiometricPrompt: Boolean get() = this == REAUTH_REQUIRED

    /** True where the app must stop retrying and say so. */
    val isTerminal: Boolean get() = this == REPAIR_REQUIRED || this == REVOKED

    companion object {
        fun of(wire: String): ConnectionState =
            entries.firstOrNull { it.wire == wire }
                ?: error("swarmmobile reported an unknown connection state: $wire")
    }
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
