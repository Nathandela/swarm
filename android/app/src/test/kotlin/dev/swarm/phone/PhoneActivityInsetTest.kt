package dev.swarm.phone

import android.graphics.Insets
import android.view.ViewGroup
import android.view.WindowInsets
import androidx.test.core.app.ActivityScenario
import org.junit.Assert.assertEquals
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for the phone refit's W1.1
 * (docs/specifications/phone-refit-playbook.md, agents-tracker-d45a.1): the top inset is the
 * platform's, not a floor.
 *
 * `screenTopOrRealInset` took `maxOf(measured, 54dp)`. On the owner's handset 54dp is TALLER than
 * the real status bar, so the header sat in dead space under it. Derivation row 19 had already
 * ruled that `screen_top` is an iPhone constant and the Android value comes from
 * `WindowInsets.statusBars`; a floor under that measurement is a second opinion the row does not
 * allow. `swarm_screen_top` stays in dimens.xml -- the design join reads it
 * (android/gate/s22b_spacing_test.go, DesignScaleResolutionTest) -- and PhoneActivity spends it
 * nowhere.
 *
 * DRIVEN THROUGH A REAL DISPATCH rather than a pure function, because the fix is the absence of
 * one: with the arithmetic gone there is nothing left to call but the listener itself.
 */
@RunWith(RobolectricTestRunner::class)
class PhoneActivityInsetTest {

    @Test
    fun `the top padding is the platform's own inset and nothing else`() {
        ActivityScenario.launch(PhoneActivity::class.java).use { scenario ->
            scenario.onActivity { activity ->
                val root = activity.findViewById<ViewGroup>(android.R.id.content).getChildAt(0)
                for (statusBarPx in listOf(0, 30, 120)) {
                    root.dispatchApplyWindowInsets(insetsWithStatusBar(statusBarPx))
                    assertEquals(
                        "W1.1: the root's top padding is not the status bar the platform reported " +
                            "($statusBarPx px). A floor under the measured inset pushes the header " +
                            "into dead space on every handset whose bar is thinner than the " +
                            "design's 54dp preview value",
                        statusBarPx,
                        root.paddingTop,
                    )
                }
            }
        }
    }

    private fun insetsWithStatusBar(topPx: Int): WindowInsets = WindowInsets.Builder()
        .setInsets(WindowInsets.Type.systemBars(), Insets.of(0, topPx, 0, 48))
        .build()
}
