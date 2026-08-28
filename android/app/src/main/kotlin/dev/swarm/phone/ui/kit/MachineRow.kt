package dev.swarm.phone.ui.kit

import android.content.Context
import android.view.Gravity
import android.view.View
import android.widget.LinearLayout
import dev.swarm.phone.R

/**
 * derived: docs/design/substrate-components.md #11 Machine row
 *
 * One machine: whether it is reachable, what it is called, and what this phone can vouch for.
 *
 * There is deliberately no `origin:` line. Substrate's artifact draws no machines screen at all --
 * its demo phone renders the inbox and nothing else -- so `.mrow` is the retired mock's class and
 * row 11 is the whole specification. [settingsRow], [activityRow] and [readOnlyNote] are in the
 * same position and say so the same way.
 *
 * **WHY THIS IS A COMPONENT AND NOT A CALL TO [sessionRow], WHICH IS WHAT IT LOOKS LIKE.** The two
 * rows really are the same drawing: a mark, a bold name, a mono identifier in `--p-ink3`, and a
 * secondary line under them, on the same card. That similarity is why the reuse was tried first,
 * and the seam is why it failed. `sessionRow` builds its leading mark itself, by calling
 * [statusDot] with a `status.Group` -- so rendering a machine through it means passing a Group the
 * server never sent, and the two whose colours happen to match (`ready_for_review` for online,
 * `completed` for offline) are exactly the fabrication `android/group-tokens.tsv`'s gate exists to
 * refuse. **A reuse justified by identical pixels is not a reuse justified by a compatible seam.**
 * The padding differs too, once the row is read rather than remembered: `space_12` x `space_14`
 * here against `.prow`'s `space_10` x `space_12`.
 *
 * WHAT IS SHARED IS SHARED PROPERLY, which is the other half of §2's reuse rule. The card is
 * [cardSurface] -- one recipe for `--p-card`, the hairline, the radius and the `--p-card-fx` key
 * light, called by every row in this app -- and the three type roles are the three `.prow` spends,
 * because `.mrow .eid` and `.prow .ag` are the same cell and the derivation table says so. What is
 * NOT shared is the leading mark and four spacing steps, which is what this file is.
 *
 * @param presence the meta line, which the SCREEN writes: `App.MachinePresence` is the relay's
 *  opinion and PB-APP-11 requires this phone's own freshness beside it, so what a person reads
 *  here is a sentence about both. Copy is the screen's (PB-DS-9) and this component styles it.
 * @param mark drives the leading dot and nothing else. It carries all three of the relay's words;
 *  see [presenceDot], which argues why `unknown` is neither `online` nor `offline` and what the
 *  maquette draws for it.
 * @param endpoint row 11's `endpoint id` cell. Null renders no cell AT ALL rather than an empty
 *  one -- [settingsRow]'s sublabel and [activityRow]'s timestamp take the same position. Its call
 *  site passes null exactly when the id is ALREADY in the name cell, which is a machine that
 *  published no hostname (agents-tracker-ksvb.1): the same string in both cells would be one fact
 *  printed twice with the second copy wearing the mock's label. See `MachineRow.endpoint` in
 *  `ui/screens/SettingsPanel.kt`, which owns that decision -- it moved there with the CONNECTION
 *  section when agents-tracker-nx44.3 deleted the Machines destination.
 * @param presenceDescription what a screen reader says about the mark, or null where [presence]
 *  already states it in words -- which it does at the call site for every state but one: a
 *  healthy machine prints no [presence] line at all (agents-tracker-ksvb.6), and the dot is what
 *  is left to carry it.
 */
fun machineRow(
    context: Context,
    machine: CharSequence,
    presence: CharSequence,
    mark: PresenceMark,
    endpoint: CharSequence? = null,
    presenceDescription: CharSequence? = null,
): View {
    val gap = Kit.dimenPx(context, R.dimen.swarm_space_8)

    val line = LinearLayout(context).apply {
        orientation = LinearLayout.HORIZONTAL
        gravity = Gravity.CENTER_VERTICAL
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    }
    line.addView(
        presenceDot(context, mark = mark, description = presenceDescription).apply {
            // ASSIGNED RATHER THAN ADDED TO, which is the one place this row and `sessionRow`
            // differ in more than a number. A glowing status dot carries negative margins that
            // compensate for the software layer its halo needs, so that row must ADD the gap or
            // spend the compensation. Row 11's mark is flat in both states -- "nothing glows
            // unless it is alive, and a reachable machine is not a running agent" -- so there is
            // no compensation here to preserve, and an `+=` would be carrying a defence against
            // a state this component cannot be in.
            (layoutParams as LinearLayout.LayoutParams).marginEnd = gap
        },
    )
    line.addView(
        Kit.textView(context).apply {
            Kit.appearance(this, R.style.TextAppearance_Swarm_Title_Row)
            // Row 11 states `--p-ink` explicitly, where `.prow .pj` inherits the same token from
            // `.pscreen`. The value is one; the authority is this row's.
            setTextColor(Kit.colour(context, R.color.swarm_text_primary))
            text = machine
            // `flex: 1` -- the name takes the slack, so the identifier sits hard right and a long
            // machine name pushes it rather than running under it.
            layoutParams = LinearLayout.LayoutParams(0, WRAP, 1f)
            tag = KitTag.MACHINE_NAME
            Kit.identityCell(this)
        },
    )
    if (endpoint != null) {
        line.addView(
            Kit.textView(context).apply {
                Kit.appearance(this, R.style.TextAppearance_Swarm_Mono_Agent)
                setTextColor(Kit.colour(context, R.color.swarm_text_tertiary))
                text = endpoint
                layoutParams = LinearLayout.LayoutParams(WRAP, WRAP).apply { marginStart = gap }
                tag = KitTag.MACHINE_ENDPOINT
                Kit.identityCell(this)
            },
        )
    }

    val row = LinearLayout(context).apply {
        orientation = LinearLayout.VERTICAL
        background = cardSurface(context, attention = false)
        setPaddingRelative(
            Kit.dimenPx(context, R.dimen.swarm_space_14),
            Kit.dimenPx(context, R.dimen.swarm_space_12),
            Kit.dimenPx(context, R.dimen.swarm_space_14),
            Kit.dimenPx(context, R.dimen.swarm_space_12),
        )
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    }
    row.addView(line)
    row.addView(
        Kit.textView(context).apply {
            Kit.appearance(this, R.style.TextAppearance_Swarm_Body_Secondary)
            setTextColor(Kit.colour(context, R.color.swarm_text_secondary))
            text = presence
            layoutParams = LinearLayout.LayoutParams(MATCH, WRAP).apply {
                topMargin = Kit.dimenPx(context, R.dimen.swarm_space_4)
            }
            tag = KitTag.MACHINE_META
        },
    )
    return row
}
