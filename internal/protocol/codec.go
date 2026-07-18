package protocol

import (
	"encoding/json"
	"strings"

	"github.com/Nathandela/swarm/internal/persist"
)

// EncodeControl serializes a Control to the JSON body of a wire.TControl frame.
func EncodeControl(c Control) ([]byte, error) {
	return json.Marshal(c)
}

// DecodeControl parses a Control from a wire.TControl frame body. It is tolerant
// of unknown fields (a newer peer's superset does not break an older decoder) but
// not of malformed JSON.
func DecodeControl(b []byte) (Control, error) {
	var c Control
	if err := json.Unmarshal(b, &c); err != nil {
		return Control{}, err
	}
	return c, nil
}

// NamespacedID composes an endpoint-scoped session id, <endpoint_id>/<local>
// (F-1). The local id is path-safe (no '/'), so the first '/' always splits the
// namespace from the local id.
func NamespacedID(endpointID, localID string) string {
	return endpointID + "/" + localID
}

// ParseID splits a namespaced id back into its endpoint and local parts. It
// reports ok only when both parts are non-empty and exactly one namespace
// boundary is present (a local id never contains '/').
func ParseID(namespaced string) (endpointID, localID string, ok bool) {
	i := strings.IndexByte(namespaced, '/')
	if i <= 0 {
		return "", "", false // no slash, or empty endpoint
	}
	endpointID = namespaced[:i]
	localID = namespaced[i+1:]
	if localID == "" || strings.Contains(localID, "/") {
		return "", "", false
	}
	return endpointID, localID, true
}

// validLocalID reports whether local is a path-safe session id (ADR-004): the
// server re-validates a client-supplied local id against it before any DaemonAPI
// call, so a traversal/NUL/oversized id never reaches the store (E6.6/P-6). It
// delegates to persist.ValidID, the single source of truth for the pattern.
func validLocalID(local string) bool {
	return persist.ValidID(local)
}
