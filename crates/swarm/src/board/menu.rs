use super::navigation::truncation;
use super::{clip, line};
use std::fmt::Write as _;
use unicode_width::{UnicodeWidthChar, UnicodeWidthStr};

pub(super) struct Control {
    pub(super) text: &'static str,
    pub(super) key: char,
    pub(super) x: u16,
    pub(super) y: u16,
}

pub(super) fn controls((cols, rows): (u16, u16)) -> [Control; 3] {
    let mut x = 1_u16;
    let mut y = 0;
    let mut controls = [
        ("[Back (esc)]", '⎋'),
        ("[Up (↑)]", '↑'),
        ("[Down (↓)]", '↓'),
    ]
    .map(|(text, key)| {
        let width = text.width() as u16;
        if x.saturating_add(width - 1) > cols {
            x = 1;
            y += 1;
        }
        let item = Control { text, key, x, y };
        x += width + 1;
        item
    });
    let start = rows.saturating_sub(y).max(1);
    for item in &mut controls {
        item.y += start;
    }
    controls
}

pub(super) fn capacity(geometry: (u16, u16)) -> usize {
    usize::from(controls(geometry)[0].y.saturating_sub(2).max(1))
}

pub(super) fn button(text: &str, key: char) -> String {
    let shortcut = match key {
        'x' => "ctrl+x".into(),
        other => other.to_string(),
    };
    format!("[{text} ({shortcut})]")
}

pub(super) fn wrap(text: &str, cols: u16) -> Vec<String> {
    let text = clip(text, u16::MAX);
    let mut lines = Vec::new();
    let mut start = 0;
    let mut width = 0;
    for (index, character) in text.char_indices() {
        let cells = character.width().unwrap_or(0);
        if width + cells > usize::from(cols.max(1)) && index > start {
            lines.push(text[start..index].to_owned());
            start = index;
            width = 0;
        }
        width += cells;
    }
    lines.push(text[start..].to_owned());
    lines
}

pub(super) fn reveal(selected: &mut usize, top: &mut usize, count: usize, budget: usize) {
    *selected = (*selected).min(count.saturating_sub(1));
    *top = (*top)
        .min(*selected)
        .max((*selected + 1).saturating_sub(budget))
        .min(count.saturating_sub(budget));
}

pub(super) struct Frame {
    geometry: (u16, u16),
    lines: Vec<String>,
}

impl Frame {
    pub(super) fn new(
        geometry: (u16, u16),
        title: &str,
        entries: Vec<(String, Option<char>)>,
        selected: usize,
        top: usize,
    ) -> Self {
        let (cols, rows) = geometry;
        let mut lines = vec![String::new(); usize::from(rows)];
        lines[0] = truncation(title, cols);
        let budget = capacity(geometry);
        for (index, (text, key)) in entries.into_iter().enumerate().skip(top).take(budget) {
            let mark = if index == selected { "▌" } else { " " };
            let text = key.map_or_else(|| text.clone(), |key| button(&text, key));
            let visible = truncation(&text, cols.saturating_sub(2));
            lines[index - top + 1] = format!("{mark} {visible}");
        }
        for item in controls(geometry) {
            let footer = &mut lines[usize::from(item.y - 1)];
            if !footer.is_empty() {
                footer.push(' ');
            }
            footer.push_str(item.text);
        }
        Self { geometry, lines }
    }

    pub(super) fn paint(&self, out: &mut String, previous: Option<&Self>) {
        let previous = previous.filter(|prev| prev.geometry == self.geometry);
        if previous.is_none() {
            out.push_str("\x1b[?25l\x1b[0m\x1b[H\x1b[2J");
        }
        for (index, text) in self.lines.iter().enumerate() {
            let row = index as u16 + 1;
            if let Some(previous) = previous {
                let old = &previous.lines[index];
                if old == text {
                    continue;
                }
                if let Some(marker) = focus_delta(old, text) {
                    let _ = write!(out, "\x1b[{row};1H{marker}");
                    continue;
                }
            } else if text.is_empty() {
                continue;
            }
            line(out, row, self.geometry.0, "", text);
        }
    }
}

fn focus_delta<'a>(old: &str, new: &'a str) -> Option<&'a str> {
    let strip = |row: &str| {
        if row.starts_with("▌ ") {
            Some(4)
        } else if row.starts_with("  ") {
            Some(2)
        } else {
            None
        }
    };
    let a = strip(old)?;
    let b = strip(new)?;
    (old[a..] == new[b..]).then_some(&new[..b])
}
