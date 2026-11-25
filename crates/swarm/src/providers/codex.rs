use crate::ipc::field;
use serde_json::{Value, json};
use std::io;

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
