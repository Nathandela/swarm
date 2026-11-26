mod diagnostic;
mod files;
pub(crate) use diagnostic::DiagnosticLog;
mod hooks;

use crate::{flags, ipc};
use files::{Directory, unsafe_target};
use rustix::fs::{Dir, OFlags};
use serde::Serialize;
use std::fs::{self, File, Metadata};
use std::io::{self, Write};
use std::os::unix::ffi::OsStrExt;
use std::os::unix::fs::MetadataExt;
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};

const PRESERVED: &str = "Histories, credentials, projects, handoff inputs, session metadata, replay/idempotency state, recovery receipts, unknown files and shared caches are preserved. Hook cleanup requires the configured daemon to be stopped. Allocated-byte changes are observations, not guaranteed free space.";
const LOG_LIMIT: u64 = 8 << 20;
#[derive(Serialize)]
struct Size {
    logical_bytes: u64,
    allocated_bytes: u64,
}
impl From<&Metadata> for Size {
    fn from(meta: &Metadata) -> Self {
        Self {
            logical_bytes: meta.len(),
            allocated_bytes: meta.blocks().saturating_mul(512),
        }
    }
}
#[derive(Serialize)]
struct Item {
    path: PathBuf,
    kind: &'static str,
    status: &'static str,
    reason: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    before: Option<Size>,
    #[serde(skip_serializing_if = "Option::is_none")]
    after: Option<Size>,
    eligible_logical_bytes: u64,
    #[serde(skip_serializing_if = "Option::is_none")]
    observed_allocated_reduction_bytes: Option<i64>,
}
impl Item {
    fn new(path: &Path, kind: &'static str) -> Self {
        Self {
            path: path.to_owned(),
            kind,
            status: "unchanged",
            reason: "no eligible bytes".into(),
            before: None,
            after: None,
            eligible_logical_bytes: 0,
            observed_allocated_reduction_bytes: None,
        }
    }
    fn eligible(&mut self, n: u64) {
        self.eligible_logical_bytes = n;
        (self.status, self.reason) = if n > 0 {
            ("eligible", "proven cleanup candidate".into())
        } else {
            ("unchanged", "no eligible bytes".into())
        };
    }
    fn changed(&mut self) {
        self.status = "cleaned";
        self.reason = "retention applied".into();
    }
    fn finish(mut self, result: io::Result<()>) -> Self {
        if let Err(error) = result {
            let changed = self.status == "cleaned";
            self.status = if !changed
                && matches!(
                    error.kind(),
                    io::ErrorKind::InvalidInput
                        | io::ErrorKind::NotFound
                        | io::ErrorKind::WouldBlock
                ) {
                "retained"
            } else {
                "error"
            };
            self.reason = if changed {
                format!("cleanup changed bytes before failing: {error}")
            } else if error.kind() == io::ErrorKind::NotFound {
                "target or required proof is absent".into()
            } else {
                error.to_string()
            };
            self.eligible_logical_bytes = 0;
        }
        if let (Some(before), Some(after)) = (&self.before, &self.after) {
            self.observed_allocated_reduction_bytes =
                Some(before.allocated_bytes as i64 - after.allocated_bytes as i64);
        }
        self
    }
}
fn parent(path: &Path) -> io::Result<(Directory, &std::ffi::OsStr)> {
    let name = path.file_name().ok_or_else(unsafe_target)?;
    let dir = path.parent().ok_or_else(unsafe_target)?;
    if dir.join(name).as_os_str() != path.as_os_str() {
        return Err(unsafe_target());
    }
    Ok((
        Directory::open(if dir.as_os_str().is_empty() {
            Path::new(".")
        } else {
            dir
        })?,
        name,
    ))
}

fn log(path: &Path, apply: bool) -> Item {
    let mut item = Item::new(path, "diagnostic-log");
    let run = DiagnosticLog::inspect(path, apply, &mut item);
    item.finish(run)
}
struct Singleton<'a> {
    parent: Directory,
    name: &'a std::ffi::OsStr,
    file: File,
}
impl<'a> Singleton<'a> {
    fn open(path: &'a Path) -> io::Result<Self> {
        let (parent, name) = parent(path)?;
        let file = parent.lock(name, OFlags::RDONLY)?;
        Ok(Self { parent, name, file })
    }
    fn check(&self) -> io::Result<()> {
        self.parent.verify_file(self.name, &self.file).map(|_| ())
    }
}
#[derive(Serialize)]
struct Report {
    mode: &'static str,
    complete: bool,
    items: Vec<Item>,
    preserved: &'static str,
}
fn inventory(root: &Path, lock: &Path, apply: bool, items: &mut Vec<Item>) -> io::Result<()> {
    let root = match Directory::open(root) {
        Ok(dir) => dir,
        Err(e) if e.kind() == io::ErrorKind::NotFound => return Ok(()),
        Err(e) => return Err(e),
    };
    let singleton = Singleton::open(lock);
    let end = Instant::now() + Duration::from_secs(2);
    let entries = Dir::read_from(&root.file)?;
    for (index, entry) in entries.enumerate() {
        if index >= 10_002 || Instant::now() >= end {
            return Err(io::Error::other(
                "discovery limit reached; inventory is incomplete",
            ));
        }
        let entry = entry?;
        let name = std::ffi::OsStr::from_bytes(entry.file_name().to_bytes());
        if name == "." || name == ".." {
            continue;
        }
        root.check()?;
        let path = root.path.join(name);
        let meta = fs::symlink_metadata(&path)?;
        if meta.is_symlink() {
            items.push(Item::new(&path, "directory").finish(Err(unsafe_target())));
            continue;
        }
        if !meta.is_dir() {
            continue;
        }
        let dir = match Directory::open(&path) {
            Ok(dir) => dir,
            Err(e) => {
                items.push(Item::new(&path, "directory").finish(Err(e)));
                continue;
            }
        };
        let spool = path.join("hooks.spool");
        let mut item = Item::new(&spool, "hook-prefix");
        match fs::symlink_metadata(&spool) {
            Ok(meta) => {
                if meta.is_file() {
                    item.before = Some((&meta).into());
                }
            }
            Err(e) if e.kind() == io::ErrorKind::NotFound => continue,
            Err(e) => {
                items.push(item.finish(Err(e)));
                continue;
            }
        }
        let result = (|| {
            let guard = singleton
                .as_ref()
                .map_err(|e| io::Error::new(e.kind(), e.to_string()))?;
            if fs::canonicalize(lock)?.starts_with(fs::canonicalize(&path)?) {
                return Err(unsafe_target());
            }
            let fence = || {
                root.check()?;
                dir.check()?;
                guard.check()
            };
            hooks::clean(&dir, apply, &mut item, fence)
        })();
        items.push(item.finish(result));
    }
    Ok(())
}
fn report(apply: bool) -> io::Result<Report> {
    let root = ipc::state_path()?;
    let log_path = DiagnosticLog::path(&root);
    let lock = ipc::env_path("SWARM_DAEMON_LOCK").unwrap_or_else(|| root.join("daemon.lock"));
    let mut items = vec![log(&log_path, apply)];
    let scan = inventory(&root, &lock, apply, &mut items);
    let complete = scan.is_ok() && items.iter().all(|i| i.status != "error");
    if let Err(error) = scan {
        items.push(Item::new(&root, "inventory").finish(Err(error)));
    }
    Ok(Report {
        mode: if apply { "apply" } else { "preview" },
        complete,
        items,
        preserved: PRESERVED,
    })
}
fn size(value: &Option<Size>) -> String {
    value.as_ref().map_or_else(
        || "unknown".into(),
        |s| {
            format!(
                "allocated={} logical={} bytes",
                s.allocated_bytes, s.logical_bytes
            )
        },
    )
}
fn write_report(mut w: impl Write, r: &Report, json: bool) -> io::Result<()> {
    if json {
        serde_json::to_writer_pretty(&mut w, r)?;
        return writeln!(w);
    }
    writeln!(w, "Storage cleanup {} (complete={})", r.mode, r.complete)?;
    for i in &r.items {
        write!(
            w,
            "{} {} {:?}: {:?}; eligible logical bytes={}; before {}; after {}",
            i.status,
            i.kind,
            i.path,
            i.reason,
            i.eligible_logical_bytes,
            size(&i.before),
            size(&i.after)
        )?;
        if let Some(delta) = i.observed_allocated_reduction_bytes {
            write!(w, "; observed allocated reduction={delta} bytes")?;
        }
        writeln!(w)?;
    }
    writeln!(w, "{}", r.preserved)?;
    if r.mode == "preview" {
        writeln!(
            w,
            "Use swarm clean --apply to revalidate and apply eligible cleanup."
        )?;
    }
    Ok(())
}
pub(crate) fn run(args: &[String]) -> u8 {
    let (mut apply, mut json) = (false, false);
    let parsed = flags::options(args, &["apply", "json"], &["apply", "json"], |k, v| {
        let b = flags::parse_bool(k, v)?;
        if k == "apply" {
            apply = b
        } else {
            json = b
        };
        Ok(())
    });
    match parsed {
        Ok([]) => {}
        Ok(rest) => {
            eprintln!("clean: unexpected argument {:?}", rest[0]);
            return 2;
        }
        Err(e) => {
            if e.is_empty() {
                eprintln!(
                    "Usage of clean:\n  -apply\n    \tapply proven cleanup (default is preview)\n  -json\n    \tprint results as JSON"
                );
            } else {
                eprintln!("{e}");
            }
            return 2;
        }
    }
    match report(apply).and_then(|r| {
        write_report(io::stdout().lock(), &r, json)?;
        Ok(r.complete)
    }) {
        Ok(true) => 0,
        Ok(false) => 1,
        Err(e) => {
            eprintln!("clean: {e}");
            1
        }
    }
}
