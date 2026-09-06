#!/bin/sh
set -eu

tmp=$(mktemp -d "${TMPDIR:-/tmp}/swarm-firestore-gate.XXXXXX")
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
cat >"$tmp/go" <<'EOF'
#!/bin/sh
for test_name in \
	TestFirestoreRepositorySharedClaims \
	TestFirestoreServerRegistrationIsSharedAndRejectsBodyMismatch \
	TestFirestoreWakeLeaseCASAndTokenGeneration \
	TestFirestoreRetentionRechecksAndCascadesBoundedly \
	TestRetentionSubcommandRunsOneBoundedFirestoreSweep
do
	if [ "${MODE:-pass}" = missing ] && [ "$test_name" = "${TARGET:-}" ]; then
		continue
	fi
	result=PASS
	if [ "${MODE:-pass}" = skip ] && [ "$test_name" = "${TARGET:-}" ]; then
		result=SKIP
	fi
	echo "--- $result: $test_name (0.00s)"
done
echo 'PASS'
test "${MODE:-pass}" != fail
EOF
chmod +x "$tmp/go"

gate=$(dirname "$0")/ci-firestore-go.sh
PATH="$tmp:$PATH" FIRESTORE_EMULATOR_HOST=127.0.0.1:1 sh "$gate" >/dev/null
for test_name in \
	TestFirestoreRepositorySharedClaims \
	TestFirestoreServerRegistrationIsSharedAndRejectsBodyMismatch \
	TestFirestoreWakeLeaseCASAndTokenGeneration \
	TestFirestoreRetentionRechecksAndCascadesBoundedly \
	TestRetentionSubcommandRunsOneBoundedFirestoreSweep
do
	for mode in skip missing
	do
		if PATH="$tmp:$PATH" FIRESTORE_EMULATOR_HOST=127.0.0.1:1 MODE=$mode TARGET=$test_name \
			sh "$gate" >/dev/null 2>&1
		then
			echo "anti-vacuity gate accepted $mode test: $test_name" >&2
			exit 1
		fi
	done
done
if PATH="$tmp:$PATH" FIRESTORE_EMULATOR_HOST=127.0.0.1:1 MODE=fail \
	sh "$gate" >/dev/null 2>&1
then
	echo 'gate accepted failing go test with pass-looking output' >&2
	exit 1
fi
