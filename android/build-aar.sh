#!/bin/sh
# PB-TOOL-2 -- the one command that builds the AAR.
#
# S8 produced its AAR by hand, which is why two of its defects survived review: the ABI set
# was whatever the invocation happened to name, and the bind ran without -trimpath, so the
# shipped libgojni.so carried 48 absolute builder paths rooted at /Users/Nathan/go/pkg/mod.
# Both are flags, and flags belong in a checked-in command rather than in someone's shell
# history.
#
# Every decision here comes from android/toolchain.env:
#   SWARM_AAR_ABIS      the explicit ABI set. The gate asserts the artifact carries EXACTLY
#                       this set, so "explicit" cannot decay into "at least these".
#   SWARM_ANDROID_API   gomobile's -androidapi. This is the NDK's floor, not the app's
#                       minSdk; gomobile defaults to API 16 and NDK 27 refuses it.

set -eu

here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo=$(CDPATH= cd -- "$here/.." && pwd)

. "$here/toolchain.env"

# gomobile takes Go platform names; the pin records Android ABI names, because that is what
# the artifact contains and what the gate inspects.
targets=""
for abi in $(echo "$SWARM_AAR_ABIS" | tr ',' ' '); do
    case "$abi" in
        armeabi-v7a) goplatform="android/arm" ;;
        arm64-v8a)   goplatform="android/arm64" ;;
        x86)         goplatform="android/386" ;;
        x86_64)      goplatform="android/amd64" ;;
        *)
            echo "build-aar.sh: SWARM_AAR_ABIS names $abi, which gomobile cannot emit" >&2
            exit 1
            ;;
    esac
    targets="${targets:+$targets,}$goplatform"
done

out="$here/app/libs/swarm.aar"
mkdir -p "$(dirname -- "$out")"

cd "$repo"
echo "building $out for $SWARM_AAR_ABIS (androidapi $SWARM_ANDROID_API)"

# -trimpath removes the module-cache and GOROOT prefixes, which is 46 of the 48 absolute
# paths the S8 reviewer found. It does NOT remove the last two, and that is not a gomobile
# defect: `gomobile bind` synthesises a throwaway module named `gobind` whose go.mod carries
# `replace github.com/Nathandela/swarm => <absolute checkout dir>`, and the Go linker records
# replacement directories in the build-info blob VERBATIM -- -trimpath does not rewrite them
# (verified directly against a two-module probe). So the builder's checkout path is stamped
# into every shipped libgojni.so, and it differs between this laptop and a CI runner.
#
# Blanking runtime.modinfo drops the embedded module graph along with it. That is a real
# cost -- an SBOM tool can no longer read the dependency list out of the .so -- accepted
# because go.mod and go.sum are tracked and authoritative, and because an artifact whose
# bytes depend on where it was built is not reproducible in any useful sense.
exec gomobile bind \
    -target "$targets" \
    -androidapi "$SWARM_ANDROID_API" \
    -trimpath \
    -ldflags "-X=runtime.modinfo=" \
    -o "$out" \
    ./mobile
