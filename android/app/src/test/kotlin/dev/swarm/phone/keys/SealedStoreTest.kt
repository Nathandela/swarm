package dev.swarm.phone.keys

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * PB-SEC-1 -- "Key material at rest is sealed under an Android-Keystore-backed KEK per the
 * PB-KEY-1 custody contract and the PB-KEY-2 tier split."
 *
 * Criterion: "Persisted blob is not the raw key and does not decrypt without the keystore
 * key."
 *
 * WHAT THIS COVERS AND WHAT IT DOES NOT. It covers the Kotlin-held half: the material the
 * custody layer persists itself. It does NOT cover the phone core's own state directory --
 * `device.key` (128 bytes of raw private scalars, crypto.NewFileKeyStore) and
 * `phone-state.json` (which carries `wake_key` and `content_key` as base64 fields,
 * internal/phonecore/state.go:176-177), both written by Go with no Keystore involvement.
 * `every_key_artifact_at_rest_is_sealed_by_the_keystore` below is the test that fails while
 * that is true, deliberately: it is the requirement, and it is not satisfiable by anything
 * inside this slice's scope. See the S14 RED report.
 *
 * Plain JVM. The KEK fake is real AES-GCM, so a store that persisted plaintext could not pass
 * by virtue of a permissive fake.
 */
class SealedStoreTest {

    @Test
    fun the_persisted_blob_is_not_the_raw_key() {
        val kek = FakeKeystoreKek(lockedTiers = emptySet())
        val store = SealedStore(kek)
        val key = tierKeyBytes(0x70)

        store.put(CustodyBlobs.tierKey(KeyTier.CONTENT), KeyTier.CONTENT, key)
        val persisted = store.rawBytes(CustodyBlobs.tierKey(KeyTier.CONTENT))

        assertFalse(
            "the raw key is sitting in the persisted blob",
            containsBytes(persisted, key),
        )
        assertTrue(
            "the blob is no longer than the key, so it carries neither nonce nor tag and " +
                "cannot be authenticated encryption",
            persisted.size > key.size,
        )
    }

    /** It must still round-trip, or "not the raw key" is satisfied by discarding it. */
    @Test
    fun a_sealed_blob_round_trips_under_its_own_tier() {
        val kek = FakeKeystoreKek(lockedTiers = emptySet())
        val store = SealedStore(kek)
        val key = tierKeyBytes(0x70)

        store.put(CustodyBlobs.tierKey(KeyTier.CONTENT), KeyTier.CONTENT, key)

        assertEquals(key.toList(), store.open(CustodyBlobs.tierKey(KeyTier.CONTENT)).toList())
        assertEquals(KeyTier.CONTENT, store.tierOf(CustodyBlobs.tierKey(KeyTier.CONTENT)))
    }

    /**
     * "Does not decrypt without the keystore key." The Keystore entry being gone -- app data
     * restored to a different device, the key rotated, the user cleared credentials -- must
     * surface as a typed failure. Returning the ciphertext, or an empty array, hands the
     * caller something that is not the key while looking like success.
     */
    @Test
    fun a_blob_does_not_decrypt_when_the_keystore_key_is_gone() {
        val kek = FakeKeystoreKek(lockedTiers = emptySet())
        val store = SealedStore(kek)
        store.put(CustodyBlobs.tierKey(KeyTier.CONTENT), KeyTier.CONTENT, tierKeyBytes(0x70))

        kek.missingTiers = setOf(KeyTier.CONTENT)

        assertThrows(KeyCustodyException.KeystoreKeyMissing::class.java) {
            store.open(CustodyBlobs.tierKey(KeyTier.CONTENT))
        }
    }

    /** The gate propagates from the store, not just from the session above it. */
    @Test
    fun a_content_blob_does_not_open_while_the_content_tier_is_gated() {
        val kek = FakeKeystoreKek(lockedTiers = setOf(KeyTier.CONTENT))
        val store = SealedStore(kek)
        store.put(CustodyBlobs.tierKey(KeyTier.WAKE), KeyTier.WAKE, tierKeyBytes(0x10))
        store.put(CustodyBlobs.tierKey(KeyTier.CONTENT), KeyTier.CONTENT, tierKeyBytes(0x70))

        assertEquals(
            "the wake tier must open while locked, or the split has collapsed the other way",
            tierKeyBytes(0x10).toList(),
            store.open(CustodyBlobs.tierKey(KeyTier.WAKE)).toList(),
        )
        assertThrows(KeyCustodyException.UserAuthenticationRequired::class.java) {
            store.open(CustodyBlobs.tierKey(KeyTier.CONTENT))
        }
    }

    // --- the inventory ------------------------------------------------------

    /**
     * Every artifact is accounted for. An artifact with no record is key material nobody
     * decided where to keep, which is how `device.key` came to sit in the clear.
     */
    @Test
    fun every_key_artifact_declares_where_it_lives_and_under_which_tier() {
        for (artifact in AtRestArtifact.entries) {
            val record = AtRestInventory.record(artifact)
            if (artifact == AtRestArtifact.SEALED_EPOCH_GRANT &&
                SealedGrantPolicy.retention == GrantRetention.DISCARDED_AFTER_OPEN
            ) {
                continue // covered by PerRoleCustodyTest; a discarded grant has no record
            }
            assertNotNull("$artifact is key material with no at-rest record", record)
            assertEquals(artifact, record!!.artifact)
            assertTrue("$artifact records no note explaining its custody", record.note.isNotBlank())
        }
    }

    /** The inventory must agree with the tier policy for the role keys. */
    @Test
    fun the_inventory_tiers_agree_with_the_per_role_assignment() {
        val byRole = mapOf(
            AtRestArtifact.DEVICE_NOISE_STATIC to KeyRole.NOISE_STATIC,
            AtRestArtifact.DEVICE_RECIPIENT to KeyRole.RECIPIENT,
            AtRestArtifact.DEVICE_COMMAND_SIGN to KeyRole.COMMAND_SIGN,
            AtRestArtifact.DEVICE_RELAY_AUTH to KeyRole.RELAY_AUTH,
        )
        for ((artifact, role) in byRole) {
            assertEquals(
                "$artifact is sealed under a different tier than $role is assigned",
                KeyTierPolicy.tierOf(role),
                AtRestInventory.record(artifact)!!.tier,
            )
        }
        assertEquals(KeyTier.WAKE, AtRestInventory.record(AtRestArtifact.EPOCH_WAKE_KEY)!!.tier)
        assertEquals(KeyTier.CONTENT, AtRestInventory.record(AtRestArtifact.EPOCH_CONTENT_KEY)!!.tier)
    }

    /**
     * PB-SEC-1 itself. Every artifact at rest is sealed under a Keystore-backed KEK -- whether
     * the custody layer wrote it or the Go core did.
     *
     * This is the test that stays RED until the phone core's state directory is sealed. It is
     * written as the requirement reads rather than as the code currently allows, because the
     * alternative -- scoping it to the artifacts that happen to be sealable today -- would
     * turn a known gap into a green build. See the S14 RED report for the two candidate
     * resolutions and why S15 needs the same one.
     */
    @Test
    fun every_key_artifact_at_rest_is_sealed_by_the_keystore() {
        for (artifact in AtRestArtifact.entries) {
            val record = AtRestInventory.record(artifact) ?: continue
            assertTrue(
                "$artifact lives in ${record.location} unsealed. PB-SEC-1: key material at " +
                    "rest is sealed under an Android-Keystore-backed KEK",
                record.sealedByKeystore,
            )
        }
    }
}
