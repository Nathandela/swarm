//! A session owns one Git registration, with the source repository and branch recorded before launch.
use crate::supervisor::runtime;
use serde_json::{Value, json};
use std::ffi::OsStr;
use std::fs;
use std::io;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};

fn git<I, S>(repo: &Path, args: I) -> io::Result<Vec<u8>>
where
    I: IntoIterator<Item = S>,
    S: AsRef<OsStr>,
{
    let out = Command::new("git")
        .arg("-C")
        .arg(repo)
        .args(args)
        .env_remove("GIT_DIR")
        .env_remove("GIT_WORK_TREE")
        .env_remove("GIT_INDEX_FILE")
        .env("GIT_TERMINAL_PROMPT", "0")
        .stdin(Stdio::null())
        .output()?;
    if !out.status.success() {
        return Err(io::Error::other(format!(
            "worktree: {}",
            String::from_utf8_lossy(&out.stderr).trim()
        )));
    }
    Ok(out
        .stdout
        .strip_suffix(b"\n")
        .unwrap_or(&out.stdout)
        .to_owned())
}
pub(crate) fn is_repo(repo: &Path) -> bool {
    git(repo, ["rev-parse", "--is-inside-work-tree"]).is_ok_and(|s| s == b"true")
}

fn prefix(label: &str) -> String {
    let mut out = String::new();
    let mut gap = false;
    for ch in label.chars() {
        if !ch.is_alphanumeric() {
            gap = !out.is_empty();
            continue;
        }
        if gap {
            out.push('-');
            gap = false;
        }
        // Go's unicode.ToLower is a single-rune mapping.
        out.push(ch.to_lowercase().next().unwrap_or(ch));
    }
    out
}
fn truncate(s: &str, limit: usize) -> &str {
    &s[..s.floor_char_boundary(s.len().min(limit))]
}

fn checked_parent(repo: &Path) -> io::Result<PathBuf> {
    if repo.canonicalize()? != repo {
        return Err(io::Error::other(
            "worktree: source directory identity changed",
        ));
    }
    let mut parent = repo.to_owned();
    for part in [".swarm", "worktrees"] {
        parent.push(part);
        match parent.symlink_metadata() {
            Ok(m) if m.is_dir() => {}
            Ok(_) => {
                return Err(io::Error::other(format!(
                    "worktree: unsafe workspace parent {}",
                    parent.display()
                )));
            }
            Err(e) if e.kind() == io::ErrorKind::NotFound => {}
            Err(e) => return Err(e),
        }
    }
    Ok(parent)
}

pub(crate) fn plan(repo: &Path, id: &str, label: &str) -> io::Result<Value> {
    if !runtime::valid_id(id) || !is_repo(repo) {
        return Err(io::Error::other(
            "worktree: expected a Git work tree and a valid Swarm session",
        ));
    }
    let parent = checked_parent(repo)?;
    let prefix = prefix(label);
    let friendly = truncate(&prefix, 128).trim_end_matches('-');
    let (name, branch) = if friendly.is_empty() {
        (id, id.to_owned())
    } else {
        (
            friendly,
            format!(
                "{}-{id}",
                truncate(friendly, 127 - id.len()).trim_end_matches('-')
            ),
        )
    };
    let path = match parent.join(name).symlink_metadata() {
        Ok(_) => parent.join(&branch),
        Err(e) if e.kind() == io::ErrorKind::NotFound => parent.join(name),
        Err(e) => return Err(e),
    };
    let common = git(
        repo,
        ["rev-parse", "--path-format=absolute", "--git-common-dir"],
    )?;
    let common = String::from_utf8(common).map_err(io::Error::other)?;
    let base = String::from_utf8(git(repo, ["rev-parse", "--verify", "HEAD^{commit}"])?)
        .map_err(io::Error::other)?;
    Ok(json!({"path":path,"ref":format!("refs/heads/swarm/{branch}"),"common":common,"base":base}))
}

fn coordinates<'a>(repo: &Path, id: &str, record: &'a Value) -> io::Result<(&'a Path, &'a str)> {
    let path = record["path"]
        .as_str()
        .map(Path::new)
        .ok_or_else(|| io::Error::other("worktree: missing path"))?;
    let reference = record["ref"]
        .as_str()
        .ok_or_else(|| io::Error::other("worktree: missing ref"))?;
    let branch = reference
        .strip_prefix("refs/heads/swarm/")
        .ok_or_else(|| io::Error::other("worktree: invalid owner ref"))?;
    if !runtime::valid_id(id)
        || branch.contains('/')
        || !(branch == id || branch.ends_with(&format!("-{id}")))
        || path.parent() != Some(checked_parent(repo)?.as_path())
    {
        return Err(io::Error::other(
            "worktree: recorded checkout is outside session custody",
        ));
    }
    if record["common"].as_str().map(str::as_bytes)
        != Some(&git(
            repo,
            ["rev-parse", "--path-format=absolute", "--git-common-dir"],
        )?)
    {
        return Err(io::Error::other("worktree: source Git directory changed"));
    }
    Ok((path, reference))
}

fn registered(repo: &Path, path: &Path, reference: &str) -> io::Result<bool> {
    use std::os::unix::ffi::OsStrExt;
    let listing = git(
        repo,
        [
            "for-each-ref",
            "--format=%(refname)%00%(worktreepath)",
            reference,
        ],
    )?;
    if listing.is_empty() {
        return Ok(false);
    }
    let mut expected = reference.as_bytes().to_vec();
    expected.push(0);
    expected.extend_from_slice(path.as_os_str().as_bytes());
    if listing != expected {
        // A retained branch with no checkout is the completed removal state.
        if listing == [reference.as_bytes(), b"\0"].concat() && !path.try_exists()? {
            return Ok(false);
        }
        return Err(io::Error::other(
            "worktree: branch registration does not match its recorded checkout",
        ));
    }
    if path.canonicalize()? != path
        || git(path, ["symbolic-ref", "HEAD"])? != reference.as_bytes()
        || git(
            path,
            ["rev-parse", "--path-format=absolute", "--git-common-dir"],
        )? != git(
            repo,
            ["rev-parse", "--path-format=absolute", "--git-common-dir"],
        )?
    {
        return Err(io::Error::other("worktree: checkout identity changed"));
    }
    Ok(true)
}

pub(crate) fn create(repo: &Path, id: &str, record: &Value) -> io::Result<()> {
    let (path, reference) = coordinates(repo, id, record)?;
    if path.symlink_metadata().is_ok() {
        return Err(io::Error::other("worktree: destination already exists"));
    }
    let branch = reference.strip_prefix("refs/heads/").unwrap();
    let base = record["base"]
        .as_str()
        .filter(|s| matches!(s.len(), 40 | 64) && s.bytes().all(|b| b.is_ascii_hexdigit()))
        .ok_or_else(|| io::Error::other("worktree: invalid base commit"))?;
    git(
        repo,
        [
            OsStr::new("worktree"),
            OsStr::new("add"),
            OsStr::new("-b"),
            OsStr::new(branch),
            path.as_os_str(),
            OsStr::new(base),
        ],
    )?;
    if !registered(repo, path, reference)? {
        return Err(io::Error::other(
            "worktree: Git registration unavailable after creation",
        ));
    }
    Ok(())
}

pub(crate) fn remove(repo: &Path, id: &str, record: &Value) -> io::Result<()> {
    let (path, reference) = coordinates(repo, id, record)?;
    if registered(repo, path, reference)? {
        git(
            repo,
            [
                OsStr::new("worktree"),
                OsStr::new("remove"),
                OsStr::new("--force"),
                path.as_os_str(),
            ],
        )?;
    } else if fs::symlink_metadata(path).is_ok() {
        return Err(io::Error::other("worktree: unregistered path retained"));
    }
    Ok(())
}

pub(crate) fn verify(repo: &Path, id: &str, record: &Value) -> io::Result<()> {
    let (path, reference) = coordinates(repo, id, record)?;
    if !registered(repo, path, reference)? {
        return Err(io::Error::other(
            "worktree: reserved checkout is not registered",
        ));
    }
    Ok(())
}
