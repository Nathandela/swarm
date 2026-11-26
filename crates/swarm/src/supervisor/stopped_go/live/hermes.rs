use super::{io, read_at};
use crate::ipc::field;
use crate::providers::hermes::{self, native};
use serde_json::{Value, json};
use std::fs::File;

const VERIFIED_VERSION: &str = "0.22.5";

pub(in crate::supervisor::stopped_go) fn managed(meta: &Value) -> bool {
    meta["agent_type"] == "hermes" && meta["schema_version"] == 5 && meta["hermes_native"] == true
}

pub(in crate::supervisor::stopped_go) fn qualified_launch(meta: &Value, launch: &Value) -> bool {
    let plan = &launch["hermes_input"];
    if !super::super::executable_binding(meta, launch, "hermes")
        || launch["cli_identity"]["version"] != VERIFIED_VERSION
        || plan["provider_version"] != VERIFIED_VERSION
        || plan["program"] != launch["cli_identity"]["path"]
        || plan["cwd"] != meta["cwd"]
        || plan["options"] != meta["launch_options"]
        || !launch["native_provider"].is_null()
        || !hermes::native_id(field(meta, "conversation_id"))
        || ["profile", "resume_id", "worker_id"]
            .iter()
            .any(|key| !plan[*key].is_null() && plan[*key] != "")
        || !field(&plan["options"], "profile").is_empty()
    {
        return false;
    }
    let request = json!({"options":plan["options"], "initial_prompt":plan["initial_prompt"]});
    let Ok(mut expected) = hermes::argv(&request) else {
        return false;
    };
    expected.insert(0, field(plan, "program").to_owned());
    launch["argv"] == json!(expected)
}

pub(super) struct Viewer {
    descriptor: Value,
    binding: native::Binding,
}

impl Viewer {
    pub(super) fn open(directory: &File, meta: &Value, launch: &Value) -> io::Result<Self> {
        super::super::absent_sidefiles(directory, &[c"hermes-stopped.json"])?;
        let descriptor = read_at(directory, c"hermes-input.json")?;
        let binding = &descriptor["binding"];
        let origins = &launch["hermes_input"]["origins"];
        let Some(entries) = origins.as_array() else {
            return Err(io::Error::other(
                "Go Hermes viewer has no persisted local origins",
            ));
        };
        let origin = native::origins(origins, true)?;
        if entries.len() != origin.len() {
            return Err(io::Error::other("Go Hermes viewer has unqualified origins"));
        }
        let mut homes = origin.iter().filter(|(key, _)| key == "HERMES_HOME");
        let home = homes.next().map(|(_, value)| value.as_str());
        let generation = field(&descriptor, "generation");
        if descriptor["worker_id"] != meta["id"]
            || descriptor["provider_version"] != VERIFIED_VERSION
            || descriptor["shim_pid"] != meta["shim_pid"]
            || descriptor["shim_start_time"] != meta["shim_start_time"]
            || generation.len() != 32
            || !generation.bytes().all(|c| c.is_ascii_hexdigit())
            || binding["session_id"] != meta["conversation_id"]
            || binding["directory"] != meta["cwd"]
            || home != binding["home"].as_str()
            || homes.next().is_some()
            || binding["home"] != binding["control_home"]
            || binding["owner_pid"] == meta["shim_pid"]
        {
            return Err(io::Error::other(
                "Go Hermes descriptor disagrees with its original viewer",
            ));
        }
        let mut binding = binding.clone();
        binding["owner_birth"] = binding["owner_start_time"].clone();
        let binding = native::Binding::load(binding)?;
        Ok(Self {
            descriptor,
            binding,
        })
    }

    pub(super) fn validate(&self, directory: &File) -> io::Result<()> {
        super::super::absent_sidefiles(directory, &[c"hermes-stopped.json"])?;
        if read_at(directory, c"hermes-input.json")? != self.descriptor {
            return Err(io::Error::other(
                "Go Hermes descriptor changed during viewer custody",
            ));
        }
        self.binding.validate()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn shared_go_viewer_rejects_another_thread_before_gateway_dial() {
        use crate::supervisor::runtime;

        let temp = std::env::temp_dir().join(runtime::random_id().unwrap());
        runtime::private_dir(&temp).unwrap();
        let directory = File::open(&temp).unwrap();
        let thread = format!("local-{}", "a".repeat(64));
        let meta = json!({
            "schema_version":5, "agent_type":"hermes", "hermes_native":true,
            "id":"original", "shim_pid":31, "shim_start_time":41,
            "conversation_id":thread, "cwd":"/fixture/cwd",
        });
        let launch = json!({"hermes_input":{"origins":["HERMES_HOME=/fixture/home"]}});
        let mut descriptor = json!({
            "worker_id":"original", "generation":"1234567890abcdef1234567890abcdef",
            "provider_version":VERIFIED_VERSION, "shim_pid":31, "shim_start_time":41,
            "binding":{"session_id":thread,"directory":"/fixture/cwd","home":"/fixture/home",
                "control_home":"/fixture/home","owner_pid":51,"owner_start_time":61,
                "instance_id":"owner","authority_epoch":1,"code_sha":"pinned-code"}
        });
        let path = temp.join("hermes-input.json");
        runtime::write_json(&path, &descriptor).unwrap();
        assert!(Viewer::open(&directory, &meta, &launch).is_ok());
        descriptor["binding"]["session_id"] = json!(format!("local-{}", "b".repeat(64)));
        runtime::write_json(&path, &descriptor).unwrap();
        assert_eq!(
            Viewer::open(&directory, &meta, &launch)
                .err()
                .unwrap()
                .to_string(),
            "Go Hermes descriptor disagrees with its original viewer"
        );
        std::fs::remove_dir_all(temp).unwrap();
    }
}
