package dev.swarm.phone.voice

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.AtomicFile
import org.json.JSONObject
import swarmmobile.VoiceDevice
import swarmmobile.VoiceEnrollmentSigner
import swarmmobile.VoiceSSHSigner
import java.io.File
import java.nio.ByteBuffer
import java.security.KeyPair
import java.security.KeyPairGenerator
import java.security.KeyStore
import java.security.PrivateKey
import java.security.Signature
import java.security.spec.ECGenParameterSpec
import java.util.Base64
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

internal data class VoiceProfile(
    val address: String, val user: String, val pin: String, val endpoint: String,
) {
    fun open(keys: VoiceKeys): VoiceDevice =
        VoiceDevice(address, user, pin, endpoint, keys.sshSigner, keys.enrollmentSigner)
}

internal class VoiceKeysUnavailable : IllegalStateException("Device does not support Ed25519 voice keys")

internal data class VoiceInvitation(val place: String, val reference: String, val endpoint: String) {
    companion object {
        fun parse(json: String): VoiceInvitation {
            require(json.length <= 2048)
            val fields = JSONObject(json)
            return VoiceInvitation(fields.getString("place_id"), fields.getString("reference"), fields.getString("endpoint"))
        }
    }
}

internal class VoiceKeys(private val ssh: KeyPair, private val enrollment: KeyPair) {
    init { require(!rawKey(ssh).contentEquals(rawKey(enrollment))) }

    val enrollmentPublic: String get() = Base64.getUrlEncoder().withoutPadding().encodeToString(rawKey(enrollment))
    val sshPublic: String get() = sshLine(rawKey(ssh))
    val sshSigner: VoiceSSHSigner = object : VoiceSSHSigner {
        override fun voiceSSHPublicKey() = encode(rawKey(ssh))
        override fun signVoiceSSH(message: String) = sign(ssh, message)
    }
    val enrollmentSigner: VoiceEnrollmentSigner = object : VoiceEnrollmentSigner {
        override fun voiceEnrollmentPublicKey() = encode(rawKey(enrollment))
        override fun signVoiceEnrollment(message: String) = sign(enrollment, message)
    }

    fun recordIdentity(json: JSONObject) {
        json.put("ssh_public", sshSigner.voiceSSHPublicKey())
        json.put("enrollment_public", enrollmentSigner.voiceEnrollmentPublicKey())
    }

    fun checkIdentity(json: JSONObject) {
        check(json.getString("ssh_public") == sshSigner.voiceSSHPublicKey())
        check(json.getString("enrollment_public") == enrollmentSigner.voiceEnrollmentPublicKey())
    }

    companion object {
        private val header = byteArrayOf(0x30, 0x2a, 0x30, 0x05, 0x06, 0x03, 0x2b, 0x65, 0x70, 0x03, 0x21, 0)
        private const val SSH_ALIAS = "swarm-voice-ssh-ed25519-v1"
        private const val ENROLLMENT_ALIAS = "swarm-voice-enrollment-ed25519-v1"

        @Synchronized
        fun open(create: Boolean): VoiceKeys {
            try {
                val store = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
                fun pair(alias: String): KeyPair {
                    val privateKey = store.getKey(alias, null) as? PrivateKey
                    if (privateKey != null) return KeyPair(store.getCertificate(alias).publicKey, privateKey)
                    check(create && !store.containsAlias(alias))
                    return KeyPairGenerator.getInstance("Ed25519", "AndroidKeyStore").run {
                        initialize(KeyGenParameterSpec.Builder(alias, KeyProperties.PURPOSE_SIGN or KeyProperties.PURPOSE_VERIFY)
                            .setAlgorithmParameterSpec(ECGenParameterSpec("ed25519"))
                            .setDigests(KeyProperties.DIGEST_NONE)
                            .build())
                        generateKeyPair()
                    }
                }
                return VoiceKeys(pair(SSH_ALIAS), pair(ENROLLMENT_ALIAS))
            } catch (_: Exception) {
                throw VoiceKeysUnavailable()
            }
        }

        private fun rawKey(key: KeyPair): ByteArray {
            val data = key.public.encoded
            require(data.size == header.size + 32 && data.copyOf(header.size).contentEquals(header))
            return data.copyOfRange(header.size, data.size)
        }
        private fun sshLine(raw: ByteArray): String {
            val algorithm = "ssh-ed25519".toByteArray(Charsets.US_ASCII)
            val wire = ByteBuffer.allocate(8 + algorithm.size + raw.size)
                .putInt(algorithm.size).put(algorithm).putInt(raw.size).put(raw).array()
            return "ssh-ed25519 ${encode(wire)} swarm-voice-android"
        }
        private fun sign(key: KeyPair, message: String): String {
            require(message.length <= 175_000)
            val decoded = decodeBase64(message)
            try {
                return encode(Signature.getInstance("Ed25519").run {
                    initSign(key.private)
                    update(decoded)
                    sign()
                })
            } finally { decoded.fill(0) }
        }
        private fun encode(bytes: ByteArray) = Base64.getEncoder().encodeToString(bytes)
        private fun decodeBase64(value: String) = Base64.getDecoder().decode(value)
    }
}

internal data class VoiceState(
    val keys: VoiceKeys,
    val profile: VoiceProfile? = null,
    val grant: String = "",
    val operation: String = "",
)

/** The voice file and its wrapping key are isolated from all dashboard identities. */
internal class VoiceStore(context: Context) {
    private val file = AtomicFile(File(context.noBackupFilesDir, "swarm-voice-device.enc"))
    private val alias = "swarm-voice-device-v1"
    private val aad = alias.toByteArray(Charsets.UTF_8)

    fun load(): VoiceState? {
        if (!file.baseFile.exists()) return null
        val blob = file.openRead().use { it.readNBytes(8193) }
        require(blob.size in 29..8192 && blob[0] == 1.toByte())
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.DECRYPT_MODE, key(create = false), GCMParameterSpec(128, blob, 1, 12))
        cipher.updateAAD(aad)
        val plain = cipher.doFinal(blob, 13, blob.size - 13)
        try {
            val data = JSONObject(plain.toString(Charsets.UTF_8))
            val profile = data.optJSONObject("profile")?.let {
                VoiceProfile(it.getString("address"), it.getString("user"), it.getString("pin"), it.getString("endpoint"))
            }
            val keys = VoiceKeys.open(create = false)
            keys.checkIdentity(data)
            return VoiceState(keys, profile, data.optString("voice_grant"), data.optString("operation"))
        } finally { plain.fill(0) }
    }

    fun save(state: VoiceState) {
        val data = JSONObject().put("voice_grant", state.grant).put("operation", state.operation)
        state.keys.recordIdentity(data)
        state.profile?.let {
            data.put("profile", JSONObject().put("address", it.address).put("user", it.user).put("pin", it.pin).put("endpoint", it.endpoint))
        }
        val plain = data.toString().toByteArray(Charsets.UTF_8)
        try {
            require(plain.size < 8100)
            val cipher = Cipher.getInstance("AES/GCM/NoPadding")
            cipher.init(Cipher.ENCRYPT_MODE, key(create = !file.baseFile.exists()))
            cipher.updateAAD(aad)
            val encrypted = cipher.doFinal(plain)
            require(cipher.iv.size == 12)
            val stream = file.startWrite()
            try {
                stream.write(1)
                stream.write(cipher.iv)
                stream.write(encrypted)
                file.finishWrite(stream)
            } catch (failure: Exception) {
                file.failWrite(stream)
                throw failure
            }
        } finally { plain.fill(0) }
    }

    private fun key(create: Boolean): SecretKey {
        val store = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        (store.getKey(alias, null) as? SecretKey)?.let { return it }
        require(create)
        return KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore").run {
            init(KeyGenParameterSpec.Builder(alias, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
                .setKeySize(256)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .build())
            generateKey()
        }
    }
}
