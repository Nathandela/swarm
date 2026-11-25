use super::{Result, store::Occurrence};
use sha2::{Digest, Sha256};
use std::{
    env, fs,
    io::{self, Read},
    os::{fd::AsRawFd, unix::process::CommandExt},
    path::{Path, PathBuf},
    process::{Command, Stdio},
    thread,
    time::{Duration, Instant},
};

pub(super) fn overlaps(a: &Path, b: &Path) -> bool {
    a.starts_with(b) || b.starts_with(a)
}

fn root(state: &Path) -> Result<PathBuf> {
    let hash = format!("{:x}", Sha256::digest(state.as_os_str().as_encoded_bytes()));
    Ok(state
        .parent()
        .ok_or("scheduler has no workspace parent")?
        .join(format!("swarm-scheduled-worktrees-{}", &hash[..12])))
}

pub(super) fn describe(state: &Path, run: &Occurrence, isolate: bool) -> Result<(String, String)> {
    let selected = Path::new(&run.spec.directory);
    let source = fs::canonicalize(selected)?;
    if !source.is_dir() || source != selected {
        return Err("scheduled directory changed via a symlink or is unavailable".into());
    }
    let workspace = if !isolate {
        source.clone()
    } else {
        let checkout = fs::canonicalize(
            git(&source, &["rev-parse", "--show-toplevel"])
                .map_err(|e| format!("isolated workspace requires a Git working tree: {e}"))?,
        )?;
        let relative = source
            .strip_prefix(&checkout)
            .map_err(|_| "job directory is outside its Git working tree")?;
        let target = root(state)?.join(&run.id);
        if overlaps(&checkout, &target) {
            return Err("the scheduled worktree location is inside the source checkout; use a daemon state directory outside the repository".into());
        }
        target.join(relative)
    };
    Ok((
        string_path(&source)?.to_owned(),
        string_path(&workspace)?.to_owned(),
    ))
}

pub(super) fn ensure(state: &Path, run: &Occurrence) -> Result<()> {
    let workspace = Path::new(&run.workspace);
    let source = Path::new(&run.source_directory);
    if workspace == source {
        return canonical_dir(workspace);
    }
    if !super::valid_id(&run.id)
        && !(run.id.len() == 64
            && run
                .id
                .bytes()
                .all(|b| b.is_ascii_hexdigit() && !b.is_ascii_uppercase()))
    {
        return Err("invalid scheduled occurrence ID".into());
    }
    let root = root(state)?;
    crate::supervisor::runtime::private_dir(&root)?;
    let target = root.join(&run.id);
    if !workspace.starts_with(&target) {
        return Err("scheduled workspace escaped its assigned worktree".into());
    }
    let branch = format!("swarm/automation/{}", run.id);
    match fs::symlink_metadata(&target) {
        Ok(_) => {}
        Err(e) if e.kind() == io::ErrorKind::NotFound => {
            git(
                source,
                &[
                    "worktree",
                    "add",
                    "-b",
                    &branch,
                    string_path(&target)?,
                    "HEAD",
                ],
            )?;
        }
        Err(e) => return Err(e.into()),
    }
    canonical_dir(&target)?;
    let common = ["rev-parse", "--path-format=absolute", "--git-common-dir"];
    if git(source, &common)? != git(&target, &common)?
        || git(&target, &["symbolic-ref", "HEAD"])? != format!("refs/heads/{branch}")
        || git(&target, &["rev-parse", "--show-toplevel"])? != string_path(&target)?
    {
        return Err(
            "existing scheduled workspace belongs to a different worktree or branch".into(),
        );
    }
    canonical_dir(workspace)
}

fn canonical_dir(path: &Path) -> Result<()> {
    if fs::canonicalize(path)? != path {
        return Err("scheduled working directory changed via a symlink".into());
    }
    if !path.is_dir() {
        return Err(format!(
            "scheduled working directory is unavailable: {}",
            path.display()
        )
        .into());
    }
    Ok(())
}

fn string_path(path: &Path) -> Result<&str> {
    path.to_str()
        .ok_or_else(|| "directory path must contain valid UTF-8 text".into())
}

fn git(cwd: &Path, args: &[&str]) -> Result<String> {
    let mut cmd = Command::new("git");
    cmd.current_dir(cwd)
        .args(["-c", "core.hooksPath=/dev/null"])
        .args(args)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .process_group(0);
    for (key, _) in env::vars_os() {
        if key.as_encoded_bytes().starts_with(b"GIT_") {
            cmd.env_remove(key);
        }
    }
    let mut child = cmd.spawn()?;
    let mut stdout = child.stdout.take().ok_or("Git stdout unavailable")?;
    let result = (|| -> Result<String> {
        // Do not let Git's descendants hold a pipe or its dispatch transaction indefinitely.
        if unsafe { libc::fcntl(stdout.as_raw_fd(), libc::F_SETFL, libc::O_NONBLOCK) } == -1 {
            return Err(io::Error::last_os_error().into());
        }
        let mut text = Vec::new();
        let mut buf = [0; 4096];
        let deadline = Instant::now() + Duration::from_secs(20);
        loop {
            let idle = match stdout.read(&mut buf) {
                Ok(0) => true,
                Ok(n) => {
                    text.extend_from_slice(&buf[..n]);
                    if text.len() > 64 * 1024 {
                        return Err("Git output exceeds 64 KiB".into());
                    }
                    false
                }
                Err(e) if e.kind() == io::ErrorKind::WouldBlock => true,
                Err(e) if e.kind() == io::ErrorKind::Interrupted => false,
                Err(e) => return Err(e.into()),
            };
            if idle && let Some(status) = child.try_wait()? {
                if !status.success() {
                    return Err(format!("git {}: {status}", args[0]).into());
                }
                let mut output = String::from_utf8(text)?;
                if output.ends_with('\n') {
                    output.pop();
                }
                return Ok(output);
            }
            if Instant::now() >= deadline {
                return Err(format!("git {} timed out", args[0]).into());
            }
            if idle {
                thread::sleep(Duration::from_millis(10));
            }
        }
    })();
    if result.is_err() && matches!(child.try_wait(), Ok(None)) {
        // An owned live child anchors the dedicated process group created above.
        let _ = unsafe { libc::kill(-(child.id() as libc::pid_t), libc::SIGKILL) };
        let _ = child.wait();
    }
    result
}
