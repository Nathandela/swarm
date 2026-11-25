pub(crate) mod claude;
pub(crate) mod codex;
pub(crate) mod codex_receipt;
pub(crate) mod hermes;

use serde_json::{Value, json};
use std::fs;
use std::io::{self, Read};
use std::os::fd::AsRawFd;
use std::os::unix::fs::MetadataExt;
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
        "hermes" => "hermes",
        _ => return Err(io::Error::other("unsupported provider")),
    };
    if agent == "codex" {
        codex::refuse_unqualified_origin(&request["env"])?;
    }
    environment.retain(|(name, _)| {
        !matches!(
            (agent, name.as_str()),
            ("codex", "ANTHROPIC_API_KEY") | ("claude", "OPENAI_API_KEY")
        )
    });

    let path = environment
        .iter()
        .rev()
        .find(|(name, _)| name == "PATH")
        .map(|(_, value)| value)
        .ok_or_else(|| io::Error::other("provider launch requires PATH"))?;
    let executable = resolve(agent, cwd, path)?;
    let before = identity(&executable)?;
    let output = command_output(
        &executable,
        cwd,
        &environment,
        &["--version".into()],
        Duration::from_secs(5),
        16 * 1024,
    )?;
    let version = match agent {
        "codex" => codex::version(&output)?,
        "hermes" => hermes::version(&output)?,
        _ => claude::version(&output)?,
    }
    .to_owned();
    let after = identity(&executable)?;
    if before != after {
        return Err(io::Error::other(
            "provider executable changed during admission",
        ));
    }
    let mut native = Value::Null;
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
        native = json!({"options":request["options"],"retained":retained,"initial_prompt":request["initial_prompt"]});
    }
    if agent == "hermes" && native.is_null() {
        environment.extend(hermes::native::origins(&request["env"], false)?);
    }
    let argv = match agent {
        "codex" => codex::argv(request)?,
        "hermes" if native.is_null() => hermes::argv(request)?,
        "hermes" => vec![],
        _ => claude::argv(label, request)?,
    };
    Ok(LaunchPlan {
        agent,
        argv,
        env: environment,
        executable,
        identity: after,
        version,
        hermes: native,
        hermes_profile: if agent == "hermes" {
            crate::ipc::field(&request["options"], "profile").to_owned()
        } else {
            String::new()
        },
    })
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

fn command_output(
    executable: &Path,
    cwd: &Path,
    env: &[(String, String)],
    args: &[String],
    budget: Duration,
    limit: usize,
) -> io::Result<String> {
    let mut child = Command::new(executable)
        .args(args)
        .current_dir(cwd)
        .env_clear()
        .envs(
            env.iter()
                .filter(|(name, _)| {
                    args != ["--version"]
                        || !matches!(name.as_str(), "ANTHROPIC_API_KEY" | "OPENAI_API_KEY")
                })
                .map(|(name, value)| (name, value)),
        )
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()?;
    let mut stdout = child.stdout.take().expect("version output captured");
    let result = (|| {
        // Nonblocking reads bound the probe even if a descendant inherits stdout.
        if unsafe { libc::fcntl(stdout.as_raw_fd(), libc::F_SETFL, libc::O_NONBLOCK) } == -1 {
            return Err(io::Error::last_os_error());
        }
        let mut buffer = [0; 1024];
        let mut output = Vec::new();
        let deadline = Instant::now() + budget;
        loop {
            let idle = match stdout.read(&mut buffer) {
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
