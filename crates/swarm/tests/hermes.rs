mod support;

use serde_json::{Value, json};
use std::fs::{self, DirBuilder};
use std::io::{BufRead, BufReader, Read, Write};
use std::net::TcpListener;
use std::os::unix::fs::{DirBuilderExt, PermissionsExt};
use std::os::unix::net::{UnixListener, UnixStream};
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Stdio};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::{Duration, Instant};
use tungstenite::Message;
use tungstenite::handshake::server::{Request, Response};

const NATIVE: &str = "local-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef";
const CLASSIC: &str = "20240229_123456_ab12cd";

struct Sandbox {
    path: PathBuf,
    daemon: Option<Child>,
}

impl Sandbox {
    fn new(suffix: &str) -> Self {
        let temp = std::env::temp_dir();
        let root = if temp.as_os_str().len() <= 45 {
            &temp
        } else {
            Path::new("/tmp")
        };
        let path = root.join(format!("sh{}{}", std::process::id(), suffix));
        DirBuilder::new().mode(0o700).create(&path).unwrap();
        Self {
            path: path.canonicalize().unwrap(),
            daemon: None,
        }
    }

    fn start(&mut self) -> Peer {
        self.daemon = Some(
            Command::new(support::executable())
                .env_clear()
                .env("HOME", &self.path)
                .env("PATH", "/usr/bin:/bin")
                .env("SWARM_DAEMON_STATE", &self.path)
                .env("SWARM_DAEMON_SOCK", self.path.join("d"))
                .arg("daemon")
                .stdin(Stdio::null())
                .spawn()
                .unwrap(),
        );
        let socket = self.path.join("d");
        let stream = eventually(|| UnixStream::connect(&socket).ok());
        stream
            .set_read_timeout(Some(Duration::from_secs(12)))
            .unwrap();
        let mut peer = Peer {
            stream,
            endpoint: String::new(),
        };
        let reply = peer.query(json!({"op":"hello","protocol_version":1,"capabilities":["semantic-send","semantic-target","native-requests-v1"]}));
        assert_eq!(reply["op"], "hello", "{reply}");
        peer.endpoint = reply["endpoint_id"].as_str().unwrap().into();
        peer
    }

    fn restart(&mut self) -> Peer {
        let mut daemon = self.daemon.take().unwrap();
        daemon.kill().unwrap();
        daemon.wait().unwrap();
        self.start()
    }

    fn launch(&self, peer: &mut Peer, options: Value) -> Value {
        peer.query(json!({"op":"launch","launch":{"agent":"hermes","cwd":self.path,
            "env":[format!("PATH={}:/usr/bin:/bin",self.path.display()),
                format!("HOME={}",self.path.display()),format!("HERMES_HOME={}/h",self.path.display())],
            "cols":80,"rows":24,"options":options}}))
    }

    fn installed(&self, version: &str) {
        let binary = self.path.join("hermes");
        fs::write(
            &binary,
            format!(
                r#"#!/bin/sh
if [ "$1" = "--version" ]; then
    echo "Hermes Agent v{version} (fixture)"
    exit 0
fi
if [ "$1" = "--profile" ]; then shift 2; fi
if [ "$1 $2" = "gateway ensure" ]; then
    cat "$HOME/ensure.json"
    exit 0
fi
printf '%s\n' "$@" >>"$HOME/argv"
if [ "$2" = "--cli" ]; then
    printf '│ Nous Research │\r\n│ Session: {CLASSIC} │\r\n──────\r\n❯\r\n──────\r\n'
fi
# A bound fixture can only retain its child briefly if the test itself fails.
exec sleep 20
"#
            ),
        )
        .unwrap();
        fs::set_permissions(binary, fs::Permissions::from_mode(0o700)).unwrap();
    }
}

impl Drop for Sandbox {
    fn drop(&mut self) {
        if let Some(daemon) = &mut self.daemon {
            let _ = daemon.kill();
            let _ = daemon.wait();
        }
        let _ = fs::remove_dir_all(&self.path);
    }
}

struct Peer {
    stream: UnixStream,
    endpoint: String,
}
impl Peer {
    fn query(&mut self, mut request: Value) -> Value {
        request["endpoint_id"] = json!(self.endpoint);
        let payload = serde_json::to_vec(&request).unwrap();
        self.stream
            .write_all(&((payload.len() as u32 + 1).to_be_bytes()))
            .unwrap();
        self.stream.write_all(&[1]).unwrap();
        self.stream.write_all(&payload).unwrap();
        let mut size = [0; 4];
        self.stream.read_exact(&mut size).unwrap();
        let mut frame = vec![0; u32::from_be_bytes(size) as usize];
        self.stream.read_exact(&mut frame).unwrap();
        assert_eq!(frame[0], 1);
        serde_json::from_slice(&frame[1..]).unwrap()
    }

    fn view(&mut self, id: &str) -> Value {
        let list = self.query(json!({"op":"list"}));
        list["sessions"]
            .as_array()
            .unwrap_or_else(|| panic!("{list}"))
            .iter()
            .find(|row| row["id"] == id)
            .cloned()
            .unwrap_or_default()
    }
}

fn eventually<T>(mut attempt: impl FnMut() -> Option<T>) -> T {
    let end = Instant::now() + Duration::from_secs(10);
    loop {
        if let Some(output) = attempt() {
            return output;
        }
        assert!(Instant::now() < end, "condition timed out");
        thread::sleep(Duration::from_millis(30));
    }
}

#[test]
fn hermes_classic_launch_status_resume_and_delete() {
    let mut fixture = Sandbox::new("c");
    fixture.installed("0.22.5");
    let mut peer = fixture.start();
    let launched = fixture.launch(&mut peer, json!({"profile":"fixture","model":"fake"}));
    assert_eq!(launched["op"], "launch", "{launched}");
    let id = launched["session"]["id"].as_str().unwrap();
    eventually(|| {
        let row = peer.view(id);
        (row["status"]["Turn"] == "idle" && row["conversation_id"] == CLASSIC).then_some(())
    });
    assert_eq!(
        peer.query(json!({"op":"semantic_target","session_id":id}))["op"],
        "error"
    );
    assert_eq!(peer.query(json!({"op":"kill","session_id":id}))["op"], "ok");
    eventually(|| (peer.view(id)["status"]["Process"] == "exited").then_some(()));
    let resumed = fixture.launch(&mut peer, json!({"resume_from":id}));
    assert_eq!(resumed["op"], "launch", "{resumed}");
    let successor = resumed["session"]["id"].as_str().unwrap();
    let trace = eventually(|| {
        fs::read_to_string(fixture.path.join("argv"))
            .ok()
            .filter(|argv| argv.contains("--no-restore-cwd"))
    });
    assert!(trace.contains(&format!("--cli\n--resume\n{CLASSIC}\n--no-restore-cwd")));
    for id in [id, successor] {
        assert_eq!(
            peer.query(json!({"op":"delete","session_id":id}))["op"],
            "ok"
        );
    }
}

#[test]
#[allow(clippy::result_large_err)] // tungstenite fixes the handshake callback error type.
fn hermes_shared_owner_submission_restart_viewer_loss_and_scoped_stop() {
    let mut fixture = Sandbox::new("n");
    fixture.installed("0.22.5");
    let home = fixture.path.join("h");
    DirBuilder::new().mode(0o700).create(&home).unwrap();
    let control_path = home.join("gateway.sock");
    let control = UnixListener::bind(&control_path).unwrap();
    fs::set_permissions(&control_path, fs::Permissions::from_mode(0o600)).unwrap();
    let tcp = TcpListener::bind("127.0.0.1:0").unwrap();
    let pid = std::process::id();
    let identify = json!({"pid":pid,"runtime_protocol":1,"state":"ready","instance_id":"test-owner","authority_epoch":4,
        "code_sha":"fixture-sha","api_origin":format!("http://{}",tcp.local_addr().unwrap()),"hermes_home":home,
        "served_profiles":[{"profile_id":home,"home":home}]});
    let methods = Arc::new(Mutex::new(Vec::new()));
    let calls = methods.clone();
    let ws_home = home.clone();
    let admissions = Arc::new(Mutex::new(Vec::<Value>::new()));
    let admissions_for_server = admissions.clone();
    let ws = thread::spawn(move || {
        for stream in tcp.incoming() {
            let stream = stream.unwrap();
            let calls = calls.clone();
            let ws_home = ws_home.clone();
            let admissions = admissions_for_server.clone();
            thread::spawn(move || {
                let Ok(mut socket) =
                    tungstenite::accept_hdr(stream, |req: &Request, mut response: Response| {
                        assert_eq!(req.uri().path(), "/api/ws");
                        assert_eq!(
                            req.headers()["Sec-WebSocket-Protocol"],
                            "hermes-gateway-v1, hermes-gateway-ticket.fixture-ticket"
                        );
                        response.headers_mut().insert(
                            "Sec-WebSocket-Protocol",
                            "hermes-gateway-v1".parse().unwrap(),
                        );
                        Ok(response)
                    })
                else {
                    return;
                };
                while let Ok(Message::Text(request)) = socket.read() {
                    let req: Value = serde_json::from_str(&request).unwrap();
                    let method = req["method"].as_str().unwrap();
                    let params = &req["params"];
                    calls
                        .lock()
                        .unwrap()
                        .push((method.to_owned(), params.clone()));
                    let snapshot = || {
                        json!({"session_id":NATIVE,"authority_epoch":4,"subscription_id":"scope",
                        "execution_state":"running","execution_generation":3,"info":{"cwd":ws_home.parent().unwrap()},
                        "pending":*admissions.lock().unwrap(),"prompts":[{"kind":"approval","prompt_id":"a1"}]})
                    };
                    let result = match method {
                        "setup.status" => json!({"provider_configured":true}),
                        "session.create" => snapshot(),
                        "session.resume" => {
                            assert_eq!(params["session_id"], NATIVE);
                            snapshot()
                        }
                        "process.list" => json!({"processes":[]}),
                        "subagent.list" => json!({"subagents":[],"delegations":[]}),
                        "prompt.submit" => {
                            assert_eq!(params["session_id"], NATIVE);
                            let receipt = json!({"ref":{"profile_id":ws_home,"session_id":NATIVE},
                                "input_id":params["input_id"],"admission_id":"queued-id","status":"queued",
                                "execution_generation":0,"authority_epoch":4});
                            admissions.lock().unwrap().push(receipt.clone());
                            receipt
                        }
                        "session.interrupt" => {
                            assert_eq!(params["execution_generation"], 3);
                            json!({})
                        }
                        "prompt.cancel" | "process.stop" => json!({}),
                        "session.detach" => {
                            json!({"session_id":NATIVE,"subscription_id":"scope","detached":true})
                        }
                        _ => panic!("unexpected provider method {method}"),
                    };
                    if method == "prompt.submit" && params["text"] == "fixture lost reply" {
                        break;
                    }
                    if socket
                        .send(Message::Text(
                            json!({"id":req["id"],"jsonrpc":"2.0","result":result})
                                .to_string()
                                .into(),
                        ))
                        .is_err()
                    {
                        break;
                    }
                }
            });
        }
    });
    let control_home = home.clone();
    let owner_changed = Arc::new(AtomicBool::new(false));
    let owner_changed_in_server = owner_changed.clone();
    let server = thread::spawn(move || {
        for conn in control.incoming() {
            let mut conn = conn.unwrap();
            let mut raw = Vec::new();
            if BufReader::new(&conn).read_until(b'\n', &mut raw).is_err() {
                continue;
            }
            let Ok(req) = serde_json::from_slice::<Value>(&raw) else {
                continue;
            };
            let result = match req["verb"].as_str().unwrap() {
                "identify" => {
                    let mut endpoint = identify.clone();
                    if owner_changed_in_server.load(Ordering::Relaxed) {
                        endpoint["authority_epoch"] = json!(5);
                    }
                    endpoint
                }
                "session-ticket" => {
                    assert_eq!(req["params"]["profile_id"], json!(control_home));
                    json!({"runtime_protocol":1,"profile_id":control_home,"instance_id":"test-owner","ticket":"fixture-ticket"})
                }
                verb => panic!("unexpected control verb {verb}"),
            };
            let _ = writeln!(conn, "{}", json!({"ok":true,"id":1,"result":result}));
        }
    });
    fs::write(fixture.path.join("ensure.json"), json!({"state":"ready","client_code_sha":"fixture-sha",
        "endpoint":{"profile_id":home,"runtime_protocol":1,"instance_id":"test-owner","authority_epoch":4,"code_sha":"fixture-sha"}}).to_string()).unwrap();
    let discord = fixture.path.join("discord");
    DirBuilder::new().mode(0o700).create(&discord).unwrap();
    let config = discord.join("config.json");
    fs::write(
        &config,
        json!({"version":1,"enabled":true,"installation_id":"test","name":"test",
        "application_id":"123","owner_id":"124","token_file":fixture.path.join("unused"),
        "daemon_socket":fixture.path.join("d"),"work_dir":fixture.path})
        .to_string(),
    )
    .unwrap();
    fs::set_permissions(&config, fs::Permissions::from_mode(0o600)).unwrap();

    let mut peer = fixture.start();
    let launched = fixture.launch(&mut peer, json!({}));
    assert_eq!(launched["op"], "launch", "{launched}");
    let id = launched["session"]["id"].as_str().unwrap();
    eventually(|| {
        let row = peer.view(id);
        (row["group"] == "needs_input"
            && row["status"]["Interaction"] == "permission"
            && row["conversation_id"] == NATIVE)
            .then_some(())
    });
    let target = peer.query(json!({"op":"semantic_target","session_id":id}));
    assert_eq!(target["op"], "semantic_target", "{target}");
    let millis = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap()
        .as_millis();
    let operation = format!("semantic:{millis}:0123456789abcdef0123456789abcdef");
    let text = "/approve remains literal prompt text";
    let message = json!({"op":"semantic_send","session_id":id,"operation_id":operation,"body_version":1,
        "composer_send":{"session":id,"session_instance":target["semantic_target_instance"],"text":text,
            "origin":{"kind":"external","installation_id":"fixture","request_id":"first","actor_id":"sender"}}});
    assert_eq!(peer.query(message.clone())["op"], "ok");
    peer = fixture.restart();
    assert_eq!(peer.query(message.clone())["op"], "ok");
    let mut changed = message;
    changed["composer_send"]["origin"]["actor_id"] = json!("different");
    assert_eq!(peer.query(changed)["op"], "error");
    let observed =
        peer.query(json!({"op":"semantic_status","session_id":id,"operation_id":operation}));
    assert_eq!(
        observed["semantic_receipt"]["state"], "submitted",
        "{observed}"
    );
    assert_eq!(
        observed["semantic_receipt"]["native_admission"]["execution_generation"],
        0
    );
    assert_eq!(
        peer.query(json!({"op":"delete","session_id":id}))["op"],
        "error"
    );
    assert_eq!(peer.view(id)["status"]["Process"], "running");
    let ambiguous = format!("semantic:{millis}:abcdef0123456789abcdef0123456789");
    let message = json!({"op":"semantic_send","session_id":id,"operation_id":ambiguous,"body_version":1,
        "composer_send":{"session":id,"session_instance":target["semantic_target_instance"],"text":"fixture lost reply"}});
    assert_eq!(peer.query(message.clone())["error_code"], "outcome_unknown");
    assert_eq!(
        peer.query(json!({"op":"semantic_status","session_id":id,"operation_id":ambiguous}))["semantic_receipt"]
            ["state"],
        "submitted"
    );
    assert_eq!(peer.query(message)["op"], "ok");
    let stopped = peer.query(json!({"op":"kill","session_id":id}));
    assert_eq!(stopped["error_code"], "outcome_unknown", "{stopped}");
    eventually(|| {
        let row = peer.view(id);
        (row["status"]["NativeWorkUnknown"] == true && row["group"] == "needs_input").then_some(())
    });
    assert_eq!(
        peer.query(json!({"op":"delete","session_id":id}))["op"],
        "error"
    );
    assert!(
        fixture
            .path
            .join("rust-v1")
            .join(id.split_once('/').unwrap().1)
            .exists()
    );
    let mut disabled: Value = serde_json::from_slice(&fs::read(&config).unwrap()).unwrap();
    disabled["enabled"] = json!(false);
    fs::write(&config, disabled.to_string()).unwrap();
    let resumed = fixture.launch(&mut peer, json!({"resume_from":id}));
    assert_eq!(resumed["session"]["conversation_id"], NATIVE, "{resumed}");
    let successor = resumed["session"]["id"].as_str().unwrap();
    let scoped = peer.query(json!({"op":"kill","session_id":id}));
    assert!(
        scoped["error"]
            .as_str()
            .is_some_and(|text| text.contains("successor")),
        "{scoped}"
    );
    owner_changed.store(true, Ordering::Relaxed);
    eventually(|| {
        let row = peer.view(successor);
        (row["status"]["Interaction"] == "error"
            && row["capabilities"]["native_owner_input"].is_null())
        .then_some(())
    });
    assert_eq!(
        peer.query(json!({"op":"semantic_target","session_id":successor}))["op"],
        "error"
    );
    assert_eq!(
        peer.query(json!({"op":"kill","session_id":successor}))["error_code"],
        "outcome_unknown"
    );
    eventually(|| (peer.view(successor)["status"]["NativeWorkUnknown"] == true).then_some(()));
    let methods = methods.lock().unwrap();
    let count = |name| methods.iter().filter(|(method, _)| method == name).count();
    assert_eq!(count("session.create"), 1);
    assert_eq!(count("prompt.submit"), 2);
    assert!(
        count("prompt.cancel") > 0 && count("session.interrupt") > 0 && count("process.stop") > 0
    );
    assert!(
        methods
            .iter()
            .any(|(method, params)| method == "prompt.submit" && params["text"] == text)
    );
    assert!(methods.iter().all(|(method, params)| !matches!(
        method.as_str(),
        "prompt.cancel" | "session.interrupt" | "process.stop" | "session.detach"
    ) || params["session_id"] == NATIVE));
    assert_eq!(count("session.close"), 0);
    assert_eq!(count("approval.respond"), 0);
    drop((ws, server));
}
