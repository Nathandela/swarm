package dev.swarm.phone.push

import java.math.BigInteger
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Test

class AndroidInstallationSignerTest {
    @Test
    fun `DER high S is normalized into fixed width P1363`() {
        val n = BigInteger("FFFFFFFF00000000FFFFFFFFFFFFFFFFBCE6FAADA7179E84F3B9CAC2FC632551", 16)
        val out = P256Signatures.derToLowSP1363(der(BigInteger.ONE, n.subtract(BigInteger.ONE)))
        assertEquals64(out)
        assertArrayEquals(fixed(BigInteger.ONE), out.copyOfRange(0, 32))
        assertArrayEquals(fixed(BigInteger.ONE), out.copyOfRange(32, 64))
    }

    @Test
    fun `DER positive padding is removed without losing width`() {
        val highBit = BigInteger.ONE.shiftLeft(255)
        val out = P256Signatures.derToLowSP1363(der(highBit, BigInteger.TWO))
        assertEquals64(out)
        assertArrayEquals(fixed(highBit), out.copyOfRange(0, 32))
        assertArrayEquals(fixed(BigInteger.TWO), out.copyOfRange(32, 64))
    }

    @Test
    fun `malformed and non canonical DER is refused`() {
        expectIllegalArgument {
            P256Signatures.derToLowSP1363(byteArrayOf(0x30, 0x03, 0x02, 0x01, 0x01))
        }
        expectIllegalArgument {
            P256Signatures.derToLowSP1363(byteArrayOf(0x30, 0x07, 0x02, 0x02, 0, 1, 0x02, 0x01, 1))
        }
    }

    private fun assertEquals64(value: ByteArray) =
        assertEquals(64, value.size)

    private fun fixed(value: BigInteger): ByteArray {
        val raw = value.toByteArray().dropWhile { it == 0.toByte() }.toByteArray()
        return ByteArray(32 - raw.size) + raw
    }

    private fun der(r: BigInteger, s: BigInteger): ByteArray {
        val ri = r.toByteArray()
        val si = s.toByteArray()
        return byteArrayOf(0x30, (4 + ri.size + si.size).toByte(), 0x02, ri.size.toByte()) +
            ri + byteArrayOf(0x02, si.size.toByte()) + si
    }

	private fun expectIllegalArgument(block: () -> Unit) {
		try {
			block()
			throw AssertionError("expected IllegalArgumentException")
		} catch (_: IllegalArgumentException) {
		}
	}
}
