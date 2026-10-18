#!/usr/bin/env node
/**
 * swarm-opencode-guard bridge tests.
 *
 * Drives the REAL exported plugin from ./tui.mjs through a REAL private Unix
 * socket HTTP listener, using only Node standard modules and a fake TUI api
 * object. The listener, auth, guards, payload construction and cleanup under
 * test are the production code paths -- nothing in the bridge itself is faked.
 *
 * Pin: upstream OpenCode e3c4eb242585dcd77be1a867391b7c25fd47ce52 (1.19.3).
 * Vertical: GET /ready + POST /dispatch { kind: "prompt" } only. Permission
 * and question decision kinds are explicitly rejected until their reviewed
 * vertical lands.
 */

// A sibling orchestration layer may leak SWARM_* keys into this process. The
// bridge must read nothing from the environment, so strip every SWARM_ key
// before the plugin module is imported.
for (const key of Object.keys(process.env)) {
  if (key.startsWith("SWARM_")) delete process.env[key]
}

import { strict as assert } from "node:assert"
import fs from "node:fs"
import os from "node:os"
import path from "node:path"
import http from "node:http"
import net from "node:net"
import crypto from "node:crypto"

const PLUGIN_ID = "swarm-opencode-guard"

const FAKE = {
  rootID: "ses_root000000000000000000aa",
  childA: "ses_childA00000000000000000aa",
  unknown: "ses_nobody000000000000000000a",
  directory: "/proj/default",
  directoryB: "/proj/other",
  nativeVersion: "1.19.3",
  workerID: "worker-A",
  sessionInstance: "inst-A1",
  backendGeneration: "gen-7",
}

// The token must never appear in any HTTP response, ever.
const TOKEN = crypto.randomBytes(24).toString("hex")

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

function deepFreeze(value) {
  if (value && typeof value === "object" && !Object.isFrozen(value)) {
    Object.freeze(value)
    for (const key of Object.keys(value)) deepFreeze(value[key])
  }
  return value
}

function frozenClone(value) {
  return deepFreeze(structuredClone(value))
}

function tempFixtureDir() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "ocbg-"))
  fs.chmodSync(dir, 0o700)
  return fs.realpathSync(dir)
}

function mintBinding(dir, mut = {}) {
  const binding = {
    version: 1,
    workerID: FAKE.workerID,
    sessionInstance: FAKE.sessionInstance,
    rootID: FAKE.rootID,
    directory: FAKE.directory,
    backendGeneration: FAKE.backendGeneration,
    socketPath: path.join(dir, `${mut.socketName ?? "w"}.sock`),
    token: TOKEN,
  }
  Object.assign(binding, mut.overrides ?? {})
  return binding
}

function writeBinding(dir, binding, mut = {}) {
  const file = path.join(dir, mut.filename ?? "bridge.json")
  if (mut.raw !== undefined) {
    fs.writeFileSync(file, mut.raw, { mode: 0o600 })
  } else if (mut.mode === "symlink") {
    fs.symlinkSync(dir + "/nothing.json", file)
  } else {
    fs.writeFileSync(file, JSON.stringify(binding), { mode: mut.mode ?? 0o600 })
  }
  return file
}

// ---------------------------------------------------------------------------
// fake TUI api (mirrors upstream TuiPluginApi reads used by the plugin)
// ---------------------------------------------------------------------------

function makeFakeApi() {
  // The session route carries an unrelated native composer draft. The plugin
  // must never read it into decisions, and must never replace the composer.
  const route = frozenClone({
    name: "session",
    params: {
      sessionID: FAKE.rootID,
      prompt: { input: "native draft the user is typing", parts: [] },
    },
  })
  const sessions = new Map()
  sessions.set(
    FAKE.rootID,
    frozenClone({ id: FAKE.rootID, parentID: undefined, directory: FAKE.directory, title: "root" }),
  )
  sessions.set(
    FAKE.childA,
    frozenClone({ id: FAKE.childA, parentID: FAKE.rootID, directory: FAKE.directory, title: "childA" }),
  )

  const rec = {
    route,
    sessions,
    nativeVersion: FAKE.nativeVersion,
    stateReady: true,
    directory: FAKE.directory,
    dialogOpen: false,
    dialogDepth: 0,
    mode: "normal",
    statusDefault: { type: "idle" },
    status: {},
    behavior: { prompt: "resolve" },
    pending: {},
    abortListeners: [],
    calls: [],
    routeReads: 0,
    lifecycleController: new AbortController(),
    disposers: [],
    serial: 0,
  }

  function record(name, input, options) {
    rec.calls.push({
      name,
      at: rec.routeReads,
      input: structuredClone(input),
      optionKeys: Object.keys(options ?? {}).sort(),
      throwOnError: options?.throwOnError,
      signal: options?.signal,
    })
  }

  const stubPromptOK = { data: undefined, response: { status: 204 } }

  function behaviorSettle(kind, value) {
    const behavior = rec.behavior[kind]
    if (behavior === "resolve") return Promise.resolve(value)
    if (behavior === "rejectPlain") {
      return Promise.reject(new Error("synthetic failure without native status"))
    }
    if (behavior === "rejectStatus") {
      const err = new Error("native server refused")
      err.response = { status: 400 }
      return Promise.reject(err)
    }
    if (behavior === "pending") {
      return new Promise((resolve, reject) => {
        ;(rec.pending[kind] ??= []).push({
          release: () => resolve(value),
          fail: (err) => reject(err),
          id: `pending-${kind}-${++rec.serial}`,
        })
      })
    }
    throw new Error(`unsupported behavior ${behavior}`)
  }

  function abortablePending(kind, value) {
    return new Promise((resolve, reject) => {
      const entry = { release: () => resolve(value), fail: (err) => reject(err) }
      ;(rec.pending[kind] ??= []).push(entry)
      const onAbort = () => {
        const err = new Error("aborted by dispose")
        err.name = "AbortError"
        reject(err)
      }
      rec.abortListeners.push({ entry, onAbort })
    })
  }

  const api = {
    app: {
      get version() {
        rec.versionReads = (rec.versionReads ?? 0) + 1
        return rec.nativeVersion
      },
    },
    state: {
      get ready() {
        rec.stateReadyReads = (rec.stateReadyReads ?? 0) + 1
        return rec.stateReady
      },
      get path() {
        return {
          state: "/state",
          config: "/config",
          worktree: rec.directory,
          get directory() {
            rec.directoryReads = (rec.directoryReads ?? 0) + 1
            return rec.directory
          },
        }
      },
      session: {
        get(sessionID) {
          return rec.sessions.get(sessionID)
        },
        status(sessionID) {
          rec.statusReads = (rec.statusReads ?? 0) + 1
          return rec.status[sessionID] ?? rec.statusDefault
        },
        permission(sessionID) {
          return []
        },
        question(sessionID) {
          return []
        },
      },
    },
    route: {
      get current() {
        rec.routeReads += 1
        return rec.route
      },
    },
    ui: {
      dialog: {
        get open() {
          rec.dialogReads = (rec.dialogReads ?? 0) + 1
          return rec.dialogOpen
        },
        get depth() {
          return rec.dialogDepth
        },
      },
    },
    mode: {
      current() {
        rec.modeReads = (rec.modeReads ?? 0) + 1
        return rec.mode
      },
    },
    client: {
      session: {
        async promptAsync(input, options) {
          record("session.promptAsync", input, options)
          const value = structuredClone(stubPromptOK)
          return rec.behavior.prompt === "pendingAbort"
            ? abortablePending("prompt", value)
            : behaviorSettle("prompt", value)
        },
      },
    },
    lifecycle: {
      get signal() {
        return rec.lifecycleController.signal
      },
      onDispose(fn) {
        const item = { fn }
        rec.disposers.push(item)
        return () => {
          item.removed = true
        }
      },
    },
  }

  rec.pendingOf = (kind) => rec.pending[kind] ?? []
  rec.releasePending = (kind) => {
    const list = rec.pending[kind] ?? []
    rec.pending[kind] = []
    for (const entry of list) entry.release()
    for (const listener of rec.abortListeners.splice(0)) listener.entry.release()
  }

  rec.hasAbortListeners = () => rec.abortListeners.length > 0

  rec.dispose = async () => {
    // Mirrors upstream plugin scope.dispose(): abort first, then run cleanups in reverse.
    rec.lifecycleController.abort()
    for (const item of [...rec.disposers].reverse()) {
      if (item.removed) continue
      await item.fn()
      item.removed = true
    }
    // any stub promise still dancing on an abort signal releases now
    for (const listener of rec.abortListeners.splice(0)) listener.entry.release()
  }
  return { api, rec }
}

const fakeMeta = Object.freeze({
  id: PLUGIN_ID,
  source: "file",
  spec: "file:///fake/tui.mjs",
  target: "/fake/tui.mjs",
  first_time: 1,
  last_time: 1,
  time_changed: 1,
  load_count: 1,
  fingerprint: "fake",
  state: "first",
})

// ---------------------------------------------------------------------------
// client over the real UDS
// ---------------------------------------------------------------------------

function rawRequest(socketPath, method, urlPath, body, { token = TOKEN, authorization, timeoutMs = 3000 } = {}) {
  const headers = {}
  if (authorization === null) {
    // explicitly no auth header
  } else if (authorization !== undefined) {
    headers.authorization = authorization
  } else if (token !== null) {
    headers.authorization = `Bearer ${token}`
  }
  if (body !== undefined) {
    headers["content-type"] = "application/json"
    headers["content-length"] = Buffer.byteLength(body)
  }
  return http.request({ socketPath, method, path: urlPath, headers }, undefined)
}

function responseOf(req) {
  return new Promise((resolve) => {
    req.on("response", (res) => {
      const chunks = []
      res.on("data", (chunk) => chunks.push(chunk))
      res.on("end", () => finish(null))
      res.on("error", (err) => finish(err))
      function finish(err) {
        const buffer = Buffer.concat(chunks)
        const text = buffer.toString("utf8")
        let json
        try {
          json = text.length ? JSON.parse(text) : undefined
        } catch {
          json = undefined
        }
        resolve({ status: res.statusCode, headers: res.headers, text, json, transportError: err })
      }
    })
    req.on("error", (err) => resolve({ status: 0, transportError: err }))
    req.on("timeout", () => {
      req.destroy(new Error("client timeout"))
    })
  })
}

function inspect(res) {
  if (res.text && res.text.includes(TOKEN)) {
    throw new Error(`response leaked the bridge token: ${res.text}`)
  }
  return res
}

function call(fixture, method, urlPath, bodyObj, opts = {}) {
  let body
  if (bodyObj === undefined) body = undefined
  else if (typeof bodyObj === "string") body = bodyObj
  else body = JSON.stringify(bodyObj)
  const req = rawRequest(fixture.socketPath, method, urlPath, body, opts)
  const pending = responseOf(req)
  req.end(body)
  return pending.then(inspect)
}

// openRequest keeps the handle so tests can flip native state while the body
// is streaming, or drop the connection after the dispatch has been taken.
function openRequest(fixture, method, urlPath, bodyObj, opts = {}) {
  let body
  if (typeof bodyObj === "string") body = bodyObj
  else if (bodyObj !== undefined) body = JSON.stringify(bodyObj)
  const req = rawRequest(fixture.socketPath, method, urlPath, body, opts)
  return { req, done: responseOf(req).then(inspect) }
}

async function waitFor(predicate, ms = 2500, label = "condition") {
  const start = Date.now()
  for (;;) {
    if (predicate()) return
    if (Date.now() - start > ms) {
      throw new Error(`waitFor timeout: ${label}`)
    }
    await sleep(5)
  }
}

function envelope(mut = {}) {
  return {
    version: 1,
    operationID: mut.operationID ?? `op-${++envelopeSerial}`,
    sessionInstance: FAKE.sessionInstance,
    backendGeneration: FAKE.backendGeneration,
    rootID: FAKE.rootID,
    kind: mut.kind ?? "prompt",
    targetID: mut.targetID ?? FAKE.rootID,
    messageID: mut.messageID ?? `msg-${++envelopeSerial}`,
    text: mut.text ?? "hello bridge",
    ...mut.extra,
  }
}

let envelopeSerial = 0

// ---------------------------------------------------------------------------
// fixtures
// ---------------------------------------------------------------------------

async function startFixture(mut = {}) {
  const dir = tempFixtureDir()
  const binding = mintBinding(dir, mut)
  const bindingPath = writeBinding(dir, binding, mut.bindingFileMut)
  const { api, rec } = makeFakeApi()
  const fx = { dir, binding, bindingPath, api, rec, socketPath: binding.socketPath, token: TOKEN }

  if (mut.expectStartFailure) {
    let err
    try {
      await plugin.tui(api, { bindingFile: bindingPath }, mut.meta ?? fakeMeta)
    } catch (caught) {
      err = caught
    }
    fx.startError = err
    fx.startErrorText = err ? `${err.name}: ${err.message}` : ""
  } else {
    await plugin.tui(api, { bindingFile: bindingPath }, mut.meta ?? fakeMeta)
    await fixtureWaitReady(fx)
  }

  fx.expectReady = () => {
    assert.equal(fx.startError, undefined, `expected started plugin, got error: ${fx.startErrorText}`)
  }

  fx.destroy = async () => {
    await rec.dispose()
    if (fx.replacementServer) {
      await new Promise((resolve) => fx.replacementServer.close(() => resolve()))
      fx.replacementServer = undefined
    }
    try {
      fs.rmSync(dir, { recursive: true, force: true })
    } catch {}
  }
  return fx
}

async function fixtureWaitReady(fx) {
  const deadline = Date.now() + 2500
  for (;;) {
    let res
    try {
      res = await call(fx, "GET", "/ready")
    } catch (err) {
      if (Date.now() > deadline) throw err
      await sleep(10)
      continue
    }
    if (res.status === 200) return res
    if (Date.now() > deadline) throw new Error(`listener never answered /ready: got ${res.status}`)
    await sleep(10)
  }
}

// ---------------------------------------------------------------------------
// tests
// ---------------------------------------------------------------------------

const tests = []
function test(name, fn) {
  tests.push({ name, fn })
}

// -- plugin shape, source and environment hygiene ----------------------------

test("export shape: default object with exactly id+tui, callable tui", async () => {
  assert.equal(typeof plugin, "object")
  assert.deepEqual(Object.keys(plugin).sort(), ["id", "tui"])
  assert.equal(plugin.id, PLUGIN_ID)
  assert.equal(typeof plugin.tui, "function")
})

const PLUGIN_SOURCE_FILE = path.join(import.meta.dirname, "tui.mjs")

test("source hygiene: no process.env, only node: imports, no npm or require", async () => {
  const source = fs.readFileSync(PLUGIN_SOURCE_FILE, "utf8")
  assert.ok(!source.includes("process.env"), "the plugin must read nothing from the environment")
  const specifiers = [...source.matchAll(/from\s+["']([^"']+)["']/g)].map((m) => m[1])
  assert.ok(specifiers.length > 0, "expected import statements")
  for (const spec of specifiers) {
    assert.ok(
      spec.startsWith("node:"),
      `import specifier must be a node builtin, got ${spec}`,
    )
  }
  assert.ok(!source.includes("npm:"), "no npm specs")
  assert.ok(!source.includes("require("), "no require reflexes")
})

// -- startup validation ------------------------------------------------------

async function expectStartFailure(mut, expects) {
  const fx = await startFixture({ ...mut, expectStartFailure: true })
  try {
    assert.ok(fx.startError, `expected start failure for ${JSON.stringify(expects ?? mut)}`)
    assert.ok(
      !/unimplemented|red phase/i.test(fx.startErrorText),
      "start failed for the wrong reason (stub reached)",
    )
    assert.ok(
      !String(fx.startErrorText).includes(TOKEN),
      "start error must not leak the bridge token",
    )
    assert.equal(fx.rec.calls.length, 0, "no SDK call on failed start")
    if (fs.existsSync(fx.socketPath)) {
      throw new Error("socket must not be bound after a failed start")
    }
    if (expects) await expects(fx)
    return fx
  } finally {
    try {
      await fs.promises.rm(fx.dir, { recursive: true, force: true })
    } catch {}
  }
}

test("start refuses a 0644 binding file", async () => {
  await expectStartFailure({ bindingFileMut: { mode: 0o644 } })
})

test("start refuses an oversized binding file", async () => {
  await expectStartFailure({ overrides: { token: "x".repeat(20_000) } })
})

test("start refuses malformed binding JSON", async () => {
  await expectStartFailure({ bindingFileMut: { raw: "{not json" } })
})

test("start refuses a symlinked binding file", async () => {
  await expectStartFailure({ bindingFileMut: { mode: "symlink" } })
})

test("start refuses missing binding fields and unknown binding fields", async () => {
  await expectStartFailure({
    overrides: { workerID: undefined },
  })
  await expectStartFailure({
    overrides: { extra: "field" },
  })
})

test("start refuses a binding file whose parent is not 0700", async () => {
  const dir = tempFixtureDir()
  fs.chmodSync(dir, 0o755)
  try {
    const binding = mintBinding(dir)
    const bindingPath = writeBinding(dir, binding)
    const { api, rec } = makeFakeApi()
    let startError
    try {
      await plugin.tui(api, { bindingFile: bindingPath }, fakeMeta)
    } catch (caught) {
      startError = caught
    }
    assert.ok(startError, "expected refusal on non-private parent")
    assert.ok(!/unimplemented|red phase/i.test(String(startError.message)), "refusal must be real validation")
    assert.ok(!String(startError.message).includes(TOKEN))
    assert.equal(rec.calls.length, 0)
  } finally {
    try {
      fs.rmSync(dir, { recursive: true, force: true })
    } catch {}
  }
})

test("start refuses an occupied socket path and never unlinks the pre-existing file", async () => {
  const dir = tempFixtureDir()
  const binding = mintBinding(dir)
  fs.writeFileSync(binding.socketPath, "collateral", { mode: 0o600 })
  const bindingPath = writeBinding(dir, binding)
  try {
    const { api, rec } = makeFakeApi()
    let startError
    try {
      await plugin.tui(api, { bindingFile: bindingPath }, fakeMeta)
    } catch (caught) {
      startError = caught
    }
    assert.ok(startError, "expected refusal on occupied socket path")
    assert.ok(!/unimplemented|red phase/i.test(String(startError.message)), "refusal must be real validation")
    assert.equal(rec.calls.length, 0)
    assert.equal(
      fs.readFileSync(binding.socketPath, "utf8"),
      "collateral",
      "existing file must survive untouched",
    )
  } finally {
    try {
      fs.rmSync(dir, { recursive: true, force: true })
    } catch {}
  }
})

test("start refuses socket paths beyond the sun_path bound", async () => {
  await expectStartFailure({ overrides: { socketPath: `/tmp/${"p".repeat(140)}.sock` } })
})

// -- /ready ------------------------------------------------------------------

test("ready reports the exact binding tuple and honest flags", async (fx) => {
  fx.expectReady()
  const res = await call(fx, "GET", "/ready")
  assert.equal(res.status, 200)
  const body = res.json
  assert.equal(body.version, 1)
  assert.equal(body.workerID, FAKE.workerID)
  assert.equal(body.sessionInstance, FAKE.sessionInstance)
  assert.equal(body.rootID, FAKE.rootID)
  assert.equal(body.directory, FAKE.directory)
  assert.equal(body.backendGeneration, FAKE.backendGeneration)
  assert.equal(body.nativeVersion, FAKE.nativeVersion)
  assert.equal(body.expectedNativeVersion, FAKE.nativeVersion)
  assert.equal(body.stateReady, true)
  assert.equal(body.inFlight, false)
  assert.equal(body.dialogOpen, false)
  assert.equal(body.draftGuardSupported, false)
  assert.equal(body.ready, true)
  assert.deepEqual(body.degraded, [])
  assert.equal(body.route.name, "session")
  assert.equal(body.route.sessionID, FAKE.rootID)
  assert.equal(fx.rec.calls.length, 0, "ready never calls the SDK")
})

test("ready names degraded reasons exactly", async () => {
  const cases = [
    { try: (fx) => { fx.rec.stateReady = false }, degraded: ["state_not_ready"] },
    { try: (fx) => { fx.rec.nativeVersion = "9.9.9" }, degraded: ["version_mismatch"] },
    { try: (fx) => { fx.rec.directory = FAKE.directoryB }, degraded: ["directory_mismatch"] },
    { try: (fx) => { fx.rec.route = frozenClone({ name: "home" }) }, degraded: ["route_mismatch"] },
    {
      try: (fx) => {
        fx.rec.route = frozenClone({ name: "session", params: { sessionID: FAKE.unknown } })
      },
      degraded: ["route_mismatch"],
    },
    { try: (fx) => { fx.rec.route = frozenClone({ name: "flyer", params: {} }) }, degraded: ["route_mismatch"] },
    { try: (fx) => { fx.rec.sessions.delete(FAKE.rootID) }, degraded: ["root_uncached"] },
    {
      try: (fx) => {
        fx.rec.sessions.set(
          FAKE.rootID,
          frozenClone({ id: FAKE.rootID, parentID: undefined, directory: FAKE.directoryB }),
        )
      },
      degraded: ["root_directory_mismatch"],
    },
  ]
  for (const c of cases) {
    const fx = await startFixture()
    try {
      fx.expectReady()
      c.try(fx)
      const body = (await call(fx, "GET", "/ready")).json
      assert.deepEqual(body?.degraded ?? ["no-body"], c.degraded, `degraded breakdown failed`)
      assert.equal(body.ready, c.degraded.length === 0)
    } finally {
      await fx.destroy()
    }
  }
})

test("ready flags inFlight while a prompt dispatch is pending", async (fx) => {
  fx.expectReady()
  fx.rec.behavior.prompt = "pending"
  const dispatched = openRequest(fx, "POST", "/dispatch", envelope())
  await waitFor(() => fx.rec.calls.length === 1, 2000, "first stub taken")
  const body = (await call(fx, "GET", "/ready")).json
  assert.equal(body.inFlight, true)
  fx.rec.releasePending("prompt")
  const out = await dispatched.done
  assert.equal(out.json.status, "admitted")
  const after = (await call(fx, "GET", "/ready")).json
  assert.equal(after.inFlight, false)
})

// -- transport: auth, methods, paths, body limits ----------------------------

test("rejects wrong, missing, malformed and non-bearer auth with zero sdk calls", async () => {
  const fx = await startFixture()
  try {
    const variants = [
      { authorization: "Bearer wrong-token" },
      { authorization: undefined, token: null },
      { authorization: "Basic abc" },
      { authorization: "Bearer" },
      { authorization: `bearer ${TOKEN}`, wrongCaseAuth: true },
    ]
    for (const opts of variants) {
      const res = await call(fx, "GET", "/ready", undefined, opts)
      assert.equal(res.status, 401, JSON.stringify(opts.authorization ?? opts.token))
      assert.equal(res.json?.reason, "unauthorized")
      const resPost = await call(fx, "POST", "/dispatch", envelope(), opts)
      assert.equal(resPost.status, 401)
      assert.equal(fx.rec.calls.length, 0, "no sdk call without auth")
    }
  } finally {
    await fx.destroy()
  }
})

test("rejects unknown methods and paths", async () => {
  const fx = await startFixture()
  try {
    const checks = [
      ["PUT", "/ready", undefined],
      ["POST", "/ready", envelope()],
      ["DELETE", "/ready", undefined],
      ["PATCH", "/dispatch", envelope()],
      ["GET", "/dispatch", undefined],
      ["GET", "/other", undefined],
      ["POST", "/other", envelope()],
      ["POST", "/dispatch/", envelope()],
      ["POST", "/DISPATCH", envelope()],
    ]
    for (const [method, urlPath, body] of checks) {
      const res = await call(fx, method, urlPath, body)
      assert.ok([404, 405].includes(res.status), `${method} ${urlPath} -> ${res.status}`)
      assert.equal(res.json?.status, "rejected")
      assert.equal(fx.rec.calls.length, 0, `no sdk call for ${method} ${urlPath}`)
    }
  } finally {
    await fx.destroy()
  }
})

test("enforces the body cap with a proven pre-dispatch refusal", async () => {
  const fx = await startFixture()
  try {
    const res = await call(fx, "POST", "/dispatch", "x".repeat(140_000))
    assert.equal(res.status, 413)
    assert.equal(res.json?.reason, "payload_too_large")
    assert.equal(fx.rec.calls.length, 0)
  } finally {
    await fx.destroy()
  }
})

test("rejects malformed, empty and non-object bodies with no sdk call", async () => {
  const fx = await startFixture()
  try {
    for (const raw of ["", " ", "not json", "[1,2,3]", '"text"', "null"]) {
      const res = await call(fx, "POST", "/dispatch", raw)
      assert.equal(res.status, 400, JSON.stringify(raw))
      assert.equal(res.json?.reason, "bad_json")
      assert.equal(fx.rec.calls.length, 0)
    }
  } finally {
    await fx.destroy()
  }
})

// -- envelope immutables and shape -------------------------------------------

test("rejects wrong immutable binding triple before any sdk call", async () => {
  const fx = await startFixture()
  try {
    const variants = [
      { extra: { sessionInstance: "inst-other" }, reason: "instance_mismatch" },
      { extra: { backendGeneration: "gen-other" }, reason: "generation_mismatch" },
      { extra: { rootID: FAKE.unknown }, reason: "root_mismatch" },
      { extra: { version: 2 }, reason: "version_unsupported" },
      { extra: { version: "1" }, reason: "version_unsupported" },
    ]
    for (const v of variants) {
      const res = await call(fx, "POST", "/dispatch", envelope(v))
      assert.equal(res.status, 422, JSON.stringify(v))
      assert.equal(res.json?.status, "rejected")
      assert.equal(res.json?.reason, v.reason, JSON.stringify(v))
      assert.equal(fx.rec.calls.length, 0, JSON.stringify(v))
    }
  } finally {
    await fx.destroy()
  }
})

test("permission, question and stop kinds are explicitly unsupported in this vertical", async () => {
  const fx = await startFixture()
  try {
    const variants = [
      { kind: "stop", extra: {} },
      { kind: "interrupt", extra: {} },
      { kind: "permission", extra: { requestID: "pcm_1", decision: "once", expectedRequest: { id: "pcm_1" } } },
      {
        kind: "question_reply",
        extra: { requestID: "qst_1", answers: [["prod"]], expectedRequest: { id: "qst_1" } },
      },
      { kind: "question_reject", extra: { requestID: "qst_1", expectedRequest: { id: "qst_1" } } },
      { kind: "wat", extra: {} },
      { kind: undefined, extra: {} },
    ]
    for (const v of variants) {
      const res = await call(fx, "POST", "/dispatch", envelope(v))
      assert.equal(res.status, 422, JSON.stringify(v.kind))
      assert.equal(res.json?.reason, "unsupported_kind", JSON.stringify(v.kind))
      assert.equal(fx.rec.calls.length, 0, JSON.stringify(v.kind))
    }
  } finally {
    await fx.destroy()
  }
})

test("prompt target must be the immutable worker root", async () => {
  const fx = await startFixture()
  try {
    for (const targetID of [FAKE.childA, FAKE.unknown, ""]) {
      const res = await call(fx, "POST", "/dispatch", envelope({ targetID }))
      assert.equal(res.status, 422)
      assert.equal(res.json.reason, "not_root_target")
      assert.equal(fx.rec.calls.length, 0)
    }
  } finally {
    await fx.destroy()
  }
})

test("unexpected envelope fields (tools/system passthrough attempts) are refused", async () => {
  const fx = await startFixture()
  try {
    const env = envelope()
    env.tools = [{ name: "bash", pattern: "*" }]
    const res = await call(fx, "POST", "/dispatch", env)
    assert.equal(res.status, 422)
    assert.equal(res.json.reason, "unexpected_field")
    assert.equal(fx.rec.calls.length, 0)

    const env2 = envelope()
    env2.system = "relay my own system prompt"
    const res2 = await call(fx, "POST", "/dispatch", env2)
    assert.equal(res2.status, 422)
    assert.equal(res2.json.reason, "unexpected_field")
    assert.equal(fx.rec.calls.length, 0)
  } finally {
    await fx.destroy()
  }
})

test("prompt payload shape is strictly validated", async () => {
  const fx = await startFixture()
  try {
    const variants = [
      { messageID: undefined, label: "missing messageID" },
      { messageID: "", label: "empty messageID" },
      { messageID: "x".repeat(200), label: "oversize messageID" },
      { text: undefined, label: "missing text" },
      { text: "", label: "empty text" },
      { text: "y".repeat(20_000), label: "oversize text" },
      { messageID: "msg-1", text: "hi", extra: { model: "gpt-5" }, label: "model wrong shape" },
      {
        messageID: "msg-1",
        text: "hi",
        extra: { model: { providerID: 3, modelID: "m" } },
        label: "model id wrong type",
      },
      { messageID: "msg-1", text: "hi", extra: { agent: 7 }, label: "agent wrong type" },
      { messageID: "msg-1", text: "hi", extra: { variant: {} }, label: "variant wrong type" },
      {
        messageID: "msg-1",
        text: "hi",
        extra: { operationID: 42 },
        label: "operationID wrong type",
      },
    ]
    for (const v of variants) {
      const res = await call(fx, "POST", "/dispatch", envelope(v))
      assert.equal(res.status, 422, `${v.label} -> ${res.text}`)
      assert.equal(fx.rec.calls.length, 0, v.label)
    }
  } finally {
    await fx.destroy()
  }
})

// -- readiness gate on dispatch ----------------------------------------------

test("dispatch refuses every unready view with zero sdk calls", async () => {
  const cases = [
    { try: (fx) => { fx.rec.stateReady = false }, reason: "state_not_ready" },
    { try: (fx) => { fx.rec.nativeVersion = "9.9.9" }, reason: "version_mismatch" },
    { try: (fx) => { fx.rec.directory = FAKE.directoryB }, reason: "directory_mismatch" },
    { try: (fx) => { fx.rec.route = frozenClone({ name: "home" }) }, reason: "route_mismatch" },
    {
      try: (fx) => {
        fx.rec.route = frozenClone({ name: "session", params: { sessionID: FAKE.unknown } })
      },
      reason: "route_mismatch",
    },
    { try: (fx) => { fx.rec.route = frozenClone({ name: "flyer", params: {} }) }, reason: "route_mismatch" },
    { try: (fx) => { fx.rec.dialogOpen = true }, reason: "modal_open" },
    { try: (fx) => { fx.rec.status[FAKE.rootID] = { type: "busy" } }, reason: "status_busy" },
    { try: (fx) => { fx.rec.status[FAKE.rootID] = { type: "retry", next: 3 } }, reason: "status_retry" },
    { try: (fx) => { fx.rec.status[FAKE.rootID] = { type: "queued" } }, reason: "status_unsupported" },
    { try: (fx) => { fx.rec.status[FAKE.rootID] = undefined }, reason: "no_status_record" },
    { try: (fx) => { fx.rec.sessions.delete(FAKE.rootID) }, reason: "root_uncached" },
    {
      try: (fx) => {
        fx.rec.sessions.set(
          FAKE.rootID,
          frozenClone({ id: FAKE.rootID, parentID: undefined, directory: FAKE.directoryB }),
        )
      },
      reason: "root_directory_mismatch",
    },
    { try: (fx) => { fx.rec.mode = 42 }, reason: "unknown_readiness" },
  ]
  for (const c of cases) {
    const fx = await startFixture()
    try {
      c.try(fx)
      const res = await call(fx, "POST", "/dispatch", envelope())
      assert.equal(res.status, 422, `expected 422 for ${c.reason}, got ${res.status} ${res.text}`)
      assert.equal(res.json.reason, c.reason)
      assert.equal(fx.rec.calls.length, 0, `zero sdk calls for ${c.reason}`)
    } finally {
      await fx.destroy()
    }
  }
})

test("routes are re-read for each dispatch: change and restore between requests", async (fx) => {
  fx.expectReady()
  const one = await call(fx, "POST", "/dispatch", envelope())
  assert.equal(one.json.status, "admitted")
  fx.rec.route = frozenClone({ name: "session", params: { sessionID: FAKE.unknown } })
  const two = await call(fx, "POST", "/dispatch", envelope())
  assert.equal(two.status, 422)
  assert.equal(two.json.reason, "route_mismatch")
  assert.equal(fx.rec.calls.length, 1, "the refused dispatch made no sdk call")
  fx.rec.route = frozenClone({
    name: "session",
    params: { sessionID: FAKE.rootID, prompt: { input: "unrelated native draft", parts: [] } },
  })
  const three = await call(fx, "POST", "/dispatch", envelope())
  assert.equal(three.json.status, "admitted")
  assert.equal(fx.rec.calls.length, 2)
})

test("route is read at dispatch time, not when the body began arriving", async () => {
  const fx = await startFixture()
  try {
    const payload = JSON.stringify(envelope())
    const bytes = Buffer.from(payload)
    const req = rawRequest(fx.socketPath, "POST", "/dispatch", undefined)
    req.setHeader("content-length", bytes.length)
    const done = responseOf(req).then(inspect)
    req.write(bytes.subarray(0, bytes.length - 2))
    // the body is not finished; the displayed session moves before the final bytes
    fx.rec.route = frozenClone({ name: "session", params: { sessionID: FAKE.unknown } })
    await sleep(50)
    req.end(bytes.subarray(bytes.length - 2))
    const res = await done
    assert.equal(res.status, 422)
    assert.equal(res.json.reason, "route_mismatch")
    assert.equal(fx.rec.calls.length, 0, "no sdk call when the dispatch-time route has moved")
  } finally {
    await fx.destroy()
  }
})

// -- prompt happy path --------------------------------------------------------

test("prompt dispatch calls promptAsync with exactly the owed payload", async (fx) => {
  fx.expectReady()
  const res = await call(fx, "POST", "/dispatch", envelope())
  assert.equal(res.status, 200)
  assert.equal(res.json.status, "admitted")
  assert.equal(res.json.nativeStatus, 204)
  assert.ok(String(res.json.operationID).startsWith("op-"), "operationID echo")
  assert.equal(res.json.inFlight, false)
  assert.equal(fx.rec.calls.length, 1)
  const call1 = fx.rec.calls[0]
  assert.equal(call1.name, "session.promptAsync")
  assert.deepEqual(call1.input, {
    sessionID: FAKE.rootID,
    messageID: call1.input.messageID,
    parts: [{ type: "text", text: "hello bridge" }],
  })
  assert.deepEqual(call1.optionKeys, ["signal", "throwOnError"])
  assert.equal(call1.throwOnError, true)
  assert.ok(call1.signal instanceof AbortSignal)
  // the guard read the route exactly once, immediately before the invocation
  assert.equal(call1.at, fx.rec.routeReads, "no route read between the guard and the invocation")
  // the native draft in the route params is untouched
  assert.equal(fx.rec.route.params.prompt.input, "native draft the user is typing")
})

test("optional model, agent and variant are carried exactly when supplied", async () => {
  const fx = await startFixture()
  try {
    const res = await call(
      fx,
      "POST",
      "/dispatch",
      envelope({ extra: { model: { providerID: "p", modelID: "m" }, agent: "build", variant: "v1" } }),
    )
    assert.equal(res.json.status, "admitted")
    const call1 = fx.rec.calls[0]
    assert.deepEqual(call1.input, {
      sessionID: FAKE.rootID,
      messageID: call1.input.messageID,
      parts: [{ type: "text", text: "hello bridge" }],
      model: { providerID: "p", modelID: "m" },
      agent: "build",
      variant: "v1",
    })
  } finally {
    await fx.destroy()
  }
})

// -- one mutation in flight ---------------------------------------------------

test("a second mutation during flight receives a proven pre-dispatch busy refusal", async () => {
  const fx = await startFixture()
  try {
    fx.rec.behavior.prompt = "pending"
    const first = openRequest(fx, "POST", "/dispatch", envelope())
    await waitFor(() => fx.rec.calls.length === 1, 2000, "first stub taken")
    const second = await call(fx, "POST", "/dispatch", envelope())
    assert.equal(second.status, 409)
    assert.equal(second.json.reason, "busy")
    assert.equal(second.json.inFlight, true)
    assert.equal(fx.rec.calls.length, 1, "the busy mutation made no second sdk call")
    fx.rec.releasePending("prompt")
    const out = await first.done
    assert.equal(out.json.status, "admitted")
    assert.equal(fx.rec.calls.length, 1)
    // after the drain, a next mutation is allowed again
    const fourth = await call(fx, "POST", "/dispatch", envelope())
    assert.equal(fourth.json.status, "admitted")
    assert.equal(fx.rec.calls.length, 2)
  } finally {
    await fx.destroy()
  }
})

// -- invocation outcomes ------------------------------------------------------

test("a plain synthetic failure after invocation stays uncertain and never retries", async () => {
  const fx = await startFixture()
  try {
    fx.rec.behavior.prompt = "rejectPlain"
    const res = await call(fx, "POST", "/dispatch", envelope())
    assert.equal(res.status, 200)
    assert.equal(res.json.status, "uncertain")
    assert.equal(res.json.reason, "native_error")
    assert.equal(fx.rec.calls.length, 1, "never a retry")
    const again = await call(fx, "POST", "/dispatch", envelope())
    assert.equal(again.json.status, "admitted", "a new operation works again")
    assert.equal(fx.rec.calls.length, 2)
  } finally {
    await fx.destroy()
  }
})

test("a definite native rejection is distinguished from uncertainty", async () => {
  const fx = await startFixture()
  try {
    fx.rec.behavior.prompt = "rejectStatus"
    const res = await call(fx, "POST", "/dispatch", envelope())
    assert.equal(res.status, 200)
    assert.equal(res.json.status, "rejected")
    assert.equal(res.json.reason, "native_rejected")
    assert.equal(res.json.nativeStatus, 400)
    assert.equal(fx.rec.calls.length, 1)
  } finally {
    await fx.destroy()
  }
})

test("dropping the http response after invocation means uncertainty and never a retry", async () => {
  const fx = await startFixture()
  try {
    fx.rec.behavior.prompt = "pending"
    const first = openRequest(fx, "POST", "/dispatch", envelope())
    await waitFor(() => fx.rec.calls.length === 1, 2000, "stub taken")
    first.req.destroy()
    fx.rec.releasePending("prompt")
    const ready = (await call(fx, "GET", "/ready")).json
    assert.equal(ready.inFlight, false, "in-flight cleared after the lost connection settled")
    assert.equal(fx.rec.calls.length, 1, "connection loss must not trigger a second native write")
    const next = await call(fx, "POST", "/dispatch", envelope())
    assert.equal(next.json.status, "admitted")
    assert.equal(fx.rec.calls.length, 2)
  } finally {
    await fx.destroy()
  }
})

test("bounded dispatch deadline turns a never-resolving call into uncertainty", async () => {
  const fx = await startFixture()
  try {
    fx.rec.behavior.prompt = "pending"
    const res = await openRequest(fx, "POST", "/dispatch", envelope()).done
    assert.equal(res.json.status, "uncertain")
    assert.equal(res.json.reason, "aborted")
    assert.equal(fx.rec.calls.length, 1, "no retry after the deadline")
  } finally {
    await fx.destroy()
  }
})

// -- lifecycle ----------------------------------------------------------------

test("dispose removes exactly its own socket and the listener stops serving", async () => {
  const fx = await startFixture()
  try {
    await fx.rec.dispose()
    assert.equal(fs.existsSync(fx.socketPath), false, "own socket removed on dispose")
    const res = await call(fx, "GET", "/ready").catch((err) => ({ transportError: err }))
    assert.ok(res.transportError, "listener must be down after dispose")
  } finally {
    await fx.destroy()
  }
})

test("dispose is idempotent and never unlinks a replacement socket", async () => {
  const fx = await startFixture()
  try {
    await fx.rec.dispose()
    assert.equal(fs.existsSync(fx.socketPath), false)
    // someone else owns the path now: a fresh socket plus a sentinel file
    const sentinel = path.join(fx.dir, "sentinel.txt")
    fs.writeFileSync(sentinel, "replacement sentinel", { mode: 0o600 })
    await new Promise((resolve, reject) => {
      const srv = net.createServer(() => {})
      fx.replacementServer = srv
      srv.on("error", (err) => reject(err))
      srv.on("close", () => resolve())
      srv.listen({ path: fx.socketPath }, () => resolve())
    })
    // second and third dispose of the SAME plugin instance must not touch the replacement
    await fx.rec.dispose()
    await fx.rec.dispose()
    assert.ok(fs.existsSync(fx.socketPath), "replacement socket must survive")
    assert.equal(fs.readFileSync(sentinel, "utf8"), "replacement sentinel")
  } finally {
    await fx.destroy()
  }
})

test("dispose aborts an in-flight native call: it becomes uncertain, once", async () => {
  const fx = await startFixture()
  try {
    fx.rec.behavior.prompt = "pendingAbort"
    const first = openRequest(fx, "POST", "/dispatch", envelope())
    await waitFor(() => fx.rec.calls.length === 1, 2000, "stub taken")
    assert.ok(fx.rec.hasAbortListeners(), "fake tracks the dispatch signal")
    const disposePromise = fx.rec.dispose()
    const res = await first.done
    assert.equal(res.json?.status, "uncertain", `got ${res.text}`)
    assert.equal(res.json.reason, "aborted")
    assert.equal(fx.rec.calls.length, 1, "no retry on disposal")
    await disposePromise
    assert.equal(fs.existsSync(fx.socketPath), false)
    const out = await Promise.race([call(fx, "GET", "/ready").catch((err) => ({ transportError: err })), sleep(200).then(() => undefined)])
    assert.ok(!out || out.transportError, "server must be down after dispose")
  } finally {
    await fx.destroy()
  }
})

// -- reflection hygiene --------------------------------------------------------

test("no response ever leaks the token or the socket path", async () => {
  const fx = await startFixture()
  try {
    const res = await call(fx, "POST", "/dispatch", JSON.stringify({ nope: true }))
    assert.equal(res.status, 400)
    assert.ok(!res.text.includes(TOKEN), "no token leak")
    assert.ok(!res.text.includes(fx.socketPath), "no socket path echo")
    const ready = await call(fx, "GET", "/ready")
    assert.ok(!ready.text.includes(TOKEN), "no token leak in ready")
    assert.ok(!ready.text.includes(fx.socketPath), "no socket path echo in ready")
  } finally {
    await fx.destroy()
  }
})

// ---------------------------------------------------------------------------
// minimal deterministic runner (no node:test)
// ---------------------------------------------------------------------------

let plugin

async function main() {
  let pluginModule
  try {
    pluginModule = await import(PLUGIN_SOURCE_FILE)
  } catch (err) {
    console.log(`FAIL plugin module import :: ${err.message}`)
    console.log("1/1 TESTS FAILED")
    process.exitCode = 1
    return
  }
  plugin = pluginModule.default
  assert.equal(typeof plugin?.tui, "function", "plugin.tui must be callable")

  const results = []
  for (const entry of tests) {
    const takesFixture = entry.fn.length >= 1
    let fx
    try {
      if (takesFixture) fx = await startFixture()
      await entry.fn(fx)
    } catch (err) {
      const text = err && err.stack ? err.stack.split("\n", 3).join(" | ") : String(err)
      results.push({ name: entry.name, failed: true, text })
    } finally {
      if (fx) {
        try {
          await fx.destroy()
        } catch {}
      }
    }
  }
  for (const r of results) {
    console.log(`FAIL ${r.name} :: ${r.text}`)
  }
  const failedCount = results.length
  if (failedCount === 0) {
    console.log(`ALL ${tests.length} TESTS PASSED`)
  } else {
    console.log(`${failedCount}/${tests.length} TESTS FAILED`)
    process.exitCode = 1
  }
}

main().catch((err) => {
  console.error("HARNESS FAILURE", err)
  process.exitCode = 1
})
