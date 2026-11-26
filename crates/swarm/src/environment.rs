//! Idempotent launch keys include the sorted, admitted environment.
mod saved;
pub(crate) use saved::{load_saved, private_text, restore, save};
pub(crate) fn child_key(key: &str) -> bool {
    matches!(
        key,
        "PATH"
            | "HOME"
            | "SHELL"
            | "TERM"
            | "LANG"
            | "LANGUAGE"
            | "VIRTUAL_ENV"
            | "CONDA_PREFIX"
            | "CONDA_DEFAULT_ENV"
            | "ANTHROPIC_API_KEY"
            | "OPENAI_API_KEY"
    ) || key.starts_with("LC_")
}

pub(crate) fn reliable(agent: &str, environment: Vec<String>) -> Vec<String> {
    let mut admitted = Vec::new();
    let mut unsupported_opencode = false;
    let mut unsupported_codex = false;
    for entry in environment {
        let (key, value) = entry.split_once('=').unwrap_or((&entry, ""));
        if child_key(key) {
            admitted.push(entry);
        } else if agent == "codex" {
            if key == "CODEX_HOME" {
                admitted.push(entry);
            } else if !value.is_empty() && crate::providers::codex::unqualified_selector(key) {
                unsupported_codex = true;
            }
        } else if agent == "hermes" {
            match key {
                "HERMES_HOME" | "HERMES_GATEWAY_LOCK_DIR" | "XDG_STATE_HOME" => {
                    admitted.push(entry)
                }
                "SWARM_HERMES_UNSUPPORTED_ORIGIN" => {
                    admitted.push("SWARM_HERMES_UNSUPPORTED_ORIGIN=1".into())
                }
                "HERMES_TUI_GATEWAY_URL" | "HERMES_TUI_SIDECAR_URL" if !value.is_empty() => {
                    admitted.push("SWARM_HERMES_UNSUPPORTED_ORIGIN=1".into())
                }
                _ => {}
            }
        } else if agent == "opencode" {
            match key {
                "XDG_CONFIG_HOME"
                | "XDG_DATA_HOME"
                | "XDG_STATE_HOME"
                | "XDG_CACHE_HOME"
                | "OPENCODE_CONFIG"
                | "OPENCODE_CONFIG_DIR"
                | "OPENCODE_TUI_CONFIG"
                | "OPENCODE_PURE"
                | "OPENCODE_DISABLE_AUTOUPDATE"
                | "OPENCODE_DISABLE_PROJECT_CONFIG"
                | "OPENCODE_DISABLE_DEFAULT_PLUGINS"
                | "OPENCODE_DISABLE_MODELS_FETCH"
                | "OPENCODE_DISABLE_TERMINAL_TITLE" => admitted.push(entry),
                _ if key.starts_with("OPENCODE_")
                    || entry == "SWARM_OPENCODE_UNSUPPORTED_ORIGIN=1" =>
                {
                    unsupported_opencode = true
                }
                _ => {}
            }
        }
    }
    if unsupported_codex {
        admitted.push("SWARM_CODEX_UNSUPPORTED_ORIGIN=1".into());
    }
    if unsupported_opencode {
        admitted.push("SWARM_OPENCODE_UNSUPPORTED_ORIGIN=1".into());
    }
    admitted.sort();
    admitted
}

#[cfg(test)]
mod tests {
    #[test]
    fn receipt_environment_omits_unrelated_values_and_retains_refusal() {
        let entries = [
            "SECRET=private",
            "PATH=/bin",
            "OPENCODE_FOO=opaque",
            "HOME=/private",
            "LC_ALL=C",
            "OPENCODE_BAR=other",
        ];
        assert_eq!(
            super::reliable("opencode", entries.map(String::from).into()),
            [
                "HOME=/private",
                "LC_ALL=C",
                "PATH=/bin",
                "SWARM_OPENCODE_UNSUPPORTED_ORIGIN=1"
            ]
        );
    }
}
