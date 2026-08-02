// Package gate holds the Go-side build gate for the Android client (Phase B
// slice S13). It carries no production code: every assertion lives in a _test.go
// file and runs under the repository's existing `go test ./...` gate, so the
// Android module's toolchain pin, build commands, signing configuration, Gradle
// wrapper, CI lane, SDK matrix, connectivity policy and theme cannot drift
// without a Go test failing.
//
// Two lanes:
//
//   - untagged  — file/content assertions only. No Android SDK, no JDK, no
//     Gradle. Runs on every runner, including the plain ubuntu `test` job.
//   - androidgate (build tag) — assertions that execute the real toolchain:
//     source the pin in a scrubbed shell, build the AAR, build the debug APK,
//     run `./gradlew lint test`. Runs only in the Android CI lane and locally.
//     PB-TOOL-7's test asserts that lane actually invokes this tag, so the
//     expensive half cannot become an orphan.
//
// Requirements covered here: PB-TOOL-1..7, PB-RUN-1, PB-RUN-3, PB-RUN-4 and the
// structural half of PB-TOK-4. PB-RUN-2, PB-RUN-5 and PB-TOK-4's behavioural
// half are Kotlin/Robolectric tests under android/app/src/test.
package gate
