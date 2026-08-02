package dev.swarm.phone.ui

import dev.swarm.phone.keys.CapabilityAnomaly

/**
 * What a [CapabilityAnomaly] says to a person.
 *
 * IT IS THE READER THE RECORD DID NOT HAVE. `CustodyPlan.Provisioned.anomalies` is computed on
 * every launch and, until this existed, observed by nobody -- which is the same defect class as
 * the verb with no caller, one layer along: a fact the product derives and then drops.
 *
 * IT IS NOT AN ERROR STATE, and that is why it is here rather than in [ErrorRouter]. An anomaly
 * means a capability the design does NOT consume came back non-PRESENT: the app provisioned,
 * paired, and works. Routing it through the error taxonomy would give it a remedy, and a remedy
 * for a working phone is PB-APP-10's failure loop reached through wording.
 *
 * A HEALTHY HANDSET GETS AN EMPTY STRING. A permanent reassurance is a label nobody reads, and
 * the one launch where this text matters is the one where it has to be noticed.
 */
object CapabilityNotice {

    fun of(anomalies: List<CapabilityAnomaly>): String {
        if (anomalies.isEmpty()) return ""
        val listed = anomalies.joinToString(", ") { "${it.capability} (${it.state})" }
        return "This handset did not confirm $listed. Nothing this app needs is affected -- " +
            "at the supported Android versions the platform is meant to offer them, so include " +
            "this line if you ever report a problem."
    }
}
