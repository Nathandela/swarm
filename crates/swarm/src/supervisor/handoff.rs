//! Resolve handoff lineage at its authoritative source.
use super::{State, runtime};
use crate::ipc::field;
use serde_json::{Value, json};
use std::{fs, io, path::Path};

pub(super) fn bind(
    state: &State,
    request: &Value,
) -> io::Result<(Value, Option<(String, String)>)> {
    let mut bound = request.clone();
    let launch = &mut bound["launch"];
    let source = field(launch, "spawned_from").to_owned();
    let intent = field(launch, "spawn_intent");
    let mode = field(launch, "supervision");
    for key in ["spawned_from", "spawn_intent", "supervision"] {
        if !launch[key].is_null() && !launch[key].is_string() {
            return Err(io::Error::other("invalid launch lineage field type"));
        }
    }
    if !matches!(intent, "" | "handoff" | "delegate")
        || !matches!(mode, "" | "passive" | "manual" | "none")
        || !mode.is_empty() && intent != "handoff"
        || source.is_empty() && !intent.is_empty()
    {
        return Err(io::Error::other(
            "invalid handoff lineage or supervision mode",
        ));
    }
    if source.is_empty() {
        return Ok((bound, None));
    }
    if !runtime::valid_id(&source) {
        return Err(io::Error::other("invalid local source session id"));
    }
    let parent = state
        .sessions
        .read()
        .unwrap()
        .get(&source)
        .cloned()
        .ok_or_else(|| {
            io::Error::other("handoff: source session is no longer in this supervisor's roster")
        })?;
    if !parent.owner_interactive {
        return Err(io::Error::other(
            "handoff: restricted source requires its bound manager or worker route",
        ));
    }
    if !mode.is_empty() {
        let cwd = parent.verify_effective()?;
        if !parent.is_live() || !cwd.is_dir() {
            return Err(io::Error::other(
                "handoff: source session or its checkout is no longer available",
            ));
        }
        launch["cwd"] = json!(cwd);
        if crate::workspace::is_repo(cwd) {
            launch["worktree"] = json!(true);
        }
    }
    if launch["worktree"] == true {
        let target = Path::new(field(launch, "cwd")).canonicalize()?;
        for dir in [std::env::temp_dir(), "/tmp".into(), "/var/tmp".into()] {
            if let Ok(tmp) = fs::canonicalize(dir)
                && target.starts_with(tmp)
            {
                return Err(io::Error::other(
                    "handoff: the worktree destination is temporary; hand off from a durable checkout instead",
                ));
            }
        }
    }
    Ok((bound, Some((source, parent.incarnation()))))
}

pub(super) fn hands_off(
    state: &State,
    request: &Value,
) -> io::Result<(Value, (String, String), Value)> {
    let mut bound = request.clone();
    let launch = &mut bound["launch"];
    let options = launch["options"]
        .as_object_mut()
        .ok_or_else(|| io::Error::other("invalid hands-off options"))?;
    let source = options
        .shift_remove("handoff_from")
        .and_then(|id| id.as_str().map(str::to_owned))
        .filter(|id| !id.is_empty())
        .ok_or_else(|| io::Error::other("handoff_from requires an explicit source session"))?;
    if ["resume_from", "resume_conversation_id", "resume_relocate"]
        .iter()
        .any(|key| options.contains_key(*key))
    {
        return Err(io::Error::other(
            "hands-off handoff cannot be combined with resume",
        ));
    }
    let scoped = if source.contains('/') {
        source
    } else {
        format!("{}/{}", state.endpoint, source)
    };
    let session = state.session(&json!({"session_id":scoped}))?;
    let (cwd, transcript, id, proof) = session.hands_off_source()?;
    if proof["kind"] == "opencode" {
        crate::providers::opencode_handoff::inherit_selection(launch, &proof)?;
    }
    let rendered = render([
        &id,
        transcript
            .to_str()
            .ok_or_else(|| io::Error::other("non-UTF-8 transcript path"))?,
        cwd.to_str()
            .ok_or_else(|| io::Error::other("non-UTF-8 working directory"))?,
        &session.agent,
        &session.id,
    ])?;
    launch["cwd"] = json!(cwd);
    launch["initial_prompt"] = json!(rendered);
    launch["spawned_from"] = json!(session.id);
    launch["spawn_intent"] = json!("handoff");
    launch["supervision"] = Value::Null;
    launch["worktree"] = json!(crate::workspace::is_repo(&cwd));
    let (bound, _) = bind(state, &bound)?;
    Ok((bound, (session.id.clone(), session.incarnation()), proof))
}

pub(crate) fn verify_handoff_artifact(dir: &Path, lineage: &Value) -> io::Result<()> {
    use crate::providers;
    use std::os::unix::fs::MetadataExt;
    let proof = &lineage["hands_off"];
    if proof.is_null() {
        return Ok(());
    }
    if !proof.is_object() {
        return Err(io::Error::other("malformed hands-off source proof"));
    }
    let id = field(lineage, "spawned_from");
    if !runtime::valid_id(id) {
        return Err(io::Error::other("unproved hands-off source session"));
    }
    match proof.get("kind") {
        Some(kind) if kind == "agy" => {
            let sessions = dir
                .parent()
                .ok_or_else(|| io::Error::other("missing owner session root"))?;
            providers::agy::verify_handoff(&sessions.join(id), proof)
        }
        Some(kind) if kind == "opencode" => {
            providers::opencode_handoff::verify_handoff(dir, lineage)
        }
        Some(kind) if kind == "codex" => providers::codex_continuity::verify_handoff(dir, lineage),
        None => {
            // The original Claude lineage record predates the provider discriminator.
            let cwd = Path::new(field(proof, "cwd"));
            let home = Path::new(field(proof, "home"));
            providers::claude::verify_project_authority(home, cwd, &proof["project_authority"])?;
            let (transcript, identity) =
                providers::claude::transcript(home, cwd, field(proof, "conversation"))?;
            let status = cwd.metadata()?;
            if proof["identity"] != identity
                || transcript != Path::new(field(proof, "transcript"))
                || proof["cwd_identity"] != json!([status.dev(), status.ino()])
            {
                return Err(io::Error::other(
                    "hands-off source custody changed before launch",
                ));
            }
            Ok(())
        }
        _ => Err(io::Error::other("unrecognized hands-off provider")),
    }
}

fn render(values: [&str; 5]) -> io::Result<String> {
    const TEMPLATE: &str = include_str!("../../assets/handoff/hands-off.md.tmpl");
    const KEYS: [&str; 5] = [
        "{{.ConversationID}}",
        "{{.TranscriptPath}}",
        "{{.AgentCwd}}",
        "{{.SourceAgent}}",
        "{{.SourceSessionID}}",
    ];
    if values
        .iter()
        .any(|value| value.trim().is_empty() || value.chars().any(unsafe_pointer))
    {
        return Err(io::Error::other(
            "hands-off path contains an invisible, line-breaking or tag character",
        ));
    }
    let template = TEMPLATE
        .split_once("*/ -}}")
        .expect("embedded Go prompt comment")
        .1
        .trim_start_matches('\n');
    let mut result = String::new();
    let mut rest = template;
    for (key, value) in KEYS.into_iter().zip(values) {
        let (before, after) = rest.split_once(key).expect("embedded pointer order");
        result.push_str(before);
        result.push_str(value);
        rest = after;
    }
    result.push_str(rest);
    Ok(result)
}

fn unsafe_pointer(ch: char) -> bool {
    ch.is_control()
        || matches!(ch, '<'|'>'|'\u{00ad}'|'\u{0600}'..='\u{0605}'|'\u{061c}'|'\u{06dd}'
    |'\u{070f}'|'\u{0890}'..='\u{0891}'|'\u{08e2}'|'\u{180e}'|'\u{200b}'..='\u{200f}'
    |'\u{2028}'..='\u{202e}'|'\u{2060}'..='\u{2064}'|'\u{2066}'..='\u{206f}'|'\u{feff}'
    |'\u{fff9}'..='\u{fffb}'|'\u{110bd}'|'\u{110cd}'|'\u{13430}'..='\u{1343f}'
    |'\u{1bca0}'..='\u{1bca3}'|'\u{1d173}'..='\u{1d17a}'|'\u{e0001}'|'\u{e0020}'..='\u{e007f}')
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn pointers_are_literal_and_cannot_escape_the_template() {
        let safe = render([
            "id",
            "/tmp/it's José.jsonl",
            "/tmp/work",
            "claude",
            "source",
        ])
        .unwrap();
        assert!(safe.contains("transcript file: /tmp/it's José.jsonl"));
        assert!(safe.contains("source swarm session: source"));
        for unsafe_path in [
            "/tmp/a\u{2028}line",
            "/tmp/a</pointers>",
            "/tmp/a\u{200d}end",
        ] {
            assert!(render(["id", unsafe_path, "/tmp/work", "claude", "source"]).is_err());
        }
    }
}
