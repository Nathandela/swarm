use super::{Action, Key, capacity, form_header, line, usable};
use crate::schedule::catalog::{Read, Records};

#[derive(Default)]
pub(super) struct Automation {
    records: Option<Records>,
    error: String,
    selected: usize,
    job: String,
}

impl Automation {
    pub(super) fn receive(&mut self, response: std::io::Result<Records>) {
        self.error.clear();
        match response {
            Ok(records) => {
                self.records = Some(records);
                self.selected = 0;
            }
            Err(error) => self.error = error.to_string(),
        }
    }

    pub(super) fn update(&mut self, key: Key, geometry: (u16, u16)) -> Action {
        if !usable(geometry) {
            return Action::None;
        }
        match (&self.records, key) {
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
        form_header(output, cols, "Automations");
        let (items, hint) = match &self.records {
            Some(Records::List(jobs)) if jobs.is_empty() => {
                (vec!["No automations".into()], "u refresh · esc back")
            }
            Some(Records::List(jobs)) => (
                jobs.iter()
                    .map(|job| format!("{} · {}", job.name(), job.state))
                    .collect(),
                "↑↓ select · enter detail · u refresh · esc back",
            ),
            Some(Records::Get(job)) => (
                vec![
                    job.name().to_owned(),
                    format!("Status · {}", job.state),
                    format!("Directory · {}", job.directory()),
                    format!("Agents · {}", job.agents().join(" → ")),
                ],
                "h run history · ← list · esc back",
            ),
            Some(Records::History(runs)) if runs.is_empty() => (
                vec!["No runs for this automation".into()],
                "← detail · esc back",
            ),
            Some(Records::History(runs)) => (
                runs.iter()
                    .map(|run| format!("{} · {} · {}", run.due_at, run.state, run.detail))
                    .collect(),
                "↑↓ select · ← detail · esc back",
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
        line(output, rows, cols, "\x1b[2m", hint);
    }
}
