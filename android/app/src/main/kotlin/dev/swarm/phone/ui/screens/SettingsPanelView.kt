package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import dev.swarm.phone.ui.kit.CtaKind
import dev.swarm.phone.ui.kit.KitTag
import dev.swarm.phone.ui.kit.ctaButton
import dev.swarm.phone.ui.kit.denyChip
import dev.swarm.phone.ui.kit.killSwitchPanel
import dev.swarm.phone.ui.kit.machineRow
import dev.swarm.phone.ui.kit.navHeader
import dev.swarm.phone.ui.kit.notice
import dev.swarm.phone.ui.kit.screenAir
import dev.swarm.phone.ui.kit.sectionLabel
import dev.swarm.phone.ui.kit.settingsRow

/**
 * Phase B slice S24 -- PB-DS-6 and PB-DS-9: the settings screen, composed from the component kit.
 *
 * WHAT IT COMPOSES. Inventory C6 is a nav header, a section label and a stack of rows, and the
 * kit ships all three: `navHeader`, `sectionLabel` and `settingsRow`. This file spends them and
 * decides nothing about how any of them looks -- `android/gate/s24_screens_test.go` fences this
 * package to exactly that, so an `R.color`, an `R.dimen`, an `R.style`, a `setTextAppearance`, a
 * `setPadding` or a `background =` here fails the build.
 *
 * WHAT IT STILL CANNOT COMPOSE IS THE TOGGLE. Derivation row 4 has no factory, so the trailing
 * control arrives as a parameter: [rowFor] is supplied by `SettingsSurface`, which owns the switch
 * anyway -- the listener, the touch filter PB-SEC-12 clause 1 requires, and the fact that the same
 * control has to survive a redraw. `settingsRow` takes a trailing `View` rather than a `Boolean`
 * precisely so a caller can place one it did not build, which is what makes the gap a parameter in
 * a signature rather than a lookalike hand-built where nothing checks it.
 *
 * THE PAIRING SECTION IS NOT IN INVENTORY C6, and that is agents-tracker-64rf's decision rather
 * than a drift: the retired mock draws `Notifications` and a now-void `Security` and nothing about
 * a paired machine, because the entry point only moved onto this screen after an owner could not
 * find it on a real handset. It is composed from the same two components the preference sections
 * are, plus `denyChip` for its one control, and it leads -- see [SettingsPanel.machineSection].
 * NOTHING HERE OWNS ITS CLICK, which is the division the toggle already arrives through: replacing
 * REVOKES THIS DEVICE, so the control carries a facade verb, PB-SEC-12 clause 1's touch filter and
 * an identity that has to survive a redraw, and `SettingsSurface` is where all three are. It
 * arrives as [settingsPanelView]'s `replaceFor` and is tagged [SettingsTag.REPLACE] once placed.
 *
 * THE CONNECTION SECTION IS NOT IN INVENTORY C6 EITHER, and it is what the Machines destination
 * left behind (agents-tracker-nx44.3). Inventory C4 was a whole screen; what survived the fold is
 * derivation row 11's `machineRow` -- this is now its only production call site, `machinesPanelView`
 * having been deleted with the destination -- and two §4 notice lines that draw only when there is
 * something wrong. Nothing here owns a click: the section reports and offers no control, because
 * the one repair action this phone has is the sync detail sheet's and the one destructive action is
 * the PAIRING row's, two lines above.
 *
 *
 * **THE COLUMN IS BARE AND ITS FLUSH CHILDREN CARRY THE SCREEN'S AIR** (owner ruling
 * 2026-08-09, agents-tracker-nx44.10). Every leaf on this screen renders at least
 * `swarm_space_12` from both edges, spent exactly once: the components that already hold
 * themselves off the glass keep their own step, and the ones §4 leaves bare -- the notice
 * line, a loose CTA, row 9's field -- get `screenAir` here. A padding on the column would
 * add 12 to the first group and re-run agents-tracker-2pnu F2's doubling; the argument is
 * `ui/kit/ScreenColumn.kt`'s and `ScreenAirSweepTest` is what holds every screen to it.
 *
 * **AND THE CARDS ARE IN THAT SECOND GROUP, WHICH THIS SCREEN READ WRONG ONCE.** Rows 11 and 15
 * spend `space_14` INSIDE their box; a padding is not a margin, so the label cleared the ruled
 * floor while the `--p-card` fill and its `--p-hair` border underneath ran edge to edge -- on the
 * one screen in the app that places those two rows on the ground instead of in `sessionList`,
 * which is the Inbox's own way of paying exactly this. So each row gets the same step here, once,
 * and keeps its 14 inside: a settings card now sits where a session card does. `killSwitchPanel`
 * is untouched, because row 12 gives that one a `space_14` margin of its own -- "the panel is the
 * one block ... that sits on the ground rather than inside a list, so nothing above it can carry
 * its inset" -- and 14 already clears 12.
 *
 * THE NOTICE LINES ARE THE KIT'S NOW (agents-tracker-ksvb.4). This paragraph read "THE NOTICE LINES
 * ARE STILL BARE `TextView`s ... that is the absence of a decision rather than one made here", and
 * the premise was false in the one way that matters: a `TextView` with no `TextAppearance` renders
 * at the platform's ~14 sp, which is larger than every body style in this app's ladder -- so the
 * disclosure and every dead-switch notice were the biggest body text on the settings screen, above
 * the rows they were about. `§4 Notice line` specifies them and `ui/kit/Notice.kt` draws them.
 * Reaching for `Body.Secondary` HERE would still be a screen choosing type, and the fence above
 * still refuses it.
 */
object SettingsTag {
    /** C6.1 `.pnav`. */
    const val NAV = "settings.nav"

    /** C6.2 `.seclabel` -- one per section. */
    const val SECTION_LABEL = "settings.section.label"

    /**
     * One `.setrow`. The row's own parts are the kit's and carry `KitTag.SETTINGS_*`; this tag is
     * the screen's handle on the whole row, which is what the accessibility consolidation below
     * needs a subject for.
     */
    const val ROW = "settings.row"

    /** What the panel says about switches that are inert or unconfirmed. */
    const val NOTICE = "settings.notice"

    /**
     * agents-tracker-u7sl: ADR-007 B143's push-delay disclosure. NOT [NOTICE] -- that tag is for
     * a fault this phone detected, and battery saver/Doze delaying a push is unconditional, so it
     * is drawn every time the section is rather than only when a switch is blocked.
     */
    const val DISCLOSURE = "settings.disclosure"

    /**
     * agents-tracker-64rf's paired-machine row. It is NOT [ROW]: that tag marks the push toggles,
     * and a caller reaching for them must not be handed a row whose control revokes a pairing.
     */
    const val MACHINE_ROW = "settings.machine.row"

    /**
     * The replace control, WHEREVER IT WAS BUILT. `SettingsSurface` builds the shipping one --
     * replacing revokes this device, so the control carries a facade verb and PB-SEC-12 clause 1's
     * touch filter, and that filter is applied at construction, on an instance that outlives every
     * redraw. The tag is how anything finds the one the panel actually placed.
     */
    const val REPLACE = "settings.machine.replace"

    /**
     * agents-tracker-0dij's way out of a permanently blocked notification permission, WHEREVER IT
     * WAS BUILT -- `SettingsSurface` builds the shipping one, because pressing it leaves the app and
     * PB-SEC-12 clause 1's filter is applied at construction on an instance that outlives every
     * redraw. The tag is how anything finds the one the panel actually placed.
     */
    const val PERMISSION_REDIRECT = "settings.permission.redirect"

    /**
     * agents-tracker-2yfn's way out of a blocked wake CHANNEL, WHEREVER IT WAS BUILT -- for
     * [PERMISSION_REDIRECT]'s reasons, which it shares exactly. It is NOT that tag: the two open
     * different system screens, and a caller reaching for one must not be handed the other.
     */
    const val DELIVERY_REDIRECT = "settings.delivery.redirect"

    /**
     * agents-tracker-nx44.3's CONNECTION row -- derivation row 11's machine row, on this screen.
     *
     * It is NOT [MACHINE_ROW]: that one is the PAIRING section's, whose control revokes this
     * device, and a caller reaching for one must not be handed the other. They sit two lines apart
     * and are about the same computer, which is exactly why they need different handles.
     */
    const val CONNECTION_ROW = "settings.connection.row"

    /**
     * The one line naming the repair channels with holes in them. Absent -- not empty -- while
     * every channel is current: a blank line still occupies its line height and its gap, so a
     * healthy phone would get a strip of nothing under its machine that reads as a warning
     * somebody forgot to write.
     */
    const val CONNECTION_HEALTH = "settings.connection.health"

    /**
     * PB-TIME-1's clock verdict, absent while the clock is in budget for [CONNECTION_HEALTH]'s
     * reason. It is a separate tag because it is a separate fault with a separate remedy: the
     * health line is about the machine's frames and this is about this phone's own clock.
     */
    const val CONNECTION_CLOCK = "settings.connection.clock"

    /**
     * Derivation row 12's kill-switch panel, drawn only where the switch is OFF
     * (agents-tracker-2pnu F5, agents-tracker-zecs).
     *
     * IT IS THE THIRD ABSENT-WHEN-HEALTHY BLOCK IN THIS SECTION, for [CONNECTION_HEALTH]'s
     * reason and one more: what row 12 draws is a bordered container in `--p-err`, and an
     * `--p-err` box reporting that nothing is wrong is the loudest possible way to say it.
     */
    const val REMOTE_ACCESS = "settings.connection.remote"

    /** The parts whose ON-SCREEN ORDER is the recorded composition. */
    val COMPOSITION: Set<String> = setOf(NAV, SECTION_LABEL, MACHINE_ROW, CONNECTION_ROW, ROW)
}

/**
 * The settings panel as a view.
 *
 * @param rowFor the trailing control for one row -- the toggle derivation row 4 specifies and the
 *  kit does not ship. It is a function of the row rather than a flat list so a caller cannot
 *  silently pair the wrong switch with the wrong preference, which is the one defect
 *  [SettingsPanel]'s bijection exists to prevent.
 * @param replaceFor the paired machine row's trailing control, for the same reason [rowFor] is a
 *  parameter: pressing it revokes this device, so it carries a facade verb, PB-SEC-12 clause 1's
 *  touch filter and an identity that has to survive a redraw -- all three of which are the
 *  surface's. It takes the row so the words on the control are the MODEL'S and a caller cannot
 *  type a second copy of them. The default is the chip this screen would place if nobody owned the
 *  click: correct to look at, and attached to nothing.
 * @param redirectFor the control that leads to the system's notification settings, for the label
 *  [SettingsPanel.permissionRedirectLabel] carries. It is a parameter for [replaceFor]'s reasons:
 *  the press starts an Activity, so it carries the touch filter and an identity that survives a
 *  redraw, and both are the surface's. The default is the control this screen would place if nobody
 *  owned the click -- correct to look at, and attached to nothing.
 * @param deliveryRedirectFor the control that leads to the WAKE CHANNEL's own page, for the label
 *  [SettingsPanel.deliveryRedirectLabel] carries. A second parameter rather than a second use of
 *  [redirectFor] because the two open different system screens and the Intent is fixed inside a
 *  listener installed at construction, so one control cannot be both. Its default is this screen's
 *  own, for [redirectFor]'s reason: correct to look at, and attached to nothing.
 * @param below views this slice has NOT recomposed, hosted under the panel. Null is the finished
 *  shape.
 * @param status the sync mark for the nav row, or null while the phone has nothing to report
 *  (agents-tracker-nx44.2). It is a view the SURFACE owns, on the scaffold slot's own terms: what
 *  it says changes on the surface's clock and this panel redraws itself, so a panel that built one
 *  would rebuild the mark under whoever is pressing it.
 */
fun settingsPanelView(
    context: Context,
    panel: SettingsPanel,
    rowFor: (SettingsRow) -> View,
    replaceFor: (PairedMachineRow) -> View = { row -> denyChip(context, row.replaceLabel) },
    redirectFor: (String) -> View = { label -> ctaButton(context, label, CtaKind.MORE) },
    deliveryRedirectFor: (String) -> View = { label -> ctaButton(context, label, CtaKind.MORE) },
    below: View? = null,
    status: View? = null,
): View {
    val column = LinearLayout(context).apply {
        orientation = LinearLayout.VERTICAL
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    }

    // C6.1. `navHeader`'s second argument is the live counter, and settings has none: the
    // counter is the inbox's in-context liveness readout (derivation §1.4) and a settings screen
    // has nothing in flight to report.
    column.addView(navHeader(context, panel.title, null, status).apply { tag = SettingsTag.NAV })

    // FIRST, above the preferences. This is where the pairing entry point lives once a phone is
    // paired (agents-tracker-64rf), and the defect it answers is an owner not finding it -- so it
    // is not put under two switches. The heading is a `.seclabel` like any other, so it carries the
    // same tag: the section order is then readable off the composition.
    panel.machineSection?.let { section ->
        column.addView(
            sectionLabel(context, section.heading).apply { tag = SettingsTag.SECTION_LABEL },
        )
        column.addView(
            settingsRow(
                context = context,
                label = section.row.label,
                sublabel = section.row.sublabel,
                // Row 13's arrangement, reused: the `.a2-no` treatment at chip metrics, for the
                // same class of action as Revoke -- because it IS the revoke. The control is the
                // caller's; this places it and tags it.
                trailing = replaceFor(section.row).apply { tag = SettingsTag.REPLACE },
            ).apply { tag = SettingsTag.MACHINE_ROW }.screenAir(),
        )
    }

    // UNDER THE PAIRING SECTION AND ABOVE THE PREFERENCES (agents-tracker-nx44.3). The row above
    // says which computer this phone is PINNED to; this says whether that computer is currently
    // reachable and whether what the app is showing came from it, so it qualifies the row above
    // and has to follow it. The preferences are a different subject and trail both.
    panel.connection?.let { section ->
        column.addView(
            sectionLabel(context, section.heading).apply { tag = SettingsTag.SECTION_LABEL },
        )
        column.addView(
            machineRow(
                context = context,
                machine = section.machine.name,
                presence = section.machine.presenceLine,
                mark = section.machine.mark,
                // Row 11's `endpoint id` cell, or nothing where the model says the id is already
                // in the name cell. The decision is the model's; this carries it.
                endpoint = section.machine.endpoint,
                // THE MODEL'S OWN CALL, carried rather than re-decided here. Null while the line
                // under the mark still says presence in words, including whose word it is -- a
                // described dot there would say it twice. Non-null exactly where a healthy machine
                // has printed nothing, which is the one case left where the dot is the only thing
                // on screen carrying the state.
                presenceDescription = section.machine.presenceDescription,
            ).apply { tag = SettingsTag.CONNECTION_ROW }.screenAir(),
        )
        // THE TWO FAULT LINES ARE PLACED ONLY WHEN THERE IS A FAULT. `notice` with an empty string
        // draws a `TextView` that still takes its line height and its gap, so an unconditional
        // placement puts a blank strip under the machine of every healthy phone -- the always-on
        // warning this app's conditional-notice discipline refuses everywhere else.
        section.health.takeIf { it.isNotEmpty() }?.let { line ->
            column.addView(
                notice(context, line).apply { tag = SettingsTag.CONNECTION_HEALTH }.screenAir(),
            )
        }
        section.clockNotice.takeIf { it.isNotEmpty() }?.let { line ->
            column.addView(
                notice(context, line).apply { tag = SettingsTag.CONNECTION_CLOCK }.screenAir(),
            )
        }
        // ROW 12'S PANEL, AND ONLY WHERE THE SWITCH IS OFF (agents-tracker-2pnu F5). It is the
        // last block in the section because it is the heaviest thing on the screen -- the one
        // component in the kit with an `--p-err` border -- and it qualifies everything above it:
        // a machine that refuses every command is why the phone is not getting what it asked for.
        // The model decides whether there is anything to say; a null here is a working switch.
        section.remoteAccess?.let { row ->
            column.addView(
                killSwitchPanel(
                    context = context,
                    title = row.title,
                    body = row.body,
                    command = row.command,
                ).apply { tag = SettingsTag.REMOTE_ACCESS },
            )
        }
    }

    panel.sections.forEach { section ->
        column.addView(
            sectionLabel(context, section.heading).apply { tag = SettingsTag.SECTION_LABEL },
        )
        section.rows.forEach { row ->
            column.addView(
                settingsRow(
                    context = context,
                    label = row.label,
                    sublabel = row.sublabel,
                    trailing = rowFor(row),
                ).apply { announceAsOneRow(row) }.screenAir(),
            )
        }
    }

    // UNCONDITIONAL AND SO DRAWN BEFORE THE NOTICES: the disclosure is not the reason a switch is
    // dead, so it does not belong under the sentence that is.
    column.addView(
        notice(context, panel.disclosure).apply { tag = SettingsTag.DISCLOSURE }.screenAir(),
    )

    // The loop variable is `line` and not `notice`: the sentence and the kit factory that draws it
    // cannot share a name in one scope.
    panel.notices.forEach { line ->
        column.addView(notice(context, line).apply { tag = SettingsTag.NOTICE }.screenAir())
    }

    // AFTER THE NOTICES, because the sentence is why the control is there: the blocked notice names
    // this control in its own words (`SettingsScreen.OPEN_NOTIFICATION_SETTINGS` is interpolated
    // into it), so a control drawn above the sentence would arrive before its reason.
    panel.permissionRedirectLabel?.let { label ->
        column.addView(
            redirectFor(label).apply { tag = SettingsTag.PERMISSION_REDIRECT }.screenAir(),
        )
    }

    // BESIDE IT AND UNDER THE NOTICES, for the same reason and by the same arrangement. The two are
    // never both present -- a permission notice suppresses the delivery one -- so what this places
    // is whichever way out the live fault has.
    panel.deliveryRedirectLabel?.let { label ->
        column.addView(
            deliveryRedirectFor(label).apply { tag = SettingsTag.DELIVERY_REDIRECT }.screenAir(),
        )
    }

    below?.let { column.addView(it) }
    return column
}

/**
 * Make the row ONE accessibility node, which is derivation row 15's "the whole row is one >=48 dp
 * target when it carries a toggle" expressed where it can be.
 *
 * Left alone a screen reader reads four things per row -- the label, the sublabel, the switch, and
 * nothing tying them together. With the description on the row and the kit's two lines excluded it
 * reads the row's words once and then the switch's state, which is two nodes and the right two.
 *
 * IT IS NOT A VISUAL DECISION AND SO IT IS NOT THE KIT'S. `importantForAccessibility` says nothing
 * about how anything looks; what it needs is the row's own [SettingsRow.description], which is copy
 * -- and copy is the screen's. `settingsRow` could not write this without being told the sentence.
 */
private fun View.announceAsOneRow(row: SettingsRow) {
    tag = SettingsTag.ROW
    isFocusable = true
    contentDescription = row.description
    listOf(KitTag.SETTINGS_LABEL, KitTag.SETTINGS_SUBLABEL).forEach { part ->
        findTagged(part)?.importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
    }
}

/**
 * The descendant the kit tagged with [tag], or null.
 *
 * It is not `kitFind`: that one lives in the TEST source set, and a production file cannot reach
 * it. Four lines rather than promoting it, because promoting a test helper into `ui/kit` is a
 * change to a package this slice may not touch.
 */
private fun View.findTagged(tag: String): View? {
    if (this.tag == tag) return this
    if (this !is ViewGroup) return null
    for (i in 0 until childCount) {
        getChildAt(i).findTagged(tag)?.let { return it }
    }
    return null
}

private const val MATCH = ViewGroup.LayoutParams.MATCH_PARENT
private const val WRAP = ViewGroup.LayoutParams.WRAP_CONTENT
