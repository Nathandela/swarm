package dev.swarm.phone.ui.screens

import dev.swarm.phone.ui.ClockBanner
import dev.swarm.phone.ui.StreamBadge
import dev.swarm.phone.ui.StreamView

/**
 * Phase B slice S25 -- PB-DS-9: what this phone can say about ITS OWN LINK to the machine.
 *
 * WHY THIS SCREEN EXISTS AT ALL. [ClockBanner] and [StreamView] were fully modelled, fully
 * unit-tested and reached no pixel (agents-tracker-ah2): `FacadeBridge.clockBanner` and
 * `FacadeBridge.streamView` had no caller outside the adapter, and both were ledgered in
 * `android/unbound-verbs.tsv` as models waiting for a screen. This is that screen. It is the
 * MACHINES destination's, which is where the ledger's own row put the clock banner -- "it belongs
 * with the machine pane" -- and where `mobile/screen_coverage.tsv` files `clock_verdict`,
 * `stale_state` and `resync.pending`.
 *
 * **WHY IT CAN RENDER ON A TAB THAT OTHERWISE CANNOT.** The Machines destination draws
 * [MachinesPanelScreen.UNAVAILABLE_COPY] and nothing else, because `MachinePane` needs two facts
 * this handset has no honest source for: `App.Presence` is a blocking relay round-trip the ledger
 * forbids calling from a render, and `pairedDeviceName` has no bound accessor (agents-tracker-xtj).
 * Neither applies here. `App.ClockVerdict` is a mutex-guarded field read; `App.StreamState` and
 * `App.ResyncPending` read local core state. So these are exactly the facts the phone can state
 * about the link WITHOUT asking the relay -- which is the same distinction PB-APP-11 draws between
 * the relay's opinion and the phone's evidence, one level up.
 *
 * IT IS A PURE FUNCTION OVER THE TWO MODELS, which is the shape this package already uses
 * ([ActivityPanelScreen], [SettingsPanelScreen], [TriageInboxScreen]). No Android import, so it is
 * checkable without a device.
 *
 * ## The two halves are rendered on opposite rules, and both are deliberate
 *
 * **The clock verdict is a FAULT REPORT and says nothing when there is no fault.** An empty
 * `App.ClockVerdict` is a HEALTHY clock, not an unknown one, and [ClockBanner.of] already decides
 * that; an unconditional notice is a warning over a working system and trains the reader to skip
 * the one that matters. `ActivityPanelTest` holds the same discipline for the stale journal notice.
 *
 * **The channels are a READOUT and all four always render.** Hiding the healthy ones makes "all
 * four are fine" indistinguishable from "this screen forgot the reply channel", which is the
 * failure PB-APP-8 is about in the first place -- and a `StreamBadge.LIVE` that never reaches a
 * screen would be the orphaned-model defect this screen exists to fix, one level down.
 *
 * ## What `live` is actually claiming, because it is stronger than it looks
 *
 * `App.streamStale` is `!reconciled || MachineSilentAt(now) || core.StreamStale(stream)`. So a
 * channel reads live only if this phone has reconciled, the machine has spoken inside section
 * 6.0's freshness budget, AND that channel's own seq space has no hole in it. The middle term is
 * PB-APP-11's, folded in per channel: the relay's cheapest attack is to withhold the machine's
 * newest frames while answering every poll, and it leaves no gap for the third term to see -- but
 * it does run the budget out, and every channel goes stale together when it does. That is why the
 * word on a healthy channel can be `Live` without overclaiming.
 *
 * ## What this screen is not
 *
 * **It is not the connection banner.** `ConnectionBanner` is the TRANSPORT's opinion -- the socket
 * is up, the polls are succeeding -- and it is on the inbox's status line beside PB-APP-11's
 * freshness notice. This is what has actually arrived, per repair channel. A phone can read
 * "Connected to your machine." with four stale channels, and that pair of facts is the whole
 * subject of PB-APP-8.
 *
 * **It has no repair control OF ITS OWN, and one now exists elsewhere** (agents-tracker-upbo,
 * landed by agents-tracker-nx44.6). `App.Resync` was unbound while this file was written, and this
 * paragraph recorded the consequence: nothing in production set `resyncAsked`, so [ChannelRow]
 * could carry a repairing notice that no user could reach. That is no longer true. The session
 * detail draws the repair beside the stale notice it mends -- closer to where a hole is actually
 * felt, which is a conversation with records missing from it -- and the rewind that verb performs
 * is the TRANSPORT's read position, shared by all four channels, so one press repairs what this
 * section reports. `StreamBadge.RESYNCING` is reachable in production from that press.
 *
 * A SECOND ENTRY POINT BELONGS HERE AND IS agents-tracker-nx44.2's, which folds a repair action
 * into the sync detail sheet; this section retires into that tab fold (agents-tracker-nx44.3)
 * rather than growing a button first.
 */
data class LinkPanel(
    /** The one `.plabel` over the channels. */
    val heading: String,
    /**
     * PB-TIME-1's verdict, or empty while the clock is in budget.
     *
     * It is the DAEMON's sentence, unaltered. `App.ClockVerdict` returns the user-legible reason
     * and [ClockBanner] carries it verbatim; re-wording it here would put a second copy of a
     * measurement's meaning on the handset, and the two would disagree the first time the budget
     * moved. It is deliberately not routed as an error either -- the daemon's refusal of a skewed
     * command reads "not authorized", which sends a user to pair again when the fix is their clock.
     */
    val clockNotice: String,
    /** One per repair channel, in the order they were asked about. */
    val channels: List<ChannelRow>,
)

/**
 * One repair channel: what it is, and what is true of it.
 *
 * **EXACTLY ONE OF [liveLabel] AND [notice] IS EVER SET, AND THE TYPE IS WHY.** Both are nullable
 * and neither has a default, so a row cannot be built that says a channel is live and describes
 * its hole in the same breath -- which is PB-SYNC-3's optimistic clear wearing different clothes.
 * Which one is filled is decided by `StreamView.badge` in an exhaustive `when`, so a fourth badge
 * value fails the build rather than falling through to a row that says nothing.
 */
data class ChannelRow(
    /**
     * The channel, as the wire names it.
     *
     * VERBATIM, for [ActivityEntry]'s rule: `journal`, `terminal`, `reply` and `grant` are
     * `internal/phonecore`'s own strings, and a table turning them into English would have to
     * invent a phrase for a fifth channel it did not know. `android/gate/pbapp8_repairchannels_test.go`
     * set-compares the four the app asks about against the four the core repairs, because
     * `App.StreamState` answers "live" for a name it has never seen rather than refusing it.
     */
    val stream: String,
    /**
     * The word a channel with nothing wrong carries, or null.
     *
     * It is rendered by the kit's `statusLabel`, which is `--p-hero` -- and hero is this skin's
     * LIVENESS claim rather than a status colour (derivation row 15 spells that out against
     * `--p-ok`). A stale channel therefore gets no label at all: the absence is the assertion, and
     * a green word beside a known hole is the one thing PB-APP-8 forbids.
     */
    val liveLabel: String?,
    /** [StreamView.notice] -- the model's own sentence about the hole, or null when there is none. */
    val notice: String?,
)

object LinkPanelScreen {

    /**
     * The section's heading.
     *
     * THE PRODUCT'S OWN WORD, not an authored one. `ConnectionBanner` already says "Lost the link
     * to your machine" and "the link" is what this section is about -- the path between handset and
     * machine, and what has come down it. "Connection" was the alternative and is the TRANSPORT's
     * word, which is the fact this section exists to be separate from.
     */
    private const val HEADING = "Link"

    /**
     * What a channel with no hole in it says.
     *
     * ONE WORD, BECAUSE IT IS THE ONE STATE THAT NEEDS NO EXPLANATION. The two unhealthy states
     * carry `StreamView.notice`, a sentence saying what is missing and what that means for what is
     * on screen; a healthy channel has nothing to warn about, and a sentence there would be the
     * unconditional notice this file's KDoc argues against. It is honest at one word for the reason
     * the KDoc gives: live folds in the freshness budget, so it is not merely "no gap seen".
     */
    private const val LIVE = "Live"

    /**
     * @param clock `FacadeBridge.clockBanner()` -- PULLED per render, never latched from an event.
     *  The KDoc on the accessor gives the reason and it is this screen's too: on Android the
     *  process is killed and rebuilt constantly, so a screen that opens after the measurement was
     *  never told, and one that latched the event has nothing to clear it with.
     * @param streams `FacadeBridge.streamViews()` -- one per repair channel, in the adapter's
     *  order. The order is not decided here: it is `FacadeBridge.REPAIR_CHANNELS`, which is the one
     *  place the four names are spelled and the one the Go gate compares against the core.
     */
    fun of(clock: ClockBanner, streams: List<StreamView>): LinkPanel = LinkPanel(
        heading = HEADING,
        // [ClockBanner.visible] AND NOT `text.isNotEmpty()`. They agree today, and only one of them
        // is the model's verdict: `of` decides that a blank verdict is a healthy clock, and a
        // screen re-deriving that from the string would be a second opinion able to disagree.
        clockNotice = if (clock.visible) clock.text else "",
        channels = streams.map(::rowFor),
    )

    /**
     * The badge decides which of the row's two slots is filled, in an EXHAUSTIVE `when`.
     *
     * Exhaustive rather than an `if (badge == LIVE)`, because the two are the same today and only
     * one of them stays honest: a fourth `StreamBadge` value would fall through an `if` into the
     * unhealthy arm and be described by whatever `notice` happened to return, where this fails the
     * build until somebody decides what the new state says.
     */
    private fun rowFor(view: StreamView): ChannelRow = when (view.badge) {
        StreamBadge.LIVE -> ChannelRow(stream = view.stream, liveLabel = LIVE, notice = null)

        // BOTH UNHEALTHY STATES TAKE THE SAME ARM, AND NEITHER GETS THE LABEL. A repair in flight
        // does not clear the hole -- `App.Resync` marks the request and the mark clears when the
        // repair LANDS -- so a repairing channel is a channel with a hole in it that is being
        // worked on. `StreamView.notice` is what says which of the two it is.
        StreamBadge.STALE, StreamBadge.RESYNCING ->
            ChannelRow(stream = view.stream, liveLabel = null, notice = view.notice)
    }
}
