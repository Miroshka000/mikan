//! A node's API port for its panel alone (the panel's "API only from the panel"). The node
//! refuses strangers in its own listener; this puts the same rule into the server's
//! firewall, so that scanners do not reach the node at all. The node's container cannot
//! touch the firewall, so it asks through its data directory (internal/hostchan apilock.go)
//! and a unit of the host keeps the rule:
//!
//!   data/node/apilock/request      {"id", "at", "allow": ["203.0.113.5/32", ...]}: the rule
//!                                  the node wants; "allow": null takes it away
//!   data/node/apilock/seen         {"at": <unix seconds>}: when the panel last reached the API
//!   data/node/apilock/status.json  written here: whether the rule is in, and why not
//!   data/node/apilock/lifted       written by `mikan api-lockdown off`: the node opens its
//!                                  listener too
//!   /opt/mikan/apilock.json        the host's own copy of what the node asked for
//!
//! The rule is an nftables table of its own (inet mikan_api) that drops TCP to NODE_API_PORT
//! from anywhere but the networks asked for and loopback; it changes nothing else of the
//! firewall, and ufw's rules still apply to what it lets through. Lockout protection: the
//! rule stays only while the panel keeps coming. When it has not reached the node for
//! STALE_SECS, the timer takes the rule away (the node stops refusing at the same time), a
//! panel that moved gets in from its new address, and the node asks for the rule again with
//! that address in it.
//!
//! The container is not trusted with more than networks: the port is the server's own
//! setting, every network must parse as an address and a prefix, and a request that holds
//! anything else is no request.

use std::io::Write;
use std::net::IpAddr;
use std::path::Path;
use std::process::{Command, Stdio};

use anyhow::{Context, Result, bail};
use serde::{Deserialize, Serialize};

use crate::ops::Install;
use crate::panelfs::Dir;
use crate::system::Level;
use crate::{DIR, clock, hostreq};

/// The nftables table of the rule.
pub const TABLE: &str = "mikan_api";

/// How long the panel may stay away before the rule goes (nodeapi.APILockStale).
pub const STALE_SECS: i64 = 15 * 60;

/// A seen time this far ahead of the clock still counts (a clock set back a little).
const AHEAD_SECS: i64 = 120;

/// The most networks a rule takes: the panel's 16 and its own addresses are far fewer.
const MAX_NETS: usize = 64;

/// The largest file the node writes here.
const LIMIT: u64 = 16 << 10;

/// The host's own record, in DIR.
const STATE: &str = "apilock.json";

/// Takes the table away, and is harmless when there is none: nft creates it first.
const REMOVE: &str = "table inet mikan_api {}\ndelete table inet mikan_api\n";

/// A network of the rule: an address and how many of its bits count, the rest cleared.
#[derive(Clone, Copy, Debug, PartialEq, Eq, PartialOrd, Ord)]
pub struct Net {
    addr: IpAddr,
    bits: u8,
}

impl Net {
    /// "203.0.113.5", "203.0.113.0/24", "2001:db8::/32"; None for anything else, a network
    /// of everything (/0) included: the lock would lock nothing.
    pub fn parse(s: &str) -> Option<Net> {
        let (addr, bits) = match s.split_once('/') {
            Some((a, b)) => {
                if b.is_empty() || b.len() > 3 || !b.bytes().all(|c| c.is_ascii_digit()) {
                    return None;
                }
                (a.parse::<IpAddr>().ok()?, b.parse::<u8>().ok()?)
            }
            None => {
                let a = s.parse::<IpAddr>().ok()?;
                (a, if a.is_ipv4() { 32 } else { 128 })
            }
        };
        let max = if addr.is_ipv4() { 32 } else { 128 };
        if bits == 0 || bits > max {
            return None;
        }
        let addr = match addr {
            IpAddr::V4(a) => IpAddr::V4((u32::from(a) & (u32::MAX << (32 - u32::from(bits)))).into()),
            IpAddr::V6(a) => {
                let mask = if bits == 128 { u128::MAX } else { !(u128::MAX >> bits) };
                IpAddr::V6((u128::from(a) & mask).into())
            }
        };
        Some(Net { addr, bits })
    }
}

impl std::fmt::Display for Net {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "{}/{}", self.addr, self.bits)
    }
}

/// What the node's request asks for: Some(None) takes the rule away, Some(Some(nets)) puts
/// it in; None when it is no request (not JSON, a network that does not parse, too many).
fn parse_request(data: &[u8]) -> Option<Option<Vec<Net>>> {
    let v: serde_json::Value = serde_json::from_slice(data).ok()?;
    match v.as_object()?.get("allow") {
        None | Some(serde_json::Value::Null) => Some(None),
        Some(serde_json::Value::Array(list)) if list.len() <= MAX_NETS => {
            let mut nets = list.iter().map(|n| n.as_str().and_then(Net::parse)).collect::<Option<Vec<_>>>()?;
            nets.sort();
            nets.dedup();
            Some(Some(nets))
        }
        Some(_) => None,
    }
}

/// When the panel last reached the node, from the node's seen file; None for never.
fn seen_at(data: &[u8]) -> Option<i64> {
    let v: serde_json::Value = serde_json::from_slice(data).ok()?;
    v.get("at")?.as_i64().filter(|at| *at > 0)
}

/// Whether the panel came lately enough for the rule to stay.
fn fresh(seen: Option<i64>, now: i64) -> bool {
    seen.is_some_and(|s| (-AHEAD_SECS..STALE_SECS).contains(&(now - s)))
}

/// The nft script that puts the rule in, replacing the table of before in one step.
pub fn render(port: u16, nets: &[Net]) -> String {
    let mut s = format!(
        "table inet {TABLE} {{}}\ndelete table inet {TABLE}\ntable inet {TABLE} {{\n\tchain input {{\n\t\ttype filter hook input priority -10; policy accept;\n\t\ttcp dport {port} iifname \"lo\" accept\n"
    );
    for n in nets {
        let family = if n.addr.is_ipv4() { "ip" } else { "ip6" };
        s.push_str(&format!("\t\ttcp dport {port} {family} saddr {n} accept\n"));
    }
    s.push_str(&format!("\t\ttcp dport {port} drop\n\t}}\n}}\n"));
    s
}

/// The host's record of the rule.
#[derive(Serialize, Deserialize, Default, Debug, PartialEq)]
struct State {
    /// What the node asked for; None: no rule.
    allow: Option<Vec<String>>,
    /// The script last put in; "" while none is.
    #[serde(default)]
    applied: String,
    /// Why the last attempt failed; "" when it did not.
    #[serde(default)]
    error: String,
}

impl State {
    fn nets(&self) -> Option<Vec<Net>> {
        self.allow.as_ref().map(|a| a.iter().filter_map(|n| Net::parse(n)).collect())
    }
}

/// What the host tells the node (hostchan.APILockStatus).
#[derive(Serialize, Default, Debug, PartialEq)]
struct Status {
    on: bool,
    ok: bool,
    #[serde(skip_serializing_if = "String::is_empty")]
    error: String,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    lifted: bool,
    at: i64,
}

/// What changed in the firewall.
#[derive(Debug, PartialEq, Eq)]
enum Change {
    None,
    Put,
    Removed,
}

/// The server's firewall as the rule sees it; tests put a fake here.
trait Firewall {
    /// Whether the table is there.
    fn present(&mut self) -> bool;
    /// Runs an nft script.
    fn run(&mut self, script: &str) -> Result<()>;
}

struct Nft;

impl Firewall for Nft {
    fn present(&mut self) -> bool {
        crate::system::succeeds("nft", &["list", "table", "inet", TABLE])
    }

    fn run(&mut self, script: &str) -> Result<()> {
        let child = Command::new("nft").args(["-f", "-"]).stdin(Stdio::piped()).stdout(Stdio::null()).stderr(Stdio::piped()).spawn();
        let mut child = match child {
            Ok(c) => c,
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => bail!("nft is not installed"),
            Err(e) => return Err(e).context("run nft"),
        };
        child.stdin.take().context("nft's input")?.write_all(script.as_bytes())?;
        let out = child.wait_with_output()?;
        if out.status.success() {
            return Ok(());
        }
        let err = String::from_utf8_lossy(&out.stderr);
        match err.lines().map(str::trim).find(|l| !l.is_empty()) {
            Some(why) => bail!("nft: {why}"),
            None => bail!("nft failed ({})", out.status),
        }
    }
}

/// Brings the firewall in line with what the node asked for and when the panel last came:
/// the rule in while the panel keeps coming, out otherwise. The table is put in again when
/// it went missing (a reboot, a flush of the ruleset).
fn reconcile(st: &mut State, seen: Option<i64>, port: Option<u16>, now: i64, fw: &mut dyn Firewall) -> (Status, Change) {
    let mut status = Status { ok: true, at: now, ..Status::default() };
    let wanted = st.nets();
    let hold = wanted.is_some() && fresh(seen, now);
    let fail = |status: &mut Status, st: &mut State, e: String| {
        status.ok = false;
        status.error = e.clone();
        st.error = e;
    };
    if let (true, Some(nets), Some(port)) = (hold, &wanted, port) {
        let script = render(port, nets);
        let mut change = Change::None;
        if st.applied != script || !fw.present() {
            if let Err(e) = fw.run(&script) {
                st.applied.clear();
                fail(&mut status, st, format!("{e:#}"));
                return (status, change);
            }
            st.applied = script;
            change = Change::Put;
        }
        st.error.clear();
        status.on = true;
        return (status, change);
    }
    if hold {
        fail(&mut status, st, "NODE_API_PORT is not set in /opt/mikan/config.json".into());
    } else {
        st.error.clear();
        status.lifted = wanted.is_some();
    }
    let mut change = Change::None;
    if fw.present() {
        match fw.run(REMOVE) {
            Ok(()) => change = Change::Removed,
            Err(e) => {
                fail(&mut status, st, format!("{e:#}"));
                status.on = true;
                return (status, change);
            }
        }
    }
    st.applied.clear();
    (status, change)
}

fn load_state() -> State {
    std::fs::read(Path::new(DIR).join(STATE)).ok().and_then(|raw| serde_json::from_slice(&raw).ok()).unwrap_or_default()
}

fn save_state(st: &State) -> Result<()> {
    crate::config::write_private(&Path::new(DIR).join(STATE), &serde_json::to_vec(st)?)
}

fn now() -> i64 {
    clock::unix(std::time::SystemTime::now())
}

fn seen(dir: &Dir) -> Option<i64> {
    dir.read("seen", LIMIT).ok().flatten().and_then(|d| seen_at(&d))
}

/// `mikan api-lockdown --requested` (the apilock path unit and its timer): takes the node's
/// request when there is one and brings the firewall in line.
pub fn requested() -> Result<()> {
    let install = Install::load()?;
    if !install.node {
        return Ok(());
    }
    // A node installed with --panel-ip lets the panel's address alone in through ufw: while
    // the panel stays away, the rule follows the panel's address (panelip).
    if let Some(port) = install.node_port() {
        let seen = Dir::open(Path::new(DIR), "data/node/apilock", false).ok().flatten().and_then(|d| seen(&d));
        if !seen.is_some_and(|at| now() - at < crate::panelip::SILENT) {
            match crate::panelip::follow(port) {
                Ok(Some(note)) => crate::out(&note),
                Ok(None) => {}
                Err(e) => crate::out(&format!("api port: the rule did not follow the panel: {e:#}")),
            }
        }
    }
    let mut st = load_state();
    let Some(dir) = hostreq::dir(true, "apilock")? else {
        // The node never asked; a rule of before (its data moved away) still goes.
        if st.allow.is_none() && st.applied.is_empty() {
            return Ok(());
        }
        st.allow = None;
        let (_, change) = reconcile(&mut st, None, install.node_port(), now(), &mut Nft);
        return save_state(&st).map(|()| say(&change, &st));
    };
    let taken = hostreq::take_bytes(&dir)?;
    if let Some(data) = &taken {
        match parse_request(data) {
            Some(allow) => st.allow = allow.map(|nets| nets.iter().map(Net::to_string).collect()),
            None => crate::out("api lockdown: a request that is not one was dropped"),
        }
    }
    // The timer runs this every minute on every node: one with nothing to keep or take away
    // is left alone.
    if taken.is_none() && st.allow.is_none() && st.applied.is_empty() {
        return Ok(());
    }
    let (status, change) = reconcile(&mut st, seen(&dir), install.node_port(), now(), &mut Nft);
    save_state(&st)?;
    dir.write("status.json", &serde_json::to_vec(&status)?, 0o644)?;
    say(&change, &st);
    Ok(())
}

fn say(change: &Change, st: &State) {
    match change {
        Change::Put => {
            crate::out(&format!("api lockdown: the API port answers {} only", st.allow.as_deref().unwrap_or_default().join(", ")))
        }
        Change::Removed if st.allow.is_some() => crate::out("api lockdown: lifted, the panel has not reached the node lately"),
        Change::Removed => crate::out("api lockdown: off"),
        Change::None if !st.error.is_empty() => crate::out(&format!("api lockdown: {}", st.error)),
        Change::None => {}
    }
}

/// `mikan api-lockdown off`: the escape hatch on the node's server. The rule goes at once and
/// the node stops refusing within seconds; the rule comes back when the panel reaches the
/// node with the setting still on.
pub fn off() -> Result<()> {
    let install = Install::load()?;
    if !install.node {
        bail!("api-lockdown is for a node: this server runs a panel");
    }
    let mut st = load_state();
    st.allow = None;
    let (mut status, _) = reconcile(&mut st, None, install.node_port(), now(), &mut Nft);
    save_state(&st)?;
    status.lifted = true;
    if let Some(dir) = Dir::open(Path::new(DIR), "data/node/apilock", true)? {
        dir.write("lifted", b"", 0o644)?;
        dir.write("status.json", &serde_json::to_vec(&status)?, 0o644)?;
    }
    if !status.ok {
        bail!("the rule could not be taken away: {}", status.error);
    }
    crate::out("The node's API port answers any address again; the node stops refusing within 10 seconds.");
    crate::out(
        "The rule comes back when the panel reaches the node with \"API only from the panel\" on: turn it off in the node's settings in the panel to keep it off.",
    );
    Ok(())
}

/// `mikan api-lockdown` and `mikan api-lockdown status`.
pub fn show() -> Result<()> {
    let install = Install::load()?;
    if !install.node {
        bail!("api-lockdown is for a node: this server runs a panel");
    }
    let r = report(&install);
    crate::out(&format!("{} {}", crate::setup::mark(r.level), r.detail));
    if let Some(fix) = r.fix {
        crate::out(&format!("    fix: {fix}"));
    }
    Ok(())
}

/// The state of the rule in words, for `mikan doctor` and `mikan api-lockdown status`.
pub struct Report {
    pub level: Level,
    pub detail: String,
    pub fix: Option<String>,
}

pub fn report(install: &Install) -> Report {
    let st = load_state();
    let seen = Dir::open(Path::new(DIR), "data/node/apilock", false).ok().flatten().and_then(|d| seen(&d));
    describe(&st, seen, install.node_port(), now(), Nft.present())
}

fn describe(st: &State, seen: Option<i64>, port: Option<u16>, now: i64, present: bool) -> Report {
    let port = port.map_or_else(|| "the API port".to_owned(), |p| format!("TCP {p}"));
    let ago = |s: Option<i64>| match s {
        Some(s) if now >= s => format!("{} min ago", (now - s) / 60),
        Some(_) => "just now".to_owned(),
        None => "never since the lock was set".to_owned(),
    };
    let Some(nets) = st.nets() else {
        if present {
            return Report {
                level: Level::Warn,
                detail: format!("off, but a rule of before still holds {port}"),
                fix: Some("mikan api-lockdown off".into()),
            };
        }
        return Report {
            level: Level::Ok,
            detail: format!("off: {port} answers any address, the panel's certificate guards it"),
            fix: None,
        };
    };
    if !fresh(seen, now) {
        return Report {
            level: Level::Warn,
            detail: format!("lifted: the panel came {}, {port} answers any address until it comes back", ago(seen)),
            fix: Some(format!("check that the panel reaches this server on {port}")),
        };
    }
    if !present {
        let fix = if st.error.contains("not installed") { "apt install nftables" } else { "journalctl -u mikan-apilock -n 20" };
        let why = if st.error.is_empty() { "the rule is not in the firewall yet".to_owned() } else { st.error.clone() };
        return Report {
            level: Level::Warn,
            detail: format!("on, the node refuses strangers itself, but the firewall has no rule: {why}"),
            fix: Some(fix.into()),
        };
    }
    Report {
        level: Level::Ok,
        detail: format!("on: {port} answers {} network(s) of the panel and loopback only; the panel came {}", nets.len(), ago(seen)),
        fix: None,
    }
}

/// Takes the rule away for good (`mikan uninstall`).
pub fn remove() {
    let mut fw = Nft;
    if fw.present() {
        let _ = fw.run(REMOVE);
    }
    let _ = std::fs::remove_file(Path::new(DIR).join(STATE));
}

#[cfg(test)]
mod tests {
    use super::*;

    #[derive(Default)]
    struct Fake {
        table: Option<String>,
        runs: Vec<String>,
        broken: Option<&'static str>,
    }

    impl Firewall for Fake {
        fn present(&mut self) -> bool {
            self.table.is_some()
        }

        fn run(&mut self, script: &str) -> Result<()> {
            if let Some(e) = self.broken {
                bail!("{e}");
            }
            self.runs.push(script.to_owned());
            self.table = (script != REMOVE).then(|| script.to_owned());
            Ok(())
        }
    }

    fn nets(list: &[&str]) -> Vec<Net> {
        list.iter().map(|n| Net::parse(n).unwrap()).collect()
    }

    #[test]
    fn networks() {
        for (s, want) in [
            ("203.0.113.5", "203.0.113.5/32"),
            ("203.0.113.5/24", "203.0.113.0/24"),
            ("2001:db8::1", "2001:db8::1/128"),
            ("2001:db8::1/32", "2001:db8::/32"),
            ("10.0.0.1/1", "0.0.0.0/1"),
        ] {
            assert_eq!(Net::parse(s).map(|n| n.to_string()).as_deref(), Some(want), "{s}");
        }
        for bad in [
            "",
            "x",
            "0.0.0.0/0",
            "::/0",
            "1.2.3.4/33",
            "::1/129",
            "1.2.3.4/",
            "1.2.3.4/+8",
            "1.2.3.4/ 8",
            "1.2.3.4/0008",
            "1.2.3.4;drop",
            "a/8",
        ] {
            assert_eq!(Net::parse(bad), None, "{bad:?}");
        }
    }

    // A request is the networks and nothing else: what does not parse drops the request.
    #[test]
    fn requests() {
        assert_eq!(
            parse_request(br#"{"id":"a","at":"x","allow":["203.0.113.9","198.51.100.7/24","203.0.113.9/32"]}"#),
            Some(Some(nets(&["198.51.100.0/24", "203.0.113.9/32"])))
        );
        assert_eq!(parse_request(br#"{"id":"a","allow":null}"#), Some(None));
        assert_eq!(parse_request(br#"{"id":"a"}"#), Some(None));
        assert_eq!(parse_request(br#"{"allow":[]}"#), Some(Some(vec![])));
        let many = format!(r#"{{"allow":[{}]}}"#, vec!["\"203.0.113.1\""; MAX_NETS + 1].join(","));
        for bad in [
            r#"{"allow":["203.0.113.9","nope"]}"#,
            r#"{"allow":[5]}"#,
            r#"{"allow":"203.0.113.9"}"#,
            r#"{"allow":["0.0.0.0/0"]}"#,
            "[1]",
            "junk",
            &many,
        ] {
            assert_eq!(parse_request(bad.as_bytes()), None, "{bad}");
        }
    }

    #[test]
    fn the_rule_drops_the_port_from_anywhere_but_the_panel() {
        let s = render(25305, &nets(&["203.0.113.5", "2001:db8::/32"]));
        assert!(s.starts_with("table inet mikan_api {}\ndelete table inet mikan_api\ntable inet mikan_api {\n"), "{s}");
        assert!(s.contains("type filter hook input priority -10; policy accept;"));
        let rules: Vec<&str> = s.lines().map(str::trim).filter(|l| l.starts_with("tcp dport")).collect();
        assert_eq!(
            rules,
            [
                "tcp dport 25305 iifname \"lo\" accept",
                "tcp dport 25305 ip saddr 203.0.113.5/32 accept",
                "tcp dport 25305 ip6 saddr 2001:db8::/32 accept",
                "tcp dport 25305 drop",
            ]
        );
        // Nothing but the port is dropped: every rule names it.
        assert!(
            s.lines().filter(|l| l.contains("drop") || l.contains("accept;")).all(|l| l.contains("25305") || l.contains("policy accept"))
        );
        // With no network of the panel's, only loopback reaches the port (the node adds the panel's).
        assert_eq!(render(1, &[]).lines().filter(|l| l.contains("accept") && !l.contains("policy")).count(), 1);
    }

    #[test]
    fn seen_times() {
        assert_eq!(seen_at(br#"{"at":1800000000,"from":["203.0.113.5"]}"#), Some(1_800_000_000));
        for none in [&br#"{"at":0}"#[..], br#"{}"#, b"junk", br#"{"at":"x"}"#] {
            assert_eq!(seen_at(none), None);
        }
        let now = 1_800_000_000;
        assert!(fresh(Some(now), now) && fresh(Some(now - STALE_SECS + 1), now) && fresh(Some(now + 60), now));
        assert!(!fresh(Some(now - STALE_SECS), now) && !fresh(None, now) && !fresh(Some(now + AHEAD_SECS + 1), now));
    }

    // The rule goes in while the panel comes, is not put in again while it holds, comes back
    // when the table went missing, and goes after 15 minutes without the panel.
    #[test]
    fn the_rule_lifts_without_the_panel() {
        let now = 1_800_000_000;
        let mut fw = Fake::default();
        let mut st = State { allow: Some(vec!["203.0.113.5/32".into()]), ..State::default() };
        let (status, change) = reconcile(&mut st, Some(now), Some(25305), now, &mut fw);
        assert_eq!(change, Change::Put);
        assert_eq!(status, Status { on: true, ok: true, at: now, ..Status::default() });
        assert!(fw.table.as_deref().unwrap().contains("ip saddr 203.0.113.5/32 accept"));
        // Held: nft is not run again every minute.
        let (_, change) = reconcile(&mut st, Some(now), Some(25305), now + 60, &mut fw);
        assert_eq!((change, fw.runs.len()), (Change::None, 1));
        // A reboot flushed the ruleset: the rule comes back.
        fw.table = None;
        let (status, change) = reconcile(&mut st, Some(now), Some(25305), now + 120, &mut fw);
        assert_eq!(change, Change::Put);
        assert!(status.on && fw.table.is_some());
        // 15 minutes without the panel: the rule goes, the node's wish stays.
        let (status, change) = reconcile(&mut st, Some(now), Some(25305), now + STALE_SECS, &mut fw);
        assert_eq!(change, Change::Removed);
        assert_eq!(status, Status { ok: true, lifted: true, at: now + STALE_SECS, ..Status::default() });
        assert!(fw.table.is_none() && st.allow.is_some() && st.applied.is_empty());
        // The panel comes back (from wherever the node says): the rule is in again.
        let (status, change) = reconcile(&mut st, Some(now + STALE_SECS + 30), Some(25305), now + STALE_SECS + 40, &mut fw);
        assert_eq!(change, Change::Put);
        assert!(status.on);
        // Off: the rule goes and stays away.
        st.allow = None;
        let (status, change) = reconcile(&mut st, Some(now + STALE_SECS + 30), Some(25305), now + STALE_SECS + 50, &mut fw);
        assert_eq!(change, Change::Removed);
        assert!(!status.on && !status.lifted && status.ok && fw.table.is_none());
        let (_, change) = reconcile(&mut st, None, Some(25305), now + STALE_SECS + 60, &mut fw);
        assert_eq!(change, Change::None);
    }

    #[test]
    fn failures_are_told() {
        let now = 1_800_000_000;
        let mut fw = Fake { broken: Some("nft is not installed"), ..Fake::default() };
        let mut st = State { allow: Some(vec!["203.0.113.5/32".into()]), ..State::default() };
        let (status, _) = reconcile(&mut st, Some(now), Some(25305), now, &mut fw);
        assert!(!status.ok && !status.on && status.error == "nft is not installed" && st.applied.is_empty());
        let r = describe(&st, Some(now), Some(25305), now, false);
        assert!(
            r.level == Level::Warn && r.detail.contains("nft is not installed") && r.fix.as_deref() == Some("apt install nftables"),
            "{}",
            r.detail
        );
        // Without the port there is nothing to lock.
        let mut fw = Fake::default();
        let (status, change) = reconcile(&mut st, Some(now), None, now, &mut fw);
        assert!(!status.ok && status.error.contains("NODE_API_PORT") && change == Change::None);
    }

    #[test]
    fn the_doctor_says_how_it_is() {
        let now = 1_800_000_000;
        let off = State::default();
        assert_eq!(describe(&off, None, Some(25305), now, false).level, Level::Ok);
        assert_eq!(describe(&off, None, Some(25305), now, true).fix.as_deref(), Some("mikan api-lockdown off"));
        let on = State { allow: Some(vec!["203.0.113.5/32".into(), "198.51.100.0/24".into()]), ..State::default() };
        let r = describe(&on, Some(now - 120), Some(25305), now, true);
        assert!(
            r.level == Level::Ok && r.detail.contains("TCP 25305") && r.detail.contains("2 network(s)") && r.detail.contains("2 min ago"),
            "{}",
            r.detail
        );
        let r = describe(&on, Some(now - STALE_SECS - 60), Some(25305), now, false);
        assert!(r.level == Level::Warn && r.detail.starts_with("lifted") && r.detail.contains("16 min ago"), "{}", r.detail);
    }
}
