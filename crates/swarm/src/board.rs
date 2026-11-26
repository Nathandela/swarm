mod attach;
mod automation;
mod handoff;
mod keys;
mod launch;
mod navigation;
mod options;
mod preferences;
mod roster;
mod term;
mod touch;

use crate::ipc::{self, Client, field};
use crate::schedule::catalog::{
    CAP as CATALOG_CAP, CONTROL as CATALOG_CONTROL, Change as CatalogChange, MANUAL_CAP, Manual,
    Read as CatalogRead, Records as CatalogRecords, RunState,
};
use automation::Automation;
use handoff::Handoff;
use keys::{Key, Keys, Mouse, MouseAction};
use launch::Launch;
use navigation::{Focus, GROUPS, ORDERS, Row, board_capacity, board_first, capacity, usable};
use preferences::Layout;
use serde_json::{Value, json};
use std::collections::BTreeSet;
use std::env;
use std::fmt::Write as _;
use std::io::{self, Write};
use std::os::fd::AsRawFd;
use std::path::Path;
use std::time::{Duration, Instant};
use unicode_width::{UnicodeWidthChar, UnicodeWidthStr};

const POLL: Duration = Duration::from_millis(40);
const ROSTER_CAPS: &[&str] = &[
    "subscribe",
    "opencode-observation",
    "resume-relocate",
    handoff::CAP,
    CATALOG_CAP,
    CATALOG_CONTROL,
    MANUAL_CAP,
    "schedule",
    "schedule-run-id",
];

pub(crate) fn run() -> u8 {
    match crate::bootstrap::ensure_daemon()
        .and_then(|()| ipc::socket_path())
        .and_then(|path| board(&path))
    {
        Ok(()) => 0,
        Err(error) => {
            eprintln!("swarm: {error}");
            1
        }
    }
}

fn board(path: &Path) -> io::Result<()> {
    let mut client = connect(path)?;
    let mut board = Board {
        hands_off: client.supports(handoff::CAP),
        ..Board::default()
    };
    match Layout::load() {
        Ok(settings) => board.layout = settings,
        Err(error) => board.notice = format!("board layout: {error}"),
    }
    board.refresh(&mut client)?;
    let _terminal = term::Terminal::raw()?;
    let mut output = io::stdout().lock();
    output.write_all(term::ENTER_BOARD.as_bytes())?;
    let mut connection = Some(client);
    let mut geometry = term::size();
    board.columns = geometry.0;
    let clock = Instant::now();
    board.reveal_focus(geometry.1);
    let mut keys = Keys::default();
    let mut raw_input = [0; 4096];
    let mut needs_paint = true;
    let mut retry = Instant::now();
    let mut delay = Duration::from_secs(1);
    while !term::stopped() {
        let dimensions = term::size();
        match &mut board.modal {
            Some(Modal::Launch(form)) => needs_paint |= form.update_detection(),
            Some(Modal::Handoff(form)) => needs_paint |= form.update_detection(),
            _ => {}
        }
        let spinner = (clock.elapsed().as_millis() / 90) as usize % 10;
        let second = clock.elapsed().as_secs();
        if board.modal.is_none() && !board.rows.is_empty() {
            needs_paint |= board.second != second
                || board.spinner != spinner
                    && board
                        .rows
                        .iter()
                        .any(|row| field(row, "group") == "working");
        }
        board.spinner = spinner;
        board.second = second;
        if dimensions != geometry || needs_paint {
            if dimensions != geometry {
                geometry = dimensions;
                board.columns = geometry.0;
                board.press = None;
                board.reveal_focus(geometry.1);
            }
            output.write_all(board.view(geometry).as_bytes())?;
            output.flush()?;
        }
        needs_paint = false;
        let (input, socket) = term::ready(
            connection
                .as_ref()
                .map(|client| client.stream().as_raw_fd()),
            POLL,
        )?;
        if let Some(client) = &mut connection {
            if socket || client.has_push() {
                match client.push(Instant::now() + Duration::from_secs(1)) {
                    Ok(event) if field(&event, "op") == "event" => {
                        if board.refresh(client).is_err() {
                            connection = None;
                        }
                        needs_paint = true;
                    }
                    Ok(_) => {}
                    Err(_) => connection = None,
                }
                if connection.is_none() {
                    board.notice = "daemon connection lost · reconnecting".to_owned();
                    retry = Instant::now() + delay;
                    needs_paint = true;
                }
            }
        } else if Instant::now() >= retry {
            match connect(path).and_then(|mut client| board.refresh(&mut client).map(|()| client)) {
                Ok(client) => {
                    board.notice.clear();
                    board.hands_off = client.supports(handoff::CAP);
                    connection = Some(client);
                    delay = Duration::from_secs(1);
                }
                Err(_) => {
                    delay = (delay * 2).min(Duration::from_secs(8));
                    retry = Instant::now() + delay;
                }
            }
            needs_paint = true;
        }
        let input_keys = if input {
            let n = term::input(&mut raw_input)?;
            if n == 0 {
                break;
            }
            keys.read(&raw_input[..n], false)
        } else {
            keys.read(&[], true)
        };
        for key in input_keys {
            match board.update(key, geometry, connection.is_some()) {
                Action::None => {}
                Action::Quit => return Ok(()),
                Action::Retry => {
                    retry = Instant::now();
                    delay = Duration::from_secs(1);
                }
                Action::SaveLayout(layout) => {
                    if let Err(error) = layout.save() {
                        board.notice = format!("board layout: {error}");
                    }
                }
                Action::Attach { id, name } => {
                    if connection.is_some() {
                        board.attach(path, &id, &name, &mut output)?;
                    }
                    // Bytes from the initiating key batch belong to the board.
                    break;
                }
                Action::Catalog(query) => {
                    let result = connection.as_mut().map_or_else(
                        || {
                            Err(io::Error::other(
                                "daemon disconnected; reconnect and refresh",
                            ))
                        },
                        |client| query.remote(client),
                    );
                    let state = automation_status(&result, connection.as_mut());
                    if let Some(Modal::Automation(view)) = &mut board.modal {
                        view.receive(result);
                        if let Some(state) = state {
                            view.triggered(state);
                        }
                    }
                }
                Action::ChangeAutomation(change) => {
                    let result = connection.as_mut().map_or_else(
                        || {
                            Err(io::Error::other(
                                "daemon disconnected; reconnect before saving",
                            ))
                        },
                        |client| change.remote(client),
                    );
                    let state = automation_status(&result, connection.as_mut());
                    if let Some(Modal::Automation(view)) = &mut board.modal {
                        view.changed(result);
                        if let Some(state) = state {
                            view.triggered(state);
                        }
                    }
                }
                Action::TriggerAutomation { job, intent } => {
                    let result = connection.as_mut().map_or_else(
                        || {
                            Err(io::Error::other(
                                "daemon disconnected; reconnect before running",
                            ))
                        },
                        |client| intent.remote(client, &job),
                    );
                    if let Some(Modal::Automation(view)) = &mut board.modal {
                        view.triggered(result);
                    }
                }
                Action::Request(message, target) => {
                    let Some(client) = &mut connection else {
                        continue;
                    };
                    let label = if target == "launch" {
                        Some(name(&message["launch"]).to_owned())
                    } else {
                        None
                    };
                    let response = if target == "launch" {
                        let timeout = ipc::launch_timeout(field(&message["launch"], "agent"));
                        client.exchange(message, target, timeout)
                    } else {
                        client.request_body(message, target)
                    };
                    match response {
                        Ok(reply) => {
                            let id = field(&reply["session"], "id");
                            if label.is_some() && !id.is_empty() {
                                board.set_focus(Focus::Session(id.to_owned()));
                            }
                            if board.refresh(client).is_err() {
                                connection = None;
                                board.notice = "daemon connection lost · reconnecting".to_owned();
                                retry = Instant::now() + delay;
                            } else if let Some(label) = label
                                && !id.is_empty()
                            {
                                board.attach(path, id, &label, &mut output)?;
                                needs_paint = true;
                                break;
                            }
                        }
                        Err(error) => board.notice = error.to_string(),
                    }
                }
            }
            needs_paint = true;
        }
        if !needs_paint && input {
            needs_paint = true;
        }
    }
    Ok(())
}

fn connect(path: &Path) -> io::Result<Client> {
    let mut client = Client::dial(path, ROSTER_CAPS)?;
    client.request("subscribe", None, "ok")?;
    Ok(client)
}

#[derive(Default)]
struct Board {
    rows: Vec<Value>,
    columns: u16,
    spinner: usize,
    hands_off: bool,
    second: u64,
    selected: String,
    focused_section: Option<String>,
    folded: BTreeSet<(usize, String)>,
    sections: Vec<(String, Vec<usize>)>,
    top: usize,
    layout: Layout,
    press: Option<Press>,
    notice: String,
    modal: Option<Modal>,
}

#[derive(Clone, Debug, PartialEq, Eq)]
enum Hit {
    Row(Focus, bool),
    Button {
        key: char,
        target: String,
        running: bool,
    },
    Menu {
        index: usize,
        key: char,
        target: String,
        running: bool,
    },
    Attention,
}

struct Press {
    hit: Hit,
    y: u16,
    invoke_on_release: bool,
}

enum Modal {
    Launch(Box<Launch>),
    Handoff(Box<Handoff>),
    Automation(Box<Automation>),
    ResumeAgy {
        id: String,
        cwd: Editor,
    },
    Options {
        draft: Layout,
        focus: usize,
    },
    More {
        target: String,
        running: bool,
        selected: usize,
        top: usize,
    },
    Edit {
        id: String,
        tag: bool,
        draft: Editor,
    },
    Confirm {
        id: String,
        name: String,
        running: bool,
        top: usize,
    },
}

enum Action {
    None,
    Quit,
    Attach { id: String, name: String },
    Retry,
    SaveLayout(Layout),
    Catalog(CatalogRead),
    ChangeAutomation(CatalogChange),
    TriggerAutomation { job: String, intent: Manual },
    Request(Value, &'static str),
}

impl Board {
    fn attach(
        &mut self,
        path: &Path,
        id: &str,
        label: &str,
        output: &mut impl Write,
    ) -> io::Result<()> {
        let mut ring: Vec<_> = self
            .display_rows()
            .into_iter()
            .filter_map(|row| match row {
                Row::Session(row) if running(row) && !linked_dispatcher(row) => {
                    Some((field(row, "id").to_owned(), name(row).to_owned()))
                }
                _ => None,
            })
            .collect();
        let mut index = ring
            .iter()
            .position(|(target, _)| target == id)
            .unwrap_or_else(|| {
                ring.push((id.to_owned(), label.to_owned()));
                ring.len() - 1
            });
        loop {
            let (id, label) = &ring[index];
            match attach::run(path, id, label, self.layout.touch_back, output) {
                Ok(attach::Exit::Next) => index = (index + 1) % ring.len(),
                Ok(attach::Exit::Previous) => index = (index + ring.len() - 1) % ring.len(),
                Ok(attach::Exit::Board) => break,
                Err(error) => {
                    self.notice = error.to_string();
                    break;
                }
            }
            self.set_focus(Focus::Session(ring[index].0.clone()));
            self.reveal_focus(term::size().1);
        }
        output.write_all(term::RESTORE.as_bytes())?;
        if !term::stopped() {
            output.write_all(term::ENTER_BOARD.as_bytes())?;
        }
        Ok(())
    }

    fn row(&self) -> Option<&Value> {
        self.rows
            .iter()
            .find(|row| field(row, "id") == self.selected)
    }

    fn refresh(&mut self, client: &mut Client) -> io::Result<()> {
        let mut rows = client.sessions()?;
        rows.retain(|row| !field(row, "id").is_empty() && row["roster_hidden"] != true);
        self.rows = rows;
        self.rebuild_sections();
        Ok(())
    }

    fn by_id(&self, id: &str) -> Option<&Value> {
        self.rows.iter().find(|row| field(row, "id") == id)
    }

    fn update(&mut self, key: Key, geometry: (u16, u16), online: bool) -> Action {
        let (_, rows) = geometry;
        if let Key::Mouse(event) = key {
            return self.mouse(event, geometry, online);
        }
        self.press = None;
        if let Some(mut modal) = self.modal.take() {
            if key == Key::Escape
                && let Modal::Automation(view) = &mut modal
                && view.cancel()
            {
                self.modal = Some(modal);
                return Action::None;
            }
            if matches!(key, Key::Escape)
                || (matches!(modal, Modal::Launch(_))
                    && !Launch::readable(geometry)
                    && key == Key::Text('n'))
            {
                self.notice.clear();
                return Action::None;
            }
            return self.update_modal(modal, key, geometry, online);
        }
        let step = board_capacity(rows) as isize;
        let navigation = match key {
            Key::Escape | Key::CtrlC => return Action::Quit,
            Key::Up | Key::Text('k') => Some((-1, true)),
            Key::Down | Key::Text('j') => Some((1, true)),
            Key::PageUp => Some((-step, false)),
            Key::PageDown => Some((step, false)),
            Key::Home => Some((isize::MIN, false)),
            Key::End => Some((isize::MAX, false)),
            Key::Left => {
                self.fold(None, true, rows);
                return Action::None;
            }
            Key::Right | Key::Enter if self.focused_section.is_some() => {
                self.fold(self.focused_section.clone(), false, rows);
                return Action::None;
            }
            Key::Text('g') => {
                self.reveal_attention(rows);
                return Action::None;
            }
            Key::Text('r') if !online => return Action::Retry,
            _ => None,
        };
        if let Some((offset, wrap)) = navigation {
            self.move_selection(offset, wrap, rows);
            return Action::None;
        }
        if !usable(geometry) {
            return Action::None;
        }
        if matches!(
            key,
            Key::Enter | Key::CtrlX | Key::Text('e' | 't' | 'r' | 'h')
        ) && self.current_focus().is_some()
            && !self.visible(rows)
        {
            self.reveal_focus(rows);
            return Action::None;
        }
        match key {
            Key::Text('m') => {
                let row = self.row().filter(|_| self.visible(rows));
                let target = row.map(|row| field(row, "id")).unwrap_or("").to_owned();
                let running = row.is_some_and(running);
                let selected = self
                    .menu(&target, running)
                    .iter()
                    .position(|entry| entry.1.is_some())
                    .unwrap_or(0);
                self.modal = Some(Modal::More {
                    target,
                    running,
                    selected,
                    top: selected.saturating_sub(capacity(rows) - 1),
                });
            }
            Key::Text('n' | 'o') | Key::Enter | Key::CtrlX | Key::Text('e' | 't' | 'r' | 'h') => {
                let id = self.selected.clone();
                return self.command(key, &id, geometry, online);
            }
            _ => {}
        }
        Action::None
    }

    fn command(&mut self, key: Key, id: &str, geometry: (u16, u16), online: bool) -> Action {
        match key {
            Key::Text('q') => return Action::Quit,
            Key::Text('n') if online => {
                self.modal = Some(Modal::Launch(Box::new(Launch::new())));
                self.notice.clear();
            }
            Key::Text('o') => {
                self.modal = Some(Modal::Options {
                    draft: self.layout,
                    focus: 0,
                });
                self.notice.clear();
            }
            Key::Text('a') => {
                self.modal = Some(Modal::Automation(Box::default()));
                self.notice.clear();
                return Action::Catalog(CatalogRead::List);
            }
            Key::Text('c') if online => {
                if let Some(row) = self.by_id(id).filter(|row| !linked_dispatcher(row)) {
                    let directory = field(row, "cwd");
                    if !directory.is_empty() {
                        self.modal = Some(Modal::Automation(Box::new(Automation::in_directory(
                            directory,
                        ))));
                        self.notice.clear();
                        return Action::Catalog(CatalogRead::List);
                    }
                }
            }
            Key::Text('g') => self.reveal_attention(geometry.1),
            _ if !online => {}
            Key::Enter => {
                if let Some(row) = self.by_id(id) {
                    if running(row) {
                        return Action::Attach {
                            id: id.to_owned(),
                            name: name(row).to_owned(),
                        };
                    }
                    self.notice = if linked_dispatcher(row) {
                        "Continue this dispatcher in its Discord thread".into()
                    } else if resumable(row) {
                        if field(row, "agent") == "hermes"
                            && row["status"]["NativeWorkUnknown"] == true
                        {
                            "Hermes viewer ended · r resume".into()
                        } else {
                            "Session ended · r resume · ctrl+x delete".into()
                        }
                    } else {
                        "Session ended · ctrl+x delete".into()
                    };
                }
            }
            Key::Text('h') => {
                if let Some(source) = self.by_id(id).filter(|source| !linked_dispatcher(source)) {
                    self.modal = Some(Modal::Handoff(Box::new(Handoff::new(source))));
                    self.notice.clear();
                }
            }
            Key::Text('r') => {
                if let Some(row) = self
                    .by_id(id)
                    .filter(|row| resumable(row) && !running(row) && !linked_dispatcher(row))
                {
                    let cwd = Path::new(field(row, "cwd"));
                    let agent = field(row, "agent");
                    if cwd.is_dir() || agent != "agy" {
                        return resume(agent, id, cwd, geometry, false);
                    }
                    let cwd = cwd
                        .ancestors()
                        .find(|path| path.is_dir())
                        .unwrap_or(Path::new("/"));
                    self.modal = Some(Modal::ResumeAgy {
                        id: id.to_owned(),
                        cwd: Editor::new(&cwd.to_string_lossy()),
                    });
                }
            }
            Key::Text('e' | 't') => {
                if let Some(row) = self.by_id(id).filter(|row| !linked_dispatcher(row)) {
                    let tag = matches!(key, Key::Text('t'));
                    self.modal = Some(Modal::Edit {
                        id: id.to_owned(),
                        tag,
                        draft: Editor::new(field(row, if tag { "tag" } else { "name" })),
                    });
                }
            }
            Key::CtrlX => {
                if let Some(row) = self.by_id(id).filter(|row| !linked_dispatcher(row)) {
                    self.modal = Some(Modal::Confirm {
                        id: id.to_owned(),
                        name: name(row).to_owned(),
                        running: running(row),
                        top: 0,
                    });
                }
            }
            _ => {}
        }
        Action::None
    }

    fn update_modal(
        &mut self,
        mut modal: Modal,
        key: Key,
        geometry: (u16, u16),
        online: bool,
    ) -> Action {
        let (_, rows) = geometry;
        let action = match &mut modal {
            Modal::Launch(form) => {
                if matches!(key, Key::Enter) && (!online || !Launch::readable(geometry)) {
                    Action::None
                } else {
                    match form.update(key, geometry) {
                        Ok(action) => action,
                        Err(error) => {
                            self.notice = error.to_string();
                            Action::None
                        }
                    }
                }
            }
            Modal::Handoff(form) => {
                let source = self.by_id(&form.source_id);
                form.update(key, source, geometry, self.hands_off, online)
            }
            Modal::ResumeAgy { id, cwd } => {
                if matches!(key, Key::Enter) {
                    if online && usable(geometry) && self.by_id(id).is_some_and(|row| !running(row))
                    {
                        let path = Path::new(cwd.text.trim());
                        if path.is_absolute() && path.is_dir() {
                            return resume("agy", id, path, geometry, true);
                        }
                        self.notice = "Choose an existing absolute workspace directory".into();
                    }
                } else {
                    cwd.apply(key);
                }
                Action::None
            }
            Modal::Automation(view) => {
                let action = view.update(key, geometry);
                self.modal = Some(modal);
                return action;
            }
            Modal::Options { draft, focus } => {
                match key {
                    Key::Up => *focus = (*focus + 2) % 3,
                    Key::Down | Key::Tab => *focus = (*focus + 1) % 3,
                    Key::Left | Key::Right if *focus == 2 => {
                        draft.touch_back = !draft.touch_back;
                    }
                    Key::Left | Key::Right => {
                        let (selected, len) = if *focus == 0 {
                            (&mut draft.group, GROUPS.len())
                        } else {
                            (&mut draft.order, ORDERS.len())
                        };
                        let offset = if matches!(key, Key::Left) { len - 1 } else { 1 };
                        *selected = (*selected + offset) % len;
                    }
                    Key::Enter if options::fits(geometry) => {
                        self.layout = *draft;
                        self.rebuild_sections();
                        self.reveal_focus(rows);
                        self.notice.clear();
                        return Action::SaveLayout(*draft);
                    }
                    _ => {}
                }
                Action::None
            }
            Modal::More {
                target,
                running,
                selected,
                top,
            } => {
                let entries = self.menu(target, *running);
                let step = capacity(rows) as isize;
                *selected = (*selected).min(entries.len() - 1);
                let delta = match key {
                    Key::Up | Key::Text('k') => -1,
                    Key::Down | Key::Text('j') => 1,
                    Key::Home => isize::MIN,
                    Key::End => isize::MAX,
                    Key::PageUp => -step,
                    Key::PageDown => step,
                    _ => 0,
                };
                if delta != 0 {
                    *selected = (*selected as isize)
                        .saturating_add(delta)
                        .clamp(0, entries.len() as isize - 1)
                        as usize;
                    *top = (*top)
                        .min(*selected)
                        .max((*selected + 1).saturating_sub(capacity(rows)));
                } else if usable(geometry) {
                    let chosen = match key {
                        Key::Enter => entries[*selected].1,
                        Key::CtrlX => Some('x'),
                        Key::Text(ch) => Some(ch),
                        _ => None,
                    };
                    if let Some(chosen) = chosen
                        && entries.iter().any(|entry| entry.1 == Some(chosen))
                    {
                        return self.command(
                            match chosen {
                                '⏎' => Key::Enter,
                                '⎋' => Key::Escape,
                                'x' => Key::CtrlX,
                                other => Key::Text(other),
                            },
                            target,
                            geometry,
                            online,
                        );
                    }
                }
                Action::None
            }
            Modal::Edit { id, tag, draft } => {
                if matches!(key, Key::Enter) {
                    if online && usable(geometry) && self.by_id(id).is_some() {
                        let (op, key, text) = if *tag {
                            ("set_tag", "tag", draft.text.trim())
                        } else {
                            ("rename", "name", draft.text.as_str())
                        };
                        let mut message = json!({"op":op, "session_id":id});
                        message[key] = json!(text);
                        return Action::Request(message, "ok");
                    }
                } else {
                    draft.apply(key);
                }
                Action::None
            }
            Modal::Confirm {
                id,
                name,
                running: was_running,
                top,
            } => {
                let words = confirm_lines(name, *was_running, geometry.0);
                let last = words.len().saturating_sub(capacity(rows));
                *top = (*top).min(last);
                match key {
                    Key::Text('n') => return Action::None,
                    Key::Up | Key::PageUp => *top = top.saturating_sub(1),
                    Key::Down | Key::PageDown | Key::Tab => *top = (*top + 1).min(last),
                    Key::Home => *top = 0,
                    Key::End => *top = last,
                    Key::Text('y') | Key::CtrlX if online && usable(geometry) && *top == last => {
                        if self
                            .by_id(id)
                            .is_some_and(|row| running(row) == *was_running)
                        {
                            let op = if *was_running { "kill" } else { "delete" };
                            return Action::Request(json!({"op":op,"session_id":id}), "ok");
                        }
                        self.notice = "Target changed; reopen confirmation".into();
                        return Action::None;
                    }
                    _ => {}
                }
                Action::None
            }
        };
        if matches!(action, Action::None) {
            self.modal = Some(modal);
        } else {
            self.notice.clear();
        }
        action
    }

    fn menu(&self, target: &str, expected_running: bool) -> Vec<(String, Option<char>)> {
        let mut entries = Vec::new();
        if let Some(row) = self.by_id(target) {
            for (key, value) in [
                ("Name", name(row)),
                ("State", field(row, "group")),
                ("Agent", field(row, "agent")),
                ("Directory", field(row, "cwd")),
                ("ID", target),
            ] {
                entries.push((format!("{key} · {value}"), None));
            }
            let unchanged = running(row) == expected_running;
            if unchanged && expected_running {
                entries.push(("Open".into(), Some('⏎')));
            }
            if !linked_dispatcher(row) {
                if unchanged
                    && !expected_running
                    && matches!(field(row, "agent"), "agy" | "opencode")
                {
                    entries.push(("Resume".into(), Some('r')));
                }
                entries.extend([
                    ("Rename".into(), Some('e')),
                    ("Tag".into(), Some('t')),
                    ("Handoff".into(), Some('h')),
                ]);
                if !field(row, "cwd").is_empty() {
                    entries.push(("Schedule in this directory".into(), Some('c')));
                }
                if unchanged {
                    let label = if expected_running { "Kill" } else { "Delete" };
                    entries.push((label.into(), Some('x')));
                }
            }
            if !unchanged {
                entries.push(("Target state changed; reopen More".into(), None));
            }
        } else if !target.is_empty() {
            entries.push(("Target disappeared".into(), None));
        }
        entries.extend([
            ("New".into(), Some('n')),
            ("Options".into(), Some('o')),
            ("Automations".into(), Some('a')),
            ("Attention".into(), Some('g')),
            ("Quit".into(), Some('q')),
        ]);
        entries
    }

    fn buttons(&self, cols: u16, rows: u16) -> Vec<(&'static str, char)> {
        let mut buttons = vec![("New (n)", 'n')];
        let target = self.row();
        if self.focused_section.is_some() {
            buttons.push(("Expand (⏎)", '⏎'));
        } else if let Some(row) = target {
            if !self.visible(rows) {
                buttons.push(("Show (⏎)", '⏎'));
            } else if running(row) || scheduled_review(row) || linked_dispatcher(row) {
                buttons.push(("Open (⏎)", '⏎'));
            } else if resumable(row) {
                buttons.push(("Resume (r)", 'r'));
            }
        }
        buttons.push(("More (m)", 'm'));
        let required = buttons
            .iter()
            .map(|(label, _)| label.width() + 3)
            .sum::<usize>()
            - 1;
        if required > usize::from(cols) {
            buttons.remove(0);
        }
        if let Some(row) = target.filter(|row| self.visible(rows) && !linked_dispatcher(row)) {
            buttons.extend([("Rename (e)", 'e'), ("Tag (t)", 't'), ("Handoff (h)", 'h')]);
            let lifecycle = if running(row) {
                "Kill (ctrl+x)"
            } else {
                "Delete (ctrl+x)"
            };
            buttons.push((lifecycle, 'x'));
        }
        buttons.extend([
            ("Options (o)", 'o'),
            ("Attention (g)", 'g'),
            ("Quit (esc)", '⎋'),
        ]);
        buttons
    }

    fn button_view(&self, cols: u16, rows: u16) -> Vec<(String, char, u16, u16)> {
        let mut x = 1;
        let mut bars = Vec::new();
        for (entry, key) in self.buttons(cols, rows) {
            let text = format!("[{entry}]");
            let right = x + (text.width() as u16) - 1;
            if right > cols {
                break;
            }
            bars.push((text, key, x, right));
            x = right + 2;
        }
        bars
    }

    fn hit(&self, event: Mouse, geometry: (u16, u16)) -> Option<Hit> {
        let (cols, rows) = geometry;
        if event.x == 0 || event.x > cols || event.y == 0 || event.y > rows {
            return None;
        }
        if let Some(Modal::More {
            target,
            running: was_running,
            top,
            ..
        }) = &self.modal
        {
            if !(2..rows).contains(&event.y) {
                return None;
            }
            let index = *top + usize::from(event.y - 2);
            let key = self.menu(target, *was_running).get(index)?.1?;
            return Some(Hit::Menu {
                index,
                key,
                target: target.clone(),
                running: self.by_id(target).is_some_and(running),
            });
        }
        if event.y == 1 {
            return Some(Hit::Attention);
        }
        if event.y == rows {
            return self
                .button_view(cols, rows)
                .into_iter()
                .find(|(_, _, left, right)| (*left..=*right).contains(&event.x))
                .map(|(_, key, _, _)| Hit::Button {
                    key,
                    target: self.selected.clone(),
                    running: self.row().is_some_and(running),
                });
        }
        let first = board_first(rows);
        if event.y < first {
            return None;
        }
        let row = self
            .display_rows()
            .into_iter()
            .nth(self.top + usize::from(event.y - first))?;
        row.focus()
            .map(|focus| Hit::Row(focus, matches!(row, Row::Session(row) if running(row))))
    }

    fn mouse(&mut self, event: Mouse, geometry: (u16, u16), online: bool) -> Action {
        if self
            .modal
            .as_ref()
            .is_some_and(|modal| !matches!(modal, Modal::More { .. }))
            || !usable(geometry)
        {
            self.press = None;
            return Action::None;
        }
        let hit = self.hit(event, geometry);
        match event.action {
            MouseAction::Drag => self.press = None,
            MouseAction::WheelUp | MouseAction::WheelDown => {
                self.press = None;
                let first = if self.modal.is_some() {
                    2
                } else {
                    board_first(geometry.1)
                };
                if (first..geometry.1).contains(&event.y) {
                    let down = event.action == MouseAction::WheelDown;
                    if let Some(Modal::More {
                        target, running, ..
                    }) = &self.modal
                    {
                        let max = self
                            .menu(target, *running)
                            .len()
                            .saturating_sub(capacity(geometry.1));
                        if let Some(Modal::More { top, .. }) = &mut self.modal {
                            *top = scroll(*top, down).min(max);
                        }
                    } else {
                        self.top = scroll(self.top, down);
                        self.clamp_top(geometry.1);
                    }
                }
            }
            MouseAction::Press => {
                if let Some(hit) = hit {
                    let invoke_on_release = match &hit {
                        Hit::Row(focus @ Focus::Session(_), _) => {
                            let already = self.current_focus().as_ref() == Some(focus);
                            self.set_focus(focus.clone());
                            already
                        }
                        _ => true,
                    };
                    self.press = Some(Press {
                        hit,
                        y: event.y,
                        invoke_on_release,
                    });
                } else {
                    self.press = None;
                }
            }
            MouseAction::Release => {
                if let Some(press) = self.press.take()
                    && press.invoke_on_release
                    && press.y == event.y
                    && hit == Some(press.hit.clone())
                {
                    match press.hit {
                        Hit::Row(Focus::Session(id), _) => {
                            return self.command(Key::Enter, &id, geometry, online);
                        }
                        Hit::Row(Focus::Section(section), _) => {
                            let collapse =
                                !self.folded.contains(&(self.layout.group, section.clone()));
                            self.fold(Some(section), collapse, geometry.1);
                        }
                        Hit::Button { key: button, .. } => {
                            let key = match button {
                                '⏎' => Key::Enter,
                                '⎋' => Key::Escape,
                                'x' => Key::CtrlX,
                                ch => Key::Text(ch),
                            };
                            return self.update(key, geometry, online);
                        }
                        Hit::Menu { index, .. } => {
                            if let Some(Modal::More { selected, .. }) = &mut self.modal {
                                *selected = index;
                            }
                            return self.update(Key::Enter, geometry, online);
                        }
                        Hit::Attention => self.reveal_attention(geometry.1),
                    }
                }
            }
        }
        Action::None
    }

    fn view(&self, (cols, rows): (u16, u16)) -> String {
        let mut screen = String::from("\x1b[?25l\x1b[0m\x1b[H\x1b[2J");
        if !usable((cols, rows)) {
            line(&mut screen, 1, cols, "", "Need 24×4 · esc back");
            return screen;
        }
        match &self.modal {
            Some(Modal::Launch(form)) => form.view(&mut screen, cols, rows),
            Some(Modal::Handoff(form)) => form.view(&mut screen, cols, rows),
            Some(Modal::Automation(view)) => view.view(&mut screen, cols, rows),
            Some(Modal::ResumeAgy { cwd, .. }) => {
                line(&mut screen, 1, cols, "\x1b[1m", "Relocate session");
                line(
                    &mut screen,
                    2,
                    cols,
                    "",
                    &format!("Directory  {}", cwd.text),
                );
                line(
                    &mut screen,
                    rows,
                    cols,
                    "\x1b[2m",
                    "enter relocate · esc cancel",
                );
            }
            Some(Modal::Options { draft, focus }) => {
                options::view(&mut screen, cols, rows, *draft, *focus);
            }
            Some(Modal::More {
                target,
                running,
                selected,
                top,
            }) => {
                form_header(&mut screen, cols, "More");
                for (index, (text, key)) in self
                    .menu(target, *running)
                    .into_iter()
                    .enumerate()
                    .skip(*top)
                    .take(capacity(rows))
                {
                    let mark = if index == *selected { "▌" } else { " " };
                    let label = key.map_or_else(|| text.clone(), |ch| format!("[{text} ({ch})]"));
                    line(
                        &mut screen,
                        (index - top) as u16 + 2,
                        cols,
                        "",
                        &format!("{mark} {label}"),
                    );
                }
                line(
                    &mut screen,
                    rows,
                    cols,
                    "\x1b[2m",
                    "↑↓ move · enter select · esc back",
                );
            }
            Some(Modal::Edit { tag, draft, .. }) => {
                form_header(
                    &mut screen,
                    cols,
                    if *tag { "Edit tag" } else { "Rename session" },
                );
                let (text, col) = draft.visible(cols);
                line(&mut screen, 2, cols, "", &text);
                let _ = write!(&mut screen, "\x1b[2;{col}H\x1b[?25h");
                line(
                    &mut screen,
                    rows,
                    cols,
                    "\x1b[2m",
                    "enter save · esc cancel",
                );
            }
            Some(Modal::Confirm {
                name, running, top, ..
            }) => {
                form_header(
                    &mut screen,
                    cols,
                    if *running {
                        "Kill session"
                    } else {
                        "Delete session"
                    },
                );
                let content = confirm_lines(name, *running, cols);
                let top = (*top).min(content.len().saturating_sub(capacity(rows)));
                for (index, text) in content.iter().skip(top).take(capacity(rows)).enumerate() {
                    line(&mut screen, index as u16 + 2, cols, "", text);
                }
                let hint = if top + capacity(rows) < content.len() {
                    "↓ read · n / esc cancel"
                } else {
                    "y / ctrl+x confirm · n / esc cancel"
                };
                line(&mut screen, rows, cols, "\x1b[2m", hint);
            }
            None => self.roster_view(&mut screen, cols, rows),
        }
        if !self.notice.is_empty() {
            line(&mut screen, rows, cols, "\x1b[33m", &self.notice);
        }
        screen
    }

    fn roster_view(&self, screen: &mut String, cols: u16, rows: u16) {
        roster::view(self, screen, cols, rows);
    }
}

fn form_header(output: &mut String, cols: u16, title: &str) {
    line(output, 1, cols, "\x1b[38;2;142;180;230;1m", title);
}

fn automation_status(
    records: &io::Result<CatalogRecords>,
    connection: Option<&mut Client>,
) -> Option<io::Result<Option<RunState>>> {
    if let (Ok(CatalogRecords::Get(job)), Some(client)) = (records, connection)
        && client.supports(MANUAL_CAP)
    {
        Some(Manual::Status.remote(client, &job.id))
    } else {
        None
    }
}

fn scroll(offset: usize, down: bool) -> usize {
    if down {
        offset.saturating_add(1)
    } else {
        offset.saturating_sub(1)
    }
}

fn scheduled_review(row: &Value) -> bool {
    field(row, "tag") == "automation" && row["scheduled_completed"] == true
}

fn running(row: &Value) -> bool {
    field(&row["status"], "Process") == "running"
}

fn resumable(row: &Value) -> bool {
    matches!(field(row, "agent"), "agy" | "claude" | "codex" | "opencode")
        || field(row, "agent") == "hermes" && !field(row, "conversation_id").is_empty()
}

fn linked_dispatcher(row: &Value) -> bool {
    field(row, "role") == "dispatcher"
}

fn confirm_lines(label: &str, running: bool, cols: u16) -> Vec<String> {
    let verb = if running { "Kill" } else { "Delete" };
    let text = format!("{verb} {label}?");
    let mut lines = Vec::new();
    let mut rest = text.as_str();
    // Use source character boundaries so even escaped metadata cannot stall wrapping.
    let limit = usize::from(cols.max(1));
    while !rest.is_empty() {
        let mut width = 0;
        let end = rest
            .char_indices()
            .find_map(|(index, ch)| {
                width += ch.width().unwrap_or(1);
                (width > limit && index > 0).then_some(index)
            })
            .unwrap_or(rest.len());
        lines.push(rest[..end].to_owned());
        rest = &rest[end..];
    }
    lines
}

struct Editor {
    text: String,
    cursor: usize,
}

impl Editor {
    fn new(text: &str) -> Self {
        Self {
            text: text.to_owned(),
            cursor: text.len(),
        }
    }

    fn insert_text(&mut self, text: impl Iterator<Item = char>) {
        for ch in text {
            if self.text.len() + ch.len_utf8() > 16384 {
                break;
            }
            self.text.insert(self.cursor, ch);
            self.cursor += ch.len_utf8();
        }
    }

    fn apply(&mut self, key: Key) {
        const LIMIT: usize = 16384;
        let previous = self.text[..self.cursor]
            .char_indices()
            .next_back()
            .map_or(0, |(i, _)| i);
        match key {
            Key::Home | Key::EditHome => self.cursor = 0,
            Key::End | Key::EditEnd => self.cursor = self.text.len(),
            Key::Left => self.cursor = previous,
            Key::Right => {
                self.cursor += self.text[self.cursor..]
                    .chars()
                    .next()
                    .map_or(0, char::len_utf8)
            }
            Key::CtrlU => {
                self.text.drain(..self.cursor);
                self.cursor = 0;
            }
            Key::Backspace => {
                self.text.drain(previous..self.cursor);
                self.cursor = previous;
            }
            Key::Text(c) if !c.is_control() && self.text.len() + c.len_utf8() <= LIMIT => {
                self.text.insert(self.cursor, c);
                self.cursor += c.len_utf8();
            }
            Key::Paste(paste) => self.insert_text(paste.chars().filter(|ch| !ch.is_control())),
            _ => {}
        }
    }

    fn visible(&self, columns: u16) -> (String, u16) {
        let mut width = 0;
        let mut start = self.cursor;
        for (index, ch) in self.text[..self.cursor].char_indices().rev() {
            let next = width + ch.width().unwrap_or(1) as u16;
            if next >= columns {
                break;
            }
            width = next;
            start = index;
        }
        (clip(&self.text[start..], columns), width + 1)
    }
}

fn group_rank(group: &str) -> usize {
    match group {
        "needs_input" => 0,
        "working" => 1,
        "ready_for_review" => 2,
        "completed" => 3,
        _ => 4,
    }
}

fn group_name(rank: usize) -> &'static str {
    [
        "NEEDS INPUT",
        "WORKING",
        "READY FOR REVIEW",
        "COMPLETED",
        "SESSIONS",
    ][rank]
}

fn name(row: &Value) -> &str {
    let label = field(row, "name");
    if label.is_empty() {
        field(row, "agent")
    } else {
        label
    }
}

fn line(output: &mut String, row: u16, cols: u16, style: &str, text: &str) {
    let _ = write!(
        output,
        "\x1b[{row};1H\x1b[0m{style}{}\x1b[0m\x1b[K",
        clip(text, cols)
    );
}

fn clip(text: &str, columns: u16) -> String {
    let mut width = 0;
    let mut clipped = String::new();
    for ch in text.chars() {
        let ch = if ch.is_control()
            || matches!(ch, '\u{061c}' | '\u{200e}'..='\u{200f}' |
            '\u{202a}'..='\u{202e}' | '\u{2066}'..='\u{2069}')
        {
            ' '
        } else {
            ch
        };
        let cells = ch.width().unwrap_or(0);
        width += cells;
        if width > usize::from(columns) {
            break;
        }
        clipped.push(ch);
    }
    clipped
}

fn caller_launch_environment(agent: &str) -> Vec<String> {
    let entries = env::vars_os()
        .filter_map(|(key, value)| Some(format!("{}={}", key.to_str()?, value.to_str()?)))
        .collect();
    crate::environment::reliable(agent, entries)
}

fn resume(agent: &str, id: &str, cwd: &Path, (cols, rows): (u16, u16), relocate: bool) -> Action {
    let mut options = json!({"resume_from": id});
    if relocate {
        options["resume_relocate"] = json!("true");
    }
    Action::Request(
        json!({"op":"launch","launch":{
            "agent":agent, "cwd":cwd, "cols":cols, "rows":rows,
            "env":caller_launch_environment(agent), "options":options,
        }}),
        "launch",
    )
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn terminal_content_is_safe_and_cell_bounded() {
        assert_eq!(clip("a\x1b[2J", 2), "a ");
        assert_eq!(clip("a年x", 2), "a");
        assert_eq!(clip("e\u{301}年", 1), "e\u{301}");
    }

    #[test]
    fn confirmation_stays_bound_to_captured_row() {
        let mut board = Board {
            selected: "id-a".into(),
            ..Board::default()
        };
        board
            .rows
            .push(json!({"id":"id-a","name":"amber","status":{"Process":"running"}}));
        board.rebuild_sections();
        assert!(matches!(
            board.update(Key::CtrlX, (80, 24), true),
            Action::None
        ));
        board.selected = "id-b".into();
        let Action::Request(request, _) = board.update(Key::Text('y'), (80, 24), true) else {
            panic!("confirmation did not submit");
        };
        assert_eq!(request["session_id"], "id-a");
    }

    #[test]
    fn mouse_release_cannot_redirect_open_after_selection_changes() {
        let mut board = Board {
            rows: vec![
                json!({"id":"a","name":"amber","group":"working","status":{"Process":"running"}}),
                json!({"id":"b","name":"violet","group":"working","status":{"Process":"running"}}),
            ],
            ..Board::default()
        };
        board.rebuild_sections();
        let geometry = (80, 24);
        let (_, _, x, _) = board
            .button_view(80, 24)
            .into_iter()
            .find(|(_, key, ..)| *key == '⏎')
            .unwrap();
        let event = Mouse {
            x,
            y: 24,
            action: MouseAction::Press,
        };
        board.update(Key::Mouse(event), geometry, true);
        board.set_focus(Focus::Session("b".into()));
        let release = Mouse {
            action: MouseAction::Release,
            ..event
        };
        assert!(matches!(
            board.update(Key::Mouse(release), geometry, true),
            Action::None
        ));
        board.update(Key::Mouse(event), geometry, true);
        assert!(matches!(board.update(Key::Mouse(release), geometry, true),
            Action::Attach { id, .. } if id == "b"));
    }

    #[test]
    fn collapsed_groups_and_attention_keep_row_identity() {
        let mut board = Board {
            rows: vec![
                json!({"id":"a","group":"needs_input","status":{"Process":"running"}}),
                json!({"id":"b","group":"working","status":{"Process":"running"}}),
            ],
            ..Board::default()
        };
        board.rebuild_sections();
        let geometry = (80, 24);
        assert_eq!(board.current_focus(), Some(Focus::Session("a".into())));
        board.update(Key::Left, geometry, true);
        assert_eq!(
            board.current_focus(),
            Some(Focus::Section("needs_input".into()))
        );
        board.update(Key::End, geometry, true);
        assert_eq!(board.current_focus(), Some(Focus::Session("b".into())));
        board.update(Key::Text('g'), geometry, true);
        assert_eq!(board.current_focus(), Some(Focus::Session("a".into())));
        assert!(board.folded.is_empty());
    }

    #[test]
    fn mutation_refuses_a_target_whose_lifecycle_changed() {
        let mut board = Board {
            rows: vec![
                json!({"id":"a","name":"amber","group":"working","status":{"Process":"running"}}),
            ],
            ..Board::default()
        };
        board.rebuild_sections();
        let geometry = (80, 24);
        board.update(Key::CtrlX, geometry, true);
        board.rows[0]["status"]["Process"] = json!("ended");
        assert!(matches!(
            board.update(Key::Text('y'), geometry, true),
            Action::None
        ));
        assert!(board.modal.is_none());
    }

    #[test]
    fn rune_editor_inserts_and_removes_without_splitting_utf8() {
        let mut edit = Editor::new("a年z");
        edit.apply(Key::Left);
        edit.apply(Key::Backspace);
        edit.apply(Key::Paste("界\r\nx".into()));
        assert_eq!(edit.text, "a界xz");
        edit.apply(Key::Home);
        edit.apply(Key::Right);
        edit.apply(Key::CtrlU);
        assert_eq!(edit.text, "界xz");
    }
}
