"""Inject terminal SSE loss into a disposable OpenCode bridge source tree."""

import sys
import tempfile
from pathlib import Path


asset = Path(sys.argv[1]).resolve(strict=True)
scratch = (Path(tempfile.gettempdir()).resolve(), Path("/tmp").resolve())
assert asset.name == "tui.mjs" and any(asset.is_relative_to(root) for root in scratch), asset
mode = sys.argv[2] if len(sys.argv) > 2 else "drop"
assert mode in ("drop", "incomplete", "conflict")
source = asset.read_text()
snapshot = "        const hierarchy = await activityScope(snapshot, check)\n"
assert source.count(snapshot) == 1
if mode != "drop":
    source = source.replace(snapshot, snapshot + """
        if (fixtureTerminal && page.messages.some(msg => msg.info.id === fixtureTerminal.parentID
            && msg.parts.some(part => part.text?.includes("RUST_TERMINAL_GAP")))) {
          if (MODE === "incomplete") {
            page.messages = page.messages.filter(msg => msg.info.id !== fixtureTerminal.parentID)
            page.nextCursor = "fixture-incomplete"
          } else {
            const info = {...fixtureTerminal, id: "msg_00112233445566778899aabbccddeeff",
                          error: {name: "MessageAbortedError"}}
            delete info.finish
            page.messages.push({info, parts: []})
          }
        }
""".replace("MODE", repr(mode)))
    source = source.replace("        const [snapshot, page]", """        if (before === "fixture-incomplete") throw fault("fixture_page_unavailable")
        const [snapshot, page]""", 1)

emit = "  function emit(event) {\n    observeReply(event)\n"
assert source.count(emit) == 1
asset.write_text(source.replace(emit, "  let fixtureTerminal\n" + emit + '''    const info = event?.properties?.info
    if (event?.type === "message.updated" && info?.role === "assistant" &&
        (info.time?.completed || info.error)) {
      fixtureTerminal = info
      fs.appendFileSync(path.join(path.dirname(options.bindingFile), "fixture-terminal-events-suppressed.jsonl"), info.id + "\\n", {mode: 0o600})
      return
    }
'''))
