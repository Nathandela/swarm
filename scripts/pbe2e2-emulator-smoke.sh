#!/usr/bin/env bash
# PB-E2E-2 -- the on-emulator smoke, as the REPRODUCIBLE RUNBOOK its acceptance criterion asks
# for ("Evidence (log + screenshots) + reproducible runbook").
#
#   "APK installs, pairs against a local relay + daemon, SAS matches, observes, takes control,
#    types -- including one real `adb shell am force-stop` mid-session (PB-STATE-2 on a real
#    runtime)."
#
# IT IS A SCRIPT AND NOT A GO TEST, deliberately. The run boots an AVD and takes minutes, and
# `go test ./...` is PB-E2E-4's own gate: a test that booted an emulator would either wedge that
# gate or have to skip, and a skipped test in an exit demonstration is the demonstration not
# happening. The facts the smoke is impossible without are asserted, unskippably, in
# android/gate/s19_pbe2e2_test.go; this file is the run.
#
# STATUS. The two blockers this file recorded are closed:
#   (a) The APK now has all five of the requirement's in-app subjects. PairingSurface adds the
#       scanner (ZXing + CameraX, ADR-007 B21), the destination confirmation, the SAS display
#       and its two answer controls; PhoneSurface adds the keyboard. PhoneSurface also calls
#       App.Start, which nothing did -- so before S19 the phone never dialled the relay and
#       "observes" could not have worked even with the controls present.
#   (b) The module has an androidTest source set and a testInstrumentationRunner. The two
#       classes below drive the installed APK through its own controls:
#         dev.swarm.phone.PbE2E2PairAndTypeTest -- pairs, confirms the destination, compares the
#           SAS, observes, takes control, types.
#         dev.swarm.phone.PbE2E2ResumeTest      -- what the app is after the force-stop.
#       Their preconditions arrive as instrumentation arguments and a missing one is a FAILURE,
#       never a skip.
#
# WHAT THIS RUN DOES NOT COVER, said in the file per the brief.
#
# PB-E2E-5 STAYS DEFERRED AND AN EMULATOR IS NOT A HANDSET. Nothing here exercises real
# biometrics, real FCM delivery, Doze, reboot, or hardware-backed Keystore attestation, and no
# artifact this script produces may be read as evidence for any of them.
#
# THE CAMERA IS NOT EXERCISED, and that is a deliberate limit rather than an oversight. The QR
# reaches the app through PB-PAIR-2's manual-entry path -- the same payload, the same
# App.BeginPairing, the same display-then-confirm step. The scanner is wired and shipped, and
# this run says nothing whatever about whether a camera decodes anything. Pointing it at an
# emulator's virtual scene would prove the pipeline decodes on an emulator; it would still not
# be a statement about a physical device.

set -euo pipefail

# ---------------------------------------------------------------------------
# Toolchain. Nothing is on PATH and no ANDROID_* variable is exported by default on this host
# (docs/verification/remote-phaseB-progress.md, "Build environment"), so a naive probe reports
# no Android toolchain at all. These are the verified locations.
# ---------------------------------------------------------------------------
export ANDROID_HOME="${ANDROID_HOME:-/usr/local/share/android-commandlinetools}"
export ANDROID_SDK_ROOT="$ANDROID_HOME"
export JAVA_HOME="${JAVA_HOME:-/usr/local/opt/openjdk@17}"
export PATH="$JAVA_HOME/bin:$ANDROID_HOME/platform-tools:$ANDROID_HOME/emulator:$PATH"

AVD="${SWARM_AVD:-swarmtest}"
PKG=dev.swarm.phone
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="${SWARM_E2E2_OUT:-$REPO/docs/verification/pbe2e2-run}"

log() { printf '\n== %s ==\n' "$*"; }
shot() { adb exec-out screencap -p > "$OUT/$1.png"; }

mkdir -p "$OUT"
exec > >(tee "$OUT/run.log") 2>&1

log "toolchain"
command -v adb >/dev/null || { echo "adb not found under $ANDROID_HOME"; exit 1; }
command -v emulator >/dev/null || { echo "emulator not found under $ANDROID_HOME"; exit 1; }
java -version
adb version
emulator -list-avds | grep -qx "$AVD" || { echo "no AVD named $AVD"; exit 1; }

# ---------------------------------------------------------------------------
# The machine side, on the HOST. The emulator reaches the host loopback at 10.0.2.2, so the
# relay and the daemon run here and the app dials that address -- which is also why the pairing
# QR has to carry a host-reachable URL rather than 127.0.0.1.
# ---------------------------------------------------------------------------
log "build the host binaries"
cd "$REPO"
go build -o "$OUT/bin/swarm" ./cmd/swarm
go build -o "$OUT/bin/swarm-relay" ./cmd/swarm-relay
go build -o "$OUT/bin/swarm-remote" ./cmd/swarm-remote
go build -o "$OUT/bin/swarm-fake-agent" ./cmd/swarm-fake-agent

STATE="$OUT/state"
rm -rf "$STATE" && mkdir -p "$STATE"
export SWARM_DAEMON_STATE="$STATE"
export SWARM_DAEMON_REMOTE_SOCK="$STATE/remote.sock"

log "relay"
"$OUT/bin/swarm-relay" --listen 127.0.0.1:8787 --tls off --db "$STATE/relay.db" &
RELAY_PID=$!
trap 'kill "${RELAY_PID:-}" "${DAEMON_PID:-}" "${GW_PID:-}" 2>/dev/null || true' EXIT

log "machine provisioning"
# The relay URL the PHONE will dial: the emulator's route to the host loopback.
RELAY_URL="ws://10.0.2.2:8787"
"$OUT/bin/swarm" remote init --relay-url "$RELAY_URL"
# R-POL.7: remote launches are confined to configured cwd roots and fail closed with none.
printf '{"version":1,"allowed_cwd_roots":["%s"]}\n' "$OUT" > "$STATE/remote-policy.json"

log "daemon"
"$OUT/bin/swarm" daemon &
DAEMON_PID=$!
sleep 2

# ---------------------------------------------------------------------------
# The phone side.
# ---------------------------------------------------------------------------
log "emulator"
emulator -avd "$AVD" -no-snapshot -no-boot-anim &
adb wait-for-device
adb shell 'while [ "$(getprop sys.boot_completed)" != 1 ]; do sleep 1; done'

log "build and install the APK"
cd "$REPO/android"
./gradlew --no-daemon :app:assembleDebug :app:assembleDebugAndroidTest
adb install -r app/build/outputs/apk/debug/app-debug.apk
shot 01-installed

log "a session for the phone to observe"
# THE ONE STEP THIS RUNBOOK CANNOT PERFORM ITSELF, recorded rather than faked.
#
# "observes", "takes control" and "types" all need a session to exist on the machine, and this
# repository has NO non-interactive way to create one: `swarm remote` has init/devices/revoke/
# regrant/pair/off/on/status and nothing that launches, and sessions are otherwise started
# through the interactive TUI. The in-product remote path is App.Launch (PB-APP-6), which has no
# screen on the handset yet -- that is a separate piece of work from PB-E2E-2's five actions and
# is not invented here.
#
# So the command is the operator's, and its absence STOPS the run rather than skipping a clause:
# a smoke that carried on against an empty roster would report "observes" against nothing.
#
#     SWARM_E2E2_SESSION_CMD='<command that leaves one session running on this daemon>' \
#         scripts/pbe2e2-emulator-smoke.sh
#
# swarm-fake-agent (built above) is the agent program such a command would run: it is a scripted
# PTY program, so it is a real session from the daemon's point of view and needs no API key.
: "${SWARM_E2E2_SESSION_CMD:?PB-E2E-2 needs one session on the daemon before the phone can observe, take control or type. Set SWARM_E2E2_SESSION_CMD to a command that starts one; see the comment above this line for why this runbook cannot write that command for you.}"
eval "$SWARM_E2E2_SESSION_CMD"

log "pair, SAS, observe, take control, type"
# The owner mints the QR on the machine; --yes auto-confirms the machine's own SAS gate, so the
# comparison this clause is named for is between the six symbols the phone displays (logged by
# the instrumented test under the SwarmE2E2 tag) and the six the command below prints. Both ends
# derive them independently from the Noise channel binding: they agree only if nothing is
# sitting between the phone and the machine.
"$OUT/bin/swarm" remote pair --yes | tee "$OUT/pair.txt"

# The QR payload the machine printed, handed to the app. It is passed as an instrumentation
# argument rather than baked in: a payload in the repository would be a pairing nobody minted.
QR="$(grep -Eo 'swarm://[^[:space:]]+' "$OUT/pair.txt" | head -1)"
[ -n "$QR" ] || { echo "no pairing payload in $OUT/pair.txt; the machine did not mint a QR"; exit 1; }

./gradlew --no-daemon :app:connectedDebugAndroidTest \
  -Pandroid.testInstrumentationRunnerArguments.class=dev.swarm.phone.PbE2E2PairAndTypeTest \
  -Pandroid.testInstrumentationRunnerArguments.swarmQr="$QR" \
  -Pandroid.testInstrumentationRunnerArguments.swarmRelay="$RELAY_URL"
adb logcat -d -s SwarmE2E2 | tee "$OUT/phone-sas.txt"
shot 02-controlling

# ---------------------------------------------------------------------------
# The clause this requirement was upgraded for. force-stop is strictly stronger than a process
# kill: it also puts the package in the STOPPED state, so no implicit broadcast -- BOOT_COMPLETED
# included -- reaches the app until a person launches it by hand.
# ---------------------------------------------------------------------------
log "force-stop mid-session"
adb shell am force-stop "$PKG"
adb shell dumpsys package "$PKG" | grep -i "stopped=true" || {
  echo "force-stop did not leave the package STOPPED; the clause this requirement was upgraded for did not run"
  exit 1
}
shot 03-force-stopped

log "relaunch and prove PB-STATE-2 on a real runtime"
# The phone must come back holding its durable coordinates -- pairing, epoch, content key,
# relay cursor, send-seq ceiling -- not start from zero. Nothing off the device can see the
# phone's durable blob, so the assertion is made ON the device, through the product: the screen
# says it is paired (which only the durable blob can say, since a completed pairing clears the
# attempt record), a session redraws, and a typed line is accepted -- which a phone whose
# send-seq had rewound would have replayed and had refused.
./gradlew --no-daemon :app:connectedDebugAndroidTest \
  -Pandroid.testInstrumentationRunnerArguments.class=dev.swarm.phone.PbE2E2ResumeTest
shot 04-resumed

log "artifacts"
ls -la "$OUT"
echo "PB-E2E-2 artifacts in $OUT (run.log + screenshots)"
