package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import dev.swarm.phone.ui.kit.CtaKind
import dev.swarm.phone.ui.kit.KitTag
import dev.swarm.phone.ui.kit.NoticeKind
import dev.swarm.phone.ui.kit.ctaButton
import dev.swarm.phone.ui.kit.monoWell
import dev.swarm.phone.ui.kit.navHeaderDrill
import dev.swarm.phone.ui.kit.notice
import dev.swarm.phone.ui.kit.readOnlyNote
import dev.swarm.phone.ui.kit.screenAir
import dev.swarm.phone.ui.kit.scrolledHorizontally
import dev.swarm.phone.ui.kit.sectionLabel

/**
 * WAVE R8 -- the capability-routed terminal fallback AS DRAWN (ADR-017 T1/T4).
 *
 * **THIS IS THE ONE BODY IN THE APP THAT MAY PRINT THE DAEMON-RENDERED GRID.** ADR-009 (1) --
 * "no terminal emulation and no raw grid anywhere in the app" -- is RE-SCOPED to `structured_chat`
 * sessions by ADR-017 T1 and is not repealed; `android/gate/r8_fallback_ui_test.go` allows
 * `monoWell(terminal = true)` in this file alone.
 *
 * **A SNAPSHOT LINE IS LITERAL MONOSPACE TEXT AND IS NEVER RE-INTERPRETED** (amendment T4-c). No
 * markdown, no annotated string, no link detection, no HTML: re-interpreting a row would let the
 * SESSION'S OWN OUTPUT author a tappable link, a heading or hidden text inside the phone's chrome,
 * out of characters that are individually innocent and that the machine-side sanitizer has no
 * reason to strip. The kit's own markdown renderer is one import away and is deliberately not
 * imported here; the gate fails the build if it ever is.
 *
 * **EVERY ROW IS LAID OUT LEFT-TO-RIGHT, AND THIS IS THE HALF THE MACHINE CANNOT SUPPLY**
 * (amendment T4-c). The machine strips every bidi formatting, override and isolate rune, but
 * IMPLICIT bidi needs no control character at all: a row containing strongly-RTL characters is
 * reordered by the text stack on its own, so `SnapText`'s stated "no Unicode bidi rune can visually
 * spoof what is displayed" is FALSE without a layout attribute on this side. Forcing the paragraph
 * direction is a layout attribute, not terminal emulation, and crosses no boundary this decision
 * draws.
 */
object TerminalFallbackTag {
    /** The drill header. */
    const val NAV = "terminal.fallback.nav"

    /** The honest header: provider, detected version, and the capability that cost it chat. */
    const val HEADER = "terminal.fallback.header"

    /** The staleness indicator, derived from the snapshot's own age. */
    const val STALENESS = "terminal.fallback.staleness"

    /** The interleaving warning. */
    const val INTERLEAVING = "terminal.fallback.interleaving"

    /** The sanitized grid itself. */
    const val GRID = "terminal.fallback.grid"

    /** The persistent control banner, on screen for the whole life of a generation. */
    const val BANNER = "terminal.fallback.banner"

    /** The in-view release. */
    const val RELEASE = "terminal.fallback.release"

    /** The read-only sentence, drawn when the machine granted no control. */
    const val READ_ONLY = "terminal.fallback.readonly"
}

/**
 * @param model the routed session's facts. Built by [TerminalFallbackModel.from], which answers
 *  null for every session the machine did not route here -- so this view cannot be composed for a
 *  structured session even by a caller who wants to.
 * @param rows the machine-sanitized grid, one string per row, exactly as `vt.SnapText` produced it.
 * @param gridRows the DECLARED row count, which is the well's floor: a well that only wraps its
 *  content resizes on every frame, and everything below it moves while the user is reading.
 * @param snapshotAge how long ago the MACHINE rendered [rows], in milliseconds. Never arrival time.
 * @param streamStale whether the phone core reports the machine's TERMINAL STREAM as stale. It is
 *  independent of [snapshotAge] and wins over it: an old snapshot on a live stream is an idle
 *  terminal, and the same snapshot on a dead stream is an unknown one.
 * @param controlRemaining milliseconds left on a live control generation, or null when there is
 *  none. Null draws the read-only sentence instead of the banner, so "read only" is STATED.
 * @param onRelease the in-view release. Null draws no control affordance at all.
 */
fun terminalFallbackView(
    context: Context,
    model: TerminalFallbackModel,
    rows: List<String>,
    gridRows: Int,
    snapshotAge: Long,
    streamStale: Boolean = false,
    controlRemaining: Long? = null,
    onBack: (() -> Unit)? = null,
    onRelease: (() -> Unit)? = null,
): View {
    val column = LinearLayout(context).apply {
        orientation = LinearLayout.VERTICAL
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    }

    column.addView(
        navHeaderDrill(context, back = if (onBack == null) null else BACK, title = model.headline).apply {
            tag = TerminalFallbackTag.NAV
            onBack?.let { back ->
                findViewWithTag<View>(KitTag.DRILL_BACK)?.setOnClickListener { back() }
            }
        },
    )

    // The honest header's second line. It is a NOTICE and not a section label because it is a
    // statement about this session's capability, not a heading over the grid.
    column.addView(
        notice(context, model.explanation).apply {
            tag = TerminalFallbackTag.HEADER
            screenAir()
        },
    )

    // The staleness indicator, and the empty string means the snapshot is FRESH rather than that
    // nobody checked -- so nothing is drawn, which is the only honest quiet state.
    val staleness = TerminalFallbackModel.stalenessLine(snapshotAge, streamStale)
    if (staleness.isNotEmpty()) {
        column.addView(
            notice(context, staleness, NoticeKind.ERROR).apply {
                tag = TerminalFallbackTag.STALENESS
                screenAir()
            },
        )
    }

    // The control affordance, drawn ABOVE the grid so it is never scrolled off the screen it is
    // making a claim about. Persistent for the whole life of the generation (T6): a sheet that
    // grants control and then disappears leaves a user typing into a generation they have to
    // remember they opened.
    if (controlRemaining != null) {
        column.addView(
            notice(context, TerminalFallbackModel.controlBanner(controlRemaining)).apply {
                tag = TerminalFallbackTag.BANNER
                screenAir()
            },
        )
        onRelease?.let { release ->
            column.addView(
                ctaButton(context, TerminalFallbackModel.RELEASE_LABEL, CtaKind.MORE).apply {
                    tag = TerminalFallbackTag.RELEASE
                    setOnClickListener { release() }
                    screenAir()
                },
            )
        }
    } else if (!model.controlOffered) {
        column.addView(
            readOnlyNote(context, TerminalFallbackModel.READ_ONLY).apply {
                tag = TerminalFallbackTag.READ_ONLY
            },
        )
    }

    column.addView(sectionLabel(context, GRID_LABEL))

    // The grid. `terminal = true` is the escape-filtered VT snapshot's ink, and the rows are
    // joined with a newline HERE rather than trusted from the machine as one blob: SnapText's
    // contract is one string per grid row, and a row that arrived carrying its own line break
    // would have made the grid taller than the machine drew it.
    val grid = rows.joinToString("\n")
    val well = monoWell(
        context = context,
        terminal = true,
        lines = gridRows,
        text = grid,
    ).apply {
        tag = TerminalFallbackTag.GRID
        // ADR-017 T4-c: the half no machine-side sanitizer can supply. Implicit bidi reorders a
        // line whenever it contains strongly-RTL characters, with NO control character present.
        textDirection = TextDirection.Ltr
        layoutDirection = LayoutDirection.Ltr
    }
    column.addView(well.scrolledHorizontally())

    // The interleaving warning goes LAST, under the grid, because it is about what happens when
    // the user acts on what they just read (playbook:286-287). Decision G keeps the owner typing
    // throughout and both streams stay live; the UX warns, and never "fixes" this by evicting the
    // terminal user.
    column.addView(
        notice(context, TerminalFallbackModel.INTERLEAVING_WARNING).apply {
            tag = TerminalFallbackTag.INTERLEAVING
            screenAir()
        },
    )

    return column
}

/** Where the drill chevron goes. */
private const val BACK = "Back"

/** The heading over the machine's own screen. */
private const val GRID_LABEL = "Terminal"

/**
 * The platform's LTR text-direction heuristic under the name the decision uses for it.
 *
 * A named constant rather than the bare platform integer because the VALUE is not the point --
 * the RULE is, and a reader who meets `textDirection = 3` at a call site has no way to reach it.
 */
private object TextDirection {
    /** Force LTR, regardless of the first strong character in the row. */
    const val Ltr = View.TEXT_DIRECTION_LTR
}

/** The same, for the paragraph the row is laid out in. */
private object LayoutDirection {
    /** Force LTR, regardless of the locale or of the row's own content. */
    const val Ltr = View.LAYOUT_DIRECTION_LTR
}

private const val MATCH = ViewGroup.LayoutParams.MATCH_PARENT
private const val WRAP = ViewGroup.LayoutParams.WRAP_CONTENT
