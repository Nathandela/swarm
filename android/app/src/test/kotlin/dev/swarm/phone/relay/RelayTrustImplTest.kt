package dev.swarm.phone.relay

import android.net.http.X509TrustManagerExtensions
import java.io.ByteArrayInputStream
import java.security.KeyStore
import java.security.cert.CertificateFactory
import java.security.cert.X509Certificate
import javax.net.ssl.TrustManagerFactory
import javax.net.ssl.X509TrustManager
import org.junit.Assert.assertThrows
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner

/**
 * ADR-016 W2 (FAILING FIRST, RED phase, bd agents-tracker-hggx.3.5): the Kotlin half of the
 * reverse-bound `RelayTrust` seam -- Go calls this, over `X509TrustManagerExtensions.
 * checkServerTrusted(chain, authType, host)` against the DEFAULT `TrustManagerFactory`, which
 * is "the platform's own verifier: it reads the Conscrypt APEX store -- the store
 * security.go:62-75 says Go cannot see -- and it honours the app's Network Security Config."
 *
 * `mobile/r2_adr016_w2_relaytrust_test.go`'s `TestADR016W2_VerdictTokensAgreeAcrossTheLanguageBoundary`
 * is this file's Go-side mirror: it reads this exact file's path (`android/app/src/main/kotlin/
 * dev/swarm/phone/relay/RelayTrust.kt`) for the two verdict tokens, `swarm-relaytrust/untrusted`
 * and `swarm-relaytrust/unavailable`, the same cross-language pin S14's
 * `TestS14_TheTwoCustodyVerdictTokensAgreeAcrossTheLanguageBoundary` performs for KeyCustody.
 *
 * PRODUCTION SHAPE THIS FILE PINS (does not exist yet -- RED):
 *
 *  class RelayTrustImpl(private val extensions: X509TrustManagerExtensions) : swarmmobile.RelayTrust {
 *      override fun verifyRelayChain(host: String, pemChain: ByteArray) { ... }
 *  }
 *
 * gomobile flattens `VerifyRelayChain(host string, pemChain []byte) error` to a Kotlin method
 * that returns Unit and THROWS on a non-nil Go error -- the same convention `KeyCustody.
 * wakeKEK()`/`contentKEK()` already use in the other direction (a non-nil `[]byte, error` result
 * becomes a return-or-throw).
 *
 * Robolectric, not plain JVM: `X509TrustManagerExtensions` is a real Android framework class
 * (`android.net.http`), the one piece of platform behaviour this seam exists to reach.
 *
 * The fixture certificates are FRESH, LOCALLY-GENERATED test keys with no relationship to any
 * real host or service -- `openssl ecparam ... -genkey` run once for this file, embedded as PEM
 * so the test needs no external cert-generation dependency (this project carries no
 * BouncyCastle). caCert is the sole trust anchor a real X509TrustManagerExtensions is built
 * over; leafCert chains to it for "swarm-relay.example.com"; untrustedCert is unrelated and
 * self-signed, standing in for a terminator presenting a certificate no APEX store trusts.
 */
@RunWith(RobolectricTestRunner::class)
class RelayTrustImplTest {

    private val caCert = """
        -----BEGIN CERTIFICATE-----
        MIIBhjCCASugAwIBAgIUGwaoDUbyKsy9HJJhPXjQD96WbaMwCgYIKoZIzj0EAwIw
        GDEWMBQGA1UEAwwNc3dhcm0tdGVzdC1jYTAeFw0yNjA4MTUxMDI1NDlaFw0zNjA4
        MTIxMDI1NDlaMBgxFjAUBgNVBAMMDXN3YXJtLXRlc3QtY2EwWTATBgcqhkjOPQIB
        BggqhkjOPQMBBwNCAARlZcauhLJ1g2lH2zZt8x3lw2kLvKvhJ8mINLUWjcIyZZXo
        mcRyrjXeFfcSzUCGA2ZYY23NhJq6pUjIy6KxXHDNo1MwUTAdBgNVHQ4EFgQU7zQc
        0AfHuW/DRQqH8HZ52MDytU0wHwYDVR0jBBgwFoAU7zQc0AfHuW/DRQqH8HZ52MDy
        tU0wDwYDVR0TAQH/BAUwAwEB/zAKBggqhkjOPQQDAgNJADBGAiEAsrKNsnFV6+Mr
        A3ZkUeyB1KG5eQKuaAja286NSLQ/h60CIQCzq6NzxUCE2sAMMt/ClOQssfsBIWfO
        YXDN5SiQu3AzFw==
        -----END CERTIFICATE-----
    """.trimIndent()

    private val leafCert = """
        -----BEGIN CERTIFICATE-----
        MIIBtzCCAV2gAwIBAgIUI7JfEdCOP72XwCvGxeLdJHcgUfowCgYIKoZIzj0EAwIw
        GDEWMBQGA1UEAwwNc3dhcm0tdGVzdC1jYTAeFw0yNjA4MTUxMDI1NDlaFw0zNjA4
        MTIxMDI1NDlaMCIxIDAeBgNVBAMMF3N3YXJtLXJlbGF5LmV4YW1wbGUuY29tMFkw
        EwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE7IY2NItitzJhYkI6E7WukdNoekO6Rktg
        752jDWsgGHhdNxpQ0OK8COClwHZHrdp6PbO9hoFs0URhgHFNK3SHe6N7MHkwIgYD
        VR0RBBswGYIXc3dhcm0tcmVsYXkuZXhhbXBsZS5jb20wEwYDVR0lBAwwCgYIKwYB
        BQUHAwEwHQYDVR0OBBYEFPAYj4+fob1Xgu5qH+hjdxcV2E5XMB8GA1UdIwQYMBaA
        FO80HNAHx7lvw0UKh/B2edjA8rVNMAoGCCqGSM49BAMCA0gAMEUCIF8Jbz2HjlGr
        yXaEhFwhUlRq1liBqfVzWm2UUAL1XLEQAiEAsSr+vFbWqhiaJczKkTBZq5ahWBqZ
        c8RLnjPRk2L1G8k=
        -----END CERTIFICATE-----
    """.trimIndent()

    private val untrustedCert = """
        -----BEGIN CERTIFICATE-----
        MIIBjTCCATOgAwIBAgIUGjnIabU92JkauBM0EgnXTkFFu5YwCgYIKoZIzj0EAwIw
        HDEaMBgGA1UEAwwRdW50cnVzdGVkLmludmFsaWQwHhcNMjYwODE1MTAyNTQ5WhcN
        MzYwODEyMTAyNTQ5WjAcMRowGAYDVQQDDBF1bnRydXN0ZWQuaW52YWxpZDBZMBMG
        ByqGSM49AgEGCCqGSM49AwEHA0IABDtPRcjUFVtcXB2CK6fWU/cIK7Ano0rZWo3M
        cSy3QhiOe2a4m7snDsDbdENs9C04qhcW2tjtl/RshRCYbEwAcI6jUzBRMB0GA1Ud
        DgQWBBQSqa6GKBeHXINZJLqDI8b/R16oyDAfBgNVHSMEGDAWgBQSqa6GKBeHXINZ
        JLqDI8b/R16oyDAPBgNVHRMBAf8EBTADAQH/MAoGCCqGSM49BAMCA0gAMEUCIQCT
        eayp+g6DuluFl4RKquvgDLpV0CPpZB4QgNZJTxbUdAIgKUQDZvclG5aBj/pPjWGi
        EvtIVNJjTme9V0MBfjUKkT8=
        -----END CERTIFICATE-----
    """.trimIndent()

    /** A real X509TrustManagerExtensions over a trust store holding ONLY caCert. */
    private fun trustingExtensions(): X509TrustManagerExtensions = trustManagerFor(caCert)

    /**
     * ADR-016 punch-list LOW finding (Kotlin hostname matching): fresh fixtures, a
     * SEPARATE throwaway CA from caCert above so this file's two existing trust stores stay
     * untouched. wildcardLeafCert carries `DNS:*.example.com`; ipLeafCert carries
     * `IP:203.0.113.7` and no dNSName SAN at all -- generated once with
     * `openssl ecparam ... -genkey` the same way the file's own doc comment describes for
     * the fixtures above, no relationship to any real host or service.
     */
    private val ca2Cert = """
        -----BEGIN CERTIFICATE-----
        MIIBhzCCAS2gAwIBAgIUXT93WBwbvtMGG8AXQIlaGn369e4wCgYIKoZIzj0EAwIw
        GTEXMBUGA1UEAwwOc3dhcm0tdGVzdC1jYTIwHhcNMjYwODE1MTM0NDI4WhcNMzYw
        ODEyMTM0NDI4WjAZMRcwFQYDVQQDDA5zd2FybS10ZXN0LWNhMjBZMBMGByqGSM49
        AgEGCCqGSM49AwEHA0IABP2ZrN1uTL4gU+Jo5oZkeH5eqLQiA7hgbf2aPe6mWbH2
        /3E4XRWb6vwiPpQZ/uobkXkuMtxDV290W10Xfo2Q8a6jUzBRMB0GA1UdDgQWBBTa
        dJoR4Z62n5u/x48VAwvuzKutazAfBgNVHSMEGDAWgBTadJoR4Z62n5u/x48VAwvu
        zKutazAPBgNVHRMBAf8EBTADAQH/MAoGCCqGSM49BAMCA0gAMEUCIQD2Opjxe1V1
        DWfcpopfwg37RZsNbxhJoTCLUHnr2xq6PQIgVEkhnUYsU01mZMgSGTeVkNCdprkL
        rS4wXit7N6DItOg=
        -----END CERTIFICATE-----
    """.trimIndent()

    private val wildcardLeafCert = """
        -----BEGIN CERTIFICATE-----
        MIIBrDCCAVGgAwIBAgIUFsjuzVJfYWsd15OzzgTD0ut7i6cwCgYIKoZIzj0EAwIw
        GTEXMBUGA1UEAwwOc3dhcm0tdGVzdC1jYTIwHhcNMjYwODE1MTM0NDI5WhcNMzYw
        ODEyMTM0NDI5WjAfMR0wGwYDVQQDDBR3aWxkY2FyZC5leGFtcGxlLmNvbTBZMBMG
        ByqGSM49AgEGCCqGSM49AwEHA0IABHzqg2qZP6xGxtrCgToPHcGoBRsnve8k0def
        I0HNlKncLzwz58l3xyXlIPf8hfbe3CvIL+2KdwrSA/KzqAgx4iKjcTBvMBgGA1Ud
        EQQRMA+CDSouZXhhbXBsZS5jb20wEwYDVR0lBAwwCgYIKwYBBQUHAwEwHQYDVR0O
        BBYEFBCHWV8E4lqyh2QtQ1y0h+E+rJnhMB8GA1UdIwQYMBaAFNp0mhHhnrafm7/H
        jxUDC+7Mq61rMAoGCCqGSM49BAMCA0kAMEYCIQDcj843o1Xz0vPbRfkMTuOXFYSz
        NveRPIarW8DE4B2OEgIhAOcN/4mO9lLZB0kxnXqSuEYXQ/l5GxnUlFp1DJkHh1Mm
        -----END CERTIFICATE-----
    """.trimIndent()

    private val ipLeafCert = """
        -----BEGIN CERTIFICATE-----
        MIIBmTCCAT+gAwIBAgIUFsjuzVJfYWsd15OzzgTD0ut7i6gwCgYIKoZIzj0EAwIw
        GTEXMBUGA1UEAwwOc3dhcm0tdGVzdC1jYTIwHhcNMjYwODE1MTM0NDI5WhcNMzYw
        ODEyMTM0NDI5WjAWMRQwEgYDVQQDDAsyMDMuMC4xMTMuNzBZMBMGByqGSM49AgEG
        CCqGSM49AwEHA0IABGigenv+37XyrgMNc8wCMrmUvUPlWin4Q2YJCoKBwzXTnL9u
        e3+Wf7Qse3QA23x11yBlX085F1uKtUsJJ+ZWP26jaDBmMA8GA1UdEQQIMAaHBMsA
        cQcwEwYDVR0lBAwwCgYIKwYBBQUHAwEwHQYDVR0OBBYEFPrSIiz4JzHV0FTPodJS
        xXMC+7RiMB8GA1UdIwQYMBaAFNp0mhHhnrafm7/HjxUDC+7Mq61rMAoGCCqGSM49
        BAMCA0gAMEUCIQDOs66IA7oCyjMugvVOcwF83C47Zj+ktPSTuXZHlCRCmAIgSkFq
        gg895C/A6rXw1bBrSm1nK/1rRtREOWuQLAGc6TE=
        -----END CERTIFICATE-----
    """.trimIndent()

    /** A real X509TrustManagerExtensions over a trust store holding ONLY the given CA PEM. */
    private fun trustManagerFor(caPem: String): X509TrustManagerExtensions {
        val ks = KeyStore.getInstance(KeyStore.getDefaultType())
        ks.load(null, null)
        val cf = CertificateFactory.getInstance("X.509")
        val ca = cf.generateCertificate(ByteArrayInputStream(caPem.toByteArray())) as X509Certificate
        ks.setCertificateEntry("swarm-test-ca", ca)

        val tmf = TrustManagerFactory.getInstance(TrustManagerFactory.getDefaultAlgorithm())
        tmf.init(ks)
        val tm = tmf.trustManagers.first { it is X509TrustManager } as X509TrustManager
        return X509TrustManagerExtensions(tm)
    }

    @Test
    fun a_wildcard_certificate_admits_a_differently_cased_host() {
        val impl = RelayTrustImpl(trustManagerFor(ca2Cert))
        // Must not throw: matchesHostname lowercases before stripping the wildcard suffix,
        // so a mixed-case host still matches *.example.com the same way Go's own
        // case-insensitive VerifyHostname does.
        impl.verifyRelayChain("Sub.Example.com", wildcardLeafCert.toByteArray())
    }

    @Test
    fun an_ip_literal_host_is_admitted_by_a_matching_ipaddress_san() {
        val impl = RelayTrustImpl(trustManagerFor(ca2Cert))
        // Must not throw: the certificate carries only an iPAddress SAN (no dNSName at
        // all), which x509.Certificate.VerifyHostname accepts on the Go side and this
        // Kotlin check must too.
        impl.verifyRelayChain("203.0.113.7", ipLeafCert.toByteArray())
    }

    @Test
    fun an_ip_literal_host_is_refused_when_no_ipaddress_san_matches() {
        val impl = RelayTrustImpl(trustManagerFor(ca2Cert))
        val thrown = assertThrows(Exception::class.java) {
            impl.verifyRelayChain("203.0.113.99", ipLeafCert.toByteArray())
        }
        assert(thrown.message?.contains("swarm-relaytrust/untrusted") == true) {
            "expected the untrusted verdict token for an IP host with no matching SAN, got: ${thrown.message}"
        }
    }

    @Test
    fun a_certificate_chaining_to_the_trusted_ca_with_a_matching_host_is_admitted() {
        val impl = RelayTrustImpl(trustingExtensions())
        // Must not throw.
        impl.verifyRelayChain("swarm-relay.example.com", leafCert.toByteArray())
    }

    @Test
    fun an_untrusted_chain_is_refused_with_the_untrusted_token() {
        val impl = RelayTrustImpl(trustingExtensions())
        val thrown = assertThrows(Exception::class.java) {
            impl.verifyRelayChain("swarm-relay.example.com", untrustedCert.toByteArray())
        }
        assert(thrown.message?.contains("swarm-relaytrust/untrusted") == true) {
            "expected the untrusted verdict token in the thrown message, got: ${thrown.message}"
        }
    }

    /**
     * W2: "A verifier that returns nil for everything still cannot admit a certificate whose
     * SAN does not cover the configured host." X509TrustManagerExtensions.checkServerTrusted
     * is HANDED the host and checks it itself (its own hostname verification), so a chain that
     * chains fine to the trusted CA but is asked about the WRONG host must still be refused --
     * this is Kotlin's OWN half of the name check, distinct from (and in addition to) Go's own
     * VerifyHostname the ADR requires on the Go side regardless of what this returns.
     */
    @Test
    fun the_wrong_host_is_refused_even_though_the_chain_is_trusted() {
        val impl = RelayTrustImpl(trustingExtensions())
        assertThrows(Exception::class.java) {
            impl.verifyRelayChain("a-different-host.example.com", leafCert.toByteArray())
        }
    }

    /**
     * ADR-016 W2 review-round fix: "swarm-relaytrust/unavailable ... never a security
     * accusation" must cover every way `checkServerTrusted` can fail that is not itself a
     * trust verdict about the peer. Before this fix the catch block downstream of
     * `checkServerTrusted` stamped `swarm-relaytrust/untrusted` on every failure regardless of
     * cause, contradicting this file's own doc comment two dozen lines up.
     *
     * `X509TrustManagerExtensions` reaches its delegate REFLECTIVELY and wraps every failure
     * of that call in a `CertificateException`, genuine trust refusal or not -- verified
     * empirically against this exact Android/Robolectric jar (a diagnostic run, not a
     * comment's assertion): a delegate that CANNOT be reflectively invoked (this test's
     * `checkServerTrusted(X509Certificate[], String, String)` overload is declared on a
     * private nested class, which the platform's reflection cannot access) surfaces as
     * `CertificateException("Failed to call checkServerTrusted", cause = IllegalAccessException)`
     * -- the SAME exception type a genuine untrusted chain surfaces as (that one instead
     * carries a `CertPathValidatorException` cause; see `an_untrusted_chain_is_refused_...`
     * above). A `catch (e: CertificateException)` alone therefore cannot separate the two;
     * this proves the fix reads the CAUSE's own domain to tell them apart.
     */
    private class ThrowingTrustManager : X509TrustManager {
        override fun checkClientTrusted(chain: Array<out X509Certificate>?, authType: String?) {}
        override fun checkServerTrusted(chain: Array<out X509Certificate>?, authType: String?) {}
        override fun getAcceptedIssuers(): Array<X509Certificate> = emptyArray()

        // The exact reflective signature android.net.http.X509TrustManagerExtensions locates
        // and delegates to (Conscrypt's own convention, shared with the desktop JDK's own
        // trust manager -- the reason trustingExtensions() above, built from a plain
        // TrustManagerFactory, already exercises this delegate path rather than the 2-arg
        // fallback). PUBLIC on a PRIVATE nested class: the platform locates the method (it is
        // public) but cannot INVOKE it (the enclosing class is not), which is exactly the
        // "delegate could not be consulted" fault this test means to exercise -- not a chain
        // or certificate problem at all.
        @Suppress("unused")
        fun checkServerTrusted(
            chain: Array<X509Certificate>,
            authType: String,
            host: String,
        ): List<X509Certificate> {
            throw IllegalArgumentException("unreachable: reflection cannot call this method")
        }
    }

    @Test
    fun a_non_certificate_failure_is_refused_with_the_unavailable_token_not_untrusted() {
        val impl = RelayTrustImpl(X509TrustManagerExtensions(ThrowingTrustManager()))
        val thrown = assertThrows(Exception::class.java) {
            impl.verifyRelayChain("swarm-relay.example.com", leafCert.toByteArray())
        }
        assert(thrown.message?.contains("swarm-relaytrust/unavailable") == true) {
            "expected the unavailable verdict token (a configuration fault, never a security " +
                "accusation) for a non-CertificateException failure, got: ${thrown.message}"
        }
        assert(thrown.message?.contains("swarm-relaytrust/untrusted") != true) {
            "the unavailable verdict must never ALSO read as the untrusted (security) verdict: " +
                "${thrown.message}"
        }
    }
}
