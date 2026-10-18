// OpenCode 1.19.3 public TUI API. Native history remains native; this bridge owns no provider credentials.
import http from "node:http"
import fs from "node:fs"
import path from "node:path"
import crypto from "node:crypto"

const ID = "swarm-opencode-guard"
const VERSION = "1.19.3"
const BODY_LIMIT = 64 << 10
const RESPONSE_LIMIT = 2 << 20
const EVENT_LIMIT = 1 << 20
const NATIVE_TIMEOUT = 4000
const sessionID = value => typeof value === "string" && /^ses_[A-Za-z0-9]{10,124}$/.test(value)
const messageID = value => typeof value === "string" && /^msg_[A-Za-z0-9]{10,124}$/.test(value)
const string = (value, max = 128) => typeof value === "string" && value.length > 0 && Buffer.byteLength(value) <= max && !/[\x00-\x1f\x7f]/.test(value)
const object = value => value !== null && typeof value === "object" && !Array.isArray(value)
const digest = value => crypto.createHash("sha256").update(value).digest("hex")
const pause = ms => new Promise(resolve => setTimeout(resolve, ms))
const reasonKey = Symbol("bridgeReason")
const fault = reason => Object.assign(new Error(reason), { [reasonKey]: reason })
const exactKeys = (value, keys) => object(value) && Object.keys(value).every(key => keys.includes(key))
const validPath = value => string(value, 4096) && path.isAbsolute(value) && path.normalize(value) === value
const list = value => { if (!Array.isArray(value)) throw fault("native_shape"); return value }
const map = value => { if (!object(value)) throw fault("native_shape"); return value }

function bindingFrom(options) {
  if (!exactKeys(options, ["bindingFile"]) || !validPath(options.bindingFile)) throw fault("binding_invalid")
  const parent = fs.lstatSync(path.dirname(options.bindingFile))
  if (fs.realpathSync(path.dirname(options.bindingFile)) !== path.dirname(options.bindingFile) || !parent.isDirectory() || parent.uid !== process.getuid() || (parent.mode & 0o777) !== 0o700) throw fault("binding_invalid")
  const fd = fs.openSync(options.bindingFile, fs.constants.O_RDONLY | fs.constants.O_NOFOLLOW)
  let binding
  try {
    const stat = fs.fstatSync(fd)
    if (!stat.isFile() || stat.uid !== process.getuid() || stat.nlink !== 1 || (stat.mode & 0o777) !== 0o600 || stat.size > 16384) throw fault("binding_invalid")
    const raw = Buffer.alloc(16385)
    const count = fs.readSync(fd, raw, 0, raw.length, 0)
    if (count > 16384) throw fault("binding_invalid")
    const text = new TextDecoder("utf-8", { fatal: true }).decode(raw.subarray(0, count))
    binding = JSON.parse(text)
    // JSON.parse accepts duplicates; reject decoded duplicate keys before trusting this flat record.
    const keys = new Set()
    for (const match of text.matchAll(/"(?:\\.|[^"\\])*"/g)) {
      if (!text.slice(match.index + match[0].length).trimStart().startsWith(":")) continue
      const key = JSON.parse(match[0])
      if (keys.has(key)) throw fault("binding_invalid")
      keys.add(key)
    }
  } finally { fs.closeSync(fd) }
  if (!exactKeys(binding, ["version", "workerID", "sessionInstance", "rootID", "directory", "backendGeneration", "socketPath", "token", "mode", "nativeCreatedAtMS", "initialPrompt"]) || binding.version !== 1 || !sessionID(binding.rootID)) throw fault("binding_invalid")
  for (const key of ["workerID", "sessionInstance", "backendGeneration", "token"]) if (!string(binding[key], 256)) throw fault("binding_invalid")
  if (Buffer.byteLength(binding.token) < 32 || !/^[a-f0-9]{32}$/.test(binding.sessionInstance) || !/^[a-f0-9]{32}$/.test(binding.backendGeneration)) throw fault("binding_invalid")
  if (!validPath(binding.directory) || !validPath(binding.socketPath) || Buffer.byteLength(binding.socketPath) > 100 || !["create", "resume"].includes(binding.mode) || typeof binding.initialPrompt !== "boolean") throw fault("binding_invalid")
  if (!Number.isSafeInteger(binding.nativeCreatedAtMS) || (binding.mode === "resume" ? binding.nativeCreatedAtMS <= 0 : binding.nativeCreatedAtMS !== 0)) throw fault("binding_invalid")
  const socketParent = fs.lstatSync(path.dirname(binding.socketPath))
  if (path.dirname(binding.socketPath) !== path.dirname(options.bindingFile) || !socketParent.isDirectory() || socketParent.uid !== process.getuid() || (socketParent.mode & 0o777) !== 0o700 || fs.existsSync(binding.socketPath)) throw fault("socket_unavailable")
  return binding
}

export default { id: ID, async tui(api, options) {
  let binding
  try { binding = bindingFrom(options) } catch { throw fault("binding_invalid") }
  const token = Buffer.from(digest(binding.token), "hex")
  const streams = new Set(), controllers = new Set(), unsubscribers = []
  let bound = false, acknowledged = false, disposed = false, bootstrapReason = "binding_pending"
  let createdAt = binding.nativeCreatedAtMS, active = null, initial = null, promptRef, promptProps
  const identity = () => ({ version: 1, workerID: binding.workerID, sessionInstance: binding.sessionInstance, rootID: binding.rootID, directory: binding.directory, backendGeneration: binding.backendGeneration, nativeCreatedAtMS: createdAt })
  const conflict = () => api.plugins.list().some(p => p.source !== "internal" && p.id !== ID && p.enabled)

  function view(requireAck = true) {
    if (disposed) throw fault("disposed")
    if (!bound) throw fault(bootstrapReason)
    if (api.app.version !== VERSION) throw fault("version_mismatch")
    if (api.state.ready !== true || api.state.path.directory !== binding.directory) throw fault("native_not_ready")
    if (requireAck && !acknowledged) throw fault("binding_unacknowledged")
    const route = api.route.current
    if (route?.name !== "session" || route.params?.sessionID !== binding.rootID) throw fault("route_mismatch")
    if (api.ui.dialog.open) throw fault("modal_open")
    return route
  }
  function readiness() {
    let reason
    try { view(false) } catch (err) { reason = err[reasonKey] ?? "native_not_ready" }
    return { ...identity(), nativeVersion: api.app.version, bound, acknowledged, ready: !reason, initialSupported: !reason && promptAvailable(), draftGuardSupported: false, inFlight: active !== null, degraded: reason ? [reason] : [], route: api.route.current }
  }
  function promptAvailable() {
    try { return binding.initialPrompt && !conflict() && !!promptRef && promptProps?.session_id === binding.rootID && !promptProps.disabled && promptProps.visible !== false && promptRef.current.input === "" && Array.isArray(promptRef.current.parts) && promptRef.current.parts.length === 0 } catch { return false }
  }
  function scoped(body) {
    if (!object(body) || body.version !== 1 || body.sessionInstance !== binding.sessionInstance || body.backendGeneration !== binding.backendGeneration || body.rootID !== binding.rootID) throw fault("binding_mismatch")
  }
  function response(res, code, value) {
    if (res.destroyed || res.writableEnded) return
    let encoded = JSON.stringify(value)
    if (Buffer.byteLength(encoded) > RESPONSE_LIMIT) { code = 502; encoded = JSON.stringify({ status: "rejected", reason: "response_oversized" }) }
    res.writeHead(code, { "content-type": "application/json", "cache-control": "no-store" }); res.end(encoded)
  }
  function rejected(res, reason, code = 422) { response(res, code, { ...identity(), status: "rejected", reason, inFlight: active !== null }) }

  async function bounded(run, timeout = NATIVE_TIMEOUT, lane) {
    const controller = new AbortController(); controllers.add(controller)
    let timer, pending
    try { pending = Promise.resolve(run(controller.signal)) } catch (err) { pending = Promise.reject(err) }
    if (lane) {
      lane.pending = true
      const settled = () => { lane.pending = false; if (lane.returned && active === lane) active = null }
      pending.then(settled, settled)
    }
    try {
      return await Promise.race([pending, new Promise((_, reject) => { timer = setTimeout(() => { controller.abort(); reject(fault("native_deadline")) }, timeout) })])
    } finally { clearTimeout(timer); controllers.delete(controller) }
  }
  async function read(run) {
    try {
      const result = await bounded(signal => run({ throwOnError: true, signal }))
      if (!result || result.error !== undefined || result.data === undefined || result.data === null) throw fault("native_read_failed")
      if (Buffer.byteLength(JSON.stringify(result.data)) > RESPONSE_LIMIT) throw fault("response_oversized")
      return result
    } catch (err) { throw fault(err[reasonKey] ?? "native_read_failed") }
  }
  function rowMatches(row) {
    if (!object(row) || row.id !== binding.rootID || (row.location?.directory ?? row.directory) !== binding.directory || !Number.isSafeInteger(row.time?.created) || row.time.created <= 0) throw fault("native_binding_mismatch")
    if (createdAt && row.time.created !== createdAt) throw fault("native_binding_mismatch")
    return row
  }
  async function history(limit = 50, before = "") {
    const result = await read(opts => api.client.session.messages({ sessionID: binding.rootID, limit, ...(before ? { before } : {}) }, opts))
    const messages = list(result.data)
    const ids = new Set()
    if (messages.length > limit) throw fault("native_shape")
    for (const message of messages) {
      const info = message?.info
      if (!object(info) || info.sessionID !== binding.rootID || !messageID(info.id) || !["user", "assistant"].includes(info.role) || ids.has(info.id) || !Array.isArray(message.parts)) throw fault("native_shape")
      ids.add(info.id)
      for (const part of message.parts) if (!object(part) || part.sessionID !== binding.rootID || part.messageID !== info.id || !string(part.id) || !string(part.type)) throw fault("native_shape")
    }
    const nextCursor = result.response?.headers?.get("x-next-cursor") ?? ""
    if (typeof nextCursor !== "string" || nextCursor.length > 4096 || /[\x00-\x20\x7f]/.test(nextCursor)) throw fault("native_shape")
    return { messages, nextCursor }
  }
  async function current() {
    const [session, statuses, permissions, questions] = await Promise.all([
      read(opts => api.client.session.get({ sessionID: binding.rootID }, opts)),
      read(opts => api.client.session.status({}, opts)),
      read(opts => api.client.permission.list({}, opts)),
      read(opts => api.client.question.list({}, opts)),
    ])
    rowMatches(session.data)
    return { session: session.data, statuses: map(statuses.data), permissions: list(permissions.data), questions: list(questions.data) }
  }
  async function idle(snapshot, check) {
    const status = snapshot.statuses[binding.rootID]
    if (status !== undefined && (!object(status) || status.type !== "idle")) throw fault("native_busy")
    const requests = [...snapshot.permissions, ...snapshot.questions]
    if (requests.length > 128) throw fault("native_shape")
    const scope = new Map([[binding.rootID, true]])
    let reads = 0
    for (const request of requests) {
      if (!object(request) || !sessionID(request.sessionID)) throw fault("native_shape")
      let id = request.sessionID
      const trail = new Set()
      while (!scope.has(id)) {
        if (trail.has(id) || trail.size >= 8 || reads++ >= 32) throw fault("request_scope_unknown")
        trail.add(id)
        const row = (await read(opts => api.client.session.get({ sessionID: id }, opts))).data
        check()
        if (!object(row) || row.id !== id || (row.parentID !== undefined && !sessionID(row.parentID))) throw fault("request_scope_unknown")
        if (!row.parentID) { scope.set(id, false); break }
        id = row.parentID
      }
      const related = scope.get(id)
      for (const entry of trail) scope.set(entry, related)
      if (related) throw fault("native_request_pending")
    }
  }
  async function latestSelection(check) {
    let before = ""
    for (let page = 0; page < 4; page++) {
      const result = await history(50, before)
      check()
      const user = result.messages.findLast(m => m.info.role === "user")?.info
      if (user) {
        if (!string(user.agent) || !string(user.model?.providerID) || !string(user.model?.modelID) || (user.model.variant !== undefined && !string(user.model.variant))) throw fault("selection_unqualified")
        return { agent: user.agent, model: { providerID: user.model.providerID, modelID: user.model.modelID }, variant: user.model.variant ?? "default", selectionBasis: "latest_committed_user", selectionMessageID: user.id }
      }
      if (!result.nextCursor || result.nextCursor === before) break
      before = result.nextCursor
    }
    throw fault("selection_unavailable")
  }
  async function observePrompt(body, selection, startedAt) {
    const until = Date.now() + 2000
    while (!disposed && Date.now() < until) {
      const row = (await history()).messages.find(m => m.info.id === body.messageID)
      const info = row?.info
      if (info?.role === "user" && Number.isSafeInteger(info.time?.created) && info.time.created >= startedAt && info.agent === selection.agent && info.model?.providerID === selection.model.providerID && info.model?.modelID === selection.model.modelID && (info.model.variant ?? "default") === selection.variant && row.parts.filter(p => p.type === "text" && !p.synthetic).map(p => p.text).join("") === body.text) return true
      await pause(100)
    }
    return false
  }
  function envelope(body, extra) {
    scoped(body)
    if (!exactKeys(body, ["version", "sessionInstance", "backendGeneration", "rootID", "operationID", "text", ...extra]) || !string(body.operationID) || typeof body.text !== "string" || !body.text || Buffer.byteLength(body.text) > 16384) throw fault("bad_envelope")
  }
  async function dispatch(body, check) {
    envelope(body, ["kind", "targetID", "messageID"])
    if (body.kind !== "prompt") throw fault("unsupported_kind")
    if (body.targetID !== binding.rootID || !messageID(body.messageID)) throw fault("target_invalid")
    if (binding.initialPrompt && initial?.status !== "admitted") throw fault("initial_pending")
    if (active) throw fault("busy")
    const lane = {}; active = lane
    let selection
    try {
      view(); const snapshot = await current(); await idle(snapshot, check)
      selection = await latestSelection(check)
      // The final route read and native invocation share one JS callback; no await separates them.
      check(); view()
      const startedAt = Date.now()
      const request = { sessionID: binding.rootID, messageID: body.messageID, parts: [{ type: "text", text: body.text }], agent: selection.agent, model: selection.model, variant: selection.variant }
      let result
      try { result = await bounded(signal => { lane.invoked = true; return api.client.session.promptAsync(request, { throwOnError: true, signal }) }, NATIVE_TIMEOUT, lane) }
      catch (err) { return { ...identity(), operationID: body.operationID, status: "uncertain", reason: err[reasonKey] ?? "native_error", ...selection, inFlight: active !== null } }
      const status = result?.response?.status
      let outcome = [400,401,403,404,405,409,422].includes(status) ? "rejected" : "uncertain"
      if (status === 204 && result.error === undefined) {
        try { if (await observePrompt(body, selection, startedAt)) outcome = "admitted" } catch { /* The write already crossed the native boundary. */ }
      }
      return { ...identity(), operationID: body.operationID, messageID: outcome === "admitted" ? body.messageID : "", status: outcome, reason: outcome === "admitted" ? "" : status === 204 ? "native_message_unobserved" : "native_response", nativeStatus: Number.isInteger(status) ? status : 0, ...selection, inFlight: active !== null }
    } catch (err) {
      if (!lane.invoked) throw err
      return { ...identity(), operationID: body.operationID, status: "uncertain", reason: "native_error", ...selection, inFlight: active !== null }
    } finally { lane.returned = true; if (!lane.pending && active === lane) active = null }
  }

  function installPrompt() {
    if (!binding.initialPrompt) return
    if (conflict()) throw fault("initial_plugin_conflict")
    api.slots.register({ slots: { session_prompt: (_context, props) => {
      promptProps = props
      return api.ui.Prompt({
        get sessionID() { return props.session_id }, get visible() { return props.visible }, get disabled() { return props.disabled },
        onSubmit: () => props.on_submit?.(), ref: ref => { promptRef = ref; props.ref?.(ref) },
        get right() { return api.ui.Slot({ name: "session_prompt_right", get session_id() { return props.session_id } }) },
      })
    } } })
  }
  async function reconcileInitial() {
    if (!initial || initial.status === "admitted") return initial
    const result = await history()
    const candidates = result.messages.filter(m => m.info.role === "user" && !initial.before.has(m.info.id) && m.info.time?.created >= initial.startedAt && m.parts.filter(p => p.type === "text" && !p.synthetic).map(p => p.text).join("") === initial.text)
    if (candidates.length === 1) {
      const info = candidates[0].info
      if (string(info.agent) && string(info.model?.providerID) && string(info.model?.modelID)) Object.assign(initial, { status: "admitted", reason: "", messageID: info.id, agent: info.agent, model: { providerID: info.model.providerID, modelID: info.model.modelID }, variant: info.model.variant ?? "default", selectionBasis: "native_composer", selectionMessageID: info.id })
    } else if (candidates.length > 1) Object.assign(initial, { status: "uncertain", reason: "native_message_ambiguous" })
    return initial
  }
  function initialReceipt() {
    if (!initial) return null
    const { before, text, startedAt, ...receipt } = initial
    return { ...identity(), ...receipt, inFlight: active !== null }
  }
  async function submitInitial(body, check) {
    envelope(body, ["requestHash"])
    if (!binding.initialPrompt || !/^[a-f0-9]{64}$/.test(body.requestHash) || digest(body.text) !== body.requestHash) throw fault("initial_invalid")
    if (initial) {
      if (initial.operationID !== body.operationID || initial.requestHash !== body.requestHash) throw fault("initial_conflict")
      await reconcileInitial(); return initialReceipt()
    }
    if (active) throw fault("busy")
    const lane = {}; active = lane
    try {
      view(); await idle(await current(), check); const baseline = await history()
      if (conflict()) throw fault("initial_plugin_conflict")
      if (body.text.trimStart().startsWith("/") || ["exit", "quit", ":q"].includes(body.text.trim())) throw fault("initial_command_unsupported")
      if (!promptAvailable()) throw fault("native_draft_unavailable")
      check(); view()
      initial = { operationID: body.operationID, requestHash: body.requestHash, text: body.text, before: new Set(baseline.messages.map(m => m.info.id)), startedAt: Date.now(), status: "uncertain", reason: "native_message_unobserved" }
      // Host keeps the launch input embargo until this genuine native user message is observed.
      try { promptRef.set({ input: body.text, parts: [] }); promptRef.submit() } catch { return initialReceipt() }
      const until = Date.now() + 6000
      while (!disposed && Date.now() < until) {
        try { await reconcileInitial() } catch { break }
        if (initial.status === "admitted") break
        await pause(100)
      }
      return initialReceipt()
    } finally { if (active === lane) active = null }
  }

  function emit(event) {
    if (![...streams].some(stream => !stream.closing)) return
    let frame
    try { frame = "data: " + JSON.stringify(event) + "\n\n" } catch { return }
    for (const stream of streams) {
      if (stream.closing) continue
      if (Buffer.byteLength(frame) > EVENT_LIMIT || stream.queue.length >= 128 || stream.bytes + Buffer.byteLength(frame) > RESPONSE_LIMIT) {
        stream.closing = true
        stream.queue = []; stream.bytes = 0
        stream.res.end('data: {"type":"swarm.bridge.gap","properties":{"reason":"event_overflow"}}\n\n')
        setTimeout(() => stream.res.destroy(), 100).unref(); continue
      }
      stream.queue.push(frame); stream.bytes += Buffer.byteLength(frame); stream.drain()
    }
  }
  for (const type of ["session.created", "session.updated", "session.deleted", "session.status", "session.error", "session.idle", "session.compacted", "message.updated", "message.removed", "message.part.updated", "message.part.removed", "message.part.delta", "permission.asked", "permission.replied", "question.asked", "question.replied", "question.rejected"]) unsubscribers.push(api.event.on(type, emit))

  async function handler(req, res) {
    res.on("error", () => {})
    const deadline = Date.now() + 12000
    const check = () => { if (res.destroyed || req.aborted) throw fault("channel_closed"); if (Date.now() >= deadline) throw fault("native_deadline") }
    if (disposed) return rejected(res, "disposed", 503)
    const authorization = req.headers.authorization
    if (typeof authorization !== "string" || !authorization.startsWith("Bearer ") || !crypto.timingSafeEqual(Buffer.from(digest(authorization.slice(7)), "hex"), token)) return rejected(res, "unauthorized", 401)
    try {
      const url = new URL(req.url, "http://swarm-bridge")
      if (req.method === "GET" && url.pathname === "/ready") return response(res, 200, readiness())
      if (req.method === "GET" && url.pathname === "/initial") { if (initial) await reconcileInitial(); return response(res, 200, initialReceipt()) }
      if (req.method === "GET" && url.pathname === "/snapshot") {
        if (!bound) throw fault(bootstrapReason)
        const limit = Number(url.searchParams.get("limit") || 50) || 50, before = url.searchParams.get("before") ?? ""
        if (!Number.isInteger(limit) || limit < 1 || limit > 200 || before.length > 4096 || /[\x00-\x20\x7f]/.test(before)) throw fault("page_invalid")
        const [snapshot, page, childResult] = await Promise.all([current(), history(limit, before), read(opts => api.client.session.children({ sessionID: binding.rootID }, opts))])
        const children = list(childResult.data), seen = new Set()
        if (children.length > 128) throw fault("native_shape")
        for (const child of children) {
          if (!object(child) || !sessionID(child.id) || child.id === binding.rootID || child.parentID !== binding.rootID || seen.has(child.id)) throw fault("native_shape")
          seen.add(child.id)
        }
        return response(res, 200, { ...identity(), ...snapshot, ...page, children })
      }
      if (req.method === "GET" && url.pathname === "/events") {
        if (!bound) throw fault(bootstrapReason)
        if (streams.size >= 4) throw fault("event_clients_full")
        res.writeHead(200, { "content-type": "text/event-stream", "cache-control": "no-store" })
        const stream = { res, queue: [], bytes: 0, blocked: false, drain() { while (!this.closing && !this.blocked && this.queue.length) { const item = this.queue.shift(); this.bytes -= Buffer.byteLength(item); this.blocked = !res.write(item) } } }
        streams.add(stream); res.on("drain", () => { stream.blocked = false; stream.drain() }); res.on("close", () => streams.delete(stream))
        res.write("data: " + JSON.stringify({ type: "swarm.bridge.connected", properties: identity() }) + "\n\n"); return
      }
      if (req.method !== "POST" || !["/ack", "/initial", "/dispatch"].includes(url.pathname)) return rejected(res, "unsupported_route", 404)
      let size = 0; const chunks = []
      for await (const chunk of req) { size += chunk.length; if (size > BODY_LIMIT) return rejected(res, "body_oversized", 413); chunks.push(chunk) }
      let body; try { body = JSON.parse(Buffer.concat(chunks)) } catch { throw fault("bad_json") }
      if (url.pathname === "/ack") {
        scoped(body); view(false)
        if (!exactKeys(body, ["version", "sessionInstance", "backendGeneration", "rootID", "nativeCreatedAtMS"]) || body.nativeCreatedAtMS !== createdAt) throw fault("binding_mismatch")
        acknowledged = true; return response(res, 200, readiness())
      }
      const receipt = url.pathname === "/initial" ? await submitInitial(body, check) : await dispatch(body, check)
      response(res, 200, { ...receipt, inFlight: active !== null })
    } catch (err) { rejected(res, err[reasonKey] ?? "native_unavailable") }
  }
  const server = http.createServer((req, res) => { void handler(req, res) })
  server.headersTimeout = 4000; server.requestTimeout = 8000; server.keepAliveTimeout = 1000
  server.on("error", () => {})
  await new Promise((resolve, reject) => { server.once("error", reject); server.listen(binding.socketPath, resolve) })
  fs.chmodSync(binding.socketPath, 0o600)
  const dispose = () => {
    if (disposed) return
    disposed = true
    for (const controller of controllers) controller.abort()
    for (const unsubscribe of unsubscribers) unsubscribe()
    for (const stream of streams) stream.res.destroy()
    streams.clear(); server.close(); server.closeAllConnections?.()
    promptRef = undefined
  }
  api.lifecycle.onDispose(dispose)
  api.lifecycle.signal.addEventListener("abort", dispose, { once: true })
  if (api.lifecycle.signal.aborted) { dispose(); return }
  try {
    installPrompt()
    const until = Date.now() + 15000
    while (!api.state.ready && !disposed && Date.now() < until) await pause(25)
    if (disposed || !api.state.ready || api.app.version !== VERSION || api.state.path.directory !== binding.directory) throw fault("native_not_ready")
    const result = await read(opts => binding.mode === "create" ? api.client.v2.session.create({ id: binding.rootID, location: { directory: binding.directory } }, opts) : api.client.v2.session.get({ sessionID: binding.rootID }, opts))
    const row = rowMatches(result.data.data); createdAt = row.time.created
    api.route.navigate("session", { sessionID: binding.rootID }); bound = true; bootstrapReason = ""
    view(false)
  } catch (err) { bootstrapReason = err[reasonKey] ?? "native_binding_failed"; bound = false }
} }
