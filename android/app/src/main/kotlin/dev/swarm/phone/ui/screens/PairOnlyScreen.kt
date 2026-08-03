package dev.swarm.phone.ui.screens

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
