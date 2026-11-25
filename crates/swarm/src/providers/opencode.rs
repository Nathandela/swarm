pub(crate) mod receipts;
use crate::{ipc::field, supervisor::runtime};
use serde_json::{Value, json};
use std::{
    collections::BTreeMap,
    env, fs, io,
    path::{Path, PathBuf},
};

const BRIDGE: &str = include_str!("../../../../internal/opencodebridge/tui.mjs");
const PROFILE: &[&str] = &[
    "HOME",
    "XDG_CONFIG_HOME",
    "XDG_DATA_HOME",
    "XDG_STATE_HOME",
    "XDG_CACHE_HOME",
    "OPENCODE_CONFIG",
    "OPENCODE_CONFIG_DIR",
];
const SELECTORS: &[&str] = &[
    "OPENCODE_PURE",
    "OPENCODE_DISABLE_AUTOUPDATE",
    "OPENCODE_DISABLE_PROJECT_CONFIG",
    "OPENCODE_DISABLE_DEFAULT_PLUGINS",
    "OPENCODE_DISABLE_MODELS_FETCH",
    "OPENCODE_DISABLE_TERMINAL_TITLE",
];

pub(crate) fn version(output: &str) -> io::Result<&str> {
    let version = output.trim();
    let components = version
        .split('.')
        .map(|part| {
            if part.is_empty()
                || !part.bytes().all(|ch| ch.is_ascii_digit())
                || part.len() > 1 && part.starts_with('0')
            {
                return Err(io::Error::other("unrecognized stable OpenCode CLI version"));
            }
            Ok((part.len(), part))
        })
        .collect::<io::Result<Vec<_>>>()?;
    if components.len() == 3 && components.as_slice() >= [(1, "1"), (2, "19"), (1, "3")].as_slice()
    {
        Ok(version)
    } else {
        Err(io::Error::other(
            "managed OpenCode requires stable 1.19.3 or newer",
        ))
    }
}

pub(crate) fn environment(supplied: &Value) -> io::Result<BTreeMap<String, String>> {
    let entries = if let Some(entries) = supplied.as_array() {
        entries
            .iter()
            .filter_map(Value::as_str)
            .filter_map(|v| v.split_once('='))
            .map(|(k, v)| (k.to_owned(), v.to_owned()))
            .collect::<Vec<_>>()
    } else {
        env::vars_os()
            .filter_map(|(k, v)| Some((k.into_string().ok()?, v.into_string().ok()?)))
            .collect()
    };
    let mut output = BTreeMap::new();
    for (key, value) in entries {
        let profile = PROFILE.contains(&key.as_str());
        let selector = SELECTORS.contains(&key.as_str());
        if !profile && !selector {
            if key.starts_with("OPENCODE_") || key == "SWARM_OPENCODE_UNSUPPORTED_ORIGIN" {
                return Err(io::Error::other(
                    "unsupported OpenCode configuration origin",
                ));
            }
            continue;
        }
        if value.len() > 4096 || value.chars().any(char::is_control) || output.contains_key(&key) {
            return Err(io::Error::other("ambiguous OpenCode configuration origin"));
        }
        let value = if profile {
            let path = Path::new(&value);
            if !path.is_absolute()
                || path.components().collect::<PathBuf>().as_os_str() != path.as_os_str()
                || path
                    .components()
                    .any(|p| matches!(p, std::path::Component::ParentDir))
            {
                return Err(io::Error::other(
                    "OpenCode configuration paths must be absolute and normalized",
                ));
            }
            let physical = if key == "OPENCODE_CONFIG" {
                canonical_origin(
                    path.parent()
                        .ok_or_else(|| io::Error::other("missing OpenCode config parent"))?,
                )?
                .join(
                    path.file_name()
                        .ok_or_else(|| io::Error::other("missing OpenCode config file name"))?,
                )
            } else {
                canonical_origin(path)?
            };
            physical
                .into_os_string()
                .into_string()
                .map_err(|_| io::Error::other("OpenCode configuration path is not UTF-8"))?
        } else {
            if !matches!(value.as_str(), "0" | "1" | "true" | "false")
                || key == "OPENCODE_PURE" && matches!(value.as_str(), "1" | "true")
            {
                return Err(io::Error::other(
                    "OpenCode configuration disables or obscures the native bridge",
                ));
            }
            value
        };
        output.insert(key, value);
    }
    if !output.contains_key("HOME") {
        return Err(io::Error::other(
            "managed OpenCode launch requires an exact HOME",
        ));
    }
    Ok(output)
}

fn canonical_origin(path: &Path) -> io::Result<PathBuf> {
    match fs::canonicalize(path) {
        Ok(p) if p.is_dir() => Ok(p),
        Ok(_) => Err(io::Error::other(
            "OpenCode configuration origin is not a directory",
        )),
        Err(err) if err.kind() == io::ErrorKind::NotFound => {
            if fs::symlink_metadata(path).is_ok() {
                return Err(err);
            }
            match (path.parent(), path.file_name()) {
                (Some(parent), Some(name)) => Ok(canonical_origin(parent)?.join(name)),
                _ => Err(err),
            }
        }
        Err(err) => Err(err),
    }
}

pub(crate) fn profile(environment: &[(String, String)]) -> Value {
    let profile: BTreeMap<_, _> = environment
        .iter()
        .filter(|(key, _)| PROFILE.contains(&key.as_str()))
        .map(|(key, value)| (key, value))
        .collect();
    json!(profile)
}

pub(crate) fn launch(request: &Value) -> io::Result<(Vec<String>, Value)> {
    let options = &request["options"];
    let model = field(options, "model");
    let agent = field(options, "agent");
    let prompt = field(request, "initial_prompt");
    if model.len() > 512
        || model.chars().any(char::is_control)
        || model.starts_with('-')
        || !model.is_empty()
            && !model
                .split_once('/')
                .is_some_and(|(provider, model)| !provider.is_empty() && !model.is_empty())
        || agent.len() > 128
        || agent.chars().any(char::is_control)
        || agent.starts_with('-')
        || prompt.len() > 16384
        || prompt.contains('\0')
    {
        return Err(io::Error::other(
            "invalid OpenCode model, agent, or launch input",
        ));
    }
    let mut argv = Vec::new();
    for (flag, value) in [("--model", model), ("--agent", agent)] {
        if !value.is_empty() {
            argv.extend([flag.to_owned(), value.to_owned()]);
        }
    }
    Ok((
        argv,
        json!({"text":prompt,"expectedModel":model,"expectedAgent":agent}),
    ))
}

pub(crate) fn artifact(
    dir: &Path,
    cwd: &Path,
    session: &str,
    initial: bool,
    resume: &Value,
) -> io::Result<Value> {
    let socket = dir.join("oc.sock");
    if socket.as_os_str().as_encoded_bytes().len() > 100 {
        return Err(io::Error::other(
            "OpenCode private socket path exceeds native limit",
        ));
    }
    let generation = &nonce()?[..32];
    let root = if resume.is_null() {
        format!("ses_{generation}")
    } else {
        field(resume, "rootID").to_owned()
    };
    let binding = json!({
        "version":1,"workerID":session,"sessionInstance":generation,"backendGeneration":generation,
        "rootID":root,"directory":cwd,"socketPath":socket,"token":nonce()?,
        "mode":if resume.is_null() { "create" } else { "resume" },
        "nativeCreatedAtMS":resume["nativeCreatedAtMS"].as_u64().unwrap_or_default(),
        "initialPrompt":initial,
    });
    let file = dir.join("opencode-binding.json");
    let plugin = dir.join("opencode-tui.mjs");
    runtime::write_json(&file, &binding)?;
    runtime::write_atomic(&plugin, |out| {
        std::io::Write::write_all(out, BRIDGE.as_bytes())
    })?;
    let url = format!("file://{}", percent_path(&plugin)?);
    runtime::write_json(
        &dir.join("opencode-tui.json"),
        &json!({"plugin":[[url,{"bindingFile":file}]]}),
    )?;
    Ok(binding)
}

pub(crate) fn nonce() -> io::Result<String> {
    Ok(format!(
        "{}{}",
        runtime::random_id()?,
        runtime::random_id()?
    ))
}

fn percent_path(path: &Path) -> io::Result<String> {
    let path = path
        .to_str()
        .ok_or_else(|| io::Error::other("OpenCode state path is not UTF-8"))?;
    let mut encoded = String::new();
    for byte in path.bytes() {
        if byte.is_ascii_alphanumeric() || b"/-._~".contains(&byte) {
            encoded.push(char::from(byte));
        } else {
            use std::fmt::Write;
            write!(encoded, "%{byte:02X}").expect("write to string");
        }
    }
    Ok(encoded)
}

pub(crate) fn valid_id(value: &str, prefix: &str) -> bool {
    value.strip_prefix(prefix).is_some_and(|suffix| {
        (10..=124).contains(&suffix.len())
            && suffix.bytes().all(|byte| byte.is_ascii_alphanumeric())
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn admits_only_canonical_stable_versions_from_the_native_floor() {
        for input in ["1.19.3\n", "1.20.0", "2.0.0", "12.0.0"] {
            assert!(version(input).is_ok(), "{input}");
        }
        for input in [
            "v1.19.3",
            "1.19.2",
            "1.019.3",
            "1.19.3-beta",
            "1.19.3+build",
            "0.200.0",
        ] {
            assert!(version(input).is_err(), "{input}");
        }
    }

    #[test]
    fn rejects_origins_that_override_native_custody() {
        for selector in [
            "OPENCODE_PURE=true",
            "OPENCODE_TUI_CONFIG=/tmp/config.json",
            "OPENCODE_UNKNOWN=value",
            "XDG_CONFIG_HOME=/tmp/../other",
        ] {
            assert!(environment(&json!(["HOME=/", selector])).is_err());
        }
        assert_eq!(
            environment(&json!(["HOME=/", "OPENCODE_DISABLE_MODELS_FETCH=1"]))
                .unwrap()
                .len(),
            2
        );
    }
}
