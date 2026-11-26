//! Open existing objects relative to a pinned directory. Cleanup never follows a final symlink.
use crate::supervisor::runtime;
use rustix::fs::{AtFlags, FlockOperation, Mode, OFlags, flock, openat, renameat, unlinkat};
use std::ffi::OsStr;
use std::fs::{self, File, Metadata, OpenOptions};
use std::io::{self, Read, Write};
use std::os::unix::fs::{MetadataExt, OpenOptionsExt};
use std::path::{Component, Path, PathBuf};

pub(super) fn unsafe_target() -> io::Error {
    io::Error::new(
        io::ErrorKind::InvalidInput,
        "ownership or identity is unproven",
    )
}

pub(super) fn same(a: &Metadata, b: &Metadata) -> bool {
    (a.dev(), a.ino()) == (b.dev(), b.ino())
}

pub(super) struct LockedFile(pub(super) File);

impl Drop for LockedFile {
    fn drop(&mut self) {
        // A concurrent fork can still hold a duplicate until its exec closes the descriptor.
        let _ = flock(&self.0, FlockOperation::Unlock);
    }
}

pub(super) struct Directory {
    pub(super) path: PathBuf,
    pub(super) file: File,
}
impl Directory {
    pub(super) fn open(path: &Path) -> io::Result<Self> {
        let clean: PathBuf = path.components().collect();
        if clean.as_os_str() != path.as_os_str()
            || path.components().any(|c| c == Component::ParentDir)
        {
            return Err(unsafe_target());
        }
        let file = OpenOptions::new()
            .read(true)
            .custom_flags(libc::O_DIRECTORY | libc::O_NOFOLLOW | libc::O_NONBLOCK)
            .open(path)
            .map_err(|e| {
                if matches!(e.raw_os_error(), Some(libc::ELOOP | libc::ENOTDIR)) {
                    unsafe_target()
                } else {
                    e
                }
            })?;
        let dir = Self {
            path: path.to_path_buf(),
            file,
        };
        dir.check()?;
        Ok(dir)
    }
    pub(super) fn check(&self) -> io::Result<()> {
        let meta = self.file.metadata()?;
        if meta.uid() != runtime::uid()
            || meta.mode() & 0o022 != 0
            || !same(&meta, &fs::symlink_metadata(&self.path)?)
        {
            return Err(unsafe_target());
        }
        Ok(())
    }
    pub(super) fn open_file(&self, name: &OsStr, flags: OFlags) -> io::Result<File> {
        self.check()?;
        let path = self.path.join(name);
        if !fs::symlink_metadata(&path)?.is_file() {
            return Err(unsafe_target());
        }
        let fd = openat(
            &self.file,
            name,
            flags | OFlags::NOFOLLOW | OFlags::NONBLOCK | OFlags::CLOEXEC,
            Mode::empty(),
        )?;
        let file = File::from(fd);
        self.verify_file(name, &file)?;
        Ok(file)
    }
    pub(super) fn verify_file(&self, name: &OsStr, file: &File) -> io::Result<Metadata> {
        self.check()?;
        let meta = file.metadata()?;
        if !meta.is_file()
            || meta.nlink() != 1
            || meta.uid() != runtime::uid()
            || !same(&meta, &fs::symlink_metadata(self.path.join(name))?)
        {
            return Err(unsafe_target());
        }
        Ok(meta)
    }
    pub(super) fn lock(&self, name: &OsStr, flags: OFlags) -> io::Result<LockedFile> {
        let file = self.open_file(name, flags)?;
        flock(&file, FlockOperation::NonBlockingLockExclusive)?;
        let held = LockedFile(file);
        self.verify_file(name, &held.0)?;
        Ok(held)
    }
    pub(super) fn read(&self, name: &str, limit: u64) -> io::Result<Vec<u8>> {
        let mut data = Vec::new();
        self.open_file(name.as_ref(), OFlags::RDONLY)?
            .take(limit + 1)
            .read_to_end(&mut data)?;
        if data.len() as u64 > limit {
            return Err(unsafe_target());
        }
        Ok(data)
    }
    // The caller's fence includes the original spool and all participating locks.
    pub(super) fn replace(
        &self,
        name: &str,
        bytes: &[u8],
        changed: &mut bool,
        fence: impl FnOnce() -> io::Result<()>,
    ) -> io::Result<()> {
        let temp = format!(".swarm-clean-{}", runtime::random_id()?);
        let fd = openat(
            &self.file,
            temp.as_str(),
            OFlags::CREATE | OFlags::EXCL | OFlags::WRONLY | OFlags::CLOEXEC,
            Mode::RUSR | Mode::WUSR,
        )?;
        let mut file = File::from(fd);
        let result = (|| {
            file.write_all(bytes)?;
            file.sync_all()?;
            self.verify_file(temp.as_ref(), &file)?;
            fence()?;
            renameat(&self.file, temp.as_str(), &self.file, name)?;
            *changed = true;
            self.file.sync_all()
        })();
        if result.is_err() && self.verify_file(temp.as_ref(), &file).is_ok() {
            let _ = unlinkat(&self.file, temp.as_str(), AtFlags::empty());
        }
        result
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn cleanup_unlocks_at_scope_exit_even_with_an_inherited_descriptor() {
        let root =
            std::env::temp_dir().join(format!("clean-lock-{}", runtime::random_id().unwrap()));
        runtime::private_dir(&root).unwrap();
        fs::write(root.join("lock"), []).unwrap();
        let dir = Directory::open(&root).unwrap();
        let lock = || dir.lock("lock".as_ref(), OFlags::RDWR);
        let first = lock().unwrap();
        let inherited = first.0.try_clone().unwrap();
        assert!(matches!(lock(), Err(error) if error.kind() == io::ErrorKind::WouldBlock));
        drop(first);
        let second = lock().unwrap();
        drop(inherited);
        assert!(matches!(lock(), Err(error) if error.kind() == io::ErrorKind::WouldBlock));
        drop(second);
        fs::remove_dir_all(&root).unwrap();
    }
}
