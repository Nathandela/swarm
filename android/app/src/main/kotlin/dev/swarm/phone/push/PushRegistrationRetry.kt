package dev.swarm.phone.push

import java.util.concurrent.atomic.AtomicLong

/** One bounded retry generation per latest FCM token; older scheduled callbacks are inert. */
internal class PushRegistrationRetry(
    private val schedule: (delayMillis: Long, work: () -> Unit) -> Unit,
    private val backoffMillis: List<Long> = listOf(0L, 5_000L, 30_000L, 120_000L),
) {
    private val generation = AtomicLong()

    fun submit(token: String, attempt: (token: String) -> Boolean) {
        val current = generation.incrementAndGet()
        scheduleAttempt(current, token, 0, attempt)
    }

    private fun scheduleAttempt(
        current: Long,
        token: String,
        index: Int,
        attempt: (token: String) -> Boolean,
    ) {
        schedule(backoffMillis[index]) {
            if (generation.get() != current) return@schedule
            val accepted = try {
                attempt(token)
            } catch (_: Exception) {
                false
            }
            if (!accepted && generation.get() == current && index + 1 < backoffMillis.size) {
                scheduleAttempt(current, token, index + 1, attempt)
            }
        }
    }
}
