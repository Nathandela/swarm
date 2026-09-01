package dev.swarm.phone.push

import android.content.Context
import android.os.Looper
import com.google.android.gms.tasks.Task
import com.google.android.gms.tasks.Tasks
import com.google.android.play.core.integrity.IntegrityManagerFactory
import com.google.android.play.core.integrity.StandardIntegrityManager
import java.util.concurrent.TimeUnit
import java.util.Base64
import swarmmobile.PushAttestor

/** The Play Integrity Standard API project linked to dev.swarm.phone in Play Console. */
const val SWARM_PLAY_CLOUD_PROJECT_NUMBER: Long = 733314021126L

internal object PlayIntegrityRequestHash {
    fun encode(hash: ByteArray): String {
        require(hash.size == 32) { "Play Integrity registration requestHash must be 32 bytes" }
        return Base64.getUrlEncoder().withoutPadding().encodeToString(hash)
    }
}

/**
 * Reverse-bound Play Integrity Standard token source. Provider preparation starts
 * asynchronously at construction; the synchronous gomobile call waits only on a background
 * registration worker and is bounded so unavailable Play Services degrades to foreground-only.
 */
class PlayIntegrityAttestor(
    context: Context,
    private val manager: StandardIntegrityManager =
        IntegrityManagerFactory.createStandard(context.applicationContext),
) : PushAttestor {
    @Volatile
    private var preparation: Task<StandardIntegrityManager.StandardIntegrityTokenProvider>? = null

    init {
        prepare()
    }

    override fun attest(requestHash: ByteArray): String {
        check(Looper.myLooper() != Looper.getMainLooper()) {
            "Play Integrity attestation must run off the Android main thread"
        }
        val encodedHash = PlayIntegrityRequestHash.encode(requestHash)
        val prepared = preparationTask()
        val provider = try {
            Tasks.await(prepared, PREPARE_TIMEOUT_SECONDS, TimeUnit.SECONDS)
        } catch (failure: Exception) {
            clearIfCurrent(prepared)
            throw IllegalStateException(
                "Play Integrity token provider is unavailable; push remains foreground-only",
                failure,
            )
        }
        val request = StandardIntegrityManager.StandardIntegrityTokenRequest.builder()
            .setRequestHash(encodedHash)
            .build()
        return try {
            Tasks.await(provider.request(request), TOKEN_TIMEOUT_SECONDS, TimeUnit.SECONDS).token()
        } catch (failure: Exception) {
            // A provider can become stale after preparation. The next Firebase token event
            // gets a fresh preparation rather than retrying a permanently failed instance.
            clearIfCurrent(prepared)
            throw IllegalStateException(
                "Play Integrity token acquisition failed; push remains foreground-only",
                failure,
            )
        }
    }

    private fun prepare() {
        preparationTask()
    }

    private fun preparationTask(): Task<StandardIntegrityManager.StandardIntegrityTokenProvider> =
        synchronized(this) {
            preparation ?: manager.prepareIntegrityToken(
                StandardIntegrityManager.PrepareIntegrityTokenRequest.builder()
                    .setCloudProjectNumber(SWARM_PLAY_CLOUD_PROJECT_NUMBER)
                    .build(),
            ).also { preparation = it }
        }

    private fun clearIfCurrent(expected: Task<StandardIntegrityManager.StandardIntegrityTokenProvider>) {
        synchronized(this) {
            if (preparation === expected) preparation = null
        }
    }

    private companion object {
        // Together these stay inside mobile.pushRegisterTimeout's 30 second bound.
        const val PREPARE_TIMEOUT_SECONDS = 15L
        const val TOKEN_TIMEOUT_SECONDS = 10L
    }
}
