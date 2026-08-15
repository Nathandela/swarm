// Package pushgw implements the Swarm push gateway HTTP service specified by
// docs/specifications/push-gateway-api.md ("the spec") and ADR-015 (WakeV1, the capability
// model): registration, token rotation, address allocation, wake submission and revocation,
// each behind the auth, quota, retention and error-vocabulary rules the spec sections cited
// throughout this package's files require. The *_test.go files in this directory are its
// conformance suite; docs/verification/r3-red/pushgw-red.txt is the RED-phase record this
// implementation was built to turn green.
package pushgw
