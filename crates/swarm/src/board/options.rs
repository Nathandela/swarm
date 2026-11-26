use super::context_settings::{Context, DESCRIPTION, Owner, Update};
use super::launch::{ACCENT, DIM, PLAIN, TITLE, block_cursor, field, label_layout, spans};
use super::navigation::{GROUPS, ORDERS, truncation};
use super::preferences::Layout;
use super::session_form::{footer, word_wrap};
use super::{Editor, Key};
use std::fmt::Write as _;
use std::path::PathBuf;
use unicode_width::UnicodeWidthStr;

const MANAGER_HINT: &str = "enter save prompt · tab/↑↓ next · esc cancel";
const MANAGER_NOTICE: [&str; 2] = [
    "Applies to all local managers.",
    "This prompt is visible to the model.",
];
const CONTEXT_HINT: &str =
    "←→ change · space toggle · type threshold · tab/↑↓ next · enter apply · esc cancel";
const HINT: &str = "←→ change · tab/↑↓ next · enter apply · esc cancel";
const CAPTURE_HINT: &str = "←→ change · terminal-wide mouse capture can affect text selection · tab/↑↓ next · enter apply · esc cancel";
const CAPTURE_NOTICE: [&str; 2] = [
    "Touch captures the whole screen.",
    "Text selection may need your terminal's override.",
];

pub(super) fn fits((cols, rows): (u16, u16)) -> bool {
    cols >= 32 && rows >= 8
}

pub(super) struct Manager {
    root: PathBuf,
    draft: Editor,
    saved: bool,
    error: String,
}

impl Manager {
    pub(super) fn open() -> Option<Self> {
        let root = crate::ipc::state_path().ok()?;
        let (value, error) = crate::manager_prompt::load(&root).map_or_else(
            |err| (String::new(), err.to_string()),
            |value| (value, String::new()),
        );
        Some(Self {
            root,
            draft: Editor::new(&value),
            saved: false,
            error,
        })
    }

    pub(super) fn update(&mut self, key: Key, geometry: (u16, u16)) {
        match key {
            Key::Enter if fits(geometry) => {
                self.saved = false;
                self.error.clear();
                match crate::manager_prompt::save(&self.root, &self.draft.text) {
                    Ok(()) => {
                        self.draft = Editor::new(self.draft.text.trim());
                        self.saved = true;
                    }
                    Err(error) => self.error = error.to_string(),
                }
            }
            Key::Paste(text) => {
                let lines = text.replace("\r\n", "\n");
                self.draft.insert_text(
                    lines
                        .chars()
                        .map(|ch| if ch.is_control() { ' ' } else { ch }),
                );
                self.saved = false;
            }
            other => {
                let original = self.draft.text.clone();
                self.draft.apply(other);
                if original != self.draft.text {
                    self.saved = false;
                }
            }
        }
    }

    fn value(&self, cols: u16, focused: bool) -> String {
        if focused {
            block_cursor(&self.draft, label_layout("manager prompt", cols).1)
        } else if self.draft.text.is_empty() {
            "(empty)".into()
        } else {
            self.draft.text.clone()
        }
    }
}

#[derive(Clone, PartialEq, Eq)]
enum Row {
    Text(Vec<(&'static str, String)>),
    Field(&'static str, Vec<(&'static str, String)>, bool),
}

impl Row {
    fn paint(&self, out: &mut String, row: u16, cols: u16) {
        match self {
            Self::Text(value) => spans(out, row, cols, value),
            Self::Field(label, value, selected) => field(out, row, cols, label, value, *selected),
        }
    }

    fn active(&self) -> bool {
        matches!(self, Self::Field(_, _, true))
    }

    fn width(&self, cols: u16) -> usize {
        let values =
            |parts: &[(&str, String)]| parts.iter().map(|(_, value)| value.width()).sum::<usize>();
        match self {
            Self::Text(parts) => values(parts),
            Self::Field(label, parts, _) => {
                (2 + label_layout(label, cols).0.width() + values(parts)).min(usize::from(cols))
            }
        }
    }
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum Focus {
    Group,
    Order,
    Touch,
    Manager,
    Compact,
    Threshold,
}

pub(super) struct Form {
    pub(super) draft: Layout,
    focus: Focus,
    manager: Option<Manager>,
    context: Option<Context>,
    read_stop: usize,
}

impl Form {
    pub(super) fn new(draft: Layout, owner: Option<Owner>) -> Self {
        Self {
            draft,
            focus: Focus::Group,
            manager: Manager::open(),
            context: owner.map(Context::open),
            read_stop: 0,
        }
    }

    fn fields(&self) -> Vec<Focus> {
        let mut fields = vec![Focus::Group, Focus::Order, Focus::Touch];
        if self.manager.is_some() {
            fields.push(Focus::Manager);
        }
        if self.context.as_ref().is_some_and(Context::loaded) {
            fields.extend([Focus::Compact, Focus::Threshold]);
        }
        fields
    }

    pub(super) fn disconnected(&mut self) {
        if let Some(context) = &mut self.context {
            context.disconnect();
        }
    }

    pub(super) fn reconnected(&mut self, owner: Option<&Owner>) {
        if let Some(context) = &mut self.context {
            context.reconnect(owner);
        }
    }

    pub(super) fn poll(&mut self) -> Update {
        let Some(context) = &mut self.context else {
            return Update::Pending;
        };
        let update = context.poll();
        if !self.fields().contains(&self.focus) {
            self.focus = Focus::Group;
            self.read_stop = 0;
        }
        update
    }

    pub(super) fn input(&mut self, key: Key, geometry: (u16, u16), online: bool) -> bool {
        if matches!(key, Key::Up | Key::Down | Key::Tab) {
            let fields = self.fields();
            let index = fields
                .iter()
                .position(|focus| *focus == self.focus)
                .unwrap_or_default();
            let offset = if key == Key::Up { fields.len() - 1 } else { 1 };
            self.focus = fields[(index + offset) % fields.len()];
            self.read_stop = 0;
            return false;
        }

        let body = self.context_lines(geometry);
        let budget = Self::budget(geometry);
        if body.len() > budget {
            match key {
                Key::PageDown => {
                    self.read_stop = (self.read_stop + budget).min(body.len());
                    return false;
                }
                Key::PageUp => {
                    self.read_stop = self.read_stop.saturating_sub(budget);
                    return false;
                }
                Key::Enter if self.read_stop < body.len() => return false,
                _ => {}
            }
        }

        if self.focus == Focus::Manager {
            if let Some(manager) = &mut self.manager {
                manager.update(key, geometry);
            }
            return false;
        }

        let mut context = self.context.as_mut();
        if context.as_ref().is_some_and(|settings| settings.pending()) {
            return false;
        }
        match key {
            Key::Enter if fits(geometry) => {
                if let Some(settings) = context {
                    if !settings.error.is_empty() {
                        return false;
                    }
                    if settings.dirty() {
                        if online {
                            settings.save();
                        }
                        return false;
                    }
                }
                true
            }
            Key::Left | Key::Right => {
                match self.focus {
                    Focus::Group => cycle(&mut self.draft.group, GROUPS.len(), key),
                    Focus::Order => cycle(&mut self.draft.order, ORDERS.len(), key),
                    Focus::Touch => self.draft.touch_back = !self.draft.touch_back,
                    Focus::Compact => {
                        if let Some(settings) = &mut context {
                            settings.enabled = !settings.enabled;
                        }
                    }
                    Focus::Threshold => {
                        if let Some(settings) = context {
                            settings.update_threshold(key);
                        }
                    }
                    Focus::Manager => {}
                }
                false
            }
            Key::Text(' ') if self.focus == Focus::Compact => {
                if let Some(settings) = context {
                    settings.enabled = !settings.enabled;
                }
                false
            }
            key if self.focus == Focus::Threshold => {
                if let Some(settings) = context {
                    settings.update_threshold(key);
                }
                false
            }
            _ => false,
        }
    }

    fn budget((_, rows): (u16, u16)) -> usize {
        usize::from(rows.saturating_sub(3).max(1))
    }

    fn compact((cols, rows): (u16, u16)) -> bool {
        cols < 64 && rows <= 10
    }

    fn context_lines(&self, (cols, rows): (u16, u16)) -> Vec<String> {
        let width = if Self::compact((cols, rows)) {
            cols
        } else {
            cols.saturating_sub(2).max(1)
        };
        let mut source = Vec::new();
        if self.focus == Focus::Manager {
            source.extend(MANAGER_NOTICE);
            if let Some(manager) = &self.manager
                && !manager.error.is_empty()
            {
                source.push(manager.error.as_str());
            }
        } else {
            if self.draft.touch_back || self.focus == Focus::Touch {
                source.extend(CAPTURE_NOTICE);
            }
            if let Some(settings) = &self.context {
                source.push(DESCRIPTION);
                if !settings.error.is_empty() {
                    source.push(settings.error.as_str());
                }
            }
        }
        source
            .into_iter()
            .flat_map(|line| word_wrap(line, width))
            .collect()
    }

    fn hint(&self) -> &'static str {
        if self.focus == Focus::Manager {
            MANAGER_HINT
        } else if self.focus == Focus::Touch {
            CAPTURE_HINT
        } else if self.context.as_ref().is_some_and(Context::loaded) {
            CONTEXT_HINT
        } else {
            HINT
        }
    }

    fn rows(&self, cols: u16) -> Vec<Row> {
        let mut body = vec![
            Row::Text(vec![(TITLE, "swarm".into()), (DIM, " · options".into())]),
            Row::Text(vec![]),
            Row::Field(
                "group",
                picker(&GROUPS, self.draft.group, cols),
                self.focus == Focus::Group,
            ),
            Row::Field(
                "order",
                picker(&ORDERS, self.draft.order, cols),
                self.focus == Focus::Order,
            ),
            Row::Field(
                "CLI button",
                vec![(
                    PLAIN,
                    if self.draft.touch_back {
                        "[x] on"
                    } else {
                        "[ ] off"
                    }
                    .into(),
                )],
                self.focus == Focus::Touch,
            ),
        ];
        if self.draft.touch_back || self.focus == Focus::Touch {
            for text in CAPTURE_NOTICE {
                body.push(Row::Text(vec![(DIM, text.into())]));
            }
        }
        if let Some(manager) = &self.manager {
            let focused = self.focus == Focus::Manager;
            if focused {
                for notice in MANAGER_NOTICE {
                    body.extend(indented(notice, cols, DIM));
                }
                if manager.saved {
                    body.push(Row::Text(vec![(DIM, "  Manager prompt saved.".into())]));
                }
            }
            if !manager.error.is_empty() {
                body.extend(indented(&manager.error, cols, ERROR));
            }
            body.push(Row::Field(
                "manager prompt",
                vec![(PLAIN, manager.value(cols, focused))],
                focused,
            ));
        }
        if let Some(settings) = &self.context {
            body.push(Row::Text(vec![]));
            body.push(Row::Text(vec![(DIM, DESCRIPTION.into())]));
            if settings.loading() {
                body.push(Row::Text(vec![(
                    DIM,
                    "  loading context guard settings…".into(),
                )]));
            } else if settings.loaded() {
                let enabled = if settings.enabled {
                    "[x] enabled"
                } else {
                    "[ ] disabled"
                };
                body.push(Row::Field(
                    "auto compact",
                    vec![(PLAIN, enabled.into())],
                    self.focus == Focus::Compact,
                ));
                let focussed = self.focus == Focus::Threshold;
                let mut threshold = if focussed {
                    block_cursor(
                        &settings.threshold,
                        label_layout("threshold", cols.saturating_sub(1)).1,
                    )
                } else {
                    settings.threshold.text.clone()
                };
                threshold.push('%');
                body.push(Row::Field("threshold", vec![(PLAIN, threshold)], focussed));
                if settings.saving() {
                    body.push(Row::Text(vec![(
                        DIM,
                        "  saving context guard settings…".into(),
                    )]));
                }
                if !settings.error.is_empty() {
                    body.push(Row::Text(vec![(ERROR, format!("  {}", settings.error))]));
                }
            } else {
                body.push(Row::Text(vec![(DIM, format!("  {}", settings.error))]));
            }
        }
        body
    }

    pub(super) fn frame(&self, geometry: (u16, u16)) -> Frame {
        let (cols, rows) = geometry;
        let body = self.rows(cols);
        let selected = body.iter().position(Row::active).unwrap_or(2);
        let text = |message: &str| Row::Text(vec![(PLAIN, message.into())]);
        if !fits(geometry) {
            return Frame::new(
                geometry,
                vec![
                    text("Resize to 32×8"),
                    body[selected].clone(),
                    text("Esc back / n deny"),
                ],
                "esc back".into(),
            );
        }

        let budget = usize::from(rows - 1);
        let mut context = self.context_lines(geometry);
        let reading = Self::budget(geometry);
        if context.len() > reading {
            let marker = if self.read_stop >= context.len() {
                "▌ "
            } else {
                "  "
            };
            context.push(format!("{marker}Confirm · enter"));
            let start = self.read_stop.min(context.len().saturating_sub(reading));
            let mut body_rows = vec![text("Options"), body[selected].clone()];
            body_rows.extend(
                context
                    .iter()
                    .skip(start)
                    .take(reading)
                    .map(|line| text(line)),
            );
            return Frame::new(
                geometry,
                body_rows,
                fit_hint("pgup/down read · esc back", cols),
            );
        }

        let compact = Self::compact(geometry)
            && (body.len() > budget || body.iter().any(|row| row.width(cols) > usize::from(cols)));
        let body_rows = if compact {
            let mut body_rows = vec![text("Options"), body[selected].clone()];
            body_rows.extend(context.iter().map(|message| text(message)));
            let remaining = budget.saturating_sub(body_rows.len());
            body_rows.extend(
                body.into_iter()
                    .enumerate()
                    .skip(1)
                    .filter(|(index, entry)| *index != selected && entry.width(cols) != 0)
                    .take(remaining)
                    .map(|(_, entry)| entry),
            );
            body_rows
        } else {
            let start = (selected + 1).saturating_sub(budget);
            body.into_iter().skip(start).take(budget).collect()
        };
        Frame::new(geometry, body_rows, fit_hint(self.hint(), cols))
    }
}

pub(super) struct Frame {
    geometry: (u16, u16),
    body: Vec<Row>,
    hint: String,
}

impl Frame {
    fn new(geometry: (u16, u16), mut body: Vec<Row>, hint: String) -> Self {
        body.resize(usize::from(geometry.1.saturating_sub(1)), Row::Text(vec![]));
        Self {
            geometry,
            body,
            hint,
        }
    }

    pub(super) fn paint(&self, out: &mut String, previous: Option<&Self>) {
        let (cols, rows) = self.geometry;
        let previous = previous.filter(|frame| frame.geometry == self.geometry);
        if previous.is_none() {
            out.push_str("\x1b[?25l\x1b[0m\x1b[H\x1b[2J");
        }
        for (index, body) in self.body.iter().enumerate() {
            let row = index as u16 + 1;
            if let Some(previous) = previous {
                if previous.body[index] == *body {
                    continue;
                }
                let _ = write!(out, "\x1b[{row};1H\x1b[0m\x1b[2K");
            } else if body.width(cols) == 0 {
                continue;
            }
            body.paint(out, row, cols);
        }
        if previous.is_none_or(|previous| previous.hint != self.hint) {
            if previous.is_some() {
                let _ = write!(out, "\x1b[{rows};1H\x1b[0m\x1b[2K");
            }
            footer(out, rows, cols, &self.hint);
        }
    }
}

const ERROR: &str = "\x1b[0;38;5;173m";

fn indented(text: &str, cols: u16, style: &'static str) -> impl Iterator<Item = Row> {
    word_wrap(text, cols.saturating_sub(2))
        .into_iter()
        .map(move |line| Row::Text(vec![(style, format!("  {line}"))]))
}

fn cycle(current: &mut usize, len: usize, key: Key) {
    let step = if key == Key::Left { len - 1 } else { 1 };
    *current = (*current + step) % len;
}

fn fit_hint(hint: &str, cols: u16) -> String {
    let available = cols.saturating_sub(2);
    if hint.width() <= usize::from(available) {
        return hint.into();
    }
    let (actions, other): (Vec<_>, Vec<_>) = hint
        .split(" · ")
        .filter(|part| !part.contains("esc"))
        .partition(|part| part.contains("enter"));
    let back = if hint.contains("esc cancel") {
        "esc cancel"
    } else {
        "esc back"
    };
    let retained = std::iter::once(back)
        .chain(actions)
        .chain(other)
        .collect::<Vec<_>>()
        .join(" · ");
    truncation(&retained, available)
}

fn picker(labels: &[&str], selected: usize, cols: u16) -> Vec<(&'static str, String)> {
    let mut parts = vec![(DIM, "◂ ".into())];
    for (index, label) in labels.iter().enumerate() {
        if index > 0 {
            parts.push((PLAIN, "   ".into()));
        }
        parts.push(if index == selected {
            (ACCENT, format!("● {label}"))
        } else {
            (PLAIN, format!("○ {label}"))
        });
    }
    parts.push((DIM, " ▸".into()));
    // The three retained fields all use the same label padding.
    let (_, budget) = label_layout("CLI button", cols);
    if parts.iter().map(|(_, text)| text.width()).sum::<usize>() <= usize::from(budget) {
        parts
    } else {
        vec![
            (DIM, "◂ ".into()),
            (ACCENT, labels[selected].into()),
            (DIM, " ▸".into()),
        ]
    }
}
