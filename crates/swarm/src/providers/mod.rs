pub(crate) mod agy;
pub(crate) mod agy_input;
pub(crate) mod claude;
pub(crate) mod claude_input;
pub(crate) mod codex;
pub(crate) mod codex_context_guard;
pub(crate) mod codex_continuity;
pub(crate) mod codex_proof;
pub(crate) mod codex_receipt;
pub(crate) mod hermes;
pub(crate) mod opencode;

use serde_json::{Value, json};
use std::fs;
use std::io::{self, Read};
use std::os::fd::OwnedFd;
use std::os::unix::fs::MetadataExt;
use std::os::unix::net::UnixStream;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::time::{Duration, Instant};

pub(crate) struct LaunchPlan {
    pub agent: &'static str,
    pub argv: Vec<String>,
    pub env: Vec<(String, String)>,
    pub executable: PathBuf,
    pub identity: Value,
    pub version: String,
    pub agy_status_line: bool,
    pub native: Value,
    pub agy_acp: bool,
    pub hermes: Value,
    pub hermes_profile: String,
}

pub(crate) fn prepare(
    agent: &str,
    cwd: &Path,
    label: &str,
    request: &Value,
    mut environment: Vec<(String, String)>,
) -> io::Result<LaunchPlan> {
    let agent = match agent {
        "claude" | "claude-code" => "claude",
        "codex" => "codex",
        "agy" => "agy",
        "opencode" => "opencode",
        "hermes" => "hermes",
        _ => return Err(io::Error::other("unsupported provider")),
    };
    if agent == "codex" {
        codex::refuse_unqualified_origin(&request["env"])?;
        let home = environment
            .iter()
            .rev()
            .find(|(key, _)| key == "HOME")
            .map_or(Path::new(""), |(_, value)| Path::new(value));
        if let Some(native) = codex::explicit_home(&request["env"], home)? {
            if !request["worker_discussion"].is_null()
                || !crate::ipc::field(request, "role").is_empty()
            {
                return Err(io::Error::other(
                    "alternate CODEX_HOME requires an ordinary owner session",
                ));
            }
            environment.push(("CODEX_HOME".into(), native.to_string_lossy().into_owned()));
        }
    }
    environment.retain(|(name, _)| match name.as_str() {
        "ANTHROPIC_API_KEY" => matches!(agent, "claude" | "hermes"),
        "OPENAI_API_KEY" => matches!(agent, "codex" | "hermes"),
        "HOME" if agent == "opencode" => false,
        _ => true,
    });
    if agent == "opencode" {
        environment.extend(opencode::environment(&request["env"])?);
    }
    let path = environment
        .iter()
        .rev()
        .find(|(name, _)| name == "PATH")
        .map(|(_, value)| value)
        .ok_or_else(|| io::Error::other("provider launch requires PATH"))?;
    let executable = resolve(agent, cwd, path)?;
    let before = identity(&executable)?;
    let output = version_banner(agent, &executable, cwd, &environment)?;
    let version = admitted_version(agent, &output)?.to_owned();
    let agy_status_line = agent == "agy"
        && agy::status_line(&version)
        && probe_output(&executable, cwd, &environment, &["--help"])
            .is_ok_and(|help| agy::advertises_settings(&help));
    let agy_acp = agent == "agy"
        && agy::supports_acp(&version)
        && output.lines().any(|line| line.trim() == version);
    let after = identity(&executable)?;
    if before != after {
        return Err(io::Error::other(
            "provider executable changed during admission",
        ));
    }
    let mut hermes_launch = Value::Null;
    let retained = &request["hermes_retained_binding"];
    if agent == "hermes"
        && (!retained.is_null()
            || crate::discord::native_input_enabled()
                && crate::ipc::field(request, "resume_conversation_id").is_empty()
                && hermes::native_version(&version))
    {
        if !hermes::native_version(&version) {
            return Err(io::Error::other(
                "Hermes native resume requires a qualified current runtime",
            ));
        }
        let origins = hermes::native::origins(&request["env"], true)?;
        if retained.is_null() {
            environment.extend(origins);
        } else {
            let saved = request["hermes_retained_origins"]
                .as_object()
                .ok_or_else(|| {
                    io::Error::other("Hermes native resume home selectors are unproven")
                })?;
            for (key, value) in saved {
                if !hermes::native::NORMAL_ORIGINS.contains(&key.as_str())
                    || value.as_str().is_none_or(|v| v.contains('\0'))
                {
                    return Err(io::Error::other("invalid retained Hermes home selector"));
                }
                environment.push((key.clone(), value.as_str().unwrap().into()));
            }
        }
        hermes_launch = json!({"options":request["options"],"retained":retained,"predecessor":request["hermes_predecessor"],"initial_prompt":request["initial_prompt"]});
    }
    if agent == "hermes" && hermes_launch.is_null() {
        environment.extend(hermes::native::origins(&request["env"], false)?);
    }
    let (argv, native) = match agent {
        "agy" => (agy::argv(request)?, Value::Null),
        "codex" => (codex::argv(request)?, Value::Null),
        "hermes" if hermes_launch.is_null() => (hermes::argv(request)?, Value::Null),
        "hermes" => (Vec::new(), Value::Null),
        "opencode" => opencode::launch(request)?,
        _ => (claude::argv(label, request)?, Value::Null),
    };
    Ok(LaunchPlan {
        agent,
        argv,
        env: environment,
        executable,
        identity: after,
        version,
        agy_status_line,
        native,
        agy_acp,
        hermes: hermes_launch,
        hermes_profile: if agent == "hermes" {
            crate::ipc::field(&request["options"], "profile").to_owned()
        } else {
            String::new()
        },
    })
}

fn version_banner(
    agent: &str,
    executable: &Path,
    cwd: &Path,
    environment: &[(String, String)],
) -> io::Result<String> {
    // A cold Hermes release probe also initializes its Python runtime.
    let budget = Duration::from_secs(if agent == "hermes" { 10 } else { 5 });
    command_output(
        executable,
        cwd,
        environment,
        &["--version"],
        budget,
        16 * 1024,
    )
}

fn admitted_version<'a>(agent: &str, output: &'a str) -> io::Result<&'a str> {
    match agent {
        "agy" => agy::version(output),
        "claude" => claude::version(output),
        "codex" => codex::version(output),
        "opencode" => opencode::version(output),
        "hermes" => hermes::version(output),
        _ => Err(io::Error::other(
            "Native launch is unavailable on this supervisor",
        )),
    }
}

pub(crate) fn detect_for_board(agent: &str, cwd: &Path) -> io::Result<String> {
    if agent == "codex" {
        codex::refuse_unqualified_origin(&Value::Null)?;
    }
    let entries = crate::supervisor::runtime::launch_env(&Value::Null);
    let path = entries
        .iter()
        .find(|(key, _)| key == "PATH")
        .map(|(_, value)| value)
        .ok_or_else(|| io::Error::other("provider detection requires PATH"))?;
    let executable = resolve(agent, cwd, path)?;
    let output = version_banner(agent, &executable, cwd, &entries)?;
    Ok(admitted_version(agent, &output)?.to_owned())
}

fn resolve(binary: &str, cwd: &Path, path: &str) -> io::Result<PathBuf> {
    for directory in std::env::split_paths(path) {
        let candidate = cwd.join(directory).join(binary);
        if let Ok(meta) = fs::metadata(&candidate)
            && meta.is_file()
            && meta.mode() & 0o111 != 0
        {
            return fs::canonicalize(candidate);
        }
    }
    Err(io::Error::new(
        io::ErrorKind::NotFound,
        format!("could not find {binary} on the launch PATH"),
    ))
}

pub(crate) fn identity(executable: &Path) -> io::Result<Value> {
    let meta = fs::metadata(executable)?;
    if !meta.is_file() || meta.mode() & 0o111 == 0 {
        return Err(io::Error::other(
            "provider executable is no longer an executable file",
        ));
    }
    Ok(json!({
        "device": meta.dev(), "inode": meta.ino(), "bytes": meta.size(),
        "mtime": meta.mtime(), "mtime_ns": meta.mtime_nsec(),
    }))
}

fn probe_output(
    executable: &Path,
    cwd: &Path,
    env: &[(String, String)],
    args: &[&str],
) -> io::Result<String> {
    let limit = if args.first() == Some(&"agentapi") {
        64 * 1024
    } else {
        16 * 1024
    };
    command_output(executable, cwd, env, args, Duration::from_secs(5), limit)
}

pub(crate) fn claude_agent_view(
    executable: &Path,
    cwd: &Path,
    env: &[(String, String)],
    all: bool,
) -> io::Result<Vec<Value>> {
    let mut args = vec!["agents", "--json"];
    if all {
        args.push("--all");
    }
    let output = command_output(
        executable,
        cwd,
        env,
        &args,
        Duration::from_secs(15),
        8 << 20,
    )?;
    Ok(serde_json::from_str(&output)?)
}

pub(crate) fn stop_claude_agent_view(
    executable: &Path,
    cwd: &Path,
    env: &[(String, String)],
) -> io::Result<()> {
    command_output(
        executable,
        cwd,
        env,
        &["daemon", "stop", "--any"],
        Duration::from_secs(30),
        8 << 20,
    )?;
    Ok(())
}

fn command_output(
    executable: &Path,
    cwd: &Path,
    env: &[(String, String)],
    args: &[&str],
    budget: Duration,
    limit: usize,
) -> io::Result<String> {
    let (mut output_socket, writer) = UnixStream::pair()?;
    output_socket.set_nonblocking(true)?;
    let stderr = if args == ["--help"] {
        Stdio::from(OwnedFd::from(writer.try_clone()?))
    } else {
        Stdio::null()
    };
    let mut child = Command::new(executable)
        .args(args)
        .current_dir(cwd)
        .env_clear()
        .envs(
            env.iter()
                .filter(|(name, _)| {
                    args.ends_with(&["gateway", "ensure"])
                        || !matches!(name.as_str(), "ANTHROPIC_API_KEY" | "OPENAI_API_KEY")
                })
                .map(|(name, value)| (name, value)),
        )
        .stdin(Stdio::null())
        .stdout(Stdio::from(OwnedFd::from(writer)))
        .stderr(stderr)
        .spawn()?;
    let result = (|| {
        // Nonblocking reads bound the probe even if a descendant inherits the output.
        let mut buffer = [0; 1024];
        let mut output = Vec::new();
        let deadline = Instant::now() + budget;
        loop {
            let idle = match output_socket.read(&mut buffer) {
                Ok(0) => true,
                Ok(n) => {
                    output.extend_from_slice(&buffer[..n]);
                    if output.len() > limit {
                        return Err(io::Error::other("provider command output exceeds limit"));
                    }
                    false
                }
                Err(err) if err.kind() == io::ErrorKind::WouldBlock => true,
                Err(err) if err.kind() == io::ErrorKind::Interrupted => false,
                Err(err) => return Err(err),
            };
            if idle && let Some(status) = child.try_wait()? {
                if !status.success() {
                    return Err(io::Error::other("provider command failed"));
                }
                return String::from_utf8(output)
                    .map_err(|_| io::Error::other("provider command output is not UTF-8"));
            }
            if Instant::now() >= deadline {
                return Err(io::Error::other("provider command timed out"));
            }
            if idle {
                std::thread::sleep(Duration::from_millis(10));
            }
        }
    })();
    if result.is_err() {
        let _ = child.kill();
        let _ = child.wait();
    }
    result
}
