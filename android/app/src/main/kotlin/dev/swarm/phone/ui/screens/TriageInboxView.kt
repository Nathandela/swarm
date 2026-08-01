package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import android.widget.ScrollView
import dev.swarm.phone.ui.kit.TabItem
import dev.swarm.phone.ui.kit.chipRow
import dev.swarm.phone.ui.kit.filterChip
import dev.swarm.phone.ui.kit.navHeader
import dev.swarm.phone.ui.kit.sectionLabel
import dev.swarm.phone.ui.kit.sessionList
import dev.swarm.phone.ui.kit.sessionRow
import dev.swarm.phone.ui.kit.tabBar

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

    /**
     * Derivation row 8 `.empty`, under the heading of a section with nothing in it.
     *
     * **NOTHING CARRIES THIS TAG YET, AND THAT IS AN UNMET CLAUSE RATHER THAN A DEAD CONSTANT.**
     * PB-DS-9 requires an empty section to render "as a section with its empty copy", and the
     * block that copy goes in is derivation table row 8 -- `Body.Message` / `--p-ink2`, centred,
     * 2 x `space_24` vertical -- which the kit does not ship. This package may not build it: a
     * visual factory outside `ui/kit/` contradicts PB-DS-6 in the same breath as claiming it, and
     * the fence in `android/gate/s24_screens_test.go` would have to allowlist the file it lives
     * in. The copy exists and is asserted ([TriageInboxScreen.emptyCopyFor]); what is missing is
     * one factory, and `TriageInboxViewTest` fails on exactly this until it lands.
     */
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
 * @param below views this slice has NOT recomposed, hosted inside the inbox's own scroll so the
 *  app still has one window, one scrolling column and one tab bar. It is a parameter rather than
 *  something the caller wraps around this view because the alternative is a split screen -- an
 *  inbox in the top half and the old flat list in the bottom -- which would read as a design
 *  decision. Null is the finished shape, and every test below passes null.
 */
fun triageInboxView(
    context: Context,
    screen: InboxScreen,
    onSelectSession: (String) -> Unit,
    onSelectScope: (String?) -> Unit,
    below: View? = null,
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
        navHeader(context, screen.title, screen.live).apply { tag = InboxTag.NAV },
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
        // EVERY Group gets its heading, whether or not anything is under it. Dropping the empty
        // ones is the obvious implementation and it is wrong for a triage surface: the sections
        // then move under the user as sessions change group, and "nothing is waiting on me" --
        // the most useful fact this screen can report -- becomes indistinguishable from "that
        // section scrolled away".
        content.addView(
            sectionLabel(context, section.heading).apply { tag = InboxTag.SECTION_LABEL },
        )
        // AND ITS COPY BELONGS HERE, in one call the kit cannot yet answer -- see
        // [InboxTag.SECTION_EMPTY]. The heading survives; the sentence under it does not exist.
        if (section.rows.isEmpty()) return@forEach
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

    val scroll = ScrollView(context).apply {
        // The content is shorter than the screen on a quiet inbox, and without this the tab bar
        // would ride up under the last section instead of sitting at the bottom.
        isFillViewport = true
        clipChildren = false
        clipToPadding = false
        // `scrollbar-width: none` (derivation row 20).
        isVerticalScrollBarEnabled = false
        // Weight 1: the list takes whatever is left after the fixed bar below it.
        layoutParams = LinearLayout.LayoutParams(MATCH, 0, 1f)
        addView(content)
    }

    return LinearLayout(context).apply {
        orientation = LinearLayout.VERTICAL
        clipChildren = false
        clipToPadding = false
        layoutParams = ViewGroup.LayoutParams(MATCH, MATCH)
        addView(scroll)
        addView(
            tabBar(
                context,
                screen.tabs.map { tab ->
                    TabItem(
                        label = tab.label,
                        // NO ICON ASSET EXISTS. `TabItem.icon` is nullable and the tab renders its
                        // label alone; the four glyphs are drawables nobody has drawn, and a
                        // placeholder would be worse than a gap because it would look finished.
                        selected = tab.selected,
                        badgeCount = tab.badgeCount,
                        badgeDescription = tab.badgeDescription,
                    )
                },
            ).apply { tag = InboxTag.TABS },
        )
    }
}

private const val MATCH = ViewGroup.LayoutParams.MATCH_PARENT
private const val WRAP = ViewGroup.LayoutParams.WRAP_CONTENT
