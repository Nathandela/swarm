use super::*;
use crate::providers::hermes::{classic_id, native};
use sha2::{Digest, Sha256};

pub(super) fn unknown_native_status() -> Value {
    json!({"Process":"lost","Turn":"unknown","Interaction":"error","Attention":true,"NativeWorkUnknown":true})
}

pub(super) struct Descriptor {
    raw: Value,
    binding: native::Binding,
}

impl Descriptor {
    fn instance(&self) -> &str {
        field(&self.raw, "generation")
    }

    pub(super) fn validate_target(&self) -> io::Result<String> {
        self.binding.validate()?;
        Ok(self.instance().to_owned())
    }

    fn receipt(
        &self,
        session: &str,
        operation: &str,
        state: &str,
        admission: Option<Value>,
    ) -> Value {
        json!({
            "session":session,"session_instance":self.instance(),"operation_id":operation,
            "agent":"hermes","state":state,"native_session_id":self.binding.id(),
            "native_generation":self.instance(),"native_admission":admission
        })
    }
}

impl Session {
    fn hermes_conversation(&self) -> io::Result<String> {
        let observed = runtime::read_json(&self.dir.join("observation.json"))?;
        let id = field(&observed, "conversation_id");
        if observed["token"] != self.token || !classic_id(id) {
            return Err(io::Error::other(
                "classic Hermes conversation identity is unproven",
            ));
        }
        Ok(id.to_owned())
    }

    pub(super) fn hermes_descriptor(&self) -> io::Result<Descriptor> {
        let descriptor = runtime::read_json(&self.dir.join(native::DESCRIPTOR))?;
        let owner = runtime::read_json(&self.dir.join("owner.json"))?;
        let meta = runtime::read_json(&self.dir.join("session.json"))?;
        if meta["token"] != self.token
            || owner["token"] != self.token
            || !owner["pid"].is_u64()
            || !owner["birth"].is_u64()
            || descriptor["token"] != self.token
            || descriptor["worker_id"] != self.id
            || descriptor["shim_pid"] != owner["pid"]
            || descriptor["shim_birth"] != owner["birth"]
            || descriptor["provider_version"] != meta["provider"]["version"]
            || !runtime::valid_id(field(&descriptor, "generation"))
            || descriptor["binding"]["directory"].as_str().map(Path::new) != Some(&self.effective)
        {
            return Err(io::Error::other(
                "Hermes native descriptor does not match its launch",
            ));
        }
        let binding = native::Binding::load(descriptor["binding"].clone())?;
        Ok(Descriptor {
            raw: descriptor,
            binding,
        })
    }

    pub(super) fn hermes_resume(&self, launch: &mut Value, cwd: &mut PathBuf) -> io::Result<()> {
        if self.is_live() || self.dial().is_ok() {
            return Err(io::Error::other(
                "Hermes saved-session resume requires a detached viewer",
            ));
        }
        let meta = runtime::read_json(&self.dir.join("session.json"))?;
        if meta["token"] != self.token || meta["id"] != self.id {
            return Err(io::Error::other("Hermes resume reservation changed"));
        }
        let profile = field(&meta["provider"], "hermes_profile");
        if !field(&launch["options"], "profile").is_empty()
            && field(&launch["options"], "profile") != profile
        {
            return Err(io::Error::other(
                "Hermes saved-session resume cannot change its profile",
            ));
        }
        launch["options"]["profile"] = json!(profile);
        if self.hermes_native {
            self.hermes_check_successor()?;
            if launch["worktree"] == true
                || (*cwd != Path::new(&self.cwd) && *cwd != self.effective)
            {
                return Err(io::Error::other(
                    "native Hermes resume retains the source workspace",
                ));
            }
            let descriptor = self.hermes_descriptor()?;
            *cwd = fs::canonicalize(&self.effective)?;
            if Path::new(field(&descriptor.binding.0, "directory")) != *cwd {
                return Err(io::Error::other("Hermes saved working directory changed"));
            }
            launch["hermes_retained_binding"] = descriptor.binding.0;
            launch["hermes_retained_origins"] = descriptor.raw["origins"].clone();
        } else {
            launch["resume_conversation_id"] = json!(self.hermes_conversation()?);
        }
        Ok(())
    }

    pub(super) fn hermes_view_fields(&self, view: &mut Value) {
        let version = runtime::read_json(&self.dir.join("session.json"))
            .ok()
            .map(|record| record["provider"]["version"].clone())
            .unwrap_or_default();
        let mut capabilities = json!({"provider":"hermes","provider_version":version,"adapter_revision":"1",
            "session_instance":codex_receipt::hash(&format!("hermes-view:{}",self.token)),"terminal_control":!self.hermes_native,
            "terminal_fallback":!self.hermes_native,"structured_chat":false});
        if self.hermes_native {
            view["provider_mode"] = json!("shared-native");
            if view["status"]["NativeWorkUnknown"] == true {
                view["resume_error"] = json!(
                    "Hermes viewer is unavailable; native job completion is unproven and the workspace is retained"
                );
            }
            if let Ok(descriptor) = self.hermes_descriptor() {
                view["conversation_id"] = json!(descriptor.binding.id());
                if view["status"]["Process"] == "running"
                    && view["status"]["Interaction"] != "error"
                {
                    capabilities["session_instance"] = json!(descriptor.instance());
                    capabilities["native_owner_input"] = json!(true);
                }
            }
        } else {
            view["provider_mode"] = json!("classic");
            if let Ok(id) = self.hermes_conversation() {
                view["conversation_id"] = json!(id);
            }
        }
        view["capabilities"] = capabilities;
    }

    pub(super) fn hermes_kill(&self) -> io::Result<()> {
        let _gate = self.hermes_authority.lock().unwrap();
        if self.dir.join("hermes-successor.json").try_exists()? {
            return Err(io::Error::other(
                "Hermes native stop authority belongs to the retained successor",
            ));
        }
        let descriptor = self.hermes_descriptor()?;
        runtime::write_json(&self.dir.join("hermes-stop.json"), &descriptor.raw)?;
        let result = descriptor.binding.stop();
        if let Ok(mut shim) = self.dial() {
            let _ = runtime::write_control(&mut shim, &json!({"type":"signal","sig":"term"}));
            let _ = runtime::read_control(&mut shim);
        }
        result?;
        Err(io::Error::other(
            "Hermes scoped cancellation requested; native job completion is unproven",
        ))
    }

    pub(super) fn hermes_semantic_send(
        &self,
        request: &Value,
        session: &str,
        state: &crate::supervisor::State,
    ) -> io::Result<Value> {
        let body = &request["composer_send"];
        let operation = field(request, "operation_id");
        let text = field(body, "text");
        let descriptor = self.hermes_descriptor()?;
        let instance = descriptor.instance();
        if request["body_version"] != 1
            || body["session"] != session
            || body["session_instance"] != instance
            || !codex_receipt::timely(operation)
            || text.is_empty()
            || text.len() > 4096
            || !(body["expected_turn"].is_null() || body["expected_turn"] == "")
            || body.as_object().is_none_or(|object| {
                object.iter().any(|(key, value)| {
                    !matches!(
                        key.as_str(),
                        "session" | "session_instance" | "text" | "expected_turn" | "origin"
                    ) && !value.is_null()
                })
            })
        {
            return Err(io::Error::other("invalid or stale Hermes semantic request"));
        }
        let intent = if body["origin"].is_null() {
            text.to_owned()
        } else {
            validate_origin(&body["origin"])?;
            let mut intent = json!({"text":text,"origin":body["origin"]});
            intent.sort_all_objects();
            intent.to_string()
        };
        let _authority = self.hermes_authority.lock().unwrap();
        {
            let digest = Sha256::digest(operation);
            let _operation = state.operation_locks
                [usize::from(digest[0]) % state.operation_locks.len()]
            .lock()
            .unwrap();
            codex_receipt::bind(&state.root, operation, session, instance, &intent)?;
        }
        let dir = self.dir.join("hermes-semantic");
        runtime::private_dir(&dir)?;
        fs::File::open(&self.dir)?.sync_all()?;
        if let Some(receipt) = self.hermes_semantic_status(operation, session, &state.root)? {
            return Ok(receipt);
        }
        if let Ok(prior) = runtime::read_json(&dir.join("pending.json")) {
            let prior = prior
                .as_str()
                .ok_or_else(|| io::Error::other("damaged Hermes pending operation"))?;
            if !self
                .hermes_semantic_status(prior, session, &state.root)?
                .is_some_and(|r| matches!(field(&r, "state"), "rejected" | "submitted"))
            {
                return Err(io::Error::other(
                    "earlier Hermes admission remains unconfirmed",
                ));
            }
        } else {
            match dir.join("pending.json").symlink_metadata() {
                Err(err) if err.kind() == io::ErrorKind::NotFound => {}
                _ => return Err(io::Error::other("unreadable Hermes pending operation")),
            }
        }
        self.dial()?;
        if self.dir.join("hermes-stop.json").try_exists()? {
            return Err(io::Error::other("Hermes native viewer is stopping"));
        }
        descriptor.binding.validate()?;
        let path = semantic_path(&dir, operation);
        let mut record = json!({"descriptor":descriptor.raw,"intent":codex_receipt::hash(&intent),"receipt":
            descriptor.receipt(session, operation, "uncertain", None)});
        runtime::write_json(&path, &record)?;
        runtime::write_json(&dir.join("pending.json"), &json!(operation))?;
        let result = match descriptor.binding.submit(operation, text) {
            Ok(admission) => descriptor.receipt(session, operation, "submitted", Some(admission)),
            Err(err) if err.kind() == io::ErrorKind::PermissionDenied => {
                descriptor.receipt(session, operation, "rejected", None)
            }
            Err(_) => return Ok(record["receipt"].clone()),
        };
        record["receipt"] = result.clone();
        runtime::write_json(&path, &record)?;
        Ok(result)
    }

    pub(super) fn hermes_semantic_status(
        &self,
        operation: &str,
        session: &str,
        root: &Path,
    ) -> io::Result<Option<Value>> {
        let path = semantic_path(&self.dir.join("hermes-semantic"), operation);
        let mut record = match runtime::read_json(&path) {
            Ok(record) => record,
            Err(err) if err.kind() == io::ErrorKind::NotFound => return Ok(None),
            Err(err) => return Err(err),
        };
        let descriptor = self.hermes_descriptor()?;
        if record["descriptor"] != descriptor.raw
            || record["receipt"]["session_instance"] != descriptor.instance()
            || record["receipt"]["session"] != session
            || record["receipt"]["operation_id"] != operation
            || record["receipt"]["native_session_id"] != descriptor.binding.id()
        {
            return Err(io::Error::other("unproven Hermes admission record"));
        }
        codex_receipt::verify_binding(
            root,
            operation,
            session,
            descriptor.instance(),
            field(&record, "intent"),
        )?;
        if record["receipt"]["state"] == "uncertain"
            && self.dial().is_ok()
            && let Ok(Some(admission)) = descriptor.binding.pending(operation)
        {
            record["receipt"] =
                descriptor.receipt(session, operation, "submitted", Some(admission));
            runtime::write_json(&path, &record)?;
        }
        Ok(Some(record["receipt"].clone()))
    }
}

fn validate_origin(origin: &Value) -> io::Result<()> {
    let get = |key| field(origin, key);
    let kind = get("kind");
    let fields = [
        "kind",
        "installation_id",
        "request_id",
        "actor_id",
        "actor_name",
        "guild_id",
        "channel_id",
        "message_id",
        "endorser_id",
        "requester_id",
        "discussion_id",
        "revision",
    ];
    let discussion = !get("discussion_id").is_empty() || !get("revision").is_empty();
    let personal = kind == "owner_direct" && get("requester_id") == get("actor_id");
    if !matches!(kind, "external" | "owner_direct" | "owner_endorsed")
        || ["installation_id", "request_id", "actor_id"]
            .iter()
            .any(|key| get(key).is_empty())
        || (kind == "owner_endorsed") == get("endorser_id").is_empty()
        || discussion
            && ([
                "discussion_id",
                "revision",
                "requester_id",
                "message_id",
                "channel_id",
            ]
            .iter()
            .any(|key| get(key).is_empty())
                || (!personal && get("guild_id").is_empty())
                || kind == "owner_endorsed")
        || origin.as_object().is_none_or(|object| {
            object.iter().any(|(key, val)| {
                !fields.contains(&key.as_str())
                    || val
                        .as_str()
                        .is_none_or(|str| str.len() > 512 || str.chars().any(char::is_control))
            })
        })
    {
        return Err(io::Error::other("invalid Hermes semantic origin"));
    }
    Ok(())
}

fn semantic_path(dir: &Path, operation: &str) -> PathBuf {
    dir.join(format!("{:x}.json", Sha256::digest(operation)))
}
