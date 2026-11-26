use super::super::launch::{Detected, Detection, PROVIDERS, block_cursor};
use super::super::{Editor, Key, menu, terminal_char};
use crate::schedule::{
    Calendar, Job, Spec, automation_invocation, catalog::Change, check_route_authority,
    editor_firing, resolve_directory,
};

const CADENCES: &[(&str, &str)] = &[
    ("once", "Once"),
    ("daily", "Daily"),
    ("weekdays", "Weekdays"),
    ("weekly", "Weekly"),
    ("cron", "Cron"),
];
const DAYS: &[(&str, &str)] = &[
    ("Monday", "Monday"),
    ("Tuesday", "Tuesday"),
    ("Wednesday", "Wednesday"),
    ("Thursday", "Thursday"),
    ("Friday", "Friday"),
    ("Saturday", "Saturday"),
    ("Sunday", "Sunday"),
];
const HARNESSES: &[(&str, &str)] = &[
    ("codex", "Codex"),
    ("claude", "Claude Code"),
    ("agy", "agy"),
    ("hermes", "Hermes"),
    ("opencode", "OpenCode"),
];
const AUTHORITIES: &[(&str, &str)] = &[
    ("observe", "Observe"),
    ("workspace-write", "Workspace write"),
    ("agent-default", "Configured agent permissions"),
];
const WORKSPACES: &[(&str, &str)] = &[
    ("directory", "Selected directory"),
    ("worktree", "Isolated Git worktree"),
];
const OVERLAP: &[(&str, &str)] = &[
    ("false", "Skip overlaps"),
    ("true", "Allow isolated overlaps"),
];
const MISSED: &[(&str, &str)] = &[
    ("false", "Skip missed runs"),
    ("true", "Catch up the latest within 1 hour"),
];
const HOUR: i64 = 3_600_000_000_000;

#[derive(Clone, Copy, PartialEq, Eq)]
pub(super) enum Field {
    Name,
    Prompt,
    Directory,
    Cadence,
    Day,
    Date,
    Time,
    Cron,
    Zone,
    ConvertZone,
    Primary,
    Authority,
    Advanced,
    Workspace,
    Overlap,
    Missed,
    Fallback(&'static str),
    SaveEnable,
    SaveDraft,
}

pub(super) struct Row {
    pub(super) field: Option<Field>,
    pub(super) text: String,
}

impl Row {
    fn field(field: Field, text: impl Into<String>) -> Self {
        Self {
            field: Some(field),
            text: text.into(),
        }
    }
}

pub(super) struct Preview {
    firing: String,
    host_zone: String,
    directory: Result<String, String>,
}

impl Preview {
    pub(super) fn read(spec: &Spec) -> Self {
        let (firing, host_zone) = editor_firing(&spec.calendar)
            .unwrap_or_else(|error| (error.to_string(), String::new()));
        Self {
            firing,
            host_zone,
            directory: resolve_directory(&spec.directory).map_err(|error| error.to_string()),
        }
    }
}

pub(super) struct Draft {
    target: Option<Job>,
    operation: Option<String>,
    pub(super) name: Editor,
    pub(super) prompt: Editor,
    pub(super) directory: Editor,
    time: Editor,
    date: Editor,
    cron: Editor,
    choices: Spec,
    missed: bool,
    advanced: bool,
    pub(super) convert_zone: bool,
    pub(super) focus: usize,
    top: usize,
    pub(super) preview: Option<(Spec, Preview)>,
    pub(super) preview_after: std::time::Instant,
}

impl Draft {
    pub(super) fn new(job: Option<&Job>) -> Self {
        let mut choices = job.map_or_else(default_spec, |job| job.spec.clone());
        let missed = choices.grace > 0;
        let day = &mut choices.calendar.weekday;
        if let Some((canonical, _)) = DAYS
            .iter()
            .find(|(name, _)| name.eq_ignore_ascii_case(day) || name[..3].eq_ignore_ascii_case(day))
        {
            *day = (*canonical).into();
        } else if day.is_empty() {
            *day = "Monday".into();
        }
        Self {
            target: job.cloned(),
            operation: None,
            name: Editor::new(&choices.name),
            prompt: Editor::new(&choices.prompt),
            directory: Editor::new(&choices.directory),
            time: Editor::new(&choices.calendar.time),
            date: Editor::new(&choices.calendar.date),
            cron: Editor::new(&choices.calendar.cron),
            choices,
            missed,
            focus: 0,
            top: 0,
            advanced: false,
            convert_zone: false,
            preview: None,
            preview_after: std::time::Instant::now(),
        }
    }

    pub(super) fn title(&self) -> &'static str {
        if self.convert_zone {
            "Use host wall time?"
        } else if self.target.is_some() {
            "Automations / Edit"
        } else {
            "Automations / New"
        }
    }

    pub(super) fn labels(&self) -> (&'static str, &'static str) {
        if self
            .target
            .as_ref()
            .is_some_and(|job| job.state == "active")
        {
            ("Save changes", "Save paused")
        } else {
            ("Save and enable", "Save draft")
        }
    }

    pub(super) fn spec(&self) -> Spec {
        let mut spec = self.choices.clone();
        spec.name = self.name.text.trim().into();
        spec.prompt.clone_from(&self.prompt.text);
        spec.directory = self.directory.text.trim().into();
        spec.grace = self.grace();
        let cal = &mut spec.calendar;
        cal.time = if cal.kind == "cron" {
            String::new()
        } else {
            self.time.text.trim().into()
        };
        cal.date = if cal.kind == "once" {
            self.date.text.trim().into()
        } else {
            String::new()
        };
        if cal.kind != "weekly" {
            cal.weekday.clear();
        }
        cal.cron = if cal.kind == "cron" {
            self.cron.text.trim().into()
        } else {
            String::new()
        };
        spec
    }

    fn grace(&self) -> i64 {
        if !self.missed {
            0
        } else if self.choices.grace == 0 {
            HOUR
        } else {
            self.choices.grace
        }
    }

    pub(super) fn use_template(&mut self, name: &str, prompt: &str) {
        let before = self.spec();
        if self.name.text.is_empty() {
            self.name = Editor::new(name);
        }
        self.prompt = Editor::new(prompt);
        self.focus = 1;
        self.top = 0;
        self.sync_preview(&before);
    }

    pub(super) fn change(&mut self, enable: Option<bool>) -> Result<Change, String> {
        let mut spec = self.spec();
        if spec.name.is_empty() || spec.prompt.trim().is_empty() || spec.directory.is_empty() {
            return Err("Enter a name, task instructions, and directory.".into());
        }
        let path = std::path::Path::new(&spec.directory);
        if !path.is_absolute() && spec.directory != "~" && !spec.directory.starts_with("~/") {
            spec.directory = std::env::current_dir()
                .map_err(|err| err.to_string())?
                .join(path)
                .into_os_string()
                .into_string()
                .map_err(|_| "directory path must contain valid UTF-8 text")?;
        }
        let spec = Box::new(spec);
        let Some(job) = &self.target else {
            if self.operation.is_none() {
                self.operation = Some(automation_invocation().map_err(|error| error.to_string())?);
            }
            let operation = self.operation.clone().expect("saved draft operation");
            return Ok(if enable.is_some() {
                Change::CreateEnabled { operation, spec }
            } else {
                Change::Create { operation, spec }
            });
        };
        let id = job.id.clone();
        let revision = job.revision_id.clone();
        let generation = job.lifecycle_generation;
        let updated_at = job.updated_at;
        let operation = if job.state == "active" && enable.is_some() {
            if self.operation.is_none() {
                self.operation = Some(automation_invocation().map_err(|error| error.to_string())?);
            }
            self.operation.clone()
        } else {
            None
        };
        Ok(match (job.state.as_str(), enable) {
            ("paused", Some(install_timer)) => Change::Enable {
                id,
                revision,
                generation,
                updated_at,
                spec,
                install_timer,
            },
            ("active", Some(_)) => Change::SaveActive {
                id,
                revision,
                operation,
                generation,
                updated_at,
                spec,
            },
            ("active", None) => Change::SaveDraft {
                id,
                revision,
                generation,
                updated_at,
                spec,
            },
            _ => Change::Edit {
                id,
                revision,
                spec,
                paused: Some((generation, updated_at)),
            },
        })
    }

    pub(super) fn sync_preview(&mut self, old_spec: &Spec) {
        if self.spec() != *old_spec {
            self.preview = None;
            self.preview_after = std::time::Instant::now() + std::time::Duration::from_millis(180);
        }
    }

    pub(super) fn apply_preview(
        &mut self,
        spec: Spec,
        preview: Preview,
        cols: u16,
        timer: &str,
        detected: Option<&Detected>,
        budget: usize,
    ) {
        let focused = self
            .rows(cols, timer, detected)
            .get(self.focus)
            .and_then(|row| row.field);
        self.preview = Some((spec, preview));
        let rows = self.rows(cols, timer, detected);
        if let Some(field) = focused
            && let Some(index) = rows.iter().position(|row| row.field == Some(field))
        {
            self.focus = index;
        }
        menu::reveal(&mut self.focus, &mut self.top, rows.len(), budget);
    }

    pub(super) fn start(&self, count: usize, budget: usize) -> usize {
        self.top
            .min(count.saturating_sub(budget))
            .min(self.focus)
            .max((self.focus + 1).saturating_sub(budget))
    }

    pub(super) fn select(&mut self, focus: usize, count: usize, budget: usize) {
        self.focus = focus;
        menu::reveal(&mut self.focus, &mut self.top, count, budget);
    }

    pub(super) fn reset_focus(&mut self) {
        self.focus = 0;
        self.top = 0;
    }

    pub(super) fn update(
        &mut self,
        key: Key,
        cols: u16,
        budget: usize,
        timer: &str,
        detected: Option<&Detected>,
    ) {
        let before = self.spec();
        let rows = self.rows(cols, timer, detected);
        let field = rows.get(self.focus).and_then(|row| row.field);
        let step = match key {
            Key::Up | Key::BackTab => Some(-1),
            Key::Down | Key::Tab => Some(1),
            Key::PageUp => Some(-(budget as isize)),
            Key::PageDown => Some(budget as isize),
            _ => None,
        };
        if let Some(delta) = step {
            self.select(self.focus.saturating_add_signed(delta), rows.len(), budget);
            return;
        }
        match field {
            Some(Field::Advanced) if matches!(key, Key::Enter | Key::Text(' ')) => {
                self.advanced = !self.advanced
            }
            Some(Field::ConvertZone) if matches!(key, Key::Enter | Key::Text(' ')) => {
                self.convert_zone = true;
                self.focus = 0;
                self.top = 0;
            }
            Some(Field::Fallback(agent))
                if matches!(key, Key::Enter | Key::Text(' ' | '-' | '+')) =>
            {
                self.fallback(agent, key)
            }
            _ if key == Key::Enter => {
                self.select(self.focus + 1, rows.len(), budget);
                return;
            }
            Some(field) => self.edit_field(field, key),
            None => {}
        }
        self.sync_preview(&before);
        let rows = self.rows(cols, timer, detected);
        if let Some(field) = field
            && let Some(index) = rows.iter().position(|row| row.field == Some(field))
        {
            self.focus = index;
        }
        self.select(self.focus, rows.len(), budget);
    }

    pub(super) fn confirm_zone(&mut self, cols: u16, timer: &str, detected: Option<&Detected>) {
        let before = self.spec();
        self.choices.calendar.time_zone = "host".into();
        self.convert_zone = false;
        self.sync_preview(&before);
        self.focus = self
            .rows(cols, timer, detected)
            .iter()
            .position(|row| row.field == Some(Field::Zone))
            .unwrap_or(0);
        self.top = 0;
    }

    fn edit_field(&mut self, field: Field, key: Key) {
        let editor = match field {
            Field::Name => Some(&mut self.name),
            Field::Prompt => Some(&mut self.prompt),
            Field::Directory => Some(&mut self.directory),
            Field::Time => Some(&mut self.time),
            Field::Date => Some(&mut self.date),
            Field::Cron => Some(&mut self.cron),
            _ => None,
        };
        if let Some(editor) = editor {
            match key {
                Key::AltEnter if field == Field::Prompt => {
                    editor.insert_text(std::iter::once('\n'))
                }
                Key::Paste(text) if field == Field::Prompt => editor.insert_text(
                    text.replace("\r\n", "\n")
                        .replace('\r', "\n")
                        .chars()
                        .filter(|ch| !ch.is_control() || matches!(ch, '\n' | '\t')),
                ),
                key => editor.apply(key),
            }
            return;
        }
        let delta = match key {
            Key::Left => -1,
            Key::Right | Key::Text(' ') => 1,
            _ => return,
        };
        match field {
            Field::Cadence => cycle(&mut self.choices.calendar.kind, CADENCES, delta),
            Field::Day => cycle(&mut self.choices.calendar.weekday, DAYS, delta),
            Field::Authority => cycle(&mut self.choices.authority, AUTHORITIES, delta),
            Field::Workspace => cycle(&mut self.choices.workspace, WORKSPACES, delta),
            Field::Overlap => self.choices.allow_overlap = !self.choices.allow_overlap,
            Field::Missed => self.missed = !self.missed,
            Field::Primary => {
                let prior = self.choices.agents.first().cloned().unwrap_or_default();
                let mut next = prior.clone();
                cycle(&mut next, HARNESSES, delta);
                self.choices
                    .agents
                    .retain(|agent| *agent != prior && *agent != next);
                self.choices.agents.insert(0, next);
            }
            _ => {}
        }
    }

    fn fallback(&mut self, agent: &str, key: Key) {
        let routes = &mut self.choices.agents;
        let position = routes.iter().position(|route| route == agent);
        match (key, position) {
            (Key::Enter | Key::Text(' '), Some(rank)) if rank > 0 => {
                routes.remove(rank);
            }
            (Key::Enter | Key::Text(' '), None) => routes.push(agent.into()),
            (Key::Text('-'), Some(rank)) if rank > 1 => routes.swap(rank, rank - 1),
            (Key::Text('+'), Some(rank)) if rank > 0 && rank + 1 < routes.len() => {
                routes.swap(rank, rank + 1)
            }
            _ => {}
        }
    }

    pub(super) fn rows(&self, cols: u16, timer: &str, detected: Option<&Detected>) -> Vec<Row> {
        let mut rows = Vec::new();
        let cal = &self.choices.calendar;
        let zone = if cal.time_zone == "host" {
            self.preview
                .as_ref()
                .filter(|(_, preview)| !preview.host_zone.is_empty())
                .map_or("Host wall time".into(), |(_, preview)| {
                    format!("Host wall time ({})", preview.host_zone)
                })
        } else {
            format!("{} (fixed)", cal.time_zone)
        };
        if self.convert_zone {
            information(&mut rows, cols, &format!("Current zone · {zone}"));
            information(&mut rows, cols, "New zone · Host wall time");
            information(
                &mut rows,
                cols,
                "The saved clock time stays the same. Its next firing can move when the zone changes.",
            );
            return rows;
        }
        text(
            &mut rows,
            self.focus,
            cols,
            Field::Name,
            "Job name",
            &self.name,
        );
        text(
            &mut rows,
            self.focus,
            cols,
            Field::Prompt,
            "Task instructions",
            &self.prompt,
        );
        text(
            &mut rows,
            self.focus,
            cols,
            Field::Directory,
            "Working directory",
            &self.directory,
        );
        if let Some((_, preview)) = &self.preview
            && let Ok(path) = &preview.directory
            && path != self.directory.text.trim()
        {
            information(&mut rows, cols, &format!("Resolved · {path}"));
        }
        choice(
            &mut rows,
            Field::Cadence,
            "Cadence",
            label(&cal.kind, CADENCES),
            "",
        );
        if cal.kind == "weekly" {
            choice(&mut rows, Field::Day, "Day", label(&cal.weekday, DAYS), "");
        }
        if cal.kind == "once" {
            text(
                &mut rows,
                self.focus,
                cols,
                Field::Date,
                "Date (YYYY-MM-DD)",
                &self.date,
            );
        }
        if cal.kind == "cron" {
            text(
                &mut rows,
                self.focus,
                cols,
                Field::Cron,
                "Five-field cron",
                &self.cron,
            );
        } else {
            text(
                &mut rows,
                self.focus,
                cols,
                Field::Time,
                "Time (HH:MM)",
                &self.time,
            );
        }
        rows.push(Row::field(Field::Zone, format!("Time zone · {zone}")));
        if cal.time_zone != "host" {
            rows.push(Row::field(Field::ConvertZone, "Use host wall time…"));
        }
        let firing = self
            .preview
            .as_ref()
            .map_or("Calculating…", |(_, preview)| preview.firing.as_str());
        information(&mut rows, cols, &format!("Next firing · {firing}"));
        let primary = self.choices.agents.first().map_or("", String::as_str);
        choice(
            &mut rows,
            Field::Primary,
            "Primary harness",
            label(primary, HARNESSES),
            &harness_hint(primary, detected),
        );
        choice(
            &mut rows,
            Field::Authority,
            "Authority",
            label(&self.choices.authority, AUTHORITIES),
            "",
        );
        information(
            &mut rows,
            cols,
            match self.choices.authority.as_str() {
                "observe" => "Read-only filesystem. Network disabled.",
                "workspace-write" => "Writes allowed in the workspace. Network disabled.",
                _ => "Filesystem and network access follow the harness configuration.",
            },
        );
        information(&mut rows, cols, &format!("Timer · {timer}"));
        if let Some((_, preview)) = &self.preview {
            for agent in &self.choices.agents {
                if let Err(error) = check_route_authority(agent, &self.choices.authority) {
                    information(&mut rows, cols, &format!("Needs attention · {error}"));
                }
            }
            match &preview.directory {
                Err(error) => information(&mut rows, cols, &format!("Needs attention · {error}")),
                Ok(_) => {
                    for agent in &self.choices.agents {
                        if check_route_authority(agent, &self.choices.authority).is_ok()
                            && matches!(
                                provider(agent, detected),
                                Some(Detection::Disabled {
                                    installed: false,
                                    ..
                                })
                            )
                        {
                            information(
                                &mut rows,
                                cols,
                                &format!(
                                    "Needs attention · {agent} · install {agent} on the executor and add it to the executor PATH"
                                ),
                            );
                        }
                    }
                }
            }
        } else {
            information(&mut rows, cols, "Checking settings…");
        }
        let advanced = if self.advanced {
            "▾ Advanced".into()
        } else {
            format!(
                "▸ Advanced · {}",
                label(&self.choices.workspace, WORKSPACES)
            )
        };
        rows.push(Row::field(Field::Advanced, advanced));
        if self.advanced {
            choice(
                &mut rows,
                Field::Workspace,
                "Workspace",
                label(&self.choices.workspace, WORKSPACES),
                "",
            );
            choice(
                &mut rows,
                Field::Overlap,
                "Overlap",
                label(
                    if self.choices.allow_overlap {
                        "true"
                    } else {
                        "false"
                    },
                    OVERLAP,
                ),
                "",
            );
            let missed = match self.grace() {
                0 => MISSED[0].1.into(),
                HOUR => MISSED[1].1.into(),
                grace => format!("Catch up the latest within {}", duration_label(grace)),
            };
            choice(&mut rows, Field::Missed, "Missed runs", &missed, "");
            for (rank, agent) in self.choices.agents.iter().enumerate().skip(1) {
                if let Some((agent, name)) = HARNESSES.iter().find(|(route, _)| *route == agent) {
                    rows.push(Row::field(
                        Field::Fallback(agent),
                        format!("Fallback {rank} · {name}{}", harness_hint(agent, detected)),
                    ));
                }
            }
            for &(agent, name) in HARNESSES {
                if !self.choices.agents.iter().any(|route| route == agent) {
                    rows.push(Row::field(
                        Field::Fallback(agent),
                        format!("+ Add {name} fallback{}", harness_hint(agent, detected)),
                    ));
                }
            }
        }
        let (enable, draft) = self.labels();
        rows.push(Row::field(Field::SaveEnable, enable));
        rows.push(Row::field(Field::SaveDraft, draft));
        rows
    }
}

pub(super) fn default_spec() -> Spec {
    Spec {
        authority: "observe".into(),
        workspace: "directory".into(),
        agents: vec!["codex".into()],
        calendar: Calendar {
            kind: "daily".into(),
            time_zone: "host".into(),
            time: "09:00".into(),
            ..Calendar::default()
        },
        ..Spec::default()
    }
}

fn cycle(value: &mut String, choices: &[(&str, &str)], delta: isize) {
    let count = choices.len() as isize;
    let index = choices
        .iter()
        .position(|(candidate, _)| *candidate == value)
        .map_or(if delta < 0 { 0 } else { count - 1 }, |index| {
            index as isize
        });
    *value = choices[(index + delta).rem_euclid(count) as usize].0.into();
}

fn label<'a>(value: &'a str, choices: &'a [(&str, &str)]) -> &'a str {
    choices.iter().find(|(key, _)| *key == value).map_or(
        if value.is_empty() { "Choose" } else { value },
        |(_, label)| label,
    )
}

fn provider<'a>(provider: &str, detected: Option<&'a Detected>) -> Option<&'a Detection> {
    let detected = detected?;
    PROVIDERS
        .iter()
        .position(|agent| *agent == provider)
        .map(|index| &detected[index])
}

fn duration_label(nanos: i64) -> String {
    fn decimal(value: i64, unit: i64, digits: usize, suffix: &str) -> String {
        let fraction = format!("{:0digits$}", value % unit);
        let fraction = fraction.trim_end_matches('0');
        if fraction.is_empty() {
            format!("{}{suffix}", value / unit)
        } else {
            format!("{}.{fraction}{suffix}", value / unit)
        }
    }
    if nanos < 1_000 {
        return format!("{nanos}ns");
    }
    if nanos < 1_000_000 {
        return decimal(nanos, 1_000, 3, "µs");
    }
    if nanos < 1_000_000_000 {
        return decimal(nanos, 1_000_000, 6, "ms");
    }
    let minutes = nanos / 60_000_000_000;
    let seconds = decimal(nanos % 60_000_000_000, 1_000_000_000, 9, "s");
    if minutes >= 60 {
        format!("{}h{}m{seconds}", minutes / 60, minutes % 60)
    } else if minutes > 0 {
        format!("{minutes}m{seconds}")
    } else {
        seconds
    }
}

fn harness_hint(provider: &str, detected: Option<&Detected>) -> String {
    match self::provider(provider, detected) {
        Some(Detection::Disabled {
            reason,
            installed: true,
        }) if !reason.is_empty() => format!(" · {reason}"),
        Some(Detection::Disabled { .. }) => {
            format!(" · Install or update {}", label(provider, HARNESSES))
        }
        _ => String::new(),
    }
}

fn information(rows: &mut Vec<Row>, cols: u16, value: &str) {
    rows.extend(
        menu::wrap(value, cols.saturating_sub(2))
            .into_iter()
            .map(|text| Row { field: None, text }),
    );
}

fn choice(rows: &mut Vec<Row>, field: Field, caption: &str, value: &str, hint: &str) {
    rows.push(Row::field(field, format!("{caption} · ‹ {value} ›{hint}")));
}

fn text(
    rows: &mut Vec<Row>,
    focus: usize,
    cols: u16,
    field: Field,
    caption: &str,
    editor: &Editor,
) {
    let prefix = format!("{caption} · ");
    let transform = |text: &str| {
        text.chars()
            .filter(|ch| *ch != '\r')
            .map(|ch| match ch {
                '\n' => '↵',
                '\t' => '⇥',
                _ => terminal_char(ch),
            })
            .collect::<String>()
    };
    let value = if rows.len() == focus {
        let before = transform(&editor.text[..editor.cursor]);
        let after = transform(&editor.text[editor.cursor..]);
        let cursor = before.len();
        let display = Editor {
            text: before + &after,
            cursor,
        };
        block_cursor(
            &display,
            cols.saturating_sub(2 + prefix.chars().count() as u16)
                .max(1),
        )
    } else {
        transform(&editor.text)
    };
    rows.push(Row::field(field, prefix + &value));
}
