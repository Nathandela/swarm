use super::{child_key, reliable};
/// Read a small owner-only record without blocking on pipes or following links.
pub(crate) fn private_text(path: &std::path::Path) -> std::io::Result<String> {
    use std::{
        fs,
        io::{self, Read},
        os::unix::fs::{MetadataExt, OpenOptionsExt},
    };
    let file = fs::OpenOptions::new()
        .read(true)
        .custom_flags(libc::O_NONBLOCK | libc::O_NOFOLLOW)
        .open(path)?;
    let meta = file.metadata()?;
    // SAFETY: geteuid takes no arguments.
    if !meta.is_file()
        || meta.mode() & 0o077 != 0
        || meta.nlink() != 1
        || meta.uid() != unsafe { libc::geteuid() }
    {
        return Err(io::Error::other("expected an owner-private regular file"));
    }
    let mut value = String::new();
    file.take((1 << 20) + 1).read_to_string(&mut value)?;
    if value.len() > 1 << 20 {
        return Err(io::Error::other("state record exceeds size limit"));
    }
    Ok(value)
}
pub(crate) fn load_saved(root: &std::path::Path) -> std::io::Result<Vec<String>> {
    let text = private_text(&root.join("daemon.env"))?;
    Ok(text
        .lines()
        .filter(|s| s.split_once('=').is_some_and(|(k, _)| !k.is_empty()))
        .map(str::to_owned)
        .collect())
}

fn config_key(k: &str) -> bool {
    matches!(
        k,
        "CODEX_HOME"
            | "CLAUDE_CONFIG_DIR"
            | "XDG_CONFIG_HOME"
            | "CLAUDE_CODE_NO_MODEL_FALLBACK"
            | "CODEX_PROFILE"
            | "ANTHROPIC_PROFILE"
            | "ANTHROPIC_CONFIG_DIR"
            | "CLAUDE_CODE_PROFILE"
            | "CLAUDE_CODE_DEFAULT_PROFILE"
            | "CLAUDE_CODE_FEDERATION_PROFILE"
            | "CLAUDE_CODE_USE_BEDROCK"
            | "CLAUDE_CODE_USE_VERTEX"
            | "CLAUDE_CODE_USE_FOUNDRY"
            | "CLAUDE_CODE_USE_GATEWAY"
            | "CLAUDE_CODE_GATEWAY_URL"
            | "CLAUDE_CODE_HOST_GATEWAY_LINEAGE"
            | "CLAUDE_CODE_USE_ANTHROPIC_AWS"
            | "CLAUDE_CODE_USE_ANTHROPIC_GOOGLE_CLOUD"
            | "CLAUDE_CODE_USE_MANTLE"
            | "CLAUDE_CODE_REMOTE_SETTINGS_PATH"
            | "CLAUDE_CODE_MANAGED_SETTINGS_PATH"
    )
}

fn unsupported_origin(provider: &str, key: &str, value: &str) -> bool {
    if provider == "claude" {
        if key == "SWARM_MANAGER_CLAUDE_UNSUPPORTED_ORIGIN" {
            return true;
        }
        if key.starts_with("CLAUDE_CODE_USE_") {
            return !matches!(value, "" | "0" | "false");
        }
        return !value.is_empty()
            && (key.starts_with("CLAUDE_CODE_HOST_")
                || key.starts_with("CLAUDE_CODE_GATEWAY_")
                || matches!(
                    key,
                    "ANTHROPIC_API_KEY"
                        | "ANTHROPIC_AUTH_TOKEN"
                        | "ANTHROPIC_BASE_URL"
                        | "ANTHROPIC_CUSTOM_HEADERS"
                        | "ANTHROPIC_PROFILE"
                        | "ANTHROPIC_CONFIG_DIR"
                        | "AWS_BEARER_TOKEN_BEDROCK"
                        | "CLAUDE_CODE_OAUTH_TOKEN"
                        | "CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR"
                        | "CLAUDE_CODE_ACCOUNT_UUID"
                        | "CLAUDE_CODE_API_KEY"
                        | "CLAUDE_CODE_API_KEY_HELPER_TTL_MS"
                        | "CLAUDE_CODE_PROFILE"
                        | "CLAUDE_CODE_DEFAULT_PROFILE"
                        | "CLAUDE_CODE_FEDERATION_PROFILE"
                        | "CLAUDE_SECURESTORAGE_CONFIG_DIR"
                ));
    }
    if key == "SWARM_MANAGER_CODEX_UNSUPPORTED_ORIGIN" {
        return true;
    }
    if key == "CODEX_DISABLE_KEYRING" {
        return !matches!(value, "" | "0" | "false");
    }
    !value.is_empty()
        && (key.starts_with("CODEX_AUTH_")
            || key.starts_with("CODEX_REMOTE_")
            || matches!(
                key,
                "CODEX_HOME"
                    | "OPENAI_API_KEY"
                    | "OPENAI_BASE_URL"
                    | "OPENAI_ORG_ID"
                    | "OPENAI_ORGANIZATION"
                    | "OPENAI_PROJECT_ID"
                    | "OPENAI_PROJECT"
                    | "CODEX_API_KEY"
                    | "CODEX_ACCESS_TOKEN"
                    | "CODEX_API_ENDPOINT"
                    | "CODEX_PROFILE"
                    | "CODEX_REFRESH_TOKEN_URL_OVERRIDE"
                    | "CODEX_REVOKE_TOKEN_URL_OVERRIDE"
                    | "CODEX_CLI_AUTH_CREDENTIALS_STORE"
            ))
}

pub(crate) fn save(root: &std::path::Path) -> std::io::Result<()> {
    use std::{
        collections::BTreeSet,
        io::{self, Write},
    };
    let original: Vec<_> = std::env::vars_os()
        .map(|(k, v)| {
            (
                k.to_string_lossy().into_owned(),
                v.to_string_lossy().into_owned(),
            )
        })
        .collect();
    let entries: Vec<_> = original.iter().map(|(k, v)| format!("{k}={v}")).collect();
    let mut saved: BTreeSet<_> = reliable("opencode", entries.clone())
        .into_iter()
        .chain(reliable("hermes", entries))
        .collect();
    for (k, v) in &original {
        if config_key(k) {
            saved.insert(format!("{k}={v}"));
        }
    }
    for provider in ["claude", "codex"] {
        if original
            .iter()
            .any(|(k, v)| unsupported_origin(provider, k, v))
        {
            saved.insert(format!(
                "SWARM_MANAGER_{}_UNSUPPORTED_ORIGIN=1",
                provider.to_uppercase()
            ));
        }
    }
    if saved.iter().any(|s| s.contains(['\r', '\n'])) {
        return Err(io::Error::other(
            "saved environment entry contains a line break",
        ));
    }
    crate::supervisor::runtime::write_atomic(&root.join("daemon.env"), |file| {
        for kv in saved {
            writeln!(file, "{kv}")?;
        }
        Ok(())
    })
}

pub(crate) fn restore(root: &std::path::Path) -> std::io::Result<Vec<String>> {
    use std::{collections::BTreeMap, io};
    let text = private_text(&root.join("daemon.env")).map_err(|error| {
        if error.kind() == io::ErrorKind::NotFound {
            io::Error::new(
                error.kind(),
                "no saved daemon environment; open Swarm interactively on this host first",
            )
        } else {
            error
        }
    })?;
    let mut unique = BTreeMap::new();
    for line in text.lines() {
        let Some((key, value)) = line.split_once('=') else {
            return Err(io::Error::other("malformed saved agent environment"));
        };
        if value.contains(['\r', '\0'])
            || !(child_key(key)
                || config_key(key)
                || matches!(
                    key,
                    "XDG_DATA_HOME"
                        | "XDG_CACHE_HOME"
                        | "XDG_STATE_HOME"
                        | "HERMES_HOME"
                        | "HERMES_GATEWAY_LOCK_DIR"
                        | "OPENCODE_CONFIG"
                        | "OPENCODE_CONFIG_DIR"
                        | "OPENCODE_TUI_CONFIG"
                        | "OPENCODE_PURE"
                        | "OPENCODE_DISABLE_AUTOUPDATE"
                        | "OPENCODE_DISABLE_PROJECT_CONFIG"
                        | "OPENCODE_DISABLE_DEFAULT_PLUGINS"
                        | "OPENCODE_DISABLE_MODELS_FETCH"
                        | "OPENCODE_DISABLE_TERMINAL_TITLE"
                        | "SWARM_MANAGER_CODEX_UNSUPPORTED_ORIGIN"
                        | "SWARM_MANAGER_CLAUDE_UNSUPPORTED_ORIGIN"
                        | "SWARM_HERMES_UNSUPPORTED_ORIGIN"
                        | "SWARM_OPENCODE_UNSUPPORTED_ORIGIN"
                ))
        {
            return Err(io::Error::other(
                "unsupported saved agent environment entry",
            ));
        }
        if unique
            .insert(key, value)
            .is_some_and(|prior| prior != value)
        {
            return Err(io::Error::other(
                "conflicting saved agent environment entries",
            ));
        }
    }
    if unique.is_empty() {
        return Err(io::Error::new(
            io::ErrorKind::UnexpectedEof,
            "the saved daemon environment is empty; open Swarm interactively on this host first",
        ));
    }
    Ok(unique
        .into_iter()
        .map(|(k, v)| format!("{k}={v}"))
        .collect())
}
