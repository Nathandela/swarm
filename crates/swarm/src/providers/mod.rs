pub(crate) mod agy;
pub(crate) mod agy_input;
pub(crate) mod claude;
pub(crate) mod codex;
pub(crate) mod codex_proof;
pub(crate) mod codex_receipt;

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
    pub agy_acp: bool,
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
        _ => return Err(io::Error::other("unsupported provider")),
    };
    if agent == "codex" {
        codex::refuse_unqualified_origin(&request["env"])?;
    }
    environment.retain(|(name, _)| match name.as_str() {
        "ANTHROPIC_API_KEY" => agent == "claude",
        "OPENAI_API_KEY" => agent == "codex",
        _ => true,
    });
    let path = environment
        .iter()
        .rev()
        .find(|(name, _)| name == "PATH")
        .map(|(_, value)| value)
        .ok_or_else(|| io::Error::other("provider launch requires PATH"))?;
    let executable = resolve(agent, cwd, path)?;
    let before = identity(&executable)?;
    let output = probe_output(&executable, cwd, &environment, &["--version"])?;
    let version = match agent {
        "agy" => agy::version(&output)?,
        "codex" => codex::version(&output)?,
        _ => claude::version(&output)?,
    }
    .to_owned();
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
    let argv = match agent {
        "agy" => agy::argv(request)?,
        "codex" => codex::argv(request)?,
        _ => claude::argv(label, request)?,
    };
    Ok(LaunchPlan {
        agent,
        argv,
        env: environment,
        executable,
        identity: after,
        version,
        agy_status_line,
        agy_acp,
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

fn probe_output(
    executable: &Path,
    cwd: &Path,
    env: &[(String, String)],
    args: &[&str],
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
                    !matches!(name.as_str(), "ANTHROPIC_API_KEY" | "OPENAI_API_KEY")
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
        let limit = if args.first() == Some(&"agentapi") {
            64 * 1024
        } else {
            16 * 1024
        };
        let deadline = Instant::now() + Duration::from_secs(5);
        loop {
            let idle = match output_socket.read(&mut buffer) {
                Ok(0) => true,
                Ok(n) => {
                    output.extend_from_slice(&buffer[..n]);
                    if output.len() > limit {
                        return Err(io::Error::other("provider probe output exceeds limit"));
                    }
                    false
                }
                Err(err) if err.kind() == io::ErrorKind::WouldBlock => true,
                Err(err) if err.kind() == io::ErrorKind::Interrupted => false,
                Err(err) => return Err(err),
            };
            if idle && let Some(status) = child.try_wait()? {
                if !status.success() {
                    return Err(io::Error::other("provider probe command failed"));
                }
                return String::from_utf8(output)
                    .map_err(|_| io::Error::other("provider probe output is not UTF-8"));
            }
            if Instant::now() >= deadline {
                return Err(io::Error::other("provider probe command timed out"));
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
