# Combined action buttons — 2026-10-28

Tracking: `agents-tracker-5779`. The owner requests `[Name (shortcut)]` controls without a repeated footer legend, and a pointer entry point for every available Swarm selected-agent command. This is a source integration follow-up to v0.25.0; it does not record another release or installation.

## Result and boundaries

One board row contains whole buttons, with New, Open/Show/Expand (Resume for visible ended sessions), and More taking priority. At 24 columns New moves into More. Wider terminals expose Rename, Tag, Handoff, Kill/Delete and global controls as space permits. More contains every available agent action plus New, Options, Attention and Quit. Back/Up/Down are pointer controls, wrapping onto two footer rows below 32 columns while retaining one content row at 24×4.

Rendering and hit targets share display-cell bounds. A gutter cannot activate a control. Selection, eligibility, resize and screen transitions invalidate pending gestures. More retains its captured session and revalidates its original running/ended eligibility. Open from More bypasses unrelated folded board focus. Forms, rename/tag editing and named confirmations keep their existing keyboard and native-selection ownership.

The attached active return label is `[swarm (ctrl+q)]` with the configured key. The complete dynamic cell width controls capture eligibility and the hit target; a partial label is never active. Mode authority, gesture ownership, default-off preference and session continuity remain unchanged. Unsupported/narrow attach retains the existing plain keyboard fallback; a long name can still obscure that plain hint at very narrow widths. Ctrl+Q continues to work.

The [interaction amendment](../../specifications/mobile-terminal-touch-plan.md#compact-action-buttons-amendment--2026-10-28), ADR-035, README and design reference are updated. Historical v0.25.0 release evidence remains unchanged.

## Test-first and independent review

New tests first failed against the old labels, missing direct agent controls, narrow-menu reachability and inactive displayed shortcuts. Attach tests first failed at the new full-label threshold and rightmost button cell. The final tests cover default Ctrl+Q and the shorter DEL label, Unicode session names, widths immediately around both thresholds, and the following non-button cell.

Sol implemented the isolated attached-label change and independently reviewed the TUI changes. Astra independently reviewed the connected layout/input code and documentation. Both found that captured More → Open could expand a folded board section instead of attaching its captured target. The fix bypasses board navigation for an explicit captured command. A regression passes on the final source and fails when that guard is removed using an external Go overlay; the shared checkout was never mutated for the negative control. Final reviews reported no remaining blocker.

New TUI coverage checks every width 24–160 for running, ended, empty and folded boards, painted button cells versus hit targets, inert gutters, existing action handlers, cancelled selection/status/resize gestures, captured targets, and pointer reachability for all nine applicable commands in both running and ended menus at 24×4. Directly affected screen goldens, style pins and CLI smoke wait labels were updated without weakening their assertions.

## Executed gates

Host: Go 1.26.5, darwin/amd64. Tests use shared caches, short task scratch and the packages' existing supervision where present. Final test commands remove inherited `SWARM_*` variables before launch so fixtures use their own sockets and hook context.

| Gate | Result |
|---|---|
| `go build ./...` | PASS, 7.713 s |
| `go vet ./...` | PASS, 3.761 s |
| `golangci-lint run` | PASS; repeated after the final test additions |
| `go test -p=1 -count=1 ./internal/tui ./internal/attach ./cmd/swarm` | PASS; package times 38.760 / 2.756 / 72.503 s |
| Complete `internal/tui` race suite | PASS, 61.949 s |
| Complete `internal/attach` race suite after DEL cases | PASS, 5.582 s |
| Complete `cmd/swarm` race suite | PASS, 76.471 s including build/runner overhead |
| Real daemon + shim + client PTY continuity | PASS in complete normal/race CLI suites; keyboard and touch returns, agent progress preserved |
| Existing real-daemon first-paint gate, 50 sessions / 25 runs | PASS, local p95 4.242042 ms against 100 ms |
| Existing 1,000-sample local added-input echo gate, default / touch | PASS, p95 6.625 / 10.125 µs against 10 ms |
| `git diff --check` | PASS |

The initial broader affected-package run was not green: old-label assertions failed and were corrected; inherited `SWARM_SHIM_HOOK_SOCK` contaminated unrelated hook fixtures. That run also observed one `TempDir RemoveAll` “directory not empty” failure in the touch smoke teardown. Two subsequent complete CLI runs (scrubbed race and normal) passed. The separately unproven fixture teardown issue is retained as `agents-tracker-gbt7`; no application workaround was introduced. No data-race diagnostic was observed.

The last lint pass also caught a test-only `if` chain after the DEL matrix addition; changing it to a tagged switch restored lint, and the configured-key cases passed under race again. Final log SHA-256 values are retained here before retiring disposable task scratch:

```text
initial affected race (failed) 4468983e1fe6e29767f614ecb5459d2356cb7654b99259c502c2e5dc0cbaf029
final TUI race                5f523a2efa24850935d6cef94b732d80ecf37deb47e7afcdba74816f41a7a0b8
final attach race             2fbcf860d6f1b768f59eb1c21e5f3cfc2428839fb1e54e5326e2d69432a4b231
final CLI race                d963b0bc24025537607d6c2472308b32a7d01df0e93613c340a32ecfd3a278d3
final affected normal         e7773483a9edc3d7f34c0e2a23438f13dd219a74a9d471371083ec8f5fa2e86f
first paint                   58f0d263c64ee454914c30744cee653634260b845a9521815deee3eadf53487d
input latency                 2e0f9ff00fdd10a30c6fa0b15480053046391eeded69a25c6c579efaad3af1d2
final lint                    e92606b0bf483111dff0a120c315ea165821348f31365020e2468a0059095c47
```

## Explicit limits

The whole-repository normal/race suites were not repeated for this follow-up; complete changed-package suites and repository-wide static gates were run. No new tag, hosted release gate or host upgrade was initiated. Physical Termux/iOS SSH/tmux behavior remains unqualified under the existing handset gates. These local timing measurements exclude network RTT, handset painting and thumb accuracy. The default-off attach preference and existing unsupported-mode fallback remain in force.
