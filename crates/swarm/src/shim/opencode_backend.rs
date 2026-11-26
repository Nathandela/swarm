mod http;
mod operations;

use crate::{ipc::field, providers::opencode, supervisor::runtime};
use http::LocalSocket;
use serde_json::{Value, json};
use std::{
    io::{self, BufRead, BufReader, Read},
    path::{Path, PathBuf},
    sync::{
        Mutex,
        atomic::{AtomicBool, AtomicU64, Ordering},
    },
    thread,
    time::{Duration, Instant},
};

const MAX_RESPONSE: u64 = 2 << 20;
const URL: &str = "http://127.0.0.1";

pub(super) struct Backend {
    binding: Value,
    cli_version: String,
    socket: LocalSocket,
    token: String,
    dir: PathBuf,
    pub ready: AtomicBool,
    actions: Mutex<()>,
}

impl Backend {
    pub(super) fn new(
        binding: Value,
        cli_version: &str,
        worker: u32,
        dir: &Path,
        token: &str,
    ) -> io::Result<Self> {
        Ok(Self {
            socket: LocalSocket {
                path: PathBuf::from(field(&binding, "socketPath")),
                worker,
                birth: runtime::birth(worker)?,
            },
            binding,
            cli_version: cli_version.to_owned(),
            token: token.to_owned(),
            dir: dir.to_owned(),
            ready: AtomicBool::new(false),
            actions: Mutex::new(()),
        })
    }

    pub(super) fn worker_birth(&self) -> u64 {
        self.socket.birth
    }

    fn http(&self, route: &str, body: Option<&Value>) -> io::Result<Value> {
        let agent = self
            .socket
            .agent(if matches!(route, "/ready" | "/ack" | "/dispatchDigest") {
                3
            } else {
                13
            });
        let url = format!("{URL}{route}");
        let authorization = format!("Bearer {}", field(&self.binding, "token"));
        let response = match body {
            Some(value) => {
                if serde_json::to_vec(value)?.len() > 64 << 10 {
                    return Err(io::Error::other("OpenCode bridge input exceeds size limit"));
                }
                agent
                    .post(&url)
                    .header("Authorization", &authorization)
                    .send_json(value)
            }
            None => agent
                .get(&url)
                .header("Authorization", &authorization)
                .call(),
        }
        .map_err(|error| match error {
            ureq::Error::Timeout(_) => {
                io::Error::new(io::ErrorKind::TimedOut, "OpenCode bridge request timed out")
            }
            _ => io::Error::other("OpenCode bridge request failed"),
        })?;
        if !response.status().is_success() {
            return Err(io::Error::other(format!(
                "OpenCode bridge HTTP {}",
                response.status()
            )));
        }
        let mut bytes = Vec::new();
        response
            .into_body()
            .into_reader()
            .take(MAX_RESPONSE + 1)
            .read_to_end(&mut bytes)?;
        if bytes.len() as u64 > MAX_RESPONSE {
            return Err(io::Error::other(
                "OpenCode bridge output exceeds size limit",
            ));
        }
        Ok(serde_json::from_slice(&bytes)?)
    }

    fn matches(&self, value: &Value, created: u64) -> bool {
        created > 0
            && created <= 9_007_199_254_740_991
            && [
                "version",
                "workerID",
                "sessionInstance",
                "backendGeneration",
                "rootID",
                "directory",
            ]
            .iter()
            .all(|key| value[*key] == self.binding[*key])
            && value["nativeCreatedAtMS"] == created
    }

    fn envelope(&self) -> Value {
        json!({"version":1,"sessionInstance":self.binding["sessionInstance"],
            "backendGeneration":self.binding["backendGeneration"],"rootID":self.binding["rootID"]})
    }

    fn bound(&self) -> io::Result<Value> {
        let began = Instant::now();
        while began.elapsed() < Duration::from_secs(22) {
            if let Ok(ready) = self.http("/ready", None)
                && ready["bound"] == true
                && ready["ready"] == true
            {
                let created = ready["nativeCreatedAtMS"].as_u64().unwrap_or_default();
                if self.matches(&ready, created)
                    && (self.binding["mode"] != "resume"
                        || self.binding["nativeCreatedAtMS"] == created)
                    && ready["nativeVersion"] == self.cli_version
                {
                    return Ok(ready);
                }
                return Err(io::Error::other(
                    "OpenCode native runtime did not prove the reserved binding",
                ));
            }
            thread::sleep(Duration::from_millis(80));
        }
        Err(io::Error::other(
            "OpenCode TUI did not acknowledge the reserved native session",
        ))
    }

    pub(super) fn launch(&self, initial: &Value, intent: &Value) -> io::Result<u64> {
        let ready = self.bound()?;
        let created = ready["nativeCreatedAtMS"].as_u64().unwrap_or_default();
        let mut ack = self.envelope();
        ack["nativeCreatedAtMS"] = json!(created);
        runtime::write_json(
            &self.dir.join("opencode-native.json"),
            &json!({
                "token":self.token,"rootID":self.binding["rootID"],"nativeCreatedAtMS":created,
            "workerID":self.binding["workerID"],
                "sessionInstance":self.binding["sessionInstance"],
                "backendGeneration":self.binding["backendGeneration"],
                "directory":self.binding["directory"],
            }),
        )?;
        let ready = self.http("/ack", Some(&ack))?;
        if !self.matches(&ready, created) || ready["ready"] != true || ready["acknowledged"] != true
        {
            return Err(io::Error::other(
                "OpenCode TUI binding acknowledgement failed",
            ));
        }
        if self.binding["initialPrompt"] == true {
            let mut body = self.envelope();
            body["operationID"] = intent["operationID"].clone();
            body["requestHash"] = intent["requestHash"].clone();
            for key in ["text", "expectedModel", "expectedAgent"] {
                body[key] = initial[key].clone();
            }
            let native = opencode::receipts::native(&self.dir, &self.token)?;
            let receipt_path = self.dir.join("opencode-initial.json");
            let record =
                |witness: &Value| opencode::initial::record(&self.token, &native, intent, witness);
            runtime::write_json(&receipt_path, &record(&Value::Null))?;
            // Receipt reads can settle a lost reply without reissuing the initial input.
            let mut receipt = self
                .http("/initial", Some(&body))
                .ok()
                .map(|witness| record(&witness));
            if !receipt
                .as_ref()
                .is_some_and(|r| opencode::initial::valid(&self.token, &native, intent, r))
            {
                receipt = self
                    .http("/initial", None)
                    .ok()
                    .map(|witness| record(&witness));
            }
            let receipt = receipt
                .filter(|r| opencode::initial::valid(&self.token, &native, intent, r))
                .ok_or_else(|| io::Error::other("OpenCode initial input outcome is unknown"))?;
            runtime::write_json(&receipt_path, &receipt)?;
            if receipt["state"] == "rejected" {
                return Err(io::Error::other(format!(
                    "OpenCode initial input rejected ({})",
                    field(&receipt["bridge_receipt"], "reason")
                )));
            }
        }
        self.ready.store(true, Ordering::Release);
        Ok(created)
    }

    pub(super) fn run(&self, initial: &Value, intent: &Value) {
        let launched = self.launch(initial, intent);
        if runtime::write_json(
            &self.dir.join("opencode-launch.json"),
            &match &launched {
                Ok(_) => json!({"token":self.token,"ready":true}),
                Err(error) => json!({"token":self.token,"ready":false,"reason":error.to_string()}),
            },
        )
        .is_err()
        {
            self.ready.store(false, Ordering::Release);
            return;
        }
        let Ok(created) = launched else { return };
        let epoch = AtomicU64::new(0);
        thread::scope(|scope| {
            let (updates, pending) = std::sync::mpsc::sync_channel(1);
            let epoch = &epoch;
            scope.spawn(move || {
                loop {
                    let outcome = self.observe(created, epoch, &updates);
                    if epoch.load(Ordering::Acquire) % 2 == 1 {
                        epoch.fetch_add(1, Ordering::AcqRel);
                    }
                    let _ = updates.try_send(());
                    if let Err(error) = outcome {
                        let _ = self.publish(None, Some(&error));
                    }
                    thread::sleep(Duration::from_millis(500));
                }
            });
            loop {
                let current = epoch.load(Ordering::Acquire);
                if current % 2 == 1 {
                    match self.snapshot(created, 1, "") {
                        Ok(snapshot) if epoch.load(Ordering::Acquire) == current => {
                            let _ = self.publish(Some(&snapshot), None);
                        }
                        Err(error) => {
                            let _ = self.publish(None, Some(&error));
                        }
                        Ok(_) => {}
                    }
                }
                let _ = pending.recv_timeout(Duration::from_secs(2));
                thread::sleep(Duration::from_millis(80));
            }
        });
    }

    fn observe(
        &self,
        created: u64,
        epoch: &AtomicU64,
        updates: &std::sync::mpsc::SyncSender<()>,
    ) -> io::Result<()> {
        let agent = self.socket.agent(90);
        let stream = agent
            .get(format!("{URL}/events"))
            .header(
                "Authorization",
                format!("Bearer {}", field(&self.binding, "token")),
            )
            .call()
            .map_err(|_| io::Error::other("OpenCode event stream unavailable"))?;
        if !stream.status().is_success()
            || !stream
                .headers()
                .get("content-type")
                .and_then(|v| v.to_str().ok())
                .is_some_and(|content| content.split(';').next() == Some("text/event-stream"))
        {
            return Err(io::Error::other("invalid OpenCode event response"));
        }
        let mut reader = BufReader::new(stream.into_body().into_reader());
        let first = event(&mut reader)?;
        if first["type"] != "swarm.bridge.connected" || !self.matches(&first["properties"], created)
        {
            return Err(io::Error::other("OpenCode event stream identity mismatch"));
        }
        epoch.fetch_add(1, Ordering::AcqRel);
        let _ = updates.try_send(());
        loop {
            let update = event(&mut reader)?;
            if update["type"] == "swarm.bridge.gap"
                || !update["type"].is_string()
                || !update["properties"].is_object()
            {
                return Err(io::Error::other(
                    "OpenCode event stream requires reconciliation",
                ));
            }
            let _ = updates.try_send(());
            let _ = self.checkpoint_terminal(created, &update);
        }
    }

    pub(super) fn snapshot(&self, created: u64, limit: u64, before: &str) -> io::Result<Value> {
        if !(1..=200).contains(&limit)
            || before.len() > 4096
            || before
                .bytes()
                .any(|ch| !(ch.is_ascii_alphanumeric() || b"-_.~+/=".contains(&ch)))
        {
            return Err(io::Error::other("invalid OpenCode history page"));
        }
        let before = before
            .replace('+', "%2B")
            .replace('/', "%2F")
            .replace('=', "%3D");
        let snapshot = self.http(&format!("/snapshot?limit={limit}&before={before}"), None)?;
        if !self.matches(&snapshot, created)
            || snapshot["hierarchyComplete"] != true
            || !valid_statuses(&snapshot["statuses"])
            || !snapshot["permissions"].is_array()
            || !snapshot["questions"].is_array()
            || !snapshot["descendants"].is_array()
            || !snapshot["messages"].is_array()
        {
            return Err(io::Error::other("OpenCode snapshot is unqualified"));
        }
        Ok(snapshot)
    }

    fn publish(&self, snapshot: Option<&Value>, error: Option<&io::Error>) -> io::Result<()> {
        let status = match snapshot {
            Some(snapshot) => status(snapshot, field(&self.binding, "rootID")),
            None => {
                json!({"Process":"running","Turn":"unknown","Interaction":"none","ObservationGap":true})
            }
        };
        runtime::write_json(
            &self.dir.join("observation.json"),
            &json!({"token":self.token,"status":status,"reason":error.map(ToString::to_string),"observed_ms":crate::ipc::spawn::now_ms()}),
        )
    }
}

fn event(reader: &mut impl BufRead) -> io::Result<Value> {
    let mut data = Vec::new();
    let mut line = Vec::new();
    loop {
        line.clear();
        let remaining = (1 << 20) - data.len();
        let count = reader
            .take(remaining as u64 + 1)
            .read_until(b'\n', &mut line)?;
        if count == 0 || count > remaining || line.last() != Some(&b'\n') {
            return Err(io::Error::other(
                "OpenCode event stream ended or exceeded size limit",
            ));
        }
        if line == b"\n" || line == b"\r\n" {
            if !data.is_empty() {
                return Ok(serde_json::from_slice(&data)?);
            }
        } else if let Some(value) = line.strip_prefix(b"data:") {
            data.extend_from_slice(value);
        }
    }
}

pub(super) fn in_scope(snapshot: &Value, root: &str, target: &str) -> bool {
    opencode::valid_id(target, "ses_")
        && (target == root
            || snapshot["descendants"]
                .as_array()
                .is_some_and(|rows| rows.iter().any(|row| row["id"] == target)))
}

fn valid_statuses(statuses: &Value) -> bool {
    statuses.as_object().is_some_and(|all| {
        all.iter().all(|(id, status)| {
            opencode::valid_id(id, "ses_")
                && matches!(field(status, "type"), "idle" | "busy" | "retry")
        })
    })
}

fn status(snapshot: &Value, root: &str) -> Value {
    let related = |id: &str| in_scope(snapshot, root, id);
    let busy = snapshot["statuses"]
        .as_object()
        .unwrap()
        .iter()
        .any(|(id, status)| related(id) && matches!(field(status, "type"), "busy" | "retry"));
    let any = |name: &str| {
        snapshot[name]
            .as_array()
            .unwrap()
            .iter()
            .any(|req| related(field(req, "sessionID")))
    };
    let interaction = if any("permissions") {
        "permission"
    } else if any("questions") {
        "prompt"
    } else {
        "none"
    };
    json!({"Process":"running","Turn":if busy { "active" } else {"idle"},
        "Interaction":interaction, "Attention":interaction != "none"})
}

fn encode_component(value: &str) -> String {
    let mut out = String::new();
    for byte in value.bytes() {
        if byte.is_ascii_alphanumeric() || b"-_.~".contains(&byte) {
            out.push(char::from(byte));
        } else {
            use std::fmt::Write;
            write!(out, "%{byte:02X}").expect("write to string");
        }
    }
    out
}

impl Backend {
    pub(super) fn history(&self, limit: u64, before: &str) -> io::Result<Value> {
        self.history_page(limit, before, false)
    }

    pub(super) fn history_page(
        &self,
        limit: u64,
        before: &str,
        with_native_identity: bool,
    ) -> io::Result<Value> {
        let native = opencode::receipts::native(&self.dir, &self.token)?;
        let snapshot = self.snapshot(
            native["nativeCreatedAtMS"].as_u64().unwrap_or_default(),
            limit,
            before,
        )?;
        let mut page = json!({
            "native_session_id":native["rootID"],"native_created_at_ms":native["nativeCreatedAtMS"],
            "session_instance":native["sessionInstance"],"history":snapshot["messages"],"next_cursor":snapshot["nextCursor"],
            "descendants":snapshot["descendants"],"hierarchy_scope":snapshot["hierarchyScope"],
        });
        if with_native_identity {
            page["info"] = snapshot["session"].clone();
            page["backend_generation"] = native["backendGeneration"].clone();
            page["native_version"] = json!(self.cli_version);
        }
        Ok(page)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn absent_native_status_is_idle_but_malformed_status_is_unqualified() {
        let root = "ses_0011223344";
        let mut snapshot = json!({"statuses":{},"permissions":[],"questions":[],"descendants":[]});
        assert!(valid_statuses(&snapshot["statuses"]));
        assert_eq!(status(&snapshot, root)["Turn"], "idle");
        snapshot["statuses"][root] = json!({"type":"unexpected"});
        assert!(!valid_statuses(&snapshot["statuses"]));
        snapshot["statuses"][root] = Value::Null;
        assert!(!valid_statuses(&snapshot["statuses"]));
    }
}
