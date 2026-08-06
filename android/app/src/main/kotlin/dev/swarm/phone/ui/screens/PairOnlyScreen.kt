package dev.swarm.phone.ui.screens

import dev.swarm.phone.keys.ConnectionState
import dev.swarm.phone.ui.CommandVerdict
import dev.swarm.phone.ui.ConnectionBanner

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
 * WHY this phone is on the pairing screen, which is the half [Presentation] does not carry
 * (agents-tracker-w6o3).
 *
 * THE SCREEN IS THE SAME SCREEN AND THE WORDS ARE NOT. `PairOnlyScreen.presentationOf` reads one
 * fact -- is this handset usably paired -- and every way of being unpaired answers PAIR_ONLY,
 * which is right and is where the last fix stopped. What it left is a phone whose owner ran
 * `swarm remote revoke` opening on the screen a FRESH INSTALL opens on: "Pair this phone", one
 * control, no statement of what happened. The two facts most worth telling a person are the two
 * this enum names, and the banners that used to carry them are unreachable on a phone the
 * transport has just unpaired -- `mobile/relay.go`'s `transportEndsPairing` folds both into
 * `paired = false`, and the pair-only gate returns before any banner is read.
 *
 * IT IS EXACTLY THE TWO STATES THAT END A PAIRING, and the boundary is Go's rather than this
 * file's. `RELAY_UNTRUSTED` and `RELAY_INSECURE` are terminal and do NOT end a pairing -- the
 * first arrives on the ordinary first pairing (ADR-007 B58) -- so a phone in them stays in the app
 * with the banner that names them, and a row here would be a screen nothing can reach.
 */
enum class PairOnlyReason {
    /**
     * No machine has ever been pinned here -- or nothing on this handset can say why one is not.
     *
     * IT IS ALSO THE ANSWER TO "I CANNOT TELL", for [PairOnlyScreen.presentationOf]'s asymmetry
     * read in the other direction: guessing a terminal reason would tell a user their owner
     * removed this device on the strength of a read that failed, which is a claim about the
     * MACHINE that a phone which cannot read its own transport is in no position to make.
     */
    FIRST_RUN,

    /** The owner removed this device. The registration is the machine's to clear. */
    REVOKED,

    /**
     * This handset's relay-auth key is gone, and the machine still has the device registered.
     *
     * THE ORDER OF THE REMEDY IS THE WHOLE POINT of this row: `swarm remote pair` is refused
     * while a device is registered (PB-STATE-10, single-device v1), so a phone offered a bare
     * pairing control here walks into a refusal with the reason on no screen at all.
     */
    REPAIR_REQUIRED,
}

/** The three sentences one draw of [pairOnlyView] puts on screen. */
data class PairOnlyCopy(val title: String, val body: String, val cta: String)

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
     * The two commands that clear a machine-side registration, spelled ONCE.
     *
     * Every sentence on this screen that sends a user to their machine sends them to the same two
     * verbs -- find the device, unregister it -- and [dev.swarm.phone.ui.ErrorRouter] spells them
     * this way for `swarm/state-corrupt`, which is the same recovery. Three copies of a command
     * line is three things to keep in agreement, and a user who reads two of them wonders which is
     * the real one.
     */
    private const val UNREGISTER_FIRST = "run `swarm remote devices` on your machine to find " +
        "this device and `swarm remote revoke <device-id>` to unregister it"

    /** What a phone the owner removed is, stated as the fact and not as the remedy. */
    const val TITLE_REVOKED = "This phone was removed"

    /** What a phone whose relay-auth key is gone is. */
    const val TITLE_REPAIR_REQUIRED = "This phone's key is gone"

    /**
     * The repair_required cause AND the order its remedy has to be carried out in
     * (agents-tracker-w6o3).
     *
     * IT IS NOT `ConnectionBanner`'S SENTENCE, and that is the defect rather than a wording
     * preference: the banner ends "Pair this device again", which is advice this screen cannot
     * offer on its own terms. Nothing removed the machine-side registration here -- the key died
     * on the HANDSET -- so `swarm remote pair` is refused until the owner clears it (PB-STATE-10),
     * and a user who presses the control before that has been told nothing walks into the failure
     * LOOP PB-APP-10 forbids, reached through the remedy.
     *
     * THE CONTROL STAYS ANYWAY, and the argument is the mirror of the one above. Nothing on this
     * device can un-destroy a Keystore key, so this state never clears on its own; a screen that
     * withheld the pairing control until it did would withhold it forever, while the recovery
     * PB-STATE-10 documents ENDS with a pairing on this handset. What the requirement forbids is a
     * BARE control -- one offered with no statement of the step that has to come first.
     */
    private const val REPAIR_REQUIRED_CAUSE = "This phone's key was destroyed and cannot be " +
        "recovered. Your machine still has this device registered and `swarm remote pair` is " +
        "refused until that is cleared, so " + UNREGISTER_FIRST + " before pairing this phone " +
        "again."

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
        "confirmed that it removed the device. If pairing is refused, " + UNREGISTER_FIRST +
        " first."

    /** The head of the refused sentence; the machine's own reason follows it. */
    private const val REVOKE_REFUSED = "Your machine refused to remove this device"

    /**
     * What is true of the handset once the revoke did not land, in either way it can fail to.
     *
     * IT IS ONE TAIL FOR BOTH because the fact is one fact: the purge ran, so the phone is
     * unpaired and the machine is not.
     */
    private const val STILL_REGISTERED = " This phone has unpaired itself anyway, so " +
        UNREGISTER_FIRST + " before pairing again."

    /**
     * What a purge that could not finish leaves behind, stated as the fact it is
     * (agents-tracker-jx23).
     *
     * IT IS A DIFFERENT FACT FROM EVERY OTHER SENTENCE ON THIS SCREEN, which is why it is its own
     * constant rather than another tail on [STILL_REGISTERED]. Those are about the REGISTRATION --
     * whether a computer somewhere still knows this device -- and this one is about the sealed
     * containers in the hand of the person reading it. `App.PurgeKeys` states the boundary
     * exactly: an error means the material AT REST survived, while the memory half happened
     * regardless. So the live keys ARE gone and the phone is unpaired; what did not happen is the
     * destruction of the blobs, and only a person who knows that can decide what to do about the
     * handset.
     *
     * THE REMEDY IS NOT NAMED, and that is deliberate. There is nothing to type: the failure is a
     * full disk or a data directory gone read-only, and the app cannot tell which. Inventing a
     * step here -- "clear the app's data" -- would send a user to destroy the durable state of a
     * pairing they may be about to re-make, on a guess about a platform condition this screen
     * cannot read.
     */
    private const val PURGE_INCOMPLETE = "This phone could not destroy the key material it had " +
        "stored, so it is still on this device."

    /**
     * The machine's answer to the revoke this phone issued, as the screen states it.
     *
     * A CONFIRMED REMOVAL SAYS NOTHING. Both sides agree, so there is no divergence to report, and
     * a warning shown over a state that is fine teaches the user to ignore the one that is not.
     * That rule is why the second fact is a PARAMETER here rather than a sentence a caller appends:
     * "say nothing" has to survive the join, and a caller that concatenated two composed strings
     * would put a lone separator on the screen of a phone where everything went right.
     *
     * THE ORDER IS A COPY DECISION AND IS MADE HERE (agents-tracker-jx23). The machine's answer
     * comes first because the user's next action turns on it -- whether `swarm remote pair` will
     * be refused -- and the purge failure is a fact about this handset that no command of theirs
     * undoes. Leading with the second buries the actionable one.
     *
     * @param purgeFailure PB-APP-9's routed reason the key material at rest survived, or empty
     *  where the purge finished. It is carried VERBATIM for [revokeUnsentNotice]'s reason: the
     *  router owns what a custody failure reads as, and a second wording here is two files
     *  deciding one sentence.
     */
    fun revokeNoticeFor(verdict: CommandVerdict, purgeFailure: String = ""): String {
        val machine = when {
            verdict.accepted -> ""
            verdict.answered -> verdict.sentence(REVOKE_REFUSED) + STILL_REGISTERED
            else -> REVOKE_UNCONFIRMED
        }
        return joinedWithPurgeFailure(machine, purgeFailure)
    }

    /**
     * The two facts as one notice, in the one place that knows how they read together.
     *
     * BOTH CAN BE TRUE AT ONCE and the worst case is exactly that: a machine that refused the
     * removal AND a handset that could not destroy what it held. Neither answers for the other, so
     * neither replaces the other.
     */
    private fun joinedWithPurgeFailure(machine: String, purgeFailure: String): String = when {
        purgeFailure.isBlank() -> machine
        machine.isEmpty() -> purgeFailure + " " + PURGE_INCOMPLETE
        else -> machine + " " + purgeFailure + " " + PURGE_INCOMPLETE
    }

    /**
     * The same join for the revoke that never reached the wire, so the panel composing the
     * fallback has one call for either shape (agents-tracker-jx23).
     */
    fun revokeUnsentNotice(routed: String, purgeFailure: String): String =
        joinedWithPurgeFailure(revokeUnsentNotice(routed), purgeFailure)

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

    /**
     * WHY this phone is here, asked of the transport once [presentationOf] has said that it is
     * (agents-tracker-w6o3).
     *
     * IT IS A SECOND QUESTION AND NOT A SECOND GATE, which is the line the d0b8 fence under
     * `android/gate` draws and this must not cross: whether a handset is USABLY paired is ONE fact,
     * assembled in Go over the durable unpair and the live transport together, and a call site
     * that rebuilt it from the connection state would hold a second opinion to keep in step with
     * the first. This asks nothing about that verdict. It asks what the screen the verdict already
     * chose should SAY.
     *
     * THE READER IS A READER for [presentationOf]'s reason, twice over: `App.ConnectionState`
     * fails through the same `ready()` check as every other verb, and `ConnectionState.of` errors
     * on a wire string this build does not know -- deliberately, so a state the facade starts
     * reporting is a loud failure rather than a screen that silently renders nothing. Neither is a
     * reason to show a user a cause.
     *
     * WHAT IT CANNOT SEE, stated because a reader will otherwise assume it does: the connection
     * state is PROCESS MEMORY. `recordUnpaired` writes down THAT the registration ended and the
     * summary carries no reason, so a handset SIGKILLed after a revoke and reopened out of signal
     * reads FIRST_RUN -- the honest answer for what is then knowable on the device, and the reason
     * `mobile/relay.go` gives for keeping the live reading at all rather than a durable verdict a
     * recovery would make stale.
     *
     * @param stateReader the transport's current opinion --
     *  `ConnectionState.of(connectionState())`.
     */
    fun reasonFor(stateReader: () -> ConnectionState): PairOnlyReason = try {
        when (stateReader()) {
            ConnectionState.REVOKED -> PairOnlyReason.REVOKED
            ConnectionState.REPAIR_REQUIRED -> PairOnlyReason.REPAIR_REQUIRED
            else -> PairOnlyReason.FIRST_RUN
        }
    } catch (unreadable: Exception) {
        PairOnlyReason.FIRST_RUN
    }

    /**
     * What the screen says for one reason.
     *
     * THE REVOKED SENTENCE IS THE BANNER'S OWN, taken rather than re-typed. `ConnectionUi` argues
     * that wording -- the owner removed this device, and the machine-side registration has to be
     * cleared before a re-pair can succeed -- and it is the ONLY place in this product that states
     * it. Since `transportEndsPairing` folds a past-grace revoke into `paired = false`, the banner
     * carrying it is unreachable on exactly the handset it describes; the sentence therefore moves
     * to the screen that handset lands on, and moving it as a reference is what stops the two
     * drifting into two answers.
     *
     * [BODY] FOLLOWS BOTH CAUSES rather than being replaced by them, because it answers a
     * different question -- why the app is empty -- and that question is sharper for a phone that
     * had a machine an hour ago than for one that never did.
     *
     * [CTA] IS THE SAME CONTROL IN EVERY ROW. It is inventory C7's own name for the flow it opens,
     * and the flow is the same flow: the recovery ends with `swarm remote pair` showing a code and
     * this phone scanning it. See [REPAIR_REQUIRED_CAUSE] for why it is not withheld in the state
     * that cannot clear itself.
     */
    fun copyFor(reason: PairOnlyReason): PairOnlyCopy = when (reason) {
        PairOnlyReason.FIRST_RUN -> PairOnlyCopy(TITLE, BODY, CTA)

        PairOnlyReason.REVOKED -> PairOnlyCopy(
            TITLE_REVOKED,
            ConnectionBanner.of(ConnectionState.REVOKED).text + " " + BODY,
            CTA,
        )

        PairOnlyReason.REPAIR_REQUIRED -> PairOnlyCopy(
            TITLE_REPAIR_REQUIRED,
            REPAIR_REQUIRED_CAUSE + " " + BODY,
            CTA,
        )
    }
}
