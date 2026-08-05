package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.CommandVerdict

/**
 * Which screen the window holds: the whole app, or the one offer an unpaired phone gets.
 *
 * IT IS A DECISION AND NOT A LAYOUT ACCIDENT, which is the whole reason it is a type. The pairing
 * block was reachable in the sense that it existed somewhere in the hierarchy -- appended below
 * four always-drawn triage section headings, on one of four tabs -- and the owner scrolled that
 * inbox on a real handset and did not find it (agents-tracker-64rf). A screen a user cannot find is
 * a screen the product does not have, so what an unpaired phone is shown is a choice this package
 * makes and states, like [PairingPanelScreen] choosing which control a step offers.
 */
enum class Presentation {
    /** One offer to pair, and nothing else. [pairOnlyView] draws it. */
    PAIR_ONLY,

    /** Today's four-tab scaffold, which a live pairing is what makes appear. */
    FULL_APP,
}

/**
 * Phase B -- agents-tracker-64rf: the screen an unpaired phone opens on.
 *
 * ## What the words are for
 *
 * Three sentences, because there are three things this screen has to do: name what is missing,
 * say why the app is otherwise empty, and offer the one action that fixes it. They are HERE and
 * not in the composition for PB-DS-9's reason -- copy belongs to the screen model, so a suite that
 * asserts what is drawn cannot agree with a re-worded copy of itself.
 *
 * [CTA] IS INVENTORY C7'S OWN NAME FOR THE FLOW IT OPENS ("Pair a computer",
 * [PairingPanelScreen]'s SCAN heading), and that is deliberate rather than a duplication: the
 * button and the screen it leads to say the same thing, which is how a person knows the press
 * worked. [TITLE] says what is being paired instead, so the two are not one sentence twice.
 */
object PairOnlyScreen {

    /** What this phone is, stated as the thing it is not yet. */
    const val TITLE = "Pair this phone"

    /**
     * Why there is nothing else on screen.
     *
     * THE SECOND HALF IS THE PART THAT MATTERS. Without it this screen reads as an app that has
     * lost its contents; with it, an empty window is the honest report that everything the app
     * shows belongs to a machine this handset has not got.
     */
    const val BODY = "Sessions, machines and activity all come from the machine this phone is " +
        "paired with. There is nothing else here until then."

    /** The one thing a person can do here. */
    const val CTA = "Pair a computer"

    /**
     * What a revoke leaves this phone unable to say, said (agents-tracker-qlf9).
     *
     * **THE PURGE ORDERING IS NOT THE DEFECT AND IS NOT CHANGED.** `SettingsSurface.onReplace`
     * destroys both key tiers in a `finally`, whether or not the command reached the machine, and
     * that is ADR-007 B133 decision 3: the situation the panic action exists for is one where the
     * phone may not reach its machine at all, and a purge that ran only on success would leave the
     * live keys on the very handset whose registration its owner has just disowned. The Go side
     * makes it structural too -- `App.RevokeThisDevice` records that a revoke "is the one command
     * whose success DESTROYS the path its own reply would come back on", because the daemon
     * removes the device and rotates the epoch in one transaction and the gateway then severs and
     * exits. Waiting for the outcome before purging would hang forever on the path that WORKED.
     *
     * **SO WHAT WAS MISSING IS THE SENTENCE, NOT THE WAIT.** Nothing told the user which of the
     * two states they are in, and they are very different: `swarm remote pair` is refused while
     * the machine still has this device registered (PB-STATE-10, single-device v1), so a user
     * whose revoke was refused walks into a pairing that fails with the reason on no screen at
     * all. It lives HERE because this is the screen the revoke drops them on and the screen the
     * next pairing attempt starts from.
     *
     * **THE REMEDY IS SPELLED THE WAY [dev.swarm.phone.ui.ErrorRouter] ALREADY SPELLS IT** for
     * `swarm/state-corrupt`, which is the same recovery: find the device, unregister it, pair
     * again. Two wordings for one pair of commands is two things for a reader to reconcile at the
     * moment they are least able to.
     */
    const val REVOKE_UNCONFIRMED = "This phone has unpaired itself, and your machine has not " +
        "confirmed that it removed the device. If pairing is refused, run `swarm remote devices` " +
        "on your machine to find this device and `swarm remote revoke <device-id>` to unregister " +
        "it first."

    /** The head of the refused sentence; the machine's own reason follows it. */
    private const val REVOKE_REFUSED = "Your machine refused to remove this device"

    /**
     * What is true of the handset once the revoke did not land, in either way it can fail to.
     *
     * IT IS ONE TAIL FOR BOTH because the fact is one fact: the purge ran, so the phone is
     * unpaired and the machine is not.
     */
    private const val STILL_REGISTERED = " This phone has unpaired itself anyway, so run " +
        "`swarm remote devices` on your machine to find this device and `swarm remote revoke " +
        "<device-id>` to unregister it before pairing again."

    /**
     * The machine's answer to the revoke this phone issued, as the screen states it.
     *
     * A CONFIRMED REMOVAL SAYS NOTHING. Both sides agree, so there is no divergence to report, and
     * a warning shown over a state that is fine teaches the user to ignore the one that is not.
     */
    fun revokeNoticeFor(verdict: CommandVerdict): String = when {
        verdict.accepted -> ""
        verdict.answered -> verdict.sentence(REVOKE_REFUSED) + STILL_REGISTERED
        else -> REVOKE_UNCONFIRMED
    }

    /**
     * The revoke that never reached the wire at all.
     *
     * @param routed PB-APP-9's own sentence for the failure, carried VERBATIM rather than
     *  re-worded: it is the one that names the remedy, and a second copy here is two files
     *  deciding what a transport failure reads as. What this adds is the half the router cannot
     *  know -- that the purge ran regardless, so the machine has kept the device for certain.
     */
    fun revokeUnsentNotice(routed: String): String = routed + STILL_REGISTERED

    /**
     * Which screen this phone gets.
     *
     * THE FACT ARRIVES AS A READER because reading it can THROW -- a state directory that will not
     * open, a core newer than this build -- and the interesting case is exactly the one a plain
     * parameter would have decided at the call site: a phone that cannot tell. Three call sites
     * each catch that separately today, and a value passed in would leave the decision testable at
     * none of them.
     *
     * WHAT IT READS IS WHETHER THIS PHONE IS PAIRED, AND IT USED TO READ THE MACHINE'S NAME. The
     * inference was that `mobile/pairing.go` clears the attempt record on `paired`, so the pinned
     * machine is the only trace a completed pairing leaves. The trace is real; the inference was
     * wrong. The machine endpoint id is a COORDINATE -- `phonecore.OpenStore` filters the durable
     * blob on it and every mutating verb signs over it -- so nothing clears it, including
     * [dev.swarm.phone.SettingsSurface]'s `Replace this computer`, which deregisters the device,
     * rotates the epoch, severs the gateway and destroys both key tiers. This gate answered
     * FULL_APP for a handset with no registration left, and the pairing entry point is inside the
     * app it was therefore shown: unpairable short of clearing the app's data
     * (agents-tracker-d0b8). `App.StateSummary.paired` states the fact instead of implying it.
     *
     * **"I CANNOT TELL" ANSWERS [Presentation.PAIR_ONLY], AND THE ASYMMETRY IS THE POINT.**
     * Guessing [Presentation.FULL_APP] lands a handset in a four-tab shell whose every screen
     * reads a roster it has no connection to fill -- an inbox announcing that nothing is waiting
     * on the user, which is a claim about a machine this phone is in no position to make. Guessing
     * PAIR_ONLY lands it on the one screen that can get it out of that state. The costs are not
     * symmetric, so the default is not a coin toss.
     *
     * @param pairedReader whether this phone is usably paired -- `stateSummary().paired`.
     */
    fun presentationOf(pairedReader: () -> Boolean): Presentation = try {
        if (pairedReader()) Presentation.FULL_APP else Presentation.PAIR_ONLY
    } catch (unreadable: Exception) {
        Presentation.PAIR_ONLY
    }
}
