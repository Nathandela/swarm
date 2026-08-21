package dev.swarm.phone

import swarmmobile.App

/**
 * The one production [LifecycleHandle]: the bound facade's own lifecycle verbs, wrapped so
 * [LifecycleLane] can be driven by a fake on the unit-test JVM (libgojni cannot load there).
 *
 * EVERY MEMBER IS A THIN WRAPPER NAMED FOR ITS VERB, and that is a fence's requirement
 * rather than taste: android/gate/s25r3_releasepath_test.go judges teardown verbs by name and
 * accepts a stray facade call in THIS file only where the enclosing function is named exactly
 * the verb it wraps -- so a call site of this handle reads to the gate as a call site of the
 * facade verb itself, and renaming the verb (agents-tracker-jx1x's laundering) is rejected
 * where it is declared.
 *
 * [app] IS EXPOSED FOR ONE READER: `PhoneSurface.lifecycleFor` caches this handle per App so
 * the lane's eager-hold identity checks are about the PHONE rather than about whichever
 * wrapper instance a redraw happened to build.
 */
internal class AppLifecycle(val app: App) : LifecycleHandle {

    override fun enterBackground() {
        app.enterBackground()
    }

    override fun unsubscribeJournal() {
        app.unsubscribeJournal()
    }

    override fun stop() {
        app.stop()
    }

    override fun start() {
        app.start()
    }
}
