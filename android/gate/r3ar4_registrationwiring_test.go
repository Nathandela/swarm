package gate

// FAILING-FIRST (TDD RED, GG-5) for Wave R3 round 4, the ZERO-PRODUCTION-CALLERS finding,
// Android half: the ADR-015 installation registration (phonecore's EnsurePushRegistration,
// via the bound facade) had no Android caller at all, and the gateway endpoint had no
// config surface -- so scope 1 shipped as a Go library the app could not reach.
//
// The orchestrator's scope ruling: wire NOW what needs no owner-gated external. That is
// exactly two things on this side.
//
//   1. THE LIFECYCLE MUST CALL IT. Android hands this app a token at two moments and both
//      already funnel into PushTokens.register -- SwarmApplication.onCreate's initial
//      getToken and SwarmMessagingService.onNewToken (the rotation re-registration). The
//      registration verb must be invoked from that one funnel, so both entry points reach
//      it and neither can drift.
//   2. THE ENDPOINT MUST COME FROM CONFIG. The build's existing operator-settings
//      convention (operatorSetting: a Gradle property or an environment variable, the same
//      mechanism PB-TOOL-3's signing material crosses on) supplies it, PhoneRuntime puts it
//      on the swarmmobile.Config beside relayURL, and no URL is spelled in Kotlin.
//
// Source-level, matching on comment-stripped text, like every neighbouring fence: this
// package was once defeated by a fence a comment satisfied. Nothing here runs Gradle.

import (
	"path/filepath"
	"strings"
	"testing"
)

// r3ar4PushGatewaySetting is the operator setting the build reads and the resource it
// publishes. Spelled once, here, because the two halves live in two languages and a rename
// on either side is an app that registers against nothing.
const (
	r3ar4PushGatewaySetting  = "SWARM_PUSH_GATEWAY_URL"
	r3ar4PushGatewayResource = "swarm_push_gateway_url"
)

// TestR3AR4_TheTokenFunnelRegistersTheInstallation: PushTokens.register is the one funnel
// both token entry points already reach, so the gateway registration must be invoked there.
// Without it, EnsurePushRegistration is a Go verb no handset ever calls -- PB-PUSH-9's own
// warning, and the sixth instance of this project's standing defect class.
func TestR3AR4_TheTokenFunnelRegistersTheInstallation(t *testing.T) {
	path := filepath.Join(appModule(t), "src", "main", "kotlin", "dev", "swarm", "phone",
		"push", "PushTokens.kt")
	code := kotlinCodeOnly(readFileOrFail(t, path,
		"the token funnel registers this installation with the push gateway"))

	if !strings.Contains(code, "ensurePushRegistration(") {
		t.Errorf("%s: nothing calls the bound ensurePushRegistration verb; ADR-015's "+
			"installation registration has no Android caller, so a paired phone holds no "+
			"gateway installation and every WakeV1 the machine submits is refused at the "+
			"gateway with nothing on either side reporting it", mustRel(t, path))
	}
	// It must ride the SHARED funnel, not a third entry point: the initial getToken and
	// onNewToken are the same fact by two routes, and PushTokens.register is where the
	// file's own doc says they join.
	if !strings.Contains(code, "fun register(") {
		t.Errorf("%s: the shared register funnel is gone; both token entry points must reach "+
			"the gateway registration through it", mustRel(t, path))
	}
}

// TestR3AR4_TheGatewayEndpointComesFromConfig: PhoneRuntime is the one place the app builds
// a phone, and it already carries the relay URL onto swarmmobile.Config. The gateway
// endpoint must arrive the same way, from the build's operator settings -- never a literal
// in Kotlin, which is an endpoint nobody can change without a source edit.
func TestR3AR4_TheGatewayEndpointComesFromConfig(t *testing.T) {
	runtimePath := filepath.Join(appModule(t), "src", "main", "kotlin", "dev", "swarm", "phone",
		"PhoneRuntime.kt")
	runtime := kotlinCodeOnly(readFileOrFail(t, runtimePath,
		"the push gateway endpoint reaches the phone core from configuration"))

	if !strings.Contains(runtime, "pushGatewayURL") {
		t.Errorf("%s: swarmmobile.Config.pushGatewayURL is never set; the bound core is "+
			"constructed with no gateway endpoint and registration cannot reach one",
			mustRel(t, runtimePath))
	}
	if !strings.Contains(runtime, r3ar4PushGatewayResource) {
		t.Errorf("%s: the endpoint does not come from the generated %s resource; a value "+
			"spelled in Kotlin is an endpoint no operator can set", mustRel(t, runtimePath),
			r3ar4PushGatewayResource)
	}
	if strings.Contains(runtime, "https://") {
		t.Errorf("%s: a literal https:// endpoint appears in the runtime; the endpoint is "+
			"operator configuration, not source", mustRel(t, runtimePath))
	}

	buildPath := filepath.Join(appModule(t), "build.gradle.kts")
	build := kotlinCodeOnly(readFileOrFail(t, buildPath,
		"the build publishes the operator-supplied push gateway endpoint"))
	if !strings.Contains(build, r3ar4PushGatewaySetting) {
		t.Errorf("%s: the build reads no %s operator setting, so there is no surface to "+
			"configure the gateway on", mustRel(t, buildPath), r3ar4PushGatewaySetting)
	}
	if !strings.Contains(build, r3ar4PushGatewayResource) {
		t.Errorf("%s: the build publishes no %s resource for the app to read",
			mustRel(t, buildPath), r3ar4PushGatewayResource)
	}
}
