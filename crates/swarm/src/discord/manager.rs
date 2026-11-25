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
        let reply = client.request_body(
            json!({
                "op":"owner_launch", "operation_id": operation, "launch": launch
            }),
            "launch",
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
        let nonce = format!(
            "{:x}",
            Sha256::digest(serde_json::to_vec(&json!([
                self.discussion,
                input["operation_id"],
                input["revision"],
                binding,
                runtime["turn"]["external_input"]
            ]))?)
        );
        Ok(json!({
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
        }))
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
            || !valid(field(proof, "native_call_id"))
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

    #[test]
    #[ignore = "requires local pinned Go Swarm and fake-Codex binaries; executes no Discord or provider network calls"]
    fn pinned_go_owner_protocol_and_scoped_launch() {
        let go = std::env::var_os("SWARM_GO_ORACLE").expect("set exact Go oracle binary");
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
        writeln!(wrapper, "#!/bin/sh\ncase \"$1\" in\n--version) echo 'codex-cli 0.167.0'; exit 0 ;;\nesac\nexec '{}/fixture' \"$@\"", bin.display()).unwrap();
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
            "manager_cli":"codex","name":"fixture","work_dir":root,"daemon_socket":socket,"supervisor":{"state_dir":state}}),
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
        let bridge = root.join("bridge");
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
        let session = manager.launch(&operation, &request).unwrap();
        let receipt = manager.launch_status(&operation).unwrap();
        assert_eq!(receipt["state"], "applied");
        assert_eq!(receipt["session_id"], session["id"]);
        let id = field(&session, "id");
        let deadline = Instant::now() + Duration::from_secs(7);
        loop {
            if let Ok(row) = manager.session(id) {
                assert!(valid(field(&row["capabilities"], "session_instance")));
                break;
            }
            assert!(
                Instant::now() < deadline,
                "pinned Go fixture did not expose a scoped running incarnation: {:?}, daemon log: {}",
                manager.dial().unwrap().sessions().unwrap(),
                fs::read_to_string(root.join("daemon.log")).unwrap_or_default()
            );
            std::thread::sleep(Duration::from_millis(100));
        }
        let mut different = request.clone();
        different["manager"]["conversation_id"] = json!("discord-teammate-different");
        assert!(manager.launch(&operation, &different).is_err());
    }
}
