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
# STATUS: IT DOES NOT PASS TODAY, AND THE REASON IS NOT THIS SCRIPT.
#   (a) The APK cannot pair. PhoneSurface ships three buttons -- take control, kill, revoke --
#       and a pairing line that renders PairingFlow's SCAN message; its own comment says it
#       "does not run a pairing on its own". There is no scanner, no destination confirmation,
#       no SAS display, no confirm control and no keyboard, so four of the requirement's five
#       in-app actions have no subject.
#   (b) The module has no instrumented source set and no testInstrumentationRunner, so there is
#       nothing to install alongside the APK. Adding one is not a line change: the module pins
#       its dependency graph (dependencyLocking + verification metadata, PB-SEC-14), so every
#       androidx.test artifact has to be locked and justified.
# Both are recorded against S19 as findings. Until they are closed, the steps below run as far
# as INSTALL and then stop at the first action the app cannot perform -- which is the honest
# outcome, not a skip.
#
# WHAT THIS RUN DOES NOT COVER, said in the file per the brief: PB-E2E-5 stays deferred. An
# emulator is not a handset. Nothing here exercises the real camera, real biometrics, real FCM
# delivery, Doze, reboot, or hardware-backed Keystore attestation, and no artifact this script
# produces may be read as evidence for any of them.

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
"$OUT/bin/swarm" remote init --relay-url ws://10.0.2.2:8787
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

log "pair, SAS, observe, take control, type"
# The owner mints the QR; the app scans (or is handed) it, the two SAS displays are compared,
# and the phone observes the roster, takes control and types.
#
# NOT RUNNABLE TODAY -- see the STATUS note at the top of this file. The instrumented test that
# performs these five actions through the APK does not exist, and the APK has no surface for
# four of them. This is the step that fails, and it fails naming the missing surface rather
# than being skipped.
"$OUT/bin/swarm" remote pair --yes | tee "$OUT/pair.txt"
./gradlew --no-daemon :app:connectedDebugAndroidTest
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
adb shell monkey -p "$PKG" -c android.intent.category.LAUNCHER 1
sleep 3
shot 04-resumed
# The phone must come back holding its durable coordinates -- pairing, epoch, content key,
# relay cursor, send-seq ceiling -- not start from zero. Asserted on device by the instrumented
# test above (StateSummary.Restored plus a send-seq that did not rewind), because nothing off
# the device can see the phone's durable blob.

log "artifacts"
ls -la "$OUT"
echo "PB-E2E-2 artifacts in $OUT (run.log + screenshots)"
