package dev.swarm.phone.ui.kit

import android.content.Context
import android.os.VibrationEffect
import android.os.Vibrator
import android.os.VibratorManager
import android.provider.Settings
import androidx.test.core.app.ApplicationProvider
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf
import kotlin.math.roundToInt

/**
 * FAILING-FIRST (TDD RED, GG-5) for the obsidian migration plan's phase O6.2 -- the haptics
 * vocabulary.
 *
 * WHAT THIS SUITE IS FOR, AND WHAT `android/gate/o6_haptics_test.go` IS FOR, because neither
 * subsumes the other. The gate reads SOURCE: it can say that nothing outside `Haptics.kt` names
 * the vibration hardware, that the six signals are the six the plan commissioned, and that the
 * user's setting is named at all. It cannot say what a signal FEELS like, and it cannot say that a
 * phone whose owner turned haptics off stays still. That is this file: the rhythm table, which is
 * where every design decision in this component lives, and the two suppression paths, which are
 * the only part of the component a user can be harmed by getting wrong.
 *
 * THE RHYTHM TABLE IS THE COMPONENT. A vibration leaves no visual trace -- no screenshot, no view
 * hierarchy, nothing a device pass can photograph -- so the only durable statement about what the
 * six signals feel like is the table itself, asserted as data. Three properties make it a
 * VOCABULARY rather than six buzzes: it covers every signal, no two entries are the same rhythm
 * (a person who cannot tell two signals apart has five), and the two the plan calls double are
 * two beats while the rest are one.
 *
 * NOTHING HERE ASSERTS A `VibrationEffect`'s CONTENTS, and the reason is the platform's rather
 * than this suite's: `VibrationEffect` exposes no getters at all, so an assertion over one could
 * only compare it against another built the same way -- the implementation checked against itself.
 * What is asserted instead is the table the effect is BUILT FROM, plus the fact that a vibration
 * reached the vibrator at all.
 */
@RunWith(RobolectricTestRunner::class)
class HapticsTest {

    private val context: Context
        get() = ApplicationProvider.getApplicationContext()

    private val vibrator: Vibrator
        get() = context.getSystemService(VibratorManager::class.java).defaultVibrator

    private fun hapticsEnabled(on: Boolean) {
        Settings.System.putInt(
            context.contentResolver,
            Settings.System.HAPTIC_FEEDBACK_ENABLED,
            if (on) 1 else 0,
        )
    }

    // ---- the vocabulary ---------------------------------------------------

    @Test
    fun `the vocabulary is six signals and every one has a rhythm`() {
        assertEquals(
            "the plan commissions six signals; a seventh is a decision somebody has to make in " +
                "the open, and a person can only learn so many by feel",
            6,
            Haptics.Signal.entries.size,
        )
        Haptics.Signal.entries.forEach { signal ->
            assertTrue(
                "$signal has no rhythm. A signal with no entry in the table is a call site that " +
                    "means something and does nothing.",
                Haptics.RHYTHMS.containsKey(signal),
            )
        }
    }

    @Test
    fun `no two signals share a rhythm`() {
        val pairs = Haptics.Signal.entries.flatMap { a ->
            Haptics.Signal.entries.filter { it != a }.map { b -> a to b }
        }
        pairs.forEach { (a, b) ->
            val left = Haptics.RHYTHMS.getValue(a)
            val right = Haptics.RHYTHMS.getValue(b)
            assertNotEquals(
                "$a and $b are the same rhythm, so a person feeling one cannot tell which " +
                    "happened. Six signals that resolve to five meanings is a vocabulary with a " +
                    "homonym in it.",
                left.timings.toList() to left.amplitudes.toList(),
                right.timings.toList() to right.amplitudes.toList(),
            )
        }
    }

    @Test
    fun `the two the plan calls double are two beats and the rest are one`() {
        val doubled = setOf(Haptics.Signal.NEEDS_YOU, Haptics.Signal.FAILED)
        Haptics.Signal.entries.forEach { signal ->
            val beats = Haptics.RHYTHMS.getValue(signal).beats.size
            val want = if (signal in doubled) 2 else 1
            assertEquals(
                "$signal is $beats beat(s). The plan writes the rhythm into the name -- " +
                    "`needs-you two-pulse` and `failed double low` are the two that repeat, and " +
                    "the repetition is what separates them from the four single strikes.",
                want,
                beats,
            )
        }
    }

    @Test
    fun `a rhythm renders as a waveform whose amplitudes follow its own scales`() {
        Haptics.Signal.entries.forEach { signal ->
            val rhythm = Haptics.RHYTHMS.getValue(signal)
            // [delay, duration] per beat: the pattern a waveform plays, silence first.
            val wantTimings = rhythm.beats.flatMap { listOf(it.delayMs, it.durationMs) }
            val wantAmplitudes = rhythm.beats.flatMap { listOf(0, (it.scale * 255f).roundToInt()) }
            assertEquals(
                "$signal's waveform must be the same rhythm its primitives are, beat for beat: " +
                    "a device with no primitive support has to feel the SAME signal, not a " +
                    "different one that happens to be available.",
                wantTimings,
                rhythm.timings.toList(),
            )
            assertEquals(
                "$signal's waveform amplitudes must be its own scales. A second set of numbers " +
                    "here is a second design decision, drifting from the first on the next edit.",
                wantAmplitudes,
                rhythm.amplitudes.toList(),
            )
        }
    }

    // ---- what reaches the hardware ---------------------------------------

    @Test
    fun `a signal reaches the vibrator`() {
        hapticsEnabled(true)
        shadowOf(vibrator).setHasVibrator(true)
        assertTrue(
            "Haptics.play reported that nothing was sent on a device that vibrates, with the " +
                "user's setting on. Every call site below this is then silent for a reason " +
                "nobody can see.",
            Haptics.play(context, Haptics.Signal.SENT),
        )
        assertTrue("the vibrator was never asked to vibrate", shadowOf(vibrator).isVibrating)
    }

    @Test
    fun `nothing vibrates when the user has turned haptics off`() {
        hapticsEnabled(false)
        shadowOf(vibrator).setHasVibrator(true)
        assertFalse(
            "Haptics.play vibrated a phone whose owner turned haptic feedback off. The plan's " +
                "own clause is that the system haptic-disable is honoured; a product that " +
                "overrides it is not a more expressive one, it is a ruder one.",
            Haptics.play(context, Haptics.Signal.NEEDS_YOU),
        )
        assertFalse("the vibrator was asked anyway", shadowOf(vibrator).isVibrating)
    }

    @Test
    fun `nothing is asked of a device with no vibrator`() {
        hapticsEnabled(true)
        shadowOf(vibrator).setHasVibrator(false)
        assertFalse(
            "Haptics.play claimed to have vibrated hardware that does not exist.",
            Haptics.play(context, Haptics.Signal.SHEET_SETTLE),
        )
    }

    /**
     * THE TWO EFFECTS ARE COMPARED AGAINST ONES BUILT HERE FROM [Haptics.RHYTHMS], not against the
     * implementation's own output and not against a Robolectric recording. That is what makes this
     * an assertion about the DESIGN reaching the hardware: a dropped delay, a scale applied to the
     * wrong beat or a fallback built from different numbers all fail it, and none of them would
     * fail a count of how many primitives the shadow saw.
     */
    @Test
    fun `the composition is used where the primitives exist and the waveform where they do not`() {
        shadowOf(vibrator).setHasVibrator(true)
        val rhythm = Haptics.RHYTHMS.getValue(Haptics.Signal.COMPLETED)
        val ids = rhythm.beats.map { it.primitive }

        shadowOf(vibrator).setSupportedPrimitives(ids)
        ids.forEach { shadowOf(vibrator).setPrimitiveDurations(it, SUPPORTED_PRIMITIVE_MS) }
        val tuned = Haptics.effectFor(vibrator, rhythm)

        var wanted = VibrationEffect.startComposition()
        rhythm.beats.forEach { beat ->
            wanted = wanted.addPrimitive(beat.primitive, beat.scale, beat.delayMs.toInt())
        }
        assertEquals(
            "a device that supports the primitives must feel the tuned composition, beat for " +
                "beat: the primitives are the hardware's own strike shapes and the waveform is " +
                "this file approximating one.",
            wanted.compose(),
            tuned,
        )

        shadowOf(vibrator).setSupportedPrimitives(emptyList())
        ids.forEach { shadowOf(vibrator).setPrimitiveDurations(it, 0) }
        val fallback = Haptics.effectFor(vibrator, rhythm)
        assertEquals(
            "a device without primitive support must still feel the SAME signal, as the waveform " +
                "the same rhythm renders to.",
            VibrationEffect.createWaveform(rhythm.timings, rhythm.amplitudes, PLAY_ONCE),
            fallback,
        )
        assertNotEquals(
            "the two branches produced the same effect, so the primitive-support check decides " +
                "nothing and one of the two paths has never been played by anybody.",
            tuned,
            fallback,
        )
    }

    @Test
    fun `every signal builds an effect`() {
        hapticsEnabled(true)
        shadowOf(vibrator).setHasVibrator(true)
        Haptics.Signal.entries.forEach { signal ->
            val effect: VibrationEffect = Haptics.effectFor(vibrator, Haptics.RHYTHMS.getValue(signal))
            assertTrue(
                "$signal built no effect, so the call site that means it does nothing",
                effect.toString().isNotEmpty(),
            )
        }
    }

    private companion object {

        /** Any non-zero duration: the shadow reads support off the per-primitive duration table. */
        const val SUPPORTED_PRIMITIVE_MS = 25

        /** `createWaveform`'s "do not repeat" sentinel. Nothing in this vocabulary loops. */
        const val PLAY_ONCE = -1
    }
}
