//! Resolve all Git registrations, including a missing linked checkout, without refreshing the index.
use super::Checkout;
use std::collections::{BTreeMap, BTreeSet};
use std::fs::{self, OpenOptions};
use std::io::{self, Read};
use std::os::fd::OwnedFd;
use std::os::unix::fs::OpenOptionsExt;
use std::os::unix::net::UnixStream;
use std::path::{Component, Path, PathBuf};
use std::process::{Command, Stdio};
use std::time::{Duration, Instant};

pub(super) fn canonical(input: &Path) -> io::Result<PathBuf> {
    let abs = std::path::absolute(input)?;
    let mut clean = PathBuf::new();
    for part in abs.components() {
        match part {
            Component::ParentDir => {
                clean.pop();
            }
            _ => clean.push(part.as_os_str()),
        }
    }
    let mut parent = clean.as_path();
    loop {
        match fs::canonicalize(parent) {
            Ok(base) => {
                let suffix = clean
                    .strip_prefix(parent)
                    .expect("ancestor of cleaned path");
                return Ok(if suffix.as_os_str().is_empty() {
                    base
                } else {
                    base.join(suffix)
                });
            }
            Err(e) if e.kind() == io::ErrorKind::NotFound => parent = parent.parent().ok_or(e)?,
            Err(e) => return Err(e),
        }
    }
}

struct Capture {
    reader: UnixStream,
    bytes: Vec<u8>,
}
impl Capture {
    fn pipe() -> io::Result<(Self, Stdio)> {
        let (reader, writer) = UnixStream::pair()?;
        reader.set_nonblocking(true)?;
        Ok((
            Self {
                reader,
                bytes: Vec::new(),
            },
            Stdio::from(OwnedFd::from(writer)),
        ))
    }
    fn drain(&mut self) -> io::Result<bool> {
        let mut buf = [0; 8192];
        loop {
            match self.reader.read(&mut buf) {
                Ok(0) => return Ok(true),
                Ok(n) => {
                    if self.bytes.len() + n > 8 << 20 {
                        return Err(io::Error::other("exceeded Git inventory output limit"));
                    }
                    self.bytes.extend_from_slice(&buf[..n]);
                }
                Err(e) if e.kind() == io::ErrorKind::WouldBlock => return Ok(false),
                Err(e) if e.kind() == io::ErrorKind::Interrupted => continue,
                Err(e) => return Err(e),
            }
        }
    }
}
fn git(dir: &Path, args: &[&str], deadline: Instant) -> io::Result<Vec<u8>> {
    if Instant::now() >= deadline {
        return Err(io::Error::new(
            io::ErrorKind::TimedOut,
            "Git inventory deadline reached",
        ));
    }
    let (mut stdout, out) = Capture::pipe()?;
    let (mut stderr, err) = Capture::pipe()?;
    let mut child = Command::new("git")
        .current_dir(dir)
        .args(args)
        .env_clear()
        .envs(std::env::vars_os().filter(|(k, _)| !k.to_string_lossy().starts_with("GIT_")))
        .env("GIT_OPTIONAL_LOCKS", "0")
        .env("GIT_CONFIG_NOSYSTEM", "1")
        .env("GIT_CONFIG_GLOBAL", "/dev/null")
        .env("GIT_TERMINAL_PROMPT", "0")
        .env("LC_ALL", "C")
        .stdin(Stdio::null())
        .stdout(out)
        .stderr(err)
        .spawn()?;
    let result = (|| {
        loop {
            let finished = stdout.drain()? & stderr.drain()?;
            if finished && let Some(status) = child.try_wait()? {
                if status.success() {
                    return Ok(stdout.bytes);
                }
                return Err(io::Error::other(format!(
                    "{status}: {}",
                    String::from_utf8_lossy(&stderr.bytes).trim()
                )));
            }
            if Instant::now() >= deadline {
                return Err(io::Error::new(
                    io::ErrorKind::TimedOut,
                    "Git inventory deadline reached",
                ));
            }
            std::thread::sleep(Duration::from_millis(5));
        }
    })();
    if result.is_err() {
        let _ = child.kill();
        let _ = child.wait();
    }
    result
}
fn git_path(dir: &Path, args: &[&str], deadline: Instant) -> io::Result<PathBuf> {
    let output = git(dir, args, deadline)?;
    let path = Path::new(
        std::str::from_utf8(output.strip_suffix(b"\n").unwrap_or(&output))
            .map_err(io::Error::other)?,
    );
    if !path.is_absolute() {
        return Err(io::Error::other("received a non-absolute Git path"));
    }
    canonical(path)
}

#[derive(Clone)]
struct Registration {
    path: PathBuf,
    kind: &'static str,
    head: String,
    branch: String,
    locked: bool,
    prunable: bool,
}
fn registrations(dir: &Path, deadline: Instant) -> io::Result<Vec<Registration>> {
    let (bytes, sep) = match git(dir, &["worktree", "list", "--porcelain", "-z"], deadline) {
        Ok(bytes) => (bytes, '\0'),
        Err(e) if e.to_string().contains("unknown switch `z'") => (
            git(dir, &["worktree", "list", "--porcelain"], deadline)?,
            '\n',
        ),
        Err(e) => return Err(e),
    };
    let raw = std::str::from_utf8(&bytes).map_err(io::Error::other)?;
    let delimiter = sep.to_string().repeat(2);
    let records = raw
        .strip_suffix(delimiter.as_str())
        .ok_or_else(|| io::Error::other("incomplete Git worktree registrations"))?;
    let mut paths = BTreeSet::new();
    let mut found = Vec::new();
    for entry in records.split(delimiter.as_str()) {
        let mut fields = entry.split(sep);
        let raw = fields
            .next()
            .and_then(|first| first.strip_prefix("worktree "))
            .ok_or_else(|| io::Error::other("invalid Git worktree path"))?;
        let path = if sep == '\n' && raw.starts_with('"') {
            quoted_git_path(raw)?
        } else {
            PathBuf::from(raw)
        };
        if !path.is_absolute() || found.len() >= 1024 || !paths.insert(path.clone()) {
            return Err(io::Error::other(
                "duplicate, non-absolute or excessive Git worktree registration",
            ));
        }
        let mut row = Registration {
            path,
            kind: if found.is_empty() {
                "primary"
            } else {
                "linked"
            },
            head: String::new(),
            branch: String::new(),
            locked: false,
            prunable: false,
        };
        let mut seen = BTreeSet::new();
        for line in fields {
            let (key, val) = line.split_once(' ').unwrap_or((line, ""));
            if !seen.insert(key) {
                return Err(io::Error::other("duplicate Git worktree field"));
            }
            match key {
                "bare" => row.kind = "bare",
                "HEAD" => row.head = val.into(),
                "branch" => row.branch = val.into(),
                "locked" => row.locked = true,
                "prunable" => row.prunable = true,
                "detached" => {}
                _ => {
                    return Err(io::Error::other(format!(
                        "unrecognized Git worktree field {key:?}"
                    )));
                }
            }
        }
        if row.kind != "bare"
            && (row.head.is_empty() || seen.contains("detached") && seen.contains("branch"))
        {
            return Err(io::Error::other("incomplete Git worktree registration"));
        }
        found.push(row);
    }
    Ok(found)
}
fn quoted_git_path(raw: &str) -> io::Result<PathBuf> {
    let malformed = || io::Error::other("ambiguous quoted Git worktree path");
    let body = raw
        .strip_prefix('"')
        .and_then(|s| s.strip_suffix('"'))
        .ok_or_else(malformed)?;
    let mut input = body.bytes();
    let mut out = Vec::new();
    while let Some(ch) = input.next() {
        if ch != b'\\' {
            out.push(ch);
            continue;
        }
        let ch = input.next().ok_or_else(malformed)?;
        out.push(match ch {
            b'\\' | b'"' => ch,
            b't' => b'\t',
            b'n' => b'\n',
            b'r' => b'\r',
            b'a' => 7,
            b'b' => 8,
            b'f' => 12,
            b'v' => 11,
            b'0'..=b'3' => {
                let octal = |b: u8| (b'0'..=b'7').contains(&b);
                let second = input.next().filter(|b| octal(*b)).ok_or_else(malformed)? - b'0';
                let third = input.next().filter(|b| octal(*b)).ok_or_else(malformed)? - b'0';
                (ch - b'0') * 64 + second * 8 + third
            }
            _ => return Err(malformed()),
        });
    }
    Ok(PathBuf::from(
        String::from_utf8(out).map_err(|_| malformed())?,
    ))
}
fn verify_registration(row: &Registration, common: &Path, timeout: Instant) -> bool {
    if row.kind == "bare" {
        return canonical(&row.path).is_ok_and(|v| v == common);
    }
    let top = git_path(&row.path, &["rev-parse", "--show-toplevel"], timeout);
    if !top.is_ok_and(|v| v == row.path) {
        return false;
    }
    if row.kind != "linked" {
        return true;
    }
    let Ok(gitdir) = git_path(&row.path, &["rev-parse", "--absolute-git-dir"], timeout) else {
        return false;
    };
    let read = || -> io::Result<bool> {
        let file = OpenOptions::new()
            .read(true)
            .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK)
            .open(gitdir.join("gitdir"))?;
        if !file.metadata()?.is_file() {
            return Ok(false);
        }
        let mut data = String::new();
        file.take(65537).read_to_string(&mut data)?;
        if data.len() > 65536 {
            return Ok(false);
        }
        Ok(
            canonical(&gitdir.join(data.strip_suffix('\n').unwrap_or(&data)))?
                == canonical(&row.path.join(".git"))?,
        )
    };
    read().unwrap_or(false)
}
fn apply(item: &mut Checkout, row: &Registration, common: &Path, repo: &Path) {
    item.repo = repo.to_path_buf();
    item.git_common_dir = common.to_path_buf();
    item.worktree = row.path.clone();
    item.git_kind = row.kind.into();
    item.head = row.head.clone();
    item.branch = row.branch.clone();
    item.locked = row.locked;
    item.prunable = row.prunable;
}
fn common_dir(dir: &Path, deadline: Instant) -> io::Result<PathBuf> {
    git_path(
        dir,
        &["rev-parse", "--path-format=absolute", "--git-common-dir"],
        deadline,
    )
}
pub(super) fn inspect(
    inputs: Vec<PathBuf>,
    deadline: Instant,
    errors: &mut Vec<String>,
) -> Vec<Checkout> {
    let mut items = BTreeMap::<PathBuf, Checkout>::new();
    let mut repos = BTreeMap::<PathBuf, Vec<Registration>>::new();
    for (n, input) in inputs.into_iter().enumerate() {
        if n >= 128 || items.len() >= 1024 || Instant::now() >= deadline {
            errors.push("checkout path discovery limit reached".into());
            break;
        }
        let path = match canonical(&input) {
            Ok(v) => v,
            Err(e) => {
                let mut row = Checkout::new(input.clone());
                row.unknowns.push(e.to_string());
                items.insert(input, row);
                continue;
            }
        };
        let mut item = Checkout::new(path.clone());
        let Some(dir) = path.ancestors().find(|p| p.is_dir()) else {
            item.unknowns
                .push("cannot find an accessible directory for Git lookup".into());
            items.insert(path, item);
            continue;
        };
        let common = match common_dir(dir, deadline) {
            Ok(dir) => dir,
            Err(e) => {
                if e.to_string().contains("not a git repository") {
                    item.git_kind = "outside_git".into()
                } else {
                    item.unknowns
                        .push(format!("cannot verify Git registration: {e}"))
                }
                items.insert(path, item);
                continue;
            }
        };
        if !repos.contains_key(&common) {
            let list = match registrations(dir, deadline) {
                Ok(rows) => rows,
                Err(e) => {
                    item.git_common_dir = common;
                    item.unknowns
                        .push(format!("cannot verify Git worktrees: {e}"));
                    items.insert(path, item);
                    continue;
                }
            };
            for row in &list {
                if items.len() >= 1024 || Instant::now() >= deadline {
                    errors.push("Git worktree discovery limit reached".into());
                    break;
                }
                let mut linked = Checkout::new(row.path.clone());
                apply(&mut linked, row, &common, &list[0].path);
                linked.unverified = row.prunable || linked.path_state != "directory";
                if row.prunable {
                    linked
                        .unknowns
                        .push("Git marks this registration prunable".into());
                }
                if linked.path_state == "directory"
                    && !(common_dir(&row.path, deadline).is_ok_and(|v| v == common)
                        && verify_registration(row, &common, deadline))
                {
                    linked.unverified = true;
                    linked.unknowns.push(
                        "checkout does not prove it still belongs to the registered repository"
                            .into(),
                    );
                }
                if let Some(prev) = items.remove(&row.path) {
                    linked.unknowns.extend(prev.unknowns);
                    if !prev.git_common_dir.as_os_str().is_empty() && prev.git_common_dir != common
                    {
                        linked.unverified = true;
                        linked.git_kind = "ambiguous".into();
                        linked.unknowns.push("conflicting Git registrations".into());
                    }
                }
                items.insert(row.path.clone(), linked);
            }
            repos.insert(common.clone(), list);
        }
        let list = &repos[&common];
        if list.iter().any(|r| r.path == path) && items.contains_key(&path) {
            continue;
        }
        item.repo = list[0].path.clone();
        item.git_common_dir = common.clone();
        if let Some(row) = list
            .iter()
            .filter(|r| {
                path.starts_with(&r.path) && items.get(&r.path).is_some_and(|v| !v.unverified)
            })
            .max_by_key(|r| r.path.as_os_str().len())
        {
            apply(&mut item, row, &common, &list[0].path)
        } else {
            item.unknowns
                .push("path is not within a registered worktree".into());
        }
        items.insert(path, item);
    }
    items.into_values().collect()
}
