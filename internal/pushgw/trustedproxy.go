package pushgw

// PG-Q-4's trusted-proxy source resolution. internal/remote/relay/trustedproxy.go already
// solves this exact pattern, but its functions are unexported and this bead may not edit
// that package, so the ~30 lines below are a deliberate, small duplication of the same
// rightmost-hop, TCP-peer-gated design -- not a reinvention. See that file for the fuller
// rationale; the properties are restated briefly here because they are safety-load-bearing:
//
//   - TRUST IS GATED ON THE TCP PEER, NEVER ON THE HEADER'S CONTENT: X-Forwarded-For is
//     consulted only when net.Conn's remote address falls inside a configured
//     trusted-proxies CIDR; an untrusted peer's header is never even parsed.
//   - ONLY THE RIGHTMOST HOP IS TRUSTED: that is the one entry the trusted proxy itself
//     appended: everything to its left is exactly as client-spoofable as any other header.
//   - THE RIGHTMOST HOP IS VALIDATED, NEVER TRUSTED BLIND: it must parse as an IP or the
//     resolution falls back to the raw peer address rather than an attacker-chosen string.

import (
	"fmt"
	"net"
	"strings"
)

// parseTrustedProxies parses cfg CIDR strings into IPNets once, at Server construction. A
// malformed CIDR fails NewServer closed rather than silently trusting fewer proxies than
// configured.
func parseTrustedProxies(cidrs []string) ([]*net.IPNet, error) {
	if len(cidrs) == 0 {
		return nil, nil
	}
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, ipnet, err := net.ParseCIDR(c)
		if err != nil {
			return nil, fmt.Errorf("pushgw: trusted_proxies: %q: %w", c, err)
		}
		nets = append(nets, ipnet)
	}
	return nets, nil
}

// resolveSourceAddr derives the address PG-Q-4 keys quota accounting by. remoteAddr is
// what the gateway's own listener (or its TLS-terminating reverse proxy) accepted, so a
// client cannot forge it; xff is every X-Forwarded-For header line the caller received,
// joined by the caller (net/http's Header.Get returns only the first line).
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
	hops := strings.Split(xff, ",")
	client := strings.TrimSpace(hops[len(hops)-1])
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
