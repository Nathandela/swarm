package dev.swarm.phone.keys

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * PB-KEY-5 -- "Custody tier per role, not one undifferentiated core key... Assign a tier to
 * each of {NoiseStatic, Recipient, CommandSign, RelayAuth} and state whether the sealed grant
 * blob is discarded after opening or retained under the content tier."
 *
 * Acceptance criterion, verbatim: "an attacker with after-first-unlock access and everything
 * at rest reaches no content key."
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
     *    (internal/remote/crypto/keystore.go:163). An after-first-unlock recipient key plus
     *    the persisted grant hands a stolen once-unlocked handset the content key -- and
     *    falsifies ADR-007:89 inside the phase meant to implement it.
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
     * PB-KEY-5's criterion, driven directly.
     *
     * The attacker has a stolen handset that has been unlocked at least once since boot (so
     * the wake tier is usable) and every byte the app has at rest. They do not have a
     * biometric. Nothing they can open may yield the content key.
     */
    @Test
    fun an_after_first_unlock_attacker_reaches_no_content_key() {
        val contentKey = tierKeyBytes(0x70)
        val recipientPriv = tierKeyBytes(0x30)
        val kek = FakeKeystoreKek(lockedTiers = setOf(KeyTier.CONTENT))
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

        val reachable = mutableMapOf<String, ByteArray>()
        for (name in store.names()) {
            runCatching { store.open(name) }.onSuccess { reachable[name] = it }
        }

        assertTrue(
            "the attacker reached NOTHING, so this assertion proves nothing. The wake tier " +
                "must really open after first unlock",
            reachable.isNotEmpty(),
        )
        for ((name, plaintext) in reachable) {
            assertFalse(
                "$name is reachable after first unlock and yields the content key",
                containsBytes(plaintext, contentKey),
            )
            assertFalse(
                "$name is reachable after first unlock and yields the recipient key, which " +
                    "opens any retained grant and therefore the content key",
                containsBytes(plaintext, recipientPriv),
            )
        }
    }

    /**
     * The same attacker, at the layer above: the custody session must refuse, and the refusal
     * must be the typed one -- not a null, not an empty array, not a silently absent key that
     * the next content operation discovers as a decrypt failure.
     */
    @Test
    fun the_custody_session_refuses_the_after_first_unlock_attacker() {
        val kek = FakeKeystoreKek(lockedTiers = setOf(KeyTier.CONTENT))
        val store = SealedStore(kek)
        val core = RecordingCore()
        store.put(CustodyBlobs.tierKey(KeyTier.CONTENT), KeyTier.CONTENT, tierKeyBytes(0x70))
        store.put(CustodyBlobs.deviceRole(KeyRole.RECIPIENT), KeyTier.CONTENT, tierKeyBytes(0x30))

        val session = KeyCustodySession(store, core)

        assertThrows(KeyCustodyException.UserAuthenticationRequired::class.java) {
            session.installTier(KeyTier.CONTENT)
        }
        assertEquals(0, core.installedContent.size)
        assertFalse(session.contentAvailable())
    }
}
