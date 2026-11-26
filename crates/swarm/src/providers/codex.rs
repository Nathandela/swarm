use crate::ipc::field;
use serde_json::{Value, json};
use std::io;
use std::os::unix::fs::MetadataExt;
use std::path::{Path, PathBuf};

pub(crate) fn version(output: &str) -> io::Result<&str> {
    let reported = output
        .split_whitespace()
        .find_map(|token| {
            let token = token.strip_prefix('v').unwrap_or(token);
            let mut parts = token.split('.');
            let major: u32 = parts.next()?.parse().ok()?;
            let minor: u32 = parts.next()?.parse().ok()?;
            let patch: u32 = parts.next().unwrap_or("0").parse().ok()?;
            parts
                .next()
                .is_none()
                .then_some((token, (major, minor, patch)))
        })
        .ok_or_else(|| io::Error::other("unrecognized Codex CLI version"))?;
    if !((0, 100, 0)..=(9999, 0, 0)).contains(&reported.1) {
        return Err(io::Error::other(format!(
            "Codex CLI {} is outside supported versions 0.100.0–9999.0.0",
            reported.0
        )));
    }
    Ok(reported.0)
}

const UNQUALIFIED: &[&str] = &[
    "SWARM_CODEX_UNSUPPORTED_ORIGIN",
    "CODEX_PROFILE",
    "CODEX_AUTH_JSON",
    "OPENAI_IDENTITY_TOKEN_FILE",
    "OPENAI_BASE_URL",
    "CODEX_API_ENDPOINT",
    "CODEX_ACCESS_TOKEN",
    "CODEX_API_KEY",
    "OPENAI_ORG_ID",
    "OPENAI_ORGANIZATION",
    "OPENAI_PROJECT_ID",
    "OPENAI_PROJECT",
    "CODEX_CLI_AUTH_CREDENTIALS_STORE",
    "CODEX_REFRESH_TOKEN_URL_OVERRIDE",
    "CODEX_REVOKE_TOKEN_URL_OVERRIDE",
];

pub(crate) fn unqualified_selector(key: &str) -> bool {
    UNQUALIFIED.contains(&key)
}

pub(crate) fn refuse_unqualified_origin(environment: &Value) -> io::Result<()> {
    let origin = if let Some(entries) = environment.as_array() {
        entries
            .iter()
            .filter_map(Value::as_str)
            .filter_map(|entry| entry.split_once('='))
            .find_map(|(key, value)| {
                (!value.is_empty() && unqualified_selector(key)).then_some(key)
            })
    } else if environment.is_null() {
        UNQUALIFIED
            .iter()
            .copied()
            .find(|key| std::env::var_os(key).is_some_and(|value| !value.is_empty()))
    } else {
        None
    };
    if let Some(origin) = origin {
        return Err(io::Error::other(format!(
            "Codex launch refuses unqualified native origin {origin}"
        )));
    }
    Ok(())
}

pub(crate) fn explicit_home(environment: &Value, home: &Path) -> io::Result<Option<PathBuf>> {
    let values = if environment.is_null() {
        std::env::var_os("CODEX_HOME")
            .map(|value| {
                value
                    .into_string()
                    .map_err(|_| io::Error::other("Codex profile path is not UTF-8"))
            })
            .transpose()?
            .into_iter()
            .collect::<Vec<_>>()
    } else {
        environment
            .as_array()
            .into_iter()
            .flatten()
            .filter_map(Value::as_str)
            .filter_map(|entry| entry.strip_prefix("CODEX_HOME="))
            .map(str::to_owned)
            .collect()
    };
    match values.as_slice() {
        [] => Ok(None),
        [value] if value.is_empty() => Ok(None),
        [value] => native_home(home, Path::new(value)).map(Some),
        _ => Err(io::Error::other("ambiguous CODEX_HOME")),
    }
}

pub(crate) fn native_home(home: &Path, explicit: &Path) -> io::Result<PathBuf> {
    let invalid =
        || io::Error::other("CODEX_HOME requires a physical private ordinary directory below HOME");
    let path = explicit.strip_prefix(home).map_err(|_| invalid())?;
    if !home.is_absolute()
        || !explicit.is_absolute()
        || path.as_os_str().is_empty()
        || home.canonicalize()? != home
        || explicit.canonicalize()? != explicit
    {
        return Err(invalid());
    }
    let names = path.components().collect::<Vec<_>>();
    if names.windows(2).any(|parts| {
        parts[0].as_os_str() == "logins"
            && matches!(
                parts[1].as_os_str().to_str(),
                Some("provider" | "enrollment" | "pending" | "saved")
            )
    }) {
        return Err(io::Error::other(
            "CODEX_HOME points into a retired Swarm-managed account pool",
        ));
    }
    // SAFETY: UID lookup has no process side effects.
    let uid = unsafe { libc::geteuid() };
    let root = home.symlink_metadata()?;
    if !root.is_dir() || root.uid() != uid || root.mode() & 0o022 != 0 {
        return Err(invalid());
    }
    let mut dir = home.to_owned();
    for name in names {
        dir.push(name.as_os_str());
        let stat = dir.symlink_metadata()?;
        if !stat.is_dir()
            || stat.uid() != uid
            || stat.mode() & 0o022 != 0
            || dir == explicit && stat.mode() & 0o077 != 0
        {
            return Err(invalid());
        }
    }
    for name in ["auth.json", "config.toml"] {
        match dir.join(name).symlink_metadata() {
            Ok(info)
                if info.is_file()
                    && info.uid() == uid
                    && info.nlink() == 1
                    && info.mode() & 0o022 == 0 => {}
            Err(err) if err.kind() == io::ErrorKind::NotFound => {}
            Err(err) => return Err(err),
            Ok(_) => {
                return Err(io::Error::other(
                    "alternate CODEX_HOME contains an unqualified configuration or authentication file",
                ));
            }
        }
    }
    Ok(dir)
}

pub(crate) fn argv(request: &Value) -> io::Result<Vec<String>> {
    let options = &request["options"];
    let model = field(options, "model");
    let sandbox = field(options, "sandbox");
    if model.starts_with('-') {
        return Err(io::Error::other("invalid Codex model"));
    }
    let mut args = Vec::new();
    if !model.is_empty() {
        args.extend(["--model".into(), model.to_owned()]);
    }
    if !sandbox.is_empty() {
        if !matches!(
            sandbox,
            "read-only" | "workspace-write" | "danger-full-access"
        ) {
            return Err(io::Error::other("invalid Codex sandbox mode"));
        }
        args.extend(["--sandbox".into(), sandbox.to_owned()]);
    }
    args.extend([
        "-c".into(),
        "sandbox_workspace_write.network_access=true".into(),
    ]);
    let prompt = field(request, "initial_prompt");
    if !prompt.is_empty() {
        if prompt.starts_with('-') {
            args.push("--".into());
        }
        args.push(prompt.to_owned());
    }
    Ok(args)
}

pub(crate) fn terminal_status(screen: &vt100::Screen) -> Option<Value> {
    let (_, cols) = screen.size();
    let rows: Vec<_> = screen.rows(0, cols.get()).collect();
    let last = rows.iter().rposition(|row| !row.trim().is_empty())?;
    let recent = &rows[last.saturating_sub(7)..=last];
    let (turn, interaction) = if recent
        .iter()
        .any(|row| row.trim() == "Press enter to confirm or esc to cancel")
    {
        ("idle", "permission")
    } else if recent.iter().any(|row| {
        row.contains("(esc to interrupt)")
            || row
                .trim_start()
                .starts_with(|ch| matches!(ch, '\u{2801}'..='\u{28ff}'))
    }) {
        ("active", "none")
    } else if !screen.hide_cursor()
        && rows
            .get(screen.cursor_position().0 as usize)
            .is_some_and(|row| row.trim_start().starts_with('›'))
    {
        ("idle", "none")
    } else {
        return None;
    };
    Some(json!({"Process":"running", "Turn":turn, "Interaction":interaction}))
}

pub(crate) fn managed(release: &str) -> bool {
    let mut parts = release
        .split('.')
        .filter_map(|part| part.parse::<u32>().ok());
    let major = parts.next().unwrap_or_default();
    let minor = parts.next().unwrap_or_default();
    major > 0 || minor >= 160
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn classic_admission_and_argv() {
        assert!(version("codex-cli 0.99.0").is_err());
        assert!(version("codex-cli 0.170.0-beta.1").is_err());
        assert_eq!(version("codex-cli 0.100.0").unwrap(), "0.100.0");
        assert!(argv(&json!({"options":{"sandbox":"unknown"}})).is_err());
        assert!(argv(&json!({"options":{"model":"--full-auto"}})).is_err());
        let origin =
            refuse_unqualified_origin(&json!(["OPENAI_BASE_URL=private-fixture-url"])).unwrap_err();
        assert!(!origin.to_string().contains("private-fixture-url"));
        assert_eq!(argv(&json!({"options":{"model":"gpt","sandbox":"read-only"},"initial_prompt":"--東京\nnext"})).unwrap(),
            ["--model","gpt","--sandbox","read-only","-c","sandbox_workspace_write.network_access=true","--","--東京\nnext"]);
    }

    #[test]
    fn status_requires_terminal_structure() {
        let mut view = vt100::Parser::new(12.try_into().unwrap(), 80.try_into().unwrap(), 0);
        view.process(b"prose about esc to interrupt");
        assert!(terminal_status(view.screen()).is_none());
        view.process(" \r\n› ".as_bytes());
        assert_eq!(terminal_status(view.screen()).unwrap()["Turn"], "idle");
        view.process(b"\r\nWorking (esc to interrupt)");
        assert_eq!(terminal_status(view.screen()).unwrap()["Turn"], "active");
        view.process(b"\r\nPress enter to confirm or esc to cancel");
        assert_eq!(
            terminal_status(view.screen()).unwrap()["Interaction"],
            "permission"
        );
    }
}
