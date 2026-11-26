use super::launch::{ACCENT, PLAIN, spans};
use super::navigation::truncation;
use super::{Action, Editor, Key, Mouse, MouseAction, form_header, line, terminal_char, usable};
use crate::schedule::catalog::{Change, Manual, Read, Records, RunState, State};
use crate::schedule::{
    Calendar, Job, Occurrence, Spec, automation_duration, automation_install_timer,
    automation_invocation, automation_timer_status,
};
use std::{
    fmt::Write as _,
    io,
    path::Path,
    sync::{
        Arc,
        atomic::{AtomicBool, Ordering},
        mpsc,
    },
    thread::{self, JoinHandle},
};
use unicode_segmentation::UnicodeSegmentation;
use unicode_width::UnicodeWidthStr;

#[derive(Default)]
pub(super) struct TimerStatusProbe {
    generation: u64,
    pending: Option<StatusRead>,
}

struct StatusRead {
    generation: u64,
    result: mpsc::Receiver<String>,
    cancel: Arc<AtomicBool>,
    worker: Option<JoinHandle<()>>,
}

impl Drop for StatusRead {
    fn drop(&mut self) {
        self.cancel.store(true, Ordering::Relaxed);
        if let Some(worker) = self.worker.take() {
            let _ = worker.join();
        }
    }
}

impl TimerStatusProbe {
    pub(super) fn refresh(&mut self) -> u64 {
        self.generation = self.generation.wrapping_add(1);
        if self.pending.is_none() {
            self.start();
        }
        self.generation
    }

    fn start(&mut self) {
        let (send, result) = mpsc::channel();
        let cancel = Arc::new(AtomicBool::new(false));
        let worker_cancel = Arc::clone(&cancel);
        let worker = thread::spawn(move || {
            let _ = send.send(automation_timer_status(&worker_cancel));
        });
        self.pending = Some(StatusRead {
            generation: self.generation,
            result,
            cancel,
            worker: Some(worker),
        });
    }

    pub(super) fn poll(&mut self) -> Option<(u64, String)> {
        let pending = self.pending.as_ref()?;
        let status = match pending.result.try_recv() {
            Ok(status) => status,
            Err(mpsc::TryRecvError::Empty) => return None,
            Err(mpsc::TryRecvError::Disconnected) => "status unavailable".into(),
        };
        let generation = pending.generation;
        self.pending = None;
        if generation != self.generation {
            // Refreshes can supersede a slow manager read without starting more children.
            self.start();
            return None;
        }
        Some((generation, status))
    }
}

#[derive(Default)]
pub(super) struct Automation {
    records: Option<Records>,
    error: String,
    selected: usize,
    detail_top: usize,
    archived: bool,
    timer_request: Option<u64>,
    timer_status: String,
    timer_owner: Option<String>,
    timer_prompt: Option<TimerPrompt>,
    timer_installing: Option<(String, mpsc::Receiver<io::Result<()>>)>,
    job: String,
    draft: Option<Draft>,
    archive: Option<ArchivePrompt>,
    enable_confirmation: bool,
    enabling: Option<mpsc::Receiver<io::Result<Option<Records>>>>,
    run: Option<Occurrence>,
    manual: Option<RunState>,
    press: Option<(Mouse, (u16, u16), PointerTarget)>,
}

struct TimerPrompt {
    endpoint: String,
    status: String,
    painted: Option<(u16, u16)>,
}

struct ArchivePrompt {
    name: String,
    change: Change,
    painted: Option<(u16, u16)>,
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum PointerTarget {
    Button(char),
    Row(usize),
}

struct Button {
    text: String,
    key: char,
    x: u16,
    y: u16,
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
        self.press = None;
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
                self.detail_top = 0;
            }
            Err(error) => {
                self.disconnect_timer_owner();
                self.error = error.to_string();
            }
        }
    }

    pub(super) fn expect_timer_status(&mut self, generation: u64, owner: Option<String>) {
        self.timer_request = Some(generation);
        self.timer_owner = owner;
    }

    pub(super) fn receive_timer_status(&mut self, generation: u64, status: String) -> bool {
        if self.timer_request != Some(generation)
            || !matches!(self.records, Some(Records::List(_) | Records::Get(_)))
        {
            return false;
        }
        self.timer_request = None;
        self.timer_status = status;
        true
    }

    fn can_offer_timer(&self) -> bool {
        self.timer_owner.is_some()
            && self.timer_request.is_none()
            && !self.timer_status.is_empty()
            && self.timer_status != "installed, active"
    }

    pub(super) fn disconnect_timer_owner(&mut self) {
        self.timer_owner = None;
        if self.timer_prompt.take().is_some() {
            self.press = None;
            self.selected = 0;
        }
    }

    pub(super) fn install_timer(&mut self, path: &Path, endpoint: String, status: String) {
        let (send, receive) = mpsc::channel();
        self.timer_installing = Some((endpoint.clone(), receive));
        self.timer_owner = None;
        self.error.clear();
        let path = path.to_owned();
        thread::spawn(move || {
            let _ = send.send(automation_install_timer(&path, &endpoint, &status));
        });
    }

    pub(super) fn poll_install_timer(&mut self) -> Option<(String, io::Result<()>)> {
        let (_, receive) = self.timer_installing.as_ref()?;
        let result = match receive.try_recv() {
            Ok(result) => result,
            Err(mpsc::TryRecvError::Empty) => return None,
            Err(mpsc::TryRecvError::Disconnected) => {
                Err(io::Error::other("timer status unconfirmed"))
            }
        };
        let (endpoint, _) = self.timer_installing.take()?;
        Some((endpoint, result))
    }

    pub(super) fn timer_install_error(&mut self, error: impl ToString) {
        self.error = error.to_string();
    }

    pub(super) fn changed(&mut self, response: std::io::Result<Records>) {
        self.press = None;
        self.error.clear();
        match response {
            Ok(records) => {
                self.draft = None;
                self.archive = None;
                self.enable_confirmation = false;
                self.receive(Ok(records));
            }
            Err(error) => self.error = error.to_string(),
        }
    }

    pub(super) fn enable(&mut self, path: &Path, endpoint: &str, change: Change) {
        let (send, receive) = mpsc::channel();
        self.enabling = Some(receive);
        self.error.clear();
        let path = path.to_owned();
        let endpoint = endpoint.to_owned();
        std::thread::spawn(move || {
            let result = (|| {
                let mut client =
                    crate::ipc::Client::dial(&path, &[crate::schedule::catalog::CONTROL])?;
                if client.endpoint() != endpoint {
                    return Err(io::Error::other(
                        "automation owner changed before activation",
                    ));
                }
                change.remote(&mut client)
            })();
            let _ = send.send(result);
        });
    }

    pub(super) fn poll_enable(&mut self) -> Option<io::Result<Option<Records>>> {
        let result = match self.enabling.as_ref()?.try_recv() {
            Ok(result) => result,
            Err(mpsc::TryRecvError::Empty) => return None,
            Err(mpsc::TryRecvError::Disconnected) => {
                Err(io::Error::other("automation activation status unconfirmed"))
            }
        };
        self.enabling = None;
        Some(result)
    }

    pub(super) fn confirm_enable_timer(&mut self) {
        self.enable_confirmation = true;
        self.error.clear();
    }

    pub(super) fn triggered(&mut self, response: std::io::Result<Option<RunState>>) {
        self.press = None;
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

    pub(super) fn confirmation_pending(&self) -> bool {
        self.archive.is_some() || self.timer_prompt.is_some()
    }

    pub(super) fn note_confirmation_paint(&mut self, geometry: (u16, u16)) {
        if let Some(archive) = &mut self.archive {
            archive.painted = Some(geometry);
        }
        if let Some(prompt) = &mut self.timer_prompt {
            prompt.painted = Some(geometry);
        }
    }

    pub(super) fn back(&mut self, online: bool) -> Option<Action> {
        self.press = None;
        if self.enabling.is_some() || self.timer_installing.is_some() {
            return Some(Action::None);
        }
        if self.timer_prompt.take().is_some() {
            self.selected = 0;
            return Some(Action::None);
        }
        if self.enable_confirmation {
            self.enable_confirmation = false;
            self.error.clear();
            return Some(Action::None);
        }
        if self.draft.take().is_some() || self.archive.take().is_some() || self.run.take().is_some()
        {
            self.error.clear();
            return Some(Action::None);
        }
        if online && let Some(Records::Get(job)) = &self.records {
            return Some(self.list(job.state == "archived"));
        }
        None
    }

    fn list(&mut self, archived: bool) -> Action {
        self.archived = archived;
        self.selected = 0;
        Action::Catalog(Read::List)
    }

    fn visible_jobs(&self) -> impl Iterator<Item = &Job> {
        let jobs = match &self.records {
            Some(Records::List(jobs)) => jobs.as_slice(),
            _ => &[],
        };
        jobs.iter()
            .filter(|job| (job.state == "archived") == self.archived)
    }

    fn buttons(&self, (cols, rows): (u16, u16)) -> Vec<Button> {
        let list_controls = [
            ("New", 'n'),
            ("Enable timer", 'i'),
            (if self.archived { "Active" } else { "Archived" }, 'v'),
            ("Refresh", 'r'),
            ("Open", '⏎'),
            ("Back", '⎋'),
        ];
        let controls: &[(&str, char)] = if self.enabling.is_some()
            || self.timer_installing.is_some()
        {
            &[]
        } else if self.timer_prompt.is_some() {
            &[("Enable timer", 'y'), ("Cancel", '⎋')]
        } else if let Some(run) = &self.run {
            if run.live_session().is_some() {
                &[("Open worker", 'o'), ("History", '←'), ("Back", '⎋')]
            } else {
                &[("History", '←'), ("Back", '⎋')]
            }
        } else if self.enable_confirmation {
            &[("Save and enable", 'y'), ("Cancel", '⎋')]
        } else if self
            .draft
            .as_ref()
            .is_some_and(|draft| draft.enable.is_some())
        {
            &[
                ("Save paused", '↡'),
                ("Save and enable", '↟'),
                ("Cancel", '⎋'),
            ]
        } else if self.draft.is_some() {
            &[("Save", '⏎'), ("Cancel", '⎋')]
        } else if self.archive.is_some() {
            &[("Archive job", 'y'), ("Cancel", '⎋')]
        } else {
            match &self.records {
                Some(Records::List(_)) => &list_controls,
                Some(Records::Get(job)) if job.state == "archived" => {
                    &[("History", 'h'), ("Back", '⎋')]
                }
                Some(Records::Get(_)) if self.manual.as_ref().is_some_and(|run| !run.pending) => &[
                    ("New run", 'R'),
                    ("Receipt", 'r'),
                    ("Edit", 'e'),
                    ("Pause/enable", 'p'),
                    ("History", 'h'),
                    ("Archive", 'x'),
                    ("List", '←'),
                    ("Back", '⎋'),
                ],
                Some(Records::Get(_)) if self.manual.as_ref().is_some_and(|run| run.pending) => &[
                    ("Reconcile", 'r'),
                    ("Edit", 'e'),
                    ("Pause/enable", 'p'),
                    ("History", 'h'),
                    ("Archive", 'x'),
                    ("List", '←'),
                    ("Back", '⎋'),
                ],
                Some(Records::Get(job)) if job.state == "paused" => &[
                    ("Edit", 'e'),
                    ("Enable…", 'p'),
                    ("Run now", 'r'),
                    ("History", 'h'),
                    ("Archive", 'x'),
                    ("Back", '⎋'),
                ],
                Some(Records::Get(_)) => &[
                    ("Edit", 'e'),
                    ("Pause", 'p'),
                    ("Run now", 'r'),
                    ("History", 'h'),
                    ("Archive", 'x'),
                    ("Back", '⎋'),
                ],
                Some(Records::History(_)) => &[("Open", '⏎'), ("Detail", '←'), ("Back", '⎋')],
                None => &[("Refresh", 'u'), ("Back", '⎋')],
            }
        };
        let compact = cols < 42 || rows < 8;
        let priority = |(_, key): &&(&str, char)| compact && *key == '⎋';
        let controls = controls
            .iter()
            .filter(priority)
            .chain(controls.iter().filter(|entry| !priority(entry)))
            .filter(|(_, key)| *key != 'i' || self.can_offer_timer());
        let mut buttons = Vec::new();
        let (mut x, mut y) = (1_u16, 0);
        for &(name, key) in controls {
            let shortcut = match key {
                '⎋' => "esc".into(),
                '↟' => "ctrl+s".into(),
                '↡' => "ctrl+d".into(),
                _ => key.to_string(),
            };
            let text = if compact {
                format!("[{shortcut}]")
            } else {
                format!("[{name} ({shortcut})]")
            };
            let width = text.width() as u16;
            if x + width - 1 > cols {
                x = 1;
                y += 1;
            }
            if y >= rows.saturating_sub(3).clamp(1, 3) {
                break;
            }
            buttons.push(Button { text, key, x, y });
            x += width + 1;
        }
        let first = rows.saturating_sub(buttons.last().map_or(0, |button| button.y));
        for button in &mut buttons {
            button.y += first;
        }
        buttons
    }

    fn body_capacity(&self, geometry: (u16, u16)) -> usize {
        let footer = self
            .buttons(geometry)
            .first()
            .map_or(geometry.1, |button| button.y);
        usize::from(footer.saturating_sub(2).max(1))
    }

    fn footer(&self, output: &mut String, cols: u16, rows: u16) {
        let buttons = self.buttons((cols, rows));
        for row in buttons.first().map_or(rows, |button| button.y)..=rows {
            let text = buttons
                .iter()
                .filter(|button| button.y == row)
                .map(|button| button.text.as_str())
                .collect::<Vec<_>>()
                .join(" ");
            line(output, row, cols, "", &text);
        }
    }

    fn rows_at(&self, geometry: (u16, u16)) -> (usize, usize, usize) {
        let budget = self.body_capacity(geometry);
        if self.enabling.is_some() || self.enable_confirmation || self.timer_installing.is_some() {
            return (0, 0, 0);
        }
        if self.timer_prompt.is_some() {
            return (
                (self.selected + 1).saturating_sub(budget),
                budget,
                timer_prompt_rows(geometry.0).count(),
            );
        }
        if let Some(draft) = &self.draft {
            let budget = budget.saturating_sub(1).max(1);
            return (
                (draft.focus + 1).saturating_sub(budget),
                budget,
                FIELDS.len(),
            );
        }
        let count = match &self.records {
            Some(Records::List(_)) => self.visible_jobs().count(),
            Some(Records::History(items)) => items.len(),
            Some(Records::Get(job)) => {
                let count = self.detail_rows(job, geometry.0).len();
                return (self.detail_start(count, budget), budget, count);
            }
            _ => 0,
        };
        ((self.selected + 1).saturating_sub(budget), budget, count)
    }

    fn target(&self, event: Mouse, geometry: (u16, u16)) -> Option<PointerTarget> {
        if !usable(geometry)
            || event.x == 0
            || event.x > geometry.0
            || event.y < 2
            || event.y > geometry.1
        {
            return None;
        }
        if let Some(button) = self.buttons(geometry).into_iter().find(|button| {
            event.y == button.y
                && (button.x..button.x + button.text.width() as u16).contains(&event.x)
        }) {
            return Some(PointerTarget::Button(button.key));
        }
        if self.run.is_some() || self.archive.is_some() || event.y == 2 && !self.error.is_empty() {
            return None;
        }
        let (start, budget, count) = self.rows_at(geometry);
        let offset = usize::from(event.y - 2);
        (offset < budget && start + offset < count).then_some(PointerTarget::Row(start + offset))
    }

    pub(super) fn mouse(&mut self, event: Mouse, geometry: (u16, u16)) -> Option<Key> {
        let target = self.target(event, geometry);
        match event.action {
            MouseAction::Drag => self.press = None,
            MouseAction::WheelUp | MouseAction::WheelDown => {
                self.press = None;
                if event.x > 0 && event.x <= geometry.0 && event.y > 0 && event.y <= geometry.1 {
                    return Some(if event.action == MouseAction::WheelUp {
                        Key::Up
                    } else {
                        Key::Down
                    });
                }
            }
            MouseAction::Press => {
                self.press = None;
                if let Some(target) = target {
                    let activate = match target {
                        PointerTarget::Button(_) => true,
                        PointerTarget::Row(index) => {
                            if let Some(draft) = &mut self.draft {
                                draft.focus = index;
                                index + 1 == FIELDS.len()
                            } else {
                                let activate = self.selected == index
                                    && matches!(
                                        self.records,
                                        Some(Records::List(_) | Records::History(_))
                                    );
                                self.selected = index;
                                activate
                            }
                        }
                    };
                    if activate {
                        self.press = Some((event, geometry, target));
                    }
                }
            }
            MouseAction::Release => {
                if let Some((press, size, prior)) = self.press.take()
                    && size == geometry
                    && press.x == event.x
                    && press.y == event.y
                    && Some(prior) == target
                {
                    return Some(match prior {
                        PointerTarget::Row(_) | PointerTarget::Button('⏎') => Key::Enter,
                        PointerTarget::Button('⎋') => Key::Escape,
                        PointerTarget::Button('←') => Key::Left,
                        PointerTarget::Button('↟') => Key::CtrlS,
                        PointerTarget::Button('↡') => Key::CtrlD,
                        PointerTarget::Button(key) => Key::Text(key),
                    });
                }
            }
        }
        None
    }

    pub(super) fn update(&mut self, key: Key, geometry: (u16, u16)) -> Action {
        self.press = None;
        if !usable(geometry) || self.enabling.is_some() || self.timer_installing.is_some() {
            return Action::None;
        }
        if let Some(prompt) = &self.timer_prompt {
            if key == Key::Text('y')
                && prompt.painted == Some(geometry)
                && geometry.0 >= 32
                && geometry.1 >= 8
            {
                let prompt = self
                    .timer_prompt
                    .take()
                    .expect("painted timer confirmation");
                self.selected = 0;
                return Action::InstallAutomationTimer {
                    endpoint: prompt.endpoint,
                    status: prompt.status,
                };
            }
            let step = self.body_capacity(geometry);
            let last = timer_prompt_rows(geometry.0).count().saturating_sub(1);
            self.selected = match key {
                Key::Up | Key::Text('k') => self.selected.saturating_sub(1),
                Key::Down | Key::Tab | Key::Text('j') => self.selected + 1,
                Key::PageUp => self.selected.saturating_sub(step),
                Key::PageDown => self.selected + step,
                Key::Home => 0,
                Key::End => last,
                _ => self.selected,
            }
            .min(last);
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
        if self.enable_confirmation && !matches!(key, Key::Text('y')) {
            return Action::None;
        }
        if let Some(draft) = &mut self.draft {
            let install_timer = self.enable_confirmation;
            if key == Key::Enter && (draft.enable.is_none() || draft.focus + 1 == FIELDS.len())
                || matches!(key, Key::CtrlS | Key::CtrlD) && draft.enable.is_some()
                || install_timer
            {
                if geometry.0 < 32 || geometry.1 < 8 {
                    self.error = "Resize to 32×8 to save".into();
                } else {
                    match draft
                        .change((key == Key::CtrlS || install_timer).then_some(install_timer))
                    {
                        Ok(change) => {
                            self.enable_confirmation = false;
                            return Action::ChangeAutomation(change);
                        }
                        Err(error) => self.error = error,
                    }
                }
            } else {
                self.error.clear();
                draft.update(key);
            }
            return Action::None;
        }
        if let Some(archive) = self.archive.take() {
            if key == Key::Text('y')
                && archive.painted == Some(geometry)
                && geometry.0 >= 32
                && geometry.1 >= 8
            {
                return Action::ChangeAutomation(archive.change);
            }
            self.archive = Some(archive);
            return Action::None;
        }
        match (&self.records, key) {
            (Some(Records::List(_)), Key::Text('i')) if self.can_offer_timer() => {
                self.timer_prompt = Some(TimerPrompt {
                    endpoint: self.timer_owner.clone().expect("qualified timer owner"),
                    status: self.timer_status.clone(),
                    painted: None,
                });
                self.selected = 0;
                self.error.clear();
                Action::None
            }
            (Some(Records::List(_)), Key::Text('n')) => {
                self.draft = Some(Draft::new(None));
                Action::None
            }
            (Some(Records::Get(job)), Key::Text('e')) if job.state != "archived" => {
                self.draft = Some(Draft::new(Some(job)));
                Action::None
            }
            (Some(Records::Get(job)), Key::Text('p')) if job.state == "paused" => {
                let mut draft = Draft::new(Some(job));
                draft.enable = Some((job.lifecycle_generation, job.updated_at));
                self.draft = Some(draft);
                Action::None
            }
            (Some(Records::Get(job)), key @ Key::Text('p' | 'x')) if job.state != "archived" => {
                let value = if key == Key::Text('x') {
                    State::Archived
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
                    self.archive = Some(ArchivePrompt {
                        name: job.name().to_owned(),
                        change,
                        painted: None,
                    });
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
            (Some(Records::List(_)), Key::Text('v')) => self.list(!self.archived),
            (Some(Records::List(_)), Key::Text('r')) => Action::Catalog(Read::List),
            (Some(Records::Get(job)), Key::Left | Key::Text('u')) => {
                self.list(job.state == "archived")
            }
            (_, Key::Text('u')) => Action::Catalog(Read::List),
            (Some(Records::List(_)), Key::Enter) => {
                let Some(id) = self
                    .visible_jobs()
                    .nth(self.selected)
                    .map(|job| job.id.clone())
                else {
                    return Action::None;
                };
                self.job.clone_from(&id);
                Action::Catalog(Read::Get { id })
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
            (records, key) => {
                let count = match records {
                    Some(Records::List(_)) => self.visible_jobs().count(),
                    Some(Records::History(items)) => items.len(),
                    Some(Records::Get(job)) => self.detail_rows(job, geometry.0).len(),
                    _ => 0,
                };
                let step = self.body_capacity(geometry);
                self.selected = match key {
                    Key::Up | Key::BackTab | Key::Text('k') => self.selected.saturating_sub(1),
                    Key::Down | Key::Text('j') => self.selected.saturating_add(1),
                    Key::Tab => self.selected.saturating_add(1),
                    Key::PageUp => self.selected.saturating_sub(step),
                    Key::PageDown => self.selected.saturating_add(step),
                    Key::Home => 0,
                    Key::End => count.saturating_sub(1),
                    _ => self.selected,
                }
                .min(count.saturating_sub(1));
                if matches!(records, Some(Records::Get(_))) {
                    self.detail_top = self.detail_start(count, step);
                }
                Action::None
            }
        }
    }

    fn list_title(&self, cols: u16) -> String {
        let title = if !self.archived {
            "Automations"
        } else if cols < 32 {
            "Archived"
        } else {
            "Automations / Archived"
        };
        if self.timer_status.is_empty() {
            title.into()
        } else if cols < 42 {
            let state = self
                .timer_status
                .split_once(", ")
                .map_or("unknown", |(_, state)| state);
            format!("{title} · {state}")
        } else {
            format!("{title} · Timer {}", self.timer_status)
        }
    }

    fn detail_rows(&self, job: &Job, cols: u16) -> Vec<String> {
        let mut lines = Vec::new();
        if !self.timer_status.is_empty() {
            lines.push(format!("Timer · {}", self.timer_status));
        }
        lines.extend([
            job.state.clone(),
            list_cadence(&job.spec.calendar),
            format!("Directory · {}", job.directory()),
            format!("Workspace · {}", job.spec.workspace),
            format!("Authority · {}", job.spec.authority),
            format!("Agent order · {}", job.agents().join(" → ")),
            String::new(),
        ]);
        lines.push(job.spec.prompt.clone());
        let width = usize::from(cols.saturating_sub(2).max(1));
        let mut rows = Vec::new();
        for line in lines.iter().flat_map(|text| text.split('\n')) {
            let mut cells = 0;
            let mut row = String::new();
            for cluster in line.graphemes(true) {
                if cluster == "\r" {
                    continue;
                }
                // The Go view expands tabs after measuring the logical row.
                let tab = cluster == "\t";
                let size = if tab { 0 } else { cluster.width() };
                if cells + size.max(usize::from(tab)) > width {
                    rows.push(std::mem::take(&mut row));
                    cells = 0;
                }
                if tab {
                    row.push_str("    ");
                } else {
                    row.extend(cluster.chars().map(terminal_char));
                }
                cells += size;
            }
            rows.push(row);
        }
        rows
    }

    fn detail_start(&self, count: usize, budget: usize) -> usize {
        let selected = self.selected.min(count.saturating_sub(1));
        self.detail_top
            .min(count.saturating_sub(budget))
            .min(selected)
            .max((selected + 1).saturating_sub(budget))
    }

    pub(super) fn view(&self, output: &mut String, cols: u16, rows: u16) {
        if self.timer_prompt.is_some() || self.timer_installing.is_some() {
            let title = if self.timer_installing.is_some() {
                "Enable automation timer? · Loading"
            } else {
                "Enable automation timer?"
            };
            line(output, 1, cols, ACCENT, &truncation(title, cols));
            let budget = self.body_capacity((cols, rows));
            let start = (self.selected + 1).saturating_sub(budget);
            for (index, text) in timer_prompt_rows(cols).enumerate().skip(start).take(budget) {
                write_list_row(
                    output,
                    (index - start) as u16 + 2,
                    cols,
                    text,
                    self.selected == index,
                );
            }
            self.footer(output, cols, rows);
            return;
        }
        if let Some(run) = &self.run {
            form_header(output, cols, "Automation run");
            for (index, text) in [
                format!("Run · {}", run.id),
                format!("Status · {}", run.state),
                format!("Due · {}", run.due_at),
                run.detail.clone(),
            ]
            .into_iter()
            .take(self.body_capacity((cols, rows)))
            .enumerate()
            {
                line(output, index as u16 + 2, cols, "", &text);
            }
            self.footer(output, cols, rows);
            return;
        }
        if self.enabling.is_some() {
            form_header(output, cols, "Checking automation activation…");
            return;
        }
        if self.enable_confirmation {
            form_header(output, cols, "Save and enable automation?");
            line(
                output,
                2,
                cols,
                "",
                "The per-user timer will be enabled on this host.",
            );
            self.footer(output, cols, rows);
            return;
        }
        if let Some(draft) = &self.draft {
            let budget = self.body_capacity((cols, rows));
            draft.view(output, cols, budget);
            if !self.error.is_empty() {
                line(output, budget as u16 + 1, cols, "\x1b[33m", &self.error);
            }
            self.footer(output, cols, rows);
            return;
        }
        if let Some(archive) = &self.archive {
            let name = archive.name.replace('\n', " ");
            let title = truncation(&format!("Archive {name}?"), cols);
            line(output, 1, cols, ACCENT, &title);
            let explanation = truncation(
                "Future triggers stop. Run history stays available in Archived.",
                cols.saturating_sub(2),
            );
            spans(
                output,
                2,
                cols,
                &[(ACCENT, "▌".into()), (PLAIN, format!(" {explanation}"))],
            );
            self.footer(output, cols, rows);
            return;
        }
        match &self.records {
            Some(Records::List(_)) => {
                let title = self.list_title(cols);
                line(output, 1, cols, ACCENT, &truncation(&title, cols));
            }
            Some(Records::Get(job)) => {
                let title = list_label(&format!("Automations / {}", job.name()), cols.into());
                let _ = write!(output, "\x1b[1;1H{ACCENT}{title}{PLAIN}\x1b[K");
            }
            _ => form_header(output, cols, "Automations"),
        }
        let items = match &self.records {
            Some(Records::List(_)) => {
                let mut items: Vec<_> = self
                    .visible_jobs()
                    .map(|job| {
                        format!(
                            "{} · {} · {}",
                            job.state,
                            job.name(),
                            list_cadence(&job.spec.calendar)
                        )
                    })
                    .collect();
                if items.is_empty() {
                    items.push("No jobs in this view.".into());
                }
                items
            }
            Some(Records::Get(job)) => self.detail_rows(job, cols),
            Some(Records::History(runs)) if runs.is_empty() => {
                vec!["No runs for this automation".into()]
            }
            Some(Records::History(runs)) => runs
                .iter()
                .map(|run| format!("{} · {} · {}", run.due_at, run.state, run.detail))
                .collect(),
            None if self.error.is_empty() => vec!["Reading the automation catalog".into()],
            None => Vec::new(),
        };
        let capacity = self.body_capacity((cols, rows));
        let detail = matches!(&self.records, Some(Records::Get(_)));
        let start = if detail {
            self.detail_start(items.len(), capacity)
        } else {
            (self.selected + 1).saturating_sub(capacity)
        };
        for (index, text) in items.iter().enumerate().skip(start).take(capacity) {
            let selected = index == self.selected.min(items.len().saturating_sub(1));
            let row = (index - start) as u16 + 2;
            if detail || matches!(&self.records, Some(Records::List(_))) {
                write_list_row(output, row, cols, text, selected);
            } else {
                let marker = if selected { "▌" } else { " " };
                line(output, row, cols, "", &format!("{marker} {text}"));
            }
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
                capacity as u16 + 1,
                cols,
                "\x1b[2m",
                &format!("{label} · {} · {}", run.id, run.state),
            );
        }
        self.footer(output, cols, rows);
    }
}

fn timer_prompt_rows(cols: u16) -> impl Iterator<Item = &'static str> {
    [
        "Install a per-user timer on this host.",
        "It checks for due jobs and launches eligible runs.",
    ]
    .into_iter()
    .flat_map(move |text| {
        text.as_bytes()
            .chunks(usize::from(cols.saturating_sub(2).max(1)))
    })
    .map(|row| std::str::from_utf8(row).expect("ASCII timer prompt"))
}

fn write_list_row(output: &mut String, row: u16, cols: u16, text: &str, selected: bool) {
    let (style, marker) = if selected {
        (ACCENT, "▌")
    } else {
        (PLAIN, " ")
    };
    let label = list_label(text, usize::from(cols.saturating_sub(2)));
    // The bounded label goes straight to the terminal to preserve whole graphemes.
    let _ = write!(output, "\x1b[{row};1H{style}{marker}{PLAIN} {label}");
}

fn list_label(text: &str, columns: usize) -> String {
    if columns == 0 {
        return String::new();
    }
    let mut text: String = text.chars().map(terminal_char).collect();
    let mut width = 0;
    let mut ellipsis_at = 0;
    for (start, cluster) in text.grapheme_indices(true) {
        width += cluster.width();
        if width > columns {
            text.truncate(ellipsis_at);
            text.push('…');
            break;
        }
        if width < columns {
            ellipsis_at = start + cluster.len();
        }
    }
    text
}

fn list_cadence(calendar: &Calendar) -> String {
    let cadence = match calendar.kind.as_str() {
        "cron" => calendar.cron.clone(),
        "once" => format!("{} {}", calendar.date, calendar.time),
        "weekly" => format!("{} {}", calendar.weekday, calendar.time),
        kind => {
            let label = match kind {
                "daily" => "Daily",
                "weekdays" => "Weekdays",
                "" => "Choose",
                _ => kind,
            };
            format!("{label} {}", calendar.time)
        }
    };
    if calendar.time_zone == "host" {
        format!("{cadence} · Host wall time")
    } else {
        format!("{cadence} · {} (fixed)", calendar.time_zone)
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
    enable: Option<(u64, jiff::Timestamp)>,
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
            enable: None,
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

    fn change(&mut self, enable: Option<bool>) -> Result<Change, String> {
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
        Ok(match (&self.target, self.enable.zip(enable)) {
            (Some((id, revision)), Some(((generation, updated_at), install_timer))) => {
                Change::Enable {
                    id: id.clone(),
                    revision: revision.clone(),
                    generation,
                    updated_at,
                    spec,
                    install_timer,
                }
            }
            (Some((id, revision)), _) => Change::Edit {
                id: id.clone(),
                revision: revision.clone(),
                spec,
                paused: self.enable,
            },
            (None, _) => {
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

    fn view(&self, output: &mut String, cols: u16, budget: usize) {
        form_header(
            output,
            cols,
            if self.target.is_some() {
                "Edit automation"
            } else {
                "New automation"
            },
        );
        let window = budget.saturating_sub(1).max(1);
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

#[cfg(test)]
mod pointer_tests {
    use super::*;

    #[test]
    fn timer_install_requires_a_painted_confirmation_for_the_current_view() {
        let mut view = Automation {
            records: Some(Records::List(vec![])),
            timer_owner: Some("owner".into()),
            timer_status: "not installed, inactive".into(),
            ..Automation::default()
        };
        let wide = (80, 24);
        let compact = (40, 8);
        let tiny = (24, 4);
        assert!(view.buttons(wide).iter().any(|button| button.key == 'i'));
        assert!(matches!(view.update(Key::Text('i'), wide), Action::None));
        assert!(matches!(view.update(Key::Text('y'), wide), Action::None));
        view.note_confirmation_paint(wide);
        assert!(matches!(view.update(Key::Text('y'), compact), Action::None));
        view.note_confirmation_paint(tiny);
        assert!(matches!(view.update(Key::Text('y'), tiny), Action::None));

        view.disconnect_timer_owner();
        assert!(!view.confirmation_pending());
        assert!(!view.buttons(wide).iter().any(|button| button.key == 'i'));
        assert!(matches!(view.update(Key::Text('y'), compact), Action::None));

        view.expect_timer_status(1, Some("owner".into()));
        assert!(view.receive_timer_status(1, "not installed, inactive".into()));
        view.update(Key::Text('i'), compact);
        view.note_confirmation_paint(compact);
        let Action::InstallAutomationTimer { endpoint, status } =
            view.update(Key::Text('y'), compact)
        else {
            panic!("expected the painted timer confirmation to take effect");
        };
        assert_eq!(endpoint, "owner");
        assert_eq!(status, "not installed, inactive");
        assert!(matches!(view.update(Key::Text('y'), compact), Action::None));
        view.expect_timer_status(2, Some("owner".into()));
        assert!(view.receive_timer_status(2, "installed, active".into()));
        assert!(!view.buttons(compact).iter().any(|button| button.key == 'i'));
    }

    #[test]
    fn detail_keeps_prompt_paragraphs_and_reveals_the_selected_line() {
        let job: Job = serde_json::from_value(serde_json::json!({
            "id":"job","revision_id":"revision","state":"paused",
            "created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z",
            "spec":{"name":"Fixture","directory":"/fixture","workspace":"directory",
                "authority":"agent-default","agents":["claude","codex"],
                "calendar":{"kind":"daily","time_zone":"UTC","time":"09:00"},
                "prompt":"A continuation that needs two labels.\r\n\nEnd\tmarker."}
        }))
        .unwrap();
        let mut view = Automation::default();
        view.expect_timer_status(1, Some("owner".into()));
        view.receive(Ok(Records::Get(Box::new(job.clone()))));
        assert!(view.receive_timer_status(1, "not installed, inactive".into()));
        assert!(
            !view
                .buttons((80, 24))
                .iter()
                .any(|button| button.key == 'i')
        );
        let expected = view.detail_rows(&job, 24);
        assert_eq!(
            expected[..3],
            ["Timer · not installed,", " inactive", "paused"]
        );
        assert_eq!(expected[expected.len() - 2..], ["", "End    marker."]);
        let geometry = (24, 8);
        let capacity = view.body_capacity(geometry);
        view.update(Key::End, geometry);
        let (top, _, _) = view.rows_at(geometry);
        assert_eq!(top, expected.len() - capacity);
        view.update(Key::BackTab, geometry);
        assert_eq!(view.selected, expected.len() - 2);
        assert_eq!(view.rows_at(geometry).0, top);
        view.update(Key::Home, geometry);
        assert_eq!(view.rows_at(geometry).0, 0);
    }

    #[test]
    fn timer_status_is_bound_to_the_latest_list_refresh() {
        let mut view = Automation {
            records: Some(Records::List(vec![])),
            ..Automation::default()
        };
        assert!(matches!(
            view.update(Key::Text('r'), (80, 24)),
            Action::Catalog(Read::List)
        ));
        view.expect_timer_status(1, None);
        view.expect_timer_status(2, None);
        assert!(!view.receive_timer_status(1, "not installed, inactive".into()));
        assert_eq!(view.list_title(80), "Automations");
        assert!(view.receive_timer_status(2, "installed, active".into()));
        assert_eq!(view.list_title(80), "Automations · Timer installed, active");
        assert_eq!(view.list_title(40), "Automations · active");
        view.archived = true;
        view.expect_timer_status(3, None);
        assert!(view.receive_timer_status(3, "status unavailable".into()));
        assert_eq!(
            view.list_title(80),
            "Automations / Archived · Timer status unavailable"
        );
        assert_eq!(view.list_title(40), "Automations / Archived · unknown");
        assert_eq!(view.list_title(30), "Archived · unknown");
    }

    #[test]
    fn list_paint_keeps_graphemes_whole_at_the_visible_boundary() {
        for (text, expected) in [
            ("👩‍💻👩‍💻x", "👩‍💻…"),
            ("🇺🇳🇺🇳x", "🇺🇳…"),
            ("e\u{301}e\u{301}xy", "e\u{301}e\u{301}…"),
            ("👩‍💻x", "👩‍💻x"),
            ("a\u{1b}\u{2067}", "a  "),
        ] {
            let mut output = String::new();
            write_list_row(&mut output, 2, 5, text, true);
            assert_eq!(output, format!("\x1b[2;1H{ACCENT}▌{PLAIN} {expected}"));
        }
    }

    #[test]
    fn footer_activation_requires_the_same_position_and_viewport() {
        let mut view = Automation {
            records: Some(Records::List(vec![])),
            ..Automation::default()
        };
        let mouse = |action| Mouse {
            x: 2,
            y: 24,
            action,
        };
        assert_eq!(view.mouse(mouse(MouseAction::Press), (80, 24)), None);
        assert_eq!(view.mouse(mouse(MouseAction::Drag), (80, 24)), None);
        assert_eq!(view.mouse(mouse(MouseAction::Release), (80, 24)), None);
        assert_eq!(view.mouse(mouse(MouseAction::Press), (80, 24)), None);
        assert_eq!(view.mouse(mouse(MouseAction::Release), (70, 24)), None);
        assert_eq!(view.mouse(mouse(MouseAction::Press), (80, 24)), None);
        let key = view.mouse(mouse(MouseAction::Release), (80, 24)).unwrap();
        assert_eq!(key, Key::Text('n'));
        view.update(key, (80, 24));
        assert!(view.draft.is_some());
    }

    #[test]
    fn wheel_moves_focused_draft_field() {
        let mut view = Automation::in_directory("/tmp");
        let wheel = Mouse {
            x: 4,
            y: 2,
            action: MouseAction::WheelDown,
        };
        let key = view.mouse(wheel, (40, 8)).unwrap();
        view.update(key, (40, 8));
        assert_eq!(view.draft.unwrap().focus, 1);
    }
}
