use super::{Action, Editor, Key, caller_launch_environment, clip};
use serde_json::{Map, json};
use std::env;
use std::fmt::Write as _;
use std::fs;
use std::io;
use std::path::PathBuf;
use std::sync::mpsc::{self, Receiver};
use std::thread;
use unicode_width::{UnicodeWidthChar, UnicodeWidthStr};

const PROVIDERS: [&str; 5] = ["agy", "claude", "codex", "hermes", "opencode"];

enum Detection {
    Checking,
    Ready,
    Disabled { reason: String, installed: bool },
}

#[derive(Default)]
pub(super) struct Completion {
    pub(super) choices: Vec<String>,
    pub(super) ghost: String,
}

impl Completion {
    pub(super) fn from_prefix(prefix: &str) -> Self {
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
                Ok(_) => Detection::Ready,
                Err(error) => Detection::Disabled {
                    installed: error.kind() != io::ErrorKind::NotFound,
                    reason: error.to_string(),
                },
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
                .position(|entry| matches!(entry, Detection::Ready))
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
        if !matches!(key, Key::Enter) {
            self.create_cwd = None;
        }
        match key {
            Key::Up => self.focus = (self.focus + count - 1) % count,
            Key::Down => self.focus = (self.focus + 1) % count,
            Key::Enter => return self.submit(cols, rows),
            Key::Left | Key::Right if self.focus == 3 => self.cycle_agent(key == Key::Left),
            Key::Text(' ') if self.focus == 5 + extras => {
                self.worktree = !self.worktree;
            }
            key => match self.focus {
                0 => update_directory(&mut self.cwd, &mut self.completion, key),
                1 => self.name.apply(key),
                2 => self.tag.apply(key),
                index if (4..4 + extras).contains(&index) => self.options[index - 4].apply(key),
                index if index == 4 + extras => self.prompt.apply(key),
                _ => {}
            },
        }
        Ok(Action::None)
    }

    fn submit(&mut self, cols: u16, rows: u16) -> io::Result<Action> {
        if !self.detection_complete {
            return Err(io::Error::other("Checking provider availability"));
        }
        match &self.detected[self.agent] {
            Detection::Checking => return Err(io::Error::other("Checking provider availability")),
            Detection::Disabled { reason, .. } => return Err(io::Error::other(reason.clone())),
            Detection::Ready => {}
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

    pub(super) fn ready_agent(&self) -> Option<&'static str> {
        (self.detection_complete && matches!(self.detected[self.agent], Detection::Ready))
            .then_some(PROVIDERS[self.agent])
    }

    pub(super) fn cycle_agent(&mut self, left: bool) {
        if self.detection_complete {
            let step = if left { PROVIDERS.len() - 1 } else { 1 };
            self.agent = (self.agent + step) % PROVIDERS.len();
            self.options = provider_options(PROVIDERS[self.agent]);
        }
    }

    pub(super) fn picker(&self, cols: u16) -> Vec<(&'static str, String)> {
        if !self.detection_complete {
            return vec![(DIM, "checking...".into())];
        }
        let render = |all_reasons: bool, selected_budget: Option<u16>| {
            let mut pieces = vec![(DIM, "◂ ".into())];
            for (i, name) in PROVIDERS.iter().enumerate() {
                if i != 0 {
                    pieces.push((PLAIN, "   ".into()));
                }
                let selected = i == self.agent;
                let mark = match (&self.detected[i], selected) {
                    (_, true) => '●',
                    (
                        Detection::Disabled {
                            installed: false, ..
                        },
                        false,
                    ) => '✕',
                    _ => '○',
                };
                let mut label = format!("{mark} {name}");
                let style = match &self.detected[i] {
                    Detection::Disabled { reason, .. } => {
                        let reason = if selected {
                            selected_budget
                                .map_or_else(|| reason.clone(), |budget| clip(reason, budget))
                        } else if all_reasons {
                            reason.clone()
                        } else {
                            String::new()
                        };
                        if !reason.is_empty() {
                            let _ = write!(&mut label, " ({reason})");
                        }
                        DIM
                    }
                    _ if selected => ACCENT,
                    _ => PLAIN,
                };
                pieces.push((style, label));
            }
            pieces.push((DIM, " ▸".into()));
            pieces
        };
        let width =
            |pieces: &[(&str, String)]| pieces.iter().map(|(_, text)| text.width()).sum::<usize>();
        let budget = usize::from(label_layout("agent", cols).1);
        let full = render(true, None);
        if width(&full) <= budget {
            return full;
        }
        let trimmed = render(false, None);
        if width(&trimmed) <= budget {
            return trimmed;
        }
        let no_reason = render(false, Some(0));
        let remaining = budget.saturating_sub(width(&no_reason) + 3);
        if remaining > 0 {
            return render(false, Some(remaining as u16));
        }
        vec![
            (DIM, "◂ ".into()),
            (ACCENT, PROVIDERS[self.agent].into()),
            (DIM, " ▸".into()),
        ]
    }

    pub(super) fn view(&self, output: &mut String, cols: u16, rows: u16) {
        let agent = if self.detection_complete {
            PROVIDERS[self.agent]
        } else {
            ""
        };
        let compact = cols < 64 && rows <= 10;
        let blocked = !Self::readable((cols, rows));

        let picker = self.picker(cols);

        let edited = |label, editor: &Editor, active, placeholder: &str| {
            if active {
                vec![(PLAIN, block_cursor(editor, label_layout(label, cols).1))]
            } else if editor.text.is_empty() {
                vec![(DIM, placeholder.to_owned())]
            } else {
                vec![(PLAIN, editor.text.clone())]
            }
        };
        let toggle = |value, hint: &str| {
            vec![
                (PLAIN, if value { "[x] " } else { "[ ] " }.into()),
                (DIM, hint.to_owned()),
            ]
        };
        let mut fields = vec![
            (
                "directory",
                edited("directory", &self.cwd, self.focus == 0, ""),
            ),
            (
                "name",
                edited(
                    "name",
                    &self.name,
                    self.focus == 1,
                    "(optional · defaults to agent-dir)",
                ),
            ),
            (
                "tag",
                edited(
                    "tag",
                    &self.tag,
                    self.focus == 2,
                    "(optional · groups the board under o)",
                ),
            ),
            ("agent", picker),
        ];
        for (index, option) in self.options.iter().enumerate() {
            let active = self.focus == index + 4;
            let value = match option.input {
                OptionInput::Toggle => toggle(option.value.text == "true", "space"),
                OptionInput::Choice(_) => vec![(PLAIN, option.display())],
                OptionInput::Text(_) => edited(option.label, &option.value, active, "(default)"),
            };
            fields.push((option.label, value));
        }
        fields.extend([
            (
                "prompt",
                edited(
                    "prompt",
                    &self.prompt,
                    self.focus == self.options.len() + 4,
                    "(optional)",
                ),
            ),
            (
                "worktree",
                toggle(self.worktree, "space · isolate in a git worktree"),
            ),
        ]);
        if self.focus == 0
            && let Some(completion) = &self.completion
            && !completion.ghost.is_empty()
        {
            fields[0].1.push((DIM, completion.ghost.clone()));
        }
        if blocked {
            spans(output, 1, cols, &[(PLAIN, "Resize to 32×8".into())]);
            if let Some((label, value)) = fields.get(self.focus) {
                field(output, 2, cols, label, value, true);
            }
            spans(output, 3, cols, &[(PLAIN, "Esc back / n deny".into())]);
            status(output, rows, cols, "esc back");
            return;
        }
        if compact {
            spans(
                output,
                1,
                cols,
                &[(PLAIN, format!("New session · {agent}"))],
            );
        } else {
            spans(
                output,
                1,
                cols,
                &[(TITLE, "swarm".into()), (DIM, " · new session".into())],
            );
        }
        let first_row = if compact { 2 } else { 3 };
        let mut order: Vec<_> = (0..fields.len()).collect();
        if compact && self.focus < order.len() {
            order.remove(self.focus);
            order.insert(0, self.focus);
        }
        for (offset, &index) in order
            .iter()
            .take(usize::from(rows.saturating_sub(first_row)))
            .enumerate()
        {
            let (label, value) = &fields[index];
            field(
                output,
                first_row + offset as u16,
                cols,
                label,
                value,
                index == self.focus,
            );
        }
        let completion = self
            .completion
            .as_ref()
            .is_some_and(|c| !c.ghost.is_empty() || c.choices.len() > 1);
        let tip = match self.focus {
            0 if completion => "tab/←→ complete · ⌘←/→ home/end · ⌘⌫/ctrl+u clear-left",
            0 => "type or paste · ⌘←/→ home/end · ⌘⌫/ctrl+u clear-left",
            3 => "arrows change",
            n if n == self.options.len() + 5 => "space toggle",
            n if n >= 4 && n < self.options.len() + 4 => match self.options[n - 4].input {
                OptionInput::Choice(_) => "arrows change",
                OptionInput::Toggle => "space toggle",
                OptionInput::Text(choices) if !choices.is_empty() => {
                    "type or paste · ←→ suggest · ⌘←/→ home/end · ⌘⌫/ctrl+u clear-left"
                }
                OptionInput::Text(_) => {
                    "type or paste · ←→ move · ⌘←/→ home/end · ⌘⌫/ctrl+u clear-left"
                }
            },
            _ => "type or paste · ←→ move · ⌘←/→ home/end · ⌘⌫/ctrl+u clear-left",
        };
        let mut hint = format!("{tip} · ↑↓ next · enter launch · esc cancel");
        if hint.width() > usize::from(cols.saturating_sub(2)) {
            hint = format!("esc cancel · enter launch · {tip} · ↑↓ next");
        }
        status(output, rows, cols, &hint);
    }
}

pub(super) const PLAIN: &str = "\x1b[0m";
pub(super) const DIM: &str = "\x1b[0;38;5;60m";
pub(super) const ACCENT: &str = "\x1b[0;38;5;110m";
pub(super) const TITLE: &str = "\x1b[0;1;38;5;110m";

pub(super) fn spans(output: &mut String, row: u16, cols: u16, pieces: &[(&str, String)]) {
    let _ = write!(output, "\x1b[{row};1H");
    let mut remaining = cols;
    for (style, text) in pieces {
        let visible = clip(text, remaining);
        output.push_str(style);
        output.push_str(&visible);
        if visible.width() < text.width() {
            break;
        }
        remaining = remaining.saturating_sub(visible.width() as u16);
        if remaining == 0 {
            break;
        }
    }
    output.push_str(PLAIN);
}

pub(super) fn field(
    output: &mut String,
    row: u16,
    cols: u16,
    label: &str,
    value: &[(&str, String)],
    focused: bool,
) {
    let marker = if focused {
        (ACCENT, "▌".into())
    } else {
        (PLAIN, " ".into())
    };
    let (label, _) = label_layout(label, cols);
    let mut pieces = vec![marker, (PLAIN, " ".into()), (DIM, label)];
    pieces.extend_from_slice(value);
    spans(output, row, cols, &pieces);
}

pub(super) fn label_layout(label: &str, cols: u16) -> (String, u16) {
    let width = label.width();
    let max_label = cols.saturating_sub(3);
    let padded = if (width + 1).max(12) > usize::from(max_label) {
        if max_label == 0 {
            String::new()
        } else {
            format!("{} ", clip(label, max_label - 1))
        }
    } else {
        format!(
            "{label}{}",
            " ".repeat(1.max(12_usize.saturating_sub(width)))
        )
    };
    let remaining = cols.saturating_sub(2 + padded.width() as u16);
    (padded, remaining)
}

pub(super) fn block_cursor(editor: &Editor, width: u16) -> String {
    if width == 0 {
        return String::new();
    }
    let before = &editor.text[..editor.cursor];
    let left = width - 1;
    if before.width() > usize::from(left) {
        let mut start = before.len();
        let mut remaining = usize::from(left);
        for (index, ch) in before.char_indices().rev() {
            let cells = ch.width().unwrap_or(1);
            if cells > remaining {
                break;
            }
            remaining -= cells;
            start = index;
        }
        return format!("{}█", &before[start..]);
    }
    let remaining = left.saturating_sub(before.width() as u16);
    format!(
        "{before}█{}",
        clip(&editor.text[editor.cursor..], remaining)
    )
}

pub(super) fn status(output: &mut String, row: u16, cols: u16, hint: &str) {
    let available = cols.saturating_sub(2);
    let hint = if hint.width() > usize::from(available) && available > 0 {
        format!("{}…", clip(hint, available - 1))
    } else {
        hint.to_owned()
    };
    let version = option_env!("SWARM_BUILD_VERSION").unwrap_or("dev");
    let gap = usize::from(available).saturating_sub(hint.width() + version.width());
    let mut pieces = vec![(PLAIN, "  ".into()), (DIM, hint)];
    if gap >= 3 && !version.is_empty() {
        pieces.extend([(PLAIN, " ".repeat(gap)), (DIM, version.into())]);
    }
    spans(output, row, cols, &pieces);
}

pub(super) fn update_directory(editor: &mut Editor, completion: &mut Option<Completion>, key: Key) {
    if matches!(key, Key::Left | Key::Right | Key::Tab) {
        complete(editor, completion, key != Key::Left);
    } else {
        let current = editor.text.clone();
        editor.apply(key);
        if current != editor.text {
            *completion = Some(Completion::from_prefix(&editor.text));
        }
    }
}

fn complete(editor: &mut Editor, state: &mut Option<Completion>, forward: bool) {
    let completion = state.get_or_insert_with(|| Completion::from_prefix(&editor.text));
    if forward && !completion.ghost.is_empty() {
        *editor = Editor::new(&format!("{}{}", editor.text, completion.ghost));
        *state = Some(Completion::from_prefix(&editor.text));
        return;
    }
    let len = completion.choices.len();
    if len < 2 {
        return;
    }
    let index = completion
        .choices
        .iter()
        .position(|path| path == &editor.text)
        .map_or_else(
            || if forward { 0 } else { len - 1 },
            |index| (index + if forward { 1 } else { len - 1 }) % len,
        );
    *editor = Editor::new(&completion.choices[index]);
}

pub(super) fn absolute_directory(raw: &str) -> io::Result<PathBuf> {
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
    fn wide_viewport_never_moves_suffix_left_of_cursor() {
        let mut editor = Editor::new("界界X");
        editor.apply(Key::Left);
        assert_eq!(block_cursor(&editor, 4), "界█");

        let mut editor = Editor::new("界e\u{301}X");
        editor.apply(Key::Left);
        assert_eq!(block_cursor(&editor, 4), "界e\u{301}█");
    }

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
