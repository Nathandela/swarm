package dev.swarm.phone.keys

import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties

/**
 * PB-KEY-8 and the requesting half of PB-SEC-2.
 *
 * What is testable here and what is NOT, stated up front because §10's honesty clause makes
 * the difference load-bearing:
 *
 *  - TESTABLE (Robolectric): that the code REQUESTS the right KeyGenParameterSpec -- auth
 *    required, per-use vs timed, invalidate-on-enrollment, StrongBox preference and its
 *    fallback -- and that it VERIFIES what it got back and fails closed on a downgrade.
 *  - NOT TESTABLE HERE: that the achieved key is really hardware-backed, that a real
 *    biometric prompt gates it, or that StrongBox behaves as advertised. Those are
 *    PB-E2E-5, the deferred physical-handset gate. Nothing in this file may be read as
 *    covering them.
 */

/** What a handset must be able to do for a role. */
enum class PlatformCapability {
    KEYSTORE_X25519,
    KEYSTORE_ED25519,
    KEYSTORE_AES_GCM,
    USER_AUTH_PER_USE,
    STRONGBOX,
}

/**
 * Three states, not two. A probe that could not answer is UNKNOWN, and UNKNOWN must fail
 * closed -- "assume present" is how a software fallback ships unnoticed.
 */
enum class CapabilityState { PRESENT, ABSENT, UNKNOWN }

sealed class CustodyPlan {
    data class Provisioned(
        val rows: Map<KeyRole, CustodyRow>,
        val strongBox: Boolean,
    ) : CustodyPlan()

    /** PB-KEY-8's "defined refusal when the handset lacks the required algorithm". */
    data class Refused(
        val role: KeyRole,
        val capability: PlatformCapability,
        val reason: String,
    ) : CustodyPlan()
}

object CustodyPlanner {

    /**
     * The capabilities the design REQUIRES, each mapped to the role that most needs it, so
     * PB-KEY-8's "defined refusal" can name a role rather than shrug.
     *
     * STRONGBOX is deliberately absent: its absence is a fallback, not a refusal, because it
     * is device-dependent and refusing without it would refuse most handsets.
     *
     * These are required even though ADR-007 B17(a) means no row is KEYSTORE_NATIVE. They are
     * a fail-closed FLOOR, not a claim about where the operation runs: `SWARM_ANDROID_MIN_SDK`
     * is 33 and every one of them is guaranteed there, so a handset that answers ABSENT is a
     * handset whose Keystore is not behaving as its API level promises. Downgrading silently
     * on that answer is exactly what PB-KEY-8 exists to prevent.
     */
    private val required = linkedMapOf(
        PlatformCapability.KEYSTORE_AES_GCM to KeyRole.RELAY_AUTH,
        PlatformCapability.USER_AUTH_PER_USE to KeyRole.COMMAND_SIGN,
        PlatformCapability.KEYSTORE_X25519 to KeyRole.NOISE_STATIC,
        PlatformCapability.KEYSTORE_ED25519 to KeyRole.COMMAND_SIGN,
    )

    fun forDevice(capabilities: Map<PlatformCapability, CapabilityState>): CustodyPlan {
        for ((capability, role) in required) {
            // A capability the probe never reported is UNKNOWN, not PRESENT. Defaulting an
            // absent map entry to present is the same defect as treating UNKNOWN as present,
            // one layer down.
            val state = capabilities[capability] ?: CapabilityState.UNKNOWN
            if (state != CapabilityState.PRESENT) {
                return CustodyPlan.Refused(
                    role = role,
                    capability = capability,
                    reason = "this handset reports $capability as $state. $role needs it, and a " +
                        "silent downgrade to software-only custody is what PB-KEY-8 forbids: " +
                        "UNKNOWN is a probe that could not answer, which fails closed exactly " +
                        "as ABSENT does.",
                )
            }
        }
        return CustodyPlan.Provisioned(
            rows = KeyCustodyMatrix.rows,
            // Claimed only on PRESENT. An UNKNOWN StrongBox that the plan claimed would have
            // every later decision taken against a guarantee the key does not carry.
            strongBox = capabilities[PlatformCapability.STRONGBOX] == CapabilityState.PRESENT,
        )
    }
}

/**
 * `KeyInfo.getSecurityLevel()`'s answer set, one constant per platform constant.
 *
 * IT IS NOT A BOOLEAN, and that is the whole of residuals §2.7's fix. The platform can say
 * SOFTWARE -- "this key is not in secure hardware" -- or UNKNOWN -- "I cannot tell you which
 * level this is". They are different statements and a boolean makes them the same `false`.
 * The distinction decides whether an answer is a DENIAL the design refuses or a SILENCE it
 * records, and collapsing them forces a choice between accepting a software KEK and refusing
 * to start on a handset that denied nothing.
 *
 * The names mirror the platform's constants exactly. An invented vocabulary here is one more
 * mapping to get wrong at the one place (`AndroidKeyInfoReader`) that no test on this machine
 * can exercise.
 */
enum class KeystoreSecurityLevel {

    /** The platform states the key is NOT in secure hardware. */
    SOFTWARE,

    /** The platform declines to name a level. NOT a statement that the key is in software. */
    UNKNOWN,

    /** Secure hardware whose enclave the platform declines to name. */
    UNKNOWN_SECURE,

    TRUSTED_ENVIRONMENT,

    STRONGBOX,
    ;

    /**
     * Did the platform AFFIRM secure hardware. [UNKNOWN] is false because an unnamed level
     * rounded up to hardware is a guarantee the key does not carry.
     */
    val insideSecureHardware: Boolean
        get() = when (this) {
            TRUSTED_ENVIRONMENT, STRONGBOX, UNKNOWN_SECURE -> true
            SOFTWARE, UNKNOWN -> false
        }

    /**
     * Did the platform DENY secure hardware -- PB-SEC-1's at-rest floor, which is the
     * question `provision` refuses on.
     *
     * IT IS NOT `!insideSecureHardware`. Refusing everything that is not an affirmative
     * would refuse [UNKNOWN], and a handset that never said its key was in software would
     * then be a handset the app cannot start on. That failure mode is residuals §2.8, and
     * it is the worst outcome in this class of defect.
     *
     * Both properties are exhaustive `when`s with no `else`, so a level added later fails
     * to compile here rather than falling silently into one side.
     */
    val deniesSecureHardware: Boolean
        get() = when (this) {
            SOFTWARE -> true
            UNKNOWN, UNKNOWN_SECURE, TRUSTED_ENVIRONMENT, STRONGBOX -> false
        }
}

/**
 * The fields of android.security.keystore.KeyInfo this design depends on, lifted into a
 * plain record so the verification logic is testable off-device. The mapping from a real
 * KeyInfo is trivial and is itself only provable on a handset (PB-E2E-5).
 *
 * [insideSecureHardware] and [strongBoxBacked] are DERIVED from [securityLevel] rather than
 * stored beside it. They were separate fields, and a record can hold two fields that
 * disagree -- which for these two means claiming hardware backing the key does not have.
 */
data class KeyInfoRecord(
    val securityLevel: KeystoreSecurityLevel,
    val userAuthenticationRequired: Boolean,
    val userAuthenticationValidityDurationSeconds: Int,
    val invalidatedByBiometricEnrollment: Boolean,
) {
    val insideSecureHardware: Boolean get() = securityLevel.insideSecureHardware

    val strongBoxBacked: Boolean get() = securityLevel == KeystoreSecurityLevel.STRONGBOX
}

/** Reads back what the platform actually generated. */
interface KeyInfoReader {
    fun read(alias: String): KeyInfoRecord
}

/** Generates a Keystore key. Throws the platform's own exceptions. */
interface KeystoreProvisioner {
    fun generate(spec: KeyGenParameterSpec)
}

data class ProvisionedKey(
    val alias: String,
    val strongBoxBacked: Boolean,
    val info: KeyInfoRecord,
)

/**
 * Generate, then READ BACK. A key whose KeyInfo is never read is how a software fallback goes
 * unnoticed: the request said hardware and per-use, the platform quietly gave neither, and
 * every test above it still passes.
 */
class CustodyProvisioning(
    private val provisioner: KeystoreProvisioner,
    private val reader: KeyInfoReader,
) {
    fun provision(tier: KeyTier, strongBoxPreferred: Boolean): ProvisionedKey {
        val fallback = KeystoreSpecs.kek(tier, strongBox = false)
        val generated = if (strongBoxPreferred) {
            try {
                KeystoreSpecs.kek(tier, strongBox = true).also { provisioner.generate(it) }
            } catch (e: android.security.keystore.StrongBoxUnavailableException) {
                // A fallback, not a refusal: StrongBox is device-dependent and refusing
                // without it would refuse most handsets. What must not happen is the result
                // claiming hardware the key does not have, which the read-back below settles.
                fallback.also { provisioner.generate(it) }
            }
        } else {
            fallback.also { provisioner.generate(it) }
        }

        // GENERATE, THEN READ BACK. A key whose KeyInfo is never read is how a software
        // fallback ships unnoticed: the request said per-use and auth-required, the platform
        // quietly gave neither, and every test above this line still passes.
        //
        // What the read-back can and cannot see, said plainly: it compares the ACHIEVED
        // parameters against the ones REQUESTED, and -- for the one parameter that has no
        // requested value to compare with -- against the design's own floor. That the key is
        // really inside a TEE, or really in StrongBox, is hardware attestation on a physical
        // handset (PB-E2E-5, deferred) and is not asserted here or anywhere in this slice:
        // everything below reads the platform's REPORT, which a broken platform can lie in.
        val alias = generated.keystoreAlias
        val info = reader.read(alias)
        val downgrades = buildList {
            if (info.userAuthenticationRequired != generated.isUserAuthenticationRequired) {
                add(
                    "user authentication required is ${info.userAuthenticationRequired}, " +
                        "requested ${generated.isUserAuthenticationRequired}",
                )
            }
            if (info.userAuthenticationValidityDurationSeconds !=
                generated.userAuthenticationValidityDurationSeconds
            ) {
                add(
                    "the authorization window is ${info.userAuthenticationValidityDurationSeconds}s, " +
                        "requested ${generated.userAuthenticationValidityDurationSeconds}s",
                )
            }
            if (info.invalidatedByBiometricEnrollment != generated.isInvalidatedByBiometricEnrollment) {
                add(
                    "invalidate-on-enrollment is ${info.invalidatedByBiometricEnrollment}, " +
                        "requested ${generated.isInvalidatedByBiometricEnrollment}",
                )
            }
            if (info.strongBoxBacked && !generated.isStrongBoxBacked) {
                add("the platform reports StrongBox for a key that did not request it")
            }
            // THE ONE ENTRY THAT IS A FLOOR AND NOT A COMPARISON (residuals §2.7). Every
            // check above compares achieved against requested; this one cannot, because
            // KeyGenParameterSpec has NO "require secure hardware" setter and so there is no
            // requested value to compare with. Until this line the security level was read
            // into KeyInfoRecord and compared with nothing at all: a handset returning a
            // purely software KEK provisioned cleanly, and PB-SEC-1's at-rest claim was void
            // on that device with nothing in the product ever saying so.
            //
            // It refuses the DENIAL only. KeystoreSecurityLevel.UNKNOWN -- the platform
            // declining to name a level -- provisions and is recorded, because refusing an
            // answer that denied nothing turns this fix into residuals §2.8: an app that
            // will not start. What the user sees on a denial is DEVICE_UNSUPPORTED
            // (KeystoreDowngrade -> Recovery.REPROVISION_KEK in PhoneRuntime's table), which
            // is the honest verdict: nothing the user does to this handset fixes it.
            //
            // FOR THIS TO BE WRONG a handset we intend to support would have to report
            // SECURITY_LEVEL_SOFTWARE for a plain AES-256/GCM Keystore key at the pinned
            // minSdk. Nothing here claims that has been observed either way -- PB-E2E-5 is
            // deferred (ADR-007 B31) -- but a device that does report it is now a device
            // that says so out loud instead of one that provisions in silence.
            if (info.securityLevel.deniesSecureHardware) {
                add(
                    "the platform reports ${info.securityLevel}: this key is not in secure " +
                        "hardware, so everything sealed under it is protected by software alone",
                )
            }
        }
        if (downgrades.isNotEmpty()) {
            throw KeyCustodyException.KeystoreDowngrade(alias, downgrades.joinToString("; "))
        }

        return ProvisionedKey(
            alias = alias,
            // From the READ-BACK, never from the request. Reporting hardware backing the key
            // does not have is worse than not having it, because every later decision is
            // taken against the claim.
            strongBoxBacked = info.strongBoxBacked,
            info = info,
        )
    }
}

/**
 * The KeyGenParameterSpec each tier is requested with. Real specs, so the Robolectric test
 * asserts the actual API calls rather than a parallel description of them.
 */
object KeystoreSpecs {

    private const val AES_KEY_SIZE = 256

    private fun aesGcm(alias: String, strongBox: Boolean): KeyGenParameterSpec.Builder =
        KeyGenParameterSpec.Builder(
            alias,
            KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT,
        )
            .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
            .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
            .setKeySize(AES_KEY_SIZE)
            .setIsStrongBoxBacked(strongBox)

    /**
     * The per-tier AES KEK of ADR-007 B8.
     *
     * The CONTENT KEK is the gate. Without `setUserAuthenticationRequired(true)` every other
     * assertion in this slice is theatre: the unwrap would succeed on a locked handset and
     * PB-KEY-2's split would be enforced by nothing at all.
     *
     * The WAKE KEK is the opposite by design, and both flags matter. Requiring authentication
     * makes push useless in exactly the state it exists for; and
     * `setUnlockedDeviceRequired(true)` -- which looks like prudence -- kills it just as dead,
     * because it demands the device be unlocked NOW, which a push on a locked handset never
     * is. It is left at its default of false rather than set, so nothing has to be un-set later.
     */
    fun kek(tier: KeyTier, strongBox: Boolean): KeyGenParameterSpec {
        val builder = aesGcm(KeystoreAliases.forTier(tier), strongBox)
        return when (tier) {
            KeyTier.CONTENT -> builder
                .setUserAuthenticationRequired(true)
                .setUserAuthenticationParameters(
                    BiometricPolicy.TIMED_WINDOW_SECONDS,
                    KeyProperties.AUTH_BIOMETRIC_STRONG,
                )
                .setInvalidatedByBiometricEnrollment(true)
                .build()

            KeyTier.WAKE -> builder
                .setUserAuthenticationRequired(false)
                // Explicit, not inherited: the Builder defaults this to TRUE, so a re-enrolled
                // fingerprint would destroy the wake KEK and the user would lose background
                // wakes with no error anywhere, recoverable only by re-pairing.
                .setInvalidatedByBiometricEnrollment(false)
                .build()
        }
    }

    /**
     * The spec for a key that authorizes [operation]. Per-use operations differ from timed
     * ones by more than an integer -- see AuthorizationSpec.requiresCryptoObject -- and the
     * difference is carried HERE by the alias: per-use gets one entry each, so a single
     * CryptoObject authorization cannot be pointed at whichever operation the caller picks.
     */
    fun forOperation(operation: GatedOperation, strongBox: Boolean): KeyGenParameterSpec =
        aesGcm(KeystoreAliases.forOperation(operation), strongBox)
            .setUserAuthenticationRequired(true)
            .setUserAuthenticationParameters(
                BiometricPolicy.specFor(operation).timeoutSeconds,
                KeyProperties.AUTH_BIOMETRIC_STRONG,
            )
            .setInvalidatedByBiometricEnrollment(true)
            .build()
}

/**
 * PB-KEY-6's Android half that IS in scope: the platform's failure surface becomes typed
 * failures, totally, so no caller can mistake a refusal for an empty result.
 */
object PlatformFailure {
    /**
     * The mapping branches on the EXACT platform types, never on their supertype. Both
     * `KeyPermanentlyInvalidatedException` and `UserNotAuthenticatedException` extend
     * `java.security.InvalidKeyException`, which is what the Cipher and Signature APIs
     * declare -- so branching on the supertype is the natural thing to write and it collapses
     * a permanent invalidation into "prompt again", producing an endless prompt against a key
     * that no longer exists.
     */
    fun map(tier: KeyTier, cause: Throwable): KeyCustodyException = when (cause) {
        is android.security.keystore.KeyPermanentlyInvalidatedException ->
            KeyCustodyException.KeyPermanentlyInvalidated(tier)

        is android.security.keystore.UserNotAuthenticatedException ->
            KeyCustodyException.UserAuthenticationRequired(tier)

        is java.security.UnrecoverableKeyException ->
            KeyCustodyException.KeystoreKeyMissing(KeystoreAliases.forTier(tier))

        is java.security.KeyStoreException ->
            KeyCustodyException.KeystoreKeyMissing(KeystoreAliases.forTier(tier))

        // Anything else is a BUG, not a state the user can act on. Reporting it as "the user
        // needs to authenticate" produces a prompt loop over a defect.
        else -> KeyCustodyException.Unexpected(
            tier,
            "${cause.javaClass.name}: ${cause.message ?: "no message"}",
        )
    }
}
