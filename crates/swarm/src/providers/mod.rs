pub(crate) mod claude;
pub(crate) mod codex;

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
}

pub(crate) fn prepare(
    agent: &str,
    cwd: &Path,
    label: &str,
    request: &Value,
    environment: Vec<(String, String)>,
) -> io::Result<LaunchPlan> {
    let agent = match agent {
        "claude" | "claude-code" => "claude",
        "codex" => "codex",
        _ => return Err(io::Error::other("unsupported provider")),
    };
    let path = environment
        .iter()
        .rev()
        .find(|(name, _)| name == "PATH")
        .map(|(_, value)| value)
        .ok_or_else(|| io::Error::other("provider launch requires PATH"))?;
    let executable = resolve(agent, cwd, path)?;
    let before = identity(&executable)?;
    let output = version_output(&executable, cwd, &environment)?;
    let version = if agent == "codex" {
        codex::version(&output)?
    } else {
        claude::version(&output)?
    }
    .to_owned();
    let after = identity(&executable)?;
    if before != after {
        return Err(io::Error::other(
            "provider executable changed during admission",
        ));
    }
    let argv = if agent == "codex" {
        codex::argv(request)?
    } else {
        claude::argv(label, request)?
    };
    Ok(LaunchPlan {
        agent,
        argv,
        env: environment,
        executable,
        identity: after,
        version,
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

fn version_output(executable: &Path, cwd: &Path, env: &[(String, String)]) -> io::Result<String> {
    let mut child = Command::new(executable)
        .arg("--version")
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
        let deadline = Instant::now() + Duration::from_secs(5);
        loop {
            let idle = match stdout.read(&mut buffer) {
                Ok(0) => true,
                Ok(n) => {
                    output.extend_from_slice(&buffer[..n]);
                    if output.len() > 16 * 1024 {
                        return Err(io::Error::other("provider version output exceeds limit"));
                    }
                    false
                }
                Err(err) if err.kind() == io::ErrorKind::WouldBlock => true,
                Err(err) if err.kind() == io::ErrorKind::Interrupted => false,
                Err(err) => return Err(err),
            };
            if idle && let Some(status) = child.try_wait()? {
                if !status.success() {
                    return Err(io::Error::other("provider version command failed"));
                }
                return String::from_utf8(output)
                    .map_err(|_| io::Error::other("provider version output is not UTF-8"));
            }
            if Instant::now() >= deadline {
                return Err(io::Error::other("provider version command timed out"));
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
