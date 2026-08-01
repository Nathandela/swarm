package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View

/**
 * Phase B slice S24 -- PB-DS-6 and PB-DS-9: the triage inbox, composed from the component kit.
 *
 * THIS FILE IS THE REQUIREMENT'S SUBJECT. PB-DS-6 was recorded NOT MET on the ground that the kit
 * had ZERO production call sites -- twelve components, four rounds of audit behind them, and three
 * surface files importing none of them, so that across the whole design-system branch nothing a
 * user sees had changed. "The kit is the only way a screen is built" is a claim about screens, and
 * until one was built out of it there was nothing to check.
 *
 * IT COMPOSES AND PASSES DATA, AND IT DOES NOTHING ELSE. There is no colour here, no dimension, no
 * radius and no typeface -- not as a literal and not as a resource. Every visual decision belongs
 * to the kit factory that renders it, and `android/gate/s24_screens_test.go` fences this package
 * to exactly that: an `R.color`, an `R.dimen`, an `R.style`, a `setTextAppearance`, a
 * `setPadding` or a `background =` here fails the build. That fence is stricter than PB-DS-11's,
 * on purpose -- PB-DS-11 stops a screen INVENTING a value, and this stops it CHOOSING one.
 *
 * THE TAGS ARE HOW A TEST FINDS THE COMPOSITION. Android has no stable child identity without an
 * `@id`, `res/values/ids.xml` is closed, and `View.generateViewId` is not stable across instances,
 * so an assertion that walked child indices would silently start checking a different view the day
 * a component gained a child. [InboxTag] names each part for the element of inventory C1 it
 * renders, which is the same discipline `KitTag` uses inside the kit.
 */
object InboxTag {
    /** C1.1 `.pnav`. */
    const val NAV = "inbox.nav"

    /** C1.2 `.chips`. */
    const val SCOPES = "inbox.scopes"

    /** C1.3 `.plabel` -- one per Group in TRIAGE_ORDER, empty or not. */
    const val SECTION_LABEL = "inbox.section.label"

    /** C1.3 `.prows`. */
    const val SECTION_ROWS = "inbox.section.rows"

    /** Derivation row 8 `.empty`, under the heading of a section with nothing in it. */
    const val SECTION_EMPTY = "inbox.section.empty"

    /** C1.3 `.prow`. */
    const val ROW = "inbox.row"

    /** C1.4 `.ptabs`. */
    const val TABS = "inbox.tabs"

    /**
     * The parts whose ON-SCREEN ORDER is the recorded composition. The section parts are not in it
     * because they repeat; what this set pins is that the header comes first and the tab bar is
     * last -- a tab bar that scrolled with the content would be a different screen.
     */
    val COMPOSITION: Set<String> = setOf(NAV, SCOPES, TABS)
}

/**
 * The triage inbox as a view.
 *
 * @param onSelectSession the session a tapped row names. The inbox used to be consumed as
 *  `.flatMap{}.firstOrNull()?.id` -- a session picker that discarded every section, row, label and
 *  grouping -- so this is the callback that makes the rows something a person acts on rather than
 *  a list the surface reads one id out of.
 * @param onSelectScope the machine a tapped chip names, or null for the "All machines" chip. A
 *  scope bar that did not narrow anything would be decoration, which is the same defect one
 *  indirection out.
 */
fun triageInboxView(
    context: Context,
    screen: InboxScreen,
    onSelectSession: (String) -> Unit,
    onSelectScope: (String?) -> Unit,
): View = TODO("S24: the triage inbox is not drawn yet")
