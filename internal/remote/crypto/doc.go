// Package crypto is the remote-control cryptographic foundation (ADR-007
// D2/D3/D4; implementation-plan R-CRY.1-.16, R-PAIR.4). It holds the two
// pinned crypto protocols — Noise XX live transport and the async epoch-key
// envelope — plus device identity/keystore, sealed epoch grants, the pairing
// SAS, and device command signatures.
//
// This file establishes the package so its _test.go files (the FROZEN
// contract, written test-first per GG-5) have a package to compile against. A
// separate implementer supplies every exported symbol the tests reference;
// until then the package has no implementation and `go test` fails to compile
// with "undefined" errors, which is the correct TDD RED.
package crypto
