# RED evidence — gateway runtime Service (agents-tracker-6rn)

Failing-first (GG-5) for the runtime composing both loops.

`go test ./internal/remotegw/ -run TestService`
```
internal/remotegw/service_test.go:73:9: undefined: NewService
internal/remotegw/service_test.go:73:20: undefined: ServiceConfig
FAIL	github.com/Nathandela/swarm/internal/remotegw [build failed]
```
Compile-fail RED: Service / NewService / ServiceConfig do not exist. Unit tests pin
clean ctx-cancel shutdown (with an unreachable daemon) and the command loop draining
a queued command via an injected forwarder; the skeleton TestGatewayServiceE2E drives
journal-OUT + command-IN through a REAL relay + REAL daemon under one Service.Run.
