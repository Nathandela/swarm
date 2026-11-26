//! Owner protocol client for a single historical Discord dispatcher binding.
use super::config::{Config, invalid};
use crate::ipc::{Client, field};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::io;
use std::path::Path;

pub(super) const CAPS: &[&str] = &[
    "owner-launch",
    "manager-launch",
    "manager-provenance",
    "semantic-send",
    "message-origin",
];

pub(super) struct Manager {
    cfg: Config,
    discussion: String,
    conversation: String,
}

impl Manager {
    pub(super) fn new(cfg: &Config) -> io::Result<Self> {
        if cfg.text("mode") != "personal" {
            return Err(invalid("manager binding requires a personal installation"));
        }
        let coordinate = serde_json::to_vec(&json!([
            cfg.text("installation_id"),
            "",
            cfg.text("dm_channel_id")
        ]))?;
        let digest = format!("{:x}", Sha256::digest(coordinate));
        let discussion = format!("dt_{}", &digest[..32]);
        let conversation = format!("discord-teammate-{discussion}");
        Ok(Self {
            cfg: cfg.clone(),
            discussion,
            conversation,
        })
    }

    pub(super) fn mail_root(&self) -> io::Result<std::path::PathBuf> {
        let _ = self.dial()?;
        match self.cfg.0["supervisor"]["state_dir"].as_str() {
            Some(root) => std::fs::canonicalize(root),
            None => std::fs::canonicalize(crate::ipc::state_path()?),
        }
    }

    fn dial(&self) -> io::Result<Client> {
        let client = Client::dial(Path::new(self.cfg.text("daemon_socket")), CAPS)?;
        self.cfg.check_supervisor(&client, CAPS)?;
        Ok(client)
    }

    pub(super) fn verify_supervisor(&self) -> io::Result<()> {
        self.dial().map(drop)
    }

    pub(super) fn launch_request(&self, bridge_socket: &Path, auth_file: &Path) -> Value {
        json!({
            "agent": self.cfg.text("manager_cli"),
            "name": self.cfg.text("name"),
            "cwd": self.cfg.text("work_dir"),
            "options": {},
            "env": null,
            "cols":120, "rows":32, "initial_prompt":"",
            "manager":{
                "conversation_id": self.conversation,
                "bridge_socket": bridge_socket, "auth_file": auth_file
            }
        })
    }

    pub(super) fn launch(&self, operation: &str, launch: &Value) -> io::Result<Value> {
        if launch["manager"]["conversation_id"] != self.conversation {
            return Err(invalid("manager launch has a different discussion binding"));
        }
        let mut client = self.dial()?;
        let reply = client.exchange(
            json!({
                "op":"owner_launch", "operation_id": operation, "launch": launch
            }),
            "launch",
            crate::ipc::launch_timeout(self.cfg.text("manager_cli")),
        )?;
        self.verify_endpoint(&client, &reply)?;
        let session = &reply["session"];
        if reply["operation_id"] != operation
            || session["role"] != "manager"
            || session["manager_conversation_id"] != self.conversation
            || session["agent"] != self.cfg.text("manager_cli")
            || !field(session, "id").starts_with(&format!("{}/", client.endpoint()))
            || session["endpoint_id"] != client.endpoint()
            || session["cwd"] != self.cfg.text("work_dir")
        {
            return Err(invalid("uncorrelated scoped manager launch receipt"));
        }
        Ok(session.clone())
    }

    pub(super) fn launch_status(&self, operation: &str) -> io::Result<Value> {
        let mut client = self.dial()?;
        let reply = client.request_body(
            json!({
                "op":"owner_launch_status", "subject_operation_id":operation
            }),
            "owner_launch_status",
        )?;
        self.verify_endpoint(&client, &reply)?;
        if reply["subject_operation_id"] != operation || !reply["operation_outcome"].is_object() {
            return Err(invalid("uncorrelated scoped manager launch status"));
        }
        Ok(reply["operation_outcome"].clone())
    }

    pub(super) fn session(&self, id: &str) -> io::Result<Value> {
        let mut client = self.dial()?;
        let endpoint = client.endpoint().to_owned();
        let rows = client.sessions()?;
        let Some(row) = rows.into_iter().find(|row| row["id"] == id) else {
            return Err(io::Error::new(
                io::ErrorKind::NotFound,
                "manager session is missing",
            ));
        };
        if row["endpoint_id"] != endpoint
            || row["role"] != "manager"
            || row["manager_conversation_id"] != self.conversation
            || row["agent"] != self.cfg.text("manager_cli")
            || row["cwd"] != self.cfg.text("work_dir")
            || row["status"]["Process"] != "running"
            || !valid(field(&row["capabilities"], "session_instance"))
        {
            return Err(invalid(
                "session does not prove the enrolled manager incarnation",
            ));
        }
        Ok(row)
    }

    pub(super) fn runtime(
        &self,
        session: &str,
        instance: &str,
        launch: &Value,
    ) -> io::Result<Value> {
        let mut client = self.dial()?;
        if !valid(instance) || !session.starts_with(&format!("{}/", client.endpoint())) {
            return Err(invalid(
                "manager runtime requires the exact historical supervisor session",
            ));
        }
        let reply = client.request_body(
            json!({
                "op":"manager_runtime", "session_id":session, "body_version":1,
                "manager_runtime_request":{"session_instance":instance}
            }),
            "manager_runtime",
        )?;
        self.verify_endpoint(&client, &reply)?;
        let status = &reply["manager_runtime"];
        let binding = &status["binding"];
        if reply["session_id"] != session
            || binding["session"] != session
            || binding["session_instance"] != instance
            || binding["conversation_id"] != self.conversation
            || binding["provider"] != self.cfg.text("manager_cli")
            || status["bridge_socket"] != launch["manager"]["bridge_socket"]
            || status["auth_file"] != launch["manager"]["auth_file"]
            || [
                "native_session_id",
                "native_generation",
                "observation_generation",
                "login_epoch",
            ]
            .iter()
            .any(|key| !valid(field(binding, key)))
            || !status["turn"]["external_input"].is_u64()
        {
            return Err(invalid(
                "manager runtime custody or provenance no longer matches",
            ));
        }
        Ok(status.clone())
    }

    pub(super) fn composer(&self, input: &Value, runtime: &Value) -> io::Result<Value> {
        let binding = &runtime["binding"];
        let actor = field(&input["author"], "id");
        let channel = field(&input["route"], "channel_id");
        if runtime["ready_for_input"] != true
            || runtime["provider_exhausted"] == true
            || runtime["turn"]["externally_steered"] == true
            || !matches!(field(&runtime["turn"], "state"), "idle" | "completed")
            || binding["conversation_id"] != self.conversation
            || input["supervisor_socket"] != self.cfg.text("daemon_socket")
            || input["authority"] != "owner_direct"
            || !field(&input["route"], "guild_id").is_empty()
            || actor != self.cfg.text("owner_id")
            || channel != self.cfg.text("dm_channel_id")
        {
            return Err(invalid(
                "manager or source is outside the current personal discussion grant",
            ));
        }
        let nonce = super::manager_nonce(
            &self.discussion,
            &input["operation_id"],
            &input["revision"],
            binding,
            &runtime["turn"]["external_input"],
        )?;
        let mut composer = json!({
            "session": binding["session"], "session_instance": binding["session_instance"],
            "text": input["text"],
            "manager_admission": {
                "binding": binding, "expected_external_input": runtime["turn"]["external_input"],
                "nonce":nonce,
            },
            "origin": {
                "kind":"owner_direct", "installation_id":self.cfg.text("installation_id"),
                "request_id":input["operation_id"], "actor_id":actor, "requester_id":actor,
                "channel_id":channel, "message_id":input["route"]["message_id"],
                "discussion_id":self.discussion, "revision":input["revision"],
            }
        });
        if input["voice_origin"].is_object() {
            composer["origin"] = input["voice_origin"].clone();
        }
        Ok(composer)
    }

    pub(super) fn send(&self, operation: &str, composer: &Value) -> io::Result<()> {
        let mut client = self.dial()?;
        if composer["manager_admission"]["binding"]["conversation_id"] != self.conversation {
            return Err(invalid("refusing unbound native manager input"));
        }
        let reply = client.request_body(
            json!({
                "op":"semantic_send", "session_id": composer["session"], "body_version":1,
                "operation_id":operation, "composer_send":composer
            }),
            "ok",
        )?;
        self.verify_endpoint(&client, &reply)?;
        if reply["operation_id"] != operation {
            return Err(invalid(
                "uncorrelated scoped manager admission acknowledgement",
            ));
        }
        Ok(())
    }

    pub(super) fn input_status(&self, composer: &Value) -> io::Result<Value> {
        let mut client = self.dial()?;
        let admission = &composer["manager_admission"];
        let reply = client.request_body(
            json!({
                "op":"manager_input_status", "session_id":composer["session"], "body_version":1,
                "manager_input_status_request":admission
            }),
            "manager_input_status",
        )?;
        self.verify_endpoint(&client, &reply)?;
        let turn = &reply["manager_input_status"];
        if reply["session_id"] != composer["session"]
            || turn["native_input_id"] != admission["nonce"]
            || !turn["external_input"].is_u64()
            || turn["externally_steered"] == true
        {
            return Err(invalid("uncorrelated manager native input observation"));
        }
        Ok(turn.clone())
    }

    pub(super) fn prove_publication(
        &self,
        composer: &Value,
        tool: &str,
        arguments_hash: &str,
        meta: &Value,
    ) -> io::Result<Value> {
        let mut client = self.dial()?;
        let admission = &composer["manager_admission"];
        let coordinates = super::provenance::coordinates(&admission["binding"], meta)?;
        let reply = client.request_body(
            json!({
                "op":"manager_resolve_call", "session_id":composer["session"], "body_version":1,
                "manager_call":{"binding":admission["binding"],"tool_name":tool,
                                "arguments_hash":arguments_hash,"host_meta":meta}
            }),
            "manager_resolve_call",
        )?;
        self.verify_endpoint(&client, &reply)?;
        let proof = &reply["manager_call_proof"];
        if reply["session_id"] != composer["session"]
            || proof["main"] != true
            || proof["native_input_id"] != admission["nonce"]
            || !valid(field(proof, "native_turn_id"))
            || proof["native_call_id"] != coordinates.call
            || coordinates
                .turn
                .is_some_and(|turn| proof["native_turn_id"] != turn)
        {
            return Err(invalid(
                "publication is not from the admitted native manager turn",
            ));
        }
        Ok(proof.clone())
    }

    fn verify_endpoint(&self, client: &Client, reply: &Value) -> io::Result<()> {
        if reply["endpoint_id"] != client.endpoint() {
            return Err(invalid("uncorrelated historical supervisor response"));
        }
        Ok(())
    }
}

fn valid(value: &str) -> bool {
    !value.is_empty()
        && value.len() <= 256
        && value.trim() == value
        && !value.chars().any(char::is_control)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::supervisor::runtime;
    use std::fs::{self, File, OpenOptions};
    use std::io::Write;
    use std::os::unix::fs::OpenOptionsExt;
    use std::process::{Child, Command, Stdio};
    use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

    struct OwnedFixture {
        child: Child,
        root: std::path::PathBuf,
        socket: std::path::PathBuf,
    }

    impl Drop for OwnedFixture {
        fn drop(&mut self) {
            if let Ok(mut client) = Client::dial(&self.socket, &[])
                && let Ok(rows) = client.sessions()
            {
                for row in rows {
                    let id = field(&row, "id");
                    let _ = client.request("kill", Some(id), "ok");
                }
            }
            let _ = self.child.kill();
            let _ = self.child.wait();
            let _ = fs::remove_dir_all(&self.root);
        }
    }

    struct FixtureAudience {
        source: Value,
        posts: std::cell::RefCell<Vec<(String, String)>>,
    }

    impl super::super::dispatcher::Audience for FixtureAudience {
        fn current(&self, _cfg: &Config, id: &str) -> io::Result<Option<Value>> {
            Ok((self.source["id"] == id).then(|| self.source.clone()))
        }

        fn verify(&self, cfg: &Config) -> io::Result<()> {
            assert_eq!(cfg.text("dm_channel_id"), "400");
            Ok(())
        }

        fn post(&self, cfg: &Config, nonce: &str, text: &str) -> super::super::rest::Post {
            self.posts.borrow_mut().push((nonce.into(), text.into()));
            super::super::rest::Post::Acknowledged(json!({
                "id":"402","channel_id":cfg.text("dm_channel_id"),"author":{"id":"200","bot":true},
                "nonce":nonce,"content":text
            }))
        }
    }

    #[test]
    #[ignore = "requires an isolated Go or Rust supervisor and fake-Codex binaries; no network calls"]
    fn pinned_go_owner_protocol_and_scoped_launch() {
        let rust = std::env::var_os("SWARM_RUST_MANAGER_ORACLE");
        let go = rust
            .clone()
            .or_else(|| std::env::var_os("SWARM_GO_ORACLE"))
            .expect("set exact owner oracle binary");
        let codex = std::env::var_os("SWARM_FAKE_CODEX").expect("set exact fake-Codex binary");
        let root = Path::new("/tmp").join(format!("gdm-{}", &runtime::random_id().unwrap()[..10]));
        runtime::private_dir(&root).unwrap();
        let root = fs::canonicalize(root).unwrap();
        let bin = root.join("bin");
        runtime::private_dir(&bin).unwrap();
        let codex_fixture = bin.join("fixture");
        fs::copy(codex, &codex_fixture).unwrap();
        let mut wrapper = OpenOptions::new()
            .write(true)
            .create_new(true)
            .mode(0o700)
            .open(bin.join("codex"))
            .unwrap();
        let cold_start = if rust.is_some() {
            "app-server) /bin/sleep 5 ;;"
        } else {
            ""
        };
        writeln!(wrapper, "#!/bin/sh\ncase \"$1\" in\n--version) echo 'codex-cli 0.167.0'; exit 0 ;;\n{cold_start}\nesac\nexec '{}/fixture' \"$@\"", bin.display()).unwrap();
        drop(wrapper);
        let state = root.join("state");
        let socket = root.join("daemon.sock");
        runtime::private_dir(&state).unwrap();
        let native_home = root.join(".codex");
        runtime::private_dir(&native_home).unwrap();
        runtime::write_json(
            &native_home.join("auth.json"),
            &json!({
                "auth_mode":"chatgpt", "tokens":{"account_id":"fixture-account"}
            }),
        )
        .unwrap();
        let enrollment = state.join("discord");
        runtime::private_dir(&enrollment).unwrap();
        runtime::write_json(&enrollment.join("config.json"), &json!({
            "version":1,"enabled":true,"mode":"personal","name":"fixture","installation_id":"fixture",
            "owner_id":"300","application_id":"200","dm_channel_id":"400","manager_cli":"codex",
            "daemon_socket":socket,"token_file":root.join("synthetic-token"),"work_dir":root
        })).unwrap();
        if rust.is_some() {
            runtime::write_json(
                &enrollment.join(".rust-offline-owner.json"),
                &json!("fixture"),
            )
            .unwrap();
        }
        let launch = Command::new(go)
            .arg("daemon")
            .env_clear()
            .env("HOME", &root)
            .env("PATH", format!("{}:/usr/bin:/bin", bin.display()))
            .env("SWARM_DAEMON_STATE", &state)
            .env("SWARM_DAEMON_SOCK", &socket)
            .env("SWARM_DAEMON_LOCK", root.join("daemon.lock"))
            .env("SWARM_DAEMON_LOG", root.join("daemon.log"))
            .stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(File::create(root.join("startup.log")).unwrap())
            .spawn()
            .unwrap();
        let _cleanup = OwnedFixture {
            child: launch,
            root: root.clone(),
            socket: socket.clone(),
        };
        let cfg = Config(
            json!({"mode":"personal","installation_id":"fixture","dm_channel_id":"400","owner_id":"300",
            "manager_cli":"codex","name":"fixture","application_id":"200","work_dir":root,"daemon_socket":socket,"supervisor":{"state_dir":state}}),
        );
        let manager = Manager::new(&cfg).unwrap();
        let deadline = Instant::now() + Duration::from_secs(5);
        loop {
            if manager.verify_supervisor().is_ok() {
                break;
            }
            assert!(
                Instant::now() < deadline,
                "pinned Go owner endpoint did not negotiate manager capability: {:?}. {}",
                manager.verify_supervisor(),
                fs::read_to_string(root.join("startup.log")).unwrap_or_default()
            );
            std::thread::sleep(Duration::from_millis(25));
        }
        let bridge = enrollment.join("manager-v1");
        runtime::private_dir(&bridge).unwrap();
        let bridge_socket = bridge.join("bridge.sock");
        let auth = bridge.join("bridge.auth");
        let _listener = runtime::bind(&bridge_socket).unwrap();
        let mut credential = OpenOptions::new()
            .create_new(true)
            .write(true)
            .mode(0o600)
            .open(&auth)
            .unwrap();
        write!(credential, "{}", "a".repeat(64)).unwrap();
        let request = manager.launch_request(&bridge_socket, &auth);
        let operation = format!(
            "launch:{}:{:032x}",
            SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_millis(),
            1
        );
        assert_eq!(
            manager.launch_status(&operation).unwrap()["state"],
            "unknown_operation"
        );
        if rust.is_some() {
            assert!(manager.launch(&operation, &request).is_err());
            let mut inbox = super::super::receipt::Inbox::open(&enrollment, &cfg).unwrap();
            inbox
                .set_manager(json!({"generation":1,"launch":request,"launch_operation":operation}))
                .unwrap();
        }
        let session = manager.launch(&operation, &request).unwrap();
        let receipt = manager.launch_status(&operation).unwrap();
        assert_eq!(receipt["state"], "applied");
        assert_eq!(receipt["session_id"], session["id"]);
        if rust.is_some() {
            let duplicate = format!(
                "launch:{}:{:032x}",
                SystemTime::now()
                    .duration_since(UNIX_EPOCH)
                    .unwrap()
                    .as_millis(),
                3
            );
            let err = manager.launch(&duplicate, &request).unwrap_err();
            assert!(err.to_string().contains("persisted operation"));
        }
        let id = field(&session, "id");
        let deadline = Instant::now() + Duration::from_secs(7);
        let row = loop {
            if let Ok(row) = manager.session(id) {
                assert!(valid(field(&row["capabilities"], "session_instance")));
                break row;
            }
            assert!(
                Instant::now() < deadline,
                "pinned Go fixture did not expose a scoped running incarnation: {:?}, daemon log: {}",
                manager.dial().unwrap().sessions().unwrap(),
                fs::read_to_string(root.join("daemon.log")).unwrap_or_default()
            );
            std::thread::sleep(Duration::from_millis(100));
        };
        if std::env::var_os("SWARM_FAKE_CODEX_MANAGER_PROOF").is_some() {
            let instance = field(&row["capabilities"], "session_instance");
            let deadline = Instant::now() + Duration::from_secs(8);
            let native = loop {
                let status = manager.runtime(id, instance, &request);
                if let Ok(status) = &status
                    && status["ready_for_input"] == true
                {
                    break status.clone();
                }
                assert!(
                    Instant::now() < deadline,
                    "fixture did not produce native manager custody: {status:?}"
                );
                std::thread::sleep(Duration::from_millis(100));
            };
            let source = json!({"id":"401","type":0,"content":"go fixture input","attachments":[],
                "channel_id":"400","author":{"id":"300"}});
            let mut inbox = super::super::receipt::Inbox::open(&enrollment, &cfg).unwrap();
            inbox.checkpoint("400").unwrap();
            let admitted = inbox.admit(&source).unwrap().unwrap();
            let composer = manager.composer(&admitted, &native).unwrap();
            let launch_operation = operation.clone();
            let operation = format!(
                "semantic:{}:{:032x}",
                SystemTime::now()
                    .duration_since(UNIX_EPOCH)
                    .unwrap()
                    .as_millis(),
                2
            );
            if rust.is_some() {
                inbox.cutover("401",field(&admitted,"revision"),json!({
                    "generation":1,"launch":request,"launch_operation":launch_operation,"session":id,"binding":native["binding"],
                    "input":{"operation":operation,"source_id":"401","composer":composer}
                })).unwrap();
            }
            if rust.is_some() {
                let mut forged = composer.clone();
                forged["text"] = json!("fabricated Discord text");
                let err = manager.send(&operation, &forged).unwrap_err();
                assert!(err.to_string().contains("exact connector cutover"));
            }
            let wake_fixture = std::env::var_os("SWARM_NATIVE_MAIL_WAKE_FIXTURE").is_some();
            if wake_fixture {
                std::fs::write(root.join(".fixture-mail-wake-enabled"), "").unwrap();
            }
            manager.send(&operation, &composer).unwrap();
            let deadline = Instant::now() + Duration::from_secs(5);
            let turn = loop {
                let evidence = manager.input_status(&composer);
                if let Ok(evidence) = &evidence
                    && evidence["state"] == "running"
                {
                    break evidence.clone();
                }
                assert!(
                    Instant::now() < deadline,
                    "fixture did not produce observed native input: {evidence:?}"
                );
                std::thread::sleep(Duration::from_millis(100));
            };
            if rust.is_some() {
                let mut altered = composer.clone();
                altered["origin"]["installation_id"] = json!("other-installation");
                assert!(manager.send(&operation, &altered).is_err());
                let mut unbound = composer.clone();
                unbound.as_object_mut().unwrap().shift_remove("origin");
                unbound
                    .as_object_mut()
                    .unwrap()
                    .shift_remove("manager_admission");
                let mut owner = manager.dial().unwrap();
                assert!(
                    owner
                        .request_body(
                            json!({"op":"semantic_send","body_version":1,"operation_id":operation,
                    "session_id":id,"composer_send":unbound}),
                            "ok"
                        )
                        .is_err()
                );
                assert!(
                    owner
                        .request_body(
                            json!({"op":"send_input","session_id":id,
                    "send_input":{"text":"raw fixture refusal"}}),
                            "ok"
                        )
                        .is_err()
                );
            }
            let session = &native["binding"]["native_session_id"];
            let host = json!({"callId":"fixture-native-call","threadId":session,"sessionId":session,
                "x-codex-turn-metadata":{"turn_id":turn["native_turn_id"],"thread_id":session,"session_id":session}});
            let mut args =
                json!({"output_id":"final","sequence":1,"text":"go-fixture text","final":true});
            args.sort_all_objects();
            let proof = manager
                .prove_publication(
                    &composer,
                    "publish",
                    &super::super::receipt::hash(&args).unwrap(),
                    &host,
                )
                .unwrap();
            assert_eq!(
                proof["native_input_id"],
                composer["manager_admission"]["nonce"]
            );
            assert_eq!(proof["native_call_id"], "fixture-native-call");
            if let Some(binary) = rust.as_ref() {
                drop(_listener);
                let dispatcher = super::super::dispatcher::Dispatcher::open(&cfg, &enrollment)
                    .unwrap()
                    .unwrap();
                let fake = FixtureAudience {
                    source,
                    posts: std::cell::RefCell::new(Vec::new()),
                };
                let local = id.split_once('/').unwrap().1;
                if std::env::var_os("SWARM_NATIVE_MAIL_FIXTURE").is_some() {
                    let owner =
                        runtime::read_json(&state.join("rust-v1").join(local).join("session.json"))
                            .unwrap();
                    let proof = crate::supervisor::mail::proof(field(&owner, "token"), "spawn");
                    let attempt = format!("launch:{}:{:032x}", crate::ipc::spawn::now_ms(), 47);
                    let document = root.join("fixture-delegate.md");
                    std::fs::write(
                        &document,
                        "# Fixture delegation\nUse your own worker note.\n",
                    )
                    .unwrap();
                    let launch_worker = || {
                        Command::new(binary)
                            .env_clear()
                            .env("HOME", &root)
                            .env("TMPDIR", &root)
                            .env("PATH", format!("{}:/usr/bin:/bin", bin.display()))
                            .env("SWARM_DAEMON_STATE", &state)
                            .env("SWARM_DAEMON_SOCK", &socket)
                            .env("SWARM_SESSION_ID", local)
                            .env("SWARM_DISPATCHER_SPAWN_PROOF", &proof)
                            .arg("spawn")
                            .arg("--cli")
                            .arg("codex")
                            .arg("--dir")
                            .arg(&root)
                            .args([
                                "--worktree=false",
                                "--delegate",
                                document.to_str().unwrap(),
                                "--operation-id",
                                &attempt,
                            ])
                            .output()
                            .unwrap()
                    };
                    let worker = launch_worker();
                    assert!(
                        worker.status.success(),
                        "worker launch: {}",
                        String::from_utf8_lossy(&worker.stderr)
                    );
                    let worker = String::from_utf8(worker.stdout).unwrap().trim().to_owned();
                    let replay = launch_worker();
                    assert!(replay.status.success());
                    assert_eq!(String::from_utf8(replay.stdout).unwrap().trim(), worker);
                    let worker_local = worker.split_once('/').unwrap().1;
                    let meta = runtime::read_json(
                        &state
                            .join("rust-v1")
                            .join(worker_local)
                            .join("session.json"),
                    )
                    .unwrap();
                    assert_eq!(meta["mailbox"]["attempt"], attempt);
                    assert_eq!(meta["mailbox"]["parent"], local);
                    let worker_proof =
                        crate::supervisor::mail::proof(field(&meta, "token"), "worker");
                    let cli = |args: &[&str], credential: &str| {
                        Command::new(binary)
                            .arg("teammate")
                            .args(args)
                            .env_clear()
                            .env(crate::teammate::SOCKET_ENV, &socket)
                            .env(crate::teammate::PROOF_ENV, credential)
                            .env("SWARM_SESSION_ID", worker_local)
                            .output()
                            .unwrap()
                    };
                    let decode = |out: std::process::Output| {
                        assert!(
                            out.status.success(),
                            "teammate: {}",
                            String::from_utf8_lossy(&out.stderr)
                        );
                        serde_json::from_slice::<Value>(&out.stdout).unwrap()
                    };
                    let submitted = decode(cli(
                        &["message", "--kind", "needs-input", "--text", "Which path?"],
                        &worker_proof,
                    ));
                    let note = field(&submitted, "id");
                    assert_eq!(submitted["state"], "queued");
                    assert!(
                        !cli(&["receipt", "--id", note], &"a".repeat(64))
                            .status
                            .success()
                    );
                    assert_eq!(
                        decode(cli(
                            &[
                                "message",
                                "--id",
                                note,
                                "--kind",
                                "needs-input",
                                "--text",
                                "Which path?"
                            ],
                            &worker_proof
                        )),
                        submitted
                    );
                    let mut invoke = |tool: &str, call_id: &str, arguments: Value| {
                        let mut meta = host.clone();
                        meta["callId"] = json!(call_id);
                        let mut proxy = Command::new(binary)
                            .arg("manager-mcp")
                            .arg(&bridge_socket)
                            .arg(&auth)
                            .arg(field(&native["binding"], "conversation_id"))
                            .arg(local)
                            .stdin(Stdio::piped())
                            .stdout(Stdio::piped())
                            .stderr(Stdio::null())
                            .spawn()
                            .unwrap();
                        writeln!(
                            proxy.stdin.take().unwrap(),
                            "{}",
                            json!({"jsonrpc":"2.0","id":1,"method":"tools/call",
                            "params":{"name":tool,"arguments":arguments,"_meta":meta}})
                        )
                        .unwrap();
                        let deadline = Instant::now() + Duration::from_secs(5);
                        while proxy.try_wait().unwrap().is_none() {
                            dispatcher.tick(&cfg, &mut inbox, &fake).unwrap();
                            assert!(Instant::now() < deadline, "mailbox tool did not answer");
                            std::thread::sleep(Duration::from_millis(10));
                        }
                        let result = decode(proxy.wait_with_output().unwrap());
                        assert!(result["result"]["isError"].is_null(), "{result}");
                        serde_json::from_str::<Value>(
                            result["result"]["content"][0]["text"].as_str().unwrap(),
                        )
                        .unwrap()
                    };
                    let listed = invoke("swarm_read_note", "fixture-mail-list", json!({}));
                    assert_eq!(listed["notes"][0]["id"], note);
                    let answer = invoke(
                        "swarm_reply",
                        "fixture-mail-reply",
                        json!({"text":"Use src"}),
                    );
                    assert_eq!(answer["id"], note);
                    assert_eq!(
                        decode(cli(&["receipt", "--id", note], &worker_proof))["state"],
                        "answered"
                    );
                    let readback = decode(cli(&["reply", "--id", note], &worker_proof));
                    assert_eq!(readback["text"], "Use src");
                    assert_eq!(
                        decode(cli(&["reply", "--id", note], &worker_proof)),
                        readback
                    );
                    let complete = decode(cli(
                        &["message", "--kind", "complete", "--text", "Finished"],
                        &worker_proof,
                    ));
                    let listed = invoke("swarm_read_note", "fixture-mail-list", json!({}));
                    assert_eq!(listed["notes"][0]["id"], complete["id"]);
                    if std::env::var_os("SWARM_NATIVE_MAIL_FILES_FIXTURE").is_some() {
                        let csv = root.join("data.csv");
                        let pdf = root.join("report.pdf");
                        std::fs::write(&csv, "source,result\nfixture,ok\n").unwrap();
                        std::fs::write(&pdf, "%PDF-1.7\n").unwrap();
                        let receipt = decode(cli(
                            &[
                                "message",
                                "--kind",
                                "complete",
                                "--file",
                                csv.to_str().unwrap(),
                                "--file",
                                pdf.to_str().unwrap(),
                            ],
                            &worker_proof,
                        ));
                        let note = receipt["id"].as_str().unwrap();
                        assert_eq!(receipt["files"].as_array().unwrap().len(), 2);
                        std::fs::write(&csv, "edited after queue").unwrap();
                        assert_eq!(
                            decode(cli(&["receipt", "--id", note], &worker_proof)),
                            receipt
                        );
                        let listed = invoke("swarm_read_note", "fixture-mail-list", json!({}));
                        assert!(
                            listed["notes"]
                                .as_array()
                                .unwrap()
                                .iter()
                                .any(|entry| entry["id"] == note
                                    && entry["files"] == receipt["files"])
                        );
                    }
                }
                let mut proxy = Command::new(binary)
                    .arg("manager-mcp")
                    .arg(&bridge_socket)
                    .arg(&auth)
                    .arg(field(&native["binding"], "conversation_id"))
                    .arg(local)
                    .stdin(Stdio::piped())
                    .stdout(Stdio::piped())
                    .stderr(Stdio::null())
                    .spawn()
                    .unwrap();
                let mut to_proxy = proxy.stdin.take().unwrap();
                writeln!(
                    to_proxy,
                    "{}",
                    json!({"jsonrpc":"2.0","id":1,"method":"tools/call",
                    "params":{"name":"publish","arguments":args,"_meta":host}})
                )
                .unwrap();
                drop(to_proxy);
                let deadline = Instant::now() + Duration::from_secs(5);
                while fake.posts.borrow().is_empty() {
                    dispatcher.tick(&cfg, &mut inbox, &fake).unwrap();
                    assert!(
                        Instant::now() < deadline,
                        "native tool never reached scoped publication"
                    );
                    std::thread::sleep(Duration::from_millis(10));
                }
                dispatcher.tick(&cfg, &mut inbox, &fake).unwrap();
                let response = proxy.wait_with_output().unwrap();
                assert!(response.status.success());
                let receipt: Value = serde_json::from_slice(&response.stdout).unwrap();
                assert!(receipt["result"]["isError"].is_null());
                assert!(!String::from_utf8_lossy(&response.stdout).contains(&"a".repeat(64)));
                let posts = fake.posts.borrow();
                assert_eq!(posts.len(), 1);
                assert_eq!(posts[0].1, "go-fixture text");
                assert_eq!(inbox.publication(&posts[0].0)["rest_acknowledged"], true);
                drop(posts);
                if wake_fixture {
                    drop(dispatcher);
                    std::fs::write(root.join(".fixture-mail-complete"), "").unwrap();
                    let dispatcher = super::super::dispatcher::Dispatcher::open(&cfg, &enrollment)
                        .unwrap()
                        .unwrap();
                    let deadline = Instant::now() + Duration::from_secs(8);
                    let cancel = std::env::var_os("SWARM_NATIVE_MAIL_CANCEL_FIXTURE").is_some();
                    if cancel {
                        let prepared = std::env::var("SWARM_NATIVE_MAIL_CANCEL_FIXTURE").as_deref()
                            == Ok("prepared");
                        if prepared {
                            std::fs::write(root.join(".fixture-mail-hold-turn"), "").unwrap();
                        }
                        let lock = (!prepared).then(|| {
                            super::super::lock_manager_mail_effect(&dispatcher.scope(&cfg)).unwrap()
                        });
                        loop {
                            let attempt = dispatcher.tick(&cfg, &mut inbox, &fake);
                            if !inbox.manager()["input"]["worker_wake"].is_null() {
                                attempt.unwrap();
                                break;
                            }
                            attempt.unwrap();
                            assert!(
                                Instant::now() < deadline,
                                "did not reserve mailbox wake: {}, observed {:?}",
                                inbox.manager(),
                                manager.input_status(&composer)
                            );
                            std::thread::sleep(Duration::from_millis(20));
                        }
                        let refused = inbox.manager()["input"]["composer"].clone();
                        assert_eq!(
                            manager.input_status(&refused).unwrap()["state"],
                            if prepared { "prepared" } else { "unknown" }
                        );
                        let mut withdrawal = fake.source.clone();
                        match std::env::var("SWARM_NATIVE_MAIL_CANCEL_FIXTURE").as_deref() {
                            Ok("edited") => {
                                withdrawal["content"] = json!("Edited after cutover");
                                withdrawal["edited_timestamp"] = json!("2026-01-01T00:00:00Z");
                            }
                            Ok("empty") => withdrawal["content"] = json!(""),
                            Ok("attachment") => withdrawal["attachments"] = json!([{"id":"430"}]),
                            _ => withdrawal["id"] = json!("407"),
                        }
                        let withdrawn = FixtureAudience {
                            source: withdrawal,
                            posts: Default::default(),
                        };
                        dispatcher.tick(&cfg, &mut inbox, &withdrawn).unwrap();
                        assert_eq!(inbox.manager()["input"]["composer"], refused);
                        drop(lock);
                        dispatcher.tick(&cfg, &mut inbox, &withdrawn).unwrap();
                        if prepared {
                            assert_eq!(inbox.manager()["input"]["composer"], refused);
                            assert_eq!(
                                manager.input_status(&refused).unwrap()["state"],
                                "prepared"
                            );
                        } else {
                            assert!(inbox.manager()["input"].is_null());
                            assert_eq!(manager.input_status(&refused).unwrap()["state"], "unknown");
                            let mut replacement = fake.source.clone();
                            replacement["id"] = json!("407");
                            replacement["content"] = json!("Independent DM after withdrawn source");
                            let other = FixtureAudience {
                                source: replacement.clone(),
                                posts: Default::default(),
                            };
                            inbox.admit(&replacement).unwrap().unwrap();
                            dispatcher.tick(&cfg, &mut inbox, &other).unwrap();
                            assert_eq!(inbox.manager()["input"]["source_id"], "407");
                            assert!(inbox.manager()["input"]["worker_wake"].is_null());
                        }
                    } else {
                        let wake = loop {
                            dispatcher.tick(&cfg, &mut inbox, &fake).unwrap();
                            let wake = &inbox.manager()["input"];
                            if !wake["worker_wake"].is_null() && !wake["turn_id"].is_null() {
                                break wake.clone();
                            }
                            assert!(
                                Instant::now() < deadline,
                                "did not observe a mailbox wake: {}",
                                inbox.manager()
                            );
                            std::thread::sleep(Duration::from_millis(30));
                        };
                        assert_ne!(wake["turn_id"], turn["native_turn_id"]);
                        assert_eq!(wake["source_id"], "401");
                        assert!(field(&wake["composer"], "text").contains("Finished"));
                        for _ in 0..3 {
                            dispatcher.tick(&cfg, &mut inbox, &fake).unwrap();
                            assert_eq!(inbox.manager()["input"], wake);
                        }
                        let mut wake_host = host.clone();
                        wake_host["callId"] = json!("fixture-wake-read");
                        wake_host["x-codex-turn-metadata"]["turn_id"] = wake["turn_id"].clone();
                        let mut proxy = Command::new(binary)
                            .arg("manager-mcp")
                            .arg(&bridge_socket)
                            .arg(&auth)
                            .arg(field(&native["binding"], "conversation_id"))
                            .arg(local)
                            .stdin(Stdio::piped())
                            .stdout(Stdio::piped())
                            .spawn()
                            .unwrap();
                        writeln!(
                            proxy.stdin.take().unwrap(),
                            "{}",
                            json!({"jsonrpc":"2.0","id":1,"method":"tools/call",
                        "params":{"name":"swarm_read_note","arguments":{},"_meta":wake_host}})
                        )
                        .unwrap();
                        let deadline = Instant::now() + Duration::from_secs(5);
                        while proxy.try_wait().unwrap().is_none() {
                            dispatcher.tick(&cfg, &mut inbox, &fake).unwrap();
                            assert!(Instant::now() < deadline, "wake note tool timed out");
                            std::thread::sleep(Duration::from_millis(15));
                        }
                        let response = proxy.wait_with_output().unwrap();
                        let result: Value = serde_json::from_slice(&response.stdout).unwrap();
                        assert!(result["result"]["isError"].is_null(), "{result}");
                        let selected: Value =
                            serde_json::from_str(field(&result["result"]["content"][0], "text"))
                                .unwrap();
                        assert_eq!(selected["id"], wake["worker_wake"]["id"]);
                        assert_eq!(selected["state"], "ingested");
                    }
                    assert_eq!(fake.posts.borrow().len(), 1);
                }
            }
            let mut stale = host;
            stale["x-codex-turn-metadata"]["turn_id"] = json!("old-turn");
            assert!(
                manager
                    .prove_publication(
                        &composer,
                        "publish",
                        &super::super::receipt::hash(&args).unwrap(),
                        &stale
                    )
                    .is_err()
            );
        }
        let mut different = request.clone();
        different["manager"]["conversation_id"] = json!("discord-teammate-different");
        assert!(manager.launch(&operation, &different).is_err());
    }
}
