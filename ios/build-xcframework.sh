#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
if [[ $# -gt 1 || (${1:-} != "" && ${1:-} != "--preflight") ]]; then
    echo "usage: ios/build-xcframework.sh [--preflight]" >&2
    exit 2
fi

missing=0
if ! xcodebuild -version >/dev/null 2>&1; then
    echo "voice iOS: full Xcode must be installed and selected" >&2
    missing=1
fi
for sdk in iphoneos iphonesimulator; do
    if ! xcrun --sdk "$sdk" --show-sdk-path >/dev/null 2>&1; then
        echo "voice iOS: missing $sdk SDK" >&2
        missing=1
    fi
done
if ! command -v go >/dev/null 2>&1; then
    echo "voice iOS: Go toolchain missing" >&2
    missing=1
fi
if [[ $missing -ne 0 ]]; then exit 1; fi
if [[ ${1:-} == "--preflight" ]]; then
    xcodebuild -version
    go version
    exit 0
fi

target=${SWARM_IOS_DEPLOYMENT_TARGET:-}
if [[ ! $target =~ ^[0-9][0-9]\.[0-9]{1,2}$ ]] || [[ ${target%%.*} -lt 13 ]]; then
    echo "voice iOS: set SWARM_IOS_DEPLOYMENT_TARGET to the intended iOS version (at least 13.0 for CryptoKit)" >&2
    exit 2
fi
artifact="$root/ios/VoiceMobile/Artifacts/Swarmmobile.xcframework"
if [[ -e "$artifact" ]]; then
    echo "voice iOS: existing XCFramework must be moved before building" >&2
    exit 2
fi
mkdir -p "$(dirname "$artifact")"
scratch=$(mktemp -d "${TMPDIR:-/tmp}/swarm-ios-build.XXXXXX")
trap 'rm -rf -- "$scratch"' EXIT
cd "$root"
go build -o "$scratch/gobind" golang.org/x/mobile/cmd/gobind
PATH="$scratch:$PATH" go run golang.org/x/mobile/cmd/gomobile bind \
    -target=ios,iossimulator -iosversion="$target" -trimpath \
    -o "$scratch/Swarmmobile.xcframework" ./mobile
mv "$scratch/Swarmmobile.xcframework" "$artifact"
