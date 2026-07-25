package gate

import (
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// PB-RUN-1 -- "minSdk and targetSdk are chosen and recorded with a
// supported-version matrix (the gomobile -androidapi floor is the NDK's, not the
// app's). Recorded; build enforces it."
//
// "Recorded" is a matrix file, not a sentence: android/supported-versions.tsv,
// one row per API level the app claims to support. A matrix makes two things
// checkable that prose does not -- that the claimed range is contiguous (a gap
// is an API level nobody decided about) and that its endpoints ARE minSdk and
// targetSdk rather than numbers that merely appear nearby.
//
// "Build enforces it" is asserted twice: here, that the build files contain no
// competing integer literal, and in the tagged lane, that the built APK's own
// badging reports the pinned values.

func matrixPath(t *testing.T) string {
	return filepath.Join(androidRoot(t), "supported-versions.tsv")
}

type sdkRow struct {
	api     int
	release string
	role    string // "min", "supported" or "target"
}

func readSDKMatrix(t *testing.T) []sdkRow {
	t.Helper()
	body := readFileOrFail(t, matrixPath(t), "PB-RUN-1")
	var rows []sdkRow
	for n, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 3 {
			t.Fatalf("PB-RUN-1: %s:%d has %d tab-separated fields, want 3 "+
				"(api\\trelease\\trole): %q", mustRel(t, matrixPath(t)), n+1, len(f), line)
		}
		api, err := strconv.Atoi(strings.TrimSpace(f[0]))
		if err != nil {
			t.Fatalf("PB-RUN-1: %s:%d api %q is not an integer", mustRel(t, matrixPath(t)), n+1, f[0])
		}
		rows = append(rows, sdkRow{api: api, release: strings.TrimSpace(f[1]), role: strings.TrimSpace(f[2])})
	}
	if len(rows) == 0 {
		t.Fatalf("PB-RUN-1: %s declares no supported API levels; every assertion over it "+
			"would pass vacuously", mustRel(t, matrixPath(t)))
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].api < rows[j].api })
	return rows
}

func TestPBRUN1_SupportedVersionMatrixIsRecorded(t *testing.T) {
	rows := readSDKMatrix(t)

	var mins, targets int
	for _, r := range rows {
		switch r.role {
		case "min":
			mins++
		case "target":
			targets++
		case "supported":
		default:
			t.Errorf("PB-RUN-1: API %d has role %q; legal roles are min, supported, target",
				r.api, r.role)
		}
		if r.release == "" || r.release == "-" {
			t.Errorf("PB-RUN-1: API %d records no Android release; the matrix is for humans "+
				"deciding what to test on", r.api)
		}
	}
	if mins != 1 {
		t.Errorf("PB-RUN-1: %d rows are marked `min`, want exactly 1", mins)
	}
	if targets != 1 {
		t.Errorf("PB-RUN-1: %d rows are marked `target`, want exactly 1", targets)
	}
	if rows[0].role != "min" {
		t.Errorf("PB-RUN-1: the lowest row (API %d) is not the `min` row", rows[0].api)
	}
	if rows[len(rows)-1].role != "target" {
		t.Errorf("PB-RUN-1: the highest row (API %d) is not the `target` row",
			rows[len(rows)-1].api)
	}

	// Contiguity. A matrix that lists 26 and 35 and nothing between claims to
	// support nine API levels it never considered.
	for i := 1; i < len(rows); i++ {
		if rows[i].api != rows[i-1].api+1 {
			t.Errorf("PB-RUN-1: the supported range skips from API %d to API %d. Every "+
				"level in between would install the app; each one is a decision",
				rows[i-1].api, rows[i].api)
		}
	}
}

func TestPBRUN1_MatrixEndpointsAreThePinnedMinAndTargetSdk(t *testing.T) {
	rows := readSDKMatrix(t)
	env := sourcePin(t, repoRoot(t))

	minSdk := pinInt(t, env, "SWARM_ANDROID_MIN_SDK")
	targetSdk := pinInt(t, env, "SWARM_ANDROID_TARGET_SDK")
	compileSdk := pinInt(t, env, "SWARM_ANDROID_COMPILE_SDK")

	if rows[0].api != minSdk {
		t.Errorf("PB-RUN-1: the matrix's lowest supported API is %d but SWARM_ANDROID_MIN_SDK "+
			"is %d", rows[0].api, minSdk)
	}
	if got := rows[len(rows)-1].api; got != targetSdk {
		t.Errorf("PB-RUN-1: the matrix's `target` row is API %d but SWARM_ANDROID_TARGET_SDK "+
			"is %d", got, targetSdk)
	}
	if compileSdk < targetSdk {
		t.Errorf("PB-RUN-1: compileSdk=%d is below targetSdk=%d; the app cannot opt into "+
			"behaviour it cannot compile against", compileSdk, targetSdk)
	}

	// PB-RUN-2 depends on this: POST_NOTIFICATIONS exists only from API 33. If
	// minSdk is below 33 the permission gate MUST have an API-level branch, and
	// the Robolectric test must exercise both sides of it.
	if minSdk < 33 {
		t.Logf("PB-RUN-1/PB-RUN-2: minSdk=%d is below 33, so POST_NOTIFICATIONS does not "+
			"exist on the lowest supported devices. PermissionGateTest must cover both "+
			"sides of that boundary", minSdk)
	}
}

// TestPBRUN1_BuildDoesNotCarryCompetingSdkLiterals. "Build enforces it" fails
// the moment the build file states its own number: the matrix then records one
// decision and the APK ships another, and nothing notices.
func TestPBRUN1_BuildDoesNotCarryCompetingSdkLiterals(t *testing.T) {
	build := moduleBuildFile(t)
	body := readFileOrFail(t, build, "PB-RUN-1")
	for _, key := range []string{"minSdk", "targetSdk", "compileSdk"} {
		re := regexp.MustCompile(key + `\s*=?\s*\(?\s*\d+`)
		if m := re.FindString(body); m != "" {
			t.Errorf("PB-RUN-1: %s hard-codes `%s`. The value must come from the pinned "+
				"toolchain so android/supported-versions.tsv and the shipped APK cannot "+
				"disagree", mustRel(t, build), strings.TrimSpace(m))
		}
	}
}
