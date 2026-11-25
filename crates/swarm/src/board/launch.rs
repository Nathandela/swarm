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

struct Completion {
    seed: String,
    choices: Vec<String>,
    next: usize,
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

    pub(super) fn update(&mut self, key: Key, (cols, rows): (u16, u16)) -> io::Result<Action> {
        self.update_detection();
        let extras = self.extra_fields();
        let count = 6 + extras;
        let directory_completion =
            self.focus == 0 && matches!(key, Key::Left | Key::Right | Key::Tab);
        if !directory_completion {
            self.completion = None;
        }
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
                0 => self.cwd.apply(key),
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
        let completion = self.completion.get_or_insert_with(|| Completion {
            seed: self.cwd.text.clone(),
            choices: directories(&self.cwd.text),
            next: 0,
        });
        let len = completion.choices.len();
        if len == 0 {
            return;
        }
        // A cycle includes the user's original prefix.
        let count = len + 1;
        completion.next = (completion.next + if forward { 1 } else { count - 1 }) % count;
        self.cwd = Editor::new(if completion.next == 0 {
            &completion.seed
        } else {
            &completion.choices[completion.next - 1]
        });
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
        form_header(output, cols, "New session");
        let agent = PROVIDERS[self.agent];
        let state = match &self.detected[self.agent] {
            Detection::Checking => "checking",
            Detection::Ready(version) => version,
            Detection::Disabled(reason) => reason,
        };
        let agent_label = format!("‹ {agent} › · {state}");
        let on = |value| if value { "Yes" } else { "No" };
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
        let mut fields = vec![
            ("Directory", self.cwd.text.as_str()),
            ("Name", self.name.text.as_str()),
            ("Tag", self.tag.text.as_str()),
            ("Agent", agent_label.as_str()),
            ("Model", self.model.text.as_str()),
        ];
        match agent {
            "codex" => fields.push(("Sandbox", mode)),
            "agy" => fields.extend([("Execution mode", mode), permission]),
            "opencode" => fields.push(("OpenCode agent", &self.opencode_agent.text)),
            _ => fields.push(permission),
        }
        fields.extend([
            ("Prompt", self.prompt.text.as_str()),
            ("Worktree", on(self.worktree)),
        ]);
        let capacity = usize::from(rows.saturating_sub(2));
        let start = (self.focus + 1).saturating_sub(capacity);
        for (index, (label, value)) in fields.iter().enumerate().skip(start).take(capacity) {
            let active = index == self.focus;
            let mark = if active { "▌" } else { " " };
            line(
                output,
                (index - start) as u16 + 2,
                cols,
                if active { "\x1b[1m" } else { "" },
                &format!("{mark} {label}  {value}"),
            );
        }
        let hint = if self.focus == 0 {
            "↑↓ field · tab/←→ complete path · enter launch · esc cancel"
        } else {
            "↑↓ field · ←→ change · enter launch · esc cancel"
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
