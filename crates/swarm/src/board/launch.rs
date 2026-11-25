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

pub(super) struct Launch {
    agent: usize,
    focus: usize,
    cwd: Editor,
    name: Editor,
    tag: Editor,
    prompt: Editor,
    model: Editor,
    opencode_agent: Editor,
    execution: usize,
    skip_permissions: bool,
    worktree: bool,
    completion: Option<Completion>,
    create_cwd: Option<PathBuf>,
    picked_agent: bool,
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

        Self {
            agent: 0,
            focus: 0,
            cwd: Editor::new(&cwd.to_string_lossy()),
            name: Editor::new(""),
            tag: Editor::new(""),
            prompt: Editor::new(""),
            model: Editor::new(""),
            opencode_agent: Editor::new(""),
            execution: 0,
            skip_permissions: false,
            worktree: false,
            completion: None,
            create_cwd: None,
            picked_agent: false,
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
            && !self.picked_agent
            && !matches!(self.detected[self.agent], Detection::Ready(_))
            && let Some(index) = self
                .detected
                .iter()
                .position(|entry| matches!(entry, Detection::Ready(_)))
        {
            self.agent = index;
        }
        changed
    }

    fn extra_fields(&self) -> usize {
        if matches!(PROVIDERS[self.agent], "agy") {
            3
        } else {
            2
        }
    }

    pub(super) fn readable((cols, rows): (u16, u16)) -> bool {
        cols >= 32 && rows >= 8
    }

    pub(super) fn update(&mut self, key: Key, (cols, rows): (u16, u16)) -> io::Result<Action> {
        self.update_detection();
        let extras = self.extra_fields();
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
            Key::Left | Key::Right if self.focus == 3 => {
                let step = if matches!(key, Key::Left) {
                    PROVIDERS.len() - 1
                } else {
                    1
                };
                self.agent = (self.agent + step) % PROVIDERS.len();
                self.picked_agent = true;
                self.model = Editor::new("");
                self.execution = 0;
                self.skip_permissions = false;
            }
            Key::Text(' ') | Key::Left | Key::Right if self.focus == 5 + extras => {
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
                4 => self.model.apply(key),
                5 if PROVIDERS[self.agent] == "opencode" => self.opencode_agent.apply(key),
                5 if PROVIDERS[self.agent] == "claude" => {
                    if matches!(key, Key::Left | Key::Right | Key::Text(' ')) {
                        self.skip_permissions = !self.skip_permissions;
                    }
                }
                5 if matches!(key, Key::Left | Key::Right) => {
                    self.execution =
                        (self.execution + if matches!(key, Key::Left) { 3 } else { 1 }) % 4;
                }
                6 if PROVIDERS[self.agent] == "agy" => {
                    if matches!(key, Key::Left | Key::Right | Key::Text(' ')) {
                        self.skip_permissions = !self.skip_permissions;
                    }
                }
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
        let mut options = Map::new();
        let mut option = |key: &str, value: &str| {
            if !value.is_empty() {
                options.insert(key.to_owned(), json!(value));
            }
        };
        option("model", self.model.text.trim());
        match agent {
            "codex" => option(
                "sandbox",
                ["", "read-only", "workspace-write", "danger-full-access"][self.execution],
            ),
            "agy" => option("mode", ["", "accept-edits", "auto", "plan"][self.execution]),
            "opencode" => option("agent", self.opencode_agent.text.trim()),
            _ => {}
        }
        if self.skip_permissions && matches!(agent, "agy" | "claude") {
            option("dangerously-skip-permissions", "true");
        }
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
        let picker = if compact {
            format!("◂ {agent} ▸")
        } else {
            let entries = PROVIDERS
                .iter()
                .zip(&self.detected)
                .map(|(name, state)| {
                    let marker = match state {
                        Detection::Ready(_) => "●",
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
        let mode = if agent == "codex" {
            [
                "Default",
                "read-only",
                "workspace-write",
                "danger-full-access",
            ][self.execution]
        } else {
            ["Default", "accept-edits", "auto", "plan"][self.execution]
        };
        let permission = ("Skip permission prompts", on(self.skip_permissions));
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
        let model = default(&self.model.text, "(default)");
        let prompt = default(&self.prompt.text, "(optional)");
        let worktree = format!("{} · isolate in a git worktree", on(self.worktree));
        let mut fields = vec![
            ("directory", directory.as_str()),
            ("name", name.as_str()),
            ("tag", tag.as_str()),
            ("agent", picker.as_str()),
            ("Model", model.as_str()),
        ];
        match agent {
            "codex" => fields.push(("Sandbox", mode)),
            "agy" => fields.extend([("Execution mode", mode), permission]),
            "opencode" => fields.push(("OpenCode agent", &self.opencode_agent.text)),
            _ => fields.push(permission),
        }
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
                &format!("{mark} {label:<12}{value}"),
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
    fn common_prefix_stops_on_a_rune_boundary() {
        let choices = ["/work/αα/".to_owned(), "/work/αβ/".to_owned()];
        assert_eq!(longest_common_path(&choices), "/work/α");
        assert_eq!(longest_common_path(&[]), "");
        assert_eq!(longest_common_path(&choices[..1]), "/work/αα/");
    }
}
