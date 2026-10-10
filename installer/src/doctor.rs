//! `mikan doctor`: checks the server of a panel or a node and says what is wrong and what
//! to do about it. The checks that wait for the network (DNS, GitHub, GHCR) run beside the
//! local ones, so that the whole run stays short.

use std::io::Read;
use std::net::ToSocketAddrs;
use std::path::Path;
use std::process::{Command, Stdio};
use std::sync::mpsc;
use std::thread;
use std::time::{Duration, Instant};

use anyhow::{Result, bail};
use serde::Deserialize;

use crate::docker::{self, Service};
use crate::ops::Install;
use crate::setup::mark;
use crate::system::{self, Level, Proto};
use crate::{DIR, clock, hello, net};

/// How long one request to the outside may take.
const NET_TIMEOUT: Duration = Duration::from_secs(8);

/// All the network answers are in by then, or they count as missing.
const NET_DEADLINE: Duration = Duration::from_secs(12);

/// A server clock this far from GitHub's is wrong: TLS and REALITY fail.
const SKEW_LIMIT: i64 = 30;

const MIB: u64 = 1024 * 1024;

/// One line of the report.
#[derive(Debug, Clone, PartialEq)]
struct Item {
    level: Level,
    label: String,
    detail: String,
    /// What to do; printed for anything that is not fine.
    fix: Option<String>,
}

impl Item {
    fn ok(label: impl Into<String>, detail: impl Into<String>) -> Self {
        Self { level: Level::Ok, label: label.into(), detail: detail.into(), fix: None }
    }

    fn warn(label: impl Into<String>, detail: impl Into<String>, fix: impl Into<String>) -> Self {
        Self { level: Level::Warn, label: label.into(), detail: detail.into(), fix: Some(fix.into()) }
    }

    fn error(label: impl Into<String>, detail: impl Into<String>, fix: impl Into<String>) -> Self {
        Self { level: Level::Error, label: label.into(), detail: detail.into(), fix: Some(fix.into()) }
    }

    /// The text as printed: the line, and under a problem the fix.
    fn lines(&self) -> Vec<String> {
        let mut out = vec![format!("{} {:<16}  {}", mark(self.level), self.label, self.detail).trim_end().to_owned()];
        if let (Some(fix), true) = (&self.fix, self.level != Level::Ok) {
            out.push(format!("    fix: {fix}"));
        }
        out
    }
}

/// Prints the items as they come and counts the problems.
#[derive(Default)]
struct Report {
    errors: usize,
    warnings: usize,
}

impl Report {
    fn add(&mut self, items: impl IntoIterator<Item = Item>) {
        for item in items {
            match item.level {
                Level::Error => self.errors += 1,
                Level::Warn => self.warnings += 1,
                Level::Ok => {}
            }
            for line in item.lines() {
                crate::out(&line);
            }
        }
    }
}

fn background<T: Send + 'static>(f: impl FnOnce() -> T + Send + 'static) -> mpsc::Receiver<T> {
    let (tx, rx) = mpsc::channel();
    thread::spawn(move || {
        let _ = tx.send(f());
    });
    rx
}

/// The answer of a background check; None when it is not in by the deadline.
fn answer<T>(rx: &mpsc::Receiver<T>, deadline: Instant) -> Option<T> {
    rx.recv_timeout(deadline.saturating_duration_since(Instant::now())).ok()
}

type Reached = Result<net::Probe, String>;

pub fn run() -> Result<()> {
    let install = Install::load()?;
    let deadline = Instant::now() + NET_DEADLINE;
    let github = background(|| net::probe("https://github.com", NET_TIMEOUT).map_err(|e| e.to_string()));
    let ghcr = background(|| net::probe("https://ghcr.io/v2/", NET_TIMEOUT).map_err(|e| e.to_string()));
    let dns = background(|| "github.com:443".to_socket_addrs().map(|a| a.count()).map_err(|e| e.to_string()));

    let mut report = Report::default();
    crate::out(&format!("mikan {}, {}", install.version(), if install.node { "node" } else { "panel" }));

    let engine = docker::version();
    report.add([docker_item(engine.as_deref())]);
    if engine.is_some() {
        report.add(containers(docker::services().map_err(|e| format!("{e:#}"))));
    }
    report.add(ports(&install, engine.is_some()));
    report.add(firewall(&install));

    let github = answer(&github, deadline);
    let skew = github
        .as_ref()
        .and_then(|r| r.as_ref().ok())
        .and_then(|p| p.date.as_deref())
        .and_then(|d| skew_secs(d, clock::unix(std::time::SystemTime::now())));
    report.add([clock_item(system::clock_synced(), skew)]);
    report.add([dns_item(answer(&dns, deadline))]);
    report.add([
        outbound_item("GitHub", "github.com", "updates cannot be downloaded", github),
        outbound_item("GHCR", "ghcr.io", "images cannot be pulled", answer(&ghcr, deadline)),
    ]);

    report.add([disk_item(system::disk_free_bytes(Path::new(DIR))), memory_item(system::mem_available_mb())]);
    if install.node {
        report.add(contact(install.node_port(), engine.is_some()));
    }

    match (report.errors, report.warnings) {
        (0, 0) => crate::out("No problems found."),
        (0, w) => crate::out(&format!("No errors; {w} thing{} to look at above.", if w == 1 { "" } else { "s" })),
        (e, _) => bail!("{e} problem{} found", if e == 1 { "" } else { "s" }),
    }
    Ok(())
}

fn docker_item(version: Option<&str>) -> Item {
    match version {
        Some(v) => Item::ok("Docker", format!("engine {v}")),
        None => Item::error("Docker", "the daemon does not answer", "systemctl start docker; journalctl -u docker -n 50"),
    }
}

fn containers(services: Result<Vec<Service>, String>) -> Vec<Item> {
    match services {
        Err(e) => vec![Item::error("Containers", e, "mikan restart")],
        Ok(list) if list.is_empty() => vec![Item::error("Containers", "none exist", "mikan restart")],
        Ok(list) => list.iter().map(container).collect(),
    }
}

fn container(s: &Service) -> Item {
    let label = format!("Container {}", s.name);
    let fix = format!("mikan restart; mikan logs {}", s.name);
    let status = if s.status.is_empty() { s.state.clone() } else { s.status.clone() };
    if s.state != "running" || s.status.contains("unhealthy") {
        Item::error(label, status, fix)
    } else if s.status.contains("starting") {
        Item::warn(label, status, "wait a minute and run mikan doctor again; mikan logs ".to_owned() + &s.name)
    } else {
        Item::ok(label, status)
    }
}

/// The port the server answers on: a node's API port, the panel's own.
fn ports(install: &Install, engine: bool) -> Vec<Item> {
    if install.node {
        return vec![match install.node_port() {
            Some(p) => match system::port_owner(p, Proto::Tcp) {
                Some(who) => Item::ok("API port", format!("{p}/tcp is listening ({who})")),
                None => Item::error(
                    "API port",
                    format!("nothing listens on {p}/tcp, where the panel connects"),
                    "mikan logs node; mikan restart",
                ),
            },
            None => {
                Item::error("API port", "NODE_API_PORT is not set in /opt/mikan/.env", "mikan join <new key from the panel's Nodes page>")
            }
        }];
    }
    let port: Option<u16> = install.env.get("PANEL_PORT").and_then(|p| p.parse().ok());
    if !engine {
        return Vec::new();
    }
    let at = port.map_or_else(String::new, |p| format!(" on port {p}"));
    vec![if docker::panel_healthy() {
        Item::ok("Panel", format!("answers{at}"))
    } else {
        let listening = port.and_then(|p| system::port_owner(p, Proto::Tcp));
        let detail = match (port, listening) {
            (Some(p), None) => format!("does not answer, nothing listens on {p}/tcp"),
            (Some(p), Some(who)) => format!("does not answer on {p}/tcp ({who} listens there)"),
            (None, _) => "does not answer".to_owned(),
        };
        Item::error("Panel", detail, "mikan logs panel; mikan restart")
    }]
}

/// A port or a range the firewall must let through.
#[derive(Debug, Clone, PartialEq)]
struct Want {
    what: String,
    lo: u16,
    hi: u16,
    /// None: the protocol is not known, any rule for the port will do.
    proto: Option<Proto>,
    /// The rule must be for everyone; a rule for the panel's address alone is enough when not.
    anyone: bool,
    /// How bad it is when missing.
    level: Level,
}

impl Want {
    /// The rule as ufw writes it: 443, 443/tcp, 20000:20100/udp.
    fn rule(&self) -> String {
        let ports = if self.lo == self.hi { self.lo.to_string() } else { format!("{}:{}", self.lo, self.hi) };
        match self.proto {
            Some(p) => format!("{ports}/{}", p.name()),
            None => ports,
        }
    }

    /// The commands that open it; ufw wants a protocol for a range.
    fn commands(&self) -> Vec<String> {
        if self.proto.is_none() && self.lo != self.hi {
            let ports = format!("{}:{}", self.lo, self.hi);
            return vec![format!("ufw allow {ports}/tcp"), format!("ufw allow {ports}/udp")];
        }
        vec![format!("ufw allow {}", self.rule())]
    }
}

/// An allow rule of `ufw status`.
#[derive(Debug, Clone, PartialEq)]
struct Rule {
    lo: u16,
    hi: u16,
    /// None: both.
    proto: Option<Proto>,
    /// From anywhere, not from one address.
    anywhere: bool,
}

/// The allow rules in the text of `ufw status`. Deny rules, rules for outgoing traffic and
/// application profiles are left out.
fn parse_ufw(text: &str) -> Vec<Rule> {
    let mut rules = Vec::new();
    for line in text.lines() {
        let words: Vec<&str> = line.split_whitespace().collect();
        let Some(at) = words.iter().position(|w| matches!(*w, "ALLOW" | "LIMIT" | "DENY" | "REJECT")) else { continue };
        if at == 0 || matches!(words[at], "DENY" | "REJECT") {
            continue;
        }
        let mut from = &words[at + 1..];
        match from.first() {
            Some(&"OUT") => continue,
            Some(&("IN" | "FWD")) => from = &from[1..],
            _ => {}
        }
        let anywhere = from.first().is_some_and(|f| f.eq_ignore_ascii_case("anywhere"));
        for (lo, hi, proto) in parse_to(words[0]) {
            rules.push(Rule { lo, hi, proto, anywhere });
        }
    }
    rules
}

/// The "To" column: 443, 443/tcp, 20000:20100/udp, 80,443/tcp, or Anywhere for every port.
fn parse_to(spec: &str) -> Vec<(u16, u16, Option<Proto>)> {
    if spec.eq_ignore_ascii_case("anywhere") {
        return vec![(1, u16::MAX, None)];
    }
    let (ports, proto) = match spec.split_once('/') {
        Some((p, "tcp")) => (p, Some(Proto::Tcp)),
        Some((p, "udp")) => (p, Some(Proto::Udp)),
        Some(_) => return Vec::new(),
        None => (spec, None),
    };
    ports
        .split(',')
        .filter_map(|part| match part.split_once(':') {
            Some((a, b)) => Some((a.parse().ok()?, b.parse().ok()?, proto)),
            None => part.parse().ok().map(|p| (p, p, proto)),
        })
        .filter(|(lo, hi, _)| lo <= hi && *lo > 0)
        .collect()
}

/// Whether every port of the wish is let through by some rule.
fn allowed(rules: &[Rule], w: &Want) -> bool {
    (w.lo..=w.hi).all(|port| {
        rules.iter().any(|r| {
            r.lo <= port
                && port <= r.hi
                && (r.anywhere || !w.anyone)
                && match (w.proto, r.proto) {
                    (Some(a), Some(b)) => a == b,
                    _ => true,
                }
        })
    })
}

/// The part of node-state.json the firewall check reads: the ports of the inbounds and of
/// the relay.
#[derive(Deserialize, Default, Debug)]
struct NodeState {
    #[serde(default)]
    inbounds: Vec<StateInbound>,
    relay: Option<StateRelay>,
}

#[derive(Deserialize, Default, Debug)]
struct StateInbound {
    #[serde(default)]
    name: String,
    listen: Option<String>,
    #[serde(default)]
    port: String,
    config: Option<serde_json::Value>,
    preset: Option<String>,
}

#[derive(Deserialize, Default, Debug)]
struct StateRelay {
    #[serde(default)]
    port: String,
}

impl StateInbound {
    /// UDP for the QUIC protocols, TCP for the ones known to run over it; None when the file
    /// does not say (then any rule for the port will do).
    fn proto(&self) -> Option<Proto> {
        let kind = self.config.as_ref().and_then(|c| c.get("type")).and_then(|t| t.as_str()).or(self.preset.as_deref());
        match kind? {
            "hysteria2" | "tuic" | "tuic_v5" | "shadowquic" => Some(Proto::Udp),
            "vless" | "vmess" | "trojan" | "anytls" | "trusttunnel" | "vless_reality_vision" | "vless_reality_xhttp" => Some(Proto::Tcp),
            _ => None,
        }
    }

    fn local(&self) -> bool {
        self.listen.as_deref().is_some_and(|l| l.starts_with("127.") || l == "::1" || l == "localhost")
    }
}

/// "443" or "20000-20100" (or with a colon).
fn parse_ports(s: &str) -> Option<(u16, u16)> {
    let (lo, hi) = match s.trim().split_once(['-', ':']) {
        Some((a, b)) => (a.trim().parse().ok()?, b.trim().parse().ok()?),
        None => {
            let p = s.trim().parse().ok()?;
            (p, p)
        }
    };
    (lo > 0 && lo <= hi).then_some((lo, hi))
}

/// What the firewall must let through: the port the panel or its panel connects to, and the
/// ports of the inbounds the node listens on.
fn wants(api: Option<u16>, panel: Option<u16>, state: Option<&NodeState>) -> Vec<Want> {
    let mut out: Vec<Want> = Vec::new();
    let mut add = |what: String, (lo, hi): (u16, u16), proto: Option<Proto>, anyone: bool, level: Level| {
        let w = Want { what, lo, hi, proto, anyone, level };
        if !out.iter().any(|o| (o.lo, o.hi, o.proto) == (w.lo, w.hi, w.proto)) {
            out.push(w);
        }
    };
    if let Some(p) = api {
        // The panel's address alone may be let in.
        add("the node's API port".into(), (p, p), Some(Proto::Tcp), false, Level::Error);
    }
    if let Some(p) = panel {
        add("the panel's port".into(), (p, p), Some(Proto::Tcp), true, Level::Error);
    }
    if let Some(state) = state {
        for i in state.inbounds.iter().filter(|i| !i.local()) {
            if let Some(range) = parse_ports(&i.port) {
                add(format!("inbound {}", i.name), range, i.proto(), true, Level::Warn);
            }
        }
        if let Some(range) = state.relay.as_ref().and_then(|r| parse_ports(&r.port)) {
            add("the cascade relay".into(), range, None, true, Level::Warn);
        }
    }
    out
}

/// The verdicts for the wishes against the text of `ufw status`: all open, or what is not.
fn firewall_items(status: &str, wanted: &[Want]) -> Vec<Item> {
    let rules = parse_ufw(status);
    let missing: Vec<&Want> = wanted.iter().filter(|w| !allowed(&rules, w)).collect();
    if missing.is_empty() {
        let list: Vec<String> = wanted.iter().map(Want::rule).collect();
        return vec![Item::ok("Firewall (ufw)", if list.is_empty() { "active".to_owned() } else { format!("open: {}", list.join(", ")) })];
    }
    [Level::Error, Level::Warn]
        .into_iter()
        .filter_map(|level| {
            let group: Vec<&&Want> = missing.iter().filter(|w| w.level == level).collect();
            if group.is_empty() {
                return None;
            }
            let list: Vec<String> = group.iter().map(|w| format!("{} ({})", w.rule(), w.what)).collect();
            let commands: Vec<String> = group.iter().flat_map(|w| w.commands()).collect();
            let detail = format!("no rule lets through {}", list.join(", "));
            Some(if level == Level::Error {
                Item::error("Firewall (ufw)", detail, commands.join("; "))
            } else {
                Item::warn("Firewall (ufw)", detail, commands.join("; "))
            })
        })
        .collect()
}

fn firewall(install: &Install) -> Vec<Item> {
    if !install.ufw() {
        return Vec::new();
    }
    let Some(status) = system::output("ufw", &["status"]).filter(|s| s.contains("Status: active")) else {
        return vec![Item::ok("Firewall (ufw)", "off; a firewall in the hoster's panel is not checked here")];
    };
    let state: Option<NodeState> =
        std::fs::read_to_string(Path::new(DIR).join("data/node/node-state.json")).ok().and_then(|t| serde_json::from_str(&t).ok());
    let panel = (!install.node).then(|| install.env.get("PANEL_PORT").and_then(|p| p.parse().ok())).flatten();
    firewall_items(&status, &wants(install.node_port(), panel, state.as_ref()))
}

/// Seconds this server's clock is ahead of the other side's, from its Date header.
fn skew_secs(date: &str, now: i64) -> Option<i64> {
    Some(now - clock::parse_http_date(date)?)
}

fn clock_item(synced: Option<bool>, skew: Option<i64>) -> Item {
    const FIX: &str = "timedatectl set-ntp true";
    let by = |s: i64| format!("{} s {} GitHub's clock", s.abs(), if s > 0 { "ahead of" } else { "behind" });
    match (synced, skew) {
        (_, Some(s)) if s.abs() >= SKEW_LIMIT => Item::error("Clock", format!("{}: TLS and REALITY need the right time", by(s)), FIX),
        (Some(false), s) => {
            let near = s.map(|s| format!(", {}", by(s))).unwrap_or_default();
            Item::warn("Clock", format!("not synchronized with a time server{near}"), FIX)
        }
        (Some(true), Some(s)) => Item::ok("Clock", format!("synchronized, within {} s of GitHub's clock", s.abs())),
        (Some(true), None) => Item::ok("Clock", "synchronized"),
        (None, Some(s)) => Item::ok("Clock", format!("within {} s of GitHub's clock", s.abs())),
        (None, None) => Item::warn("Clock", "cannot tell whether it is right", FIX),
    }
}

/// None: the lookup did not finish in time.
fn dns_item(result: Option<Result<usize, String>>) -> Item {
    const FIX: &str = "check the nameserver in /etc/resolv.conf (resolvectl status); test it: getent hosts github.com";
    match result {
        Some(Ok(n)) => Item::ok("DNS", format!("github.com resolves ({n} address{})", if n == 1 { "" } else { "es" })),
        Some(Err(e)) => Item::error("DNS", format!("cannot resolve github.com: {e}"), FIX),
        None => Item::error("DNS", "github.com did not resolve in time", FIX),
    }
}

/// Any answer shows the way is open (ghcr.io/v2/ says 401 to everyone); consequence is what
/// breaks without it.
fn outbound_item(label: &str, host: &str, consequence: &str, result: Option<Reached>) -> Item {
    let fix = format!("check the hoster's outbound firewall, DNS and any proxy; test it: curl -sSI https://{host}");
    match result {
        Some(Ok(p)) => Item::ok(label, format!("{host} answers (HTTP {})", p.status)),
        Some(Err(e)) => Item::error(label, format!("cannot connect to {host}: {e}; {consequence}"), fix),
        None => Item::error(label, format!("no answer from {host} in time; {consequence}"), fix),
    }
}

fn disk_item(free: Option<u64>) -> Item {
    let Some(bytes) = free else { return Item::warn("Disk", "cannot tell the free space", "df -h /opt/mikan") };
    let detail = format!("{:.1} GB free in {DIR}", bytes as f64 / (1024.0 * MIB as f64));
    if bytes < 500 * MIB {
        Item::error("Disk", detail, "free space: docker image prune -a removes unused images; old files in /opt/mikan/backups")
    } else if bytes < 2048 * MIB {
        Item::warn("Disk", detail, "free space before the next update: docker image prune -a; old files in /opt/mikan/backups")
    } else {
        Item::ok("Disk", detail)
    }
}

fn memory_item(available_mb: Option<u64>) -> Item {
    match available_mb {
        Some(mb) if mb < 100 => Item::warn("Memory", format!("{mb} MB available"), "add swap or memory; docker stats shows what uses it"),
        Some(mb) => Item::ok("Memory", format!("{mb} MB available")),
        None => Item::warn("Memory", "cannot tell", "free -m"),
    }
}

/// The hello of a node to its panel, tried once more now; the file's last result when that
/// cannot be done.
fn contact(port: Option<u16>, engine: bool) -> Vec<Item> {
    const LABEL: &str = "Panel contact";
    if !hello::path().exists() {
        return vec![Item::ok(LABEL, "this node version does not report it; mikan update")];
    }
    let fresh = engine
        .then(|| capture(docker::compose(&["exec", "-T", "node", "/usr/local/bin/mikan-node", "hello"]), Duration::from_secs(30)))
        .flatten()
        .and_then(|text| hello::parse(text.trim()).or_else(|| text.lines().rev().find_map(|l| hello::parse(l.trim()))));
    let (h, earlier) = match fresh {
        Some(h) => (h, None),
        None => match hello::last() {
            Some(h) => {
                let at = h.at.clone();
                (h, Some(at))
            }
            None => return vec![Item::warn(LABEL, "the node's report cannot be read", "mikan logs node")],
        },
    };
    let mut items = Vec::new();
    if let Some(at) = earlier {
        items.push(Item::warn(LABEL, format!("cannot ask the node now; its last result, from {at}:"), "mikan logs node"));
    }
    items.extend(hello::lines(&h, port).into_iter().map(|(level, text)| Item { level, label: LABEL.into(), detail: text, fix: None }));
    items
}

/// A command's stdout; None when it cannot be run or does not finish in time. Its exit
/// status does not matter.
fn capture(mut cmd: Command, limit: Duration) -> Option<String> {
    let mut child = cmd.stdin(Stdio::null()).stdout(Stdio::piped()).stderr(Stdio::null()).spawn().ok()?;
    let mut pipe = child.stdout.take()?;
    let reader = thread::spawn(move || {
        let mut text = String::new();
        let _ = pipe.read_to_string(&mut text);
        text
    });
    let start = Instant::now();
    loop {
        match child.try_wait() {
            Ok(Some(_)) => return reader.join().ok(),
            Ok(None) if start.elapsed() < limit => thread::sleep(Duration::from_millis(100)),
            _ => {
                let _ = child.kill();
                let _ = child.wait();
                return None;
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const UFW: &str = "Status: active

To                         Action      From
--                         ------      ----
22/tcp                     ALLOW       Anywhere
31234/tcp                  ALLOW       203.0.113.5
443                        ALLOW       Anywhere
20000:20100/udp            ALLOW       Anywhere
80,8080/tcp                ALLOW       Anywhere
8443/tcp (v6)              ALLOW       Anywhere (v6)
2053/tcp                   DENY        Anywhere
2087/tcp                   ALLOW OUT   Anywhere
3000/tcp                   ALLOW IN    Anywhere on eth0
OpenSSH                    ALLOW       Anywhere
Anywhere                   ALLOW       10.0.0.0/8
";

    fn want(lo: u16, hi: u16, proto: Option<Proto>, anyone: bool) -> Want {
        Want { what: "test".into(), lo, hi, proto, anyone, level: Level::Error }
    }

    #[test]
    fn the_rules_of_ufw_status() {
        let rules = parse_ufw(UFW);
        let has = |lo, hi, proto, anywhere| rules.contains(&Rule { lo, hi, proto, anywhere });
        assert!(has(22, 22, Some(Proto::Tcp), true));
        assert!(has(31234, 31234, Some(Proto::Tcp), false), "a rule for one address is kept, marked");
        assert!(has(443, 443, None, true));
        assert!(has(20000, 20100, Some(Proto::Udp), true));
        assert!(has(80, 80, Some(Proto::Tcp), true) && has(8080, 8080, Some(Proto::Tcp), true));
        assert!(has(8443, 8443, Some(Proto::Tcp), true), "the (v6) mark is not part of the port");
        assert!(has(3000, 3000, Some(Proto::Tcp), true));
        assert!(has(1, 65535, None, false));
        assert!(!rules.iter().any(|r| r.lo == 2053), "a deny rule opens nothing");
        assert!(!rules.iter().any(|r| r.lo == 2087), "outgoing rules open nothing");
        assert_eq!(parse_ufw("Status: inactive\n"), Vec::new());
        assert_eq!(parse_to("esp"), Vec::new());
        assert_eq!(parse_to("70000"), vec![]);
    }

    #[test]
    fn which_ports_are_open() {
        let rules = parse_ufw(UFW);
        let tcp = Some(Proto::Tcp);
        let udp = Some(Proto::Udp);
        assert!(allowed(&rules, &want(443, 443, tcp, true)), "443 without a protocol is both");
        assert!(allowed(&rules, &want(443, 443, udp, true)));
        assert!(allowed(&rules, &want(443, 443, None, true)));
        assert!(allowed(&rules, &want(20000, 20100, udp, true)));
        assert!(allowed(&rules, &want(20050, 20060, udp, true)));
        assert!(!allowed(&rules, &want(20000, 20100, tcp, true)), "the range is udp only");
        assert!(!allowed(&rules, &want(20000, 20101, udp, true)), "one port outside the range");
        assert!(allowed(&rules, &want(20000, 20100, None, true)), "a rule of either protocol serves an unknown one");
        // the API port may be open for the panel's address alone, an inbound may not
        assert!(allowed(&rules, &want(31234, 31234, tcp, false)));
        assert!(!allowed(&rules, &want(31234, 31234, tcp, true)));
        assert!(!allowed(&rules, &want(2053, 2053, tcp, true)));
        assert!(!allowed(&rules, &want(9999, 9999, None, true)));
        assert!(!allowed(&[], &want(443, 443, None, false)));
        // two rules side by side cover a range together
        let split = parse_ufw("5000:5009/tcp ALLOW Anywhere\n5010:5019/tcp ALLOW Anywhere\n");
        assert!(allowed(&split, &want(5000, 5019, tcp, true)));
    }

    #[test]
    fn the_wishes_of_a_node() {
        let state: NodeState = serde_json::from_str(
            r#"{"inbounds":[
                {"name":"vision","listen":"0.0.0.0","port":"443","config":{"type":"vless"}},
                {"name":"hy2","listen":"::","port":"20000-20100","config":{"type":"hysteria2"}},
                {"name":"hy2 again","port":"443","config":{"type":"hysteria2"}},
                {"name":"inner","listen":"127.0.0.1","port":"9999"},
                {"name":"old","port":"8443","preset":"tuic_v5"},
                {"name":"odd","port":"nothing"}
              ],"relay":{"port":"31000","config":{}},"slots":[]}"#,
        )
        .unwrap();
        let w = wants(Some(31234), None, Some(&state));
        let rules: Vec<String> = w.iter().map(Want::rule).collect();
        assert_eq!(rules, ["31234/tcp", "443/tcp", "20000:20100/udp", "443/udp", "8443/udp", "31000"]);
        assert!(!w[0].anyone && w[1].anyone);
        assert_eq!((w[0].level, w[1].level), (Level::Error, Level::Warn));
        assert_eq!(w[5].commands(), ["ufw allow 31000"]);
        assert_eq!(want(2000, 2010, None, true).commands(), ["ufw allow 2000:2010/tcp", "ufw allow 2000:2010/udp"]);
        // a panel wants its own port, for everyone
        let p = wants(None, Some(21355), None);
        assert_eq!((p.len(), p[0].rule(), p[0].anyone), (1, "21355/tcp".to_owned(), true));
        assert!(wants(None, None, None).is_empty());
        assert_eq!(parse_ports("20000-20100"), Some((20000, 20100)));
        assert_eq!(parse_ports("20000:20100"), Some((20000, 20100)));
        assert_eq!(parse_ports("20100-20000"), None);
        assert_eq!(parse_ports("0"), None);
    }

    #[test]
    fn the_firewall_verdicts() {
        let ok = firewall_items(UFW, &wants(Some(31234), None, None));
        assert_eq!(ok.len(), 1);
        assert_eq!((ok[0].level, ok[0].detail.as_str()), (Level::Ok, "open: 31234/tcp"));

        let plain = "Status: active\n\n22/tcp ALLOW Anywhere\n";
        let items = firewall_items(
            plain,
            &[want(31235, 31235, Some(Proto::Tcp), false), Want { level: Level::Warn, ..want(2053, 2053, None, true) }],
        );
        assert_eq!(items.len(), 2);
        assert_eq!(items[0].level, Level::Error);
        assert!(items[0].detail.contains("31235/tcp (test)"), "{}", items[0].detail);
        assert_eq!(items[0].fix.as_deref(), Some("ufw allow 31235/tcp"));
        assert_eq!((items[1].level, items[1].fix.as_deref()), (Level::Warn, Some("ufw allow 2053")));
        assert_eq!(items[0].lines()[1], "    fix: ufw allow 31235/tcp");
        assert_eq!(firewall_items("Status: active\n", &[]).len(), 1);
    }

    #[test]
    fn items_print_as_lines() {
        assert_eq!(Item::ok("Docker", "engine 28.0").lines(), ["✓ Docker            engine 28.0"]);
        let bad = Item::error("Disk", "0.3 GB free", "free space").lines();
        assert_eq!(bad, ["✗ Disk              0.3 GB free", "    fix: free space"]);
        assert_eq!(Item::warn("Clock", "late", "set it").lines()[0], "! Clock             late");
        // a fine line has no fix under it
        let fine = Item { fix: Some("x".into()), ..Item::ok("A", "b") };
        assert_eq!(fine.lines().len(), 1);
    }

    #[test]
    fn the_clock() {
        // Date headers are read in UTC; the same moment 5 s later is 5 s ahead.
        let now = 784_111_782;
        assert_eq!(skew_secs("Sun, 06 Nov 1994 08:49:37 GMT", now), Some(5));
        assert_eq!(skew_secs("Sun, 06 Nov 1994 08:49:37 GMT", now - 100), Some(-95));
        assert_eq!(skew_secs("yesterday", now), None);

        assert_eq!(clock_item(Some(true), Some(2)).level, Level::Ok);
        assert_eq!(clock_item(Some(true), None).level, Level::Ok);
        assert_eq!(clock_item(None, Some(-3)).level, Level::Ok);
        let late = clock_item(Some(true), Some(-47));
        assert_eq!(late.level, Level::Error);
        assert!(late.detail.contains("47 s behind GitHub's clock"), "{}", late.detail);
        assert_eq!(late.fix.as_deref(), Some("timedatectl set-ntp true"));
        let ahead = clock_item(Some(false), Some(31));
        assert!(ahead.level == Level::Error && ahead.detail.contains("ahead of"));
        assert_eq!(clock_item(Some(false), Some(4)).level, Level::Warn);
        assert_eq!(clock_item(Some(false), None).level, Level::Warn);
        assert_eq!(clock_item(None, None).level, Level::Warn);
        assert_eq!(clock_item(None, Some(29)).level, Level::Ok);
    }

    #[test]
    fn the_outside() {
        let answered = |status| Some(Ok(net::Probe { status, date: None }));
        assert_eq!(outbound_item("GHCR", "ghcr.io", "x", answered(401)).level, Level::Ok, "401 is an answer");
        let down = outbound_item("GitHub", "github.com", "updates cannot be downloaded", Some(Err("connection refused".into())));
        assert_eq!(down.level, Level::Error);
        assert!(down.detail.contains("updates cannot be downloaded") && down.detail.contains("connection refused"));
        assert_eq!(outbound_item("GitHub", "github.com", "x", None).level, Level::Error);
        assert_eq!(dns_item(Some(Ok(2))).level, Level::Ok);
        assert!(dns_item(Some(Ok(1))).detail.contains("1 address)"));
        assert!(dns_item(Some(Err("failed to lookup".into()))).fix.unwrap().contains("resolv.conf"));
        assert_eq!(dns_item(None).level, Level::Error);
    }

    #[test]
    fn disk_and_memory() {
        assert_eq!(disk_item(Some(100 * MIB)).level, Level::Error);
        assert_eq!(disk_item(Some(499 * MIB)).level, Level::Error);
        assert_eq!(disk_item(Some(500 * MIB)).level, Level::Warn);
        assert_eq!(disk_item(Some(2047 * MIB)).level, Level::Warn);
        assert_eq!(disk_item(Some(2048 * MIB)).level, Level::Ok);
        assert_eq!(disk_item(Some(10 * 1024 * MIB)).detail, "10.0 GB free in /opt/mikan");
        assert_eq!(disk_item(None).level, Level::Warn);
        assert_eq!(memory_item(Some(99)).level, Level::Warn);
        assert_eq!(memory_item(Some(100)).level, Level::Ok);
        assert_eq!(memory_item(None).level, Level::Warn);
        assert_eq!(docker_item(Some("28.0.1")).level, Level::Ok);
        assert_eq!(docker_item(None).level, Level::Error);
    }

    #[test]
    fn containers_by_state() {
        let service = |state: &str, status: &str| Service { name: "node".into(), state: state.into(), status: status.into() };
        assert_eq!(container(&service("running", "Up 3 hours")).level, Level::Ok);
        assert_eq!(container(&service("running", "Up 3 hours (healthy)")).level, Level::Ok);
        assert_eq!(container(&service("running", "Up 5 seconds (health: starting)")).level, Level::Warn);
        let sick = container(&service("running", "Up 2 minutes (unhealthy)"));
        assert_eq!(sick.level, Level::Error);
        assert_eq!(sick.fix.as_deref(), Some("mikan restart; mikan logs node"));
        assert_eq!(container(&service("exited", "Exited (1) 2 minutes ago")).level, Level::Error);
        assert_eq!(container(&service("restarting", "Restarting (1) 5 seconds ago")).level, Level::Error);
        assert_eq!(containers(Ok(Vec::new()))[0].level, Level::Error);
        assert_eq!(containers(Err("no docker".into()))[0].level, Level::Error);
        assert_eq!(containers(Ok(vec![service("running", "Up")])).len(), 1);
    }

    #[test]
    fn a_command_with_a_limit() {
        assert_eq!(capture(Command::new("echo"), Duration::from_secs(5)).as_deref(), Some("\n"));
        let mut slow = Command::new("sleep");
        slow.arg("30");
        let t = Instant::now();
        assert_eq!(capture(slow, Duration::from_millis(300)), None);
        assert!(t.elapsed() < Duration::from_secs(5));
        assert_eq!(capture(Command::new("/nonexistent/mikan-test"), Duration::from_secs(1)), None);
    }
}
