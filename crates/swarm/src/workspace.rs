//! A session owns one Git registration, with the source repository and branch recorded before launch.
use crate::supervisor::runtime;
use serde_json::{Value, json};
use std::ffi::OsStr;
use std::fs;
use std::io::{self, Read, Write};
use std::os::unix::{
    ffi::OsStrExt,
    fs::{MetadataExt, OpenOptionsExt},
};
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
        .env_remove("GIT_IMPLICIT_WORK_TREE")
        .env_remove("GIT_INDEX_FILE")
        .env_remove("GIT_PREFIX")
        .env_remove("GIT_INTERNAL_SUPER_PREFIX")
        .env_remove("GIT_OBJECT_DIRECTORY")
        .env_remove("GIT_ALTERNATE_OBJECT_DIRECTORIES")
        .env_remove("GIT_GRAFT_FILE")
        .env_remove("GIT_SHALLOW_FILE")
        .env_remove("GIT_CONFIG")
        .env_remove("GIT_CONFIG_PARAMETERS")
        .env_remove("GIT_CONFIG_COUNT")
        .env_remove("GIT_NO_REPLACE_OBJECTS")
        .env_remove("GIT_REPLACE_REF_BASE")
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

fn git_common(repo: &Path) -> io::Result<PathBuf> {
    let path = git(
        repo,
        ["rev-parse", "--path-format=absolute", "--git-common-dir"],
    )?;
    Path::new(OsStr::from_bytes(&path)).canonicalize()
}

fn contains_directory(parent: &Path, candidate: &Path) -> bool {
    candidate.starts_with(parent)
        || parent.metadata().is_ok_and(|expected| {
            candidate.ancestors().any(|ancestor| {
                ancestor.metadata().is_ok_and(|actual| {
                    (actual.dev(), actual.ino()) == (expected.dev(), expected.ino())
                })
            })
        })
}

pub(crate) fn destination_root(repo: &Path) -> io::Result<PathBuf> {
    let common = git_common(repo)?;
    let primary = registered_paths(repo)?[0].canonicalize()?;
    if primary != common
        && (git_common(&primary)? != common
            || git(&primary, ["rev-parse", "--show-toplevel"])? != primary.as_os_str().as_bytes())
    {
        return Err(io::Error::other("worktree: cannot verify primary checkout"));
    }

    let mut root = primary.clone();
    for segment in [".swarm", "worktrees"] {
        root.push(segment);
        match root.symlink_metadata() {
            Ok(info) if info.is_symlink() => {
                root = root.canonicalize()?;
                if !root.metadata()?.is_dir() {
                    return Err(io::Error::other(
                        "worktree: destination alias is not a directory",
                    ));
                }
            }
            Ok(info) if info.is_dir() => {}
            Ok(_) => return Err(io::Error::other("worktree: destination is not a directory")),
            Err(error) if error.kind() == io::ErrorKind::NotFound => {}
            Err(error) => return Err(error),
        }
    }
    let aliased = root != primary.join(".swarm/worktrees");
    if aliased
        && (contains_directory(&root, &primary)
            || contains_directory(&root, &common)
            || contains_directory(&common, &root))
    {
        return Err(io::Error::other(
            "worktree: destination alias overlaps repository custody",
        ));
    }
    for ancestor in root.ancestors() {
        match ancestor.join(".git").symlink_metadata() {
            Ok(marker) if aliased || !marker.is_dir() => {
                let owner = git_common(ancestor)?;
                if git(ancestor, ["rev-parse", "--absolute-git-dir"])?
                    != owner.as_os_str().as_bytes()
                    || (aliased && owner != common)
                {
                    return Err(io::Error::other(
                        "worktree: destination lies inside another checkout",
                    ));
                }
            }
            Ok(_) => {}
            Err(error) if error.kind() == io::ErrorKind::NotFound => {}
            Err(error) => return Err(error),
        }
    }
    Ok(root)
}

pub(crate) fn claim(
    repo: &Path,
    id: &str,
    label: &str,
    allow: impl Fn(&Path) -> io::Result<()>,
) -> io::Result<Value> {
    if !runtime::valid_id(id) || !is_repo(repo) {
        return Err(io::Error::other(
            "worktree: expected a Git work tree and a valid Swarm session",
        ));
    }
    let parent = destination_root(repo)?;
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
    let common = git_common(repo)?;
    let base = String::from_utf8(git(repo, ["rev-parse", "--verify", "HEAD^{commit}"])?)
        .map_err(io::Error::other)?;
    let mut path = parent.join(name);
    let authorize = |path: &Path| {
        allow(path)?;
        if git_common(repo)? != common || destination_root(repo)? != parent {
            return Err(io::Error::other(
                "worktree: destination changed during reservation",
            ));
        }
        Ok(())
    };
    // The persistent reservation stores the actual mkdir winner across supervisors.
    authorize(&path)?;
    if parent.to_str().is_none() || common.to_str().is_none() {
        return Err(io::Error::other(
            "worktree: cannot persist non-UTF-8 checkout coordinates",
        ));
    }
    fs::create_dir_all(&parent)?;
    if parent.canonicalize()? != parent {
        return Err(io::Error::other(
            "worktree: destination parent was redirected",
        ));
    }
    match fs::create_dir(&path) {
        Err(error) if error.kind() == io::ErrorKind::AlreadyExists && name != branch => {
            path = parent.join(&branch);
            authorize(&path)?;
            fs::create_dir(&path)?;
        }
        result => result?,
    }
    let info = path.symlink_metadata()?;
    let record = json!({"path":path,"root":parent,"claim":[info.dev(),info.ino()],
        "ref":format!("refs/heads/swarm/{branch}"),"common":common,"base":base});
    if !claim_matches(&path, &record).unwrap_or(false) {
        release_claim(&record);
        return Err(io::Error::other(
            "worktree: claimed destination was redirected",
        ));
    }
    Ok(record)
}

fn claim_matches(path: &Path, record: &Value) -> io::Result<bool> {
    let info = path.symlink_metadata()?;
    Ok(info.is_dir()
        && record["claim"] == json!([info.dev(), info.ino()])
        && path.canonicalize()? == path)
}

pub(crate) fn release_claim(record: &Value) {
    if let Some(path) = record["path"].as_str().map(Path::new)
        && claim_matches(path, record).unwrap_or(false)
    {
        let _ = fs::remove_dir(path);
    }
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
    {
        return Err(io::Error::other(
            "worktree: recorded checkout is outside session custody",
        ));
    }
    let common = if record["claim"].is_null() {
        if !record["root"].is_null() || path.parent() != Some(&checked_parent(repo)?) {
            return Err(io::Error::other(
                "worktree: invalid legacy checkout location",
            ));
        }
        git(
            repo,
            ["rev-parse", "--path-format=absolute", "--git-common-dir"],
        )?
    } else {
        let parent = destination_root(repo)?;
        if path.parent() != Some(&parent) || record["root"].as_str().map(Path::new) != Some(&parent)
        {
            return Err(io::Error::other("worktree: recorded destination changed"));
        }
        git_common(repo)?.as_os_str().as_bytes().to_vec()
    };
    if record["common"].as_str().map(str::as_bytes) != Some(common.as_slice()) {
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

fn owner_marker(path: &Path) -> io::Result<PathBuf> {
    let admin = git(path, ["rev-parse", "--absolute-git-dir"])?;
    let admin = Path::new(OsStr::from_bytes(&admin));
    if admin.parent() != Some(&git_common(path)?.join("worktrees"))
        || admin.canonicalize()? != admin
    {
        return Err(io::Error::other(
            "worktree: unexpected Git administrative directory",
        ));
    }
    Ok(admin.join("swarm-session"))
}

fn check_owner(path: &Path, id: &str, repair: bool) -> io::Result<()> {
    let marker = owner_marker(path)?;
    match fs::OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK)
        .open(&marker)
    {
        Ok(mut file) if file.metadata()?.is_file() && file.metadata()?.len() == id.len() as u64 => {
            let mut owner = vec![0; id.len()];
            file.read_exact(&mut owner)?;
            let mut trailing = [0];
            if owner != id.as_bytes() || file.read(&mut trailing)? != 0 {
                return Err(io::Error::other("worktree: checkout has another owner"));
            }
            Ok(())
        }
        Err(error) if error.kind() == io::ErrorKind::NotFound && repair => {
            let mut file = fs::OpenOptions::new()
                .write(true)
                .create_new(true)
                .mode(0o600)
                .open(marker)?;
            file.write_all(id.as_bytes())?;
            file.sync_all()
        }
        _ => Err(io::Error::other(
            "worktree: checkout owner record is unavailable",
        )),
    }
}

pub(crate) fn create(repo: &Path, id: &str, record: &Value) -> io::Result<()> {
    let (path, reference) = coordinates(repo, id, record)?;
    let claimed = !record["claim"].is_null();
    if claimed {
        if !claim_matches(path, record)? {
            return Err(io::Error::other("worktree: reservation directory changed"));
        }
        if registered(repo, path, reference)? {
            return check_owner(path, id, true);
        }
        if fs::read_dir(path)?.next().transpose()?.is_some() {
            return Err(io::Error::other(
                "worktree: reservation directory is not empty",
            ));
        }
    } else {
        match path.symlink_metadata() {
            Ok(_) => return Err(io::Error::other("worktree: destination already exists")),
            Err(error) if error.kind() == io::ErrorKind::NotFound => {}
            Err(error) => return Err(error),
        }
    }
    let branch = reference.strip_prefix("refs/heads/").unwrap();
    let base = record["base"]
        .as_str()
        .filter(|s| matches!(s.len(), 40 | 64) && s.bytes().all(|b| b.is_ascii_hexdigit()))
        .ok_or_else(|| io::Error::other("worktree: invalid base commit"))?;
    let result = git(
        repo,
        [
            OsStr::new("worktree"),
            OsStr::new("add"),
            OsStr::new("-b"),
            OsStr::new(branch),
            path.as_os_str(),
            OsStr::new(base),
        ],
    );
    if let Err(error) = result {
        release_claim(record);
        return Err(error);
    }
    if !registered(repo, path, reference)? {
        return Err(io::Error::other(
            "worktree: Git registration unavailable after creation",
        ));
    }
    if claimed {
        if !claim_matches(path, record)? {
            return Err(io::Error::other(
                "worktree: checkout directory changed after creation",
            ));
        }
        check_owner(path, id, true)?;
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
    for (index, registered) in registered_paths(repo)?.into_iter().enumerate() {
        if registered == path && index != 0 {
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
    let repo = if !record["claim"].is_null()
        && repo
            .metadata()
            .is_err_and(|error| error.kind() == io::ErrorKind::NotFound)
    {
        let path = record["path"].as_str().map(Path::new).ok_or_else(|| {
            io::Error::other("worktree: missing checkout after source disappeared")
        })?;
        verify_claim_owner(path, id, record)?;
        path
    } else {
        repo
    };
    let (path, reference) = coordinates(repo, id, record)?;
    let owned = if record["claim"].is_null() {
        registered(repo, path, reference)?
    } else {
        match owner_marker(path) {
            Ok(marker) if marker.symlink_metadata().is_ok() => {
                verify_claim_owner(path, id, record)?;
                if git_common(path)? != git_common(repo)?
                    || git(path, ["rev-parse", "--show-toplevel"])? != path.as_os_str().as_bytes()
                {
                    return Err(io::Error::other(
                        "worktree: owned checkout identity changed",
                    ));
                }
                true
            }
            _ => {
                // A claim can exist before Git adds it. Never empty or unregister
                // a checkout that got as far as registration without its marker.
                if registered(repo, path, reference)? {
                    return Err(io::Error::other(
                        "worktree: no owner record; checkout retained",
                    ));
                }
                false
            }
        }
    };
    if owned {
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
    } else {
        release_claim(record);
        match path.symlink_metadata() {
            Ok(_) => return Err(io::Error::other("worktree: unregistered path retained")),
            Err(error) if error.kind() == io::ErrorKind::NotFound => {}
            Err(error) => return Err(error),
        }
    }
    Ok(())
}

fn verify_claim_owner(path: &Path, id: &str, record: &Value) -> io::Result<()> {
    if !claim_matches(path, record)? {
        return Err(io::Error::other("worktree: checkout directory changed"));
    }
    check_owner(path, id, false)
}

pub(crate) fn repair_reservation(repo: &Path, id: &str, record: &Value) -> io::Result<()> {
    if record["claim"].is_null() {
        return Ok(());
    }
    let path = record["path"]
        .as_str()
        .map(Path::new)
        .ok_or_else(|| io::Error::other("worktree: missing checkout path"))?;
    match path.join(".git").symlink_metadata() {
        Ok(_) => {}
        Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(()),
        Err(error) => return Err(error),
    }
    let marker = owner_marker(path)?;
    match marker.symlink_metadata() {
        Ok(_) => return Ok(()),
        Err(error) if error.kind() == io::ErrorKind::NotFound => {}
        Err(error) => return Err(error),
    }
    let (path, reference) = coordinates(repo, id, record)?;
    if !claim_matches(path, record)? || !registered(repo, path, reference)? {
        return Err(io::Error::other(
            "worktree: cannot prove incomplete ownership record",
        ));
    }
    check_owner(path, id, true)
}

pub(crate) fn verify(repo: &Path, id: &str, record: &Value) -> io::Result<()> {
    let (path, reference) = coordinates(repo, id, record)?;
    if !registered(repo, path, reference)? {
        return Err(io::Error::other(
            "worktree: reserved checkout is not registered",
        ));
    }
    if !record["claim"].is_null() {
        verify_claim_owner(path, id, record)?;
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
    fn sibling_reservations_claim_collisions_and_repair_exact_checkout() {
        let fixture = crate::test_temp::root()
            .canonicalize()
            .unwrap()
            .join(format!("placement-{}", runtime::random_id().unwrap()));
        fs::create_dir(&fixture).unwrap();
        let root = fixture.join("primary");
        repository(&root);
        let first_id = "0123456789abcdef01234567";
        let second_id = "0123456789abcdef01234568";
        let first = claim(&root, first_id, "Same name", |_| Ok(())).unwrap();
        create(&root, first_id, &first).unwrap();
        let source = Path::new(first["path"].as_str().unwrap());
        let subdir = source.join("subdir");
        fs::create_dir(&subdir).unwrap();
        assert_eq!(
            destination_root(&subdir).unwrap(),
            root.join(".swarm/worktrees")
        );
        let second = claim(&subdir, second_id, "Same name", |_| Ok(())).unwrap();
        let target = Path::new(second["path"].as_str().unwrap());
        assert_eq!(
            target,
            root.join(format!(".swarm/worktrees/same-name-{second_id}"))
        );
        create(&subdir, second_id, &second).unwrap();
        let marker = owner_marker(target).unwrap();
        fs::remove_file(&marker).unwrap();
        assert!(verify(&subdir, second_id, &second).is_err());
        repair_reservation(&subdir, second_id, &second).unwrap();
        verify(&subdir, second_id, &second).unwrap();
        assert_eq!(fs::read(&marker).unwrap(), second_id.as_bytes());
        fs::write(&marker, first_id).unwrap();
        assert!(create(&subdir, second_id, &second).is_err());
        assert!(remove(&subdir, second_id, &second).is_err());
        fs::write(&marker, second_id).unwrap();
        git(target, ["checkout", "--detach"]).unwrap();
        remove(&root, first_id, &first).unwrap();
        remove(&subdir, second_id, &second).unwrap();
        assert!(!target.exists());

        let aliases = fixture.join("aliases");
        fs::create_dir(&aliases).unwrap();
        fs::remove_dir(root.join(".swarm/worktrees")).unwrap();
        symlink(&aliases, root.join(".swarm/worktrees")).unwrap();
        assert_eq!(destination_root(&root).unwrap(), aliases);
        assert!(
            claim(&root, second_id, "Denied", |_| Err(io::Error::other(
                "out of scope"
            )))
            .is_err()
        );
        assert!(fs::read_dir(&aliases).unwrap().next().is_none());
        fs::remove_file(root.join(".swarm/worktrees")).unwrap();
        symlink(root.join(".git"), root.join(".swarm/worktrees")).unwrap();
        assert!(destination_root(&root).is_err());
        fs::remove_dir_all(fixture).unwrap();
    }

    #[test]
    fn removal_retains_nested_git_and_uncertain_registrations() {
        let root = crate::test_temp::root()
            .canonicalize()
            .unwrap()
            .join(format!("worktree-{}", runtime::random_id().unwrap()));
        repository(&root);
        let id = "0123456789abcdef01234567";
        let record = claim(&root, id, "outer", |_| Ok(())).unwrap();
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
