package dev.swarm.phone.push

import org.junit.Assert.assertEquals
import org.junit.Test

class PlayIntegrityAttestorTest {
    @Test
    fun `request hash is 32 byte base64url without padding`() {
        val hash = ByteArray(32) { it.toByte() }
        assertEquals(
            "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8",
            PlayIntegrityRequestHash.encode(hash),
        )
    }

    @Test
    fun `wrong size request hash is refused before Play Services`() {
        expectIllegalArgument {
            PlayIntegrityRequestHash.encode(ByteArray(31))
        }
    }

	private fun expectIllegalArgument(block: () -> Unit) {
		try {
			block()
			throw AssertionError("expected IllegalArgumentException")
		} catch (_: IllegalArgumentException) {
		}
	}
}
