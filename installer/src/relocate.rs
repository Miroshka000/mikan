//! `mikan move user@new-server`: the panel goes to another server in one command, run on
//! the old one. It backs the panel up, installs mikan on the new server over ssh with the
//! same panel port, copies the backup there and restores it, and opens in the new
//! server's ufw what this one lets in for mikan. The old panel keeps running until the
//! admin stops it (or --stop-old does).
//!
//! ssh is the system's, with the admin's own keys and agent: the move never asks for a
//! password and keeps none. A step that fails stops the move, and what is left is printed
//! as commands to run by hand.

use std::fs::File;
use std::io::Read;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};

use anyhow::{Context, Result, bail};
use sha2::{Digest, Sha256};

use crate::ops::Install;
use crate::release::REPO;
use crate::{DIR, docker, lock, release, system};

/// The page with the move by hand.
pub const GUIDE: &str = "https://miroshka000.github.io/mikan/en/start/migrate/#moving-mikan-to-another-server";

/// Where the backup lands on the new server: where its own backups are.
const REMOTE_BACKUPS: &str = "/opt/mikan/backups";

/// The command mikan has on a server.
const REMOTE_MIKAN: &str = "/usr/local/bin/mikan";

#[derive(clap::Args, Debug, Clone)]
pub struct MoveArgs {
    /// The new server as ssh takes it: user@host or a Host of ~/.ssh/config. The user is
    /// root or has sudo without a password; ssh logs in with your key or agent
    pub target: String,
    /// The new server's ssh port
    #[arg(long, short = 'p', value_name = "PORT")]
    pub ssh_port: Option<u16>,
    /// Go on when the new server already has mikan: its data are replaced with this panel's
    /// (a copy of them stays in its /opt/mikan/backups)
    #[arg(long)]
    pub force: bool,
    /// Stop the panel here once the new server runs it
    #[arg(long)]
    pub stop_old: bool,
    /// Print the steps and do nothing
    #[arg(long)]
    pub dry_run: bool,
    /// Do not ask
    #[arg(long, short = 'y')]
    pub yes: bool,
}

/// What the move does, from this server's settings.
#[derive(Debug, Clone, PartialEq)]
pub struct Plan {
    pub target: String,
    pub ssh_port: Option<u16>,
    pub panel_port: u16,
    /// This panel's release: the new server must run the same or a newer one.
    pub version: String,
    /// ufw is mikan's here (not installed with --no-firewall).
    pub firewall: bool,
    /// The ufw rules to open on the new server beside the install's own.
    pub rules: Vec<String>,
    pub force: bool,
    pub stop_old: bool,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Step {
    Check,
    Backup,
    Install,
    Copy,
    Restore,
    Firewall,
    StopOld,
}

/// Whether s is a destination ssh takes as one, never as an option: [user@]host, a host
/// being a name, an IPv4 or an IPv6 address.
pub fn valid_target(s: &str) -> bool {
    let host = s.rsplit_once('@').map_or(s, |(user, host)| if valid_user(user) { host } else { "" });
    !host.is_empty()
        && !host.starts_with('-')
        && host.len() <= 253
        && host.bytes().all(|b| b.is_ascii_alphanumeric() || matches!(b, b'.' | b'-' | b'_' | b':'))
}

fn valid_user(u: &str) -> bool {
    !u.is_empty() && !u.starts_with('-') && u.len() <= 64 && u.bytes().all(|b| b.is_ascii_alphanumeric() || matches!(b, b'.' | b'-' | b'_'))
}

/// The host part of a target, for the messages.
fn host_of(target: &str) -> &str {
    target.rsplit_once('@').map_or(target, |(_, h)| h)
}

/// Whether name is one of the archives a backup makes: it goes into a shell line.
fn valid_backup_name(name: &str) -> bool {
    name.ends_with(".tar.gz")
        && !name.starts_with(['.', '-'])
        && name.bytes().all(|b| b.is_ascii_alphanumeric() || matches!(b, b'.' | b'-' | b'_'))
}

/// s in single quotes, for sh.
fn quote(s: &str) -> String {
    format!("'{}'", s.replace('\'', r"'\''"))
}

/// A script for the new server's sh, as root: through sudo when the user is not.
fn as_root(sudo: bool, script: &str) -> String {
    if sudo { format!("sudo -n sh -c {}", quote(script)) } else { format!("sh -c {}", quote(script)) }
}

/// What the check reads on the new server: one word a line.
const PROBE: &str = "if [ -e /opt/mikan/config.json ] || [ -e /opt/mikan/.env ]; then echo installed; fi; \
if grep -qs '\"MIKAN_MODE\": *\"node\"' /opt/mikan/config.json || grep -qs '^MIKAN_MODE=node' /opt/mikan/.env; then echo node; fi; \
command -v curl >/dev/null 2>&1 || echo no-curl";

/// Takes the backup on stdin into the new server's backups, private and whole or not at
/// all, and says its sha256.
fn copy_script(name: &str) -> String {
    let (dir, part) = (REMOTE_BACKUPS, format!("{REMOTE_BACKUPS}/.{name}.part"));
    format!("umask 077 && mkdir -p {dir} && cat > {part} && mv -f {part} {dir}/{name} && sha256sum {dir}/{name}")
}

#[derive(Debug, Default, PartialEq)]
struct Probe {
    installed: bool,
    node: bool,
    curl: bool,
}

fn parse_probe(out: &str) -> Probe {
    let has = |w: &str| out.lines().any(|l| l.trim() == w);
    Probe { installed: has("installed"), node: has("node"), curl: !has("no-curl") }
}

impl Plan {
    /// The plan for this server: a panel, run as root.
    pub fn load(a: &MoveArgs) -> Result<Self> {
        let install = Install::load()?;
        install.panel_only()?;
        let panel_port =
            install.config.get("PANEL_PORT").and_then(|p| p.parse().ok()).context("PANEL_PORT is not set in /opt/mikan/config.json")?;
        let rules = if install.ufw() { crate::doctor::moved_rules(&install) } else { Vec::new() };
        Self::new(a, panel_port, install.version(), install.ufw(), rules)
    }

    pub fn new(a: &MoveArgs, panel_port: u16, version: String, firewall: bool, rules: Vec<String>) -> Result<Self> {
        let target = a.target.trim().to_owned();
        if !valid_target(&target) {
            bail!("{target:?} is not a server for ssh: give user@host, such as root@203.0.113.7");
        }
        if let Some(bad) = rules.iter().find(|r| !crate::host::valid_rule(r)) {
            bail!("{bad:?} is not a firewall rule like 8443/tcp");
        }
        Ok(Self { target, ssh_port: a.ssh_port, panel_port, version, firewall, rules, force: a.force, stop_old: a.stop_old })
    }

    pub fn steps(&self) -> Vec<Step> {
        let mut s = vec![Step::Check, Step::Backup, Step::Install, Step::Copy, Step::Restore];
        if !self.rules.is_empty() {
            s.push(Step::Firewall);
        }
        if self.stop_old {
            s.push(Step::StopOld);
        }
        s
    }

    fn install_flags(&self) -> String {
        let mut f = format!("--yes --port {}", self.panel_port);
        if !self.firewall {
            f.push_str(" --no-firewall");
        }
        f
    }

    /// The one-line install with this panel's port, as root.
    fn install_script(&self) -> String {
        format!(
            "f=$(mktemp) && curl -fsSL --proto '=https' https://github.com/{REPO}/releases/latest/download/install.sh -o \"$f\" && bash \"$f\" {}; s=$?; rm -f \"$f\"; exit $s",
            self.install_flags()
        )
    }

    fn ufw_script(&self) -> String {
        let allow: Vec<String> = self.rules.iter().map(|r| format!("ufw allow {r} >/dev/null")).collect();
        format!("if ufw status 2>/dev/null | grep -q 'Status: active'; then {} && echo ufw-on; else echo ufw-off; fi", allow.join(" && "))
    }

    fn ssh_hint(&self) -> String {
        match self.ssh_port {
            Some(p) => format!("ssh -p {p} {}", self.target),
            None => format!("ssh {}", self.target),
        }
    }

    /// What a step does, for the progress and --dry-run; name is the backup's, once made.
    pub fn describe(&self, step: Step, name: Option<&str>) -> String {
        let t = &self.target;
        let file = name.unwrap_or("<the backup>");
        match step {
            Step::Check => {
                format!("Check that {t} lets ssh in with your key, runs as root or sudo without a password and has no mikan yet")
            }
            Step::Backup => "Back up this panel (mikan backup)".into(),
            Step::Install => {
                format!("Install mikan on {t} with the panel port {} (install.sh {})", self.panel_port, self.install_flags())
            }
            Step::Copy => format!("Copy {file} to {t}:{REMOTE_BACKUPS} and compare its sha256"),
            Step::Restore => format!("Restore it there (mikan restore -y {REMOTE_BACKUPS}/{file})"),
            Step::Firewall => format!("Open in the new server's ufw what this one lets in for mikan: {}", self.rules.join(" ")),
            Step::StopOld => "Stop the panel here (mikan restart starts it again)".into(),
        }
    }

    /// The step as commands to run by hand; copied: the backup is on the new server already.
    fn by_hand(&self, step: Step, name: Option<&str>, copied: bool) -> Vec<String> {
        let file = name.unwrap_or("<the backup>");
        let on_new = |c: &str| format!("on the new server ({}): {c}", self.ssh_hint());
        match step {
            Step::Check => vec![self.ssh_hint()],
            Step::Backup => vec!["mikan backup".into()],
            Step::Install => vec![on_new(&format!(
                "curl -fsSL https://github.com/{REPO}/releases/latest/download/install.sh | sudo bash -s -- {}",
                self.install_flags()
            ))],
            Step::Copy => {
                let port = self.ssh_port.map(|p| format!("-P {p} ")).unwrap_or_default();
                // scp reads the colon of an IPv6 address as the path's.
                let to = match self.target.rsplit_once('@') {
                    Some((u, h)) if h.contains(':') => format!("{u}@[{h}]"),
                    None if self.target.contains(':') => format!("[{}]", self.target),
                    _ => self.target.clone(),
                };
                vec![format!("scp {port}{DIR}/backups/{file} {to}:")]
            }
            Step::Restore if copied => vec![on_new(&format!("sudo mikan restore {REMOTE_BACKUPS}/{file}"))],
            Step::Restore => vec![on_new(&format!("sudo mikan restore ~/{file}"))],
            Step::Firewall => vec![on_new(&self.rules.iter().map(|r| format!("sudo ufw allow {r}")).collect::<Vec<_>>().join("; "))],
            Step::StopOld => vec![format!("docker compose --project-directory {DIR} stop")],
        }
    }

    /// What is left for the admin once the move is done.
    fn after(&self) -> Vec<String> {
        let host = host_of(&self.target);
        let mut l = vec![
            format!("The panel runs on {host}, at the same port and link (mikan url there shows it). What is left:"),
            format!(
                "  1. Point the panel's domain (and the subscription domain, if you have one) at {host}'s IP. Subscription links keep working once it does."
            ),
            "     Without a domain the links hold this server's IP: change Server address in Settings → General → Server on the new panel, and clients take the new links.".into(),
            "  2. The nodes reach the new panel by themselves. Those with \"API only from the panel\" let it in within 15 minutes.".into(),
        ];
        if self.stop_old {
            l.push("  3. The panel here is stopped; mikan restart starts it again, mikan uninstall removes it.".into());
        } else {
            l.push("  3. The panel here still runs: what changes in it from now on does not come over. Once the new one works, stop it: mikan uninstall (the data stay in /opt/mikan).".into());
        }
        l
    }
}

/// What the move does through: the new server over ssh, this one's backup and containers.
pub trait Runner {
    /// Runs a command line on the new server; its output goes to the terminal, or comes back
    /// when capture is set. input is a file fed to it on stdin.
    fn remote(&mut self, command: &str, input: Option<&Path>, capture: bool) -> Result<String>;
    fn backup(&mut self) -> Result<PathBuf>;
    fn sha256(&mut self, file: &Path) -> Result<String>;
    fn stop_old(&mut self) -> Result<()>;
    fn say(&mut self, line: &str);
}

#[derive(Default)]
struct State {
    sudo: bool,
    backup: Option<PathBuf>,
    copied: bool,
}

impl State {
    fn name(&self) -> Option<&str> {
        self.backup.as_deref().and_then(Path::file_name).and_then(|n| n.to_str())
    }
}

/// The plan's steps in order. One that fails stops the move: the rest is printed as
/// commands to run by hand.
pub fn execute(plan: &Plan, r: &mut dyn Runner) -> Result<()> {
    let steps = plan.steps();
    let mut st = State::default();
    for (i, &step) in steps.iter().enumerate() {
        r.say(&format!("▸ {}", plan.describe(step, st.name())));
        if let Err(e) = run(plan, step, &mut st, r) {
            r.say("");
            if i == 0 {
                r.say("Nothing is changed. The move by hand:");
            } else {
                r.say("The panel here runs as before. The rest by hand:");
            }
            for &s in &steps[i..] {
                for l in plan.by_hand(s, st.name(), st.copied) {
                    r.say(&format!("  {l}"));
                }
            }
            r.say(&format!("All the steps: {GUIDE}"));
            return Err(e);
        }
    }
    r.say("");
    for l in plan.after() {
        r.say(&l);
    }
    Ok(())
}

fn run(plan: &Plan, step: Step, st: &mut State, r: &mut dyn Runner) -> Result<()> {
    let t = &plan.target;
    match step {
        Step::Check => {
            let id = r.remote("id -u", None, true).with_context(|| {
                format!(
                    "ssh to {t} without a password failed: check that `{}` logs in with your key (ssh-agent works) and knows the host key",
                    plan.ssh_hint()
                )
            })?;
            st.sudo = id.trim() != "0";
            let out = r
                .remote(&as_root(st.sudo, PROBE), None, true)
                .with_context(|| format!("{t} needs the user root, or sudo without a password"))?;
            let p = parse_probe(&out);
            if p.node {
                bail!("{t} is a node of a panel: the panel needs a server of its own");
            }
            if p.installed && !plan.force {
                bail!(
                    "{t} already has mikan in /opt/mikan: --force replaces its data with this panel's (a copy of them stays in its /opt/mikan/backups)"
                );
            }
            if !p.curl {
                bail!("{t} has no curl: install it there (apt install curl) and run the move again");
            }
        }
        Step::Backup => {
            let file = r.backup()?;
            let name = file.file_name().and_then(|n| n.to_str()).unwrap_or_default();
            if !valid_backup_name(name) {
                bail!("the backup {} has a name the move does not take", file.display());
            }
            r.say(&format!("  {}", file.display()));
            st.backup = Some(file);
        }
        Step::Install => {
            r.remote(&as_root(st.sudo, &plan.install_script()), None, false).with_context(|| format!("the install on {t} failed"))?;
        }
        Step::Copy => {
            let file = st.backup.clone().context("no backup")?;
            let name = st.name().unwrap_or_default().to_owned();
            let want = r.sha256(&file)?;
            let out = r
                .remote(&as_root(st.sudo, &copy_script(&name)), Some(&file), true)
                .with_context(|| format!("copying the backup to {t} failed"))?;
            let got = out.split_whitespace().next().unwrap_or_default();
            if got != want {
                bail!("the backup on {t} differs from this one (sha256 {got:?}, here {want}): the copy was damaged on the way");
            }
            st.copied = true;
        }
        Step::Restore => {
            let out = r.remote(&as_root(st.sudo, &format!("{REMOTE_MIKAN} --version")), None, true)?;
            let there = out.split_whitespace().nth(1).unwrap_or_default();
            if release::newer(&plan.version, there) {
                bail!(
                    "{t} got mikan {there}, older than this panel's {}: a backup goes onto the same release or a newer one",
                    plan.version
                );
            }
            let name = st.name().unwrap_or_default().to_owned();
            r.remote(&as_root(st.sudo, &format!("{REMOTE_MIKAN} restore -y {REMOTE_BACKUPS}/{name}")), None, false)
                .with_context(|| format!("the restore on {t} failed"))?;
        }
        Step::Firewall => {
            let out = r
                .remote(&as_root(st.sudo, &plan.ufw_script()), None, true)
                .with_context(|| format!("ufw on {t} did not take the rules"))?;
            if out.lines().any(|l| l.trim() == "ufw-off") {
                r.say(&format!(
                    "  ufw is off on {t}: nothing to open there. A firewall in the hoster's panel needs: {}",
                    plan.rules.join(" ")
                ));
            } else {
                r.say(&format!("  opened: {}", plan.rules.join(" ")));
            }
        }
        Step::StopOld => r.stop_old()?,
    }
    Ok(())
}

/// The move through the system's ssh and this server.
struct System {
    target: String,
    ssh_port: Option<u16>,
}

impl System {
    fn ssh(&self) -> Command {
        let mut c = Command::new("ssh");
        // Never a password prompt: the key, the agent or nothing. A dead connection ends
        // in two minutes, a long install with no output keeps it.
        c.args(["-o", "BatchMode=yes", "-o", "ConnectTimeout=20", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=8"]);
        if let Some(p) = self.ssh_port {
            c.args(["-p", &p.to_string()]);
        }
        c.arg("--").arg(&self.target);
        c
    }
}

impl Runner for System {
    fn remote(&mut self, command: &str, input: Option<&Path>, capture: bool) -> Result<String> {
        let mut c = self.ssh();
        c.arg(command);
        c.stdin(match input {
            Some(f) => Stdio::from(File::open(f).with_context(|| format!("open {}", f.display()))?),
            None => Stdio::null(),
        });
        if capture {
            let out = c.stderr(Stdio::inherit()).output().context("ssh")?;
            if !out.status.success() {
                bail!("ssh: exit status {}", out.status.code().unwrap_or(-1));
            }
            return Ok(String::from_utf8_lossy(&out.stdout).into_owned());
        }
        let status = c.status().context("ssh")?;
        if !status.success() {
            bail!("exit status {}", status.code().unwrap_or(-1));
        }
        Ok(String::new())
    }

    fn backup(&mut self) -> Result<PathBuf> {
        crate::backup::backup(&mut crate::out)
    }

    fn sha256(&mut self, file: &Path) -> Result<String> {
        let mut f = File::open(file).with_context(|| format!("open {}", file.display()))?;
        let mut h = Sha256::new();
        let mut buf = vec![0u8; 1 << 16];
        loop {
            let n = f.read(&mut buf)?;
            if n == 0 {
                break;
            }
            h.update(&buf[..n]);
        }
        Ok(h.finalize().iter().map(|x| format!("{x:02x}")).collect())
    }

    fn stop_old(&mut self) -> Result<()> {
        let _lock = lock::acquire(lock::Wait::Block, &mut crate::out)?;
        crate::addon::down();
        docker::compose_run(&["stop"])?;
        Ok(())
    }

    fn say(&mut self, line: &str) {
        crate::out(line);
    }
}

/// `mikan move`.
pub fn run_move(a: &MoveArgs) -> Result<()> {
    if !system::is_root() {
        bail!("run as root: sudo mikan move {}", a.target);
    }
    let plan = Plan::load(a)?;
    if a.dry_run {
        crate::out(&format!("mikan move to {}: the steps (nothing is done with --dry-run)", plan.target));
        for (i, s) in plan.steps().into_iter().enumerate() {
            crate::out(&format!("  {}. {}", i + 1, plan.describe(s, None)));
        }
        return Ok(());
    }
    if !system::succeeds("ssh", &["-V"]) {
        bail!("ssh is missing here: apt install openssh-client");
    }
    let question = format!("Move this panel to {}? It keeps running here until you stop it.", plan.target);
    if !a.yes && !crate::ops::confirm(&question) {
        return Ok(());
    }
    execute(&plan, &mut System { target: plan.target.clone(), ssh_port: plan.ssh_port })
}

#[cfg(test)]
mod tests {
    use super::*;
    use clap::Parser;

    #[derive(Parser)]
    struct Cli {
        #[command(flatten)]
        args: MoveArgs,
    }

    fn args(line: &[&str]) -> MoveArgs {
        Cli::try_parse_from(std::iter::once("move").chain(line.iter().copied())).unwrap().args
    }

    fn plan(line: &[&str]) -> Plan {
        Plan::new(&args(line), 21355, "0.5.2.1".into(), true, vec!["2097/tcp".into(), "20000:20100/udp".into()]).unwrap()
    }

    #[test]
    fn the_flags() {
        let a = args(&["root@203.0.113.7"]);
        assert_eq!(
            (a.target.as_str(), a.ssh_port, a.force, a.stop_old, a.dry_run, a.yes),
            ("root@203.0.113.7", None, false, false, false, false)
        );
        let a = args(&["admin@new", "-p", "2222", "--force", "--stop-old", "--dry-run", "-y"]);
        assert_eq!((a.ssh_port, a.force, a.stop_old, a.dry_run, a.yes), (Some(2222), true, true, true, true));
        assert!(Cli::try_parse_from(["move"]).is_err(), "the target is needed");
        assert!(Cli::try_parse_from(["move", "a", "b"]).is_err());
    }

    #[test]
    fn a_target_is_never_an_option() {
        for ok in ["root@203.0.113.7", "new-server", "admin@panel.example.com", "root@2001:db8::1", "u_1.x@h"] {
            assert!(valid_target(ok), "{ok}");
        }
        for bad in
            ["", "-oProxyCommand=sh", "root@-oProxyCommand=sh", "-l@host", "root@", "@host", "a b", "root@h;id", "root@h$(id)", "root@h\n"]
        {
            assert!(!valid_target(bad), "{bad:?}");
        }
        let a = args(&["--", "-oProxyCommand=x"]);
        assert!(Plan::new(&a, 1, String::new(), true, Vec::new()).is_err());
        let a = args(&["root@h"]);
        assert!(Plan::new(&a, 1, String::new(), true, vec!["22; reboot".into()]).is_err(), "rules go into a shell line");
    }

    #[test]
    fn the_plan() {
        let p = plan(&["root@new"]);
        assert_eq!(p.steps(), [Step::Check, Step::Backup, Step::Install, Step::Copy, Step::Restore, Step::Firewall]);
        let p = plan(&["root@new", "--stop-old"]);
        assert_eq!(p.steps().last(), Some(&Step::StopOld));
        let mut p = plan(&["root@new"]);
        p.rules.clear();
        assert!(!p.steps().contains(&Step::Firewall), "nothing to open, no step");
        assert!(p.install_script().contains("bash \"$f\" --yes --port 21355;"));
        assert!(p.install_script().contains(&format!("https://github.com/{REPO}/releases/latest/download/install.sh")));
        p.firewall = false;
        assert!(p.install_script().contains("--port 21355 --no-firewall"), "ufw left alone here, left alone there");
        let p = plan(&["root@new"]);
        assert_eq!(
            p.ufw_script(),
            "if ufw status 2>/dev/null | grep -q 'Status: active'; then ufw allow 2097/tcp >/dev/null && ufw allow 20000:20100/udp >/dev/null && echo ufw-on; else echo ufw-off; fi"
        );
        assert!(p.describe(Step::Install, None).contains("port 21355"));
    }

    #[test]
    fn quoting_for_sh() {
        assert_eq!(quote("a b"), "'a b'");
        assert_eq!(quote("it's"), r"'it'\''s'");
        assert_eq!(as_root(false, "id"), "sh -c 'id'");
        assert_eq!(as_root(true, "echo 'x'"), r"sudo -n sh -c 'echo '\''x'\'''");
        assert!(valid_backup_name("mikan-20261028-101500.tar.gz"));
        assert!(!valid_backup_name("x;reboot.tar.gz") && !valid_backup_name(".x.tar.gz") && !valid_backup_name("mikan.tar"));
    }

    /// The scripts as the new server's shell reads them: each one parses, and the line ssh
    /// carries gives sh the script as it was.
    #[test]
    fn the_scripts_parse_in_sh() {
        let p = plan(&["root@new"]);
        for s in [PROBE.to_owned(), p.install_script(), p.ufw_script(), copy_script("mikan-1.tar.gz")] {
            let line = as_root(false, &s);
            for check in [&s, &line] {
                let ok = Command::new("sh").args(["-n", "-c", check]).status().unwrap().success();
                assert!(ok, "sh does not take: {check}");
            }
        }
        let out = Command::new("sh").args(["-c", &as_root(false, r#"printf '%s|' "it's" '$HOME' "a b""#)]).output().unwrap();
        assert_eq!(String::from_utf8_lossy(&out.stdout), "it's|$HOME|a b|");
    }

    #[test]
    fn the_probe() {
        assert_eq!(parse_probe(""), Probe { installed: false, node: false, curl: true });
        assert_eq!(parse_probe("installed\nnode\nno-curl\n"), Probe { installed: true, node: true, curl: false });
    }

    /// The new server and this one as the test says they answer.
    struct Fake {
        uid: &'static str,
        probe: &'static str,
        version: &'static str,
        /// The sha256 the new server reports for the copy.
        copy_sum: &'static str,
        ufw: &'static str,
        /// A remote command with this in it fails.
        fail: Option<&'static str>,
        calls: Vec<String>,
        said: Vec<String>,
    }

    const SUM: &str = "5f2b2bbbd2a8a01f41e4fdeb3e0e7a5fca0ea0b2d1e3c96f8ed52f8a1e1b4a10";

    impl Default for Fake {
        fn default() -> Self {
            Self {
                uid: "0\n",
                probe: "",
                version: "mikan 0.5.2.1\n",
                copy_sum: SUM,
                ufw: "ufw-on\n",
                fail: None,
                calls: Vec::new(),
                said: Vec::new(),
            }
        }
    }

    impl Runner for Fake {
        fn remote(&mut self, command: &str, input: Option<&Path>, _capture: bool) -> Result<String> {
            let fed = input.map(|f| format!(" < {}", f.display())).unwrap_or_default();
            self.calls.push(format!("remote {command}{fed}"));
            if self.fail.is_some_and(|f| command.contains(f)) {
                bail!("exit status 1");
            }
            Ok(if command == "id -u" {
                self.uid.into()
            } else if command.contains("command -v curl") {
                self.probe.into()
            } else if command.contains("--version") {
                self.version.into()
            } else if command.contains("sha256sum") {
                format!("{}  /opt/mikan/backups/mikan-1.tar.gz\n", self.copy_sum)
            } else if command.contains("ufw status") {
                self.ufw.into()
            } else {
                String::new()
            })
        }
        fn backup(&mut self) -> Result<PathBuf> {
            self.calls.push("backup".into());
            Ok(PathBuf::from("/opt/mikan/backups/mikan-1.tar.gz"))
        }
        fn sha256(&mut self, _: &Path) -> Result<String> {
            Ok(SUM.into())
        }
        fn stop_old(&mut self) -> Result<()> {
            self.calls.push("stop old".into());
            Ok(())
        }
        fn say(&mut self, line: &str) {
            self.said.push(line.into());
        }
    }

    impl Fake {
        /// Whether the calls were these, each told by its start.
        fn began(&self, starts: &[&str]) -> bool {
            self.calls.len() == starts.len() && self.calls.iter().zip(starts).all(|(c, s)| c.starts_with(s))
        }
        fn said(&self, s: &str) -> bool {
            self.said.iter().any(|l| l.contains(s))
        }
    }

    #[test]
    fn a_move_runs_its_steps_in_order() {
        let mut f = Fake::default();
        execute(&plan(&["root@new", "--stop-old"]), &mut f).unwrap();
        assert!(
            f.began(&[
                "remote id -u",
                "remote sh -c 'if [ -e /opt/mikan/config.json ] ",
                "backup",
                "remote sh -c 'f=$(mktemp) && curl -fsSL --proto",
                "remote sh -c 'umask 077 && mkdir -p /opt/mikan/backups",
                "remote sh -c '/usr/local/bin/mikan --version",
                "remote sh -c '/usr/local/bin/mikan restore -y",
                "remote sh -c 'if ufw status 2>/dev/null | grep",
                "stop old",
            ]),
            "{:#?}",
            f.calls
        );
        assert!(f.calls[4].ends_with("< /opt/mikan/backups/mikan-1.tar.gz"), "the backup goes on stdin: {}", f.calls[4]);
        assert!(f.calls[6].contains("restore -y /opt/mikan/backups/mikan-1.tar.gz"));
        assert!(f.said("opened: 2097/tcp 20000:20100/udp"));
        assert!(f.said("Point the panel's domain") && f.said("within 15 minutes") && f.said("is stopped"));
    }

    #[test]
    fn a_user_not_root_goes_through_sudo() {
        let mut f = Fake { uid: "1000\n", ufw: "ufw-off\n", ..Default::default() };
        execute(&plan(&["admin@new"]), &mut f).unwrap();
        assert!(f.calls[1..].iter().filter(|c| c.starts_with("remote")).all(|c| c.starts_with("remote sudo -n sh -c '")), "{:?}", f.calls);
        assert!(f.said("ufw is off on admin@new"));
        assert!(f.said("still runs"), "the old panel is not stopped without --stop-old");
        assert!(!f.calls.contains(&"stop old".to_owned()));
    }

    #[test]
    fn a_server_with_mikan_is_refused_without_force() {
        let mut f = Fake { probe: "installed\n", ..Default::default() };
        let e = execute(&plan(&["root@new"]), &mut f).unwrap_err();
        assert!(format!("{e:#}").contains("--force"), "{e:#}");
        assert_eq!(f.calls.len(), 2, "nothing is done after the check: {:?}", f.calls);
        assert!(f.said("Nothing is changed"));

        let mut f = Fake { probe: "installed\n", ..Default::default() };
        execute(&plan(&["root@new", "--force"]), &mut f).unwrap();
        assert!(f.calls.iter().any(|c| c.contains("mikan restore")));

        let mut f = Fake { probe: "installed\nnode\n", ..Default::default() };
        assert!(execute(&plan(&["root@new", "--force"]), &mut f).is_err(), "a node never takes a panel");
        let mut f = Fake { probe: "no-curl\n", ..Default::default() };
        assert!(format!("{:#}", execute(&plan(&["root@new"]), &mut f).unwrap_err()).contains("curl"));
    }

    #[test]
    fn a_failed_step_stops_and_tells_the_rest() {
        let mut f = Fake { fail: Some("install.sh"), ..Default::default() };
        let e = execute(&plan(&["root@new", "-p", "2222"]), &mut f).unwrap_err();
        assert!(format!("{e:#}").contains("the install on root@new failed"), "{e:#}");
        assert_eq!(f.calls.len(), 4, "nothing after the install: {:?}", f.calls);
        assert!(f.said("The rest by hand"));
        assert!(f.said("install.sh | sudo bash -s -- --yes --port 21355"));
        assert!(f.said("scp -P 2222 /opt/mikan/backups/mikan-1.tar.gz root@new:"));
        assert!(f.said("sudo mikan restore ~/mikan-1.tar.gz"), "not copied: the restore takes it from the home");
        assert!(f.said("sudo ufw allow 2097/tcp; sudo ufw allow 20000:20100/udp"));
        assert!(f.said(GUIDE));

        let mut f = Fake { fail: Some("mikan restore"), ..Default::default() };
        assert!(execute(&plan(&["root@new"]), &mut f).is_err());
        assert!(f.said("sudo mikan restore /opt/mikan/backups/mikan-1.tar.gz"), "copied: {:?}", f.said);
        assert!(!f.said("scp "), "the copy is done, not repeated");

        let mut f = Fake { fail: Some("id -u"), ..Default::default() };
        let e = execute(&plan(&["root@new", "-p", "2222"]), &mut f).unwrap_err();
        assert!(format!("{e:#}").contains("`ssh -p 2222 root@new` logs in with your key"), "{e:#}");
    }

    #[test]
    fn a_damaged_copy_or_an_older_release_is_not_restored() {
        let mut f = Fake { copy_sum: "0000000000000000000000000000000000000000000000000000000000000000", ..Default::default() };
        let e = execute(&plan(&["root@new"]), &mut f).unwrap_err();
        assert!(format!("{e:#}").contains("damaged"), "{e:#}");
        assert!(!f.calls.iter().any(|c| c.contains("mikan restore")));

        let mut f = Fake { version: "mikan 0.5.2.0\n", ..Default::default() };
        let e = execute(&plan(&["root@new"]), &mut f).unwrap_err();
        assert!(format!("{e:#}").contains("older than this panel's 0.5.2.1"), "{e:#}");
        assert!(!f.calls.iter().any(|c| c.contains("mikan restore")));

        let mut f = Fake { version: "mikan 0.5.3.0\n", ..Default::default() };
        execute(&plan(&["root@new"]), &mut f).unwrap();
    }
}
