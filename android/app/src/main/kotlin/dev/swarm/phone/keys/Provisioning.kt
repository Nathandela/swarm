package dev.swarm.phone.keys

import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties

/**
 * PB-KEY-8: what this handset must be able to do, what is REQUESTED of its Keystore, and what
 * is checked on the way back.
 *
 * What is testable here and what is NOT, stated up front because §10's honesty clause makes
 * the difference load-bearing:
 *
 *  - TESTABLE (Robolectric): that the code REQUESTS the right KeyGenParameterSpec -- no
 *    authenticator, no invalidate-on-enrollment, StrongBox preference and its fallback -- and
 *    that it VERIFIES what it got back and fails closed on a downgrade.
 *  - NOT TESTABLE HERE: that the achieved key is really hardware-backed, or that StrongBox
 *    behaves as advertised. Those are PB-E2E-5, the deferred physical-handset gate. Nothing in
 *    this file may be read as covering them.
 *
 * PB-SEC-2 IS VOID (ADR-007 B133) and this file no longer requests anything on its behalf.
 */

/**
 * What a handset must be able to do for a role.
 *
 * `USER_AUTH_PER_USE` left this enum with PB-SEC-2 (ADR-007 B133). Dropping an authenticator
 * from PB-KEY-8's matrix is a NARROWING, which B8 permits; the entry is removed rather than
 * left unconsumed, because a capability nothing asks for is a refusal waiting to be re-armed
 * by whoever next reads the list as a to-do.
 */
enum class PlatformCapability {
    KEYSTORE_X25519,
    KEYSTORE_ED25519,
    KEYSTORE_AES_GCM,
    STRONGBOX,
}

/**
 * Three states, not two. A probe that could not answer is UNKNOWN, and UNKNOWN must fail
 * closed -- "assume present" is how a software fallback ships unnoticed.
 */
enum class CapabilityState { PRESENT, ABSENT, UNKNOWN }

/**
 * A capability the handset did not confirm that NO row consumes.
 *
 * It is recorded and not refused. At the pinned minSdk the platform is meant to offer it, so
 * a non-PRESENT answer means a Keystore not behaving as its API level promises -- worth
 * knowing, and not worth an app that will not start (residuals §2.8).
 */
data class CapabilityAnomaly(
    val capability: PlatformCapability,
    val state: CapabilityState,
)

sealed class CustodyPlan {
    data class Provisioned(
        val rows: Map<KeyRole, CustodyRow>,
        val strongBox: Boolean,
        /** Non-PRESENT answers that did not stop provisioning. Empty on a capable handset. */
        val anomalies: List<CapabilityAnomaly>,
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
     * The capabilities the design CONSUMES, each mapped to the role that most needs it, so
     * PB-KEY-8's "defined refusal" can name a role rather than shrug. Non-PRESENT on any of
     * them is a refusal, and UNKNOWN fails closed exactly as ABSENT does.
     *
     * THE SET IS NOW ONE ROW (ADR-007 B133). USER_AUTH_PER_USE was the second, and the handset
     * B132 met is the reason its removal matters rather than being bookkeeping: the design no
     * longer asks Keystore for an authenticator anywhere, so refusing a phone over one would be
     * an app that will not start for a capability nothing uses -- residuals §2.8, which is what
     * moved the Curve25519 entries to [canaries] for exactly the same reason.
     *
     * WHY THE CURVE25519 ENTRIES ARE GONE (residuals §2.8). KEYSTORE_X25519 and
     * KEYSTORE_ED25519 used to be here, and refusing over them refused a handset over
     * something the design never asks for: ADR-007 B17(a) makes every row KEYSTORE_WRAPPED,
     * so the X25519/Ed25519 private halves live in the Go core and Keystore is asked only for
     * the AES-GCM KEK that seals them. The argument for keeping them was a canary -- at the
     * pinned minSdk both are meant to be present, so a non-PRESENT answer means a Keystore
     * misbehaving -- and that argument is intact, but its price was not: on such a handset the
     * app would not provision AT ALL, over a capability nothing uses. So the canary moved to
     * [canaries], which records the same answer without being able to stop the app starting.
     *
     * If a row ever becomes KEYSTORE_NATIVE, its algorithm belongs back in this map -- and
     * KeyCustodyMatrixTest derives the consumed set from the matrix so that omission fails
     * a test rather than shipping.
     *
     * STRONGBOX is deliberately in neither list: its absence is a fallback, not a refusal,
     * because it is device-dependent and refusing without it would refuse most handsets.
     */
    private val required = linkedMapOf(
        PlatformCapability.KEYSTORE_AES_GCM to KeyRole.RELAY_AUTH,
    )

    /**
     * Probed, recorded, never fatal: capabilities the platform guarantees at the pinned
     * minSdk that no row consumes. A non-PRESENT answer here says something is wrong with
     * this Keystore, so it is carried on the plan rather than dropped -- but it cannot refuse
     * a phone, because the design does not use it.
     */
    private val canaries = listOf(
        PlatformCapability.KEYSTORE_X25519,
        PlatformCapability.KEYSTORE_ED25519,
    )

    fun forDevice(capabilities: Map<PlatformCapability, CapabilityState>): CustodyPlan {
        for ((capability, role) in required) {
            if (stateOf(capabilities, capability) == CapabilityState.PRESENT) continue
            return CustodyPlan.Refused(
                role = role,
                capability = capability,
                reason = "this handset reports $capability as " +
                    "${stateOf(capabilities, capability)}. $role needs it, and a silent " +
                    "downgrade to software-only custody is what PB-KEY-8 forbids: UNKNOWN is " +
                    "a probe that could not answer, which fails closed exactly as ABSENT does.",
            )
        }
        return CustodyPlan.Provisioned(
            rows = KeyCustodyMatrix.rows,
            // Claimed only on PRESENT. An UNKNOWN StrongBox that the plan claimed would have
            // every later decision taken against a guarantee the key does not carry.
            strongBox = capabilities[PlatformCapability.STRONGBOX] == CapabilityState.PRESENT,
            anomalies = canaries.mapNotNull { capability ->
                val state = stateOf(capabilities, capability)
                if (state == CapabilityState.PRESENT) null else CapabilityAnomaly(capability, state)
            },
        )
    }

    /**
     * A capability the probe never reported is UNKNOWN, not PRESENT. Defaulting an absent map
     * entry to present is the same defect as treating UNKNOWN as present, one layer down.
     */
    private fun stateOf(
        capabilities: Map<PlatformCapability, CapabilityState>,
        capability: PlatformCapability,
    ): CapabilityState = capabilities[capability] ?: CapabilityState.UNKNOWN
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
    /**
     * Requested as FALSE (ADR-007 B133) and still read back, because the comparison's direction
     * inverted rather than losing its point: what must not happen now is the platform silently
     * ADDING an authenticator to a key nothing in this app can ever satisfy.
     */
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

        return readBack(generated)
    }

    /**
     * The achieved parameters against the requested ones.
     *
     * IT IS STILL A SEPARATE FUNCTION FROM [provision] although only one path reaches it now.
     * `provisionGate` -- PB-SEC-2's per-use CryptoObject entries -- was the second caller and
     * left with the requirement (ADR-007 B133). The split stays because the read-back is the
     * thing this class exists for, and a table inlined into its one caller is a table the next
     * provisioning path can be written without.
     */
    private fun readBack(generated: KeyGenParameterSpec): ProvisionedKey {
        // GENERATE, THEN READ BACK. A key whose KeyInfo is never read is how a software
        // fallback ships unnoticed: the request said hardware, the platform quietly gave
        // software, and every test above this line still passes.
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
            // (KeystoreDowngrade -> DEVICE_UNSUPPORTED in PhoneRuntime's table), which
            // is the honest verdict: nothing the user does to this handset fixes it. It is the
            // AT-REST floor and is explicitly KEPT by ADR-007 B133 -- non-exportability defends
            // an extracted data directory, which is a wire-side concern and not a holder-side
            // one.
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
     * NEITHER TIER ASKS FOR AN AUTHENTICATOR (ADR-007 B133). The CONTENT KEK used to carry
     * `setUserAuthenticationParameters(60, AUTH_BIOMETRIC_STRONG)` and was the gate: the unwrap
     * refused unless the user had passed a Class-3 biometric inside the window. The trust
     * boundary is now the WIRE, the phone endpoint is trusted the way the Mac's owner-uid user
     * always has been, and the flag is the whole cost of that decision.
     *
     * WHAT IS DELIBERATELY UNCHANGED, because a strict reading of "remove the gate" would take
     * it too. The key is still generated INSIDE Keystore, still hardware-backed, still
     * non-exportable, and StrongBox is still preferred with a fallback ([aesGcm] carries all of
     * it and [CustodyProvisioning.readBack] refuses a key the platform says is in software).
     * None of that is about the person holding the handset: it is what makes a copy of the app's
     * data directory -- taken by backup, by `run-as`, or off a disk -- unopenable anywhere else.
     * B133 keeps it explicitly.
     *
     * BOTH FLAGS BELOW ARE SET RATHER THAN INHERITED. `setInvalidatedByBiometricEnrollment`
     * defaults to TRUE, and leaving it there would let a re-enrolled fingerprint destroy a KEK
     * that no longer has anything to do with fingerprints: the content tier would become
     * unopenable, and the wake tier would lose background wakes with no error anywhere,
     * recoverable in both cases only by re-pairing.
     *
     * `setUnlockedDeviceRequired` is NOT set, and that has always been deliberate for the wake
     * tier: it looks like prudence and demands the device be unlocked NOW, which a push on a
     * locked handset never is. It is left at its default of false rather than set, so nothing
     * has to be un-set later.
     *
     * THE TIERS ARE STILL TWO KEYS. They share a spec and not an alias, which is what keeps a
     * purge, a discard or a platform invalidation of one from taking the other -- see
     * [KeystoreAliases].
     */
    fun kek(tier: KeyTier, strongBox: Boolean): KeyGenParameterSpec =
        aesGcm(KeystoreAliases.forTier(tier), strongBox)
            .setUserAuthenticationRequired(false)
            .setInvalidatedByBiometricEnrollment(false)
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

        // KEPT although nothing this build provisions can raise it (ADR-007 B133). An install
        // made before that change still holds an AUTH_BIOMETRIC_STRONG content KEK, and the
        // alternative to naming that refusal is [KeyCustodyException.Unexpected] -- which
        // `phonecore.openSealedDeviceKeys` treats as a Resume it must refuse outright, turning
        // an upgraded handset into an app that will not start with nothing on screen saying why.
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
