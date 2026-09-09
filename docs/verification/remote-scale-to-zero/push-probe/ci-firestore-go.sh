#!/bin/sh
set -eu

: "${FIRESTORE_EMULATOR_HOST:?Firestore emulator is required}"
output=$(mktemp "${TMPDIR:-/tmp}/swarm-firestore-go.XXXXXX")
trap 'rm -f "$output"' EXIT HUP INT TERM

if ! go test -race ./internal/pushgw ./cmd/swarm-pushgw -count=1 -timeout 120s -v >"$output" 2>&1; then
	cat "$output"
	exit 1
fi
cat "$output"
for test_name in \
	TestFirestoreRepositorySharedClaims \
	TestFirestoreServerRegistrationIsSharedAndRejectsBodyMismatch \
	TestFirestoreWakeLeaseCASAndTokenGeneration \
	TestFirestoreRetentionRechecksAndCascadesBoundedly \
	TestRegistrationExpiry_CompletedAuthorityOutlivesAttemptWindow/firestore \
	TestRegistrationAuthority_StaleLeaseCannotComplete/firestore \
	TestRegistrationAuthority_CorruptCompletedHalfFailsClosed/firestore \
	TestRegistrationAuthority_RetentionDeletesExactCompletedHalf/firestore \
	TestRegistrationAuthority_ReplayTouchesBeforeRetention/firestore \
	TestRegistrationAuthority_RejectsUnknownDigestRevision/firestore \
	TestRegistrationAuthority_RetentionRejectsUnknownRevision/pending/firestore \
	TestRegistrationAuthority_RetentionRejectsUnknownRevision/completed/firestore \
	TestRetentionSubcommandRunsOneBoundedFirestoreSweep
do
	grep -Fq -- "--- PASS: $test_name " "$output" || {
		echo "required Firestore test did not pass: $test_name" >&2
		exit 1
	}
done
