package dev.swarm.phone.keys

import android.security.keystore.KeyGenParameterSpec

/**
 * PB-KEY-8 and the requesting half of PB-SEC-2. SCAFFOLDING ONLY.
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
    fun forDevice(capabilities: Map<PlatformCapability, CapabilityState>): CustodyPlan =
        TODO("PB-KEY-8: plan or refuse against the handset's real capabilities")
}

/**
 * The fields of android.security.keystore.KeyInfo this design depends on, lifted into a
 * plain record so the verification logic is testable off-device. The mapping from a real
 * KeyInfo is trivial and is itself only provable on a handset (PB-E2E-5).
 */
data class KeyInfoRecord(
    val insideSecureHardware: Boolean,
    val strongBoxBacked: Boolean,
    val userAuthenticationRequired: Boolean,
    val userAuthenticationValidityDurationSeconds: Int,
    val invalidatedByBiometricEnrollment: Boolean,
)

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
    fun provision(tier: KeyTier, strongBoxPreferred: Boolean): ProvisionedKey =
        TODO("PB-KEY-8: generate the $tier KEK, read KeyInfo back, fail closed on a downgrade")
}

/**
 * The KeyGenParameterSpec each tier is requested with. Real specs, so the Robolectric test
 * asserts the actual API calls rather than a parallel description of them.
 */
object KeystoreSpecs {

    /** The per-tier AES KEK of ADR-007 B8. */
    fun kek(tier: KeyTier, strongBox: Boolean): KeyGenParameterSpec =
        TODO("PB-SEC-2: the $tier KEK spec")

    /**
     * The spec for a key that authorizes [operation]. Per-use operations differ from timed
     * ones by more than an integer -- see AuthorizationSpec.requiresCryptoObject.
     */
    fun forOperation(operation: GatedOperation, strongBox: Boolean): KeyGenParameterSpec =
        TODO("PB-SEC-2: the spec authorizing $operation")
}

/**
 * PB-KEY-6's Android half that IS in scope: the platform's failure surface becomes typed
 * failures, totally, so no caller can mistake a refusal for an empty result.
 */
object PlatformFailure {
    fun map(tier: KeyTier, cause: Throwable): KeyCustodyException =
        TODO("PB-KEY-6: map $cause for $tier onto a typed custody failure")
}
