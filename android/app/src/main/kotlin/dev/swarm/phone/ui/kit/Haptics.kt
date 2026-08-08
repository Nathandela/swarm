package dev.swarm.phone.ui.kit

import android.content.Context
import android.os.VibrationAttributes
import android.os.VibrationEffect
import android.os.Vibrator
import android.os.VibratorManager
import android.provider.Settings
import kotlin.math.roundToInt

/**
 * The obsidian migration plan's phase O6.2: the haptics vocabulary, and the app's ONLY vibration.
 *
 * SIX SIGNALS, TOLD APART BY RHYTHM. The plan writes the rhythm into each name -- "needs-you
 * two-pulse, sent single sharp, completed soft thud, failed double low, sheet-settle thud, scroll
 * ratchet tick" -- and that is the whole design: a person cannot learn six intensities of the same
 * buzz, but they can learn a double from a single, and a thud from a tick. [RHYTHMS] is where that
 * lives, as data, because a vibration leaves no visual trace at all -- not in a screenshot, not in
 * a view hierarchy, not in anything a device pass can photograph -- so the table IS the component
 * and everything else here is plumbing around it.
 *
 * **ALL VIBRATION IN THIS APP HAPPENS HERE, AND `android/gate/o6_haptics_test.go` IS WHY IT STAYS
 * THAT WAY.** That fence refuses the vibration hardware's name -- `Vibrator`, `VibratorManager`,
 * `VibrationEffect`, `VibrationAttributes`, `vibrate` -- in every production Kotlin file but this
 * one, and refuses the platform's other buzz path (`View.performHapticFeedback`) everywhere. The
 * argument is D8.2's, for the sweep, and it is stronger here: a stray sweep is at least visible to
 * anyone who looks at the screen, whereas a stray `vibrate(50)` is a seventh signal nobody wrote
 * down, felt by a user who has no way to report what they felt. A control asks for a signal as
 * `Haptics.play(context, Haptics.Signal.SENT)` and has no other vocabulary available to it.
 *
 * **FIRED LOCALLY ON TAP, NEVER ON A SERVER ACK** -- the plan's own clause, and the reason it is a
 * clause: a confirmation that waits for the machine's answer arrives between 40 ms and five
 * seconds after the finger left the glass ([dev.swarm.phone.VerbDispatch] has the numbers), which
 * is not feedback, it is a second event. What the hand is told is what the PHONE just did.
 *
 * **THE COMPOSITION IS PREFERRED AND THE WAVEFORM IS THE FALLBACK, and the split is the DEVICE's
 * rather than the API level's.** `minSdk` is 33, so `VibrationEffect.startComposition` and
 * `VibratorManager` are available on every supported handset and there is no version branch to
 * write; what actually varies is whether a given actuator implements the primitives, which
 * [Vibrator.areAllPrimitivesSupported] answers per device. A primitive is the hardware's own tuned
 * strike shape; the waveform is this file approximating one, so it is what a device without them
 * gets rather than what everybody gets. [Rhythm] renders BOTH from the same beats, which is what
 * makes the fallback the same signal rather than a different one that happened to be available.
 *
 * **THE USER'S SETTING WINS, TWICE OVER.** `HAPTIC_FEEDBACK_ENABLED` is read here, because it is
 * the switch a person actually touches and this app can honour it for itself; and every vibration
 * is attributed `USAGE_TOUCH`, because an UNCLASSIFIED vibration is not what the platform's own
 * touch-feedback suppression governs. Either alone is a half-measure -- the first is one OS
 * release from being wrong, the second is a suppression this app never verified -- so both are
 * spent. `HapticsTest` turns the setting off and asserts silence.
 */
object Haptics {

    /**
     * The vocabulary. Six meanings, one name each, spent at every call site that means it.
     *
     * The names are joined to the plan's own phrases by `o6_haptics_test.go`, so a seventh signal
     * is a decision somebody has to make in the open and a rename cannot drift away from the
     * document that commissioned it.
     */
    enum class Signal {
        NEEDS_YOU,
        SENT,
        COMPLETED,
        FAILED,
        SHEET_SETTLE,
        SCROLL_TICK,
    }

    /**
     * One strike: which primitive the hardware plays, how hard, how long after the previous one,
     * and how long it lasts if the waveform has to stand in for it.
     *
     * SCALE IS THE ONE INTENSITY NUMBER. The composition takes it directly (0..1) and the waveform
     * derives its 0..255 amplitude from it, so a signal that is softened is softened once. Two
     * intensity numbers per beat would be two design decisions that drift apart on the first edit.
     */
    data class Beat(
        val primitive: Int,
        val scale: Float,
        val delayMs: Long,
        val durationMs: Long,
    )

    /**
     * One signal's rhythm, in the two forms the platform can play it.
     *
     * The waveform is DERIVED and not declared: `[delay, duration]` per beat with a zero amplitude
     * across each delay. That is what makes "the fallback is the same signal" a property of the
     * code rather than a claim in a comment.
     */
    data class Rhythm(val beats: List<Beat>) {

        val timings: LongArray
            get() = beats.flatMap { listOf(it.delayMs, it.durationMs) }.toLongArray()

        val amplitudes: IntArray
            get() = beats.flatMap { listOf(0, (it.scale * FULL_AMPLITUDE).roundToInt()) }
                .toIntArray()
    }

    /**
     * The table, and the whole of this component's design.
     *
     * WHY EACH ONE IS SHAPED AS IT IS, since none of it is visible to review any other way:
     *
     *  - **NEEDS_YOU** is the only signal that interrupts rather than confirms, so it is the only
     *    one that repeats at full strength: two clicks 90 ms apart, which is far enough to read as
     *    two and close enough to read as one gesture.
     *  - **SENT** is the shortest thing here. It answers a finger that is still on the glass, so
     *    it is a single 18 ms click at 0.7 -- present, and gone before the hand moves.
     *  - **COMPLETED** is a soft thud: a low, longer strike at half scale. Something finished and
     *    nothing is being asked, which is the opposite end of the register from NEEDS_YOU.
     *  - **FAILED** is two LOW ticks, slower and further apart than NEEDS_YOU's two clicks (140 ms
     *    against 90). Two signals repeating is the point of the pair -- what separates them is the
     *    TEMPO and the register, which is exactly what a hand can tell apart and an intensity is
     *    not.
     *  - **SHEET_SETTLE** is one thud at 0.8, the weight of the app's heaviest surface arriving.
     *    ADR-009 D4.4 calls the approval sheet "the heaviest material in the app"; this is that
     *    sentence in the hand.
     *  - **SCROLL_TICK** is the faintest by an order of feel: a 10 ms tick at 0.3. It fires per
     *    detent, so anything stronger becomes noise within one flick.
     */
    val RHYTHMS: Map<Signal, Rhythm> = mapOf(
        Signal.NEEDS_YOU to Rhythm(
            listOf(
                Beat(VibrationEffect.Composition.PRIMITIVE_CLICK, 1.0f, 0L, 30L),
                Beat(VibrationEffect.Composition.PRIMITIVE_CLICK, 1.0f, 90L, 30L),
            ),
        ),
        Signal.SENT to Rhythm(
            listOf(Beat(VibrationEffect.Composition.PRIMITIVE_CLICK, 0.7f, 0L, 18L)),
        ),
        Signal.COMPLETED to Rhythm(
            listOf(Beat(VibrationEffect.Composition.PRIMITIVE_THUD, 0.5f, 0L, 90L)),
        ),
        Signal.FAILED to Rhythm(
            listOf(
                Beat(VibrationEffect.Composition.PRIMITIVE_LOW_TICK, 1.0f, 0L, 70L),
                Beat(VibrationEffect.Composition.PRIMITIVE_LOW_TICK, 1.0f, 140L, 70L),
            ),
        ),
        Signal.SHEET_SETTLE to Rhythm(
            listOf(Beat(VibrationEffect.Composition.PRIMITIVE_THUD, 0.8f, 0L, 60L)),
        ),
        Signal.SCROLL_TICK to Rhythm(
            listOf(Beat(VibrationEffect.Composition.PRIMITIVE_TICK, 0.3f, 0L, 10L)),
        ),
    )

    /**
     * Play one signal, and report whether anything was actually asked of the hardware.
     *
     * THE RETURN VALUE IS FOR THE TESTS AND FOR NOTHING ELSE, and it is a boolean rather than a
     * thrown refusal because every one of the three false branches is a NORMAL device state: a
     * tablet with no actuator, a user who turned haptics off, a build with no vibrator service.
     * No call site is expected to look at it -- a signal that could not be delivered changes
     * nothing about what the screen does next.
     */
    fun play(context: Context, signal: Signal): Boolean {
        if (!enabled(context)) return false
        val vibrator = vibratorOf(context) ?: return false
        if (!vibrator.hasVibrator()) return false
        vibrator.vibrate(effectFor(vibrator, RHYTHMS.getValue(signal)), TOUCH)
        return true
    }

    /**
     * The effect one rhythm plays as on THIS vibrator.
     *
     * `internal` rather than private so `HapticsTest` can build every signal's effect and see that
     * none of them throws -- a `VibrationEffect` reports nothing about itself, so "it was built at
     * all" is the whole of what can be checked, and it is worth checking: an amplitude outside
     * 0..255 or a timings array a different length from its amplitudes throws at construction, on
     * the device, in front of the user.
     */
    internal fun effectFor(vibrator: Vibrator, rhythm: Rhythm): VibrationEffect {
        val primitives = rhythm.beats.map { it.primitive }.toIntArray()
        if (!vibrator.areAllPrimitivesSupported(*primitives)) {
            return VibrationEffect.createWaveform(rhythm.timings, rhythm.amplitudes, NO_REPEAT)
        }
        var composition = VibrationEffect.startComposition()
        rhythm.beats.forEach { beat ->
            composition = composition.addPrimitive(beat.primitive, beat.scale, beat.delayMs.toInt())
        }
        return composition.compose()
    }

    /**
     * The user's own switch, read for ourselves.
     *
     * The default is ON, which is the platform's: a device whose settings table has never been
     * written should feel what every other device feels, not nothing.
     */
    private fun enabled(context: Context): Boolean =
        Settings.System.getInt(
            context.contentResolver,
            Settings.System.HAPTIC_FEEDBACK_ENABLED,
            HAPTICS_ON,
        ) != HAPTICS_OFF

    /**
     * NULLABLE, because `getSystemService` is. A Context that cannot answer for the vibrator is
     * not an error condition worth throwing over -- it is a signal that does not get played.
     */
    private fun vibratorOf(context: Context): Vibrator? =
        context.getSystemService(VibratorManager::class.java)?.defaultVibrator

    /**
     * Every vibration this app raises is a response to a touch, and says so.
     *
     * `USAGE_TOUCH` is what makes the platform's own suppression apply. An unclassified vibration
     * -- which is what `vibrate(effect)` with no attributes produces -- is not governed by the
     * touch-feedback switch at all, so the setting a user turned off would go on being honoured
     * only by the check this file makes for itself.
     */
    private val TOUCH: VibrationAttributes =
        VibrationAttributes.createForUsage(VibrationAttributes.USAGE_TOUCH)

    /** `VibrationEffect`'s amplitude ceiling: the scale of 1.0 in the units a waveform takes. */
    private const val FULL_AMPLITUDE = 255f

    /** `createWaveform`'s "play once" sentinel. Nothing in this vocabulary loops. */
    private const val NO_REPEAT = -1

    private const val HAPTICS_ON = 1

    private const val HAPTICS_OFF = 0
}
