package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import dev.swarm.phone.ui.kit.notice
import dev.swarm.phone.ui.kit.sectionLabel
import dev.swarm.phone.ui.kit.sessionList
import dev.swarm.phone.ui.kit.settingsRow
import dev.swarm.phone.ui.kit.statusLabel

/**
 * Phase B slice S25 -- PB-DS-6 and PB-DS-9: the link section, composed from the component kit.
 *
 * WHAT IT COMPOSES. A `sectionLabel`, the clock verdict when there is one, and a `settingsRow` per
 * repair channel inside a `sessionList`. It decides nothing about how any of them looks:
 * `android/gate/s24_screens_test.go` fences this package to exactly that, so an `R.color`, an
 * `R.dimen`, an `R.style`, a `setTextAppearance`, a `setPadding` or a `background =` here fails the
 * build.
 *
 * **THE DESIGN HAS NO ROW FOR EITHER OF THESE THINGS, AND THAT IS SAID HERE RATHER THAN WORKED
 * AROUND SILENTLY.** `docs/design/substrate-components.md` specifies no clock-skew notice and no
 * per-channel arrival readout; Substrate's artifact draws neither and the retired mock draws
 * neither. The alternative to what is below was inventing a component to carry them. What ships
 * instead is derivation row 15 spent twice -- `settingsRow` is "a labelled row with an optional
 * second line and an optional trailing control", and `statusLabel` is row 15's own other trailing
 * form, "status text `Label.CardHead` / `--p-hero`". Both already exist with their own `derived:`
 * citations, so this file adds no derivation and needs no annotation of its own.
 *
 * **THE LIVENESS LABEL IS ON THE LIVE CHANNELS AND NOWHERE ELSE.** `statusLabel` is `--p-hero`,
 * and row 15 spells out that hero is the LIVENESS claim rather than a status colour -- its one
 * other caller says "active" about encryption. A stale channel therefore gets no label at all
 * rather than a differently-coloured one: this file makes no ink decision, and the absence is what
 * `LinkPanelViewTest` asserts. What a stale channel gets instead is the second line, carrying
 * `StreamView.notice` -- which says what is missing, where a colour could only say that something
 * is.
 *
 * THE CLOCK LINE IS THE KIT'S `notice`, for the reason `ActivityPanelView`'s stale line is
 * (agents-tracker-ksvb.4). It was a bare `TextView` carrying the model's copy and no appearance at
 * all, and this paragraph called that the absence of a decision. It was not one: the platform
 * default is ~14 sp, larger than every body style in this app's ladder, so a clock warning was set
 * bigger than the four channel rows underneath it. `§4 Notice line` specifies the type and the ink
 * and `ui/kit/Notice.kt` chooses them, which is what keeps this screen out of the choice.
 *
 * IT SITS UNDER THE HEADING AND ABOVE THE CHANNELS, which is the one placement decision here. The
 * heading names the subject; a clock this phone cannot trust is the first thing true of it, and it
 * is true of all four channels at once rather than of any one of them -- so it cannot attach to a
 * row, and it has to be read before them rather than found among them.
 *
 * **NOTHING IN THIS FILE OWNS A CLICK**, and here that is a gap rather than the usual division.
 * PB-SYNC-1's repair action is `App.Resync`, which stays unbound: it is rate-bounded per section
 * 6.0 and its refusal needs rendering. So this screen reports and cannot repair, and [below] is
 * where the rest of the Machines destination is hosted.
 */
object LinkTag {
    /** `.plabel` over the four channels. */
    const val SECTION_LABEL = "link.section.label"

    /** PB-TIME-1: what the screen says when this phone's clock cannot be trusted. */
    const val CLOCK = "link.clock"

    /** One repair channel: derivation row 15. */
    const val CHANNEL = "link.channel"

    /** The parts whose ON-SCREEN ORDER is the recorded composition. */
    val COMPOSITION: Set<String> = setOf(SECTION_LABEL, CLOCK, CHANNEL)
}

/**
 * The link section as a view.
 *
 * @param below views this section does not own, hosted under it -- on the Machines destination
 *  that is [MachinesPanelScreen.UNAVAILABLE_COPY], the sentence saying what this phone still
 *  cannot read about its machine. Null is the shape this section has on its own.
 */
fun linkPanelView(
    context: Context,
    panel: LinkPanel,
    below: View? = null,
): View {
    val column = LinearLayout(context).apply {
        orientation = LinearLayout.VERTICAL
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    }

    column.addView(sectionLabel(context, panel.heading).apply { tag = LinkTag.SECTION_LABEL })

    // NO VIEW AT ALL RATHER THAN AN EMPTY ONE, which is `settingsRow`'s rule for its own second
    // line and holds harder here: an always-attached notice is either a permanent warning over a
    // phone whose clock is fine, or a permanent blank line where one goes.
    if (panel.clockNotice.isNotEmpty()) {
        column.addView(notice(context, panel.clockNotice).apply { tag = LinkTag.CLOCK })
    }

    // `sessionList` IS `.prows`, and it is a REUSE for `ActivityPanelView`'s reason: it carries
    // the gap and the side padding, and building either here would be the PB-DS-6 violation the
    // kit exists to stop -- a screen typing spacing.
    column.addView(
        sessionList(context).apply {
            panel.channels.forEach { channel ->
                addView(
                    settingsRow(
                        context = context,
                        label = channel.stream,
                        sublabel = channel.notice,
                        // The liveness label, or NOTHING. `statusLabel` is `--p-hero` and row 15
                        // spells out that hero is the liveness claim, so a stale channel gets no
                        // trailing view rather than a differently-inked one -- which would be
                        // this file choosing an ink for a state.
                        trailing = channel.liveLabel?.let { statusLabel(context, it) },
                    ).apply { tag = LinkTag.CHANNEL },
                )
            }
        },
    )

    below?.let { column.addView(it) }
    return column
}

private const val MATCH = ViewGroup.LayoutParams.MATCH_PARENT
private const val WRAP = ViewGroup.LayoutParams.WRAP_CONTENT
