use super::{
    Editor, FormViewport, Key, clip, field,
    launch::{PLAIN, block_cursor, spans, status},
    menu,
    navigation::truncation,
};
use serde_json::Value;
use std::fmt::Write as _;
use unicode_width::UnicodeWidthStr;

pub(super) const CONFIRM_HINT: &str = "y / ctrl+x confirm · n / esc cancel";
pub(super) const RENAME_HINT: &str =
    "type  ←→ move  ⌘←/→ home/end · ⌘⌫/ctrl+u clear-left  ⏎ save  esc cancel";
pub(super) const TAG_HINT: &str =
    "edit tag · type  ←→ move  ⌘←/→ home/end · ⌘⌫/ctrl+u clear-left  ⏎ save  esc cancel";
const EDIT_HINT: &str = "enter save · esc cancel";

pub(super) enum Kind<'a> {
    Edit { tag: bool, editor: &'a Editor },
    Confirm { running: bool },
}

pub(super) struct Sheet<'a> {
    cols: u16,
    viewport: FormViewport<'a>,
    title: String,
    editor: Option<String>,
    context: Vec<String>,
    confirm: bool,
}

impl<'a> Sheet<'a> {
    pub(super) fn new(
        viewport: FormViewport<'a>,
        target: &str,
        row: Option<&Value>,
        kind: Kind<'_>,
    ) -> Self {
        let (cols, _) = viewport.geometry;
        let label = row
            .map(super::name)
            .unwrap_or("session no longer available");
        let provider = row.map(|row| field(row, "agent")).unwrap_or("");
        let directory = row.map(|row| field(row, "cwd")).unwrap_or("");
        let action = match kind {
            Kind::Edit { tag: false, .. } => "Rename",
            Kind::Edit { tag: true, .. } => "Tag",
            Kind::Confirm { running: false } => "Delete",
            Kind::Confirm { running: true } => "Kill",
        };
        let (editor, confirm) = match kind {
            Kind::Edit { tag, editor } => {
                let (_, text) = edit_text(editor, tag, cols);
                (Some(format!("▌ {action}  {text}")), false)
            }
            Kind::Confirm { .. } => (None, true),
        };
        let mut context = vec![
            format!("Session: {label}"),
            format!("Agent: {provider}"),
            format!("ID: {target}"),
            format!("Directory: {directory}"),
        ];
        if confirm {
            context.push(format!("{action} this session?"));
        }
        let context = context
            .iter()
            .flat_map(|text| word_wrap(text, cols))
            .collect();
        Self {
            cols,
            viewport,
            title: format!("{action}: {label}"),
            editor,
            context,
            confirm,
        }
    }

    pub(super) fn readable(&self) -> bool {
        self.cols >= 32 && self.viewport.geometry.1 >= 8
    }

    fn budget(&self) -> usize {
        usize::from(self.viewport.body_rows())
            .saturating_sub(1 + usize::from(self.editor.is_some()))
            .max(1)
    }

    fn needs_reading(&self) -> bool {
        self.context.len() > self.budget()
    }

    pub(super) fn can_submit(&self, stop: usize) -> bool {
        self.readable() && (!self.needs_reading() || stop >= self.context.len())
    }

    pub(super) fn scroll(&self, stop: &mut usize, key: &Key) -> bool {
        if !self.readable() || !self.needs_reading() {
            *stop = 0;
            return false;
        }
        let limit = self.context.len();
        *stop = match key {
            Key::Down | Key::Tab if self.confirm => (*stop + 1).min(limit),
            Key::Up if self.confirm => stop.saturating_sub(1),
            Key::PageDown => (*stop + self.budget()).min(limit),
            Key::PageUp => stop.saturating_sub(self.budget()),
            Key::End if self.confirm => limit,
            Key::Home if self.confirm => 0,
            _ => return false,
        };
        true
    }

    pub(super) fn paint(&self, out: &mut String, stop: usize) {
        let row =
            |out: &mut String, y, text: &str| spans(out, y, self.cols, &[(PLAIN, text.into())]);
        if !self.readable() {
            row(out, 1, "Resize to 32×8");
            let mut y = 2;
            if let Some(editor) = &self.editor {
                row(out, y, editor);
                y += 1;
            }
            if y <= self.viewport.body_rows() {
                row(out, y, "Esc back / n deny");
            }
            self.viewport.footer(out, "esc back");
            return;
        }
        row(out, 1, &self.title);
        let mut y = 2;
        if let Some(editor) = &self.editor {
            row(out, y, editor);
            y += 1;
        }
        let mut hint = if self.confirm {
            CONFIRM_HINT
        } else {
            EDIT_HINT
        }
        .to_owned();
        let budget = self.budget();
        let mut context = self.context.clone();
        let mut start = 0;
        if self.needs_reading() {
            let placement = if stop >= context.len() { "▌ " } else { "  " };
            let action = if self.confirm {
                "Confirm · y / ctrl+x"
            } else {
                "Confirm · enter"
            };
            context.push(format!("{placement}{action}"));
            start = stop.min(context.len().saturating_sub(budget));
            hint = if self.confirm {
                "↑↓/tab read · end confirm · esc back"
            } else {
                "pgup/down read · esc back"
            }
            .into();
        }
        for text in context.iter().skip(start).take(budget) {
            row(out, y, text);
            y += 1;
        }
        if hint.width() > usize::from(self.cols.saturating_sub(2)) && self.confirm {
            hint = if self.needs_reading() {
                "esc back · end confirm · ↑↓/tab read"
            } else {
                "esc cancel · y / ctrl+x confirm"
            }
            .into();
        }
        self.viewport
            .footer(out, &truncation(&hint, self.cols.saturating_sub(2)));
    }
}

pub(super) fn footer(out: &mut String, row: u16, cols: u16, hint: &str) {
    let hint = clip(hint, cols.saturating_sub(2));
    let version = option_env!("SWARM_BUILD_VERSION").unwrap_or("dev");
    let width = hint.width().saturating_add(2);
    let gap = usize::from(cols).saturating_sub(width + version.width());
    status(out, row, cols, &hint);
    if width == usize::from(cols) || gap >= 3 && !version.is_empty() {
        let _ = write!(out, "\x1b[{row};{cols}H");
    }
}

pub(super) fn edit_text(editor: &Editor, tag: bool, cols: u16) -> (u16, String) {
    let action = if tag { "Tag" } else { "Rename" };
    let start = action.len() as u16 + 4;
    (
        start + 1,
        block_cursor(editor, cols.saturating_sub(start).max(1)),
    )
}

#[derive(PartialEq, Eq)]
pub(super) struct EditorBasis {
    pub(super) geometry: (u16, u16),
    pub(super) target: String,
    pub(super) tag: bool,
    pub(super) top: usize,
    pub(super) revision: u64,
    pub(super) tick: Option<usize>,
    pub(super) second: Option<u64>,
}

pub(super) struct EditorFrame {
    pub(super) basis: EditorBasis,
    pub(super) x: u16,
    pub(super) y: u16,
    pub(super) style: &'static str,
    pub(super) text: String,
}

impl EditorFrame {
    pub(super) fn paint_change(&self, previous: Option<&Self>, out: &mut String) -> bool {
        let Some(previous) = previous
            .filter(|prev| self.basis == prev.basis && self.x == prev.x && self.y == prev.y)
        else {
            return false;
        };
        let mut prefix = 0;
        for (old, new) in previous.text.chars().zip(self.text.chars()) {
            if old != new {
                break;
            }
            prefix += new.len_utf8();
        }
        // Combine marks belong to their base cell. When a common base's
        // marks differ, write the whole cluster from its original position.
        let suffix_is_mark = |text: &str| {
            text[prefix..]
                .chars()
                .next()
                .is_some_and(|ch| unicode_width::UnicodeWidthChar::width(ch) == Some(0))
        };
        if suffix_is_mark(&self.text) || suffix_is_mark(&previous.text) {
            let before = &self.text[..prefix];
            prefix = before
                .char_indices()
                .rev()
                .find(|(_, ch)| unicode_width::UnicodeWidthChar::width(*ch).unwrap_or(0) > 0)
                .map_or(0, |(i, _)| i);
        }
        let changed = &self.text[prefix..];
        let erased = previous.text.width().saturating_sub(self.text.width());
        if !changed.is_empty() || erased > 0 {
            let x = self.x + self.text[..prefix].width() as u16;
            let y = self.y;
            let style = self.style;
            let _ = write!(
                out,
                "\x1b[{y};{x}H{style}{changed}{PLAIN}{}",
                " ".repeat(erased)
            );
        }
        true
    }
}

pub(super) fn word_wrap(text: &str, cols: u16) -> Vec<String> {
    let text = clip(text, u16::MAX);
    let mut rows = Vec::new();
    let mut line = String::new();
    for word in text.split_whitespace() {
        if !line.is_empty() && line.width() + 1 + word.width() > usize::from(cols) {
            rows.push(std::mem::take(&mut line));
        }
        if !line.is_empty() {
            line.push(' ');
        }
        if word.width() > usize::from(cols) {
            let mut parts = menu::wrap(word, cols);
            line = parts.pop().unwrap_or_default();
            rows.extend(parts);
        } else {
            line.push_str(word);
        }
    }
    if !line.is_empty() {
        rows.push(line);
    }
    rows
}
