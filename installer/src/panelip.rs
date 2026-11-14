//! A node installed or joined with `--panel-ip` opens its API port in ufw to the panel's
//! address alone (host::allow_from). Should the panel move to another address, that rule
//! would keep it out for good: the apilock timer moves it along. While the panel has not
//! reached the node for a few minutes, the addresses the host of the panel's address leads to
//! (the node's last state names it: data/node/node-state.json, panel_url) take the place of
//! the ones in the rule. The port stays closed to everyone else, and a node without such a
//! rule is left as it is.

use std::net::{IpAddr, ToSocketAddrs};
use std::path::Path;

use anyhow::Result;

use crate::DIR;

/// The node's last state, as the node saves it.
const STATE: &str = "data/node/node-state.json";

/// How long the panel may stay away before the rule is looked at: one that reaches the node
/// is let in by the rule as it is.
pub const SILENT: i64 = 180;

/// The sources ufw lets in to port over TCP, from `ufw status`: "31234/tcp ALLOW
/// 203.0.113.5". None when the port is open to anyone ("Anywhere"): nothing to follow.
pub fn sources(status: &str, port: u16) -> Option<Vec<IpAddr>> {
    let want = format!("{port}/tcp");
    let mut out = Vec::new();
    for line in status.lines() {
        let words: Vec<&str> = line.split_whitespace().collect();
        if words.first() != Some(&want.as_str()) {
            continue;
        }
        let Some(at) = words.iter().position(|w| *w == "ALLOW") else { continue };
        let from: Vec<&str> = words[at + 1..].iter().copied().filter(|w| *w != "IN").collect();
        match from.first() {
            Some(w) if w.starts_with("Anywhere") => return None,
            Some(w) => {
                if let Ok(ip) = w.parse::<IpAddr>()
                    && !out.contains(&ip)
                {
                    out.push(ip);
                }
            }
            None => {}
        }
    }
    Some(out)
}

/// The host of the panel's address in the node's state; None when it names none.
pub fn panel_host(state: &[u8]) -> Option<String> {
    #[derive(serde::Deserialize)]
    struct State {
        #[serde(default)]
        panel_url: String,
    }
    let st: State = serde_json::from_slice(state).ok()?;
    let rest = st.panel_url.strip_prefix("https://")?;
    let authority = rest.split(['/', '?', '#']).next()?;
    let host = if let Some(v6) = authority.strip_prefix('[') {
        v6.split(']').next()?
    } else {
        authority.rsplit_once(':').map_or(authority, |(h, _)| h)
    };
    (!host.is_empty()).then(|| host.to_ascii_lowercase())
}

/// Whether an address is one the internet reaches: a name that led into the node's own
/// network must not open the port to it.
fn public(ip: &IpAddr) -> bool {
    match ip {
        IpAddr::V4(a) => {
            !(a.is_private()
                || a.is_loopback()
                || a.is_link_local()
                || a.is_unspecified()
                || a.is_broadcast()
                || a.octets()[0] == 100 && (a.octets()[1] & 0xC0) == 64)
        }
        IpAddr::V6(a) => {
            !(a.is_loopback() || a.is_unspecified() || (a.segments()[0] & 0xfe00) == 0xfc00 || (a.segments()[0] & 0xffc0) == 0xfe80)
        }
    }
}

/// What the rule needs: the panel's addresses it lacks, and the ones it has that are no
/// longer the panel's. Nothing while the panel's addresses are all in it, or none is known.
pub fn plan(allowed: &[IpAddr], panel: &[IpAddr]) -> (Vec<IpAddr>, Vec<IpAddr>) {
    let add: Vec<IpAddr> = panel.iter().filter(|ip| !allowed.contains(ip)).copied().collect();
    if add.is_empty() {
        return (Vec::new(), Vec::new());
    }
    let remove = allowed.iter().filter(|ip| !panel.contains(ip)).copied().collect();
    (add, remove)
}

fn resolve(host: &str) -> Vec<IpAddr> {
    let mut out: Vec<IpAddr> = match host.parse::<IpAddr>() {
        Ok(ip) => vec![ip],
        Err(_) => (host, 443).to_socket_addrs().map(|a| a.map(|s| s.ip()).collect()).unwrap_or_default(),
    };
    out.retain(public);
    out.sort();
    out.dedup();
    out
}

/// Moves the `--panel-ip` rule of port to the panel's addresses now; says what it did.
pub fn follow(port: u16) -> Result<Option<String>> {
    let Some(status) = crate::system::output("ufw", &["status"]) else { return Ok(None) };
    if !status.contains("Status: active") {
        return Ok(None);
    }
    let Some(allowed) = sources(&status, port) else { return Ok(None) };
    if allowed.is_empty() {
        return Ok(None);
    }
    let Ok(state) = std::fs::read(Path::new(DIR).join(STATE)) else { return Ok(None) };
    let Some(host) = panel_host(&state) else { return Ok(None) };
    let (add, remove) = plan(&allowed, &resolve(&host));
    if add.is_empty() {
        return Ok(None);
    }
    for ip in &add {
        crate::host::allow_from(*ip, port)?;
    }
    let port_s = port.to_string();
    for ip in &remove {
        let ip_s = ip.to_string();
        crate::system::run_quiet("ufw", &["delete", "allow", "from", &ip_s, "to", "any", "port", &port_s, "proto", "tcp"])?;
    }
    let join = |l: &[IpAddr]| l.iter().map(ToString::to_string).collect::<Vec<_>>().join(", ");
    Ok(Some(format!("api port: it follows the panel to {} (was {})", join(&add), join(&remove))))
}

#[cfg(test)]
mod tests {
    use super::*;

    const STATUS: &str = "Status: active

To                         Action      From
--                         ------      ----
22/tcp                     ALLOW       Anywhere
443                        ALLOW       Anywhere
31234/tcp                  ALLOW       203.0.113.5
31234/tcp                  ALLOW IN    2001:db8::5
22/tcp (v6)                ALLOW       Anywhere (v6)
";

    #[test]
    fn rules_of_the_port() {
        let ips = sources(STATUS, 31234).unwrap();
        assert_eq!(ips, vec!["203.0.113.5".parse::<IpAddr>().unwrap(), "2001:db8::5".parse().unwrap()]);
        assert_eq!(sources(STATUS, 4000), Some(vec![]));
        assert_eq!(sources(STATUS, 22), None, "open to anyone");
        assert_eq!(sources("31234/tcp ALLOW Anywhere\n", 31234), None);
    }

    #[test]
    fn the_panels_host() {
        assert_eq!(panel_host(br#"{"panel_url":"https://Vpn.Example.com:2053"}"#).as_deref(), Some("vpn.example.com"));
        assert_eq!(panel_host(br#"{"panel_url":"https://203.0.113.9:2053/"}"#).as_deref(), Some("203.0.113.9"));
        assert_eq!(panel_host(br#"{"panel_url":"https://[2001:db8::9]:2053"}"#).as_deref(), Some("2001:db8::9"));
        assert_eq!(panel_host(br#"{"panel_url":""}"#), None);
        assert_eq!(panel_host(br#"{"inbounds":[]}"#), None);
        assert_eq!(panel_host(br#"{"panel_url":"http://plain.example.com"}"#), None);
        assert_eq!(panel_host(b"not json"), None);
    }

    #[test]
    fn what_the_rule_needs() {
        let ip = |s: &str| s.parse::<IpAddr>().unwrap();
        let (add, remove) = plan(&[ip("203.0.113.5")], &[ip("198.51.100.9")]);
        assert_eq!((add, remove), (vec![ip("198.51.100.9")], vec![ip("203.0.113.5")]));
        assert_eq!(plan(&[ip("203.0.113.5")], &[ip("203.0.113.5")]), (vec![], vec![]));
        assert_eq!(plan(&[ip("203.0.113.5")], &[]), (vec![], vec![]), "nothing known: the rule stays");
        assert!(!public(&ip("10.0.0.5")) && !public(&ip("100.64.1.1")) && !public(&ip("fd00::1")) && public(&ip("203.0.113.5")));
        assert_eq!(resolve("10.0.0.5"), Vec::<IpAddr>::new());
    }
}
