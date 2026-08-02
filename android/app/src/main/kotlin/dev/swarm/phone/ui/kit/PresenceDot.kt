package dev.swarm.phone.ui.kit

import android.content.Context
import android.view.View
import android.widget.LinearLayout
import dev.swarm.phone.R

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
 * has four. So the binding here is a BOOLEAN and the Group fence stays exactly as strict as it was.
 *
 * **IT NEVER GLOWS, IN EITHER STATE.** Row 11: "Flat in both states -- no glow. Nothing glows
 * unless it is alive, and a reachable machine is not a running agent." Online is the state that
 * feels alive, so this is the component's one plausible wrong answer; it is also why this factory
 * takes no glow parameter at all rather than defaulting one to null.
 *
 * @param online true ONLY for the relay's own `online`. `App.Presence` has three states --
 *  `unknown`, `offline`, `online` (internal/remote/relay/client.go) -- and `unknown` means the
 *  relay has no live record, e.g. after its own restart. It takes the recessive ink with offline,
 *  which is §10's reading of the same collision on `completed`: both mean not active. What must
 *  not happen is `unknown` reading as reachable, and the caller says the relay's actual word in
 *  the line beside this mark rather than letting a colour translate it.
 * @param description what a screen reader says about the mark, or null where the row states
 *  presence in words -- [statusDot]'s arrangement and its reason. Never the empty string, which is
 *  the platform's idiom for "decorative, skip me" rather than for "I have no words of my own".
 */
fun presenceDot(context: Context, online: Boolean, description: CharSequence? = null): View {
    val corePx = Kit.dpPx(context, KitMetrics.DOT_DP)
    return View(context).apply {
        background = StatusDotDrawable(
            fill = Kit.colour(context, presenceColourRes(online)),
            glow = null,
            diameterPx = Kit.dp(context, KitMetrics.DOT_DP),
            glowRadiusPx = 0f,
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
 * The two colours row 11 binds presence to, DIRECTLY.
 *
 * It goes through neither `Kit.groupColour` nor `group-tokens.tsv`, and that is the point: those
 * answer "what colour is this Group", and presence is not one. `--p-ink3`'s own row in
 * `android/design-tokens.tsv` already records this second use -- "per inventory G2, the offline
 * machine dot" -- so the token join sees this site without the Group join having to.
 */
private fun presenceColourRes(online: Boolean) = if (online) {
    R.color.swarm_state_ok
} else {
    R.color.swarm_text_tertiary
}
