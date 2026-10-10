//! Let's Encrypt behind a web server. The panel answers the HTTP-01 challenge of its
//! domain on port 80; when nginx or Caddy holds that port, the panel answers on a local
//! port instead (MIKAN_ACME_LISTEN) and the web server passes the challenge there. With
//! the admin's consent the installer adds that rule itself: the config is backed up, the
//! web server tests it, and a reload follows only a passed test. Anything else puts the old
//! config back and leaves the rule for the admin to add, shown on the finish screen.

use std::fs;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::time::{SystemTime, UNIX_EPOCH};

use crate::system::{self, Proto};

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Front {
    Nginx,
    Caddy,
}

impl Front {
    pub fn name(self) -> &'static str {
        match self {
            Front::Nginx => "nginx",
            Front::Caddy => "Caddy",
        }
    }

    pub fn from_process(name: &str) -> Option<Self> {
        match name {
            "nginx" => Some(Front::Nginx),
            "caddy" => Some(Front::Caddy),
            _ => None,
        }
    }
}

/// The web server on port 80 the installer can pass the challenge through; None when the
/// port is free or something else holds it.
pub fn front() -> Option<Front> {
    system::port_owner(80, Proto::Tcp).and_then(|who| Front::from_process(&who))
}

/// The local port the panel answers the challenge on: the first free one from 18080.
pub fn free_port() -> u16 {
    (18080..18180).find(|&p| system::port_owner(p, Proto::Tcp).is_none()).unwrap_or(18080)
}

/// What MIKAN_ACME_LISTEN is set to for port.
pub fn listen(port: u16) -> String {
    format!("127.0.0.1:{port}")
}

/// The port of a MIKAN_ACME_LISTEN value the installer wrote.
pub fn listen_port(value: &str) -> Option<u16> {
    value.strip_prefix("127.0.0.1:")?.parse().ok()
}

/// What `mikan cert proxy` does about port 80 on an installed panel or node.
#[derive(Debug, PartialEq, Eq)]
pub enum Port80 {
    /// Port 80 is mikan's: free between challenges, or mikan's own. A MIKAN_ACME_LISTEN
    /// left from a web server that has gone goes too (drop).
    Own { drop: bool },
    /// nginx or Caddy holds it: the rule passes the challenge to mikan on port.
    Front(Front, u16),
    /// Something mikan cannot configure holds it.
    Other(String),
}

/// What to do about port 80, from who holds it (`ss`), MIKAN_ACME_LISTEN in .env and a
/// free local port for a new rule. A port chosen before is kept: a rule may point there.
pub fn port80(owner: Option<&str>, listen: Option<&str>, free: impl FnOnce() -> u16) -> Port80 {
    let set = listen.map(str::trim).filter(|l| !l.is_empty() && *l != ":80");
    match owner {
        None | Some("mikan" | "mikan-node") => Port80::Own { drop: set.is_some() },
        Some(who) => match Front::from_process(who) {
            Some(front) => Port80::Front(front, set.and_then(listen_port).unwrap_or_else(free)),
            None => Port80::Other(who.to_owned()),
        },
    }
}

/// What to add by hand when the admin keeps the web server's config to themselves: the
/// block, where it goes, and the line of .env that makes mikan answer behind it.
pub fn manual_text(front: Front, domain: &str, port: u16) -> String {
    let (place, block) = match front {
        Front::Nginx => (
            "as a new file in nginx's config (conf.d or sites-enabled), then: nginx -t && nginx -s reload",
            nginx_server(domain, port, false),
        ),
        Front::Caddy => ("at the end of the Caddyfile, then: caddy reload", caddy_site(domain, port)),
    };
    format!(
        "Add this {place}

{block}
Then set MIKAN_ACME_LISTEN={} in {}/.env and run: mikan restart",
        listen(port),
        crate::DIR
    )
}

/// What became of the rule.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Report {
    /// Who holds port 80: nginx, Caddy or the process ss names.
    pub front: String,
    /// The file the rule went into, live after a reload.
    pub added: Option<PathBuf>,
    /// What is left for the admin.
    pub manual: Option<Manual>,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Manual {
    /// Why the installer did not add it.
    pub why: String,
    /// Where it goes, as the end of "Add this …".
    pub place: String,
    pub snippet: String,
}

impl Report {
    /// Port 80 held by something the installer does not know how to configure.
    pub fn other(who: &str) -> Self {
        Report {
            front: who.to_owned(),
            added: None,
            manual: Some(Manual {
                why: format!("port 80 is taken ({who}), and Let's Encrypt checks the domain there"),
                place: String::new(),
                snippet: String::new(),
            }),
        }
    }

    /// One line for the install's step.
    pub fn note(&self) -> String {
        match (&self.added, &self.manual) {
            (Some(file), _) => format!("{} passes Let's Encrypt to mikan ({})", self.front, file.display()),
            _ if self.front_known() => format!("{} holds port 80: add its rule for Let's Encrypt (shown at the end)", self.front),
            _ => format!("port 80 is taken ({}): no Let's Encrypt until it is free", self.front),
        }
    }

    fn front_known(&self) -> bool {
        matches!(self.front.as_str(), "nginx" | "Caddy")
    }

    /// What the admin reads in the terminal after the install; None when nothing is left.
    pub fn text(&self) -> Option<String> {
        let m = self.manual.as_ref()?;
        if !self.front_known() {
            return Some(format!(
                "Let's Encrypt: {}. Free port 80 and mikan gets its certificate by itself: it tries again on its own. Until then it works with a self-signed certificate.",
                m.why
            ));
        }
        Some(format!(
            "Let's Encrypt: {}. Add this {}, then reload {}:\n\n{}\nUntil then mikan works with a self-signed certificate; it tries again by itself.",
            m.why, m.place, self.front, m.snippet
        ))
    }
}

/// Runs a command: its stdout when it succeeds, its output and errors when it does not.
pub trait Run {
    fn run(&mut self, cmd: &str, args: &[&str]) -> Result<String, String>;
}

/// The commands of this server.
pub struct Host;

impl Run for Host {
    fn run(&mut self, cmd: &str, args: &[&str]) -> Result<String, String> {
        match Command::new(cmd).args(args).stdin(Stdio::null()).output() {
            Ok(o) if o.status.success() => Ok(String::from_utf8_lossy(&o.stdout).into_owned()),
            Ok(o) => {
                let text = format!("{}{}", String::from_utf8_lossy(&o.stderr), String::from_utf8_lossy(&o.stdout));
                Err(text.trim().to_owned())
            }
            Err(e) => Err(format!("{cmd}: {e}")),
        }
    }
}

/// Passes the challenge for domain to the local port through front; changes nothing
/// without consent.
pub fn setup(front: Front, domain: &str, port: u16, consent: bool) -> Report {
    match front {
        Front::Nginx => nginx(&mut Host, domain, port, consent),
        Front::Caddy => caddy(&mut Host, domain, port, consent, caddyfile()),
    }
}

/// The last line of a command's complaint, short enough for a note.
fn short(e: &str) -> String {
    let line = e.lines().rev().find(|l| !l.trim().is_empty()).unwrap_or("").trim();
    line.chars().take(160).collect()
}

/// A file written in place of what was there; undo puts that back (or removes a new file).
struct Change {
    path: PathBuf,
    before: Option<Vec<u8>>,
}

impl Change {
    /// Writes text to path; what was there is copied to backup first.
    fn write(path: &Path, text: &str, backup: &Path) -> std::io::Result<Change> {
        let before = fs::read(path).ok();
        if let Some(b) = &before {
            fs::write(backup, b)?;
        }
        // In place, so the file keeps its owner and mode.
        fs::write(path, text)?;
        Ok(Change { path: path.to_owned(), before })
    }

    fn undo(self) {
        let _ = match &self.before {
            Some(b) => fs::write(&self.path, b),
            None => fs::remove_file(&self.path),
        };
    }
}

fn stamp() -> u64 {
    SystemTime::now().duration_since(UNIX_EPOCH).map(|d| d.as_secs()).unwrap_or(0)
}

/// Reloads a web server: through systemd when it runs the unit, else by its own command.
fn reload(run: &mut dyn Run, unit: &str, own: &[&str]) -> Result<(), String> {
    if run.run("systemctl", &["is-active", "--quiet", unit]).is_ok() {
        return run.run("systemctl", &["reload", unit]).map(drop);
    }
    run.run(own[0], &own[1..]).map(drop)
}

// ---- nginx ----

/// The location that passes the challenge; the panel's challenge server checks the Host.
fn nginx_location(port: u16, indent: &str) -> String {
    format!(
        "{indent}location /.well-known/acme-challenge/ {{\n{indent}    proxy_pass http://127.0.0.1:{port};\n{indent}    proxy_set_header Host $host;\n{indent}}}\n"
    )
}

/// A server block of its own for domain on port 80: the challenge, and nothing else. Short,
/// so that the finish screen of a narrow terminal shows it whole.
pub fn nginx_server(domain: &str, port: u16, ipv6: bool) -> String {
    let v6 = if ipv6 { "    listen [::]:80;\n" } else { "" };
    format!(
        "server {{\n    listen 80;\n{v6}    server_name {domain};\n{}    location / {{ return 404; }}\n}}\n",
        nginx_location(port, "    ")
    )
}

/// The file the installer writes: the server block, and who wrote it.
fn nginx_file(domain: &str, port: u16, ipv6: bool) -> String {
    format!(
        "# Written by the mikan installer: Let's Encrypt checks {domain} here for mikan's certificate.\n{}",
        nginx_server(domain, port, ipv6)
    )
}

/// The parts of nginx's config (the dump of `nginx -T`) the rule depends on.
#[derive(Debug, Default)]
struct NginxConf {
    /// nginx.conf, the first file of the dump.
    main: PathBuf,
    /// The include patterns of its http block, absolute.
    includes: Vec<String>,
    /// Every include pattern anywhere, absolute.
    all_includes: Vec<String>,
    servers: Vec<Server>,
    /// Some server listens on [::]:80: the new one does too.
    ipv6: bool,
}

#[derive(Debug)]
struct Server {
    file: PathBuf,
    names: Vec<String>,
    port80: bool,
    /// It has a listen directive: without one nginx puts it on port 80.
    listens: bool,
}

/// nginx's words: quoted strings whole, `;`, `{` and `}` apart, comments gone.
fn nginx_tokens(text: &str) -> Vec<String> {
    let mut out = Vec::new();
    let mut cur = String::new();
    let mut chars = text.chars().peekable();
    while let Some(c) = chars.next() {
        match c {
            '#' if cur.is_empty() => while chars.next_if(|&n| n != '\n').is_some() {},
            '"' | '\'' if cur.is_empty() => {
                while let Some(n) = chars.next() {
                    match n {
                        '\\' => cur.extend(chars.next()),
                        n if n == c => break,
                        n => cur.push(n),
                    }
                }
                out.push(std::mem::take(&mut cur));
            }
            ';' | '{' | '}' => {
                if !cur.is_empty() {
                    out.push(std::mem::take(&mut cur));
                }
                out.push(c.to_string());
            }
            '\\' => {
                cur.push(c);
                cur.extend(chars.next());
            }
            c if c.is_whitespace() => {
                if !cur.is_empty() {
                    out.push(std::mem::take(&mut cur));
                }
            }
            c => cur.push(c),
        }
    }
    if !cur.is_empty() {
        out.push(cur);
    }
    out
}

/// Whether a listen directive's address is port 80 (`80`, `*:80`, `[::]:80`, `1.2.3.4:80`).
fn listens_80(addr: &str) -> bool {
    if addr.starts_with("unix:") {
        return false;
    }
    let port = match addr.rsplit_once(':') {
        Some((_, p)) if !addr.starts_with('[') || addr.contains("]:") => p,
        _ => addr,
    };
    port == "80"
}

fn parse_nginx(dump: &str) -> NginxConf {
    let mut conf = NginxConf::default();
    let mut files: Vec<(PathBuf, String)> = Vec::new();
    for line in dump.lines() {
        if let Some(name) = line.strip_prefix("# configuration file ").and_then(|l| l.strip_suffix(':')) {
            files.push((PathBuf::from(name), String::new()));
        } else if let Some((_, text)) = files.last_mut() {
            text.push_str(line);
            text.push('\n');
        }
    }
    let Some((main, _)) = files.first() else { return conf };
    conf.main = main.clone();
    let dir = main.parent().map(Path::to_path_buf).unwrap_or_default();
    let absolute = |p: &str| if p.starts_with('/') { p.to_owned() } else { dir.join(p).to_string_lossy().into_owned() };
    for (i, (file, text)) in files.iter().enumerate() {
        // nginx.conf has its servers in http; an included file has them at its top.
        let server_depth = if i == 0 { 2 } else { 1 };
        let mut stack: Vec<String> = Vec::new();
        let mut words: Vec<String> = Vec::new();
        let mut server: Option<Server> = None;
        for t in nginx_tokens(text) {
            let in_server = server.is_some() && stack.len() == server_depth;
            match t.as_str() {
                "{" => {
                    let name = words.first().cloned().unwrap_or_default();
                    stack.push(name.clone());
                    let parent_ok = i != 0 || stack[0] == "http";
                    if name == "server" && stack.len() == server_depth && parent_ok {
                        server = Some(Server { file: file.clone(), names: Vec::new(), port80: true, listens: false });
                    }
                    words.clear();
                }
                "}" => {
                    if in_server {
                        conf.servers.extend(server.take());
                    }
                    stack.pop();
                    words.clear();
                }
                ";" => {
                    match (words.first().map(String::as_str), server.as_mut().filter(|_| in_server)) {
                        (Some("include"), _) if words.len() > 1 => {
                            let p = absolute(&words[1]);
                            if i == 0 && stack.len() == 1 && stack[0] == "http" {
                                conf.includes.push(p.clone());
                            }
                            conf.all_includes.push(p);
                        }
                        (Some("listen"), Some(s)) if words.len() > 1 => {
                            // the first listen takes the place of the default port 80
                            if !s.listens {
                                s.listens = true;
                                s.port80 = false;
                            }
                            if listens_80(&words[1]) {
                                s.port80 = true;
                                conf.ipv6 |= words[1].starts_with('[');
                            }
                        }
                        (Some("server_name"), Some(s)) => s.names.extend(words[1..].iter().map(|n| n.to_lowercase())),
                        _ => {}
                    }
                    words.clear();
                }
                _ => words.push(t),
            }
        }
    }
    conf
}

/// Whether an nginx server_name covers domain: the name itself, `*.example.com` or
/// `.example.com`. Regular expressions are not read.
fn name_covers(name: &str, domain: &str) -> bool {
    if let Some(rest) = name.strip_prefix("*.") {
        return domain.strip_suffix(rest).is_some_and(|head| head.ends_with('.') && head.len() > 1);
    }
    if let Some(rest) = name.strip_prefix('.') {
        return domain == rest || domain.ends_with(&format!(".{rest}"));
    }
    name == domain
}

/// A shell-like match of one path against a pattern of nginx's include: `*` and `?` stop
/// at `/`, and a name starting with a dot is matched only by a dot.
fn glob(pattern: &str, path: &str) -> bool {
    let (p, s): (Vec<char>, Vec<char>) = (pattern.chars().collect(), path.chars().collect());
    fn go(p: &[char], s: &[char], start: bool) -> bool {
        match (p.first(), s.first()) {
            (None, None) => true,
            // at the start of a name a dot is not for `*` to take
            (Some('*'), Some('.')) if start => go(&p[1..], s, true),
            (Some('*'), _) => go(&p[1..], s, false) || (s.first().is_some_and(|&c| c != '/') && go(p, &s[1..], false)),
            (Some('?'), Some(&c)) => c != '/' && !(start && c == '.') && go(&p[1..], &s[1..], false),
            (Some(&a), Some(&b)) => a == b && go(&p[1..], &s[1..], b == '/'),
            _ => false,
        }
    }
    go(&p, &s, true)
}

/// The directory a new file of nginx's config goes in, from the http block's includes:
/// conf.d or http.d first, then sites-enabled, the file name matching the pattern.
fn nginx_dir(conf: &NginxConf, name: &str, exists: &dyn Fn(&Path) -> bool) -> Option<PathBuf> {
    let rank = |dir: &Path| match dir.file_name().and_then(|n| n.to_str()) {
        Some("conf.d" | "http.d") => 0,
        Some("sites-enabled") => 1,
        _ => 2,
    };
    let mut found: Vec<PathBuf> = conf
        .includes
        .iter()
        .filter_map(|pat| {
            let (dir, last) = pat.rsplit_once('/')?;
            let dir = PathBuf::from(dir);
            (last.contains('*')
                && !dir.to_string_lossy().contains(['*', '?'])
                && glob(pat, &format!("{}/{name}", dir.display()))
                && exists(&dir))
            .then_some(dir)
        })
        .collect();
    found.sort_by_key(|d| rank(d));
    found.into_iter().next()
}

fn nginx(run: &mut dyn Run, domain: &str, port: u16, consent: bool) -> Report {
    let report = |manual: Manual| Report { front: "nginx".into(), added: None, manual: Some(manual) };
    // Reading the config changes nothing, and tells what to add even without consent.
    let conf = match run.run("nginx", &["-T"]) {
        Ok(d) => Ok(parse_nginx(&d)),
        Err(e) => Err(short(&e)),
    };
    let ipv6 = conf.as_ref().is_ok_and(|c| c.ipv6);
    let own = |why: String| Manual {
        why,
        place: "as a new file in nginx's config (in conf.d, or sites-enabled)".into(),
        snippet: nginx_server(domain, port, ipv6),
    };
    let name = format!("mikan-acme-{domain}.conf");
    let ours = |f: &Path| f.file_name().is_some_and(|n| n.to_string_lossy().starts_with("mikan-acme-"));
    // A server block for the domain is there already: a second one would take its requests
    // (or be ignored), so the rule goes into it, one line of the admin's.
    if let Ok(conf) = &conf
        && let Some(s) = conf.servers.iter().find(|s| s.port80 && !ours(&s.file) && s.names.iter().any(|n| name_covers(n, domain)))
    {
        // A return of the server itself answers before any location is picked.
        let place = format!(
            "inside the server block for {domain} in {} (a return right in that block goes into location / first)",
            s.file.display()
        );
        let why = format!("nginx has a server block for {domain} on port 80 already");
        let dir = conf.main.parent().map(Path::to_path_buf).unwrap_or_else(|| PathBuf::from("/etc/nginx"));
        let snippet = dir.join("snippets").join(&name);
        let path = snippet.to_string_lossy().into_owned();
        // nginx would read a file some include matches on its own, where a location is wrong.
        let written = consent
            && !conf.all_includes.iter().any(|p| glob(p, &path))
            && fs::create_dir_all(dir.join("snippets")).is_ok()
            && fs::write(&snippet, nginx_location(port, "")).is_ok();
        let text = if written { format!("include {path};\n") } else { nginx_location(port, "") };
        return report(Manual { why, place, snippet: text });
    }
    if !consent {
        return report(own("nginx holds port 80 and was left alone".into()));
    }
    let conf = match conf {
        Ok(c) => c,
        Err(e) => return report(own(format!("nginx -T fails ({e}), so its config was left alone"))),
    };
    let Some(dir) = nginx_dir(&conf, &name, &|d: &Path| d.is_dir()) else {
        return report(own("nginx's config includes no conf.d or sites-enabled directory to add a file to".into()));
    };
    let path = dir.join(&name);
    // A dot keeps the copy out of nginx's `*` and `*.conf`.
    let backup = dir.join(format!(".{name}.{}.bak", stamp()));
    let change = match Change::write(&path, &nginx_file(domain, port, conf.ipv6), &backup) {
        Ok(c) => c,
        Err(e) => return report(own(format!("{} cannot be written ({e})", path.display()))),
    };
    if let Err(e) = run.run("nginx", &["-t"]) {
        change.undo();
        return report(own(format!("nginx -t did not pass with the rule ({}), so it was taken out again", short(&e))));
    }
    if let Err(e) = reload(run, "nginx", &["nginx", "-s", "reload"]) {
        change.undo();
        return report(own(format!("nginx did not reload ({}), so the rule was taken out again", short(&e))));
    }
    Report { front: "nginx".into(), added: Some(path), manual: None }
}

// ---- Caddy ----

fn caddy_begin(domain: &str) -> String {
    format!("# mikan: Let's Encrypt checks {domain} here for the panel's certificate (written by the installer)")
}

fn caddy_end(domain: &str) -> String {
    format!("# mikan: end of {domain}")
}

/// A plain-HTTP site for domain: Caddy gets no certificate for it, so it does not compete
/// with the panel, and passes only the challenge on.
pub fn caddy_site(domain: &str, port: u16) -> String {
    format!(
        "http://{domain} {{\n\thandle /.well-known/acme-challenge/* {{\n\t\treverse_proxy 127.0.0.1:{port}\n\t}}\n\thandle {{\n\t\trespond 404\n\t}}\n}}\n"
    )
}

/// The site as the installer writes it: between lines that let a later run replace it.
fn caddy_block(domain: &str, port: u16) -> String {
    format!("{}\n{}{}\n", caddy_begin(domain), caddy_site(domain, port), caddy_end(domain))
}

/// The Caddyfile without the installer's earlier site for domain.
fn without_ours(text: &str, domain: &str) -> String {
    let (begin, end) = (caddy_begin(domain), caddy_end(domain));
    let mut out = String::new();
    let mut inside = false;
    for line in text.lines() {
        if line.trim() == begin {
            inside = true;
        } else if inside && line.trim() == end {
            inside = false;
        } else if !inside {
            out.push_str(line);
            out.push('\n');
        }
    }
    out
}

/// Whether a Caddyfile has a site for domain: an address of a top-level block, with or
/// without a scheme and a port, or a wildcard that covers it.
fn caddy_has_site(text: &str, domain: &str) -> bool {
    let mut depth = 0usize;
    for raw in text.lines() {
        let line = raw.split(" #").next().unwrap_or("").trim();
        if line.starts_with('#') || line.is_empty() {
            continue;
        }
        if depth == 0 && line.ends_with('{') && !line.starts_with('(') && !line.starts_with('&') {
            let addrs = line.trim_end_matches('{');
            for a in addrs.split([' ', ',', '\t']).filter(|a| !a.is_empty()) {
                let a = a.split("://").last().unwrap_or(a);
                let host = a.split('/').next().unwrap_or(a);
                let host = host.rsplit_once(':').map_or(host, |(h, p)| if p.bytes().all(|b| b.is_ascii_digit()) { h } else { host });
                if name_covers(&host.to_lowercase(), domain) {
                    return true;
                }
            }
        }
        depth = (depth + line.matches('{').count()).saturating_sub(line.matches('}').count());
    }
    false
}

/// Where Caddy's Caddyfile is, from the command line of the running caddy.
fn caddyfile() -> Result<PathBuf, String> {
    let procs = fs::read_dir("/proc").map_err(|e| e.to_string())?;
    let found = procs.flatten().find_map(|e| {
        let pid: u32 = e.file_name().to_str()?.parse().ok()?;
        (fs::read_to_string(format!("/proc/{pid}/comm")).ok()?.trim() == "caddy").then_some(pid)
    });
    let Some(pid) = found else { return caddyfile_from(None, None, &|p| p.is_file()) };
    let cmdline = fs::read(format!("/proc/{pid}/cmdline")).unwrap_or_default();
    let args: Vec<String> = String::from_utf8_lossy(&cmdline).split('\0').filter(|a| !a.is_empty()).map(str::to_owned).collect();
    let cwd = fs::read_link(format!("/proc/{pid}/cwd")).ok();
    caddyfile_from(Some(&args), cwd.as_deref(), &|p| p.is_file())
}

const CADDYFILE: &str = "/etc/caddy/Caddyfile";

fn caddyfile_from(args: Option<&[String]>, cwd: Option<&Path>, exists: &dyn Fn(&Path) -> bool) -> Result<PathBuf, String> {
    let Some(args) = args else {
        return if exists(Path::new(CADDYFILE)) { Ok(CADDYFILE.into()) } else { Err(format!("no Caddyfile at {CADDYFILE}")) };
    };
    let flag = |name: &str| {
        args.iter().enumerate().find_map(|(i, a)| {
            let a = a.trim_start_matches('-');
            match a.strip_prefix(name) {
                Some("") if args[i].starts_with('-') => args.get(i + 1).cloned(),
                Some(v) if args[i].starts_with('-') && v.starts_with('=') => Some(v[1..].to_owned()),
                _ => None,
            }
        })
    };
    let adapter = flag("adapter");
    let path = match flag("config") {
        Some(c) => PathBuf::from(c),
        None if args.iter().any(|a| a.trim_start_matches('-') == "resume") => {
            return Err("Caddy runs from the config last given to its API (--resume)".into());
        }
        // Without --config Caddy reads the Caddyfile of its working directory.
        None => cwd.map(|d| d.join("Caddyfile")).ok_or("Caddy runs without a config file mikan can find")?,
    };
    let name = path.file_name().map(|n| n.to_string_lossy().into_owned()).unwrap_or_default();
    let caddyfile = match adapter.as_deref() {
        Some("caddyfile") => true,
        Some(_) => false,
        None => name.starts_with("Caddyfile") || name.ends_with(".caddyfile") || name.ends_with(".Caddyfile"),
    };
    if !caddyfile {
        return Err(format!("Caddy runs from {}, which is not a Caddyfile", path.display()));
    }
    if !path.is_absolute() {
        return Err(format!("Caddy runs from {}, a path relative to where it started", path.display()));
    }
    if !exists(&path) {
        return Err(format!("Caddy's {} is not there", path.display()));
    }
    Ok(path)
}

fn caddy(run: &mut dyn Run, domain: &str, port: u16, consent: bool, file: Result<PathBuf, String>) -> Report {
    let report = |manual: Manual| Report { front: "Caddy".into(), added: None, manual: Some(manual) };
    let place = |file: &str| format!("at the end of {file}");
    let manual = |why: String, file: &str| Manual { why, place: place(file), snippet: caddy_site(domain, port) };
    if !consent {
        return report(manual("Caddy holds port 80 and was left alone".into(), "the Caddyfile"));
    }
    let path = match file {
        Ok(p) => p,
        Err(why) => return report(manual(why, "Caddy's Caddyfile")),
    };
    let shown = path.display().to_string();
    let text = match fs::read_to_string(&path) {
        Ok(t) => t,
        Err(e) => return report(manual(format!("{shown} cannot be read ({e})"), &shown)),
    };
    let rest = without_ours(&text, domain);
    if caddy_has_site(&rest, domain) {
        return report(Manual {
            why: format!("the Caddyfile has a site for {domain}, and Caddy wants that certificate for itself"),
            place: format!("in place of that site in {shown}"),
            snippet: caddy_site(domain, port),
        });
    }
    let mut new = rest.trim_end().to_owned();
    if !new.is_empty() {
        new.push_str("\n\n");
    }
    new.push_str(&caddy_block(domain, port));
    let name = path.file_name().map(|n| n.to_string_lossy().into_owned()).unwrap_or_default();
    let backup = path.with_file_name(format!("{name}.mikan-{}.bak", stamp()));
    let change = match Change::write(&path, &new, &backup) {
        Ok(c) => c,
        Err(e) => return report(manual(format!("{shown} cannot be written ({e})"), &shown)),
    };
    if let Err(e) = run.run("caddy", &["validate", "--config", &shown, "--adapter", "caddyfile"]) {
        change.undo();
        return report(manual(format!("caddy validate did not pass with the site ({}), so it was taken out again", short(&e)), &shown));
    }
    if let Err(e) = reload(run, "caddy", &["caddy", "reload", "--config", &shown, "--adapter", "caddyfile"]) {
        change.undo();
        return report(manual(format!("Caddy did not reload ({}), so the site was taken out again", short(&e)), &shown));
    }
    Report { front: "Caddy".into(), added: Some(path), manual: None }
}

#[cfg(test)]
mod tests {
    use super::*;

    // `mikan cert proxy` on a node or a panel: who holds port 80 decides, and a port chosen
    // before stays (the web server's rule points there).
    #[test]
    fn port80_is_decided_by_its_holder() {
        assert_eq!(port80(None, None, || 18080), Port80::Own { drop: false });
        assert_eq!(port80(Some("mikan-node"), Some(":80"), || 18080), Port80::Own { drop: false });
        assert_eq!(port80(None, Some("127.0.0.1:18081"), || 18080), Port80::Own { drop: true }, "the web server has gone");
        assert_eq!(port80(Some("nginx"), None, || 18085), Port80::Front(Front::Nginx, 18085));
        assert_eq!(port80(Some("nginx"), Some("127.0.0.1:18081"), || 18085), Port80::Front(Front::Nginx, 18081));
        assert_eq!(port80(Some("caddy"), None, || 18080), Port80::Front(Front::Caddy, 18080));
        assert_eq!(port80(Some("apache2"), None, || 18080), Port80::Other("apache2".into()));
    }

    #[test]
    fn the_manual_rule_names_the_port_and_the_env_line() {
        let text = manual_text(Front::Nginx, "node.example.com", 18080);
        assert!(text.contains("server_name node.example.com;") && text.contains("proxy_pass http://127.0.0.1:18080;"), "{text}");
        assert!(text.contains("MIKAN_ACME_LISTEN=127.0.0.1:18080 in /opt/mikan/.env"), "{text}");
        let text = manual_text(Front::Caddy, "node.example.com", 18080);
        assert!(text.contains("http://node.example.com {") && text.contains("reverse_proxy 127.0.0.1:18080"), "{text}");
    }

    /// Answers commands from a script and records them.
    #[derive(Default)]
    struct Fake {
        calls: Vec<String>,
        answers: Vec<(&'static str, Result<String, String>)>,
    }

    impl Fake {
        fn on(mut self, prefix: &'static str, answer: Result<&str, &str>) -> Self {
            self.answers.push((prefix, answer.map(str::to_owned).map_err(str::to_owned)));
            self
        }
    }

    impl Run for Fake {
        fn run(&mut self, cmd: &str, args: &[&str]) -> Result<String, String> {
            let line = format!("{cmd} {}", args.join(" "));
            self.calls.push(line.clone());
            self.answers.iter().find(|(p, _)| line.starts_with(p)).map(|(_, a)| a.clone()).unwrap_or(Err(format!("{cmd}: not found")))
        }
    }

    fn tmpdir(name: &str) -> PathBuf {
        let d = std::env::temp_dir().join(format!("acme-{name}-{}", std::process::id()));
        let _ = fs::remove_dir_all(&d);
        fs::create_dir_all(&d).unwrap();
        d
    }

    /// An nginx -T dump of a Debian-like layout under root.
    fn dump(root: &Path, site: &str) -> String {
        let r = root.display();
        format!(
            "# configuration file {r}/nginx.conf:\nuser www-data;\nevents {{ worker_connections 768; }}\nhttp {{\n    include mime.types;\n    include {r}/conf.d/*.conf;\n    include {r}/sites-enabled/*;\n}}\n\n# configuration file {r}/mime.types:\ntypes {{ text/html html; }}\n\n# configuration file {r}/sites-enabled/default:\n{site}\n"
        )
    }

    const DEFAULT_SITE: &str = "server {\n    listen 80 default_server;\n    listen [::]:80 default_server;\n    server_name _;\n    root /var/www/html; # the welcome page\n}\n";

    #[test]
    fn nginx_config_is_read_from_its_dump() {
        let root = Path::new("/etc/nginx");
        let site = "server {\n  listen 443 ssl;\n  server_name shop.example.com;\n}\nserver {\n  server_name \"vpn.example.com\" www.example.com;\n  location / { return 301 https://$host$request_uri; }\n}\n";
        let conf = parse_nginx(&format!("nginx: the configuration file syntax is ok\n{}", dump(root, site)));
        assert_eq!(conf.main, root.join("nginx.conf"));
        assert_eq!(conf.includes, ["/etc/nginx/mime.types", "/etc/nginx/conf.d/*.conf", "/etc/nginx/sites-enabled/*"]);
        assert_eq!(conf.servers.len(), 2);
        assert!(!conf.servers[0].port80, "443 only");
        assert!(conf.servers[1].port80, "no listen is port 80");
        assert_eq!(conf.servers[1].names, ["vpn.example.com", "www.example.com"]);
        assert_eq!(conf.servers[1].file, root.join("sites-enabled/default"));
        assert!(!conf.ipv6);
        assert!(parse_nginx(&dump(root, DEFAULT_SITE)).ipv6);
        for (addr, yes) in [("80", true), ("*:80", true), ("[::]:80", true), ("10.0.0.1:80", true), ("8080", false), ("[::]:8080", false)] {
            assert_eq!(listens_80(addr), yes, "{addr}");
        }
        assert!(!listens_80("unix:/run/nginx.sock"));
    }

    #[test]
    fn names_and_globs() {
        assert!(name_covers("vpn.example.com", "vpn.example.com"));
        assert!(name_covers("*.example.com", "vpn.example.com"));
        assert!(!name_covers("*.example.com", "example.com"));
        assert!(name_covers(".example.com", "example.com") && name_covers(".example.com", "a.example.com"));
        assert!(!name_covers("_", "vpn.example.com") && !name_covers("example.com", "vpn.example.com"));
        assert!(glob("/etc/nginx/conf.d/*.conf", "/etc/nginx/conf.d/mikan-acme-vpn.example.com.conf"));
        assert!(glob("/etc/nginx/sites-enabled/*", "/etc/nginx/sites-enabled/mikan-acme-x.conf"));
        assert!(!glob("/etc/nginx/conf.d/*.conf", "/etc/nginx/conf.d/x.conf.bak"));
        assert!(!glob("/etc/nginx/sites-enabled/*", "/etc/nginx/sites-enabled/.mikan-acme-x.conf.1.bak"), "a dot file is hidden");
        assert!(!glob("/etc/nginx/*.conf", "/etc/nginx/snippets/x.conf"), "* stops at /");
    }

    #[test]
    fn the_nginx_server_block() {
        let s = nginx_server("vpn.example.com", 18080, false);
        assert!(s.contains("server_name vpn.example.com;") && s.contains("listen 80;") && !s.contains("[::]"));
        assert!(s.contains("location /.well-known/acme-challenge/ {\n        proxy_pass http://127.0.0.1:18080;\n"));
        assert!(s.contains("proxy_set_header Host $host;"), "the challenge server checks the Host");
        assert!(s.contains("return 404;"));
        assert!(nginx_server("vpn.example.com", 18080, true).contains("listen [::]:80;"));
        // it reads back as one server for the domain on port 80
        let conf = parse_nginx(&format!("# configuration file /etc/nginx/conf.d/x.conf:\n{s}"));
        assert!(conf.servers.is_empty(), "only the main file's http block or an included file's top");
        let conf = parse_nginx(&format!(
            "# configuration file /etc/nginx/nginx.conf:\nhttp {{}}\n# configuration file /etc/nginx/conf.d/x.conf:\n{s}"
        ));
        assert_eq!((conf.servers.len(), conf.servers[0].port80), (1, true));
        assert_eq!(conf.servers[0].names, ["vpn.example.com"]);
    }

    fn layout(name: &str) -> PathBuf {
        let root = tmpdir(name);
        for d in ["conf.d", "sites-enabled"] {
            fs::create_dir_all(root.join(d)).unwrap();
        }
        root
    }

    #[test]
    fn nginx_gets_a_file_tested_then_reloaded() {
        let root = layout("nginx-ok");
        let mut run = Fake::default().on("nginx -T", Ok(&dump(&root, DEFAULT_SITE))).on("nginx -t", Ok("")).on("nginx -s reload", Ok(""));
        let r = nginx(&mut run, "vpn.example.com", 18081, true);
        let file = root.join("conf.d/mikan-acme-vpn.example.com.conf");
        assert_eq!(r.added.as_deref(), Some(file.as_path()), "{r:?}");
        assert!(r.manual.is_none() && r.text().is_none());
        assert_eq!(fs::read_to_string(&file).unwrap(), nginx_file("vpn.example.com", 18081, true), "IPv6 like the others");
        assert_eq!(run.calls, ["nginx -T", "nginx -t", "systemctl is-active --quiet nginx", "nginx -s reload"]);
        // again: the file is replaced, the old one kept apart
        let mut run = Fake::default().on("nginx -T", Ok(&dump(&root, DEFAULT_SITE))).on("nginx -t", Ok("")).on("nginx -s reload", Ok(""));
        assert!(nginx(&mut run, "vpn.example.com", 18082, true).added.is_some());
        assert!(fs::read_to_string(&file).unwrap().contains("127.0.0.1:18082"));
        let kept: Vec<_> = fs::read_dir(root.join("conf.d")).unwrap().flatten().map(|e| e.file_name().into_string().unwrap()).collect();
        assert_eq!(kept.len(), 2);
        assert!(kept.iter().any(|n| n.starts_with(".mikan-acme-vpn.example.com.conf.") && n.ends_with(".bak")));
        fs::remove_dir_all(&root).unwrap();
    }

    #[test]
    fn a_failed_test_or_reload_puts_the_config_back() {
        let root = layout("nginx-fail");
        let file = root.join("conf.d/mikan-acme-vpn.example.com.conf");
        let mut run = Fake::default().on("nginx -T", Ok(&dump(&root, DEFAULT_SITE))).on("nginx -t", Err("nginx: [emerg] duplicate listen"));
        let r = nginx(&mut run, "vpn.example.com", 18081, true);
        assert!(r.added.is_none() && !file.exists(), "a new file is removed");
        assert!(!run.calls.iter().any(|c| c.contains("reload")), "no reload after a failed test");
        let m = r.manual.as_ref().unwrap();
        assert!(m.why.contains("nginx -t did not pass") && m.why.contains("duplicate listen"), "{}", m.why);
        assert!(m.snippet.contains("proxy_pass http://127.0.0.1:18081;"));
        assert!(r.text().unwrap().contains("self-signed"));
        // a file there from before is put back as it was
        fs::write(&file, "# before\n").unwrap();
        let mut run =
            Fake::default().on("nginx -T", Ok(&dump(&root, DEFAULT_SITE))).on("nginx -t", Ok("")).on("nginx -s reload", Err("no pid file"));
        let r = nginx(&mut run, "vpn.example.com", 18081, true);
        assert!(r.manual.unwrap().why.contains("did not reload"));
        assert_eq!(fs::read_to_string(&file).unwrap(), "# before\n");
        fs::remove_dir_all(&root).unwrap();
    }

    #[test]
    fn nginx_with_a_site_of_the_domain_gets_one_line_to_add() {
        let root = layout("nginx-site");
        let site = "server {\n    listen 80;\n    server_name vpn.example.com;\n    return 301 https://$host$request_uri;\n}\n";
        let mut run = Fake::default().on("nginx -T", Ok(&dump(&root, site)));
        let r = nginx(&mut run, "vpn.example.com", 18081, true);
        assert!(r.added.is_none());
        assert_eq!(run.calls, ["nginx -T"], "nothing tested or reloaded: nginx's own files are not touched");
        let m = r.manual.unwrap();
        let snippet = root.join("snippets/mikan-acme-vpn.example.com.conf");
        assert_eq!(m.snippet, format!("include {};\n", snippet.display()));
        assert!(m.place.contains("sites-enabled/default"), "{}", m.place);
        assert_eq!(fs::read_to_string(&snippet).unwrap(), nginx_location(18081, ""));
        assert!(fs::read_dir(root.join("conf.d")).unwrap().next().is_none());
        fs::remove_dir_all(&root).unwrap();
    }

    #[test]
    fn nginx_is_left_alone_without_consent_or_a_place() {
        let mut run = Fake::default();
        let r = nginx(&mut run, "vpn.example.com", 18081, false);
        assert_eq!(run.calls, ["nginx -T"], "only read");
        assert!(r.manual.unwrap().snippet.contains("server_name vpn.example.com;"));
        // without consent a server of the domain gets its location to add, and no file
        let root = layout("nginx-no");
        let site = "server {\n    listen 80;\n    server_name vpn.example.com;\n}\n";
        let mut run = Fake::default().on("nginx -T", Ok(&dump(&root, site)));
        let m = nginx(&mut run, "vpn.example.com", 18081, false).manual.unwrap();
        assert_eq!((m.snippet, run.calls.len()), (nginx_location(18081, ""), 1));
        assert!(!root.join("snippets").exists());
        fs::remove_dir_all(&root).unwrap();
        // no include of a directory: nothing is written
        let root = tmpdir("nginx-flat");
        let flat = format!("# configuration file {}/nginx.conf:\nhttp {{\n  server {{ listen 80; }}\n}}\n", root.display());
        let mut run = Fake::default().on("nginx -T", Ok(&flat));
        let r = nginx(&mut run, "vpn.example.com", 18081, true);
        assert!(r.manual.unwrap().why.contains("no conf.d"));
        assert_eq!(fs::read_dir(&root).unwrap().count(), 0);
        let r = nginx(&mut Fake::default(), "vpn.example.com", 18081, true);
        assert!(r.manual.unwrap().why.contains("nginx -T fails"));
        fs::remove_dir_all(&root).unwrap();
    }

    const CADDYFILE_TEXT: &str = "{\n\temail admin@example.com\n}\n\nshop.example.com {\n\treverse_proxy 127.0.0.1:3000\n}\n";

    #[test]
    fn caddy_gets_a_plain_http_site_validated_then_reloaded() {
        let d = tmpdir("caddy-ok");
        let file = d.join("Caddyfile");
        fs::write(&file, CADDYFILE_TEXT).unwrap();
        let f = file.display().to_string();
        let mut run = Fake::default().on("caddy validate", Ok("Valid configuration")).on("caddy reload", Ok(""));
        let r = caddy(&mut run, "vpn.example.com", 18081, true, Ok(file.clone()));
        assert_eq!(r.added.as_deref(), Some(file.as_path()), "{r:?}");
        let text = fs::read_to_string(&file).unwrap();
        assert!(text.starts_with(CADDYFILE_TEXT.trim_end()));
        assert!(text.ends_with(&caddy_block("vpn.example.com", 18081)));
        assert!(text.contains("http://vpn.example.com {") && text.contains("reverse_proxy 127.0.0.1:18081"));
        assert_eq!(
            run.calls,
            [
                format!("caddy validate --config {f} --adapter caddyfile"),
                "systemctl is-active --quiet caddy".into(),
                format!("caddy reload --config {f} --adapter caddyfile"),
            ]
        );
        let backups: Vec<_> = fs::read_dir(&d).unwrap().flatten().filter(|e| e.file_name().to_string_lossy().ends_with(".bak")).collect();
        assert_eq!(fs::read_to_string(backups[0].path()).unwrap(), CADDYFILE_TEXT);
        // again with another port: the site is replaced, not added twice
        let mut run = Fake::default().on("caddy validate", Ok("")).on("caddy reload", Ok(""));
        assert!(caddy(&mut run, "vpn.example.com", 18082, true, Ok(file.clone())).added.is_some());
        let text = fs::read_to_string(&file).unwrap();
        assert_eq!(text.matches("http://vpn.example.com").count(), 1);
        assert!(text.contains("127.0.0.1:18082") && !text.contains("127.0.0.1:18081"));
        fs::remove_dir_all(&d).unwrap();
    }

    #[test]
    fn caddy_goes_back_when_its_check_fails() {
        let d = tmpdir("caddy-fail");
        let file = d.join("Caddyfile");
        fs::write(&file, CADDYFILE_TEXT).unwrap();
        let mut run = Fake::default().on("caddy validate", Err("Error: adapting config using caddyfile: ambiguous site definition"));
        let r = caddy(&mut run, "vpn.example.com", 18081, true, Ok(file.clone()));
        assert_eq!(fs::read_to_string(&file).unwrap(), CADDYFILE_TEXT);
        assert!(!run.calls.iter().any(|c| c.contains("reload")));
        let m = r.manual.unwrap();
        assert!(m.why.contains("caddy validate did not pass") && m.snippet.contains("http://vpn.example.com {"));
        let mut run = Fake::default().on("caddy validate", Ok("")).on("caddy reload", Err("connection refused: admin endpoint"));
        assert!(caddy(&mut run, "vpn.example.com", 18081, true, Ok(file.clone())).manual.unwrap().why.contains("did not reload"));
        assert_eq!(fs::read_to_string(&file).unwrap(), CADDYFILE_TEXT);
        // a site of the domain in Caddy already: not touched
        let with_site = format!("{CADDYFILE_TEXT}\nhttps://vpn.example.com:443 {{\n\trespond hi\n}}\n");
        fs::write(&file, &with_site).unwrap();
        let mut run = Fake::default();
        let r = caddy(&mut run, "vpn.example.com", 18081, true, Ok(file.clone()));
        assert!(run.calls.is_empty() && r.manual.unwrap().why.contains("has a site for vpn.example.com"));
        assert_eq!(fs::read_to_string(&file).unwrap(), with_site);
        // no consent, no Caddyfile: printed
        assert!(caddy(&mut Fake::default(), "vpn.example.com", 1, false, Ok(file.clone())).manual.is_some());
        let r = caddy(&mut Fake::default(), "vpn.example.com", 1, true, Err("Caddy runs from x.json".into()));
        assert_eq!(r.manual.unwrap().why, "Caddy runs from x.json");
        fs::remove_dir_all(&d).unwrap();
    }

    #[test]
    fn caddy_sites_are_found() {
        assert!(caddy_has_site("vpn.example.com {\n}\n", "vpn.example.com"));
        assert!(caddy_has_site("a.example.com, http://vpn.example.com:80 {\n}\n", "vpn.example.com"));
        assert!(caddy_has_site("*.example.com {\n}\n", "vpn.example.com"));
        assert!(!caddy_has_site("{\n\temail a@vpn.example.com\n}\n:80 {\n\trespond hi\n}\n", "vpn.example.com"));
        assert!(!caddy_has_site("(common) {\n}\nshop.example.com {\n\tvpn.example.com {\n\t}\n}\n", "vpn.example.com"));
        let ours = format!("shop.example.com {{\n}}\n\n{}", caddy_block("vpn.example.com", 1));
        assert!(!caddy_has_site(&without_ours(&ours, "vpn.example.com"), "vpn.example.com"));
        assert_eq!(without_ours(&ours, "vpn.example.com"), "shop.example.com {\n}\n\n");
    }

    #[test]
    fn the_caddyfile_is_found_from_caddys_command_line() {
        let args = |s: &str| s.split(' ').map(str::to_owned).collect::<Vec<_>>();
        let yes = |_: &Path| true;
        let got = |a: &str| caddyfile_from(Some(&args(a)), Some(Path::new("/srv")), &yes);
        assert_eq!(got("caddy run --environ --config /etc/caddy/Caddyfile"), Ok(PathBuf::from("/etc/caddy/Caddyfile")));
        assert_eq!(got("caddy run --config=/opt/site.conf --adapter caddyfile"), Ok(PathBuf::from("/opt/site.conf")));
        assert!(got("caddy run --config /etc/caddy/caddy.json").unwrap_err().contains("not a Caddyfile"));
        assert!(got("caddy run --resume").unwrap_err().contains("--resume"));
        assert!(got("caddy run --config Caddyfile").unwrap_err().contains("relative"));
        assert_eq!(got("caddy run"), Ok(PathBuf::from("/srv/Caddyfile")));
        assert!(caddyfile_from(Some(&args("caddy run --config /etc/caddy/Caddyfile")), None, &|_| false).is_err());
        assert_eq!(caddyfile_from(None, None, &yes), Ok(PathBuf::from(CADDYFILE)));
    }

    #[test]
    fn what_the_admin_reads() {
        let r = Report::other("apache2");
        assert!(r.note().contains("port 80 is taken (apache2)"));
        assert!(r.text().unwrap().contains("Free port 80"));
        let m = nginx(&mut Fake::default(), "vpn.example.com", 18081, false);
        let text = m.text().unwrap();
        assert!(text.contains("Add this as a new file in nginx's config") && text.contains("then reload nginx:"), "{text}");
        assert!(!text.contains('—'));
        assert_eq!(listen(18081), "127.0.0.1:18081");
        assert_eq!(listen_port("127.0.0.1:18081"), Some(18081));
        assert_eq!(listen_port(":80"), None);
    }
}
