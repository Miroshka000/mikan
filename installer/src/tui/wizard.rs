//! The first run: language, checks, address, domain, options, then the install itself,
//! the REALITY site and the admin's login.

use std::collections::VecDeque;
use std::net::Ipv4Addr;
use std::sync::mpsc::{self, Receiver};
use std::thread;

use ratatui::Frame;
use ratatui::crossterm::event::{KeyCode, KeyEvent, KeyModifiers};
use ratatui::layout::{Constraint, Layout, Rect};
use ratatui::style::{Style, Stylize};
use ratatui::text::{Line, Span};
use ratatui::widgets::{LineGauge, Paragraph};

use super::Screen;
use super::widgets::{
    ACCENT, Card, DIM, ERR, FAINT, Input, OK, Task, checkbox, dim, field, field_styled, item, level_color, level_span, qr, spinner, wrap,
};
use crate::acme;
use crate::net::{self, DomainCheck};
use crate::setup::{self, Event, Options, Outcome, Plan, Step};
use crate::sites::{self, Site};
use crate::system::{self, Check, Level, Proto};

#[derive(Clone, Copy, PartialEq, Eq)]
enum Page {
    Lang,
    Checks,
    Docker,
    Address,
    Domain,
    Email,
    Proxy,
    Options,
    Confirm,
    Install,
    Sites,
    Done,
}

const LANGS: [(&str, &str, &str); 2] = [
    ("en", "English", "the panel, subscription pages and bot in English"),
    ("ru", "Русский", "админка, страницы подписки и бот на русском"),
];

struct StepView {
    step: Step,
    state: Level,
    running: bool,
    note: String,
    progress: Option<f64>,
}

pub struct Wizard {
    plan: Plan,
    page: Page,
    tick: usize,
    lang: usize,
    checks: Task<Vec<Check>>,
    detect: Task<Option<Ipv4Addr>>,
    host: Input,
    host_err: String,
    domain: Input,
    domain_err: String,
    dns: Task<DomainCheck>,
    dns_for: String,
    email: Input,
    email_err: String,
    /// The nginx or Caddy on port 80 of a domain's server.
    front: Option<acme::Front>,
    port: Input,
    port_err: String,
    ufw: bool,
    focus: usize,
    run: Option<Receiver<Event>>,
    steps: Vec<StepView>,
    log: VecDeque<String>,
    failed: Option<(Step, String)>,
    outcome: Option<Outcome>,
    scan: Task<anyhow::Result<sites::Scan>>,
    rows: Vec<Site>,
    own: Option<Site>,
    own_wait: Task<Option<Site>>,
    pick: usize,
    moved: bool,
    apply: Task<anyhow::Result<String>>,
    applied: Option<String>,
    show_qr: bool,
    quit_armed: bool,
    finished: bool,
    plan_lang_given: bool,
}

impl Wizard {
    pub fn new(opts: Options) -> Self {
        let plan = Plan::from(&opts);
        let node = plan.node();
        let lang = LANGS.iter().position(|l| l.0 == plan.lang).unwrap_or(0);
        let mut w = Self {
            page: if node || opts.lang.is_some() { Page::Checks } else { Page::Lang },
            tick: 0,
            lang,
            checks: Task::Idle,
            detect: Task::Idle,
            host: Input::new(&plan.host),
            host_err: String::new(),
            domain: Input::new(&plan.domain),
            domain_err: String::new(),
            dns: Task::Idle,
            dns_for: String::new(),
            email: Input::new(&plan.email),
            email_err: String::new(),
            front: None,
            port: Input::new(&plan.port.to_string()),
            port_err: String::new(),
            ufw: false,
            focus: 0,
            run: None,
            steps: Vec::new(),
            log: VecDeque::new(),
            failed: None,
            outcome: None,
            scan: Task::Idle,
            rows: Vec::new(),
            own: None,
            own_wait: Task::Idle,
            pick: 0,
            moved: false,
            apply: Task::Idle,
            applied: None,
            show_qr: false,
            quit_armed: false,
            finished: false,
            plan_lang_given: opts.lang.is_some(),
            plan,
        };
        if w.page == Page::Checks {
            w.start_checks();
        }
        w
    }

    fn node(&self) -> bool {
        self.plan.node()
    }

    /// What stays in the terminal after the TUI closes: the admin's login is shown once.
    pub fn farewell(&self) -> Option<String> {
        let o = self.outcome.as_ref()?;
        let mut text = setup::summary(o, false);
        if let Some(a) = &self.applied {
            text.push_str(&format!("\n\nREALITY camouflage:\n{a}"));
        }
        Some(text)
    }

    pub fn result(&self) -> anyhow::Result<()> {
        match (&self.failed, &self.outcome) {
            (Some((s, e)), _) => anyhow::bail!("{}: {e}", s.label()),
            (None, Some(_)) => Ok(()),
            (None, None) => anyhow::bail!("the install was cancelled"),
        }
    }

    fn start_checks(&mut self) {
        let node = self.node();
        self.checks = Task::start(move || system::checks(node));
        if !node && self.host.value.is_empty() && !self.detect.running() {
            self.detect = Task::start(net::public_ipv4);
        }
        self.ufw = system::ufw_active();
    }

    fn numbered(&self) -> Option<(usize, usize)> {
        let pages: &[Page] = if self.node() {
            &[Page::Checks, Page::Confirm]
        } else {
            &[Page::Lang, Page::Checks, Page::Address, Page::Domain, Page::Options, Page::Confirm]
        };
        let page = if matches!(self.page, Page::Email | Page::Proxy) { Page::Domain } else { self.page };
        pages.iter().position(|p| *p == page).map(|i| (i + 1, pages.len()))
    }

    fn after_checks(&self) -> Page {
        if self.node() { Page::Confirm } else { Page::Address }
    }

    fn back(&mut self) {
        self.page = match self.page {
            Page::Docker => Page::Checks,
            Page::Checks if !self.node() && !self.plan_lang_given => Page::Lang,
            Page::Address => Page::Checks,
            Page::Domain => Page::Address,
            Page::Email => Page::Domain,
            Page::Proxy => Page::Email,
            Page::Options => {
                if self.plan.domain.is_empty() {
                    Page::Domain
                } else if self.front.is_some() {
                    Page::Proxy
                } else {
                    Page::Email
                }
            }
            Page::Confirm if self.node() => Page::Checks,
            Page::Confirm => Page::Options,
            p => p,
        };
    }

    fn next(&mut self) {
        match self.page {
            Page::Lang => {
                self.plan.lang = LANGS[self.lang].0.to_owned();
                self.page = Page::Checks;
                self.start_checks();
            }
            Page::Checks => match self.checks.done() {
                Some(c) if c.iter().any(|c| c.level == Level::Error) => self.start_checks(),
                Some(c) if system::docker_to_replace(c).is_some() && !self.plan.replace_docker => self.page = Page::Docker,
                Some(_) => self.page = self.after_checks(),
                None => {}
            },
            Page::Docker => {
                self.plan.replace_docker = true;
                self.page = self.after_checks();
            }
            Page::Address => {
                let h = self.host.text();
                let ok = h.parse::<Ipv4Addr>().is_ok() || net::valid_domain(&h);
                self.host_err = if ok { String::new() } else { "an IPv4 address like 203.0.113.10, or a host name".into() };
                if ok {
                    self.plan.host = h;
                    self.page = Page::Domain;
                }
            }
            Page::Domain => self.domain_next(),
            Page::Email => {
                let e = self.email.text();
                self.email_err =
                    if e.is_empty() || net::valid_email(&e) { String::new() } else { "an address like you@example.com".into() };
                if self.email_err.is_empty() {
                    self.plan.email = e;
                    // A web server on port 80 is asked about before Let's Encrypt goes through it.
                    self.front = acme::front();
                    self.page = if self.front.is_some() { Page::Proxy } else { Page::Options };
                }
            }
            Page::Proxy => {
                self.plan.proxy_rule = true;
                self.page = Page::Options;
            }
            Page::Options => {
                let p = self.port.text();
                self.port_err = check_port(&p);
                if self.port_err.is_empty() {
                    self.plan.port = p.parse().unwrap_or(self.plan.port);
                    self.page = Page::Confirm;
                }
            }
            Page::Confirm => self.start_install(),
            Page::Install => {
                if let Some(o) = &self.outcome {
                    if o.node_port.is_some() {
                        self.page = Page::Done;
                    } else {
                        self.page = Page::Sites;
                        self.scan = Task::start(sites::scan);
                    }
                }
            }
            Page::Sites => self.sites_next(),
            Page::Done => self.finished = true,
        }
    }

    fn domain_next(&mut self) {
        let d = self.domain.text().to_lowercase();
        self.domain_err.clear();
        if d.is_empty() {
            self.plan.domain.clear();
            self.plan.email.clear();
            self.front = None;
            self.page = Page::Options;
            return;
        }
        if !net::valid_domain(&d) {
            self.domain_err = "a domain name like vpn.example.com".into();
            return;
        }
        if self.dns.running() {
            return;
        }
        let checked = self.dns_for == d && self.dns.done().is_some_and(|c| c.level() != Level::Error);
        if checked {
            self.plan.domain = d;
            self.page = Page::Email;
            return;
        }
        let Ok(server) = self.plan.host.parse::<Ipv4Addr>() else {
            // The address is a host name: nothing to compare the A record with.
            self.plan.domain = d;
            self.page = Page::Email;
            return;
        };
        self.dns_for = d.clone();
        self.dns = Task::start(move || net::check_domain(&d, server));
    }

    fn start_install(&mut self) {
        let (tx, rx) = mpsc::channel();
        let plan = self.plan.clone();
        thread::spawn(move || setup::execute(plan, tx));
        self.run = Some(rx);
        self.steps = Step::all(self.node())
            .into_iter()
            .map(|step| StepView { step, state: Level::Ok, running: false, note: String::new(), progress: None })
            .collect();
        self.page = Page::Install;
    }

    fn installing(&self) -> bool {
        self.run.is_some() && self.outcome.is_none() && self.failed.is_none()
    }

    fn take_events(&mut self) {
        let Some(rx) = &self.run else { return };
        let mut events: Vec<Event> = Vec::new();
        let mut gone = false;
        loop {
            match rx.try_recv() {
                Ok(ev) => events.push(ev),
                Err(mpsc::TryRecvError::Empty) => break,
                Err(mpsc::TryRecvError::Disconnected) => {
                    gone = true;
                    break;
                }
            }
        }
        for ev in events {
            match ev {
                Event::Start(s) => self.view(s, |v| v.running = true),
                Event::Progress(s, p) => self.view(s, |v| v.progress = Some(p)),
                Event::Note(s, n) => self.view(s, |v| v.note = n),
                Event::Log(l) => {
                    self.log.push_back(l);
                    if self.log.len() > 4 {
                        self.log.pop_front();
                    }
                }
                Event::Done(s) => {
                    self.view(s, |v| {
                        v.running = false;
                        v.progress = None;
                    });
                    self.log.clear();
                }
                Event::Failed(s, e) => {
                    self.view(s, |v| {
                        v.running = false;
                        v.state = Level::Error;
                    });
                    self.failed = Some((s, e));
                }
                Event::Finished(o) => self.outcome = Some(*o),
            }
        }
        // The worker is gone without a last word (it panicked past its own handler): the
        // screen would wait for it for ever.
        if gone && self.outcome.is_none() && self.failed.is_none() {
            let at = self.steps.iter().find(|v| v.running).map_or(Step::Start, |v| v.step);
            self.view(at, |v| {
                v.running = false;
                v.state = Level::Error;
            });
            self.failed = Some((at, "the installer stopped without saying why; `mikan install` continues from here".into()));
        }
    }

    /// The text field of the page, when it has one.
    fn text_field(&mut self) -> Option<&mut Input> {
        match self.page {
            Page::Address => Some(&mut self.host),
            Page::Domain => Some(&mut self.domain),
            Page::Email => Some(&mut self.email),
            Page::Options if self.focus == 0 => Some(&mut self.port),
            _ => None,
        }
    }

    fn view(&mut self, s: Step, f: impl FnOnce(&mut StepView)) {
        if let Some(v) = self.steps.iter_mut().find(|v| v.step == s) {
            f(v);
        }
    }

    fn sites_next(&mut self) {
        if self.applied.is_some() || matches!(self.scan.done(), Some(Err(_))) {
            self.page = Page::Done;
            return;
        }
        if self.apply.running() || self.scan.running() {
            return;
        }
        if self.pick >= self.rows.len() {
            self.applied = Some("the default site stays".into());
            self.page = Page::Done;
            return;
        }
        let site = self.rows[self.pick].clone();
        let own = self.own.clone();
        self.apply = Task::start(move || sites::apply(&site, own.as_ref()));
    }

    fn scan_finished(&mut self) {
        let Some(Ok(scan)) = self.scan.done() else { return };
        let scan = scan.clone();
        self.rows.clear();
        if let Some(own) = &scan.self_steal {
            self.rows.push(own.clone());
            self.own = Some(own.clone());
            if !own.ok {
                let own = own.clone();
                self.own_wait = Task::start(move || sites::wait_own(&own));
            }
        }
        self.rows.extend(scan.results.iter().cloned());
        let best = sites::best(&scan);
        self.pick = best.and_then(|b| self.rows.iter().position(|r| *r == b)).unwrap_or(self.rows.len());
    }
}

/// A config snippet as it is: indents kept, tabs as four spaces, a line too long for the
/// card cut into pieces rather than cut off.
fn snippet_lines(text: &str, width: usize) -> Vec<Line<'static>> {
    text.lines()
        .flat_map(|l| {
            let chars: Vec<char> = l.replace('\t', "    ").chars().collect();
            if chars.is_empty() {
                return vec![Line::from("")];
            }
            chars.chunks(width.max(8)).map(|c| Line::from(c.iter().collect::<String>())).collect()
        })
        .collect()
}

/// A port the panel can take: not the protocols' ports, free on the server.
fn check_port(p: &str) -> String {
    let Ok(n) = p.parse::<u16>() else { return "a number from 1024 to 65535".into() };
    if n < 1024 {
        return "a number from 1024 to 65535".into();
    }
    if n == 8443 || crate::host::POOL.contains(&n) {
        return format!("{n} is kept for the VPN protocols");
    }
    match system::port_owner(n, Proto::Tcp) {
        Some(who) => format!("{n} is taken ({who})"),
        None => String::new(),
    }
}

impl Screen for Wizard {
    fn tick(&mut self) {
        self.tick = self.tick.wrapping_add(1);
        self.checks.poll();
        if self.detect.poll()
            && let Some(Some(ip)) = self.detect.done()
            && self.host.value.is_empty()
        {
            self.host.set(&ip.to_string());
        }
        self.dns.poll();
        self.take_events();
        if self.scan.poll() {
            self.scan_finished();
        }
        if self.own_wait.poll()
            && let Some(Some(ready)) = self.own_wait.done()
        {
            let ready = ready.clone();
            if let Some(r) = self.rows.first_mut() {
                *r = ready.clone();
            }
            self.own = Some(ready);
            if !self.moved {
                self.pick = 0;
            }
        }
        if self.apply.poll() {
            self.applied = Some(match self.apply.done() {
                Some(Ok(msg)) => msg.clone(),
                Some(Err(e)) => format!("not changed: {e:#}"),
                None => "not changed".into(),
            });
        }
    }

    fn paste(&mut self, text: &str) {
        let Some(field) = self.text_field() else { return };
        field.paste(text);
        if self.page == Page::Domain {
            self.dns = Task::Idle;
            self.domain_err.clear();
        }
    }

    fn key(&mut self, k: KeyEvent) -> bool {
        let ctrl_c = k.code == KeyCode::Char('c') && k.modifiers.contains(KeyModifiers::CONTROL);
        if ctrl_c {
            if self.installing() && !self.quit_armed {
                self.quit_armed = true;
                return false;
            }
            return true;
        }
        self.quit_armed = false;
        if self.finished {
            return true;
        }
        let text_page = matches!(self.page, Page::Address | Page::Domain | Page::Email) || (self.page == Page::Options && self.focus == 0);
        match k.code {
            KeyCode::Enter => {
                self.next();
                return self.finished;
            }
            KeyCode::Esc => {
                if matches!(self.page, Page::Install | Page::Sites | Page::Done) {
                    return self.page == Page::Done || self.failed.is_some();
                }
                self.back();
                return false;
            }
            _ => {}
        }
        if text_page {
            let field = match self.page {
                Page::Address => &mut self.host,
                Page::Domain => &mut self.domain,
                Page::Email => &mut self.email,
                _ => &mut self.port,
            };
            if field.key(k) {
                if self.page == Page::Domain {
                    self.dns = Task::Idle;
                    self.domain_err.clear();
                }
                return false;
            }
        }
        match (self.page, k.code) {
            (Page::Lang, KeyCode::Up | KeyCode::Char('k')) => self.lang = self.lang.saturating_sub(1),
            (Page::Lang, KeyCode::Down | KeyCode::Char('j')) => self.lang = (self.lang + 1).min(LANGS.len() - 1),
            (Page::Lang | Page::Checks | Page::Docker | Page::Proxy | Page::Confirm, KeyCode::Char('q')) => return true,
            (Page::Proxy, KeyCode::Char('n')) => {
                self.plan.proxy_rule = false;
                self.page = Page::Options;
            }
            (Page::Checks, KeyCode::Char('r')) if !self.checks.running() => self.start_checks(),
            (Page::Options, KeyCode::Up | KeyCode::BackTab) => self.focus = self.focus.saturating_sub(1),
            (Page::Options, KeyCode::Down | KeyCode::Tab) => self.focus = (self.focus + 1).min(2),
            (Page::Options, KeyCode::Char(' ')) if self.focus == 1 => self.plan.firewall = !self.plan.firewall,
            (Page::Options, KeyCode::Char(' ')) if self.focus == 2 => self.plan.tune = !self.plan.tune,
            (Page::Install, KeyCode::Char('q')) if self.failed.is_some() => return true,
            (Page::Sites, KeyCode::Up | KeyCode::Char('k')) if !self.apply.running() && self.applied.is_none() => {
                self.pick = self.pick.saturating_sub(1);
                self.moved = true;
            }
            (Page::Sites, KeyCode::Down | KeyCode::Char('j')) if !self.apply.running() && self.applied.is_none() => {
                self.pick = (self.pick + 1).min(self.rows.len());
                self.moved = true;
            }
            (Page::Sites, KeyCode::Char('s')) if !self.apply.running() => {
                self.applied.get_or_insert_with(|| "the default site stays".into());
                self.page = Page::Done;
            }
            (Page::Done, KeyCode::Char('q')) => self.show_qr = !self.show_qr,
            _ => {}
        }
        false
    }

    fn draw(&mut self, f: &mut Frame) {
        match self.page {
            Page::Lang => self.draw_lang(f),
            Page::Checks => self.draw_checks(f),
            Page::Docker => self.draw_docker(f),
            Page::Address => self.draw_address(f),
            Page::Domain => self.draw_domain(f),
            Page::Email => self.draw_email(f),
            Page::Proxy => self.draw_proxy(f),
            Page::Options => self.draw_options(f),
            Page::Confirm => self.draw_confirm(f),
            Page::Install => self.draw_install(f),
            Page::Sites => self.draw_sites(f),
            Page::Done => self.draw_done(f),
        }
    }
}

impl Wizard {
    fn card<'a>(&self, title: &'a str, lead: &'a str, keys: &'a [(&'a str, &'a str)]) -> Card<'a> {
        let c = Card::new(title, lead, keys);
        match self.numbered() {
            Some((i, n)) => c.step(i, n),
            None => c,
        }
    }

    /// Draws a card fitted to lines, with the lines in it.
    fn page(f: &mut Frame, card: Card, lines: Vec<Line<'static>>) {
        let Some(body) = card.fit(lines.len()).draw(f) else { return };
        f.render_widget(Paragraph::new(lines), body);
    }

    fn draw_lang(&self, f: &mut Frame) {
        let keys = [("↑↓", "choose"), ("enter", "next"), ("q", "quit")];
        let lead = "The admin panel, subscription pages and the bot open in it until someone picks another one. You can change it later in the panel's settings.";
        let w = Card::body_width(f.area());
        let lines = LANGS.iter().enumerate().map(|(i, (_, name, hint))| item(name, hint, i == self.lang, w)).collect();
        Self::page(f, self.card("Panel language", lead, &keys), lines);
    }

    fn draw_checks(&self, f: &mut Frame) {
        let failed = self.checks.done().is_some_and(|c| c.iter().any(|c| c.level == Level::Error));
        // A node's install and a language given as a flag start here: nothing to go back to.
        let first = self.node() || self.plan_lang_given;
        let keys: &[(&str, &str)] = match (failed, first) {
            (true, true) => &[("enter", "check again"), ("q", "quit")],
            (true, false) => &[("enter", "check again"), ("esc", "back"), ("q", "quit")],
            (false, true) => &[("enter", "next"), ("r", "check again"), ("q", "quit")],
            (false, false) => &[("enter", "next"), ("r", "check again"), ("esc", "back"), ("q", "quit")],
        };
        let lead = if self.node() { "A node of another panel will run here." } else { "What the server needs before mikan goes on it." };
        let mut lines = Vec::new();
        match self.checks.done() {
            None => lines.push(Line::from(vec![spinner(self.tick), Span::raw(" Checking the server…")])),
            Some(checks) => {
                let w = Card::body_width(f.area());
                for c in checks {
                    let color = if c.level == Level::Ok { DIM } else { level_color(c.level) };
                    for (i, d) in wrap(&c.detail, w.saturating_sub(19)).into_iter().enumerate() {
                        let head = if i == 0 {
                            vec![level_span(c.level), Span::raw(format!(" {:<17}", c.label))]
                        } else {
                            vec![Span::raw(" ".repeat(19))]
                        };
                        lines.push(Line::from([head, vec![Span::styled(d, Style::new().fg(color))]].concat()));
                    }
                }
                if failed {
                    lines.push(Line::from(""));
                    lines.push(Line::from(Span::styled("Fix what is marked ✗, then check again.", Style::new().fg(ERR))));
                }
            }
        }
        Self::page(f, self.card("Server check", lead, keys), lines);
    }

    /// Replacing Docker removes packages: asked, never assumed.
    fn draw_docker(&self, f: &mut Frame) {
        let keys = [("enter", "replace"), ("esc", "back"), ("q", "quit")];
        let lead = "mikan runs with Docker compose v2, and the Docker on this server has none: it is the one from the system's packages.";
        let w = Card::body_width(f.area());
        let found =
            self.checks.done().and_then(|c| system::docker_to_replace(c)).map(|c| c.detail.split(' ').next().unwrap_or("").to_owned());
        let mut lines = field("Found", format!("Docker {}", found.unwrap_or_default()), w);
        lines.push(Line::from(""));
        for text in [
            "The installer removes it (docker.io, containerd, runc and the old compose) and installs the current Docker from get.docker.com.",
            "Images, volumes and containers stay in /var/lib/docker and run again on the new Docker; running containers restart once.",
            "Say no (esc) to keep it: then install Docker with compose v2 yourself and run the installer again.",
        ] {
            lines.extend(wrap(text, w).into_iter().map(|l| Line::from(dim(l))));
            lines.push(Line::from(""));
        }
        lines.pop();
        Self::page(f, self.card("Replace Docker?", lead, &keys), lines);
    }

    fn draw_address(&self, f: &mut Frame) {
        let keys = [("enter", "next"), ("esc", "back")];
        let lead = "Clients connect here when there is no domain. It was detected; change it only when the server sits behind NAT.";
        let Some(body) = self.card("Server address", lead, &keys).fit(4).draw(f) else { return };
        let [input, note] = Layout::vertical([Constraint::Length(3), Constraint::Min(0)]).areas(body);
        let color = (!self.host_err.is_empty()).then_some(ERR);
        self.host.draw(f, Rect { width: input.width.min(40), ..input }, "203.0.113.10", true, color);
        let msg = if !self.host_err.is_empty() {
            Line::from(Span::styled(self.host_err.clone(), Style::new().fg(ERR)))
        } else if self.detect.running() {
            Line::from(vec![spinner(self.tick), dim(" detecting the public address…")])
        } else {
            Line::from(dim("IPv4 only: the protocols' links carry it."))
        };
        f.render_widget(Paragraph::new(msg), note);
    }

    fn draw_domain(&self, f: &mut Frame) {
        let keys = [("enter", "check · next"), ("esc", "back")];
        let lead = "A domain gets the panel a Let's Encrypt certificate and TLS protocols a real name. Its A record must point to this server. Leave it empty to go with the IP.";
        let w = Card::body_width(f.area());
        let checked = self.dns.done().filter(|_| self.dns_for == self.domain.text().to_lowercase());
        let mut notes = Vec::new();
        if !self.domain_err.is_empty() {
            notes.push(Line::from(Span::styled(self.domain_err.clone(), Style::new().fg(ERR))));
        } else if self.dns.running() {
            notes.push(Line::from(vec![spinner(self.tick), dim(" asking Cloudflare and Google DNS…")]));
        } else if let Some(c) = checked {
            for n in &c.notes {
                for (i, l) in wrap(&n.text, w.saturating_sub(2)).into_iter().enumerate() {
                    let head = if i == 0 { level_span(n.level) } else { Span::raw(" ") };
                    notes.push(Line::from(vec![head, Span::raw(" "), Span::raw(l)]));
                }
            }
            notes.push(Line::from(""));
            let hint = if c.level() == Level::Error {
                "Fix the record and press Enter to check again, or clear the field to go without a domain."
            } else {
                "Press Enter to go on."
            };
            notes.extend(wrap(hint, w).into_iter().map(|l| Line::from(dim(l))));
        } else if self.domain.value.is_empty() {
            let hint = "Without a domain the panel has a self-signed certificate: the browser warns once.";
            notes.extend(wrap(hint, w).into_iter().map(|l| Line::from(dim(l))));
        }
        let Some(body) = self.card("Domain", lead, &keys).fit(4 + notes.len()).draw(f) else { return };
        let [input, _, rest] = Layout::vertical([Constraint::Length(3), Constraint::Length(1), Constraint::Min(0)]).areas(body);
        let color = if self.domain_err.is_empty() { checked.map(|c| level_color(c.level())) } else { Some(ERR) };
        self.domain.draw(f, Rect { width: input.width.min(48), ..input }, "vpn.example.com  (optional)", true, color);
        f.render_widget(Paragraph::new(notes), rest);
    }

    fn draw_email(&self, f: &mut Frame) {
        let keys = [("enter", "next"), ("esc", "back")];
        let lead = "Let's Encrypt writes here if the certificate cannot renew. Optional.";
        let Some(body) = self.card("Email for Let's Encrypt", lead, &keys).fit(4).draw(f) else { return };
        let [input, note] = Layout::vertical([Constraint::Length(3), Constraint::Min(0)]).areas(body);
        let color = (!self.email_err.is_empty()).then_some(ERR);
        self.email.draw(f, Rect { width: input.width.min(48), ..input }, "you@example.com  (optional)", true, color);
        if !self.email_err.is_empty() {
            f.render_widget(Paragraph::new(Span::styled(self.email_err.clone(), Style::new().fg(ERR))), note);
        }
    }

    /// A web server's config is changed only with the admin's yes.
    fn draw_proxy(&self, f: &mut Frame) {
        let Some(front) = self.front else { return };
        let keys = [("enter", "add the rule"), ("n", "no, I add it myself"), ("esc", "back"), ("q", "quit")];
        let w = Card::body_width(f.area());
        let (name, domain) = (front.name(), &self.plan.domain);
        let title = format!("Let's Encrypt through {name}");
        let lead = format!(
            "Let's Encrypt checks {domain} on port 80, and {name} holds that port. mikan can add a rule to {name} that passes only those checks to the panel."
        );
        let texts: &[&str] = match front {
            acme::Front::Nginx => &[
                "It is a new file in nginx's config: a server for this domain on port 80. nginx -t tests it before a reload; if the test fails, the file is taken out again.",
                "If nginx has a server for this domain already, mikan leaves it as it is and shows the one line to add to it.",
            ],
            acme::Front::Caddy => &[
                "It is an http:// site for this domain at the end of the Caddyfile, which is backed up first. caddy validate checks it before a reload; if the check fails, the old Caddyfile goes back.",
            ],
        };
        let mut lines = Vec::new();
        for text in texts.iter().copied().chain([
            "Say no to add it yourself: the last screen shows what to add. Until then the panel works with a self-signed certificate.",
        ]) {
            lines.extend(wrap(text, w).into_iter().map(|l| Line::from(dim(l))));
            lines.push(Line::from(""));
        }
        lines.pop();
        Self::page(f, self.card(&title, &lead, &keys), lines);
    }

    fn draw_options(&self, f: &mut Frame) {
        let keys = [("↑↓", "move"), ("space", "switch"), ("enter", "next"), ("esc", "back")];
        let w = Card::body_width(f.area());
        let mut rest = Vec::new();
        if self.ufw {
            rest.push(checkbox("Open the ports in ufw", self.plan.firewall, self.focus == 1));
        } else {
            let text = "ufw is off: if the hoster has a firewall, open 443, 8443 and the panel port there.";
            for (i, l) in wrap(text, w.saturating_sub(2)).into_iter().enumerate() {
                let marker = if i == 0 && self.focus == 1 { "› " } else { "  " };
                rest.push(Line::from(vec![Span::styled(marker, Style::new().fg(ACCENT)), dim(l)]));
            }
        }
        rest.push(Line::from(""));
        rest.push(checkbox("BBR and bigger UDP buffers", self.plan.tune, self.focus == 2));
        rest.push(Line::from(dim("      faster TCP on long routes, steadier Hysteria2 and TUIC")));
        let Some(body) = self.card("Options", "The defaults suit most servers.", &keys).fit(6 + rest.len()).draw(f) else { return };
        let [label, input, err, _, tail] = Layout::vertical([
            Constraint::Length(1),
            Constraint::Length(3),
            Constraint::Length(1),
            Constraint::Length(1),
            Constraint::Min(0),
        ])
        .areas(body);
        let focus_port = self.focus == 0;
        let marker = if focus_port { Span::styled("› ", Style::new().fg(ACCENT).bold()) } else { Span::raw("  ") };
        f.render_widget(
            Paragraph::new(Line::from(vec![marker, "Panel port".bold(), dim("   the panel and its subscription links")])),
            label,
        );
        let color = (!self.port_err.is_empty()).then_some(ERR);
        self.port.draw(f, Rect { x: input.x + 2, width: 14, ..input }, "34567", focus_port, color);
        if !self.port_err.is_empty() {
            f.render_widget(Paragraph::new(Span::styled(format!("  {}", self.port_err), Style::new().fg(ERR))), err);
        }
        f.render_widget(Paragraph::new(rest), tail);
    }

    fn draw_confirm(&self, f: &mut Frame) {
        let keys = [("enter", "install"), ("esc", "back"), ("q", "quit")];
        let p = &self.plan;
        let w = Card::body_width(f.area());
        let mut lines = Vec::new();
        if let Some(key) = &p.join {
            let tail: String = key.chars().rev().take(6).collect::<Vec<_>>().into_iter().rev().collect();
            lines.extend(field("Mode", format!("a node of another panel (key …{tail})"), w));
        } else {
            lines.extend(field("Language", LANGS[self.lang].1, w));
            lines.extend(field("Address", p.host.clone(), w));
            lines.extend(field("Domain", if p.domain.is_empty() { "none: self-signed certificate".into() } else { p.domain.clone() }, w));
            if !p.domain.is_empty() {
                lines.extend(field("Email", if p.email.is_empty() { "none".into() } else { p.email.clone() }, w));
            }
            if let Some(front) = self.front {
                let rule = if p.proxy_rule {
                    "mikan adds a rule for Let's Encrypt, tested before a reload"
                } else {
                    "you add the rule for Let's Encrypt (the last screen shows it)"
                };
                lines.extend(field("Port 80", format!("{}: {rule}", front.name()), w));
            }
            lines.extend(field("Panel port", p.port.to_string(), w));
        }
        let fw = match (self.ufw, p.firewall) {
            (false, _) => "ufw is off",
            (true, true) => "open the ports in ufw",
            (true, false) => "leave ufw alone",
        };
        lines.extend(field("Firewall", fw, w));
        lines.extend(field("Tuning", if p.tune { "BBR and bigger UDP buffers" } else { "leave the kernel alone" }, w));
        let image = match (&p.image, &p.image_tar) {
            (Some(i), _) => i.clone(),
            (_, Some(t)) => format!("from {t}"),
            _ => "the latest signed release from GitHub".into(),
        };
        lines.extend(field("Image", image, w));
        if p.replace_docker {
            lines.extend(field("Docker", "replace the system's one from get.docker.com", w));
        }
        lines.push(Line::from(""));
        lines.push(Line::from(dim("Docker is installed if missing; mikan goes to /opt/mikan.")));
        Self::page(f, self.card("Ready to install", "Nothing has changed on the server yet.", &keys), lines);
    }

    fn draw_install(&self, f: &mut Frame) {
        let keys: &[(&str, &str)] = if self.failed.is_some() {
            &[("q", "quit")]
        } else if self.outcome.is_some() {
            &[("enter", "next")]
        } else if self.quit_armed {
            &[("ctrl+c", "again to quit: the install stops here; `mikan install` goes on later")]
        } else {
            &[]
        };
        let title = if self.outcome.is_some() {
            "Installed"
        } else if self.failed.is_some() {
            "The install stopped"
        } else {
            "Installing"
        };
        let w = Card::body_width(f.area());
        let mut lines = Vec::new();
        let mut gauge = None;
        for v in &self.steps {
            let (icon, label_style) = if v.state == Level::Error {
                (Span::styled("✗", Style::new().fg(ERR).bold()), Style::new().fg(ERR))
            } else if v.running {
                (spinner(self.tick), Style::new().bold())
            } else if v.note.is_empty() {
                (Span::styled("·", Style::new().fg(FAINT)), Style::new().fg(DIM))
            } else {
                (Span::styled("✓", Style::new().fg(OK)), Style::new())
            };
            let note = wrap(&v.note, w.saturating_sub(32));
            for (i, n) in note.into_iter().enumerate() {
                if i == 0 {
                    lines.push(Line::from(vec![
                        icon.clone(),
                        Span::raw(" "),
                        Span::styled(format!("{:<30}", v.step.label()), label_style),
                        dim(n),
                    ]));
                } else {
                    lines.push(Line::from(vec![Span::raw(" ".repeat(32)), dim(n)]));
                }
            }
            if let (true, Some(p)) = (v.running, v.progress) {
                gauge = Some((lines.len(), p));
                lines.push(Line::from(""));
            }
        }
        for l in &self.log {
            let short: String = l.chars().take(w.saturating_sub(4)).collect();
            lines.push(Line::from(vec![Span::raw("    "), Span::styled(short, Style::new().fg(FAINT))]));
        }
        if let Some((_, e)) = &self.failed {
            lines.push(Line::from(""));
            for l in wrap(e, w).into_iter().take(12) {
                lines.push(Line::from(Span::styled(l, Style::new().fg(ERR))));
            }
            lines.push(Line::from(""));
            let hint = "Fix the cause and run the installer again. What got as far as /opt/mikan is kept: mikan status, mikan logs.";
            lines.extend(wrap(hint, w).into_iter().map(|l| Line::from(dim(l))));
        }
        let Some(body) = Card::new(title, "", keys).fit(lines.len()).draw(f) else { return };
        f.render_widget(Paragraph::new(lines), body);
        if let Some((row, p)) = gauge {
            let area = Rect { x: body.x + 2, y: body.y + row as u16, width: body.width.saturating_sub(2).min(50), height: 1 };
            let g =
                LineGauge::default().filled_style(Style::new().fg(ACCENT)).unfilled_style(Style::new().fg(FAINT)).ratio(p.clamp(0.0, 1.0));
            f.render_widget(g, area);
        }
    }

    fn draw_sites(&self, f: &mut Frame) {
        let keys: &[(&str, &str)] = if self.applied.is_some() {
            &[("enter", "next")]
        } else if self.scan.running() || self.apply.running() {
            &[]
        } else {
            &[("↑↓", "choose"), ("enter", "apply"), ("s", "skip")]
        };
        let lead = "Probes of the VPN ports see this site's TLS. A site next to your server, or your own domain, looks the most natural.";
        let w = Card::body_width(f.area());
        let mut lines = Vec::new();
        match self.scan.done() {
            None => {
                lines.push(Line::from(vec![spinner(self.tick), Span::raw(" Looking for sites next to the server: up to a minute…")]));
            }
            Some(Err(e)) => {
                for l in wrap(&format!("The scan failed: {e:#}"), w) {
                    lines.push(Line::from(Span::styled(l, Style::new().fg(ERR))));
                }
                lines.push(Line::from(dim("The default site stays; pick another later: mikan → REALITY sites.")));
            }
            Some(Ok(scan)) => {
                lines.push(Line::from(dim(format!("{} addresses around {} checked.", scan.scanned, scan.ip))));
                lines.push(Line::from(""));
                for (i, s) in self.rows.iter().enumerate() {
                    let sel = i == self.pick;
                    let own = self.own.as_ref().is_some_and(|o| o.dest == s.dest && i == 0);
                    let marker = if sel { Span::styled("› ", Style::new().fg(ACCENT).bold()) } else { Span::raw("  ") };
                    let name = Span::styled(format!("{:<34}", s.sni), if sel { Style::new().bold() } else { Style::new() });
                    let detail = if own {
                        if s.ok {
                            Span::styled("your domain, certificate ready", Style::new().fg(OK))
                        } else if self.own_wait.running() {
                            Span::styled(
                                format!("your domain, waiting for its certificate {}", spinner(self.tick).content),
                                Style::new().fg(DIM),
                            )
                        } else {
                            Span::styled("your domain, no certificate yet", Style::new().fg(DIM))
                        }
                    } else {
                        dim(format!("{:<22} {:>4} ms", s.dest, s.rtt_ms))
                    };
                    lines.push(Line::from(vec![marker, name, detail]));
                }
                let keep = scan.current.first().map(|c| c.sni.clone()).unwrap_or_else(|| "the default".into());
                let sel = self.pick >= self.rows.len();
                let marker = if sel { Span::styled("› ", Style::new().fg(ACCENT).bold()) } else { Span::raw("  ") };
                lines.push(Line::from(vec![
                    marker,
                    Span::styled(format!("Keep {keep}"), if sel { Style::new().bold() } else { Style::new() }),
                ]));
                if self.rows.is_empty() {
                    lines.push(Line::from(""));
                    let hint = "No site next to the server passed the checks: TLS 1.3, HTTP/2, X25519 and its own certificate.";
                    lines.extend(wrap(hint, w).into_iter().map(|l| Line::from(dim(l))));
                }
            }
        }
        if self.apply.running() {
            lines.push(Line::from(""));
            lines.push(Line::from(vec![spinner(self.tick), Span::raw(" Checking the site from the node and applying…")]));
        }
        if let Some(a) = &self.applied {
            lines.push(Line::from(""));
            for l in a.lines().flat_map(|l| wrap(l, w)) {
                lines.push(Line::from(dim(l)));
            }
        }
        Self::page(f, Card::new("REALITY camouflage", lead, keys), lines);
    }

    fn draw_done(&self, f: &mut Frame) {
        let Some(o) = &self.outcome else { return };
        let w = Card::body_width(f.area());
        if o.node_port.is_some() {
            let keys = [("enter", "finish")];
            let mut lines = vec![
                Line::from(vec![Span::styled("✓ ", Style::new().fg(OK)), Span::raw(format!("mikan {} node", o.version))]),
                Line::from(""),
            ];
            lines.extend(field("Waits on", format!("port {}", o.node_port.unwrap_or_default()), w));
            lines.push(Line::from(""));
            match &o.hello {
                // What the panel found when it connected, with what to do about it.
                Some(report) => {
                    for (level, text) in report {
                        for (i, part) in wrap(text, w.saturating_sub(2)).into_iter().enumerate() {
                            let lead = if i == 0 { level_span(*level) } else { Span::raw(" ") };
                            lines.push(Line::from(vec![lead, Span::raw(format!(" {part}"))]));
                        }
                    }
                }
                None => lines.extend(
                    wrap("Its panel connects within 30 seconds: see the panel's Nodes page.", w).into_iter().map(|l| Line::from(dim(l))),
                ),
            }
            lines.push(Line::from(""));
            lines.extend(
                wrap("On this server: mikan (menu), mikan status, mikan doctor, mikan update.", w).into_iter().map(|l| Line::from(dim(l))),
            );
            Self::page(f, Card::new("The node is running", "", &keys), lines);
            return;
        }
        let keys = [("q", if self.show_qr { "hide QR" } else { "QR code" }), ("enter", "finish")];
        let lead = "The password is shown here only, not after you finish: keep it in a password manager.";
        if self.show_qr {
            let code = qr(&o.url);
            let w = code.first().map_or(0, |l| l.spans.len()) as u16;
            let Some(body) = Card::new("mikan is running", lead, &keys).fit(code.len()).draw(f) else { return };
            if code.len() as u16 <= body.height && w <= body.width {
                let area = Rect { x: body.x + (body.width - w) / 2, y: body.y, width: w, height: code.len() as u16 };
                f.render_widget(Paragraph::new(code), area);
            } else {
                f.render_widget(Paragraph::new(dim("The terminal is too small for the QR code.")), body);
            }
            return;
        }
        let mut lines = field("Panel", o.url.clone(), w);
        lines.extend(field("Login", o.login.clone(), w));
        lines.extend(field_styled("Password", o.password.clone(), Style::new().fg(ACCENT).bold(), w));
        lines.push(Line::from(""));
        if let Some(a) = &self.applied {
            let first = a.lines().find(|l| l.starts_with("Inbound")).unwrap_or_else(|| a.lines().next().unwrap_or(""));
            lines.extend(field("REALITY", first.to_owned(), w));
        }
        let domain = &self.plan.domain;
        let cert = match &o.acme {
            _ if domain.is_empty() => "self-signed: the browser warns once, then the panel works".to_string(),
            None => format!("Let's Encrypt for {domain} within a minute"),
            Some(r) if r.added.is_some() => format!("Let's Encrypt for {domain} within a minute, through {}", r.front),
            Some(r) if r.manual.as_ref().is_some_and(|m| m.snippet.is_empty()) => {
                format!("self-signed until port 80 is free ({}); the panel tries again by itself", r.front)
            }
            Some(r) => format!("self-signed until {} passes Let's Encrypt to the panel; it tries again by itself", r.front),
        };
        lines.extend(field("Certificate", cert, w));
        if let Some((r, m)) = o.acme.as_ref().and_then(|r| r.manual.as_ref().map(|m| (r, m))).filter(|(_, m)| !m.snippet.is_empty()) {
            lines.push(Line::from(""));
            let how = format!("Add this {}, then reload {} (it is printed again when you finish):", m.place, r.front);
            lines.extend(wrap(&how, w).into_iter().map(|l| Line::from(dim(l))));
            lines.extend(snippet_lines(&m.snippet, w));
        }
        lines.push(Line::from(""));
        let head = vec![dim("On this server: "), "mikan".bold(), dim(" for this menu, ")];
        let tail = vec!["mikan update".bold(), dim(", "), "mikan status".bold()];
        let one: usize = head.iter().chain(&tail).map(|s| s.width()).sum();
        if one <= w {
            lines.push(Line::from([head, tail].concat()));
        } else {
            lines.push(Line::from(head));
            lines.push(Line::from(tail));
        }
        Self::page(f, Card::new("mikan is running", lead, &keys), lines);
    }
}
