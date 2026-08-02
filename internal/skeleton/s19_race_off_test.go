//go:build !race

package skeleton

// s19RaceEnabled is true when this test binary was built with `go test -race`. See the
// //go:build race twin for why the GATEWAY subprocess has to carry the same setting.
const s19RaceEnabled = false
