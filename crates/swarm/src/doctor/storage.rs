//! Count allocated blocks from directory-relative metadata; never follow a symlink.
use super::{Finding, finding};
use rustix::fd::OwnedFd;
use rustix::fs::{
    AtFlags, Dir, FileType, Mode, OFlags, Stat, fstat, fstatvfs, open, openat, statat,
};
use std::collections::HashSet;
use std::path::Path;
use std::time::{Duration, Instant};

const FLAGS: OFlags = OFlags::RDONLY
    .union(OFlags::DIRECTORY)
    .union(OFlags::NOFOLLOW)
    .union(OFlags::CLOEXEC);
#[derive(Default)]
struct Usage {
    buckets: [u64; 3],
    logical: u64,
    links: usize,
    partial: bool,
    visited: HashSet<(u64, u64)>,
    count: usize,
}

impl Usage {
    #[allow(clippy::unnecessary_cast)] // stat fields differ between Darwin and Linux.
    fn add(&mut self, st: &Stat, bucket: usize) -> bool {
        self.count += 1;
        if !self.visited.insert((st.st_dev as u64, st.st_ino as u64)) {
            return false;
        }
        let kind = FileType::from_raw_mode(st.st_mode);
        let bucket = if kind == FileType::Symlink {
            self.links += 1;
            2
        } else {
            bucket
        };
        self.buckets[bucket] =
            self.buckets[bucket].saturating_add((st.st_blocks.max(0) as u64).saturating_mul(512));
        if kind == FileType::RegularFile {
            self.logical = self.logical.saturating_add(st.st_size.max(0) as u64);
        }
        kind == FileType::Directory
    }

    fn walk(&mut self, fd: OwnedFd, top: &[u8], deadline: Instant) -> rustix::io::Result<()> {
        let mut entries = Dir::read_from(&fd)?;
        for entry in &mut entries {
            if self.count >= 100_000 || Instant::now() >= deadline {
                self.partial = true;
                break;
            }
            let entry = match entry {
                Ok(e) => e,
                Err(_) => {
                    self.partial = true;
                    break;
                }
            };
            let name = entry.file_name();
            let bytes = name.to_bytes();
            if matches!(bytes, b"." | b"..") {
                continue;
            }
            let st = match statat(&fd, name, AtFlags::SYMLINK_NOFOLLOW) {
                Ok(s) => s,
                Err(_) => {
                    self.partial = true;
                    continue;
                }
            };
            let first = if top.is_empty() { bytes } else { top };
            let regular = FileType::from_raw_mode(st.st_mode) == FileType::RegularFile;
            let bucket = if matches!(first, b"accounts" | b"logins") {
                1
            } else if matches!(
                first,
                b"rust-v1" | b"journal" | b"idempotency" | b"remote" | b"devices" | b"upgrade"
            ) || top.is_empty() && regular
                || matches!(
                    bytes,
                    b"meta.json"
                        | b"shim-launch.json"
                        | b"exit.json"
                        | b"identity-tail.bin"
                        | b"hooks.spool"
                        | b"hooks.spool.floor"
                        | b"hooks.spool.incarnation"
                        | b"hook.fold"
                        | b"hook.applied"
                        | b"capabilities.json"
                        | b"backend.json"
                        | b"observed-cli.json"
                        | b"session-instance"
                )
            {
                0
            } else {
                2
            };
            if self.add(&st, bucket) {
                let child = match openat(&fd, name, FLAGS, Mode::empty()) {
                    Ok(f) => f,
                    Err(_) => {
                        self.partial = true;
                        continue;
                    }
                };
                let current = fstat(&child)?;
                if (current.st_dev, current.st_ino) != (st.st_dev, st.st_ino)
                    || self.walk(child, first, deadline).is_err()
                {
                    self.partial = true;
                }
            }
        }
        Ok(())
    }
}

pub(super) fn check(path: &Path) -> Finding {
    let mut usage = Usage::default();
    let mut available = None;
    match open(path, FLAGS, Mode::empty()) {
        Err(rustix::io::Errno::NOENT) => {
            return finding(
                "storage",
                "ok",
                "no state directory; no storage measured",
                "",
            );
        }
        Err(_) => usage.partial = true,
        Ok(root) => {
            available = fstatvfs(&root)
                .ok()
                .and_then(|s| s.f_bavail.checked_mul(s.f_bsize));
            if let Ok(stat) = fstat(&root) {
                usage.add(&stat, 0);
            } else {
                usage.partial = true;
            }
            if usage
                .walk(root, b"", Instant::now() + Duration::from_secs(2))
                .is_err()
            {
                usage.partial = true;
            }
        }
    }
    let [supervisor, managed, other] = usage.buckets;
    let total = supervisor.saturating_add(managed).saturating_add(other);
    let scope = if usage.partial {
        "partial lower bound"
    } else {
        "observed snapshot"
    };
    let free = available.map_or_else(
        || "filesystem availability unknown".to_string(),
        |n| format!("filesystem available={n} bytes"),
    );
    let detail = format!(
        "{scope}: {total} bytes allocated, {} regular-file bytes logical; supervisor={supervisor}, managed={managed}, other retained={other} allocated bytes; {} symlink targets excluded; {free}; external histories, projects and temporary caches not measured; no automatic cleanup or admission limit",
        usage.logical, usage.links
    );
    if usage.partial || total >= 256 << 20 || available.is_none_or(|n| n < 1 << 30) {
        finding(
            "storage",
            "warn",
            detail,
            "review storage ownership and available space; preserve histories and unresolved recovery data",
        )
    } else {
        finding("storage", "ok", detail, "")
    }
}
