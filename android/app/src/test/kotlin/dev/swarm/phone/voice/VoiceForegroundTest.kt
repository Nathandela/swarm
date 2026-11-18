package dev.swarm.phone.voice

import org.json.JSONObject
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertThrows
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import java.nio.ByteBuffer
import java.security.KeyFactory
import java.security.KeyPairGenerator
import java.security.Signature
import java.security.spec.X509EncodedKeySpec
import java.util.Base64

@RunWith(RobolectricTestRunner::class)
class VoiceForegroundTest {
    @Test fun pcmIsLittleEndianAndBounded() {
        val pcm = ByteArray(6)
        assertEquals(6, appendVoicePCM(shortArrayOf(0, 0x1234, -32768), 3, pcm, 0))
        assertArrayEquals(byteArrayOf(0, 0, 0x34, 0x12, 0, -128), pcm)
        assertThrows(IllegalArgumentException::class.java) {
            appendVoicePCM(shortArrayOf(1), 1, pcm, pcm.size)
        }
    }

    @Test fun invitationHasExactForwardAndDistinctIdentitiesPersist() {
        val invitation = VoiceInvitation.parse("""{"place_id":"place","reference":"reference","endpoint":"127.0.0.1:32100"}""")
        assertEquals("127.0.0.1:32100", invitation.endpoint)

        val generator = KeyPairGenerator.getInstance("Ed25519")
        val ssh = generator.generateKeyPair()
        val enrollment = generator.generateKeyPair()
        val original = VoiceKeys(ssh, enrollment)
        val recorded = JSONObject().also { original.recordIdentity(it) }
        val restored = VoiceKeys(ssh, enrollment)
        restored.checkIdentity(recorded)
        assertThrows(IllegalStateException::class.java) { VoiceKeys(enrollment, ssh).checkIdentity(recorded) }
        assertEquals(original.sshPublic, restored.sshPublic)
        assertEquals(original.enrollmentPublic, restored.enrollmentPublic)
        assertNotEquals(restored.sshSigner.voiceSSHPublicKey(), restored.enrollmentSigner.voiceEnrollmentPublicKey())
        assertArrayEquals(
            Base64.getDecoder().decode(restored.enrollmentSigner.voiceEnrollmentPublicKey()),
            Base64.getUrlDecoder().decode(restored.enrollmentPublic),
        )

        val wire = ByteBuffer.wrap(Base64.getDecoder().decode(restored.sshPublic.split(' ')[1]))
        val algorithm = ByteArray(wire.int).also { wire.get(it) }
        val key = ByteArray(wire.int).also { wire.get(it) }
        assertEquals("ssh-ed25519", algorithm.toString(Charsets.US_ASCII))
        assertArrayEquals(Base64.getDecoder().decode(restored.sshSigner.voiceSSHPublicKey()), key)
        val prefix = byteArrayOf(0x30, 0x2a, 0x30, 5, 6, 3, 0x2b, 0x65, 0x70, 3, 0x21, 0)
        val public = KeyFactory.getInstance("Ed25519").generatePublic(X509EncodedKeySpec(prefix + key))
        val raw = "synthetic challenge".toByteArray()
        val signed = restored.sshSigner.signVoiceSSH(Base64.getEncoder().encodeToString(raw))
        org.junit.Assert.assertTrue(Signature.getInstance("Ed25519").run {
            initVerify(public)
            update(raw)
            verify(Base64.getDecoder().decode(signed))
        })
    }
}
