package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import dev.swarm.phone.ui.kit.chipRow
import dev.swarm.phone.ui.kit.emptyState
import dev.swarm.phone.ui.kit.filterChip
import dev.swarm.phone.ui.kit.navHeader
import dev.swarm.phone.ui.kit.sectionLabel
import dev.swarm.phone.ui.kit.sessionList
import dev.swarm.phone.ui.kit.sessionRow

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

    /** C1.3 `.plabel` -- one per Group in TRIAGE_ORDER that has rows, plus an empty Needs you (W7.2). */
    const val SECTION_LABEL = "inbox.section.label"

    /** C1.3 `.prows`. */
    const val SECTION_ROWS = "inbox.section.rows"

    /**
     * Derivation row 8, under the heading of a section with nothing in it.
     *
     * IT IS ON THE BLOCK AND NOT ON THE SECTION, so a test can count how many sections are saying
     * "nothing here" and compare that to how many are actually empty. Both directions are defects:
     * a missing block is a heading over nothing, and a block on a populated section tells a user
     * holding two live sessions that there is nothing in it.
     */
    const val SECTION_EMPTY = "inbox.section.empty"

    /** C1.3 `.prow`. */
    const val ROW = "inbox.row"

    /**
     * The parts whose ON-SCREEN ORDER is the recorded composition. The section parts are not in it
     * because they repeat; what this set pins is that the header comes first.
     *
     * C1.4 `.ptabs` IS NO LONGER IN IT AND IS NO LONGER THIS SCREEN'S. The tab bar was composed
     * here, which is exactly why the other three destinations could not be reached: a bar drawn
     * inside one of four screens is a bar the other three do not have, so a tab that swapped the
     * content would land the user on Machines with nothing to come back with. It is
     * [ScaffoldTag.TABS] now, and "the tab bar is the last thing on screen" is asserted there --
     * over this screen's tags and the scaffold's together, so it is still one statement about one
     * screen.
     */
    val COMPOSITION: Set<String> = setOf(NAV, SCOPES)
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
 * @param promoted the sessions whose Group became NeedsInput since the last draw --
 *  `TriageInboxScreen.promotions` over the screen this one replaces. The row it names plays
 *  ADR-009 D5's specular sweep once as it is built.
 *
 *  IT IS A SET OF IDS AND NOT A FLAG ON THE SCREEN, for the reason the empty case makes plain: a
 *  session promoted on a machine the user has just scoped away from is not on this viewport, and
 *  an id this list does not hold sweeps nothing. A boolean would have to be resolved against
 *  "whichever row is first", which is the wrong row every time two are in flight.
 * @param below views this slice has NOT recomposed, hosted under the sections so the app still has
 *  one window and one scrolling column. It is a parameter rather than something the caller wraps
 *  around this view because the alternative is a split screen -- an inbox in the top half and the
 *  old flat list in the bottom -- which would read as a design decision. Null is the finished
 *  shape, and every test below passes null.
 * @param status the sync mark for the nav row, or null while the phone has nothing to report
 *  (agents-tracker-nx44.2). It is a view the SURFACE owns, on the scaffold slot's own terms: what
 *  it says changes on the surface's clock and this screen is rebuilt on its model's, so a screen
 *  that built one would rebuild the mark under whoever is pressing it.
 */
fun triageInboxView(
    context: Context,
    screen: InboxScreen,
    onSelectSession: (String) -> Unit,
    onSelectScope: (String?) -> Unit,
    promoted: Set<String> = emptySet(),
    below: View? = null,
    status: View? = null,
): View {
    val content = LinearLayout(context).apply {
        orientation = LinearLayout.VERTICAL
        // A glowing dot is inflated past its own bounds and the tab badge overhangs its icon, so
        // every container between them and the window has to be told not to clip. Necessary at
        // each level: a parent that clips undoes what its child allowed.
        clipChildren = false
        clipToPadding = false
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    }

    content.addView(
        navHeader(context, screen.title, screen.live, status).apply { tag = InboxTag.NAV },
    )
    content.addView(
        chipRow(context).apply {
            tag = InboxTag.SCOPES
            screen.scopes.forEach { chip ->
                addView(
                    filterChip(
                        context = context,
                        label = chip.label,
                        selected = chip.selected,
                        present = chip.present,
                        contentDescription = chip.description,
                    ).apply { setOnClickListener { onSelectScope(chip.machine) } },
                )
            }
        },
    )

    screen.sections.forEach { section ->
        // AN EMPTY SECTION COLLAPSES, EXCEPT NEEDS YOU (phone-refit-playbook W7.2). This used to
        // draw every Group's heading whether or not anything was under it, on the argument that
        // "nothing is waiting on me" must stay distinguishable from "that section scrolled
        // away". The argument is right about exactly ONE section: the one blocked on the human,
        // whose emptiness is the most useful fact this screen reports. For the other three, a
        // heading over a caption is three headings over nothing on a one-session inbox, and the
        // list under them reads as empty when it is not. The model still emits all four
        // sections; which one survives empty is its own named decision, not a second one here.
        if (section.rows.isEmpty() && section.group != TriageInboxScreen.BLOCKED) return@forEach
        content.addView(
            sectionLabel(context, section.heading).apply { tag = InboxTag.SECTION_LABEL },
        )
        // AND NEEDS YOU'S COPY GOES UNDER IT. A heading over nothing is the same defect wearing
        // a heading, which is why row 8's own note says "the `.plabel` stays and this block sits
        // under it". Only the blocked section reaches here empty, so this is its caption.
        //
        // THE CONDITION IS THE POINT AND NOT BOILERPLATE. Drawing the block unconditionally is
        // the obvious over-correction, and it tells a user holding two live sessions that the
        // section has nothing in it. `TriageInboxViewTest` asserts both directions.
        if (section.rows.isEmpty()) {
            // agents-tracker-nx44.1: `compact = true`. Row 8's block is authored for ONE
            // whole-panel empty state; this screen can draw up to four of them, and the
            // whole-panel 96 dp vertical air pushes the last section's caption under the tab bar
            // on a quiet inbox. `emptyState`'s own KDoc has both cells; this is the per-section one.
            content.addView(
                emptyState(context, section.emptyCopy, compact = true)
                    .apply { tag = InboxTag.SECTION_EMPTY },
            )
            return@forEach
        }
        content.addView(
            sessionList(context).apply {
                tag = InboxTag.SECTION_ROWS
                section.rows.forEach { row ->
                    addView(
                        sessionRow(
                            context = context,
                            project = row.project,
                            agent = row.agent,
                            need = row.need,
                            group = row.group,
                            lit = row.lit,
                            promoted = row.id in promoted,
                            stateDescription = row.stateDescription,
                        ).apply {
                            tag = InboxTag.ROW
                            // THE ROW IS THE PICKER. PhoneSurface used to read the inbox as
                            // `.flatMap{}.firstOrNull()?.id` and act on whatever came first; the
                            // session a person is looking at is the one they mean.
                            setOnClickListener { onSelectSession(row.id) }
                        },
                    )
                }
            },
        )
    }

    below?.let { content.addView(it) }

    // THE COLUMN IS RETURNED BARE: no scroll of its own, and no tab bar. Both belong to
    // [phoneScaffoldView], which hosts this screen and the other three above one shared bar
    // (derivation row 20). A scroll here would be a second one inside the scaffold's, and the bar
    // here was what left `activityPanelView` -- and the since-deleted `machinesPanelView` -- with
    // no way in.
    return content
}

private const val MATCH = ViewGroup.LayoutParams.MATCH_PARENT
private const val WRAP = ViewGroup.LayoutParams.WRAP_CONTENT
