package dev.swarm.phone.ui.kit

import android.content.Context
import android.content.res.ColorStateList
import android.graphics.drawable.LevelListDrawable
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.widget.ImageView
import android.widget.LinearLayout
import androidx.annotation.DrawableRes
import dev.swarm.phone.R

/**
 * derived: docs/design/substrate-components.md #9 Composer
 *
 * Row 9's BAR: `--p-tabbg` under a 1 dp `--p-hair` top rule, `space_8` x `space_14` of padding and
 * a `space_8` gap between the field and the control that sends what is in it.
 *
 * IT IS THE HALF OF ROW 9 NO FACTORY HAD BUILT. `textField` has cited this row since S23 and it is
 * the row's FIELD -- the well, its radius, its `--p-ink2` placeholder. The bar around it was never
 * spent anywhere, so the app's only composer was an `EditText` and a button added to a bare column
 * under the triage inbox, with none of row 9's own surface, rule, padding or gap.
 *
 * **THE FIELD AND THE SEND CONTROL ARE SLOTS**, which is `approvalSheet`'s ruling one component
 * over: `textField` is already every field in this app and `ctaButton` already every action, so a
 * bar that built its own would be a second copy of both. It is also structural rather than tidy --
 * the send control reaches a facade verb, carries PB-SEC-12 clause 1's touch filter and must
 * survive a redraw, and all three of those are `PhoneSurface`'s and cannot be a factory's.
 *
 * **THE BAR TAKES NO HEIGHT OF ITS OWN, AND ROW 9 STATES TWO NUMBERS THAT CANNOT BOTH BE SPENT.**
 * The row gives `composer_height` 52 and, in the same cell, "visual height 36, touch target 48" for
 * the field inside it. 52 measures the mock's 36 dp field between `space_8` above and below; this
 * kit's field is a 48 dp TARGET with the well inset inside it, which is `textField`'s own recorded
 * decision and PB-DS-12's floor -- so pinning 52 here would clip exactly the target that decision
 * bought. The bar wraps its content instead and spends the padding the row names, which is 48 + 16.
 *
 * **`tabbar_height` IS NOT SPENT HERE EITHER**, and for a reason that is about siting rather than
 * size: row 9 measures the composer's BOTTOM up from the tab bar, which is the scaffold's frame and
 * not this component's. The session detail composes the bar as the last thing in its own column,
 * above the bar the scaffold already draws.
 *
 * **THE BACKDROP BLUR IS NOT IMPLEMENTED, AND THIS IS THE SECOND SITE OF THAT OMISSION**
 * (agents-tracker-hxv records it, disposition on agents-tracker-dw8). `RenderEffect` blurs the view
 * it is set on rather than the content behind it, so applying row 9's 16 dp here would blur the
 * field and the send control and leave the transcript behind them sharp -- a visible defect rather
 * than an approximation. `tabBar` is the first site and carries the same paragraph; the 88%
 * translucency ships and does most of what the token was pinned for.
 *
 * @param field row 9's well. It holds what the user typed, so a caller builds it once and hands the
 *  same view to every bar it composes -- see the detach below.
 * @param send what puts those bytes on the wire -- and, since phone refit W3, what stops the
 *  agent: [composerAction], one control whose meaning the screen switches on the live field.
 *  Row 9's voice glyph is still NOT BUILT: no facade verb takes dictation, which is the call
 *  the quick-reply chips row already made ("a control whose behaviour the wire does not
 *  define").
 */
/**
 * Mirror M2.4/M2.5 (Wave R6) -- the composer's MODEL, beside the bar factory below: the
 * availability gate, the visible per-send lifecycle, the gentle stale_turn notice, the
 * status-driven placeholder, and paste discipline. Pure JVM (ComposerSendStateTest drives it);
 * the live bar's rebind onto these states is the wave's disclosed view-side residual
 * (android/unbound-verbs.tsv, App.ComposerSend row).
 */

/**
 * Whether the composer can send right now, and WHY NOT when it cannot.
 *
 * The history-gap state is not a shut reason: it says retained chronology is incomplete while
 * the capability record can still prove that the live message sink exists. What shipped before
 * this had a single ABSENT
 * state drawn on `!structuredChat`, whose sentence accused the session's record of BREAKING
 * -- while the same condition also covered "no record was ever authored", "the record is
 * inconsistent" and "this machine predates R8". Three of those four are not a break.
 *
 * ORDERING IS PART OF THE CONTRACT: see [ComposerModel.availabilityFor].
 */
enum class ComposerAvailability { AVAILABLE, OFFLINE, TORN, NO_CHAT, ENDED }

/**
 * What a shut composer says: the sentence in the field itself, and the line under it that
 * says what is still possible.
 *
 * THE SECOND HALF IS NOT DECORATION. A control that simply goes quiet reads as a bug. Offline and
 * no-chat have distinct next steps; ended deliberately has no second line, and TORN is a transcript
 * warning rather than a shut state. The copy must never imply sending will return where it will not.
 */
data class ComposerShut(val placeholder: String, val detail: String)

/**
 * The visible per-send lifecycle (ADR-009 (6): "pending -> sent -> refused ... A send that
 * cannot get [through] is shown refused, not silently swallowed"). STALE_TURN is REFUSED's
 * one refined form: the same terminal state with its own gentle copy, because the
 * conversation moving on is ordinary and the remedy is mild (M2.4).
 */
enum class SendState { PENDING, SENT, REFUSED, STALE_TURN }

/**
 * One refusal notice: the copy the composer shows, and whether that operation retains its submitted
 * text for retry. The live field may already have been released at local seal for the next send.
 */
data class ComposerNotice(val copy: String, val retainsDraft: Boolean)

/** One accepted paste: the draft it becomes, and whether it submits (it never does). */
data class ComposerPaste(val draft: String, val submits: Boolean)

object ComposerModel {

    /**
     * The availability gate (M2.4, R3's ruling): the lease is out of the UX entirely. Every
     * conversation retains the same composer-shaped shell; this value decides whether the field
     * and action are live and which inline reason a disabled shell shows.
     *
     * THE ORDER IS THE DESIGN. Ended and an authoritative no-chat capability outrank a transient
     * disconnect because reconnecting cannot restore them. Offline outranks TORN because a history
     * warning does not shut a live sink; when both are present, connectivity is the actual blocker.
     *
     * [ended] outranks everything because there is nothing to type into whatever else is true.
     * The capability record then answers whether a live message sink exists. A
     * [recordTorn] gap comes after that gate: it is a history warning, not an inference that the
     * sink disappeared, and therefore remains sendable when the record still says chat exists.
     */
    fun availabilityFor(
        online: Boolean,
        structuredChat: Boolean,
        recordTorn: Boolean = false,
        ended: Boolean = false,
    ): ComposerAvailability = when {
        ended -> ComposerAvailability.ENDED
        !structuredChat -> ComposerAvailability.NO_CHAT
        !online -> ComposerAvailability.OFFLINE
        recordTorn -> ComposerAvailability.TORN
        else -> ComposerAvailability.AVAILABLE
    }

    /**
     * What a shut composer says, or null when it is not shut.
     *
     * EVERY SENTENCE IS ABOUT THIS STATE AND NO OTHER, and none of them offers a step that
     * cannot produce typing -- which is what the deleted "Read-only -- take control to type"
     * did on every route, including the two where take-control was not even drawn.
     */
    fun shutCopyFor(availability: ComposerAvailability): ComposerShut? = when (availability) {
        ComposerAvailability.AVAILABLE -> null
        ComposerAvailability.OFFLINE -> ComposerShut(
            placeholder = "Not connected.",
            // Input is live-only and never queued (ADR-007 B43). A composer that went quiet
            // without saying so invites the reader to believe their words are waiting.
            detail = "Reconnect to send.",
        )
        // TORN names incomplete retained history. The transcript's structured-gap card explains
        // that chronology; it does not shut or relabel a composer whose sink remains available.
        ComposerAvailability.TORN -> null
        ComposerAvailability.NO_CHAT -> ComposerShut(
            // NOT "broke", NOT "gap", NOT "record": this machine never claimed a chat
            // surface for this session, which is not a failure of anything.
            placeholder = "Chat is off for this session.",
            detail = "Reply on your computer.",
        )
        ComposerAvailability.ENDED -> ComposerShut(
            placeholder = "This session has ended",
            // NO SECOND SENTENCE. Offline and no-chat carry their own nearby remedies, while
            // TORN is no longer a shut reason at all: its transcript marker owns the history
            // warning and a proven sink keeps the normal composer live. An ended session has
            // nothing on the other side to type into, so the permanent shell keeps this
            // placeholder and disables its field/action.
            detail = "",
        )
    }

    /**
     * The send item's state label. Each state is tellable apart: "delivery unknown" rendered
     * as "sent" is a lie (ADR-009 (6)).
     *
     * **AND THE SENTENCE ABOVE USED TO SIT DIRECTLY OVER THE LIE IT NAMES.** `SENT` returned
     * the word "Sent". What PRODUCES `SENT` is `composerVerdictFor` on `verdict.accepted` --
     * the daemon's OK for `composer_send`, which means the daemon wrote bytes into a PTY. That
     * is not delivery, and there is no layer beneath this one that knows better: on the
     * keystroke path the CLI acknowledges nothing at all. "Sent" was the strongest claim on
     * the screen standing on the weakest fact on the wire, which is the exact shape of the
     * defect the first paragraph names.
     *
     * **SO THE SETTLED STATE HAS NO LABEL, AND THAT IS THE DRAWING'S OWN COPY**: "No tick, no
     * label. Settling IS the acknowledgement." Owner ruling R6 moves the acknowledgement to
     * the agent's own echo -- the bubble stays [BubbleState.PENDING] under `sending` until the
     * transcript reflects the prompt back, and then it simply settles. The empty string is not
     * a missing label; it is the whole of what this build can honestly say about a message it
     * has stopped worrying about, and the state is still tellable apart from every other one
     * -- by the bubble's SURFACE, which is where derivation row 26 puts the difference.
     *
     * ## A LABEL NAMES A STATE, A NOTICE EXPLAINS ONE, AND NO STATE GETS BOTH
     *
     * (Owner ruling, this wave, on the composer's voice.) This function and [noticeFor] are
     * two renderings of one send, and the panel draws both at once --
     * `SessionDetailPanel.kt` fills `composerStateLabel` from here and `composerNotice` from
     * there. So every state that had words in both said the same thing twice, in two
     * wordings: the label `Not sent - the conversation moved on` sat directly above the notice
     * `Not sent - the conversation moved on. Read the latest turn and send again.`, and after
     * INPUT_BUSY joined REFUSED the label `Not sent` became a verbatim prefix of
     * `Not sent - the terminal's input line was not empty.` The drawing draws ONE line under a
     * refused bubble, and this is the rule that produces one.
     *
     * **PENDING IS THE ONLY STATE THAT KEEPS A LABEL**, because `sending` NAMES the state and
     * explains nothing -- there is no remedy to offer while a message is in flight, and
     * [noticeFor] has no arm for it. It is lower case because the copy sheet's `bubble.pending`
     * row is, and this file's copy is now extracted from that sheet rather than typed.
     *
     * **EVERYTHING WITH A REMEDY SPEAKS THROUGH [noticeFor]**, which is where the sheet's
     * sentences live and where the refusal taxonomy is single. REFUSED and STALE_TURN keep
     * their words in the fullest sense -- a send that cannot get through is still shown
     * refused and never silently swallowed -- they just say them once, in the place that can
     * also say WHY. SENT has neither, because there is nothing to explain about a message the
     * agent has echoed back.
     */
    fun stateLabel(state: SendState): String = when (state) {
        // The copy sheet's `bubble.pending`, lower case, and the one surviving label.
        SendState.PENDING -> "sending"
        // Empty, deliberately, all three. A settled bubble is DRAWN and not narrated; the two
        // refusals are EXPLAINED, once, by `noticeFor`. See the KDoc.
        SendState.SENT -> ""
        SendState.REFUSED -> ""
        SendState.STALE_TURN -> ""
    }

    /**
     * The notice for one refusal code (the wire's own vocabulary: Outcome.Code / the
     * taxonomy's rendered states). stale_turn gets its OWN gentle copy -- it never claims
     * the text was sent, and the operation retains its submitted text for a deliberate retry.
     * Every other refusal retains that text too (a refusal must not erase the only retry source),
     * even though local sealing may already have freed the live field for a newer message.
     *
     * **THE KEY IS THE `ErrorState` ENUM NAME AND NOT A MESSAGE**, which is what makes this the
     * ONE place a refusal acquires its sentence: the chain is `composerVerdictFor` ->
     * `ErrorRouter.routeMachineCode(outcome.code)` -> here. A cause with no arm does not fail
     * loudly -- it falls to `else` and reads as the generic refusal, which is how a machine
     * answer this product went to some trouble to produce can arrive on screen saying nothing
     * particular. That is exactly what happened to INPUT_BUSY.
     *
     * **INPUT_BUSY IS SLICE 0'S WHOLE POINT REACHING THE READER.** The shim refuses rather than
     * joining the reader's words to somebody's half-typed line, having written nothing -- and
     * a person who is told only "your message was refused" cannot tell that from a dropped
     * link, an expired session or a bug. The sentence names the one fact the shim actually
     * knows: nobody has written to this PTY since the last submit, so the line was not empty.
     *
     * **AND THERE IS NO `SendState` FOR IT** (owner ruling, this wave). REFUSED plus the copy
     * carries the reason, which is what derivation row 26 and the drawing specify:
     * `bubble.refused` is ONE state whose sentence varies, not a state per cause. A SendState
     * per machine code would put the refusal taxonomy in two places -- this `when` and that
     * enum -- and guarantee the two drift.
     */
    fun noticeFor(code: String, machine: String = ""): ComposerNotice = when (code) {
        // The drawing's `bubble.stale` row, verbatim, and the row's own note is "shipped copy,
        // kept" -- so what shipped had drifted from what the sheet records. The em dash is
        // U+2014 and the apostrophe is straight, as the copy table writes them.
        "STALE_TURN" -> ComposerNotice(
            copy = "Not sent. There's a new reply. Read it, then send again.",
            retainsDraft = true,
        )
        // The drawing's `bubble.refused` row, verbatim, where the screen cannot name the
        // computer -- one contiguous literal so the copy checker binds it -- and the same
        // sentence over the name where it can (phone refit W5.2).
        "INPUT_BUSY" -> ComposerNotice(
            copy = if (machine.isEmpty()) {
                "Not sent. Finish typing on your computer first."
            } else {
                "Not sent. Finish typing on $machine first."
            },
            retainsDraft = true,
        )
        else -> ComposerNotice(
            copy = "Not sent. Try again.",
            retainsDraft = true,
        )
    }

    /**
     * M2.5's status-driven placeholder: an idle session invites a message, and a WORKING
     * agent still accepts input -- as feedback into the running turn, and the placeholder
     * says so rather than implying the composer is closed.
     */
    fun placeholderFor(working: Boolean): String = if (working) "Add a note while it works" else "Message"

    /**
     * M2.4's paste discipline: a multi-line paste is ONE draft and never auto-submits -- a
     * newline in pasted text is content, not a submit (the r3p submit-boundary rule,
     * phone-side; PB-INPUT-6's atomic paste one plane over).
     */
    fun acceptPaste(text: String): ComposerPaste = ComposerPaste(draft = text, submits = false)
}

fun composerBar(context: Context, field: View, send: View): LinearLayout = KitStack(
    context,
    LinearLayout.HORIZONTAL,
    Kit.dimenPx(context, R.dimen.swarm_space_8),
).apply {
    layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    background = TopRule(
        fill = Kit.colour(context, R.color.swarm_tabbar_background),
        rule = Kit.colour(context, R.color.swarm_hairline),
        // `dpPx` and not `dp`, which is `tabBar`'s own line and the same third of a pixel: one
        // design value rendered two ways on one screen antialiases the hairline into a smear.
        rulePx = Kit.dpPx(context, KitMetrics.HAIRLINE_DP).toFloat(),
    )
    val vertical = Kit.dimenPx(context, R.dimen.swarm_space_8)
    val horizontal = Kit.dimenPx(context, R.dimen.swarm_space_14)
    setPaddingRelative(horizontal, vertical, horizontal, vertical)
    // The field is taller than the label beside it, and a send control anchored to the top of the
    // bar would sit above the line the user is typing on.
    gravity = Gravity.CENTER_VERTICAL
    // THE DETACH IS NOT TIDINESS. The bar is rebuilt whenever the screen holding it is, and the
    // field is built once and re-parented because it holds what the user typed; a child arriving
    // at its next addView still claiming a discarded parent is refused by Android with "the
    // specified child already has a parent". `sessionDetailView.tagged` carries the same four lines.
    (field.parent as? ViewGroup)?.removeView(field)
    (send.parent as? ViewGroup)?.removeView(send)
    // The field takes the bar's spare room and the control keeps its own, so a one-word label and a
    // four-word one leave the same field.
    addView(field, LinearLayout.LayoutParams(0, WRAP, 1f))
    addView(send, LinearLayout.LayoutParams(WRAP, WRAP))
}

/**
 * The composer's ONE control, and the whole of phone refit W3 (docs/specifications/phone-refit-playbook.md
 * §4, owner ruling; row 9 records it as `action-box 40`): the square that sends what is in the
 * field, or stops the agent while it works and the field is empty.
 *
 * **IT DRAWS THE GLYPHS AND CHOOSES NEITHER.** Which of the two shows, and what a screen reader
 * hears, is a fact about the session and the live field -- state and copy, both the screen's
 * (PB-DS-9) -- so the view carries both drawables as one level list and the screen selects by
 * [ComposerActionGlyph]. It sets no content description, [overflowControl]'s ruling: the words
 * are copy, and the screen owes them on every draw and on every keystroke.
 *
 * **THE SQUARE IS INSET INSIDE THE TARGET**, which is [textField]'s own arrangement one slot over:
 * row 9's 48 dp floor is the control's minimum height and the 40 dp box sits centred in it, so
 * the drawing is the design's and the room for a finger is PB-DS-12's. The bar hands every slot
 * `WRAP` x `WRAP` ([composerBar]), so both numbers are MINIMUMS here rather than layout params.
 *
 * **ONE INK FOR BOTH GLYPHS, DELIBERATELY.** Row 9's `--p-err` names the mock's separate stop
 * glyph beside a voice glyph; this is one control whose MEANING changes under the finger, and a
 * red square standing where the arrow just stood would be `.a2-no`'s claim on the thing that
 * also sends. The glyph is `--p-ink`, the back chevron's own arrangement, and the drawables ship
 * the platform's white so there is something opaque for the tint to replace. Disabled -- offline,
 * or while a send crosses, both of which the surface says through `isEnabled` -- is `--p-ink3`,
 * the ink every dead control shares (row 24's pair, `CtaButton`'s arrangement); a single-colour
 * tint drew a dead square at full strength (W3 review round, 2026-08-28). Row 23's ring at
 * radius 0, [overflowControl]'s reason: this paints no surface of its own.
 */
fun composerAction(context: Context): ImageView = ImageView(context).apply {
    val box = Kit.dpPx(context, KitMetrics.COMPOSER_ACTION_DP)
    val room = (Kit.dpPx(context, KitMetrics.MIN_TARGET_DP) - box) / 2
    setImageDrawable(
        LevelListDrawable().apply {
            for (glyph in ComposerActionGlyph.entries) {
                addLevel(glyph.ordinal, glyph.ordinal, context.getDrawable(glyph.drawable))
            }
        },
    )
    // THE LIVE INK AND THE DEAD ONE (W3 review round): the surface holds this control disabled
    // offline and while a send crosses, and says so through the drawable state alone --
    // CtaButton's arrangement, row 24's pair. Row 9 records it as `disabled: ink3`.
    imageTintList = ColorStateList(
        arrayOf(intArrayOf(-android.R.attr.state_enabled), intArrayOf()),
        intArrayOf(
            Kit.colour(context, R.color.swarm_text_tertiary),
            Kit.colour(context, R.color.swarm_text_primary),
        ),
    )
    scaleType = ImageView.ScaleType.CENTER
    minimumWidth = box
    minimumHeight = Kit.dpPx(context, KitMetrics.MIN_TARGET_DP)
    // Only the vertical room moves: the box is the control's whole width.
    setPadding(paddingLeft, room, paddingRight, room)
    Kit.focusable(this, componentRadiusPx = 0f)
    layoutParams = LinearLayout.LayoutParams(WRAP, WRAP)
}

/**
 * The two things [composerAction] can show. The screen selects (`ImageView.setImageLevel` with
 * the ordinal); the kit draws. An enum and not two resource ids at the call site, so that no
 * screen ever names a drawable.
 */
enum class ComposerActionGlyph(@DrawableRes internal val drawable: Int) {
    SEND(R.drawable.swarm_send),
    STOP(R.drawable.swarm_stop),
}
