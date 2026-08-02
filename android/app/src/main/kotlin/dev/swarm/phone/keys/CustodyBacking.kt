package dev.swarm.phone.keys

import java.io.File

/**
 * Phase B slice S16 -- where the sealed blobs actually live.
 *
 * WHY THIS EXISTS. [SealedStore] used to be a `LinkedHashMap`, so the sealed material lived for
 * exactly one process while its own doc called the bytes "the persisted bytes, exactly as they
 * sit on disk". On Android a process death is routine, so the second launch found `device.key`
 * and `phone-state.json` sealed under a state KEK that no longer existed: permanently
 * unopenable, and unrecoverable, because the material needed to open them was gone.
 *
 * THE LOCATION IS INJECTED, NEVER LOOKED UP. The custody policy in this package is testable on
 * a plain JVM precisely because nothing in it reaches for a `Context`; a backing that resolved
 * `filesDir` for itself would drag every test in the package onto Robolectric and would give
 * the store a second, invisible input. [FileCustodyBacking] takes a directory, and
 * `dev.swarm.phone.PhoneRuntime` -- which is the only thing here that has a Context -- decides
 * which one.
 *
 * WHAT IS NOT CLAIMED. Nothing here is a statement about hardware. Whether the KEK that sealed
 * these bytes is really in a TEE is PB-E2E-5, the deferred physical-handset gate; this file is
 * about whether the bytes outlive the object that wrote them.
 */

/**
 * One sealed blob and the tier whose KEK opens it.
 *
 * THE TIER IS PERSISTED WITH THE BLOB and is not re-derived from the entry name. It decides
 * which KEK is asked for the unwrap, so an entry that came back on the wrong tier would open
 * the content key with no user present -- PB-KEY-2's split collapsed by a restart, with every
 * round-trip test still green.
 */
class SealedRecord(val tier: KeyTier, val blob: ByteArray)

/**
 * The store's durable half: named sealed blobs that outlive the process.
 *
 * It carries CIPHERTEXT ONLY. Every value handed to [save] has already been through the tier's
 * KEK, so an implementation may write it anywhere the app can write -- the confidentiality is
 * the seal's, not the medium's. That is also why no method here can fail on authorisation:
 * asking the KEK is [SealedStore]'s job and happens on every open.
 */
interface PersistentCustodyBacking {

    /** Every entry the backing holds. An empty map is a FIRST LAUNCH, and is load-bearing:
     *  `KeystoreCustodyBootstrap` reads it to tell a fresh install from a destroyed key. */
    fun load(): Map<String, SealedRecord>

    fun save(name: String, record: SealedRecord)

    fun delete(name: String)

    companion object {
        /**
         * A backing that models the CONTRACT and nothing else. Named for what it is: it keeps
         * bytes for exactly one process, which is the defect this file was written to remove,
         * so a production caller that reached for it would be re-introducing the brick under a
         * name that says so.
         */
        fun inMemoryForTest(): PersistentCustodyBacking = VolatileCustodyBacking()

        /**
         * The implementation the handset ships.
         *
         * It answers the TYPE rather than an instance, and that is not evasion: a durable
         * backing needs a directory, the directory comes from a `Context`, and a companion
         * that reached for one statically would be the static lookup this file exists to
         * avoid. What the declaration buys is checked by the COMPILER -- the return type
         * requires the named class to implement this contract -- and what it stops is the
         * whole contract above being satisfied by a test double forever.
         */
        fun production(): Class<out PersistentCustodyBacking> = FileCustodyBacking::class.java
    }
}

/**
 * Bytes for one process. It is what [SealedStore]'s one-argument constructor uses, so the
 * pure-JVM policy tests need no filesystem, and it must never be what the app uses.
 */
class VolatileCustodyBacking : PersistentCustodyBacking {

    private val entries = LinkedHashMap<String, SealedRecord>()

    override fun load(): Map<String, SealedRecord> = LinkedHashMap(entries)

    override fun save(name: String, record: SealedRecord) {
        entries[name] = record
    }

    override fun delete(name: String) {
        entries.remove(name)
    }
}

/**
 * The production backing: one file per entry in an app-private directory.
 *
 * ONE FILE PER ENTRY, not one file holding all of them, because the failure modes differ. A
 * single container has to be rewritten in full for every put, so a torn write loses entries
 * that were not being changed -- and the two state KEKs are exactly the entries whose loss is
 * unrecoverable. Per-entry files also make [load] tolerant in the only direction that is safe:
 * an entry that is not there is absent, and an entry that is there is whole.
 *
 * EVERY WRITE IS STAGED AND RENAMED. `rename` within one directory is atomic, so a reader --
 * including the next launch after a kill mid-write -- sees either the old blob or the new one.
 * Writing in place would leave a truncated state KEK, which is not a lost setting but a state
 * directory nothing can ever open again.
 *
 * A RECORD THAT WILL NOT PARSE IS A REFUSAL, NEVER A SKIP. Dropping an unreadable entry would
 * present a paired handset as a fresh install, which silently discards the user's pairing --
 * the exact direction of the fresh-install defect this phase already paid for once.
 */
class FileCustodyBacking(private val dir: File) : PersistentCustodyBacking {

    override fun load(): Map<String, SealedRecord> {
        val files = dir.listFiles { file: File -> file.isFile && file.name.endsWith(SUFFIX) }
            ?: return emptyMap()
        return files.sortedBy { it.name }.associate { file ->
            val name = file.name.removeSuffix(SUFFIX)
            name to decode(name, file.readBytes())
        }
    }

    override fun save(name: String, record: SealedRecord) {
        dir.mkdirs()
        val target = File(dir, fileNameOf(name))
        val staged = File(dir, fileNameOf(name) + STAGING_SUFFIX)
        staged.writeBytes(encode(record))
        if (!staged.renameTo(target)) {
            staged.delete()
            throw KeyCustodyException.Unexpected(
                record.tier,
                "could not put $name in place at ${target.path}",
            )
        }
    }

    override fun delete(name: String) {
        File(dir, fileNameOf(name)).delete()
    }

    private fun encode(record: SealedRecord): ByteArray =
        (record.tier.name + "\n").toByteArray(Charsets.US_ASCII) + record.blob

    private fun decode(name: String, raw: ByteArray): SealedRecord {
        val split = raw.indexOf(NEWLINE)
        if (split <= 0) throw KeyCustodyException.KeystoreKeyMissing(name)
        val tier = runCatching {
            KeyTier.valueOf(String(raw, 0, split, Charsets.US_ASCII))
        }.getOrElse { throw KeyCustodyException.KeystoreKeyMissing(name) }
        return SealedRecord(tier, raw.copyOfRange(split + 1, raw.size))
    }

    /**
     * The entry name IS the file name, so it is checked rather than trusted. Every name in
     * [CustodyBlobs] is already safe; the guard is here because a name that ever came from
     * outside would otherwise be a path.
     */
    private fun fileNameOf(name: String): String {
        require(SAFE_NAME.matches(name) && !name.contains("..")) {
            "$name is not a usable custody entry name"
        }
        return name + SUFFIX
    }

    private companion object {
        const val SUFFIX = ".sealed"
        const val STAGING_SUFFIX = ".writing"
        const val NEWLINE = '\n'.code.toByte()
        val SAFE_NAME = Regex("[A-Za-z0-9._-]+")
    }
}
