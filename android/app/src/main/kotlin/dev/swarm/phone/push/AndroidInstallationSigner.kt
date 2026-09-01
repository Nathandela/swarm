package dev.swarm.phone.push

import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import java.math.BigInteger
import java.security.KeyPairGenerator
import java.security.KeyStore
import java.security.PrivateKey
import java.security.Signature
import java.security.interfaces.ECPublicKey
import java.security.spec.ECGenParameterSpec
import swarmmobile.PushInstallationSigner

private const val INSTALLATION_KEY_ALIAS = "swarm.push.installation.p256.v1"
private const val ANDROID_KEYSTORE = "AndroidKeyStore"

/** Android Keystore P-256 authority. The private key object is used only by Signature. */
class AndroidInstallationSigner : PushInstallationSigner {
    private val store: KeyStore = KeyStore.getInstance(ANDROID_KEYSTORE).apply { load(null) }

    init {
        ensureKey()
    }

    override fun publicKey(): ByteArray {
        val public = store.getCertificate(INSTALLATION_KEY_ALIAS)?.publicKey as? ECPublicKey
            ?: throw IllegalStateException("Android Keystore installation public key is unavailable")
        return byteArrayOf(0x04) + fixed32(public.w.affineX) + fixed32(public.w.affineY)
    }

    override fun sign(canonical: ByteArray): ByteArray {
        val privateKey = store.getKey(INSTALLATION_KEY_ALIAS, null) as? PrivateKey
            ?: throw IllegalStateException("Android Keystore installation key is unavailable")
        val signature = Signature.getInstance("SHA256withECDSA").apply {
            initSign(privateKey)
            update(canonical)
        }
        return P256Signatures.derToLowSP1363(signature.sign())
    }

    private fun ensureKey() {
        if (store.containsAlias(INSTALLATION_KEY_ALIAS)) return
        KeyPairGenerator.getInstance(KeyProperties.KEY_ALGORITHM_EC, ANDROID_KEYSTORE).apply {
            initialize(
                KeyGenParameterSpec.Builder(INSTALLATION_KEY_ALIAS, KeyProperties.PURPOSE_SIGN)
                    .setAlgorithmParameterSpec(ECGenParameterSpec("secp256r1"))
                    .setDigests(KeyProperties.DIGEST_SHA256)
                    .build(),
            )
            generateKeyPair()
        }
    }

    private fun fixed32(value: BigInteger): ByteArray {
        val unsigned = value.toByteArray().dropWhile { it == 0.toByte() }.toByteArray()
        check(unsigned.size <= 32) { "P-256 coordinate exceeds 32 bytes" }
        return ByteArray(32 - unsigned.size) + unsigned
    }
}

internal object P256Signatures {
    private val order = BigInteger(
        "FFFFFFFF00000000FFFFFFFFFFFFFFFFBCE6FAADA7179E84F3B9CAC2FC632551",
        16,
    )
    private val halfOrder = order.shiftRight(1)

    fun derToLowSP1363(der: ByteArray): ByteArray {
        val reader = DerReader(der)
        reader.expect(0x30)
        val sequenceLength = reader.length()
        require(sequenceLength == reader.remaining) { "ECDSA DER sequence length mismatch" }
        val r = reader.positiveInteger()
        val sRaw = reader.positiveInteger()
        require(reader.remaining == 0) { "ECDSA DER has trailing data" }
        require(r.signum() > 0 && r < order && sRaw.signum() > 0 && sRaw < order) {
            "ECDSA scalar is outside P-256"
        }
        val s = if (sRaw > halfOrder) order.subtract(sRaw) else sRaw
        return fixedScalar(r) + fixedScalar(s)
    }

    private fun fixedScalar(value: BigInteger): ByteArray {
        val raw = value.toByteArray().dropWhile { it == 0.toByte() }.toByteArray()
        require(raw.size <= 32) { "ECDSA scalar exceeds P-256" }
        return ByteArray(32 - raw.size) + raw
    }

    private class DerReader(private val bytes: ByteArray) {
        private var offset = 0
        val remaining: Int get() = bytes.size - offset

        fun expect(tag: Int) {
            require(read() == tag) { "unexpected ECDSA DER tag" }
        }

        fun length(): Int {
            val first = read()
            if (first and 0x80 == 0) return first
            val count = first and 0x7f
            require(count in 1..2 && count <= remaining) { "invalid ECDSA DER length" }
            var value = 0
            repeat(count) { value = (value shl 8) or read() }
            require(value >= 128) { "non-canonical ECDSA DER length" }
            return value
        }

        fun positiveInteger(): BigInteger {
            expect(0x02)
            val size = length()
            require(size in 1..33 && size <= remaining) { "invalid ECDSA DER integer length" }
            val raw = bytes.copyOfRange(offset, offset + size)
            offset += size
            require(raw[0].toInt() and 0x80 == 0) { "negative ECDSA DER integer" }
            require(size == 1 || raw[0] != 0.toByte() || raw[1].toInt() and 0x80 != 0) {
                "non-canonical ECDSA DER integer"
            }
            return BigInteger(1, raw)
        }

        private fun read(): Int {
            require(offset < bytes.size) { "truncated ECDSA DER" }
            return bytes[offset++].toInt() and 0xff
        }
    }
}
