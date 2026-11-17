#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
scratch=$(mktemp -d "${TMPDIR:-/tmp}/swarm-apple-bind.XXXXXX")
trap 'rm -rf -- "$scratch"' EXIT
cd "$root"

swift run --package-path ios --scratch-path "$scratch/swift" VoiceKitChecks
bin=$(swift build --package-path ios --scratch-path "$scratch/swift" --show-bin-path)
go tool gobind -lang=objc -outdir "$scratch/objc" ./mobile
generated="$scratch/objc/src/gobind"
printf 'module Swarmmobile { header "Swarmmobile.objc.h" export * }\n' > "$generated/module.modulemap"
swiftc -typecheck -I "$bin" -I "$bin/Modules" -I "$generated" \
    ios/VoiceMobile/Sources/SwarmVoiceMobile/VoiceDeviceBridge.swift
