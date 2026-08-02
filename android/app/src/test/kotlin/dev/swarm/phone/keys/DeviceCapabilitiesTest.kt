package dev.swarm.phone.keys

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * The PROBE that makes [CustodyPlanner] a live gate rather than a pure function nobody calls.
 *
 * WHY THIS FILE EXISTS. `CustodyPlanner.forDevice` had no production caller at all: it was
 * fully tested, it decided PB-KEY-8's refusal, and `PhoneRuntime.construct()` went straight to
 * [KeystoreCustodyBootstrap] without ever building a capability map -- so
 * [KeyCustodyException.PlatformCapabilityMissing] was declared, routed, and never thrown. The
 * app never refused a handset over a capability, because the thing that refuses was never
 * asked. That is residuals §2.10(a), and it is the sixth instance of this phase's standing
 * defect class.
 *
 * WHAT MAKES WIRING IT DANGEROUS, and why the assertions below are shaped the way they are.
 * The fatal floor was narrowed to the capabilities the design actually consumes -- today
 * {KEYSTORE_AES_GCM} alone, since ADR-007 B133 took USER_AUTH_PER_USE out of the enum with the
 * authenticator it described -- precisely so this gate
 * could not stop a phone starting over a capability nothing consumes (residuals §2.8). Turning
 * the gate on re-opens that risk one layer down: a probe that answers non-PRESENT for a
 * consumed capability on a handset the app supports produces an app that will not start, which
 * is the worst outcome in this whole class. So the probe is asserted from BOTH sides -- it
 * refuses what the design genuinely needs, and it cannot refuse anything else.
 *
 * PB-E2E-5 STAYS DEFERRED. Nothing here claims a real Keystore answers any particular way; the
 * platform half is [AndroidKeystoreAlgorithms], whose only assertion below is that it ANSWERS
 * rather than throwing. Whether a given handset's KeyMint offers XDH is a physical-handset
 * fact and is not claimed anywhere.
 */
@RunWith(RobolectricTestRunner::class)
class DeviceCapabilitiesTest {

    /** A Keystore that answers whatever the test says, so the probe's policy is the subject. */
    private class FakeAlgorithms(
        private val secret: Map<String, CapabilityState> = mapOf("AES" to CapabilityState.PRESENT),
        private val pairs: Map<String, CapabilityState> = mapOf(
            "XDH" to CapabilityState.PRESENT,
            "Ed25519" to CapabilityState.PRESENT,
        ),
    ) : KeystoreAlgorithms {
        override fun secretKey(algorithm: String): CapabilityState =
            secret[algorithm] ?: CapabilityState.ABSENT

        override fun keyPair(algorithm: String): CapabilityState =
            pairs[algorithm] ?: CapabilityState.ABSENT
    }

    /**
     * THE `sdkInt` PARAMETER IS GONE FROM THE PROBE (ADR-007 B133) and this helper lost it with
     * the production constructor. The only capability that was an API-LEVEL fact was
     * USER_AUTH_PER_USE -- `setUserAuthenticationParameters(timeout, type)` landed in API 30 --
     * and the design makes that call nowhere any more. Every remaining capability is a question
     * about what this Keystore offers, which is what [KeystoreAlgorithms] answers.
     */
    private fun capabilities(
        strongBox: Boolean = false,
        algorithms: KeystoreAlgorithms = FakeAlgorithms(),
    ) = DeviceCapabilities(strongBox = strongBox, algorithms = algorithms)

    /**
     * The probe answers EVERY capability the planner can ask about.
     *
     * `CustodyPlanner.stateOf` reads a missing map entry as UNKNOWN and UNKNOWN fails closed,
     * so a capability added to the enum and forgotten here is not a gap -- it is an app that
     * refuses to start the moment somebody adds it to the consumed set. This is the assertion
     * that makes that a compile-then-test failure instead of a field report.
     */
    @Test
    fun the_probe_answers_every_capability_the_planner_can_ask_about() {
        assertEquals(
            "a capability the probe never reports is UNKNOWN to the planner, which fails " +
                "closed -- so an unanswered capability that later joins the consumed set is an " +
                "app that will not start on any handset",
            PlatformCapability.entries.toSet(),
            capabilities().probe().keys,
        )
    }

    /**
     * The whole point of the wiring, from the safe side: a capable handset provisions.
     */
    @Test
    fun a_handset_that_answers_present_provisions() {
        val plan = CustodyPlanner.forDevice(capabilities().probe())
        assertTrue(
            "a handset that can make an AES-GCM Keystore key at the pinned minSdk was refused",
            plan is CustodyPlan.Provisioned,
        )
        assertEquals(emptyList<CapabilityAnomaly>(), (plan as CustodyPlan.Provisioned).anomalies)
    }

    /**
     * PB-KEY-8's defined refusal, reached through the probe rather than through a hand-built
     * map. A Keystore that cannot produce the AES-GCM KEK cannot seal anything: every custody
     * blob in the design is wrapped under it (ADR-007 B8), so this refusal costs a user nothing
     * they would otherwise have had -- it replaces an opaque failure deeper in
     * `KeystoreCustodyBootstrap.ensure` with a named one.
     */
    @Test
    fun a_keystore_that_cannot_make_the_kek_is_refused_by_name() {
        val plan = CustodyPlanner.forDevice(
            capabilities(algorithms = FakeAlgorithms(secret = mapOf("AES" to CapabilityState.ABSENT)))
                .probe(),
        )
        assertTrue("a Keystore with no AES got a plan instead of a refusal", plan is CustodyPlan.Refused)
        assertEquals(PlatformCapability.KEYSTORE_AES_GCM, (plan as CustodyPlan.Refused).capability)
    }

    /**
     * residuals §2.8, re-fenced at the layer that could re-open it.
     *
     * The Curve25519 entries are CANARIES: no matrix row consumes them, because every role is
     * KEYSTORE_WRAPPED and the private halves live in the Go core. A handset whose KeyMint does
     * not offer XDH or Ed25519 must still pair, still sync and still take control. The version
     * of this gate that shipped before refused it outright, and the user's whole product was a
     * DEVICE_UNSUPPORTED screen.
     */
    @Test
    fun a_handset_without_curve25519_still_starts_and_says_so() {
        val plan = CustodyPlanner.forDevice(
            capabilities(
                algorithms = FakeAlgorithms(
                    pairs = mapOf(
                        "XDH" to CapabilityState.ABSENT,
                        "Ed25519" to CapabilityState.UNKNOWN,
                    ),
                ),
            ).probe(),
        )
        assertTrue(
            "a handset whose Keystore offers no Curve25519 cannot start the app, over two " +
                "capabilities no row consumes",
            plan is CustodyPlan.Provisioned,
        )
        assertEquals(
            "the anomaly is recorded rather than refused, and it is recorded with the state " +
                "the platform actually answered",
            listOf(
                CapabilityAnomaly(PlatformCapability.KEYSTORE_X25519, CapabilityState.ABSENT),
                CapabilityAnomaly(PlatformCapability.KEYSTORE_ED25519, CapabilityState.UNKNOWN),
            ),
            (plan as CustodyPlan.Provisioned).anomalies,
        )
    }

    /**
     * DELETED, NOT REWRITTEN: `per_use_authentication_is_present_at_every_supported_api_level`.
     * It swept API levels from the pinned minSdk upwards and required the probe to answer
     * PRESENT for USER_AUTH_PER_USE at each. There is no such capability: ADR-007 B133 removed
     * the entry from the enum rather than demoting it to a canary, because a canary records a
     * Keystore behaving unlike its API level promises and an API LEVEL is not something a
     * Keystore can get wrong.
     *
     * HAD THE ENTRY BEEN KEPT, this test would have compiled and passed while fencing nothing at
     * all -- the probe would answer PRESENT from a constant, about an authenticator the design
     * asks for nowhere. That is why the enum entry going is what makes the deletion honest:
     * `the_probe_answers_every_capability_the_planner_can_ask_about` above is now the thing that
     * fails if anybody puts it back without a consumer.
     */

    /** StrongBox is a fallback, never a refusal, and it is claimed only when it is there. */
    @Test
    fun strongbox_is_reported_and_never_refused() {
        assertEquals(
            CapabilityState.PRESENT,
            capabilities(strongBox = true).probe()[PlatformCapability.STRONGBOX],
        )
        val plan = CustodyPlanner.forDevice(capabilities(strongBox = false).probe())
        assertTrue("a handset without StrongBox was refused", plan is CustodyPlan.Provisioned)
        assertEquals(false, (plan as CustodyPlan.Provisioned).strongBox)
    }

    /**
     * The platform adapter ANSWERS; it never throws.
     *
     * This runs on Robolectric, which has no AndroidKeyStore provider, so every question below
     * fails at the provider -- which is exactly the shape the assertion needs. A probe that let
     * a `NoSuchProviderException` escape would turn the capability question into a crash on the
     * startup path, and the app would be gone before [routeStartupFailure] could say anything.
     *
     * It does NOT assert what a real handset answers. That is PB-E2E-5 and stays deferred.
     */
    @Test
    fun the_platform_adapter_answers_rather_than_throwing() {
        val platform = AndroidKeystoreAlgorithms()
        assertNotEquals(
            "a Keystore the adapter could not even reach answered PRESENT. Assuming present is " +
                "how a software fallback ships unnoticed, and it is the direction UNKNOWN exists " +
                "to keep the probe out of",
            CapabilityState.PRESENT,
            platform.secretKey("NoSuchAlgorithmAnywhere"),
        )
        assertNotEquals(
            CapabilityState.PRESENT,
            platform.keyPair("NoSuchAlgorithmAnywhere"),
        )
    }
}
