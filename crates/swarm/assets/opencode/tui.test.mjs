import test from "node:test"
import assert from "node:assert/strict"
import fs from "node:fs"
import os from "node:os"
import path from "node:path"
import http from "node:http"
import crypto from "node:crypto"
import plugin from "./tui.mjs"

const rootID = "ses_0123456789abcdef0123456789abcdef"
const messageID = "msg_0123456789abcdef0123456789abcdef"
const tuple = { agent: "plan", model: { providerID: "opencode", modelID: "deepseek-v4.1-flash-free", variant: "max" } }
const identity = { version: 1, workerID: "worker-A", sessionInstance: "a".repeat(32), rootID, directory: "/project", backendGeneration: "b".repeat(32) }
const pause = ms => new Promise(resolve => setTimeout(resolve, ms))

async function fixture(t, change = () => {}) {
  const dir = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), "ocb-")))
  fs.chmodSync(dir, 0o700)
  const binding = { ...identity, mode: "create", nativeCreatedAtMS: 0, initialPrompt: false, socketPath: path.join(dir, "bridge.sock"), token: crypto.randomBytes(24).toString("hex") }
  const row = { id: rootID, directory: binding.directory, location: { directory: binding.directory }, time: { created: 123456 }, title: "Root", agent: tuple.agent, model: { id: tuple.model.modelID, providerID: tuple.model.providerID, variant: tuple.model.variant } }
  const state = { route: { name: "session", params: { sessionID: rootID } }, row, version: "1.19.3", ready: true, statuses: {}, permissions: [], questions: [], children: [], messages: [{ info: { id: messageID, sessionID: rootID, role: "user", ...tuple }, parts: [{ id: "prt_" + "a".repeat(32), sessionID: rootID, messageID, type: "text", text: "previous" }] }], calls: [], disposers: [], events: new Map(), plugins: [], dialog: false, ack: false, autoAck: true }
  const result = data => ({ data, response: { status: 200, headers: new Headers() } })
  const read = (name, value) => async (...args) => { state.calls.push({ name, args }); return result(value()) }
  const api = {
    app: { get version() { return state.version } },
    state: { get ready() { return state.ready }, get path() { return { directory: binding.directory } }, session: { get: () => state.row, status: id => state.statuses[id], permission: () => state.permissions, question: () => state.questions } },
    route: { get current() { return state.route }, navigate(name, params) { state.route = { name, params } } },
    ui: { dialog: { get open() { return state.dialog } }, Prompt(props) { state.promptProps = props; props.ref(state.prompt); return {} }, Slot: props => props },
    mode: { current: () => "normal" },
    plugins: { list: () => state.plugins },
    slots: { register(spec) { state.slot = spec.slots.session_prompt; state.slot({}, { session_id: rootID, visible: true, disabled: false, ref: value => { state.hostRef = value }, on_submit: () => state.hostSubmitted = true }); return "guard" } },
    lifecycle: { signal: new AbortController().signal, onDispose(fn) { state.disposers.push(fn); return () => {} } },
    event: { on(type, fn) { state.events.set(type, fn); return () => state.events.delete(type) } },
    client: {
      v2: { session: { create: read("create", () => ({ data: state.row })), get: read("v2get", () => ({ data: state.row })) } },
      session: { get: read("get", () => state.row), status: read("status", () => state.statuses), children: read("children", () => state.children), messages: read("messages", () => state.messages), promptAsync: async (input, options) => { state.calls.push({ name: "prompt", input, options, route: state.route }); state.messages.push({ info: { id: input.messageID, sessionID: rootID, role: "user", time: { created: Date.now() }, agent: input.agent, model: { ...input.model, variant: input.variant } }, parts: input.parts.map((part, i) => ({ ...part, id: "prt_" + input.messageID.slice(4) + i, sessionID: rootID, messageID: input.messageID })) }); return { response: { status: 204 } } } },
      config: { providers: read("providers", () => ({ providers: [{ id: tuple.model.providerID, models: { [tuple.model.modelID]: { id: tuple.model.modelID } } }] })) },
      app: { agents: read("agents", () => [{ name: tuple.agent, mode: "primary" }]) },
      permission: { list: read("permissions", () => state.permissions) }, question: { list: read("questions", () => state.questions) },
    },
  }
  state.prompt = { current: { input: "", parts: [] }, set(value) { this.current = value }, submit() { state.calls.push({ name: "nativeSubmit" }); const text = this.current.input; this.current = { input: "", parts: [] }; state.promptProps.onSubmit?.(); queueMicrotask(() => { state.messages = [...state.messages, { info: { id: "msg_" + "c".repeat(32), sessionID: rootID, role: "user", time: { created: Date.now() }, ...tuple }, parts: [{ id: "prt_" + "c".repeat(32), sessionID: rootID, messageID: "msg_" + "c".repeat(32), type: "text", text }] }]; state.events.get("message.updated")?.({ type: "message.updated", properties: { info: state.messages.at(-1).info } }) }) } }
  api.client.session.message = async ({ messageID }) => { const data = state.messages.find(message => message.info.id === messageID); return { data, response: { status: data ? 200 : 404 } } }
  api.client.permission.reply = async input => {
    state.calls.push({ name: "permissionReply", input })
    const pending = state.permissions.find(request => request.id === input.requestID)
    state.permissions = state.permissions.filter(request => request.id !== input.requestID)
    state.events.get("permission.replied")?.({ type: "permission.replied", properties: { sessionID: pending.sessionID, requestID: input.requestID, reply: input.reply } })
    return { data: true, response: { status: 200 } }
  }
  api.client.question.reply = async input => {
    state.calls.push({ name: "questionReply", input })
    const pending = state.questions.find(request => request.id === input.requestID)
    state.questions = state.questions.filter(request => request.id !== input.requestID)
    state.events.get("question.replied")?.({ type: "question.replied", properties: { sessionID: pending.sessionID, requestID: input.requestID, answers: input.answers } })
    return { data: true, response: { status: 200 } }
  }
  api.client.question.reject = async input => {
    state.calls.push({ name: "questionReject", input })
    const pending = state.questions.find(request => request.id === input.requestID)
    state.questions = state.questions.filter(request => request.id !== input.requestID)
    state.events.get("question.rejected")?.({ type: "question.rejected", properties: { sessionID: pending.sessionID, requestID: input.requestID } })
    return { data: true, response: { status: 200 } }
  }
  change({ state, api, binding })
  fs.writeFileSync(path.join(dir, "binding.json"), state.bindingRaw?.(binding) ?? JSON.stringify(binding), { mode: 0o600 })
  if (state.hardlinkBinding) fs.linkSync(path.join(dir, "binding.json"), path.join(dir, "linked.json"))
  t.after(async () => { for (const dispose of state.disposers.reverse()) await dispose(); fs.rmSync(dir, { recursive: true, force: true }) })
  await plugin.tui(api, { bindingFile: path.join(dir, "binding.json") }, { id: plugin.id })
  async function call(method, route, value, token = binding.token) {
    const body = value === undefined ? undefined : JSON.stringify(value)
    return new Promise((resolve, reject) => {
      const req = http.request({ socketPath: binding.socketPath, path: route, method, headers: { authorization: `Bearer ${token}`, ...(body ? { "content-type": "application/json", "content-length": Buffer.byteLength(body) } : {}) } }, res => {
        const parts = []; res.on("data", part => parts.push(part)); res.on("error", reject); res.on("end", () => { try { resolve({ code: res.statusCode, body: JSON.parse(Buffer.concat(parts)) }) } catch (err) { reject(err) } })
      })
      req.setTimeout(7000, () => req.destroy(new Error("test request timeout")))
      req.on("error", reject); req.end(body)
    })
  }
  if (state.autoAck) { const ready = await call("GET", "/ready"); assert.equal(ready.body.bound, true, JSON.stringify(ready.body)); await call("POST", "/ack", { version: 1, sessionInstance: binding.sessionInstance, backendGeneration: binding.backendGeneration, rootID, nativeCreatedAtMS: row.time.created }) }
  const envelope = extra => ({ ...identity, operationID: crypto.randomBytes(16).toString("hex"), kind: "prompt", targetID: rootID, messageID: "msg_" + crypto.randomBytes(16).toString("hex"), text: "hello", ...extra })
  // workerID/directory are binding facts, not dispatch fields.
  const dispatch = extra => { const e = envelope(extra); delete e.workerID; delete e.directory; return call("POST", "/dispatch", e) }
  return { state, api, binding, call, dispatch }
}

test("private listener authenticates and never echoes its token", async t => {
  const f = await fixture(t)
  assert.equal((await f.call("GET", "/ready", undefined, "wrong")).code, 401)
  const ready = await f.call("GET", "/ready")
  assert.equal(ready.code, 200)
  assert.ok(!JSON.stringify(ready).includes(f.binding.token))
  assert.equal(fs.statSync(f.binding.socketPath).mode & 0o777, 0o600)
})

const supportedVersions = ["1.19.3", "1.19.4", "1.20.0", "2.0.0", "10000.0.0", "999999999999999999999999999999.0.0"]
for (const mode of ["create", "resume"]) for (const version of supportedVersions) test(`${mode} binds stable OpenCode ${version} and dispatches`, async t => {
  const f = await fixture(t, ({ binding, state }) => { binding.mode = mode; binding.nativeCreatedAtMS = mode === "resume" ? 123456 : 0; state.version = version })
  const ready = (await f.call("GET", "/ready")).body
  assert.equal(ready.nativeVersion, version)
  assert.equal(ready.ready, true)
  assert.equal(ready.acknowledged, true)
  assert.equal(ready.draftGuardSupported, true)
  assert.equal(f.state.calls.filter(call => call.name === (mode === "create" ? "create" : "v2get")).length, 1)
  assert.equal((await f.dispatch()).body.status, "admitted")
  assert.equal(f.state.calls.filter(call => call.name === "prompt").length, 1)
})

for (const version of supportedVersions) test(`bound bridge remains ready and dispatches on stable OpenCode ${version}`, async t => {
  const f = await fixture(t)
  f.state.version = version
  const ready = (await f.call("GET", "/ready")).body
  assert.equal(ready.nativeVersion, version)
  assert.equal(ready.ready, true)
  assert.equal(ready.draftGuardSupported, true)
  assert.equal((await f.dispatch()).body.status, "admitted")
  assert.equal(f.state.calls.filter(call => call.name === "prompt").length, 1)
})

const unsupportedVersions = ["0.99.99", "1.18.99", "1.19.2", "1.19.3-rc.1", "1.19.4-beta", "2.0.0-rc.1", "1.19.3+build", "2.0.0+build", "v1.19.3", "01.19.3", "1.019.3", "1.19.03", "1.19", "1.19.3.0", "1.19.3\n", "1.19.3\r", "1.19.3 ", " 1.19.3", "1.19.+3", "1.19.x", "", null, 1193]
for (const version of unsupportedVersions) test(`bootstrap refuses unsupported OpenCode ${JSON.stringify(version)} before native effects`, async t => {
  const f = await fixture(t, ({ state }) => { state.version = version; state.autoAck = false; state.route = { name: "home" } })
  const ready = (await f.call("GET", "/ready")).body
  assert.equal(ready.nativeVersion, version)
  assert.equal(ready.bound, false)
  assert.equal(ready.ready, false)
  assert.equal(ready.draftGuardSupported, false)
  assert.equal((await f.dispatch()).body.status, "rejected")
  assert.deepEqual(f.state.route, { name: "home" })
  assert.deepEqual(f.state.calls, [])
})

for (const version of unsupportedVersions) test(`bound bridge refuses unsupported OpenCode ${JSON.stringify(version)} before native effects`, async t => {
  const f = await fixture(t)
  f.state.calls = []
  f.state.version = version
  const ready = (await f.call("GET", "/ready")).body
  assert.equal(ready.nativeVersion, version)
  assert.equal(ready.bound, true)
  assert.equal(ready.ready, false)
  assert.equal(ready.draftGuardSupported, false)
  assert.deepEqual(ready.degraded, ["version_mismatch"])
  const receipt = (await f.dispatch()).body
  assert.equal(receipt.status, "rejected")
  assert.equal(receipt.reason, "version_mismatch")
  assert.deepEqual(f.state.calls, [])
})

test("native empty successful status map allows idle, cached missing status does not decide", async t => {
  const f = await fixture(t)
  const result = await f.dispatch()
  assert.equal(result.body.status, "admitted")
  assert.equal(result.body.inFlight, false)
  assert.equal(f.state.calls.filter(x => x.name === "prompt").length, 1)
  assert.ok(f.state.calls.some(x => x.name === "status"))
})

test("route is checked again after native status read and before SDK invocation", async t => {
  const f = await fixture(t)
  f.api.client.session.status = async () => { f.state.route = { name: "home" }; return { data: {}, response: { status: 200 } } }
  assert.equal((await f.dispatch()).body.status, "rejected")
  assert.equal(f.state.calls.filter(x => x.name === "prompt").length, 0)
})

test("semantic prompt forwards the authoritative native session tuple, never an implicit default", async t => {
  const f = await fixture(t, ({ state }) => { state.statuses[rootID] = { type: "idle" } })
  assert.equal((await f.dispatch()).body.status, "admitted")
  const sent = f.state.calls.find(x => x.name === "prompt")
  assert.equal(sent.input.agent, tuple.agent)
  assert.deepEqual(sent.input.model, { providerID: tuple.model.providerID, modelID: tuple.model.modelID })
  assert.equal(sent.input.variant, "max")
  assert.equal(sent.input.tools, undefined)
})

for (const status of [200, 400, 500]) test(`native status ${status} is not a confirmed 204 admission`, async t => {
  const f = await fixture(t, ({ state, api }) => { state.statuses[rootID] = { type: "idle" }; api.client.session.promptAsync = async () => ({ response: { status } }) })
  const out = await f.dispatch()
  assert.equal(out.body.status, status === 400 ? "rejected" : "uncertain")
})

test("synchronous throw after SDK invocation is uncertain", async t => {
  const f = await fixture(t, ({ state, api }) => { state.statuses[rootID] = { type: "idle" }; api.client.session.promptAsync = () => { throw new Error("may have sent") } })
  assert.equal((await f.dispatch()).body.status, "uncertain")
})

test("ignored SDK abort is bounded and cannot reopen mutation lane", { timeout: 9000 }, async t => {
  const f = await fixture(t, ({ state, api }) => { state.statuses[rootID] = { type: "idle" }; api.client.session.promptAsync = () => new Promise(() => {}) })
  const out = await f.dispatch()
  assert.equal(out.body.status, "uncertain")
  assert.equal((await f.dispatch()).body.status, "rejected")
})

test("valid whitespace and Unicode directory reaches the same bound route", async t => {
  const f = await fixture(t, ({ binding, state }) => { binding.directory = "/project/naïve work"; state.row.directory = binding.directory; state.row.location.directory = binding.directory; state.statuses[rootID] = { type: "idle" } })
  assert.equal((await f.call("GET", "/ready")).body.ready, true)
})

test("busy and pending requests refuse before native mutation", async t => {
  const f = await fixture(t, ({ state }) => { state.statuses[rootID] = { type: "busy" } })
  assert.equal((await f.dispatch()).body.status, "rejected")
  f.state.statuses = {}; f.state.permissions = [{ id: "per_pending", sessionID: rootID }]
  assert.equal((await f.dispatch()).body.status, "rejected")
  assert.equal(f.state.calls.filter(x => x.name === "prompt").length, 0)
})

test("204 without the exact native user row is uncertain", { timeout: 9000 }, async t => {
  const f = await fixture(t)
  f.api.client.session.promptAsync = async () => ({ response: { status: 204 } })
  const out = await f.dispatch()
  assert.equal(out.body.status, "uncertain")
  assert.match(out.body.messageID, /^msg_/)
})

test("binding is persisted and acknowledged before genuine native initial submission", async t => {
  const f = await fixture(t, ({ binding, state }) => { binding.initialPrompt = true; state.autoAck = false; state.messages = [] })
  const body = { version: 1, rootID, sessionInstance: identity.sessionInstance, backendGeneration: identity.backendGeneration, operationID: "initial_op", text: "Genuine initial instructions" }
  body.requestHash = crypto.createHash("sha256").update(body.text).digest("hex")
  assert.equal((await f.call("POST", "/initial", body)).body.reason, "binding_unacknowledged")
  assert.equal(f.state.calls.filter(x => x.name === "nativeSubmit").length, 0)
  assert.equal((await f.call("POST", "/ack", { version: 1, rootID, sessionInstance: identity.sessionInstance, backendGeneration: identity.backendGeneration, nativeCreatedAtMS: 123456 })).body.acknowledged, true)
  const receipt = (await f.call("POST", "/initial", body)).body
  assert.equal(receipt.status, "admitted")
  assert.equal(receipt.selectionBasis, "native_composer")
  assert.equal(receipt.agent, "plan")
  assert.equal(f.state.hostSubmitted, true)
  assert.equal(f.state.hostRef, f.state.prompt)
  assert.equal((await f.call("POST", "/initial", body)).body.messageID, receipt.messageID)
  assert.equal((await f.call("GET", "/initial")).body.messageID, receipt.messageID)
  assert.equal(f.state.calls.filter(x => x.name === "nativeSubmit").length, 1)
})

test("initial mode refuses another enabled external plugin before root allocation", async t => {
  const f = await fixture(t, ({ binding, state }) => { binding.initialPrompt = true; state.autoAck = false; state.plugins = [{ id: "other", source: "file", enabled: true }] })
  const ready = (await f.call("GET", "/ready")).body
  assert.equal(ready.bound, false)
  assert.equal(ready.initialSupported, false)
  assert.ok(ready.degraded.includes("initial_plugin_conflict"))
  assert.equal(f.state.calls.filter(x => x.name === "create").length, 0)
  assert.equal(f.state.promptProps, undefined)
})

test("resume never creates when exact saved native history is missing", async t => {
  const f = await fixture(t, ({ binding, state, api }) => { binding.mode = "resume"; binding.nativeCreatedAtMS = 123456; state.autoAck = false; api.client.v2.session.get = async () => { throw new Error("not found") } })
  assert.equal((await f.call("GET", "/ready")).body.bound, false)
  assert.equal(f.state.calls.filter(x => x.name === "create").length, 0)
})

test("snapshot retains raw rows, current requests and opaque native cursor", async t => {
  const f = await fixture(t)
  f.state.permissions = [{ id: "per_a", sessionID: rootID, metadata: { nativeUnknown: true } }]
  f.state.questions = [{ id: "que_a", sessionID: rootID, questions: [{ custom: false }] }]
  f.api.client.session.messages = async input => { assert.equal(input.before, "opaque+/?="); return { data: f.state.messages, response: { headers: new Headers({ "x-next-cursor": "next-opaque" }) } } }
  const snapshot = (await f.call("GET", "/snapshot?limit=2&before=" + encodeURIComponent("opaque+/?="))).body
  assert.deepEqual(snapshot.messages, f.state.messages)
  assert.deepEqual(snapshot.permissions, f.state.permissions)
  assert.deepEqual(snapshot.questions, f.state.questions)
  assert.equal(snapshot.nextCursor, "next-opaque")
})

test("binding parser errors never expose binding contents", async t => {
  const dir = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), "ocb-"))); fs.chmodSync(dir, 0o700)
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }))
  const file = path.join(dir, "binding.json")
  fs.writeFileSync(file, '{"token":"SECRET_BINDING_TOKEN",!}', { mode: 0o600 })
  await assert.rejects(plugin.tui({}, { bindingFile: file }), error => error.message === "binding_invalid")
})

test("weak bridge tokens and non-native message IDs refuse", async t => {
  await assert.rejects(fixture(t, ({ binding }) => { binding.token = "x" }), /binding_invalid/)
  const f = await fixture(t)
  assert.equal((await f.dispatch({ messageID: "msg-1" })).body.reason, "target_invalid")
  assert.equal(f.state.calls.filter(x => x.name === "prompt").length, 0)
})

test("missing native session selection refuses and does not guess from old history", async t => {
  const f = await fixture(t)
  delete f.state.row.model
  assert.equal((await f.dispatch()).body.reason, "selection_unavailable")
  assert.equal(f.state.calls.filter(x => x.name === "prompt").length, 0)
})


test("failed status reads never become idle", async t => {
  const f = await fixture(t)
  f.api.client.session.status = async () => ({ data: null, response: { status: 200 } })
  assert.equal((await f.dispatch()).body.reason, "native_read_failed")
  assert.equal(f.state.calls.filter(x => x.name === "prompt").length, 0)
})

test("initial readiness waits for the actual same-root native composer ref", async t => {
  const f = await fixture(t, ({ binding }) => { binding.initialPrompt = true })
  assert.equal((await f.call("GET", "/ready")).body.initialSupported, true)
  f.state.promptProps.ref(undefined)
  assert.equal((await f.call("GET", "/ready")).body.initialSupported, false)
  f.state.promptProps.ref(f.state.prompt)
  f.state.prompt.current.input = "owner draft"
  assert.equal((await f.call("GET", "/ready")).body.initialSupported, false)
  f.state.prompt.current.input = ""; f.state.dialog = true
  assert.equal((await f.call("GET", "/ready")).body.ready, false)
})

async function events(t, fixture) {
  const seen = [], stream = { seen, bytes: 0, ended: false }
  const req = http.get({ socketPath: fixture.binding.socketPath, path: "/events", headers: { authorization: `Bearer ${fixture.binding.token}` } }, res => {
    stream.res = res; let buffer = ""
    res.on("data", chunk => { stream.bytes += chunk.length; buffer += chunk; for (;;) { const at = buffer.indexOf("\n\n"); if (at < 0) break; const frame = buffer.slice(0, at); buffer = buffer.slice(at + 2); if (frame.startsWith("data: ")) seen.push(JSON.parse(frame.slice(6))) } })
    res.on("end", () => stream.ended = true); res.on("close", () => stream.ended = true); res.on("error", () => stream.ended = true)
  })
  req.on("error", () => stream.ended = true); t.after(() => req.destroy())
  await waitFor(() => seen.length > 0)
  return stream
}
async function waitFor(check) {
  const until = Date.now() + 2000
  while (!check()) { if (Date.now() >= until) throw new Error("bounded test wait expired"); await pause(5) }
}

test("event stream announces exact identity before raw native events and snapshot", async t => {
  const f = await fixture(t), stream = await events(t, f)
  assert.equal(stream.seen[0].type, "swarm.bridge.connected")
  assert.deepEqual(stream.seen[0].properties, { ...identity, nativeCreatedAtMS: 123456 })
  const native = { type: "session.status", properties: { sessionID: rootID, status: { type: "busy" } } }
  f.state.events.get(native.type)(native)
  await waitFor(() => stream.seen.length === 2)
  assert.deepEqual(stream.seen[1], native)
  const snapshot = await f.call("GET", "/snapshot")
  assert.equal(snapshot.body.backendGeneration, identity.backendGeneration)
})

test("oversized native events yield a gap and disconnect without their body", async t => {
  const f = await fixture(t), stream = await events(t, f)
  f.state.events.get("message.updated")({ type: "message.updated", properties: { text: "x".repeat((1 << 20) + 1) } })
  await waitFor(() => stream.ended)
  assert.ok(stream.seen.some(event => event.type === "swarm.bridge.gap"))
  assert.ok(stream.bytes < 4096)
})

test("slow event readers are disconnected within the bounded queue", async t => {
  const f = await fixture(t), stream = await events(t, f)
  stream.res.pause()
  for (let i = 0; i < 150; i++) f.state.events.get("message.updated")({ type: "message.updated", properties: { text: "x".repeat(32768) } })
  await pause(150); stream.res.resume()
  await waitFor(() => stream.ended)
  assert.ok(stream.bytes < (2 << 20))
  assert.equal((await f.call("GET", "/ready")).body.bound, true)
})

for (const duplicate of ['"token"', '"to\\u006ben"']) test(`binding rejects duplicate decoded key ${duplicate}`, async t => {
  await assert.rejects(fixture(t, ({ state }) => { state.bindingRaw = binding => JSON.stringify(binding).replace(/}$/, `,${duplicate}:${JSON.stringify(binding.token)}}`) }), /binding_invalid/)
})

test("binding rejects hardlinks and a socket in another private directory", async t => {
  await assert.rejects(fixture(t, ({ state }) => { state.hardlinkBinding = true }), /binding_invalid/)
  await assert.rejects(fixture(t, ({ binding }) => { const other = path.join(path.dirname(binding.socketPath), "other"); fs.mkdirSync(other, { mode: 0o700 }); binding.socketPath = path.join(other, "bridge.sock") }), /binding_invalid/)
})


test("host disconnect during native reads prevents a later SDK write", async t => {
  const f = await fixture(t)
  let release, entered = false
  f.api.client.session.status = () => { entered = true; return new Promise(resolve => { release = () => resolve({ data: {}, response: { status: 200 } }) }) }
  const body = JSON.stringify({ version: 1, sessionInstance: identity.sessionInstance, backendGeneration: identity.backendGeneration, rootID, operationID: "disconnected_op", kind: "prompt", targetID: rootID, messageID: "msg_" + "d".repeat(32), text: "must never send" })
  const req = http.request({ socketPath: f.binding.socketPath, path: "/dispatch", method: "POST", headers: { authorization: `Bearer ${f.binding.token}`, "content-length": Buffer.byteLength(body) } })
  req.on("error", () => {}); req.end(body)
  await waitFor(() => entered)
  req.destroy(); await pause(25); release()
  await pause(25)
  assert.equal(f.state.calls.filter(call => call.name === "prompt").length, 0)
})

test("a pre-existing native message ID refuses before native mutation", async t => {
  const f = await fixture(t)
  f.state.messages[0].info.time = { created: 1 }
  f.state.messages[0].parts[0].text = "hello"
  const receipt = (await f.dispatch({ messageID })).body
  assert.equal(receipt.reason, "native_message_exists")
  assert.equal(f.state.calls.filter(call => call.name === "prompt").length, 0)
})


test("unrelated native requests do not block this root; nested descendants do", async t => {
  const f = await fixture(t), other = "ses_" + "d".repeat(32), middle = "ses_" + "e".repeat(32)
  f.state.questions = [{ id: "que_pending", sessionID: other }]
  const rows = new Map([[rootID, f.state.row], [other, { id: other, directory: "/other", time: { created: 1 } }]])
  f.api.client.session.get = async ({ sessionID }) => ({ data: rows.get(sessionID), response: { status: 200 } })
  assert.equal((await f.dispatch()).body.status, "admitted")
  rows.get(other).parentID = middle; rows.set(middle, { id: middle, parentID: rootID })
  assert.equal((await f.dispatch()).body.reason, "native_request_pending")
  assert.equal(f.state.calls.filter(call => call.name === "prompt").length, 1)
  rows.get(middle).parentID = other
  assert.equal((await f.dispatch()).body.reason, "request_scope_unknown")
  f.state.questions = [{ id: "que_bad" }]
  assert.equal((await f.dispatch()).body.reason, "native_shape")
})


test("an unexpected response error after native invocation remains uncertain", async t => {
  const f = await fixture(t)
  f.api.client.session.promptAsync = async () => ({ get response() { throw new Error("invalid native response") } })
  assert.equal((await f.dispatch()).body.status, "uncertain")
})


const permissionID = "per_" + "f".repeat(32)
const questionID = "que_" + "f".repeat(32)
const requestBody = extra => ({ version: 1, sessionInstance: identity.sessionInstance, backendGeneration: identity.backendGeneration, rootID, operationID: crypto.randomBytes(16).toString("hex"), kind: "permission_reply", targetID: rootID, requestID: permissionID, reply: "once", ...extra })
const question = (extra = {}) => ({ question: "Choose", header: "Choice", options: [{ label: "A", description: "first" }, { label: "B", description: "second" }], ...extra })

test("permission reply targets one exact pending request while the native root is busy", async t => {
  const f = await fixture(t)
  f.state.statuses[rootID] = { type: "busy" }
  f.state.permissions = [{ id: permissionID, sessionID: rootID }, { id: "per_" + "e".repeat(32), sessionID: rootID }]
  const body = requestBody(), receipt = (await f.call("POST", "/dispatch", body)).body
  assert.equal(receipt.status, "admitted")
  assert.equal(receipt.kind, body.kind)
  assert.equal(receipt.requestID, permissionID)
  assert.equal(receipt.targetID, rootID)
  assert.equal(receipt.nativeEffect, "single_request")
  assert.equal(receipt.inFlight, false)
  assert.deepEqual(f.state.calls.find(call => call.name === "permissionReply").input, { requestID: permissionID, reply: "once" })
  assert.equal(f.state.permissions.length, 1)
})

test("request replies verify nested ancestry and refuse unrelated sessions", async t => {
  const f = await fixture(t), child = "ses_" + "d".repeat(32), middle = "ses_" + "e".repeat(32)
  const rows = new Map([[rootID, f.state.row], [child, { id: child, parentID: middle }], [middle, { id: middle, parentID: rootID }]])
  f.api.client.session.get = async ({ sessionID }) => ({ data: rows.get(sessionID), response: { status: 200 } })
  f.state.permissions = [{ id: permissionID, sessionID: child }]
  assert.equal((await f.call("POST", "/dispatch", requestBody({ targetID: child }))).body.status, "admitted")
  delete rows.get(middle).parentID
  const otherID = "per_" + "e".repeat(32)
  f.state.permissions = [{ id: otherID, sessionID: child }]
  assert.equal((await f.call("POST", "/dispatch", requestBody({ targetID: child, requestID: otherID }))).body.reason, "request_unrelated")
  assert.equal(f.state.calls.filter(call => call.name === "permissionReply").length, 1)
})

test("stale, duplicated, mismatched and route-drifted requests never reach native reply", async t => {
  const f = await fixture(t)
  assert.equal((await f.call("POST", "/dispatch", requestBody())).body.reason, "request_stale")
  f.state.permissions = [{ id: permissionID, sessionID: rootID }, { id: permissionID, sessionID: rootID }]
  assert.equal((await f.call("POST", "/dispatch", requestBody())).body.reason, "native_shape")
  f.state.permissions.pop()
  assert.equal((await f.call("POST", "/dispatch", requestBody({ targetID: "ses_" + "c".repeat(32) }))).body.reason, "request_target_mismatch")
  f.api.client.permission.list = async () => { f.state.route = { name: "home" }; return { data: f.state.permissions, response: { status: 200 } } }
  assert.equal((await f.call("POST", "/dispatch", requestBody())).body.reason, "route_mismatch")
  assert.equal(f.state.calls.filter(call => call.name === "permissionReply").length, 0)
})

test("question answers honor option restrictions, custom defaults and multiple selections", async t => {
  const f = await fixture(t)
  f.state.questions = [{ id: questionID, sessionID: rootID, questions: [question({ custom: false }), question({ multiple: true })] }]
  const body = requestBody({ kind: "question_reply", requestID: questionID, reply: undefined, answers: [["outside"], ["A", "B"]] })
  assert.equal((await f.call("POST", "/dispatch", body)).body.reason, "answer_invalid")
  body.answers = [["A", "B"], []]
  assert.equal((await f.call("POST", "/dispatch", body)).body.reason, "answer_invalid")
  body.answers = [["A"], ["custom\nanswer", "B"]]
  const receipt = (await f.call("POST", "/dispatch", body)).body
  assert.equal(receipt.status, "admitted")
  assert.deepEqual(f.state.calls.find(call => call.name === "questionReply").input, { requestID: questionID, answers: body.answers })
})

test("an awaited native true reply qualifies even when the event is delayed", async t => {
  const f = await fixture(t)
  f.state.permissions = [{ id: permissionID, sessionID: rootID }]
  f.api.client.permission.reply = async () => ({ data: true, response: { status: 200 } })
  assert.equal((await f.call("POST", "/dispatch", requestBody())).body.status, "admitted")
})

test("native disappearance and an unrelated acknowledgement never prove a request reply", async t => {
  const f = await fixture(t)
  f.state.permissions = [{ id: permissionID, sessionID: rootID }]
  f.api.client.permission.reply = async () => {
    f.state.permissions = []
    f.state.events.get("permission.replied")({ type: "permission.replied", properties: { sessionID: rootID, requestID: permissionID, reply: "always" } })
    return { data: false, response: { status: 200 } }
  }
  const body = requestBody()
  assert.equal((await f.call("POST", "/dispatch", body)).body.status, "uncertain")
  assert.equal((await f.call("GET", "/receipt?operationID=" + body.operationID)).body.status, "uncertain")
})

test("lost reply response reconciles from an exact late event without a second write", async t => {
  const f = await fixture(t)
  f.state.permissions = [{ id: permissionID, sessionID: rootID }]
  let calls = 0
  f.api.client.permission.reply = async () => { calls++; throw new Error("lost native response") }
  const body = requestBody()
  assert.equal((await f.call("POST", "/dispatch", body)).body.status, "uncertain")
  assert.equal((await f.call("POST", "/dispatch", body)).body.status, "uncertain")
  assert.equal((await f.call("POST", "/dispatch", { ...body, reply: "always" })).body.reason, "operation_conflict")
  assert.equal((await f.call("POST", "/dispatch", { ...body, operationID: "another_operation" })).body.reason, "request_unresolved")
  f.state.events.get("permission.replied")({ type: "permission.replied", properties: { sessionID: rootID, requestID: permissionID, reply: "once" } })
  assert.equal((await f.call("GET", "/receipt?operationID=" + body.operationID)).body.status, "admitted")
  assert.equal((await f.call("POST", "/dispatch", body)).body.status, "admitted")
  assert.equal(calls, 1)
  assert.equal((await f.call("GET", "/receipt?operationID=missing_operation")).body, null)
})

test("question rejection and answer reconciliation require the exact native event", async t => {
  const f = await fixture(t)
  f.state.questions = [{ id: questionID, sessionID: rootID, questions: [question()] }]
  f.api.client.question.reply = () => { throw new Error("lost response") }
  const answer = requestBody({ kind: "question_reply", requestID: questionID, reply: undefined, answers: [["A"]] })
  assert.equal((await f.call("POST", "/dispatch", answer)).body.status, "uncertain")
  f.state.events.get("question.replied")({ type: "question.replied", properties: { sessionID: rootID, requestID: questionID, answers: [["B"]] } })
  assert.equal((await f.call("GET", "/receipt?operationID=" + answer.operationID)).body.status, "uncertain")
  f.state.events.get("question.replied")({ type: "question.replied", properties: { sessionID: rootID, requestID: questionID, answers: [["A"]] } })
  assert.equal((await f.call("GET", "/receipt?operationID=" + answer.operationID)).body.status, "admitted")
  const secondID = "que_" + "e".repeat(32)
  f.state.questions = [{ id: secondID, sessionID: rootID, questions: [question()] }]
  const reject = requestBody({ kind: "question_reject", requestID: secondID, reply: undefined })
  assert.equal((await f.call("POST", "/dispatch", reject)).body.status, "admitted")
  assert.deepEqual(f.state.calls.find(call => call.name === "questionReject").input, { requestID: secondID })
})


test("uncertain prompt receipts retain caller identity and recover by exact native message without replay", async t => {
  const f = await fixture(t)
  const operationID = "late_prompt", id = "msg_" + "e".repeat(32)
  let calls = 0, request
  f.api.client.session.promptAsync = async input => { calls++; request = input; return { response: { status: 204 } } }
  const first = (await f.dispatch({ operationID, messageID: id })).body
  assert.equal(first.status, "uncertain")
  assert.equal(first.messageID, id)
  assert.equal((await f.dispatch({ operationID, messageID: id, text: "different" })).body.reason, "operation_conflict")
  f.api.client.session.messages = () => { throw new Error("receipt recovery must use the exact message endpoint") }
  f.state.messages.push({ info: { id, sessionID: rootID, role: "user", time: { created: Date.now() }, agent: request.agent, model: { ...request.model, variant: request.variant } }, parts: [{ id: "prt_" + "e".repeat(32), sessionID: rootID, messageID: id, type: "text", text: request.parts[0].text }] })
  assert.equal((await f.call("GET", "/receipt?operationID=" + operationID)).body.status, "admitted")
  assert.equal((await f.dispatch({ operationID, messageID: id })).body.status, "admitted")
  assert.equal(calls, 1)
})


for (const [reply, effect] of [["reject", "reject_session_pending_permissions"], ["always", "approve_native_patterns_and_matching_session_requests"]]) test(`permission ${reply} exposes its native scope`, async t => {
  const f = await fixture(t)
  f.state.permissions = [{ id: permissionID, sessionID: rootID }]
  const receipt = (await f.call("POST", "/dispatch", requestBody({ reply }))).body
  assert.equal(receipt.status, "admitted")
  assert.equal(receipt.nativeEffect, effect)
  assert.equal(f.state.calls.find(call => call.name === "permissionReply").input.reply, reply)
})

test("receipt capacity retains uncertain records and only evicts terminal ones", async t => {
  const f = await fixture(t)
  let calls = 0
  f.api.client.permission.reply = async () => { calls++; return { data: false, response: { status: 200 } } }
  for (let i = 0; i < 128; i++) {
    const id = "per_" + i.toString(16).padStart(32, "0")
    f.state.permissions = [{ id, sessionID: rootID }]
    assert.equal((await f.call("POST", "/dispatch", requestBody({ operationID: "capacity_" + i, requestID: id }))).body.status, "uncertain")
  }
  const body = requestBody({ operationID: "capacity_next" })
  f.state.permissions = [{ id: permissionID, sessionID: rootID }]
  assert.equal((await f.call("POST", "/dispatch", body)).body.reason, "receipt_capacity")
  assert.equal(calls, 128)
  assert.equal((await f.call("GET", "/receipt?operationID=capacity_0")).body.status, "uncertain")
  f.state.events.get("permission.replied")({ type: "permission.replied", properties: { sessionID: rootID, requestID: "per_" + "0".repeat(32), reply: "once" } })
  assert.equal((await f.call("POST", "/dispatch", body)).body.status, "uncertain")
  assert.equal(calls, 129)
  assert.equal((await f.call("GET", "/receipt?operationID=capacity_0")).body, null)
  assert.equal((await f.call("GET", "/receipt?operationID=capacity_1")).body.status, "uncertain")
})


for (const status of [200, 401, 500]) test(`message lookup ${status} never proves caller ID unused`, async t => {
  const f = await fixture(t)
  f.api.client.session.message = async () => ({ response: { status } })
  assert.equal((await f.dispatch()).body.status, "rejected")
  assert.equal(f.state.calls.filter(call => call.name === "prompt").length, 0)
})

test("a thrown message lookup never becomes absence", async t => {
  const f = await fixture(t)
  f.api.client.session.message = () => { throw { status: 404 } }
  assert.equal((await f.dispatch()).body.reason, "native_read_failed")
  assert.equal(f.state.calls.filter(call => call.name === "prompt").length, 0)
})


test("snapshot verifies current activity ancestry without enumerating historical children", async t => {
  const f = await fixture(t), child = "ses_" + "d".repeat(32), middle = "ses_" + "e".repeat(32), unrelated = "ses_" + "f".repeat(32)
  const rows = new Map([[rootID, f.state.row], [child, { id: child, parentID: middle, title: "Working child" }], [middle, { id: middle, parentID: rootID, title: "Required ancestor" }], [unrelated, { id: unrelated }]])
  f.api.client.session.get = async ({ sessionID }) => ({ data: rows.get(sessionID), response: { status: 200 } })
  f.api.client.session.children = () => { throw new Error("unpaginated historical children must not gate readiness") }
  f.state.statuses = { [child]: { type: "retry", attempt: 1 }, [unrelated]: { type: "busy" } }
  let snapshot = (await f.call("GET", "/snapshot")).body
  assert.equal(snapshot.hierarchyComplete, true)
  assert.equal(snapshot.hierarchyScope, "current_activity_requests")
  assert.deepEqual(snapshot.descendants.map(row => row.id), [middle, child])
  assert.deepEqual(snapshot.children.map(row => row.id), [middle])
  assert.equal((await f.dispatch()).body.reason, "native_busy")
  f.state.statuses = { [child]: { type: "idle" } }
  snapshot = (await f.call("GET", "/snapshot")).body
  assert.equal(snapshot.hierarchyComplete, true)
  assert.deepEqual(snapshot.descendants, [])
  assert.deepEqual(snapshot.children, [])
  assert.equal((await f.dispatch()).body.status, "admitted")
})

test("quiet descendant requests remain in current scope and prevent new prompts", async t => {
  const f = await fixture(t), child = "ses_" + "d".repeat(32)
  f.api.client.session.get = async ({ sessionID }) => ({ data: sessionID === rootID ? f.state.row : { id: child, parentID: rootID }, response: { status: 200 } })
  f.state.questions = [{ id: questionID, sessionID: child, questions: [question()] }]
  const snapshot = (await f.call("GET", "/snapshot")).body
  assert.equal(snapshot.hierarchyComplete, true)
  assert.deepEqual(snapshot.descendants.map(row => row.id), [child])
  assert.equal((await f.dispatch()).body.reason, "native_request_pending")
  assert.equal((await f.call("POST", "/dispatch", requestBody({ kind: "question_reply", targetID: child, requestID: questionID, reply: undefined, answers: [["A"]] }))).body.status, "admitted")
})

test("unknown or cyclic active ancestry cannot become a complete quiet snapshot", async t => {
  const f = await fixture(t), child = "ses_" + "d".repeat(32), other = "ses_" + "e".repeat(32)
  f.state.statuses = { [child]: { type: "busy" } }
  f.api.client.session.get = async ({ sessionID }) => ({ data: sessionID === rootID ? f.state.row : { id: sessionID, parentID: sessionID === child ? other : child }, response: { status: 200 } })
  assert.equal((await f.call("GET", "/snapshot")).body.reason, "request_scope_unknown")
  assert.equal((await f.dispatch()).body.reason, "request_scope_unknown")
  f.api.client.session.get = async ({ sessionID }) => ({ data: sessionID === rootID ? f.state.row : null, response: { status: sessionID === rootID ? 200 : 404 } })
  assert.equal((await f.call("GET", "/snapshot")).body.reason, "native_read_failed")
  assert.equal((await f.dispatch()).body.status, "rejected")
  assert.equal(f.state.calls.filter(call => call.name === "prompt").length, 0)
})

test("activity candidates and native ancestry reads have explicit bounds", async t => {
  const f = await fixture(t)
  const id = i => "ses_" + i.toString(16).padStart(32, "0")
  f.state.statuses = Object.fromEntries(Array.from({ length: 129 }, (_, i) => [id(i), { type: "busy" }]))
  assert.equal((await f.call("GET", "/snapshot")).body.reason, "scope_limit")
  f.state.statuses = Object.fromEntries(Array.from({ length: 33 }, (_, i) => [id(i), { type: "busy" }]))
  let reads = 0
  f.api.client.session.get = async ({ sessionID }) => { if (sessionID !== rootID) reads++; return { data: sessionID === rootID ? f.state.row : { id: sessionID }, response: { status: 200 } } }
  assert.equal((await f.call("GET", "/snapshot")).body.reason, "request_scope_unknown")
  assert.equal(reads, 32)
})


test("exact message reads retain native data across route drift and distinguish native absence", async t => {
  const f = await fixture(t)
  f.state.route = { name: "home" }
  f.state.messages[0].nativeExtension = { preserved: true }
  const exact = await f.call("GET", "/message?messageID=" + messageID)
  assert.equal(exact.code, 200)
  assert.deepEqual(exact.body, f.state.messages[0])
  const missing = await f.call("GET", "/message?messageID=msg_" + "0".repeat(32))
  assert.equal(missing.code, 404)
  assert.equal(missing.body.reason, "native_message_missing")
  f.api.client.session.message = async () => ({ response: { status: 500 } })
  assert.notEqual((await f.call("GET", "/message?messageID=" + messageID)).code, 404)
  f.state.row.time.created++
  assert.equal((await f.call("GET", "/message?messageID=" + messageID)).body.reason, "native_binding_mismatch")
})

test("exact message reads reject foreign native parts and IDs", async t => {
  const f = await fixture(t)
  f.state.messages[0].parts[0].sessionID = "ses_" + "0".repeat(32)
  assert.equal((await f.call("GET", "/message?messageID=" + messageID)).body.reason, "native_shape")
  f.state.messages[0].parts[0].sessionID = rootID
  const original = f.api.client.session.message
  f.api.client.session.message = () => original({ messageID })
  assert.equal((await f.call("GET", "/message?messageID=msg_" + "0".repeat(32))).body.reason, "native_shape")
})

test("cached ancestry cannot hide a descendant beyond the depth bound", async t => {
  const f = await fixture(t), id = depth => "ses_" + depth.toString(16).padStart(32, "0")
  const rows = new Map([[rootID, f.state.row]])
  for (let depth = 1; depth <= 9; depth++) rows.set(id(depth), { id: id(depth), parentID: depth === 1 ? rootID : id(depth - 1) })
  f.api.client.session.get = async ({ sessionID }) => ({ data: rows.get(sessionID), response: { status: 200 } })
  f.state.statuses = { [id(8)]: { type: "busy" }, [id(9)]: { type: "busy" } }
  assert.equal((await f.call("GET", "/snapshot")).body.reason, "request_scope_unknown")
})


test("managed non-initial sessions guard the actual native composer after a shim Enter reset", async t => {
  const f = await fixture(t)
  assert.equal((await f.call("GET", "/ready")).body.draftGuardSupported, true)
  assert.equal(f.state.hostRef, f.state.prompt)
  f.state.prompt.current = { input: "native queued or refused draft", parts: [] }
  // A cleared PTY line tracker is not proof that the native composer accepted Enter.
  f.state.shimLine = ""
  assert.equal((await f.call("GET", "/ready")).body.draftGuardSupported, false)
  assert.equal((await f.dispatch()).body.reason, "native_draft_occupied")
  assert.equal(f.state.prompt.current.input, "native queued or refused draft")
  assert.equal(f.state.calls.filter(call => call.name === "prompt").length, 0)
  f.state.prompt.current = { input: "", parts: [{ type: "file", url: "file:///private/draft" }] }
  assert.equal((await f.dispatch()).body.reason, "native_draft_occupied")
  f.state.prompt.current = { input: "", parts: [] }
  assert.equal((await f.dispatch()).body.status, "admitted")
})

test("native composer is rechecked after asynchronous reads immediately before dispatch", async t => {
  const f = await fixture(t)
  const lookup = f.api.client.session.message
  f.api.client.session.message = async input => { const result = await lookup(input); f.state.prompt.current.input = "late native draft"; return result }
  assert.equal((await f.dispatch()).body.reason, "native_draft_occupied")
  assert.equal(f.state.calls.filter(call => call.name === "prompt").length, 0)
})

test("non-initial external-plugin profiles preserve their composer and withhold draft qualification", async t => {
  const f = await fixture(t, ({ state }) => { state.plugins = [{ id: "owner-plugin", source: "file", enabled: true }] })
  const ready = (await f.call("GET", "/ready")).body
  assert.equal(ready.bound, true)
  assert.equal(ready.draftGuardSupported, false)
  assert.equal(f.state.promptProps, undefined)
  assert.equal((await f.dispatch()).body.reason, "native_draft_unavailable")
  assert.equal(f.state.calls.filter(call => call.name === "prompt").length, 0)
})


test("binding-valid preflight refusal is a correlated receipt rather than an uncertain transport error", async t => {
  const f = await fixture(t), operationID = "definitive_refusal", id = "msg_" + "e".repeat(32)
  f.state.route = { name: "home" }
  const result = await f.dispatch({ operationID, messageID: id })
  assert.equal(result.code, 200)
  assert.equal(result.body.status, "rejected")
  assert.equal(result.body.operationID, operationID)
  assert.equal(result.body.messageID, id)
  assert.equal(result.body.kind, "prompt")
  assert.equal(result.body.targetID, rootID)
  assert.equal(result.body.nativeCreatedAtMS, 123456)
  assert.equal(result.body.reason, "route_mismatch")
  assert.equal(f.state.calls.filter(call => call.name === "prompt").length, 0)
  assert.equal((await f.dispatch({ rootID: "ses_" + "f".repeat(32) })).code, 422)
})

function initialBody(extra = {}) {
  const text = "Genuine initial instructions"
  return { version: 1, rootID, sessionInstance: identity.sessionInstance, backendGeneration: identity.backendGeneration, operationID: "initial_preflight", text, requestHash: crypto.createHash("sha256").update(text).digest("hex"), ...extra }
}

test("initial requested choices use the fresh native launch catalogue before touching the composer", async t => {
  const f = await fixture(t, ({ binding, state }) => { binding.initialPrompt = true; state.messages = [] })
  const body = initialBody({ expectedModel: "opencode/deepseek-v4.1-flash-free", expectedAgent: "plan" })
  assert.equal((await f.call("POST", "/initial", body)).body.status, "admitted")
  const names = f.state.calls.map(call => call.name)
  assert.ok(names.indexOf("providers") >= 0 && names.indexOf("providers") < names.indexOf("nativeSubmit"))
  assert.ok(names.indexOf("agents") >= 0 && names.indexOf("agents") < names.indexOf("nativeSubmit"))
  assert.equal((await f.call("POST", "/initial", { ...body, expectedAgent: "build" })).body.reason, "initial_conflict")
  assert.equal(f.state.calls.filter(call => call.name === "nativeSubmit").length, 1)
})

for (const [choices, reason] of [
  [{ expectedModel: "opencode/typo" }, "initial_model_unavailable"],
  [{ expectedModel: "disconnected/deepseek-v4.1-flash-free" }, "initial_model_unavailable"],
  [{ expectedModel: "missing-slash" }, "initial_choice_invalid"],
  [{ expectedModel: "opencode/" }, "initial_choice_invalid"],
  [{ expectedModel: "opencode/" + "x".repeat(513) }, "initial_choice_invalid"],
  [{ expectedAgent: "typo" }, "initial_agent_unavailable"],
  [{ expectedAgent: null }, "initial_choice_invalid"],
]) test(`initial invalid requested choice refuses without native submission: ${reason} ${JSON.stringify(choices).slice(0, 70)}`, async t => {
  const f = await fixture(t, ({ binding, state }) => { binding.initialPrompt = true; state.messages = [] })
  const out = await f.call("POST", "/initial", initialBody(choices))
  assert.equal(out.body.reason, reason)
  assert.equal(f.state.calls.filter(call => call.name === "nativeSubmit").length, 0)
  assert.deepEqual(f.state.prompt.current, { input: "", parts: [] })
})

for (const agent of [{ name: "plan", mode: "subagent" }, { name: "plan", mode: "primary", hidden: true }]) test(`initial agent cannot silently fall back from ${JSON.stringify(agent)}`, async t => {
  const f = await fixture(t, ({ binding, state, api }) => { binding.initialPrompt = true; state.messages = []; api.client.app.agents = async () => ({ data: [agent] }) })
  assert.equal((await f.call("POST", "/initial", initialBody({ expectedAgent: "plan" }))).body.reason, "initial_agent_unavailable")
  assert.equal(f.state.calls.filter(call => call.name === "nativeSubmit").length, 0)
})

test("initial create refuses native history that arrived before submission", async t => {
  const f = await fixture(t, ({ binding }) => { binding.initialPrompt = true })
  assert.equal((await f.call("POST", "/initial", initialBody())).body.reason, "initial_history_present")
  assert.equal(f.state.calls.filter(call => call.name === "nativeSubmit").length, 0)
})

test("initial catalogue read failure and route changes cannot submit", async t => {
  const f = await fixture(t, ({ binding, state }) => { binding.initialPrompt = true; state.messages = [] })
  f.api.client.config.providers = async () => ({ data: null })
  assert.equal((await f.call("POST", "/initial", initialBody({ expectedModel: "opencode/valid" }))).body.reason, "native_read_failed")
  assert.equal(f.state.calls.filter(call => call.name === "nativeSubmit").length, 0)
  const g = await fixture(t, ({ binding, state }) => { binding.initialPrompt = true; state.messages = [] })
  g.api.client.config.providers = async () => { g.state.route = { name: "home" }; return { data: { providers: [{ id: "opencode", models: { valid: {} } }] } } }
  assert.equal((await g.call("POST", "/initial", initialBody({ expectedModel: "opencode/valid" }))).body.reason, "route_mismatch")
  assert.equal(g.state.calls.filter(call => call.name === "nativeSubmit").length, 0)
})

test("initial model IDs retain namespace slashes and omitted choices retain native defaults", async t => {
  const f = await fixture(t, ({ binding, state, api }) => { binding.initialPrompt = true; state.messages = []; api.client.config.providers = async () => ({ data: { providers: [{ id: "fixture", models: { "namespace/model": { id: "namespace/model" } } }] } }) })
  assert.equal((await f.call("POST", "/initial", initialBody({ expectedModel: "fixture/namespace/model" }))).body.status, "admitted")
  assert.equal(f.state.calls.some(call => call.name === "agents"), false)
})

test("request content guard binds fresh native metadata and live receipt identity", async t => {
  const f = await fixture(t)
  const pending = { id: permissionID, sessionID: rootID, patterns: ["safe command"], metadata: { detail: "original" } }
  f.state.permissions = [pending]
  const body = requestBody({ expectedRequest: structuredClone(pending), requestExpiresAtMS: Date.now() + 5000 })
  f.state.permissions[0].metadata.detail = "changed"
  assert.equal((await f.call("POST", "/dispatch", body)).body.reason, "request_content_changed")
  assert.equal(f.state.calls.filter(call => call.name === "permissionReply").length, 0)
  f.state.permissions[0].metadata.detail = "original"
  body.expectedRequest = { metadata: { detail: "original" }, patterns: ["safe command"], sessionID: rootID, id: permissionID }
  assert.equal((await f.call("POST", "/dispatch", body)).body.status, "admitted")
  assert.equal((await f.call("POST", "/dispatch", { ...body, expectedRequest: { ...body.expectedRequest, extra: "changed" } })).body.reason, "operation_conflict")
  assert.equal(f.state.calls.filter(call => call.name === "permissionReply").length, 1)
})

test("request expiry is checked after native preflight and before SDK reply", async t => {
  const f = await fixture(t)
  const pending = { id: permissionID, sessionID: rootID }
  f.state.permissions = [pending]
  const body = requestBody({ expectedRequest: pending, requestExpiresAtMS: Date.now() + 50 })
  f.api.client.session.status = async () => { await pause(75); return { data: {} } }
  assert.equal((await f.call("POST", "/dispatch", body)).body.reason, "request_expired")
  assert.equal(f.state.calls.filter(call => call.name === "permissionReply").length, 0)
})

test("configured native primary agent with nullable hidden remains selectable", async t => {
  const f = await fixture(t, ({ binding, state, api }) => { binding.initialPrompt = true; state.messages = []; api.client.app.agents = async () => ({ data: [{ name: "build", mode: "primary", hidden: null }] }) })
  const result = (await f.call("POST", "/initial", initialBody({ expectedAgent: "build" }))).body
  assert.equal(result.status, "admitted")
  assert.equal(f.state.calls.filter(call => call.name === "nativeSubmit").length, 1)
})

test("initial preflight refusal is a cached no-effect receipt and cannot later match owner text", async t => {
  const f = await fixture(t, ({ binding, state }) => { binding.initialPrompt = true; state.messages = [] })
  const body = initialBody({ expectedModel: "opencode/typo" })
  const response = await f.call("POST", "/initial", body)
  assert.equal(response.code, 200)
  assert.equal(response.body.status, "rejected")
  assert.equal(response.body.reason, "initial_model_unavailable")
  assert.equal(response.body.nativeEffect, "none")
  assert.equal(response.body.operationID, body.operationID)
  assert.equal(response.body.requestHash, body.requestHash)
  assert.equal(response.body.inFlight, false)
  assert.equal(response.body.messageID, undefined)
  f.state.messages = [{ info: { id: messageID, sessionID: rootID, role: "user", time: { created: Date.now() }, ...tuple }, parts: [{ id: "prt_owner", sessionID: rootID, messageID, type: "text", text: body.text }] }]
  assert.equal((await f.call("GET", "/initial")).body.status, "rejected")
  assert.equal((await f.call("POST", "/initial", body)).body.status, "rejected")
  assert.equal((await f.call("POST", "/initial", { ...body, expectedModel: "opencode/deepseek-v4.1-flash-free" })).body.reason, "initial_conflict")
  assert.equal(f.state.calls.filter(call => call.name === "nativeSubmit").length, 0)
  assert.deepEqual(f.state.prompt.current, { input: "", parts: [] })
})

test("SSE peer disconnect releases capacity when Bun omits ServerResponse close", async t => {
  const originalEmit = http.ServerResponse.prototype.emit
  http.ServerResponse.prototype.emit = function(event, ...args) {
    if (event === "close") return false
    return originalEmit.call(this, event, ...args)
  }
  t.after(() => { http.ServerResponse.prototype.emit = originalEmit })
  const f = await fixture(t)
  const open = () => new Promise((resolve, reject) => {
    const req = http.get({ socketPath: f.binding.socketPath, path: "/events", headers: { authorization: "Bearer " + f.binding.token } }, res => {
      res.once("data", () => resolve({ req, res, status: res.statusCode }))
      res.on("error", () => {})
    })
    req.on("error", reject)
  })
  const anchor = await open()
  t.after(() => { anchor.res.destroy(); anchor.req.destroy() })
  for (let i = 0; i < 8; i++) {
    const client = await open()
    client.res.destroy(); client.req.destroy()
    assert.equal(client.status, 200, `reconnect ${i} exhausted leaked stream slots`)
    await pause(15)
  }
})

test("exact descendant reads prove current ancestry without depending on active membership", async t => {
  const f = await fixture(t), target = "ses_" + "d".repeat(32), parent = "ses_" + "e".repeat(32)
  const childRow = structuredClone(f.state.messages[0])
  childRow.info.sessionID = target
  for (const part of childRow.parts) part.sessionID = target
  const rows = new Map([[rootID, f.state.row], [target, { id: target, parentID: parent }], [parent, { id: parent, parentID: rootID }]])
  f.api.client.session.get = async ({ sessionID }) => ({ data: rows.get(sessionID), response: { status: 200 } })
  let messageReads = 0
  f.api.client.session.message = async input => { messageReads++; assert.equal(input.sessionID, target); return { data: childRow, response: { status: 200 } } }
  f.state.route = { name: "home" }
  assert.equal(Object.keys(f.state.statuses).length, 0)
  const route = "/message?sessionID=" + target + "&messageID=" + messageID
  assert.deepEqual((await f.call("GET", route)).body, childRow)
  assert.equal(messageReads, 1)
  rows.get(parent).parentID = target
  assert.equal((await f.call("GET", route)).body.reason, "request_scope_unknown")
  rows.set(parent, { id: parent })
  assert.equal((await f.call("GET", route)).body.reason, "request_unrelated")
  assert.equal(messageReads, 1, "unproven ancestry reached exact native message read")
  rows.get(parent).parentID = rootID
  childRow.parts[0].sessionID = rootID
  assert.equal((await f.call("GET", route)).body.reason, "native_shape")
  f.state.row.time.created++
  assert.equal((await f.call("GET", route)).body.reason, "native_binding_mismatch")
})


test("prompt admission requires one exact genuine text part on both native routes", { concurrency: true }, async t => {
  const cases = [
    ["ignored-only text", false, row => { row.parts[0].ignored = true }],
    ["synthetic-only text", false, row => { row.parts[0].synthetic = true }],
    ["genuine attachment", false, row => { row.parts.push({ ...row.parts[0], id: "prt_file", type: "file", text: undefined, url: "file:///attachment" }) }],
    ["multiple genuine texts", false, row => { const part = row.parts[0]; row.parts = [{ ...part, text: part.text.slice(0, 1) }, { ...part, id: "prt_second", text: part.text.slice(1) }] }],
    ["duplicate synthetic identity", false, row => { row.parts.push({ ...row.parts[0], synthetic: true }) }],
    ["foreign synthetic identity", false, row => { row.parts.push({ ...row.parts[0], id: "prt_foreign", sessionID: "ses_" + "d".repeat(32), synthetic: true }) }],
    ["malformed ignored marker", false, row => { row.parts[0].ignored = "false" }],
    ["plain text", true, () => {}],
    ["injected synthetic and ignored context", true, row => { row.parts.push({ ...row.parts[0], id: "prt_synthetic", synthetic: true, text: "native context" }, { ...row.parts[0], id: "prt_ignored", ignored: true, text: "ignored context" }) }],
    ["null flags and unknown metadata", true, row => { Object.assign(row.parts[0], { synthetic: null, ignored: null, metadata: null, nativeUnknown: { value: true } }) }],
  ]
  await Promise.all(["prompt", "initial"].flatMap(route => cases.map(([name, admitted, mutate]) => t.test(`${route}: ${name}`, { timeout: 10000 }, async t => {
    const f = await fixture(t, ({ binding, state }) => { if (route === "initial") { binding.initialPrompt = true; state.messages = [] } })
    if (route === "prompt") {
      const submit = f.api.client.session.promptAsync
      f.api.client.session.promptAsync = async (...args) => { const reply = await submit(...args); mutate(f.state.messages.at(-1)); return reply }
    } else {
      const submit = f.state.prompt.submit
      f.state.prompt.submit = function() { submit.call(this); queueMicrotask(() => mutate(f.state.messages.at(-1))) }
    }
    const body = route === "initial" ? initialBody() : undefined
    const receipt = (route === "initial" ? await f.call("POST", "/initial", body) : await f.dispatch()).body
    assert.equal(receipt.status, admitted ? "admitted" : "uncertain", JSON.stringify(receipt))
    const query = route === "initial" ? "/initial" : "/receipt?operationID=" + receipt.operationID
    const refreshed = (await f.call("GET", query)).body
    if (admitted) assert.equal(refreshed.status, "admitted")
    else assert.notEqual(refreshed.status, "admitted")
    assert.equal(f.state.calls.filter(call => call.name === (route === "initial" ? "nativeSubmit" : "prompt")).length, 1)
  }))))
})
