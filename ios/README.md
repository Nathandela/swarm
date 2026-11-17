# Apple voice companion foundation

`SwarmVoiceKit` owns a pinned place, separate Ed25519 keys for SSH and voice
device enrollment, and the original scoped Go grant. It keeps each profile
in one non-synchronizing Keychain item using
`kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly`. It treats an inaccessible
item as an error and preserves the existing profile when a place is retargeted.
The native signer callbacks return public keys and signatures to Go. Enrollment
stores the exact Go grant string. The Go facade verifies its profile digest
before using the grant.

`VoiceMobile` is a separate Swift package for an iOS app to link. Its bridge
creates a `SwarmmobileVoiceDevice` from the pinned profile and wires both
generated signer protocols. Enrollment and other synchronous Go
network calls belong off the UI thread. The package consumes the XCFramework
built from this checkout. It requires that artifact at its declared path.

## macOS verification

Run the verification from the repository root.

```bash
ios/check-apple-bindings.sh
```

The check builds the profile package with the host macOS SDK, exercises real
CryptoKit signing with an in-memory profile backend, generates the exact
Objective-C headers from `./mobile`, and typechecks the Swift bridge against
those headers. The check uses an executable harness so it can run on Apple
Command Line Tools without XCTest. It does not access the user's Keychain
or contact a remote service.

## iOS build prerequisites

```bash
ios/build-xcframework.sh --preflight
SWARM_IOS_DEPLOYMENT_TARGET="$QUALIFIED_IOS_VERSION" ios/build-xcframework.sh
```

The build needs full Xcode selected through `xcode-select`, both the device and
simulator SDKs, and the repository's Go toolchain. Go Mobile and gobind resolve
from the pinned module in `go.mod`. The script creates
`VoiceMobile/Artifacts/Swarmmobile.xcframework` with device and simulator
slices. The script does not sign, install, or deploy an app. iOS 13 is the
package's API floor for CryptoKit and concurrency. The supported deployment
version remains an iOS qualification decision.

The next device gate needs an iPhone, an approved Apple development team and
bundle identity, a provisioning profile for that device, and an app with
microphone usage and any required background-audio configuration. Test
Keychain access before first unlock, after unlocking and re-locking, after
reboot, and after profile revocation. Verify the actual signed app's native
audio, lock-screen controls, interruption and reconnect behavior with the
deployment settings used for the test. No app target or signed handset build
exists in this slice.

See [Apple device-only first-unlock Keychain policy](https://developer.apple.com/documentation/security/ksecattraccessibleafterfirstunlockthisdeviceonly), [Apple Keychain item access](https://developer.apple.com/documentation/security/item-attribute-keys-and-values), [Go Mobile Apple bind implementation](https://github.com/golang/mobile/blob/master/cmd/gomobile/bind_iosapp.go).
