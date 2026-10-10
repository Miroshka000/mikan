//! What a node says about its first contact with the panel. The node signs a "hello" to
//! its panel, the panel dials the node back, and the node writes the outcome to
//! data/node/hello.json; an image from before that never writes it. The file is read here
//! after an install or a join, and again by `mikan doctor`.

use std::collections::BTreeMap;
use std::fs;
use std::net::IpAddr;
use std::path::{Path, PathBuf};
use std::thread;
use std::time::{Duration, Instant, SystemTime};

use serde::Deserialize;

use crate::system::Level;
use crate::{DIR, clock};

/// How long an install waits for the node to finish: its own series of attempts ends after
/// about 60 s.
pub const LIMIT: Duration = Duration::from_secs(75);

/// A node that restarted within this much before the wait began still counts as started
/// by it (the clock of the file is the node's, this one is ours).
const SLACK: i64 = 10;

/// The node's data directory on the host (the container sees it as /data/node).
pub fn path() -> PathBuf {
    Path::new(DIR).join("data/node/hello.json")
}

/// One attempt of the node's hello, as the node writes it. Only the first four fields are
/// always there.
#[derive(Deserialize, Debug, Clone, PartialEq)]
pub struct Hello {
    /// This attempt.
    pub at: String,
    /// The first attempt of the series: the node process's start.
    pub started: String,
    #[serde(default)]
    pub attempt: u32,
    /// No more attempts follow: it worked, the node gave up, or the failure is final.
    #[serde(rename = "final")]
    pub last: bool,
    pub ok: bool,
    code: Option<String>,
    #[serde(default)]
    params: BTreeMap<String, serde_json::Value>,
    /// The address the panel saw the hello come from.
    seen_ip: Option<String>,
    /// The address the panel has for this node.
    host: Option<String>,
    error: Option<String>,
}

impl Hello {
    pub fn code(&self) -> &str {
        self.code.as_deref().unwrap_or_default()
    }

    fn param(&self, name: &str) -> Option<String> {
        match self.params.get(name)? {
            serde_json::Value::String(s) if !s.is_empty() => Some(s.clone()),
            serde_json::Value::Number(n) => Some(n.to_string()),
            _ => None,
        }
    }
}

pub fn parse(text: &str) -> Option<Hello> {
    serde_json::from_str(text).ok()
}

fn read(path: &Path) -> Option<Hello> {
    parse(&fs::read_to_string(path).ok()?)
}

/// The last report in the node's data directory, whenever it was made.
pub fn last() -> Option<Hello> {
    read(&path())
}

/// Whether the series began when or after `since` (and not in an earlier life of the node).
fn fresh(h: &Hello, since: SystemTime) -> bool {
    clock::parse_rfc3339(&h.started).is_some_and(|t| t >= clock::unix(since) - SLACK)
}

/// Waits for the node's report of the series it began after `since`: until it is final or
/// ok, or limit passes (then the latest attempt of that series, if there was one). None for
/// an image that does not write the file.
pub fn wait(since: SystemTime, limit: Duration) -> Option<Hello> {
    wait_at(&path(), since, limit)
}

fn wait_at(path: &Path, since: SystemTime, limit: Duration) -> Option<Hello> {
    let start = Instant::now();
    let mut latest = None;
    loop {
        if let Some(h) = read(path).filter(|h| fresh(h, since)) {
            if h.last || h.ok {
                return Some(h);
            }
            latest = Some(h);
        }
        let left = limit.saturating_sub(start.elapsed());
        if left.is_zero() {
            return latest;
        }
        thread::sleep(left.min(Duration::from_secs(1)));
    }
}

/// What the report means for the admin, one line each, with what to do about it. api_port
/// is the node's own, for a report that does not name it.
pub fn lines(h: &Hello, api_port: Option<u16>) -> Vec<(Level, String)> {
    let port = h.param("port").or_else(|| api_port.map(|p| p.to_string()));
    let at = port.as_ref().map_or_else(|| "its API port".to_owned(), |p| format!("port {p}"));
    let ufw = port.as_ref().map_or_else(|| "ufw allow <API port>/tcp".to_owned(), |p| format!("ufw allow {p}/tcp"));
    let host = h.host.clone().filter(|s| !s.is_empty()).or_else(|| h.param("host"));
    let node_host = host.clone().unwrap_or_else(|| "this node".into());
    let detail = h.error.as_deref().and_then(|e| e.lines().next()).map(|e| e.chars().take(160).collect::<String>()).unwrap_or_default();
    let because = if detail.is_empty() { String::new() } else { format!(": {detail}") };
    if h.ok {
        let mut out = vec![(Level::Ok, "The panel connected to this node.".to_owned())];
        let seen = h.seen_ip.as_deref().and_then(|s| s.parse::<IpAddr>().ok());
        let known = host.as_deref().and_then(|s| s.parse::<IpAddr>().ok());
        if let (Some(seen), Some(known)) = (seen, known)
            && seen != known
        {
            out.push((
                Level::Warn,
                format!(
                    "The panel has {known} as this node's address, but this server goes out to the internet from {seen}: if connections fail, check the node's address on the panel's Nodes page"
                ),
            ));
        }
        return out;
    }
    let (level, text) = match h.code() {
        "timeout" => (
            Level::Error,
            format!("The panel could not reach {at} here: timed out. Open the port in the hoster's firewall panel; ufw: {ufw}"),
        ),
        "refused" => (Level::Error, format!("The panel reached this server, but nothing answers on {at}: mikan status, mikan logs node")),
        "unreachable" => (Level::Error, format!("The panel has no route to {node_host}: check the node's IP on the panel's Nodes page")),
        "dns" => (
            Level::Error,
            format!("The panel cannot resolve {node_host}: check the node's address on the panel's Nodes page and its DNS record"),
        ),
        "pin_mismatch" => (
            Level::Error,
            "The panel's key for this node does not match: on the Nodes page use Reissue key, then mikan join <new key>".to_owned(),
        ),
        "tls" => (Level::Error, format!("Something else answers on {at}: free the port or add the node again with another API port")),
        "http_status" => {
            let status = h.param("status").map(|s| format!(" (HTTP {s})")).unwrap_or_default();
            (Level::Error, format!("The panel got an unexpected answer{status} from {at}: something other than mikan may listen there, mikan logs node"))
        }
        "panel_rejected" => (
            Level::Error,
            "The panel does not know this key: it was replaced or the node was removed; take a new key on the Nodes page".to_owned(),
        ),
        "no_panel_url" => (
            Level::Warn,
            "This node's key has no panel address (it comes from an older panel): the panel still connects on its own, see its Nodes page".to_owned(),
        ),
        "panel_unreachable" => {
            let url = h.param("url").map(|u| format!(" at {u}")).unwrap_or_default();
            (Level::Warn, format!("This server cannot reach the panel{url}: the panel still connects on its own, see its Nodes page"))
        }
        "panel_untrusted" => {
            let url = h.param("url").map(|u| format!(" at {u}")).unwrap_or_default();
            (
                Level::Warn,
                format!("The panel{url} has no public certificate yet, so this server did not ask it: the panel still connects on its own, see its Nodes page"),
            )
        }
        "panel_unverified" => (
            Level::Warn,
            "The panel's answer was not signed by it: the panel still connects on its own, see its Nodes page; if it does not, take a new key there".to_owned(),
        ),
        "" => (Level::Error, format!("The panel could not connect to this node{because}: mikan logs node, and see the panel's Nodes page")),
        code => (Level::Error, format!("The panel could not connect to this node ({code}){because}: mikan logs node, and see the panel's Nodes page")),
    };
    vec![(level, text)]
}

/// The lines as they are printed: a mark, then the text.
pub fn render(lines: &[(Level, String)]) -> Vec<String> {
    lines.iter().map(|(level, text)| format!("{} {text}", crate::setup::mark(*level))).collect()
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::time::UNIX_EPOCH;

    const TIMEOUT: &str = r#"{"at":"2026-10-10T12:01:00Z","started":"2026-10-10T12:00:00Z","attempt":2,"final":true,"ok":false,"code":"timeout","params":{"port":"31234","host":"203.0.113.10"},"seen_ip":"203.0.113.5","host":"203.0.113.10","error":"dial tcp 203.0.113.10:31234: i/o timeout"}"#;

    fn hello(json: &str) -> Hello {
        parse(json).unwrap_or_else(|| panic!("{json}"))
    }

    fn failed(code: &str, params: &str) -> Hello {
        hello(&format!(
            r#"{{"at":"2026-10-10T12:00:00Z","started":"2026-10-10T12:00:00Z","final":true,"ok":false,"code":"{code}","params":{params}}}"#
        ))
    }

    fn texts(h: &Hello) -> Vec<String> {
        lines(h, None).into_iter().map(|(_, t)| t).collect()
    }

    #[test]
    fn the_file_is_parsed() {
        let h = hello(TIMEOUT);
        assert_eq!((h.attempt, h.last, h.ok, h.code()), (2, true, false, "timeout"));
        assert_eq!(h.param("port").as_deref(), Some("31234"));
        assert_eq!(h.host.as_deref(), Some("203.0.113.10"));
        // only at, started, final and ok are required
        let bare = hello(r#"{"at":"2026-10-10T12:00:00Z","started":"2026-10-10T12:00:00Z","final":false,"ok":false}"#);
        assert_eq!((bare.attempt, bare.code(), bare.last), (0, "", false));
        // numbers in params, nulls instead of text
        let odd = hello(r#"{"at":"a","started":"b","final":true,"ok":false,"code":null,"params":{"port":31234},"host":null}"#);
        assert_eq!(odd.param("port").as_deref(), Some("31234"));
        for bad in ["", "{}", "not json", r#"{"at":"a","started":"b","ok":true}"#] {
            assert_eq!(parse(bad), None, "{bad:?}");
        }
    }

    #[test]
    fn a_connected_node() {
        let h = hello(
            r#"{"at":"2026-10-10T12:00:03Z","started":"2026-10-10T12:00:00Z","final":true,"ok":true,"seen_ip":"203.0.113.10","host":"203.0.113.10"}"#,
        );
        assert_eq!(lines(&h, None), [(Level::Ok, "The panel connected to this node.".to_owned())]);
        // a name for the host is no reason to compare
        let named = hello(r#"{"at":"a","started":"b","final":true,"ok":true,"seen_ip":"203.0.113.10","host":"node.example.com"}"#);
        assert_eq!(lines(&named, None).len(), 1);
    }

    #[test]
    fn a_node_that_goes_out_from_another_address() {
        let h = hello(r#"{"at":"a","started":"b","final":true,"ok":true,"seen_ip":"203.0.113.5","host":"203.0.113.10"}"#);
        let out = lines(&h, None);
        assert_eq!((out[0].0, out.len(), out[1].0), (Level::Ok, 2, Level::Warn));
        assert!(out[1].1.contains("203.0.113.10 as this node's address") && out[1].1.contains("from 203.0.113.5"), "{}", out[1].1);
    }

    #[test]
    fn each_failure_says_what_to_do() {
        let out = lines(&hello(TIMEOUT), None);
        assert_eq!(out.len(), 1);
        assert_eq!(out[0].0, Level::Error);
        assert!(
            out[0].1.contains("port 31234") && out[0].1.contains("timed out") && out[0].1.contains("ufw allow 31234/tcp"),
            "{}",
            out[0].1
        );

        let refused = texts(&failed("refused", r#"{"port":"31234"}"#));
        assert!(refused[0].contains("nothing answers on port 31234") && refused[0].contains("mikan logs node"), "{refused:?}");

        let pin = lines(&failed("pin_mismatch", "{}"), None);
        assert_eq!(pin[0].0, Level::Error);
        assert!(pin[0].1.contains("Reissue key") && pin[0].1.contains("mikan join <new key>"));

        let gone = texts(&failed("unreachable", r#"{"host":"198.51.100.9"}"#));
        assert!(gone[0].contains("no route to 198.51.100.9"), "{gone:?}");
        assert!(texts(&failed("tls", r#"{"port":"443"}"#))[0].contains("Something else answers on port 443"));
        assert!(texts(&failed("panel_rejected", "{}"))[0].contains("take a new key"));
        assert!(texts(&failed("http_status", r#"{"status":"404"}"#))[0].contains("HTTP 404"));
        assert!(texts(&failed("dns", r#"{"host":"node.example.com"}"#))[0].contains("cannot resolve node.example.com"));
        let other = texts(&failed("weird", "{}"));
        assert!(other[0].contains("(weird)"), "{other:?}");
        // the node's own port fills in when the report does not name it
        assert!(lines(&failed("timeout", "{}"), Some(25305))[0].1.contains("ufw allow 25305/tcp"));
        assert!(texts(&failed("timeout", "{}"))[0].contains("<API port>"));
    }

    #[test]
    fn what_the_node_cannot_do_is_a_warning() {
        let out = lines(&failed("panel_unreachable", r#"{"url":"https://panel.example.com:8443"}"#), None);
        assert_eq!(out[0].0, Level::Warn);
        assert!(out[0].1.contains("cannot reach the panel at https://panel.example.com:8443") && out[0].1.contains("still connects"));
        for code in ["no_panel_url", "panel_unverified"] {
            assert_eq!(lines(&failed(code, "{}"), None)[0].0, Level::Warn, "{code}");
        }
        assert_eq!(render(&out)[0].chars().next(), Some('!'));
        assert!(render(&[(Level::Ok, "x".into()), (Level::Error, "y".into())]) == ["✓ x", "✗ y"]);
    }

    #[test]
    fn waiting_for_the_file() {
        let dir = std::env::temp_dir().join(format!("hello-wait-{}", std::process::id()));
        let _ = fs::remove_dir_all(&dir);
        fs::create_dir_all(&dir).unwrap();
        let file = dir.join("hello.json");
        let since = UNIX_EPOCH + Duration::from_secs(1_790_000_000);
        let t = std::time::Instant::now();
        assert_eq!(wait_at(&file, since, Duration::from_millis(50)), None, "no file, no report");
        assert!(t.elapsed() < Duration::from_secs(2));

        let series = |started: &str, last: bool, ok: bool| {
            format!(r#"{{"at":"{started}","started":"{started}","final":{last},"ok":{ok},"code":"timeout"}}"#)
        };
        // 1790000000 is 2026-09-21T14:13:20Z
        fs::write(&file, series("2026-09-21T14:00:00Z", true, false)).unwrap();
        assert_eq!(wait_at(&file, since, Duration::from_millis(50)), None, "a report of an earlier life of the node is not an answer");
        fs::write(&file, series("2026-09-21T14:13:15Z", true, false)).unwrap();
        assert!(wait_at(&file, since, Duration::from_millis(50)).is_some_and(|h| h.last), "a start a few seconds before still counts");
        fs::write(&file, series("2026-09-21T14:14:00Z", false, true)).unwrap();
        assert!(wait_at(&file, since, Duration::from_millis(50)).is_some_and(|h| h.ok));
        // a series still going on is reported at the limit
        fs::write(&file, series("2026-09-21T14:14:00Z", false, false)).unwrap();
        assert!(wait_at(&file, since, Duration::from_millis(50)).is_some_and(|h| !h.last));
        fs::write(&file, "{").unwrap();
        assert_eq!(wait_at(&file, since, Duration::from_millis(50)), None);
        fs::remove_dir_all(&dir).unwrap();
    }
}
