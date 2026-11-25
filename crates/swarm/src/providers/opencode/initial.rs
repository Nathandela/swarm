use crate::{ipc::field, providers::codex_receipt, supervisor::runtime};
use serde_json::{Value, json};
use std::{io, path::Path};

const IDENTITY: &[&str] = &[
    "workerID",
    "sessionInstance",
    "backendGeneration",
    "rootID",
    "directory",
    "nativeCreatedAtMS",
];

pub(crate) fn intent(operation: &str, initial: &Value) -> Value {
    let request_hash = codex_receipt::hash(field(initial, "text"));
    // Keep the model/agent keys in the native composer's intentHash order.
    let intent_hash = codex_receipt::hash(
        &json!([request_hash,{"model":initial["expectedModel"],"agent":initial["expectedAgent"]}])
            .to_string(),
    );
    json!({"operationID":operation,"requestHash":request_hash,"intentHash":intent_hash})
}

pub(crate) fn record(token: &str, native: &Value, intent: &Value, witness: &Value) -> Value {
    let state = match field(witness, "status") {
        "admitted" => "native_accepted",
        "rejected" => "rejected",
        _ => "outcome_unknown",
    };
    let identity = IDENTITY
        .iter()
        .map(|key| ((*key).to_owned(), native[*key].clone()))
        .collect::<serde_json::Map<_, _>>();
    json!({"token":token,"identity":identity,"intent":intent,"state":state,"bridge_receipt":witness})
}

pub(crate) fn valid(token: &str, native: &Value, intent: &Value, receipt: &Value) -> bool {
    let operation = field(intent, "operationID");
    let witness = &receipt["bridge_receipt"];
    if !runtime::valid_id(field(native, "workerID"))
        || !super::valid_id(field(native, "rootID"), "ses_")
        || field(native, "sessionInstance").len() != 32
        || native["sessionInstance"] != native["backendGeneration"]
        || native["nativeCreatedAtMS"]
            .as_u64()
            .is_none_or(|at| at == 0)
        || !Path::new(field(native, "directory")).is_absolute()
        || receipt["token"] != token
        || receipt["intent"] != *intent
        || operation.len() != 48
        || !operation
            .bytes()
            .all(|ch| ch.is_ascii_digit() || (b'a'..=b'f').contains(&ch))
        || ["requestHash", "intentHash"]
            .into_iter()
            .any(|key| !super::hex_hash(field(intent, key)) || witness[key] != intent[key])
        || witness["version"] != 1
        || witness["operationID"] != operation
        || IDENTITY
            .iter()
            .any(|key| witness[*key] != native[*key] || receipt["identity"][*key] != native[*key])
    {
        return false;
    }
    match field(receipt, "state") {
        "native_accepted" => {
            let message = field(witness, "messageID");
            let clean = |value: &str, max| {
                !value.is_empty() && value.len() <= max && !value.chars().any(char::is_control)
            };
            witness["status"] == "admitted"
                && witness["selectionBasis"] == "native_composer"
                && super::valid_id(message, "msg_")
                && witness["selectionMessageID"] == message
                && clean(field(witness, "agent"), 128)
                && clean(field(witness, "variant"), 128)
                && clean(field(&witness["model"], "providerID"), 128)
                && clean(field(&witness["model"], "modelID"), 512)
                && witness["nativeEffect"].is_null()
                && witness["reason"] == ""
        }
        "rejected" => {
            witness["status"] == "rejected"
                && witness["nativeEffect"] == "none"
                && witness["messageID"].is_null()
                && [
                    "selectionMessageID",
                    "selectionBasis",
                    "agent",
                    "model",
                    "variant",
                ]
                .into_iter()
                .all(|key| witness[key].is_null())
                && !field(witness, "reason").is_empty()
        }
        _ => false,
    }
}

pub(crate) fn verify_resume(
    dir: &Path,
    token: &str,
    native: &Value,
    meta: &Value,
) -> io::Result<()> {
    let binding = runtime::read_json(&dir.join("opencode-binding.json"))?;
    let intent = meta
        .get("opencode_initial")
        .ok_or_else(|| io::Error::other("OpenCode initial input plan is absent"))?;
    if binding["version"] != 1
        || binding["workerID"] != meta["id"]
        || [
            "workerID",
            "sessionInstance",
            "backendGeneration",
            "rootID",
            "directory",
        ]
        .into_iter()
        .any(|key| binding[key] != native[key])
        || binding["initialPrompt"].as_bool() != Some(!intent.is_null())
        || !match field(&binding, "mode") {
            "create" => binding["nativeCreatedAtMS"] == 0,
            "resume" => binding["nativeCreatedAtMS"] == native["nativeCreatedAtMS"],
            _ => false,
        }
    {
        return Err(io::Error::other(
            "OpenCode initial input binding is unverified",
        ));
    }
    let receipt = runtime::read_json(&dir.join("opencode-initial.json"));
    match receipt {
        Err(error) if intent.is_null() && error.kind() == io::ErrorKind::NotFound => Ok(()),
        Ok(receipt) if !intent.is_null() && valid(token, native, intent, &receipt) => Ok(()),
        _ => Err(io::Error::other(
            "OpenCode prior initial input remains unverified",
        )),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn initial_resume_requires_correlated_admission_or_proven_no_effect() {
        let native = json!({
            "workerID":"0123456789abcdef01234567","rootID":"ses_0123456789abcdef",
            "sessionInstance":"a".repeat(32),"backendGeneration":"a".repeat(32),
            "directory":"/fixture","nativeCreatedAtMS":42,
        });
        let intent = intent(
            &"b".repeat(48),
            &json!({
                "text":"test input","expectedModel":"fixture/model","expectedAgent":"build",
            }),
        );
        let mut witness = native.clone();
        witness["version"] = json!(1);
        for field in ["operationID", "requestHash", "intentHash"] {
            witness[field] = intent[field].clone();
        }
        let check = |witness: &Value| {
            valid(
                "private-token",
                &native,
                &intent,
                &record("private-token", &native, &intent, witness),
            )
        };
        assert!(!valid(
            "private-token",
            &native,
            &intent,
            &json!({"token":"private-token","state":"native_accepted"})
        ));
        witness["status"] = json!("rejected");
        witness["reason"] = json!("initial_command_unsupported");
        assert!(!check(&witness));
        witness["nativeEffect"] = json!("none");
        assert!(check(&witness));
        witness["status"] = json!("admitted");
        witness["reason"] = json!("");
        witness["nativeEffect"] = Value::Null;
        witness["agent"] = json!("build");
        witness["variant"] = json!("default");
        witness["model"] = json!({"providerID":"fixture","modelID":"model"});
        witness["selectionBasis"] = json!("native_composer");
        witness["messageID"] = json!("msg_0123456789abcdef");
        assert!(!check(&witness));
        witness["selectionMessageID"] = witness["messageID"].clone();
        assert!(check(&witness));
        for key in [
            "operationID",
            "requestHash",
            "intentHash",
            "sessionInstance",
            "rootID",
        ] {
            let mut damaged = witness.clone();
            damaged[key] = json!("wrong");
            assert!(!check(&damaged), "{key}");
        }
    }
}
