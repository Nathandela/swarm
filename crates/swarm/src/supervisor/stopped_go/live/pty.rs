use super::{Process, runtime};
use std::io;

/// A classic Go shim runs a single ordinary PTY process without the managed
/// native-process side file. Discover the original session leader from the
/// authenticated Go shim's actual children. This is terminal custody only.
pub(super) fn worker(shim: &Process) -> io::Result<Process> {
    shim.matches()?;
    let mut worker = None;
    for pid in children(shim.pid)? {
        let born = runtime::birth(pid)?;
        let (parent, group, tty, _) = runtime::job_control(pid)?;
        // SAFETY: getsid is only a kernel query for the candidate process.
        let session = unsafe { libc::getsid(pid as i32) };
        if parent != shim.pid
            || group != pid
            || session != pid as i32
            || matches!(tty, 0 | u32::MAX)
        {
            continue;
        }
        if born < shim.birth || born != runtime::birth(pid)? || worker.is_some() {
            return Err(io::Error::other("ambiguous Go terminal worker custody"));
        }
        worker = Some(Process { pid, birth: born });
    }
    shim.matches()?;
    worker.ok_or_else(|| io::Error::other("no owned Go terminal worker"))
}

#[cfg(target_os = "linux")]
fn children(pid: u32) -> io::Result<Vec<u32>> {
    use std::{collections::BTreeSet, fs};
    let mut children = BTreeSet::new();
    for task in fs::read_dir(format!("/proc/{pid}/task"))? {
        let tasks = match fs::read_to_string(task?.path().join("children")) {
            Ok(tasks) => tasks,
            Err(err) if err.kind() == io::ErrorKind::NotFound => continue,
            Err(err) => return Err(err),
        };
        for child in tasks.split_whitespace() {
            let child: u32 = child.parse().map_err(io::Error::other)?;
            children.insert(child);
        }
        if children.len() > 64 {
            return Err(io::Error::other("unbounded Go child inventory"));
        }
    }
    Ok(children.into_iter().collect())
}

#[cfg(target_os = "macos")]
fn children(pid: u32) -> io::Result<Vec<u32>> {
    let mut children = [0i32; 65];
    // SAFETY: the output buffer has the exact byte capacity passed to libproc.
    let count = unsafe {
        libc::proc_listchildpids(
            pid as i32,
            children.as_mut_ptr().cast(),
            std::mem::size_of_val(&children) as i32,
        )
    };
    if count <= 0 || count as usize >= children.len() {
        return Err(io::Error::other("unproved Go child inventory"));
    }
    children[..count as usize]
        .iter()
        .map(|pid| {
            u32::try_from(*pid)
                .ok()
                .filter(|pid| *pid > 1)
                .ok_or_else(|| io::Error::other("invalid Go child inventory"))
        })
        .collect()
}
