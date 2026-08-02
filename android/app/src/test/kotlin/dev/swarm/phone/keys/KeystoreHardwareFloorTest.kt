package dev.swarm.phone.keys

import android.security.keystore.KeyGenParameterSpec
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST tests for residuals §2.7: the Keystore read-back never compared
 * `insideSecureHardware` against anything.
 *
 * THE DEFECT. [CustodyProvisioning.provision] generates, reads `KeyInfo` back and refuses a
 * downgrade -- but its downgrade list compared only `userAuthenticationRequired`, the validity
 * window, `invalidatedByBiometricEnrollment` and a spuriously-reported StrongBox. The security
 * level went into [KeyInfoRecord] and was compared with nothing, so a handset returning a
 * purely software KEK provisioned cleanly and nothing objected. PB-SEC-1's at-rest claim was
 * void on such a device and no part of the product would ever say so.
 *
 * WHY IT IS NOT SIMPLY A MISSING COMPARISON. `KeyGenParameterSpec` has no "require secure
 * hardware" setter, so there is no REQUESTED value to compare the achieved one against. The
 * answer is therefore a FLOOR the design asserts, not a request the platform agreed to -- and
 * a floor has to say what it does with an answer that is neither yes nor no.
 *
 * THE THREE-WAY ANSWER IS THE WHOLE DESIGN. `KeyInfo.getSecurityLevel()` can say SOFTWARE
 * ("this key is not in secure hardware"), or UNKNOWN ("I cannot tell you which level this
 * is"). Those are different statements, and the old boolean collapsed them into the same
 * `false`. Refusing on the collapsed boolean would refuse a handset that never denied secure
 * hardware -- the failure mode residuals §2.8 exists about, an app that will not start.
 * So the floor refuses the DENIAL and records the SILENCE.
 *
 * WHAT WOULD MAKE THIS WRONG ON A REAL DEVICE. A handset we intend to support would have to
 * report SECURITY_LEVEL_SOFTWARE for a plain AES-256/GCM Keystore key at the pinned minSdk.
 * If that is ever observed, the refusal is the thing to revisit -- but it must be revisited
 * deliberately, which is the difference between this and the silent acceptance it replaces.
 *
 * NOTHING HERE IS A HARDWARE CLAIM. That the key is really in a TEE, that StrongBox behaves
 * as advertised, that a real biometric gates anything: PB-E2E-5, the deferred physical-handset
 * gate (ADR-007 B31). These tests assert only what the code does with the answer the platform
 * hands it, over a fake that hands it whatever the case under test names.
 *
 * RED before the fix: [KeystoreSecurityLevel] does not exist, so this file does not compile --
 * the accepted compile-fail RED for a new API, unambiguous by name.
 */
@RunWith(RobolectricTestRunner::class)
class KeystoreHardwareFloorTest {

    private class FixedProvisioner : KeystoreProvisioner {
        override fun generate(spec: KeyGenParameterSpec) = Unit
    }

    private class FixedReader(private val record: KeyInfoRecord) : KeyInfoReader {
        override fun read(alias: String): KeyInfoRecord = record
    }

    /** A read-back that is faithful to [spec] in every respect EXCEPT the security level. */
    private fun achieved(spec: KeyGenParameterSpec, level: KeystoreSecurityLevel) = KeyInfoRecord(
        securityLevel = level,
        userAuthenticationRequired = spec.isUserAuthenticationRequired,
        userAuthenticationValidityDurationSeconds = spec.userAuthenticationValidityDurationSeconds,
        invalidatedByBiometricEnrollment = spec.isInvalidatedByBiometricEnrollment,
    )

    /**
     * A StrongBox read-back must follow a StrongBox REQUEST. Otherwise the pre-existing
     * "reports StrongBox for a key that did not request it" downgrade fires and the level
     * under test never reaches the floor at all -- which is that fence working, not this one.
     */
    private fun provision(tier: KeyTier, level: KeystoreSecurityLevel): ProvisionedKey {
        val strongBox = level == KeystoreSecurityLevel.STRONGBOX
        val requested = KeystoreSpecs.kek(tier, strongBox = strongBox)
        return CustodyProvisioning(FixedProvisioner(), FixedReader(achieved(requested, level)))
            .provision(tier, strongBoxPreferred = strongBox)
    }

    /**
     * The defect itself. A platform that states plainly that this KEK is not in secure
     * hardware must not be allowed to provision it silently: everything sealed under it --
     * the two state KEKs, and through them `device.key` and `phone-state.json` -- is then
     * protected by software alone, which is not what PB-SEC-1 claims.
     */
    @Test
    fun a_kek_the_platform_says_is_not_in_secure_hardware_is_refused() {
        for (tier in KeyTier.entries) {
            val refusal = assertThrows(
                "the platform reported SECURITY_LEVEL_SOFTWARE for the $tier KEK and " +
                    "provisioning accepted it; PB-SEC-1's at-rest claim is void on such a " +
                    "handset and nothing else in the product will ever say so",
                KeyCustodyException::class.java,
            ) { provision(tier, KeystoreSecurityLevel.SOFTWARE) }

            assertTrue(
                "the refusal must name the level the platform reported, or the bug report " +
                    "says only that something was weaker: ${refusal.message}",
                refusal.message!!.contains("SOFTWARE"),
            )
        }
    }

    /**
     * And the other half, which is what stops this fix becoming residuals §2.8 in a new
     * place. SECURITY_LEVEL_UNKNOWN is the platform declining to name a level -- it is not a
     * statement that the key is in software. Refusing it would brick a handset that denied
     * nothing, and a phone that cannot start is the worst outcome in this class of defect.
     *
     * The silence must still be REPRESENTABLE rather than rounded up: `insideSecureHardware`
     * is false for it, so any consumer that asks the affirmative question fails closed
     * without the app refusing to run.
     */
    @Test
    fun a_kek_whose_level_the_platform_declines_to_name_provisions_and_claims_nothing() {
        val key = provision(KeyTier.CONTENT, KeystoreSecurityLevel.UNKNOWN)

        assertEquals(KeystoreSecurityLevel.UNKNOWN, key.info.securityLevel)
        assertFalse(
            "an unnamed level was rounded up to secure hardware; the app would then hold a " +
                "guarantee the key does not carry",
            key.info.insideSecureHardware,
        )
        assertFalse("an unnamed level is not StrongBox", key.strongBoxBacked)
    }

    /** The affirmative answers provision, or every refusal above is a refusal of everything. */
    @Test
    fun every_level_that_affirms_secure_hardware_provisions() {
        val affirmative = listOf(
            KeystoreSecurityLevel.TRUSTED_ENVIRONMENT,
            KeystoreSecurityLevel.STRONGBOX,
            KeystoreSecurityLevel.UNKNOWN_SECURE,
        )
        for (level in affirmative) {
            val key = provision(KeyTier.CONTENT, level)
            assertEquals(level, key.info.securityLevel)
            assertTrue("$level affirms secure hardware", key.info.insideSecureHardware)
        }
        assertEquals(
            "StrongBox is the only level that IS StrongBox",
            KeystoreSecurityLevel.STRONGBOX,
            provision(KeyTier.CONTENT, KeystoreSecurityLevel.STRONGBOX).info.securityLevel,
        )
    }

    /**
     * The floor, stated TOTALLY over the platform's answer set. Written this way because the
     * two ways to get it wrong are opposite and both look reasonable in isolation: refuse
     * nothing (the defect) or refuse everything that is not an affirmative (an app that will
     * not start on a handset that denied nothing). Iterating the enum is what makes a later
     * edit to either side fail here rather than on a device.
     */
    @Test
    fun the_at_rest_floor_refuses_exactly_the_level_that_denies_secure_hardware() {
        for (level in KeystoreSecurityLevel.entries) {
            val denied = level == KeystoreSecurityLevel.SOFTWARE
            val refused = runCatching { provision(KeyTier.CONTENT, level) }.isFailure

            assertEquals(
                if (denied) {
                    "$level is the platform DENYING secure hardware and it provisioned anyway"
                } else {
                    "$level is not a denial of secure hardware, and refusing it makes the app " +
                        "unable to start on a handset that never said its key was in software"
                },
                denied,
                refused,
            )
        }
    }

    /**
     * The floor is a floor and not a substitute for the request comparison. A software-level
     * answer must not be the only thing checked, nor must adding it have displaced the
     * disagreements that were already caught.
     *
     * THE DOWNGRADE AXIS CHANGED WITH ADR-007 B133 AND THE TEST DID NOT LOSE ITS SUBJECT. It
     * used to weaken `userAuthenticationRequired` to false against a spec that requested TRUE;
     * the spec now requests FALSE, so that mutation is no longer a disagreement at all and the
     * test would have passed against a `readBack` that compared nothing. Both axes below are
     * disagreements the platform can still produce, both are invisible to the security-level
     * floor -- TRUSTED_ENVIRONMENT and STRONGBOX are affirmative answers -- and both matter:
     *
     *  - a SPOOFED StrongBox claim on a key that did not request it is the plan holding a
     *    guarantee the key does not carry, which every later decision is then taken against;
     *  - an enrollment invalidation the spec explicitly turned OFF is a KEK that a re-enrolled
     *    fingerprint destroys, over a biometric this design no longer uses for anything. The
     *    only way back from that is a re-pair.
     */
    @Test
    fun the_floor_does_not_replace_the_requested_versus_achieved_comparison() {
        val requested = KeystoreSpecs.kek(KeyTier.CONTENT, strongBox = false)
        val disagreements = mapOf(
            "claimed StrongBox for a key that did not request it" to
                achieved(requested, KeystoreSecurityLevel.STRONGBOX),
            "re-armed the enrollment invalidation the spec turned off" to
                achieved(requested, KeystoreSecurityLevel.TRUSTED_ENVIRONMENT)
                    .copy(invalidatedByBiometricEnrollment = true),
        )

        for ((what, weakened) in disagreements) {
            assertThrows(
                "the platform $what and provisioning accepted it. The level it reported " +
                    "AFFIRMS secure hardware, so the floor says nothing here: the requested " +
                    "versus achieved comparison has to be additional to it, not instead of it",
                KeyCustodyException::class.java,
            ) {
                CustodyProvisioning(FixedProvisioner(), FixedReader(weakened))
                    .provision(KeyTier.CONTENT, strongBoxPreferred = false)
            }
        }
    }
}
