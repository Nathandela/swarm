use super::worker;
use crate::{ipc::field, providers, supervisor::runtime};
use serde_json::{Value, json};
use std::env;
use std::fs::{self, File};
use std::io::{self, BufRead, BufReader, Read, Write};
use std::os::fd::AsRawFd;
use std::os::unix::fs::MetadataExt;
use std::path::Path;
use std::process::{ChildStdin, Command, Stdio};
use std::sync::mpsc::{self, Receiver};
use std::thread;
use std::time::{Duration, Instant};

const ACP_FRAME: u64 = 2 << 20;
const CLOSING_LIMIT: usize = 64 << 10;

pub(crate) fn worker(args: &[String]) -> u8 {
    let [directory] = args else {
        return 2;
    };
    match run(Path::new(directory)) {
        Ok(()) => 0,
        Err(error) => {
            eprintln!("Scheduled Antigravity worker: {error}");
            1
        }
    }
}

fn run(dir: &Path) -> io::Result<()> {
    let meta = runtime::read_json(&dir.join("session.json"))?;
    let executor = &meta["provider"];
    let binding = worker::read_binding(&meta)?
        .filter(|binding| {
            binding.authority == "agent-default"
                && executor["name"] == "agy"
                && executor["mode"] == "acp"
                && providers::agy::supports_acp(field(executor, "version"))
        })
        .ok_or_else(|| io::Error::other("worker has no qualified Antigravity ACP reservation"))?;
    let token = field(&meta, "token");
    let session = field(&meta, "id");
    if !runtime::valid_id(token)
        || !runtime::valid_id(session)
        || dir.file_name().and_then(|name| name.to_str()) != Some(session)
        || env::var(worker::ATTEMPT).as_deref() != Ok(binding.attempt_id.as_str())
        || env::var(worker::RUN).as_deref() != Ok(binding.run_id.as_str())
        || env::var(worker::SESSION).as_deref() != Ok(session)
    {
        return Err(io::Error::other("worker receipt binding changed"));
    }
    let cwd = env::current_dir()?.canonicalize()?;
    let identity = cwd.metadata()?;
    if Path::new(field(&meta, "cwd")) != cwd
        || meta["cwd_identity"] != json!([identity.dev(), identity.ino()])
    {
        return Err(io::Error::other("Antigravity scheduled workspace changed"));
    }
    let mut evidence = Evidence {
        dir,
        token,
        attempt: &binding.attempt_id,
        conversation: String::new(),
        operation: String::new(),
    };
    let result = relay(executor, &mut evidence, &cwd);
    if result.is_err() && evidence.conversation.is_empty() {
        let _ = evidence.save("startup_refused", "");
    }
    result
}

struct Evidence<'a> {
    dir: &'a Path,
    token: &'a str,
    attempt: &'a str,
    conversation: String,
    operation: String,
}

impl Evidence<'_> {
    fn save(&self, phase: &str, text: &str) -> io::Result<()> {
        let turn = match phase {
            "ready" | "finished" | "permission" => "idle",
            "working" => "active",
            _ => "unknown",
        };
        runtime::write_json(
            &self.dir.join("observation.json"),
            &json!({
                "token":self.token,"attempt":self.attempt,"conversation_id":self.conversation,
                "schedule_ready":phase == "ready","phase":phase,"operation":self.operation,
                "status":{"Process":"running","Turn":turn,
                    "Interaction":if phase == "permission" {"permission"} else {"none"}},
                "text":text,
            }),
        )
    }

    fn wait_prompt(
        &mut self,
        mut provider_alive: impl FnMut() -> io::Result<bool>,
    ) -> io::Result<String> {
        let path = self.dir.join("agy-prompt.json");
        loop {
            if !provider_alive()? {
                return Err(io::Error::other("Antigravity closed before opening prompt"));
            }
            match runtime::read_json(&path) {
                Ok(value) => {
                    let operation = field(&value, "operation");
                    if value["token"] != self.token
                        || value["attempt"] != self.attempt
                        || value["conversation_id"] != self.conversation
                        || !providers::codex_receipt::timely(operation)
                    {
                        return Err(io::Error::other("unproven Antigravity prompt"));
                    }
                    let text = field(&value, "text");
                    if text.is_empty() || !worker::prompt_fits(text, true) {
                        return Err(io::Error::other("invalid Antigravity opening prompt"));
                    }
                    self.operation = operation.into();
                    fs::remove_file(&path)?;
                    File::open(self.dir)?.sync_all()?;
                    return Ok(text.to_owned());
                }
                Err(e) if e.kind() == io::ErrorKind::NotFound => {
                    thread::sleep(Duration::from_millis(100))
                }
                Err(e) => return Err(e),
            }
        }
    }
}

fn relay(executor: &Value, evidence: &mut Evidence<'_>, cwd: &Path) -> io::Result<()> {
    let executable = Path::new(field(executor, "executable"));
    if !executable.is_absolute() || providers::identity(executable)? != executor["identity"] {
        return Err(io::Error::other(
            "Antigravity executable changed before ACP startup",
        ));
    }
    let bridge = env::var(worker::BIN).map_err(io::Error::other)?;
    if !Path::new(&bridge).is_absolute() {
        return Err(io::Error::other("worker receipt bridge is unavailable"));
    }
    let mut receipt_env = Vec::new();
    for name in [
        worker::RUN,
        worker::ATTEMPT,
        worker::PROOF,
        worker::SOCKET,
        worker::SESSION,
    ] {
        let value = env::var(name).map_err(io::Error::other)?;
        if value.is_empty() {
            return Err(io::Error::other("worker receipt bridge is unbound"));
        }
        receipt_env.push(json!({"name":name, "value":value}));
    }
    let mut child = Command::new(executable)
        .args(["acp", "--stdio"])
        .current_dir(cwd)
        .env_clear()
        .envs(runtime::launch_env(&Value::Null))
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()?;
    let mut to_provider = child.stdin.take().expect("ACP stdin");
    let fd = to_provider.as_raw_fd();
    if unsafe { libc::fcntl(fd, libc::F_SETFL, libc::O_NONBLOCK) } == -1 {
        let _ = child.kill();
        let _ = child.wait();
        return Err(io::Error::last_os_error());
    }
    let stdout = child.stdout.take().expect("ACP stdout");
    let rx = {
        let (send, receive) = mpsc::sync_channel(8);
        let mut reader = BufReader::new(stdout);
        thread::spawn(move || {
            loop {
                let mut frame = Vec::new();
                let read = reader
                    .by_ref()
                    .take(ACP_FRAME + 1)
                    .read_until(b'\n', &mut frame);
                if !matches!(read, Ok(n) if n != 0 && n as u64 <= ACP_FRAME && frame.last() == Some(&b'\n'))
                {
                    break;
                }
                let Ok(frame) = serde_json::from_slice::<Value>(&frame) else {
                    break;
                };
                if send.send(frame).is_err() {
                    break;
                }
            }
        });
        receive
    };
    let result = session(
        &mut Rpc {
            input: rx,
            output: &mut to_provider,
        },
        evidence,
        cwd,
        json!({"name":"swarm_schedule","command":bridge,"args":["schedule-receipt-bridge"],"env":receipt_env}),
        || Ok(child.try_wait()?.is_none()),
    );
    let _ = child.kill();
    let _ = child.wait();
    result
}

struct Rpc<'a> {
    input: Receiver<Value>,
    output: &'a mut ChildStdin,
}

impl Rpc<'_> {
    fn send(&mut self, message: Value) -> io::Result<()> {
        let mut frame = serde_json::to_vec(&message)?;
        frame.push(b'\n');
        if frame.len() as u64 > ACP_FRAME {
            return Err(io::Error::other(
                "Antigravity ACP request exceeds frame limit",
            ));
        }
        let mut pending = frame.as_slice();
        let deadline = Instant::now() + Duration::from_secs(5);
        while !pending.is_empty() && Instant::now() < deadline {
            match self.output.write(pending) {
                Ok(0) => return Err(io::ErrorKind::WriteZero.into()),
                Ok(n) => pending = &pending[n..],
                Err(e) if e.kind() == io::ErrorKind::Interrupted => {}
                Err(e) if e.kind() == io::ErrorKind::WouldBlock => {
                    let mut descriptor = libc::pollfd {
                        fd: self.output.as_raw_fd(),
                        events: libc::POLLOUT,
                        revents: 0,
                    };
                    if unsafe { libc::poll(&mut descriptor, 1, 25) } == -1 {
                        let e = io::Error::last_os_error();
                        if e.kind() != io::ErrorKind::Interrupted {
                            return Err(e);
                        }
                    }
                }
                Err(e) => return Err(e),
            }
        }
        if !pending.is_empty() {
            return Err(io::ErrorKind::TimedOut.into());
        }
        Ok(())
    }

    fn request(&mut self, id: u64, method: &str, params: Value) -> io::Result<()> {
        self.send(json!({"jsonrpc":"2.0","id":id,"method":method,"params":params}))
    }

    fn permission(&mut self, id: &Value, option: Option<&str>) -> io::Result<()> {
        let outcome = option.map_or_else(
            || json!({"outcome":"cancelled"}),
            |id| json!({"outcome":"selected","optionId":id}),
        );
        self.send(json!({"jsonrpc":"2.0","id":id,"result":{"outcome":outcome}}))
    }

    fn await_result(
        &mut self,
        id: u64,
        deadline: Option<Instant>,
        mut notify: impl FnMut(&mut Self, &Value) -> io::Result<()>,
    ) -> io::Result<Value> {
        loop {
            let frame = if let Some(deadline) = deadline {
                if Instant::now() >= deadline {
                    return Err(io::ErrorKind::TimedOut.into());
                }
                self.input
                    .recv_timeout(deadline.saturating_duration_since(Instant::now()))
                    .map_err(io::Error::other)?
            } else {
                self.input.recv().map_err(io::Error::other)?
            };
            if frame["jsonrpc"] != "2.0" {
                return Err(io::Error::other("invalid Antigravity ACP frame"));
            }
            match (field(&frame, "method"), frame["id"].is_null()) {
                ("", _) if frame["id"] == id => {
                    if frame.get("error").is_some() || !frame["result"].is_object() {
                        return Err(io::Error::other("Antigravity rejected an ACP request"));
                    }
                    return Ok(frame["result"].clone());
                }
                ("session/update", true) | ("session/request_permission", false) => notify(self, &frame)?,
                ("", _) | (_, true) => {}
                (_, false) => self.send(json!({
                    "jsonrpc":"2.0","id":frame["id"],"error":{"code":-32601,"message":"Unsupported ACP method"}
                }))?,
            }
        }
    }
}

fn session(
    rpc: &mut Rpc<'_>,
    evidence: &mut Evidence<'_>,
    cwd: &Path,
    bridge: Value,
    provider_alive: impl FnMut() -> io::Result<bool>,
) -> io::Result<()> {
    let deadline = Some(Instant::now() + Duration::from_secs(30));
    let startup = |rpc: &mut Rpc<'_>, frame: &Value| {
        if !frame["id"].is_null() {
            rpc.permission(&frame["id"], None)?;
        }
        Ok(())
    };
    rpc.request(
        1,
        "initialize",
        json!({"protocolVersion":1,"clientCapabilities":{}}),
    )?;
    if rpc.await_result(1, deadline, startup)?["protocolVersion"] != 1 {
        return Err(io::Error::other("Antigravity did not initialize ACP v1"));
    }
    rpc.request(2, "session/new", json!({"cwd":cwd,"mcpServers":[bridge]}))?;
    let opened = rpc.await_result(2, deadline, startup)?;
    let id = field(&opened, "sessionId");
    if !providers::codex_receipt::canonical_thread(id) {
        return Err(io::Error::other(
            "Antigravity did not open a qualified ACP conversation",
        ));
    }
    evidence.conversation = id.into();
    evidence.save("ready", "")?;
    let prompt = evidence.wait_prompt(provider_alive)?;
    evidence.save("working", "")?;
    rpc.request(
        3,
        "session/prompt",
        json!({
            "sessionId":id,"prompt":[{"type":"text","text":prompt}],
        }),
    )?;
    let mut closing = String::new();
    let mut overflow = false;
    let result = rpc.await_result(3, None, |rpc, frame| {
        let event = &frame["params"];
        if event["sessionId"] != id {
            if frame["method"] == "session/request_permission" {
                rpc.permission(&frame["id"], None)?;
            }
            return Ok(());
        }
        if frame["method"] == "session/request_permission" {
            let options = event["options"]
                .as_array()
                .filter(|opts| (1..=12).contains(&opts.len()));
            if let Some(options) = options
                && unsafe { libc::tcflush(libc::STDIN_FILENO, libc::TCIFLUSH) } == 0
            {
                let challenge = runtime::random_id()?;
                let code = &challenge[..12];
                evidence.save("permission", "")?;
                println!(
                    "\r\nAntigravity requests permission for {}",
                    display(field(&event["toolCall"], "title"))
                );
                for (index, option) in options.iter().enumerate() {
                    println!("{}. {}", index + 1, display(field(option, "name")));
                }
                println!("Enter the option number followed by {code}, or 0 to cancel.");
                io::stdout().flush()?;
                let mut line = String::new();
                let option = if io::stdin().lock().take(1025).read_line(&mut line).is_ok()
                    && line.ends_with('\n')
                {
                    permission_option(&line, code, options)
                } else {
                    None
                };
                rpc.permission(&frame["id"], option)?;
                evidence.save("working", "")?;
            } else {
                rpc.permission(&frame["id"], None)?;
            }
            return Ok(());
        }
        let update = &event["update"];
        match field(update, "sessionUpdate") {
            "agent_message_chunk" if update["content"]["type"] == "text" => {
                let chunk = field(&update["content"], "text");
                if !overflow && closing.len() + chunk.len() <= CLOSING_LIMIT {
                    closing.push_str(chunk);
                } else {
                    overflow = true;
                }
            }
            "tool_call" => {
                closing.clear();
                overflow = false;
            }
            _ => {}
        }
        Ok(())
    })?;
    if result["stopReason"] != "end_turn" || overflow {
        return Err(io::Error::other(
            "Antigravity did not produce a bounded completed response",
        ));
    }
    evidence.save("finished", &closing)?;
    println!("{}", display(&closing));
    Ok(())
}

fn permission_option<'a>(line: &str, challenge: &str, options: &'a [Value]) -> Option<&'a str> {
    let mut fields = line.split_whitespace();
    let index = fields.next()?.parse::<usize>().ok()?.checked_sub(1)?;
    if fields.next() != Some(challenge) || fields.next().is_some() {
        return None;
    }
    options.get(index)?["optionId"].as_str().filter(|option| {
        !option.is_empty() && option.len() <= 128 && !option.chars().any(char::is_control)
    })
}

fn display(text: &str) -> String {
    text.chars()
        .filter(|ch| !ch.is_control() || matches!(ch, '\n' | '\t'))
        .collect()
}
