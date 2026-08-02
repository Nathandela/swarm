package dev.swarm.phone.keys

import java.security.SecureRandom
import javax.crypto.Cipher
import javax.crypto.spec.GCMParameterSpec
import javax.crypto.spec.SecretKeySpec

/**
 * Test scaffolding for slice S14. Fakes only -- no assertions live here.
 *
 * The Keystore fake does REAL AES-GCM under a JDK provider. That matters: PB-SEC-1's
 * criterion is "the persisted blob is not the raw key and does not decrypt without the
 * keystore key", and a fake whose `wrap` returned its input would make SealedStore's own test
 * pass over a store that persisted plaintext. Real AEAD also gives the "wrong KEK" case its
 * genuine failure mode (a tag mismatch) rather than a simulated one.
 *
 * What the fake CANNOT model, and what nothing on this machine can: that the KEK is really
 * held in hardware, that a real biometric prompt gates it, or that StrongBox behaves as
 * advertised. Those are PB-E2E-5, the deferred physical-handset gate.
 */

/** The pin, read from the staged toolchain.env so no API level is written twice. */
object Pin {
    private val values: Map<String, String> by lazy {
        val stream = javaClass.classLoader?.getResourceAsStream("toolchain.env")
            ?: error(
                "toolchain.env is not on the unit-test classpath. android/app/build.gradle.kts " +
                    "must stage it, or PB-KEY-8's matrix silently stops being bound to " +
                    "PB-RUN-1's minSdk",
            )
        val assignment = Regex("""^\s*(SWARM_[A-Z0-9_]+)=(.*)$""")
        stream.bufferedReader().useLines { lines ->
            lines.mapNotNull { assignment.find(it) }
                .associate { m -> m.groupValues[1] to m.groupValues[2].trim().trim('"') }
        }
    }

    fun int(key: String): Int {
        val raw = values[key] ?: error("android/toolchain.env does not export $key")
        return raw.toIntOrNull() ?: error("$key=$raw is not an integer")
    }

    val minSdk: Int get() = int("SWARM_ANDROID_MIN_SDK")
}

/**
 * A KekProvider backed by real AES-GCM, one key per tier, with a gate that can be driven the
 * way the platform drives it: by REFUSING, not by a flag the code under test can read.
 */
class FakeKeystoreKek(
    /**
     * Tiers whose key refuses to unwrap for want of a user authentication.
     *
     * THE DEFAULT IS EMPTY BECAUSE NOTHING THIS BUILD PROVISIONS CAN BE LOCKED (ADR-007 B133):
     * every tier KEK is generated with `setUserAuthenticationRequired(false)`. It defaulted to
     * `setOf(CONTENT)` and that default was what kept four tests green over a state production
     * could no longer enter -- a fixture asserting the old decision on behalf of every test
     * that never mentioned it.
     *
     * IT IS NOT REMOVED, because one population can still raise it: an install provisioned
     * BEFORE B133, whose content KEK still carries `AUTH_BIOMETRIC_STRONG` and which
     * `KeystoreCustodyBootstrap.ensure` does not re-spec because the alias already exists
     * (`KeyCustodyException.UserAuthenticationRequired`'s own note). A test that wants that
     * handset must now ASK for it -- and must assert the verdict production actually gives it,
     * which is permanent-and-re-pair, not prompt-and-retry.
     */
    var lockedTiers: Set<KeyTier> = emptySet(),
    /** Tiers whose key has been destroyed by a biometric enrollment change. */
    var invalidatedTiers: Set<KeyTier> = emptySet(),
    /** Tiers whose Keystore entry is gone entirely. */
    var missingTiers: Set<KeyTier> = emptySet(),
) : KekProvider {

    private val random = SecureRandom()
    private val keys: Map<KeyTier, ByteArray> = KeyTier.entries.associateWith { tier ->
        ByteArray(32).also { random.nextBytes(it) }.also { it[0] = tier.ordinal.toByte() }
    }

    /** Every array this provider has ever handed out, so a test can prove they were zeroized. */
    val handedOut = mutableListOf<ByteArray>()

    override fun wrap(tier: KeyTier, plaintext: ByteArray): ByteArray {
        val iv = ByteArray(12).also { random.nextBytes(it) }
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.ENCRYPT_MODE, secret(tier), GCMParameterSpec(128, iv))
        return iv + cipher.doFinal(plaintext)
    }

    override fun unwrap(tier: KeyTier, blob: ByteArray): ByteArray {
        if (tier in invalidatedTiers) throw KeyCustodyException.KeyPermanentlyInvalidated(tier)
        if (tier in missingTiers) throw KeyCustodyException.KeystoreKeyMissing(tier.name)
        if (tier in lockedTiers) throw KeyCustodyException.UserAuthenticationRequired(tier)
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.DECRYPT_MODE, secret(tier), GCMParameterSpec(128, blob, 0, 12))
        val out = try {
            cipher.doFinal(blob, 12, blob.size - 12)
        } catch (e: javax.crypto.AEADBadTagException) {
            throw KeyCustodyException.KeystoreKeyMissing(tier.name).initCause(e) as KeyCustodyException
        }
        handedOut += out
        return out
    }

    private fun secret(tier: KeyTier) = SecretKeySpec(keys.getValue(tier), "AES")

    fun unlockAll() {
        lockedTiers = emptySet()
    }
}

/**
 * Records the inbound crossing. It COPIES what it is given, because the custody layer
 * zeroizes the array it passed -- a fake that kept the reference would observe zeros and
 * make the "installed the right bytes" assertion vacuous.
 */
class RecordingCore : CoreKeyCustody {
    val installedWake = mutableListOf<ByteArray>()
    val installedContent = mutableListOf<ByteArray>()
    var purgeCount = 0
        private set

    override fun installWakeKey(key: ByteArray) {
        installedWake += key.copyOf()
    }

    override fun installContentKey(key: ByteArray) {
        installedContent += key.copyOf()
    }

    override fun purgeKeys() {
        purgeCount++
    }
}

/** A fixed 32-byte tier key with recognisable bytes, so a leak is findable in any buffer. */
fun tierKeyBytes(marker: Byte): ByteArray = ByteArray(32) { (marker + it).toByte() }

/**
 * Every ByteArray reachable from [target]'s own fields, including through arrays and
 * collections. PB-KEY-7's purge is about live memory, so the test has to look at what the
 * object still holds rather than at what it was handed -- an implementation that caches the
 * unwrapped key to avoid re-prompting keeps it in a field, not in the caller's buffer.
 */
fun reachableByteArrays(target: Any): List<ByteArray> {
    val found = mutableListOf<ByteArray>()

    fun collect(value: Any?) {
        when (value) {
            null -> return
            is ByteArray -> found += value
            is Array<*> -> value.forEach(::collect)
            is Iterable<*> -> value.forEach(::collect)
            is Map<*, *> -> {
                value.keys.forEach(::collect)
                value.values.forEach(::collect)
            }
        }
    }

    var cls: Class<*>? = target.javaClass
    while (cls != null && cls != Any::class.java) {
        for (field in cls.declaredFields) {
            if (java.lang.reflect.Modifier.isStatic(field.modifiers)) continue
            runCatching {
                field.isAccessible = true
                collect(field.get(target))
            }
        }
        cls = cls.superclass
    }
    return found
}

/** True when [haystack] contains [needle] anywhere. Used to hunt for leaked key bytes. */
fun containsBytes(haystack: ByteArray, needle: ByteArray): Boolean {
    if (needle.isEmpty() || needle.size > haystack.size) return false
    outer@ for (i in 0..haystack.size - needle.size) {
        for (j in needle.indices) if (haystack[i + j] != needle[j]) continue@outer
        return true
    }
    return false
}
