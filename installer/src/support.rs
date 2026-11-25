//! `mikan support <code>`: the way to show support a server whose panel is down or whose node
//! the panel cannot reach, when the usual way, the panel's "Access for support", does not
//! work. The admin gets a code from the ticket, runs this one command, and the server sends
//! the support desk what `mikan doctor`, `mikan status` and the last lines of the
//! containers' logs say, in one JSON document:
//!
//!   {"code", "kind": "panel" | "node", "version", "doctor": {...}, "status": "...", "logs": "..."}
//!
//! `doctor` is the report of `mikan doctor --json`; `status` and `logs` are text. The code
//! is the ticket's, one-time and short-lived on the other side. Everything that leaves is
//! cut of secrets (redact.rs, and the values of the secret keys of .env wherever they
//! stand); .env itself, keys and certificates are not read for it and are never sent.
//! `--dry-run` prints the document and sends nothing.

use std::time::Duration;

use anyhow::{Context, Result, bail};
use serde::Serialize;

use crate::doctor::{self, JsonReport};
use crate::ops::{self, Install};
use crate::{docker, redact};

/// Where the report goes: the support desk, under the name the panel's support access and the
/// bot's Tribute webhook have too (the panel's `support.DefaultBotURL`).
const SUPPORT_URL: &str = "https://mikanpanel.dpdns.org/support/report";

/// Overrides SUPPORT_URL (a self-hosted desk, a test).
const URL_ENV: &str = "MIKAN_SUPPORT_URL";

/// How many lines of the containers' log are asked for, and the most of them that is sent.
const LOG_LINES: usize = 300;
const LOG_BYTES: usize = 192 << 10;
const STATUS_BYTES: usize = 16 << 10;

/// What is sent.
#[derive(Serialize, Debug)]
pub struct Payload {
    pub code: String,
    pub kind: &'static str,
    pub version: String,
    pub doctor: JsonReport,
    pub status: String,
    pub logs: String,
}

/// A ticket's code: plain characters, short.
fn valid_code(code: &str) -> Result<String> {
    let code = code.trim();
    if code.len() < 4 || code.len() > 64 || !code.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'-' || b == b'_') {
        bail!("{code:?} is not a support code: copy it from your ticket, letters, digits and dashes");
    }
    Ok(code.to_owned())
}

/// The address to send to: the constant, or the override when it is a web address.
fn endpoint(over: Option<&str>) -> Result<String> {
    let Some(url) = over.map(str::trim).filter(|u| !u.is_empty()) else { return Ok(SUPPORT_URL.to_owned()) };
    if !(url.starts_with("https://") || url.starts_with("http://")) || url.contains(char::is_whitespace) {
        bail!("{URL_ENV} is not a web address: {url:?}");
    }
    Ok(url.to_owned())
}

pub fn run(code: &str, dry_run: bool) -> Result<()> {
    let code = valid_code(code)?;
    let install = Install::load()?;
    let url = endpoint(std::env::var(URL_ENV).ok().as_deref())?;
    crate::out("Collecting the server check, the status and the last lines of the log; secrets are cut out, .env and keys are not read.");
    let payload = gather(&install, &code);
    if dry_run {
        crate::out(&serde_json::to_string_pretty(&payload)?);
        return Ok(());
    }
    crate::out(&format!("Sending the report ({} KB) to {url}", serde_json::to_vec(&payload)?.len() / 1024 + 1));
    match send(&url, &payload)? {
        200..=299 => crate::out("Sent. Tell support in the ticket that the report is in; nothing else is needed from you."),
        404 | 410 => bail!("support does not know this code, or it has expired (a code lives 24 hours): ask for a new one in the ticket"),
        429 => bail!("support took too many reports from this server: wait a few minutes and run the command once more"),
        status => bail!("support answered HTTP {status}: send the output of mikan doctor and mikan status in the ticket by hand"),
    }
    Ok(())
}

/// The three parts of the report, read from this server.
fn gather(install: &Install, code: &str) -> Payload {
    let doctor = doctor::collect(install, "");
    let mut status = Vec::new();
    let mut say = |l: &str| status.push(l.to_owned());
    if let Err(e) = ops::status_to(install, &mut say) {
        status.push(format!("status is not available: {e:#}"));
    }
    let logs = docker::recent_logs(LOG_LINES);
    assemble(code, install.node, &install.version(), doctor, &status, logs, &install.config.secret_values())
}

/// The report from its parts: every text is cut of secrets (and of the words only this
/// server knows), the status and the log are kept to a size, the log from its end.
fn assemble(
    code: &str,
    node: bool,
    version: &str,
    mut doctor: JsonReport,
    status: &[String],
    logs: Option<String>,
    secrets: &[String],
) -> Payload {
    let clean = |s: &str| redact::text_with(s, secrets);
    for item in &mut doctor.items {
        item.label = clean(&item.label);
        item.detail = clean(&item.detail);
        item.fix = clean(&item.fix);
    }
    let logs = match logs {
        Some(text) => text.lines().map(&clean).collect::<Vec<_>>().join("\n"),
        None => "the containers' log is not available: Docker did not answer".to_owned(),
    };
    Payload {
        code: code.to_owned(),
        kind: if node { "node" } else { "panel" },
        version: clean(version),
        doctor,
        status: head(&clean(&status.join("\n")), STATUS_BYTES),
        logs: tail(&logs, LOG_BYTES),
    }
}

/// The first max bytes of s, cut between characters.
fn head(s: &str, max: usize) -> String {
    if s.len() <= max {
        return s.to_owned();
    }
    let mut end = max;
    while !s.is_char_boundary(end) {
        end -= 1;
    }
    s[..end].to_owned()
}

/// The last max bytes of s, from the start of a line.
fn tail(s: &str, max: usize) -> String {
    if s.len() <= max {
        return s.to_owned();
    }
    let mut start = s.len() - max;
    while !s.is_char_boundary(start) {
        start += 1;
    }
    match s[start..].find('\n') {
        Some(i) => s[start + i + 1..].to_owned(),
        None => s[start..].to_owned(),
    }
}

/// POSTs the report as JSON; the HTTP status of the answer, whatever it is.
fn send(url: &str, payload: &Payload) -> Result<u16> {
    let agent: ureq::Agent = ureq::Agent::config_builder()
        .timeout_global(Some(Duration::from_secs(60)))
        .http_status_as_error(false)
        .user_agent(format!("mikan-installer/{}", crate::version()))
        .build()
        .into();
    let resp = agent
        .post(url)
        .send_json(payload)
        .with_context(|| format!("cannot reach {url}: send the output of mikan doctor and mikan status in the ticket by hand"))?;
    Ok(resp.status().as_u16())
}

#[cfg(test)]
mod tests {
    use std::io::{Read, Write};
    use std::net::TcpListener;

    use super::*;
    use crate::doctor::JsonItem;

    const SECRET: &str = "AbCdEfGhIjKlMnOpQrStUv0123456789";

    fn doctor(detail: &str) -> JsonReport {
        JsonReport {
            id: String::new(),
            at: "2026-10-14T10:00:00Z".into(),
            version: "0.5.1.1".into(),
            kind: "panel",
            errors: 1,
            warnings: 0,
            items: vec![JsonItem { level: "error", label: "Panel".into(), detail: detail.into(), fix: "mikan restart".into() }],
        }
    }

    #[test]
    fn the_code_is_a_plain_ticket_code() {
        for ok in ["ABCD-1234-wxyz", "abcd", "a_b-c-d-e"] {
            assert_eq!(valid_code(&format!(" {ok}\n")).unwrap(), ok);
        }
        for bad in ["", "abc", "a b c d", "x/../y", "ab;rm -rf /", "é-é-é-é", &"a".repeat(65), "a\nb-cd"] {
            assert!(valid_code(bad).is_err(), "{bad:?}");
        }
    }

    #[test]
    fn the_address_is_the_desks_unless_overridden_with_a_web_address() {
        assert_eq!(endpoint(None).unwrap(), SUPPORT_URL);
        assert_eq!(endpoint(Some("  ")).unwrap(), SUPPORT_URL);
        assert_eq!(endpoint(Some("https://desk.example.com:8790/support/report")).unwrap(), "https://desk.example.com:8790/support/report");
        assert_eq!(endpoint(Some("http://127.0.0.1:9/x")).unwrap(), "http://127.0.0.1:9/x");
        for bad in ["ftp://x/y", "file:///etc/passwd", "desk.example.com", "https://a b/c", "javascript:alert(1)"] {
            assert!(endpoint(Some(bad)).is_err(), "{bad}");
        }
        assert!(SUPPORT_URL.starts_with("https://mikanpanel.dpdns.org/"));
    }

    // The document names its parts as the desk reads them, and nothing else.
    #[test]
    fn the_report_has_the_shape_the_desk_reads() {
        let p =
            assemble("ABCD-1234", true, "0.5.1.1", doctor("down"), &["mikan 0.5.1.1, node".into()], Some("node  | started\n".into()), &[]);
        let v = serde_json::to_value(&p).unwrap();
        let mut keys: Vec<&str> = v.as_object().unwrap().keys().map(String::as_str).collect();
        keys.sort_unstable();
        assert_eq!(keys, ["code", "doctor", "kind", "logs", "status", "version"]);
        assert_eq!((v["code"].as_str(), v["kind"].as_str(), v["version"].as_str()), (Some("ABCD-1234"), Some("node"), Some("0.5.1.1")));
        assert_eq!(v["doctor"]["items"][0]["label"], "Panel");
        assert_eq!(v["status"], "mikan 0.5.1.1, node");
        assert_eq!(v["logs"], "node  | started");
        assert_eq!(assemble("c", false, "x", doctor(""), &[], None, &[]).kind, "panel");
    }

    // Whatever the containers wrote, and whatever the checks found, leaves without a secret:
    // by its shape (a token, a link, a password), and by its value when .env holds it.
    #[test]
    fn the_report_has_no_secrets() {
        let env_secret = "s3cr3t-env-value-xyz";
        let logs = format!(
            "panel  | level=WARN msg=\"login\" password=hunter2hunter2 token=ms_{SECRET}\n\
             panel  | GET /{SECRET}/api/v1/users 200\n\
             node   | link vless://u@h:443?pbk={SECRET}\n\
             node   | joined with {env_secret} as the key\n\
             node   | Authorization: Bearer {SECRET}\n"
        );
        let status = vec!["mikan 0.5.1.1, node".to_owned(), format!("note: {env_secret}")];
        let p = assemble(
            "ABCD-1234",
            true,
            "0.5.1.1",
            doctor(&format!("the panel said token={SECRET} {env_secret}")),
            &status,
            Some(logs),
            &[env_secret.to_owned()],
        );
        let all = serde_json::to_string(&p).unwrap();
        // A failure names the secret by its place in the list: the test prints no secret either.
        for (i, s) in [SECRET, "hunter2hunter2", "u@h", env_secret].into_iter().enumerate() {
            assert!(!all.contains(s), "secret {i} leaves");
        }
        assert!(all.contains("level=WARN") && all.contains("/api/v1/users") && all.contains("mikan restart"), "too much is gone: {all}");
    }

    // The log keeps its end, from a whole line; the status keeps its start; both are bounded.
    #[test]
    fn the_parts_are_bounded() {
        let line = "node  | a line of the log that is long enough to count";
        let logs: String = (0..20_000).map(|i| format!("{line} {i}\n")).collect();
        let status: Vec<String> = (0..2000).map(|i| format!("status line {i} {}", "x".repeat(40))).collect();
        let p = assemble("ABCD", false, "0.5.1.1", doctor(""), &status, Some(logs), &[]);
        assert!(p.logs.len() <= LOG_BYTES && p.logs.len() > LOG_BYTES / 2, "{}", p.logs.len());
        assert!(p.logs.starts_with("node  | a line"), "the log starts mid-line: {:?}", &p.logs[..30]);
        assert!(p.logs.ends_with("19999"), "the newest line is kept");
        assert!(p.status.len() <= STATUS_BYTES && p.status.starts_with("status line 0"));
        // Multi-byte text is cut between characters.
        assert_eq!(head("ééé", 3), "é");
        assert_eq!(tail("ééé\nééé", 5), "éé");
        assert_eq!(tail("ab", 5), "ab");
    }

    // The report goes out as a JSON POST, and a desk's answer comes back as its status.
    #[test]
    fn the_report_is_posted_as_json() {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let addr = listener.local_addr().unwrap();
        let server = std::thread::spawn(move || {
            let (mut sock, _) = listener.accept().unwrap();
            let mut raw = Vec::new();
            let mut buf = [0u8; 4096];
            let (head_end, len) = loop {
                let n = sock.read(&mut buf).unwrap();
                raw.extend_from_slice(&buf[..n]);
                if let Some(at) = raw.windows(4).position(|w| w == b"\r\n\r\n") {
                    let head = String::from_utf8_lossy(&raw[..at]).to_ascii_lowercase();
                    let len = head
                        .lines()
                        .find_map(|l| l.strip_prefix("content-length:"))
                        .and_then(|v| v.trim().parse::<usize>().ok())
                        .unwrap_or(0);
                    break (at + 4, len);
                }
            };
            while raw.len() < head_end + len {
                let n = sock.read(&mut buf).unwrap();
                raw.extend_from_slice(&buf[..n]);
            }
            sock.write_all(b"HTTP/1.1 410 Gone\r\nContent-Length: 0\r\nConnection: close\r\n\r\n").unwrap();
            raw
        });
        let p = assemble("ABCD-1234", false, "0.5.1.1", doctor("down"), &["mikan 0.5.1.1, panel".into()], Some("panel | hi".into()), &[]);
        let status = send(&format!("http://{addr}/support/report"), &p).unwrap();
        assert_eq!(status, 410, "the answer's status is returned, not turned into an error");
        let raw = String::from_utf8(server.join().unwrap()).unwrap();
        let (head, body) = raw.split_once("\r\n\r\n").unwrap();
        assert!(head.starts_with("POST /support/report "), "{head}");
        assert!(head.to_ascii_lowercase().contains("content-type: application/json"), "{head}");
        let v: serde_json::Value = serde_json::from_str(body).unwrap();
        assert_eq!((v["code"].as_str(), v["kind"].as_str(), v["logs"].as_str()), (Some("ABCD-1234"), Some("panel"), Some("panel | hi")));
    }
}
