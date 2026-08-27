package dev.swarm.phone.ui.screens

import android.content.Context
import android.view.View
import android.widget.HorizontalScrollView
import android.widget.ScrollView
import android.widget.TextView
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import dev.swarm.phone.ui.kit.KitTag
import dev.swarm.phone.ui.kit.kitFind
import dev.swarm.phone.ui.kit.kitRequire
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for owner ruling R9's second half: **a file change is a chip in the
 * flow; its diff opens on its own screen.**
 *
 * THE DIFF IS MOVED AND NOT DELETED, and this screen is the whole of that claim. The counter
 * argument that kept the diff in the reading column was that "a diff is the only rendering of what
 * actually changed on disk", and it is answered rather than overruled: what changed is WHERE it is
 * drawn. Without this screen R9 is a deletion wearing a routing decision.
 *
 * **SIDEWAYS IS THE POINT.** A unified diff is column-aligned text whose leading `+`/`-` column
 * carries the meaning; a reading column at body width either wraps it -- misreporting what the
 * producer normalized -- or clips it with nothing below wide enough to reach the rest. The
 * drawing's row for this screen is one line: "one file's unified diff, same well, same rules".
 *
 * **AND IT NEITHER RE-RENDERS NOR RE-WRAPS.** §3.4's `diff_excerpt` is the producer's own
 * normalized unified diff -- "producers normalize ... consumers never see the raw pair" -- so what
 * this side owes it is a surface wide enough to read it on, and nothing else.
 */
@RunWith(RobolectricTestRunner::class)
class DiffScreenTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private val unified = """
        @@ -1,4 +1,4 @@
        -line two
        +line TWO EDITED
         line three
    """.trimIndent()

    private fun screen(
        path: CharSequence = "ui/kit/Composer.kt",
        diff: CharSequence = unified,
        onBack: (() -> Unit)? = null,
    ): View = diffScreen(context = context, path = path, diff = diff, onBack = onBack)

    private fun textOf(view: View?): String = (view as? TextView)?.text?.toString().orEmpty()

    @Test
    fun `the producer's own diff is drawn byte for byte`() {
        assertEquals(
            "the diff was re-rendered, re-wrapped or re-escaped, which rewrites what the producer " +
                "normalized -- the one thing this side may not do to a diff_excerpt",
            unified,
            textOf(screen().kitRequire(DiffTag.WELL)),
        )
    }

    @Test
    fun `the diff scrolls sideways, and down`() {
        val well = screen().kitRequire(DiffTag.WELL)

        val sideways = well.parent
        assertTrue(
            "a diff line wider than the viewport is silently clipped, which is the defect that " +
                "sent the diff to its own screen in the first place",
            sideways is HorizontalScrollView,
        )
        assertTrue(
            "the screen does not scroll down, so a long diff ends at the fold",
            (sideways as View).parent is ScrollView,
        )
    }

    /**
     * The path IS the title, and it is the wire's own `path` rather than a sentence about it.
     *
     * `TranscriptScreen` writes "modify · ui/kit/Composer.kt · +12 -24" for the CHIP, which is
     * three cells for a row in a stream. A screen showing one file's diff has a header, and what
     * belongs in it is the file -- the verb and the counts are already on the chip the reader
     * tapped, and repeating them in the title would be the same fact in two voices.
     */
    @Test
    fun `the title is the file the diff is about`() {
        assertEquals(
            "ui/kit/Composer.kt",
            textOf(screen().kitRequire(KitTag.DRILL_TITLE)),
        )
    }

    @Test
    fun `the back control acts, and a screen with nowhere to go draws none`() {
        var went = 0
        val wired = screen(onBack = { went++ })

        wired.kitRequire(KitTag.DRILL_BACK).performClick()
        assertEquals("the back control does not act, so the reader is stranded on the diff", 1, went)

        assertNotNull(
            "the screen has no header at all, so nothing on it says which file this diff is of",
            screen().kitFind(DiffTag.NAV),
        )
        assertNull(
            "the screen draws a back control with no destination behind it",
            screen().kitFind(KitTag.DRILL_BACK),
        )
    }
}
