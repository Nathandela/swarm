use super::{Process, read_at};
use crate::{
    ipc::field,
    providers::{opencode, opencode_handoff},
    shim::opencode_backend::http::LocalSocket,
};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::{
    fs::File,
    io::{self, Read},
    path::Path,
};

const RELEASE: &str = "1.19.3";
const PLUGIN_SHA256: &str = "33c5d3e3ddcd906b7ed30cf44315a12dd3f5c628c11d079867b02e80714943a9";

pub(in crate::supervisor::stopped_go) fn managed(meta: &Value) -> bool {
    meta["agent_type"] == "opencode"
        && meta["schema_version"] == 3
        && meta["required_runtime"] == "opencode-tui"
}

fn hex(text: &str, length: usize) -> bool {
    text.len() == length
        && text
            .bytes()
            .all(|c| c.is_ascii_digit() || (b'a'..=b'f').contains(&c))
}

pub(super) fn launch(dir: &File, meta: &Value, path: &Path) -> io::Result<Value> {
    let mut ordinary = meta.clone();
    for option in ["required_runtime", "opencode_binding"] {
        ordinary[option] = Value::Null;
    }
    ordinary["schema_version"] = 1.into();
    super::super::qualified_role(&ordinary)?;

    let launch = super::super::read_launch(dir)?;
    let native = &launch["opencode"];
    let durable = &meta["opencode_binding"];
    let generation = field(durable, "generation");
    let created = durable["native_created_at_ms"].as_u64().unwrap_or_default();
    let mut expected = vec![launch["cli_identity"]["path"].clone()];
    for (key, option) in [("model", "--model"), ("agent", "--agent")] {
        let value = field(&meta["launch_options"], key);
        if !value.is_empty() {
            expected.extend([json!(option), json!(value)]);
        }
    }
    if launch["session_id"] != meta["id"]
        || launch["cwd"] != meta["cwd"]
        || Path::new(field(&launch, "session_dir")) != path
        || Path::new(field(&launch, "socket_path")) != path.join("shim.sock")
        || !super::super::executable_binding(meta, &launch, "opencode")
        || launch["cli_identity"]["version"] != RELEASE
        || launch["argv"] != json!(expected)
        || super::super::backend_declared(&launch)
        || [
            "agy_input",
            "claude_input",
            "hermes_input",
            "login_runtime",
            "account_binding",
        ]
        .iter()
        .any(|key| !launch[*key].is_null())
        || [
            "native_provider",
            "manager_replay_id",
            "input_embargo",
            "account_projection_ref",
            "account_state_root",
        ]
        .iter()
        .any(|key| !launch[*key].is_null() && launch[*key] != "")
        || [
            "require_launch_identity",
            "scheduled_codex_backend",
            "backend_required",
        ]
        .iter()
        .any(|key| !launch[*key].is_null() && launch[*key] != false)
        || native.as_object().is_none_or(|fields| fields.len() != 4)
        || durable["revision"].as_u64().is_none_or(|rev| rev == 0)
        || durable["phase"] != "ready"
        || durable["mode"] != "create"
        || !settled_initial(&durable["initial"])
        || !hex(generation, 32)
        || !(1..=9_007_199_254_740_991).contains(&created)
        || !opencode::valid_id(field(meta, "conversation_id"), "ses_")
        || native["root_id"] != meta["conversation_id"]
        || native["generation"] != generation
        || !hex(field(native, "embargo_token"), 64)
        || Path::new(field(native, "binding_file")) != path.join("opencode-binding.json")
    {
        return Err(io::Error::other(
            "unqualified original Go OpenCode launch or native generation",
        ));
    }
    Ok(launch)
}

fn settled_initial(initial: &Value) -> bool {
    initial.is_null()
        || initial["phase"] == "admitted"
            && initial["reason"].is_null()
            && hex(field(initial, "operation_id"), 32)
            && hex(field(initial, "request_hash"), 64)
            && opencode::valid_id(field(initial, "message_id"), "msg_")
            && !field(initial, "agent").is_empty()
            && field(initial, "model")
                .split_once('/')
                .is_some_and(|(provider, model)| !provider.is_empty() && !model.is_empty())
}

pub(super) struct Bridge {
    socket: LocalSocket,
    authorization: String,
    identity: Value,
}

impl Bridge {
    pub(super) fn open(
        path: &Path,
        dir: &File,
        meta: &Value,
        launch: &Value,
        worker: &Process,
    ) -> io::Result<Self> {
        let durable = &meta["opencode_binding"];
        let generation = &durable["generation"];
        let embargo = read_at(dir, c"opencode-input-embargo.json")?;
        let binding = read_at(dir, c"opencode-binding.json")?;
        let expected = json!({"version":1,"workerID":meta["id"],"sessionInstance":generation,
            "rootID":meta["conversation_id"],"directory":meta["cwd"],"backendGeneration":generation,
            "nativeCreatedAtMS":durable["native_created_at_ms"]});
        let mut expected_binding = expected.clone();
        expected_binding["nativeCreatedAtMS"] = 0.into();
        expected_binding["socketPath"] = json!(path.join("oc.sock"));
        expected_binding["token"] = binding["token"].clone();
        expected_binding["mode"] = "create".into();
        expected_binding["initialPrompt"] = (!durable["initial"].is_null()).into();

        let plugin = super::super::open_at(dir, c"opencode-tui.mjs", libc::O_RDONLY)?;
        let plugin = super::runtime::read_private_file(plugin, 128 << 10)?;
        let uri = format!("file://{}", path.join("opencode-tui.mjs").display());
        let config = read_at(dir, c"opencode-tui.json")?;
        if embargo != json!({"schema_version":1,"incident_id":generation,"held":false})
            || binding != expected_binding
            || !hex(field(&binding, "token"), 64)
            || format!("{:x}", Sha256::digest(&plugin)) != PLUGIN_SHA256
            || config
                != json!({"plugin":[[uri,{"bindingFile":launch["opencode"]["binding_file"]}]]})
        {
            return Err(io::Error::other(
                "unproved Go OpenCode plugin, bridge or input release",
            ));
        }
        let result = Self {
            socket: LocalSocket {
                path: path.join("oc.sock"),
                worker: worker.pid,
                birth: worker.birth,
            },
            authorization: format!("Bearer {}", field(&binding, "token")),
            identity: expected,
        };
        let ready = result.get("/ready")?;
        if ready["nativeVersion"] != RELEASE
            || ready["bound"] != true
            || ready["acknowledged"] != true
            || ready["inFlight"] != false
        {
            return Err(io::Error::other("Go OpenCode native backend is not ready"));
        }
        let initial = &durable["initial"];
        if !initial.is_null() {
            let receipt = result.get("/initial")?;
            let model = format!(
                "{}/{}",
                field(&receipt["model"], "providerID"),
                field(&receipt["model"], "modelID")
            );
            if receipt["status"] != "admitted"
                || receipt["inFlight"] != false
                || receipt["operationID"] != initial["operation_id"]
                || receipt["requestHash"] != initial["request_hash"]
                || receipt["messageID"] != initial["message_id"]
                || receipt["agent"] != initial["agent"]
                || model != initial["model"]
                || field(&receipt, "variant") != field(initial, "variant")
            {
                return Err(io::Error::other("Go OpenCode initial receipt is unproved"));
            }
        }
        result.history(1, "")?;
        Ok(result)
    }

    fn get(&self, route: &str) -> io::Result<Value> {
        let response = self
            .socket
            .agent(5)
            .get(format!("http://127.0.0.1{route}"))
            .header("Authorization", &self.authorization)
            .call()
            .map_err(|_| io::Error::other("Go OpenCode private bridge request failed"))?;
        if !response.status().is_success() {
            return Err(io::Error::other(
                "Go OpenCode private bridge refused the request",
            ));
        }
        let mut bytes = Vec::new();
        response
            .into_body()
            .into_reader()
            .take((8 << 20) + 1)
            .read_to_end(&mut bytes)?;
        if bytes.len() > 8 << 20 {
            return Err(io::Error::other("Go OpenCode bridge response too large"));
        }
        let result: Value = serde_json::from_slice(&bytes)?;
        if !self
            .identity
            .as_object()
            .expect("known identity")
            .iter()
            .all(|(key, value)| result[key] == *value)
        {
            return Err(io::Error::other("Go OpenCode native root identity changed"));
        }
        Ok(result)
    }

    pub(super) fn history(&self, limit: u64, before: &str) -> io::Result<Value> {
        if !(1..=200).contains(&limit)
            || before.len() > 4096
            || before
                .bytes()
                .any(|x| !(x.is_ascii_alphanumeric() || b"-_.~+/=".contains(&x)))
        {
            return Err(io::Error::other(
                "invalid Go OpenCode native history cursor",
            ));
        }
        let cursor = before
            .replace('+', "%2B")
            .replace('/', "%2F")
            .replace('=', "%3D");
        let page = self.get(&format!("/snapshot?limit={limit}&before={cursor}"))?;
        if page["hierarchyComplete"] != true
            || !page["descendants"].is_array()
            || !page["nextCursor"]
                .as_str()
                .is_some_and(|next| next.len() <= 4096 && (next.is_empty() || next != before))
            || !page["messages"]
                .as_array()
                .is_some_and(|messages| messages.len() as u64 <= limit)
        {
            return Err(io::Error::other("unqualified Go OpenCode history page"));
        }
        opencode_handoff::validate(
            &json!({"info":page["session"],"messages":page["messages"]}),
            &self.identity,
        )?;
        Ok(
            json!({"native_session_id":self.identity["rootID"],"native_created_at_ms":self.identity["nativeCreatedAtMS"],
            "session_instance":self.identity["sessionInstance"],"history":page["messages"],"next_cursor":page["nextCursor"],
            "descendants":page["descendants"],"hierarchy_scope":page["hierarchyScope"]}),
        )
    }
}
