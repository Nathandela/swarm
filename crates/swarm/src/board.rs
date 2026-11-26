mod attach;
mod automation;
mod context_settings;
mod discussions;
mod handoff;
mod keys;
mod launch;
mod menu;
mod navigation;
mod options;
mod preferences;
mod resume;
mod roster;
mod session_form;
mod term;
mod touch;
mod version;

use crate::ipc::{self, Client, field};
use crate::schedule::catalog::{
    CAP as CATALOG_CAP, CONTROL as CATALOG_CONTROL, Change as CatalogChange, MANUAL_CAP, Manual,
    Read as CatalogRead, Records as CatalogRecords, RunState,
};
use automation::Automation;
use handoff::Handoff;
use keys::{Key, Keys, Mouse, MouseAction};
use launch::Launch;
use navigation::{Focus, Row, usable};
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
    resume::CAP,
    handoff::CAP,
    context_settings::CAP,
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
        daemon_version: client.build_version().to_owned(),
        skew_notice: version::notice(client.build_version()),
        can_relocate: client.supports(resume::CAP),
        context_owner: context_settings::Owner::from_connection(path, &client),
        ..Board::default()
    };
    match Layout::load() {
        Ok(settings) => board.layout = settings,
        Err(error) => board.notice = format!("board layout: {error}"),
    }
    board.refresh(&mut client)?;
    let mut upgrade;
    let _terminal = term::Terminal::raw()?;
    let mut output = io::stdout().lock();
    output.write_all(term::ENTER_BOARD.as_bytes())?;
    upgrade = match version::start(path, &client) {
        Ok(pending) => pending,
        Err(error) => {
            board.note_attention(Some(format!("daemon upgrade failed: {error}")));
            None
        }
    };
    let mut upgrade_result = None;
    if upgrade.is_some() {
        board.upgrading = true;
    }
    let mut connection = upgrade.is_none().then_some(client);
    let mut geometry = term::size();
    board.columns = geometry.0;
    let clock = Instant::now();
    board.reveal_focus(geometry.1);
    let mut keys = Keys::default();
    let mut raw_input = [0; 4096];
    let mut needs_paint = true;
    let mut painted = false;
    let mut mouse_enabled = true;
    let mut previous_menu = None;
    let mut previous_editor = None;
    let mut previous_options = None;
    let mut linked_poll = discussions::Poll::default();
    let mut retry = Instant::now();
    let mut delay = Duration::from_secs(1);
    while !term::stopped() {
        let dimensions = term::size();
        if painted && let Some(result) = upgrade.as_ref().and_then(version::Upgrade::result) {
            upgrade = None;
            upgrade_result = Some(result);
            board.upgrading = false;
            retry = Instant::now();
            needs_paint = true;
        }
        if board
            .banner
            .as_ref()
            .is_some_and(|(_, expires)| Instant::now() >= *expires)
        {
            board.banner = None;
            board.press = None;
            board.clamp_top(dimensions.1);
            needs_paint = true;
        }
        if let Some(client) = &connection
            && let Some(snapshot) = linked_poll.update(path, client.endpoint())
        {
            needs_paint |= board.update_linked(snapshot, dimensions.1);
        }
        let saved_layout = match &mut board.modal {
            Some(Modal::Launch(form)) => {
                needs_paint |= form.update_detection();
                None
            }
            Some(Modal::Handoff(form)) => {
                needs_paint |= form.update_detection();
                None
            }
            Some(Modal::Options(form)) => match form.poll() {
                context_settings::Update::Pending => None,
                context_settings::Update::Changed => {
                    needs_paint = true;
                    None
                }
                context_settings::Update::Saved => Some(form.draft),
            },
            _ => None,
        };
        if let Some(layout) = saved_layout {
            board.modal = None;
            board.apply_layout(layout, dimensions.1);
            board.persist_layout(layout);
            needs_paint = true;
        }
        let spinner = (clock.elapsed().as_millis() / 90) as usize % 10;
        let second = clock.elapsed().as_secs();
        if !board.rows.is_empty()
            && (board.modal.is_none()
                || matches!(&board.modal, Some(Modal::Edit { id, .. } | Modal::Confirm { id, .. })
                    if board.inline_session_form(id, geometry)))
        {
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
                board.reflow_more(geometry.0, dimensions);
                geometry = dimensions;
                board.columns = geometry.0;
                board.press = None;
                board.reveal_focus(geometry.1);
            }
            let wants_mouse = board.wants_mouse(connection.is_some());
            if mouse_enabled != wants_mouse {
                let mode = if wants_mouse {
                    "\x1b[?1002h\x1b[?1006h"
                } else {
                    "\x1b[?1002l\x1b[?1006l"
                };
                output.write_all(mode.as_bytes())?;
                board.press = None;
                mouse_enabled = wants_mouse;
            }
            let menu = board.menu_frame(geometry);
            let editor = board.editor_frame(geometry);
            let options = board.options_frame(geometry);
            let mut display = String::new();
            if let Some(frame) = &menu {
                frame.paint(&mut display, previous_menu.as_ref());
            } else if let Some(frame) = &options {
                frame.paint(&mut display, previous_options.as_ref());
            } else if !editor
                .as_ref()
                .is_some_and(|frame| frame.paint_change(previous_editor.as_ref(), &mut display))
            {
                display = board.view(geometry);
            }
            output.write_all(display.as_bytes())?;
            previous_menu = menu;
            previous_editor = editor;
            previous_options = options;
            output.flush()?;
            painted = true;
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
                    linked_poll = discussions::Poll::default();
                    board.update_linked(None, geometry.1);
                    board.context_owner = None;
                    if let Some(Modal::Options(form)) = &mut board.modal {
                        form.disconnected();
                    }
                    board.notice = "daemon connection lost · reconnecting".to_owned();
                    retry = Instant::now() + delay;
                    needs_paint = true;
                }
            }
        } else if upgrade.is_none() && Instant::now() >= retry {
            match connect(path).and_then(|mut client| board.refresh(&mut client).map(|()| client)) {
                Ok(client) => {
                    board.notice.clear();
                    board.daemon_version = client.build_version().to_owned();
                    board.skew_notice = version::notice(client.build_version());
                    if let Some(result) = upgrade_result.take() {
                        board.note_attention(Some(match result {
                            Ok(()) => "daemon upgraded".into(),
                            Err(error) => format!("daemon upgrade failed: {error}"),
                        }));
                    }
                    board.hands_off = client.supports(handoff::CAP);
                    board.can_relocate = client.supports(resume::CAP);
                    board.context_owner = context_settings::Owner::from_connection(path, &client);
                    if let Some(Modal::Options(form)) = &mut board.modal {
                        form.reconnected(board.context_owner.as_ref());
                    }
                    connection = Some(client);
                    delay = Duration::from_secs(1);
                }
                Err(_) => {
                    if let Some(Err(error)) = upgrade_result.take() {
                        board.note_attention(Some(format!("daemon upgrade failed: {error}")));
                    }
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
            if upgrade.is_some()
                && !matches!(
                    key,
                    Key::Escape
                        | Key::CtrlC
                        | Key::Up
                        | Key::Down
                        | Key::Home
                        | Key::End
                        | Key::PageUp
                        | Key::PageDown
                )
            {
                continue;
            }
            let (action, resume) = match board.update(key, geometry, connection.is_some()) {
                Action::Resume(resume) => {
                    (Action::Request(resume.message(), "launch"), Some(resume))
                }
                other => (other, None),
            };
            match action {
                Action::Resume(_) => unreachable!("resume normalized to owner request"),
                Action::None => {}
                Action::Quit => return Ok(()),
                Action::Retry => {
                    retry = Instant::now();
                    delay = Duration::from_secs(1);
                }
                Action::SaveLayout(layout) => board.persist_layout(layout),
                Action::Discussion { row, binding, op } => {
                    if let Some(local) = &connection {
                        let result =
                            discussions::revalidate(path, local.endpoint(), &binding, &row);
                        let id = field(&row, "id");
                        match (op, result) {
                            ("attach", Ok(client)) => {
                                let attached = attach::run_client(
                                    client,
                                    id,
                                    name(&row),
                                    board.layout.touch_back,
                                    false,
                                    &mut output,
                                );
                                if let Err(error) = attached {
                                    board.notice = error.to_string();
                                }
                                board.restore_terminal(&mut output)?;
                                previous_menu = None;
                                previous_editor = None;
                                previous_options = None;
                                linked_poll = discussions::Poll::default();
                                needs_paint = true;
                                break;
                            }
                            (op, Ok(mut client)) => {
                                if let Err(error) = client.exchange(
                                    json!({"op":op,"session_id":id}),
                                    "ok",
                                    Duration::from_secs(2),
                                ) {
                                    board.notice = error.to_string();
                                }
                                linked_poll = discussions::Poll::default();
                            }
                            (_, Err(error)) => board.notice = error.to_string(),
                        }
                    }
                }
                Action::Attach { id, name } => {
                    if connection.is_some() {
                        board.attach(path, &id, &name, &mut output)?;
                        previous_menu = None;
                        previous_editor = None;
                        previous_options = None;
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
                                linked_poll = discussions::Poll::default();
                                board.update_linked(None, geometry.1);
                                board.notice = "daemon connection lost · reconnecting".to_owned();
                                retry = Instant::now() + delay;
                            } else if let Some(label) = label
                                && !id.is_empty()
                            {
                                let actual = field(&reply["session"], "name");
                                board.attach(
                                    path,
                                    id,
                                    if actual.is_empty() { &label } else { actual },
                                    &mut output,
                                )?;
                                needs_paint = true;
                                break;
                            }
                        }
                        Err(error) => {
                            if let Some(attempt) = resume {
                                board.resume_failed(*attempt, &error.to_string());
                            } else {
                                board.notice = error.to_string();
                            }
                        }
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
    linked: Option<discussions::Snapshot>,
    roster_revision: u64,
    columns: u16,
    spinner: usize,
    hands_off: bool,
    can_relocate: bool,
    context_owner: Option<context_settings::Owner>,
    second: u64,
    selected: String,
    focused_section: Option<String>,
    folded: BTreeSet<(usize, String)>,
    sections: Vec<(String, Vec<usize>)>,
    top: usize,
    layout: Layout,
    press: Option<Press>,
    notice: String,
    daemon_version: String,
    skew_notice: Option<String>,
    upgrading: bool,
    banner: Option<(String, Instant)>,
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
    MenuControl(char),
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
    Relocate(Box<resume::Relocation>),
    Options(Box<options::Form>),
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
        top: usize,
    },
    Confirm {
        id: String,
        running: bool,
        linked: Option<(crate::discord::linked_board::Binding, Box<Value>)>,
        top: usize,
    },
}

enum Action {
    None,
    Quit,
    Attach {
        id: String,
        name: String,
    },
    Discussion {
        row: Box<Value>,
        binding: crate::discord::linked_board::Binding,
        op: &'static str,
    },
    Retry,
    SaveLayout(Layout),
    Catalog(CatalogRead),
    ChangeAutomation(CatalogChange),
    TriggerAutomation {
        job: String,
        intent: Manual,
    },
    Request(Value, &'static str),
    Resume(Box<resume::Request>),
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
                Row::Session(row)
                    if running(row) && self.linked_binding(field(row, "id")).is_none() =>
                {
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
        self.restore_terminal(output)
    }

    fn restore_terminal(&self, output: &mut impl Write) -> io::Result<()> {
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
        let mut rows = discussions::project(client.sessions()?, client.endpoint(), false);
        if let Some(linked) = &self.linked {
            rows.extend(linked.rows.iter().cloned());
        }
        self.note_attention(roster::attention_change(&self.rows, &rows));
        self.rows = rows;
        self.roster_revision = self.roster_revision.wrapping_add(1);
        self.rebuild_sections();
        Ok(())
    }

    fn note_attention(&mut self, text: Option<String>) {
        if let Some(text) = text {
            self.banner = Some((text, Instant::now() + Duration::from_secs(4)));
            self.press = None;
        }
    }

    fn linked_binding(&self, id: &str) -> Option<&crate::discord::linked_board::Binding> {
        self.linked
            .as_ref()
            .filter(|linked| linked.rows.iter().any(|row| field(row, "id") == id))
            .map(|linked| &linked.binding)
    }

    fn update_linked(&mut self, next: Option<discussions::Snapshot>, height: u16) -> bool {
        if self.linked.as_ref().map(|s| (&s.binding, &s.rows))
            == next.as_ref().map(|s| (&s.binding, &s.rows))
        {
            return false;
        }
        if let Some(next) = &next {
            self.note_attention(roster::attention_change(&self.rows, &next.rows));
        }
        if let Some(previous) = self.linked.take() {
            let endpoint = &previous.binding.endpoint;
            self.rows
                .retain(|row| field(row, "endpoint_id") != endpoint);
            if let Some(Modal::More { target, .. }) = &self.modal
                && let Some(old) = previous.rows.iter().find(|row| field(row, "id") == target)
                && next.as_ref().is_none_or(|fresh| {
                    fresh.binding != previous.binding
                        || !fresh
                            .rows
                            .iter()
                            .any(|row| discussions::same_target(old, row))
                })
            {
                self.modal = None;
                self.notice = "Linked dispatcher or target changed; reopen the action".into();
            }
        }
        let at_top = self.top == 0;
        self.linked = next;
        if let Some(linked) = &self.linked {
            self.rows.extend(linked.rows.iter().cloned());
        }
        self.press = None;
        self.roster_revision = self.roster_revision.wrapping_add(1);
        self.rebuild_sections();
        if !at_top {
            self.reveal_focus(height);
        }
        true
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
        let step = self.body_capacity(rows) as isize;
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
                let entries = self.menu(&target, running, geometry.0);
                let mut selected = entries
                    .iter()
                    .position(|entry| entry.1.is_some())
                    .unwrap_or(0);
                let mut top = 0;
                menu::reveal(
                    &mut selected,
                    &mut top,
                    entries.len(),
                    menu::capacity(geometry),
                );
                self.modal = Some(Modal::More {
                    target,
                    running,
                    selected,
                    top,
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
                self.modal = Some(Modal::Options(Box::new(options::Form::new(
                    self.layout,
                    self.context_owner.clone().filter(|_| online),
                ))));
                self.notice.clear();
            }
            Key::Text('a') => {
                self.modal = Some(Modal::Automation(Box::default()));
                self.notice.clear();
                return Action::Catalog(CatalogRead::List);
            }
            Key::Text('c') if online => {
                if let Some(row) = self.by_id(id).filter(|_| self.linked_binding(id).is_none()) {
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
                    if let Some(binding) = self.linked_binding(id) {
                        if running(row) {
                            return Action::Discussion {
                                row: Box::new(row.clone()),
                                binding: binding.clone(),
                                op: "attach",
                            };
                        }
                        self.notice = "This process has exited. Continue the discussion in its Discord thread.".into();
                        return Action::None;
                    }
                    if running(row) {
                        return Action::Attach {
                            id: id.to_owned(),
                            name: name(row).to_owned(),
                        };
                    }
                    if scheduled_review(row) {
                        return self.begin_resume(id, geometry);
                    }
                    self.notice = if resumable(row) {
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
                if let Some(source) = self.by_id(id).filter(|_| self.linked_binding(id).is_none()) {
                    self.modal = Some(Modal::Handoff(Box::new(Handoff::new(source))));
                    self.notice.clear();
                }
            }
            Key::Text('r') => return self.begin_resume(id, geometry),
            Key::Text('e' | 't') => {
                if let Some(row) = self.by_id(id).filter(|_| self.linked_binding(id).is_none()) {
                    let tag = matches!(key, Key::Text('t'));
                    self.modal = Some(Modal::Edit {
                        id: id.to_owned(),
                        tag,
                        draft: Editor::new(field(row, if tag { "tag" } else { "name" })),
                        top: 0,
                    });
                }
            }
            Key::CtrlX => {
                if let Some(row) = self.by_id(id) {
                    self.modal = Some(Modal::Confirm {
                        id: id.to_owned(),
                        running: running(row),
                        linked: self
                            .linked_binding(id)
                            .map(|binding| (binding.clone(), Box::new(row.clone()))),
                        top: 0,
                    });
                }
            }
            _ => {}
        }
        Action::None
    }

    fn begin_resume(&mut self, id: &str, geometry: (u16, u16)) -> Action {
        let Some(row) = self
            .by_id(id)
            .filter(|row| !running(row) && self.linked_binding(id).is_none())
        else {
            return Action::None;
        };
        if !resumable(row) {
            self.notice = "No supported native conversation is bound to this session".into();
            return Action::None;
        }
        let source = resume::Source::from_row(row);
        if source.scheduled || source.directory().is_dir() {
            return Action::Resume(Box::new(resume::Request::original(source, geometry)));
        }
        if source.can_relocate(self.can_relocate) {
            self.modal = Some(Modal::Relocate(Box::new(resume::Relocation::missing(
                source,
            ))));
            self.notice.clear();
        } else {
            self.notice = format!(
                "Resume unavailable: the working directory is gone and {} relocation is unsupported",
                field(row, "agent")
            );
        }
        Action::None
    }

    fn resume_failed(&mut self, request: resume::Request, error: &str) {
        if request.source.scheduled {
            self.notice = format!("Unable to reopen scheduled conversation: {error}");
        } else if request.source.can_relocate(self.can_relocate)
            && self
                .by_id(&request.source.id)
                .is_some_and(|row| request.source.matches(row))
        {
            self.modal = Some(Modal::Relocate(Box::new(request.rejected(error))));
            self.notice.clear();
        } else {
            self.notice = format!("Resume failed: {error}");
        }
    }

    fn apply_layout(&mut self, layout: Layout, rows: u16) {
        self.layout = layout;
        self.rebuild_sections();
        self.reveal_focus(rows);
        self.notice.clear();
    }

    fn persist_layout(&mut self, layout: Layout) {
        if let Err(error) = layout.save() {
            self.notice = format!("board layout: {error}");
        }
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
                    form.update(key, geometry)
                }
            }
            Modal::Handoff(form) => {
                let source = self.by_id(&form.source_id);
                form.update(key, source, geometry, self.hands_off, online)
            }
            Modal::Relocate(form) => {
                form.update(key, geometry, self.by_id(&form.source.id), online)
            }
            Modal::Automation(view) => {
                let action = view.update(key, geometry);
                self.modal = Some(modal);
                return action;
            }
            Modal::Options(form) => {
                if form.input(key, geometry, online) {
                    self.apply_layout(form.draft, rows);
                    return Action::SaveLayout(form.draft);
                }
                Action::None
            }
            Modal::More {
                target,
                running,
                selected,
                top,
            } => {
                let entries = self.menu(target, *running, geometry.0);
                let budget = menu::capacity(geometry);
                let step = budget as isize;
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
                    menu::reveal(selected, top, entries.len(), budget);
                } else if usable(geometry) {
                    let chosen = match key {
                        Key::Enter if (*top..*top + budget).contains(selected) => {
                            entries[*selected].1
                        }
                        Key::Enter => {
                            menu::reveal(selected, top, entries.len(), budget);
                            None
                        }
                        Key::CtrlX => Some('x'),
                        Key::Text(ch) if ch != 'x' => Some(ch),
                        _ => None,
                    };
                    if let Some(chosen) = chosen
                        && entries.iter().any(|entry| entry.1 == Some(chosen))
                        && (online || matches!(chosen, 'q' | 'a'))
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
            Modal::Edit {
                id,
                tag,
                draft,
                top,
            } => {
                let inline = self.inline_session_form(id, geometry);
                let row = self.by_id(id);
                let sheet = (!inline).then(|| {
                    session_form::Sheet::new(
                        geometry,
                        id,
                        row,
                        session_form::Kind::Edit {
                            tag: *tag,
                            editor: draft,
                        },
                    )
                });
                if sheet.as_ref().is_some_and(|sheet| sheet.scroll(top, &key)) {
                    Action::None
                } else if matches!(key, Key::Enter) {
                    if online
                        && row.is_some()
                        && sheet.as_ref().is_none_or(|sheet| sheet.can_submit(*top))
                    {
                        let (op, key, text) = if *tag {
                            ("set_tag", "tag", draft.text.trim())
                        } else {
                            ("rename", "name", draft.text.as_str())
                        };
                        let mut message = json!({"op":op, "session_id":id});
                        message[key] = json!(text);
                        return Action::Request(message, "ok");
                    }
                    Action::None
                } else {
                    draft.apply(key);
                    Action::None
                }
            }
            Modal::Confirm {
                id,
                running: was_running,
                linked,
                top,
            } => {
                let row = self.by_id(id);
                let inline = self.inline_session_form(id, geometry);
                let sheet = (!inline).then(|| {
                    session_form::Sheet::new(
                        geometry,
                        id,
                        row,
                        session_form::Kind::Confirm {
                            running: *was_running,
                        },
                    )
                });
                if key == Key::Text('n') {
                    return Action::None;
                }
                if sheet.as_ref().is_some_and(|sheet| sheet.scroll(top, &key)) {
                    Action::None
                } else if online
                    && matches!(key, Key::Text('y') | Key::CtrlX)
                    && sheet.as_ref().is_none_or(|sheet| sheet.can_submit(*top))
                {
                    if row.is_some_and(|row| running(row) == *was_running) {
                        let op = if *was_running { "kill" } else { "delete" };
                        if let Some((binding, captured)) = linked {
                            if self.linked_binding(id) == Some(binding)
                                && row.is_some_and(|row| discussions::same_target(captured, row))
                            {
                                return Action::Discussion {
                                    row: captured.clone(),
                                    binding: binding.clone(),
                                    op,
                                };
                            }
                        } else if self.linked_binding(id).is_none() {
                            return Action::Request(json!({"op":op,"session_id":id}), "ok");
                        }
                    }
                    self.notice = "Target changed; reopen confirmation".into();
                    return Action::None;
                } else {
                    Action::None
                }
            }
        };
        if matches!(action, Action::None) {
            self.modal = Some(modal);
        } else {
            self.notice.clear();
        }
        action
    }

    fn menu(&self, target: &str, expected_running: bool, cols: u16) -> Vec<(String, Option<char>)> {
        let mut entries = Vec::new();
        if let Some(row) = self.by_id(target) {
            let state = roster::status(row, false);
            let mut context = vec![
                format!("Name: {}", name(row)),
                format!("State: {state}"),
                format!("Agent: {}", field(row, "agent")),
                format!("Repo: {}", field(row, "cwd")),
                format!("ID: {target}"),
            ];
            for (property, label) in [
                ("backend_plan_error", "No backend"),
                ("resume_error", "Resume failed"),
            ] {
                let reason = field(row, property);
                if !reason.is_empty() {
                    context.push(format!("{label}: {reason}"));
                }
            }
            if row["supervision_pending"] == true {
                context.push(format!("Supervision: {}", roster::supervision(self, row)));
            }
            entries.extend(
                context
                    .iter()
                    .flat_map(|text| menu::wrap(text, cols.saturating_sub(2)))
                    .map(|text| (text, None)),
            );
            let unchanged = running(row) == expected_running;
            if unchanged && (expected_running || scheduled_review(row)) {
                entries.push(("Open".into(), Some('⏎')));
            }
            let linked = self.linked_binding(target).is_some();
            if !linked {
                entries.extend([("Rename".into(), Some('e')), ("Tag".into(), Some('t'))]);
                if unchanged && !expected_running && !scheduled_review(row) && resumable(row) {
                    entries.push(("Resume".into(), Some('r')));
                }
                entries.push(("Handoff".into(), Some('h')));
                if unchanged {
                    let label = if expected_running { "Kill" } else { "Delete" };
                    entries.push((label.into(), Some('x')));
                }
                if !field(row, "cwd").is_empty() {
                    entries.push(("Schedule in this directory".into(), Some('c')));
                }
            }
            if linked && unchanged {
                entries.push((
                    if expected_running { "Kill" } else { "Delete" }.into(),
                    Some('x'),
                ));
            }
            if !unchanged {
                entries.push(("Target state changed; reopen More".into(), None));
            }
        } else if !target.is_empty() {
            entries.push(("Target disappeared".into(), None));
        }
        entries.extend([
            ("Automations".into(), Some('a')),
            ("New".into(), Some('n')),
            ("Options".into(), Some('o')),
            ("Attention".into(), Some('g')),
            ("Quit".into(), Some('q')),
        ]);
        entries
    }

    fn reflow_more(&mut self, previous_cols: u16, geometry: (u16, u16)) {
        let Some(Modal::More {
            target,
            running,
            selected,
            ..
        }) = &self.modal
        else {
            return;
        };
        let key = self
            .menu(target, *running, previous_cols)
            .get(*selected)
            .and_then(|entry| entry.1);
        let entries = self.menu(target, *running, geometry.0);
        let position = key.and_then(|key| entries.iter().position(|entry| entry.1 == Some(key)));
        if let Some(Modal::More { selected, top, .. }) = &mut self.modal {
            if let Some(position) = position {
                *selected = position;
            }
            menu::reveal(selected, top, entries.len(), menu::capacity(geometry));
        }
    }

    fn buttons(&self, cols: u16, rows: u16) -> Vec<(&'static str, char)> {
        let mut buttons = vec![("New (n)", 'n')];
        let target = self.row();
        if self.focused_section.is_some() {
            buttons.push(("Expand (⏎)", '⏎'));
        } else if let Some(row) = target {
            if !self.visible(rows) {
                buttons.push(("Show (⏎)", '⏎'));
            } else if running(row)
                || scheduled_review(row)
                || self.linked_binding(field(row, "id")).is_some()
            {
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
        if let Some(row) = target.filter(|_| self.visible(rows)) {
            if self.linked_binding(field(row, "id")).is_none() {
                buttons.extend([("Rename (e)", 'e'), ("Tag (t)", 't'), ("Handoff (h)", 'h')]);
            }
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
            for item in menu::controls(geometry) {
                if event.y == item.y
                    && (item.x..item.x + item.text.width() as u16).contains(&event.x)
                {
                    return Some(Hit::MenuControl(item.key));
                }
            }
            if !(2..2 + menu::capacity(geometry) as u16).contains(&event.y) || event.x < 3 {
                return None;
            }
            let index = *top + usize::from(event.y - 2);
            let entries = self.menu(target, *was_running, cols);
            let (text, key) = entries.get(index)?;
            let key = (*key)?;
            let width = menu::button(text, key).width() as u16;
            if event.x >= 3 + width {
                return None;
            }
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
        let first = self.body_first(rows);
        if event.y < first || usize::from(event.y - first) >= self.body_capacity(rows) {
            return None;
        }
        let row = self
            .display_rows()
            .into_iter()
            .nth(self.top + usize::from(event.y - first))?;
        row.focus()
            .map(|focus| Hit::Row(focus, matches!(row, Row::Session(row) if running(row))))
    }

    fn wants_mouse(&self, online: bool) -> bool {
        matches!(self.modal, Some(Modal::Automation(_)))
            || online && matches!(self.modal, None | Some(Modal::More { .. }))
    }

    fn mouse(&mut self, event: Mouse, geometry: (u16, u16), online: bool) -> Action {
        if let Some(Modal::Automation(view)) = &mut self.modal {
            let action = view.mouse(event, geometry);
            return action.map_or(Action::None, |key| self.update(key, geometry, online));
        }
        if !online || !matches!(self.modal, None | Some(Modal::More { .. })) || !usable(geometry) {
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
                    self.body_first(geometry.1)
                };
                let end = if self.modal.is_some() {
                    2 + menu::capacity(geometry) as u16
                } else {
                    first + self.body_capacity(geometry.1) as u16
                };
                if (first..end).contains(&event.y) {
                    let down = event.action == MouseAction::WheelDown;
                    if let Some(Modal::More {
                        target, running, ..
                    }) = &self.modal
                    {
                        let max = self
                            .menu(target, *running, geometry.0)
                            .len()
                            .saturating_sub(menu::capacity(geometry));
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
                        Hit::MenuControl(key) => {
                            let key = match key {
                                '↑' => Key::Up,
                                '↓' => Key::Down,
                                _ => Key::Escape,
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

    fn menu_frame(&self, geometry: (u16, u16)) -> Option<menu::Frame> {
        if self.notice.is_empty() && usable(geometry) {
            self.build_menu_frame(geometry)
        } else {
            None
        }
    }

    fn build_menu_frame(&self, geometry: (u16, u16)) -> Option<menu::Frame> {
        let Some(Modal::More {
            target,
            running,
            selected,
            top,
        }) = &self.modal
        else {
            return None;
        };
        let title = if let Some(row) = self.by_id(target) {
            format!("More · {} · {}", name(row), roster::status(row, false))
        } else if target.is_empty() {
            "More".into()
        } else {
            "More · target disappeared".into()
        };
        Some(menu::Frame::new(
            geometry,
            &title,
            self.menu(target, *running, geometry.0),
            *selected,
            *top,
        ))
    }

    fn inline_session_row(&self, target: &str, (cols, rows): (u16, u16)) -> Option<(u16, &Value)> {
        if cols < 64 || rows < 10 {
            return None;
        }
        self.display_rows()
            .into_iter()
            .skip(self.top)
            .take(self.body_capacity(rows))
            .enumerate()
            .find_map(|(index, row)| match row {
                Row::Session(row) if field(row, "id") == target => {
                    Some((self.body_first(rows) + index as u16, row))
                }
                _ => None,
            })
    }

    fn inline_session_form(&self, target: &str, geometry: (u16, u16)) -> bool {
        self.inline_session_row(target, geometry).is_some()
    }

    fn options_frame(&self, geometry: (u16, u16)) -> Option<options::Frame> {
        let Some(Modal::Options(form)) = &self.modal else {
            return None;
        };
        (self.notice.is_empty() && usable(geometry)).then(|| form.frame(geometry))
    }

    fn editor_frame(&self, geometry: (u16, u16)) -> Option<session_form::EditorFrame> {
        let Some(Modal::Edit {
            id,
            tag,
            draft,
            top,
        }) = &self.modal
        else {
            return None;
        };
        if !usable(geometry) || !self.notice.is_empty() {
            return None;
        }
        let inline = self.inline_session_row(id, geometry);
        let (cols, _) = geometry;
        let (x, y, style, text) = if let Some((y, row)) = inline {
            (
                5,
                y,
                roster::BOLD,
                roster::edit_label(row, draft, *tag, cols),
            )
        } else {
            let (x, text) = session_form::edit_text(draft, *tag, cols);
            (x, 2, launch::PLAIN, text)
        };
        let basis = session_form::EditorBasis {
            geometry,
            target: id.clone(),
            tag: *tag,
            top: *top,
            revision: self.roster_revision,
            tick: (inline.is_some()
                && self.rows.iter().any(|row| field(row, "group") == "working"))
            .then_some(self.spinner),
            second: (inline.is_some() && !roster::compact(cols)).then_some(self.second),
        };
        Some(session_form::EditorFrame {
            basis,
            x,
            y,
            style,
            text: clip(&text, cols.saturating_sub(x - 1)),
        })
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
            Some(Modal::Relocate(form)) => form.view(&mut screen, (cols, rows)),
            Some(Modal::Options(form)) => form.frame((cols, rows)).paint(&mut screen, None),
            Some(Modal::More { .. }) => {
                self.build_menu_frame((cols, rows))
                    .expect("More open")
                    .paint(&mut screen, None);
            }
            Some(Modal::Edit {
                id,
                tag,
                draft,
                top,
            }) => {
                if self.inline_session_form(id, (cols, rows)) {
                    self.roster_view(&mut screen, cols, rows);
                } else {
                    session_form::Sheet::new(
                        (cols, rows),
                        id,
                        self.by_id(id),
                        session_form::Kind::Edit {
                            tag: *tag,
                            editor: draft,
                        },
                    )
                    .paint(&mut screen, *top);
                }
            }
            Some(Modal::Confirm {
                id, running, top, ..
            }) => {
                if self.inline_session_form(id, (cols, rows)) {
                    self.roster_view(&mut screen, cols, rows);
                } else {
                    session_form::Sheet::new(
                        (cols, rows),
                        id,
                        self.by_id(id),
                        session_form::Kind::Confirm { running: *running },
                    )
                    .paint(&mut screen, *top);
                }
            }
            None => self.roster_view(&mut screen, cols, rows),
        }
        if !self.notice.is_empty() {
            line(&mut screen, rows, cols, "\x1b[33m", &self.notice);
        }
        screen
    }

    fn upgrade_banner(&self, height: u16) -> Vec<String> {
        if !self.upgrading {
            return Vec::new();
        }
        let title = format!(
            "● upgrading daemon {} -> {}...",
            self.daemon_version,
            version::CLIENT
        );
        session_form::word_wrap(&title, self.columns.saturating_sub(2))
            .into_iter()
            .take(usize::from(height.saturating_sub(3)))
            .collect()
    }

    fn version_notice(&self, height: u16) -> Option<&str> {
        if height.saturating_sub(self.body_first(height)) < 2 {
            return None;
        }
        self.skew_notice.as_deref()
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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn scheduled_enter_retains_captured_review_even_with_missing_directory() {
        let mut board = Board {
            rows: vec![json!({
                "id":"scheduled", "agent":"agy", "name":"Review fixture", "cwd":"/unavailable/fixture",
                "tag":"automation", "scheduled_completed":true, "group":"completed",
                "status":{"Process":"exited"},
            })],
            can_relocate: true,
            ..Board::default()
        };
        board.rebuild_sections();
        let Action::Resume(request) = board.update(Key::Enter, (80, 24), true) else {
            panic!("scheduled review did not request native resume");
        };
        let message = request.message();
        assert_eq!(
            message["launch"]["options"],
            json!({"resume_from":"scheduled"})
        );
        assert_eq!(message["launch"]["name"], "Review fixture");
        assert!(board.modal.is_none());
    }

    #[test]
    fn removed_relocation_target_cannot_become_the_next_selected_row() {
        let mut board = Board {
            rows: vec![
                json!({"id":"source","agent":"agy","name":"Source","cwd":"/unavailable/fixture",
                       "group":"completed","status":{"Process":"exited"}}),
                json!({"id":"neighbor","agent":"agy","name":"Neighbor","cwd":"/",
                       "group":"completed","status":{"Process":"exited"}}),
            ],
            can_relocate: true,
            ..Board::default()
        };
        board.rebuild_sections();
        board.set_focus(Focus::Session("source".into()));
        assert!(matches!(
            board.update(Key::Text('r'), (80, 24), true),
            Action::None
        ));
        board.rows.retain(|row| field(row, "id") != "source");
        board.rebuild_sections();
        assert!(matches!(
            board.update(Key::Enter, (80, 24), true),
            Action::None
        ));
        assert!(
            board
                .view((80, 24))
                .contains("Resume source changed or disappeared")
        );
        board.update(Key::Escape, (80, 24), true);
        let Action::Resume(request) = board.update(Key::Text('r'), (80, 24), true) else {
            panic!("freshly selected neighbor did not request resume");
        };
        assert_eq!(
            request.message()["launch"]["options"],
            json!({"resume_from":"neighbor"})
        );
    }

    #[test]
    fn missing_source_directory_opens_the_selector_only_with_negotiated_relocation() {
        let mut board = Board {
            rows: vec![json!({
                "id":"claude", "agent":"claude", "name":"Source", "cwd":"/unavailable/fixture",
                "group":"completed", "status":{"Process":"exited"},
            })],
            can_relocate: true,
            ..Board::default()
        };
        board.rebuild_sections();
        assert!(matches!(
            board.update(Key::Text('r'), (80, 24), true),
            Action::None
        ));
        assert!(matches!(board.modal, Some(Modal::Relocate(_))));
        board.modal = None;
        board.can_relocate = false;
        assert!(matches!(
            board.update(Key::Text('r'), (80, 24), true),
            Action::None
        ));
        assert!(board.modal.is_none());
        assert!(!board.notice.is_empty());
    }

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
