// The pinned Go shim retains PTY and hook ownership across supervisor changes.
use super::{read_at, same_file};
use crate::{
    ipc::field,
    supervisor::{read_attach_snapshot, runtime},
};
use serde_json::{Value, json};
use std::fs::{self, File};
use std::io;
use std::net::Shutdown;
use std::os::unix::fs::{FileTypeExt, MetadataExt};
use std::os::unix::net::UnixStream;
use std::path::{Path, PathBuf};
use std::sync::Mutex;

mod agy;
mod codex;

pub(in crate::supervisor) const OPT_IN: &str = "SWARM_DAEMON_LIVE_GO_COMPAT";
const SHIM_WIRE_VERSION: u64 = 2;

pub(in crate::supervisor) struct Owner {
    dir: PathBuf,
    directory: File,
    shim: Process,
    worker: Process,
    backend: Option<Process>,
    lease: Mutex<Option<Controller>>,
}

struct Controller {
    generation: u64,
    shim: UnixStream,
    client: UnixStream,
}

pub(in crate::supervisor) struct Lease<'a>(&'a Owner, u64);

impl Lease<'_> {
    pub(in crate::supervisor) fn current<T>(
        &self,
        effect: impl FnOnce() -> io::Result<T>,
    ) -> io::Result<T> {
        let lease = self.0.lease.lock().unwrap();
        if !lease
            .as_ref()
            .is_some_and(|active| active.generation == self.1)
        {
            return Err(io::Error::new(
                io::ErrorKind::ConnectionAborted,
                "Go terminal lease was superseded",
            ));
        }
        effect()
    }
}

impl Drop for Lease<'_> {
    fn drop(&mut self) {
        let mut lease = self.0.lease.lock().unwrap();
        if lease
            .as_ref()
            .is_some_and(|active| active.generation == self.1)
        {
            lease.take();
        }
    }
}

struct Process {
    pid: u32,
    birth: u64,
}

impl Process {
    fn new(pid: &Value, birth: &Value) -> io::Result<Self> {
        let pid = pid
            .as_u64()
            .and_then(|n| u32::try_from(n).ok())
            .filter(|pid| (2..=i32::MAX as u32).contains(pid));
        let birth = birth.as_u64().filter(|n| *n > 0);
        match (pid, birth) {
            (Some(pid), Some(birth)) => Ok(Self { pid, birth }),
            _ => Err(io::Error::other("unproven Go process birth identity")),
        }
    }

    fn matches(&self) -> io::Result<()> {
        if runtime::birth(self.pid)? != self.birth {
            return Err(io::Error::other("Go process birth identity changed"));
        }
        Ok(())
    }
}

fn managed_processes(
    agent: &str,
    path: &Path,
    directory: &File,
    launch: &Value,
    meta: &Value,
) -> io::Result<(Process, Option<Process>)> {
    let native = read_at(directory, c"native-process.json")?;
    if native["schema_version"] != 3
        || native["provider"] != agent
        || native["shim_pid"] != meta["shim_pid"]
        || native["shim_start_time"] != meta["shim_start_time"]
        || native["pid"] != native["pgid"]
        || field(&native, "generation").is_empty()
        || !native["incident_id"].is_null()
        || agent == "claude"
            && (!native["backend_pid"].is_null() || !native["backend_start_time"].is_null())
    {
        return Err(io::Error::other(
            "Go shim and native process ownership disagree",
        ));
    }
    let worker = Process::new(&native["pid"], &native["start_time"])?;
    let backend = (agent == "codex")
        .then(|| codex::owned_backend(path, directory, launch, &native))
        .transpose()?;
    Ok((worker, backend))
}

impl Owner {
    pub(super) fn open(path: PathBuf, directory: &File, meta: &Value) -> io::Result<Self> {
        super::qualified_role(meta)?;
        let agent = field(meta, "agent_type");
        if !matches!(agent, "claude" | "codex" | "agy")
            || meta["status"]["Process"] != "running"
            || meta["shim_wire_version"] != SHIM_WIRE_VERSION
            || !meta["exit_code"].is_null()
        {
            return Err(io::Error::other(
                "unsupported live Go shim state or wire version",
            ));
        }
        let launch = super::launch_evidence(directory, meta)?;
        if Path::new(field(&launch, "session_dir")) != path
            || Path::new(field(&launch, "hook_socket_path")) != path.join("hook.sock")
            || !valid_token(field(&launch, "hook_drain_token"))
            || !launch["env"].as_array().is_some_and(|env| {
                env.iter().any(|entry| {
                    entry
                        .as_str()
                        .and_then(|entry| entry.strip_prefix("SWARM_HOOK_TOKEN="))
                        .is_some_and(valid_token)
                })
            })
        {
            return Err(io::Error::other(
                "Go live launch must stay bound to its original private directory and hook spool",
            ));
        }
        super::absent_sidefiles(directory, &[c"exit.json", c"native-writers-stopped.json"])?;
        if agent != "codex" {
            super::absent_sidefiles(directory, &[c"backend.json", c"codex.sock"])?;
        }
        let shim = Process::new(&meta["shim_pid"], &meta["shim_start_time"])?;
        let (worker, backend) = if agent == "agy" {
            super::absent_sidefiles(directory, &[c"native-process.json"])?;
            (agy::pty_worker(&shim)?, None)
        } else {
            managed_processes(agent, &path, directory, &launch, meta)?
        };
        let owner = Self {
            dir: path,
            directory: directory.try_clone()?,
            shim,
            worker,
            backend,
            lease: Mutex::new(None),
        };
        owner.connect("hook.sock")?;
        owner.dial()?;
        if let Some(backend) = &owner.backend {
            codex::verify_thread(&owner.dir, backend, meta)?;
        }
        Ok(owner)
    }

    fn connect(&self, name: &str) -> io::Result<UnixStream> {
        let directory = self.directory.metadata()?;
        if !same_file(&directory, &fs::symlink_metadata(&self.dir)?)
            || directory.mode() & 0o077 != 0
            || directory.uid() != unsafe { libc::geteuid() }
        {
            return Err(io::Error::other("live Go session directory changed"));
        }
        let socket = self.dir.join(name);
        let info = fs::symlink_metadata(&socket)?;
        if !info.file_type().is_socket()
            || info.uid() != directory.uid()
            || info.mode() & 0o077 != 0
        {
            return Err(io::Error::other("unproven live Go socket custody"));
        }
        self.worker.matches()?;
        if let Some(backend) = &self.backend {
            backend.matches()?;
        }
        let stream = UnixStream::connect(socket)?;
        stream.set_read_timeout(Some(runtime::IO_TIMEOUT))?;
        stream.set_write_timeout(Some(runtime::IO_TIMEOUT))?;
        if runtime::peer_pid(&stream)? != self.shim.pid {
            return Err(io::Error::other("Go shim socket peer identity mismatch"));
        }
        self.shim.matches()?;
        Ok(stream)
    }

    pub(in crate::supervisor) fn dial(&self) -> io::Result<UnixStream> {
        let mut stream = self.connect("shim.sock")?;
        runtime::write_control(
            &mut stream,
            &json!({
                "type":"hello", "wire_version":SHIM_WIRE_VERSION, "snapshot_chunking":true,
            }),
        )?;
        let hello = runtime::read_control(&mut stream)?;
        if hello["type"] != "hello"
            || hello["wire_version"] != SHIM_WIRE_VERSION
            || hello["snapshot_chunking"] != true
            || hello["snapshot_only"] != true
            || hello["opencode_dispatch"] == true
        {
            return Err(io::Error::other(
                "Go shim wire or snapshot capability was refused",
            ));
        }
        Ok(stream)
    }

    pub(in crate::supervisor) fn snapshot(&self) -> io::Result<Value> {
        let mut shim = self.dial()?;
        runtime::write_control(&mut shim, &json!({"type":"snapshot_req"}))?;
        let snapshot: Value = serde_json::from_slice(&read_attach_snapshot(&mut shim)?)?;
        let rows = snapshot["rows"].as_u64().filter(|n| (1..=300).contains(n));
        let cols = snapshot["cols"].as_u64().filter(|n| (1..=300).contains(n));
        let lines = snapshot["lines"].as_array();
        let (Some(rows), Some(cols), Some(lines)) = (rows, cols, lines) else {
            return Err(io::Error::other("invalid Go grid"));
        };
        if snapshot["version"] != 1 || lines.len() as u64 != rows {
            return Err(io::Error::other("invalid Go grid version or line count"));
        }
        let lines = lines
            .iter()
            .map(|line| {
                let mut text = String::new();
                let runs = line["runs"]
                    .as_array()
                    .ok_or_else(|| io::Error::other("invalid Go grid line"))?;
                for run in runs {
                    let value = run["text"]
                        .as_str()
                        .ok_or_else(|| io::Error::other("invalid Go grid run"))?;
                    text.extend(value.chars().filter(|c| !c.is_control()));
                }
                Ok(text)
            })
            .collect::<io::Result<Vec<_>>>()?;
        Ok(
            json!({"cols":cols, "rows":rows, "lines":lines, "session":self.dir.file_name().and_then(|name| name.to_str())}),
        )
    }

    pub(super) fn retire(&self) {
        if let Some(controller) = self.lease.lock().unwrap().take() {
            let _ = controller.shim.shutdown(Shutdown::Both);
            let _ = controller.client.shutdown(Shutdown::Both);
        }
    }

    pub(in crate::supervisor) fn begin(
        &self,
        generation: u64,
        shim: &UnixStream,
        client: &UnixStream,
    ) -> io::Result<Lease<'_>> {
        let next = Controller {
            generation,
            shim: shim.try_clone()?,
            client: client.try_clone()?,
        };
        let mut lease = self.lease.lock().unwrap();
        if let Some(old) = lease.replace(next) {
            let _ = old.shim.shutdown(Shutdown::Both);
            let _ = old.client.shutdown(Shutdown::Both);
        }
        Ok(Lease(self, generation))
    }
}

fn valid_token(token: &str) -> bool {
    token.len() == 64
        && token
            .bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
}
