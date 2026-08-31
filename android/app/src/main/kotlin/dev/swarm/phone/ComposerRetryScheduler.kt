package dev.swarm.phone

import android.os.Handler
import android.os.Looper

/** Testable delay seam for composer retries; production posts on the main looper. */
internal fun interface ComposerRetryDelay {
    fun post(delayMillis: Long, action: () -> Unit)
}

/**
 * Bounded retry cadence for the one terminal composer outcome that proves no bytes were written.
 * Admission happens only after the delay; a refused admission is surfaced to the ledger so the
 * durable outcome can be considered again instead of leaving a logical bubble stuck forever.
 */
class ComposerRetryScheduler internal constructor(private val delay: ComposerRetryDelay) {
    /** Main-thread generation: cancellation invalidates callbacks already handed to the clock. */
    private var generation: Long = 0

    fun schedule(
        attempt: Int,
        submit: () -> Boolean,
        rejected: () -> Unit,
    ) {
        val scheduledGeneration = generation
        delay.post(delayFor(attempt)) scheduled@{
            if (scheduledGeneration != generation) return@scheduled
            if (!submit()) rejected()
        }
    }

    /** Invalidate every callback scheduled by the surface generation being released. */
    fun cancel() {
        generation += 1
    }

    internal fun delayFor(attempt: Int): Long {
        val exponent = (attempt - 1).coerceIn(0, MAX_EXPONENT)
        return (FIRST_DELAY_MS shl exponent).coerceAtMost(MAX_DELAY_MS)
    }

    companion object {
        private const val FIRST_DELAY_MS = 1_000L
        private const val MAX_DELAY_MS = 30_000L
        private const val MAX_EXPONENT = 5

        fun main(): ComposerRetryScheduler {
            val handler = Handler(Looper.getMainLooper())
            return ComposerRetryScheduler(
                ComposerRetryDelay { delayMillis, action ->
                    handler.postDelayed(Runnable(action), delayMillis)
                },
            )
        }
    }
}
