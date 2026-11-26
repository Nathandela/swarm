# Terminal restore ticker

Swarm uses Bubble Tea's terminal handoff when a user attaches to a session and returns to the board. In v2.0.2, the stopping renderer can stop the shared ticker after its successor resets it. The board paints before release and can remain blank after restore. [Upstream v2.1.2](https://github.com/charmbracelet/bubbletea/blob/v2.1.2/tea.go#L1519-L1536) gives each renderer its own ticker.

This branch selects v2.1.3. It is the smallest published version with the ticker fix and the renderer crash fix that upstream recommends for apps displaying content they do not control. Its manifest adds one ANSI patch beyond v2.1.2. The six companion module updates are the versions required by its published manifest. The Go floor is unchanged. `go mod tidy -diff` and independent inspection of the module diff passed. [Upstream v2.1.3 release](https://github.com/charmbracelet/bubbletea/releases/tag/v2.1.3)

## Controlled result

The unchanged `TestTerminalRestoreRedrawsUnchangedBoard` failed all eight runs with v2.0.2 and passed all twenty with v2.1.3. Both runs used the same disposable composition of cleanup `f366e91c` and worktree fix `0100e86f`, with Go 1.26.10 on Darwin arm64 and `GOMAXPROCS=1`. Only the module graph changed between runs. A prior disposable ticker-only replacement passed all eight one-thread runs on Darwin amd64.

## Reproduction and remaining gates

Start from a clean Swarm clone whose Git objects include all three heads. The commands below build the combined source in disposable scratch.

```sh
git fetch --no-tags origin main \
  swarm/dsp-test-suite-cleanup-and-refactor-sp47avkj5iusn6zk \
  swarm/dsp-nested-worktree-investigation-6emzxb4em42ed2bh \
  review/terminal-restore-ipoo
combined_dir=$(mktemp -d /tmp/sw-combined.XXXXXX)
git clone --shared --no-checkout . "$combined_dir"
git -C "$combined_dir" switch --detach 0100e86f
git diff --binary 7f67c067 f366e91c | git -C "$combined_dir" apply
git diff --binary 7f67c067 1a1c86bf -- go.mod go.sum > "$combined_dir/runtime.patch"
cd -P "$combined_dir"
umask 022
GOMAXPROCS=1 GOFLAGS=-p=1 go test -count=8 -timeout 5m \
  -run '^TestTerminalRestoreRedrawsUnchangedBoard$' ./internal/tui
# The pinned version above should fail. Apply the dependency candidate before continuing.
git apply runtime.patch
rm runtime.patch
GOMAXPROCS=1 GOFLAGS=-p=1 go test -v -count=20 -timeout 5m \
  -run '^TestTerminalRestoreRedrawsUnchangedBoard$' ./internal/tui
```

The following gates remain pending on the upgraded graph.

```sh
go mod tidy -diff
go build ./...
go vet ./...
golangci-lint run
go test -race -p 2 -count=1 ./internal/tui ./internal/attach ./internal/vt
go test -race -p 2 -count=1 \
  -run 'Test(TUI_TouchReturnPreservesLiveSession|TUI_OpensAndRestoresOverPTY|AttachDialer_AttachesAfterAutoUpgrade|DaemonRestarter_RestartsRealDaemonAndReconnects)' ./cmd/swarm
go test -count=1 -run '^TestFirstPaintGate_RealDaemon_FiftySessions_P95$' ./internal/tui
go test -count=1 -run '^TestE2E_LiveShimKeystrokeEchoLatencyP95$' ./internal/e2e
go test -race -p 2 -count=1 ./internal/worktree
go test -race -p 2 -count=1 \
  -run 'Test(CheckoutInventory|.*Worktree|.*LaunchUsesResolvedCwd|.*DeleteDiscussion|CompatMatrix_OldShimNewDaemon|LaunchRefuses.*State.*|WorkspaceSymlinkParentResolvesBeforeCleaning|B94_EveryExportedSymbolIsReachableFromProduction)' \
  ./cmd/swarm ./internal/daemon ./internal/protocol ./internal/skeleton ./internal/verify
go test -tags integration -race -p 2 -count=1 ./internal/engine/
go test -race -p 2 -count=1 -timeout 35m ./...
```

The Mac had roughly 1 GiB available after the focused restore run. The dispatcher confirmed no eligible cleanup space and no verified alternate runner. Batch SSH to the approved VM `tf_nathan` was rejected. Qualification remains blocked under `agents-tracker-ipoo` and gates the combined release `agents-tracker-2qb2`. The dispatcher owns exact-tag verification and publication once the gates are green.
