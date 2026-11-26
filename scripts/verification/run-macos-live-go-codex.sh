#!/bin/sh
# Prepare private native fixture executables. The release builds have no fixed-ID hook.
set -eu
test "$#" -ge 1 && test "$#" -le 2
mode=${2:-}
case "$mode" in ""|--relocation) ;; *) exit 2 ;; esac
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
git -C "$repo" archive "$oracle" | tar -x -C "$build/go"
git -C "$repo" archive HEAD | tar -x -C "$build/rust"
python3 - "$build" <<'PY'
from pathlib import Path
import sys

base = Path(sys.argv[1])
go = base / "go/internal/daemon/identity.go"
rust = base / "rust/crates/swarm/src/supervisor/session.rs"
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
rust.write_text(source.replace(allocation, '''let id = match std::env::var("SWARM_FIXTURE_SESSION_ID") {
                Ok(id) if runtime::valid_id(&id) => id,
                _ => runtime::random_id()?,
            };''', 1))
PY
(cd "$build/go" && GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -o "$build/go-oracle" ./cmd/swarm)
(cd "$build/rust" && CARGO_TARGET_DIR="$build/target" cargo build --offline --locked -p swarm)
if [ "$mode" = --relocation ]; then
  witness="$repo/scripts/verification/codex_relocation.py"
  python3 -Bu "$witness" --binary "$build/go-oracle" --provider "$vendor" --implementation go
  exec python3 -Bu "$witness" --binary "$build/target/debug/swarm" --provider "$vendor" --implementation rust
fi
cp -p "$vendor" "$run/bin/codex"
exec python3 -Bu "$repo/scripts/verification/live_go_codex.py" "$build/go-oracle" "$build/target/debug/swarm" "$run" 0.172.0 --cold
