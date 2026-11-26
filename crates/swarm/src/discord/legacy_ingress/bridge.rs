//! Custody of the original Go Voice controller's Unix endpoint.
use super::*;
use crate::discord::dispatcher::read_tool_bytes;
use std::{
    io::Write,
    os::unix::fs::{FileExt, FileTypeExt},
    os::unix::net::UnixListener,
    time::Duration,
};

pub(in crate::discord) struct Custody {
    socket: PathBuf,
    root_identity: (u64, u64),
    lock: LockDescription,
    auth_path: PathBuf,
    auth: fs::File,
    credential: String,
}

impl Custody {
    pub(in crate::discord) fn claim(
        directory: &Path,
        id: &str,
        retained: bool,
    ) -> io::Result<Self> {
        let root = fs::canonicalize("/tmp")?
            .join(format!("swarm-dm-{}", runtime::uid()))
            .join(id.get(..32).ok_or_else(invalid)?);
        if fs::canonicalize(&root)? != root {
            return Err(invalid());
        }
        runtime::private_dir(&root)?;
        let root_identity = identity(&fs::symlink_metadata(&root)?);
        let lock_path = root.join("controller.lock");
        let original = retained.then(|| open_owner_file(&lock_path)).transpose()?;
        let lock = LockDescription(runtime::lock(&lock_path)?);
        if let Some(original) = original {
            let (saved, held) = (original.metadata()?, lock.0.metadata()?);
            if (saved.dev(), saved.ino()) != (held.dev(), held.ino()) {
                return Err(invalid());
            }
        }
        let auth_path = directory.join("bridge.auth");
        let auth = open_owner_file(&auth_path)?;
        let credential = credential(&auth)?;
        Ok(Self {
            socket: root.join("bridge.sock"),
            root_identity,
            lock,
            auth_path,
            auth,
            credential,
        })
    }

    fn verify_files(&self) -> io::Result<()> {
        let root = fs::symlink_metadata(self.socket.parent().ok_or_else(invalid)?)?;
        let lock = open_owner_file(&self.socket.with_file_name("controller.lock"))?;
        let auth = open_owner_file(&self.auth_path)?;
        if identity(&root) != self.root_identity
            || !root.is_dir()
            || root.uid() != runtime::uid()
            || root.mode() & 0o077 != 0
            || identity(&lock.metadata()?) != identity(&self.lock.0.metadata()?)
            || identity(&auth.metadata()?) != identity(&self.auth.metadata()?)
            || credential(&auth)? != self.credential
        {
            return Err(invalid());
        }
        Ok(())
    }

    pub(in crate::discord) fn verify_stopped(&self) -> io::Result<()> {
        self.verify_files()?;
        if !matches!(fs::symlink_metadata(&self.socket), Err(e) if e.kind() == io::ErrorKind::NotFound)
        {
            return Err(invalid());
        }
        Ok(())
    }

    pub(in crate::discord) fn matches_runtime(&self, runtime: &Value) -> bool {
        runtime["bridge_socket"].as_str().map(Path::new) == Some(self.socket.as_path())
            && runtime["auth_file"].as_str().map(Path::new) == Some(self.auth_path.as_path())
    }

    #[cfg(test)]
    pub(in crate::discord) fn duplicate_lock_for_test(&self) -> io::Result<fs::File> {
        self.lock.0.try_clone()
    }

    pub(in crate::discord) fn listen(&self) -> io::Result<ToolSocket<'_>> {
        self.verify_files()?;
        match fs::symlink_metadata(&self.socket) {
            Ok(meta) => {
                if !owner_socket(&meta) {
                    return Err(invalid());
                }
                match crate::discord::local::connect_available(&self.socket) {
                    Err(error) if error.kind() == io::ErrorKind::ConnectionRefused => {}
                    _ => return Err(invalid()),
                }
                if identity(&fs::symlink_metadata(&self.socket)?) != identity(&meta) {
                    return Err(invalid());
                }
                fs::remove_file(&self.socket)?;
            }
            Err(error) if error.kind() == io::ErrorKind::NotFound => {}
            Err(error) => return Err(error),
        }
        let listener = runtime::bind(&self.socket)?;
        let metadata = fs::symlink_metadata(&self.socket)?;
        let socket = ToolSocket {
            listener,
            custody: self,
            identity: identity(&metadata),
        };
        socket.listener.set_nonblocking(true)?;
        Ok(socket)
    }
}

fn credential(file: &fs::File) -> io::Result<String> {
    let mut token = [0; 64];
    file.read_exact_at(&mut token, 0)?;
    if file.metadata()?.len() != token.len() as u64
        || !token
            .iter()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(b))
    {
        return Err(invalid());
    }
    String::from_utf8(token.to_vec()).map_err(io::Error::other)
}

fn identity(meta: &fs::Metadata) -> (u64, u64) {
    (meta.dev(), meta.ino())
}

fn owner_socket(meta: &fs::Metadata) -> bool {
    meta.file_type().is_socket()
        && meta.nlink() == 1
        && meta.uid() == runtime::uid()
        && meta.mode() & 0o777 == 0o600
}

pub(in crate::discord) struct ToolSocket<'a> {
    listener: UnixListener,
    custody: &'a Custody,
    identity: (u64, u64),
}

impl ToolSocket<'_> {
    pub(in crate::discord) fn verify_path(&self) -> io::Result<()> {
        self.custody.verify_files()?;
        let metadata = fs::symlink_metadata(&self.custody.socket)?;
        if identity(&metadata) != self.identity || !owner_socket(&metadata) {
            return Err(invalid());
        }
        Ok(())
    }

    pub(in crate::discord) fn accept(
        &self,
        handle: impl FnOnce(&[u8]) -> io::Result<Value>,
    ) -> io::Result<Option<String>> {
        self.accept_guarded(|raw| Ok((handle(raw)?, ())))
    }

    pub(in crate::discord) fn accept_guarded<G>(
        &self,
        handle: impl FnOnce(&[u8]) -> io::Result<(Value, G)>,
    ) -> io::Result<Option<String>> {
        let mut connection = match self.listener.accept() {
            Ok((connection, _)) => connection,
            Err(error) if error.kind() == io::ErrorKind::WouldBlock => return Ok(None),
            Err(error) => return Err(error),
        };
        let result = read_tool_bytes(&connection, &self.custody.credential).and_then(|raw| {
            self.verify_path()?;
            let response = handle(&raw)?;
            self.verify_path()?;
            Ok(response)
        });
        let output = result
            .as_ref()
            .ok()
            .map(|(value, _)| field(value, "id").to_owned());
        let (reply, _guard) = match result {
            Ok((value, guard)) => (json!({"data":value}), Some(guard)),
            Err(_) => (json!({"error":"unconfirmed"}), None),
        };
        let bytes = serde_json::to_vec(&reply)?;
        connection.set_write_timeout(Some(Duration::from_secs(2)))?;
        let _ = write!(connection,"HTTP/1.0 200 OK\r\nContent-Type: application/json\r\nCache-Control: no-store\r\nContent-Length: {}\r\n\r\n",bytes.len())
            .and_then(|_| connection.write_all(&bytes));
        Ok(output)
    }
}

impl Drop for ToolSocket<'_> {
    fn drop(&mut self) {
        if fs::symlink_metadata(&self.custody.socket)
            .is_ok_and(|meta| identity(&meta) == self.identity)
        {
            let _ = fs::remove_file(&self.custody.socket);
        }
    }
}
