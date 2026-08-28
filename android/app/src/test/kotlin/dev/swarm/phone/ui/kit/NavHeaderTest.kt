package dev.swarm.phone.ui.kit

import android.content.Context
import android.view.View
import android.widget.LinearLayout
import androidx.test.core.app.ApplicationProvider
import dev.swarm.phone.theme.SwarmTheme
import org.junit.Assert.assertEquals
import org.junit.Assert.assertSame
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * FAILING-FIRST (TDD RED, GG-5) for phone-refit-playbook W7.6: `navHeader` gains one parameter,
 * `trailing: View? = null` -- a slot for a header action, drawn after the status slot. The
 * default keeps every existing caller compiling and drawing exactly what it drew; the Computers
 * screen is the first caller to pass one (its Add action).
 */
@RunWith(RobolectricTestRunner::class)
class NavHeaderTest {

    private val context: Context
        get() = SwarmTheme.applyTo(ApplicationProvider.getApplicationContext())

    private fun children(header: LinearLayout): List<View> =
        (0 until header.childCount).map { header.getChildAt(it) }

    @Test
    fun `a trailing action is drawn after the status slot`() {
        val status = View(context).apply { tag = "status probe" }
        val action = ctaButton(context, "Add", CtaKind.MORE)

        val header = navHeader(context, "Computers", "2 LIVE", status, trailing = action)

        val drawn = children(header)
        assertSame(
            "the trailing action is not the LAST thing on the header row; it reads outward " +
                "from the title -- counter, status, then the action (W7.6)",
            action,
            drawn.last(),
        )
        assertSame("the status slot no longer sits between the counter and the action", status, drawn[drawn.size - 2])
        assertEquals("the title is not the first thing on the row", KitTag.TITLE, drawn.first().tag)
    }

    @Test
    fun `a null trailing draws nothing`() {
        val header = navHeader(context, "Inbox", null, null, trailing = null)

        assertEquals(
            "a header with no trailing action drew a child for the empty slot",
            listOf(KitTag.TITLE),
            children(header).map { it.tag },
        )
        assertEquals(
            "the default is not the same as passing null, so an existing caller draws differently",
            children(header).size,
            children(navHeader(context, "Inbox", null)).size,
        )
    }
}
