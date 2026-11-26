use super::launch::{ACCENT, DIM, PLAIN, TITLE, block_cursor, field, label_layout, spans};
use super::navigation::{GROUPS, ORDERS, truncation};
use super::preferences::Layout;
use super::session_form::{footer, word_wrap};
use super::{Editor, Key};
use std::path::PathBuf;
use unicode_width::UnicodeWidthStr;

const MANAGER_HINT: &str = "enter save prompt · tab/↑↓ next · esc cancel";
const MANAGER_NOTICE: [&str; 2] = [
    "Applies to all local managers.",
    "This prompt is visible to the model.",
];
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
            Self::Field(label, parts, _) => 2 + label_layout(label, cols).0.width() + values(parts),
        }
    }
}

pub(super) fn view(
    out: &mut String,
    cols: u16,
    rows: u16,
    layout: Layout,
    focus: usize,
    manager: Option<&Manager>,
    focus_only: bool,
) {
    let manager_focused = focus == 3;
    let capture_notice = layout.touch_back || focus == 2;
    let fallback_hint = if manager_focused {
        MANAGER_HINT
    } else if focus == 2 {
        CAPTURE_HINT
    } else {
        HINT
    };
    let mut context = Vec::new();
    let body = {
        let mut body = vec![
            Row::Text(vec![(TITLE, "swarm".into()), (DIM, " · options".into())]),
            Row::Text(vec![]),
            Row::Field("group", picker(&GROUPS, layout.group, cols), focus == 0),
            Row::Field("order", picker(&ORDERS, layout.order, cols), focus == 1),
            Row::Field(
                "CLI button",
                vec![(
                    PLAIN,
                    if layout.touch_back {
                        "[x] on"
                    } else {
                        "[ ] off"
                    }
                    .into(),
                )],
                focus == 2,
            ),
        ];
        if capture_notice {
            for text in CAPTURE_NOTICE {
                body.push(Row::Text(vec![(DIM, text.into())]));
                context.extend(word_wrap(text, cols));
            }
        }
        if let Some(manager) = manager {
            if manager_focused {
                context.clear();
                for notice in MANAGER_NOTICE {
                    let words = word_wrap(notice, cols.saturating_sub(2));
                    for line in words {
                        body.push(Row::Text(vec![(DIM, format!("  {line}"))]));
                    }
                    context.extend(word_wrap(notice, cols));
                }
                if manager.saved {
                    body.push(Row::Text(vec![(DIM, "  Manager prompt saved.".into())]));
                }
            }
            if !manager.error.is_empty() {
                let error_style = "\x1b[0;38;5;173m";
                for text in word_wrap(&manager.error, cols.saturating_sub(2)) {
                    body.push(Row::Text(vec![(error_style, format!("  {text}"))]));
                }
                if manager_focused {
                    context.extend(word_wrap(&manager.error, cols));
                }
            }
            body.push(Row::Field(
                "manager prompt",
                vec![(PLAIN, manager.value(cols, manager_focused))],
                manager_focused,
            ));
        }
        body
    };
    let selected = body.iter().position(Row::active).unwrap_or(2);
    if !fits((cols, rows)) {
        if focus_only {
            out.push_str("\x1b[2;1H\x1b[0m\x1b[2K");
            body[selected].paint(out, 2, cols);
            return;
        }
        spans(out, 1, cols, &[(PLAIN, "Resize to 32×8".into())]);
        body[selected].paint(out, 2, cols);
        spans(out, 3, cols, &[(PLAIN, "Esc back / n deny".into())]);
        footer(out, rows, cols, "esc back");
        return;
    }
    let budget = usize::from(rows - 1);
    let compact = cols < 64
        && rows <= 10
        && (body.len() > budget || body.iter().any(|row| row.width(cols) > usize::from(cols)));
    if compact {
        spans(out, 1, cols, &[(PLAIN, "Options".into())]);
        body[selected].paint(out, 2, cols);
        let mut row = 3;
        for text in context.iter().take(usize::from(rows.saturating_sub(row))) {
            spans(out, row, cols, &[(PLAIN, text.clone())]);
            row += 1;
        }
        for (index, entry) in body.iter().enumerate().skip(1) {
            if index == selected || entry.width(cols) == 0 {
                continue;
            }
            if row >= rows {
                break;
            }
            entry.paint(out, row, cols);
            row += 1;
        }
    } else {
        let start = (selected + 1).saturating_sub(budget);
        for (index, row) in body.iter().skip(start).take(budget).enumerate() {
            row.paint(out, index as u16 + 1, cols);
        }
    }
    let hint = fit_hint(fallback_hint, cols);
    footer(out, rows, cols, &hint);
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
    let retained = std::iter::once("esc cancel")
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
