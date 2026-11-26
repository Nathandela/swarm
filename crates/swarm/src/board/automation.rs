use super::{Action, Editor, Key, capacity, form_header, line, usable};
use crate::schedule::catalog::{Change, Manual, Read, Records, RunState, State};
use crate::schedule::{
    Calendar, Job, Occurrence, Spec, automation_duration, automation_invocation,
};

#[derive(Default)]
pub(super) struct Automation {
    records: Option<Records>,
    error: String,
    selected: usize,
    job: String,
    draft: Option<Draft>,
    archive: Option<Change>,
    run: Option<Occurrence>,
    manual: Option<RunState>,
}

impl Automation {
    pub(super) fn in_directory(directory: &str) -> Self {
        let mut draft = Draft::new(None);
        draft.values[1] = Editor::new(directory);
        Self {
            draft: Some(draft),
            ..Self::default()
        }
    }

    pub(super) fn receive(&mut self, response: std::io::Result<Records>) {
        self.error.clear();
        match response {
            Ok(records) => {
                if let Records::Get(job) = &records {
                    self.job.clone_from(&job.id);
                }
                self.manual = None;
                self.records = Some(records);
                self.run = None;
                self.selected = 0;
            }
            Err(error) => self.error = error.to_string(),
        }
    }

    pub(super) fn changed(&mut self, response: std::io::Result<Records>) {
        self.error.clear();
        match response {
            Ok(records) => {
                self.draft = None;
                self.archive = None;
                self.receive(Ok(records));
            }
            Err(error) => self.error = error.to_string(),
        }
    }

    pub(super) fn triggered(&mut self, response: std::io::Result<Option<RunState>>) {
        match response {
            Ok(run) => {
                self.error.clear();
                self.manual = run;
            }
            Err(error) => {
                self.manual = None;
                self.error = format!("Run status unconfirmed · {error}");
            }
        }
    }

    pub(super) fn cancel(&mut self) -> bool {
        let cancelled = self.draft.take().is_some()
            || self.archive.take().is_some()
            || self.run.take().is_some();
        if cancelled {
            self.error.clear();
        }
        cancelled
    }

    pub(super) fn update(&mut self, key: Key, geometry: (u16, u16)) -> Action {
        if !usable(geometry) {
            return Action::None;
        }
        if let Some(run) = &self.run {
            if key == Key::Left {
                self.run = None;
            } else if key == Key::Text('o')
                && let Some(id) = run.live_session()
            {
                return Action::Attach {
                    id: id.to_owned(),
                    name: "Automation run".into(),
                };
            }
            return Action::None;
        }
        if let Some(draft) = &mut self.draft {
            if key == Key::Enter {
                if geometry.0 < 32 || geometry.1 < 8 {
                    self.error = "Resize to 32×8 to save".into();
                } else {
                    match draft.change() {
                        Ok(change) => return Action::ChangeAutomation(change),
                        Err(error) => self.error = error,
                    }
                }
            } else {
                self.error.clear();
                draft.update(key);
            }
            return Action::None;
        }
        if let Some(change) = self.archive.take() {
            if matches!(key, Key::Enter | Key::Text('y')) && geometry.0 >= 32 && geometry.1 >= 8 {
                return Action::ChangeAutomation(change);
            }
            if key != Key::Text('n') {
                self.archive = Some(change);
            }
            return Action::None;
        }
        match (&self.records, key) {
            (Some(Records::List(_)), Key::Text('n')) => {
                self.draft = Some(Draft::new(None));
                Action::None
            }
            (Some(Records::Get(job)), Key::Text('e')) if job.state != "archived" => {
                self.draft = Some(Draft::new(Some(job)));
                Action::None
            }
            (Some(Records::Get(job)), key @ Key::Text('p' | 'x')) if job.state != "archived" => {
                let value = if key == Key::Text('x') {
                    State::Archived
                } else if job.state == "paused" {
                    State::Active
                } else {
                    State::Paused
                };
                let change = Change::State {
                    id: job.id.clone(),
                    revision: job.revision_id.clone(),
                    expected: job.state.clone(),
                    generation: job.lifecycle_generation,
                    updated_at: job.updated_at,
                    value,
                };
                if key == Key::Text('x') {
                    self.archive = Some(change);
                    Action::None
                } else if geometry.0 >= 32 && geometry.1 >= 8 {
                    Action::ChangeAutomation(change)
                } else {
                    self.error = "Resize to 32×8 to change automation state".into();
                    Action::None
                }
            }
            (Some(Records::Get(job)), key @ Key::Text('r' | 'R')) if job.state != "archived" => {
                if geometry.0 < 32 || geometry.1 < 8 {
                    self.error = "Resize to 32×8 to run an automation".into();
                    return Action::None;
                }
                let previous = if key == Key::Text('R') {
                    let Some(known) = self.manual.as_ref().filter(|run| !run.pending) else {
                        self.error = "Resolve the current run before starting another".into();
                        return Action::None;
                    };
                    Some(known.id.clone())
                } else {
                    None
                };
                Action::TriggerAutomation {
                    job: job.id.clone(),
                    intent: Manual::Run { previous },
                }
            }
            (_, Key::Text('u')) => Action::Catalog(Read::List),
            (Some(Records::List(jobs)), Key::Enter) if !jobs.is_empty() => {
                self.job = jobs[self.selected.min(jobs.len() - 1)].id.clone();
                Action::Catalog(Read::Get {
                    id: self.job.clone(),
                })
            }
            (Some(Records::Get(job)), Key::Text('h')) => {
                Action::Catalog(Read::History { id: job.id.clone() })
            }
            (Some(Records::History(runs)), Key::Enter) if !runs.is_empty() => {
                self.run = runs.get(self.selected).cloned();
                Action::None
            }
            (Some(Records::History(_)), Key::Left) => Action::Catalog(Read::Get {
                id: self.job.clone(),
            }),
            (Some(Records::Get(_)), Key::Left) => Action::Catalog(Read::List),
            (records, key) => {
                let count = match records {
                    Some(Records::List(items)) => items.len(),
                    Some(Records::History(items)) => items.len(),
                    _ => 0,
                };
                let step = capacity(geometry.1);
                self.selected = match key {
                    Key::Up | Key::Text('k') => self.selected.saturating_sub(1),
                    Key::Down | Key::Text('j') => self.selected.saturating_add(1),
                    Key::PageUp => self.selected.saturating_sub(step),
                    Key::PageDown => self.selected.saturating_add(step),
                    Key::Home => 0,
                    Key::End => count.saturating_sub(1),
                    _ => self.selected,
                }
                .min(count.saturating_sub(1));
                Action::None
            }
        }
    }

    pub(super) fn view(&self, output: &mut String, cols: u16, rows: u16) {
        if let Some(run) = &self.run {
            form_header(output, cols, "Automation run");
            for (index, text) in [
                format!("Run · {}", run.id),
                format!("Status · {}", run.state),
                format!("Due · {}", run.due_at),
                run.detail.clone(),
            ]
            .into_iter()
            .take(usize::from(rows.saturating_sub(2)))
            .enumerate()
            {
                line(output, index as u16 + 2, cols, "", &text);
            }
            let hint = if run.live_session().is_some() {
                "o open session · ← history · esc back"
            } else {
                "← history · esc back"
            };
            line(output, rows, cols, "\x1b[2m", hint);
            return;
        }
        if let Some(draft) = &self.draft {
            draft.view(output, cols, rows);
            if !self.error.is_empty() {
                line(
                    output,
                    rows.saturating_sub(1),
                    cols,
                    "\x1b[33m",
                    &self.error,
                );
            }
            return;
        }
        if self.archive.is_some() {
            form_header(output, cols, "Archive automation?");
            if let Some(Records::Get(job)) = &self.records {
                line(output, 2, cols, "", job.name());
            }
            line(
                output,
                rows,
                cols,
                "\x1b[2m",
                "y / enter archive · n / esc cancel",
            );
            return;
        }
        form_header(output, cols, "Automations");
        let (items, hint) = match &self.records {
            Some(Records::List(jobs)) if jobs.is_empty() => (
                vec!["No automations".into()],
                "n new · u refresh · esc back",
            ),
            Some(Records::List(jobs)) => (
                jobs.iter()
                    .map(|job| format!("{} · {}", job.name(), job.state))
                    .collect(),
                "↑↓ select · enter detail · n new · u refresh · esc back",
            ),
            Some(Records::Get(job)) => (
                vec![
                    job.name().to_owned(),
                    format!("Status · {}", job.state),
                    format!("Directory · {}", job.directory()),
                    format!("Agents · {}", job.agents().join(" → ")),
                ],
                if job.state == "archived" {
                    "h history · ← list · esc back"
                } else {
                    match &self.manual {
                        Some(run) if run.pending => {
                            "r reconcile · e edit · p pause/resume · h history · x archive"
                        }
                        Some(_) => {
                            "R new run · r receipt · e edit · p pause/resume · h history · x archive"
                        }
                        None => {
                            "e edit · p pause/resume · r run · h history · x archive · esc back"
                        }
                    }
                },
            ),
            Some(Records::History(runs)) if runs.is_empty() => (
                vec!["No runs for this automation".into()],
                "← detail · esc back",
            ),
            Some(Records::History(runs)) => (
                runs.iter()
                    .map(|run| format!("{} · {} · {}", run.due_at, run.state, run.detail))
                    .collect(),
                "↑↓ select · enter run · ← detail · esc back",
            ),
            None if self.error.is_empty() => {
                (vec!["Reading the automation catalog".into()], "esc back")
            }
            None => (Vec::new(), "u retry · esc back"),
        };
        let capacity = capacity(rows);
        let start = (self.selected + 1).saturating_sub(capacity);
        for (index, text) in items.iter().enumerate().skip(start).take(capacity) {
            let marker = if index == self.selected { "▌" } else { " " };
            line(
                output,
                (index - start) as u16 + 2,
                cols,
                "",
                &format!("{marker} {text}"),
            );
        }
        if !self.error.is_empty() {
            line(output, 2, cols, "\x1b[33m", &self.error);
        }
        if let Some(run) = &self.manual
            && rows > 3
        {
            let label = if run.pending {
                "Run pending"
            } else {
                "Run recorded"
            };
            line(
                output,
                rows - 1,
                cols,
                "\x1b[2m",
                &format!("{label} · {} · {}", run.id, run.state),
            );
        }
        line(output, rows, cols, "\x1b[2m", hint);
    }
}

const FIELDS: [&str; 15] = [
    "Name",
    "Directory",
    "Prompt",
    "Agents",
    "Authority",
    "Workspace",
    "Calendar",
    "Timezone",
    "Time",
    "Date",
    "Weekday",
    "Cron expression",
    "Catch-up duration",
    "Allow overlap",
    "Save",
];

struct Draft {
    target: Option<(String, String)>,
    operation: Option<String>,
    values: Vec<Editor>,
    focus: usize,
}

impl Draft {
    fn new(job: Option<&Job>) -> Self {
        let initial = job.map_or_else(default_spec, |job| job.spec.clone());
        let cal = initial.calendar;
        let values = [
            initial.name,
            initial.directory,
            initial.prompt,
            initial.agents.join(","),
            initial.authority,
            initial.workspace,
            cal.kind,
            cal.time_zone,
            cal.time,
            cal.date,
            cal.weekday,
            cal.cron,
            format!("{}ns", initial.grace),
            initial.allow_overlap.to_string(),
        ];
        Self {
            target: job.map(|job| (job.id.clone(), job.revision_id.clone())),
            operation: None,
            values: values.iter().map(|value| Editor::new(value)).collect(),
            focus: 0,
        }
    }

    fn update(&mut self, key: Key) {
        match key {
            Key::Up => self.focus = (self.focus + FIELDS.len() - 1) % FIELDS.len(),
            Key::Down | Key::Tab => self.focus = (self.focus + 1) % FIELDS.len(),
            Key::Paste(text) if self.focus == 2 => {
                self.values[2].insert_text(
                    text.chars()
                        .filter(|ch| !ch.is_control() || matches!(ch, '\n' | '\r' | '\t')),
                );
            }
            key => {
                if let Some(editor) = self.values.get_mut(self.focus) {
                    editor.apply(key);
                }
            }
        }
    }

    fn change(&mut self) -> Result<Change, String> {
        let get = |index: usize| self.values[index].text.trim().to_owned();
        let kind = get(6);
        let (time, date, weekday, cron) = match kind.as_str() {
            "cron" => (String::new(), String::new(), String::new(), get(11)),
            "once" => (get(8), get(9), String::new(), String::new()),
            "weekly" => (get(8), String::new(), get(10), String::new()),
            "daily" | "weekdays" => (get(8), String::new(), String::new(), String::new()),
            _ => return Err("Calendar requires once, daily, weekdays, weekly, or cron".into()),
        };
        let spec = Box::new(Spec {
            name: get(0),
            directory: get(1),
            prompt: self.values[2].text.clone(),
            agents: self.values[3]
                .text
                .split(',')
                .map(str::trim)
                .filter(|value| !value.is_empty())
                .map(str::to_owned)
                .collect(),
            authority: get(4),
            workspace: get(5),
            calendar: Calendar {
                kind,
                time_zone: get(7),
                time,
                date,
                weekday,
                cron,
            },
            grace: automation_duration(&get(12)).map_err(|error| error.to_string())?,
            allow_overlap: get(13)
                .parse()
                .map_err(|_| "Allow overlap requires true or false")?,
        });
        Ok(match &self.target {
            Some((id, revision)) => Change::Edit {
                id: id.clone(),
                revision: revision.clone(),
                spec,
            },
            None => {
                if self.operation.is_none() {
                    self.operation =
                        Some(automation_invocation().map_err(|error| error.to_string())?);
                }
                Change::Create {
                    operation: self.operation.clone().unwrap(),
                    spec,
                }
            }
        })
    }

    fn view(&self, output: &mut String, cols: u16, rows: u16) {
        form_header(
            output,
            cols,
            if self.target.is_some() {
                "Edit automation"
            } else {
                "New automation"
            },
        );
        let window = capacity(rows).saturating_sub(1).max(1);
        let start = (self.focus + 1).saturating_sub(window);
        for (index, label) in FIELDS.iter().enumerate().skip(start).take(window) {
            let marker = if index == self.focus { "▌" } else { " " };
            let value = self
                .values
                .get(index)
                .map(|editor| editor.text.as_str())
                .unwrap_or_else(|| {
                    if self.target.is_some() {
                        "Save revision"
                    } else {
                        "Save paused draft"
                    }
                });
            line(
                output,
                (index - start) as u16 + 2,
                cols,
                "",
                &format!("{marker} {label}  {value}"),
            );
        }
        line(
            output,
            rows,
            cols,
            "\x1b[2m",
            "↑↓ field · enter save · esc cancel",
        );
    }
}

fn default_spec() -> Spec {
    Spec {
        directory: std::env::current_dir()
            .unwrap_or_default()
            .to_string_lossy()
            .into(),
        authority: "agent-default".into(),
        workspace: "directory".into(),
        agents: vec!["codex".into()],
        calendar: Calendar {
            kind: "daily".into(),
            time_zone: "UTC".into(),
            time: "09:00".into(),
            ..Calendar::default()
        },
        ..Spec::default()
    }
}
