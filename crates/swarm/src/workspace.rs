//! A session owns one Git registration, with the source repository and branch recorded before launch.
use crate::supervisor::runtime;
use serde_json::{Value, json};
use std::ffi::OsStr;
use std::fs;
use std::io;
use std::os::unix::{ffi::OsStrExt, fs::MetadataExt};
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
        .env_remove("GIT_COMMON_DIR")
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

fn unquote_registration(raw: &[u8]) -> Option<Vec<u8>> {
    let mut input = raw
        .strip_prefix(b"\"")?
        .strip_suffix(b"\"")?
        .iter()
        .copied();
    let mut path = Vec::with_capacity(raw.len());
    while let Some(byte) = input.next() {
        path.push(match byte {
            b'"' | 0 => return None,
            b'\\' => match input.next()? {
                b'a' => 7,
                b'b' => 8,
                b't' => b'\t',
                b'n' => b'\n',
                b'v' => 11,
                b'f' => 12,
                b'r' => b'\r',
                byte @ (b'"' | b'\\') => byte,
                first @ b'0'..=b'3' => {
                    let second = input.next()?;
                    let third = input.next()?;
                    if !(b'0'..=b'7').contains(&second) || !(b'0'..=b'7').contains(&third) {
                        return None;
                    }
                    let decoded = (first - b'0') * 64 + (second - b'0') * 8 + (third - b'0');
                    if decoded == 0 {
                        return None;
                    }
                    decoded
                }
                _ => return None,
            },
            _ => byte,
        });
    }
    Some(path)
}

fn registered_paths(repo: &Path) -> io::Result<Vec<PathBuf>> {
    let (listing, separator) = match git(repo, ["worktree", "list", "--porcelain", "-z"]) {
        Ok(listing) => (listing, 0),
        Err(error) if error.to_string().contains("unknown switch `z'") => {
            (git(repo, ["worktree", "list", "--porcelain"])?, b'\n')
        }
        Err(error) => return Err(error),
    };
    let paths = listing
        .split(|byte| *byte == separator)
        .filter_map(|field| field.strip_prefix(b"worktree "))
        .map(|raw| {
            let decoded;
            let raw = if separator == b'\n' && raw.starts_with(b"\"") {
                decoded = unquote_registration(raw)
                    .ok_or_else(|| io::Error::other("worktree: malformed Git registration path"))?;
                decoded.as_slice()
            } else {
                raw
            };
            let path = PathBuf::from(OsStr::from_bytes(raw));
            if !path.is_absolute() {
                return Err(io::Error::other(
                    "worktree: ambiguous Git registration path",
                ));
            }
            Ok(path)
        })
        .collect::<io::Result<Vec<_>>>()?;
    if paths.is_empty() {
        return Err(io::Error::other("worktree: missing Git registrations"));
    }
    Ok(paths)
}

fn check_remove(repo: &Path, path: &Path) -> io::Result<()> {
    let root = path.metadata()?;
    let retained = |child: &Path| {
        io::Error::other(format!(
            "worktree: nested Git checkout at {}; outer checkout retained",
            child.display()
        ))
    };
    let mut owners = 0;
    for registered in registered_paths(repo)? {
        if registered == path {
            owners += 1;
            continue;
        }
        if registered.starts_with(path) {
            return Err(retained(&registered));
        }
        // This also detects registered paths through aliases or alternate
        // case spellings, including a registration whose checkout is missing.
        for ancestor in registered.ancestors() {
            match ancestor.metadata() {
                Ok(info) if (info.dev(), info.ino()) == (root.dev(), root.ino()) => {
                    return Err(retained(&registered));
                }
                Ok(_) => {}
                Err(error) if error.kind() == io::ErrorKind::NotFound => {}
                Err(error) => return Err(error),
            }
        }
    }
    if owners != 1 {
        return Err(io::Error::other(
            "worktree: ambiguous outer Git registration; checkout retained",
        ));
    }
    let mut directories = vec![path.to_owned()];
    while let Some(directory) = directories.pop() {
        if directory != path {
            match directory.join(".git").symlink_metadata() {
                Ok(_) => return Err(retained(&directory)),
                Err(error) if error.kind() == io::ErrorKind::NotFound => {}
                Err(error) => return Err(error),
            }
        }
        for entry in fs::read_dir(&directory)? {
            let entry = entry?;
            if entry.file_type()?.is_dir() {
                directories.push(entry.path());
            }
        }
    }
    Ok(())
}

pub(crate) fn remove(repo: &Path, id: &str, record: &Value) -> io::Result<()> {
    let (path, reference) = coordinates(repo, id, record)?;
    if registered(repo, path, reference)? {
        check_remove(repo, path)?;
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

#[cfg(test)]
mod tests {
    use super::*;
    use std::os::unix::fs::symlink;

    fn repository(path: &Path) {
        fs::create_dir(path).unwrap();
        git(path, ["init"]).unwrap();
        git(
            path,
            [
                "-c",
                "user.name=Swarm Fixture",
                "-c",
                "user.email=fixture@example.invalid",
                "-c",
                "core.hooksPath=/dev/null",
                "commit",
                "--allow-empty",
                "-m",
                "fixture",
            ],
        )
        .unwrap();
    }

    #[test]
    fn git_registration_c_quotes_preserve_raw_path_bytes() {
        assert_eq!(
            unquote_registration(br#""/path\twith\\\"\377""#).unwrap(),
            b"/path\twith\\\"\xff"
        );
        for invalid in [
            br#""/path\x""#.as_slice(),
            br#""/path\000""#,
            br#""/path\37""#,
            br#""/path"#,
            br#""/path"tail""#,
        ] {
            assert!(unquote_registration(invalid).is_none());
        }
    }

    #[test]
    fn removal_retains_nested_git_and_uncertain_registrations() {
        let root = crate::test_temp::root()
            .canonicalize()
            .unwrap()
            .join(format!("worktree-{}", runtime::random_id().unwrap()));
        repository(&root);
        let id = "0123456789abcdef01234567";
        let record = plan(&root, id, "outer").unwrap();
        create(&root, id, &record).unwrap();
        let path = Path::new(record["path"].as_str().unwrap());

        let independent = path.join("independent");
        repository(&independent);
        fs::write(independent.join("kept"), "Nested local work").unwrap();
        assert!(remove(&root, id, &record).is_err());
        assert!(independent.join("kept").is_file());
        let moved = root.join("independent");
        fs::rename(independent, &moved).unwrap();

        let questionable = path.join("questionable");
        fs::create_dir(&questionable).unwrap();
        symlink("missing-git-directory", questionable.join(".git")).unwrap();
        assert!(remove(&root, id, &record).is_err());
        assert!(
            questionable
                .join(".git")
                .symlink_metadata()
                .unwrap()
                .is_symlink()
        );
        fs::rename(questionable, root.join("questionable")).unwrap();

        let linked = path.join("linked\tnested");
        git(
            &root,
            [
                OsStr::new("worktree"),
                OsStr::new("add"),
                OsStr::new("--detach"),
                linked.as_os_str(),
            ],
        )
        .unwrap();
        fs::write(linked.join("kept"), "Nested linked work").unwrap();
        assert!(remove(&root, id, &record).is_err());
        let gitfile = linked.join(".git");
        let marker = root.join("saved-gitfile");
        fs::rename(&gitfile, &marker).unwrap();
        assert!(remove(&root, id, &record).is_err());
        assert!(linked.join("kept").is_file());
        fs::rename(marker, gitfile).unwrap();
        git(
            &root,
            [
                "worktree",
                "move",
                linked.to_str().unwrap(),
                root.join("linked\tsibling").to_str().unwrap(),
            ],
        )
        .unwrap();

        symlink(&moved, path.join("external-alias")).unwrap();
        fs::write(path.join("ordinary-untracked"), "Plain local data").unwrap();
        remove(&root, id, &record).unwrap();
        assert!(!path.exists());
        assert_eq!(
            fs::read_to_string(moved.join("kept")).unwrap(),
            "Nested local work"
        );
        assert!(root.join("linked\tsibling/kept").is_file());
        fs::remove_dir_all(root).unwrap();
    }
}
