package dev.swarm.phone.relay

import android.net.http.X509TrustManagerExtensions
import java.io.ByteArrayInputStream
import java.security.cert.CertificateFactory
import java.security.cert.X509Certificate
import swarmmobile.RelayTrust

/**
 * ADR-016 W2's Kotlin half of the reverse-bound `RelayTrust` seam. Go calls this, over
 * `X509TrustManagerExtensions.checkServerTrusted(chain, authType, host)` against the
 * DEFAULT `TrustManagerFactory` -- "the platform's own verifier: it reads the Conscrypt
 * APEX store -- the store `security.go:62-75` says Go cannot see -- and it honours the
 * app's Network Security Config."
 *
 * gomobile flattens `VerifyRelayChain(host string, pemChain []byte) error` to a method
 * that returns `Unit` and THROWS on a non-nil Go error -- the same convention
 * `KeystoreKeyCustody.wakeKEK()`/`contentKEK()` already use in the other direction (a
 * non-nil `([]byte, error)` result becomes a return-or-throw). `mobile/relaytrust.go`'s
 * `TestADR016W2_VerdictTokensAgreeAcrossTheLanguageBoundary` pins this file's path and the
 * two verdict tokens below, the same cross-language pin S14 performs for `KeyCustody`.
 *
 * THE TWO VERDICTS, stamped into every thrown message so the Go side can tell them apart
 * (`classifyCustodyVerdict`'s own convention, one seam over):
 *   - `swarm-relaytrust/untrusted` -- a REAL security verdict: the chain was PARSED and
 *     `checkServerTrusted` refused it (an untrusted issuer, or the wrong host --
 *     `X509TrustManagerExtensions` checks the hostname itself, in addition to Go's own
 *     independent `VerifyHostname`).
 *   - `swarm-relaytrust/unavailable` -- a CONFIGURATION fault, never a security
 *     accusation: the presented bytes could not even be parsed as a certificate chain, or
 *     `checkServerTrusted` itself failed for a reason that is not "untrusted" (e.g. no
 *     default trust manager is available on this device). Distinguished from the verdict
 *     above because a caller must never present either as the other -- W8's
 *     `relay_trust_unavailable` is an app fault, `relay_untrusted` is "not the relay your
 *     machine published".
 */
class RelayTrustImpl(private val extensions: X509TrustManagerExtensions) : RelayTrust {

    override fun verifyRelayChain(host: String, pemChain: ByteArray) {
        val chain: List<X509Certificate>
        try {
            val factory = CertificateFactory.getInstance("X.509")
            @Suppress("UNCHECKED_CAST")
            chain = factory.generateCertificates(ByteArrayInputStream(pemChain))
                .toList() as List<X509Certificate>
        } catch (e: Exception) {
            throw Exception(
                "swarm-relaytrust/unavailable: the presented certificate chain could not be parsed: ${e.message}",
                e,
            )
        }
        if (chain.isEmpty()) {
            throw Exception("swarm-relaytrust/unavailable: no certificate in the presented chain")
        }

        try {
            // authType "RSA" is the same fixed value Conscrypt's own internal callers use when
            // the real algorithm is unknown to the caller: checkServerTrusted's own contract
            // (and Conscrypt's implementation) only consult it as a hint for chain building, and
            // the actual leaf key algorithm is read from the certificate itself.
            extensions.checkServerTrusted(chain.toTypedArray(), "RSA", host)
        } catch (e: Exception) {
            throw Exception("swarm-relaytrust/untrusted: ${e.message}", e)
        }

        // KOTLIN'S OWN HOSTNAME CHECK, explicit and independent of whatever
        // checkServerTrusted's `host` argument does on this particular JCA provider.
        // checkServerTrusted's own hostname enforcement is provider-specific (it walks
        // through a reflection-located delegate on a real device's Conscrypt stack), so
        // this leaf-SAN check is what makes "Kotlin checks the hostname itself" a property
        // of THIS implementation rather than an assumption about the platform underneath
        // it. It is not a substitute for Go's own independent VerifyHostname -- "neither
        // half alone admits a peer" -- it is Kotlin's half of that pair, made unconditional.
        verifyHostnameCoversLeaf(host, chain[0])
    }
}

/**
 * verifyHostnameCoversLeaf refuses unless one of the leaf's dNSName SAN entries covers
 * host, case-insensitively, with ordinary single-label wildcard matching
 * (`*.example.com` covers `swarm-relay.example.com`, not `a.b.example.com`). It is the
 * same shape `x509.Certificate.VerifyHostname` checks on the Go side, kept independent
 * so a certificate mis-scoped for a different name is refused however this device's
 * default `TrustManagerFactory` happens to be implemented.
 */
private fun verifyHostnameCoversLeaf(host: String, leaf: X509Certificate) {
    val dnsNames = try {
        leaf.subjectAlternativeNames
    } catch (e: Exception) {
        null
    }?.mapNotNull { entry ->
        val type = (entry.getOrNull(0) as? Number)?.toInt()
        val value = entry.getOrNull(1) as? String
        if (type == 2 && value != null) value else null
    } ?: emptyList()

    if (dnsNames.none { matchesHostname(it, host) }) {
        throw Exception(
            "swarm-relaytrust/untrusted: the certificate's SANs ($dnsNames) do not cover host $host",
        )
    }
}

private fun matchesHostname(pattern: String, host: String): Boolean {
    if (pattern.equals(host, ignoreCase = true)) return true
    if (pattern.startsWith("*.")) {
        val suffix = pattern.substring(1) // ".example.com"
        val label = host.removeSuffix(suffix)
        return label != host && label.isNotEmpty() && !label.contains('.') &&
            host.endsWith(suffix, ignoreCase = true)
    }
    return false
}
