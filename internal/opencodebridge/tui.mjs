// OpenCode >=1.19.3 stable public TUI API. Native history remains native; this bridge owns no provider credentials.
import http from "node:http"
import fs from "node:fs"
import path from "node:path"
import crypto from "node:crypto"

const ID = "swarm-opencode-guard"
const BODY_LIMIT = 64 << 10
const RESPONSE_LIMIT = 2 << 20
const EVENT_LIMIT = 1 << 20
const NATIVE_TIMEOUT = 4000
const sessionID = value => typeof value === "string" && /^ses_[A-Za-z0-9]{10,124}$/.test(value)
const messageID = value => typeof value === "string" && /^msg_[A-Za-z0-9]{10,124}$/.test(value)
const string = (value, max = 128) => typeof value === "string" && value.length > 0 && Buffer.byteLength(value) <= max && !/[\x00-\x1f\x7f]/.test(value)
const object = value => value !== null && typeof value === "object" && !Array.isArray(value)
const canonical = value => JSON.stringify(value, (_key, item) => object(item) ? Object.fromEntries(Object.keys(item).sort().map(key => [key, item[key]])) : item)
const digest = value => crypto.createHash("sha256").update(value).digest("hex")
const pause = ms => new Promise(resolve => setTimeout(resolve, ms))
const reasonKey = Symbol("bridgeReason")
const fault = reason => Object.assign(new Error(reason), { [reasonKey]: reason })
const exactKeys = (value, keys) => object(value) && Object.keys(value).every(key => keys.includes(key))
const validPath = value => string(value, 4096) && path.isAbsolute(value) && path.normalize(value) === value
const list = value => { if (!Array.isArray(value)) throw fault("native_shape"); return value }
const map = value => { if (!object(value)) throw fault("native_shape"); return value }

function supportedVersion(value) {
  const match = typeof value === "string" && /^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/.exec(value)
  if (!match || match[0] !== value) return false
  const [major, minor, patch] = match.slice(1).map(BigInt)
  return major > 1n || major === 1n && (minor > 19n || minor === 19n && patch >= 3n)
}

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
  const streams = new Set(), controllers = new Set(), unsubscribers = [], operations = new Map()
  let bound = false, acknowledged = false, disposed = false, bootstrapReason = "binding_pending"
  let createdAt = binding.nativeCreatedAtMS, active = null, initial = null, promptRef, promptProps
  const identity = () => ({ version: 1, workerID: binding.workerID, sessionInstance: binding.sessionInstance, rootID: binding.rootID, directory: binding.directory, backendGeneration: binding.backendGeneration, nativeCreatedAtMS: createdAt })
  const conflict = () => api.plugins.list().some(p => p.source !== "internal" && p.id !== ID && p.enabled)

  function view(requireAck = true) {
    if (disposed) throw fault("disposed")
    if (!bound) throw fault(bootstrapReason)
    if (!supportedVersion(api.app.version)) throw fault("version_mismatch")
    if (api.state.ready !== true || api.state.path.directory !== binding.directory) throw fault("native_not_ready")
    if (requireAck && !acknowledged) throw fault("binding_unacknowledged")
    const route = api.route.current
    if (route?.name !== "session" || route.params?.sessionID !== binding.rootID) throw fault("route_mismatch")
    if (api.ui.dialog.open) throw fault("modal_open")
    return route
  }
  function readiness() {
    let reason
    try { view(false) } catch (err) { reason = err?.[reasonKey] ?? "native_not_ready" }
    return { ...identity(), nativeVersion: api.app.version, bound, acknowledged, ready: !reason, initialSupported: !reason && binding.initialPrompt && promptAvailable(), draftGuardSupported: !reason && promptAvailable(), inFlight: active !== null, degraded: reason ? [reason] : [], route: api.route.current }
  }
  function emptyDraft() {
    if (conflict() || !promptRef || promptProps?.session_id !== binding.rootID || promptProps.disabled || promptProps.visible === false) throw fault("native_draft_unavailable")
    const draft = promptRef.current
    if (!object(draft) || typeof draft.input !== "string" || !Array.isArray(draft.parts)) throw fault("native_draft_unavailable")
    if (draft.input !== "" || draft.parts.length) throw fault("native_draft_occupied")
  }
  function promptAvailable() {
    try { emptyDraft(); return true } catch { return false }
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
    } catch (err) { throw fault(err?.[reasonKey] ?? "native_read_failed") }
  }
  function rowMatches(row) {
    if (!object(row) || row.id !== binding.rootID || (row.location?.directory ?? row.directory) !== binding.directory || !Number.isSafeInteger(row.time?.created) || row.time.created <= 0) throw fault("native_binding_mismatch")
    if (createdAt && row.time.created !== createdAt) throw fault("native_binding_mismatch")
    return row
  }
  function checkedMessage(message, target = binding.rootID) {
    const info = message?.info
    if (!object(info) || info.sessionID !== target || !messageID(info.id) || !["user", "assistant"].includes(info.role) || !Array.isArray(message.parts)) throw fault("native_shape")
    for (const part of message.parts) if (!object(part) || part.sessionID !== target || part.messageID !== info.id || !string(part.id) || !string(part.type)) throw fault("native_shape")
    return message
  }
  function exactInputText(message, text) {
    const seen = new Set()
    let genuine = 0
    for (const part of message.parts) {
      if (seen.has(part.id) || (part.synthetic != null && typeof part.synthetic !== "boolean") || (part.ignored != null && typeof part.ignored !== "boolean") || (part.text != null && typeof part.text !== "string")) return false
      seen.add(part.id)
      if (part.synthetic === true || part.ignored === true) continue
      if (part.type !== "text" || part.text !== text) return false
      genuine++
    }
    return genuine === 1
  }
  async function history(limit = 50, before = "") {
    const result = await read(opts => api.client.session.messages({ sessionID: binding.rootID, limit, ...(before ? { before } : {}) }, opts))
    const messages = list(result.data)
    const ids = new Set()
    if (messages.length > limit) throw fault("native_shape")
    for (const message of messages) {
      const info = checkedMessage(message).info
      if (ids.has(info.id)) throw fault("native_shape")
      ids.add(info.id)
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
  function requestScope(check) {
    const scope = new Map([[binding.rootID, true]]), depths = new Map([[binding.rootID, 0]]), rows = new Map()
    let reads = 0
    async function contains(id) {
      if (!sessionID(id)) throw fault("native_shape")
      const trail = new Map()
      while (!scope.has(id)) {
        if (trail.has(id) || trail.size >= 8 || reads++ >= 32) throw fault("request_scope_unknown")
        const row = (await read(opts => api.client.session.get({ sessionID: id }, opts))).data
        check()
        if (!object(row) || row.id !== id || (row.parentID !== undefined && !sessionID(row.parentID))) throw fault("request_scope_unknown")
        trail.set(id, row)
        if (!row.parentID) { scope.set(id, false); break }
        id = row.parentID
      }
      const result = scope.get(id)
      let depth = depths.get(id) ?? 0
      for (const [entry, row] of [...trail].reverse()) {
        if (result && ++depth > 8) throw fault("request_scope_unknown")
        scope.set(entry, result)
        if (result) { depths.set(entry, depth); rows.set(entry, row) }
      }
      return result
    }
    return { contains, rows }
  }
  async function activityScope(snapshot, check) {
    const candidates = new Set(), requests = [...snapshot.permissions, ...snapshot.questions]
    if (requests.length > 128) throw fault("scope_limit")
    for (const [id, status] of Object.entries(snapshot.statuses)) {
      if (!sessionID(id) || !object(status) || !["idle", "busy", "retry"].includes(status.type)) throw fault("native_shape")
      if (status.type !== "idle") candidates.add(id)
    }
    for (const request of requests) {
      if (!object(request) || !sessionID(request.sessionID)) throw fault("native_shape")
      candidates.add(request.sessionID)
    }
    if (candidates.size > 128) throw fault("scope_limit")
    const scope = requestScope(check)
    for (const id of candidates) { await scope.contains(id); check() }
    const descendants = [...scope.rows.values()]
    return { descendants, children: descendants.filter(row => row.parentID === binding.rootID), hierarchyComplete: true, hierarchyScope: "current_activity_requests" }
  }
  async function idle(snapshot, check) {
    const hierarchy = await activityScope(snapshot, check), related = new Set([binding.rootID, ...hierarchy.descendants.map(row => row.id)])
    for (const id of related) {
      const status = snapshot.statuses[id]
      if (status !== undefined && status.type !== "idle") throw fault("native_busy")
    }
    if ([...snapshot.permissions, ...snapshot.questions].some(request => related.has(request.sessionID))) throw fault("native_request_pending")
  }
  async function lookupMessage(id, target = binding.rootID) {
    let result
    try { result = await bounded(signal => api.client.session.message({ sessionID: target, messageID: id }, { throwOnError: false, signal })) }
    catch { throw fault("native_read_failed") }
    if (result?.response?.status === 404) return null
    if (result?.response?.status !== 200 || result.error !== undefined || !object(result.data)) throw fault("native_read_failed")
    if (Buffer.byteLength(JSON.stringify(result.data)) > RESPONSE_LIMIT) throw fault("response_oversized")
    const message = checkedMessage(result.data, target)
    if (message.info.id !== id) throw fault("native_shape")
    return message
  }
  async function requireUnusedMessage(id) {
    if (await lookupMessage(id)) throw fault("native_message_exists")
  }
  function nativeSelection(session) {
    if (!string(session.agent) || !string(session.model?.providerID) || !string(session.model?.id) || (session.model.variant !== undefined && !string(session.model.variant))) throw fault("selection_unavailable")
    return { agent: session.agent, model: { providerID: session.model.providerID, modelID: session.model.id }, variant: session.model.variant ?? "default", selectionBasis: "native_session_selection", selectionMessageID: "" }
  }
  function operationHash(body) {
    return digest(JSON.stringify([body.version, body.sessionInstance, body.backendGeneration, body.rootID, body.kind, body.targetID, body.messageID ?? null, body.text ?? null, body.requestID ?? null, body.reply ?? null, body.answers ?? null, body.expectedRequest === undefined ? null : canonical(body.expectedRequest), body.requestExpiresAtMS ?? null]))
  }
  function operationReceipt(record) {
    return record ? { ...identity(), ...record.receipt, inFlight: active !== null } : null
  }
  function admit(record) { record.receipt.status = "admitted"; record.receipt.reason = "" }
  function remember(body, selection) {
    const bytes = Buffer.byteLength(JSON.stringify(body))
    while (operations.size >= 128 || [...operations.values()].reduce((total, entry) => total + entry.bytes, bytes) > RESPONSE_LIMIT) {
      const terminal = [...operations.entries()].find(([, entry]) => entry.receipt.status !== "uncertain")
      if (!terminal) throw fault("receipt_capacity")
      operations.delete(terminal[0])
    }
    const nativeEffect = body.kind === "prompt" ? "" : body.kind === "permission_reply" && body.reply === "reject" ? "reject_session_pending_permissions" : body.kind === "permission_reply" && body.reply === "always" ? "approve_native_patterns_and_matching_session_requests" : "single_request"
    const record = { body, bytes, selection, startedAt: Date.now(), invoked: false, receipt: { operationID: body.operationID, requestHash: operationHash(body), kind: body.kind, targetID: body.targetID, nativeEffect, ...(body.messageID ? { messageID: body.messageID } : { requestID: body.requestID }), ...selection, status: "uncertain", reason: "native_ack_unobserved" } }
    operations.set(body.operationID, record)
    return record
  }
  async function reconcilePrompt(record, wait = 0) {
    if (record.body.kind !== "prompt" || !record.invoked || record.receipt.status !== "uncertain") return
    const { body, selection, startedAt } = record, until = Date.now() + wait
    try { rowMatches((await read(opts => api.client.session.get({ sessionID: binding.rootID }, opts))).data) } catch { return }
    do {
      try {
        const row = await lookupMessage(body.messageID), info = row?.info
        if (info?.id === body.messageID && info.role === "user" && Number.isSafeInteger(info.time?.created) && info.time.created >= startedAt && info.agent === selection.agent && info.model?.providerID === selection.model.providerID && info.model?.modelID === selection.model.modelID && (info.model.variant ?? "default") === selection.variant && exactInputText(row, body.text)) { admit(record); return }
      } catch { /* Native acknowledgement remains uncertain until the exact row can be read. */ }
      if (disposed || Date.now() >= until) return
      await pause(100)
    } while (!disposed)
  }
  async function savedOperation(body) {
    const record = operations.get(body.operationID)
    if (!record) return null
    if (record.receipt.requestHash !== operationHash(body)) throw fault("operation_conflict")
    await reconcilePrompt(record)
    return operationReceipt(record)
  }
  function observeReply(event) {
    const properties = event?.properties
    if (!object(properties)) return
    for (const record of operations.values()) {
      const body = record.body
      if (!record.invoked || record.receipt.status !== "uncertain" || properties.sessionID !== body.targetID || properties.requestID !== body.requestID) continue
      if ((body.kind === "permission_reply" && event.type === "permission.replied" && properties.reply === body.reply) ||
          (body.kind === "question_reply" && event.type === "question.replied" && JSON.stringify(properties.answers) === JSON.stringify(body.answers)) ||
          (body.kind === "question_reject" && event.type === "question.rejected")) admit(record)
    }
  }
  function envelope(body, extra) {
    scoped(body)
    if (!exactKeys(body, ["version", "sessionInstance", "backendGeneration", "rootID", "operationID", "text", ...extra]) || !string(body.operationID) || typeof body.text !== "string" || !body.text || Buffer.byteLength(body.text) > 16384) throw fault("bad_envelope")
  }
  function answersMatch(questions, answers) {
    if (!Array.isArray(questions) || !questions.length || questions.length > 16 || !Array.isArray(answers) || answers.length !== questions.length || Buffer.byteLength(JSON.stringify(answers)) > 16384) throw fault("answer_invalid")
    for (let index = 0; index < questions.length; index++) {
      const question = questions[index], answer = answers[index]
      if (!object(question) || !Array.isArray(question.options) || question.options.length > 128 || (question.custom !== undefined && typeof question.custom !== "boolean") || (question.multiple !== undefined && typeof question.multiple !== "boolean")) throw fault("native_shape")
      const labels = new Set()
      for (const option of question.options) { if (!object(option) || typeof option.label !== "string" || labels.has(option.label)) throw fault("native_shape"); labels.add(option.label) }
      if (!Array.isArray(answer) || answer.length > 128 || (!question.multiple && answer.length > 1) || new Set(answer).size !== answer.length) throw fault("answer_invalid")
      for (const label of answer) if (typeof label !== "string" || !label.length || Buffer.byteLength(label) > 4096 || label.includes("\0") || (question.custom === false && !labels.has(label))) throw fault("answer_invalid")
    }
  }
  async function dispatch(body, check) {
    scoped(body)
    const prompt = body.kind === "prompt", permission = body.kind === "permission_reply"
    if (prompt) {
      envelope(body, ["kind", "targetID", "messageID"])
      if (body.targetID !== binding.rootID || !messageID(body.messageID)) throw fault("target_invalid")
    } else {
      if (!["permission_reply", "question_reply", "question_reject"].includes(body.kind)) throw fault("unsupported_kind")
      const fields = permission ? ["reply"] : body.kind === "question_reply" ? ["answers"] : []
      if (!exactKeys(body, ["version", "sessionInstance", "backendGeneration", "rootID", "operationID", "kind", "targetID", "requestID", "expectedRequest", "requestExpiresAtMS", ...fields]) || !string(body.operationID) || !sessionID(body.targetID) || typeof body.requestID !== "string" || !(permission ? /^per_[A-Za-z0-9]{10,124}$/ : /^que_[A-Za-z0-9]{10,124}$/).test(body.requestID) || (permission && !["once", "always", "reject"].includes(body.reply))) throw fault("bad_envelope")
      if (body.expectedRequest !== undefined || body.requestExpiresAtMS !== undefined) {
        if (!object(body.expectedRequest) || Buffer.byteLength(JSON.stringify(body.expectedRequest)) > 32768 || !Number.isSafeInteger(body.requestExpiresAtMS) || body.requestExpiresAtMS <= 0) throw fault("bad_envelope")
      }
    }
    const saved = await savedOperation(body)
    if (saved) return saved
    if (binding.initialPrompt && initial?.status !== "admitted") throw fault("initial_pending")
    if (active) throw fault("busy")
    for (const record of operations.values()) {
      if (prompt ? record.body.messageID === body.messageID : record.body.requestID === body.requestID && record.body.targetID === body.targetID) {
        if (record.receipt.status === "uncertain") throw fault("request_unresolved")
        if (record.receipt.status === "admitted") throw fault("request_stale")
      }
    }
    const lane = {}; active = lane
    let record
    try {
      view(); const snapshot = await current()
      let selection, invoke
      if (prompt) {
        await idle(snapshot, check); selection = nativeSelection(snapshot.session)
        await requireUnusedMessage(body.messageID)
        const request = { sessionID: binding.rootID, messageID: body.messageID, parts: [{ type: "text", text: body.text }], agent: selection.agent, model: selection.model, variant: selection.variant }
        invoke = options => api.client.session.promptAsync(request, options)
      } else {
        const requests = permission ? snapshot.permissions : snapshot.questions
        if (requests.length > 128) throw fault("native_shape")
        const matches = requests.filter(request => object(request) && request.id === body.requestID)
        if (matches.length > 1) throw fault("native_shape")
        if (!matches.length) throw fault("request_stale")
        const pending = matches[0]
        if (pending.sessionID !== body.targetID) throw fault("request_target_mismatch")
        if (body.expectedRequest !== undefined && canonical(pending) !== canonical(body.expectedRequest)) throw fault("request_content_changed")
        if (!await requestScope(check).contains(pending.sessionID)) throw fault("request_unrelated")
        if (body.kind === "question_reply") answersMatch(pending.questions, body.answers)
        invoke = options => permission ? api.client.permission.reply({ requestID: body.requestID, reply: body.reply }, options) : body.kind === "question_reply" ? api.client.question.reply({ requestID: body.requestID, answers: body.answers }, options) : api.client.question.reject({ requestID: body.requestID }, options)
      }
      // The final route read and native invocation share one JS callback; no await separates them.
      check(); view(); if (prompt) emptyDraft()
      if (!prompt && body.requestExpiresAtMS !== undefined && Date.now() >= body.requestExpiresAtMS) throw fault("request_expired")
      record = remember(body, selection)
      let result
      try { result = await bounded(signal => { record.invoked = true; return invoke({ throwOnError: true, signal }) }, NATIVE_TIMEOUT, lane) }
      catch (err) { if (record.receipt.status !== "admitted") record.receipt.reason = err?.[reasonKey] ?? "native_error"; return operationReceipt(record) }
      const status = result?.response?.status
      record.receipt.nativeStatus = Number.isInteger(status) ? status : 0
      if (!prompt && status === 200 && result.data === true && result.error === undefined) admit(record)
      if (record.receipt.status !== "admitted") {
        record.receipt.reason = "native_ack_unobserved"
        if ([400,401,403,404,405,409,422].includes(status)) { record.receipt.status = "rejected"; record.receipt.reason = "native_response" }
      }
      if (prompt && status === 204 && result.error === undefined) await reconcilePrompt(record, 2000)
      return operationReceipt(record)
    } catch (err) {
      if (!record?.invoked) throw err
      if (record.receipt.status !== "admitted") record.receipt.reason = "native_error"
      return operationReceipt(record)
    } finally { lane.returned = true; if (!lane.pending && active === lane) active = null }
  }

  function installPrompt() {
    if (conflict()) { if (binding.initialPrompt) throw fault("initial_plugin_conflict"); return }
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
    if (!initial || initial.status === "admitted" || initial.status === "rejected") return initial
    const result = await history()
    const candidates = result.messages.filter(m => m.info.role === "user" && !initial.before.has(m.info.id) && Number.isSafeInteger(m.info.time?.created) && m.info.time.created >= initial.startedAt && exactInputText(m, initial.text))
    if (candidates.length === 1) {
      const info = candidates[0].info
      if (string(info.agent) && string(info.model?.providerID) && string(info.model?.modelID)) Object.assign(initial, { status: "admitted", reason: "", messageID: info.id, agent: info.agent, model: { providerID: info.model.providerID, modelID: info.model.modelID }, variant: info.model.variant ?? "default", selectionBasis: "native_composer", selectionMessageID: info.id })
    } else if (candidates.length > 1) Object.assign(initial, { status: "uncertain", reason: "native_message_ambiguous" })
    return initial
  }
  function initialReceipt() {
    if (!initial) return null
    const { before, text, startedAt, intentHash, ...receipt } = initial
    return { ...identity(), ...receipt, inFlight: active !== null }
  }
  async function initialChoices(model, agent) {
    await Promise.all([
      model ? (async () => {
        const [providerID, ...rest] = model.split("/"), modelID = rest.join("/")
        const result = await read(opts => api.client.config.providers({}, opts))
        const providers = list(map(result.data).providers).filter(provider => provider?.id === providerID)
        if (providers.length !== 1 || !object(providers[0].models) || !Object.hasOwn(providers[0].models, modelID) || !object(providers[0].models[modelID])) throw fault("initial_model_unavailable")
      })() : undefined,
      agent ? (async () => {
        const agents = list((await read(opts => api.client.app.agents({}, opts))).data).filter(value => value?.name === agent)
        if (agents.length !== 1 || !["primary", "all"].includes(agents[0].mode) || (agents[0].hidden != null && agents[0].hidden !== false)) throw fault("initial_agent_unavailable")
      })() : undefined,
    ])
  }
  async function submitInitial(body, check) {
    envelope(body, ["requestHash", "expectedModel", "expectedAgent"])
    const model = body.expectedModel ?? "", agent = body.expectedAgent ?? ""
    const intentHash = digest(JSON.stringify([body.requestHash, { model: body.expectedModel, agent: body.expectedAgent }]))
    if (!binding.initialPrompt || !/^[a-f0-9]{64}$/.test(body.requestHash) || digest(body.text) !== body.requestHash) throw fault("initial_invalid")
    if (initial) {
      if (initial.operationID !== body.operationID || initial.intentHash !== intentHash) throw fault("initial_conflict")
      await reconcileInitial(); return initialReceipt()
    }
    if (active) throw fault("busy")
    const lane = {}; active = lane
    let preflightOwned = false
    try {
      view(); preflightOwned = true
      for (const [key, max] of [["expectedModel", 512], ["expectedAgent", 128]]) if (body[key] !== undefined && body[key] !== "" && !string(body[key], max)) throw fault("initial_choice_invalid")
      if (model && (!model.includes("/") || model.startsWith("/") || model.endsWith("/"))) throw fault("initial_choice_invalid")
      await idle(await current(), check); const baseline = await history()
      if (binding.mode === "create" && (baseline.messages.length || baseline.nextCursor)) throw fault("initial_history_present")
      await initialChoices(model, agent)
      if (conflict()) throw fault("initial_plugin_conflict")
      if (body.text.trimStart().startsWith("/") || ["exit", "quit", ":q"].includes(body.text.trim())) throw fault("initial_command_unsupported")
      if (!promptAvailable()) throw fault("native_draft_unavailable")
      check(); view()
      initial = { operationID: body.operationID, requestHash: body.requestHash, intentHash, text: body.text, before: new Set(baseline.messages.map(m => m.info.id)), startedAt: Date.now(), status: "uncertain", reason: "native_message_unobserved" }
      // Host keeps the launch input embargo until this genuine native user message is observed.
      try { promptRef.set({ input: body.text, parts: [] }); promptRef.submit() } catch { return initialReceipt() }
      const until = Date.now() + 6000
      while (!disposed && Date.now() < until) {
        try { await reconcileInitial() } catch { break }
        if (initial.status === "admitted") break
        await pause(100)
      }
      return initialReceipt()
    } catch (err) {
      if (!initial && preflightOwned) {
        initial = { operationID: body.operationID, requestHash: body.requestHash, intentHash, status: "rejected", reason: err?.[reasonKey] ?? "native_unavailable", nativeEffect: "none" }
        return initialReceipt()
      }
      throw err
    } finally { if (active === lane) active = null }
  }

  function emit(event) {
    observeReply(event)
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
    let operationBody
    const authorization = req.headers.authorization
    if (typeof authorization !== "string" || !authorization.startsWith("Bearer ") || !crypto.timingSafeEqual(Buffer.from(digest(authorization.slice(7)), "hex"), token)) return rejected(res, "unauthorized", 401)
    try {
      const url = new URL(req.url, "http://swarm-bridge")
      if (req.method === "GET" && url.pathname === "/ready") return response(res, 200, readiness())
      if (req.method === "GET" && url.pathname === "/message") {
        if (!bound) throw fault(bootstrapReason)
        const id = url.searchParams.get("messageID"), target = url.searchParams.get("sessionID") ?? binding.rootID
        if (!messageID(id) || !sessionID(target)) throw fault("target_invalid")
        rowMatches((await read(opts => api.client.session.get({ sessionID: binding.rootID }, opts))).data)
        if (!await requestScope(check).contains(target)) throw fault("request_unrelated")
        check()
        const message = await lookupMessage(id, target)
        if (!message) return rejected(res, "native_message_missing", 404)
        return response(res, 200, message)
      }
      if (req.method === "GET" && url.pathname === "/receipt") {
        const operationID = url.searchParams.get("operationID")
        if (!string(operationID)) throw fault("bad_envelope")
        const record = operations.get(operationID)
        if (record) await reconcilePrompt(record)
        return response(res, 200, operationReceipt(record))
      }
      if (req.method === "GET" && url.pathname === "/initial") { if (initial) await reconcileInitial(); return response(res, 200, initialReceipt()) }
      if (req.method === "GET" && url.pathname === "/snapshot") {
        if (!bound) throw fault(bootstrapReason)
        const limit = Number(url.searchParams.get("limit") || 50) || 50, before = url.searchParams.get("before") ?? ""
        if (!Number.isInteger(limit) || limit < 1 || limit > 200 || before.length > 4096 || /[\x00-\x20\x7f]/.test(before)) throw fault("page_invalid")
        const [snapshot, page] = await Promise.all([current(), history(limit, before)])
        const hierarchy = await activityScope(snapshot, check)
        return response(res, 200, { ...identity(), ...snapshot, ...page, ...hierarchy })
      }
      if (req.method === "GET" && url.pathname === "/events") {
        if (!bound) throw fault(bootstrapReason)
        if (streams.size >= 4) throw fault("event_clients_full")
        res.writeHead(200, { "content-type": "text/event-stream", "cache-control": "no-store" })
        const stream = { res, queue: [], bytes: 0, blocked: false, drain() { while (!this.closing && !this.blocked && this.queue.length) { const item = this.queue.shift(); this.bytes -= Buffer.byteLength(item); this.blocked = !res.write(item) } } }
        streams.add(stream)
        const release = () => { stream.closing = true; stream.queue = []; stream.bytes = 0; streams.delete(stream); if (!res.destroyed) res.destroy() }
        // Bun 1.3.14 reports peer loss on request/socket, without a response close event.
        req.once("aborted", release); req.on("error", release); req.socket?.once("close", release)
        res.once("close", release); res.on("error", release)
        res.on("drain", () => { stream.blocked = false; stream.drain() })
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
      if (url.pathname === "/dispatch") { scoped(body); if (string(body.operationID)) operationBody = body }
      const receipt = url.pathname === "/initial" ? await submitInitial(body, check) : await dispatch(body, check)
      response(res, 200, { ...receipt, inFlight: active !== null })
    } catch (err) {
      const reason = err?.[reasonKey] ?? "native_unavailable"
      if (operationBody) {
        const body = operationBody
        return response(res, 200, { ...identity(), operationID: body.operationID, kind: body.kind, targetID: body.targetID, messageID: body.messageID, requestID: body.requestID, status: "rejected", reason, inFlight: active !== null })
      }
      rejected(res, reason)
    }
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
    if (disposed || !api.state.ready || !supportedVersion(api.app.version) || api.state.path.directory !== binding.directory) throw fault("native_not_ready")
    const result = await read(opts => binding.mode === "create" ? api.client.v2.session.create({ id: binding.rootID, location: { directory: binding.directory } }, opts) : api.client.v2.session.get({ sessionID: binding.rootID }, opts))
    const row = rowMatches(result.data.data); createdAt = row.time.created
    api.route.navigate("session", { sessionID: binding.rootID }); bound = true; bootstrapReason = ""
    view(false)
  } catch (err) { bootstrapReason = err?.[reasonKey] ?? "native_binding_failed"; bound = false }
} }
