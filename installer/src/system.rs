//! What the server has: root, the OS, memory, disk, the clock, free ports, the package
//! manager. Each check says what is wrong and what to do about it.

use std::fs;
use std::net::{Ipv4Addr, Ipv6Addr};
use std::path::Path;
use std::process::{Command, Stdio};

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Level {
    Ok,
    Warn,
    Error,
}

#[derive(Clone, Debug)]
pub struct Check {
    pub label: &'static str,
    pub level: Level,
    pub detail: String,
}

impl Check {
    fn new(label: &'static str, level: Level, detail: impl Into<String>) -> Self {
        Self { label, level, detail: detail.into() }
    }
}

/// Runs a command for its effect, quietly; when it fails the error carries the last line
/// it wrote to stderr, which says why ("ufw allow ... failed: ERROR: Bad port").
pub fn run_quiet(cmd: &str, args: &[&str]) -> anyhow::Result<()> {
    let what = || format!("{cmd} {}", args.join(" "));
    let out = Command::new(cmd).args(args).stdin(Stdio::null()).stdout(Stdio::null()).stderr(Stdio::piped()).output();
    let out = match out {
        Ok(o) => o,
        Err(e) => anyhow::bail!("{} failed: {e}", what()),
    };
    if out.status.success() {
        return Ok(());
    }
    let err = String::from_utf8_lossy(&out.stderr);
    match err.lines().rev().map(str::trim).find(|l| !l.is_empty()) {
        Some(why) => anyhow::bail!("{} failed: {why}", what()),
        None => anyhow::bail!("{} failed ({})", what(), out.status),
    }
}

/// Whether a command ran and succeeded, quietly.
pub fn succeeds(cmd: &str, args: &[&str]) -> bool {
    Command::new(cmd).args(args).stdin(Stdio::null()).stdout(Stdio::null()).stderr(Stdio::null()).status().is_ok_and(|s| s.success())
}

/// Runs a command and returns its stdout when it succeeds.
pub fn output(cmd: &str, args: &[&str]) -> Option<String> {
    let out = Command::new(cmd).args(args).stdin(Stdio::null()).stderr(Stdio::null()).output().ok()?;
    out.status.success().then(|| String::from_utf8_lossy(&out.stdout).into_owned())
}

pub fn is_root() -> bool {
    fs::read_to_string("/proc/self/status")
        .ok()
        .and_then(|s| s.lines().find_map(|l| l.strip_prefix("Uid:").map(|r| r.split_whitespace().nth(1) == Some("0"))))
        .unwrap_or(false)
}

pub struct Os {
    pub id: String,
    pub version: String,
    pub pretty: String,
}

pub fn os() -> Option<Os> {
    parse_os_release(&fs::read_to_string("/etc/os-release").ok()?)
}

fn parse_os_release(text: &str) -> Option<Os> {
    let get = |key: &str| {
        text.lines().find_map(|l| l.strip_prefix(key)?.strip_prefix('=')).map(|v| v.trim_matches('"').to_owned()).unwrap_or_default()
    };
    let os = Os { id: get("ID"), version: get("VERSION_ID"), pretty: get("PRETTY_NAME") };
    (!os.id.is_empty()).then_some(os)
}

/// Ubuntu 22.04+ and Debian 12+ are what the installer is tested on.
fn os_supported(os: &Os) -> bool {
    let major: u32 = os.version.split('.').next().and_then(|v| v.parse().ok()).unwrap_or(0);
    match os.id.as_str() {
        "ubuntu" => major >= 22,
        "debian" => major >= 12,
        _ => false,
    }
}

pub fn memory_mb() -> Option<u64> {
    parse_meminfo(&fs::read_to_string("/proc/meminfo").ok()?, "MemTotal:")
}

/// Memory the kernel can hand out without swapping (MemAvailable), in MB.
pub fn mem_available_mb() -> Option<u64> {
    parse_meminfo(&fs::read_to_string("/proc/meminfo").ok()?, "MemAvailable:")
}

fn parse_meminfo(text: &str, key: &str) -> Option<u64> {
    let kb: u64 = text.lines().find_map(|l| l.strip_prefix(key))?.split_whitespace().next()?.parse().ok()?;
    Some(kb / 1024)
}

/// Bytes an unprivileged writer may still put on the filesystem of path (root has a little
/// more: the reserve is not counted on).
pub fn disk_free_bytes(path: &Path) -> Option<u64> {
    let s = rustix::fs::statvfs(path).ok()?;
    Some(s.f_bavail.saturating_mul(s.f_frsize))
}

/// The bytes of the files below path, links not followed; 0 for what is not there.
pub fn tree_bytes(path: &Path) -> u64 {
    let Ok(meta) = fs::symlink_metadata(path) else { return 0 };
    if !meta.is_dir() {
        return meta.len();
    }
    fs::read_dir(path).map(|d| d.flatten().map(|e| tree_bytes(&e.path())).sum()).unwrap_or(0)
}

/// Free space for Docker's images, in MB.
pub fn disk_free_mb() -> Option<u64> {
    let dir = if fs::metadata("/var/lib/docker").is_ok() { "/var/lib/docker" } else { "/var/lib" };
    parse_df(&output("df", &["-Pk", dir])?)
}

fn parse_df(text: &str) -> Option<u64> {
    let kb: u64 = text.lines().nth(1)?.split_whitespace().nth(3)?.parse().ok()?;
    Some(kb / 1024)
}

/// None when the system does not say (no timedatectl).
pub fn clock_synced() -> Option<bool> {
    output("timedatectl", &["show", "-p", "NTPSynchronized", "--value"]).map(|v| v.trim() == "yes")
}

/// A fresh VPS keeps apt busy for minutes (the hoster's setup, unattended upgrades).
/// Who keeps the package manager busy: whoever holds one of its locks, or a running
/// apt, dpkg or unattended-upgrade. Read from /proc, so it needs neither fuser (Debian
/// images come without psmisc) nor pgrep. Matching names alone is not enough:
/// unattended-upgrades keeps `unattended-upgrade-shutdown --wait-for-signal` running
/// for good, and its name is cut to the same "unattended-upgr" as the real run.
pub fn dpkg_holder() -> Option<String> {
    use std::os::unix::fs::MetadataExt;
    let locks = ["/var/lib/dpkg/lock-frontend", "/var/lib/dpkg/lock", "/var/lib/apt/lists/lock", "/var/cache/apt/archives/lock"];
    let inodes: Vec<u64> = locks.iter().filter_map(|p| fs::metadata(p).ok()).map(|m| m.ino()).collect();
    if let Some(pid) = fs::read_to_string("/proc/locks").ok().and_then(|t| lock_holder(&t, &inodes)) {
        return Some(match pid {
            Some(pid) => format!("{} pid {pid}", proc_name(pid).unwrap_or_else(|| "a process".into())),
            None => "a package manager".into(),
        });
    }
    let procs = fs::read_dir("/proc").ok()?;
    procs.flatten().filter_map(|e| e.file_name().to_str()?.parse::<u32>().ok()).find_map(|pid| {
        let comm = proc_name(pid)?;
        let cmdline = fs::read(format!("/proc/{pid}/cmdline")).unwrap_or_default();
        package_process(&comm, &String::from_utf8_lossy(&cmdline)).then(|| format!("{comm} pid {pid}"))
    })
}

/// The pid holding a lock on one of the inodes in /proc/locks text: Some(None) for an
/// open-file-description lock, which has no owner pid.
fn lock_holder(text: &str, inodes: &[u64]) -> Option<Option<u32>> {
    text.lines().find_map(|l| {
        let f: Vec<&str> = l.split_whitespace().collect();
        // "1: POSIX  ADVISORY  WRITE 1234 fd:01:131090 0 EOF"; blocked waiters start with "->".
        let at = f.iter().position(|s| s.matches(':').count() == 2)?;
        if f.get(1) == Some(&"->") {
            return None;
        }
        let ino: u64 = f[at].rsplit(':').next()?.parse().ok()?;
        inodes.contains(&ino).then(|| f.get(at - 1).and_then(|p| p.parse::<u32>().ok()).filter(|&p| p > 0))
    })
}

fn package_process(comm: &str, cmdline: &str) -> bool {
    match comm {
        "apt-get" | "apt" | "aptitude" | "dpkg" => true,
        "unattended-upgr" => !cmdline.contains("unattended-upgrade-shutdown"),
        _ => false,
    }
}

fn proc_name(pid: u32) -> Option<String> {
    fs::read_to_string(format!("/proc/{pid}/comm")).ok().map(|s| s.trim().to_owned())
}

/// Half-configured packages left by the hoster's image: every apt call fails until
/// `dpkg --configure -a` finishes them.
pub fn dpkg_unfinished() -> bool {
    output("dpkg", &["--audit"]).is_some_and(|s| !s.trim().is_empty())
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum Proto {
    Tcp,
    Udp,
}

impl Proto {
    pub fn name(self) -> &'static str {
        match self {
            Proto::Tcp => "tcp",
            Proto::Udp => "udp",
        }
    }
}

/// Who listens on a port: None when it is free, Some("?") when ss cannot name the process.
pub fn port_owner(port: u16, proto: Proto) -> Option<String> {
    let flags = match proto {
        Proto::Tcp => "-Hlntp",
        Proto::Udp => "-Hlnup",
    };
    let filter = format!("sport = :{port}");
    match Command::new("ss").args([flags, &filter]).stdin(Stdio::null()).stderr(Stdio::null()).output() {
        Ok(out) if out.status.success() => {
            let out = String::from_utf8_lossy(&out.stdout);
            if out.trim().is_empty() {
                return None;
            }
            Some(process_name(&out).unwrap_or_else(|| "?".into()))
        }
        // No ss (a minimal image without iproute2) is not "the port is free": the kernel's
        // own table says the same without it.
        _ => proc_owner(port, proto),
    }
}

fn proc_owner(port: u16, proto: Proto) -> Option<String> {
    let tables = match proto {
        Proto::Tcp => ["/proc/net/tcp", "/proc/net/tcp6"],
        Proto::Udp => ["/proc/net/udp", "/proc/net/udp6"],
    };
    tables.iter().filter_map(|t| fs::read_to_string(t).ok()).any(|text| proc_listening(&text, port, proto)).then(|| "?".into())
}

/// Whether /proc/net/{tcp,udp}[6] text has a socket listening on port: state 0A for TCP,
/// 07 (the only one a bound UDP socket has) for UDP.
fn proc_listening(table: &str, port: u16, proto: Proto) -> bool {
    let state = match proto {
        Proto::Tcp => "0A",
        Proto::Udp => "07",
    };
    table.lines().skip(1).any(|l| {
        let f: Vec<&str> = l.split_whitespace().collect();
        f.len() > 3 && f[3] == state && f[1].rsplit(':').next().and_then(|p| u16::from_str_radix(p, 16).ok()) == Some(port)
    })
}

fn process_name(ss: &str) -> Option<String> {
    let start = ss.find("users:((\"")? + 9;
    let rest = &ss[start..];
    Some(rest[..rest.find('"')?].to_owned())
}

/// The source address of the default route: the server's IPv4 when it is not behind NAT.
pub fn route_ipv4() -> Option<Ipv4Addr> {
    let out = output("ip", &["-4", "route", "get", "1.1.1.1"])?;
    let mut words = out.split_whitespace();
    while let Some(w) = words.next() {
        if w == "src" {
            return words.next()?.parse().ok();
        }
    }
    None
}

/// The server's public IPv6 addresses, to tell a domain's AAAA record that points here.
pub fn global_ipv6() -> Vec<Ipv6Addr> {
    let Some(out) = output("ip", &["-6", "-o", "addr", "show", "scope", "global"]) else { return Vec::new() };
    out.lines()
        .filter_map(|l| {
            let mut w = l.split_whitespace();
            w.find(|&x| x == "inet6")?;
            w.next()?.split('/').next()?.parse().ok()
        })
        .collect()
}

/// The text of `ufw status` when ufw is there and on.
pub fn ufw_status() -> Option<String> {
    output("ufw", &["status"]).filter(|s| s.contains("Status: active"))
}

pub fn ufw_active() -> bool {
    ufw_status().is_some()
}

/// The ports every install needs: the protocols listen on 443 and 8443, TCP and UDP.
pub const VPN_PORTS: [(u16, Proto); 4] = [(443, Proto::Tcp), (443, Proto::Udp), (8443, Proto::Tcp), (8443, Proto::Udp)];

/// The checks before an install; node is an install of a node for another panel.
const DOCKER: &str = "Docker";

/// The Docker check when Docker is there but cannot run mikan (no compose v2): replacing
/// it removes packages, so the admin is asked first.
pub fn docker_to_replace(checks: &[Check]) -> Option<&Check> {
    checks.iter().find(|c| c.label == DOCKER && c.level == Level::Warn)
}

pub fn checks(node: bool) -> Vec<Check> {
    checks_for(node, false)
}

/// The checks of an install that continues (resume): the VPN ports are held by the
/// containers of the attempt before, which is no problem.
pub fn checks_for(node: bool, resume: bool) -> Vec<Check> {
    let mut out = Vec::new();
    out.push(if is_root() {
        Check::new("Root", Level::Ok, "running as root")
    } else {
        Check::new("Root", Level::Error, "run as root: sudo mikan install")
    });
    out.push(match os() {
        Some(o) if os_supported(&o) => Check::new("System", Level::Ok, o.pretty),
        Some(o) => Check::new("System", Level::Warn, format!("{}: tested on Ubuntu 22.04+ and Debian 12+", o.pretty)),
        None => Check::new("System", Level::Warn, "unknown: tested on Ubuntu 22.04+ and Debian 12+"),
    });
    out.push(Check::new("Architecture", Level::Ok, std::env::consts::ARCH));
    out.push(match memory_mb() {
        Some(mb) if mb < 400 => Check::new("Memory", Level::Warn, format!("{mb} MB: Docker needs about 300 MB, mikan 60 MB more")),
        Some(mb) if mb >= 1024 => Check::new("Memory", Level::Ok, format!("{:.1} GB", mb as f64 / 1024.0)),
        Some(mb) => Check::new("Memory", Level::Ok, format!("{mb} MB")),
        None => Check::new("Memory", Level::Warn, "unknown"),
    });
    out.push(match disk_free_mb() {
        Some(mb) if mb < 1024 => Check::new("Disk", Level::Error, format!("{mb} MB free: Docker and mikan need at least 1 GB")),
        Some(mb) if mb < 3072 => {
            Check::new("Disk", Level::Warn, format!("{:.1} GB free: tight for updates and backups", mb as f64 / 1024.0))
        }
        Some(mb) => Check::new("Disk", Level::Ok, format!("{:.0} GB free", mb as f64 / 1024.0)),
        None => Check::new("Disk", Level::Warn, "unknown"),
    });
    out.push(match clock_synced() {
        Some(true) => Check::new("Clock", Level::Ok, "synchronized"),
        Some(false) => Check::new("Clock", Level::Warn, "not synchronized: TLS and REALITY need the right time (timedatectl set-ntp true)"),
        None => Check::new("Clock", Level::Warn, "cannot tell: keep it synchronized, TLS and REALITY need the right time"),
    });
    let taken: Vec<(u16, Proto, String)> = if resume {
        Vec::new()
    } else {
        VPN_PORTS.iter().filter_map(|&(p, proto)| port_owner(p, proto).map(|who| (p, proto, who))).collect()
    };
    out.push(vpn_ports_check(&taken));
    if !node && !resume {
        out.push(port80_check(port_owner(80, Proto::Tcp)));
    }
    out.push(match crate::docker::version() {
        Some(v) if crate::docker::compose_ok() => Check::new(DOCKER, Level::Ok, format!("{v} with compose")),
        Some(v) => Check::new(DOCKER, Level::Warn, format!("{v} without compose v2: replace it from get.docker.com? You are asked first")),
        None => Check::new(DOCKER, Level::Ok, "not installed: the installer sets it up (get.docker.com)"),
    });
    if let Some(who) = dpkg_holder() {
        out.push(Check::new("Packages", Level::Warn, format!("busy, {who}: the installer waits for it")));
    }
    out
}

/// The protocols' usual ports. Taken ones do not stop the install: the panel moves an
/// inbound whose port is in use to a free one.
fn vpn_ports_check(taken: &[(u16, Proto, String)]) -> Check {
    if taken.is_empty() {
        return Check::new("Ports 443, 8443", Level::Ok, "free");
    }
    let list: Vec<String> = taken.iter().map(|(p, proto, who)| format!("{p}/{} taken ({who})", proto.name())).collect();
    Check::new("Ports 443, 8443", Level::Warn, format!("{}: those protocols get other free ports", list.join(", ")))
}

/// Let's Encrypt checks a domain on port 80; nginx and Caddy can pass that on to the panel.
fn port80_check(owner: Option<String>) -> Check {
    match owner {
        None => Check::new("Port 80", Level::Ok, "free for Let's Encrypt"),
        Some(who) if crate::acme::Front::from_process(&who).is_some() => {
            Check::new("Port 80", Level::Warn, format!("taken ({who}): with a domain, mikan offers to pass Let's Encrypt through it"))
        }
        Some(who) => Check::new("Port 80", Level::Warn, format!("taken ({who}): Let's Encrypt cannot issue a certificate for a domain")),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn os_release() {
        let os = parse_os_release("PRETTY_NAME=\"Ubuntu 24.04.1 LTS\"\nNAME=\"Ubuntu\"\nVERSION_ID=\"24.04\"\nID=ubuntu\n").unwrap();
        assert_eq!((os.id.as_str(), os.version.as_str(), os.pretty.as_str()), ("ubuntu", "24.04", "Ubuntu 24.04.1 LTS"));
        assert!(os_supported(&os));
        let old = Os { id: "debian".into(), version: "11".into(), pretty: String::new() };
        assert!(!os_supported(&old));
    }

    #[test]
    fn package_locks() {
        let locks = "1: POSIX  ADVISORY  WRITE 4242 fd:01:131090 0 EOF\n\
                     1: -> POSIX  ADVISORY  WRITE 5151 fd:01:131090 0 EOF\n\
                     2: OFDLCK ADVISORY  WRITE -1 fd:01:131091 0 EOF\n\
                     3: FLOCK  ADVISORY  WRITE 777 00:19:999 0 EOF\n";
        assert_eq!(lock_holder(locks, &[131090]), Some(Some(4242)));
        assert_eq!(lock_holder(locks, &[131091]), Some(None));
        assert_eq!(lock_holder(locks, &[555]), None);
        assert_eq!(lock_holder("", &[131090]), None);

        // The idle helper unattended-upgrades keeps running must not read as busy.
        assert!(!package_process(
            "unattended-upgr",
            "/usr/bin/python3\0/usr/share/unattended-upgrades/unattended-upgrade-shutdown\0--wait-for-signal\0"
        ));
        assert!(package_process("unattended-upgr", "/usr/bin/python3\0/usr/bin/unattended-upgrade\0"));
        assert!(package_process("apt-get", "apt-get\0install\0"));
        assert!(!package_process("aptd", ""));
    }

    // Without ss the kernel's table decides: a listening socket is a taken port, a
    // connection to a remote port 443 is not.
    #[test]
    fn listening_sockets_without_ss() {
        let tcp = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n\
                   0: 00000000:01BB 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12345 1 0000000000000000 100 0 0 10 0\n\
                   1: 0100007F:7A69 0100007F:C350 01 00000000:00000000 00:00000000 00000000     0        0 23456 1 0000000000000000 100 0 0 10 0\n\
                   2: 0100007F:1F90 0100007F:01BB 01 00000000:00000000 00:00000000 00000000     0        0 34567 1 0000000000000000 100 0 0 10 0\n";
        assert!(proc_listening(tcp, 443, Proto::Tcp));
        assert!(!proc_listening(tcp, 31337, Proto::Tcp), "an established connection is not a listener");
        assert!(!proc_listening(tcp, 8080, Proto::Tcp));
        assert!(!proc_listening(tcp, 443, Proto::Udp));
        let udp = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops\n\
                   0: 00000000:20FB 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 12345 2 0000000000000000 0\n";
        assert!(proc_listening(udp, 8443, Proto::Udp));
        assert!(!proc_listening(udp, 8443, Proto::Tcp));
        assert!(!proc_listening("", 443, Proto::Tcp));
    }

    // nginx or caddy on 443 is no reason to stop: the panel moves those inbounds.
    #[test]
    fn taken_ports_warn_and_do_not_stop_the_install() {
        assert_eq!(vpn_ports_check(&[]).level, Level::Ok);
        let c = vpn_ports_check(&[(443, Proto::Tcp, "nginx".into()), (8443, Proto::Udp, "?".into())]);
        assert_eq!(c.level, Level::Warn);
        assert_eq!(c.detail, "443/tcp taken (nginx), 8443/udp taken (?): those protocols get other free ports");
        assert_eq!(port80_check(None).level, Level::Ok);
        for who in ["nginx", "caddy", "apache2"] {
            assert_eq!(port80_check(Some(who.into())).level, Level::Warn, "{who}");
        }
        assert!(port80_check(Some("caddy".into())).detail.contains("pass Let's Encrypt through it"));
        assert!(port80_check(Some("apache2".into())).detail.contains("cannot issue"));
    }

    #[test]
    fn ss_and_df() {
        let ss = "LISTEN 0 4096 0.0.0.0:443 0.0.0.0:* users:((\"nginx\",pid=812,fd=6),(\"nginx\",pid=811,fd=6))\n";
        assert_eq!(process_name(ss).as_deref(), Some("nginx"));
        assert_eq!(process_name("UNCONN 0 0 0.0.0.0:443 0.0.0.0:*"), None);
        let df = "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/vda1 25623780 4830532 19720516 20% /\n";
        assert_eq!(parse_df(df), Some(19258));
    }

    #[test]
    fn memory_disk_and_tree_sizes() {
        let meminfo = "MemTotal:        2014468 kB\nMemFree:          120000 kB\nMemAvailable:     786432 kB\n";
        assert_eq!(parse_meminfo(meminfo, "MemAvailable:"), Some(768));
        assert_eq!(parse_meminfo("MemTotal: 1 kB\n", "MemAvailable:"), None);
        let d = std::env::temp_dir().join(format!("system-tree-{}", std::process::id()));
        let _ = fs::remove_dir_all(&d);
        fs::create_dir_all(d.join("a/b")).unwrap();
        fs::write(d.join("a/one"), vec![0u8; 1000]).unwrap();
        fs::write(d.join("a/b/two"), vec![0u8; 24]).unwrap();
        std::os::unix::fs::symlink("/usr", d.join("a/link")).unwrap();
        assert_eq!(tree_bytes(&d.join("a/one")), 1000);
        assert!((1024..1024 + 64).contains(&tree_bytes(&d)), "a link is not followed: {}", tree_bytes(&d));
        assert_eq!(tree_bytes(&d.join("missing")), 0);
        assert!(disk_free_bytes(&d).is_some_and(|b| b > 0));
        fs::remove_dir_all(&d).unwrap();
    }
}
