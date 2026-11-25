mod pty;
mod screen;

use crate::ipc::{field, wire};
use crate::providers;
use crate::supervisor::runtime::{self, DATA_IN, DATA_OUT, SHIM_VERSION, SNAPSHOT};
use serde_json::{Value, json};
use std::fs::{self, File, OpenOptions};
use std::io::{self, Read};
use std::net::Shutdown;
use std::os::unix::fs::OpenOptionsExt;
use std::os::unix::net::{UnixListener, UnixStream};
use std::os::unix::process::ExitStatusExt;
use std::path::{Path, PathBuf};
use std::process::{Child, Command};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::mpsc::{self, SyncSender};
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::{Duration, Instant};

pub(crate) fn run(args: &[String]) -> u8 {
    if let [flag, path] = args
        && flag == "--rust-session"
    {
        return match serve(Path::new(path)) {
            Ok(()) => 0,
            Err(err) => {
                eprintln!("shim: {err}");
                1
            }
        };
    }
    eprintln!("usage: swarm shim --rust-session <private-session-dir>");
    2
}

struct State {
    token: String,
    hook_token: String,
    dir: PathBuf,
    working_dir: PathBuf,
    observations: Mutex<ProviderObservation>,
    master: File,
    screen: Mutex<ScreenState>,
    control: Mutex<u64>,
    generation: AtomicU64,
    child: Mutex<Option<Child>>,
    terminate_at: Mutex<Option<Instant>>,
}

#[derive(Default)]
struct ProviderObservation {
    state: providers::claude::Observation,
    conversation: Option<String>,
}

struct ScreenState {
    parser: vt100::Parser,
    subscriber: Option<Subscriber>,
}

struct Subscriber {
    generation: u64,
    output: SyncSender<Vec<u8>>,
    conn: UnixStream,
}

fn serve(dir: &Path) -> io::Result<()> {
    runtime::private_dir(dir)?;
    let config_file = dir.join("session.json");
    let config = runtime::read_json(&config_file)?;
    let token = field(&config, "token");
    if config["schema"] != 1 || !runtime::valid_id(token) {
        return Err(io::Error::other("incompatible session reservation"));
    }
    let bootstrap = runtime::read_control(&mut io::stdin().lock())?;
    // A reservation can spawn a worker at most once, even after its socket disappears.
    OpenOptions::new()
        .write(true)
        .create_new(true)
        .mode(0o600)
        .open(dir.join("claimed"))?
        .sync_all()?;
    File::open(dir)?.sync_all()?;
    let socket = dir.join("p");
    let listener = runtime::bind(&socket)?;
    let provider = &config["provider"];
    let executable = Path::new(field(provider, "executable"));
    let argv: Vec<_> = bootstrap["argv"]
        .as_array()
        .and_then(|args| args.iter().map(Value::as_str).collect::<Option<_>>())
        .ok_or_else(|| io::Error::other("malformed transient launch arguments"))?;
    let working_dir = field(&config, "cwd");
    let mut command = Command::new(executable);
    command.current_dir(working_dir).args(argv);
    command.env_clear().envs(runtime::launch_env(&Value::Null));
    let hook_token = runtime::random_id()?;
    command
        .env("TERM", "xterm-256color")
        .env("SWARM_RUST_HOOK_SOCKET", &socket)
        .env("SWARM_HOOK_TOKEN", &hook_token)
        .env("SWARM_SESSION_ID", field(&config, "id"));
    let (cols, rows) = runtime::dims(&config);
    pty::arm_shutdown()?;
    let (mut child, master) = (|| {
        if provider["name"] != "claude" || providers::identity(executable)? != provider["identity"]
        {
            return Err(io::Error::other(
                "Claude executable identity changed before exec",
            ));
        }
        pty::start(command, cols, rows)
    })()
    .inspect_err(|err| {
        let _ = runtime::write_json(
            &dir.join("exit.json"),
            &json!({"error": err.to_string(), "token": token}),
        );
        let _ = fs::remove_file(&socket);
    })?;
    let pid = std::process::id();
    if let Err(err) = (|| {
        runtime::write_json(
            &dir.join("owner.json"),
            &json!({
                "token": token, "pid": pid, "birth": runtime::birth(pid)?,
                "worker_pid": child.id(),
            }),
        )
    })() {
        let _ = pty::signal_group(&child, libc::SIGKILL);
        let _ = child.wait();
        let _ = fs::remove_file(&socket);
        return Err(err);
    }
    let state = Arc::new(State {
        token: token.into(),
        hook_token,
        dir: dir.to_owned(),
        working_dir: PathBuf::from(working_dir),
        observations: Mutex::new(ProviderObservation::default()),
        master,
        screen: Mutex::new(ScreenState {
            parser: vt100::Parser::new(rows.try_into().unwrap(), cols.try_into().unwrap(), 0),
            subscriber: None,
        }),
        control: Mutex::new(0),
        generation: AtomicU64::new(1),
        child: Mutex::new(Some(child)),
        terminate_at: Mutex::new(None),
    });
    let reader = state.clone();
    let output = thread::spawn(move || drain(reader));
    let shared = state.clone();
    thread::spawn(move || accept(listener, shared));
    let status = loop {
        if pty::SHUTDOWN_SIGNAL.load(Ordering::Relaxed) {
            terminate(&state)?;
        }
        let mut active = state.child.lock().unwrap();
        let terminate_at = state.terminate_at.lock().unwrap();
        let child = active.as_mut().expect("worker owned while serving");
        let exited = pty::child_exited(child)?;
        let kill_group = match *terminate_at {
            // Keep an exited leader unreaped while its descendants grace their TERM.
            Some(at) => Instant::now() >= at,
            None => exited,
        };
        if kill_group {
            pty::signal_group(child, libc::SIGKILL)?;
            break active
                .take()
                .expect("worker owned during shutdown")
                .wait()?;
        }
        drop((active, terminate_at));
        thread::sleep(Duration::from_millis(40));
    };
    runtime::write_json(
        &dir.join("exit.json"),
        &json!({
            "token": token, "code": status.code(), "signal": status.signal(),
        }),
    )?;
    let _ = fs::remove_file(socket);
    // Do not wait for a provider-owned descendant that holds the slave open.
    drop(output);
    Ok(())
}

fn terminate(state: &State) -> io::Result<()> {
    let active = state.child.lock().unwrap();
    let mut deadline = state.terminate_at.lock().unwrap();
    if let Some(child) = active.as_ref().filter(|_| deadline.is_none()) {
        pty::signal_group(child, libc::SIGTERM)?;
        *deadline = Some(Instant::now() + Duration::from_secs(2));
    }
    Ok(())
}

fn drain(state: Arc<State>) {
    let mut bytes = [0; 8192];
    let mut master = &state.master;
    loop {
        let n = match master.read(&mut bytes) {
            Ok(0) => break,
            Ok(n) => n,
            Err(err) if err.kind() == io::ErrorKind::WouldBlock => {
                let _ = pty::wait(master, libc::POLLIN, Duration::from_millis(100));
                continue;
            }
            Err(err) if err.kind() == io::ErrorKind::Interrupted => continue,
            Err(_) => break,
        };
        let mut screen = state.screen.lock().unwrap();
        screen.parser.process(&bytes[..n]);
        if let Some(sub) = &screen.subscriber
            && sub.output.try_send(bytes[..n].to_vec()).is_err()
        {
            let _ = sub.conn.shutdown(Shutdown::Both);
            screen.subscriber = None;
        }
    }
}

fn accept(listener: UnixListener, state: Arc<State>) {
    for incoming in listener.incoming() {
        let Ok(stream) = incoming else { continue };
        let state = state.clone();
        thread::spawn(move || {
            let _ = connection(stream, &state);
        });
    }
}

fn connection(mut stream: UnixStream, state: &State) -> io::Result<()> {
    stream.set_read_timeout(Some(runtime::IO_TIMEOUT))?;
    stream.set_write_timeout(Some(runtime::IO_TIMEOUT))?;
    let hello = runtime::read_control(&mut stream)?;
    if hello["type"] == "hook" {
        return hook_observation(&mut stream, state, &hello);
    }
    if field(&hello, "type") != "hello"
        || hello["wire_version"] != SHIM_VERSION
        || field(&hello, "token") != state.token
    {
        return Err(io::Error::other("shim custody handshake refused"));
    }
    runtime::write_control(
        &mut stream,
        &json!({
            "type": "hello", "wire_version": SHIM_VERSION, "token": state.token
        }),
    )?;
    let request = runtime::read_control(&mut stream)?;
    match field(&request, "type") {
        "snapshot_req" => {
            let terminal = screen::terminal(state.screen.lock().unwrap().parser.screen());
            runtime::write_control(&mut stream, &terminal)
        }
        "attach" => attach(stream, state),
        "signal" if request["sig"] == "term" => {
            terminate(state)?;
            runtime::write_control(&mut stream, &json!({"type": "ok"}))
        }
        "input" => {
            let text = field(&request, "data");
            if text.is_empty() || text.len() > 4096 {
                return Err(io::Error::other("invalid shim input payload"));
            }
            let _guard = state.control.lock().unwrap();
            let bracketed = request["paste"] == true
                && state
                    .screen
                    .lock()
                    .unwrap()
                    .parser
                    .screen()
                    .bracketed_paste();
            let mut data = Vec::new();
            if bracketed {
                data.extend_from_slice(b"\x1b[200~");
            }
            data.extend_from_slice(text.as_bytes());
            if bracketed {
                data.extend_from_slice(b"\x1b[201~");
            }
            if let Err(err) = pty::input(&state.master, &data) {
                return runtime::write_control(
                    &mut stream,
                    &json!({
                        "type": "error", "error": format!("input delivery may have been partial: {err}")
                    }),
                );
            }
            if request["submit"] == true {
                thread::sleep(Duration::from_millis(150));
                if let Err(err) = pty::input(&state.master, b"\r") {
                    return runtime::write_control(
                        &mut stream,
                        &json!({
                            "type": "error", "error": format!("text delivered but submit failed: {err}")
                        }),
                    );
                }
            }
            runtime::write_control(&mut stream, &json!({"type": "ok"}))
        }
        _ => Err(io::Error::other("unsupported shim operation")),
    }
}

fn hook_observation(stream: &mut UnixStream, state: &State, request: &Value) -> io::Result<()> {
    let event = field(request, "event");
    let payload = &request["payload"];
    if field(request, "token") != state.hook_token
        || !providers::claude::EVENTS.contains(&event)
        || !payload.is_object()
    {
        return Err(io::Error::other("invalid provider hook custody"));
    }
    let cwd = field(payload, "cwd");
    if !cwd.is_empty() && fs::canonicalize(cwd)? != state.working_dir {
        return Err(io::Error::other("provider hook cwd changed"));
    }
    let conversation = providers::claude::conversation_id(payload);
    let mut observed = state.observations.lock().unwrap();
    if let Some(id) = conversation {
        if let Some(first) = &observed.conversation {
            if first != id {
                return Err(io::Error::other("provider hook conversation changed"));
            }
        } else if !cwd.is_empty() {
            observed.conversation = Some(id.to_owned());
        }
    }
    observed.state.apply(event, payload);
    runtime::write_json(
        &state.dir.join("observation.json"),
        &json!({
            "token": state.token,
            "conversation_id": observed.conversation,
            "status": observed.state.status(),
        }),
    )?;
    runtime::write_control(stream, &json!({"type":"ok"}))
}

fn attach(mut stream: UnixStream, state: &State) -> io::Result<()> {
    let generation = state.generation.fetch_add(1, Ordering::Relaxed);
    let (tx, rx) = mpsc::sync_channel(32);
    let worker = stream.try_clone()?;
    let eviction = stream.try_clone()?;
    let snap;
    {
        let mut owner = state.control.lock().unwrap();
        let mut screen = state.screen.lock().unwrap();
        snap = screen::snapshot(screen.parser.screen())?;
        if let Some(previous) = screen.subscriber.replace(Subscriber {
            generation,
            output: tx,
            conn: eviction,
        }) {
            let _ = previous.conn.shutdown(Shutdown::Both);
        }
        *owner = generation;
    }

    stream.set_read_timeout(None)?;
    let mut writer = worker;
    let pump = thread::spawn(move || {
        let result = (|| {
            runtime::write_control(
                &mut writer,
                &json!({"type": "snapshot_info", "snapshot_len": snap.len()}),
            )?;
            for chunk in snap.chunks(64 * 1024) {
                wire::write_frame(&mut writer, SNAPSHOT, chunk)?;
            }
            for output in rx {
                wire::write_frame(&mut writer, DATA_OUT, &output)?;
            }
            Ok::<(), io::Error>(())
        })();
        if result.is_err() {
            let _ = writer.shutdown(Shutdown::Both);
        }
    });
    let result = (|| {
        loop {
            let (tag, bytes) = wire::read_frame(&mut stream)?;
            let owner = state.control.lock().unwrap();
            if *owner != generation {
                break;
            }
            match tag {
                DATA_IN => pty::input(&state.master, &bytes)?,
                wire::CONTROL => {
                    let request: Value = serde_json::from_slice(&bytes)?;
                    if request["type"] != "resize" {
                        break;
                    }
                    let (cols, rows) = runtime::dims(&request);
                    let mut view = state.screen.lock().unwrap();
                    pty::resize(&state.master, cols, rows)?;
                    view.parser
                        .set_size(rows.try_into().unwrap(), cols.try_into().unwrap());
                }
                _ => break,
            }
        }
        Ok::<(), io::Error>(())
    })();
    {
        let mut owner = state.control.lock().unwrap();
        let mut screen = state.screen.lock().unwrap();
        if screen
            .subscriber
            .as_ref()
            .is_some_and(|sub| sub.generation == generation)
        {
            screen.subscriber = None;
        }
        if *owner == generation {
            *owner = 0;
        }
    }
    let _ = stream.shutdown(Shutdown::Both);
    let _ = pump.join();
    result
}
