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
 * PB-KEY-8's requesting half and its verifying half, after ADR-007 B133.
 *
 * WHAT THIS FILE USED TO BE, because the inversion is the point rather than a tidy-up. It was
 * PB-SEC-2's requesting half: it asserted that the content KEK DEMANDED a Class-3 biometric,
 * that a re-enrollment destroyed it, and that each gated operation carried the freshness window
 * its tier implied. PB-SEC-2 is VOID -- the trust boundary is the wire, and there is no local
 * authentication on this handset for a spec to request. Those assertions were correct as written
 * for a decision that has been superseded, so they are inverted or deleted here deliberately,
 * after the ADR, rather than relaxed until the code passed.
 *
 * WHAT IS KEPT, and B133 keeps it explicitly: the KEK is generated INSIDE Keystore, is
 * non-exportable, is refused when the platform says it is in software, and prefers StrongBox
 * with a fallback. None of that is about the person holding the handset -- it is what makes a
 * copy of the app's data directory unopenable anywhere else, which is a wire-side concern.
 *
 * THE HARD LIMIT, stated because everything below sits just under it. Hardware-backed Keystore
 * attestation and real TEE/StrongBox behaviour cannot be exercised on an emulator or in
 * Robolectric. They belong to PB-E2E-5, the physical-handset gate that §13 records as explicitly
 * deferred. Nothing here may be read as covering them.
 *
 * What IS asserted:
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
     * NEITHER TIER ASKS FOR AN AUTHENTICATOR (ADR-007 B133), and the content tier is the one
     * that changed. It carried `setUserAuthenticationParameters(60, AUTH_BIOMETRIC_STRONG)` and
     * was the gate; the flag is now the whole cost of the trust-boundary decision.
     *
     * IT IS ASSERTED RATHER THAN LEFT UNSAID because the request is baked into the key at
     * GENERATION and `KeystoreCustodyBootstrap.ensure` returns early when the alias exists. A
     * spec that re-acquired an authenticator would reach fresh installs only, and every one of
     * them would hold a content KEK that nothing in this app can ever satisfy -- there is no
     * prompt anywhere in the product to offer.
     */
    @Test
    fun the_content_kek_asks_for_no_user_authentication() {
        val spec = KeystoreSpecs.kek(KeyTier.CONTENT, strongBox = false)

        assertFalse(
            "the content KEK requests a user authentication that nothing in this app can " +
                "satisfy: ADR-007 B133 removed every prompt, so this key would refuse forever",
            spec.isUserAuthenticationRequired,
        )
        assertEquals(KeystoreAliases.forTier(KeyTier.CONTENT), spec.keystoreAlias)
    }

    /**
     * AND THE ENROLLMENT FLAG IS OFF, which is the inverse of what this file used to assert.
     *
     * `setInvalidatedByBiometricEnrollment` defaults to TRUE, so this is set rather than
     * inherited: leaving it would let a re-enrolled fingerprint destroy a KEK that no longer has
     * anything to do with fingerprints. The content tier would become unopenable and the wake
     * tier would lose background wakes, recoverable in both cases only by re-pairing -- over a
     * biometric the design stopped using.
     */
    @Test
    fun neither_kek_is_destroyed_by_a_biometric_enrollment_change() {
        for (tier in KeyTier.entries) {
            assertFalse(
                "the $tier KEK is destroyed when the user re-enrolls a fingerprint, which this " +
                    "design no longer uses for anything. The only way back is a re-pair",
                KeystoreSpecs.kek(tier, strongBox = false).isInvalidatedByBiometricEnrollment,
            )
        }
    }

    /**
     * PB-SEC-1's non-exportability, at the one flag that could take it away.
     *
     * A symmetric AndroidKeyStore key is generated INSIDE Keystore and its bytes are never
     * handed back -- that is what makes an extracted data directory unopenable off the device,
     * and it is what B133 keeps while dropping the authenticator. `PURPOSE_WRAP_KEY` is the
     * purpose that would change it: it exists so a key can be exported in wrapped form, and it
     * is one `or` away from the two this spec carries.
     */
    @Test
    fun the_kek_is_an_encrypt_decrypt_key_with_no_export_purpose() {
        for (tier in KeyTier.entries) {
            val spec = KeystoreSpecs.kek(tier, strongBox = false)
            assertEquals(
                "the $tier KEK carries a purpose beyond encrypt/decrypt. PURPOSE_WRAP_KEY in " +
                    "particular exists so a Keystore key can leave the device in wrapped form, " +
                    "which is the one thing PB-SEC-1's at-rest claim rests on not happening",
                KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT,
                spec.purposes,
            )
            assertEquals(256, spec.keySize)
        }
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

    // --- StrongBox: a preference with a fallback, not a claim ----------------
    //
    // The two per-operation tests that used to sit here are DELETED, not relaxed. They asserted
    // that `KeystoreSpecs.forOperation` requested the freshness window each gated operation's
    // tier implied, and that the per-use operations did not share a Keystore entry so one
    // CryptoObject authorization could not be pointed at another verb. Both subjects left the
    // product with PB-SEC-2: there is no `GatedOperation`, no `BiometricPolicy` and no
    // `forOperation`. A rewrite would have had to invent something for them to fence.

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
     * KeyInfo is never read back so a software fallback goes unnoticed. Each disagreement below
     * is one the platform is entitled to hand you.
     *
     * EVERY AXIS HAS FLIPPED DIRECTION (ADR-007 B133) and the check has not lost its point --
     * `KeyInfoRecord`'s own note says so. What must not happen now is the platform silently
     * ADDING an authenticator, or an enrollment invalidation, to a key that requested neither:
     * both produce a KEK that refuses over something no screen in this app can resolve, and the
     * user's only exit is a re-pair. The read-back is what turns that into a named refusal at
     * provisioning time instead of an app that stops working later for no stated reason.
     */
    @Test
    fun provisioning_fails_closed_when_the_platform_delivers_something_other_than_requested() {
        val requested = KeystoreSpecs.kek(KeyTier.CONTENT, strongBox = false)
        val faithful = faithfulRecord(requested, strongBox = false)

        val downgrades = mapOf(
            "an authenticator this app has no prompt to satisfy" to
                faithful.copy(userAuthenticationRequired = true),
            "a window that was never asked for" to
                faithful.copy(
                    userAuthenticationValidityDurationSeconds =
                        faithful.userAuthenticationValidityDurationSeconds + 60,
                ),
            "an enrollment invalidation that was explicitly turned off" to
                faithful.copy(invalidatedByBiometricEnrollment = true),
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
     * Cipher/Signature APIs declare -- collapses the two into one.
     *
     * THE `UserNotAuthenticatedException` ARM IS KEPT ALTHOUGH NOTHING THIS BUILD PROVISIONS CAN
     * RAISE IT (ADR-007 B133), and the reason is not symmetry. An install made BEFORE B133 still
     * holds an `AUTH_BIOMETRIC_STRONG` content KEK -- `KeystoreCustodyBootstrap.ensure` returns
     * early when the alias exists, so the new spec never reaches it. The alternative to naming
     * that refusal is [KeyCustodyException.Unexpected], which `phonecore.openSealedDeviceKeys`
     * treats as a Resume it must refuse outright: the upgraded handset becomes an app that will
     * not start with nothing on screen saying why. Where the two verdicts go once named is
     * `PhoneStartupRoutingTest`'s subject, and after B133 it is the same screen for both.
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
