use crate::ipc::Client;
use std::cmp::Ordering;
use std::io;
use std::os::unix::fs::MetadataExt;
use std::path::Path;
use std::sync::mpsc::{self, Receiver};

pub(super) const CLIENT: &str = match option_env!("SWARM_BUILD_VERSION") {
    Some(version) => version,
    None => "dev",
};

fn precedence(version: &str) -> Option<(Vec<u64>, Option<&str>)> {
    let version = version.strip_prefix('v').unwrap_or(version);
    let version = version.split_once('+').map_or(version, |(core, _)| core);
    let (core, preview) = version
        .split_once('-')
        .map_or((version, None), |(core, suffix)| (core, Some(suffix)));
    let mut parts = core
        .split('.')
        .map(str::parse)
        .collect::<Result<Vec<u64>, _>>()
        .ok()?;
    while parts.last() == Some(&0) {
        parts.pop();
    }
    Some((parts, preview))
}

fn compare(daemon: &str, local: &str) -> Option<Ordering> {
    let (daemon, before) = precedence(daemon)?;
    let (local, after) = precedence(local)?;
    Some(daemon.cmp(&local).then_with(|| match (before, after) {
        (Some(before), Some(after)) => before.cmp(after),
        (Some(_), None) => Ordering::Less,
        (None, Some(_)) => Ordering::Greater,
        (None, None) => Ordering::Equal,
    }))
}

pub(super) fn notice(daemon: &str) -> Option<String> {
    (compare(daemon, CLIENT) == Some(Ordering::Greater))
        .then(|| format!("daemon {daemon} differs from swarm {CLIENT} - run: swarm daemon restart"))
}

pub(super) struct Upgrade {
    result: Receiver<io::Result<()>>,
    thread: Option<std::thread::JoinHandle<()>>,
}

impl Upgrade {
    pub(super) fn result(&self) -> Option<io::Result<()>> {
        match self.result.try_recv() {
            Ok(result) => Some(result),
            Err(mpsc::TryRecvError::Empty) => None,
            Err(mpsc::TryRecvError::Disconnected) => {
                Some(Err(io::Error::other("daemon upgrade worker closed")))
            }
        }
    }
}

impl Drop for Upgrade {
    fn drop(&mut self) {
        // Let an acknowledged restart finish even if the board has quit.
        if let Some(thread) = self.thread.take() {
            let _ = thread.join();
        }
    }
}

pub(super) fn start(path: &Path, client: &Client) -> io::Result<Option<Upgrade>> {
    if compare(client.build_version(), CLIENT) != Some(Ordering::Less) {
        return Ok(None);
    }
    let socket = std::fs::metadata(path)?;
    let birth = (socket.dev(), socket.ino());
    let (path, endpoint, version) = (
        path.to_owned(),
        client.endpoint().to_owned(),
        client.build_version().to_owned(),
    );
    let (send, receive) = mpsc::sync_channel(1);
    let thread = std::thread::Builder::new()
        .name("board-daemon-upgrade".into())
        .spawn(move || {
            let _ = send.send(crate::bootstrap::restart_board(
                &path, &endpoint, &version, birth,
            ));
        })?;
    Ok(Some(Upgrade {
        result: receive,
        thread: Some(thread),
    }))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn compares_release_direction_without_treating_dev_as_an_old_daemon() {
        assert_eq!(compare("v0.29.22+Go", "0.29.22"), Some(Ordering::Equal));
        assert_eq!(compare("0.29.22-rc.1", "0.29.22"), Some(Ordering::Less));
        assert_eq!(compare("0.29.23", "0.29.22"), Some(Ordering::Greater));
        assert_eq!(compare("0.29.22.0", "0.29.22"), Some(Ordering::Equal));
        assert_eq!(compare("dev", "0.29.22"), None);
        assert_eq!(compare("0.29.22", ""), None);
    }
}
