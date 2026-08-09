package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.view.ViewGroup
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.R
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.ui.PairingAttempt
import dev.swarm.phone.ui.PairingStep
import dev.swarm.phone.ui.ScannerState
import dev.swarm.phone.ui.kit.kitRequire
import org.junit.Assert.assertEquals
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for agents-tracker-2pnu F2: row 18's padding is spent TWICE on
 * the one path that draws it.
 *
 * `pairOnlyView` builds a `screenColumn` and hosts the pairing flow inside it; `pairingPanelView`
 * builds a `screenColumn` of its own. `PhoneSurface.drawPairOnly` is the only production caller
 * of either -- the flow is ALWAYS hosted by the pair-only screen -- so a started pairing renders
 * with 48 dp of side inset and 20 dp of top, and row 18 says `space_24` horizontal by `space_10`
 * vertical, once.
 *
 * WHY THIS IS ASSERTED ON THE COMPOSITION AND NOT ON EITHER FILE. Each column is correct on its
 * own and each has a test that says so; the defect exists only where the two meet, which is the
 * only arrangement a user ever sees. So the claim is over the EFFECTIVE inset: every padding
 * between the screen's own edge and the flow's first row, added up.
 */
@RunWith(RobolectricTestRunner::class)
class PairOnlyPairingInsetTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    /** One distinct view per slot, so nothing below can be reading the wrong one. */
    private fun slots(): PairingSlots = PairingSlots(
        body = View(context),
        notice = View(context),
        destination = View(context),
        sas = View(context),
        sasInstruction = View(context),
        scanner = View(context),
        scanProgress = View(context),
        controls = PairingControl.entries.associateWith { View(context) },
    )

    /** The REAL pairing panel, as `PairingSurface.draw` builds it. */
    private fun pairing(): View = pairingPanelView(
        context,
        PairingPanelScreen.of(
            attempt = PairingAttempt(
                step = PairingStep.SCAN,
                originShown = "",
                originIsLocalNetwork = false,
                explainsInterruptedAttempt = false,
            ),
            scanner = ScannerState.SCANNING,
            sas = null,
            holding = false,
            machine = "",
            relayKnown = true,
        ),
        slots(),
    )

    /** Every padding between [root] and this view, which is the inset a person measures. */
    private fun View.effectiveInsetFrom(root: View): Pair<Int, Int> {
        var side = 0
        var top = 0
        var v: View = this
        while (true) {
            val parent = v.parent as? View ?: break
            side += parent.paddingStart
            top += parent.paddingTop
            if (parent === root) break
            v = parent
        }
        return side to top
    }

    @Test
    fun `a started pairing spends row 18's padding exactly once`() {
        val root = pairOnlyView(
            context = context,
            pairing = pairing(),
            started = true,
            onStartPairing = {},
        )
        val (side, top) = root.kitRequire(PairingTag.NAV).effectiveInsetFrom(root)

        assertEquals(
            "the pairing flow's side inset is not row 18's `space_24`. `pairOnlyView` hosts the " +
                "flow inside its own `screenColumn` and `pairingPanelView` builds a second one, " +
                "so the row's cell is spent twice on the only path that draws it",
            context.resources.getDimensionPixelSize(R.dimen.swarm_space_24),
            side,
        )
        assertEquals(
            "the pairing flow's top inset is not row 18's `space_10`, for the same doubling",
            context.resources.getDimensionPixelSize(R.dimen.swarm_space_10),
            top,
        )
    }

    /**
     * The other half of the same claim: the OFFER (the screen before anyone presses anything)
     * keeps the row's padding, which is what makes the assertion above about a doubling rather
     * than about the padding having moved out of the pair-only screen altogether.
     */
    @Test
    fun `the offer keeps row 18's padding`() {
        val root = pairOnlyView(
            context = context,
            pairing = View(context),
            started = false,
            onStartPairing = {},
        )
        val (side, top) = root.kitRequire(PairOnlyTag.BODY).effectiveInsetFrom(root)

        assertEquals(context.resources.getDimensionPixelSize(R.dimen.swarm_space_24), side)
        assertEquals(context.resources.getDimensionPixelSize(R.dimen.swarm_space_10), top)
    }

    /**
     * The negative control: the walk is reading paddings and not answering 0 for everything. A
     * reader that always answered 0 would certify any composition at all, including one that had
     * lost row 18's cell entirely.
     */
    @Test
    fun `the inset reader can see a padding that is not there`() {
        val bare = android.widget.FrameLayout(context)
        val child = View(context)
        bare.addView(child)

        assertEquals(0 to 0, child.effectiveInsetFrom(bare))
    }
}
