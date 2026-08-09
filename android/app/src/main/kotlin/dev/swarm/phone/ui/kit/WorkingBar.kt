package dev.swarm.phone.ui.kit

import android.content.Context
import android.graphics.Color
import android.view.View
import android.widget.LinearLayout
import dev.swarm.phone.R

/**
 * origin: .workbar
 *
 * Substrate's Working affordance, and the half that is easy to lose.
 *
 * The mock pulses the Working dot on a 1.6 s loop; Substrate declares no animation anywhere and
 * said instead that the state was carried by a STATIC glow plus this static gradient (ADR-007
 * B134 decision 3). Ruling R8 (2026-08-09) retired the glow half -- the maquette's `.sdot.work`
 * declares no `box-shadow` at all -- so this bar is now Working's WHOLE static affordance. A row
 * that is computing shows a cyan bar fading out to the right, once, and nothing moves.
 */
fun workingBar(context: Context): View {
    val work = Kit.colour(context, R.color.swarm_state_working)
    return View(context).apply {
        background = WorkingBarShape(
            startColour = work,
            // `transparent` with --p-work's RGB kept. See WorkingBarShape: the obvious spelling
            // fades the bar through black.
            endColour = Color.argb(0, Color.red(work), Color.green(work), Color.blue(work)),
            fadeStop = KitMetrics.WORKBAR_FADE_STOP,
            radiusPx = Kit.dp(context, KitMetrics.WORKBAR_RADIUS_DP),
        )
        layoutParams = LinearLayout.LayoutParams(
            MATCH,
            Kit.dpPx(context, KitMetrics.WORKBAR_HEIGHT_DP),
        ).apply { topMargin = Kit.dimenPx(context, R.dimen.swarm_space_2) }
        tag = KitTag.WORKBAR
    }
}
