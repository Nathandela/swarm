package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.CommandVerdict
import dev.swarm.phone.ui.LaunchRendering
import dev.swarm.phone.ui.LaunchResult

/**
 * Phase B slice S24 -- PB-DS-9: the LAUNCH form's screen model.
 *
 * IT HAS NO ENTRY IN THE SCREEN INVENTORY, and that is worth saying first rather than leaving a
 * reader to look for one. The eight screens the artifacts draw are the inbox, the session detail,
 * the terminal peek, machines, activity, settings, pairing and the approval sheet. Launching a
 * session is PB-APP-6's requirement and the mock never drew it, so every string below is the
 * PRODUCT'S OWN -- carried here verbatim from `PhoneSurface`, not re-worded, because re-wording
 * copy while moving it is how a move becomes a change nobody reviewed.
 *
 * WHAT MOVING IT BUYS. `launchNotice` was a private `when` in `PhoneSurface` over
 * [LaunchRendering], and the three sentences it chose between were `const val`s in the same file's
 * companion object. Nothing could reach either, so the one branch that matters -- a refusal the
 * machine says is worth retrying, which appends a sentence, against one it does not -- had no
 * test. It is a pure function over a data class and it is now in a file that is one.
 */
data class LaunchPanel(
    /** The section this form sits under. */
    val heading: String,
    /** The three fields [dev.swarm.phone.ui.LaunchDraft] carries, in the order they are asked. */
    val fields: List<LaunchField>,
    val submit: String,
    /**
     * The machine's answer to the launch this screen issued, or empty until one was issued.
     *
     * IT IS EMPTY AND NOT "nothing yet". A form that has launched nothing has not been refused and
     * has not succeeded; [dev.swarm.phone.ui.LaunchScreen] resolves an outcome for anybody else's
     * operation as PENDING precisely so a screen is never told something happened.
     */
    val notice: String,
    /**
     * The MACHINE'S own words under [notice], or empty where there are none
     * (agents-tracker-ksvb.10).
     *
     * THIS FORM HAD NO SENTENCE OF ITS OWN FOR A REFUSAL. `noticeFor` returned `rendering.reason`
     * and nothing else, so the whole notice under a refused launch was whatever string the daemon
     * sent, set in the form's own body type. The head is the screen's now and this is the wire's,
     * drawn through the kit's `noticeDetail` -- the `.sheet2 .ctx` cell, mono and tertiary.
     */
    val noticeDetail: String,
)

/** One field of the launch form. */
data class LaunchField(
    val id: LaunchFieldId,
    /**
     * The hint, which IS the label on this surface -- there are no XML layouts here, so a field
     * added without one is a box a user cannot identify.
     */
    val hint: String,
    /**
     * Whether [dev.swarm.phone.ui.LaunchScreen.missingField] refuses a draft without it.
     *
     * THE DAEMON HAS NO DEFAULT FOR EITHER REQUIRED FIELD, so a form that sent one anyway would be
     * inventing a value nobody chose. This is the model's own bar restated as data, not a second
     * one: `LaunchPanelScreenTest` checks the two agree.
     */
    val required: Boolean,
)

/** Which field. Named rather than positional, so a caller cannot pair a hint with the wrong box. */
enum class LaunchFieldId { AGENT, CWD, PROMPT }

object LaunchPanelScreen {

    private const val HEADING = "Launch"

    private const val SUBMIT = "Launch a session"

    /** The hints `PhoneSurface` already used, verbatim. */
    private val HINTS: Map<LaunchFieldId, String> = mapOf(
        LaunchFieldId.AGENT to "Which agent to start",
        LaunchFieldId.CWD to "Working directory on your computer",
        LaunchFieldId.PROMPT to "First message for the agent, if any",
    )

    /**
     * The two the daemon refuses a launch without.
     *
     * THE PROMPT IS NOT AMONG THEM AND IS STILL A FIELD. `LaunchDraft` models three; passing `""`
     * for the third would be a literal standing in for something nobody was asked.
     */
    private val REQUIRED: Set<LaunchFieldId> = setOf(LaunchFieldId.AGENT, LaunchFieldId.CWD)

    private val ORDER: List<LaunchFieldId> =
        listOf(LaunchFieldId.AGENT, LaunchFieldId.CWD, LaunchFieldId.PROMPT)

    /** PB-SYNC-2: an unresolved launch is neither a success nor a failure. */
    private const val PENDING = "Waiting for your computer to answer the launch."

    private const val ACCEPTED = "Your computer started the session."

    /**
     * What a refusal says, in the FORM'S own words (agents-tracker-ksvb.10).
     *
     * THIS FORM HAD NO SENTENCE FOR A REFUSAL AT ALL. [noticeFor] returned `rendering.reason` and
     * nothing else, so the whole notice under a refused launch was whatever string the daemon sent
     * -- set in this form's own body type, as though the product had written it.
     *
     * IT SAYS WHAT IS TRUE OF THE SESSION rather than naming the verb, which is
     * `SessionDetailScreen.KILL_REFUSED`'s rule one screen over: "launch failed" is a report about
     * a button, and "your machine did not start the session" is the fact the user acts on. It is
     * ONE sentence for all three refusal results, because the difference between them reaches the
     * user through the retry clause and through the machine's own words beneath, both of which say
     * more than a third wording of the same fact would.
     */
    private const val REFUSED = "Your computer did not start the session."

    /**
     * What a refusal waiting would fix says about itself.
     *
     * IT IS READ FROM [CommandVerdict] RATHER THAN SPELLED AGAIN (agents-tracker-qlf9). This
     * screen wrote the sentence first and it is not this screen's: kill, take_control and revoke
     * append the same words to their own refusals, and it is a property of the ANSWER rather than
     * of any one panel. Two copies of one piece of copy is the drift PB-DS-9 assigns copy to a
     * single place to prevent; the value is unchanged to the character.
     */
    private const val RETRYABLE = CommandVerdict.RETRY_HINT

    fun hintFor(field: LaunchFieldId): String = checkNotNull(HINTS[field]) {
        "PB-DS-9: no hint for $field. The hint is this surface's only label, so a field without " +
            "one is a box a user cannot identify."
    }

    fun isRequired(field: LaunchFieldId): Boolean = field in REQUIRED

    /**
     * The machine's answer in a sentence -- the form's own, and the whole of the primary copy.
     *
     * THE `when` IS EXHAUSTIVE so a result added later has to state its own wording rather than
     * inheriting one, and `retryable` is the model's own distinction between a refusal that
     * waiting fixes and one it does not. The machine's own words are [detailFor]: the user's next
     * step still depends on which refusal it was, and a kill-switch refusal told to the user as
     * "against policy" sends them to change a spec that was fine -- so the words are demoted to
     * their own register rather than dropped (agents-tracker-ksvb.10).
     */
    fun noticeFor(rendering: LaunchRendering): String = when (rendering.result) {
        LaunchResult.PENDING -> PENDING
        LaunchResult.LAUNCHED -> ACCEPTED
        LaunchResult.REJECTED_BY_POLICY,
        LaunchResult.REFUSED_TRANSIENTLY,
        LaunchResult.REFUSED,
        -> if (rendering.retryable) REFUSED + RETRYABLE else REFUSED
    }

    /**
     * The machine's OWN words about the refusal, for the detail cell beside the sentence
     * (agents-tracker-ksvb.10).
     *
     * ONLY A REFUSAL HAS ONE. [PENDING] and [ACCEPTED] are this screen's own statements about an
     * operation nobody needs diagnosed, and `rendering.reason` on an accepted launch is the wire's
     * `ok` -- a mono line reading `ok` under "your machine started the session" is a diagnostic
     * about nothing.
     *
     * THE RETRY CLAUSE STAYS ON THE HEAD, where [noticeFor] appends it: it is copy this product
     * wrote about what to do next, and the detail is what the machine said.
     */
    fun detailFor(rendering: LaunchRendering): String = when (rendering.result) {
        LaunchResult.PENDING, LaunchResult.LAUNCHED -> ""
        LaunchResult.REJECTED_BY_POLICY,
        LaunchResult.REFUSED_TRANSIENTLY,
        LaunchResult.REFUSED,
        -> rendering.reason
    }

    /**
     * @param rendering the machine's answer to the launch this screen issued, or null while it has
     *  issued none. Null is not PENDING: a form nobody has submitted is not waiting for anything,
     *  and saying it is would be a status about an operation that does not exist.
     */
    fun of(rendering: LaunchRendering? = null): LaunchPanel = LaunchPanel(
        heading = HEADING,
        fields = ORDER.map { field ->
            LaunchField(id = field, hint = hintFor(field), required = isRequired(field))
        },
        submit = SUBMIT,
        notice = rendering?.let { noticeFor(it) }.orEmpty(),
        noticeDetail = rendering?.let { detailFor(it) }.orEmpty(),
    )
}
