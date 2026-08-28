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
 * FOUR REASONS AND FOUR STATES, because they have four different remedies and only one of
 * them is anything the reader can act on. What shipped before this had a single ABSENT
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
 * THE SECOND HALF IS NOT DECORATION. A control that simply goes quiet reads as a bug, and
 * three of the four reasons have a real remedy nearby -- the machine still has the session,
 * and the owner can type at it. What the copy must never do is imply the phone will get the
 * composer back where it will not.
 */
data class ComposerShut(val placeholder: String, val detail: String)

/**
 * The visible per-send lifecycle (ADR-009 (6): "pending -> sent -> refused ... A send that
 * cannot get [through] is shown refused, not silently swallowed"). STALE_TURN is REFUSED's
 * one refined form: the same terminal state with its own gentle copy, because the
 * conversation moving on is ordinary and the remedy is mild (M2.4).
 */
enum class SendState { PENDING, SENT, REFUSED, STALE_TURN }

/** One refusal notice: the copy the composer shows, and whether the draft survives it. */
data class ComposerNotice(val copy: String, val retainsDraft: Boolean)

/** One accepted paste: the draft it becomes, and whether it submits (it never does). */
data class ComposerPaste(val draft: String, val submits: Boolean)

object ComposerModel {

    /**
     * The availability gate (M2.4, R3's ruling): the lease is out of the UX entirely, and a
     * session with no message sink has no composer rather than a greyed one promising a verb
     * it structurally lacks (ADR-017).
     *
     * THE ORDER IS THE DESIGN. A PERMANENT reason outranks a TRANSIENT one, and the reason is
     * honesty rather than precedence for its own sake: a session that is both offline and
     * torn would, under the other order, be told "not connected" -- which implies the composer
     * comes back when the link does. It never will; the structured degrade is one-way for the
     * life of the session instance. So the permanent fact is the one reported.
     *
     * [ended] outranks everything because there is nothing to type into whatever else is
     * true. [recordTorn] and [structuredChat] are then read together: a session with no
     * structured chat is TORN when this phone holds the daemon's own `structured_gap`
     * element, and NO_CHAT when it does not -- the difference between a record that broke and
     * a machine that never claimed one.
     */
    fun availabilityFor(
        online: Boolean,
        structuredChat: Boolean,
        recordTorn: Boolean = false,
        ended: Boolean = false,
    ): ComposerAvailability = when {
        ended -> ComposerAvailability.ENDED
        recordTorn -> ComposerAvailability.TORN
        !structuredChat -> ComposerAvailability.NO_CHAT
        !online -> ComposerAvailability.OFFLINE
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
            placeholder = "Not connected to your machine",
            // Input is live-only and never queued (ADR-007 B43). A composer that went quiet
            // without saying so invites the reader to believe their words are waiting.
            detail = "Messages are never held — nothing is sent when the link returns.",
        )
        ComposerAvailability.TORN -> ComposerShut(
            placeholder = "This session's record has a gap",
            detail = "Still typeable at your machine.",
        )
        ComposerAvailability.NO_CHAT -> ComposerShut(
            // NOT "broke", NOT "gap", NOT "record": this machine never claimed a chat
            // surface for this session, which is not a failure of anything.
            placeholder = "This agent reports no chat surface",
            detail = "You can watch it here, and type at your machine.",
        )
        ComposerAvailability.ENDED -> ComposerShut(
            placeholder = "This session has ended",
            // NO SECOND SENTENCE, and [ComposerShut]'s own KDoc has always said why: "three
            // of the four reasons have a real remedy nearby". This is the fourth. Offline,
            // torn and no-chat all end in "and you can still type at your machine"; an ended
            // session has nothing on the other side to type AT, so a second line here can
            // only restate the first in more words. The owner-signed drawing tables
            // `composer.ended` as the placeholder alone and draws NO COMPOSER for this state
            // at all -- "Its conversation is kept; there is nothing to type into." was on
            // screen and on no copy sheet.
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
     * the text was sent, and the draft is RETAINED for a re-send against the refreshed turn.
     * Every other refusal keeps the draft too (a refusal that eats the user's words punishes
     * them for the machine's answer), with the generic copy.
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
    fun noticeFor(code: String): ComposerNotice = when (code) {
        // The drawing's `bubble.stale` row, verbatim, and the row's own note is "shipped copy,
        // kept" -- so what shipped had drifted from what the sheet records. The em dash is
        // U+2014 and the apostrophe is straight, as the copy table writes them.
        "STALE_TURN" -> ComposerNotice(
            copy = "Not sent — the conversation moved on. Read the latest turn and send again.",
            retainsDraft = true,
        )
        // The drawing's `bubble.refused` row, verbatim.
        "INPUT_BUSY" -> ComposerNotice(
            copy = "Not sent — the terminal's input line was not empty.",
            retainsDraft = true,
        )
        else -> ComposerNotice(
            copy = "Your message was refused and not delivered.",
            retainsDraft = true,
        )
    }

    /**
     * M2.5's status-driven placeholder: an idle session invites a message, and a WORKING
     * agent still accepts input -- as feedback into the running turn, and the placeholder
     * says so rather than implying the composer is closed.
     */
    fun placeholderFor(working: Boolean): String = if (working) "Add feedback..." else "Message"

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
 * the platform's white so there is something opaque for the tint to replace. Row 23's ring at
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
    imageTintList = ColorStateList.valueOf(Kit.colour(context, R.color.swarm_text_primary))
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
