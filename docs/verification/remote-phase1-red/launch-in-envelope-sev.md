# RED evidence — launch over the command loop (agents-tracker-sev)

Failing-first (GG-5) for carrying the LaunchReq in the sealed command envelope.

`go test ./internal/remotegw/ -run 'TestOpenRemoteCommand|TestCommandBridge_ForwardsLaunch'`
```
internal/remotegw/launch_loop_test.go:23:80: undefined: protocol.RemoteCommand
internal/remotegw/launch_loop_test.go:43:13: undefined: OpenRemoteCommand
internal/remotegw/launch_loop_test.go:70:58: undefined: protocol.RemoteCommand
FAIL	github.com/Nathandela/swarm/internal/remotegw [build failed]
```
Compile-fail RED: protocol.RemoteCommand and remotegw.OpenRemoteCommand do not
exist; the bridge still refuses launch. Unit test pins backward-compat open of a
bare-auth envelope + launch forwarding; skeleton TestRemoteLaunchE2E drives a real
remote launch through relay+daemon that SPAWNS a session, and proves a gateway that
tampers with the spec is refused not_authorized (content-hash binding).
