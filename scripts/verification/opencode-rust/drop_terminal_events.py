"""Inject terminal SSE loss into a disposable OpenCode bridge source tree."""

import sys
import tempfile
from pathlib import Path


asset = Path(sys.argv[1]).resolve(strict=True)
scratch = (Path(tempfile.gettempdir()).resolve(), Path("/tmp").resolve())
assert asset.name == "tui.mjs" and any(asset.is_relative_to(root) for root in scratch), asset
source = asset.read_text()
emit = "  function emit(event) {\n    observeReply(event)\n"
assert source.count(emit) == 1
asset.write_text(source.replace(emit, emit + '''    const info = event?.properties?.info
    if (event?.type === "message.updated" && info?.role === "assistant" &&
        (info.time?.completed || info.error)) {
      fs.appendFileSync(path.join(path.dirname(options.bindingFile), "fixture-terminal-events-suppressed.jsonl"), info.id + "\\n", {mode: 0o600})
      return
    }
'''))
