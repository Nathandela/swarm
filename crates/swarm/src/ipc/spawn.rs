use super::{CliResult, Client, field, flag_error, options, report};
use hmac::{Hmac, Mac};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::collections::BTreeMap;
use std::env;
use std::fs::{self, DirBuilder, File, OpenOptions};
use std::io::{self, Read, Write};
use std::os::unix::ffi::OsStrExt;
use std::os::unix::fs::{DirBuilderExt, MetadataExt, OpenOptionsExt};
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

const NEW_ID: &str = "new-operation-id";
const OWNER: &str = "owner-launch";
const MAIL: &str = "dispatcher-worker-mail";
pub(super) const CAPS: &[&str] = &[OWNER, MAIL];
const FLAGS: &[&str] = &[
    "cli",
    "dir",
    "model",
    "worktree",
    "name",
    "prompt",
    "handoff",
    "delegate",
    "operation-id",
    NEW_ID,
    "status",
];

pub(crate) fn now_ms() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_millis() as u64
}

fn nonce() -> io::Result<String> {
    let mut bytes = [0u8; 16];
    File::open("/dev/urandom")?.read_exact(&mut bytes)?;
    Ok(bytes.iter().map(|byte| format!("{byte:02x}")).collect())
}

pub(super) fn new_id() -> io::Result<String> {
    Ok(format!("launch:{}:{}", now_ms(), nonce()?))
}

pub(super) fn print_id() -> CliResult {
    println!("{}", new_id().map_err(report)?);
    Ok(())
}

pub(crate) fn replayable(id: &str, now: u64) -> bool {
    let Some((ms, random)) = id.strip_prefix("launch:").and_then(|s| s.split_once(':')) else {
        return false;
    };
    let Ok(issued) = ms.parse::<u64>() else {
        return false;
    };
    issued > 0
        && ms.len() <= 13
        && issued.to_string() == ms
        && random.len() == 32
        && random
            .bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
        && issued <= now.saturating_add(30_000)
        && now.saturating_sub(issued) <= 3_600_000
}

pub(super) fn absolute(path: &Path) -> io::Result<PathBuf> {
    use std::path::Component;
    let source = if path.is_absolute() {
        path.to_owned()
    } else {
        let physical = env::current_dir()?;
        let pwd = env::var_os("PWD")
            .map(PathBuf::from)
            .filter(|p| p.is_absolute())
            .filter(|p| {
                fs::metadata(p)
                    .and_then(|logical| {
                        fs::metadata(&physical)
                            .map(|here| (logical.dev(), logical.ino()) == (here.dev(), here.ino()))
                    })
                    .unwrap_or(false)
            })
            .unwrap_or(physical);
        pwd.join(path)
    };
    let mut clean = PathBuf::from("/");
    for component in source.components() {
        match component {
            Component::Normal(s) => clean.push(s),
            Component::ParentDir => {
                clean.pop();
            }
            _ => {}
        }
    }
    Ok(clean)
}

fn private_dir(path: &Path) -> io::Result<File> {
    let dir = OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_DIRECTORY | libc::O_NOFOLLOW)
        .open(path)?;
    let meta = dir.metadata()?;
    // SAFETY: getuid has no preconditions.
    if meta.uid() != unsafe { libc::getuid() } || meta.mode() & 0o077 != 0 {
        return Err(io::Error::other(format!(
            "unsafe handoff directory {}",
            path.display()
        )));
    }
    dir.lock()?;
    Ok(dir)
}

pub(super) fn snapshot(id: &str, source: &str) -> io::Result<PathBuf> {
    let tmp = absolute(&env::temp_dir())?;
    if id.is_empty() {
        let data = fs::read(source)?;
        let directory = tmp.join(format!("swarm-handoff-{}", nonce()?));
        DirBuilder::new().mode(0o700).create(&directory)?;
        let destination = directory.join("handoff.md");
        if let Err(e) = write_snapshot(&destination, &data) {
            let _ = fs::remove_file(&destination);
            let _ = fs::remove_dir(&directory);
            return Err(e);
        }
        return Ok(destination);
    }
    let source = absolute(Path::new(source))?;
    let mut key = Sha256::new();
    key.update(id.as_bytes());
    key.update([0]);
    key.update(source.as_os_str().as_bytes());
    // SAFETY: getuid has no preconditions.
    let directory = tmp.join(format!(
        "swarm-handoff-{}-{:x}",
        unsafe { libc::getuid() },
        key.finalize()
    ));
    if let Err(e) = DirBuilder::new().mode(0o700).create(&directory)
        && e.kind() != io::ErrorKind::AlreadyExists
    {
        return Err(e);
    }
    let lock = private_dir(&directory)?;
    let staging = directory.join(".partial");
    if let Err(e) = fs::remove_file(&staging)
        && e.kind() != io::ErrorKind::NotFound
    {
        return Err(e);
    }
    let entries: Vec<_> = directory.read_dir()?.take(2).collect::<io::Result<_>>()?;
    let source_data = fs::read(source);
    let destination = match entries.as_slice() {
        [] => {
            let bytes = source_data?;
            let dest = directory.join(format!("{:x}.md", Sha256::digest(&bytes)));
            write_snapshot(&staging, &bytes)?;
            fs::rename(&staging, &dest)?;
            dest
        }
        [entry] => {
            let mut reader = OpenOptions::new()
                .read(true)
                .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK)
                .open(entry.path())?;
            let m = reader.metadata()?;
            // SAFETY: getuid has no preconditions.
            if !m.is_file()
                || m.uid() != unsafe { libc::getuid() }
                || m.mode() & 0o077 != 0
                || m.nlink() != 1
            {
                return Err(io::Error::other(format!(
                    "unsafe handoff snapshot {}",
                    entry.path().display()
                )));
            }
            let mut bytes = Vec::new();
            reader.read_to_end(&mut bytes)?;
            if entry.file_name().to_str() != Some(&format!("{:x}.md", Sha256::digest(&bytes))) {
                return Err(io::Error::other(format!(
                    "invalid handoff snapshot digest in {}",
                    directory.display()
                )));
            }
            match source_data {
                Ok(data) if data != bytes => {
                    return Err(io::Error::other(
                        "handoff document changed for this operation ID; check its status before issuing new work",
                    ));
                }
                Err(e) if e.kind() != io::ErrorKind::NotFound => return Err(e),
                _ => {}
            }
            entry.path()
        }
        _ => {
            return Err(io::Error::other(format!(
                "ambiguous handoff snapshot in {}",
                directory.display()
            )));
        }
    };
    lock.sync_all()?;
    File::open(tmp)?.sync_all()?;
    Ok(destination)
}

pub(super) fn dispose_refused(path: &Path, e: &io::Error, verb: &str) {
    // A lost acknowledgement can leave a live worker reading this file.
    if e.get_ref()
        .and_then(|inner| inner.downcast_ref::<super::Refusal>())
        .is_some_and(|r| r.1)
    {
        let _ = fs::remove_file(path);
        if let Some(dir) = path.parent() {
            let _ = fs::remove_dir(dir);
        }
    } else {
        eprintln!(
            "{verb}: unconfirmed launch; retained handoff at {}",
            path.display()
        );
    }
}

fn write_snapshot(path: &Path, data: &[u8]) -> io::Result<()> {
    let mut file = OpenOptions::new()
        .create_new(true)
        .write(true)
        .mode(0o600)
        .open(path)?;
    file.write_all(data)?;
    file.sync_all()
}

fn env_value(key: &str) -> String {
    env::var(key).unwrap_or_default()
}

fn dispatcher_proof() -> String {
    let explicit = env_value("SWARM_DISPATCHER_SPAWN_PROOF");
    if !explicit.is_empty() {
        return explicit;
    }
    let token = env_value("SWARM_HOOK_TOKEN");
    if token.is_empty() {
        return String::new();
    }
    let mut h =
        Hmac::<Sha256>::new_from_slice(token.as_bytes()).expect("HMAC accepts every key length");
    h.update(b"spawn");
    format!("{:x}", h.finalize().into_bytes())
}

pub(super) fn run(client: &mut Client, args: &[String]) -> CliResult {
    let mut fields = BTreeMap::new();
    let rest = options(args, FLAGS, &["worktree", NEW_ID], |name, value| {
        let canonical = FLAGS
            .iter()
            .copied()
            .find(|f| *f == name)
            .expect("validated flag");
        if ["worktree", NEW_ID].contains(&canonical) {
            crate::flags::parse_bool(name, value)?;
        }
        fields.insert(canonical, value);
        Ok(())
    })
    .map_err(|e| flag_error("spawn", 2, e))?;
    if !rest.is_empty() {
        return Err((2, "unexpected positional arguments".into()));
    }
    let get = |k: &str| fields.get(k).copied().unwrap_or("");
    let enabled = |k: &str| crate::flags::parse_bool(k, get(k)).unwrap_or(false);
    if enabled(NEW_ID) {
        if fields.len() != 1 {
            return Err((2, "--new-operation-id takes no other arguments".into()));
        }
        return print_id();
    }
    if ["status", "operation-id"]
        .iter()
        .any(|k| fields.get(k) == Some(&""))
    {
        return Err((2, "operation ID must not be empty".into()));
    }
    let status = get("status");
    if !status.is_empty() {
        if fields.len() != 1 {
            return Err((2, "--status takes only an operation ID".into()));
        }
        if !client.supports(OWNER) {
            return Err((1,"protocol: reliable launch status requires an updated daemon; run swarm daemon restart".into()));
        }
        let reply = client
            .exchange(
                json!({"op":"owner_launch_status","subject_operation_id":status}),
                "owner_launch_status",
                super::REQUEST_TIMEOUT,
            )
            .map_err(report)?;
        if field(&reply, "subject_operation_id") != status
            || !reply["operation_outcome"].is_object()
        {
            return Err((1, "protocol: uncorrelated launch status".into()));
        }
        println!(
            "{}",
            super::escape_json(reply["operation_outcome"].to_string())
        );
        return Ok(());
    }
    let mut id = get("operation-id").to_string();
    if !id.is_empty() && !replayable(&id, now_ms()) {
        return Err((
            2,
            "invalid or expired operation ID; check an earlier attempt with --status".into(),
        ));
    }
    let agent = get("cli");
    if agent.is_empty() {
        return Err((2,"--cli is required: swarm spawn --cli <agent> (--prompt t | --handoff f | --delegate f)".into()));
    }
    if ["prompt", "handoff", "delegate"]
        .iter()
        .filter(|k| !get(k).is_empty())
        .count()
        != 1
    {
        return Err((
            2,
            "need exactly one of --prompt, --handoff or --delegate".into(),
        ));
    }
    let cwd = absolute(Path::new(get("dir"))).map_err(report)?;
    let intent = if get("handoff").is_empty() {
        "delegate"
    } else {
        "handoff"
    };
    let doc = get(intent);
    let isolate = if fields.contains_key("worktree") {
        enabled("worktree")
    } else {
        !doc.is_empty()
            && Command::new("git")
                .arg("-C")
                .arg(&cwd)
                .args(["rev-parse", "--is-inside-work-tree"])
                .stdin(Stdio::null())
                .output()
                .is_ok_and(|r| r.status.success() && r.stdout == b"true\n")
    };
    let parent = env_value("SWARM_SESSION_ID");
    let dispatcher = env_value("SWARM_MANAGER_VM_CLI") == "1";
    let mut proof = String::new();
    if dispatcher && intent == "delegate" {
        proof = dispatcher_proof();
        if parent.is_empty() || proof.is_empty() {
            return Err((1,"this retained dispatcher lacks its worker mailbox credential; allow this manager to park and resume the same discussion on the upgraded supervisor".into()));
        }
    }
    if dispatcher && !parent.is_empty() && id.is_empty() {
        id = new_id().map_err(report)?;
        writeln!(io::stderr().lock(), "operation_id={id}").map_err(report)?;
    }
    if !id.is_empty() && !client.supports(OWNER) {
        return Err((
            1,
            "protocol: reliable launch requires an updated daemon; run swarm daemon restart".into(),
        ));
    }
    if !proof.is_empty() && !client.supports(MAIL) {
        return Err((
            1,
            "dispatcher worker mailbox requires an updated daemon".into(),
        ));
    }
    let destination = if doc.is_empty() {
        None
    } else {
        Some(snapshot(&id, doc).map_err(report)?)
    };
    let prompt = destination
        .as_ref()
        .map(|p| format!("Read and follow the instructions in {}.", p.display()))
        .unwrap_or_else(|| get("prompt").to_owned());
    let options = if get("model").is_empty() {
        Value::Null
    } else {
        json!({"model":get("model")})
    };
    let env: Vec<_> = env::vars_os()
        .map(|(k, v)| format!("{}={}", k.to_string_lossy(), v.to_string_lossy()))
        .collect();
    if agent == "codex" {
        crate::providers::codex::refuse_unqualified_origin(&json!(env)).map_err(report)?;
    }
    let env = if id.is_empty() {
        env
    } else {
        crate::environment::reliable(agent, env)
    };
    let mut launch = json!({"agent":agent,"cwd":cwd.to_string_lossy(),"options":options,"env":env,"cols":80,"rows":24,"initial_prompt":prompt});
    if !get("name").is_empty() {
        launch["name"] = json!(get("name"));
    }
    if isolate {
        launch["worktree"] = json!(true);
    }
    if !parent.is_empty() {
        launch["spawned_from"] = json!(parent);
        launch["spawn_intent"] = json!(intent);
    }
    if !proof.is_empty() {
        launch["dispatcher_proof"] = json!(proof);
    }
    let mut request = json!({"op":"launch","launch":launch});
    let timeout = if !id.is_empty() {
        request["op"] = json!("owner_launch");
        request["operation_id"] = json!(id);
        100
    } else {
        match agent {
            "hermes" => 100,
            "opencode" => 75,
            _ => 10,
        }
    };
    let reply = client
        .exchange(request, "launch", Duration::from_secs(timeout))
        .map_err(|e| {
            if id.is_empty()
                && let Some(dest) = destination.as_ref()
            {
                dispose_refused(dest, &e, "spawn");
            }
            report(e)
        })?;
    if !id.is_empty() && field(&reply, "operation_id") != id {
        return Err((
            1,
            "protocol: uncorrelated launch receipt; outcome unknown".into(),
        ));
    }
    let Some(session) = reply["session"]["id"].as_str() else {
        return Err((1, "protocol: launch reply carried no session".into()));
    };
    println!("{session}");
    eprintln!("spawned {} ({session})", field(&reply["session"], "name"));
    Ok(())
}

#[cfg(test)]
mod tests {
    #[test]
    fn launch_id_time_and_canonical_encoding() {
        let ms = 1795633093000;
        let id = format!("launch:{ms}:{}", "a0".repeat(16));
        for now in [ms - 30000, ms, ms + 3600000] {
            assert!(super::replayable(&id, now));
        }
        for now in [ms - 30001, ms + 3600001] {
            assert!(!super::replayable(&id, now));
        }
        assert!(!super::replayable(&id.to_uppercase(), ms));
        assert!(!super::replayable(&id.replace("launch:", "launch:0"), ms));
    }
}
