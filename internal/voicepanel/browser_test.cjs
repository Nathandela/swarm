"use strict";
const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");
const path = require("node:path");
const source = (name) => fs.readFileSync(path.join(__dirname, "web", name), "utf8");

function audioFixture() {
  let Capture, delivered;
  const context = vm.createContext({
    sampleRate: 48000,
    AudioWorkletProcessor: class {
      port = {postMessage: (pcm) => { assert.equal(delivered, undefined); delivered = pcm; }};
    },
    registerProcessor: (_name, implementation) => { Capture = implementation; },
  });
  vm.runInContext(source("capture.js"), context);
  const processor = new Capture();
  processor.process([[[1], [-1]]]);
  processor.process([[[1, 1, 1], [1, -1, -1]]]);
  processor.port.onmessage();
  processor.port.onmessage();
  assert.deepEqual(Array.from(new Uint8Array(delivered)), [0, 64, 0, 0]);
  assert.equal(processor.process([]), false);
}

async function browserFixture() {
  const storage = new Map();
  const writes = [], audioCalls = [];
  let trackStopped = false, nextID = 0;
  const settle = () => new Promise((resolve) => setImmediate(resolve));
  const secret = "x".repeat(43);
  let fail = true, accepted = false, address, emptyCapture = false;
  function open(fragment) {
    class Element {
      value = "";
      events = {};
      children = [];
      addEventListener(name, handler) { this.events[name] = handler; }
      setAttribute() {}
      replaceChildren(...children) { this.children = children; }
    }
    const elements = new Map();
    const element = (id) => {
      if (!elements.has(id)) elements.set(id, new Element());
      return elements.get(id);
    };
    class AudioIO {
      audioWorklet = {addModule: async () => {}};
      async resume() {}
      async close() { this.state = "closed"; }
      createMediaStreamSource() { return {connect: (node) => node, disconnect() {}}; }
    }
    class Worklet {
      port = {postMessage: () => this.port.onmessage({data: new ArrayBuffer(emptyCapture ? 0 : 2)})};
      connect() {}
      disconnect() {}
    }
    const track = {stop: () => { trackStopped = true; }, addEventListener() {}};
    const stream = {getAudioTracks: () => [track], getTracks: () => [track]};
    const ctx = vm.createContext({
      document: {getElementById: element, createElement: () => new Element()},
      location: {hash: fragment, pathname: "/"},
      history: {replaceState: (_, __, path) => { address = path; }},
      sessionStorage: {getItem: (key) => storage.get(key),setItem: (key,value) => storage.set(key,value),removeItem: (key) => storage.delete(key)},
      navigator: {mediaDevices: {getUserMedia: async () => stream}},
      crypto: {randomUUID: () => "00000000-1234-4567-89ab-00000000000" + (++nextID)},
      AudioContext: AudioIO, AudioWorkletNode: Worklet, AbortSignal,
      addEventListener() {}, setTimeout() { return 1; }, clearTimeout() {},
      fetch: async (url, options) => {
        assert.equal(options.headers.Authorization, "Bearer " + secret);
        if (url === "/api/status") return {ok: true, json: async () => ({ready:true})};
        if (url === "/api/text") {
          writes.push(options.body);
          if (fail) throw new Error("lost reply");
          accepted = true;
          return {ok:true,json:async () => ({sequence:1})};
        }
        if (url === "/api/ptt/begin") {
          audioCalls.push(["begin", JSON.parse(options.body).id]);
          return {ok: true, json: async () => ({phase:"capturing"})};
        }
        if (url === "/api/ptt/finalize") {
          assert.equal(trackStopped, true, "stop recording before uploading");
          assert.equal(options.body.byteLength, 2);
          audioCalls.push(["finalize", options.headers["Swarm-Utterance-ID"]]);
          throw new Error("lost recording reply");
        }
        if (url === "/api/receipt") {
          return accepted ? {ok:true,json:async () => ({sequence:1,phase:"queued"})} : {ok:false,status:404};
        }
        if (url === "/api/publications") return {ok:true,json:async () => [{text:"<script>literal</script>"}]};
        assert.fail(url);
      },
    });
    vm.runInContext(source("panel.js"), ctx);
    return element;
  }
  let ui = open("#" + secret);
  await settle();
  assert.equal(address, "/");
  ui("request").value = "A neutral message";
  ui("text-form").events.submit({preventDefault() {}});
  ui("text-form").events.submit({preventDefault() {}});
  await settle();
  assert.equal(writes.length, 1, "rapid duplicate click must be suppressed");
  ui = open("");
  await settle();
  assert.equal(ui("retry").hidden, false, "refresh offers explicit same-ID retry");
  fail = false;
  ui("retry").events.click();
  await settle();
  assert.equal(writes.length, 2);
  assert.equal(writes[0], writes[1]);
  assert.equal(ui("publications").children[0].textContent, "<script>literal</script>");
  await ui("mic").events.click();
  assert.equal(audioCalls.length, 1, "start only reserves an ID");
  await ui("mic").events.click();
  await settle();
  assert.deepEqual(audioCalls.map(([stage]) => stage), ["begin", "finalize"]);
  assert.equal(audioCalls[0][1], audioCalls[1][1], "end must finalize the reserved ID");
  ui = open("");
  await settle();
  assert.equal(audioCalls.length, 2, "refresh must not replay lost audio");
  assert.equal(ui("dismiss").disabled, false);
  ui("dismiss").events.click();
  await settle();
  assert.equal(audioCalls.length, 2, "dismiss must not resubmit audio");
  assert.equal(writes.length, 2, "dismiss must not resubmit text");
  assert.equal(ui("history").children.length, 1, "dismiss removes only this tab's selected record");

  emptyCapture = true;
  for (let i = 0; i < 18; i++) {
    await ui("mic").events.click();
    await ui("mic").events.click();
    await settle();
  }
  assert.equal(audioCalls.filter(([stage]) => stage === "finalize").length, 1, "unsent captures never finalize");
  assert.equal(audioCalls.filter(([stage]) => stage === "begin").length, 19, "unsent captures do not exhaust the local tab");
  assert.equal(ui("history").children.length, 1, "unsent captures do not retain a local record");

  const full = Array.from({length: 16}, (_, i) => ({id: "pending-" + i, kind: "text", text: "Pending"}));
  storage.set("swarm-voice-requests", JSON.stringify(full));
  ui = open("");
  await settle();
  ui("dismiss").events.click();
  await settle();
  assert.equal(writes.length, 2, "dismissal of a full pending list is local only");
  ui("request").value = "New after a dismissal";
  ui("text-form").events.submit({preventDefault() {}});
  await settle();
  assert.equal(writes.length, 3, "dismissal frees a full pending list slot");
}

audioFixture();
browserFixture().catch((error) => { console.error(error); process.exitCode = 1; });
