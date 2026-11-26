# Terminal restore ticker

Swarm uses Bubble Tea's terminal handoff when a user attaches to a session and returns to the board. In v2.0.2, the stopping renderer can stop the shared ticker after its successor resets it. The board paints before release and can remain blank after restore.

On a disposable checkout combining cleanup `f366e91c` and worktree review `0100e86f`, the pinned dependency failed all eight runs of the unchanged `TestTerminalRestoreRedrawsUnchangedBoard` with Go 1.26.10 on Darwin arm64 and `GOMAXPROCS=1`. A prior disposable ticker-only replacement passed all eight one-thread runs on Darwin amd64. [Upstream v2.1.2](https://github.com/charmbracelet/bubbletea/blob/v2.1.2/tea.go#L1519-L1536) gives each renderer its own ticker.

This branch selects v2.1.3. It is the smallest published version with the ticker fix and the renderer crash fix that upstream recommends for apps displaying content they do not control. Its manifest adds one ANSI patch beyond v2.1.2. The six companion module updates are the versions required by its published manifest. `go mod tidy -diff` and independent inspection of the module diff passed. [Upstream v2.1.3 release](https://github.com/charmbracelet/bubbletea/releases/tag/v2.1.3)

## Qualification status

Host storage exhaustion prevented compilation and execution of the new module graph. `agents-tracker-ipoo` remains open and blocks release follow-up `agents-tracker-2qb2`. The cleanup audit and nested-worktree fix remain on their separate branches.

For the combined candidate, run the unchanged restore test with `GOMAXPROCS=1 go test -count=20 -run '^TestTerminalRestoreRedrawsUnchangedBoard$' ./internal/tui`. Run the full attach, TUI, and VT packages under race, real CLI PTY attach and returns, the non-race first-paint and live-shim echo gates, and the worktree/policy/rollback/retained-data checks on the same source. Build, vet and lint the combined graph. The dispatcher owns the exact-tag release and publication after those checks pass.
