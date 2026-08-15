package dev.swarm.phone.relay

import android.net.http.X509TrustManagerExtensions
import java.io.ByteArrayInputStream
import java.security.cert.CertPathValidatorException
import java.security.cert.CertificateException
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
        } catch (e: CertificateException) {
            // X509TrustManagerExtensions reaches its DELEGATE reflectively, and wraps EVERY
            // failure of that call -- a genuine trust refusal from the delegate AND a failure
            // to invoke the delegate at all (access, provider, initialisation) -- in a
            // CertificateException, so this type alone does not separate the two: verified
            // empirically, an untrusted chain surfaces with a java.security.cert cause (a
            // CertPathValidatorException, here), while a delegate the platform could not even
            // call (e.g. an IllegalAccessException) surfaces with the SAME exception type and
            // a fixed "Failed to call checkServerTrusted" message. The cause's own domain is
            // therefore the signal: a cause inside java.security.cert (or none at all) is a
            // real verdict about the peer; anything else is Android's own plumbing failing to
            // reach the verifier, which W8 forbids presenting as a security accusation.
            val cause = e.cause
            if (cause != null && cause !is CertificateException && cause !is CertPathValidatorException) {
                throw Exception(
                    "swarm-relaytrust/unavailable: the platform trust manager could not be " +
                        "consulted: ${e.message}: ${cause.message}",
                    e,
                )
            }
            throw Exception("swarm-relaytrust/untrusted: ${e.message}", e)
        } catch (e: Exception) {
            // Any OTHER exception type reaching here is not even a CertificateException --
            // never a trust verdict about the peer, and W8 forbids presenting it as one.
            throw Exception(
                "swarm-relaytrust/unavailable: checkServerTrusted failed for a reason that is " +
                    "not a trust verdict: ${e.message}",
                e,
            )
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
 * verifyHostnameCoversLeaf refuses unless the leaf's SANs cover host, mirroring
 * `x509.Certificate.VerifyHostname` on the Go side: an IP-literal host is matched against
 * `iPAddress` SAN entries (type 7) EXACTLY -- the ADR-016 W6 development population, an
 * IP literal a `pinned_spki` relay may legitimately present -- and any other host is
 * matched against `dNSName` SAN entries (type 2), case-insensitively, with ordinary
 * single-label wildcard matching (`*.example.com` covers `swarm-relay.example.com`, not
 * `a.b.example.com`). Kept independent of `checkServerTrusted`'s own hostname handling so
 * a certificate mis-scoped for a different name is refused however this device's default
 * `TrustManagerFactory` happens to be implemented.
 */
private fun verifyHostnameCoversLeaf(host: String, leaf: X509Certificate) {
    val sans = try {
        leaf.subjectAlternativeNames
    } catch (e: Exception) {
        null
    } ?: emptyList()

    val dnsNames = sanValues(sans, type = 2)
    val covered = if (isIPLiteral(host)) {
        sanValues(sans, type = 7).any { it == host }
    } else {
        dnsNames.any { matchesHostname(it, host) }
    }
    if (!covered) {
        throw Exception(
            "swarm-relaytrust/untrusted: the certificate's SANs ($dnsNames) do not cover host $host",
        )
    }
}

/** sanValues extracts every SAN entry of the given GeneralName type (RFC 5280 numbering). */
private fun sanValues(sans: Collection<List<*>>, type: Int): List<String> =
    sans.mapNotNull { entry ->
        val entryType = (entry.getOrNull(0) as? Number)?.toInt()
        val value = entry.getOrNull(1) as? String
        if (entryType == type && value != null) value else null
    }

/**
 * isIPLiteral is a SYNTACTIC check only, deliberately: this runs inside a TLS verification
 * callback and must never trigger a DNS lookup -- `java.net.InetAddress.getByName` would,
 * for anything that is not already a literal, which is precisely the wrong failure mode on
 * this path (blocking, and the ordinary case is a genuine hostname). An IPv6 literal always
 * contains ':', which no valid DNS label may (RFC 1123); an IPv4 literal is dotted-quad
 * decimal. A string this loosely matches but is not actually a valid address simply matches
 * no real `iPAddress` SAN below, so looseness here costs nothing.
 */
private fun isIPLiteral(host: String): Boolean = host.contains(':') || ipv4Literal.matches(host)

private val ipv4Literal = Regex("^\\d{1,3}(\\.\\d{1,3}){3}$")

/**
 * matchesHostname is dNSName-only (see verifyHostnameCoversLeaf for the iPAddress half).
 * Both pattern and host are lowercased BEFORE any comparison, including before the
 * wildcard suffix is stripped -- `host.removeSuffix(suffix)` is itself case-sensitive, so
 * comparing the two in their original case let a differently-cased host (`Relay.Example.com`
 * against a `*.example.com` SAN) fail here while passing Go's own case-insensitive
 * `VerifyHostname`.
 */
private fun matchesHostname(pattern: String, host: String): Boolean {
    val p = pattern.lowercase()
    val h = host.lowercase()
    if (p == h) return true
    if (p.startsWith("*.")) {
        val suffix = p.substring(1) // ".example.com"
        val label = h.removeSuffix(suffix)
        return label != h && label.isNotEmpty() && !label.contains('.') && h.endsWith(suffix)
    }
    return false
}
