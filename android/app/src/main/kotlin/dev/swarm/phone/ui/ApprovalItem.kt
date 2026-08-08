package dev.swarm.phone.ui

import org.json.JSONObject

/**
 * ADR-009-structured-chat-interaction (1)/(4) -- §3.5's `approval_request`, as this app reads it.
 *
 * WHY A DECODE EXISTS AT ALL. `mobile/types.go` binds an item's body as a RAW JSON STRING, and
 * that is a gomobile constraint stated on the type itself: "there is no bound map and no variant
 * type, the same limit that makes Snapshot.Text a joined string rather than a []string ... which
 * is what makes an unknown kind or an unknown field free on this boundary (IS-COMPAT-1/-2)". So
 * the per-kind fields are decoded client-side, and this is that side.
 *
 * IT LIVES IN `ui/` AND NOT IN A SCREEN, which is where every other wire-to-screen model in this
 * app already lives. PB-DS-9 gives a screen its copy and its arrangement; a screen that parsed
 * JSON would be a composition that also owns a wire format, and `android/gate/s24_screens_test.go`
 * fences that package to component calls plus layout for the same reason.
 *
 * WHAT IT DELIBERATELY DOES NOT CARRY: a polarity. IS-APR-4 keeps the decision's verdict --
 * `allow` | `deny` | `other` -- MACHINE-SIDE and off the item: "the card labels its buttons from
 * `decisions[].label` and no phone surface switches on polarity, so putting it on the wire would
 * only create a second place for the two to disagree". `internal/skeleton/interaction_chain_e2e_
 * test.go` fails the build if one ever appears beside `id`/`label`, and this file is the phone-side
 * twin of that fence: there is nothing here to read a polarity out of, so no screen below can paint
 * one. The daemon classifies §3.6's `allowed`/`denied` from the verdict it kept; the phone sends
 * back the id of the button that was tapped and nothing more (IS-LIFE-4).
 */
data class ApprovalItem(
    /** The session the card interrupts. `App.Approve` takes it beside the item id. */
    val sessionId: String,
    /**
     * §3.5: the item's own `item_id`, which **is** ADR-007 D7's `interaction_id` (IS-APR-1).
     *
     * IT IS THE ONLY HANDLE A TAP NEEDS. The binding tuple -- the agent instance, the content hash
     * and the daemon's expiry -- is NOT carried here and must not be: IS-APR-2 makes the phone echo
     * `content_hash` and `expires_at` verbatim and compute neither, and `App.Approve` reads them off
     * the item this phone is already holding. A model that carried them is a model a screen could
     * pass, and a screen that can pass them is one that could compute them.
     */
    val itemId: String,
    /** §3.5's `summary`: the adapter's one-line headline. This is the blocking question. */
    val summary: String,
    /**
     * §7's `action`, rendered down to the one literal a card shows.
     *
     * The classification is the MACHINE's (IS-TOOL-1: "a phone SHALL NOT parse `tool` or raw
     * arguments to infer an action"), so this reads the field the adapter already chose --
     * `command` for an execute, `path` for a file-scoped call, `query` for a search -- and never
     * reconstructs one. Empty when the action names none, and the sheet then draws no well.
     */
    val command: String,
    /** §3.5's `decisions[]`, in the order the adapter offered them. `{id, label}` and nothing else. */
    val decisions: List<ApprovalDecision>,
    /**
     * IS-LIFE-6's fallback mode: `prompt_card` where the hook cannot resolve the approval.
     *
     * IT CHANGES WHAT THE CARD IS MADE OF AND NOT WHAT IT IS. ADR-009 (4) is explicit that the
     * fallback is "a card, never a grid": the sanitized prompt region arrives as `prompt_lines`
     * rather than as an `action`, so [command] is those lines, and the buttons still carry the same
     * signed `ActionApprove` every other card's do. There is no second screen.
     */
    val promptCard: Boolean,
) {
    companion object {

        /**
         * Decode one `approval_request` body, or null when it is not one this build can render.
         *
         * NULL IS A SKIP AND NOT AN ERROR, which is IS-COMPAT-1's rule read at the client: an item
         * this side cannot make a card out of costs the transcript nothing but the space it does
         * not take. A malformed body is the same case -- `org.json` throws on one, and a card
         * fabricated from what parsed would be a question with an unknown half.
         */
        fun of(sessionId: String, itemId: String, body: String): ApprovalItem? = try {
            decode(sessionId, itemId, JSONObject(body))
        } catch (malformed: Exception) {
            null
        }

        private fun decode(sessionId: String, itemId: String, item: JSONObject): ApprovalItem? {
            val decisions = decisionsOf(item.optJSONArray(DECISIONS))
            // A CARD WITH NO BUTTONS CANNOT BE ANSWERED, and the machine side already refuses to
            // emit one (`internal/adapter/interaction.go`: "an adapter that cannot enumerate the
            // CLI's decisions emits no approval at all"). Drawing it anyway would be a sheet whose
            // only honest action is to be dismissed.
            if (decisions.isEmpty()) return null
            val promptCard = item.optString(MODE) == PROMPT_CARD
            return ApprovalItem(
                sessionId = sessionId,
                itemId = itemId,
                summary = item.optString(SUMMARY),
                command = if (promptCard) promptOf(item) else actionOf(item.optJSONObject(ACTION)),
                decisions = decisions,
                promptCard = promptCard,
            )
        }

        /** §3.5: `{id, label}`. A decision missing either is dropped -- see [ApprovalDecision]. */
        private fun decisionsOf(array: org.json.JSONArray?): List<ApprovalDecision> {
            if (array == null) return emptyList()
            val out = mutableListOf<ApprovalDecision>()
            for (i in 0 until array.length()) {
                val decision = array.optJSONObject(i) ?: continue
                val id = decision.optString(ID)
                val label = decision.optString(LABEL)
                if (id.isEmpty() || label.isEmpty()) continue
                out.add(ApprovalDecision(id = id, label = label))
            }
            return out
        }

        /**
         * §7's four fields, in the order a card reads them.
         *
         * THE ORDER IS NOT A GUESS: §7 gives each `type` exactly one filled field -- `command` for
         * `execute`, `path` for `read`/`edit`/`write`, `query` for `search` -- so the first
         * non-empty one IS the action's literal. Nothing is composed out of two of them, because
         * that would be this side deciding what the tool did, which IS-TOOL-1 puts on the machine.
         */
        private fun actionOf(action: JSONObject?): String {
            if (action == null) return ""
            for (field in listOf(COMMAND, PATH, QUERY)) {
                val value = action.optString(field)
                if (value.isNotEmpty()) return value
            }
            return ""
        }

        /**
         * IS-APR-3's `prompt_lines`, joined for the one well that prints them.
         *
         * THEY ARE ALREADY SANITIZED, by the same machine-side path as a terminal snapshot, and
         * they are TEXT: "no cursor, no styling, no addressability". Joining with newlines is the
         * whole of the rendering, and it is the same shape `swarmmobile.Snapshot.Text` arrived in.
         */
        private fun promptOf(item: JSONObject): String {
            val lines = item.optJSONArray(PROMPT_LINES) ?: return ""
            return (0 until lines.length()).joinToString(separator = "\n") { lines.optString(it) }
        }

        // §3.5 and §7's key names, spelled once. They are the WIRE's, so they are snake_case and
        // are not translated on the way in.
        private const val SUMMARY = "summary"
        private const val ACTION = "action"
        private const val DECISIONS = "decisions"
        private const val ID = "id"
        private const val LABEL = "label"
        private const val MODE = "mode"
        private const val PROMPT_CARD = "prompt_card"
        private const val PROMPT_LINES = "prompt_lines"
        private const val COMMAND = "command"
        private const val PATH = "path"
        private const val QUERY = "query"
    }
}

/**
 * One offered decision: the CLI's own id, and the label the card puts on the button.
 *
 * THE ID IS NOT NORMALIZED AND MUST NOT BE. §3.5 keeps the ids the CLI's own -- Codex offers
 * `accept` | `acceptWithExecpolicyAmendment` | `cancel`, Claude Code a numbered dialog -- and
 * `mobile/commands.go`'s `Approve` says why the phone hands one back untouched: "a daemon reading
 * `cancel` as a refusal would be guessing at a vocabulary it does not own".
 *
 * A DECISION WITH NO LABEL IS DROPPED RATHER THAN LABELLED HERE. IS-APR-3 says the card labels its
 * buttons from `decisions[].label`; the only string this side could put on an unlabelled one is the
 * id, which is a vocabulary written for a machine, and the only other option is a blank button.
 */
data class ApprovalDecision(val id: String, val label: String)
