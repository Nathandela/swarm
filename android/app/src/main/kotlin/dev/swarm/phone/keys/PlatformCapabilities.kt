package dev.swarm.phone.keys

import android.os.Build
import java.security.KeyPairGenerator
import java.security.NoSuchAlgorithmException
import java.security.NoSuchProviderException
import javax.crypto.KeyGenerator

/**
 * What this handset answers to the questions [CustodyPlanner] asks.
 *
 * WHY IT EXISTS. `CustodyPlanner.forDevice` had no production caller: [dev.swarm.phone.PhoneRuntime] went
 * straight to [KeystoreCustodyBootstrap] without ever building a capability map, so PB-KEY-8's
 * defined refusal was a function nothing invoked and
 * [KeyCustodyException.PlatformCapabilityMissing] was declared, routed, and never thrown
 * (residuals §2.10(a)). The planner was never the missing piece; the question was.
 *
 * WHAT IT IS ALLOWED TO REFUSE, because getting this wrong is the worst outcome in this class.
 * The consumed set is {KEYSTORE_AES_GCM, USER_AUTH_PER_USE} and nothing else -- the Curve25519
 * entries are canaries that are recorded and never fatal (residuals §2.8). So the probe below
 * is written to make a non-PRESENT answer for the two consumed capabilities MEAN something:
 *
 *  - KEYSTORE_AES_GCM asks the real provider for the real generator. Every custody blob in the
 *    design is wrapped under an AES-GCM Keystore KEK (ADR-007 B8), so a handset that answers
 *    non-PRESENT here could not have provisioned anyway -- `KeystoreCustodyBootstrap.ensure`
 *    would have failed a few lines later with a platform exception routed as INTERNAL. The gate
 *    does not cost such a handset a working app; it replaces an opaque refusal with a named one.
 *  - USER_AUTH_PER_USE is an API-LEVEL fact, not an enrollment one, and the distinction is the
 *    whole safety argument. `setUserAuthenticationParameters(timeout, type)` -- the call that
 *    expresses per-use versus timed at all -- landed in API 30, and PB-RUN-1 pins minSdk above
 *    it. Probing the user's ENROLLED biometrics instead (BiometricManager) would refuse every
 *    handset with no fingerprint registered, which is an app that will not start for a reason
 *    the user could fix but is never told about.
 *
 * The split into [KeystoreAlgorithms] is the same one [KeyInfoReader] already uses: the
 * platform call is a thin adapter nothing on this machine can exercise, and the POLICY over its
 * answers is a plain object a JVM test drives.
 */
class DeviceCapabilities(
    private val sdkInt: Int,
    private val strongBox: Boolean,
    private val algorithms: KeystoreAlgorithms,
) {

    /**
     * EVERY capability, always. `CustodyPlanner.stateOf` reads a missing entry as UNKNOWN and
     * UNKNOWN fails closed, so an unanswered capability that later joins the consumed set is an
     * app that refuses to start on every handset. `DeviceCapabilitiesTest` holds this map to
     * the enum for exactly that reason.
     */
    fun probe(): Map<PlatformCapability, CapabilityState> = mapOf(
        PlatformCapability.KEYSTORE_AES_GCM to algorithms.secretKey(AES),
        PlatformCapability.USER_AUTH_PER_USE to stateOf(sdkInt >= PER_USE_AUTH_API),
        PlatformCapability.STRONGBOX to stateOf(strongBox),

        // The canaries. Non-PRESENT here is recorded on the plan and refuses nothing: no matrix
        // row consumes Curve25519, because every role is KEYSTORE_WRAPPED and the private
        // halves live in the Go core (ADR-007 B17(a)).
        PlatformCapability.KEYSTORE_X25519 to algorithms.keyPair(XDH),
        PlatformCapability.KEYSTORE_ED25519 to algorithms.keyPair(ED25519),
    )

    private fun stateOf(present: Boolean) =
        if (present) CapabilityState.PRESENT else CapabilityState.ABSENT

    private companion object {
        const val AES = "AES"
        const val XDH = "XDH"
        const val ED25519 = "Ed25519"

        /** `setUserAuthenticationParameters(timeout, type)`, matching CustodyRow.requiresApi. */
        const val PER_USE_AUTH_API = Build.VERSION_CODES.R
    }
}

/** Asks the platform's Keystore provider what it can generate. */
interface KeystoreAlgorithms {

    /** A symmetric key -- the tier KEKs. */
    fun secretKey(algorithm: String): CapabilityState

    /** An asymmetric pair. Only the canaries are asked for these today. */
    fun keyPair(algorithm: String): CapabilityState
}

/**
 * The real provider, asked the way the provisioning path asks it.
 *
 * IT ANSWERS AND NEVER THROWS. This runs on the startup path before anything has a screen: an
 * escaping `NoSuchProviderException` would take the process out before the routing table
 * could say a word, which is the failure the whole failable-startup design exists to prevent.
 *
 * ABSENT AND UNKNOWN ARE KEPT APART, in the direction [CapabilityState] documents. The two
 * "this provider does not offer it" exceptions are a DENIAL; anything else is a platform doing
 * something unexpected, which is a silence. Both fail closed for a consumed capability, and
 * only the distinction survives into a [CapabilityAnomaly] a person reads.
 */
class AndroidKeystoreAlgorithms : KeystoreAlgorithms {

    override fun secretKey(algorithm: String): CapabilityState =
        answer { KeyGenerator.getInstance(algorithm, ANDROID_KEYSTORE) }

    override fun keyPair(algorithm: String): CapabilityState =
        answer { KeyPairGenerator.getInstance(algorithm, ANDROID_KEYSTORE) }

    private fun answer(ask: () -> Any): CapabilityState = try {
        ask()
        CapabilityState.PRESENT
    } catch (denied: NoSuchAlgorithmException) {
        CapabilityState.ABSENT
    } catch (denied: NoSuchProviderException) {
        CapabilityState.ABSENT
    } catch (silent: Throwable) {
        // `Throwable`, for the reason PhoneRuntime.attach gives: a Keystore that cannot load its
        // own provider raises an Error, and a probe is the one place that must survive it.
        CapabilityState.UNKNOWN
    }

    private companion object {
        const val ANDROID_KEYSTORE = "AndroidKeyStore"
    }
}
