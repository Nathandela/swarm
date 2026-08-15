package relay

import (
	"fmt"
	"net"
	"strings"
)

// parseTrustedProxies parses cfg.TrustedProxies (CIDR strings) into IPNets once,
// at Server construction. A malformed CIDR fails New closed -- the same
// convention a malformed config file failing LoadConfig already follows --
// rather than silently running with fewer trusted proxies than the operator
// configured.
func parseTrustedProxies(cidrs []string) ([]*net.IPNet, error) {
	if len(cidrs) == 0 {
		return nil, nil
	}
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, ipnet, err := net.ParseCIDR(c)
		if err != nil {
			return nil, fmt.Errorf("relay: trusted_proxies: %q: %w", c, err)
		}
		nets = append(nets, ipnet)
	}
	return nets, nil
}

// resolveSourceAddr derives the address the relay keys per-source quotas by
// (playbook 6.5, R2 "proxy-quota"). Behind Caddy -- or any reverse proxy --
// every connection's transport peer is the proxy itself, which, left
// unhandled, collapses every real client into one shared bucket
// (docs/operations/relay-vps-deploy.md's amended _comment_quotas note).
//
// TRUST IS GATED ON THE TCP PEER, NEVER ON THE HEADER'S CONTENT: remoteAddr is
// what the relay's own listener accepted, so a client cannot forge it. Only
// when that peer's address falls inside a configured trusted_proxies CIDR is
// X-Forwarded-For consulted at all; an untrusted peer's header, however it is
// shaped, is never even parsed, and the source key is the peer address
// exactly as it was before this feature existed.
//
// ONLY THE RIGHTMOST HOP IS TRUSTED (the "rightmost-untrusted" convention).
// The relay's one trusted proxy is what appends the LAST entry in the header
// as it forwards a request, so that entry -- and only that entry -- is the
// address the proxy itself observed. Every entry to its left was supplied by
// the client in the original request and is exactly as spoofable as any other
// header the client controls; taking the FIRST (leftmost) entry, a common
// mistake, would let a client dictate its own quota bucket. Stopping at the
// rightmost entry closes that off: no matter how many fabricated hops a
// client prepends, the boundary the relay honours is the one entry its
// trusted proxy itself wrote, appended after everything the client sent.
//
// xff is the CALLER'S responsibility to assemble from every X-Forwarded-For
// header LINE the request carried (see handleHTTP), not just the first --
// net/http's r.Header.Get returns only the first line, and an add-header-style
// proxy (HAProxy's default `option forwardfor`) emits the client's original
// header, untouched, as its own separate line rather than merging into it.
// Reading only the first line would let that first, entirely client-supplied,
// line stand in for the trusted proxy's own appended hop.
//
// THE RIGHTMOST HOP IS VALIDATED, NEVER TRUSTED BLIND (playbook 6.5: quotas
// key "by the validated external address"): after isolating the rightmost
// entry, an optional SplitHostPort unwraps an "IP:port" or "[v6]:port" shape,
// and the result must parse as an IP. Anything else -- whatever a party able
// to reach a trusted peer chooses to write there -- falls back to remoteAddr
// rather than becoming an arbitrary, attacker-chosen bucket key. The parsed
// IP's canonical String() form, not the raw header text, is what gets
// returned: distinct textual spellings of the same host (an IPv4-mapped
// "::ffff:198.51.100.7" vs plain "198.51.100.7"; an expanded vs compressed
// IPv6 form) must share one bucket, or a party able to reach a trusted peer
// mints unbounded buckets for a single host by varying spelling alone.
func resolveSourceAddr(remoteAddr, xff string, trusted []*net.IPNet) string {
	if len(trusted) == 0 || xff == "" {
		return remoteAddr
	}
	peerHost, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		peerHost = remoteAddr
	}
	peerIP := net.ParseIP(peerHost)
	if peerIP == nil || !trustedContains(trusted, peerIP) {
		return remoteAddr
	}
	// Reviewer finding, R2 "proxy-quota" (BLOCKING): strings.Split(xff, ",")
	// materializes one slice element per comma-separated hop, so a trusted
	// peer forwarding an attacker-sized X-Forwarded-For value (a header an
	// internet client controls end-to-end, unauthenticated) costs allocation
	// proportional to its length on every connection. LastIndexByte finds the
	// same rightmost segment Split's last element would -- the text after the
	// LAST comma, or the whole string when there is none -- as an O(1)
	// allocation subslice instead (docs/verification/r2-red/proxy-quota-red.txt
	// Round 4).
	client := xff
	if i := strings.LastIndexByte(xff, ','); i >= 0 {
		client = xff[i+1:]
	}
	client = strings.TrimSpace(client)
	if client == "" {
		return remoteAddr
	}
	if host, _, err := net.SplitHostPort(client); err == nil {
		client = host
	}
	ip := net.ParseIP(client)
	if ip == nil {
		return remoteAddr
	}
	return ip.String()
}

func trustedContains(trusted []*net.IPNet, ip net.IP) bool {
	for _, n := range trusted {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
