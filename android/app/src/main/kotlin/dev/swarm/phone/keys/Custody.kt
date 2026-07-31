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
 * does with each. They cover no hardware property whatsoever. That the key is really in a TEE
 * or in StrongBox, that a real FCM push arrives under Doze -- all of it is PB-E2E-5, the
 * physical-handset gate, which is DEFERRED and is not claimed anywhere in this slice.
 *
 * NOTHING HERE IS GATED ON A USER AUTHENTICATION ANY MORE (ADR-007 B133). The trust boundary is
 * the WIRE, so the tier KEKs ask for no authenticator. What is KEPT, and is easy to delete by
 * mistake while reading the above, is the SEALING: hardware backing, the StrongBox preference
 * and non-exportability all defend offline extraction of the app's data directory by someone
 * who has the bytes and not the running device -- an attacker the new boundary does not trust
 * either. The cost of keeping it is one flag.
 */

/**
 * The four device roles `crypto.KeyStore` is an interface over
 * (`internal/remote/crypto/keystore.go:47-56`). PB-KEY-5 exists because "one core key" is
 * wrong: background reconnect needs RELAY_AUTH while the handset is locked, and RECIPIENT
 * recovers BOTH epoch keys from a grant.
 */
enum class KeyRole { NOISE_STATIC, RECIPIENT, COMMAND_SIGN, RELAY_AUTH }

/**
 * ADR-007 D2's two epoch keys. WAKE is content-free so a push can wake a locked handset;
 * CONTENT opens session content. Conflating them is the defect PB-KEY-2 exists to prevent.
 *
 * THE SPLIT'S RATIONALE IS TRANSPORT, NOT THE DEVICE HOLDER (ADR-007 A15 as amended by B133).
 * It was argued from a stolen phone, and that argument is retired. What requires two keys is
 * that the push payload passes through FCM, WHICH READS IT: the wake key must be readable by a
 * path Google's carrier can observe, and the content key must not be derivable from it. That is
 * a property of the wire, it is enforced at the SENDER (PB-PUSH-0, in the gateway), and it is
 * untouched by anything removed from the phone.
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
            // 30 was the level `setUserAuthenticationParameters` landed at, and that call is
            // gone (ADR-007 B133). The floor is NOT lowered to match: raising the set of
            // handsets a row claims to be achievable on is a widening, which B8 forbids the
            // matrix -- and PB-RUN-1 pins minSdk to 33 above it either way, so the number
            // decides nothing on any handset the app installs on.
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
                "(internal/remote/crypto/keystore.go), so a recipient key IS a content key. " +
                "Content tier, sealed at rest under the content KEK.",
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
                "and the Go core signs every command with it. Content tier: the seed that can " +
                "sign a launch or a kill must not be readable from an extracted data directory.",
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
                "so this seed is wake tier. Its KEK is a Keystore AES key that is NOT " +
                "invalidated by an enrollment change: destroying it would kill the sole " +
                "background wake path in exactly the state that path exists for. The tier " +
                "split is what keeps this key from reaching content material.",
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
     * PB-KEY-2 requires this be stated, and ADR-007 B133 NARROWED the answer.
     *
     * It was KEYSTORE_AUTH_GATING: the content KEK's unwrap refused while the user was not
     * authenticated, and [PushWakePath]'s code discipline sat behind it. The KEK no longer asks
     * for an authenticator, so what is left on the PHONE side is the code discipline alone.
     * That is a real reduction and it is recorded rather than glossed.
     *
     * IT IS SMALL FOR ONE REASON. The property the split exists to buy is that the CARRIER of
     * the push cannot read session content, and that is enforced at the SENDER -- the gateway
     * holds the wake key only (PB-PUSH-0). The receiver-side half was always the weaker one on
     * Android, because FirebaseMessagingService runs IN the app process (ADR-007 B9) rather
     * than in an iOS-style Notification Service Extension.
     */
    val enforcement: EnforcementMechanism = EnforcementMechanism.CODE_DISCIPLINE
}

/**
 * PB-KEY-2: "The enforcement mechanism must be stated". On iOS the split leans on the
 * Notification Service Extension being a separate process; on Android
 * FirebaseMessagingService runs IN the app process, so that argument does not transfer.
 */
enum class EnforcementMechanism { OS_PROCESS_ISOLATION, CODE_DISCIPLINE }

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
 * Keystore alias per tier. Distinct aliases are what keeps the tiers separately addressable:
 * two tiers under one alias is one tier with two names, so a purge, a discard or an
 * invalidation of either would take both.
 */
object KeystoreAliases {
    fun forTier(tier: KeyTier): String = when (tier) {
        KeyTier.WAKE -> "dev.swarm.phone.kek.wake"
        KeyTier.CONTENT -> "dev.swarm.phone.kek.content"
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
            note = "phone-state.json's content key, sealed under the content KEK. The two tiers " +
                "are sealed SEPARATELY inside it -- a single seal over the whole blob would " +
                "collapse PB-KEY-2's split at rest, leaving one key whose holder has both.",
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
                "content Keystore KEK. The KEK is hardware-backed and non-exportable, which is " +
                "what makes an extracted copy of the app's data directory unopenable off the " +
                "device; it asks for no user authentication (ADR-007 B133).",
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
     * The platform refused for want of a user authentication.
     *
     * NOTHING THIS BUILD PROVISIONS CAN RAISE IT (ADR-007 B133): the tier KEKs are generated
     * with `setUserAuthenticationRequired(false)`. It is kept for the population that CAN --
     * an install provisioned before that change, whose content KEK still carries
     * `AUTH_BIOMETRIC_STRONG` and which `KeystoreCustodyBootstrap.ensure` does not re-spec
     * because the alias already exists. `PhoneRuntime` routes it to the permanent verdict,
     * because a re-pair discards the alias and the next provision writes one that asks for
     * no authenticator -- and there is no prompt anywhere in the app to satisfy the old one.
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
     * Permanent. The Keystore key is destroyed and nothing on the device brings it back.
     *
     * Same token discipline as above. It is what `phonecore.openSealedDeviceKeys` reads to tell
     * a per-operation refusal from a blob it can never open, so a drifted copy of the token
     * turns a re-pairable handset into an app that will not start.
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
 * only a MESSAGE. PB-KEY-6 wants each to arrive as itself rather than as an opaque refusal, so
 * the facade stamps each with a stable token before it crosses
 * (`swarmmobile.KeyCustodyAuthRequired` / `KeyCustodyKeyInvalidated`, applied centrally in the
 * panic barrier every entry point already installs, so no verb can forget) and this maps the
 * token back onto a type.
 *
 * BOTH TOKENS SURVIVE ADR-007 B133 even though the two verdicts now route to the same screen.
 * `internal/remote/crypto` is FROZEN and still raises both sentinels, and
 * `phonecore.openSealedDeviceKeys` refuses a Resume outright for any content-tier error that is
 * neither of them -- so dropping a token here would turn a legible refusal into an app that
 * cannot start. The classification is a WIRE property; what changed is only what the UI does
 * with the answer.
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
}

/**
 * The transport-level states a dial failure must reach the user as.
 *
 * `mobile/relay.go` used to discard its dial error with a bare `continue`, which was
 * unreachable while the shipped app ran on the software keystore and went LIVE the moment
 * the Keystore-backed KEK landed: a destroyed key became an endless "reconnecting" loop
 * against something that was never going to start working.
 *
 * THERE IS NO `reauth_required` ROW ANY MORE (ADR-007 B133). It meant "prompt for the biometric
 * and it will connect", and there is no prompt anywhere in this app to offer -- so the state
 * had lost its producer AND its remedy at once. It was removed atomically with
 * `mobile/relay.go`'s `connReauthRequired`, the taxonomy row and `Remedy.AUTHENTICATE`,
 * because a state kept on one side of that join is a screen nothing can ever reach.
 *
 * The strings are the ones `App.ConnectionState` reports, so the mapping is total in the
 * direction the UI reads it.
 */
enum class ConnectionState(val wire: String) {
    OFFLINE("offline"),
    CONNECTING("connecting"),
    ONLINE("online"),
    RECONNECTING("reconnecting"),

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

    /**
     * PERMANENT and TERMINAL. The relay presented a key this phone did not pin at pairing --
     * or nothing was ever pinned and the platform has no usable trust roots (ADR-007 B34,
     * residual 1.9).
     *
     * It is the transport POLICY refusing, not the network failing, and the difference is the
     * whole point: before this state existed the dial switch fell through to a bare `continue`
     * and the phone redialled every 250 ms behind a "reconnecting" spinner, against a
     * certificate that was never going to start matching. The fourth instance of that defect
     * in this one switch.
     *
     * The remedy is pairing again: pairing is the only channel that carries a relay pin.
     */
    RELAY_UNTRUSTED("relay_untrusted"),

    /**
     * PERMANENT and TERMINAL, and NOT the phone's to fix.
     *
     * The machine's relay.json names a cleartext `ws://` relay, which PB-NET-2 refuses outside
     * a loopback carve-out. It shares RELAY_UNTRUSTED's shape and not its remedy: pairing
     * again re-delivers the SAME URL, so a user told to re-pair would go round a loop. The
     * owner has to fix the machine's configuration first.
     */
    RELAY_INSECURE("relay_insecure"),
    ;

    /** True where the app must stop retrying and say so. */
    val isTerminal: Boolean
        get() = this == REPAIR_REQUIRED || this == REVOKED ||
            this == RELAY_UNTRUSTED || this == RELAY_INSECURE

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
 * The Keystore AES KEK, one per tier (ADR-007 B8). It is hardware-backed and non-exportable;
 * it asks for no user authentication (B133). What it defends is the app's data directory
 * copied OFF the device -- an attacker holding the bytes and not the running handset.
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

    /** PB-KEY-7's purge. Its trigger is revoke/unpair (ADR-007 B133); there is no lock event. */
    fun purgeKeys()
}
