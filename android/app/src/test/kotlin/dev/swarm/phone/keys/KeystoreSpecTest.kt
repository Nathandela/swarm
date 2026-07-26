package dev.swarm.phone.keys

import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyPermanentlyInvalidatedException
import android.security.keystore.KeyProperties
import android.security.keystore.UserNotAuthenticatedException
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import java.security.KeyStoreException
import java.security.UnrecoverableKeyException

/**
 * PB-SEC-2's requesting half and PB-KEY-8's verifying half.
 *
 * THE HARD LIMIT, stated first because everything below sits just under it. Real biometric
 * prompts, hardware-backed Keystore attestation and real TEE/StrongBox behaviour cannot be
 * exercised on an emulator or in Robolectric. They belong to PB-E2E-5, the physical-handset
 * gate that §13 records as explicitly deferred. Nothing here may be read as covering them.
 *
 * What IS asserted, and why it is worth asserting:
 *   - that the code REQUESTS the right KeyGenParameterSpec. These are real specs built by the
 *     real Builder and read back through the real getters, so the assertion is on the API call
 *     the device will receive -- not on a parallel description of it that can drift.
 *   - that the code VERIFIES what it got back through KeyInfo and fails closed on a downgrade.
 *     A key whose KeyInfo is never read is exactly how a software fallback ships unnoticed.
 *   - that the platform's exception surface becomes typed failures, totally.
 *
 * Robolectric rather than plain JVM because android.security.keystore is Android's; §10's tier
 * order puts this one rung above the JVM tests and no higher.
 */
@RunWith(RobolectricTestRunner::class)
class KeystoreSpecTest {

    // --- the KEK per tier ---------------------------------------------------

    /**
     * The content KEK is the gate. If it does not require user authentication, every other
     * assertion in this slice is theatre: the unwrap would succeed on a locked handset and
     * PB-KEY-2's split would be enforced by nothing at all.
     */
    @Test
    fun the_content_kek_requires_a_strong_biometric() {
        val spec = KeystoreSpecs.kek(KeyTier.CONTENT, strongBox = false)

        assertTrue("the content KEK is the gate; without this it is a UI boolean", spec.isUserAuthenticationRequired)
        assertTrue(
            "the content KEK must accept a strong biometric (authType was ${spec.userAuthenticationType})",
            (spec.userAuthenticationType and KeyProperties.AUTH_BIOMETRIC_STRONG) != 0,
        )
        assertEquals(KeystoreAliases.forTier(KeyTier.CONTENT), spec.keystoreAlias)
    }

    /** PB-SEC-2 requires invalidation on biometric-enrollment change, and this is the flag. */
    @Test
    fun the_content_kek_is_invalidated_by_a_biometric_enrollment_change() {
        assertTrue(KeystoreSpecs.kek(KeyTier.CONTENT, strongBox = false).isInvalidatedByBiometricEnrollment)
    }

    /**
     * ADR-007 B16 makes a high-priority FCM push the SOLE background wake path, and B9 puts
     * RelayAuth in the wake tier so background reconnect works on a locked handset. Both die
     * if the wake KEK requires authentication -- and `setUnlockedDeviceRequired(true)` kills
     * them just as dead while looking like prudence, because it demands the device be unlocked
     * NOW, which a push on a locked handset never is.
     */
    @Test
    fun the_wake_kek_works_on_a_locked_handset() {
        val spec = KeystoreSpecs.kek(KeyTier.WAKE, strongBox = false)

        assertFalse(
            "a wake key behind a biometric makes push useless in exactly the state it exists for",
            spec.isUserAuthenticationRequired,
        )
        assertFalse(
            "setUnlockedDeviceRequired(true) on the wake KEK means no push can be opened " +
                "while the screen is locked",
            spec.isUnlockedDeviceRequired,
        )
        assertEquals(KeystoreAliases.forTier(KeyTier.WAKE), spec.keystoreAlias)
    }

    /**
     * And a re-enrolled fingerprint must not take the wake path with it: the user would lose
     * background wakes with no error anywhere, recoverable only by re-pairing.
     */
    @Test
    fun the_wake_kek_survives_a_biometric_enrollment_change() {
        assertFalse(KeystoreSpecs.kek(KeyTier.WAKE, strongBox = false).isInvalidatedByBiometricEnrollment)
    }

    @Test
    fun the_two_tier_keks_are_distinct_keystore_entries() {
        assertNotEquals(
            KeystoreSpecs.kek(KeyTier.WAKE, strongBox = false).keystoreAlias,
            KeystoreSpecs.kek(KeyTier.CONTENT, strongBox = false).keystoreAlias,
        )
    }

    // --- per-operation specs ------------------------------------------------

    /**
     * 6.0's freshness row, expressed in the parameter the platform actually reads. Per-use is
     * timeout 0 -- which is what forces a CryptoObject -- and the timed tier is 60.
     *
     * The expected window is stated as a LITERAL as well as read from the policy. Comparing
     * the spec only against BiometricPolicy would be circular, because the spec builder
     * derives its timeout from that same policy: the two would move together and a wrong
     * number would agree with itself.
     */
    @Test
    fun each_operation_requests_the_timeout_its_freshness_tier_implies() {
        val expectedWindow = mapOf(
            GatedOperation.INPUT to 60,
            GatedOperation.TAKE_CONTROL to 60,
            GatedOperation.REVOKE to 0,
            GatedOperation.KILL_SWITCH to 0,
            GatedOperation.LAUNCH to 0,
            GatedOperation.KILL to 0,
        )
        assertEquals(GatedOperation.entries.toSet(), expectedWindow.keys)

        for (op in GatedOperation.entries) {
            val expected = BiometricPolicy.specFor(op)
            val spec = KeystoreSpecs.forOperation(op, strongBox = false)

            assertTrue("$op is gated, so its key requires authentication", spec.isUserAuthenticationRequired)
            assertEquals(
                "6.0 binds $op to a ${expectedWindow.getValue(op)}s window; the key requests " +
                    "${spec.userAuthenticationValidityDurationSeconds}s",
                expectedWindow.getValue(op),
                spec.userAuthenticationValidityDurationSeconds,
            )
            assertEquals(
                "$op wants ${expected.freshness} but requests a ${spec.userAuthenticationValidityDurationSeconds}s window",
                expected.timeoutSeconds,
                spec.userAuthenticationValidityDurationSeconds,
            )
            assertTrue(
                "$op must be authorized by a strong biometric",
                (spec.userAuthenticationType and KeyProperties.AUTH_BIOMETRIC_STRONG) != 0,
            )
            assertTrue(
                "$op is a gated action; a biometric enrollment change must invalidate it",
                spec.isInvalidatedByBiometricEnrollment,
            )
        }
    }

    /**
     * The mutation that makes the timeout assertion above insufficient on its own: revoke and
     * kill are per-use, and per-use means each one gets its own key entry. One shared entry
     * with a zero timeout still lets a single CryptoObject authorization be pointed at
     * whichever operation the caller picks.
     */
    @Test
    fun per_use_operations_do_not_share_a_keystore_entry() {
        val perUse = GatedOperation.entries.filter { BiometricPolicy.specFor(it).requiresCryptoObject }
        val aliases = perUse.map { KeystoreSpecs.forOperation(it, strongBox = false).keystoreAlias }

        assertTrue("precondition: some operations are per-use", perUse.isNotEmpty())
        assertEquals(
            "per-use operations share a Keystore entry, so one authorization reaches all of " +
                "them: $aliases",
            perUse.size,
            aliases.toSet().size,
        )
    }

    // --- StrongBox: a preference with a fallback, not a claim ----------------

    @Test
    fun the_strongbox_fallback_differs_only_in_strongbox() {
        val preferred = KeystoreSpecs.kek(KeyTier.CONTENT, strongBox = true)
        val fallback = KeystoreSpecs.kek(KeyTier.CONTENT, strongBox = false)

        assertTrue(preferred.isStrongBoxBacked)
        assertFalse(fallback.isStrongBoxBacked)
        assertEquals(preferred.keystoreAlias, fallback.keystoreAlias)
        assertEquals(preferred.isUserAuthenticationRequired, fallback.isUserAuthenticationRequired)
        assertEquals(
            preferred.userAuthenticationValidityDurationSeconds,
            fallback.userAuthenticationValidityDurationSeconds,
        )
        assertEquals(preferred.userAuthenticationType, fallback.userAuthenticationType)
        assertEquals(
            "the fallback must not quietly relax the enrollment invalidation as well",
            preferred.isInvalidatedByBiometricEnrollment,
            fallback.isInvalidatedByBiometricEnrollment,
        )
        assertEquals(preferred.purposes, fallback.purposes)
        assertEquals(preferred.keySize, fallback.keySize)
    }

    // --- generate, then READ BACK -------------------------------------------

    private class FakeProvisioner(
        val strongBoxWorks: Boolean = true,
    ) : KeystoreProvisioner {
        val accepted = mutableListOf<KeyGenParameterSpec>()

        override fun generate(spec: KeyGenParameterSpec) {
            if (spec.isStrongBoxBacked && !strongBoxWorks) {
                throw android.security.keystore.StrongBoxUnavailableException("no StrongBox here")
            }
            accepted += spec
        }
    }

    private class FakeKeyInfoReader(val record: KeyInfoRecord) : KeyInfoReader {
        override fun read(alias: String): KeyInfoRecord = record
    }

    /**
     * `strongBox` picks the security level rather than setting a separate flag: StrongBox IS
     * a level, and KeyInfoRecord derives `strongBoxBacked` from it so the two can no longer
     * be given disagreeing values (residuals §2.7). The non-StrongBox case is
     * TRUSTED_ENVIRONMENT, an affirmative answer, so these fixtures still exercise a
     * hardware-backed key -- the levels that are NOT affirmative have their own file,
     * KeystoreHardwareFloorTest.
     */
    private fun faithfulRecord(spec: KeyGenParameterSpec, strongBox: Boolean) = KeyInfoRecord(
        securityLevel = if (strongBox) {
            KeystoreSecurityLevel.STRONGBOX
        } else {
            KeystoreSecurityLevel.TRUSTED_ENVIRONMENT
        },
        userAuthenticationRequired = spec.isUserAuthenticationRequired,
        userAuthenticationValidityDurationSeconds = spec.userAuthenticationValidityDurationSeconds,
        invalidatedByBiometricEnrollment = spec.isInvalidatedByBiometricEnrollment,
    )

    /** The happy path, so every refusal below is a refusal of something and not of everything. */
    @Test
    fun provisioning_succeeds_when_the_platform_delivers_what_was_requested() {
        val requested = KeystoreSpecs.kek(KeyTier.CONTENT, strongBox = true)
        val provisioner = FakeProvisioner()
        val key = CustodyProvisioning(provisioner, FakeKeyInfoReader(faithfulRecord(requested, true)))
            .provision(KeyTier.CONTENT, strongBoxPreferred = true)

        assertEquals(KeystoreAliases.forTier(KeyTier.CONTENT), key.alias)
        assertTrue(key.strongBoxBacked)
        assertEquals(1, provisioner.accepted.size)
    }

    /**
     * PB-KEY-8's verification, and the standing defect class it belongs to: a key whose
     * KeyInfo is never read back so a software fallback goes unnoticed. Each downgrade below
     * is one the platform is entitled to hand you.
     */
    @Test
    fun provisioning_fails_closed_when_the_platform_delivers_something_weaker() {
        val requested = KeystoreSpecs.kek(KeyTier.CONTENT, strongBox = false)
        val faithful = faithfulRecord(requested, strongBox = false)

        val downgrades = mapOf(
            "authentication silently not required" to
                faithful.copy(userAuthenticationRequired = false),
            "a longer window than was asked for" to
                faithful.copy(
                    userAuthenticationValidityDurationSeconds =
                        faithful.userAuthenticationValidityDurationSeconds + 60,
                ),
            "enrollment invalidation dropped" to
                faithful.copy(invalidatedByBiometricEnrollment = false),
        )

        for ((description, achieved) in downgrades) {
            assertThrows(
                "the platform returned $description and provisioning accepted it; the KeyInfo " +
                    "read-back exists precisely to refuse this",
                KeyCustodyException::class.java,
            ) {
                CustodyProvisioning(FakeProvisioner(), FakeKeyInfoReader(achieved))
                    .provision(KeyTier.CONTENT, strongBoxPreferred = false)
            }
        }
    }

    /**
     * StrongBox absence is a fallback, not a refusal -- but the result must say so. Reporting
     * hardware backing the key does not have is worse than not having it, because every later
     * decision is taken against the claim.
     */
    @Test
    fun strongbox_unavailability_falls_back_and_reports_the_truth() {
        val fallbackSpec = KeystoreSpecs.kek(KeyTier.CONTENT, strongBox = false)
        val provisioner = FakeProvisioner(strongBoxWorks = false)

        val key = CustodyProvisioning(
            provisioner,
            FakeKeyInfoReader(faithfulRecord(fallbackSpec, strongBox = false)),
        ).provision(KeyTier.CONTENT, strongBoxPreferred = true)

        assertFalse("this key is not StrongBox-backed and must not claim to be", key.strongBoxBacked)
        assertFalse(key.info.strongBoxBacked)
        assertEquals("the fallback spec must have been the one generated", 1, provisioner.accepted.size)
        assertFalse(provisioner.accepted.single().isStrongBoxBacked)
    }

    // --- the platform's failures become our failures ------------------------

    /**
     * Both Keystore exceptions extend java.security.InvalidKeyException. A mapping that
     * branches on the SUPERTYPE -- which is the natural thing to write, since that is what the
     * Cipher/Signature APIs declare -- collapses a permanent invalidation into "prompt again",
     * and produces an endless prompt against a key that no longer exists.
     */
    @Test
    fun platform_failures_map_onto_the_right_typed_custody_failure() {
        for (tier in KeyTier.entries) {
            assertTrue(
                PlatformFailure.map(tier, UserNotAuthenticatedException())
                    is KeyCustodyException.UserAuthenticationRequired,
            )
            assertTrue(
                "a permanent invalidation must not be reported as an authentication prompt",
                PlatformFailure.map(tier, KeyPermanentlyInvalidatedException())
                    is KeyCustodyException.KeyPermanentlyInvalidated,
            )
            assertTrue(
                PlatformFailure.map(tier, UnrecoverableKeyException("gone"))
                    is KeyCustodyException.KeystoreKeyMissing,
            )
            assertTrue(
                PlatformFailure.map(tier, KeyStoreException("gone"))
                    is KeyCustodyException.KeystoreKeyMissing,
            )
        }
    }

    /**
     * An unexpected failure must not be dressed up as one the user can fix. Mapping everything
     * unknown onto "authenticate again" produces a prompt loop over a bug.
     */
    @Test
    fun an_unrecognised_failure_is_not_reported_as_an_authentication_problem() {
        val mapped = PlatformFailure.map(KeyTier.CONTENT, IllegalStateException("provider blew up"))

        assertFalse(
            "an unrelated failure was reported as 'the user needs to authenticate'",
            mapped is KeyCustodyException.UserAuthenticationRequired,
        )
        assertTrue("the cause must survive for the log", mapped.message!!.isNotBlank())
    }
}
