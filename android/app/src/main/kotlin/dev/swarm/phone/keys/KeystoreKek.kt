package dev.swarm.phone.keys

import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyInfo
import android.security.keystore.KeyProperties
import java.security.KeyStore
import java.security.SecureRandom
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.SecretKeyFactory
import javax.crypto.spec.GCMParameterSpec

/**
 * Phase B slice S16 -- the far side of the [KekProvider] seam, in production.
 *
 * Until this file existed the only implementation in the repository was a software AES key held
 * in the test JVM, so every Go-side fence proving the facade seals correctly was proving it over
 * a path the handset could not take.
 *
 * WHAT IS ASSERTED AND WHAT IS NOT, because this is the file most likely to be misread as
 * hardware coverage. The policy tests around this package cover which spec is REQUESTED, which
 * failures are DISTINGUISHED and what the code does with each. That the KEK is really in a TEE,
 * that a real biometric prompt gates the content tier, that StrongBox behaves as advertised:
 * PB-E2E-5, the physical-handset gate, DEFERRED. Nothing here may be read as covering it.
 */

/** The Keystore provider name. Named once; a typo here is a silent software key. */
private const val ANDROID_KEYSTORE = "AndroidKeyStore"

/** AES-GCM, 96-bit nonce and a 128-bit tag -- the shape ADR-007 B8's KEK wraps under. */
private const val TRANSFORMATION = "AES/GCM/NoPadding"
private const val IV_BYTES = 12
private const val TAG_BITS = 128

/** The per-tier data key the Go core seals its state directory with. AES-256. */
private const val STATE_KEY_BYTES = 32

/**
 * The Keystore entries themselves: does this alias exist, and drop it.
 *
 * It is a separate object from the provider because the two ask different questions. The
 * provider asks the platform to USE a key; this asks whether the entry is there at all, which
 * is half of the discrimination [KeystoreCustodyBootstrap] makes, and getting those two
 * confused is how "the key is gone" becomes "this must be a fresh install".
 */
internal object KeystoreEntries {

    fun exists(alias: String): Boolean = keystore().containsAlias(alias)

    fun drop(alias: String) {
        keystore().deleteEntry(alias)
    }

    /**
     * A fresh handle per call. `KeyStore.load` is what refreshes the view of the entries, and a
     * handle cached in a field would answer from whatever the process saw at startup -- which
     * is exactly the window in which an enrollment change deletes a key.
     */
    fun keystore(): KeyStore = KeyStore.getInstance(ANDROID_KEYSTORE).apply { load(null) }
}

/**
 * PB-KEY-9's Android half: the authenticated Keystore AES KEK, one per tier (ADR-007 B8).
 *
 * IT HOLDS NO KEY IN A FIELD, and that is the property the content tier's gate rests on. The
 * `SecretKey` is fetched from Keystore for EVERY wrap and EVERY unwrap, so an auth-gated key
 * re-checks authorisation each time and a locked handset makes `unwrap` fail. A provider that
 * cached the handle -- or worse, the unwrapped bytes -- would keep decrypting content after the
 * screen locked (PB-KEY-7) while every restart-based test still passed.
 *
 * EVERY PLATFORM FAILURE BECOMES A TYPED ONE, through [PlatformFailure] rather than through a
 * second mapping written here. Two copies of that table is two things to get wrong, and the way
 * it goes wrong is silent: both Keystore exceptions extend `java.security.InvalidKeyException`,
 * so branching on the supertype -- which is what the Cipher API declares -- turns a PERMANENT
 * invalidation into a prompt the user can never satisfy.
 */
class KeystoreKekProvider : KekProvider {

    override fun wrap(tier: KeyTier, plaintext: ByteArray): ByteArray = guarded(tier) {
        val cipher = Cipher.getInstance(TRANSFORMATION)
        // NO IV IS SUPPLIED. Keystore requires randomized encryption for these keys and refuses
        // a caller-chosen nonce, which is the right refusal: a repeated GCM nonce under one key
        // is a total break, and this is the one place a well-meaning "deterministic for tests"
        // change could introduce one.
        cipher.init(Cipher.ENCRYPT_MODE, secretKey(tier))
        cipher.iv + cipher.doFinal(plaintext)
    }

    override fun unwrap(tier: KeyTier, blob: ByteArray): ByteArray = guarded(tier) {
        if (blob.size <= IV_BYTES) {
            throw KeyCustodyException.KeystoreKeyMissing(KeystoreAliases.forTier(tier))
        }
        val cipher = Cipher.getInstance(TRANSFORMATION)
        cipher.init(
            Cipher.DECRYPT_MODE,
            secretKey(tier),
            GCMParameterSpec(TAG_BITS, blob, 0, IV_BYTES),
        )
        cipher.doFinal(blob, IV_BYTES, blob.size - IV_BYTES)
    }

    /**
     * The entry, or a typed refusal. A missing entry is [KeyCustodyException.KeystoreKeyMissing]
     * rather than a null the caller has to remember to check.
     */
    private fun secretKey(tier: KeyTier): SecretKey {
        val alias = KeystoreAliases.forTier(tier)
        return KeystoreEntries.keystore().getKey(alias, null) as? SecretKey
            ?: throw KeyCustodyException.KeystoreKeyMissing(alias)
    }

    /**
     * A custody failure passes through unchanged; anything else the platform threw is
     * classified. Re-wrapping an already-typed failure would strip the verdict token the Go
     * core reads, and an unclassifiable refusal makes `phonecore.openSealedDeviceKeys` refuse a
     * Resume outright -- a locked handset would become an app that cannot start.
     */
    private inline fun guarded(tier: KeyTier, body: () -> ByteArray): ByteArray = try {
        body()
    } catch (e: KeyCustodyException) {
        throw e
    } catch (e: Throwable) {
        throw PlatformFailure.map(tier, e)
    }
}

/**
 * [KeystoreProvisioner] over the real platform. `KeyGenerator.init(spec)` is where the
 * KeyGenParameterSpec in [KeystoreSpecs] stops being a description and becomes a key.
 *
 * IT LETS THE PLATFORM'S EXCEPTIONS OUT, deliberately: [CustodyProvisioning] catches
 * `StrongBoxUnavailableException` to fall back, and swallowing it here would hand it a
 * generator that quietly succeeded with nothing generated.
 */
class AndroidKeystoreProvisioner : KeystoreProvisioner {

    override fun generate(spec: KeyGenParameterSpec) {
        val generator = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, ANDROID_KEYSTORE)
        generator.init(spec)
        generator.generateKey()
    }
}

/**
 * [KeyInfoReader] over the real platform -- the read-back half of PB-KEY-8.
 *
 * It reports what the platform ACHIEVED, never what was asked for. A key whose KeyInfo is never
 * read is how a software fallback ships unnoticed: the request said per-use and auth-required,
 * the platform quietly gave neither, and every test above it still passes.
 */
class AndroidKeyInfoReader : KeyInfoReader {

    override fun read(alias: String): KeyInfoRecord {
        val key = KeystoreEntries.keystore().getKey(alias, null) as? SecretKey
            ?: throw KeyCustodyException.KeystoreKeyMissing(alias)
        val info = SecretKeyFactory.getInstance(key.algorithm, ANDROID_KEYSTORE)
            .getKeySpec(key, KeyInfo::class.java) as KeyInfo
        return KeyInfoRecord(
            securityLevel = securityLevelOf(info.securityLevel),
            userAuthenticationRequired = info.isUserAuthenticationRequired,
            userAuthenticationValidityDurationSeconds = info.userAuthenticationValidityDurationSeconds,
            invalidatedByBiometricEnrollment = info.isInvalidatedByBiometricEnrollment,
        )
    }

    /**
     * The platform's int onto [KeystoreSecurityLevel], one constant per constant.
     *
     * UNKNOWN_SECURE and UNKNOWN are kept APART, which the boolean this replaced could not
     * do: the first is the platform declining to name the enclave, the second is the
     * platform declining to claim one at all. Only the second shares a bucket with SOFTWARE
     * under "did not affirm hardware", and only SOFTWARE is a denial the design refuses.
     *
     * A level the platform adds later falls to UNKNOWN and NOT to SOFTWARE. The wrong way
     * round would refuse a device over a constant that did not exist when this was written.
     */
    private fun securityLevelOf(level: Int): KeystoreSecurityLevel = when (level) {
        KeyProperties.SECURITY_LEVEL_STRONGBOX -> KeystoreSecurityLevel.STRONGBOX
        KeyProperties.SECURITY_LEVEL_TRUSTED_ENVIRONMENT -> KeystoreSecurityLevel.TRUSTED_ENVIRONMENT
        KeyProperties.SECURITY_LEVEL_UNKNOWN_SECURE -> KeystoreSecurityLevel.UNKNOWN_SECURE
        KeyProperties.SECURITY_LEVEL_SOFTWARE -> KeystoreSecurityLevel.SOFTWARE
        else -> KeystoreSecurityLevel.UNKNOWN
    }
}

/**
 * The launch-time decision the app cannot get wrong: is this a FIRST LAUNCH or a DESTROYED KEY?
 *
 * The two look identical from the alias alone -- neither has a usable Keystore key -- and the
 * remedies are opposites. Treating a destroyed key as a fresh install silently discards a
 * working pairing and every session behind it; treating a first launch as a destroyed key
 * bricks an install that has done nothing wrong, before the user has anything to lose.
 *
 * THE DISCRIMINATOR IS THE SEALED MATERIAL, not a flag written beside it. Blobs on the tier are
 * evidence that a KEK for that tier once existed and sealed them, and they are evidence that
 * survives the thing being asked about -- a marker file would be one more thing to keep in step
 * with reality, and it would be wrong in exactly the cases that matter.
 *
 * The OTHER route to the same verdict needs no help from this class: an enrollment change leaves
 * the alias in place and destroys the material behind it, so the platform itself throws
 * `KeyPermanentlyInvalidatedException` on first use and [PlatformFailure] types it.
 */
class KeystoreCustodyBootstrap(
    private val backing: PersistentCustodyBacking,
    private val provisioning: CustodyProvisioning,
    /** From the handset's own answer, not from optimism. StrongBox absence is a fallback. */
    private val strongBoxPreferred: Boolean,
) {

    /**
     * Make [tier]'s Keystore KEK usable, or say why it never will be again.
     *
     * @throws KeyCustodyException.KeyPermanentlyInvalidated when the alias is gone and sealed
     *  material for the tier is not: the bytes cannot be recovered, and PB-KEY-6's permanent
     *  verdict -- re-pair this device -- is the only honest answer.
     * @throws KeyCustodyException.KeystoreDowngrade when the platform generated something
     *  weaker than [KeystoreSpecs] asked for.
     */
    fun ensure(tier: KeyTier) {
        if (KeystoreEntries.exists(KeystoreAliases.forTier(tier))) return
        if (backing.load().values.any { it.tier == tier }) {
            throw KeyCustodyException.KeyPermanentlyInvalidated(tier)
        }
        provisioning.provision(tier, strongBoxPreferred)
    }

    /**
     * Generate the tier's state KEK on a first launch and seal it into [store].
     *
     * It is generated HERE and not by the Go core because it is the key the core seals its own
     * state directory with -- `swarmmobile.KeyCustody` hands it across, so the core can only
     * ever receive it. It must be stable for the life of the install: regenerating one would
     * leave `device.key` sealed under a key nothing holds, which is the brick this slice
     * removed, arriving by the other door.
     *
     * The plaintext is zeroized in a `finally`, so a failing seal does not leave it live on the
     * Java heap for the GC to reach eventually.
     */
    fun ensureStateKey(store: SealedStore, tier: KeyTier) {
        val name = CustodyBlobs.stateKek(tier)
        if (name in store.names()) return
        val key = ByteArray(STATE_KEY_BYTES).also { SecureRandom().nextBytes(it) }
        try {
            store.put(name, tier, key)
        } finally {
            key.fill(0)
        }
    }

    /**
     * The way out of the permanent verdict, and the reason it is a verdict rather than a dead
     * end: a re-pair discards the tier's unopenable blobs and its alias, so the next
     * [ensure] sees a genuine first launch.
     *
     * Nothing calls this automatically. An automatic discard is precisely the "treat it as
     * fresh" behaviour that silently throws away a pairing -- it has to be the user answering
     * the re-pair prompt.
     */
    fun discard(tier: KeyTier) {
        for ((name, record) in backing.load()) {
            if (record.tier == tier) backing.delete(name)
        }
        KeystoreEntries.drop(KeystoreAliases.forTier(tier))
    }
}
