mod codex_backend;
mod opencode_backend;
mod pty;
mod screen;
mod supervision;
mod terminal_reply;

use crate::ipc::{field, wire};
use crate::providers::{
    self, agy, agy_input, codex_receipt,
    hermes::{native, transfer},
};
use crate::schedule::worker;
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
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::mpsc::{self, SyncSender};
use std::sync::{Arc, Condvar, Mutex};
use std::thread;
use std::time::{Duration, Instant};

fn prove_codex_relogin(
    plan: &providers::LaunchPlan,
    cwd: &Path,
    runtime_root: &Path,
    thread: &str,
) -> io::Result<()> {
    if plan.agent != "codex"
        || !providers::codex::managed(&plan.version)
        || !codex_receipt::canonical_thread(thread)
        || providers::identity(&plan.executable)? != plan.identity
    {
        return Err(io::Error::other("unqualified Codex relogin access probe"));
    }
    let directory = runtime_root.join(format!(".login-{}", &runtime::random_id()?[..8]));
    use std::os::unix::fs::DirBuilderExt;
    fs::DirBuilder::new().mode(0o700).create(&directory)?;
    let policy = [
        "-c".into(),
        "check_for_update_on_startup=false".into(),
        "-c".into(),
        r#"approval_policy="never""#.into(),
        "-c".into(),
        r#"sandbox_mode="read-only""#.into(),
    ];
    let attempt = codex_backend::start_bounded(
        &plan.executable,
        cwd,
        &directory,
        &plan.env,
        &policy,
        Duration::from_secs(8),
    );
    let (mut backend, mut channel) = attempt.inspect_err(|error| {
        if !error
            .get_ref()
            .is_some_and(|e| e.is::<codex_backend::ContainmentUnknown>())
        {
            let _ = fs::remove_dir_all(&directory);
        }
    })?;
    let proof = channel.prove_saved_thread(thread, cwd);
    drop(channel);
    backend.shutdown()?;
    fs::remove_dir_all(directory)?;
    proof?;
    if providers::identity(&plan.executable)? != plan.identity {
        return Err(io::Error::other(
            "Codex executable changed during relogin access proof",
        ));
    }
    Ok(())
}

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
    provider: String,
    semantic: Option<SyncSender<codex_backend::SemanticAction>>,
    agy_broker: Option<agy_input::Broker>,
    claude_broker: Option<Arc<providers::claude_input::Broker>>,
    schedule: Option<worker::Binding>,
    claude_schedule: Option<String>,
    manager: bool,
    terminal_input: AtomicBool,
    context_guard_busy: AtomicBool,
    context_guard_activity: AtomicU64,
    opencode: Option<Arc<opencode_backend::Backend>>,
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
    agy_status: Option<Value>,
    agy_hook_status: Option<Value>,
    agy_hook_at: Option<Instant>,
    agy_title: Option<Value>,
    agy_latest: u64,
    agy_thread_confirmed: bool,
    claude_paste_attempted: bool,
    claude_dispatch: Option<(String, String)>,
    claude_prompt: Option<String>,
}

struct ScreenState {
    parser: vt100::Parser,
    mouse: crate::terminal_mouse::Tracker,
    subscriber: Option<Subscriber>,
}

struct Subscriber {
    generation: u64,
    output: SyncSender<Vec<u8>>,
    conn: UnixStream,
    replies: Arc<Mutex<terminal_reply::Debt>>,
}

fn serve(dir: &Path) -> io::Result<()> {
    runtime::private_dir(dir)?;
    let config_file = dir.join("session.json");
    let config = runtime::read_json(&config_file)?;
    let token = field(&config, "token");
    let hermes_native =
        config["provider"]["name"] == "hermes" && config["provider"]["mode"] == "shared-native";
    if config["schema"] != (if hermes_native { 2 } else { 1 }) || !runtime::valid_id(token) {
        return Err(io::Error::other("incompatible session reservation"));
    }
    let bootstrap = runtime::read_control(&mut io::stdin().lock())?;
    let schedule = worker::read_binding(&config)?;
    let rollback = config["go_rollback"]["schema"] == 1;
    #[cfg(target_os = "linux")]
    if rollback {
        // Only this dedicated shim can qualify all descendants of its own native process.
        if unsafe { libc::prctl(libc::PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0) } != 0 {
            return Err(io::Error::last_os_error());
        }
    }
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
    let scope = &config["manager"];
    let manager_args = if scope.is_null() {
        Vec::new()
    } else {
        let auth = Path::new(field(&scope["launch"]["manager"], "auth_file"));
        let credential = crate::manager_mcp::read_credential(auth)?;
        use sha2::{Digest, Sha256};
        if scope["credential_hash"] != format!("{:x}", Sha256::digest(credential.as_bytes())) {
            return Err(io::Error::other(
                "manager bridge custody changed before exec",
            ));
        }
        if scope["launch"]["agent"] == "claude" {
            providers::claude::verify_manager_origin(
                scope,
                Path::new(field(&scope["launch"], "cwd")),
            )?;
        } else if scope["account"]
            != crate::supervisor::manager::account(Path::new(field(scope, "account_home")))?
        {
            return Err(io::Error::other(
                "manager account custody changed before exec",
            ));
        }
        crate::supervisor::manager::mcp_args(scope, field(&config, "id"))?
    };
    let provider = &config["provider"];
    let executable = Path::new(field(provider, "executable"));
    let mut argv: Vec<_> = bootstrap["argv"]
        .as_array()
        .and_then(|args| {
            args.iter()
                .map(|value| value.as_str().map(str::to_owned))
                .collect::<Option<_>>()
        })
        .ok_or_else(|| io::Error::other("malformed transient launch arguments"))?;
    let working_dir = if config["checkout"].is_null() {
        field(&config, "cwd")
    } else {
        let path = config["checkout"]["path"]
            .as_str()
            .ok_or_else(|| io::Error::other("missing reserved checkout path"))?;
        crate::workspace::verify(
            Path::new(field(&config, "cwd")),
            field(&config, "id"),
            &config["checkout"],
        )?;
        path
    };
    let agy_acp = provider["name"] == "agy" && provider["mode"] == "acp" && schedule.is_some();
    let mut command = if agy_acp {
        let mut relay = Command::new(std::env::current_exe()?);
        relay.arg("schedule-agy-worker").arg(dir);
        relay
    } else {
        Command::new(executable)
    };
    let mut environment = runtime::launch_env(&Value::Null);
    let owner_socket = field(&bootstrap, "owner_socket");
    if !owner_socket.is_empty() && (!config["manager"].is_null() || !config["mailbox"].is_null()) {
        environment.push(("SWARM_DAEMON_SOCK".into(), owner_socket.into()));
        environment.push(("SWARM_SESSION_ID".into(), field(&config, "id").into()));
        if !config["manager"].is_null() {
            if scope["launch"]["manager"]["native_cli"] == true {
                crate::discord::verify_manager_vm_scope(
                    scope,
                    dir.parent().and_then(Path::parent),
                )?;
                environment.push((
                    "SWARM_DISPATCHER_SPAWN_PROOF".into(),
                    crate::supervisor::mail::proof(token, "spawn"),
                ));
                environment.push(("SWARM_MANAGER_VM_CLI".into(), "1".into()));
                for (key, field) in [
                    ("SWARM_DAEMON_STATE", "owner_root"),
                    ("SWARM_DAEMON_LOCK", "owner_lock"),
                    ("SWARM_DAEMON_LOG", "owner_log"),
                ] {
                    let value = bootstrap[field]
                        .as_str()
                        .filter(|value| Path::new(value).is_absolute())
                        .ok_or_else(|| {
                            io::Error::other("VM manager supervisor environment is unavailable")
                        })?;
                    environment.push((key.into(), value.into()));
                }
            }
        } else if config["mailbox"]["structured"] != true {
            environment.push((crate::teammate::SOCKET_ENV.into(), owner_socket.into()));
            environment.push((
                crate::teammate::PROOF_ENV.into(),
                crate::supervisor::mail::proof(token, "worker"),
            ));
        }
    }
    if provider["name"] == "hermes" {
        environment.extend(native::origins(&Value::Null, hermes_native)?);
    }
    if provider["name"] == "codex" && !config["codex_home"].is_null() {
        use std::os::unix::fs::MetadataExt;
        let home = environment
            .iter()
            .rev()
            .find(|(key, _)| key == "HOME")
            .map_or(Path::new(""), |(_, value)| Path::new(value));
        let native = providers::codex::explicit_home(&Value::Null, home)?
            .ok_or_else(|| io::Error::other("reserved alternate CODEX_HOME is unavailable"))?;
        let info = native.metadata()?;
        if config["codex_home"] != json!({"path":native,"identity":[info.dev(),info.ino()]}) {
            return Err(io::Error::other("alternate CODEX_HOME changed before exec"));
        }
        environment.push(("CODEX_HOME".into(), native.to_string_lossy().into_owned()));
    }
    let mut app_args = vec![
        "-c".into(),
        "sandbox_workspace_write.network_access=true".into(),
    ];
    if let Some(binding) = &schedule {
        let transport = &bootstrap["schedule"];
        let executable = Path::new(field(transport, "binary"));
        let socket = Path::new(field(transport, "socket"));
        environment.extend(
            binding
                .environment(
                    field(transport, "proof"),
                    socket,
                    executable,
                    field(&config, "id"),
                )
                .map_err(|e| io::Error::other(e.to_string()))?,
        );
        if provider["name"] == "codex" {
            app_args = worker::codex_policy(&binding.authority, executable, true)
                .map_err(|e| io::Error::other(e.to_string()))?;
        }
    }
    let claude_schedule = if schedule.is_some() && provider["name"] == "claude" {
        let id = field(&config, "schedule_claude_conversation");
        if !providers::claude::canonical_id(id)
            || !providers::claude::scheduled(field(provider, "version"))
        {
            return Err(io::Error::other("invalid scheduled Claude conversation"));
        }
        argv.extend(["--session-id".into(), id.to_owned()]);
        Some(id.to_owned())
    } else {
        None
    };
    let agy_broker = if schedule.is_none() && provider["agy_native_installation"].is_string() {
        let broker = agy_input::Broker::prepare(
            dir,
            &config,
            Path::new(working_dir),
            &environment,
            field(&bootstrap, "owner_socket"),
        )?;
        broker.launch_args(&mut argv);
        Some(broker)
    } else {
        None
    };
    let claude_broker = if schedule.is_none()
        && (provider["claude_native_installation"].is_string()
            || provider["name"] == "claude" && !scope.is_null())
    {
        let mut flags = Vec::new();
        let broker = providers::claude_input::Broker::prepare(
            dir,
            &config,
            field(&bootstrap, "owner_socket"),
            &mut flags,
        )?;
        argv.splice(..0, flags);
        Some(broker)
    } else {
        None
    };
    command
        .env_clear()
        .envs(environment.iter().map(|(key, value)| (key, value)));
    let agy_args = if !agy_acp && provider["name"] == "agy" && provider["agy_status_line"] == true {
        agy::status_line_args(
            std::env::var_os("HOME").as_deref().map(Path::new),
            &std::env::current_exe()?,
        )
    } else {
        Vec::new()
    };
    let hook_socket = if agy_args.is_empty() {
        socket.clone()
    } else {
        dir.join("h")
    };
    let agy_listener = if agy_args.is_empty() {
        None
    } else {
        Some(runtime::bind(&hook_socket)?)
    };
    let hook_token = if provider["name"] == "claude" || agy_listener.is_some() {
        let hook_token = runtime::random_id()?;
        command
            .env("SWARM_RUST_HOOK_SOCKET", &hook_socket)
            .env("SWARM_HOOK_TOKEN", &hook_token);
        hook_token
    } else {
        String::new()
    };
    command
        .env("TERM", "xterm-256color")
        .env("SWARM_SESSION_ID", field(&config, "id"));
    let (cols, rows) = runtime::dims(&config);
    // A local agent's CLI commands belong to its own supervisor, including private places.
    if config["manager"].is_null()
        && config["schedule"].is_null()
        && let Some(socket) = bootstrap["owner_socket"].as_str()
    {
        let root = dir
            .parent()
            .and_then(Path::parent)
            .ok_or_else(|| io::Error::other("unproven supervisor root"))?;
        command
            .env("SWARM_DAEMON_SOCK", socket)
            .env("SWARM_DAEMON_STATE", root);
    }
    pty::arm_shutdown()?;
    if !scope.is_null() && schedule.is_some() {
        return Err(io::Error::other("conflicting manager and scheduled roles"));
    }
    if !scope.is_null() {
        app_args.extend(manager_args.iter().cloned());
    }
    let mut backend = None;
    let mut channel = None;
    let mut hermes = None;
    let mut hermes_transfer = None;
    let pid = std::process::id();
    let mut viewer = Vec::<String>::new();
    let mut opencode_binding = None;
    let (mut child, master) = (|| {
        if !matches!(
            field(provider, "name"),
            "claude" | "codex" | "agy" | "opencode" | "hermes"
        ) || providers::identity(executable)? != provider["identity"]
        {
            return Err(io::Error::other(
                "provider executable identity changed before exec",
            ));
        }
        command.current_dir(working_dir);
        if scope["launch"]["manager"]["native_cli"] == true {
            crate::discord::verify_manager_vm_scope(scope,dir.parent().and_then(Path::parent))?;
        }
        if provider["name"]=="claude" && config["claude_resume"].is_object() {
            let saved=&config["claude_profile"];
            let live=providers::claude::profile(&environment,&Value::Null,&saved["options"])?;
            let id=field(&config["claude_resume"],"conversation_id");
            let home=Path::new(field(saved,"home"));
            let cwd=Path::new(working_dir);
            providers::claude::verify_project_authority(home,cwd,&config["claude_resume"]["project_authority"])?;
            let (_,transcript)=providers::claude::transcript(home,cwd,id)?;
            if config["claude_resume"]["external_source"].is_object()
                && providers::claude::external_proof(executable,cwd,&environment,home,id,true)?
                    !=config["claude_resume"]["external_source"] {
                return Err(io::Error::other("Claude external rollout changed before exec"));
            }
            if &live!=saved || transcript!=config["claude_resume"]["transcript"]
                || argv.iter().filter(|arg|arg.as_str()=="--resume").count()!=1
                || !argv.ends_with(&["--resume".to_owned(),id.to_owned()])
            {
                return Err(io::Error::other("Claude resume custody changed before exec"));
            }
        }
        if let Some(proof)=config["lineage"]["hands_off"].as_object(){
            use std::os::unix::fs::MetadataExt;
            let get=|key|proof.get(key).and_then(Value::as_str).unwrap_or_default();
            let cwd=Path::new(get("cwd"));
            let home=Path::new(get("home"));
            let authority=proof.get("project_authority").unwrap_or(&Value::Null);
            providers::claude::verify_project_authority(home,cwd,authority)?;
            let (transcript,identity)=providers::claude::transcript(home,cwd,get("conversation"))?;
            let status=cwd.metadata()?;
            if proof.get("identity")!=Some(&identity)
                || transcript!=Path::new(get("transcript"))
                || proof.get("cwd_identity")!=Some(&json!([status.dev(),status.ino()]))
            {
                return Err(io::Error::other("hands-off source custody changed before launch"));
            }
        }
        if provider["name"]=="claude" {
            let cwd=Path::new(working_dir);
            let profile=&config["claude_profile"];
            let home=Path::new(field(profile,"home"));
            if !scope.is_null() {
                providers::claude::verify_manager_origin(scope,cwd)?;
            }
            if let Ok(authority)=providers::claude::project_authority(home,cwd) {
                runtime::write_json(&dir.join("claude-authority.json"),
                    &json!({"token":token,"home":home,"cwd":cwd,"authority":authority}))?;
            } else if !scope.is_null() || config["claude_resume"].is_object() || config["lineage"]["hands_off"].is_object() {
                return Err(io::Error::other("Claude project authority cannot be proved before exec"));
            }
        }
        if provider["name"] == "codex" && provider["mode"] == "managed" {
            use providers::codex_continuity;
            let cwd=Path::new(working_dir);
            let restored=&config["codex_resume"];
            if restored.is_object() && !scope["resume_request"].is_null() {
                return Err(io::Error::other("overlapping manager and owner Codex resume requests"));
            }
            let same_rollout=|profile:&Value| -> io::Result<bool> {
                let home=Path::new(field(profile,"native_home"));
                let thread=field(restored,"thread");
                if restored["external_rollout"].is_object() {
                    Ok(codex_continuity::discover_external(home,true,thread)?
                        .first()==Some(&restored["external_rollout"]))
                } else {
                    Ok(codex_continuity::rollout(home,cwd,thread)?==restored["rollout"])
                }
            };
            let profile=codex_continuity::profile(&environment,cwd,&config["codex_options"]);
            if let Ok(profile)=&profile{
                runtime::write_json(&dir.join("codex-profile.json"),&json!({"token":token,"profile":profile}))?;
            }
            if restored.is_object() {
                let profile=profile?;
                if profile!=restored["profile"] ||
                    !same_rollout(&profile)?
                    || !argv.as_slice().starts_with(&["resume".into(),field(restored,"thread").into()])
                {
                    return Err(io::Error::other("Codex resume profile, rollout or argument custody changed"));
                }
                let sandbox=field(&profile["options"],"sandbox");
                if !sandbox.is_empty(){
                    if !matches!(sandbox,"read-only"|"workspace-write"|"danger-full-access"){
                        return Err(io::Error::other("Codex resume sandbox policy is unqualified"));
                    }
                    app_args.extend(["-c".into(),format!("sandbox_mode={sandbox}")]);
                }
            }
            let (owner, mut connection) = codex_backend::start(
                executable,
                Path::new(working_dir),
                dir,
                &environment,
                &app_args,
            )?;
            backend = Some(owner);
            if let Some(native) = scope["resume_request"]["binding"]["native_session_id"].as_str() {
                if !providers::codex_receipt::canonical_thread(native)
                    || argv.first().map(String::as_str) != Some("resume")
                    || argv.get(1).map(String::as_str) != Some(native)
                {
                    return Err(io::Error::other("manager resume arguments differ from retirement custody"));
                }
                command.args(argv.drain(..2));
            } else if restored.is_object() {
                if codex_continuity::profile(&environment,cwd,&config["codex_options"])?!=restored["profile"] || !same_rollout(&restored["profile"])?{
                    return Err(io::Error::other("Codex resume authority changed during app-server startup"));
                }
                connection.bind_resume(field(restored,"thread"),cwd)?;
            }
            command
                .args(["--remote", &connection.endpoint])
                .env_remove("OPENAI_API_KEY");
            channel = Some(connection);
            if providers::identity(executable)? != provider["identity"] {
                return Err(io::Error::other(
                    "provider executable changed before terminal exec",
                ));
            }
        }
        if provider["name"] == "opencode" {
            let environment: Vec<_> = providers::opencode::environment(&Value::Null)?
                .into_iter()
                .collect();
            if providers::opencode::profile(&environment) != config["opencode_profile"] {
                return Err(io::Error::other(
                    "OpenCode native profile changed before exec",
                ));
            }
            command.envs(environment);
            let initial = &bootstrap["native"];
            let intent = &config["opencode_initial"];
            if (intent.is_null() != field(initial, "text").is_empty())
                || !intent.is_null()
                    && *intent
                        != providers::opencode::initial::intent(
                            field(intent, "operationID"),
                            initial,
                        )
            {
                return Err(io::Error::other(
                    "OpenCode initial input differs from the reservation",
                ));
            }
            let binding = providers::opencode::artifact(
                dir,
                Path::new(working_dir),
                field(&config, "id"),
                !field(&bootstrap["native"], "text").is_empty(),
                &config["opencode_resume"],
            )?;
            command.env("OPENCODE_TUI_CONFIG", dir.join("opencode-tui.json"));
            opencode_binding = Some(binding);
        }
        if hermes_native {
            if bootstrap["hermes"].is_null()
                || !providers::hermes::native_version(field(provider, "version"))
            {
                return Err(io::Error::other("invalid Hermes shared-native launch plan"));
            }
            let plan = &bootstrap["hermes"];
            if !plan["retained"].is_null() {
                hermes_transfer = Some(transfer::start(
                    dir.parent().ok_or_else(|| io::Error::other("missing Hermes session root"))?,
                    field(plan, "predecessor"), field(&config, "id"), token, &plan["retained"],
                )?);
            }
            let cwd = Path::new(working_dir).canonicalize()?;
            let binding = native::prepare(
                executable,
                &cwd,
                field(&config, "id"),
                &plan["options"],
                &environment,
                &plan["retained"],
            )?;
            let origins: std::collections::BTreeMap<_,_> = environment.iter().filter(|(key,_)|
                native::NORMAL_ORIGINS.contains(&key.as_str())).map(|(key,value)| (key,value)).collect();
            let descriptor = json!({"worker_id":config["id"],"token":token,"shim_pid":pid,"origins":origins,
                "shim_birth":runtime::birth(pid)?,"generation":runtime::random_id()?,
                "provider_version":provider["version"],"binding":binding.0});
            runtime::write_json(&dir.join(native::DESCRIPTOR), &descriptor)?;
            viewer.extend([
                "chat".into(),
                "--tui".into(),
                "--resume".into(),
                binding.id().into(),
            ]);
            if !field(plan, "initial_prompt").is_empty() {
                viewer.extend(["-q".into(), field(plan, "initial_prompt").into()]);
            }
            command.env("HERMES_HOME", field(&binding.0, "home"));
            if providers::identity(executable)? != provider["identity"] {
                return Err(io::Error::other(
                    "Hermes executable changed before viewer exec",
                ));
            }
            hermes = Some((binding, descriptor));
            argv.clear();
        }
        command.args(agy_args).args(&manager_args).args(argv).args(&viewer);
        pty::start(command, cols, rows)
    })()
    .inspect_err(|err| {
        drop(hermes_transfer.take());
        let lingering = err
            .get_ref()
            .is_some_and(|e| e.is::<codex_backend::ContainmentUnknown>())
            || backend
                .as_mut()
                .is_some_and(|owned| owned.shutdown().is_err());
        if !lingering {
            let _ = runtime::write_json(
                &dir.join("exit.json"),
                &json!({"error": err.to_string(), "token": token}),
            );
        }
        let _ = fs::remove_file(&socket);
    })?;
    let opencode = match opencode_binding {
        Some(binding) => match opencode_backend::Backend::new(
            binding,
            field(provider, "version"),
            child.id(),
            dir,
            token,
        ) {
            Ok(backend) => Some(Arc::new(backend)),
            Err(err) => {
                let _ = pty::signal_group(&child, libc::SIGKILL);
                let _ = child.wait();
                let _ = runtime::write_json(
                    &dir.join("exit.json"),
                    &json!({"error":err.to_string(),"token":token}),
                );
                let _ = fs::remove_file(&socket);
                return Err(err);
            }
        },
        None => None,
    };
    let worker_pid = child.id();
    if let Err(err) = (|| {
        let mut owner = json!({
            "token": token, "pid": pid, "birth": runtime::birth(pid)?,
            "worker_pid": child.id(),
            "worker_birth": opencode.as_ref().map(|backend| backend.worker_birth()),
        });
        if let Some((_, descriptor)) = &hermes {
            owner["hermes_descriptor_sha256"] = json!(native::descriptor_hash(descriptor));
        }
        runtime::write_json(&dir.join("owner.json"), &owner)
    })() {
        let _ = pty::signal_group(&child, libc::SIGKILL);
        let _ = child.wait();
        let _ = fs::remove_file(&socket);
        return Err(err);
    }
    drop(hermes_transfer);
    let (action_sink, action_receiver) = mpsc::sync_channel(1);
    let state = Arc::new(State {
        token: token.into(),
        hook_token,
        opencode: opencode.clone(),
        provider: field(provider, "name").to_owned(),
        semantic: channel.is_some().then_some(action_sink),
        agy_broker,
        claude_broker,
        schedule: schedule.clone(),
        claude_schedule,
        manager: !scope.is_null(),
        terminal_input: AtomicBool::new(false),
        context_guard_busy: AtomicBool::new(false),
        context_guard_activity: AtomicU64::new(0),
        dir: dir.to_owned(),
        working_dir: PathBuf::from(working_dir),
        observations: Mutex::new(ProviderObservation {
            conversation: config["agy_launch"]["options"]["resume_conversation_id"]
                .as_str()
                .or_else(|| config["claude_resume"]["conversation_id"].as_str())
                .map(str::to_owned),
            ..ProviderObservation::default()
        }),
        master,
        screen: Mutex::new(ScreenState {
            parser: vt100::Parser::new(rows.try_into().unwrap(), cols.try_into().unwrap(), 0),
            mouse: crate::terminal_mouse::Tracker::new(crate::terminal_mouse::Modes::at_reset()),
            subscriber: None,
        }),
        control: Mutex::new(0),
        generation: AtomicU64::new(1),
        child: Mutex::new(Some(child)),
        terminate_at: Mutex::new(None),
    });
    let reader = state.clone();
    let pending = (!agy_acp
        && matches!(state.provider.as_str(), "agy" | "codex" | "hermes")
        && channel.is_none()
        && !hermes_native)
        .then(|| Arc::new((Mutex::new(None), Condvar::new())));
    if let Some(pending) = pending.clone() {
        let state = state.clone();
        thread::spawn(move || terminal_status_writer(&state, &pending));
    }
    if let Some((binding, _)) = &hermes {
        let state = state.clone();
        let binding = binding.clone();
        thread::spawn(move || {
            let mut last = Value::Null;
            loop {
                let status = binding.inspect().unwrap_or_else(|_| json!({"Process":"running","Turn":"unknown","Interaction":"error","Attention":true}));
                if status != last
                    && runtime::write_json(
                        &state.dir.join("observation.json"),
                        &json!({
                            "token":state.token,"conversation_id":binding.id(),"status":status
                        }),
                    )
                    .is_ok()
                {
                    last = status;
                }
                thread::sleep(Duration::from_secs(2));
            }
        });
    }
    if let Some(channel) = channel {
        let mut manager = scope.clone();
        if !manager.is_null() {
            manager["session_id"] = config["id"].clone();
        }
        let guard_version = (schedule.is_none()
            && manager.is_null()
            && config["worker_discussion"].is_null()
            && field(&config, "role").is_empty())
        .then(|| field(provider, "version").to_owned());
        let backend_state = state.clone();
        thread::spawn(move || {
            codex_backend::observe(
                channel,
                backend_state,
                action_receiver,
                manager,
                guard_version,
            )
        });
    }
    if let Some(opencode) = opencode {
        let initial = bootstrap["native"].clone();
        let intent = config["opencode_initial"].clone();
        thread::spawn(move || opencode.run(&initial, &intent));
    }
    let (replies, replies_in) = mpsc::sync_channel::<String>(256);
    let reply_owner = state.clone();
    thread::spawn(move || {
        for reply in replies_in {
            let _owner = reply_owner.control.lock().unwrap();
            let _ = pty::input(&reply_owner.master, reply.as_bytes());
        }
    });
    let (drained, drain_done) = mpsc::sync_channel(1);
    let output = thread::spawn(move || {
        drain(reader, pending, replies);
        let _ = drained.send(());
    });
    let shared = state.clone();
    thread::spawn(move || accept(listener, shared));
    if let Some(listener) = agy_listener {
        let state = state.clone();
        thread::spawn(move || {
            // Keep acceptance and application in order even if callers time out waiting for a reply.
            for mut stream in listener.incoming().flatten() {
                let _ = receive_agy_hook(&mut stream, &state);
            }
        });
    }
    let mut diagnostic_log = crate::ipc::env_path("SWARM_DAEMON_LOG")
        .and_then(|path| crate::clean::DiagnosticLog::from_stderr(&path).ok());
    let status = loop {
        if let Some(log) = &mut diagnostic_log {
            log.tick();
        }
        if pty::SHUTDOWN_SIGNAL.load(Ordering::Relaxed) {
            terminate(&state).map_err(|e| io::Error::other(format!("terminate provider: {e}")))?;
        }
        let _native = state
            .agy_broker
            .as_ref()
            .map(|broker| broker.joined.lock().unwrap());
        let mut active = state.child.lock().unwrap();
        let terminate_at = state.terminate_at.lock().unwrap();
        let child = active.as_mut().expect("worker owned while serving");
        let exited = pty::child_exited(child)
            .map_err(|e| io::Error::other(format!("observe provider exit: {e}")))?;
        let kill_group = match *terminate_at {
            // Keep an exited leader unreaped while its descendants grace their TERM.
            Some(at) => Instant::now() >= at,
            None => exited,
        };
        if kill_group {
            pty::signal_group(child, libc::SIGKILL)
                .map_err(|e| io::Error::other(format!("contain provider process group: {e}")))?;
            break active
                .take()
                .expect("worker owned during shutdown")
                .wait()
                .map_err(|e| io::Error::other(format!("join provider process: {e}")))?;
        }
        drop((active, terminate_at, _native));
        thread::sleep(Duration::from_millis(40));
    };
    if let Some(mut owned) = backend {
        owned.shutdown()?;
    }
    if state.provider == "agy" {
        let _ = drain_done.recv_timeout(Duration::from_millis(150));
    }
    if let Some((binding, descriptor)) = hermes
        && runtime::read_json(&dir.join("owner.json"))
            .and_then(|owner| native::read_descriptor(dir, &owner, &config))
            .is_ok_and(|stored| stored == descriptor)
    {
        let stopped = runtime::read_json(&dir.join("hermes-stop.json"))
            .is_ok_and(|record| record == descriptor)
            && binding.stop().is_ok();
        let _ = binding.detach(stopped);
    }
    let mut exit = json!({"token":token, "code":status.code(), "signal":status.signal()});
    if rollback {
        exit["finished_at"] = json!(runtime::timestamp_now());
    }
    runtime::write_json(&dir.join("exit.json"), &exit)?;
    let _ = fs::remove_file(socket);
    if rollback {
        // Keep the ordinary Rust exit report even if containment is uncertain.
        let containment = stopped_rollback_containment(worker_pid)?;
        runtime::write_json(
            &dir.join("go-stop-proof.json"),
            &json!({
                "token":token, "finished_at":exit["finished_at"], "containment":containment,
            }),
        )?;
    }
    if state.provider == "agy" {
        let _ = fs::remove_file(dir.join("h"));
    }
    // Do not wait for a provider-owned descendant that holds the slave open.
    drop(output);
    Ok(())
}

#[cfg(target_os = "linux")]
fn stopped_rollback_containment(_: u32) -> io::Result<&'static str> {
    let deadline = Instant::now() + Duration::from_secs(2);
    loop {
        // The PTY child is already joined. ECHILD establishes that this subreaper
        // has no remaining owned children, including former detached descendants.
        let pid = unsafe { libc::waitpid(-1, std::ptr::null_mut(), libc::WNOHANG) };
        if pid == -1 {
            let err = io::Error::last_os_error();
            if err.raw_os_error() == Some(libc::ECHILD) {
                return Ok("linux-subreaper");
            }
            if err.kind() != io::ErrorKind::Interrupted {
                return Err(err);
            }
        }
        if Instant::now() >= deadline {
            return Err(io::Error::other(
                "cannot prove all Claude descendants have stopped",
            ));
        }
        if pid <= 0 {
            thread::sleep(Duration::from_millis(20));
        }
    }
}

#[cfg(target_os = "macos")]
fn stopped_rollback_containment(worker_pid: u32) -> io::Result<&'static str> {
    // Signal zero observes the group without mutating any process.
    if unsafe { libc::kill(-(worker_pid as i32), 0) } == -1
        && io::Error::last_os_error().raw_os_error() == Some(libc::ESRCH)
    {
        Ok("darwin-process-group")
    } else {
        Err(io::Error::other(
            "cannot prove the Claude process group has stopped",
        ))
    }
}

fn observation_is_quiescent(
    observed: &Value,
    token: &str,
    binding: &Value,
    native: &Value,
) -> bool {
    observed["token"] == token
        && observed["backend_available"] == true
        && observed["conversation_id"] == binding["native_session_id"]
        && observed["manager_ingress_ready"] == true
        && native["thread_state"] == "idle"
        && native["blocked"] == false
}

fn terminate(state: &State) -> io::Result<()> {
    let _native = state
        .agy_broker
        .as_ref()
        .map(|broker| broker.joined.lock().unwrap());
    let active = state.child.lock().unwrap();
    let mut deadline = state.terminate_at.lock().unwrap();
    if let Some(child) = active.as_ref().filter(|_| deadline.is_none()) {
        pty::signal_group(child, libc::SIGTERM)?;
        *deadline = Some(Instant::now() + Duration::from_secs(2));
    }
    Ok(())
}

type PendingStatus = (Mutex<Option<Value>>, Condvar);

fn terminal_status_writer(state: &State, pending: &PendingStatus) {
    loop {
        let mut slot = pending.0.lock().unwrap();
        while slot.is_none() {
            slot = pending.1.wait(slot).unwrap();
        }
        let status = slot.take().expect("notification carries a status");
        drop(slot);
        if state.provider == "agy" {
            let mut observed = state.observations.lock().unwrap();
            let can_fallback = observed.agy_hook_status.as_ref().is_none_or(|hook| {
                hook["Interaction"] != "error"
                    && observed
                        .agy_hook_at
                        .is_some_and(|at| at.elapsed() >= Duration::from_secs(30))
            });
            if can_fallback
                && (observed.agy_hook_status.take().is_some()
                    || observed.agy_status.as_ref() != Some(&status))
            {
                observed.agy_status = Some(status);
                let _ = write_agy_observation(state, &observed);
            }
            continue;
        }
        let _ = runtime::write_json(
            &state.dir.join("observation.json"),
            &json!({
                "token": state.token, "status": status,
                "conversation_id": state.observations.lock().unwrap().conversation,
            }),
        );
    }
}

fn drain(state: Arc<State>, pending: Option<Arc<PendingStatus>>, replies: SyncSender<String>) {
    let mut bytes = [0; 8192];
    let mut master = &state.master;
    let mut observed = None;
    let mut last_emit = Instant::now();
    let mut agy_tail = Vec::new();
    let mut classic_tail = Vec::new();
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
        if state.provider == "agy" && state.schedule.is_none() {
            const TAIL: usize = 16 * 1024;
            agy_tail.extend_from_slice(&bytes[..n]);
            if agy_tail.len() > TAIL {
                agy_tail.drain(..agy_tail.len() - TAIL);
            }
        }
        let mut screen = state.screen.lock().unwrap();
        screen.parser.process(&bytes[..n]);
        // The screen emulator collapses conflicting DEC mouse modes to a single
        // effective mode. The attach controller also needs the full native mask.
        let subscriber = screen.subscriber.as_ref().map(|sub| sub.replies.clone());
        let _ = screen.mouse.feed(&bytes[..n], None, |_, change, _| {
            if let Some((mode, status)) = change.report
                && replies.try_send(format!("\x1b[?{mode};{status}$y")).is_ok()
                && let Some(subscriber) = &subscriber
            {
                // Register before exposing the triggering query to this attachment.
                subscriber.lock().unwrap().register(mode, Instant::now());
            }
            Ok(())
        });
        if let Some(sub) = &screen.subscriber
            && sub.output.try_send(bytes[..n].to_vec()).is_err()
        {
            let _ = sub.conn.shutdown(Shutdown::Both);
            screen.subscriber = None;
        }
        let mut identity_changed = false;
        let status = pending.as_ref().and_then(|_| {
            let screen = screen.parser.screen();
            if state.provider == "agy" {
                return agy::terminal_status(screen);
            }
            if state.provider != "hermes" {
                return providers::codex::terminal_status(screen);
            }
            let (_, width) = screen.size();
            let width = width.get();
            let rows: Vec<_> = screen.rows(0, width).collect();
            let mut observation = state.observations.lock().unwrap();
            if observation.conversation.is_none() {
                let bytes = &bytes[..n];
                let remove = (classic_tail.len() + bytes.len()).saturating_sub(64 * 1024);
                classic_tail.drain(..remove);
                classic_tail.extend_from_slice(bytes);
                observation.conversation =
                    providers::hermes::classic_observation(&classic_tail, &rows);
                identity_changed = observation.conversation.is_some();
                if identity_changed {
                    classic_tail = Vec::new();
                }
            }
            providers::hermes::grid(&rows, width)
        });
        drop(screen);
        if let Some(pending) = &pending
            && let Some(status) = if identity_changed {
                status.or_else(|| observed.clone()).or_else(|| {
                    Some(json!({"Process":"running", "Turn":"unknown","Interaction":"none"}))
                })
            } else {
                status.filter(|status| {
                    observed.as_ref() != Some(status)
                        || (state.provider == "agy"
                            && last_emit.elapsed() >= Duration::from_secs(1))
                })
            }
        {
            last_emit = Instant::now();
            observed = Some(status.clone());
            *pending.0.lock().unwrap() = Some(status);
            pending.1.notify_one();
        }
    }
    if state.provider == "agy" && state.schedule.is_none() {
        let grid = state.screen.lock().unwrap().parser.screen().contents() + "\n";
        if let Some(id) =
            agy::exit_conversation(&agy_tail).or_else(|| agy::exit_conversation(grid.as_bytes()))
        {
            let mut observed = state.observations.lock().unwrap();
            if observed.conversation.is_none() {
                observed.conversation = Some(id.into());
                let _ = write_agy_observation(&state, &observed);
            }
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

fn claude_manager_has_empty_composer(state: &State) -> bool {
    let terminal = state.control.lock().unwrap();
    *terminal == 0
        && providers::claude::manager_empty_composer(state.screen.lock().unwrap().parser.screen())
}

fn connection(mut stream: UnixStream, state: &State) -> io::Result<()> {
    stream.set_read_timeout(Some(runtime::IO_TIMEOUT))?;
    stream.set_write_timeout(Some(runtime::IO_TIMEOUT))?;
    let hello = runtime::read_control(&mut stream)?;
    if hello["type"] == "agy_register" {
        stream.set_write_timeout(Some(Duration::from_secs(2)))?;
        return register_agy(&mut stream, state, &hello);
    }
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
        "attach" => {
            opencode_terminal_ready(state)?;
            attach(stream, state)
        }
        "manager_park" if state.manager => {
            let result = (|| {
                let owner = state.control.lock().unwrap();
                if *owner != 0 {
                    return Err(io::Error::other("manager is attached"));
                }
                let request = &request["retire"];
                let binding = &request["binding"];
                let meta = runtime::read_json(&state.dir.join("session.json"))?;
                let intent = crate::discord::connector_intent(&meta["manager"])?;
                if intent["manager"]["retirement"]["request"] != *request
                    || !intent["manager"]["input"].is_null()
                    || intent["manager"]["session"] != binding["session"]
                {
                    return Err(io::Error::other(
                        "manager park lacks persisted connector custody",
                    ));
                }
                let (native, quiescent) = if state.provider == "claude" {
                    let broker = state
                        .claude_broker
                        .as_ref()
                        .ok_or_else(|| io::Error::other("Claude manager broker is unavailable"))?;
                    let native = broker.manager_snapshot()?;
                    let quiescent = native["binding"] == *binding
                        && native["thread_state"] == "idle"
                        && native["externally_steered"] != true
                        && providers::claude::manager_empty_composer(
                            state.screen.lock().unwrap().parser.screen(),
                        );
                    (native, quiescent)
                } else {
                    let observed = runtime::read_json(&state.dir.join("observation.json"))?;
                    let native = providers::codex_proof::Ledger::snapshot(&state.dir, binding)?;
                    let quiescent =
                        observation_is_quiescent(&observed, &state.token, binding, &native);
                    (native, quiescent)
                };
                let turn = providers::codex_proof::input_status(
                    &native,
                    field(&request["expected_turn"], "native_input_id"),
                );
                if quiescent
                    && native["current"] == turn["native_turn_id"]
                    && turn == request["expected_turn"]
                {
                    runtime::write_json(
                        &state.dir.join("manager-retire.json"),
                        &json!({
                            "token":state.token,"request":request
                        }),
                    )?;
                    terminate(state)?;
                    Ok(())
                } else {
                    Err(io::Error::other(
                        "manager native turn is no longer quiescent",
                    ))
                }
            })();
            runtime::write_control(
                &mut stream,
                &match result {
                    Ok(()) => json!({"type":"ok"}),
                    Err(error) => json!({"type":"error","error":error.to_string()}),
                },
            )
        }
        "maintenance_probe" => {
            let result = maintenance_probe(state, &request);
            let reply = match result {
                Ok(()) => json!({"type":"ok"}),
                Err(error) => json!({"type":"error","error":error.to_string()}),
            };
            runtime::write_control(&mut stream, &reply)
        }
        "maintenance_stop" => {
            let result = maintenance_stop(state, &request);
            let reply = match result {
                Ok(()) => json!({"type":"ok"}),
                Err(err) if err.kind() == io::ErrorKind::WouldBlock => json!({"type":"busy"}),
                Err(err) => json!({"type":"error","error":err.to_string()}),
            };
            runtime::write_control(&mut stream, &reply)
        }
        "signal" if request["sig"] == "term" => {
            terminate(state)?;
            runtime::write_control(&mut stream, &json!({"type": "ok"}))
        }
        "agy_ready" => {
            let result = agy_ready(state);
            runtime::write_control(
                &mut stream,
                &result.unwrap_or_else(|e| json!({"type":"error", "error":e.to_string()})),
            )
        }
        "semantic" if state.provider == "agy" => {
            let result = agy_send(state, &request);
            let reply = match result {
                Ok(receipt) => json!({"type":"semantic_result", "receipt":receipt}),
                Err(e) => json!({"type":"error", "error":e.to_string()}),
            };
            runtime::write_control(&mut stream, &reply)
        }
        "claude_manager_proof" if state.manager && state.provider == "claude" => {
            let result = state
                .claude_broker
                .as_ref()
                .ok_or_else(|| io::Error::other("Claude manager broker is unavailable"))
                .and_then(|broker| broker.manager_snapshot());
            let reply = result.map_or_else(
                |error| json!({"type":"error","error":error.to_string()}),
                |mut evidence| {
                    evidence["owned_ingress_ready"] =
                        json!(claude_manager_has_empty_composer(state));
                    json!({"type":"ok","evidence":evidence})
                },
            );
            runtime::write_control(&mut stream, &reply)
        }
        "claude_ready" => {
            let ready = state
                .claude_broker
                .as_ref()
                .and_then(|broker| broker.ready().ok());
            runtime::write_control(
                &mut stream,
                &ready.map_or_else(
                    || json!({"type":"error"}),
                    |conversation| json!({"type":"ok","conversation_id":conversation}),
                ),
            )
        }
        "semantic" if state.provider == "claude" => {
            let terminal = state.manager.then(|| state.control.lock().unwrap());
            if terminal.as_ref().is_some_and(|owner| {
                **owner != 0
                    || !providers::claude::manager_empty_composer(
                        state.screen.lock().unwrap().parser.screen(),
                    )
            }) {
                return runtime::write_control(
                    &mut stream,
                    &json!({"type":"error","error":"Claude manager terminal has a draft, attachment or native dialog"}),
                );
            }
            let result = state
                .claude_broker
                .as_ref()
                .ok_or_else(|| io::Error::other("Claude native input broker is unavailable"))
                .and_then(|broker| broker.submit(&request));
            runtime::write_control(
                &mut stream,
                &result.map_or_else(
                    |error| json!({"type":"error","error":error.to_string()}),
                    |receipt| json!({"type":"semantic_result","receipt":receipt}),
                ),
            )
        }
        "schedule_agy_prompt" => {
            let result = queue_agy_prompt(state, &request);
            runtime::write_control(
                &mut stream,
                &result.map_or_else(
                    |err| json!({"type":"error","error":err.to_string()}),
                    |()| json!({"type":"ok"}),
                ),
            )
        }
        "schedule_claude_prompt" => {
            let result = queue_claude_prompt(state, &request);
            runtime::write_control(
                &mut stream,
                &result.map_or_else(
                    |err| json!({"type":"error","error":err.to_string()}),
                    |()| json!({"type":"ok"}),
                ),
            )
        }
        "schedule_claude_attention"
            if state.claude_schedule.is_some()
                && state
                    .schedule
                    .as_ref()
                    .is_some_and(|b| b.attempt_id == request["attempt"]) =>
        {
            let attention =
                providers::claude::startup_attention(state.screen.lock().unwrap().parser.screen());
            runtime::write_control(&mut stream, &json!({"type":"ok","attention":attention}))
        }
        "schedule_ready" => {
            let authority = state.control.lock().unwrap();
            let ready = state
                .schedule
                .as_ref()
                .is_some_and(|binding| request["attempt"] == binding.attempt_id)
                && *authority == 0
                && !state.terminal_input.load(Ordering::Acquire)
                && (state.claude_schedule.is_none() || claude_composer(state));
            runtime::write_control(
                &mut stream,
                &json!({"type": if ready {"ok"} else {"error"}, "conversation_id":state.claude_schedule}),
            )
        }
        "opencode_requests" | "opencode_operation" | "opencode_history" => {
            let result = (|| {
                stream.set_write_timeout(Some(Duration::from_secs(5)))?;
                let native = state
                    .opencode
                    .as_ref()
                    .filter(|native| native.ready.load(Ordering::Acquire))
                    .ok_or_else(|| io::Error::other("OpenCode native binding is unavailable"))?;
                let session = field(&request, "session");
                match field(&request, "type") {
                    "opencode_requests" => native.requests(
                        session,
                        field(&request, "request_id"),
                        field(&request, "native_session_id"),
                    ),
                    "opencode_history" => native.history(
                        request["limit"].as_u64().unwrap_or(50),
                        field(&request, "before"),
                    ),
                    _ => {
                        let params = (!request["body"].is_null()).then_some(&request["body"]);
                        let guard = if params.is_some() {
                            let guard = state.control.try_lock().map_err(|_| {
                                io::Error::other("OpenCode terminal input is active")
                            })?;
                            if *guard != 0 {
                                return Err(io::Error::other("OpenCode terminal is attached"));
                            }
                            Some(guard)
                        } else {
                            None
                        };
                        let result = native.operation(
                            session,
                            field(&request, "operation_id"),
                            field(&request, "kind"),
                            params,
                        );
                        drop(guard);
                        Ok(result?.unwrap_or(Value::Null))
                    }
                }
            })();
            let response = match result {
                Ok(body) if serde_json::to_vec(&body)?.len() < (1 << 20) - 4096 => {
                    json!({"type":"opencode_result","body":body})
                }
                Ok(_) => {
                    json!({"type":"error","error":"OpenCode native response exceeds the IPC limit; request a smaller page"})
                }
                Err(error) => json!({"type":"error","error":error.to_string()}),
            };
            runtime::write_control(&mut stream, &response)
        }
        "semantic" => {
            let operation = field(&request, "operation_id");
            let text = field(&request, "text");
            let session = field(&request, "session");
            let body = (|| {
                let scheduled = request["schedule_attempt"].as_str();
                if scheduled.is_some_and(|attempt| {
                    state
                        .schedule
                        .as_ref()
                        .is_none_or(|b| b.attempt_id != attempt)
                }) || field(&request, "session_instance")
                    != codex_receipt::instance(&state.token)
                    || !codex_receipt::timely(operation)
                    || session.is_empty()
                    || text.is_empty()
                    || !worker::prompt_fits(text, scheduled.is_some())
                    || state.manager != !request["manager_admission"].is_null()
                {
                    return Err(io::Error::other("invalid or stale Codex semantic request"));
                }
                let _authority = state.control.lock().unwrap();
                // shortcut: terminal edits make draft state uncertain until the native TUI exposes a verified empty composer.
                if *_authority != 0
                    || state.context_guard_busy.load(Ordering::Acquire)
                    || state.terminal_input.load(Ordering::Acquire)
                {
                    return Err(io::Error::other("Codex terminal draft is not proven empty"));
                }
                let sender = state
                    .semantic
                    .as_ref()
                    .ok_or_else(|| io::Error::other("Codex app-server is unavailable"))?;
                let (answer, response) = mpsc::sync_channel(1);
                sender
                    .try_send(codex_backend::SemanticAction {
                        operation: operation.into(),
                        session: session.into(),
                        text: text.into(),
                        schedule_attempt: scheduled.map(str::to_owned),
                        composer: request["composer_send"].clone(),
                        enrolled: request["enrolled_worker_send"].clone(),
                        answer,
                        not_after: Instant::now()
                            + if request["enrolled_worker_send"].is_null() {
                                Duration::from_secs(2)
                            } else {
                                Duration::from_secs(9)
                            },
                    })
                    .map_err(|_| io::Error::other("Codex native input lane is unavailable"))?;
                response
                    .recv_timeout(Duration::from_secs(
                        if request["enrolled_worker_send"].is_null() {
                            6
                        } else {
                            14
                        },
                    ))
                    .map_err(|_| io::Error::other("Codex semantic submission outcome is unknown"))?
            })();
            let response = match body {
                Ok(body) => json!({"type":"semantic_result", "receipt":body}),
                Err(err) => json!({"type":"error", "error":err.to_string()}),
            };
            runtime::write_control(&mut stream, &response)
        }
        "supervision_input" => {
            let response = supervision::notify(state, &request)
                .unwrap_or_else(|e| json!({"type":"error","error":e.to_string()}));
            runtime::write_control(&mut stream, &response)
        }
        "input" => {
            if state.manager {
                return Err(io::Error::other(
                    "terminal input requires an explicit manager custody transition",
                ));
            }
            opencode_terminal_ready(state)?;
            let text = field(&request, "data");
            if text.is_empty() || text.len() > 4096 {
                return Err(io::Error::other("invalid shim input payload"));
            }
            let _guard = state.control.lock().unwrap();
            if state.context_guard_busy.load(Ordering::Acquire) {
                return runtime::write_control(
                    &mut stream,
                    &json!({"type":"error","error":"Codex Context Guard has a native action active"}),
                );
            }
            state.terminal_input.store(true, Ordering::Release);
            if let Err(e) = write_input(
                state,
                text,
                request["paste"] == true,
                request["submit"] == true,
            ) {
                return runtime::write_control(
                    &mut stream,
                    &json!({"type":"error","error":e.to_string()}),
                );
            }
            runtime::write_control(&mut stream, &json!({"type": "ok"}))
        }
        _ => Err(io::Error::other("unsupported shim operation")),
    }
}

fn opencode_terminal_ready(state: &State) -> io::Result<()> {
    if state
        .opencode
        .as_ref()
        .is_some_and(|native| !native.ready.load(Ordering::Acquire))
    {
        return Err(io::Error::other(
            "OpenCode native launch or initial input remains unresolved",
        ));
    }
    Ok(())
}

fn maintenance_probe(state: &State, request: &Value) -> io::Result<()> {
    let thread = field(request, "conversation_id");
    let meta = runtime::read_json(&state.dir.join("session.json"))?;
    let source = runtime::read_json(&state.dir.join("observation.json"))?;
    let saved = &meta["maintenance_env"];
    if state.provider != "codex"
        || state.schedule.is_some()
        || !meta["manager"].is_null()
        || !meta["checkout"].is_null()
        || source["token"] != state.token
        || source["conversation_id"] != thread
        || !codex_receipt::canonical_thread(thread)
        || !request["account"].is_string()
        || json!(crate::refresh::auth_identity("codex", saved)) != request["account"]
    {
        return Err(io::Error::other(
            "original native account context is unproved",
        ));
    }
    let args = json!({"env":saved});
    let target = providers::prepare(
        "codex",
        &state.working_dir,
        "",
        &args,
        runtime::launch_env(saved),
    )?;
    if target.version != field(request, "version")
        || target.identity != request["identity"]
        || json!(target.executable) != request["executable"]
    {
        return Err(io::Error::other(
            "Codex executable changed before native access probe",
        ));
    }
    let root = state
        .dir
        .parent()
        .ok_or_else(|| io::Error::other("missing native runtime root"))?;
    prove_codex_relogin(&target, &state.working_dir, root, thread)?;
    if json!(crate::refresh::auth_identity("codex", saved)) != request["account"] {
        return Err(io::Error::other(
            "Codex account changed during native access probe",
        ));
    }
    Ok(())
}

fn maintenance_stop(state: &State, request: &Value) -> io::Result<()> {
    let nonce = field(request, "nonce");
    let thread = field(request, "conversation_id");
    let busy = || {
        io::Error::new(
            io::ErrorKind::WouldBlock,
            "worker is active or has unresolved input",
        )
    };
    let root = state
        .dir
        .parent()
        .and_then(Path::parent)
        .ok_or_else(|| io::Error::other("missing maintenance state root"))?;
    let _policy = runtime::lock(&root.join(".native-maintenance-policy.lock"))?;
    if let Some(name) = request["automatic_policy"].as_str()
        && (!matches!(name, crate::refresh::AUTH | crate::refresh::POLICY)
            || crate::refresh::disabled(root, name)?)
    {
        return Err(busy());
    }
    let _control = state.control.lock().unwrap();
    if *_control != 0
        || state.terminal_input.load(Ordering::Acquire)
        || state.context_guard_busy.load(Ordering::Acquire)
    {
        return Err(busy());
    }
    if !runtime::valid_id(nonce)
        || !codex_receipt::canonical_thread(thread)
        || state.schedule.is_some()
        || !matches!(state.provider.as_str(), "codex" | "claude" | "agy")
    {
        return Err(io::Error::other("unsupported maintenance stop binding"));
    }
    let observation = runtime::read_json(&state.dir.join("observation.json"))?;
    let status = &observation["status"];
    if observation["token"] != state.token
        || observation["conversation_id"] != thread
        || status["Process"] != "running"
        || status["Turn"] != "idle"
        || status["Interaction"] != "none"
        || status["Attention"] == true
        || state.provider == "codex" && observation["manager_ingress_ready"] != true
    {
        return Err(busy());
    }
    let journal = crate::refresh::records(root)?;
    let source = state
        .dir
        .file_name()
        .and_then(|name| name.to_str())
        .unwrap_or_default();
    let claim = &journal["records"][source];
    let target = Path::new(field(claim, "executable"));
    if claim["state"] != "claimed"
        || claim["nonce"] != nonce
        || claim["conversation_id"] != thread
        || claim["policy"] != request["automatic_policy"]
        || claim["agent"] != state.provider
        || !target.is_absolute()
        || providers::identity(target)? != claim["identity"]
        || json!(crate::refresh::auth_identity(
            &state.provider,
            &claim["source_env"]
        )) != claim["account"]
    {
        return Err(io::Error::other(
            "maintenance claim no longer matches the approved target",
        ));
    }
    if !claim["account_proof"].is_null() {
        let proof = providers::codex_continuity::relogin_proof(
            &state.dir,
            &state.token,
            &state.working_dir,
            &runtime::launch_env(&claim["source_env"]),
            thread,
        )?;
        if proof != claim["account_proof"] {
            return Err(io::Error::other(
                "native rollout or effective authority changed before stop",
            ));
        }
    }
    let path = state.dir.join("maintenance-stop.json");
    match runtime::read_json(&path) {
        Ok(proof)
            if proof["token"] == state.token
                && proof["nonce"] == nonce
                && proof["conversation_id"] == thread => {}
        Err(err) if err.kind() == io::ErrorKind::NotFound => runtime::write_json(
            &path,
            &json!({
                "token":state.token,"nonce":nonce,"conversation_id":thread,
            }),
        )?,
        _ => {
            return Err(io::Error::other(
                "conflicting durable maintenance stop proof",
            ));
        }
    }
    terminate(state)
}

fn queue_agy_prompt(state: &State, request: &Value) -> io::Result<()> {
    let authority = state.control.lock().unwrap();
    let attempt = field(request, "attempt");
    let conversation = field(request, "conversation_id");
    let operation = field(request, "operation");
    let text = field(request, "text");
    if state.provider != "agy"
        || state
            .schedule
            .as_ref()
            .is_none_or(|binding| binding.attempt_id != attempt)
        || *authority != 0
        || state.terminal_input.load(Ordering::Acquire)
        || !codex_receipt::canonical_thread(conversation)
        || !codex_receipt::timely(operation)
        || text.is_empty()
        || !worker::prompt_fits(text, true)
    {
        return Err(io::Error::other(
            "Antigravity scheduled prompt is not authorized",
        ));
    }
    agy_worker_live(state)?;
    let observation = runtime::read_json(&state.dir.join("observation.json"))?;
    let prompt = runtime::read_json(&state.dir.join("schedule-prompt.json"))?;
    let path = state.dir.join("agy-prompt.json");
    if observation["token"] != state.token
        || observation["attempt"] != attempt
        || observation["phase"] != "ready"
        || observation["conversation_id"] != conversation
        || prompt["operation"] != operation
        || prompt["attempt"] != attempt
        || prompt["conversation_id"] != conversation
        || path.try_exists()?
    {
        return Err(io::Error::other(
            "Antigravity scheduled worker is no longer ready",
        ));
    }
    OpenOptions::new()
        .write(true)
        .create_new(true)
        .mode(0o600)
        .open(state.dir.join("agy-prompt-claimed"))?
        .sync_all()?;
    File::open(&state.dir)?.sync_all()?;
    runtime::write_json(
        &path,
        &json!({
            "token":state.token,"attempt":attempt,"operation":operation,
            "conversation_id":conversation,"text":text,
        }),
    )
}

fn agy_worker_live(state: &State) -> io::Result<()> {
    let worker = state.child.lock().unwrap();
    if let Some(worker) = worker.as_ref()
        && state.terminate_at.lock().unwrap().is_none()
        && !pty::child_exited(worker)?
    {
        return Ok(());
    }
    Err(io::Error::other("Antigravity worker is stopping"))
}

fn register_agy(stream: &mut UnixStream, state: &State, request: &Value) -> io::Result<()> {
    let broker = state
        .agy_broker
        .as_ref()
        .ok_or_else(|| io::Error::other("Antigravity native input is disabled"))?;
    let mut joined = broker.joined.lock().unwrap();
    agy_worker_live(state)?;
    let source = broker.validate_registration(request)?;
    agy_worker_live(state)?;
    if joined.as_ref().is_some_and(|current| current != &source) {
        return Err(io::Error::other(
            "Antigravity registration changed after joining",
        ));
    }
    let mut observed = state.observations.lock().unwrap();
    let id = source.conversation();
    if observed
        .conversation
        .as_deref()
        .is_some_and(|current| current != id)
    {
        return Err(io::Error::other(
            "Antigravity registration does not match the observed conversation",
        ));
    }
    observed.conversation = Some(id.to_owned());
    write_agy_observation(state, &observed)?;
    *joined = Some(source);
    runtime::write_control(stream, &json!({"type":"ok"}))
}

fn agy_ready(state: &State) -> io::Result<Value> {
    let broker = state
        .agy_broker
        .as_ref()
        .ok_or_else(|| io::Error::other("Antigravity native input is disabled"))?;
    let joined = broker.joined.lock().unwrap();
    let source = joined.as_ref().ok_or_else(|| {
        io::Error::other("Antigravity has no qualified native input registration")
    })?;
    agy_worker_live(state)?;
    broker.verify(source)?;
    agy_worker_live(state)?;
    Ok(json!({"type":"ok", "conversation_id":source.conversation()}))
}

fn agy_send(state: &State, request: &Value) -> io::Result<Value> {
    let broker = state
        .agy_broker
        .as_ref()
        .ok_or_else(|| io::Error::other("Antigravity native input is disabled"))?;
    let joined = broker.joined.lock().unwrap();
    let source = joined.as_ref().ok_or_else(|| {
        io::Error::other("Antigravity has no qualified native input registration")
    })?;
    agy_worker_live(state)?;
    broker.send(source, &state.token, request, || agy_worker_live(state))
}

fn claude_composer(state: &State) -> bool {
    let observed = state.observations.lock().unwrap();
    !observed.claude_paste_attempted
        && observed.state.status()["Interaction"] == "none"
        && providers::claude::empty_composer(state.screen.lock().unwrap().parser.screen())
}

fn queue_claude_prompt(state: &State, request: &Value) -> io::Result<()> {
    let guard = state.control.lock().unwrap();
    let attempt = field(request, "attempt");
    let operation = field(request, "operation");
    let text = field(request, "text");
    let Some(conversation) = state.claude_schedule.as_deref() else {
        return Err(io::Error::other("Claude schedule binding is unavailable"));
    };
    let path = state.dir.join("schedule-prompt.json");
    let record = runtime::read_json(&path)?;
    let hash = providers::claude::text_hash(text);
    if *guard != 0
        || state.terminal_input.load(Ordering::Acquire)
        || !state
            .schedule
            .as_ref()
            .is_some_and(|b| b.attempt_id == attempt)
        || record["token"] != state.token
        || record["attempt"] != attempt
        || record["operation"] != operation
        || operation.is_empty()
        || record["conversation_id"] != conversation
        || record["text_hash"] != hash
        || !worker::prompt_fits(text, true)
        || !providers::claude::scheduled_text(text)
    {
        return Err(io::Error::other("Claude opening prompt custody changed"));
    }
    let mut paste = Vec::with_capacity(text.len() + 12);
    paste.extend_from_slice(b"\x1b[200~");
    paste.extend_from_slice(text.as_bytes());
    paste.extend_from_slice(b"\x1b[201~");
    {
        let mut observed = state.observations.lock().unwrap();
        let screen = state.screen.lock().unwrap();
        if observed.claude_paste_attempted
            || observed.state.status()["Interaction"] != "none"
            || !providers::claude::empty_composer(screen.parser.screen())
        {
            return Err(io::Error::other("Claude opening prompt custody changed"));
        }
        observed.claude_paste_attempted = true;
        pty::input(&state.master, &paste)?;
    }
    let deadline = Instant::now() + Duration::from_secs(1);
    loop {
        if runtime::read_json(&path)? != record {
            return Err(io::Error::other(
                "Claude opening prompt reservation changed after paste",
            ));
        }
        {
            let mut observed = state.observations.lock().unwrap();
            let screen = state.screen.lock().unwrap();
            let screen = screen.parser.screen();
            if observed.state.status()["Interaction"] != "none"
                || providers::claude::startup_attention(screen)
                || !screen.bracketed_paste()
            {
                break;
            }
            if providers::claude::rendered_paste(screen, text) {
                pty::input(&state.master, b"\r")?;
                observed.claude_dispatch = Some((operation.into(), hash));
                return Ok(());
            }
        }
        if Instant::now() >= deadline {
            break;
        }
        thread::sleep(Duration::from_millis(15));
    }
    Err(io::Error::other(
        "Claude opening prompt draft could not be verified after paste",
    ))
}

fn observe_claude_schedule(
    state: &State,
    observed: &mut ProviderObservation,
    event: &str,
    payload: &Value,
) -> io::Result<()> {
    let Some(conversation) = state.claude_schedule.as_deref() else {
        return Ok(());
    };
    if providers::claude::conversation_id(payload) != Some(conversation)
        || field(payload, "cwd").is_empty()
        || !field(payload, "agent_id").is_empty()
        || payload["hook_event_name"] != event
    {
        return Ok(());
    }
    let Some((operation, hash)) = observed.claude_dispatch.as_ref() else {
        return Ok(());
    };
    let prompt = field(payload, "prompt_id");
    if !providers::claude::canonical_id(prompt) {
        return Ok(());
    }
    if event == "UserPromptSubmit" {
        if observed.claude_prompt.is_none() && payload["scheduled_paste_hash"] == *hash {
            observed.claude_prompt = Some(prompt.to_owned());
        }
    } else if event == "Stop" && observed.claude_prompt.as_deref() == Some(prompt) {
        let text = field(payload, "last_assistant_message");
        let attempt = &state
            .schedule
            .as_ref()
            .expect("bound Claude schedule")
            .attempt_id;
        if !text.trim().is_empty() {
            runtime::write_json(
                &state.dir.join("schedule-finish.json"),
                &json!({
                    "token":state.token,"attempt":attempt,"operation":operation,
                    "conversation_id":conversation,"outcome":"completed","item":prompt,"text":text,
                }),
            )?;
        }
    }
    Ok(())
}

fn hook_observation(stream: &mut UnixStream, state: &State, request: &Value) -> io::Result<()> {
    let event = field(request, "event");
    let payload = &request["payload"];
    if state.provider != "claude"
        || field(request, "token") != state.hook_token
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
                runtime::write_json(
                    &state.dir.join("claude-thread-changed.json"),
                    &json!({"token":state.token}),
                )?;
                return Err(io::Error::other("provider hook conversation changed"));
            }
        } else if !cwd.is_empty() {
            observed.conversation = Some(id.to_owned());
        }
    }
    if event == "Stop"
        && conversation.is_some()
        && !cwd.is_empty()
        && observed.conversation.as_deref() == conversation
    {
        let reply = field(payload, "last_assistant_message");
        if !reply.is_empty() && reply.len() <= 4096 {
            capture_pilot_item(
                &state.dir,
                &state.token,
                json!({"kind":"agent_message","status":"completed","text":reply}),
            )?;
        }
    }
    observe_claude_schedule(state, &mut observed, event, payload)?;
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

fn capture_pilot_item(dir: &Path, token: &str, mut item: Value) -> io::Result<()> {
    let path = dir.join("pilot-replies.json");
    let mut record = match runtime::read_json(&path) {
        Ok(record) if record["token"] == token => record,
        Err(err) if err.kind() == io::ErrorKind::NotFound => {
            json!({"token":token,"next":1,"captures":[]})
        }
        Err(err) => return Err(err),
        _ => {
            return Err(io::Error::other(
                "pilot history file belongs to another worker",
            ));
        }
    };
    let next = record["next"]
        .as_u64()
        .filter(|n| *n > 0 && *n < u64::MAX)
        .ok_or_else(|| io::Error::other("invalid pilot history sequence"))?;
    let captures = record["captures"]
        .as_array_mut()
        .filter(|rows| rows.len() <= 16)
        .ok_or_else(|| io::Error::other("invalid pilot history captures"))?;
    if captures.len() == 16 {
        captures.remove(0);
    }
    item["item_id"] = json!(codex_receipt::hash(&format!("pilot:{token}:{next}")));
    captures.push(json!({"sequence":next,"item":item}));
    record["next"] = json!(next + 1);
    runtime::write_json(&path, &record)
}

fn receive_agy_hook(stream: &mut UnixStream, state: &State) -> io::Result<()> {
    stream.set_read_timeout(Some(Duration::from_millis(250)))?;
    stream.set_write_timeout(Some(Duration::from_millis(100)))?;
    let request = runtime::read_control(stream)?;
    let payload = &request["payload"];
    if request["type"] != "hook"
        || request["event"] != agy::EVENT
        || state.hook_token.is_empty()
        || request["token"] != state.hook_token
        || !payload.is_object()
    {
        return Err(io::Error::other("invalid Antigravity hook custody"));
    }
    agy_hook_observation(stream, state, payload)
}

fn agy_hook_observation(stream: &mut UnixStream, state: &State, payload: &Value) -> io::Result<()> {
    let mut observed = state.observations.lock().unwrap();
    let at = payload["observed_ns"].as_u64().unwrap_or_default();
    let id = agy::status_conversation(payload)?;
    if at <= observed.agy_latest {
        return Err(io::Error::other("stale Antigravity status line"));
    }
    let mut changed = false;
    if let Some(id) = id {
        if observed
            .conversation
            .as_deref()
            .is_some_and(|first| first != id)
        {
            return Err(io::Error::other("Antigravity hook conversation changed"));
        }
        if observed.conversation.is_none() {
            observed.conversation = Some(id.to_owned());
            changed = true;
        }
        if !observed.agy_thread_confirmed {
            observed.agy_thread_confirmed = true;
            changed = true;
        }
    }
    observed.agy_latest = at;
    if let Some(status) = agy::hook_status(field(payload, "agent_state")) {
        observed.agy_hook_at = Some(Instant::now());
        if observed.agy_hook_status.as_ref() != Some(&status) {
            observed.agy_hook_status = Some(status);
            changed = true;
        }
    }
    let title = field(payload, "conversation_title");
    if !title.is_empty()
        && observed
            .agy_title
            .as_ref()
            .is_none_or(|old| old["name"] != title)
    {
        observed.agy_title = Some(json!({"name": title, "observed_ns": at}));
        changed = true;
    }
    if changed {
        write_agy_observation(state, &observed)?;
    }
    runtime::write_control(stream, &json!({"type":"ok"}))
}

fn write_agy_observation(state: &State, observed: &ProviderObservation) -> io::Result<()> {
    runtime::write_json(
        &state.dir.join("observation.json"),
        &json!({
            "token": state.token, "conversation_id": observed.conversation,
            "status": observed.agy_hook_status.as_ref().or(observed.agy_status.as_ref()),
            "provider_title": observed.agy_title, "native_joined":observed.agy_thread_confirmed,
        }),
    )
}

fn attach(mut stream: UnixStream, state: &State) -> io::Result<()> {
    let generation = state.generation.fetch_add(1, Ordering::Relaxed);
    let (tx, rx) = mpsc::sync_channel(32);
    let worker = stream.try_clone()?;
    let eviction = stream.try_clone()?;
    let snap;
    let replies = Arc::new(Mutex::new(terminal_reply::Debt::default()));
    let mut filter = terminal_reply::Filter::default();
    {
        let mut owner = state.control.lock().unwrap();
        let mut screen = state.screen.lock().unwrap();
        snap = screen::snapshot(screen.parser.screen(), screen.mouse.snapshot())?;
        if let Some(previous) = screen.subscriber.replace(Subscriber {
            generation,
            output: tx,
            conn: eviction,
            replies: replies.clone(),
        }) {
            let _ = previous.conn.shutdown(Shutdown::Both);
        }
        *owner = generation;
        state.context_guard_activity.fetch_add(1, Ordering::AcqRel);
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
            if let Some(delay) = filter.delay(Instant::now())
                && (delay.is_zero() || !pty::wait(&stream, libc::POLLIN, delay)?)
            {
                let owner = state.control.lock().unwrap();
                if *owner != generation {
                    break;
                }
                let bytes = filter.flush();
                if !bytes.is_empty() {
                    state.terminal_input.store(true, Ordering::Release);
                    pty::input(&state.master, &bytes)?;
                }
                continue;
            }
            let (tag, bytes) = wire::read_frame(&mut stream)?;
            let owner = state.control.lock().unwrap();
            if *owner != generation {
                break;
            }
            match tag {
                DATA_IN => {
                    if state.manager {
                        return Err(io::Error::other(
                            "manager terminal input requires a custody transition",
                        ));
                    }
                    let bytes = filter.feed(&mut replies.lock().unwrap(), &bytes, Instant::now());
                    if !bytes.is_empty() {
                        state.terminal_input.store(true, Ordering::Release);
                        pty::input(&state.master, &bytes)?;
                    }
                }
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
            let bytes = filter.flush();
            if !bytes.is_empty() {
                state.terminal_input.store(true, Ordering::Release);
                let _ = pty::input(&state.master, &bytes);
            }
        }
    }
    let _ = stream.shutdown(Shutdown::Both);
    let _ = pump.join();
    result
}

fn write_input(state: &State, text: &str, paste: bool, submit: bool) -> io::Result<()> {
    let bracketed = paste
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
    pty::input(&state.master, &data)
        .map_err(|e| io::Error::other(format!("input delivery may have been partial: {e}")))?;
    if submit {
        thread::sleep(Duration::from_millis(150));
        pty::input(&state.master, b"\r")
            .map_err(|e| io::Error::other(format!("text delivered but submit failed: {e}")))?;
    }
    Ok(())
}
