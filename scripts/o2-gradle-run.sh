#!/usr/bin/env bash
# Run the Android unit-test suite from a subagent shell, serialized.
#
# WHY THIS EXISTS. Three facts about running Gradle here cost an hour each the first time and are
# encoded rather than re-discovered:
#
#   1. A subagent shell has neither JAVA_HOME nor ANDROID_HOME. Without them the wrapper reports
#      "Unable to locate a Java Runtime", then "SDK location not found". Setting them here avoids
#      writing a local.properties into the worktree.
#   2. The house serialization guard `while pgrep -x java; do sleep 15; done` DOES NOT TERMINATE.
#      An IDLE Gradle daemon is also a java process -- one sat resident for 80 minutes at 0% CPU
#      -- so the loop waits forever on something that is not a build. `--stop` then `--no-daemon`
#      is the terminating form of the same intent: it leaves no daemon behind for the next run to
#      block on.
#   3. `./gradlew ... | tail` EXITS 0 WHEN GRADLE EXITS 1. Never pipe the wrapper without
#      capturing its own status; this script tees to a log and reports ${PIPESTATUS[0]}.
#
# Verify results by COUNTING the JUnit XML and checking mtimes, never by exit code alone. This
# script prints that count. It never deletes build/ or test-results/.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export JAVA_HOME="${JAVA_HOME:-/usr/local/Cellar/openjdk@21/21.0.12/libexec/openjdk.jdk/Contents/Home}"
export ANDROID_HOME="${ANDROID_HOME:-/usr/local/share/android-commandlinetools}"
export PATH="$JAVA_HOME/bin:$PATH"

cd "$ROOT/android" || exit 1

LOG="${TMPDIR:-/tmp}/o2-gradle-$(date +%Y%m%d-%H%M%S).log"
echo "log: $LOG"

./gradlew --stop >/dev/null 2>&1
./gradlew --no-daemon "${@:-test}" 2>&1 | tee "$LOG"
status=${PIPESTATUS[0]}
echo "gradle exit status: $status"

for variant in testDebugUnitTest testReleaseUnitTest; do
  dir="app/build/test-results/$variant"
  [ -d "$dir" ] || { echo "MISSING $dir"; continue; }
  n=$(find "$dir" -name 'TEST-*.xml' | wc -l | tr -d ' ')
  newest=$(find "$dir" -name 'TEST-*.xml' -newermt '-1 hour' | wc -l | tr -d ' ')
  echo "$variant: $n result files, $newest written in the last hour"
done

exit "$status"
