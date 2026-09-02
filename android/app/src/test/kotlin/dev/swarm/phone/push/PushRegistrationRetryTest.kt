package dev.swarm.phone.push

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class PushRegistrationRetryTest {
    private data class Scheduled(val delayMillis: Long, val work: () -> Unit)

    @Test
    fun `failed attestation retries with bounded backoff and stops on success`() {
        val scheduled = mutableListOf<Scheduled>()
        val attempts = mutableListOf<String>()
        val answers = ArrayDeque(listOf(false, false, true))
        val retry = PushRegistrationRetry(
            schedule = { delay, work -> scheduled += Scheduled(delay, work) },
        )

        retry.submit("token-a") { token -> attempts += token; answers.removeFirst() }
        assertEquals(0L, scheduled.removeFirst().also { it.work() }.delayMillis)
        assertEquals(5_000L, scheduled.removeFirst().also { it.work() }.delayMillis)
        assertEquals(30_000L, scheduled.removeFirst().also { it.work() }.delayMillis)
        assertEquals(listOf("token-a", "token-a", "token-a"), attempts)
        assertTrue(scheduled.isEmpty())
    }

    @Test
    fun `new token makes every old callback inert and starts immediately`() {
        val scheduled = mutableListOf<Scheduled>()
        val attempts = mutableListOf<String>()
        val retry = PushRegistrationRetry(
            schedule = { delay, work -> scheduled += Scheduled(delay, work) },
        )

        val attempt = { token: String -> attempts += token; false }
        retry.submit("old", attempt)
        scheduled.removeFirst().work() // old immediate fails, queues old retry
        retry.submit("new", attempt) // queues new immediate
        val oldRetry = scheduled.removeFirst()
        val newImmediate = scheduled.removeFirst()
        oldRetry.work()
        newImmediate.work()

        assertEquals(listOf("old", "new"), attempts)
        assertEquals(5_000L, oldRetry.delayMillis)
        assertEquals(0L, newImmediate.delayMillis)
    }
}
