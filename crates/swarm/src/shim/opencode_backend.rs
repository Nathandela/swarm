pub(crate) mod http;
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
const MAX_SNAPSHOT_REJECTION: u64 = 8 << 10;
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
        self.bridge_request(route, body, None)
    }

    fn bridge_request(
        &self,
        route: &str,
        body: Option<&Value>,
        snapshot_created: Option<u64>,
    ) -> io::Result<Value> {
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
        let status = response.status();
        let rejected = || io::Error::other(format!("OpenCode bridge HTTP {status}"));
        let snapshot_rejection = snapshot_created.filter(|_| {
            status.as_u16() == 422
                && response
                    .headers()
                    .get("content-type")
                    .is_some_and(|value| value.as_bytes() == b"application/json")
        });
        if !status.is_success() && snapshot_rejection.is_none() {
            return Err(rejected());
        }
        let limit = if snapshot_rejection.is_some() {
            MAX_SNAPSHOT_REJECTION
        } else {
            MAX_RESPONSE
        };
        let mut bytes = Vec::new();
        let read = response
            .into_body()
            .into_reader()
            .take(limit + 1)
            .read_to_end(&mut bytes);
        if let Some(created) = snapshot_rejection {
            if read.is_ok()
                && bytes.len() as u64 <= limit
                && let Ok(body) = serde_json::from_slice::<Value>(&bytes)
                && self.matches(&body, created)
                && body["status"] == "rejected"
                && let Some(reason) = snapshot_rejection_reason(&body)
            {
                return Err(io::Error::other(format!(
                    "OpenCode bridge HTTP {status} ({reason})"
                )));
            }
            return Err(rejected());
        }
        read?;
        if bytes.len() as u64 > limit {
            return Err(io::Error::other(
                "OpenCode bridge output exceeds size limit",
            ));
        }
        Ok(serde_json::from_slice(&bytes)?)
    }

    fn matches(&self, value: &Value, created: u64) -> bool {
        created > 0
            && created <= 9_007_199_254_740_991
            && self.matches_reservation(value)
            && value["nativeCreatedAtMS"] == created
    }

    fn matches_reservation(&self, value: &Value) -> bool {
        [
            "version",
            "workerID",
            "sessionInstance",
            "backendGeneration",
            "rootID",
            "directory",
        ]
        .iter()
        .all(|key| value[*key] == self.binding[*key])
    }

    fn envelope(&self) -> Value {
        json!({"version":1,"sessionInstance":self.binding["sessionInstance"],
            "backendGeneration":self.binding["backendGeneration"],"rootID":self.binding["rootID"]})
    }

    fn bound(&self) -> io::Result<Value> {
        let began = Instant::now();
        let mut last_readiness = "no correlated bridge response";
        while began.elapsed() < Duration::from_secs(22) {
            if let Ok(ready) = self.http("/ready", None) {
                if ready["bound"] == true && ready["ready"] == true {
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
                if !self.matches_reservation(&ready) {
                    thread::sleep(Duration::from_millis(80));
                    continue;
                }
                last_readiness = match ready["degraded"][0].as_str() {
                    Some("binding_pending") => "native session binding pending",
                    Some("native_not_ready") => "native runtime not ready",
                    Some("native_deadline") => "native bootstrap request timed out",
                    Some("native_read_failed") => "native bootstrap request failed",
                    Some("native_binding_mismatch") => {
                        "native bootstrap returned a different session"
                    }
                    Some("initial_plugin_conflict") => {
                        "active external plugin conflicts with initial input"
                    }
                    Some("route_mismatch") => "terminal is on a different native route",
                    Some("modal_open") => "native terminal dialog is open",
                    _ => "native binding unavailable",
                };
            }
            thread::sleep(Duration::from_millis(80));
        }
        Err(io::Error::other(format!(
            "OpenCode TUI did not acknowledge the reserved native session ({last_readiness})",
        )))
    }

    pub(super) fn launch(&self, initial: &Value, intent: &Value) -> io::Result<u64> {
        let ready = self.bound()?;
        let created = ready["nativeCreatedAtMS"].as_u64().unwrap_or_default();
        let mut ack = self.envelope();
        ack["nativeCreatedAtMS"] = json!(created);
        let database = runtime::read_json(&self.dir.join("session.json"))
            .and_then(|meta| {
                crate::providers::opencode_handoff::database_identity(&meta["opencode_profile"])
            })
            .unwrap_or(Value::Null);
        runtime::write_json(
            &self.dir.join("opencode-native.json"),
            &json!({
                "token":self.token,"rootID":self.binding["rootID"],"nativeCreatedAtMS":created,
            "workerID":self.binding["workerID"],
                "sessionInstance":self.binding["sessionInstance"],
                "backendGeneration":self.binding["backendGeneration"],
                "directory":self.binding["directory"],"nativeDatabase":database,
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
            let mut next_checkpoint = Instant::now();
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
                // Completion events are hints. Reconcile the durable prompt
                // independently when a notification is lost or contended.
                if Instant::now() >= next_checkpoint {
                    let _ = self.checkpoint_prompt(created);
                    next_checkpoint = Instant::now() + Duration::from_secs(2);
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
        let snapshot = self.bridge_request(
            &format!("/snapshot?limit={limit}&before={before}"),
            None,
            Some(created),
        )?;
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

fn snapshot_rejection_reason(body: &Value) -> Option<&'static str> {
    [
        "native_deadline",
        "native_read_failed",
        "native_binding_mismatch",
        "native_shape",
        "request_scope_unknown",
        "scope_limit",
        "response_oversized",
        "page_invalid",
    ]
    .into_iter()
    .find(|reason| *reason == field(body, "reason"))
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
    use std::io::Write;

    #[test]
    fn snapshot_reports_only_a_bounded_correlated_bridge_reason() {
        let dir =
            crate::test_temp::root().join(format!("oc-{}", &runtime::random_id().unwrap()[..12]));
        runtime::private_dir(&dir).unwrap();
        let socket = dir.join("p");
        let listener = runtime::bind(&socket).unwrap();
        let created = 123;
        let body = json!({
            "version": 1, "workerID": "worker", "sessionInstance": "instance",
            "backendGeneration": "generation", "rootID": "ses_0123456789",
            "directory": "/private-fixture", "nativeCreatedAtMS": created,
            "status": "rejected", "reason": "native_deadline"
        });
        let mut binding = body.clone();
        binding["socketPath"] = json!(socket);
        binding["token"] = json!("fixture-token");
        let backend =
            Backend::new(binding, "1.19.3", std::process::id(), &dir, "fixture-token").unwrap();

        let replace = |key: &str, value| {
            let mut changed = body.clone();
            changed[key] = value;
            changed
        };
        const SNAPSHOT: &str = "/snapshot?limit=1&before=";
        const JSON: &str = "application/json";
        let oversized = replace(
            "padding",
            json!("a".repeat(MAX_SNAPSHOT_REJECTION as usize)),
        );
        let cases = [
            (SNAPSHOT, JSON, body.clone()),
            (SNAPSHOT, JSON, replace("reason", json!("untrusted detail"))),
            (
                SNAPSHOT,
                JSON,
                replace("nativeCreatedAtMS", json!(created + 1)),
            ),
            (
                SNAPSHOT,
                JSON,
                replace("backendGeneration", json!("other-generation")),
            ),
            (SNAPSHOT, JSON, replace("status", json!("admitted"))),
            (SNAPSHOT, "text/plain", body.clone()),
            (SNAPSHOT, JSON, oversized),
            ("/ready", JSON, body),
        ];
        let server = thread::spawn(move || {
            for (route, content_type, payload) in cases {
                let (mut stream, _) = listener.accept().unwrap();
                stream.set_read_timeout(Some(runtime::IO_TIMEOUT)).unwrap();
                let mut reader = BufReader::new(&mut stream);
                let mut line = String::new();
                reader.read_line(&mut line).unwrap();
                assert_eq!(line, format!("GET {route} HTTP/1.1\r\n"));
                loop {
                    line.clear();
                    assert!(reader.read_line(&mut line).unwrap() > 0);
                    if line == "\r\n" {
                        break;
                    }
                }
                let payload = payload.to_string();
                let response = format!(
                    "HTTP/1.1 422 Unprocessable Entity\r\nContent-Type: {content_type}\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{payload}",
                    payload.len()
                );
                if let Err(error) = stream.write_all(response.as_bytes()) {
                    assert_eq!(error.kind(), io::ErrorKind::BrokenPipe);
                }
            }
        });
        let generic = "OpenCode bridge HTTP 422 Unprocessable Entity";
        assert_eq!(
            backend.snapshot(created, 1, "").unwrap_err().to_string(),
            format!("{generic} (native_deadline)")
        );
        for case in 0..6 {
            assert_eq!(
                backend.snapshot(created, 1, "").unwrap_err().to_string(),
                generic,
                "rejected case {case}"
            );
        }
        assert_eq!(
            backend.http("/ready", None).unwrap_err().to_string(),
            generic
        );
        server.join().unwrap();
        std::fs::remove_dir_all(dir).unwrap();
    }

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
