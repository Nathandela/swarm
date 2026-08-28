package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.widget.LinearLayout
import android.widget.TextView
import dev.swarm.phone.ui.kit.CtaKind
import dev.swarm.phone.ui.kit.KitTag
import dev.swarm.phone.ui.kit.MenuChoice
import dev.swarm.phone.ui.kit.NoticeKind
import dev.swarm.phone.ui.kit.conversationMenu
import dev.swarm.phone.ui.kit.ctaButton
import dev.swarm.phone.ui.kit.navHeaderDrill
import dev.swarm.phone.ui.kit.notice
import dev.swarm.phone.ui.kit.overflowControl
import dev.swarm.phone.ui.kit.screenAir
import dev.swarm.phone.ui.kit.sessionList
import dev.swarm.phone.ui.kit.settingsRow

/**
 * Wave R4's machine switcher AS DRAWN (bead agents-tracker-0ox9): the companion view of
 * [MachinesPanelScreen], in the module's established shape (screen model + View beside it --
 * TriageInboxView, ActivityPanelView, PairOnlyView).
 *
 * EVERY AFFORDANCE SPENDS THE MODEL'S RECORDED COPY and no other words: [MachinesPanelScreen] is
 * where ADD_LABEL, FORGET_LABEL and GLOBAL_INBOX_LABEL live, so the control a test (or a screen
 * reader, or a person) finds by those words is the one the composition wired. A label typed here
 * is the drift that made the pairing panel exist and be unfindable (agents-tracker-64rf). The one
 * word this file does type is [ADD_TOGGLE_LABEL], the header action's, because the model is
 * frozen this wave (phone-refit-playbook W7 leaves MachinesPanelScreen.kt untouched) and the
 * action is chrome that reveals the form, not the affordance that submits it -- that one still
 * reads ADD_LABEL.
 *
 * A ROW IS THE SWITCH CONTROL, BY MACHINE ID (MM4): tapping a row fires [onSwitchComputer] with
 * the row's OWN id, never its display name, so two rows sharing a name switch to two different
 * machines. A BROKEN row takes no tap at all -- the panel already knows the row is broken, so it
 * renders [MachinesPanelScreen.brokenNotice]'s fault AT REST instead of spending a signed
 * operation on a refusal the screen can state itself (MM8) -- and it still offers Forget, because
 * forget-or-re-pair are the broken pairing's whole affordance set.
 *
 * ADD IS BEHIND THE HEADER AND FORGET IS BEHIND THE ROW (phone-refit-playbook W7.6). The add form,
 * its CTA and its limits used to be unconditional children under the rows, and every row's
 * trailing control was the destructive Forget -- so a healthy two-computer screen was two rows, a
 * form nobody asked for, and two red controls. Now the header carries one Add action that reveals
 * the form block, and each row carries an overflow that opens a one-item `conversationMenu`
 * (`ConversationMenu.kt`, reused) whose one item is Forget. A healthy panel is its rows and
 * nothing below them.
 *
 * THE DESTRUCTIVE AND AUTHORISING CONTROLS FILTER OBSCURED TOUCHES (PB-SEC-12 clause 1). They are
 * built per draw from the row set, so they cannot join PhoneSurface's construction-time list the
 * way the long-lived controls do; the property is applied here, at construction of each control
 * -- the menu's Forget row included, when the menu is opened.
 */
object MachinesTag {
    /** The drill header. */
    const val NAV = "machines.nav"

    /** W7.6: the header's Add action, which reveals [ADD_FORM]. */
    const val ADD_TOGGLE = "machines.add.toggle"

    /** W7.6: the block holding the surface's add form, its CTA and its limits; composed by [ADD_TOGGLE]. */
    const val ADD_FORM = "machines.add.form"

    /** The aggregate inbox entry (inbox.global). */
    const val GLOBAL_INBOX = "machines.global.inbox"

    /** ADR-018's cap sentence, present only when the roster exceeds the cap. */
    const val CAP = "machines.cap"

    /** One switcher row -- the switch control for its machine id. */
    const val ROW = "machines.row"

    /** W7.6: a row's overflow, which opens the row's menu. */
    const val ROW_MENU = "machines.row.menu"

    /** A broken row's own fault, rendered at rest (MM8). */
    const val BROKEN = "machines.broken"

    /** The per-row phone-side forget (playbook 4.9): since W7.6 the row menu's one item. */
    const val FORGET = "machines.forget"

    /** Playbook 4.1's add entry: the control that submits the add form beside it. */
    const val ADD = "machines.add"

    /**
     * What Add computer cannot finish, stated under the form it is about (round 3). The tag is
     * named for the slot rather than for the constant it renders: a `val ADD_LIMITS` in this file
     * would make it the DECLARING file of that name, and the gate that asks who SPENDS
     * MachinesPanelScreen.ADD_LIMITS would then skip the one file that does.
     */
    const val LIMITS = "machines.add.limits"
}

/**
 * The machine switcher as a view.
 *
 * @param onBack where the drill chevron goes, or null for a composition with no way back to wire
 *  (the JVM suite's), which draws no dead control (`navHeaderDrill(back = null)`'s own ruling).
 * @param addForm the add-computer form the SURFACE owns (its fields must survive a redraw, like
 *  the launch form's), placed inside the form block under [MachinesPanelScreen.ADD_LABEL]'s
 *  control -- a NAMED slot, never an anonymous `below:` column. Null composes no form fields; the
 *  block still carries the CTA and the limits.
 * @param nowUnixMs this phone's clock, for the row's last-sync age alone (round 2: playbook
 *  4.2:198's third row fact, previously not rendered). A parameter rather than a read so the
 *  JVM suite can freeze the words -- the surface passes the same `System.currentTimeMillis()`
 *  it already spends into `SyncStatus.of`; the default keeps a caller that has no opinion
 *  honest rather than frozen at the epoch.
 */
fun machinesPanelView(
    context: Context,
    panel: MachinesPanel,
    onAddComputer: () -> Unit,
    onSwitchComputer: (String) -> Unit,
    onForgetComputer: (String) -> Unit,
    onOpenGlobalInbox: () -> Unit,
    onBack: (() -> Unit)? = null,
    addForm: View? = null,
    nowUnixMs: Long = System.currentTimeMillis(),
): View {
    val column = LinearLayout(context).apply {
        orientation = LinearLayout.VERTICAL
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
    }

    // THE FORM BLOCK IS COMPOSED WHEN ADD IS PRESSED AND REMOVED WHEN IT IS PRESSED AGAIN --
    // never hidden. PB-DS-9's fence (android/gate/s24_screens_test.go): a screen states what is
    // on it by composing it, and a second statement that hides what it just added is the shape
    // three `render*Step` functions had. So at rest the block does not exist on the screen, and
    // a healthy panel is its rows and nothing below them.
    val formBlock = addFormBlock(context, addForm, onAddComputer)
    column.addView(
        navHeaderDrill(
            context,
            back = if (onBack == null) null else panel.back,
            title = panel.title,
            trailing = ctaButton(context, ADD_TOGGLE_LABEL, CtaKind.MORE).apply {
                tag = MachinesTag.ADD_TOGGLE
                setOnClickListener {
                    if (formBlock.parent == null) column.addView(formBlock) else column.removeView(formBlock)
                }
            },
        ).apply {
            tag = MachinesTag.NAV
            onBack?.let { back ->
                findViewWithTag<View>(KitTag.DRILL_BACK)?.setOnClickListener { back() }
            }
        },
    )

    // The aggregate destination's NAMED way in, above the per-machine rows because it is about
    // all of them at once.
    column.addView(
        ctaButton(context, MachinesPanelScreen.GLOBAL_INBOX_LABEL, CtaKind.MORE).apply {
            tag = MachinesTag.GLOBAL_INBOX
            setOnClickListener { onOpenGlobalInbox() }
        }.screenAir(),
    )

    // The documented limitation, rendered honestly and only when it binds (ADR-018).
    panel.capNotice?.let { line ->
        column.addView(notice(context, line).apply { tag = MachinesTag.CAP }.screenAir())
    }

    val list = sessionList(context)
    panel.rows.forEach { row ->
        val rowView = settingsRow(
            context = context,
            label = row.displayName,
            // THE MARK IS ON THE ROW AND NOT ON THE LABEL (round 3): the label is the
            // machine's own name and a control is found by it, so the selection joins the
            // row's other facts on the second line instead of renaming the machine.
            sublabel = MachinesPanelScreen.statusLine(
                row,
                nowUnixMs,
                selected = row.machineId == panel.selectedMachineId,
            ),
            trailing = overflowControl(context).apply { tag = MachinesTag.ROW_MENU },
        ).apply {
            tag = MachinesTag.ROW
            // A broken row is deliberately NOT a switch control: pressing it must surface
            // the fault below rather than issue a select the facade will refuse (MM8).
            if (!row.broken) {
                filterTouchesWhenObscured = true
                setOnClickListener { onSwitchComputer(row.machineId) }
            }
        }
        list.addView(rowView)
        // THE MENU IS BUILT WHEN IT IS OPENED, not at rest with its visibility off: a Forget
        // that exists invisibly is still a Forget on the screen -- to a screen reader, to a
        // test reading the words, and to the next person who lifts the visibility.
        rowView.findViewWithTag<View>(MachinesTag.ROW_MENU)?.setOnClickListener {
            rowMenu(context, list, rowView, row.machineId, onForgetComputer)
        }
        MachinesPanelScreen.brokenNotice(row)?.let { fault ->
            list.addView(
                notice(context, fault, NoticeKind.ERROR).apply { tag = MachinesTag.BROKEN },
            )
        }
    }
    column.addView(list)

    // BEHIND THE HEADER'S ACTION (W7.6): the form block joins the column under the rows when
    // Add is pressed (see the header above), and nothing is composed below the rows until then.
    return column
}

/**
 * The header action's word. Chrome rather than an affordance the model records -- it reveals
 * the form whose own CTA spends [MachinesPanelScreen.ADD_LABEL] -- and the model is frozen this
 * wave; see the file comment.
 */
private const val ADD_TOGGLE_LABEL = "Add"

/** The one id the row menu reports; the words on the row are the model's FORGET_LABEL. */
private const val FORGET_CHOICE = "forget"

/**
 * The add block: the surface's form (when it has one), the CTA that submits it, and what Add
 * cannot finish. Built once per draw and composed under the rows by the header's action.
 */
private fun addFormBlock(context: Context, addForm: View?, onAddComputer: () -> Unit): LinearLayout =
    LinearLayout(context).apply {
        orientation = LinearLayout.VERTICAL
        layoutParams = LinearLayout.LayoutParams(MATCH, WRAP)
        tag = MachinesTag.ADD_FORM
        addForm?.let { addView(it) }
        addView(
            ctaButton(context, MachinesPanelScreen.ADD_LABEL, CtaKind.APPROVE).apply {
                tag = MachinesTag.ADD
                filterTouchesWhenObscured = true
                setOnClickListener { onAddComputer() }
            }.screenAir(),
        )
        // WHAT ADD CANNOT FINISH, UNDER THE FORM IT IS ABOUT (round 3) and rendered the way
        // ADR-018's cap sentence is: this slice registers the pairing and no more -- the computer
        // still needs its own pairing ceremony (bead agents-tracker-ak2s) and switching does not
        // move the live session. Unlike the cap sentence it is UNCONDITIONAL within the block,
        // because unlike the cap it always binds; a form that cannot be completed and does not
        // say so leaves a permanently stale row and a user with no way to know why.
        addView(
            notice(context, MachinesPanelScreen.ADD_LIMITS).apply { tag = MachinesTag.LIMITS }
                .screenAir(),
        )
    }

/**
 * Open or close one row's menu: `conversationMenu` with its one item, Forget, placed directly
 * under the row it is about and removed on the next press. The Forget row carries
 * [MachinesTag.FORGET] and PB-SEC-12 clause 1's touch filter, exactly as the chip it replaces
 * did; the confirmation ([MachinesPanelScreen.FORGET_CONFIRM]) is still the surface's, behind
 * [onForgetComputer].
 */
private fun rowMenu(
    context: Context,
    list: ViewGroup,
    rowView: View,
    machineId: String,
    onForgetComputer: (String) -> Unit,
) {
    val at = list.indexOfChild(rowView) + 1
    val open = list.getChildAt(at)?.takeIf { it.tag == MachinesTag.FORGET }
    if (open != null) {
        list.removeView(open)
        return
    }
    val menu = conversationMenu(
        context,
        listOf(MenuChoice(FORGET_CHOICE, MachinesPanelScreen.FORGET_LABEL, destructive = true)),
    ) { onForgetComputer(machineId) }.apply {
        tag = MachinesTag.FORGET
        // Under the overflow that opened it, at the trailing edge, hugging its one row.
        (layoutParams as? LinearLayout.LayoutParams)?.gravity = Gravity.END
        for (i in 0 until childCount) {
            (getChildAt(i) as? TextView)?.filterTouchesWhenObscured = true
        }
    }
    list.addView(menu, at)
}

private const val MATCH = ViewGroup.LayoutParams.MATCH_PARENT
private const val WRAP = ViewGroup.LayoutParams.WRAP_CONTENT
