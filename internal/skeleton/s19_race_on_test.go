//go:build race

package skeleton

// s19RaceEnabled is true when this test binary was built with `go test -race`. PB-E2E-1
// requires the exit demonstration to pass under the race detector, and the GATEWAY is a
// separate process: a sidecar built without instrumentation would leave a third of the
// chain unwatched while the run still called itself a race run. s19GatewayBinary therefore
// builds cmd/swarm-remote with `-race` exactly when the test binary carries it.
const s19RaceEnabled = true
