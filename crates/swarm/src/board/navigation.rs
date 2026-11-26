use super::{Board, clip, field, name};
use serde_json::Value;
use std::cmp::Ordering;
use std::collections::BTreeMap;
use std::path::Path;

#[derive(Clone, Debug, PartialEq, Eq)]
pub(super) enum Focus {
    Session(String),
    Section(String),
}

pub(super) enum Row<'a> {
    Section {
        id: &'a str,
        count: usize,
        folded: bool,
    },
    Session(&'a Value),
    Spacer,
}

impl Row<'_> {
    pub(super) fn focus(&self) -> Option<Focus> {
        match self {
            Self::Section { id, .. } => Some(Focus::Section((*id).to_owned())),
            Self::Session(row) => Some(Focus::Session(field(row, "id").to_owned())),
            Self::Spacer => None,
        }
    }

    fn selectable(&self) -> bool {
        matches!(self, Self::Session(_) | Self::Section { folded: true, .. })
    }
}

pub(super) const GROUPS: [&str; 3] = ["status", "repo", "tag"];
pub(super) const ORDERS: [&str; 4] = ["arrival", "activity", "created", "name"];

impl Board {
    pub(super) fn rebuild_sections(&mut self) {
        let mut sections = BTreeMap::<String, Vec<usize>>::new();
        for (index, row) in self.rows.iter().enumerate() {
            sections.entry(self.section(row)).or_default().push(index);
        }
        for indexes in sections.values_mut() {
            indexes.sort_by(|&left, &right| self.compare_rows(&self.rows[left], &self.rows[right]));
        }
        self.sections = sections.into_iter().collect();
        if self.layout.group == 0 {
            self.sections.sort_by_key(|(id, _)| super::group_rank(id));
        }
        if let Some(row) = self.row()
            && self
                .folded
                .contains(&(self.layout.group, self.section(row)))
        {
            self.focused_section = Some(self.section(row));
            self.selected.clear();
        }
        if self
            .focused_section
            .as_ref()
            .is_some_and(|id| !self.sections.iter().any(|(section, _)| section == id))
        {
            self.focused_section = None;
        }
        if self.current_focus().is_none() {
            let first = self
                .display_rows()
                .into_iter()
                .find(Row::selectable)
                .and_then(|row| row.focus());
            if let Some(first) = first {
                self.set_focus(first);
            }
        }
    }

    fn section(&self, row: &Value) -> String {
        match self.layout.group {
            1 => Path::new(field(row, "cwd"))
                .file_name()
                .and_then(|name| name.to_str())
                .unwrap_or("(no repo)")
                .to_owned(),
            2 => {
                let tag = field(row, "tag");
                if tag.is_empty() { "(untagged)" } else { tag }.to_owned()
            }
            _ => field(row, "group").to_owned(),
        }
    }

    fn compare_rows(&self, left: &Value, right: &Value) -> Ordering {
        let timestamp = |row: &Value, names: &[&str]| {
            names
                .iter()
                .find_map(|name| field(row, name).parse::<jiff::Timestamp>().ok())
        };
        let rank = |row: &Value| super::group_rank(field(row, "group"));
        rank(left)
            .cmp(&rank(right))
            .then_with(|| match self.layout.order {
                1 => timestamp(right, &["last_activity"]).cmp(&timestamp(left, &["last_activity"])),
                3 => name(left).to_lowercase().cmp(&name(right).to_lowercase()),
                0 if self.layout.group == 0 => {
                    let category = ["group_entered_at", "last_activity", "created_at"];
                    timestamp(right, &category).cmp(&timestamp(left, &category))
                }
                _ => Ordering::Equal,
            })
            .then_with(|| timestamp(right, &["created_at"]).cmp(&timestamp(left, &["created_at"])))
            .then_with(|| field(left, "id").cmp(field(right, "id")))
    }

    pub(super) fn section_title(&self, section: &str) -> String {
        if self.layout.group == 0 {
            super::group_name(super::group_rank(section)).into()
        } else {
            section.into()
        }
    }

    pub(super) fn display_rows(&self) -> Vec<Row<'_>> {
        let mut rows = Vec::with_capacity(self.rows.len() + self.sections.len());
        for (id, indexes) in &self.sections {
            let folded = self.folded.contains(&(self.layout.group, id.clone()));
            rows.push(Row::Section {
                id,
                count: indexes.len(),
                folded,
            });
            if !folded {
                rows.extend(indexes.iter().map(|&index| Row::Session(&self.rows[index])));
                if !super::roster::compact(self.columns) {
                    rows.push(Row::Spacer);
                }
            }
        }
        rows
    }

    pub(super) fn current_focus(&self) -> Option<Focus> {
        self.focused_section
            .as_ref()
            .map(|id| Focus::Section(id.clone()))
            .or_else(|| self.row().map(|_| Focus::Session(self.selected.clone())))
    }

    pub(super) fn set_focus(&mut self, focus: Focus) {
        match focus {
            Focus::Session(id) => {
                self.selected = id;
                self.focused_section = None;
            }
            Focus::Section(id) => {
                self.selected.clear();
                self.focused_section = Some(id);
            }
        }
    }

    pub(super) fn visible(&self, rows: u16) -> bool {
        self.focus_line()
            .is_some_and(|line| line >= self.top && line < self.top + board_capacity(rows))
    }

    fn focus_line(&self) -> Option<usize> {
        let focus = self.current_focus()?;
        self.display_rows()
            .iter()
            .position(|row| row.focus().as_ref() == Some(&focus))
    }

    pub(super) fn reveal_focus(&mut self, rows: u16) {
        self.clamp_top(rows);
        if let Some(line) = self.focus_line() {
            self.top = self
                .top
                .min(line)
                .max((line + 1).saturating_sub(board_capacity(rows)));
        }
    }

    pub(super) fn clamp_top(&mut self, rows: u16) {
        let count = self.display_rows().len();
        self.top = self.top.min(count.saturating_sub(board_capacity(rows)));
    }

    pub(super) fn move_selection(&mut self, offset: isize, wrap: bool, rows: u16) {
        let focuses: Vec<_> = self
            .display_rows()
            .iter()
            .filter(|row| row.selectable())
            .filter_map(Row::focus)
            .collect();
        if focuses.is_empty() {
            return;
        }
        let current = self
            .current_focus()
            .and_then(|focus| focuses.iter().position(|item| item == &focus))
            .unwrap_or(0);
        let next = (current as isize).saturating_add(offset);
        let target = if wrap {
            next.rem_euclid(focuses.len() as isize)
        } else {
            next.clamp(0, focuses.len() as isize - 1)
        };
        self.set_focus(focuses[target as usize].clone());
        self.reveal_focus(rows);
    }

    pub(super) fn fold(&mut self, section: Option<String>, collapse: bool, rows: u16) {
        let section = if let Some(section) = section {
            section
        } else if let Some(row) = self.row() {
            self.section(row)
        } else {
            return;
        };
        if collapse {
            self.folded.insert((self.layout.group, section.clone()));
            self.set_focus(Focus::Section(section));
        } else {
            self.folded.remove(&(self.layout.group, section.clone()));
            if let Some((_, indexes)) = self.sections.iter().find(|(id, _)| id == &section)
                && let Some(&first) = indexes.first()
            {
                self.set_focus(Focus::Session(field(&self.rows[first], "id").to_owned()));
            }
        }
        self.reveal_focus(rows);
    }

    pub(super) fn reveal_attention(&mut self, rows: u16) {
        let mut order = self
            .sections
            .iter()
            .flat_map(|(_, indexes)| indexes)
            .copied()
            .collect::<Vec<_>>();
        if let Some(index) = order
            .iter()
            .position(|&i| field(&self.rows[i], "id") == self.selected)
        {
            let len = order.len();
            order.rotate_left((index + 1) % len);
        }
        if let Some(target) = order
            .into_iter()
            .map(|i| &self.rows[i])
            .find(|row| matches!(field(row, "group"), "needs_input" | "ready_for_review"))
        {
            self.folded
                .remove(&(self.layout.group, self.section(target)));
            self.set_focus(Focus::Session(field(target, "id").to_owned()));
            self.reveal_focus(rows);
        }
    }
}

pub(super) fn board_first(rows: u16) -> u16 {
    if rows >= 12 { 3 } else { 2 }
}

pub(super) fn board_capacity(rows: u16) -> usize {
    usize::from(rows.saturating_sub(board_first(rows)).max(1))
}

pub(super) fn capacity(rows: u16) -> usize {
    usize::from(rows.saturating_sub(2).max(1))
}

pub(super) fn usable((cols, rows): (u16, u16)) -> bool {
    cols >= 24 && rows >= 4
}

pub(super) fn truncation(text: &str, cols: u16) -> String {
    let clipped = clip(text, cols);
    if clipped == text || clipped.chars().count() == text.chars().count() {
        clipped
    } else {
        format!("{}…", clip(text, cols.saturating_sub(1)))
    }
}
