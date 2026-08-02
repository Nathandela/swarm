package dev.swarm.phone.keys

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * PB-KEY-5 -- "Custody tier per role, not one undifferentiated core key... Assign a tier to
 * each of {NoiseStatic, Recipient, CommandSign, RelayAuth} and state whether the sealed grant
 * blob is discarded after opening or retained under the content tier."
 *
 * PB-KEY-5 ITSELF IS UNAFFECTED BY ADR-007 B133. Role separation has nothing to do with
 * authentication and survives whole; the tier assignment, the matrix agreement and the sealed
 * grant's fate are all asserted below unchanged.
 *
 * THE ACCEPTANCE CRITERION QUOTED AGAINST IT IS NOT. Verbatim, and left verbatim: "an attacker
 * with after-first-unlock access and everything at rest reaches no content key." That clause
 * belongs to PB-KEY-2, it is FALSIFIED by B133 rather than narrowed -- the content KEK now asks
 * for no authenticator, so the content key is reachable after first unlock by design -- and it
 * is recorded here as struck rather than quietly reworded into something true. What stands in
 * its place is the attacker B133 keeps; see the note above the last test.
 *
 * Plain JVM.
 */
class PerRoleCustodyTest {

    /** Four roles, fixed. A fifth added later must be given a tier, not inherit one. */
    @Test
    fun the_role_set_is_the_four_crypto_KeyStore_roles() {
        assertEquals(
            setOf(
                KeyRole.NOISE_STATIC,
                KeyRole.RECIPIENT,
                KeyRole.COMMAND_SIGN,
                KeyRole.RELAY_AUTH,
            ),
            KeyRole.entries.toSet(),
        )
    }

    /**
     * ADR-007 B9's assignment, and the reasoning is load-bearing in both directions:
     *
     *  - RELAY_AUTH must be WAKE because background reconnect happens on a locked handset. Put
     *    it under the content tier and the app cannot reconnect until the user authenticates,
     *    which defeats the wake path B16 depends on.
     *  - RECIPIENT must be CONTENT because OpenSealedBox recovers BOTH epoch keys from a grant
     *    (internal/remote/crypto/keystore.go:163), so a recipient key in the WAKE tier is a
     *    content key in the WAKE tier. ITS REASON CHANGED WITH ADR-007 B133 AND THE ASSIGNMENT
     *    DID NOT: the argument used to be about a stolen once-unlocked handset, which is no
     *    longer a boundary this design defends. What it is about now is the PUSH PATH.
     *    `PushWakePath` declares the WAKE tier and nothing else, because FirebaseMessagingService
     *    runs IN the app process and that declaration is the only phone-side enforcement left;
     *    a RECIPIENT key inside the set that path may touch would put the content key in reach
     *    of a process FCM woke, which is the whole subject of PB-KEY-2's split.
     */
    @Test
    fun each_role_has_the_tier_ADR_007_B9_assigns_it() {
        assertEquals(
            "background reconnect must work while locked",
            KeyTier.WAKE,
            KeyTierPolicy.tierOf(KeyRole.RELAY_AUTH),
        )
        assertEquals(
            "OpenSealedBox recovers both epoch keys, so an after-first-unlock recipient key " +
                "IS a content key",
            KeyTier.CONTENT,
            KeyTierPolicy.tierOf(KeyRole.RECIPIENT),
        )
        assertEquals(KeyTier.CONTENT, KeyTierPolicy.tierOf(KeyRole.NOISE_STATIC))
        assertEquals(KeyTier.CONTENT, KeyTierPolicy.tierOf(KeyRole.COMMAND_SIGN))
    }

    /** The matrix must agree with the tier policy; two sources of truth is one bug away. */
    @Test
    fun the_custody_matrix_agrees_with_the_tier_policy() {
        for (role in KeyRole.entries) {
            assertEquals(
                "matrix and tier policy disagree about $role",
                KeyTierPolicy.tierOf(role),
                KeyCustodyMatrix.row(role).tier,
            )
        }
    }

    // --- the sealed grant --------------------------------------------------

    /**
     * PB-KEY-5 requires the grant's fate be STATED. Either answer is admissible; what is not
     * admissible is a grant blob sitting at rest under the wake tier, which is the content
     * key with extra steps.
     */
    @Test
    fun the_sealed_grant_is_discarded_or_retained_under_the_content_tier() {
        val record = AtRestInventory.record(AtRestArtifact.SEALED_EPOCH_GRANT)
        when (SealedGrantPolicy.retention) {
            GrantRetention.DISCARDED_AFTER_OPEN -> assertNull(
                "the policy says the grant is discarded, but it is still recorded at rest",
                record,
            )

            GrantRetention.RETAINED_UNDER_CONTENT_TIER -> {
                assertNotNull("the policy says the grant is retained; say where", record)
                assertEquals(
                    "a retained grant opens BOTH epoch keys, so it is content-tier material",
                    KeyTier.CONTENT,
                    record!!.tier,
                )
            }
        }
    }

    // --- the attacker ------------------------------------------------------

    /**
     * THE AFTER-FIRST-UNLOCK ATTACKER IS GONE FROM THIS FILE, AND THE CLAIM IS FALSIFIED RATHER
     * THAN NARROWED (ADR-007 B133). Two tests stood here:
     * `an_after_first_unlock_attacker_reaches_no_content_key` and
     * `the_custody_session_refuses_the_after_first_unlock_attacker`. They drove the criterion
     * quoted at the top of this file -- "an attacker with after-first-unlock access and
     * everything at rest reaches no content key" -- and they PASSED, because they set the
     * fixture's content tier to locked themselves.
     *
     * That state is now unreachable in production. Every KEK carries
     * `setUserAuthenticationRequired(false)`, so the content key IS reachable after first unlock,
     * BY DESIGN. The claim is false, not reduced: a narrowed requirement leaves a true residue
     * and this one leaves none. Re-wording it into something true would have produced a test that
     * read as this phase's central security claim while fencing a fixture's own lock.
     *
     * The residual risk is stated where a residual risk belongs -- ADR-007 B133: a stolen
     * unlocked phone gives the holder full control of the agents on the machine, and the only
     * surviving mitigation is `swarm remote off` or a revoke issued FROM the machine.
     *
     * The criterion in this file's header is left quoted verbatim, and marked, rather than
     * silently edited to match what the code now does.
     *
     * What replaces them is below, and it is a DIFFERENT attacker.
     */

    /**
     * The attacker B133 explicitly keeps: someone holding the app's bytes and NOT the handset.
     *
     * This is a copied data directory -- a backup, a `run-as` extraction, an image lifted off the
     * disk -- opened somewhere the Keystore entries do not exist. It is a WIRE-side concern and
     * not a holder-side one, which is why the sealing, the hardware backing and the
     * non-exportability all survive the removal of the gate: they are the whole of what makes
     * those bytes worthless elsewhere.
     *
     * The sweep is over EVERY blob rather than a chosen one, because the failure this catches is
     * an artifact that was never given a tier and so was never sealed -- which is how `device.key`
     * came to sit in the clear.
     */
    @Test
    fun an_attacker_holding_the_bytes_without_the_keystore_reaches_nothing() {
        val contentKey = tierKeyBytes(0x70)
        val recipientPriv = tierKeyBytes(0x30)
        val kek = FakeKeystoreKek()
        val store = SealedStore(kek)

        store.put(CustodyBlobs.tierKey(KeyTier.WAKE), KeyTier.WAKE, tierKeyBytes(0x10))
        store.put(CustodyBlobs.tierKey(KeyTier.CONTENT), KeyTier.CONTENT, contentKey)
        for (role in KeyRole.entries) {
            val material = if (role == KeyRole.RECIPIENT) recipientPriv else tierKeyBytes(0x50)
            store.put(CustodyBlobs.deviceRole(role), KeyTierPolicy.tierOf(role), material)
        }
        if (SealedGrantPolicy.retention == GrantRetention.RETAINED_UNDER_CONTENT_TIER) {
            // The grant seals both epoch keys to the recipient key; modelled as a blob that
            // literally contains the content key, because that is what opening it yields.
            store.put(CustodyBlobs.SEALED_GRANT, KeyTier.CONTENT, contentKey + recipientPriv)
        }

        // ANTI-VACUITY FIRST, because every assertion below is of the form "nothing was
        // reached": on the device that sealed them, these blobs really do open. A store that
        // held nothing at all would satisfy the sweep and prove no custody whatsoever.
        assertTrue("precondition: the store holds something", store.names().isNotEmpty())
        for (name in store.names()) {
            assertTrue(
                "$name does not open on the handset that sealed it, so the sweep below is over " +
                    "a store that never worked",
                store.open(name).isNotEmpty(),
            )
        }
        for (name in store.names()) {
            assertFalse(
                "$name sits at rest carrying its own plaintext: the seal is decoration",
                containsBytes(store.rawBytes(name), store.open(name)),
            )
        }

        // The bytes are now somewhere the Keystore entries are not.
        kek.missingTiers = KeyTier.entries.toSet()

        for (name in store.names()) {
            val opened = runCatching { store.open(name) }
            assertTrue(
                "$name opened off the device, without either tier KEK. Everything PB-SEC-1 " +
                    "claims about an extracted data directory rests on this refusing",
                opened.isFailure,
            )
            assertTrue(
                "$name refused with ${opened.exceptionOrNull()}, which is not a typed custody " +
                    "failure -- a caller that read it as 'no key yet' would carry on",
                opened.exceptionOrNull() is KeyCustodyException,
            )
        }
    }
}
