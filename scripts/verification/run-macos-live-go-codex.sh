#!/bin/sh
# Prepare private native fixture executables. The release builds have no fixed-ID hook.
set -eu
test "$#" -ge 1 && test "$#" -le 3
mode=${2:-}
case "$mode" in
  --cross-provider) test "$#" -eq 3 ;;
  ""|--relocation|--stopped-resume) test "$#" -le 2 ;;
  *) exit 2 ;;
esac
test "$(uname -sm)" = "Darwin arm64"
vendor=$1
repo=$(git -C "$(dirname "$0")" rev-parse --show-toplevel)
oracle=f26648d69086e329132ac7849954d2f29a895387
umask 077
build=$(mktemp -d /private/tmp/cx-build.XXXXXX)
run=$(mktemp -d /private/tmp/cx-run.XXXXXX)
mkdir "$build/go" "$build/rust" "$run/bin"
echo "build scratch $build"
echo "native fixture $run"
if [ "$mode" != --cross-provider ]; then
  git -C "$repo" archive "$oracle" | tar -x -C "$build/go"
fi
git -C "$repo" archive HEAD | tar -x -C "$build/rust"
python3 - "$build" "$mode" <<'PY'
from pathlib import Path
import sys

base = Path(sys.argv[1])
go = base / "go/internal/daemon/identity.go"
rust = base / "rust/crates/swarm/src/supervisor/session.rs"
cross_provider = sys.argv[2] == "--cross-provider"
if not cross_provider:
    source = go.read_text()
    signature = "func generateID() string {"
    assert source.count(signature) == 1
    source = source.replace('"encoding/base32"', '"encoding/base32"\n\t"os"', 1)
    go.write_text(source.replace(signature, signature + '''
    if id := os.Getenv("SWARM_FIXTURE_SESSION_ID"); id != "" { return id }
''', 1))
source = rust.read_text()
allocation = "let id = runtime::random_id()?;"
assert source.count(allocation) == 1
variable = "SWARM_FIXTURE_CODEX_ID" if cross_provider else "SWARM_FIXTURE_SESSION_ID"
guard = 'provider.agent == "codex" && ' if cross_provider else ""
rust.write_text(source.replace(allocation, f'''let id = match std::env::var("{variable}") {{
                Ok(id) if {guard}runtime::valid_id(&id) => id,
                _ => runtime::random_id()?,
            }};''', 1))
PY
target=$build/target
(cd "$build/rust" && CARGO_TARGET_DIR="$target" cargo build --offline --locked -p swarm)
if [ "$mode" = --cross-provider ]; then
  exec python3 -Bu "$repo/scripts/verification/rust_cross_provider_handoff.py" "$target/debug/swarm" "$3" "$vendor"
fi
(cd "$build/go" && GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -o "$build/go-oracle" ./cmd/swarm)
if [ "$mode" = --relocation ]; then
  witness="$repo/scripts/verification/codex_relocation.py"
  python3 -Bu "$witness" --binary "$build/go-oracle" --provider "$vendor" --implementation go
  exec python3 -Bu "$witness" --binary "$target/debug/swarm" --provider "$vendor" --implementation rust
fi
cp -p "$vendor" "$run/bin/codex"
if [ "$mode" = --stopped-resume ]; then
  exec python3 -Bu "$repo/scripts/verification/stopped_go_codex_resume.py" "$build/go-oracle" "$target/debug/swarm" "$run"
fi
exec python3 -Bu "$repo/scripts/verification/live_go_codex.py" "$build/go-oracle" "$target/debug/swarm" "$run" 0.172.0 --cold
