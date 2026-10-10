//! The install: what the admin chose (Plan) and the steps that carry it out. The TUI and
//! the plain mode drive the same steps; progress goes out as events.

use std::fs::{self, File};
use std::io::Read;
use std::net::IpAddr;
use std::path::Path;
use std::process::{Command, Stdio};
use std::sync::mpsc::Sender;
use std::thread;
use std::time::{Duration, Instant, SystemTime};

use anyhow::{Context, Result, anyhow, bail};

use crate::envfile::{EnvFile, write_private};
use crate::lock::{self, Wait};
use crate::system::{self, Proto};
use crate::{DIR, acme, docker, hello, host, panelfs, release};

/// Present from the first file written until the last step is done: a server with it and
/// a .env is an install that stopped half way, and `mikan install` continues it.
const UNFINISHED: &str = ".installing";

fn unfinished(root: &Path) -> bool {
    root.join(".env").exists() && root.join(UNFINISHED).exists()
}

/// The flags of `mikan install`; the installer asks for what they leave out.
#[derive(clap::Args, Clone, Debug, Default)]
pub struct Options {
    /// Default language of the panel: en or ru
    #[arg(long, value_parser = ["en", "ru"])]
    pub lang: Option<String>,
    /// Public IPv4 address of the server (detected by default)
    #[arg(long)]
    pub host: Option<String>,
    /// Domain that points to this server, for a Let's Encrypt certificate
    #[arg(long)]
    pub domain: Option<String>,
    /// Email for Let's Encrypt
    #[arg(long)]
    pub email: Option<String>,
    /// Panel port (random from 20000–60000 by default)
    #[arg(long)]
    pub port: Option<u16>,
    /// Leave ufw alone
    #[arg(long)]
    pub no_firewall: bool,
    /// Leave the kernel settings alone (BBR, UDP buffers)
    #[arg(long)]
    pub no_tune: bool,
    /// With a domain and nginx or Caddy on port 80: leave their config alone and print the
    /// rule for Let's Encrypt instead (with --yes it is added, tested and reloaded)
    #[arg(long)]
    pub no_proxy_rule: bool,
    /// Install a node of another panel with the key from its Nodes page. The key holds the
    /// node's private key: MIKAN_JOIN_KEY keeps it out of the process list.
    #[arg(long, value_name = "KEY", env = "MIKAN_JOIN_KEY", hide_env_values = true)]
    pub join: Option<String>,
    /// A node: open its API port in ufw for this address only, the panel's (all by default)
    #[arg(long, value_name = "IP", requires = "join")]
    pub panel_ip: Option<IpAddr>,
    /// An image instead of the latest release, for tests
    #[arg(long, value_name = "IMAGE", conflicts_with = "image_tar")]
    pub image: Option<String>,
    /// An image archive (docker save | gzip) instead of the latest release
    #[arg(long, value_name = "FILE")]
    pub image_tar: Option<String>,
    /// Ask nothing: take the flags and the defaults
    #[arg(long, short = 'y')]
    pub yes: bool,
    /// Replace a Docker without compose v2 (the distribution's docker.io) with Docker from
    /// get.docker.com; images, volumes and containers stay. The interactive installer asks.
    #[arg(long)]
    pub replace_docker: bool,
    /// The old installer's flag; --join is enough now
    #[arg(long, hide = true)]
    pub node: bool,
}

#[derive(Clone, Debug)]
pub struct Plan {
    pub lang: String,
    pub host: String,
    pub domain: String,
    pub email: String,
    pub port: u16,
    pub firewall: bool,
    pub tune: bool,
    pub join: Option<String>,
    /// A node: the panel's address, the only one its API port is opened for.
    pub panel_ip: Option<IpAddr>,
    pub image: Option<String>,
    pub image_tar: Option<String>,
    /// The admin agreed to replace a Docker without compose v2.
    pub replace_docker: bool,
    /// The admin agreed to a rule for Let's Encrypt in the nginx or Caddy on port 80.
    pub proxy_rule: bool,
    /// An earlier install stopped half way and this one continues it: .env says what it
    /// was, and what is running is not in the way.
    pub resume: bool,
}

impl Plan {
    /// The plan the flags make; host stays empty for the caller to detect or ask.
    pub fn from(o: &Options) -> Self {
        // Over SSH the admin's own locale usually comes along: a Russian one suggests Russian.
        let locale =
            ["LC_ALL", "LC_MESSAGES", "LANG"].iter().find_map(|v| std::env::var(v).ok().filter(|s| !s.is_empty())).unwrap_or_default();
        let lang = if locale.starts_with("ru") { "ru" } else { "en" };
        Self {
            lang: o.lang.clone().unwrap_or_else(|| lang.into()),
            host: o.host.clone().unwrap_or_default(),
            domain: o.domain.clone().unwrap_or_default().trim().to_lowercase(),
            email: o.email.clone().unwrap_or_default().trim().to_owned(),
            port: o.port.unwrap_or_else(free_port),
            firewall: !o.no_firewall,
            tune: !o.no_tune,
            join: o.join.clone().map(|k| k.trim().to_owned()),
            panel_ip: o.panel_ip,
            image: o.image.clone(),
            image_tar: o.image_tar.clone(),
            replace_docker: o.replace_docker,
            // Asked in the installer; --yes agrees to it, nothing else does.
            proxy_rule: o.yes && !o.no_proxy_rule,
            resume: false,
        }
    }

    pub fn node(&self) -> bool {
        self.join.is_some()
    }

    /// The plan of an install that stopped: what it chose is in .env (the port, the mode,
    /// the key, the image), and that is what continues.
    fn resumed(mut self, env: &EnvFile) -> Self {
        self.resume = true;
        if let Some(p) = env.get("PANEL_PORT").and_then(|p| p.parse().ok()) {
            self.port = p;
        }
        if env.get("MIKAN_MODE") == Some("node") {
            self.join = env.get("MIKAN_NODE_JOIN").map(str::to_owned).or(self.join);
        }
        self
    }
}

/// A free port for the panel, from 20000–60000.
pub fn free_port() -> u16 {
    loop {
        let mut b = [0u8; 2];
        urandom(&mut b);
        let p = 20000 + u16::from_le_bytes(b) % 40001;
        if system::port_owner(p, Proto::Tcp).is_none() {
            return p;
        }
    }
}

fn urandom(buf: &mut [u8]) {
    File::open("/dev/urandom").and_then(|mut f| f.read_exact(buf)).expect("read /dev/urandom");
}

/// A random string over alphabet, each character equally likely.
pub fn token(n: usize, alphabet: &[u8]) -> String {
    let limit = 256 - 256 % alphabet.len();
    let mut out = String::with_capacity(n);
    while out.len() < n {
        let mut b = [0u8; 64];
        urandom(&mut b);
        for x in b.iter().map(|&x| x as usize).filter(|&x| x < limit) {
            if out.len() < n {
                out.push(alphabet[x % alphabet.len()] as char);
            }
        }
    }
    out
}

const ALNUM: &[u8] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789";
const LOWER: &[u8] = b"abcdefghijklmnopqrstuvwxyz";
const LOWER_DIGITS: &[u8] = b"abcdefghijklmnopqrstuvwxyz0123456789";

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Step {
    Packages,
    Docker,
    Image,
    Files,
    Bootstrap,
    System,
    Start,
}

impl Step {
    pub fn all(node: bool) -> Vec<Step> {
        let mut s = vec![Step::Packages, Step::Docker, Step::Image, Step::Files];
        if !node {
            s.push(Step::Bootstrap);
        }
        s.extend([Step::System, Step::Start]);
        s
    }

    pub fn label(self) -> &'static str {
        match self {
            Step::Packages => "Package manager",
            Step::Docker => "Docker",
            Step::Image => "mikan image",
            Step::Files => "Files in /opt/mikan",
            Step::Bootstrap => "Admin and secret links",
            Step::System => "Network tuning and firewall",
            Step::Start => "Start",
        }
    }
}

pub enum Event {
    Start(Step),
    Progress(Step, f64),
    Note(Step, String),
    Log(String),
    Done(Step),
    Failed(Step, String),
    Finished(Box<Outcome>),
}

#[derive(Clone, Debug)]
pub struct Outcome {
    pub version: String,
    /// A node's API port; None for a panel.
    pub node_port: Option<u16>,
    pub url: String,
    pub login: String,
    pub password: String,
    /// A domain whose port 80 something else holds: what became of Let's Encrypt.
    pub acme: Option<acme::Report>,
    /// A node: how the panel's first contact with it went. None for a panel, and for a
    /// node whose image does not report it.
    pub hello: Option<Vec<(system::Level, String)>>,
}

/// Port 80 of a panel with a domain, when something else holds it.
enum Port80 {
    /// nginx or Caddy, and the local port the panel answers Let's Encrypt on behind it.
    Front(acme::Front, u16),
    Other(String),
}

fn port80(plan: &Plan) -> Option<Port80> {
    if plan.node() || plan.domain.is_empty() {
        return None;
    }
    let who = system::port_owner(80, Proto::Tcp)?;
    // The panel of the attempt before this one.
    if who == "mikan" {
        return None;
    }
    let Some(front) = acme::Front::from_process(&who) else { return Some(Port80::Other(who)) };
    // A continued install keeps the port the attempt before it chose.
    let earlier = plan
        .resume
        .then(|| EnvFile::load(Path::new(DIR).join(".env")).ok())
        .flatten()
        .and_then(|e| e.get("MIKAN_ACME_LISTEN").and_then(acme::listen_port));
    Some(Port80::Front(front, earlier.unwrap_or_else(acme::free_port)))
}

/// Runs the install and reports through tx; the last event is Finished or Failed.
pub fn execute(plan: Plan, tx: Sender<Event>) {
    // Only one change to the server at a time. The install does not wait: whoever holds the
    // lock is an update or a restore of the server this one is about to be made on.
    let held = lock::acquire(Wait::Skip, &mut |_| {});
    let result = match held {
        Ok(Some(_guard)) => {
            // A panic in a step must end the install with a reason, not leave the screen
            // waiting for events that never come.
            std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| run(&plan, &tx))).unwrap_or_else(|panic| {
                let why = panic.downcast_ref::<&str>().map(|s| (*s).to_owned()).or_else(|| panic.downcast_ref::<String>().cloned());
                Err((CURRENT.with(std::cell::Cell::get), anyhow!("the installer stopped unexpectedly: {}", why.unwrap_or_default())))
            })
        }
        Ok(None) => Err((Step::Packages, anyhow!("another mikan operation is running: wait for it to finish and run the installer again"))),
        Err(e) => Err((Step::Packages, e)),
    };
    if let Err((step, e)) = result {
        let _ = tx.send(Event::Failed(step, format!("{e:#}")));
    }
}

type StepResult<T> = std::result::Result<T, (Step, anyhow::Error)>;

thread_local! {
    /// The step that runs, for a panic to be reported at.
    static CURRENT: std::cell::Cell<Step> = const { std::cell::Cell::new(Step::Packages) };
}

fn step<T>(tx: &Sender<Event>, s: Step, f: impl FnOnce() -> Result<T>) -> StepResult<T> {
    CURRENT.with(|c| c.set(s));
    let _ = tx.send(Event::Start(s));
    let v = f().map_err(|e| (s, e))?;
    let _ = tx.send(Event::Done(s));
    Ok(v)
}

fn note(tx: &Sender<Event>, s: Step, text: impl Into<String>) {
    let _ = tx.send(Event::Note(s, text.into()));
}

fn run(plan: &Plan, tx: &Sender<Event>) -> StepResult<()> {
    let node = plan.node();
    step(tx, Step::Packages, || {
        let start = Instant::now();
        let mut shown = String::new();
        while let Some(who) = system::dpkg_holder() {
            if start.elapsed() > Duration::from_secs(600) {
                bail!("the package manager has been busy for 10 minutes, {who}: wait for it to finish and run the installer again");
            }
            if who != shown {
                note(tx, Step::Packages, format!("busy, {who}: waiting for it"));
                shown = who;
            }
            thread::sleep(Duration::from_secs(5));
        }
        if system::dpkg_unfinished() {
            note(tx, Step::Packages, "finishing the hoster's half-done package setup");
            let _ = Command::new("dpkg")
                .args(["--configure", "-a", "--force-confdef", "--force-confold"])
                .env("DEBIAN_FRONTEND", "noninteractive")
                .output();
        }
        note(tx, Step::Packages, "free");
        Ok(())
    })?;

    step(tx, Step::Docker, || {
        let log = |l: &str| {
            let _ = tx.send(Event::Log(l.to_owned()));
        };
        match docker::version() {
            Some(v) if docker::compose_ok() => {
                note(tx, Step::Docker, format!("Docker {v}"));
                return Ok(());
            }
            Some(v) => {
                // The distribution's Docker (Ubuntu 22.04's docker.io 24.0.7) has no compose v2.
                if !plan.replace_docker {
                    bail!("Docker {v} has no compose v2: agree to replace it in the installer, or add --replace-docker");
                }
                note(tx, Step::Docker, format!("Docker {v} has no compose v2: replacing it from get.docker.com"));
                docker::replace(log)?;
            }
            None => {
                note(tx, Step::Docker, "installing from get.docker.com");
                docker::install(log)?;
            }
        }
        note(tx, Step::Docker, format!("Docker {} installed", docker::version().unwrap_or_default()));
        Ok(())
    })?;

    let (image, version) = step(tx, Step::Image, || {
        let progress = |p| {
            let _ = tx.send(Event::Progress(Step::Image, p));
        };
        let earlier = plan
            .resume
            .then(|| EnvFile::load(Path::new(DIR).join(".env")).ok())
            .flatten()
            .and_then(|e| e.get("MIKAN_IMAGE").map(str::to_owned));
        let image = if let Some(tar) = &plan.image_tar {
            note(tx, Step::Image, format!("loading {tar}"));
            docker::load(tar)?
        } else if let Some(image) = earlier.filter(|i| plan.image.is_none() && docker::image_present(i)) {
            note(tx, Step::Image, format!("{image}, from the earlier attempt"));
            image
        } else if let Some(image) = &plan.image {
            note(tx, Step::Image, format!("pulling {image}"));
            docker::pull(image, progress)?;
            image.clone()
        } else {
            let m = release::latest()?;
            note(tx, Step::Image, format!("mikan {}, signed release", m.version));
            docker::pull(&m.reference(), progress)?;
            m.reference()
        };
        let version = image_version(&image)?;
        note(tx, Step::Image, format!("mikan {version}"));
        if !node {
            note(tx, Step::Image, "PostgreSQL 18");
            docker::pull_postgres(progress)?;
        }
        Ok((image, version))
    })?;

    let port80 = port80(plan);
    let acme_port = match &port80 {
        Some(Port80::Front(_, p)) => Some(*p),
        _ => None,
    };
    let api_port = step(tx, Step::Files, || {
        let api_port = match &plan.join {
            Some(key) => {
                let p = node_port(&image, key)?;
                // On a continued install the node's own containers may hold the port.
                if !plan.resume
                    && let Some(who) = system::port_owner(p, Proto::Tcp)
                {
                    bail!("port {p}, where the panel will reach the node, is taken ({who})");
                }
                Some(p)
            }
            None => None,
        };
        // An uninstall keeps the database's volume; its password went with the .env, and a
        // new one does not open it. The data are never removed for the admin.
        if !node && !plan.resume && docker::volume_mountpoint(docker::PG_VOLUME).is_some() {
            bail!(
                "the Docker volume {v} holds the database of an earlier mikan, whose password was in the .env that is gone, so this install cannot open it. To keep those data, put the old .env and compose.yaml back in {DIR} (every mikan backup archive has them) and run the installer again; if they are not needed: docker volume rm {v}, then run the installer again",
                v = docker::PG_VOLUME
            );
        }
        write_files(plan, &image, &version, api_port, acme_port)?;
        note(tx, Step::Files, DIR);
        Ok(api_port)
    })?;

    let creds = if node {
        None
    } else {
        Some(step(tx, Step::Bootstrap, || {
            docker::postgres_ready()?;
            docker::database(&["migrate"])?;
            bootstrap(plan, tx)
        })?)
    };
    // What the admin must not lose: the password is shown once, and it exists from here on.
    let kept = |(s, e): (Step, anyhow::Error)| match creds.as_ref().and_then(|c| c.password.as_ref().map(|p| (c, p))) {
        Some((c, p)) => (
            s,
            anyhow!(
                "{e:#}\n\nThe admin was created before this: login {}, password {p} (shown once, keep it). The install continues where it stopped: mikan install",
                c.login
            ),
        ),
        None => (s, e),
    };

    let acme = step(tx, Step::System, || {
        let mut done = Vec::new();
        if plan.tune {
            done.push(match host::tune() {
                Ok(()) => "BBR on".to_string(),
                Err(e) => format!("no BBR ({e})"),
            });
        }
        if plan.firewall {
            let panel_port = (!node).then_some(plan.port);
            // A node's API port may be opened for its panel's address alone.
            let open_api = api_port.filter(|_| plan.panel_ip.is_none());
            let opened = host::open(&host::rules(panel_port, open_api)).and_then(|on| match (on, api_port, plan.panel_ip) {
                (true, Some(p), Some(ip)) => host::allow_from(ip, p).map(|()| true),
                _ => Ok(on),
            });
            // A firewall that does not take a rule is a warning, not the end of the install.
            done.push(match opened {
                Ok(true) => "ports open in ufw".to_string(),
                Ok(false) => "ufw is off".to_string(),
                Err(e) => format!("ufw did not take the rules ({e}): open the ports yourself"),
            });
        }
        host::install_self()?;
        done.push(match host::install_units(!node) {
            Ok(()) => "daily update check".into(),
            Err(e) => format!("no update timer ({e})"),
        });
        // Before the panel starts, so that its first try at the certificate gets through.
        let acme = match &port80 {
            Some(Port80::Front(front, port)) => Some(acme::setup(*front, &plan.domain, *port, plan.proxy_rule)),
            Some(Port80::Other(who)) => Some(acme::Report::other(who)),
            None => None,
        };
        done.extend(acme.as_ref().map(acme::Report::note));
        note(tx, Step::System, done.join(" · "));
        Ok(acme)
    })
    .map_err(kept)?;

    let hello = step(tx, Step::Start, || {
        let since = SystemTime::now();
        docker::compose_run(&["up", "-d"])?;
        wait_ready(api_port, Duration::from_secs(90))?;
        note(tx, Step::Start, "running");
        // A node tells how its first contact with the panel went; a panel has no one to
        // wait for. What it says never fails the install.
        let Some(port) = api_port else { return Ok(None) };
        note(tx, Step::Start, "waiting for the panel to connect");
        Ok(hello::wait(since, hello::LIMIT).map(|h| hello::lines(&h, Some(port))))
    })
    .map_err(kept)?;
    // Everything is done: the install is not unfinished any more.
    let _ = fs::remove_file(Path::new(DIR).join(UNFINISHED));

    let outcome = match creds {
        Some(c) => Outcome { version, node_port: None, url: c.url, login: c.login, password: c.password.unwrap_or_default(), acme, hello },
        None => Outcome { version, node_port: api_port, url: String::new(), login: String::new(), password: String::new(), acme, hello },
    };
    let _ = tx.send(Event::Finished(Box::new(outcome)));
    Ok(())
}

/// The panel's first admin. The password goes in on stdin and nothing secret on the command
/// line; the secret paths are the panel's own to choose and are read back from `admin url`.
/// An install that is continued finds the admin made already and does not make another.
fn bootstrap(plan: &Plan, tx: &Sender<Event>) -> Result<Credentials> {
    let known = docker::admin_once(&["url"], None)?;
    if known.status.success() {
        let c = credentials_from_url(&known, None);
        note(tx, Step::Bootstrap, "the admin exists from the earlier attempt: a new password with mikan reset-password");
        return Ok(c);
    }
    let login = token(1, LOWER) + &token(11, LOWER_DIGITS);
    let password = token(32, ALNUM);
    let port = plan.port.to_string();
    let mut args = vec!["bootstrap", "--public-host", plan.host.as_str(), "--port", port.as_str(), "--username", login.as_str()];
    args.extend(["--password-stdin", "--lang", plan.lang.as_str()]);
    if !plan.domain.is_empty() {
        args.extend(["--domain", plan.domain.as_str()]);
    }
    if !plan.email.is_empty() {
        args.extend(["--email", plan.email.as_str()]);
    }
    docker::check(docker::admin_once(&args, Some(&format!("{password}\n")))?)?;
    let url = docker::check(docker::admin_once(&["url"], None)?).map(|o| credentials_from_url(&o, Some(password.clone())));
    // The admin exists, so the password must not be lost to a failed read of the URL.
    let c = url.unwrap_or(Credentials { url: String::new(), login: login.clone(), password: Some(password) });
    note(tx, Step::Bootstrap, format!("login {}", c.login));
    Ok(c)
}

/// What `mikan admin url` printed: the address on stdout, "Login: x" on stderr.
fn credentials_from_url(out: &std::process::Output, password: Option<String>) -> Credentials {
    let url = String::from_utf8_lossy(&out.stdout).lines().rev().find(|l| l.starts_with("https://")).unwrap_or_default().trim().to_owned();
    let login = String::from_utf8_lossy(&out.stderr).lines().find_map(|l| l.strip_prefix("Login: ")).unwrap_or_default().trim().to_owned();
    Credentials { url, login, password }
}

struct Credentials {
    url: String,
    login: String,
    /// None when the admin was made by an earlier attempt: the password is not known.
    password: Option<String>,
}

/// How an image is run before the server has decided to use it: it is code from a
/// registry, so no network, no capabilities but the one its binary needs, a read-only root,
/// the panel's user and small limits.
const SANDBOX: [&str; 17] = [
    "run",
    "--rm",
    "--network",
    "none",
    "--cap-drop",
    "ALL",
    // The image's binary carries a file capability (it binds low ports): with an empty
    // bounding set the kernel refuses to run it at all (the compose files do the same).
    "--cap-add",
    "NET_BIND_SERVICE",
    "--security-opt",
    "no-new-privileges:true",
    "--read-only",
    "--user",
    "65532:65532",
    "--pids-limit",
    "64",
    "--memory",
    "128m",
];

fn sandboxed() -> Command {
    let mut cmd = Command::new("docker");
    cmd.args(SANDBOX).stdin(Stdio::null());
    cmd
}

/// The version the image reports.
pub fn image_version(image: &str) -> Result<String> {
    let out = sandboxed().args([image, "version"]).output()?;
    let v = String::from_utf8_lossy(&out.stdout).trim().to_owned();
    if !out.status.success() || v.is_empty() {
        bail!("the image does not start: {}", String::from_utf8_lossy(&out.stderr).trim());
    }
    Ok(v)
}

/// The image checks a node's join key and names the port the panel will connect to. The
/// key goes through the environment, not the command line.
pub fn node_port(image: &str, key: &str) -> Result<u16> {
    let out = sandboxed()
        .env("MIKAN_NODE_JOIN", key)
        .args(["-e", "MIKAN_NODE_JOIN", "--entrypoint", "/usr/local/bin/mikan-node", image, "key-port"])
        .output()?;
    let port = String::from_utf8_lossy(&out.stdout).trim().parse().ok();
    match port {
        Some(p) if out.status.success() => Ok(p),
        _ => bail!("the join key does not fit: copy it from the panel's Nodes page again"),
    }
}

fn write_files(plan: &Plan, image: &str, version: &str, api_port: Option<u16>, acme_port: Option<u16>) -> Result<()> {
    let root = Path::new(DIR);
    write_files_in(root, plan, image, version, api_port, acme_port)?;
    // The containers run as 65532 and each mounts its own directory of data/, which a bind
    // mount does not give that owner; data/ itself stays root's (the compose file written
    // above mounts nothing else).
    panelfs::own_dirs(root, !plan.node())?;
    panelfs::seal(root)
}

/// The files of an install. A first install makes them; a continued one finds .env there
/// and keeps what it chose, writing only what is missing or has changed. acme_port is
/// where the panel answers Let's Encrypt when a web server holds port 80.
fn write_files_in(root: &Path, plan: &Plan, image: &str, version: &str, api_port: Option<u16>, acme_port: Option<u16>) -> Result<()> {
    use std::os::unix::fs::{DirBuilderExt, PermissionsExt};
    let env_path = root.join(".env");
    if env_path.exists() && !(plan.resume && unfinished(root)) {
        bail!("mikan is already installed in {}", root.display());
    }
    fs::create_dir_all(root)?;
    // Before the first file, so that an install that stops from here on is known as such.
    write_private(&root.join(UNFINISHED), b"an install of mikan did not finish: run `mikan install` to continue\n")?;
    match fs::DirBuilder::new().mode(0o700).create(root.join("backups")) {
        Ok(()) => {}
        Err(e) if e.kind() == std::io::ErrorKind::AlreadyExists => {
            fs::set_permissions(root.join("backups"), fs::Permissions::from_mode(0o700))?
        }
        Err(e) => return Err(e.into()),
    }
    let mut env = if plan.resume { EnvFile::load(&env_path)? } else { EnvFile::new(&env_path) };
    env.set("MIKAN_IMAGE", image)?;
    env.set("MIKAN_VERSION", version)?;
    env.set("MIKAN_UFW", if plan.firewall { "1" } else { "0" })?;
    let node = match (&plan.join, api_port) {
        (Some(key), Some(port)) => {
            env.set("MIKAN_MODE", "node")?;
            env.set("NODE_API_PORT", &port.to_string())?;
            env.set("MIKAN_NODE_JOIN", key)?;
            true
        }
        _ => {
            env.set("PANEL_PORT", &plan.port.to_string())?;
            match acme_port {
                Some(p) => env.set("MIKAN_ACME_LISTEN", &acme::listen(p))?,
                None => env.remove("MIKAN_ACME_LISTEN"),
            }
            false
        }
    };
    if plan.resume {
        docker::ensure_compose(root, node)?;
    } else {
        write_private(&root.join("compose.yaml"), docker::compose_text(node).as_bytes())?;
    }
    if !node {
        postgres_env(&mut env)?;
    }
    env.save()
}

/// Credentials persist across updates and resumed installs; never rotate a live database.
pub fn postgres_env(env: &mut EnvFile) -> Result<()> {
    if env.get("MIKAN_POSTGRES_PASSWORD").is_none() {
        env.set("MIKAN_POSTGRES_PASSWORD", &token(48, ALNUM))?;
    }
    if env.get("MIKAN_DATABASE_URL").is_none() {
        let password = env.get("MIKAN_POSTGRES_PASSWORD").context("postgres password missing")?;
        let url = format!("postgresql://mikan:{password}@localhost/mikan?host=/run/postgresql");
        env.set("MIKAN_DATABASE_URL", &url)?;
    }
    Ok(())
}

/// Waits for the panel to answer, or for a node's API port to listen.
pub fn wait_ready(node_port: Option<u16>, limit: Duration) -> Result<()> {
    let start = Instant::now();
    loop {
        let ready = match node_port {
            Some(p) => system::port_owner(p, Proto::Tcp).is_some(),
            None => docker::panel_healthy(),
        };
        if ready {
            return Ok(());
        }
        if start.elapsed() > limit {
            let logs = docker::compose(&["logs", "--tail", "30"])
                .output()
                .map(|o| String::from_utf8_lossy(&o.stdout).into_owned())
                .unwrap_or_default();
            return Err(anyhow!("mikan did not start in {} s. Its last logs:\n{}", limit.as_secs(), logs.trim_end()));
        }
        thread::sleep(Duration::from_secs(1));
    }
}

/// What a finished install tells the admin, for the terminal: the link and the login whole,
/// for copying from any terminal. The installer's last screen shows the password; without
/// with_password it stays out of the terminal's scrollback.
pub fn summary(o: &Outcome, with_password: bool) -> String {
    let mut text = match o.node_port {
        Some(p) => {
            // An image that does not report says nothing: the panel is only expected.
            let contact = match &o.hello {
                Some(lines) => hello::render(lines).join("\n"),
                None => "The panel connects within 30 seconds: see its Nodes page.".to_owned(),
            };
            format!(
                "mikan {} node is running and waits for its panel on port {p}.\n{contact}\nCommands on this server: mikan (menu), mikan status, mikan doctor, mikan update",
                o.version
            )
        }
        None => {
            let password = if o.password.is_empty() {
                "not known: the admin was made by the attempt before this one; set a new password with: mikan reset-password".to_owned()
            } else if with_password {
                format!("{}   ← shown once, keep it in a password manager", o.password)
            } else {
                "shown on the installer's last screen only; a new one: mikan reset-password".to_owned()
            };
            format!(
                "mikan {} is running.\n\n  Panel     {}\n  Login     {}\n  Password  {password}\n\nCommands on this server: mikan (menu), mikan status, mikan doctor, mikan update",
                o.version, o.url, o.login
            )
        }
    };
    if let Some(t) = o.acme.as_ref().and_then(acme::Report::text) {
        text.push_str("\n\n");
        text.push_str(&t);
    }
    text
}

/// Whether an install stopped half way here: `mikan install` continues it.
pub fn unfinished_install() -> bool {
    unfinished(Path::new(DIR))
}

/// `mikan install`: the TUI in a terminal, the plain mode with --yes or without one. On
/// an installed server it opens the menu, or with --yes updates: the one-line install
/// is also how a server of 0.3.8 and before moves to this installer. An install that
/// stopped half way is continued, in the plain mode, from what its .env says.
pub fn install(opts: Options) -> Result<()> {
    let root = Path::new(DIR);
    if unfinished(root) {
        crate::out("An earlier install did not finish: continuing it.");
        return plain(opts, Some(EnvFile::load(root.join(".env"))?));
    }
    if root.join(".env").exists() {
        if crate::tui::interactive() && !opts.yes {
            return crate::tui::menu();
        }
        crate::out(&format!("mikan is already installed in {DIR}: updating it."));
        let args = crate::update::UpdateArgs { target: opts.image_tar.or(opts.image), ..Default::default() };
        return crate::update::update(&args, &mut crate::out, &mut |_| {});
    }
    if crate::tui::interactive() && !opts.yes {
        return crate::tui::wizard(opts);
    }
    plain(opts, None)
}

/// The install without the TUI: every step as a line.
fn plain(opts: Options, earlier: Option<EnvFile>) -> Result<()> {
    let mut plan = Plan::from(&opts);
    if let Some(env) = &earlier {
        plan = plan.resumed(env);
    }
    let checks = system::checks_for(plan.node(), plan.resume);
    for c in &checks {
        crate::out(&format!("{} {:<16} {}", mark(c.level), c.label, c.detail));
    }
    if checks.iter().any(|c| c.level == system::Level::Error) {
        bail!("fix the problems above and run the installer again");
    }
    if system::docker_to_replace(&checks).is_some() && !plan.replace_docker {
        bail!("this Docker has no compose v2: add --replace-docker to replace it from get.docker.com (images and containers stay)");
    }
    if !plan.node() {
        if plan.host.is_empty() {
            plan.host = crate::net::public_ipv4().context("cannot tell the server's IP: pass --host")?.to_string();
        }
        crate::out(&format!("Server address: {}", plan.host));
        if !plan.domain.is_empty() {
            if !crate::net::valid_domain(&plan.domain) {
                bail!("--domain {:?} is not a domain name", plan.domain);
            }
            let ip = plan.host.parse().context("--domain needs --host to be the server's IPv4 address")?;
            let check = crate::net::check_domain(&plan.domain, ip);
            for n in &check.notes {
                crate::out(&format!("{} {}", mark(n.level), n.text));
            }
            if check.level() == system::Level::Error {
                bail!("the domain does not lead to this server; fix its DNS or install without --domain");
            }
        }
        if !plan.email.is_empty() && !crate::net::valid_email(&plan.email) {
            bail!("--email {:?} is not an email address", plan.email);
        }
    }
    let (tx, rx) = std::sync::mpsc::channel();
    let p = plan.clone();
    thread::spawn(move || execute(p, tx));
    let mut last = -1i64;
    for ev in rx {
        match ev {
            Event::Start(s) => crate::out(&format!("▸ {}", s.label())),
            Event::Note(_, t) => crate::out(&format!("  {t}")),
            Event::Progress(_, p) => {
                let pct = (p * 100.0) as i64;
                if pct / 25 != last / 25 {
                    crate::out(&format!("  {pct}%"));
                    last = pct;
                }
            }
            Event::Log(_) | Event::Done(_) => {}
            Event::Failed(s, e) => bail!("{}: {e}", s.label()),
            Event::Finished(o) => {
                if o.node_port.is_none() {
                    crate::sites::auto(crate::out);
                }
                // Plain mode has no screen to show the password on: the terminal is the place.
                crate::out(&format!("\n{}", summary(&o, true)));
                return Ok(());
            }
        }
    }
    bail!("the install stopped")
}

pub fn mark(l: system::Level) -> &'static str {
    match l {
        system::Level::Ok => "✓",
        system::Level::Warn => "!",
        system::Level::Error => "✗",
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn tokens() {
        let t = token(32, ALNUM);
        assert_eq!(t.len(), 32);
        assert!(t.bytes().all(|b| b.is_ascii_alphanumeric()));
        assert_ne!(t, token(32, ALNUM));
        let login = token(1, LOWER) + &token(11, LOWER_DIGITS);
        assert!(login.as_bytes()[0].is_ascii_lowercase() && login.len() == 12);
        let p = free_port();
        assert!((20000..=60000).contains(&p));
    }

    #[test]
    fn postgres_credentials_survive_resumes_and_updates() {
        let mut env = EnvFile::new("unused");
        postgres_env(&mut env).unwrap();
        let before = env.render();
        let password = env.get("MIKAN_POSTGRES_PASSWORD").unwrap();
        assert_eq!(password.len(), 48);
        assert!(password.bytes().all(|b| b.is_ascii_alphanumeric()));
        assert_eq!(
            env.get("MIKAN_DATABASE_URL"),
            Some(format!("postgresql://mikan:{password}@localhost/mikan?host=/run/postgresql").as_str())
        );
        postgres_env(&mut env).unwrap();
        assert_eq!(env.render(), before, "live credentials must never rotate");
    }

    #[test]
    fn steps() {
        assert_eq!(Step::all(false).len(), 7);
        assert!(!Step::all(true).contains(&Step::Bootstrap));
    }

    // An image from a registry runs with nothing it does not need; the one capability is
    // what lets its setcap'd binary start (found by running it: without, exec fails).
    #[test]
    fn registry_images_run_in_a_sandbox() {
        let has = |pair: [&str; 2]| SANDBOX.windows(2).any(|w| w == pair);
        for pair in [
            ["--network", "none"],
            ["--cap-drop", "ALL"],
            ["--cap-add", "NET_BIND_SERVICE"],
            ["--user", "65532:65532"],
            ["--security-opt", "no-new-privileges:true"],
        ] {
            assert!(has(pair), "{pair:?}");
        }
        assert!(SANDBOX.contains(&"--read-only") && SANDBOX.contains(&"--rm"));
        assert!(SANDBOX.contains(&"--pids-limit") && SANDBOX.contains(&"--memory"));
        assert!(!SANDBOX.contains(&"--privileged"));
        let cmd = format!("{:?}", sandboxed());
        assert!(cmd.starts_with("\"docker\" \"run\""), "{cmd}");
    }

    fn tmpdir(name: &str) -> std::path::PathBuf {
        let d = std::env::temp_dir().join(format!("setup-{name}-{}", std::process::id()));
        let _ = fs::remove_dir_all(&d);
        fs::create_dir_all(&d).unwrap();
        d
    }

    fn plan() -> Plan {
        Plan::from(&Options { port: Some(21355), host: Some("203.0.113.7".into()), ..Default::default() })
    }

    // A first install leaves the marker of an unfinished one until the last step removes
    // it; a second `mikan install` on such a server continues with what .env chose.
    #[test]
    fn an_install_that_stopped_is_continued_not_refused_and_not_redone() {
        let root = tmpdir("resume");
        let mut p = plan();
        // nginx holds port 80: the panel answers Let's Encrypt on a local port behind it
        write_files_in(&root, &p, "ghcr.io/miroshka000/mikan@sha256:aa", "0.4.4", None, Some(18080)).unwrap();
        assert!(unfinished(&root), "the marker is there from the first file");
        let env = EnvFile::load(root.join(".env")).unwrap();
        assert_eq!(env.get("PANEL_PORT"), Some("21355"));
        assert_eq!(env.get("MIKAN_ACME_LISTEN"), Some("127.0.0.1:18080"));
        assert!(docker::PANEL_COMPOSE.contains("MIKAN_ACME_LISTEN: \"${MIKAN_ACME_LISTEN:-:80}\""), "compose passes it on");
        assert_eq!(fs::read_to_string(root.join("compose.yaml")).unwrap(), docker::PANEL_COMPOSE);
        // a fresh install over it is refused
        assert!(write_files_in(&root, &p, "x", "0.4.4", None, None).is_err(), "an install does not overwrite another");
        // the continued one takes the port from .env, whatever it would have drawn
        p = Plan::from(&Options { port: Some(30000), ..Default::default() }).resumed(&env);
        assert!(p.resume);
        assert_eq!(p.port, 21355);
        write_files_in(&root, &p, "ghcr.io/miroshka000/mikan@sha256:bb", "0.4.5", None, None).unwrap();
        let again = EnvFile::load(root.join(".env")).unwrap();
        assert_eq!((again.get("PANEL_PORT"), again.get("MIKAN_VERSION")), (Some("21355"), Some("0.4.5")));
        assert_eq!(again.get("MIKAN_ACME_LISTEN"), None, "port 80 is free now: the panel takes it");
        // done: the last step removes the marker, and the server is an installed one
        fs::remove_file(root.join(UNFINISHED)).unwrap();
        assert!(!unfinished(&root));
        assert!(write_files_in(&root, &p, "x", "0.4.4", None, None).is_err(), "an installed server is not continued");
        fs::remove_dir_all(&root).unwrap();
    }

    #[test]
    fn a_node_install_continues_in_node_mode() {
        let root = tmpdir("resume-node");
        let key = "mikan1.AbC_-9";
        let p = Plan::from(&Options { join: Some(key.into()), ..Default::default() });
        write_files_in(&root, &p, "ghcr.io/miroshka000/mikan", "0.4.4", Some(25305), None).unwrap();
        let env = EnvFile::load(root.join(".env")).unwrap();
        assert_eq!((env.get("MIKAN_MODE"), env.get("NODE_API_PORT"), env.get("MIKAN_NODE_JOIN")), (Some("node"), Some("25305"), Some(key)));
        assert_eq!(fs::read_to_string(root.join("compose.yaml")).unwrap(), docker::NODE_COMPOSE);
        let resumed = Plan::from(&Options::default()).resumed(&env);
        assert!(resumed.node() && resumed.join.as_deref() == Some(key));
        fs::remove_dir_all(&root).unwrap();
    }

    // A password that exists only in memory must not be lost to a failure after it: the
    // summary of a continued install says why there is none.
    #[test]
    fn the_summary_tells_a_lost_password_from_a_shown_one() {
        let shown = Outcome {
            version: "0.4.4".into(),
            node_port: None,
            url: "https://h:1/x/".into(),
            login: "l".into(),
            password: "p4ssw0rd".into(),
            acme: None,
            hello: None,
        };
        assert!(summary(&shown, true).contains("Password  p4ssw0rd"));
        // after the installer's screen the terminal gets the link and login, not the password
        let after_tui = summary(&shown, false);
        assert!(after_tui.contains("  Panel     https://h:1/x/\n  Login     l\n"), "no link and login after the screen");
        assert!(!after_tui.contains("p4ssw0rd") && after_tui.contains("last screen only"));
        let lost = Outcome { password: String::new(), ..shown.clone() };
        assert!(summary(&lost, true).contains("mikan reset-password") && !summary(&lost, true).contains("shown once"));
        // a rule for Let's Encrypt left to the admin comes last
        let report = acme::Report::other("apache2");
        let with_acme = summary(&Outcome { acme: Some(report.clone()), ..shown }, false);
        assert!(with_acme.ends_with(&report.text().unwrap()), "the rule is not last");
    }

    // A node says how its panel's first contact went; an old image says nothing, and the
    // summary then only expects the panel.
    #[test]
    fn the_summary_of_a_node_tells_how_the_panel_connected() {
        let silent = Outcome {
            version: "0.5.0.6".into(),
            node_port: Some(31234),
            url: String::new(),
            login: String::new(),
            password: String::new(),
            acme: None,
            hello: None,
        };
        assert!(summary(&silent, true).contains("The panel connects within 30 seconds"));
        let ok = Outcome { hello: Some(vec![(system::Level::Ok, "The panel connected to this node.".into())]), ..silent.clone() };
        let text = summary(&ok, true);
        assert!(
            text.contains("\n✓ The panel connected to this node.\n") && !text.contains("within 30 seconds"),
            "the summary does not tell the panel connected"
        );
        let blocked = Outcome { hello: Some(vec![(system::Level::Error, "The panel could not reach port 31234 here".into())]), ..silent };
        assert!(summary(&blocked, true).contains("\n✗ The panel could not reach port 31234 here\n"));
    }

    #[test]
    fn the_url_and_login_come_from_admin_url() {
        use std::os::unix::process::ExitStatusExt;
        let out = std::process::Output {
            status: std::process::ExitStatus::from_raw(0),
            stdout: b"https://203.0.113.7:21355/AbCdEf/\n".to_vec(),
            stderr: b"Login: abc123\n".to_vec(),
        };
        let c = credentials_from_url(&out, Some("pw".into()));
        assert_eq!((c.url.as_str(), c.login.as_str(), c.password.as_deref()), ("https://203.0.113.7:21355/AbCdEf/", "abc123", Some("pw")));
    }
}
