"use strict";
const element = (id) => document.getElementById(id);
const form = element("text-form"), message = element("request"), notice = element("notice");
const mic = element("mic"), receipt = element("receipt"), historySelect = element("history");
const updates = element("publications"), retry = element("retry"), dismiss = element("dismiss"), connection = element("connection");
const tokenKey = "swarm-voice-token", recordKey = "swarm-voice-requests";
let secret = "", records = [], selected = "", seen = "", busy = false, available = false, capture, opening;
try {
  const fragment = location.hash.slice(1);
  history.replaceState(null, "", location.pathname);
  if (/^[A-Za-z0-9_-]{43}$/.test(fragment) && sessionStorage.getItem(tokenKey) !== fragment) {
    sessionStorage.setItem(tokenKey, fragment);
    sessionStorage.removeItem(recordKey);
  }
  secret = sessionStorage.getItem(tokenKey) || "";
  const stored = JSON.parse(sessionStorage.getItem(recordKey) || "[]");
  records = stored.filter((item) => item.kind !== "audio" || item.audioSent !== false);
  if (records.length !== stored.length) sessionStorage.setItem(recordKey, JSON.stringify(records));
} catch {
  notice.textContent = "Browser storage is required to preserve request IDs.";
  secret = "";
}

async function api(route, body, pcmID, previewSignal) {
  const headers = {Authorization: "Bearer " + secret};
  const options = {headers, cache: "no-store", redirect: "error", signal: previewSignal || AbortSignal.timeout(42000)};
  if (body !== undefined) {
    options.method = "POST";
    headers["Content-Type"] = pcmID ? "application/octet-stream" : "application/json";
    if (pcmID) headers["Swarm-Utterance-ID"] = pcmID;
    options.body = pcmID ? body : JSON.stringify(body);
  }
  const response = await fetch("/api/" + route, options);
  if (!response.ok) throw Object.assign(new Error("Control request failed"), {status: response.status});
  return previewSignal ? response.arrayBuffer() : response.json();
}

function controls() {
  element("send").disabled = !available || busy || !!capture;
  mic.disabled = busy || !navigator.mediaDevices || (!available && !capture);
  mic.textContent = capture?.pcm ? "Send recording" : capture ? "Stop and send recording" : "Start recording";
  mic.setAttribute("aria-pressed", String(!!capture));
  historySelect.disabled = records.length === 0;
  dismiss.disabled = !selected || busy || capture?.id === selected;
}
function select(id) {
  stopPreview();
  selected = id;
  historySelect.value = id;
  seen = "";
  updates.replaceChildren();
  receipt.textContent = id ? "Checking receipt…" : "";
  retry.hidden = true;
}
function options() {
  historySelect.replaceChildren(...records.map((item, index) => {
    const option = document.createElement("option");
    option.value = item.id;
    option.textContent = `${records.length - index}. ${item.kind === "text" ? item.text.slice(0, 44) : "Recording"}`;
    return option;
  }));
  if (!records.some((record) => record.id === selected)) select(records[0]?.id || "");
  else historySelect.value = selected;
  controls();
}
function forget(id) {
  if (!records.some((item) => item.id === id)) return;
  const next = records.filter((item) => item.id !== id);
  sessionStorage.setItem(recordKey, JSON.stringify(next));
  records = next;
  options();
}
function discardUnsent(id) {
  const item = records.find((record) => record.id === id);
  if (item?.kind === "audio" && item.audioSent === false) {
    try { forget(id); } catch { /* Keep the ID if local storage cannot be updated. */ }
  }
}
function record(kind, text = "") {
  const item = {id: crypto.randomUUID(), kind, text};
  if (kind === "audio") item.audioSent = false;
  // Persist before sending so refresh or a lost reply cannot create a second ID.
  const next = [item, ...records];
  if (next.length > 16) {
    const oldestSettled = next.findLastIndex((old) => old.settled);
    if (oldestSettled < 0) throw new Error("Dismiss a request from this tab before adding another.");
    next.splice(oldestSettled, 1);
  }
  sessionStorage.setItem(recordKey, JSON.stringify(next));
  records = next;
  options();
  select(item.id);
  return item;
}
async function refreshReceipt() {
  const id = selected;
  if (!id) return;
  const item = records.find((record) => record.id === id);
  try {
    const result = await api("receipt", {id});
    if (selected !== id) return;
    const phase = result.phase || "queued";
    if (!item.settled && (phase === "completed" || phase === "rejected") && !result.native_binding_retired && !result.recovery_only) {
      item.settled = true;
      sessionStorage.setItem(recordKey, JSON.stringify(records));
    }
    const description = result.native_binding_retired || result.recovery_only || phase.includes("uncertain")
      ? "Outcome uncertain. Check the native manager before repeating the work."
      : `Request #${result.sequence} · ${phase.replaceAll("_", " ")}`;
    if (receipt.textContent !== description) receipt.textContent = description;
    retry.hidden = true;
    const publications = await api("publications", {id});
    const canRead = previewAvailable && phase === "completed" && !result.native_binding_retired && !result.recovery_only;
    const rendered = JSON.stringify([publications, canRead]);
    if (selected !== id || rendered === seen) return;
    if (!canRead) stopPreview();
    seen = rendered;
    updates.replaceChildren(...(publications || []).map((entry) => {
      const row = document.createElement("li");
      row.textContent = entry.text;
      if (canRead && entry.id) {
        const button = document.createElement("button");
        button.type = "button";
        button.textContent = "Read aloud";
        button.addEventListener("click", () => void startPreview(id, entry.id));
        row.append(button);
      }
      return row;
    }));
  } catch (error) {
    if (selected !== id) return;
    retry.hidden = item.kind !== "text" || error.status !== 404;
    const description = error.status === 404
      ? (item.kind === "text" ? "No receipt yet. Retry the same message to preserve its ID." : "Awaiting receipt. Audio is never sent again automatically.")
      : "Receipt unavailable. The outcome may be uncertain.";
    if (receipt.textContent !== description) receipt.textContent = description;
  }
}
let previewAvailable = true;
async function poll() {
  if (!secret) {
    connection.textContent = "Open the private URL from Swarm to connect.";
    return;
  }
  try {
    const status = await api("status");
    const binding = status.binding || {};
    previewAvailable = !status.preview_unavailable;
    const nextBinding = JSON.stringify(binding);
    if (nextBinding !== playbackBinding || status.custody_lost) stopPreview();
    playbackBinding = nextBinding;
    available = !status.custody_lost;
    const live = status.custody_lost ? "Manager custody lost" : status.scheduler_paused ? "Scheduler paused" : status.ready ? "Ready" : "Connected · Manager busy";
    const identity = [binding.provider, binding.session_id].filter(Boolean).join(" · ");
    const summary = [live, identity, status.attention].filter(Boolean).join(" · ");
    if (connection.textContent !== summary) connection.textContent = summary;
    await refreshReceipt();
  } catch {
    available = false;
    stopPreview();
    connection.textContent = "Manager connection unavailable";
  } finally {
    controls();
    setTimeout(poll, 1250);
  }
}
async function submit(item) {
  busy = true;
  controls();
  try {
    await api("text", {id: item.id, text: item.text});
    notice.textContent = "Message accepted by the manager controller.";
  } catch {
    notice.textContent = "Message delivery is uncertain. Check its receipt.";
  } finally {
    busy = false;
    controls();
    await refreshReceipt();
  }
}
form.addEventListener("submit", (event) => {
  event.preventDefault();
  if (busy || capture || !available || !message.value.trim()) return;
  try {
    const item = record("text", message.value);
    message.value = "";
    void submit(item);
  } catch (error) {
    notice.textContent = error.message;
  }
});
retry.addEventListener("click", () => {
  const item = records.find((record) => record.id === selected);
  if (item?.kind === "text" && !busy && !capture && available) void submit(item);
});
dismiss.addEventListener("click", () => {
  if (!selected || busy || capture?.id === selected) return;
  try {
    forget(selected);
    notice.textContent = "Removed from this tab. The durable request was unchanged.";
    void refreshReceipt();
  } catch {
    notice.textContent = "Browser storage could not dismiss this request.";
  }
});
historySelect.addEventListener("change", () => {
  select(historySelect.value);
  void refreshReceipt();
});

function release(device) {
  device.stream?.getTracks().forEach((track) => track.stop());
  device.input?.disconnect();
  device.node?.disconnect();
  if (device.context && device.context.state !== "closed") void device.context.close().catch(() => {});
  device.stream = device.input = device.node = device.context = undefined;
}
async function finish(device, pcm) {
  if (capture !== device) return;
  capture = undefined;
  clearTimeout(device.stopTimer);
  release(device);
  busy = true;
  controls();
  try {
    if (!pcm.byteLength) {
      discardUnsent(device.id);
      notice.textContent = "No microphone audio was captured.";
      return;
    }
    const item = records.find((record) => record.id === device.id);
    if (!item) throw new Error("Recording identity unavailable");
    const next = records.map((record) => record === item ? {...record, audioSent: true} : record);
    sessionStorage.setItem(recordKey, JSON.stringify(next));
    records = next;
    notice.textContent = "Finalizing the recording…";
    await api("ptt/finalize", pcm, device.id);
    notice.textContent = "Recording accepted by the manager controller.";
  } catch {
    if (records.find((item) => item.id === device.id)?.audioSent === false) {
      discardUnsent(device.id);
      notice.textContent = "Recording was not sent. Browser storage could not save its request identity.";
    } else {
      notice.textContent = "Recording outcome uncertain. Check its receipt.";
    }
  } finally {
    busy = false;
    controls();
    await refreshReceipt();
  }
}
mic.addEventListener("click", async () => {
  if (busy) return;
  if (capture) {
    busy = true;
    controls();
    const device = capture;
    if (device.pcm) {
      void finish(device, device.pcm);
      return;
    }
    device.stopTimer = setTimeout(() => {
      if (capture !== device) return;
      capture = undefined;
      release(device);
      busy = false;
      discardUnsent(device.id);
      notice.textContent = "Recording stopped without an audio result.";
      controls();
    }, 2000);
    device.node.port.postMessage("stop");
    return;
  }
  if (!available) return;
  busy = true;
  controls();
  const device = {};
  opening = device;
  try {
    device.context = new AudioContext();
    await device.context.resume();
    if (opening !== device) return;
    device.stream = await navigator.mediaDevices.getUserMedia({audio: true});
    if (opening !== device) return;
    await device.context.audioWorklet.addModule("/capture.js");
    if (opening !== device) return;
    device.node = new AudioWorkletNode(device.context, "swarm-capture");
    const item = record("audio");
    device.id = item.id;
    await api("ptt/begin", {id: item.id});
    if (opening !== device) return;
    device.input = device.context.createMediaStreamSource(device.stream);
    device.node.port.onmessage = (event) => {
      if (capture !== device) return;
      if (device.stopTimer) {
        void finish(device, event.data);
      } else {
        device.pcm = event.data;
        release(device);
        notice.textContent = "Recording limit reached. Select Send recording to submit.";
        controls();
      }
    };
    device.stream.getAudioTracks().forEach((track) => track.addEventListener("ended", () => {
      if (capture !== device || busy || device.pcm) return;
      capture = undefined;
      release(device);
      discardUnsent(device.id);
      notice.textContent = "Microphone disconnected. The recording was not sent.";
      controls();
    }));
    capture = device;
    device.input.connect(device.node).connect(device.context.destination);
    notice.textContent = "Recording. Select Stop to send.";
  } catch {
    if (capture === device) capture = undefined;
    discardUnsent(device.id);
    if (opening === device) notice.textContent = "Microphone recording could not start. Check permission and connection.";
  } finally {
    if (capture !== device) release(device);
    if (opening === device) {
      opening = undefined;
      busy = false;
      controls();
    }
  }
});
addEventListener("pagehide", () => {
  stopPreview();
  const device = capture || opening;
  if (!device) return;
  capture = opening = undefined;
  busy = false;
  clearTimeout(device.stopTimer);
  release(device);
  discardUnsent(device.id);
  notice.textContent = "Recording discarded when the page closed.";
  controls();
});
let preview, playbackBinding = "";
const previewControl = element("preview"), previewStatus = element("preview-status"), progress = element("audio-progress");

function stopPreview(message = "") {
  const reading = preview;
  preview = undefined;
  previewControl.hidden = true;
  if (!reading) {
    if (message) previewStatus.textContent = message;
    return;
  }
  reading.abort.abort();
  reading.source?.stop();
  void reading.context?.close().catch(() => {});
  previewStatus.textContent = message || "Reading stopped.";
}
element("stop-preview").addEventListener("click", () => stopPreview());
async function startPreview(id, publication) {
  if (!available || selected !== id || !playbackBinding) return;
  stopPreview();
  const reading = {abort: new AbortController()};
  preview = reading;
  previewControl.hidden = false;
  progress.value = 0;
  previewStatus.textContent = "Preparing an audio preview…";
  try {
    const ac = new AudioContext();
    reading.context = ac;
    await ac.resume();
    if (preview !== reading) return;
    const pcm = await api("read-aloud", {id, publication_id: publication}, undefined, reading.abort.signal);
    if (preview !== reading) return;
    if (!pcm.byteLength || pcm.byteLength > 24_000 * 2 * 6 || pcm.byteLength % 2) throw new Error("Invalid audio preview");
    const input = new DataView(pcm);
    const audio = ac.createBuffer(1, pcm.byteLength / 2, 24000);
    const channel = audio.getChannelData(0);
    for (let i = 0; i < channel.length; i++) channel[i] = input.getInt16(i * 2, true) / 32768;
    const source = ac.createBufferSource();
    source.buffer = audio;
    source.connect(ac.destination);
    source.onended = () => {
      if (preview === reading) stopPreview("Audio preview ended. Read the text for the exact publication.");
    };
    const started = ac.currentTime;
    const draw = () => {
      if (preview !== reading) return;
      progress.value = Math.min(1, (ac.currentTime - started) / audio.duration);
      requestAnimationFrame(draw);
    };
    source.start();
    reading.source = source;
    previewStatus.textContent = "Playing an audio preview.";
    requestAnimationFrame(draw);
  } catch {
    if (preview === reading) stopPreview("Audio preview unavailable. The publication text is still available.");
  }
}
options();
void poll();
