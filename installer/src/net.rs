//! The world outside: the server's public address, public DNS over HTTPS and downloads.

use std::net::{IpAddr, Ipv4Addr, Ipv6Addr};
use std::time::Duration;

use anyhow::{Context, Result, bail};
use serde::Deserialize;

use crate::system::Level;

fn agent(timeout: Duration, v4_only: bool) -> ureq::Agent {
    let mut cfg = ureq::Agent::config_builder().timeout_global(Some(timeout)).user_agent(format!("mikan-installer/{}", crate::version()));
    if v4_only {
        cfg = cfg.ip_family(ureq::config::IpFamily::Ipv4Only);
    }
    cfg.build().into()
}

/// Downloads a URL whole; limit guards against a runaway body. None when it is not there.
pub fn get_opt(url: &str, limit: u64) -> Result<Option<Vec<u8>>> {
    let mut resp = match agent(Duration::from_secs(120), false).get(url).call() {
        Err(ureq::Error::StatusCode(404)) => return Ok(None),
        r => r.with_context(|| format!("GET {url}"))?,
    };
    Ok(Some(resp.body_mut().with_config().limit(limit).read_to_vec().with_context(|| format!("GET {url}"))?))
}

pub fn get(url: &str, limit: u64) -> Result<Vec<u8>> {
    get_opt(url, limit)?.with_context(|| format!("GET {url}: not found"))
}

/// What an address answers over HTTPS, whatever the status is.
pub struct Probe {
    pub status: u16,
    /// The Date header: the other side's clock.
    pub date: Option<String>,
}

/// Asks an address and says what it answered; an error when the connection fails. Any
/// status counts as an answer (ghcr.io/v2/ says 401 to everyone).
pub fn probe(url: &str, timeout: Duration) -> Result<Probe> {
    let agent: ureq::Agent = ureq::Agent::config_builder()
        .timeout_global(Some(timeout))
        .http_status_as_error(false)
        .user_agent(format!("mikan-installer/{}", crate::version()))
        .build()
        .into();
    let resp = agent.get(url).call()?;
    let date = resp.headers().get("date").and_then(|v| v.to_str().ok()).map(str::to_owned);
    Ok(Probe { status: resp.status().as_u16(), date })
}

/// The address the world sees this server at; the route's source address when the
/// lookup services are out of reach.
pub fn public_ipv4() -> Option<Ipv4Addr> {
    let a = agent(Duration::from_secs(6), true);
    for url in ["https://api.ipify.org", "https://ipv4.icanhazip.com", "https://ifconfig.me/ip"] {
        let body = a.get(url).call().ok().and_then(|mut r| r.body_mut().read_to_string().ok());
        if let Some(ip) = body.and_then(|b| b.trim().parse().ok()) {
            return Some(ip);
        }
    }
    crate::system::route_ipv4()
}

#[derive(Deserialize)]
struct DohAnswer {
    #[serde(rename = "Status")]
    status: u32,
    #[serde(rename = "Answer", default)]
    answer: Vec<DohRecord>,
}

#[derive(Deserialize)]
struct DohRecord {
    #[serde(rename = "type")]
    rtype: u16,
    data: String,
}

const A: u16 = 1;
const AAAA: u16 = 28;

/// Public resolvers: what the world resolves the domain to, not this server's resolver
/// (which a VPN client or /etc/hosts may bend).
const RESOLVERS: [(&str, &str); 2] = [("Cloudflare", "https://cloudflare-dns.com/dns-query"), ("Google", "https://dns.google/resolve")];

fn doh(base: &str, name: &str, rtype: u16) -> Result<Vec<IpAddr>> {
    let url = format!("{base}?name={name}&type={}", if rtype == A { "A" } else { "AAAA" });
    let mut resp = agent(Duration::from_secs(8), false).get(&url).header("accept", "application/dns-json").call()?;
    let ans: DohAnswer = resp.body_mut().read_json()?;
    if ans.status != 0 && ans.status != 3 {
        bail!("DNS status {}", ans.status);
    }
    Ok(ans.answer.iter().filter(|r| r.rtype == rtype).filter_map(|r| r.data.parse().ok()).collect())
}

pub struct Note {
    pub level: Level,
    pub text: String,
}

/// What public DNS says about a domain, and whether it fits this server.
pub struct DomainCheck {
    pub notes: Vec<Note>,
}

impl DomainCheck {
    pub fn level(&self) -> Level {
        self.notes.iter().map(|n| n.level).max_by_key(|l| *l as u8).unwrap_or(Level::Ok)
    }
}

pub fn check_domain(domain: &str, server: Ipv4Addr) -> DomainCheck {
    let mut answers = Vec::new();
    for (who, base) in RESOLVERS {
        match doh(base, domain, A) {
            Ok(ips) => answers.push((who, v4(&ips))),
            Err(e) => answers.push((who, Err(e.to_string()))),
        }
    }
    let aaaa = doh(RESOLVERS[0].1, domain, AAAA).map(|ips| v6(&ips)).unwrap_or_default();
    judge(domain, server, &crate::system::global_ipv6(), &answers, &aaaa)
}

fn v4(ips: &[IpAddr]) -> Result<Vec<Ipv4Addr>, String> {
    Ok(ips.iter().filter_map(|ip| if let IpAddr::V4(a) = ip { Some(*a) } else { None }).collect())
}

fn v6(ips: &[IpAddr]) -> Vec<Ipv6Addr> {
    ips.iter().filter_map(|ip| if let IpAddr::V6(a) = ip { Some(*a) } else { None }).collect()
}

/// Judges the answers of the public resolvers for a domain that must lead to server.
fn judge(
    domain: &str,
    server: Ipv4Addr,
    own_v6: &[Ipv6Addr],
    answers: &[(&str, Result<Vec<Ipv4Addr>, String>)],
    aaaa: &[Ipv6Addr],
) -> DomainCheck {
    let mut notes = Vec::new();
    let mut note = |level, text: String| notes.push(Note { level, text });
    let ok: Vec<&Vec<Ipv4Addr>> = answers.iter().filter_map(|(_, r)| r.as_ref().ok()).collect();
    let Some(&first) = ok.first() else {
        note(Level::Error, "public DNS is out of reach from this server: check the network and try again".into());
        return DomainCheck { notes };
    };
    let a = first.clone();
    if a.is_empty() {
        note(Level::Error, format!("{domain} has no A record: add one pointing to {server} at your DNS host"));
    } else if a.contains(&server) {
        note(Level::Ok, format!("{domain} → {server}, this server"));
        let others: Vec<String> = a.iter().filter(|ip| **ip != server).map(ToString::to_string).collect();
        if !others.is_empty() {
            // The panel refuses such a domain too (internal/panel/dnscheck): some clients would
            // land on another machine.
            note(Level::Error, format!("it also points to {}: remove that A record, some clients would go there", others.join(", ")));
        }
    } else if a.iter().all(|ip| cloudflare(*ip)) {
        note(Level::Error, format!("{domain} is behind Cloudflare's proxy: turn the orange cloud off (DNS only) so it points to {server}"));
    } else {
        let list: Vec<String> = a.iter().map(ToString::to_string).collect();
        note(Level::Error, format!("{domain} points to {}, not to this server ({server}): fix the A record", list.join(", ")));
    }
    let sorted = |v: &[Ipv4Addr]| {
        let mut v = v.to_vec();
        v.sort();
        v
    };
    if ok.iter().any(|x| sorted(x) != sorted(&a)) {
        note(Level::Warn, "Cloudflare and Google answer differently: the record is still spreading, check again in a few minutes".into());
    }
    let strangers: Vec<String> = aaaa.iter().filter(|ip| !own_v6.contains(ip)).map(ToString::to_string).collect();
    if !strangers.is_empty() {
        note(Level::Error, format!("an AAAA record points to {}: IPv6 clients would go there, remove it", strangers.join(", ")));
    }
    DomainCheck { notes }
}

/// Cloudflare's IPv4 ranges (cloudflare.com/ips-v4): a proxied domain resolves into them.
fn cloudflare(ip: Ipv4Addr) -> bool {
    const RANGES: [(u32, u8); 15] = [
        (0xADF5_3000, 20), // 173.245.48.0/20
        (0x6715_F400, 22), // 103.21.244.0/22
        (0x6716_C800, 22), // 103.22.200.0/22
        (0x671F_0400, 22), // 103.31.4.0/22
        (0x8D65_4000, 18), // 141.101.64.0/18
        (0x6CA2_C000, 18), // 108.162.192.0/18
        (0xBE5D_F000, 20), // 190.93.240.0/20
        (0xBC72_6000, 20), // 188.114.96.0/20
        (0xC5EA_F000, 22), // 197.234.240.0/22
        (0xC629_8000, 17), // 198.41.128.0/17
        (0xA29E_0000, 15), // 162.158.0.0/15
        (0x6810_0000, 13), // 104.16.0.0/13
        (0x6818_0000, 14), // 104.24.0.0/14
        (0xAC40_0000, 13), // 172.64.0.0/13
        (0x8300_4800, 22), // 131.0.72.0/22
    ];
    let x = u32::from(ip);
    RANGES.iter().any(|&(net, bits)| x >> (32 - bits) == net >> (32 - bits))
}

/// A domain name the panel can get a certificate for.
pub fn valid_domain(s: &str) -> bool {
    s.len() <= 253
        && s.contains('.')
        && s.parse::<IpAddr>().is_err()
        && s.split('.').all(|l| {
            !l.is_empty()
                && l.len() <= 63
                && !l.starts_with('-')
                && !l.ends_with('-')
                && l.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'-')
        })
        && s.rsplit('.').next().is_some_and(|tld| tld.bytes().any(|b| b.is_ascii_alphabetic()))
}

pub fn valid_email(s: &str) -> bool {
    let Some((user, domain)) = s.split_once('@') else { return false };
    !user.is_empty() && !user.contains(char::is_whitespace) && valid_domain(domain)
}

#[cfg(test)]
mod tests {
    use super::*;

    const SERVER: Ipv4Addr = Ipv4Addr::new(203, 0, 113, 10);

    fn both(ips: &[Ipv4Addr]) -> Vec<(&'static str, Result<Vec<Ipv4Addr>, String>)> {
        vec![("Cloudflare", Ok(ips.to_vec())), ("Google", Ok(ips.to_vec()))]
    }

    #[test]
    fn domain_verdicts() {
        let d = "vpn.example.com";
        assert_eq!(judge(d, SERVER, &[], &both(&[SERVER]), &[]).level(), Level::Ok);
        let proxied = judge(d, SERVER, &[], &both(&[Ipv4Addr::new(104, 21, 32, 1)]), &[]);
        assert!(proxied.level() == Level::Error && proxied.notes[0].text.contains("Cloudflare"));
        let elsewhere = judge(d, SERVER, &[], &both(&[Ipv4Addr::new(198, 51, 100, 7)]), &[]);
        assert!(elsewhere.level() == Level::Error && elsewhere.notes[0].text.contains("198.51.100.7"));
        assert!(judge(d, SERVER, &[], &both(&[]), &[]).notes[0].text.contains("no A record"));
        let v6: Ipv6Addr = "2001:db8::1".parse().unwrap();
        assert_eq!(judge(d, SERVER, &[], &both(&[SERVER]), &[v6]).level(), Level::Error);
        let mixed = judge(d, SERVER, &[], &both(&[SERVER, Ipv4Addr::new(198, 51, 100, 7)]), &[]);
        assert!(mixed.level() == Level::Error && mixed.notes.iter().any(|n| n.text.contains("198.51.100.7")));
        assert_eq!(judge(d, SERVER, &[v6], &both(&[SERVER]), &[v6]).level(), Level::Ok);
        let split = vec![("Cloudflare", Ok(vec![SERVER])), ("Google", Ok(vec![Ipv4Addr::new(198, 51, 100, 7)]))];
        assert_eq!(judge(d, SERVER, &[], &split, &[]).level(), Level::Warn);
        let down = vec![("Cloudflare", Err("timeout".into())), ("Google", Err("timeout".into()))];
        assert_eq!(judge(d, SERVER, &[], &down, &[]).level(), Level::Error);
    }

    #[test]
    fn cloudflare_ranges() {
        assert!(cloudflare(Ipv4Addr::new(104, 21, 32, 1)));
        assert!(cloudflare(Ipv4Addr::new(172, 67, 1, 1)));
        assert!(cloudflare(Ipv4Addr::new(188, 114, 97, 3)));
        assert!(!cloudflare(Ipv4Addr::new(104, 32, 0, 1)));
        assert!(!cloudflare(SERVER));
    }

    #[test]
    fn names() {
        for ok in ["vpn.example.com", "a-b.example.co.uk", "xn--80ak6aa92e.com"] {
            assert!(valid_domain(ok), "{ok}");
        }
        for bad in ["example", "1.2.3.4", "-a.example.com", "a..example.com", "a_b.example.com", "vpn.example.123"] {
            assert!(!valid_domain(bad), "{bad}");
        }
        assert!(valid_email("admin@example.com"));
        assert!(!valid_email("admin@") && !valid_email("a b@example.com"));
    }

    /// The panel judges names by the same cases (internal/hostname, Name).
    #[test]
    fn names_match_the_panel() {
        let cases = std::fs::read_to_string("../internal/hostname/testdata/hosts.txt").unwrap();
        let mut n = 0;
        for line in cases.lines().map(str::trim_end).filter(|l| !l.is_empty() && !l.starts_with('#')) {
            let (verdict, host) = line.split_once(' ').unwrap();
            assert!(["name", "ip", "bad"].contains(&verdict), "{line}");
            assert_eq!(valid_domain(host), verdict == "name", "{verdict} {host:?}");
            n += 1;
        }
        assert!(n >= 20, "only {n} cases");
    }
}
