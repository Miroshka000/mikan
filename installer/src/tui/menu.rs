//! The menu of an installed server: status, updates, logs, access, REALITY sites, nodes
//! and backups. Every action is a `mikan` command too.

use std::collections::VecDeque;
use std::path::PathBuf;
use std::process::Child;
use std::sync::mpsc::{self, Receiver};
use std::thread;
use std::time::{Duration, Instant};

use ratatui::Frame;
use ratatui::crossterm::event::{KeyCode, KeyEvent, KeyModifiers};
use ratatui::layout::{Constraint, Layout, Rect};
use ratatui::style::{Style, Stylize};
use ratatui::text::{Line, Span};
use ratatui::widgets::{Block, BorderType, Borders, Clear, LineGauge, Padding, Paragraph};

use super::Screen;
use super::widgets::{self, ACCENT, DIM, ERR, FAINT, Input, OK, Task, WARN, center, dim, field, item, spinner, wrap};
use crate::envfile::EnvFile;
use crate::lock::{self, Wait};
use crate::ops::{self, Install};
use crate::release::{self, Manifest};
use crate::sites::{self, Site};
use crate::update::{self, UpdateArgs};
use crate::{DIR, backup, docker, system};

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
enum Section {
    Status,
    Update,
    Logs,
    Access,
    Sites,
    Nodes,
    Backups,
    Join,
    Uninstall,
}

impl Section {
    fn label(self) -> &'static str {
        match self {
            Section::Status => "Status",
            Section::Update => "Update",
            Section::Logs => "Logs",
            Section::Access => "Access",
            Section::Sites => "REALITY sites",
            Section::Nodes => "Nodes",
            Section::Backups => "Backups",
            Section::Join => "Join key",
            Section::Uninstall => "Uninstall",
        }
    }

    fn lead(self) -> &'static str {
        match self {
            Section::Status => "Containers, resources and the release. Refreshes every 10 seconds.",
            Section::Update => "Updates pull the signed release, back up first and go back if the new version does not start.",
            Section::Logs => "The last lines of the panel and the node, live.",
            Section::Access => "The panel's link and the admin's login.",
            Section::Sites => {
                "The sites whose TLS probes of the VPN ports see. Look for sites next to the server and point every REALITY inbound at one."
            }
            Section::Nodes => "Other servers the panel drives. A new node gets a one-line install command with its key.",
            Section::Backups => "The database, certificates and settings, in /opt/mikan/backups.",
            Section::Join => "The node reaches its panel with the key from the panel's Nodes page.",
            Section::Uninstall => {
                "Stops mikan and removes the mikan command, the update timer and the kernel tuning. The data and backups stay in /opt/mikan."
            }
        }
    }
}

struct Status {
    services: Vec<docker::Service>,
    stats: Vec<docker::Stats>,
    healthy: bool,
}

enum Upd {
    Line(String),
    Progress(f64),
    Done(Result<(), String>),
}

#[derive(Clone)]
enum Act {
    ResetPassword,
    ResetPath,
    Disable2fa,
    NodeName,
    NodeHost(String),
    NodeKey,
    Backup,
    Restore(PathBuf),
    Join,
    Uninstall,
}

enum Prompt {
    Confirm { question: String, then: Act },
    Text { label: String, hint: String, input: Input, then: Act },
}

type Job = Task<Result<String, String>>;

fn job(f: impl FnOnce() -> anyhow::Result<String> + Send + 'static) -> Job {
    Task::start(move || f().map_err(|e| format!("{e:#}")))
}

/// Runs `mikan admin …` in the panel and returns what it printed.
fn admin_text(args: &[&str]) -> anyhow::Result<String> {
    let out = docker::check(docker::admin(args, None)?)?;
    let mut text = String::from_utf8_lossy(&out.stdout).trim_end().to_owned();
    let err = String::from_utf8_lossy(&out.stderr);
    if !err.trim().is_empty() {
        if !text.is_empty() {
            text.push('\n');
        }
        text.push_str(err.trim_end());
    }
    Ok(text)
}

pub struct Menu {
    install: Install,
    version: String,
    sections: Vec<Section>,
    at: usize,
    inside: bool,
    tick: usize,
    status: Task<Status>,
    status_at: Instant,
    latest: Task<anyhow::Result<Manifest>>,
    updating: Option<Receiver<Upd>>,
    upd_lines: Vec<String>,
    upd_progress: Option<f64>,
    upd_result: Option<Result<(), String>>,
    logs: Option<(Child, Receiver<String>)>,
    log_lines: VecDeque<String>,
    log_filter: usize,
    log_back: usize,
    pick: usize,
    prompt: Option<Prompt>,
    job: Job,
    job_title: String,
    job_at: Section,
    last_status: Option<Status>,
    /// What stays in the terminal after the menu closes: new keys, links, passwords.
    notes: Vec<String>,
    url: Job,
    nodes: Job,
    backups: Vec<PathBuf>,
    scan: Task<anyhow::Result<sites::Scan>>,
    rows: Vec<Site>,
    gone: bool,
    /// Whether the panel's settings have automatic updates on (a file of the panel's, read
    /// every few seconds with the status, not on every frame).
    policy_on: bool,
    /// When a quit was refused because an operation runs.
    denied: Option<Instant>,
}

impl Menu {
    pub fn new() -> anyhow::Result<Self> {
        let install = Install::load()?;
        let version = install.version();
        let sections = if install.node {
            vec![Section::Status, Section::Update, Section::Logs, Section::Join, Section::Backups, Section::Uninstall]
        } else {
            vec![
                Section::Status,
                Section::Update,
                Section::Logs,
                Section::Access,
                Section::Sites,
                Section::Nodes,
                Section::Backups,
                Section::Uninstall,
            ]
        };
        let mut m = Self {
            install,
            version,
            sections,
            at: 0,
            inside: false,
            tick: 0,
            status: Task::Idle,
            status_at: Instant::now(),
            latest: Task::start(release::latest),
            updating: None,
            upd_lines: Vec::new(),
            upd_progress: None,
            upd_result: None,
            logs: None,
            log_lines: VecDeque::new(),
            log_filter: 0,
            log_back: 0,
            pick: 0,
            prompt: None,
            job: Task::Idle,
            job_title: String::new(),
            job_at: Section::Status,
            last_status: None,
            notes: Vec::new(),
            url: Task::Idle,
            nodes: Task::Idle,
            backups: Vec::new(),
            scan: Task::Idle,
            rows: Vec::new(),
            gone: false,
            policy_on: false,
            denied: None,
        };
        m.refresh_status();
        Ok(m)
    }

    pub fn farewell(&self) -> Option<String> {
        let mut parts = self.notes.clone();
        if self.gone {
            parts.push(format!("mikan is stopped and the mikan command removed. The data and backups stay in {}.", crate::DIR));
        }
        (!parts.is_empty()).then(|| parts.join("\n\n"))
    }

    fn section(&self) -> Section {
        self.sections[self.at]
    }

    /// Whether an update, a backup, a restore or another change is running: the menu does
    /// not close on it, because the process would end with it half done.
    fn busy(&self) -> bool {
        self.updating.is_some() || self.job.running()
    }

    /// Quits unless something is running; then it says why not.
    fn quit(&mut self) -> bool {
        if self.busy() {
            self.denied = Some(Instant::now());
            return false;
        }
        self.stop_logs();
        true
    }

    /// Switches a node's automatic updates. .env is read again first: the node's own timer
    /// may have changed its image since the menu opened, and saving the old copy would put
    /// the old version back.
    fn toggle_auto(&mut self) {
        let mut quiet = |_: &str| {};
        let result = (|| -> anyhow::Result<()> {
            let Some(_lock) = lock::acquire(Wait::Skip, &mut quiet)? else {
                anyhow::bail!("an update or another change is running: try again when it ends");
            };
            let mut env = EnvFile::load(std::path::Path::new(DIR).join(".env"))?;
            let on = env.get("MIKAN_AUTO_UPDATE") == Some("1");
            env.set("MIKAN_AUTO_UPDATE", if on { "0" } else { "1" })?;
            env.save()?;
            self.install.env = env;
            Ok(())
        })();
        if let Err(e) = result {
            self.upd_result = Some(Err(format!("not changed: {e:#}")));
        }
    }

    fn refresh_status(&mut self) {
        self.policy_on = update::policy_on();
        let node_port = self.install.node_port();
        self.status = Task::start(move || {
            let services = docker::services().unwrap_or_default();
            Status {
                stats: docker::stats(&services),
                healthy: match node_port {
                    Some(p) => system::port_owner(p, system::Proto::Tcp).is_some(),
                    None => docker::panel_healthy(),
                },
                services,
            }
        });
        self.status_at = Instant::now();
    }

    fn newer(&self) -> Option<&Manifest> {
        match self.latest.done() {
            Some(Ok(m)) if release::newer(&m.version, &self.version) => Some(m),
            _ => None,
        }
    }

    /// What a section loads when it is opened.
    fn open(&mut self) {
        self.pick = 0;
        self.prompt = None;
        if self.section() != Section::Logs {
            self.stop_logs();
        }
        match self.section() {
            Section::Logs if self.logs.is_none() => self.start_logs(),
            Section::Access if self.url.idle() => self.url = job(|| admin_text(&["url"])),
            Section::Nodes if self.nodes.idle() => self.nodes = job(|| admin_text(&["node", "list"])),
            Section::Backups => self.backups = backup::backups(),
            _ => {}
        }
    }

    fn start_logs(&mut self) {
        self.stop_logs();
        self.log_lines.clear();
        self.log_back = 0;
        let mut args = vec!["logs", "-f", "--tail", "300", "--no-color"];
        match self.log_filter {
            1 if !self.install.node => args.push("panel"),
            2 => args.push("node"),
            _ => {}
        }
        match docker::follow(docker::compose(&args)) {
            Ok(l) => self.logs = Some(l),
            Err(e) => self.log_lines.push_back(format!("cannot read the logs: {e:#}")),
        }
    }

    fn stop_logs(&mut self) {
        if let Some((mut child, _)) = self.logs.take() {
            let _ = child.kill();
            let _ = child.wait();
        }
    }

    fn start_update(&mut self) {
        let (tx, rx) = mpsc::channel();
        thread::spawn(move || {
            let lines = tx.clone();
            let progress = tx.clone();
            let r = update::update(
                &UpdateArgs::default(),
                &mut |l| {
                    let _ = lines.send(Upd::Line(l.to_owned()));
                },
                &mut |p| {
                    let _ = progress.send(Upd::Progress(p));
                },
            );
            let _ = tx.send(Upd::Done(r.map_err(|e| format!("{e:#}"))));
        });
        self.updating = Some(rx);
        self.upd_lines.clear();
        self.upd_result = None;
        self.upd_progress = None;
    }

    /// The rows of the content an action list has, for ↑↓.
    fn rows_len(&self) -> usize {
        match self.section() {
            Section::Access => 3,
            Section::Nodes => 2,
            Section::Backups => 1 + self.backups.len(),
            Section::Sites => self.rows.len(),
            _ => 0,
        }
    }

    fn enter(&mut self) {
        if self.job.running() {
            return;
        }
        let ask = |q: &str, then| Some(Prompt::Confirm { question: q.to_owned(), then });
        let text = |label: &str, hint: &str, then| {
            Some(Prompt::Text { label: label.to_owned(), hint: hint.to_owned(), input: Input::default(), then })
        };
        match self.section() {
            Section::Status => self.refresh_status(),
            Section::Update => {
                if self.updating.is_none() && self.newer().is_some() {
                    self.start_update();
                }
            }
            Section::Access => {
                self.prompt = match self.pick {
                    0 => ask("Set a new admin password? All sessions end.", Act::ResetPassword),
                    1 => ask("Give the panel a new secret link? The old one stops working.", Act::ResetPath),
                    _ => ask("Turn off the admin's 2FA?", Act::Disable2fa),
                }
            }
            Section::Nodes => {
                self.prompt = if self.pick == 0 {
                    text("Name of the node", "its group in subscriptions, like 🇺🇸 USA", Act::NodeName)
                } else {
                    text("Node ID", "from the list above; the old key stops working", Act::NodeKey)
                }
            }
            Section::Backups => {
                self.prompt = if self.pick == 0 {
                    ask("Back up now?", Act::Backup)
                } else {
                    let f = self.backups[self.pick - 1].clone();
                    let name = f.file_name().map(|n| n.to_string_lossy().into_owned()).unwrap_or_default();
                    ask(&format!("Replace the current data with {name}? mikan restarts."), Act::Restore(f))
                }
            }
            Section::Sites => match self.scan.done() {
                Some(Ok(scan)) if self.pick < self.rows.len() => {
                    let site = self.rows[self.pick].clone();
                    let own = scan.self_steal.clone();
                    self.job_title = format!("REALITY site {}", site.sni);
                    self.job_at = Section::Sites;
                    self.job = job(move || sites::apply(&site, own.as_ref()));
                }
                Some(Ok(_)) => {}
                _ if !self.scan.running() => self.scan = Task::start(sites::scan),
                _ => {}
            },
            Section::Join => self.prompt = text("New join key", "from the panel's Nodes page", Act::Join),
            Section::Uninstall => {
                if self.gone {
                    return;
                }
                self.prompt = ask("Stop mikan and remove the mikan command? The data stays.", Act::Uninstall);
            }
            Section::Logs => {}
        }
    }

    fn act(&mut self, a: Act, value: String) {
        let (title, j): (&str, Job) = match a {
            Act::ResetPassword => ("New admin password", job(|| admin_text(&["reset-password"]))),
            Act::ResetPath => ("New secret link", job(|| admin_text(&["reset-path"]))),
            Act::Disable2fa => ("2FA", job(|| admin_text(&["disable-2fa"]))),
            Act::NodeName => {
                self.prompt = Some(Prompt::Text {
                    label: "The node server's IP".into(),
                    hint: "or its host name".into(),
                    input: Input::default(),
                    then: Act::NodeHost(value),
                });
                return;
            }
            Act::NodeHost(name) => (
                "New node",
                job(move || {
                    let out = docker::check(docker::admin(&["node", "add", "--name", name.as_str(), "--host", value.as_str()], None)?)?;
                    let key = String::from_utf8_lossy(&out.stdout).trim().to_owned();
                    Ok(format!("Node {name} is added. On its server, as root, run:\n\n{}", release::join_command(&key)))
                }),
            ),
            Act::NodeKey => (
                "New node key",
                job(move || {
                    let out = docker::check(docker::admin(&["node", "key", value.as_str()], None)?)?;
                    let key = String::from_utf8_lossy(&out.stdout).trim().to_owned();
                    Ok(format!(
                        "The old key no longer works. On the node's server run `mikan join` and paste this key when asked (it holds the node's private key, so it stays out of the command line):\n\n{key}"
                    ))
                }),
            ),
            Act::Backup => ("Backup", job(|| backup::backup(&mut |_| {}).map(|f| format!("Saved {}", f.display())))),
            Act::Restore(f) => {
                ("Restore", job(move || backup::restore(&f, false, &mut |_| {}).map(|()| format!("Restored from {}", f.display()))))
            }
            Act::Join => ("Join key", job(move || ops::join(&value, None).map(|()| "The node runs with the new key.".into()))),
            Act::Uninstall => ("Uninstall", job(|| ops::uninstall().map(|_| "Done. Press Enter to leave.".into()))),
        };
        self.job_title = title.to_owned();
        self.job_at = self.section();
        self.job = j;
    }

    fn prompt_key(&mut self, k: KeyEvent) {
        let Some(p) = &mut self.prompt else { return };
        match p {
            Prompt::Confirm { then, .. } => match k.code {
                KeyCode::Enter | KeyCode::Char('y') => {
                    let a = then.clone();
                    self.prompt = None;
                    self.act(a, String::new());
                }
                KeyCode::Esc | KeyCode::Char('n') => self.prompt = None,
                _ => {}
            },
            Prompt::Text { input, then, .. } => match k.code {
                KeyCode::Enter if !input.text().is_empty() => {
                    let (a, v) = (then.clone(), input.text());
                    self.prompt = None;
                    self.act(a, v);
                }
                KeyCode::Esc => self.prompt = None,
                _ => {
                    input.key(k);
                }
            },
        }
    }
}

impl Screen for Menu {
    fn tick(&mut self) {
        self.tick = self.tick.wrapping_add(1);
        if self.status.poll() {
            self.last_status = self.status.take().or(self.last_status.take());
        }
        // Every refresh starts docker processes (`docker stats` samples for a second or two):
        // on a small server, every 10 s is enough.
        if self.section() == Section::Status && !self.status.running() && self.status_at.elapsed() > Duration::from_secs(10) {
            self.refresh_status();
        }
        self.latest.poll();
        self.url.poll();
        self.nodes.poll();
        if self.scan.poll()
            && let Some(Ok(s)) = self.scan.done()
        {
            self.rows = s.self_steal.iter().filter(|o| o.ok).cloned().chain(s.results.iter().cloned()).collect();
            let best = sites::best(s);
            self.pick = best.and_then(|b| self.rows.iter().position(|r| *r == b)).unwrap_or(0);
        }
        if self.job.poll() {
            if let (Section::Access | Section::Nodes, Some(Ok(text))) = (self.job_at, self.job.done()) {
                self.notes.push(format!("{}:\n{text}", self.job_title));
            }
            match self.job_at {
                Section::Backups => self.backups = backup::backups(),
                Section::Uninstall => self.gone = matches!(self.job.done(), Some(Ok(_))),
                Section::Access => self.url = job(|| admin_text(&["url"])),
                Section::Nodes => self.nodes = job(|| admin_text(&["node", "list"])),
                _ => {}
            }
        }
        if let Some((_, rx)) = &self.logs {
            for l in rx.try_iter() {
                self.log_lines.push_back(l);
                if self.log_lines.len() > 2000 {
                    self.log_lines.pop_front();
                }
            }
        }
        if let Some(rx) = &self.updating {
            let msgs: Vec<Upd> = rx.try_iter().collect();
            for m in msgs {
                match m {
                    Upd::Line(l) => self.upd_lines.push(l),
                    Upd::Progress(p) => self.upd_progress = Some(p),
                    Upd::Done(r) => {
                        self.upd_result = Some(r);
                        self.updating = None;
                        self.upd_progress = None;
                        if let Ok(i) = Install::load() {
                            self.version = i.version();
                            self.install = i;
                        }
                        break;
                    }
                }
            }
        }
    }

    fn paste(&mut self, text: &str) {
        if let Some(Prompt::Text { input, .. }) = &mut self.prompt {
            input.paste(text);
        }
    }

    fn key(&mut self, k: KeyEvent) -> bool {
        if k.code == KeyCode::Char('c') && k.modifiers.contains(KeyModifiers::CONTROL) {
            return self.quit();
        }
        if self.prompt.is_some() {
            self.prompt_key(k);
            return false;
        }
        if self.gone {
            return matches!(k.code, KeyCode::Enter | KeyCode::Esc | KeyCode::Char('q'));
        }
        if !self.inside {
            match k.code {
                KeyCode::Up | KeyCode::Char('k') => {
                    self.at = self.at.saturating_sub(1);
                    self.open();
                }
                KeyCode::Down | KeyCode::Char('j') => {
                    self.at = (self.at + 1).min(self.sections.len() - 1);
                    self.open();
                }
                KeyCode::Enter | KeyCode::Right | KeyCode::Tab => {
                    self.inside = true;
                    self.open();
                    if matches!(self.section(), Section::Update | Section::Status | Section::Join | Section::Uninstall) {
                        self.enter();
                    }
                }
                KeyCode::Char('q') | KeyCode::Esc => return self.quit(),
                _ => {}
            }
            return false;
        }
        match k.code {
            KeyCode::Esc | KeyCode::Left => self.inside = false,
            KeyCode::Up | KeyCode::Char('k') if self.section() == Section::Logs => self.log_back += 1,
            KeyCode::Down | KeyCode::Char('j') if self.section() == Section::Logs => self.log_back = self.log_back.saturating_sub(1),
            KeyCode::PageUp if self.section() == Section::Logs => self.log_back += 20,
            KeyCode::PageDown if self.section() == Section::Logs => self.log_back = self.log_back.saturating_sub(20),
            KeyCode::Tab if self.section() == Section::Logs => {
                self.log_filter = (self.log_filter + 1) % if self.install.node { 1 } else { 3 };
                self.start_logs();
            }
            KeyCode::Up | KeyCode::Char('k') => self.pick = self.pick.saturating_sub(1),
            KeyCode::Down | KeyCode::Char('j') => self.pick = (self.pick + 1).min(self.rows_len().saturating_sub(1)),
            KeyCode::Enter => self.enter(),
            KeyCode::Char('r') if self.section() == Section::Sites && !self.scan.running() => self.scan = Task::start(sites::scan),
            KeyCode::Char('a') if self.section() == Section::Update && self.install.node => self.toggle_auto(),
            KeyCode::Char('q') => return self.quit(),
            _ => {}
        }
        false
    }

    fn draw(&mut self, f: &mut Frame) {
        let area = f.area();
        if area.width < 72 || area.height < 22 {
            f.render_widget(
                Paragraph::new(vec![Line::from("mikan".fg(ACCENT).bold()), Line::from(dim("Make the terminal at least 72×22."))]),
                area,
            );
            return;
        }
        let card = center(area, area.width.min(112), area.height.min(36));
        f.render_widget(Clear, card);
        let kind = if self.install.node { "node" } else { "panel" };
        let refused = self.denied.is_some_and(|t| t.elapsed() < Duration::from_secs(4)) && self.busy();
        let keys: &[(&str, &str)] = if refused {
            &[("wait", "an operation is running: closing now would stop it half way")]
        } else if self.prompt.is_some() {
            &[("enter", "confirm"), ("esc", "cancel")]
        } else if !self.inside {
            &[("↑↓", "section"), ("enter", "open"), ("q", "quit")]
        } else if self.section() == Section::Logs {
            &[("↑↓ pgup pgdn", "scroll"), ("tab", "panel · node · all"), ("esc", "back")]
        } else {
            &[("↑↓", "choose"), ("enter", "run"), ("esc", "back"), ("q", "quit")]
        };
        let block = Block::bordered()
            .border_type(BorderType::Rounded)
            .border_style(Style::new().fg(FAINT))
            .title(Line::from(vec![Span::raw(" "), "●".fg(ACCENT), Span::raw(" "), "mikan".bold(), Span::raw(" ")]))
            .title(Line::from(dim(format!(" {kind} {} ", self.version))).right_aligned())
            .title_bottom(widgets::keys(keys))
            .padding(Padding::new(2, 2, 1, 0));
        let inner = block.inner(card);
        f.render_widget(block, card);
        let [side, content] = Layout::horizontal([Constraint::Length(20), Constraint::Min(0)]).areas(inner);
        self.draw_sidebar(f, side);
        let sep = Block::new().borders(Borders::LEFT).border_style(Style::new().fg(FAINT)).padding(Padding::left(2));
        let body = sep.inner(content);
        f.render_widget(sep, content);
        self.draw_content(f, body);
    }
}

impl Menu {
    fn draw_sidebar(&self, f: &mut Frame, area: Rect) {
        let mut lines = Vec::new();
        for (i, s) in self.sections.iter().enumerate() {
            if *s == Section::Uninstall {
                let pad = (area.height as usize).saturating_sub(lines.len() + 1);
                lines.extend(std::iter::repeat_n(Line::from(""), pad.min(40)));
            }
            let sel = i == self.at;
            let marker =
                if sel { Span::styled("› ", Style::new().fg(if self.inside { DIM } else { ACCENT }).bold()) } else { Span::raw("  ") };
            let style = if sel {
                Style::new().bold()
            } else {
                Style::new().fg(if *s == Section::Uninstall { DIM } else { ratatui::style::Color::Reset })
            };
            let mut spans = vec![marker, Span::styled(s.label(), style)];
            if *s == Section::Update && self.newer().is_some() {
                spans.push(Span::styled(" ●", Style::new().fg(ACCENT)));
            }
            lines.push(Line::from(spans));
        }
        f.render_widget(Paragraph::new(lines), area);
    }

    fn draw_content(&self, f: &mut Frame, area: Rect) {
        let s = self.section();
        let lead = wrap(s.lead(), area.width as usize);
        let head_h = 2 + lead.len() as u16;
        let [head, body] = Layout::vertical([Constraint::Length(head_h), Constraint::Min(0)]).areas(area);
        let mut lines = vec![Line::from(s.label().bold())];
        lines.extend(lead.into_iter().map(|l| Line::from(dim(l))));
        f.render_widget(Paragraph::new(lines), head);
        match s {
            Section::Status => self.draw_status(f, body),
            Section::Update => self.draw_update(f, body),
            Section::Logs => self.draw_logs(f, body),
            Section::Access => self.draw_access(f, body),
            Section::Sites => self.draw_sites(f, body),
            Section::Nodes => self.draw_nodes(f, body),
            Section::Backups => self.draw_backups(f, body),
            Section::Join | Section::Uninstall => self.draw_job(f, body, Vec::new()),
        }
        if let Some(p) = &self.prompt {
            self.draw_prompt(f, body, p);
        }
    }

    fn draw_status(&self, f: &mut Frame, area: Rect) {
        let mut lines = Vec::new();
        match &self.last_status {
            None => lines.push(Line::from(vec![spinner(self.tick), Span::raw(" reading…")])),
            Some(st) => {
                for s in &st.services {
                    let ok = s.state == "running";
                    let mark = if ok { Span::styled("● ", Style::new().fg(OK)) } else { Span::styled("● ", Style::new().fg(ERR)) };
                    let stats = st.stats.iter().find(|x| x.name.contains(&format!("-{}-", s.name)) || x.name.ends_with(&s.name));
                    let usage =
                        stats.map(|x| format!("CPU {:>6}   memory {}", x.cpu, x.mem.split(" / ").next().unwrap_or(""))).unwrap_or_default();
                    lines.push(Line::from(vec![
                        mark,
                        Span::styled(format!("{:<7}", s.name), Style::new().bold()),
                        dim(format!("{:<28}", s.status)),
                        Span::raw(usage),
                    ]));
                }
                if st.services.is_empty() {
                    lines.push(Line::from(Span::styled("No containers: docker compose up -d in /opt/mikan", Style::new().fg(ERR))));
                }
                lines.push(Line::from(""));
                lines.push(match (self.install.node_port(), st.healthy) {
                    (Some(p), true) => Line::from(vec![
                        Span::styled("✓ ", Style::new().fg(OK)),
                        Span::raw(format!("The node waits for its panel on port {p}.")),
                    ]),
                    (Some(p), false) => {
                        Line::from(Span::styled(format!("✗ The node does not listen on port {p}: see Logs."), Style::new().fg(ERR)))
                    }
                    (None, true) => Line::from(vec![Span::styled("✓ ", Style::new().fg(OK)), Span::raw("The panel answers.")]),
                    (None, false) => Line::from(Span::styled("✗ The panel does not answer: see Logs.", Style::new().fg(ERR))),
                });
            }
        }
        match self.latest.done() {
            None => lines.push(Line::from(vec![spinner(self.tick), dim(" looking for a new release…")])),
            Some(Err(e)) => {
                lines.extend(wrap(&format!("Cannot check for updates: {e:#}"), area.width as usize).into_iter().map(|l| Line::from(dim(l))))
            }
            Some(Ok(m)) if release::newer(&m.version, &self.version) => lines.push(Line::from(vec![
                Span::styled("● ", Style::new().fg(ACCENT)),
                Span::raw(format!("mikan {} is out: see Update.", m.version)),
            ])),
            Some(Ok(_)) => lines.push(Line::from(vec![
                Span::styled("✓ ", Style::new().fg(OK)),
                Span::raw(format!("mikan {} is the latest release.", self.version)),
            ])),
        }
        f.render_widget(Paragraph::new(lines), area);
    }

    fn draw_update(&self, f: &mut Frame, area: Rect) {
        let w = area.width as usize;
        let mut lines = field("This server", self.version.clone(), w);
        match self.latest.done() {
            None => lines.push(Line::from(vec![spinner(self.tick), dim(" reading the latest release…")])),
            Some(Err(e)) => {
                for l in wrap(&format!("Cannot read the latest release: {e:#}"), area.width as usize) {
                    lines.push(Line::from(Span::styled(l, Style::new().fg(ERR))));
                }
            }
            Some(Ok(m)) => {
                lines.extend(field("Latest", format!("{} ({})", m.version, m.published.get(..10).unwrap_or(&m.published)), w));
                if release::newer(&m.version, &self.version) && self.updating.is_none() && self.upd_result.is_none() {
                    lines.push(Line::from(""));
                    if let Some(notes) = m.notes.get("en") {
                        for l in notes.lines().take(8) {
                            for w in wrap(l, area.width as usize) {
                                lines.push(Line::from(dim(w)));
                            }
                        }
                        lines.push(Line::from(""));
                    }
                    lines.push(Line::from(vec![Span::styled("› ", Style::new().fg(ACCENT).bold()), "Update now".bold(), dim("   enter")]));
                }
            }
        }
        let auto = if self.install.node {
            let on = self.install.env.get("MIKAN_AUTO_UPDATE") == Some("1");
            format!("{}   a to switch", if on { "on" } else { "off" })
        } else {
            let on = self.policy_on;
            format!("{}   switched in the panel's settings", if on { "on" } else { "off" })
        };
        lines.push(Line::from(""));
        lines.extend(field("Automatic", auto, w));
        if self.updating.is_some() || self.upd_result.is_some() {
            lines.push(Line::from(""));
            for l in &self.upd_lines {
                lines.push(Line::from(Span::raw(l.clone())));
            }
            if self.updating.is_some() {
                lines.push(Line::from(vec![spinner(self.tick), dim(" updating…")]));
            }
            match &self.upd_result {
                Some(Ok(())) => lines.push(Line::from(Span::styled("✓ Done.", Style::new().fg(OK)))),
                Some(Err(e)) => {
                    for w in wrap(e, area.width as usize) {
                        lines.push(Line::from(Span::styled(w, Style::new().fg(ERR))));
                    }
                }
                None => {}
            }
        }
        let n = lines.len() as u16;
        f.render_widget(Paragraph::new(lines), area);
        if let Some(p) = self.upd_progress {
            let g =
                LineGauge::default().filled_style(Style::new().fg(ACCENT)).unfilled_style(Style::new().fg(FAINT)).ratio(p.clamp(0.0, 1.0));
            f.render_widget(g, Rect { y: area.y + n.min(area.height.saturating_sub(1)), height: 1, width: area.width.min(50), ..area });
        }
    }

    fn draw_logs(&self, f: &mut Frame, area: Rect) {
        let filter = ["all", "panel", "node"][self.log_filter];
        let [bar, body] = Layout::vertical([Constraint::Length(2), Constraint::Min(0)]).areas(area);
        let follow = if self.log_back == 0 { "following" } else { "scrolled back" };
        f.render_widget(Paragraph::new(Line::from(vec![dim("showing "), filter.bold(), dim(format!(" · {follow}"))])), bar);
        let h = body.height as usize;
        let end = self.log_lines.len().saturating_sub(self.log_back.min(self.log_lines.len().saturating_sub(h)));
        let start = end.saturating_sub(h);
        let w = body.width as usize;
        let lines: Vec<Line> = self
            .log_lines
            .range(start..end)
            .map(|l| {
                // compose prefixes "panel-1  | "; a short colored tag leaves room for the line.
                let (tag, text) = match l.split_once(" | ") {
                    Some((svc, rest)) => (svc.trim().trim_end_matches("-1").to_owned(), rest),
                    None => (String::new(), l.as_str()),
                };
                let tag_color = if tag == "panel" { ACCENT } else { DIM };
                let color = if text.contains("level=ERROR") || text.contains("level=error") {
                    ERR
                } else if text.contains("WARN") || text.contains("level=warning") {
                    WARN
                } else {
                    ratatui::style::Color::Reset
                };
                let short: String = text.chars().take(w.saturating_sub(7)).collect();
                Line::from(vec![
                    Span::styled(format!("{tag:<6} "), Style::new().fg(tag_color)),
                    Span::styled(short, Style::new().fg(color)),
                ])
            })
            .collect();
        f.render_widget(Paragraph::new(lines), body);
    }

    fn actions(&self, items: &[(&str, &str)], width: usize) -> Vec<Line<'static>> {
        items.iter().enumerate().map(|(i, (l, h))| item(l, h, self.inside && i == self.pick, width)).collect()
    }

    fn draw_access(&self, f: &mut Frame, area: Rect) {
        let w = area.width as usize;
        let mut lines = match self.url.done() {
            None => vec![Line::from(vec![spinner(self.tick), dim(" reading the link…")])],
            Some(Ok(t)) => field("Panel", t.lines().next().unwrap_or("").to_owned(), w),
            Some(Err(e)) => wrap(e, w).into_iter().map(|l| Line::from(Span::styled(l, Style::new().fg(ERR)))).collect(),
        };
        if let Some(Ok(t)) = self.url.done()
            && let Some(login) = t.lines().nth(1).and_then(|l| l.strip_prefix("Login: "))
        {
            lines.extend(field("Login", login.to_owned(), w));
        }
        lines.push(Line::from(""));
        lines.extend(self.actions(
            &[
                ("New password", "all sessions end; shown once"),
                ("New secret link", "the old link stops working"),
                ("Turn off 2FA", "when the phone with the codes is lost"),
            ],
            area.width as usize,
        ));
        self.draw_job(f, area, lines);
    }

    fn draw_nodes(&self, f: &mut Frame, area: Rect) {
        let mut lines = match self.nodes.done() {
            None => vec![Line::from(vec![spinner(self.tick), dim(" reading the nodes…")])],
            Some(Ok(t)) => t.lines().map(|l| Line::from(Span::raw(l.to_owned()))).collect(),
            Some(Err(e)) => vec![Line::from(Span::styled(e.clone(), Style::new().fg(ERR)))],
        };
        lines.push(Line::from(""));
        lines.extend(self.actions(
            &[("Add a node", "prints the install command with its key"), ("New key", "for a node whose key leaked")],
            area.width as usize,
        ));
        self.draw_job(f, area, lines);
    }

    fn draw_backups(&self, f: &mut Frame, area: Rect) {
        let w = area.width as usize;
        let mut lines = vec![item("Back up now", "", self.inside && self.pick == 0, w), Line::from("")];
        if self.backups.is_empty() {
            lines.push(Line::from(dim("No backups yet. Updates back up by themselves.")));
        }
        for (i, b) in self.backups.iter().enumerate().take(12) {
            let name = b.file_name().map(|n| n.to_string_lossy().into_owned()).unwrap_or_default();
            let size = std::fs::metadata(b).map(|m| format!("{:.1} MB", m.len() as f64 / 1e6)).unwrap_or_default();
            lines.push(item(&name, &format!("{size}   enter restores"), self.inside && self.pick == i + 1, w));
        }
        self.draw_job(f, area, lines);
    }

    fn draw_sites(&self, f: &mut Frame, area: Rect) {
        let mut lines = Vec::new();
        match self.scan.done() {
            None if self.scan.running() => {
                lines.push(Line::from(vec![spinner(self.tick), Span::raw(" Looking for sites next to the server: up to a minute…")]))
            }
            None => lines.push(Line::from(vec!["Enter".bold(), dim(" looks for sites next to the server.")])),
            Some(Err(e)) => lines.push(Line::from(Span::styled(format!("The scan failed: {e:#}"), Style::new().fg(ERR)))),
            Some(Ok(s)) => {
                for t in &s.current {
                    lines.push(Line::from(vec![dim(format!("{:<14}", t.inbound)), Span::raw(t.sni.clone()), dim(format!("  {}", t.dest))]));
                }
                lines.push(Line::from(""));
                for (i, r) in self.rows.iter().enumerate() {
                    let own = s.self_steal.as_ref().is_some_and(|o| o.dest == r.dest);
                    let hint = if own { "your domain".to_owned() } else { format!("{}   {} ms", r.dest, r.rtt_ms) };
                    lines.push(item(&r.sni, &hint, self.inside && i == self.pick, area.width as usize));
                }
                if self.rows.is_empty() {
                    lines.push(Line::from(dim("No site next to the server passed the checks.")));
                }
                lines.push(Line::from(dim("r scans again")));
            }
        }
        self.draw_job(f, area, lines);
    }

    /// The section's lines, then the last action's result under them.
    fn draw_job(&self, f: &mut Frame, area: Rect, mut lines: Vec<Line<'static>>) {
        let here = self.job_at == self.section();
        if here && (self.job.running() || self.job.done().is_some()) {
            lines.push(Line::from(""));
            lines.push(Line::from(Span::styled(self.job_title.clone(), Style::new().bold())));
        }
        match self.job.done() {
            _ if !here => {}
            _ if self.job.running() => lines.push(Line::from(vec![spinner(self.tick), dim(" working…")])),
            Some(Ok(t)) => {
                for l in t.lines() {
                    for w in wrap(l, area.width as usize) {
                        lines.push(Line::from(Span::styled(w, Style::new().fg(OK))));
                    }
                }
            }
            Some(Err(e)) => {
                for w in wrap(e, area.width as usize) {
                    lines.push(Line::from(Span::styled(w, Style::new().fg(ERR))));
                }
            }
            None => {}
        }
        f.render_widget(Paragraph::new(lines), area);
    }

    fn draw_prompt(&self, f: &mut Frame, area: Rect, p: &Prompt) {
        let h = if matches!(p, Prompt::Text { .. }) { 7 } else { 4 };
        let r = Rect { y: area.y + area.height.saturating_sub(h), height: h.min(area.height), ..area };
        f.render_widget(Clear, r);
        let block =
            Block::bordered().border_type(BorderType::Rounded).border_style(Style::new().fg(ACCENT)).padding(Padding::horizontal(1));
        let inner = block.inner(r);
        f.render_widget(block, r);
        match p {
            Prompt::Confirm { question, .. } => {
                let lines =
                    vec![Line::from(question.clone().bold()), Line::from(vec!["enter".bold(), dim(" yes   "), "esc".bold(), dim(" no")])];
                f.render_widget(Paragraph::new(lines), inner);
            }
            Prompt::Text { label, hint, input, .. } => {
                let [l, i, _] = Layout::vertical([Constraint::Length(1), Constraint::Length(3), Constraint::Min(0)]).areas(inner);
                f.render_widget(Paragraph::new(Line::from(vec![label.clone().bold(), dim(format!("   {hint}"))])), l);
                input.draw(f, i, "", true, None);
            }
        }
    }
}

impl Drop for Menu {
    fn drop(&mut self) {
        self.stop_logs();
    }
}
