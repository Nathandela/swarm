//! Resolve the pinned media bundle before the connector trusts a media process.
use super::config::invalid;
use sha2::{Digest, Sha256};
use std::collections::BTreeMap;
use std::fs::{self, File, Metadata, OpenOptions};
use std::io::{self, Read};
use std::os::fd::AsRawFd;
use std::os::unix::fs::{MetadataExt, OpenOptionsExt};
use std::os::unix::process::CommandExt;
use std::path::{Path, PathBuf};
use std::process::Command;

const ENTRY: &str = "swarm-discord-voice";
const SOURCE: &str = include_str!("../../../../services/discord-voice/swarm-discord-voice");
const MEDIA: &str = include_str!("../../../../services/discord-voice/media.mjs");
const MEDIA_IMPORT: &str = "import { daveState, frameBytes, frameMS, frameSamples, guardReceiver, maxLine, pcmFrame } from './media.mjs';";
const ARTIFACTS: [(&str, &[u8]); 4] = [
    (ENTRY, SOURCE.as_bytes()),
    ("media.mjs", MEDIA.as_bytes()),
    (
        "package.json",
        include_bytes!("../../../../services/discord-voice/package.json"),
    ),
    (
        "package-lock.json",
        include_bytes!("../../../../services/discord-voice/package-lock.json"),
    ),
];
const NODE_PATHS: [&str; 3] = [
    "/usr/local/bin/node",
    "/opt/homebrew/bin/node",
    "/usr/bin/node",
];

#[derive(PartialEq)]
struct Identity(u64, u64, u64, i64, i64, i64, i64);

impl Identity {
    fn of(meta: &Metadata) -> Self {
        Self(
            meta.dev(),
            meta.ino(),
            meta.len(),
            meta.ctime(),
            meta.ctime_nsec(),
            meta.mtime(),
            meta.mtime_nsec(),
        )
    }
}

pub(super) enum Helper {
    Pinned(Box<Pinned>),
    #[cfg(test)]
    Fixture(PathBuf),
}

pub(super) struct Pinned {
    path: PathBuf,
    root: PathBuf,
    modules: Vec<(PathBuf, Identity)>,
    root_identity: Identity,
    artifacts: Vec<Identity>,
    node: PathBuf,
    node_identity: Identity,
}

impl Helper {
    pub(super) fn open(path: &Path) -> io::Result<Self> {
        #[cfg(test)]
        if path.file_name().is_some_and(|name| name == "helper") {
            return Ok(Self::Fixture(path.into()));
        }
        let node = NODE_PATHS
            .into_iter()
            .find_map(|path| fs::canonicalize(path).ok())
            .ok_or_else(|| invalid("Node 24.17+ was not found on the call helper PATH"))?;
        Self::pinned(path, node)
    }

    pub(super) fn bundled() -> io::Result<Self> {
        let exe = fs::canonicalize(std::env::current_exe()?)?;
        let platform = format!("{}-{}", std::env::consts::OS, std::env::consts::ARCH);
        let pins: BTreeMap<String, BTreeMap<String, String>> =
            serde_json::from_slice(crate::release::MEDIA)?;
        let expected = pins
            .get(&platform)
            .ok_or_else(|| invalid("no pinned media for this build"))?;
        let root = exe.parent().unwrap().join("media").join(platform);
        let node = root.join("node");
        let helper = Self::pinned(&root.join(ENTRY), node.clone())?;
        let pinned = match &helper {
            Self::Pinned(pinned) => pinned,
            #[cfg(test)]
            Self::Fixture(_) => unreachable!(),
        };
        let extra_files = ARTIFACTS
            .into_iter()
            .map(|(name, _)| root.join(name))
            .chain([node, root.join("NODE-LICENSE")]);
        let mut actual = BTreeMap::new();
        for file in pinned
            .modules
            .iter()
            .map(|(path, _)| path.clone())
            .chain(extra_files)
        {
            if file.is_dir() {
                continue;
            }
            let mut hash = Sha256::new();
            io::copy(&mut trusted_file(&file)?, &mut hash)?;
            let relative = file
                .strip_prefix(&root)
                .map_err(|_| invalid("invalid media path"))?;
            let relative = relative
                .to_str()
                .ok_or_else(|| invalid("invalid media path"))?;
            actual.insert(relative.to_owned(), format!("{:x}", hash.finalize()));
        }
        if &actual != expected {
            return Err(invalid("packaged media differs from the release pins"));
        }
        // Recheck the identities used to hash the dependency tree before giving it to the caller.
        helper.command()?;
        Ok(helper)
    }

    pub(super) fn check(&self) -> io::Result<()> {
        let status = self
            .command()?
            .env_clear()
            .args(["--", ENTRY, "--check"])
            .status()?;
        if status.success() {
            Ok(())
        } else {
            Err(invalid("codec, cipher or DAVE self-check failed"))
        }
    }

    pub(super) fn pinned(path: &Path, node: PathBuf) -> io::Result<Self> {
        if !path.is_absolute() || path.file_name().is_none_or(|name| name != ENTRY) {
            return Err(invalid("select the pinned swarm-discord-voice executable"));
        }
        let root = fs::canonicalize(path.parent().unwrap())?;
        if !root
            .to_str()
            .is_some_and(|path| !path.contains(['*', ',', '\r', '\n']))
        {
            return Err(invalid(
                "call helper directory cannot be scoped to Node permissions",
            ));
        }
        let root_identity = Identity::of(&trusted_dir(&root)?.metadata()?);
        // Require the installed dependency root and all of its loadable paths to
        // be controlled by this owner or root. npm's CLI symlinks are unused.
        let modules = validate_modules(&root.join("node_modules"))?;
        let artifacts = read_artifacts(&root)?;
        let node_identity = executable(&node)?;
        Ok(Self::Pinned(Box::new(Pinned {
            path: path.into(),
            root,
            modules,
            root_identity,
            artifacts,
            node,
            node_identity,
        })))
    }

    pub(super) fn command(&self) -> io::Result<Command> {
        match self {
            #[cfg(test)]
            Self::Fixture(path) => Ok(Command::new(path)),
            Self::Pinned(pinned) => pinned.command(),
        }
    }
}

impl Pinned {
    fn command(&self) -> io::Result<Command> {
        let root = trusted_dir(&self.root)?;
        if fs::canonicalize(self.path.parent().unwrap())? != self.root
            || Identity::of(&root.metadata()?) != self.root_identity
            || read_artifacts(&self.root)? != self.artifacts
            || validate_modules(&self.root.join("node_modules"))? != self.modules
            || executable(&self.node)? != self.node_identity
        {
            return Err(invalid("pinned call helper installation changed"));
        }
        // Execute the exact embedded sources, never reopen a checked script after
        // validation. Imports resolve from the held dependency directory.
        let media_body = MEDIA.split_once('\n').unwrap().1;
        let source = SOURCE.replace(MEDIA_IMPORT, media_body);
        let mut command = Command::new(&self.node);
        command
            .arg("--permission")
            .arg(format!("--allow-fs-read={}", self.root.display()))
            .arg("--allow-addons")
            .args(["--input-type=module", "--eval", &source]);
        // SAFETY: the child closure only calls async-signal-safe fchdir on the
        // open directory. It performs no Rust I/O after fork.
        unsafe {
            command.pre_exec(move || {
                if libc::fchdir(root.as_raw_fd()) == -1 {
                    return Err(io::Error::last_os_error());
                }
                Ok(())
            });
        }
        Ok(command)
    }
}

fn read_artifacts(root: &Path) -> io::Result<Vec<Identity>> {
    ARTIFACTS
        .into_iter()
        .map(|(name, expected)| {
            let file = trusted_file(&root.join(name))?;
            let identity = Identity::of(&file.metadata()?);
            let mut bytes = Vec::new();
            file.take((expected.len() + 1) as u64)
                .read_to_end(&mut bytes)?;
            if bytes != expected {
                return Err(invalid(
                    "call helper bundle differs from the pinned version",
                ));
            }
            Ok(identity)
        })
        .collect()
}

fn owner_permissions(meta: &Metadata) -> bool {
    // SAFETY: geteuid takes no arguments.
    (meta.uid() == 0 || meta.uid() == unsafe { libc::geteuid() }) && meta.mode() & 0o022 == 0
}

fn trusted_dir(path: &Path) -> io::Result<File> {
    for parent in path.ancestors().skip(1) {
        let meta = fs::symlink_metadata(parent)?;
        if !meta.is_dir()
            || !(owner_permissions(&meta) || meta.uid() == 0 && meta.mode() & 0o1000 != 0)
        {
            return Err(invalid("unsafe parent directory for call helper"));
        }
    }
    let file = OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_DIRECTORY | libc::O_NOFOLLOW)
        .open(path)?;
    if !owner_permissions(&file.metadata()?) {
        return Err(invalid("unsafe directory for call helper"));
    }
    Ok(file)
}

fn trusted_file(path: &Path) -> io::Result<File> {
    let file = OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK)
        .open(path)?;
    let meta = file.metadata()?;
    if !meta.is_file() || !owner_permissions(&meta) || meta.nlink() != 1 {
        return Err(invalid("unsafe call helper artifact"));
    }
    Ok(file)
}

fn executable(path: &Path) -> io::Result<Identity> {
    trusted_dir(path.parent().unwrap())?;
    let meta = trusted_file(path)?.metadata()?;
    if meta.mode() & 0o111 == 0 {
        return Err(invalid("call helper Node runtime is not executable"));
    }
    Ok(Identity::of(&meta))
}

fn validate_modules(root: &Path) -> io::Result<Vec<(PathBuf, Identity)>> {
    let mut files = vec![(
        root.to_owned(),
        Identity::of(&trusted_dir(root)?.metadata()?),
    )];
    let mut pending = vec![root.to_owned()];
    while let Some(dir) = pending.pop() {
        for entry in fs::read_dir(dir)? {
            let entry = entry?;
            let meta = fs::symlink_metadata(entry.path())?;
            if files.len() >= 8192
                || !owner_permissions(&meta)
                || !meta.is_dir() && (!meta.is_file() || meta.nlink() != 1)
            {
                return Err(invalid("unsafe or oversized call helper dependency tree"));
            }
            let path = entry.path();
            if meta.is_dir() && entry.file_name() != ".bin" {
                pending.push(path.clone());
            }
            files.push((path, Identity::of(&meta)));
        }
    }
    files.sort_unstable_by(|left, right| left.0.cmp(&right.0));
    Ok(files)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::supervisor::runtime;
    use std::os::unix::fs::{PermissionsExt, symlink};

    #[test]
    fn exact_helper_bundle_fails_closed_after_replacement() {
        let root =
            std::env::temp_dir().join(format!("swarm-bundle-{}", runtime::random_id().unwrap()));
        runtime::private_dir(&root).unwrap();
        let root = fs::canonicalize(root).unwrap();
        runtime::private_dir(&root.join("node_modules")).unwrap();
        let dep = root.join("node_modules/module.js");
        fs::write(&dep, "fixture").unwrap();
        fs::set_permissions(&dep, fs::Permissions::from_mode(0o600)).unwrap();
        for (name, bytes) in ARTIFACTS {
            let path = root.join(name);
            fs::write(&path, bytes).unwrap();
            fs::set_permissions(&path, fs::Permissions::from_mode(0o600)).unwrap();
        }
        let entry = root.join(ENTRY);
        // This test constructs the command without launching it.
        let interpreter = fs::canonicalize("/bin/sh").unwrap();
        let helper = Helper::pinned(&entry, interpreter.clone()).unwrap();
        assert!(helper.command().is_ok());
        fs::write(&dep, "replaced").unwrap();
        assert!(helper.command().is_err());
        let helper = Helper::pinned(&entry, interpreter.clone()).unwrap();
        fs::set_permissions(&dep, fs::Permissions::from_mode(0o666)).unwrap();
        assert!(helper.command().is_err());
        fs::set_permissions(&dep, fs::Permissions::from_mode(0o600)).unwrap();
        let helper = Helper::pinned(&entry, interpreter.clone()).unwrap();
        fs::rename(&entry, root.join("original")).unwrap();
        symlink(root.join("original"), &entry).unwrap();
        assert!(helper.command().is_err());
        assert!(Helper::pinned(&entry, interpreter).is_err());
        fs::remove_dir_all(root).unwrap();
    }
}
