# RED evidence — enrollment keystone (agents-tracker-qo4)

Failing-first (GG-5) for wiring the pairing outcome to the device registry and
epoch-key delivery. Captured 2026-07-20 before any production code.

## pairing: MachineSignPub pinned on the device at pairing
`go vet ./internal/remote/pairing/`
```
vet: internal/remote/pairing/machinesignpub_test.go:57:3: unknown field MachineSignPub in struct literal of type MachinePayload
```
Compile-fail RED: MachinePayload has no MachineSignPub field yet, so the phone
has no key to verify epoch grants against.

## enroll: pairing outcome -> registry Record + sealed grant
`go test ./internal/remote/enroll/`
```
internal/remote/enroll/enroll_test.go:62:14: undefined: Enroll
FAIL	github.com/Nathandela/swarm/internal/remote/enroll [build failed]
```
Compile-fail RED: package enroll / func Enroll does not exist.

## phonecore: accept the sealed initial grant
`go vet ./internal/phonecore/`
```
vet: internal/phonecore/accept_test.go:38:33: undefined: AcceptGrant
```
Compile-fail RED: phonecore.AcceptGrant does not exist.
