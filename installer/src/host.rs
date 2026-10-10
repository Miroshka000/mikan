//! What mikan changes on the host besides /opt/mikan: kernel tuning, ufw rules, the
//! update timer and the mikan command itself.

use std::fs;
use std::path::Path;
use std::process::{Command, Stdio};

use anyhow::{Context, Result, bail};

use crate::system;

pub const SYSCTL: &str = "/etc/sysctl.d/99-mikan.conf";

const SYSCTL_CONF: &str = "# mikan: BBR for the TCP protocols, bigger UDP buffers for Hysteria2 and TUIC (QUIC)
net.core.default_qdisc = fq
net.ipv4.tcp_congestion_control = bbr
net.core.rmem_max = 16777216
net.core.wmem_max = 16777216
";

pub fn tune() -> Result<()> {
    fs::write(SYSCTL, SYSCTL_CONF)?;
    let ok = Command::new("sysctl").arg("--system").stdout(Stdio::null()).stderr(Stdio::null()).status().is_ok_and(|s| s.success());
    if !ok {
        bail!("sysctl failed");
    }
    Ok(())
}

/// HTTPS ports the panel moves a blocked inbound to on its own (internal/panel/autotune
/// Pool): the node cannot open them in the firewall itself.
pub const POOL: [u16; 15] = [2053, 2083, 2087, 2096, 2443, 3443, 4443, 5443, 6443, 7443, 8443, 9443, 10443, 11443, 12443];

/// The ufw rules of an install: the panel's port (or a node's API port), 80 for the
/// certificate authority's HTTP-01 check, 443 and the pool over TCP and UDP. A node needs 80
/// too: the panel orders its public certificate, and the CA comes to the node's port 80.
pub fn rules(panel_port: Option<u16>, node_api: Option<u16>) -> Vec<String> {
    let mut r = Vec::new();
    if let Some(p) = panel_port {
        r.push(format!("{p}/tcp"));
    }
    if let Some(p) = node_api {
        r.push(format!("{p}/tcp"));
    }
    r.push(ACME_RULE.into());
    r.extend(pool_rules());
    r
}

/// Port 80, where the certificate authority checks the server (HTTP-01).
pub const ACME_RULE: &str = "80/tcp";

/// What `mikan update` opens again on a server with ufw on: port 80 (nodes installed before
/// they got public certificates lack it) and the pool, which may have grown.
pub fn update_rules() -> Vec<String> {
    std::iter::once(ACME_RULE.to_string()).chain(pool_rules()).collect()
}

/// 443 and the pool over TCP and UDP: what the protocols and the panel's moves need open.
pub fn pool_rules() -> Vec<String> {
    std::iter::once(443).chain(POOL).flat_map(|p| [format!("{p}/tcp"), format!("{p}/udp")]).collect()
}

/// Opens the rules when ufw is on; returns whether it was.
pub fn open(rules: &[String]) -> Result<bool> {
    if !system::ufw_active() {
        return Ok(false);
    }
    for rule in rules {
        allow(rule)?;
    }
    Ok(true)
}

/// Whether a rule is a port or a range of ports over tcp or udp, like 8443/tcp or
/// 20000:20010/udp: all `ufw allow` takes from here, so that nothing a container printed
/// can become an option or another command.
pub fn valid_rule(rule: &str) -> bool {
    let Some((ports, proto)) = rule.split_once('/') else { return false };
    if proto != "tcp" && proto != "udp" {
        return false;
    }
    let port = |p: &str| p.len() <= 5 && p.bytes().all(|b| b.is_ascii_digit()) && p.parse::<u32>().is_ok_and(|n| (1..=65535).contains(&n));
    match ports.split_once(':') {
        Some((a, b)) => port(a) && port(b) && a.parse::<u32>().unwrap_or(0) <= b.parse::<u32>().unwrap_or(0),
        None => port(ports),
    }
}

pub fn allow(rule: &str) -> Result<()> {
    if !valid_rule(rule) {
        bail!("{rule:?} is not a firewall rule like 8443/tcp");
    }
    ufw(&["allow", rule])
}

/// A node's API port for its panel's address only.
pub fn allow_from(ip: std::net::IpAddr, port: u16) -> Result<()> {
    let ip = ip.to_string();
    let port = port.to_string();
    ufw(&["allow", "from", &ip, "to", "any", "port", &port, "proto", "tcp"])
}

fn ufw(args: &[&str]) -> Result<()> {
    let ok = Command::new("ufw").args(args).stdout(Stdio::null()).stderr(Stdio::null()).status().is_ok_and(|s| s.success());
    if !ok {
        bail!("ufw {} failed", args.join(" "));
    }
    Ok(())
}

pub const BIN: &str = "/usr/local/bin/mikan";

/// Puts this binary at /usr/local/bin/mikan, the server's command, unless it runs from there.
pub fn install_self() -> Result<()> {
    let me = std::env::current_exe()?;
    if fs::canonicalize(&me).ok() == fs::canonicalize(BIN).ok() {
        return Ok(());
    }
    replace_bin(&fs::read(&me)?, None)
}

/// Replaces /usr/local/bin/mikan at once; the running process keeps its old copy. The new
/// file is on the disk, not only in the page cache (a power cut must not leave the command,
/// which both update units run, empty), and it runs and reports the release it is meant to
/// be before it takes the working one's place.
pub fn replace_bin(data: &[u8], release: Option<&str>) -> Result<()> {
    replace_at(Path::new(BIN), data, release)
}

fn replace_at(bin: &Path, data: &[u8], release: Option<&str>) -> Result<()> {
    use std::io::Write;
    use std::os::unix::fs::OpenOptionsExt;
    let mut tmp = bin.as_os_str().to_owned();
    tmp.push(".new");
    let tmp = std::path::PathBuf::from(tmp);
    let _ = fs::remove_file(&tmp);
    let mut f =
        fs::OpenOptions::new().write(true).create_new(true).mode(0o755).open(&tmp).with_context(|| format!("create {}", tmp.display()))?;
    let checked = f.write_all(data).and_then(|()| f.sync_all()).map_err(anyhow::Error::from).and_then(|()| {
        drop(f);
        let out = version_of(&tmp)?;
        match release {
            Some(v) if !out.contains(v) => bail!("the downloaded command is {:?}, not release {v}", out.trim()),
            _ => Ok(()),
        }
    });
    if let Err(e) = checked {
        let _ = fs::remove_file(&tmp);
        return Err(e);
    }
    fs::rename(&tmp, bin).with_context(|| format!("replace {}", bin.display()))?;
    if let Some(dir) = bin.parent().and_then(|d| fs::File::open(d).ok()) {
        let _ = dir.sync_all();
    }
    Ok(())
}

/// What the command at path says to --version; it must run and succeed.
fn version_of(path: &Path) -> Result<String> {
    let mut last = None;
    for _ in 0..5 {
        match Command::new(path).arg("--version").stdin(Stdio::null()).output() {
            Ok(o) if o.status.success() => return Ok(String::from_utf8_lossy(&o.stdout).into_owned()),
            Ok(o) => bail!("the downloaded command does not run ({})", o.status),
            // A file only just written can still be busy for a moment when another thread forked.
            Err(e) if e.kind() == std::io::ErrorKind::ExecutableFileBusy => {
                last = Some(e);
                std::thread::sleep(std::time::Duration::from_millis(100));
            }
            Err(e) => return Err(e).context("run the downloaded command"),
        }
    }
    Err(last.map_or_else(|| anyhow::anyhow!("the downloaded command does not run"), Into::into))
}

const UNIT_NAMES: [&str; 4] = ["mikan-update.service", "mikan-update.timer", "mikan-update-request.service", "mikan-update-request.path"];

/// The unit files of a panel or of a node: the same four, except that the path unit watches
/// the request of the one the server runs (the panel's Update button, or the panel asking
/// a node over its API: data/panel/update/request and data/node/update/request).
fn units(panel: bool) -> [(&'static str, String); 4] {
    let (dir, from) = if panel { ("panel", "the panel") } else { ("node", "the panel this node belongs to") };
    [
        (
            UNIT_NAMES[0],
            "[Unit]
Description=mikan: update when automatic updates are on
After=docker.service network-online.target
Wants=network-online.target

[Service]
Type=oneshot
# A oneshot unit has no time limit by default: an update stuck on a dead connection
# would keep the timer from ever starting it again.
TimeoutStartSec=45min
ExecStart=/usr/local/bin/mikan update --auto
"
            .into(),
        ),
        (
            UNIT_NAMES[1],
            "[Unit]
Description=mikan: look for an update once a day

[Timer]
OnCalendar=*-*-* 03:00:00
RandomizedDelaySec=3h
Persistent=true

[Install]
WantedBy=timers.target
"
            .into(),
        ),
        (
            UNIT_NAMES[2],
            "[Unit]
Description=mikan: update asked for in the panel
After=docker.service

[Service]
Type=oneshot
TimeoutStartSec=45min
ExecStart=/usr/local/bin/mikan update --requested
"
            .into(),
        ),
        (
            UNIT_NAMES[3],
            format!(
                "[Unit]
Description=mikan: wait for an update request from {from}

[Path]
PathExists=/opt/mikan/data/{dir}/update/request
Unit=mikan-update-request.service

[Install]
WantedBy=paths.target
"
            ),
        ),
    ]
}

/// Whether the unit files on disk are the ones this installer writes for a panel or a node
/// (a server updated by an older command has the older ones; a node before 0.5.0.2 has no
/// request unit at all).
pub fn units_current(panel: bool) -> bool {
    units_current_in(Path::new("/etc/systemd/system"), panel)
}

fn units_current_in(dir: &Path, panel: bool) -> bool {
    units(panel).iter().all(|(name, text)| fs::read_to_string(dir.join(name)).is_ok_and(|have| have == *text))
}

/// The update timer and the watch on the update request: a panel's (its Update button) or a
/// node's (the panel asking over the node API).
pub fn install_units(panel: bool) -> Result<()> {
    if !Path::new("/run/systemd/system").exists() {
        bail!("no systemd");
    }
    for (name, text) in units(panel) {
        fs::write(format!("/etc/systemd/system/{name}"), text)?;
    }
    systemctl(&["daemon-reload"])?;
    systemctl(&["enable", "--now", "mikan-update.timer"])?;
    systemctl(&["enable", "--now", "mikan-update-request.path"])?;
    Ok(())
}

pub fn remove_units() {
    let _ = systemctl(&["disable", "--now", "mikan-update.timer", "mikan-update-request.path"]);
    for name in UNIT_NAMES {
        let _ = fs::remove_file(format!("/etc/systemd/system/{name}"));
    }
    let _ = systemctl(&["daemon-reload"]);
}

fn systemctl(args: &[&str]) -> Result<()> {
    let ok = Command::new("systemctl").args(args).stdout(Stdio::null()).stderr(Stdio::null()).status().is_ok_and(|s| s.success());
    if !ok {
        bail!("systemctl {} failed", args.join(" "));
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    // The panel moves a blocked inbound and puts a cascade relay on a port of this pool;
    // the installer must have opened them all.
    #[test]
    fn pool_matches_the_panel() {
        let go = fs::read_to_string("../internal/panel/domain/ports.go").unwrap();
        let start = go.find("var PortPool = []int{").expect("domain.PortPool") + "var PortPool = []int{".len();
        let list = &go[start..start + go[start..].find('}').unwrap()];
        let pool: Vec<u16> = list.split(',').map(|p| p.trim().parse().unwrap()).collect();
        assert_eq!(pool, POOL);
    }

    // What a container prints is not a command: only a port (range) and a protocol pass.
    #[test]
    fn only_port_rules_reach_ufw() {
        for ok in ["443/tcp", "8443/udp", "20000:20010/tcp", "1/tcp", "65535/udp"] {
            assert!(valid_rule(ok), "{ok}");
        }
        for bad in [
            "",
            "443",
            "443/icmp",
            "0/tcp",
            "65536/tcp",
            "99999/tcp",
            "-1/tcp",
            "443/tcp;ls",
            "443/tcp --force",
            "--help/tcp",
            "443:/tcp",
            ":443/tcp",
            "20010:20000/tcp",
            "4 43/tcp",
            "443/TCP",
            "443/tcp\n",
            "1e3/tcp",
            "+443/tcp",
            "443/tcp/udp",
        ] {
            assert!(!valid_rule(bad), "{bad:?}");
        }
        assert!(allow("--help").is_err());
    }

    fn tmpdir(name: &str) -> std::path::PathBuf {
        let d = std::env::temp_dir().join(format!("host-{name}-{}", std::process::id()));
        let _ = fs::remove_dir_all(&d);
        fs::create_dir_all(&d).unwrap();
        d
    }

    // The command is replaced only by a file that runs and is the release meant: a
    // truncated or wrong download leaves the working one.
    #[test]
    fn the_command_is_replaced_only_by_a_working_release() {
        use std::os::unix::fs::PermissionsExt;
        let d = tmpdir("bin");
        let bin = d.join("mikan");
        fs::write(&bin, "#!/bin/sh\necho mikan 0.4.3\n").unwrap();
        fs::set_permissions(&bin, fs::Permissions::from_mode(0o755)).unwrap();
        let old = fs::read(&bin).unwrap();
        let new = b"#!/bin/sh\necho mikan 0.4.4\n";
        for (what, data, release) in [
            ("wrong release", &new[..], Some("0.4.5")),
            ("does not run", &b"#!/bin/sh\nexit 3\n"[..], None),
            ("truncated", &new[..8], Some("0.4.4")),
            ("empty", &b""[..], None),
        ] {
            assert!(replace_at(&bin, data, release).is_err(), "{what} replaced the command");
            assert_eq!(fs::read(&bin).unwrap(), old, "{what}");
            assert!(!d.join("mikan.new").exists(), "{what} left its file");
        }
        replace_at(&bin, new, Some("0.4.4")).unwrap();
        assert_eq!(fs::read(&bin).unwrap(), new);
        assert_eq!(fs::metadata(&bin).unwrap().permissions().mode() & 0o777, 0o755);
        fs::remove_dir_all(&d).unwrap();
    }

    // A oneshot unit has no time limit unless it is given one.
    #[test]
    fn update_units_have_a_time_limit() {
        for panel in [true, false] {
            for (name, text) in units(panel) {
                if name.ends_with(".service") {
                    assert!(text.contains("TimeoutStartSec="), "{name}");
                }
            }
        }
    }

    // A panel's path unit watches the panel's request, a node's the node's: the same four
    // units, the path unit apart.
    #[test]
    fn the_request_unit_watches_the_directory_of_what_the_server_runs() {
        let (panel, node) = (units(true), units(false));
        let path = |all: &[(&str, String); 4]| all.iter().find(|(n, _)| *n == "mikan-update-request.path").unwrap().1.clone();
        assert!(path(&panel).contains("\nPathExists=/opt/mikan/data/panel/update/request\n"));
        assert!(path(&node).contains("\nPathExists=/opt/mikan/data/node/update/request\n"));
        assert!(!path(&panel).contains("data/node") && !path(&node).contains("data/panel"));
        for ((pn, pt), (nn, nt)) in panel.iter().zip(&node) {
            assert_eq!(pn, nn);
            assert!(UNIT_NAMES.contains(pn), "{pn}");
            if !pn.ends_with(".path") {
                assert_eq!(pt, nt, "{pn} is the same on a panel and on a node");
            }
        }
        // both run the same command, which finds out by itself whose request it is
        let service = &node.iter().find(|(n, _)| *n == "mikan-update-request.service").unwrap().1;
        assert!(service.contains("ExecStart=/usr/local/bin/mikan update --requested\n"));
        assert!(path(&node).contains("Unit=mikan-update-request.service"));
    }

    // A server updated by an older command has the older units: a node with none of the
    // request unit, a panel's file on a node, are not current; each mode's own are.
    #[test]
    fn units_are_current_for_the_mode_they_were_written_for() {
        let d = tmpdir("units");
        assert!(!units_current_in(&d, true) && !units_current_in(&d, false), "nothing written yet");
        let write = |panel: bool| {
            for (name, text) in units(panel) {
                fs::write(d.join(name), text).unwrap();
            }
        };
        write(false);
        assert!(units_current_in(&d, false));
        assert!(!units_current_in(&d, true), "a node's units are not a panel's");
        write(true);
        assert!(units_current_in(&d, true) && !units_current_in(&d, false));
        // a node of 0.5.0.1: the timer and the services, no path unit
        write(false);
        fs::remove_file(d.join("mikan-update-request.path")).unwrap();
        assert!(!units_current_in(&d, false), "a node without the request unit gets it on its next update");
        // an older text of the path unit
        write(false);
        let old = fs::read_to_string(d.join("mikan-update-request.path")).unwrap().replace("data/node/", "data/panel/");
        fs::write(d.join("mikan-update-request.path"), old).unwrap();
        assert!(!units_current_in(&d, false));
        fs::remove_dir_all(&d).unwrap();
    }

    #[test]
    fn firewall_rules() {
        let r = rules(Some(21355), None);
        assert_eq!(&r[..4], ["21355/tcp", "80/tcp", "443/tcp", "443/udp"]);
        assert!(r.contains(&"9443/udp".to_string()));
        assert_eq!(rules(None, Some(25305))[0], "25305/tcp");
        // A node gets 80 for its certificate, also when its API port opens for the panel alone.
        assert!(rules(None, Some(25305)).contains(&"80/tcp".to_string()));
        assert!(rules(None, None).contains(&"80/tcp".to_string()));
        let u = update_rules();
        assert_eq!(u[0], "80/tcp");
        assert!(u.contains(&"12443/udp".to_string()) && u.iter().all(|r| valid_rule(r)));
    }
}
