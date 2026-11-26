//! Active Discord custody for queued worker effects. The provider never reads the bot token.
use super::super::{
    Config, connector_intent, dispatcher::enrolled_source, invalid, native_manager_enrollment_at,
    receipt::Inbox, rest::Discord, team_thread,
};
use crate::{
    ipc::field,
    providers::{codex_proof, codex_receipt},
    socket::DeadlineReader,
    supervisor::runtime,
};
use serde_json::{Value, json};
use std::{
    fs::{self, File},
    io,
    os::unix::{
        fs::{FileTypeExt, MetadataExt},
        net::UnixStream,
    },
    path::{Path, PathBuf},
    sync::{
        Arc,
        atomic::{AtomicBool, Ordering},
    },
    time::{Duration, Instant},
};

const SOCKET: &str = "e";
const LEASE: &str = "e.lock";
const TIMEOUT: Duration = Duration::from_secs(7);

fn custody(scope: &Value) -> io::Result<&Path> {
    Path::new(field(&scope["launch"]["manager"], "auth_file"))
        .parent()
        .and_then(Path::parent)
        .ok_or_else(|| invalid("missing enrolled worker source custody"))
}

pub(in crate::discord) struct Service {
    path: PathBuf,
    identity: [u64; 2],
    issuer: Value,
    _lease: File,
    running: Arc<AtomicBool>,
}

impl Service {
    pub(in crate::discord) fn discord(scope: &Value, cfg: &Config) -> io::Result<Self> {
        let api = Discord::connect(cfg)?;
        let route = team_thread::Route::configured(cfg)?;
        Self::bind(scope, cfg.clone(), move |cfg, inbox, anchor| {
            if let Some(route) = &route {
                enrolled_source(
                    cfg,
                    inbox,
                    &team_thread::ThreadAudience {
                        route: route.clone(),
                        api: &api,
                    },
                    anchor,
                )
            } else {
                enrolled_source(cfg, inbox, &api, anchor)
            }
        })
    }

    pub(in crate::discord) fn issuer(&self) -> &Value {
        &self.issuer
    }

    pub(in crate::discord) fn bind(
        scope: &Value,
        cfg: Config,
        mut current: impl FnMut(&Config, &mut Inbox, &Value) -> io::Result<()> + Send + 'static,
    ) -> io::Result<Self> {
        let place = custody(scope)?.to_owned();
        let directory = place.join("manager-v1");
        let lease = runtime::lock(&directory.join(LEASE))?;
        let lease_metadata = lease.metadata()?;
        let path = directory.join(SOCKET);
        runtime::reclaim_socket(&path)?;
        let listener = runtime::bind(&path)?;
        listener.set_nonblocking(true)?;
        let metadata = fs::symlink_metadata(&path)?;
        let pid = std::process::id();
        let issuer = json!({
            "pid":pid,"birth":runtime::birth(pid)?,
            "socket":[metadata.dev(),metadata.ino()],
            "lease":[lease_metadata.dev(),lease_metadata.ino()]
        });
        let running = Arc::new(AtomicBool::new(true));
        let active = running.clone();
        let expected = scope.clone();
        std::thread::spawn(move || {
            while active.load(Ordering::Acquire) {
                let Ok((mut stream, _)) = listener.accept() else {
                    std::thread::sleep(Duration::from_millis(10));
                    continue;
                };
                let deadline = Instant::now() + TIMEOUT;
                let request = runtime::read_control(&mut DeadlineReader(&stream, deadline));
                if let Ok(request) = request {
                    let action = &request["action"];
                    let approved = (|| -> io::Result<()> {
                        if !request["challenge"].as_str().is_some_and(runtime::valid_id) {
                            return Err(invalid("missing enrolled send challenge"));
                        }
                        validate_connector(&expected, action)?;
                        let mut inbox = Inbox::open(&place, &cfg)?;
                        let job =
                            &inbox.manager()["worker_actions"][field(&action["target"], "job")];
                        let original = job["source_anchor"].clone();
                        let anchor = &action["source_anchor"];
                        current(&cfg, &mut inbox, anchor)?;
                        if anchor["composer"]["origin"] != original["composer"]["origin"] {
                            current(&cfg, &mut inbox, &original)?;
                        }
                        validate_connector(&expected, action)
                    })()
                    .is_ok();
                    if Instant::now() < deadline
                        && active.load(Ordering::Acquire)
                        && stream
                            .set_write_timeout(Some(Duration::from_secs(1)))
                            .is_ok()
                    {
                        let _ = runtime::write_control(
                            &mut stream,
                            &json!({
                                "challenge":request["challenge"],"approved":approved
                            }),
                        );
                    }
                }
            }
        });
        Ok(Self {
            path,
            identity: [metadata.dev(), metadata.ino()],
            issuer,
            _lease: lease,
            running,
        })
    }
}

impl Drop for Service {
    fn drop(&mut self) {
        self.running.store(false, Ordering::Release);
        if fs::symlink_metadata(&self.path)
            .is_ok_and(|meta| [meta.dev(), meta.ino()] == self.identity)
        {
            let _ = fs::remove_file(&self.path);
        }
    }
}

fn validate_connector(scope: &Value, action: &Value) -> io::Result<()> {
    let snapshot = connector_intent(scope)?;
    let state = &snapshot["manager"];
    let target = &action["target"];
    let job = &state["worker_actions"][field(target, "job")];
    if action["kind"] != "send"
        || action["state"] != "executing"
        || state["binding"] != action["binding"]
        || state["worker_actions"][field(action, "key")] != *action
        || job["call"]["name"] != "swarm_create"
        || job["arguments"]["agent"] != "codex"
        || job["operation_id"] != target["attempt"]
        || job["arguments"]["cwd"] != target["project"]
    {
        return Err(invalid(
            "enrolled send does not match a current connector intent",
        ));
    }
    Ok(())
}

pub(crate) fn guard(
    directory: &Path,
    cwd: &Path,
    token: &str,
    operation: &str,
    composer: &Value,
    grant: &Value,
) -> io::Result<File> {
    let action = &grant["action"];
    let scope = &grant["scope"];
    let args = &action["arguments"];
    let owned = runtime::read_json(&directory.join("session.json"))?;
    let child = &owned["mailbox"];
    let root = directory
        .parent()
        .ok_or_else(|| invalid("missing worker runtime root"))?;
    let supervisor = root
        .parent()
        .ok_or_else(|| invalid("missing worker supervisor root"))?;
    let parent_session = field(&action["binding"], "session");
    let parent_id = parent_session
        .rsplit_once('/')
        .map(|(_, id)| id)
        .filter(|id| runtime::valid_id(id))
        .ok_or_else(|| invalid("invalid enrolled manager custody"))?;
    let parent_dir = root.join(parent_id);
    let parent = runtime::read_json(&parent_dir.join("session.json"))?;
    let owner = runtime::read_json(&parent_dir.join("owner.json"))?;
    let boot_alive = || {
        owner["pid"]
            .as_u64()
            .and_then(|pid| u32::try_from(pid).ok())
            .is_some_and(|pid| {
                owner["birth"]
                    .as_u64()
                    .is_some_and(|birth| runtime::birth(pid).ok() == Some(birth))
            })
    };
    let binding = &action["binding"];
    if owned["token"] != token
        || field(args, "session")
            .rsplit_once('/')
            .is_none_or(|(endpoint, id)| {
                owned["id"] != id || !parent_session.starts_with(&format!("{endpoint}/"))
            })
        || !codex_receipt::timely(operation)
        || child["structured"] != true
        || child["discussion"] != binding["conversation_id"]
        || child["attempt"] != action["target"]["attempt"]
        || child["project"] != action["target"]["project"]
        || !boot_alive()
        || owner["token"] != parent["token"]
        || parent["manager"] != *scope
        || binding["session_instance"]
            != codex_receipt::provider_instance(field(binding, "provider"), field(&parent, "token"))
        || scope["launch"]["manager"]["native_cli"] == true
        || action["operation_id"] != operation
        || args["session"] != composer["session"]
        || args["session_instance"] != codex_receipt::instance(token)
        || args["session_instance"] != composer["session_instance"]
        || args["text"] != composer["text"]
    {
        return Err(invalid(
            "enrolled worker send incarnation or authority changed",
        ));
    }
    let verify_place = || -> io::Result<()> {
        let current = native_manager_enrollment_at(supervisor, &scope["launch"])?;
        if [
            "launch",
            "installation_id",
            "channel_id",
            "guild_id",
            "owner_id",
            "application_id",
            "projects",
            "supervisor_socket",
        ]
        .iter()
        .any(|key| current[*key] != scope[*key])
            || !boot_alive()
            || runtime::read_json(&parent_dir.join("owner.json"))? != owner
            || codex_receipt::hash(&crate::manager_mcp::read_credential(Path::new(field(
                &scope["launch"]["manager"],
                "auth_file",
            )))?)
                != scope["credential_hash"]
        {
            return Err(invalid(
                "enrolled worker send place or manager was withdrawn",
            ));
        }
        let checkout = &owned["checkout"];
        if checkout.is_null() {
            let info = cwd.metadata()?;
            if owned["cwd"] != json!(cwd)
                || cwd.canonicalize()? != cwd
                || !info.is_dir()
                || owned["cwd_identity"] != json!([info.dev(), info.ino()])
            {
                return Err(invalid("enrolled worker checkout changed"));
            }
        } else {
            if checkout["path"] != json!(cwd) {
                return Err(invalid("enrolled worker worktree changed"));
            }
            crate::workspace::verify(
                Path::new(field(&owned, "cwd")),
                field(&owned, "id"),
                checkout,
            )?;
        }
        let pro = runtime::read_json(&directory.join("codex-profile.json"))?;
        let settings = &pro["profile"];
        if pro["token"] != token
            || *settings
                != crate::providers::codex_continuity::profile(
                    &runtime::launch_env(&Value::Null),
                    cwd,
                    &settings["options"],
                )?
        {
            return Err(invalid("enrolled worker Codex profile changed"));
        }
        if binding["provider"] == "claude" {
            crate::providers::claude::verify_manager_origin(
                scope,
                Path::new(field(&scope["launch"], "cwd")),
            )?;
        } else if scope["account"]
            != crate::supervisor::manager::account(Path::new(field(scope, "account_home")))?
        {
            return Err(invalid("enrolled worker send account changed"));
        }
        Ok(())
    };
    verify_place()?;
    let path = custody(scope)?;
    let lock = super::super::worker_input::lock_ledger(path)?;
    validate_connector(scope, action)?;
    let native = codex_proof::Ledger::snapshot(&parent_dir, binding)?;
    super::verify_send_evidence(&native, action)?;
    let issuer = &action["issuer"];
    let directory = path.join("manager-v1");
    let socket = directory.join(SOCKET);
    let challenge = runtime::random_id()?;
    verify_bridge(scope, issuer)?;
    verify_issuer(&directory, issuer)?;
    let mut stream = UnixStream::connect(socket)?;
    if issuer["pid"] != runtime::peer_pid(&stream)? {
        return Err(invalid("enrolled send issuer peer changed"));
    }
    verify_issuer(&directory, issuer)?;
    stream.set_write_timeout(Some(Duration::from_secs(1)))?;
    runtime::write_control(&mut stream, &json!({"action":action,"challenge":challenge}))?;
    let reply = runtime::read_control(&mut DeadlineReader(&stream, Instant::now() + TIMEOUT))?;
    if reply["challenge"] != challenge || reply["approved"] != true {
        return Err(invalid(
            "Discord no longer authorizes the queued worker send",
        ));
    }
    verify_issuer(&directory, issuer)?;
    verify_bridge(scope, issuer)?;
    verify_place()?;
    validate_connector(scope, action)?;
    Ok(lock)
}

fn verify_bridge(scope: &Value, issuer: &Value) -> io::Result<()> {
    let pin = &scope["bridge_issuer"];
    let socket = Path::new(field(&scope["launch"]["manager"], "bridge_socket"));
    if issuer["pid"] != pin["pid"] || issuer["birth"] != pin["birth"] {
        return Err(invalid("send issuer is outside the bound manager bridge"));
    }
    let check = || -> io::Result<()> {
        let meta = fs::symlink_metadata(socket)?;
        if !meta.file_type().is_socket() || pin["socket"] != json!([meta.dev(), meta.ino()]) {
            return Err(invalid("bound manager bridge changed"));
        }
        Ok(())
    };
    check()?;
    let peer = UnixStream::connect(socket)?;
    let pid = runtime::peer_pid(&peer)?;
    if pin["pid"] != pid || pin["birth"] != runtime::birth(pid)? {
        return Err(invalid("manager bridge peer identity changed"));
    }
    check()
}

fn verify_issuer(directory: &Path, issuer: &Value) -> io::Result<()> {
    let refused = || invalid("enrolled send authority issuer changed");
    let pid = issuer["pid"]
        .as_u64()
        .and_then(|pid| u32::try_from(pid).ok())
        .ok_or_else(refused)?;
    let socket = fs::symlink_metadata(directory.join(SOCKET))?;
    let lease_path = directory.join(LEASE);
    let lease = fs::symlink_metadata(&lease_path)?;
    // SAFETY: geteuid takes no arguments.
    let uid = unsafe { libc::geteuid() };
    if !socket.file_type().is_socket()
        || !lease.is_file()
        || [&socket, &lease]
            .iter()
            .any(|meta| meta.mode() & 0o777 != 0o600 || meta.nlink() != 1 || meta.uid() != uid)
        || issuer["socket"] != json!([socket.dev(), socket.ino()])
        || issuer["lease"] != json!([lease.dev(), lease.ino()])
        || issuer["birth"] != runtime::birth(pid)?
        || !matches!(runtime::lock(&lease_path), Err(err) if err.kind() == io::ErrorKind::WouldBlock)
    {
        return Err(refused());
    }
    Ok(())
}

#[cfg(test)]
pub(in crate::discord) fn replace_fixture_issuer(
    bridge: &Path,
    issuer: &mut Value,
) -> io::Result<()> {
    let Some(root) =
        std::env::var_os("SWARM_CLAUDE_MANAGER_FIXTURE_REPLACED_ISSUER").map(PathBuf::from)
    else {
        return Ok(());
    };
    let withdrawn = root.join(".fixture-enrolled-deleted");
    if withdrawn.exists() {
        return Ok(());
    }
    let directory = bridge.parent().unwrap();
    fs::remove_file(directory.join(SOCKET))?;
    fs::remove_file(directory.join(LEASE))?;
    let mut impostor = std::process::Command::new(std::env::current_exe()?)
        .args([
            "--ignored",
            "--exact",
            "discord::enrolled_worker::authority::replacement_issuer_process",
        ])
        .env("SWARM_FIXTURE_IMPOSTOR_BRIDGE", directory)
        .spawn()?;
    let deadline = Instant::now() + Duration::from_secs(4);
    let forged = loop {
        match runtime::read_json(&root.join(".fixture-impostor-ready")) {
            Ok(pin) => break pin,
            Err(err) if err.kind() == io::ErrorKind::NotFound && Instant::now() < deadline => {
                std::thread::sleep(Duration::from_millis(10));
            }
            Err(err) => {
                let _ = impostor.kill();
                let _ = impostor.wait();
                return Err(err);
            }
        }
    };
    std::thread::spawn(move || {
        let _ = impostor.wait();
    });
    verify_issuer(directory, &forged)?;
    *issuer = forged;
    fs::write(withdrawn, b"source withdrawn after preflight")?;
    Ok(())
}

#[cfg(test)]
#[test]
#[ignore = "private subprocess for the enrolled issuer replacement fixture"]
fn replacement_issuer_process() {
    let root =
        PathBuf::from(std::env::var_os("SWARM_CLAUDE_MANAGER_FIXTURE_REPLACED_ISSUER").unwrap());
    let directory = PathBuf::from(std::env::var_os("SWARM_FIXTURE_IMPOSTOR_BRIDGE").unwrap());
    let lease = runtime::lock(&directory.join(LEASE)).unwrap();
    let listener = runtime::bind(&directory.join(SOCKET)).unwrap();
    listener.set_nonblocking(true).unwrap();
    let socket_meta = fs::symlink_metadata(directory.join(SOCKET)).unwrap();
    let lock_meta = lease.metadata().unwrap();
    let pid = std::process::id();
    runtime::write_json(
        &root.join(".fixture-impostor-ready"),
        &json!({
            "pid":pid,"birth":runtime::birth(pid).unwrap(),
            "socket":[socket_meta.dev(),socket_meta.ino()],
            "lease":[lock_meta.dev(),lock_meta.ino()]
        }),
    )
    .unwrap();
    let deadline = Instant::now() + TIMEOUT;
    while Instant::now() < deadline {
        if let Ok((mut stream, _)) = listener.accept()
            && let Ok(req) = runtime::read_control(&mut DeadlineReader(&stream, deadline))
        {
            fs::write(
                root.join(".fixture-impostor-contacted"),
                b"unexpected authority request",
            )
            .unwrap();
            runtime::write_control(
                &mut stream,
                &json!({"challenge":req["challenge"],"approved":true}),
            )
            .unwrap();
            return;
        }
        std::thread::sleep(Duration::from_millis(10));
    }
}
