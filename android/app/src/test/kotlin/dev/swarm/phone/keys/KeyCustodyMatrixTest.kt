package dev.swarm.phone.keys

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * PB-KEY-8 -- "A platform capability matrix, so the custody design is implementable on real
 * devices. For each of {NoiseStatic, Recipient, CommandSign, RelayAuth} state whether it is
 * generated/used natively in Keystore, held as an app-format key wrapped by an authenticated
 * Keystore AES key, or software-only with a documented residual. Bind this to PB-RUN-1's
 * minSdk."
 *
 * The requirement's own criterion is about the matrix being RECORDED and ENFORCED, plus a
 * defined refusal when the handset cannot comply. Achieved hardware backing via KeyInfo is
 * explicitly PB-E2E-5's, on a handset that does not exist here. So these tests assert the
 * REQUEST and the VERIFICATION -- never the hardware.
 *
 * Plain JVM; the matrix is data and the planner is a pure function. The pin is read from the
 * staged toolchain.env (see Pin), never written down again here: S13 established that
 * discipline because a second copy of "33" is a second thing to forget.
 */
class KeyCustodyMatrixTest {

    /** API 33 is where Curve25519 entered KeyMint. Named once, in the one place it means something. */
    private val curve25519KeyMintApi = 33

    @Test
    fun the_matrix_has_a_row_for_every_role() {
        assertEquals(
            "a matrix missing a role is a role nobody assigned a backing to",
            KeyRole.entries.toSet(),
            KeyCustodyMatrix.rows.keys,
        )
        for (role in KeyRole.entries) {
            assertEquals(role, KeyCustodyMatrix.row(role).role)
        }
    }

    /**
     * PB-RUN-1's floor exists FOR this matrix: ADR-007 B16 pinned minSdk 33 because below it
     * the Curve25519 roles cannot be Keystore-native at all, and the degradation is silent.
     * So a row may not claim a native Curve25519 key at an API level where the platform has
     * no such algorithm.
     */
    @Test
    fun no_row_claims_native_curve25519_below_the_api_that_introduced_it() {
        for (role in KeyRole.entries) {
            val row = KeyCustodyMatrix.row(role)
            val curve = row.algorithm == KeyAlgorithm.X25519 || row.algorithm == KeyAlgorithm.ED25519
            if (row.backing == KeyBacking.KEYSTORE_NATIVE && curve) {
                assertTrue(
                    "$role claims a Keystore-native ${row.algorithm} at API ${row.requiresApi}; " +
                        "Curve25519 entered KeyMint at API $curve25519KeyMintApi",
                    row.requiresApi >= curve25519KeyMintApi,
                )
            }
        }
    }

    /**
     * And every row must be achievable at the pinned floor. This is the drift catch: lower
     * SWARM_ANDROID_MIN_SDK and the rows that need 33 stop being reachable on the lowest
     * device the app claims to support, which is precisely how PB-KEY-8's central property
     * degrades without anyone noticing.
     */
    @Test
    fun every_row_is_achievable_at_the_pinned_min_sdk() {
        val minSdk = Pin.minSdk
        for (role in KeyRole.entries) {
            val row = KeyCustodyMatrix.row(role)
            assertTrue(
                "$role needs API ${row.requiresApi} but SWARM_ANDROID_MIN_SDK is $minSdk, so " +
                    "the app installs on devices where this row is a fiction",
                row.requiresApi <= minSdk,
            )
        }
    }

    /** Every row states why. A backing with no reason is a backing nobody chose. */
    @Test
    fun every_row_carries_a_rationale() {
        for (role in KeyRole.entries) {
            assertTrue(
                "$role has no rationale for its ${KeyCustodyMatrix.row(role).backing} backing",
                KeyCustodyMatrix.row(role).rationale.isNotBlank(),
            )
        }
    }

    /** PB-KEY-8: "software-only with a documented residual". Documented means present. */
    @Test
    fun software_only_rows_record_a_residual_and_others_do_not_pretend_to() {
        for (role in KeyRole.entries) {
            val row = KeyCustodyMatrix.row(role)
            if (row.backing == KeyBacking.SOFTWARE_ONLY) {
                assertNotNull("$role is software-only and records no residual", row.residual)
                assertTrue(row.residual!!.isNotBlank())
            } else {
                assertNull(
                    "$role is ${row.backing} but records a residual; a residual on a backing " +
                        "that has none makes the field meaningless where it matters",
                    row.residual,
                )
            }
        }
    }

    /**
     * A key that never leaves Keystore cannot be used outside it, so a KEYSTORE_NATIVE row
     * implies the operation runs in Keystore -- which implies a Java -> Go seam for that
     * operation. Stating the boundary in the row is what makes that consequence visible
     * instead of being discovered at integration time.
     */
    @Test
    fun native_rows_perform_their_operation_in_keystore() {
        for (role in KeyRole.entries) {
            val row = KeyCustodyMatrix.row(role)
            if (row.backing == KeyBacking.KEYSTORE_NATIVE) {
                assertEquals(
                    "$role is Keystore-native, so its private key never leaves Keystore and " +
                        "the Go core cannot perform the operation",
                    CustodyBoundary.ANDROID_KEYSTORE,
                    row.boundary,
                )
            }
        }
    }

    // --- the defined refusal -----------------------------------------------

    private fun allPresent(): MutableMap<PlatformCapability, CapabilityState> =
        PlatformCapability.entries.associateWith { CapabilityState.PRESENT }.toMutableMap()

    /**
     * Does the SHIPPED MATRIX ask Keystore for [capability] -- derived here, in the test,
     * from `KeyCustodyMatrix` itself.
     *
     * IT IS DERIVED RATHER THAN LISTED, and that is the fix for residuals §2.8. The planner
     * used to require KEYSTORE_X25519 and KEYSTORE_ED25519, and the test used to list the
     * same two, so the two agreed with each other and neither agreed with the matrix -- which
     * has no KEYSTORE_NATIVE row at all (ADR-007 B17(a)) and therefore never asks Keystore
     * for a Curve25519 key. Deriving it means a row that LATER becomes KEYSTORE_NATIVE puts
     * its algorithm back in the fail-closed floor with no edit here or in the planner.
     */
    private fun matrixConsumes(capability: PlatformCapability): Boolean = when (capability) {
        // Every KEYSTORE_WRAPPED row seals its private half under the tier's AES-GCM Keystore
        // KEK (ADR-007 B8), so the AES-GCM capability is consumed as the matrix stands.
        PlatformCapability.KEYSTORE_AES_GCM ->
            KeyCustodyMatrix.rows.values.any { it.backing == KeyBacking.KEYSTORE_WRAPPED } ||
                nativeRowUses(KeyAlgorithm.AES_GCM)

        // A Curve25519 capability is consumed only by a row that asks KEYSTORE for a
        // Curve25519 key, which means KEYSTORE_NATIVE backing. No row is.
        PlatformCapability.KEYSTORE_X25519 -> nativeRowUses(KeyAlgorithm.X25519)
        PlatformCapability.KEYSTORE_ED25519 -> nativeRowUses(KeyAlgorithm.ED25519)

        // Per-use authorization is not a matrix column: it is BiometricPolicy's freshness
        // tier for the gated operations, and KeystoreSpecs.forOperation requests it.
        PlatformCapability.USER_AUTH_PER_USE ->
            GatedOperation.entries.any { BiometricPolicy.specFor(it).requiresCryptoObject }

        // Its absence is a fallback, never a refusal.
        PlatformCapability.STRONGBOX -> false
    }

    private fun nativeRowUses(algorithm: KeyAlgorithm): Boolean = KeyCustodyMatrix.rows.values
        .any { it.backing == KeyBacking.KEYSTORE_NATIVE && it.algorithm == algorithm }

    /** The planner must actually plan on a capable handset, or every refusal test below is vacuous. */
    @Test
    fun a_capable_handset_gets_a_plan() {
        val plan = CustodyPlanner.forDevice(allPresent())
        assertTrue("a fully capable handset was refused", plan is CustodyPlan.Provisioned)
        assertEquals(
            KeyRole.entries.toSet(),
            (plan as CustodyPlan.Provisioned).rows.keys,
        )
        assertEquals(
            "a handset that answered PRESENT to everything recorded an anomaly, so the " +
                "anomaly list says nothing when it is non-empty",
            emptyList<CapabilityAnomaly>(),
            plan.anomalies,
        )
    }

    /**
     * PB-KEY-8: "a defined refusal/fallback when the handset lacks the required algorithm or
     * auth capability". Refusal is the interesting half -- a silent downgrade to SOFTWARE_ONLY
     * is what the requirement exists to prevent.
     *
     * THE LIST IS THE CONSUMED SET, and it shrank in the residuals §2.8 fix: it used to name
     * KEYSTORE_X25519 and KEYSTORE_ED25519 as well, which no row consumes. Their case did not
     * go untested -- it moved to [a_capability_no_row_consumes_is_recorded_rather_than_refused],
     * and [the_floor_refuses_exactly_the_capabilities_the_matrix_consumes] now covers every
     * capability in both non-PRESENT states rather than four of them in one.
     */
    @Test
    fun a_missing_algorithm_is_refused_by_name_not_downgraded() {
        for (capability in listOf(
            PlatformCapability.KEYSTORE_AES_GCM,
            PlatformCapability.USER_AUTH_PER_USE,
        )) {
            val caps = allPresent()
            caps[capability] = CapabilityState.ABSENT

            val plan = CustodyPlanner.forDevice(caps)
            assertTrue(
                "a handset without $capability got a plan instead of a refusal",
                plan is CustodyPlan.Refused,
            )
            assertEquals(capability, (plan as CustodyPlan.Refused).capability)
            assertTrue("the refusal names no reason", plan.reason.isNotBlank())
        }
    }

    /**
     * UNKNOWN is not PRESENT. A probe that could not answer must fail closed; treating it as
     * present is exactly how a software fallback reaches production, and this is the path
     * that never gets exercised unless a test drives it.
     *
     * Scoped to the consumed set for the same reason as the test above: failing closed is
     * right for a capability the design USES, and refusing to provision at all over one it
     * does not is residuals §2.8.
     */
    @Test
    fun an_unknown_capability_fails_closed() {
        for (capability in PlatformCapability.entries) {
            if (!matrixConsumes(capability)) continue
            val caps = allPresent()
            caps[capability] = CapabilityState.UNKNOWN

            assertTrue(
                "$capability could not be probed and the planner assumed it was there",
                CustodyPlanner.forDevice(caps) is CustodyPlan.Refused,
            )
        }
    }

    /**
     * residuals §2.8: the planner refused to provision unless KEYSTORE_X25519 and
     * KEYSTORE_ED25519 both probed PRESENT -- two capabilities no matrix row consumes,
     * because every role is KEYSTORE_WRAPPED and the Curve25519 private halves live in the
     * Go core (ADR-007 B17(a)). Keystore is never asked for a Curve25519 key at all.
     *
     * WHAT THE USER SAW. On a handset whose probe answered ABSENT or UNKNOWN, the app would
     * not provision, so PhoneRuntime's DEVICE_UNSUPPORTED screen was the whole product: no
     * pairing, no sessions, no remedy, over a capability nothing uses. That is the single
     * worst outcome available in this class of defect, and it is the likeliest way the
     * physical-handset gate fails on first contact with real hardware.
     *
     * WHAT THE USER SEES NOW. Nothing: the app provisions. The non-PRESENT answer is recorded
     * on the plan instead, so the canary argument survives -- at the pinned minSdk both are
     * meant to be there, and a handset that says otherwise has a Keystore not behaving as its
     * API level promises, which is worth knowing -- without the app being unable to start
     * over it.
     */
    @Test
    fun a_capability_no_row_consumes_is_recorded_rather_than_refused() {
        val unconsumed = PlatformCapability.entries
            .filter { it != PlatformCapability.STRONGBOX && !matrixConsumes(it) }
        assertTrue(
            "precondition: the matrix consumes every capability, so this test asserts nothing",
            unconsumed.isNotEmpty(),
        )

        for (capability in unconsumed) {
            for (state in listOf(CapabilityState.ABSENT, CapabilityState.UNKNOWN)) {
                val caps = allPresent()
                caps[capability] = state

                val plan = CustodyPlanner.forDevice(caps)
                assertTrue(
                    "a handset reporting $capability as $state cannot provision at all, over a " +
                        "capability no row in the matrix consumes",
                    plan is CustodyPlan.Provisioned,
                )
                assertEquals(
                    "$capability at $state was neither refused NOR recorded, so a Keystore " +
                        "not behaving as its API level promises now goes unnoticed entirely",
                    listOf(CapabilityAnomaly(capability, state)),
                    (plan as CustodyPlan.Provisioned).anomalies,
                )
                assertEquals(
                    "a recorded anomaly must not cost the plan its rows",
                    KeyRole.entries.toSet(),
                    plan.rows.keys,
                )
            }
        }
    }

    /**
     * The invariant both tests above are instances of, stated TOTALLY: the fail-closed floor
     * is exactly what the matrix consumes, over every capability and both non-PRESENT states.
     *
     * It is the drift catch in both directions, and both directions have shipped here once.
     * Too wide is residuals §2.8 -- an app that will not start over a capability it does not
     * use. Too narrow is PB-KEY-8's whole subject -- a silent downgrade to software-only
     * custody on a handset that could not do what the design requires.
     */
    @Test
    fun the_floor_refuses_exactly_the_capabilities_the_matrix_consumes() {
        for (capability in PlatformCapability.entries) {
            if (capability == PlatformCapability.STRONGBOX) continue
            for (state in listOf(CapabilityState.ABSENT, CapabilityState.UNKNOWN)) {
                val caps = allPresent()
                caps[capability] = state
                val plan = CustodyPlanner.forDevice(caps)

                if (matrixConsumes(capability)) {
                    assertTrue(
                        "the matrix asks Keystore for $capability and a handset reporting it " +
                            "as $state still got a plan -- a silent downgrade to software-only " +
                            "custody is what PB-KEY-8 exists to prevent",
                        plan is CustodyPlan.Refused,
                    )
                    assertEquals(capability, (plan as CustodyPlan.Refused).capability)
                } else {
                    assertTrue(
                        "no row in the matrix consumes $capability, and a handset reporting it " +
                            "as $state cannot provision at all",
                        plan is CustodyPlan.Provisioned,
                    )
                }
            }
        }
    }

    /**
     * StrongBox is the one capability whose absence is a FALLBACK rather than a refusal: it is
     * device-dependent, and refusing without it would refuse most handsets. What must not
     * happen is the plan claiming StrongBox it does not have.
     */
    @Test
    fun absent_strongbox_falls_back_without_claiming_hardware_it_lacks() {
        for (state in listOf(CapabilityState.ABSENT, CapabilityState.UNKNOWN)) {
            val caps = allPresent()
            caps[PlatformCapability.STRONGBOX] = state

            val plan = CustodyPlanner.forDevice(caps)
            assertTrue("StrongBox $state must fall back, not refuse", plan is CustodyPlan.Provisioned)
            assertEquals(
                "the plan claims StrongBox on a handset reporting $state",
                false,
                (plan as CustodyPlan.Provisioned).strongBox,
            )
        }
    }

    /** And it is claimed when it IS there, or the assertion above passes on a constant false. */
    @Test
    fun present_strongbox_is_used() {
        val plan = CustodyPlanner.forDevice(allPresent())
        assertEquals(true, (plan as CustodyPlan.Provisioned).strongBox)
    }
}
