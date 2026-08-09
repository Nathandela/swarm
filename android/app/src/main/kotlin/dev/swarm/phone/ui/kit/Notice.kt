package dev.swarm.phone.ui.kit

import android.content.Context
import android.widget.LinearLayout
import android.widget.TextView
import dev.swarm.phone.R

/**
 * Who is speaking in a notice: the screen, or the machine refusing something.
 *
 * IT IS TWO WORDS AND NOT A COLOUR, which is [CtaKind]'s arrangement and for [CtaKind]'s reason.
 * A factory taking an ink would put the choice back at the sixteen call sites this component
 * exists to take it away from -- and the two inks are not interchangeable: `--p-ink2` is a
 * secondary voice and `--p-err` is a verdict.
 */
enum class NoticeKind { INFO, ERROR }

/**
 * derived: docs/design/substrate-components.md §4 Notice line
 *
 * The sentence a screen says about its own state: a stale mark, a warning, a refusal.
 *
 * There is deliberately no `origin:` line. Neither artifact draws a notice -- there is no `.note`
 * rule in the shared Substrate block and the retired mock has no class for one either -- so §4 is
 * the whole specification. [emptyState] and [readOnlyNote] are in the same position and say so the
 * same way.
 *
 * **THE DEFECT IT CLOSES IS THAT "NO APPEARANCE" IS AN APPEARANCE.** Sixteen sites built a bare
 * `TextView` and set text on it, and eight KDocs recorded that as the absence of a decision --
 * "there is no notice or body-copy component in the kit ... so this renders at the theme's default
 * until there is one". A `TextView` with no `TextAppearance` renders at the platform's ~14 sp, and
 * the largest body style in this app's ladder is `Body.Message` at 12.5 sp. So every stale mark,
 * every warning and every routed refusal was set BIGGER than the block it was qualifying, on eight
 * screens, for as long as the absence was documented.
 *
 * **THE ERROR VARIANT MOVES THE INK AND NOTHING ELSE.** §4 is explicit that the style and the box
 * are shared: what a refusal changes is who is speaking, not how loudly, and a second size or a
 * second weight would make the machine's answer shout over the screen's own sentences. It is a
 * parameter rather than a second factory for the reason `bloom` is a parameter on [ctaButton] --
 * the site differs, not the component.
 *
 * IT CARRIES NO MARGIN, NO PADDING AND NO GRAVITY, and that is §4's own cell rather than an
 * omission. [readOnlyNote] centres itself and insets `space_18` because it sits under a full-bleed
 * well; this line appears in eight different stacks, and a component that carried one screen's air
 * would be wrong in the other seven. The air belongs to whatever column composes it, exactly as it
 * did when these were bare views -- which is what makes the change to those sixteen sites a change
 * of type and ink and of nothing else.
 *
 * IT SETS NO TAG. Every call site names the PART it is drawing (`DetailTag.STALE`,
 * `PeekTag.STALE`, `SettingsTag.NOTICE`), because a screen with four of these needs to tell them
 * apart and one shared kit tag would find whichever came first.
 */
fun notice(
    context: Context,
    text: CharSequence,
    kind: NoticeKind = NoticeKind.INFO,
): TextView = Kit.textView(context).apply {
    setTextAppearance(R.style.TextAppearance_Swarm_Body_Secondary)
    setTextColor(Kit.colour(context, noticeInk(kind)))
    this.text = text
    layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
}

/**
 * origin: .sheet2 .ctx
 *
 * The MACHINE'S own words under a [notice]: a raw reason, in the machine's own register.
 *
 * **THE DEFECT IT CLOSES IS A SPLICE.** Five screens composed `head: reason.` and drew the result
 * as one sentence, so `kill_switch: remote control is disabled (kill switch off)` -- a daemon Go
 * error, lower case, parenthesised, with a wire code in front of it -- rendered in this app's own
 * body type as though this app had written it. Two of the five had no head at all and showed the
 * bare reason as the whole notice. A reader cannot tell where the product's copy stops and the
 * diagnostic starts, and the product answers for both.
 *
 * **IT IS `.sheet2 .ctx` AND NOT A NEW DERIVATION.** Substrate draws this exact cell -- `500
 * 10.5px var(--p-mono)` in `--p-ink3`, the context line under the approval sheet's question -- and
 * `type.xml` already cites the rule as `Mono.Meta`'s origin. What that rule is FOR is the same
 * thing this is for: the identifiers under a sentence, which qualify it and are not it.
 *
 * **IT IS A SECOND FACTORY AND NOT A THIRD [NoticeKind].** That parameter moves the ink and
 * nothing else, on §4's own instruction that a refusal is the same KIND of sentence in a different
 * voice. This is not a sentence: it is mono, it is tertiary, and it is a different type role
 * entirely -- a variant that changed family, size and ink would be a second component wearing an
 * enum.
 *
 * **`--p-ink3` IS BELOW APCA'S ABSOLUTE 30 AND IS TAKEN ON THE STANDING RULE, NOT BY EXCEPTION.**
 * `android/gate/obsidian_contrast_test.go` declares the tertiary ink `roleIncidental`, accepted
 * because it "is never the sole carrier of required information". That constraint BINDS THIS
 * FACTORY and is the reason it may exist: the head sentence carries the user's next act -- your
 * machine did not end this session, your machine kept this device registered -- and this line is
 * diagnostic. A caller that put a remedy here, or that drew this with no notice above it, would
 * make the tertiary ink the sole carrier and the deviation would stop being legitimate.
 *
 * IT CARRIES NO MARGIN, NO PADDING AND NO TAG, which is [notice]'s arrangement for [notice]'s
 * reason: the air belongs to the column that composes the pair, and every call site names the PART
 * it is drawing so a screen with two of these can tell them apart.
 */
fun noticeDetail(context: Context, text: CharSequence): TextView = Kit.textView(context).apply {
    setTextAppearance(R.style.TextAppearance_Swarm_Mono_Meta)
    setTextColor(Kit.colour(context, R.color.swarm_text_tertiary))
    this.text = text
    layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
}

/**
 * `--p-ink2`, or `--p-err` when the machine is the one talking.
 *
 * `--p-ink3` is the plausible wrong answer and it is wrong for [readOnlyNote]'s reason: it is what
 * de-emphasised looks like everywhere else in this kit, and it is 3.17 to 3.50:1 on every surface
 * in this product -- under the 4.5:1 floor for text a user is meant to read. A notice is the one
 * kind of prose that is always worth reading.
 */
private fun noticeInk(kind: NoticeKind): Int = when (kind) {
    NoticeKind.INFO -> R.color.swarm_text_secondary
    NoticeKind.ERROR -> R.color.swarm_state_error
}
