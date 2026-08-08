package dev.swarm.phone.ui.kit

import android.content.Context
import android.view.View
import android.widget.LinearLayout
import dev.swarm.phone.R

/**
 * The three states the maquette draws for the presence mark, and the whole of what a caller may
 * ask for.
 *
 * IT IS AN ENUM AND NOT A `status.Group` STRING, and that distinction is the same one
 * [presenceDot] exists for: a Group is the server's derived session state, rendered verbatim
 * through `android/group-tokens.tsv`, and presence is the RELAY's opinion about reachability with
 * three values where a Group has four. An enum is also what makes the third state unskippable --
 * `when (mark)` over a closed set has no default arm to fold `UNKNOWN` into, which is exactly how
 * it went unrendered while the parameter was a `Boolean`.
 *
 * The names are the relay's own words (`internal/remote/relay/client.go`) and the maquette's own
 * class suffixes, which is what lets the tests read `.pdot.${mark}` straight out of the design.
 */
enum class PresenceMark { ONLINE, OFFLINE, UNKNOWN }

/**
 * derived: docs/design/substrate-components.md #11 Machine row
 *
 * The 7 dp mark that says whether a machine is reachable.
 *
 * There is deliberately no `origin:` line. Substrate draws no machines screen at all, so `.mrow`
 * is the retired mock's class and row 11 is the whole specification -- the same position
 * [settingsRow] and [emptyState] are in. What this shares with [statusDot] -- the drawable,
 * [KitMetrics.DOT_DP], the round cap PB-DS-4 records -- is `.pdot`'s geometry borrowed by row 11,
 * not a rule that declares this component.
 *
 * **IT IS A SEPARATE FACTORY BECAUSE PRESENCE IS NOT A `status.Group`, AND THE CHEAP
 * IMPLEMENTATION IS THE DEFECT.** Row 11 gives presence `--p-ok` online and `--p-ink3` offline,
 * which are the same two tokens `ready_for_review` and `completed` already carry -- so
 * `statusDot(context, if (online) "ready_for_review" else "completed")` renders every pixel of
 * this component correctly and is the phone inventing a Group for a machine. The four Groups are
 * derived once, on the server, and rendered verbatim (PB-TOK-8); `android/group-tokens.tsv` is the
 * checked-in join and `TestPBDS7_TheStatusDotBindingIsTheCheckedInMapping` refuses any bound key
 * that is not in it, deliberately. Presence is not on that table and could not be: it is
 * `App.Presence`, the RELAY's opinion about reachability, and it has three values where a Group
 * has four. So the binding here is [PresenceMark] and the Group fence stays exactly as strict as
 * it was.
 *
 * **IT NEVER GLOWS, IN EITHER STATE.** Row 11: "Flat in both states -- no glow. Nothing glows
 * unless it is alive, and a reachable machine is not a running agent." Online is the state that
 * feels alive, so this is the component's one plausible wrong answer; it is also why this factory
 * takes no glow parameter at all rather than defaulting one to null.
 *
 * **THE THIRD STATE IS DRAWN, AND IT USED TO BE FOLDED ONTO THE SECOND.** This parameter was
 * `online: Boolean` and `unknown` took the offline fill, on the argument that the caller states
 * the relay's actual word in the line beside the mark. That argument was correct against the
 * SUBSTRATE artifact, which draws no `.pdot.unknown` rule at all -- row 11 gives presence two
 * colours because two was all there was to render. The Obsidian maquette draws three, and
 * ADR-009 D2 makes it normative:
 *
 *	.pdot.unknown { background: transparent; border: 1px solid var(--p-ink3); }
 *
 * -- a hollow ring, which is the absence of a record drawn as the absence of a mark. What the old
 * reasoning protected survives untouched: `unknown` still must never read as REACHABLE, and a
 * recessive-ink ring could not be mistaken for the `--p-ok` disc.
 *
 * @param mark which of the relay's three words this is. `App.Presence` returns `unknown`,
 *  `offline` or `online` (internal/remote/relay/client.go), and `unknown` means the relay has no
 *  live record -- after its own restart, for instance, because presence is never persisted.
 * @param description what a screen reader says about the mark, or null where the row states
 *  presence in words -- [statusDot]'s arrangement and its reason. Never the empty string, which is
 *  the platform's idiom for "decorative, skip me" rather than for "I have no words of my own".
 */
fun presenceDot(context: Context, mark: PresenceMark, description: CharSequence? = null): View {
    val corePx = Kit.dpPx(context, KitMetrics.DOT_DP)
    return View(context).apply {
        background = StatusDotDrawable(
            fill = Kit.colour(context, presenceColourRes(mark)),
            glow = null,
            diameterPx = Kit.dp(context, KitMetrics.DOT_DP),
            glowRadiusPx = 0f,
            // The ring's weight, and 0 for the two discs. `--p-hair`'s own 1 dp: the maquette
            // writes `border: 1px solid`, which is the same `1px solid` every other hairline in
            // the design is, so it spends the constant that already carries it rather than a
            // second 1 that would have to be kept in step with the first.
            //
            // ROUNDED, THROUGH `dpPx`, LIKE EVERY OTHER HAIRLINE IN THIS KIT. The diameter beside
            // it is exact because a 7 dp circle's radius is arithmetic; a 1 dp STROKE is a border,
            // and PB-DS-6 requires one design value to have one rendering -- at density 2.625 the
            // exact form is 2.625 px and the rounded one is 3, so a ring quantised differently
            // from `cardSurface`'s hairline would paint two weights of the same rule on one
            // screen.
            strokePx = when (mark) {
                PresenceMark.UNKNOWN -> Kit.dpPx(context, KitMetrics.HAIRLINE_DP).toFloat()
                else -> 0f
            },
        )
        // No halo, so no inflation and nothing to give back: the mark occupies the design's 7 dp
        // and that is also the whole of the view. The negative margins statusDot carries are
        // compensation for a software layer this component never allocates.
        layoutParams = LinearLayout.LayoutParams(corePx, corePx)
        contentDescription = description
        if (description == null) importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
        tag = KitTag.PRESENCE_DOT
    }
}

/**
 * The two colours the design binds presence to, DIRECTLY.
 *
 * It goes through neither `Kit.groupColour` nor `group-tokens.tsv`, and that is the point: those
 * answer "what colour is this Group", and presence is not one. `--p-ink3`'s own row in
 * `android/design-tokens.tsv` already records this second use -- "per inventory G2, the offline
 * machine dot" -- so the token join sees this site without the Group join having to.
 *
 * TWO COLOURS FOR THREE STATES IS THE MAQUETTE'S OWN ARITHMETIC. `.pdot.unknown` takes `--p-ink3`
 * for its BORDER, the same ink `.pdot.offline` takes for its fill; what separates the two states
 * is the shape, and the shape is [StatusDotDrawable.strokePx]'s business rather than this
 * function's. A third colour here would be a value the design does not state.
 */
private fun presenceColourRes(mark: PresenceMark) = when (mark) {
    PresenceMark.ONLINE -> R.color.swarm_state_ok
    PresenceMark.OFFLINE, PresenceMark.UNKNOWN -> R.color.swarm_text_tertiary
}
