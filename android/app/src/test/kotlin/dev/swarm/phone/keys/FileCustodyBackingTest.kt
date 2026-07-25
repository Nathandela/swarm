package dev.swarm.phone.keys

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File

/**
 * The PRODUCTION backing, over real files.
 *
 * CustodyPersistenceTest states the contract and drives it through
 * `PersistentCustodyBacking.inMemoryForTest()`, which is right -- the contract is what
 * [SealedStore] depends on. But a double can satisfy that contract forever, and the thing the
 * handset actually runs is [FileCustodyBacking]: the only copy of the two state KEKs goes
 * through it, and if it loses them `device.key` and `phone-state.json` are permanently
 * unopenable. `a production backing exists` can only assert that the type is there.
 *
 * Plain JVM, and it claims nothing beyond file I/O. Whether the KEK that sealed these bytes is
 * really in a TEE is PB-E2E-5, the deferred physical-handset gate; §10's tier order puts a
 * question about files on the cheapest tier that can answer it.
 */
class FileCustodyBackingTest {

    private fun tempDir(): File =
        java.nio.file.Files.createTempDirectory("swarm-custody").toFile().also { it.deleteOnExit() }

    private fun blob(marker: Byte) = ByteArray(48) { (marker + it).toByte() }

    /** The property, over the medium the app uses: the object goes and the bytes do not. */
    @Test
    fun a_record_written_by_one_backing_is_found_by_the_next_over_the_same_directory() {
        val dir = tempDir()
        FileCustodyBacking(dir).save("probe", SealedRecord(KeyTier.CONTENT, blob(0x40)))

        val restored = FileCustodyBacking(dir).load()

        assertEquals(setOf("probe"), restored.keys)
        assertArrayEquals(blob(0x40), restored.getValue("probe").blob)
    }

    /**
     * The tier is persisted WITH the blob. It decides which KEK is asked for the unwrap, so a
     * record that came back on the wake tier would open the content key with no user present --
     * PB-KEY-2's split collapsed by a restart, with every round-trip assertion still green.
     */
    @Test
    fun the_tier_is_part_of_the_record_and_not_re_derived_from_the_name() {
        val dir = tempDir()
        val backing = FileCustodyBacking(dir)
        backing.save("wake-thing", SealedRecord(KeyTier.WAKE, blob(0x10)))
        backing.save("content-thing", SealedRecord(KeyTier.CONTENT, blob(0x70)))

        val restored = FileCustodyBacking(dir).load()

        assertEquals(KeyTier.WAKE, restored.getValue("wake-thing").tier)
        assertEquals(KeyTier.CONTENT, restored.getValue("content-thing").tier)
    }

    /** A blob that is all zeroes, or empty, must survive as itself rather than as an absence. */
    @Test
    fun an_empty_blob_round_trips_as_an_entry_that_exists() {
        val dir = tempDir()
        FileCustodyBacking(dir).save("empty", SealedRecord(KeyTier.WAKE, ByteArray(0)))

        val restored = FileCustodyBacking(dir).load()

        assertTrue("an entry with no bytes is still an entry", restored.containsKey("empty"))
        assertEquals(0, restored.getValue("empty").blob.size)
    }

    @Test
    fun a_deleted_record_is_gone_from_the_next_load() {
        val dir = tempDir()
        val backing = FileCustodyBacking(dir)
        backing.save("probe", SealedRecord(KeyTier.WAKE, blob(0x10)))
        backing.delete("probe")

        assertTrue(FileCustodyBacking(dir).load().isEmpty())
    }

    /**
     * THE ONE THAT MATTERS MOST, and the direction a tidy implementation gets wrong: a record
     * that will not parse must REFUSE, never be skipped.
     *
     * A skipped record presents a paired handset as a fresh install -- the store looks empty,
     * the bootstrap decides this is a first launch, and the user's pairing and every session
     * behind it are silently discarded. The same failure shape as the fresh-install defect this
     * phase already paid for once.
     */
    @Test
    fun a_record_that_will_not_parse_refuses_instead_of_presenting_as_a_fresh_install() {
        val dir = tempDir()
        FileCustodyBacking(dir).save("probe", SealedRecord(KeyTier.CONTENT, blob(0x40)))
        File(dir, "probe.sealed").writeBytes("NOT_A_TIER\nstill sealed bytes".toByteArray())

        assertThrows(
            "an unreadable record was dropped, so the store reports itself empty and the next " +
                "launch looks like a first launch",
            KeyCustodyException.KeystoreKeyMissing::class.java,
        ) {
            FileCustodyBacking(dir).load()
        }
    }

    /** A directory that was never written to is a first launch, not an error. */
    @Test
    fun a_directory_that_does_not_exist_yet_loads_as_empty() {
        assertTrue(FileCustodyBacking(File(tempDir(), "never-written")).load().isEmpty())
    }

    /**
     * The store built over it sees the same thing, which is the join the app depends on: the
     * backing is durable AND [SealedStore] reads it at construction.
     */
    @Test
    fun a_sealed_store_over_the_file_backing_opens_what_a_previous_store_sealed() {
        val dir = tempDir()
        val kek = FakeKeystoreKek(lockedTiers = emptySet())
        val key = tierKeyBytes(0x70)

        SealedStore.openOver(kek, FileCustodyBacking(dir))
            .put(CustodyBlobs.stateKek(KeyTier.CONTENT), KeyTier.CONTENT, key)
        val restarted = SealedStore.openOver(kek, FileCustodyBacking(dir))

        assertArrayEquals(key, restarted.open(CustodyBlobs.stateKek(KeyTier.CONTENT)))
        assertEquals(KeyTier.CONTENT, restarted.tierOf(CustodyBlobs.stateKek(KeyTier.CONTENT)))
    }
}
