package dev.swarm.phone.ui

import java.util.UUID

/**
 * Phase B slice S16 -- PB-APP-5 (machine pane) and PB-APP-6 (launch).
 *
 * WHAT THIS FILE NO LONGER MODELS (ADR-007 B133). It carried PB-SEC-2's freshness table -- the
 * set of gated actions, the two freshness tiers, the events that invalidated an outstanding
 * authentication, and a grant scoped to the action it was obtained for. PB-SEC-2 is VOID: the
 * trust boundary is the wire, and there is no local authentication on this handset for a table
 * to describe. The types are deleted rather than left answering NONE for everything, because a
 * freshness model that always says "none" reads as coverage.
 *
 * PB-APP-5's own criterion narrows with it. "Revoke + kill switch gated per PB-SEC-2" loses its
 * second half; what survives is that the phone SHOWS the kill switch and can never set it, which
 * is a daemon-side refusal and is unaffected -- see [MachinePane].
 */

/**
 * PB-APP-5's machine pane: presence, the paired device, the activity log and the kill switch.
 *
 * THE KILL SWITCH IS READ-ONLY, and that is a security property rather than a simplification.
 * protocol/server.go handleRemoteSetControl refuses the remote tier BEFORE consulting its
 * backend -- a remote device must never re-enable a switch its owner turned off -- so a control
 * here would be advertising a bypass of a daemon gate (PB-SEC-6). The phone SHOWS the state so
 * a kill_switch refusal is legible, and offers the only panic action a phone legitimately has:
 * revoking itself.
 */
object MachineLabel {

    /**
     * What to CALL a machine, decided in ONE place (agents-tracker-ksvb.1).
     *
     * THE NAME IS A WIRE FACT AND THE ID IS THE FALLBACK, never the other way round.
     * `swarmmobile.App.MachineName` returns the hostname the machine published in its pairing
     * payload; the endpoint id is `ep-` plus four bytes of a hash of a state directory, which is
     * the string this product used to put in front of people because nothing carried the name.
     *
     * AN EMPTY NAME MEANS THE MACHINE PUBLISHED NONE and the id renders -- which is a fact, is
     * unique, and is exactly what shipped before. Nothing here invents a word for the gap:
     * ADR-007 B135, and `Unnamed`/`Unknown` on a machine row would be indistinguishable from a
     * machine actually called that.
     *
     * IT IS ONE FUNCTION RATHER THAN AN `ifEmpty` AT EACH SITE because there are seven sites --
     * the machine row, the inbox scope chips, the settings pairing row and its destructive
     * Replace confirmation among them -- and the one that got written the other way round would
     * show a raw id beside six names with nothing to fail. `TriageInboxScreen` records the same
     * lesson about `needs_input` written out three times before it was named once.
     */
    fun of(name: String, endpointId: String): String = name.ifEmpty { endpointId }
}

data class MachinePane(
    val machineId: String,
    /**
     * The machine's own name, verbatim from `swarmmobile.App.MachineName`, or empty where it
     * published none. [MachineLabel.of] is what turns the pair into what a person reads; this
     * field never renders on its own.
     *
     * IT IS DEFAULTED WHERE [dev.swarm.phone.ui.SessionRow.agent] IS NOT, and the difference is
     * what an omission COSTS. Forgetting the agent renders exactly what a machine reporting none
     * renders -- nothing -- so the two are indistinguishable on screen. Forgetting this renders
     * the endpoint id, which is what every one of these screens rendered before this field
     * existed: visible, honest, and the thing the bead is about, so an unpopulated site announces
     * itself the moment anyone looks at the screen.
     */
    val machineName: String = "",
    /**
     * `App.Presence`, verbatim -- and it is the RELAY'S OPINION, never evidence about the
     * machine (PB-APP-11). It must be rendered with [freshness] beside it, which is why that
     * is a required parameter of this pane rather than a screen's option.
     */
    val presence: String,
    /**
     * `App.MachineFreshness` -- the phone's OWN evidence: how long since the machine's newest
     * authenticated word. A relay that withholds every frame while answering every poll leaves
     * presence reading "online" and this reading silent, which is the whole of ADR-007 B121.
     */
    val freshness: MachineFreshness,
    val pairedDeviceName: String,
    /** True when the owner has turned remote control OFF at the machine. */
    val killSwitchEngaged: Boolean,
    val activity: List<JournalRow>,
) {
    val canSetKillSwitch: Boolean = false

    val canRevokeThisDevice: Boolean = true

    /**
     * What the pane says about reachability, and it refuses to let the relay's word stand
     * alone: while the machine is silent past section 6.0's budget, the presence line is
     * qualified by what this phone can actually vouch for.
     *
     * A MACHINE INSIDE THE BUDGET RENDERS NOTHING (agents-tracker-ksvb.6). It used to print
     * "Your machine is $presence." unconditionally, restating what the presence dot beside it
     * already says in colour, on every visit -- the same always-on sentence this app's
     * conditional-notice discipline exists to refuse everywhere else. [presenceAnnouncement] is
     * where the fact goes for a reader this line no longer speaks to.
     */
    fun presenceExplanation(formatTime: (Long) -> String): String =
        explanationOf(presence, freshness, formatTime)

    /**
     * The sentence [presenceExplanation] no longer prints for a healthy machine, said once more
     * for a screen reader.
     *
     * IT IS NOT DELETED WITH THE LINE, because the presence dot's colour is the only thing left
     * on screen once the sentence is gone, and a mark with no words of its own is exactly the
     * accessibility gap a silent notice must not open. Read only where [presenceExplanation]
     * renders nothing -- see the row that carries both.
     */
    val presenceAnnouncement: String
        get() = announcementOf(presence)

    val killSwitchExplanation: String
        get() = if (killSwitchEngaged) {
            "Remote control is switched off at your machine, so it will refuse anything this " +
                "phone asks it to change. Only the machine's owner can switch it back on."
        } else {
            // ON IS ONE SHORT LINE, AND SAYS NO MORE (agents-tracker-ksvb.6). It used to spend a
            // second sentence on the same words the OFF branch above needs for its own reason --
            // that only the machine can move the switch -- rendered on every visit to a screen
            // that is, in this state, reporting nothing wrong.
            "Remote control is on. Only the machine can switch it off."
        }

    /**
     * The two sentences this pane owns, reachable WITHOUT one (agents-tracker-nx44.3).
     *
     * WHY THEY MOVED HERE AND WHY THE PANE STILL SPENDS THEM. The Machines destination is deleted
     * and the settings screen's CONNECTION section renders presence now -- from `App.MachinePresence`
     * and `App.MachineFreshness`, and from neither a paired-device name nor a kill-switch state,
     * which are the pane's other required fields. A section that constructed a whole [MachinePane]
     * to reach one sentence would have to invent values for the fields it does not render, which is
     * ADR-007 B135's defect class arriving through the back door; a section that re-worded the
     * sentence would be the second copy of a measurement's meaning that PB-APP-11 exists to
     * prevent, and the two would disagree the first time either moved.
     *
     * SO THE SENTENCE IS THE PANE'S AND THE PANE IS NOT REQUIRED TO SAY IT. Both members above
     * delegate here, so there is exactly one wording and every caller reads it.
     */
    companion object {

        /**
         * What a phone may say about reachability given the relay's word and its OWN evidence.
         *
         * EMPTY IS A HEALTHY MACHINE (agents-tracker-ksvb.6): inside section 6.0's freshness
         * budget there is nothing to report, and an unconditional sentence restating what the
         * presence dot already says in colour is the always-on notice this app refuses everywhere
         * else. [announcementOf] is where the fact goes for a reader the silence excludes.
         *
         * @param presence `App.MachinePresence`'s state, verbatim -- the RELAY's opinion.
         * @param freshness the phone's own evidence, which is what decides whether the relay's
         *  word is allowed to stand alone.
         * @param formatTime an Android formatter carrying the user's locale and time zone, passed
         *  through so this stays testable without one.
         */
        fun explanationOf(
            presence: String,
            freshness: MachineFreshness,
            formatTime: (Long) -> String,
        ): String = freshness.notice(formatTime)?.let { notice ->
            "$notice The relay reports \"$presence\", which is the relay's word and not your " +
                "machine's."
        } ?: ""

        /** What the presence mark announces where [explanationOf] has printed nothing. */
        fun announcementOf(presence: String): String = "Your machine is $presence."
    }
}

/** PB-APP-6's v1 builder: the three fields a launch spec carries from the handset. */
data class LaunchDraft(
    val agent: String,
    val cwd: String,
    val prompt: String,
)

/** One issued operation, keyed the way PB-SYNC-2 keys outcomes. */
data class LaunchOperation(
    val action: String,
    val operationId: String,
)

/** `swarmmobile.Outcome`, as the screen takes it. An empty [code] is an unresolved operation. */
data class OperationOutcome(
    val operationId: String,
    val code: String,
    val message: String,
)

/** What the launch screen shows for an operation. */
enum class LaunchResult {
    /** No answer yet, and saying anything else would be a guess. */
    PENDING,

    LAUNCHED,

    /** schema.CodePolicy. The machine's considered answer; retrying changes nothing. */
    REJECTED_BY_POLICY,

    /** schema.CodeRateLimit. Retryable, but only after waiting. */
    REFUSED_TRANSIENTLY,

    /** Every other refusal the machine can seal: kill switch, authorization, a bad field. */
    REFUSED,
}

/** A resolved (or still unresolved) launch, with the machine's own reason attached. */
data class LaunchRendering(
    val result: LaunchResult,
    /** The machine's message, verbatim. Empty only while [LaunchResult.PENDING]. */
    val reason: String,
    val retryable: Boolean,
)

/**
 * PB-APP-6's screen.
 *
 * IT REMEMBERS THE OPERATION ID IT ISSUED, because PB-SYNC-2 keys outcomes by operation id
 * precisely so that a screen never resolves one by proximity. An outcome for someone else's
 * operation leaves this one pending, which is the honest answer: an unresolved launch is
 * neither a success nor a failure, and claiming either is worse than saying nothing.
 */
class LaunchScreen {

    var inFlight: LaunchOperation? = null
        private set

    /**
     * The required field [draft] is missing, in the words a user reads, or null when it carries
     * both. The daemon has no default for either -- it refuses a launch without an agent and
     * without a working directory -- so a form that sent one anyway would be inventing a value
     * nobody chose, which is the defect `leaseHeld = false` already cost this project once.
     *
     * IT IS ONE STATEMENT OF THE RULE AND IT IS WHY THIS IS PUBLIC. [submit] refuses on it and
     * the screen asks on it, so a surface cannot enforce a different bar from the model's -- and
     * a screen that discovered the refusal by catching submit's own exception would report a
     * programming error to a user who simply left a field empty.
     */
    fun missingField(draft: LaunchDraft): String? = when {
        draft.agent.isBlank() -> "Say which agent to start."
        draft.cwd.isBlank() -> "Say which folder on your machine it starts in."
        else -> null
    }

    /**
     * @param operationId the id the MACHINE will key the outcome by --
     *  `swarmmobile.Op.getOperationID()`, which `App.Launch` returns synchronously even for an
     *  operation it queued. The default exists so the screen's policy can be exercised without
     *  a facade; production passes the core's id, and a locally minted one would never match
     *  the outcome that comes back.
     */
    fun submit(
        draft: LaunchDraft,
        operationId: String = UUID.randomUUID().toString(),
    ): LaunchOperation {
        val missing = missingField(draft)
        require(missing == null) { "PB-APP-6: $missing" }
        val op = LaunchOperation(action = ACTION_LAUNCH, operationId = operationId)
        inFlight = op
        return op
    }

    /**
     * THE CODE TABLE IS [CommandVerdict]'S NOW AND IS NOT COPIED HERE (agents-tracker-qlf9). It
     * was this function's, and it was right -- a policy rejection apart from every other refusal
     * because retrying changes nothing, a rate limit apart from both because waiting is what
     * helps, and the machine's own words carried through all of them. Kill, take_control and
     * revoke needed the same reading and had none; a second table beside this one is two readings
     * of a taxonomy that belongs to the machine, and the failure would be silent on whichever of
     * them nobody revisited.
     *
     * A success carries the reply op rather than an error code (`mobile/app.go`'s `outcomeOf`
     * falls back to `Control.Op` when `ErrorCode` is empty), and the reply op for an accepted
     * command is `protocol.OpOK`.
     */
    fun resolve(outcome: OperationOutcome): LaunchRendering {
        val verdict = CommandVerdict.of(
            outcome,
            operationId = inFlight?.operationId.orEmpty(),
            accepted = CommandVerdict.ACCEPTED_OK,
        )
        return LaunchRendering(
            result = when (verdict.result) {
                CommandResult.PENDING -> LaunchResult.PENDING
                CommandResult.ACCEPTED -> LaunchResult.LAUNCHED
                CommandResult.REJECTED_BY_POLICY -> LaunchResult.REJECTED_BY_POLICY
                CommandResult.REFUSED_TRANSIENTLY -> LaunchResult.REFUSED_TRANSIENTLY
                // A LAUNCH HAS NO GRANT TO LOSE, so `protocol.OpDetach` cannot land on one: the
                // severance notice is sealed under a take_control's operation id and nothing else
                // issues one. It is folded here rather than given a launch state that no reply can
                // produce, because a dead branch's wording is trusted by the next reader.
                CommandResult.ENDED, CommandResult.REFUSED -> LaunchResult.REFUSED
            },
            reason = verdict.reason,
            retryable = verdict.retryable,
        )
    }

    private companion object {
        /** schema.ActionLaunch. */
        const val ACTION_LAUNCH = "launch"
    }
}
