/**
 * swarm-opencode-guard — RED PHASE STUB.
 *
 * Only the pinned default-export boundary shape exists so tui.test.mjs runs
 * its full suite and fails for the right reasons (absence of listener,
 * validation, guard and cleanup behavior). This stub must be replaced by the
 * reviewed implementation in the green phase before anything is shipped.
 */
const PLUGIN_ID = "swarm-opencode-guard"

export default {
  id: PLUGIN_ID,
  async tui(api, options, meta) {
    throw new Error("unimplemented: red phase, plugin behavior not implemented yet")
  },
}
