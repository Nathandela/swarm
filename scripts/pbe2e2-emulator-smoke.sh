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
# STATUS: THIS SCRIPT HAS NEVER RUN TO COMPLETION. PB-E2E-2 IS NOT MET.
#
# Read that before anything else in this file. The requirement sat `shipped` for the whole of
# Phase B because evidence was measured per SLICE rather than per requirement, and nothing
# checked whether the run had happened. It had not.
#
# What HAS been executed, on 2026-07-26, against this tree:
#   - the emulator half: the swarmtest AVD boots headless, adb reaches it, :app:assembleDebug
#     and :app:assembleDebugAndroidTest build, and the APK installs;
#   - the machine half: relay, TLS terminator, pin, `swarm remote init --relay-pin`, daemon,
#     and `swarm remote pair` minting a QR over wss:// and waiting for a phone;
#   - the session helper (scripts/e2e2session), which creates a real session on the daemon.
#
# What has NEVER been executed: everything from the pairing handshake onwards -- the five
# in-app actions, the force-stop and the resume. The phone cannot dial the relay at all; see
# the [UNRUN] block further down, and ADR-007 residual 1.9. No artifact this script has so far
# produced is evidence for any clause of PB-E2E-2.
#
# SIX THINGS IN THIS FILE COULD NOT HAVE WORKED, which is how it is known it never ran:
#
#   1. swarm-relay was called with --listen/--tls/--db; it takes only --config.
#   2. `swarm remote pair` was called with a --yes that does not exist (--capability is the
#      only flag). None is needed: the SAS gate reads from stdin.
#   3. The QR was grepped for `swarm://`; the CLI prints `swarm-pair:1:`, WRAPPED across
#      terminal-width lines, so the pattern could never have matched anything.
#   4. The ordering could not complete: the pair command blocks until the phone connects, so
#      it could not be run to completion and then read for the QR the phone needs to connect.
#   5. `swarm daemon` reads SWARM_DAEMON_SOCK/LOCK/LOG with no defaults and was given none, so
#      it would have died with "serve: open : no such file or directory".
#   6. The daemon socket was placed under $OUT, which on a normal checkout overflows sun_path.
#
# All six are fixed below, and the relay URL had to change scheme AND address besides. None of
# the fixes has been exercised past the [UNRUN] stop.
#
# Two earlier blockers this file recorded ARE closed:
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
# The machine side, on the HOST: the relay, the TLS terminator, the daemon and the session all
# run here, and the phone reaches them over the network.
#
# THE ADDRESS IS THE HOST'S LAN IP, not 10.0.2.2 and not 127.0.0.1. relay.json carries ONE
# relay_url and it serves two ends -- the machine dials it, and `swarm remote pair` copies it
# verbatim into the QR the phone dials (PB-PAIR-7) -- so it has to be reachable from both.
# 10.0.2.2 is the emulator's private alias for the host loopback and means nothing on the host;
# 127.0.0.1 means the emulator itself. The LAN address is the only one both ends resolve, and it
# is the topology docs/operations/relay-runbook.md already prescribes.
# ---------------------------------------------------------------------------
log "build the host binaries"
cd "$REPO"
go build -o "$OUT/bin/swarm" ./cmd/swarm
go build -o "$OUT/bin/swarm-relay" ./cmd/swarm-relay
go build -o "$OUT/bin/swarm-remote" ./cmd/swarm-remote
go build -o "$OUT/bin/swarm-fake-agent" ./cmd/swarm-fake-agent
# TEST-ONLY, and not one of the shipped binaries -- see the header of scripts/e2e2session.
go build -o "$OUT/bin/e2e2session" ./scripts/e2e2session

STATE="$OUT/state"
rm -rf "$STATE" && mkdir -p "$STATE"
export SWARM_DAEMON_STATE="$STATE"
# THE SOCKETS LIVE IN A SHORT PATH, NOT UNDER $OUT. A unix socket path must fit in sun_path
# (104 bytes) and $OUT defaults to a directory inside the repository, which on a normal
# checkout is already long enough to overflow it -- the daemon would fail to bind with a
# message about the path rather than about the length. internal/skeleton's own E2E rig makes
# the same accommodation for the same reason.
SOCKDIR="$(mktemp -d /tmp/e2e2.XXXXXX)"
export SWARM_DAEMON_REMOTE_SOCK="$SOCKDIR/r.sock"
# `swarm daemon` reads these three from the environment with NO defaults
# (skeletonConfigFromEnv), so a daemon started with only SWARM_DAEMON_STATE set dies with
# "serve: open : no such file or directory". The TUI never hits it because daemon autostart
# sets them; a script that starts the daemon by hand has to. The socket path is the one
# dialClient defaults to, so the CLI and the daemon agree on it.
export SWARM_DAEMON_SOCK="$SOCKDIR/d.sock"
export SWARM_DAEMON_LOCK="$STATE/daemon.lock"
export SWARM_DAEMON_LOG="$STATE/daemon.log"
# The reserved dev/test agent the session helper launches. Without it the daemon has no `fake`
# agent to run and the session the phone must observe cannot be created.
export SWARM_FAKE_AGENT_BIN="$OUT/bin/swarm-fake-agent"

log "relay"
# swarm-relay takes a CONFIG FILE and nothing else -- there is no --listen/--tls/--db. The
# invocation here used those three flags and would have died on its first host step, which is
# one of the proofs this script had never been run (ADR-007, PB-E2E-2 backfill).
cat > "$STATE/relay-config.json" <<JSON
{
  "listen": "127.0.0.1:8787",
  "tls_mode": "off",
  "db_path": "$STATE/relay.db",
  "sweep_interval": 30000000000,
  "quotas": { "mailbox_append_per_min": 600, "push_per_min": 600 }
}
JSON
"$OUT/bin/swarm-relay" --config "$STATE/relay-config.json" &
RELAY_PID=$!
# Everything this run started is torn down on ANY exit, including the [UNRUN] stop. The
# emulator needs its own verb (`adb emu kill`), and the SHIMS need killing by hand: a shim
# deliberately outlives the daemon that spawned it (ADR-001), so a killed daemon leaves the
# session's agent running for the rest of its scripted idle. They are matched on this run's own
# bin directory, so nothing outside it is touched.
cleanup() {
  kill "${RELAY_PID:-}" "${DAEMON_PID:-}" "${GW_PID:-}" "${TERM_PID:-}" "${PAIR_PID:-}" 2>/dev/null || true
  pkill -f "$OUT/bin/" 2>/dev/null || true
  adb emu kill >/dev/null 2>&1 || true
  rm -rf "${SOCKDIR:-}"
}
trap cleanup EXIT

# ---------------------------------------------------------------------------
# TLS, which is no longer optional. PB-NET-2 is now applied on every production dial path
# (ADR-007 B34/B37), and it refuses cleartext to anything but a LOOPBACK IP LITERAL. The old
# ws://10.0.2.2:8787 here is refused by the machine's own pairing rendezvous, measured:
#
#   remote pair: pair_start: open rendezvous: relay: cleartext ws:// refused; use wss://
#
# THE ADDRESS ALSO HAD TO CHANGE, and this is a second defect the scheme change exposes.
# 10.0.2.2 is the EMULATOR's alias for the host loopback; it means nothing on the host. But
# relay.json carries ONE relay_url that serves both ends -- the machine dials it AND it is
# copied verbatim into the pairing QR (PB-PAIR-7) -- so the address must be reachable from
# BOTH. The host's own LAN address is, from the host and from the emulator, and it is the
# topology docs/operations/relay-runbook.md already prescribes.
# ---------------------------------------------------------------------------
HOST_IP="${SWARM_E2E2_HOST_IP:-$(ipconfig getifaddr en0 2>/dev/null || hostname -I 2>/dev/null | awk '{print $1}')}"
[ -n "$HOST_IP" ] || { echo "cannot determine this host's LAN address; set SWARM_E2E2_HOST_IP"; exit 1; }
log "TLS terminator in front of the relay (host LAN address $HOST_IP)"

# A self-signed certificate, reissued per run. The SPKI pin replaces name verification, so the
# SAN is belt-and-braces rather than load-bearing -- see the runbook section 6.
openssl req -new -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes \
  -keyout "$STATE/relay.key" -out "$STATE/relay.crt" -days 90 \
  -subj "/CN=swarm-relay.local" \
  -addext "subjectAltName=DNS:swarm-relay.local,IP:$HOST_IP,IP:127.0.0.1" 2>/dev/null
PIN="$(openssl x509 -in "$STATE/relay.crt" -pubkey -noout |
  openssl pkey -pubin -outform der | openssl dgst -sha256 -binary | openssl base64)"

# scripts/relay-tls-terminator.py is a stdlib TLS-to-TCP pipe so this runbook is executable on
# a machine with nothing installed. It has no access control, no rate limiting, no supervision
# and no certificate reloading. DO NOT DEPLOY IT. In Phase C this is a real reverse proxy.
python3 "$REPO/scripts/relay-tls-terminator.py" \
  --listen "$HOST_IP:8443" --target 127.0.0.1:8787 \
  --cert "$STATE/relay.crt" --key "$STATE/relay.key" &
TERM_PID=$!
sleep 2

log "machine provisioning"
# The relay URL BOTH ends dial, and the pin the machine verifies it with. Every machine-side
# dial path -- the gateway sidecar, this CLI's owner connection and the daemon's pairing
# rendezvous -- reads the pin from the one relay.json parser (internal/remote/relaycfg).
RELAY_URL="wss://$HOST_IP:8443"
"$OUT/bin/swarm" remote init --relay-url "$RELAY_URL" --relay-pin "$PIN"
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
# "observes", "takes control" and "types" all need a session to exist on the machine, and this
# repository still has no non-interactive PRODUCT path that creates one: `swarm remote` has
# init/devices/revoke/regrant/pair/off/on/status and nothing that launches, the TUI refuses a
# non-terminal ("the TUI needs an interactive terminal"), and App.Launch (PB-APP-6) has no
# screen on the handset. Adding a launch verb was ruled out at closure and stays ruled out:
# product surface added so a demonstration can be automated is how a demonstration stops being
# about the product.
#
# scripts/e2e2session is therefore a TEST-ONLY helper, outside the shipped binaries, which
# speaks the daemon's EXISTING owner protocol over the same UDS the TUI uses and calls the same
# protocol.Client.Launch. The session it creates is indistinguishable to the daemon from one the
# TUI created, and nothing about the PHONE's half is simulated -- the five in-app actions below
# still run against the installed APK through its own controls.
#
# The override remains, because the smoke's contract has always been that this command is the
# operator's; the default merely makes the run reproducible without one.
SESSION_CMD="${SWARM_E2E2_SESSION_CMD:-"$OUT/bin/e2e2session" --cwd "$OUT/session"}"
mkdir -p "$OUT/session"
eval "$SESSION_CMD" | tee "$OUT/session-id.txt"
[ -s "$OUT/session-id.txt" ] || { echo "no session was created; the phone would observe an empty roster"; exit 1; }

# ---------------------------------------------------------------------------
# [UNRUN] EVERYTHING BELOW THIS LINE HAS NEVER BEEN EXECUTED.
#
# The phone cannot reach the relay yet, and it is this repository's own doing. PB-NET-2 is now
# applied on the handset's dial path too, and relay.TrustRootSourceFor makes Android
# TrustRootsPinned: a wss:// dial with no pin fails closed with relay.ErrPinRequired, and there
# is no channel to give the handset a pin -- the pairing QR has no field for one and no room for
# one (pairing.MaxRelayURLLen). That is ADR-007 residual 1.9.
#
# So the requirement that would have caught residual 1.9 end to end is the requirement residual
# 1.9 prevents from running. The remedy is the pin arriving over the pairing channel in
# pairing.MachinePayload, alongside the machine keys the phone already pins there.
#
# THE STOP IS DELIBERATE AND MUST NOT BE REMOVED TO GET A GREEN RUN. Everything above it has
# been executed and works: the relay, the terminator, the pin, the machine provisioning, the
# daemon, the emulator boot, the APK build and install, and the session. Everything below is
# written to run the moment the pin lands and has been checked no further than that.
# ---------------------------------------------------------------------------
if [ "${SWARM_E2E2_PHONE_READY:-0}" != "1" ]; then
  cat <<'BLOCKED'

[UNRUN] PB-E2E-2 stops here, and stops HONESTLY rather than reporting a pass.

  The handset has no way to verify the relay's certificate, so its first dial is refused with
  relay.ErrPinRequired before the pairing handshake starts. See ADR-007 residual 1.9.

  Everything before this point ran. Nothing after it has ever run.

  Re-run with SWARM_E2E2_PHONE_READY=1 once pairing.MachinePayload carries the relay pin and
  the phone persists it. Until then this exit is the correct outcome and the artifacts in the
  output directory cover the MACHINE half only.

BLOCKED
  exit 2
fi

log "pair, SAS, observe, take control, type"
# THE PAIR COMMAND BLOCKS UNTIL THE PHONE CONNECTS, so it cannot be run synchronously and then
# grepped for the QR the phone needs in order to connect -- the previous form of this step was
# an ordering that could not complete. It runs in the background and the QR is polled out of its
# output instead.
#
# `--yes` never existed (the only flag is --capability). It was never needed either: the machine's
# SAS gate reads from STDIN, so the operator's answer is piped. The six symbols it prints are
# ASSERTED against the six the phone logs -- see the "SAS matches" block after the instrumented
# run, which fails this script if they differ.
( printf 'y\n' | "$OUT/bin/swarm" remote pair > "$OUT/pair.txt" 2>&1 ) &
PAIR_PID=$!

# The QR payload the machine printed, handed to the app. It is passed as an instrumentation
# argument rather than baked in: a payload in the repository would be a pairing nobody minted.
#
# It is WRAPPED across terminal-width lines by printPairingQR, so it is reassembled rather than
# grepped as one token -- and its scheme is `swarm-pair:1:`, not the `swarm://` the previous
# form looked for, which is a pattern that could never have matched anything this CLI prints.
QR=""
for _ in $(seq 1 30); do
  QR="$(awk '/^Or enter this pairing code manually:$/{f=1;next} /^Scan this QR/{f=0} f{printf "%s",$0}' "$OUT/pair.txt" 2>/dev/null)"
  [ -n "$QR" ] || QR="$(grep -Eo '^swarm-pair:[^[:space:]]+' "$OUT/pair.txt" 2>/dev/null | head -1)"
  [ -n "$QR" ] && break
  sleep 1
done
[ -n "$QR" ] || { echo "no pairing payload in $OUT/pair.txt; the machine did not mint a QR"; cat "$OUT/pair.txt"; exit 1; }

./gradlew --no-daemon :app:connectedDebugAndroidTest \
  -Pandroid.testInstrumentationRunnerArguments.class=dev.swarm.phone.PbE2E2PairAndTypeTest \
  -Pandroid.testInstrumentationRunnerArguments.swarmQr="$QR" \
  -Pandroid.testInstrumentationRunnerArguments.swarmRelay="$RELAY_URL"
adb logcat -d -s SwarmE2E2 | tee "$OUT/phone-sas.txt"

# ---------------------------------------------------------------------------
# "SAS matches" -- ASSERTED, not merely captured.
#
# Capturing both values into two files is not a comparison, and this clause is the one that
# says a man-in-the-middle is absent. Both ends derive their six symbols independently from the
# Noise channel binding, so they agree only if nothing sits between the phone and the machine.
#
# WHAT THIS DOES NOT PROVE, said here rather than left to be assumed. The machine's answer at
# its own SAS gate was piped by this script before the comparison ran, so what is asserted is
# that the two ends AGREED -- not that a human refused a mismatch. A mismatch fails the run
# after the fact rather than declining the pairing at the gate. Making the answer conditional
# means feeding the machine's stdin only after the phone has logged its symbols, which is a
# further change to this step and has not been made. The clause the operator still owns on a
# real handset is the refusal; this asserts the agreement.
# ---------------------------------------------------------------------------
MACHINE_SAS="$(sed -n 's/^Verify these emoji match your phone: //p' "$OUT/pair.txt" | head -1)"
PHONE_SAS="$(sed -n 's/.*phone SAS: //p' "$OUT/phone-sas.txt" | head -1)"
[ -n "$MACHINE_SAS" ] || { echo "the machine printed no SAS; PB-E2E-2's 'SAS matches' clause cannot be checked"; exit 1; }
[ -n "$PHONE_SAS" ] || { echo "the phone logged no SAS under the SwarmE2E2 tag; PB-E2E-2's 'SAS matches' clause cannot be checked"; exit 1; }
if [ "$MACHINE_SAS" != "$PHONE_SAS" ]; then
  echo "SAS MISMATCH -- the two ends did not derive the same short authentication string."
  echo "  machine: $MACHINE_SAS"
  echo "  phone:   $PHONE_SAS"
  echo "This is the signal the SAS exists to give. Something sat between the phone and the machine."
  exit 1
fi
echo "SAS matches on both ends: $MACHINE_SAS"

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
