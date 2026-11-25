use super::{Action, Editor, Key, caller_launch_environment, form_header, line};
use serde_json::{Map, json};
use std::env;
use std::fs;
use std::io;
use std::path::PathBuf;
use std::sync::mpsc::{self, Receiver};
use std::thread;

const PROVIDERS: [&str; 5] = ["agy", "claude", "codex", "hermes", "opencode"];

enum Detection {
    Checking,
    Ready(String),
    Disabled(String),
}

#[derive(Default)]
struct Completion {
    choices: Vec<String>,
    ghost: String,
}

impl Completion {
    fn from_prefix(prefix: &str) -> Self {
        let choices = directories(prefix);
        let ghost = longest_common_path(&choices)
            .strip_prefix(prefix)
            .unwrap_or("")
            .to_owned();
        Self { choices, ghost }
    }
}

fn longest_common_path(paths: &[String]) -> &str {
    let Some(first) = paths.first() else {
        return "";
    };
    let Some(last) = paths.last() else { return "" };
    let bytes = first
        .chars()
        .zip(last.chars())
        .take_while(|(a, b)| a == b)
        .map(|(ch, _)| ch.len_utf8())
        .sum();
    &first[..bytes]
}

enum OptionInput {
    Text(&'static [&'static str]),
    Choice(&'static [&'static str]),
    Toggle,
}

struct LaunchOption {
    key: &'static str,
    label: &'static str,
    input: OptionInput,
    value: Editor,
}

impl LaunchOption {
    fn new(key: &'static str, label: &'static str, input: OptionInput, default: &str) -> Self {
        Self {
            key,
            label,
            input,
            value: Editor::new(default),
        }
    }

    fn apply(&mut self, key: Key) {
        match (&self.input, &key) {
            (OptionInput::Toggle, Key::Text(' ')) => {
                self.value = Editor::new(if self.value.text == "true" {
                    "false"
                } else {
                    "true"
                });
            }
            (OptionInput::Choice(choices) | OptionInput::Text(choices), Key::Left | Key::Right)
                if !choices.is_empty() =>
            {
                let forward = matches!(key, Key::Right);
                let index = choices.iter().position(|value| *value == self.value.text);
                let count = choices.len();
                let selected = match (index, forward) {
                    (None, true) => 0,
                    (None, false) => count - 1,
                    (Some(i), true) => (i + 1) % count,
                    (Some(i), false) => (i + count - 1) % count,
                };
                self.value = Editor::new(choices[selected]);
            }
            (OptionInput::Text(_), _) => self.value.apply(key),
            _ => {}
        }
    }

    fn display(&self) -> String {
        match self.input {
            OptionInput::Toggle => if self.value.text == "true" {
                "[x] space"
            } else {
                "[ ] space"
            }
            .into(),
            OptionInput::Choice(_) => format!("{} ▾", self.value.text),
            OptionInput::Text(_) if self.value.text.is_empty() => "(default)".into(),
            OptionInput::Text(_) => self.value.text.clone(),
        }
    }
}

fn provider_options(agent: &str) -> Vec<LaunchOption> {
    use OptionInput::{Choice, Text, Toggle};
    let field = LaunchOption::new;
    let model = || field("model", "Model", Text(&[]), "");
    let skip = || {
        field(
            "dangerously-skip-permissions",
            "Skip permission prompts",
            Toggle,
            "false",
        )
    };
    match agent {
        "agy" => vec![
            field(
                "model",
                "Model",
                Text(&[
                    "Orchestrator (Carbon)",
                    "Orchestrator (Argon)",
                    "Gemini 4 Carbon (Medium)",
                    "Gemini 4 Carbon (High)",
                    "Gemini 4 Argon (Medium)",
                    "Gemini 4 Hydrogen (Medium)",
                    "Gemini 4 Hydrogen (High)",
                    "Gemini 3.8 Flash (Medium)",
                    "Claude Sonnet 5.5 (Medium)",
                    "Claude Opus 5.5 (Medium)",
                    "GPT-OSS 120B (Medium)",
                ]),
                "",
            ),
            field(
                "mode",
                "Execution mode",
                Choice(&["accept-edits", "auto", "plan"]),
                "",
            ),
            skip(),
        ],
        "claude" => vec![
            field("model", "Model", Text(&["sonnet", "opus", "haiku"]), ""),
            skip(),
        ],
        "codex" => vec![
            model(),
            field(
                "sandbox",
                "Sandbox mode",
                Choice(&["read-only", "workspace-write", "danger-full-access"]),
                "workspace-write",
            ),
        ],
        "hermes" => vec![
            field("profile", "Profile", Text(&[]), ""),
            field("provider", "Provider", Text(&[]), ""),
            model(),
            field(
                "reasoning",
                "Reasoning",
                Choice(&[
                    "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra",
                ]),
                "",
            ),
            field("toolsets", "Toolsets (comma-separated)", Text(&[]), ""),
            field("skills", "Skills (comma-separated)", Text(&[]), ""),
            field(
                "yolo",
                "YOLO — auto-approve all tool calls (dangerous)",
                Toggle,
                "false",
            ),
        ],
        "opencode" => vec![
            field("model", "Model (provider/model)", Text(&[]), ""),
            field("agent", "Agent", Text(&[]), ""),
        ],
        _ => vec![],
    }
}

pub(super) struct Launch {
    agent: usize,
    focus: usize,
    cwd: Editor,
    name: Editor,
    tag: Editor,
    prompt: Editor,
    options: Vec<LaunchOption>,
    worktree: bool,
    completion: Option<Completion>,
    create_cwd: Option<PathBuf>,
    detection_complete: bool,
    detected: [Detection; PROVIDERS.len()],
    detection: Receiver<(usize, io::Result<String>)>,
}

impl Launch {
    pub(super) fn new() -> Self {
        let cwd = env::current_dir().unwrap_or_default();
        let (send, detection) = mpsc::channel();
        for (index, agent) in PROVIDERS.into_iter().enumerate() {
            let send = send.clone();
            let cwd = cwd.clone();
            thread::spawn(move || {
                let _ = send.send((index, crate::providers::detect_for_board(agent, &cwd)));
            });
        }

        Self::with_detection(cwd, detection)
    }

    fn with_detection(cwd: PathBuf, detection: Receiver<(usize, io::Result<String>)>) -> Self {
        Self {
            agent: 0,
            focus: 0,
            cwd: Editor::new(&cwd.to_string_lossy()),
            name: Editor::new(""),
            tag: Editor::new(""),
            prompt: Editor::new(""),
            options: Vec::new(),
            worktree: false,
            completion: None,
            create_cwd: None,
            detection_complete: false,
            detected: std::array::from_fn(|_| Detection::Checking),
            detection,
        }
    }

    pub(super) fn update_detection(&mut self) -> bool {
        let mut changed = false;
        while let Ok((index, result)) = self.detection.try_recv() {
            self.detected[index] = match result {
                Ok(version) => Detection::Ready(version),
                Err(error) => Detection::Disabled(error.to_string()),
            };
            changed = true;
        }
        if changed
            && !self.detection_complete
            && self
                .detected
                .iter()
                .all(|entry| !matches!(entry, Detection::Checking))
        {
            self.detection_complete = true;
            self.agent = self
                .detected
                .iter()
                .position(|entry| matches!(entry, Detection::Ready(_)))
                .unwrap_or(0);
            self.options = provider_options(PROVIDERS[self.agent]);
            // The prompt and worktree keep focus when the provider options arrive.
            if self.focus >= 4 {
                self.focus += self.options.len();
            }
        }
        changed
    }

    pub(super) fn readable((cols, rows): (u16, u16)) -> bool {
        cols >= 32 && rows >= 8
    }

    pub(super) fn update(&mut self, key: Key, (cols, rows): (u16, u16)) -> io::Result<Action> {
        self.update_detection();
        let extras = self.options.len();
        let count = 6 + extras;
        let directory_completion =
            self.focus == 0 && matches!(key, Key::Left | Key::Right | Key::Tab);
        if !matches!(key, Key::Enter) {
            self.create_cwd = None;
        }
        match key {
            Key::Up => self.focus = (self.focus + count - 1) % count,
            Key::Down => self.focus = (self.focus + 1) % count,
            Key::Enter => return self.submit(cols, rows),
            Key::Left | Key::Right | Key::Tab if directory_completion => {
                self.complete(!matches!(key, Key::Left));
            }
            Key::Left | Key::Right if self.focus == 3 && self.detection_complete => {
                let step = if matches!(key, Key::Left) {
                    PROVIDERS.len() - 1
                } else {
                    1
                };
                self.agent = (self.agent + step) % PROVIDERS.len();
                self.options = provider_options(PROVIDERS[self.agent]);
            }
            Key::Text(' ') if self.focus == 5 + extras => {
                self.worktree = !self.worktree;
            }
            key => match self.focus {
                0 => {
                    let current = self.cwd.text.clone();
                    self.cwd.apply(key);
                    if self.cwd.text != current {
                        self.completion = Some(Completion::from_prefix(&self.cwd.text));
                    }
                }
                1 => self.name.apply(key),
                2 => self.tag.apply(key),
                index if (4..4 + extras).contains(&index) => self.options[index - 4].apply(key),
                index if index == 4 + extras => self.prompt.apply(key),
                _ => {}
            },
        }
        Ok(Action::None)
    }

    fn complete(&mut self, forward: bool) {
        let completion = self
            .completion
            .get_or_insert_with(|| Completion::from_prefix(&self.cwd.text));
        if forward && !completion.ghost.is_empty() {
            self.cwd = Editor::new(&format!("{}{}", self.cwd.text, completion.ghost));
            self.completion = Some(Completion::from_prefix(&self.cwd.text));
            return;
        }
        let len = completion.choices.len();
        if len < 2 {
            return;
        }
        let index = completion
            .choices
            .iter()
            .position(|path| path == &self.cwd.text)
            .map_or_else(
                || if forward { 0 } else { len - 1 },
                |index| (index + if forward { 1 } else { len - 1 }) % len,
            );
        self.cwd = Editor::new(&completion.choices[index]);
    }

    fn submit(&mut self, cols: u16, rows: u16) -> io::Result<Action> {
        if !self.detection_complete {
            return Err(io::Error::other("Checking provider availability"));
        }
        match &self.detected[self.agent] {
            Detection::Checking => return Err(io::Error::other("Checking provider availability")),
            Detection::Disabled(reason) => return Err(io::Error::other(reason.clone())),
            Detection::Ready(_) => {}
        }
        let agent = PROVIDERS[self.agent];
        let raw = self.cwd.text.trim();
        if raw.is_empty() {
            return Err(io::Error::other("Directory is required"));
        }
        let cwd = absolute_directory(raw)?;
        match fs::metadata(&cwd) {
            Ok(metadata) if metadata.is_dir() => {}
            Ok(_) => return Err(io::Error::other("Workspace exists but is not a directory")),
            Err(error) if error.kind() == io::ErrorKind::NotFound => {
                if self.create_cwd.as_ref() != Some(&cwd) {
                    self.create_cwd = Some(cwd);
                    return Err(io::Error::other(
                        "Directory does not exist · press Enter again to create it",
                    ));
                }
                fs::create_dir_all(&cwd)?;
            }
            Err(error) => return Err(error),
        }
        if agent == "codex" {
            crate::providers::codex::refuse_unqualified_origin(&serde_json::Value::Null)?;
        }
        let options: Map<_, _> = self
            .options
            .iter()
            .map(|field| (field.key.to_owned(), json!(field.value.text)))
            .collect();
        let name = self.name.text.trim();
        let default_name = format!(
            "{agent}-{}",
            cwd.file_name().and_then(|n| n.to_str()).unwrap_or("root")
        );
        let mut launch = json!({
            "agent":agent, "cwd":cwd, "name":if name.is_empty() { &default_name } else { name },
            "options":options, "env":caller_launch_environment(agent), "cols":cols, "rows":rows,
            "initial_prompt":self.prompt.text,
        });
        if !self.tag.text.trim().is_empty() {
            launch["tag"] = json!(self.tag.text.trim());
        }
        if self.worktree {
            launch["worktree"] = json!(true);
        }
        Ok(Action::Request(
            json!({"op":"launch","launch":launch}),
            "launch",
        ))
    }

    pub(super) fn view(&self, output: &mut String, cols: u16, rows: u16) {
        let agent = PROVIDERS[self.agent];
        let compact = rows < 12 || cols < 60;
        let blocked = !Self::readable((cols, rows));
        let issue = match &self.detected[self.agent] {
            Detection::Checking => Some("checking native executable"),
            Detection::Disabled(reason) => Some(reason.as_str()),
            Detection::Ready(_) => None,
        };
        if blocked {
            form_header(output, cols, "Resize to 32×8");
            let label = match self.focus {
                0 => format!(
                    "▌ directory   {}",
                    self.cwd.visible(cols.saturating_sub(14).max(1)).0
                ),
                1 => format!("▌ name        {}", self.name.text),
                2 => format!("▌ tag         {}", self.tag.text),
                _ => format!("▌ agent       ◂ {agent} ▸"),
            };
            line(output, 2, cols, "", &label);
            line(output, 3, cols, "", "Esc back / n deny");
            line(output, rows, cols, "\x1b[2m", "esc back");
            return;
        }
        let title = if compact {
            format!("New session · {agent}")
        } else {
            "swarm · new session".into()
        };
        form_header(output, cols, &title);
        let picker = if !self.detection_complete {
            "checking...".into()
        } else if compact {
            format!("◂ {agent} ▸")
        } else {
            let entries = PROVIDERS
                .iter()
                .zip(&self.detected)
                .map(|(name, state)| {
                    let marker = match state {
                        _ if *name == agent => "●",
                        Detection::Ready(_) => "○",
                        Detection::Checking => "…",
                        Detection::Disabled(_) => "✕",
                    };
                    format!("{marker} {name}")
                })
                .collect::<Vec<_>>()
                .join("   ");
            format!("◂ {entries} ▸")
        };
        let on = |value| if value { "[x] space" } else { "[ ] space" };
        let available_completion = self
            .completion
            .as_ref()
            .is_some_and(|c| !c.ghost.is_empty() || c.choices.len() > 1);
        let directory = if self.focus == 0 {
            self.cwd.visible(cols.saturating_sub(14).max(1)).0
        } else {
            self.cwd.text.clone()
        };
        let default = |value: &str, fallback: &'static str| {
            if value.is_empty() {
                fallback.to_owned()
            } else {
                value.to_owned()
            }
        };
        let name = default(&self.name.text, "(optional · defaults to agent-dir)");
        let tag = default(&self.tag.text, "(optional · groups the board under o)");
        let options: Vec<_> = self.options.iter().map(LaunchOption::display).collect();
        let prompt = default(&self.prompt.text, "(optional)");
        let worktree = format!("{} · isolate in a git worktree", on(self.worktree));
        let mut fields = vec![
            ("directory", directory.as_str()),
            ("name", name.as_str()),
            ("tag", tag.as_str()),
            ("agent", picker.as_str()),
        ];
        fields.extend(
            self.options
                .iter()
                .zip(&options)
                .map(|(field, text)| (field.label, text.as_str())),
        );
        fields.extend([("prompt", prompt.as_str()), ("worktree", worktree.as_str())]);
        let mut order: Vec<usize> = (0..fields.len()).collect();
        let first_row = if compact { 2 } else { 3 };
        if compact && self.focus < order.len() {
            order.remove(self.focus);
            order.insert(0, self.focus);
        }
        let capacity = usize::from(rows.saturating_sub(first_row));
        let focused_line = order
            .iter()
            .position(|&index| index == self.focus)
            .unwrap_or(0);
        let start = (focused_line + 1).saturating_sub(capacity);
        for (offset, &index) in order.iter().skip(start).take(capacity).enumerate() {
            let (label, value) = fields[index];
            let mark = if index == self.focus { "▌" } else { " " };
            line(
                output,
                offset as u16 + first_row,
                cols,
                "",
                &format!("{mark} {label:<11} {value}"),
            );
        }
        if self.focus == 3 || !compact {
            let status = match &self.detected[self.agent] {
                Detection::Ready(version) if !compact => Some(format!("{agent} {version}")),
                _ => issue.map(|reason| format!("{agent} · {reason}")),
            };
            if let Some(status) = status {
                let bottom = first_row + order.len().saturating_sub(start) as u16;
                if bottom < rows {
                    line(output, bottom, cols, "\x1b[2m", &status);
                }
            }
        }
        let hint = if compact {
            "esc cancel · enter launch · ↑↓ next"
        } else if self.focus == 0 && available_completion {
            "tab/←→ complete · ↑↓ next · enter launch · esc cancel"
        } else if self.focus == 3 {
            "arrows change · ↑↓ next · enter launch · esc cancel"
        } else {
            "↑↓ next · enter launch · esc cancel"
        };
        line(output, rows, cols, "\x1b[2m", hint);
    }
}

fn absolute_directory(raw: &str) -> io::Result<PathBuf> {
    let home = || {
        env::var_os("HOME")
            .map(PathBuf::from)
            .ok_or_else(|| io::Error::other("HOME is undefined"))
    };
    match raw {
        "~" => home(),
        _ if raw.starts_with("~/") => Ok(home()?.join(&raw[2..])),
        _ => std::path::absolute(raw),
    }
}

fn directories(raw: &str) -> Vec<String> {
    let (base, prefix) = raw
        .rsplit_once('/')
        .map_or(("", raw), |(base, prefix)| (base, prefix));
    let directory = if raw.starts_with('/') && base.is_empty() {
        "/"
    } else if base.is_empty() {
        "."
    } else {
        base
    };
    let Ok(directory) = absolute_directory(directory) else {
        return vec![];
    };
    let lead = &raw[..raw.len() - prefix.len()];
    let Ok(entries) = fs::read_dir(directory) else {
        return vec![];
    };
    let mut matches: Vec<_> = entries
        .flatten()
        .filter_map(|entry| {
            let name = entry.file_name().into_string().ok()?;
            (name.starts_with(prefix)
                && (!name.starts_with('.') || prefix.starts_with('.'))
                && entry.path().is_dir())
            .then(|| format!("{lead}{name}/"))
        })
        .collect();
    matches.sort_unstable();
    matches
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn detection_order_preserves_prompt_focus_and_public_launch_options() {
        let (probe, detection) = mpsc::channel();
        let mut form = Launch::with_detection(env::temp_dir(), detection);
        form.focus = 4;
        for i in (1..PROVIDERS.len()).rev() {
            probe.send((i, Ok("1.0".into()))).unwrap();
            form.update_detection();
        }
        assert!(form.options.is_empty());
        probe.send((0, Err(io::Error::other("missing")))).unwrap();
        form.update(Key::Text('p'), (80, 24)).unwrap();
        assert_eq!(PROVIDERS[form.agent], "claude");
        assert_eq!(form.prompt.text, "p");
        form.focus = 4;
        form.update(Key::Text('x'), (80, 24)).unwrap();
        form.update(Key::Right, (80, 24)).unwrap();
        let Action::Request(payload, _) = form.submit(80, 24).unwrap() else {
            panic!("expected launch")
        };
        assert_eq!(
            payload["launch"]["options"],
            json!({"model":"sonnet", "dangerously-skip-permissions":"false"})
        );
        form.focus = 3;
        form.update(Key::Right, (80, 24)).unwrap();
        assert_eq!(form.options[1].value.text, "workspace-write");
        form.update(Key::Right, (80, 24)).unwrap();
        assert_eq!(
            form.options.iter().map(|o| o.key).collect::<Vec<_>>(),
            crate::providers::hermes::OPTIONS
        );
        form.focus = 4 + form.options.len() - 1;
        form.update(Key::Text(' '), (80, 24)).unwrap();
        let Action::Request(payload, _) = form.submit(80, 24).unwrap() else {
            panic!("expected launch")
        };
        assert_eq!(payload["launch"]["agent"], "hermes");
        assert_eq!(payload["launch"]["options"]["yolo"], "true");
        assert_eq!(payload["launch"]["initial_prompt"], "p");
    }

    #[test]
    fn common_prefix_stops_on_a_rune_boundary() {
        let choices = ["/work/αα/".to_owned(), "/work/αβ/".to_owned()];
        assert_eq!(longest_common_path(&choices), "/work/α");
        assert_eq!(longest_common_path(&[]), "");
        assert_eq!(longest_common_path(&choices[..1]), "/work/αα/");
    }
}
