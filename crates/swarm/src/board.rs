mod attach;
mod keys;
mod navigation;
mod preferences;
mod term;

use crate::ipc::{self, Client, field};
use keys::{Key, Keys, Mouse, MouseAction};
use navigation::{Focus, GROUPS, ORDERS, Row, capacity, truncation, usable};
use preferences::Layout;
use serde_json::{Value, json};
use std::collections::BTreeSet;
use std::env;
use std::fmt::Write as _;
use std::io::{self, Write};
use std::os::fd::AsRawFd;
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};
use unicode_width::UnicodeWidthChar;

const POLL: Duration = Duration::from_millis(40);
const ROSTER_CAPS: &[&str] = &["subscribe", "opencode-observation"];

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
    let mut board = Board::default();
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
    board.reveal_focus(geometry.1);
    let mut keys = Keys::default();
    let mut raw_input = [0; 4096];
    let mut needs_paint = true;
    let mut retry = Instant::now();
    let mut delay = Duration::from_secs(1);
    while !term::stopped() {
        let dimensions = term::size();
        if dimensions != geometry || needs_paint {
            if dimensions != geometry {
                geometry = dimensions;
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
                Action::Request(message, target) => {
                    let Some(client) = &mut connection else {
                        continue;
                    };
                    let label = if target == "launch" {
                        Some(name(&message["launch"]).to_owned())
                    } else {
                        None
                    };
                    match client.request_body(message, target) {
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
    Row(Focus),
    Button(char),
}

struct Press {
    hit: Hit,
    invoke_on_release: bool,
}

enum Modal {
    Launch(Launch),
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
    Request(Value, &'static str),
}

impl Board {
    fn attach(
        &mut self,
        path: &Path,
        id: &str,
        name: &str,
        output: &mut impl Write,
    ) -> io::Result<()> {
        let result = attach::run(path, id, name, output);
        output.write_all(term::RESTORE.as_bytes())?;
        if !term::stopped() {
            output.write_all(term::ENTER_BOARD.as_bytes())?;
        }
        if let Err(error) = result {
            self.notice = error.to_string();
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
        if let Some(modal) = self.modal.take() {
            if matches!(key, Key::Escape) {
                self.notice.clear();
                return Action::None;
            }
            return self.update_modal(modal, key, geometry, online);
        }
        let step = capacity(rows) as isize;
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
        if matches!(key, Key::Enter | Key::CtrlX | Key::Text('e' | 't'))
            && self.current_focus().is_some()
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
            Key::Text('n' | 'o') | Key::Enter | Key::CtrlX | Key::Text('e' | 't') => {
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
                self.modal = Some(Modal::Launch(Launch::new()));
                self.notice.clear();
            }
            Key::Text('o') => {
                self.modal = Some(Modal::Options {
                    draft: self.layout,
                    focus: 0,
                });
                self.notice.clear();
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
                    } else {
                        "Session ended · ctrl+x delete".into()
                    };
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
                if matches!(key, Key::Enter) && (!online || !usable(geometry)) {
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
            Modal::Options { draft, focus } => {
                match key {
                    Key::Up => *focus = (*focus + 1) % 2,
                    Key::Down | Key::Tab => *focus = (*focus + 1) % 2,
                    Key::Left | Key::Right => {
                        let (selected, len) = if *focus == 0 {
                            (&mut draft.group, GROUPS.len())
                        } else {
                            (&mut draft.order, ORDERS.len())
                        };
                        let offset = if matches!(key, Key::Left) { len - 1 } else { 1 };
                        *selected = (*selected + offset) % len;
                    }
                    Key::Enter if usable(geometry) => {
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
        self.modal = Some(modal);
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
                entries.extend([("Rename".into(), Some('e')), ("Tag".into(), Some('t'))]);
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
            ("Attention".into(), Some('g')),
            ("Quit".into(), Some('q')),
        ]);
        entries
    }

    fn buttons(&self, cols: u16) -> Vec<(&'static str, char)> {
        if cols < 50 {
            return vec![("New (n)", 'n'), ("More (m)", 'm')];
        }
        let mut buttons = vec![("New (n)", 'n')];
        if self.row().is_some_and(running) {
            buttons.push(("Open (⏎)", '⏎'));
        }
        buttons.push(("More (m)", 'm'));
        if let Some(row) = self.row().filter(|row| !linked_dispatcher(row)) {
            if cols >= 64 {
                buttons.push(("Rename (e)", 'e'));
                buttons.push(("Tag (t)", 't'));
            }
            let lifecycle = if running(row) {
                "Kill (ctrl+x)"
            } else {
                "Delete (ctrl+x)"
            };
            buttons.push((lifecycle, 'x'));
        }
        buttons
    }

    fn button_view(&self, cols: u16) -> Vec<(String, char, u16, u16)> {
        let mut x = 1;
        let mut bars = Vec::new();
        for (entry, key) in self.buttons(cols) {
            let text = format!("[{entry}]");
            let right = x + (text.chars().count() as u16) - 1;
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
        if event.x > cols || event.y < 2 || event.y > rows {
            return None;
        }
        if event.y == rows {
            return self
                .button_view(cols)
                .into_iter()
                .find(|(_, _, left, right)| (*left..=*right).contains(&event.x))
                .map(|(_, button, _, _)| Hit::Button(button));
        }
        self.display_rows()
            .get(self.top + usize::from(event.y - 2))
            .map(|row| Hit::Row(row.focus()))
    }

    fn mouse(&mut self, event: Mouse, geometry: (u16, u16), online: bool) -> Action {
        if self.modal.is_some() || !usable(geometry) {
            self.press = None;
            return Action::None;
        }
        let hit = self.hit(event, geometry);
        match event.action {
            MouseAction::Drag => self.press = None,
            MouseAction::WheelUp | MouseAction::WheelDown => {
                self.press = None;
                if (2..geometry.1).contains(&event.y) {
                    self.top = if event.action == MouseAction::WheelUp {
                        self.top.saturating_sub(3)
                    } else {
                        self.top + 3
                    };
                    self.clamp_top(geometry.1);
                }
            }
            MouseAction::Press => {
                if let Some(hit) = hit {
                    let invoke_on_release = match &hit {
                        Hit::Row(focus @ Focus::Session(_)) => {
                            let already = self.current_focus().as_ref() == Some(focus);
                            self.set_focus(focus.clone());
                            already
                        }
                        _ => true,
                    };
                    self.press = Some(Press {
                        hit,
                        invoke_on_release,
                    });
                } else {
                    self.press = None;
                }
            }
            MouseAction::Release => {
                if let Some(press) = self.press.take()
                    && press.invoke_on_release
                    && hit == Some(press.hit.clone())
                {
                    match press.hit {
                        Hit::Row(Focus::Session(id)) => {
                            return self.command(Key::Enter, &id, geometry, online);
                        }
                        Hit::Row(Focus::Section(section)) => {
                            let collapse =
                                !self.folded.contains(&(self.layout.group, section.clone()));
                            self.fold(Some(section), collapse, geometry.1);
                        }
                        Hit::Button(button) => {
                            let key = match button {
                                '⏎' => Key::Enter,
                                'x' => Key::CtrlX,
                                ch => Key::Text(ch),
                            };
                            return self.update(key, geometry, online);
                        }
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
            Some(Modal::Options { draft, focus }) => {
                form_header(&mut screen, cols, "Options");
                for (index, (label, value)) in [
                    ("Group", GROUPS[draft.group]),
                    ("Order", ORDERS[draft.order]),
                ]
                .into_iter()
                .enumerate()
                {
                    let marker = if index == *focus { "▌" } else { " " };
                    line(
                        &mut screen,
                        index as u16 + 2,
                        cols,
                        "",
                        &format!("{marker} {label}  ‹ {value} ›"),
                    );
                }
                line(
                    &mut screen,
                    rows,
                    cols,
                    "\x1b[2m",
                    "↑↓ field · ←→ change · enter save · esc cancel",
                );
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
        let input = self
            .rows
            .iter()
            .filter(|row| field(row, "group") == "needs_input")
            .count();
        let review = self
            .rows
            .iter()
            .filter(|row| field(row, "group") == "ready_for_review")
            .count();
        let header = format!(
            "swarm · {input} input · {review} review · group: {} · order: {}",
            GROUPS[self.layout.group], ORDERS[self.layout.order]
        );
        form_header(screen, cols, &header);
        let focus = self.current_focus();
        let display = self.display_rows();
        if display.is_empty() {
            line(
                screen,
                2,
                cols,
                "\x1b[2m",
                "No sessions · press n to launch one",
            );
        }
        for (index, row) in display
            .iter()
            .skip(self.top)
            .take(capacity(rows))
            .enumerate()
        {
            let selected = focus.as_ref() == Some(&row.focus());
            let marker = if selected { "▌" } else { " " };
            let text = match row {
                Row::Section { id, count, folded } => {
                    let arrow = if *folded { "▸" } else { "▾" };
                    format!("{marker} {arrow} {} ({count})", self.section_title(id))
                }
                Row::Session(row) => {
                    let tag = field(row, "tag");
                    let separator = if tag.is_empty() { "" } else { " #" };
                    let details = if cols >= 64 {
                        format!("   {:<8}  {}", field(row, "agent"), field(row, "cwd"))
                    } else {
                        String::new()
                    };
                    format!("{marker} {}{separator}{tag}{details}", name(row))
                }
            };
            let style = if selected {
                "\x1b[38;2;142;180;230;1m"
            } else if matches!(row, Row::Section { .. }) {
                "\x1b[38;2;111;195;188;1m"
            } else {
                ""
            };
            line(
                screen,
                index as u16 + 2,
                cols,
                style,
                &truncation(&text, cols),
            );
        }
        let buttons: Vec<_> = self
            .button_view(cols)
            .into_iter()
            .map(|(button, ..)| button)
            .collect();
        line(screen, rows, cols, "\x1b[2m", &buttons.join(" "));
    }
}

fn form_header(output: &mut String, cols: u16, title: &str) {
    line(output, 1, cols, "\x1b[38;2;142;180;230;1m", title);
}

fn running(row: &Value) -> bool {
    field(&row["status"], "Process") == "running"
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

    fn apply(&mut self, key: Key) {
        const LIMIT: usize = 16384;
        let previous = self.text[..self.cursor]
            .char_indices()
            .next_back()
            .map_or(0, |(i, _)| i);
        match key {
            Key::Home => self.cursor = 0,
            Key::End => self.cursor = self.text.len(),
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
            Key::Paste(paste) => {
                for c in paste.chars().filter(|ch| !ch.is_control()) {
                    if self.text.len() + c.len_utf8() > LIMIT {
                        break;
                    }
                    self.text.insert(self.cursor, c);
                    self.cursor += c.len_utf8();
                }
            }
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

fn edit(value: &mut String, key: Key) {
    const LIMIT: usize = 16384;
    match key {
        Key::Text(ch) if !ch.is_control() && value.len() + ch.len_utf8() <= LIMIT => value.push(ch),
        Key::Paste(text) => {
            for ch in text.chars().filter(|ch| !ch.is_control()) {
                if value.len() + ch.len_utf8() > LIMIT {
                    break;
                }
                value.push(ch);
            }
        }
        Key::Backspace => {
            value.pop();
        }
        Key::CtrlU => value.clear(),
        _ => {}
    }
}

struct Launch {
    agents: Vec<&'static str>,
    agent: usize,
    focus: usize,
    cwd: String,
    name: String,
    tag: String,
    prompt: String,
}

impl Launch {
    fn new() -> Self {
        let agents = ["agy", "claude", "codex", "hermes", "opencode"]
            .into_iter()
            .filter(|program| {
                env::var_os("PATH").is_some_and(|path| {
                    env::split_paths(&path).any(|dir| {
                        use std::os::unix::fs::PermissionsExt;
                        dir.join(program).metadata().is_ok_and(|meta| {
                            meta.is_file() && meta.permissions().mode() & 0o111 != 0
                        })
                    })
                })
            })
            .collect();
        Self {
            agents,
            agent: 0,
            focus: 0,
            cwd: env::current_dir()
                .unwrap_or_default()
                .to_string_lossy()
                .into_owned(),
            name: String::new(),
            tag: String::new(),
            prompt: String::new(),
        }
    }

    fn update(&mut self, key: Key, (cols, rows): (u16, u16)) -> io::Result<Action> {
        match key {
            Key::Up => self.focus = (self.focus + 4) % 5,
            Key::Down | Key::Tab => self.focus = (self.focus + 1) % 5,
            Key::Left | Key::Right if self.focus == 0 && !self.agents.is_empty() => {
                let delta = if matches!(key, Key::Left) {
                    self.agents.len() - 1
                } else {
                    1
                };
                self.agent = (self.agent + delta) % self.agents.len();
            }
            Key::Enter => {
                let agent = self
                    .agents
                    .get(self.agent)
                    .ok_or_else(|| io::Error::other("no supported agent executable on PATH"))?;
                let cwd = PathBuf::from(&self.cwd);
                if !cwd.is_absolute() || !cwd.is_dir() {
                    return Err(io::Error::other(
                        "choose an existing absolute workspace directory",
                    ));
                }
                let caller_env: Vec<_> = env::vars_os()
                    .filter_map(|(key, value)| {
                        let key = key.to_str()?;
                        crate::environment::child_key(key)
                            .then(|| format!("{key}={}", value.to_string_lossy()))
                    })
                    .collect();
                let mut launch = json!({
                    "agent":agent, "cwd":cwd, "options":{}, "env":caller_env,
                    "cols":cols, "rows":rows, "initial_prompt":self.prompt,
                });
                for (key, text) in [("name", &self.name), ("tag", &self.tag)] {
                    if !text.is_empty() {
                        launch[key] = json!(text);
                    }
                }
                return Ok(Action::Request(
                    json!({"op":"launch","launch":launch}),
                    "launch",
                ));
            }
            key => match self.focus {
                1 => edit(&mut self.cwd, key),
                2 => edit(&mut self.name, key),
                3 => edit(&mut self.tag, key),
                4 => edit(&mut self.prompt, key),
                _ => {}
            },
        }
        Ok(Action::None)
    }

    fn view(&self, output: &mut String, cols: u16, rows: u16) {
        line(output, 1, cols, "\x1b[38;2;142;180;230;1m", "New session");
        let agent = self
            .agents
            .get(self.agent)
            .copied()
            .unwrap_or("No agent found");
        let fields = [
            ("Agent", agent),
            ("Directory", self.cwd.as_str()),
            ("Name", &self.name),
            ("Tag", &self.tag),
            ("Prompt", &self.prompt),
        ];
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
        if rows >= 2 {
            line(
                output,
                rows,
                cols,
                "\x1b[2m",
                "↑↓ field · ←→ agent · enter launch · esc cancel",
            );
        }
    }
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
