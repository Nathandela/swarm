mod handoff;
mod reliable;
mod removal;
pub(crate) mod runtime;
mod scheduling;
mod session;
mod supervision;

use crate::ipc::{self, VERSION, field, wire};
use runtime::{DATA_IN, DATA_OUT, IO_TIMEOUT, SNAPSHOT};
use serde_json::{Value, json};
use session::Session;
use sha2::{Digest, Sha256};
use std::collections::BTreeMap;
use std::fs;
use std::io::{self, Read, Write};
use std::net::Shutdown;
use std::os::unix::ffi::OsStrExt;
use std::os::unix::net::UnixStream;
use std::path::PathBuf;
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::mpsc::{self, SyncSender};
use std::sync::{Arc, Mutex, RwLock, Weak};
use std::thread;
use std::time::Duration;

const SESSION_LIMIT: usize = 64;

pub(crate) fn run(args: &[String]) -> u8 {
    if args == ["restart"] {
        return match crate::bootstrap::restart() {
            Ok(()) => 0,
            Err(e) => {
                eprintln!("daemon restart: {e}");
                1
            }
        };
    }
    if !args.is_empty() {
        eprintln!("usage: swarm daemon");
        return 2;
    }
    match serve() {
        Ok(()) => 0,
        Err(err) => {
            eprintln!("daemon: serve: {err}");
            1
        }
    }
}

struct State {
    root: PathBuf,
    endpoint: String,
    sessions: RwLock<BTreeMap<String, Arc<Session>>>,
    subscribers: Mutex<Vec<Weak<Subscriber>>>,
    launch_gate: Mutex<()>,
    operation_locks: [Mutex<()>; 64],
    transactions: RwLock<()>,
    deletion_gate: Mutex<()>,
    handoff_wake: SyncSender<()>,
    generation: AtomicU64,
    schedule_socket: Option<PathBuf>,
}

impl State {
    fn rows(&self) -> Vec<Value> {
        let sessions = self.sessions.read().unwrap();
        let mut rows: Vec<_> = sessions.values().map(|s| s.view(&self.endpoint)).collect();
        rows.sort_by(|a, b| field(b, "created_at").cmp(field(a, "created_at")));
        rows
    }

    fn session(&self, request: &Value) -> io::Result<Arc<Session>> {
        let id = field(request, "session_id");
        let Some((endpoint, local)) = id.split_once('/') else {
            return Err(io::Error::other("invalid session id"));
        };
        if endpoint != self.endpoint || !runtime::valid_id(local) {
            return Err(io::Error::other("invalid session id"));
        }
        self.sessions
            .read()
            .unwrap()
            .get(local)
            .cloned()
            .ok_or_else(|| io::Error::other(format!("unknown session {id:?}")))
    }

    fn publish(&self, session: &Session) {
        let _ = self.handoff_wake.try_send(());
        let event = json!({
            "op": "event", "endpoint_id": self.endpoint, "session": session.view(&self.endpoint)
        });
        self.subscribers.lock().unwrap().retain(|subscriber| {
            let Some(subscriber) = subscriber.upgrade() else {
                return false;
            };
            if subscriber.events.try_send(event.clone()).is_ok() {
                return true;
            }
            let _ = subscriber.conn.shutdown(Shutdown::Both);
            false
        });
    }

    fn reserve(&self, request: &Value, caps: &[&str]) -> io::Result<Arc<Session>> {
        let mut request = request.clone();
        // The caller cannot stamp a linked launch; only resolving an owned source can.
        if let Some(fields) = request.as_object_mut() {
            fields.remove("resolved_agy_source");
        }
        let launch = &request["launch"];
        let options = &launch["options"];
        for (key, cap) in [
            ("resume_conversation_id", "external-resume"),
            ("resume_relocate", "resume-relocate"),
        ] {
            if options.get(key).is_some() && !caps.contains(&cap) {
                return Err(io::Error::other(format!(
                    "{cap} capability was not negotiated"
                )));
            }
        }
        let resume = field(launch, "agent") == "agy" && !field(options, "resume_from").is_empty();
        let _deletion = resume.then(|| self.deletion_gate.lock().unwrap());
        if resume {
            let source = field(options, "resume_from").to_owned();
            let session = self.session(&json!({"session_id":source}))?;
            session.expand_agy_resume(&mut request, &source)?;
        }
        let (bound, source) = handoff::bind(self, &request)?;
        self.reserve_prepared(Session::prepare(&bound)?, false, source)
    }

    fn reserve_prepared(
        &self,
        mut prepared: session::Prepared,
        scheduled: bool,
        source: Option<(String, String)>,
    ) -> io::Result<Arc<Session>> {
        let _reservation = self.launch_gate.lock().unwrap();
        if let Some((local, instance)) = source {
            if !self.sessions.read().unwrap().get(&local).is_some_and(|s| {
                s.is_live() && s.incarnation() == instance && s.verify_effective().is_ok()
            }) {
                return Err(io::Error::other(
                    "handoff: source changed during provider admission",
                ));
            }
            prepared.lineage["source_instance"] = json!(instance);
        }
        if self
            .sessions
            .read()
            .unwrap()
            .values()
            .filter(|s| s.is_live())
            .count()
            >= SESSION_LIMIT
        {
            return Err(io::Error::other("session limit reached"));
        }
        if let Some(directory) = scheduling::conflict(self, &prepared.cwd, scheduled) {
            return Err(io::Error::new(
                io::ErrorKind::WouldBlock,
                format!("workspace overlaps active Swarm session in {directory}"),
            ));
        }
        let session = Arc::new(Session::reserve(&self.root, prepared)?);
        self.sessions
            .write()
            .unwrap()
            .insert(session.id.clone(), session.clone());
        Ok(session)
    }

    fn start(&self, session: &Session) -> io::Result<Value> {
        session.start()?;
        session.await_ready()?;
        self.publish(session);
        Ok(session.view(&self.endpoint))
    }
}

fn serve() -> io::Result<()> {
    let configured = ipc::env_path("SWARM_DAEMON_STATE").ok_or_else(|| {
        io::Error::other("SWARM_DAEMON_STATE is required for the isolated Rust runtime")
    })?;
    runtime::private_dir(&configured)?;
    let root = fs::canonicalize(configured)?;
    let lock = ipc::env_path("SWARM_DAEMON_LOCK").unwrap_or_else(|| root.join("daemon.lock"));
    // Alternate sockets and lock overrides cannot create two writers for one state root.
    let _lock = runtime::lock(&lock)?;
    let ownership = root.join(".rust-state-writer.lock");
    let _state_lock = if lock == ownership {
        None
    } else {
        Some(runtime::lock(&ownership)?)
    };
    for entry in fs::read_dir(&root)? {
        let entry = entry?;
        if entry.path().join("meta.json").symlink_metadata().is_ok() {
            return Err(io::Error::other(
                "Go session state requires a separately qualified migration",
            ));
        }
    }
    let digest = Sha256::digest(root.as_os_str().as_bytes());
    let endpoint = format!(
        "ep-{}",
        digest[..4]
            .iter()
            .map(|b| format!("{b:02x}"))
            .collect::<String>()
    );
    let runtime_root = root.join("rust-v1");
    runtime::private_dir(&runtime_root)?;
    runtime::private_dir(&runtime_root.join("operations"))?;
    removal::recover(&runtime_root)?;
    let mut sessions = BTreeMap::new();
    for entry in fs::read_dir(&runtime_root)? {
        let entry = entry?;
        if entry.file_type()?.is_dir() && entry.file_name().to_str().is_some_and(runtime::valid_id)
        {
            match Session::load(entry.path()) {
                Ok(session) => {
                    session.reconcile();
                    sessions.insert(session.id.clone(), Arc::new(session));
                }
                Err(err) => eprintln!(
                    "daemon: session {}: {err}",
                    entry.file_name().to_string_lossy()
                ),
            }
        }
    }
    let socket = ipc::socket_path()?;
    if let Some(parent) = socket.parent().filter(|p| !p.as_os_str().is_empty()) {
        runtime::private_dir(parent)?;
    }
    // The daemon allocates files and sockets only within private state directories.
    // Set the process mask before starting workers so there is no bind/chmod exposure.
    unsafe { libc::umask(0o077) };
    runtime::reclaim_socket(&socket)?;
    let listener = runtime::bind(&socket)?;
    crate::environment::save(&root)?;
    let (handoff_wake, handoff_events) = mpsc::sync_channel(1);
    let receipt = scheduling::socket(&socket)
        .map_err(|e| {
            eprintln!("daemon: scheduled completion listener unavailable: {e}");
            e
        })
        .ok();
    let state = Arc::new(State {
        handoff_wake,
        root: runtime_root,
        endpoint,
        sessions: RwLock::new(sessions),
        subscribers: Mutex::new(Vec::new()),
        launch_gate: Mutex::new(()),
        operation_locks: std::array::from_fn(|_| Mutex::new(())),
        transactions: RwLock::new(()),
        deletion_gate: Mutex::new(()),
        generation: AtomicU64::new(1),
        schedule_socket: receipt.as_ref().map(|(path, _)| path.clone()),
    });
    let handoffs = state.clone();
    let _ = handoffs.handoff_wake.try_send(());
    thread::spawn(move || supervision::run(handoffs, handoff_events));
    if let Some((_, listener)) = receipt {
        scheduling::receipts(listener, state.clone());
    }
    let monitor = state.clone();
    thread::spawn(move || {
        loop {
            thread::sleep(Duration::from_millis(150));
            let sessions: Vec<_> = monitor.sessions.read().unwrap().values().cloned().collect();
            for session in sessions {
                if session.reconcile() {
                    monitor.publish(&session);
                }
            }
        }
    });
    for incoming in listener.incoming() {
        let stream = incoming?;
        let state = state.clone();
        thread::spawn(move || {
            let _ = connection(stream, &state);
        });
    }
    Ok(())
}

type Writer = Arc<Mutex<UnixStream>>;

fn reply(destination: &Writer, state: &State, mut message: Value) -> io::Result<()> {
    message["endpoint_id"] = json!(state.endpoint);
    runtime::write_control(&mut *destination.lock().unwrap(), &message)
}

fn connection(mut stream: UnixStream, state: &State) -> io::Result<()> {
    let _cleanup = Close(stream.try_clone()?);
    stream.set_read_timeout(Some(IO_TIMEOUT))?;
    stream.set_write_timeout(Some(IO_TIMEOUT))?;
    let mut first = [0];
    stream.read_exact(&mut first)?;
    if first == *b"V" {
        stream.read_exact(&mut [0; 4])?;
        return stream.write_all(&2u32.to_be_bytes());
    }
    let hello = runtime::read_control(&mut first.as_slice().chain(&mut stream))?;
    let writer = Arc::new(Mutex::new(stream.try_clone()?));
    if field(&hello, "op") != "hello" || hello["protocol_version"] != VERSION {
        return reply(
            &writer,
            state,
            json!({"op": "error", "error": "incompatible protocol version"}),
        );
    }
    let caps: Vec<_> = hello["capabilities"]
        .as_array()
        .into_iter()
        .flatten()
        .filter_map(Value::as_str)
        .filter(|cap| {
            matches!(
                *cap,
                "attach"
                    | "subscribe"
                    | "owner-launch"
                    | "external-resume"
                    | "resume-relocate"
                    | "semantic-send"
                    | "semantic-target"
                    | "native-requests-v1"
                    | crate::bootstrap::RESTART
            ) || (state.schedule_socket.is_some() && scheduling::CAPS.contains(cap))
        })
        .collect();
    let mut welcome = json!({
        "op": "hello", "protocol_version": VERSION,
        "build_version": option_env!("SWARM_BUILD_VERSION").unwrap_or("dev"),
    });
    if !caps.is_empty() {
        welcome["capabilities"] = json!(caps);
    }
    reply(&writer, state, welcome)?;
    stream.set_read_timeout(None)?;
    let mut subscribed = None;
    loop {
        let (tag, payload) = wire::read_frame(&mut stream)?;
        if tag != wire::CONTROL {
            continue;
        }
        let request: Value = serde_json::from_slice(&payload)?;
        if request["endpoint_id"] != state.endpoint {
            reply(
                &writer,
                state,
                json!({"op": "error", "error": "invalid endpoint id"}),
            )?;
            continue;
        }
        let op = field(&request, "op");
        if op == "owner_daemon_restart" {
            let requested = std::path::Path::new(field(&request, "state_dir"));
            if !caps.contains(&crate::bootstrap::RESTART) || Some(requested) != state.root.parent()
            {
                reply(
                    &writer,
                    state,
                    json!({"op":"error","error":"restart requires the bound supervisor state directory"}),
                )?;
                continue;
            }
            let _drained = state.transactions.write().unwrap();
            reply(&writer, state, json!({"op":"ok"}))?;
            std::process::exit(0);
        }
        // Attach can stay open for hours. Its live lease is scoped to the socket.
        let _transaction = (op != "attach").then(|| state.transactions.read().unwrap());
        let result = match op {
            "list" => reply(
                &writer,
                state,
                json!({"op": "list", "sessions": state.rows()}),
            ),
            "subscribe" => {
                if subscribed.is_none() {
                    let (sink, events) = mpsc::sync_channel(64);
                    let subscriber = Arc::new(Subscriber {
                        conn: stream.try_clone()?,
                        events: sink,
                    });
                    {
                        let mut subscribers = state.subscribers.lock().unwrap();
                        subscribers.retain(|sink| sink.strong_count() != 0);
                        subscribers.push(Arc::downgrade(&subscriber));
                    }
                    let destination = writer.clone();
                    subscribed = Some(subscriber);
                    thread::spawn(move || {
                        for event in events {
                            let mut conn = destination.lock().unwrap();
                            if runtime::write_control(&mut *conn, &event).is_err() {
                                let _ = conn.shutdown(Shutdown::Both);
                                break;
                            }
                        }
                    });
                }
                reply(&writer, state, json!({"op": "ok"}))
            }
            "schedule_tick" | "schedule_run" | "schedule_workspace_check" | "schedule_route_check" | "schedule_native_status" => {
                let message = match scheduling::handle(state, &request, &caps) {
                    Ok(message) => message,
                    Err(e) => json!({"op":"error","error_code":"invalid_field","error":e.to_string()}),
                };
                reply(&writer, state, message)
            }
            "owner_launch" | "owner_launch_status" => {
                reliable::handle(state, &request, &caps)
                    .and_then(|v| reply(&writer, state, v))
            }
            "launch" => {
                let message = match state.reserve(&request, &caps) {
                    Err(e) => {
                        json!({"op":"error","error_code":"invalid_field","error":e.to_string()})
                    }
                    Ok(s) => match state.start(&s) {
                        Ok(view) => json!({"op":"launch","session":view}),
                        Err(e) => {
                            json!({"op":"error","error_code":"outcome_unknown","error":e.to_string()})
                        }
                    },
                };
                reply(&writer, state, message)
            }
            "rename" | "set_tag" => state.session(&request).and_then(|session| {
                let key = if op == "rename" { "name" } else { "tag" };
                let value = match request.get(key) {
                    None | Some(Value::Null) => "",
                    Some(Value::String(s)) => s,
                    _ => return Err(io::Error::other("invalid session label type")),
                };
                if session.set_label(key, value)? {
                    state.publish(&session);
                }
                reply(
                    &writer,
                    state,
                    json!({"op":"ok","session_id":request["session_id"]}),
                )
            }),
            "delete" => removal::delete(state, &request).and_then(|()| {
                reply(
                    &writer,
                    state,
                    json!({"op":"ok","session_id":request["session_id"]}),
                )
            }),
            "kill" => state.session(&request).and_then(|session| {
                session.kill()?;
                reply(
                    &writer,
                    state,
                    json!({"op": "ok", "session_id": request["session_id"]}),
                )
            }),
            "native_requests" if caps.contains(&"native-requests-v1") => state.session(&request).and_then(|session| {
                let body = &request["native_requests"];
                let id = field(&request,"session_id");
                if body["session"] != id || !["request_id","native_session_id"].into_iter()
                    .all(|key|body[key].is_null() || body[key] == "") {
                    return Err(io::Error::other("only native request summary reads are supported"));
                }
                let result=session.native_requests(id)?;
                reply(&writer,state,json!({"op":"native_requests","session_id":id,"native_requests_result":result}))
            }),
            "semantic_target" if caps.contains(&"semantic-target") => state.session(&request).and_then(|session| {
                let instance = session.semantic_target()?;
                reply(&writer,state,json!({"op":"semantic_target","session_id":request["session_id"],"semantic_target_instance":instance}))
            }),
            "semantic_status" if caps.contains(&"semantic-send") => state.session(&request).and_then(|session| {
                let operation = field(&request,"operation_id");
                let receipt = session.semantic_status(operation,field(&request,"session_id"),&state.root)?.unwrap_or_else(|| json!({
                    "state":"unknown_operation", "session":request["session_id"],"operation_id":operation,
                }));
                reply(&writer,state,json!({"op":"semantic_status","operation_id":operation,"semantic_receipt":receipt}))
            }),
            "semantic_send" if caps.contains(&"semantic-send") => state.session(&request).and_then(|session| {
                let operation = field(&request,"operation_id");
                let receipt = session.semantic_send(&request,field(&request,"session_id"),state)?;
                let response = match field(&receipt,"state") {
                    "native_accepted" => json!({"op":"ok","operation_id":operation}),
                    "rejected" => json!({"op":"error","operation_id":operation,"error_code":"structured_unsupported","error":"Codex app-server rejected semantic input"}),
                    _ => json!({"op":"error","operation_id":operation,"error_code":"outcome_unknown","error":"Codex semantic submission outcome is unknown; inspect --status"}),
                };
                reply(&writer,state,response)
            }),
            "terminal_subscribe" => state.session(&request).and_then(|session| {
                let mut terminal = session.status_exchange(&json!({"type": "snapshot_req"}))?;
                terminal["session"] = json!(session.id);
                reply(&writer, state, json!({"op": "ok"}))?;
                reply(
                    &writer,
                    state,
                    json!({"op": "terminal_snapshot", "terminal": terminal}),
                )
            }),
            "send_input" => state.session(&request).and_then(|session| {
                let data = &request["send_input"];
                if !data.is_object()
                    || !["key", "text"]
                        .into_iter()
                        .all(|key| data[key].is_null() || data[key].is_string())
                    || !(data["submit"].is_null() || data["submit"].is_boolean())
                {
                    return Err(io::Error::other("invalid send_input field type"));
                }
                let key = field(data, "key");
                let text = field(data, "text");
                let submit = data["submit"] == true;
                let (bytes, paste) = if key.is_empty() && !text.is_empty() && text.len() <= 4096 {
                    (text, true)
                } else if !key.is_empty() && text.is_empty() && !submit {
                    let bytes = match key {
                        "enter" => "\r",
                        "esc" => "\x1b",
                        "ctrl-c" => "\x03",
                        "tab" => "\t",
                        "up" => "\x1b[A",
                        "down" => "\x1b[B",
                        _ => return Err(io::Error::other("unknown key")),
                    };
                    (bytes, false)
                } else {
                    return Err(io::Error::other("need exactly one of text or key"));
                };
                let result = session.status_exchange(&json!({
                    "type": "input", "data": bytes, "submit": submit, "paste": paste
                }))?;
                if field(&result, "type") != "ok" {
                    return Err(io::Error::other(field(&result, "error").to_owned()));
                }
                reply(
                    &writer,
                    state,
                    json!({"op": "ok", "session_id": request["session_id"]}),
                )
            }),
            "attach" => state
                .session(&request)
                .and_then(|session| attachment(&mut stream, &writer, state, &request, &session)),
            "resize" => Ok(()),
            _ => Err(io::Error::other(format!("unknown op {op:?}"))),
        };
        if let Err(err) = result {
            let message = if err.to_string() == "invalid session id" || op.is_empty() {
                err.to_string()
            } else {
                format!("{op}: {err}")
            };
            reply(&writer, state, json!({"op": "error", "error": message}))?;
        }
    }
}

struct Close(UnixStream);

impl Drop for Close {
    fn drop(&mut self) {
        let _ = self.0.shutdown(Shutdown::Both);
    }
}

struct Subscriber {
    conn: UnixStream,
    events: SyncSender<Value>,
}

fn attachment(
    client: &mut UnixStream,
    writer: &Writer,
    state: &State,
    request: &Value,
    session: &Session,
) -> io::Result<()> {
    let mut shim = session.dial()?;
    runtime::write_control(&mut shim, &json!({"type": "attach"}))?;
    let info = runtime::read_control(&mut shim)?;
    let length = info["snapshot_len"]
        .as_u64()
        .filter(|n| *n > 0 && *n <= 32 * 1024 * 1024)
        .ok_or_else(|| io::Error::other("invalid shim snapshot length"))? as usize;
    if field(&info, "type") != "snapshot_info" {
        return Err(io::Error::other("expected shim snapshot envelope"));
    }
    let mut snapshot = Vec::with_capacity(length);
    while snapshot.len() < length {
        let (tag, bytes) = wire::read_frame(&mut shim)?;
        if tag != SNAPSHOT || bytes.is_empty() || bytes.len() > length - snapshot.len() {
            return Err(io::Error::other("invalid shim snapshot chunk"));
        }
        snapshot.extend(bytes);
    }
    let generation = state.generation.fetch_add(1, Ordering::Relaxed);
    let id = field(request, "session_id");
    {
        let mut destination = writer.lock().unwrap();
        runtime::write_control(
            &mut *destination,
            &json!({
                "op": "lease", "endpoint_id": state.endpoint, "session_id": id,
                "generation": generation, "snapshot_len": snapshot.len(),
            }),
        )?;
        for chunk in snapshot.chunks(64 * 1024) {
            wire::write_frame(&mut *destination, SNAPSHOT, chunk)?;
        }
    }
    shim.set_read_timeout(None)?;
    let mut source = shim.try_clone()?;
    let pump_writer = writer.clone();
    let closing = Arc::new(AtomicBool::new(false));
    let pump_closing = closing.clone();
    let pump = thread::spawn(move || {
        while let Ok((DATA_OUT, bytes)) = wire::read_frame(&mut source) {
            if wire::write_frame(&mut *pump_writer.lock().unwrap(), DATA_OUT, &bytes).is_err() {
                break;
            }
        }
        if !pump_closing.load(Ordering::Acquire) {
            let _ = pump_writer.lock().unwrap().shutdown(Shutdown::Read);
        }
    });
    let handoff = (|| {
        loop {
            let (tag, bytes) = wire::read_frame(client)?;
            if tag == DATA_IN {
                wire::write_frame(&mut shim, DATA_IN, &bytes)?;
            } else if tag == wire::CONTROL {
                let control: Value = serde_json::from_slice(&bytes)?;
                if control["endpoint_id"] != state.endpoint
                    || control["session_id"] != id
                    || control["generation"] != generation
                {
                    continue;
                }
                match field(&control, "op") {
                    "resize" => runtime::write_control(
                        &mut shim,
                        &json!({
                            "type": "resize", "cols": control["cols"], "rows": control["rows"]
                        }),
                    )?,
                    "detach" => break,
                    _ => {}
                }
            }
        }
        Ok::<(), io::Error>(())
    })();
    closing.store(true, Ordering::Release);
    let _ = shim.shutdown(Shutdown::Both);
    let _ = pump.join();
    reply(
        writer,
        state,
        json!({"op": "detach", "session_id": id, "generation": generation}),
    )?;
    match handoff {
        Err(err) if err.kind() == io::ErrorKind::UnexpectedEof => Ok(()),
        outcome => outcome,
    }
}
