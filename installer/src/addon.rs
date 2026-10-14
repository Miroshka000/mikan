//! Addons of the marketplace (getmikan/marketplace): small services next to the panel, run
//! as their own compose project so panel updates never touch them. Payment adapters speak
//! protocol 1; bots, pages and the rest protocol 2, which also gives them a token to call the
//! panel's API for addons. The panel has no Docker socket: it asks through files in its data
//! directory (internal/panel/addons), and `mikan update --requested` hands the requests here.
//!
//!   addons/requests/<seq>-<id>.json  the panel: {"action": "install" | "remove", "id", "at"},
//!                        for an addon of the admin's own also "source" (custom | external)
//!                        with "image" and "digest" or "url", "kind", "mode", "permissions",
//!                        "mem_mb"; taken oldest first
//!   addons/state.json    this: what runs where, with its tokens, the addresses of the panel's
//!                        API for addons, and how the last request went
//!
//! An addon comes from the signed catalog (official: ghcr.io/getmikan/, the host's network,
//! loopback), is an image the admin named by its digest (custom: on the bridge network
//! mikan-addons, its port published on loopback, the panel's API at the bridge's gateway), or
//! is a process of the admin's own on a loopback port (external: no container, only tokens).

use std::collections::BTreeMap;
use std::fs;
use std::io::Read;
use std::net::Ipv4Addr;
use std::os::unix::fs::PermissionsExt;
use std::path::Path;
use std::process::{Command, Output, Stdio};
use std::thread;
use std::time::{Duration, Instant};

use anyhow::{Context, Result, bail};
use ed25519_dalek::VerifyingKey;
use serde::{Deserialize, Serialize};

use crate::lock::{self, Wait};
use crate::panelfs::Dir;
use crate::{clock, docker, net, panelfs, release, setup};

pub const CATALOG_URL: &str = "https://github.com/getmikan/marketplace/releases/latest/download/index.json";
/// The payment adapters' protocol (internal/panel/addons).
const PROTOCOL: u32 = 1;
/// That of every other kind.
const PROTOCOL_2: u32 = 2;
const KINDS_2: [&str; 3] = ["bot", "ui", "generic"];
const MODES: [&str; 2] = ["http", "client"];
const PERMISSIONS: [&str; 9] = ["bot", "users:read", "users:write", "users:all", "users:grant", "billing:invoice", "events", "kv", "ui"];
/// What an addon may ask for, in MiB; 0 is DEFAULT_MEM_MB.
const MEM_MB: std::ops::RangeInclusive<u32> = 64..=256;
const DEFAULT_MEM_MB: u32 = 128;
/// Sources besides the catalog ("" in the state): an image the admin named, a process of
/// theirs. Protocol 2 only: a payment adapter decides where the money goes.
const CUSTOM: &str = "custom";
const EXTERNAL: &str = "external";
/// Where the panel and this command meet, below DIR; the panel sees it as
/// /data/panel/addons. What lies there is the panel's to write: root reads only its
/// requests from there, through panelfs, and keeps its own copy of the state.
const PANEL_DIR: &str = "data/panel/addons";
/// The queue below PANEL_DIR.
const REQUESTS: &str = "requests";
/// The largest request: one with an image, a URL and every permission is a few hundred bytes.
const REQUEST_LIMIT: u64 = 4096;
/// Requests taken in one run; the rest wait for the panel's next wake-up.
const REQUESTS_PER_RUN: usize = 64;
const COMPOSE_DIR: &str = "/opt/mikan/addons";
/// The state root trusts: the panel cannot see or change it.
const ROOT_STATE: &str = "/opt/mikan/addons/state.json";
/// What `mikan addon load` took: digest → the image ID to run it by. root's own.
const LOADED: &str = "/opt/mikan/addons/loaded.json";
const PROJECT: &str = "mikan-addons";
/// The bridge network of custom addons, also the name of its interface (≤ 15 characters).
const NETWORK: &str = "mikan-addons";

/// The panel's side of the exchange, opened without following a link; None while the
/// panel has not made it.
fn panel_dir(create: bool) -> Result<Option<Dir>> {
    Dir::open(Path::new(crate::DIR), PANEL_DIR, create)
}

/// The panel's queue of requests; None while it has queued nothing.
fn queue_dir() -> Result<Option<Dir>> {
    Dir::open(Path::new(crate::DIR), &format!("{PANEL_DIR}/{REQUESTS}"), false)
}

#[derive(Deserialize, Debug, Clone)]
pub struct Entry {
    pub id: String,
    pub version: String,
    pub protocol: u32,
    /// Protocol 2: bot | ui | generic; protocol 1 is a payment adapter.
    #[serde(default)]
    pub kind: String,
    /// Protocol 2: http (serves /v2/*) | client (only calls the panel).
    #[serde(default)]
    pub mode: String,
    #[serde(default)]
    pub permissions: Vec<String>,
    #[serde(default)]
    pub resources: Resources,
    pub image: String,
    pub digest: String,
    #[serde(default)]
    pub min_panel: String,
    #[serde(default)]
    pub name: BTreeMap<String, String>,
}

#[derive(Deserialize, Debug, Clone, Default)]
pub struct Resources {
    #[serde(default)]
    pub mem_mb: u32,
}

impl Entry {
    /// Fills what protocol 1 leaves out and tells whether this host can run the entry: the
    /// right kind for its protocol, a known mode, known permissions, sane memory. The panel
    /// checks the same (Entry.normalize in internal/panel/addons).
    fn normalize(&mut self) -> bool {
        match self.protocol {
            PROTOCOL => {
                let plain = plain_payment(&self.kind, &self.mode, &self.permissions, self.resources.mem_mb);
                self.kind = "payment".into();
                self.mode = "http".into();
                plain
            }
            PROTOCOL_2 => {
                if self.mode.is_empty() {
                    self.mode = "http".into();
                }
                shape_2_ok(&self.kind, &self.mode, &self.permissions, self.resources.mem_mb)
            }
            _ => false,
        }
    }
}

/// A payment adapter (protocol 1) carries nothing of protocol 2: an empty or its own kind and
/// mode, no permissions, no memory of its own.
fn plain_payment(kind: &str, mode: &str, permissions: &[String], mem_mb: u32) -> bool {
    (kind.is_empty() || kind == "payment") && (mode.is_empty() || mode == "http") && permissions.is_empty() && mem_mb == 0
}

/// What protocol 2 allows, whoever names it (the catalog, the state): a known kind and mode,
/// known permissions each once, sane memory.
fn shape_2_ok(kind: &str, mode: &str, permissions: &[String], mem_mb: u32) -> bool {
    let mut seen = std::collections::BTreeSet::new();
    KINDS_2.contains(&kind)
        && MODES.contains(&mode)
        && permissions.iter().all(|p| PERMISSIONS.contains(&p.as_str()) && seen.insert(p.as_str()))
        && (mem_mb == 0 || MEM_MB.contains(&mem_mb))
}

#[derive(Deserialize)]
struct Catalog {
    adapters: Vec<Entry>,
}

/// An addon this host runs. `token` is what the panel calls the addon with; `panel_token`
/// (protocol 2) what the addon calls the panel with.
#[derive(Serialize, Deserialize, Debug, Clone, Default, PartialEq)]
pub struct Installed {
    pub version: String,
    /// Empty for an external addon, which has no image.
    pub image: String,
    pub digest: String,
    pub listen: String,
    pub token: String,
    /// States written before protocol 2 hold none of the three (protocol 0, no kind, no mode):
    /// load_from reads them as a payment adapter.
    #[serde(default)]
    pub protocol: u32,
    /// "" is the catalog; custom | external.
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub source: String,
    #[serde(default)]
    pub kind: String,
    #[serde(default)]
    pub mode: String,
    /// What the admin approved for an addon of their own (the catalog holds the official ones').
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub permissions: Vec<String>,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub panel_token: String,
    #[serde(default, skip_serializing_if = "is_zero")]
    pub mem_mb: u32,
    pub status: String,
    pub at: String,
}

#[derive(Serialize, Deserialize, Debug, Clone)]
pub struct LastRequest {
    pub id: String,
    pub action: String,
    pub state: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub error: String,
    pub at: String,
}

fn is_zero(n: &u32) -> bool {
    *n == 0
}

impl Installed {
    fn v2(&self) -> bool {
        self.protocol == PROTOCOL_2
    }
}

#[derive(Serialize, Deserialize, Debug, Default)]
pub struct State {
    #[serde(default)]
    pub adapters: BTreeMap<String, Installed>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub request: Option<LastRequest>,
    /// The loopback address the panel serves its API for addons on: given out here once, with
    /// the first addon of protocol 2, and kept.
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub panel_api: String,
    /// The same API on the gateway of the bridge custom addons run on (same port): set while
    /// one runs, for the panel to listen there too.
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub panel_api_bridge: String,
    /// The version of mikan that wrote this state: the panel holds back what needs a newer host.
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub host_version: String,
}

/// What the panel asks for. The fields after id come only with an addon of the admin's own.
#[derive(Deserialize, Debug, Default)]
struct Request {
    action: String,
    id: String,
    #[serde(default)]
    source: String,
    #[serde(default)]
    image: String,
    #[serde(default)]
    digest: String,
    #[serde(default)]
    url: String,
    #[serde(default)]
    kind: String,
    #[serde(default)]
    mode: String,
    #[serde(default)]
    permissions: Vec<String>,
    #[serde(default)]
    mem_mb: u32,
}

/// Adapter ids are path, YAML and provider-name safe (internal/panel/addons.ValidID).
pub fn valid_id(id: &str) -> bool {
    let b = id.as_bytes();
    !b.is_empty() && b.len() <= 32 && b[0] != b'-' && b.iter().all(|&c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == b'-')
}

fn valid_digest(d: &str) -> bool {
    d.strip_prefix("sha256:").is_some_and(release::is_sha256_hex)
}

/// Where the marketplace's adapters live. A signed catalog could name another registry, but
/// what root runs from a state the panel once wrote must come from this organization.
const IMAGE_PREFIX: &str = "ghcr.io/getmikan/";

fn valid_image(i: &str) -> bool {
    i.strip_prefix(IMAGE_PREFIX).is_some_and(|r| {
        !r.is_empty()
            && !r.contains("..")
            && !r.contains("//")
            && r.bytes().all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || b"._/-".contains(&b))
    })
}

/// An image the admin named: any registry, but docker's own grammar and nothing more,
/// lowercase and without a tag (the digest goes apart): [host[:port]/]path/name, so no value
/// can carry YAML. The panel checks the same (ValidImage in internal/panel/addons).
fn valid_reference(r: &str) -> bool {
    if r.is_empty() || r.len() > 255 {
        return false;
    }
    let parts: Vec<&str> = r.split('/').collect();
    // The first part is a registry when it looks like a host, as docker reads it.
    let path = match parts.split_first() {
        Some((host, rest)) if !rest.is_empty() && (host.contains(['.', ':']) || *host == "localhost") => {
            if !valid_host(host) {
                return false;
            }
            rest
        }
        _ => &parts[..],
    };
    path.iter().all(|p| valid_component(p))
}

fn valid_host(h: &str) -> bool {
    let (name, port) = match h.split_once(':') {
        Some((n, p)) => (n, Some(p)),
        None => (h, None),
    };
    port.is_none_or(|p| !p.is_empty() && p.len() <= 5 && p.bytes().all(|b| b.is_ascii_digit()))
        && !name.is_empty()
        && name.split('.').all(|l| {
            !l.is_empty()
                && !l.starts_with('-')
                && !l.ends_with('-')
                && l.bytes().all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || b == b'-')
        })
}

/// A path component: runs of a-z0-9 joined by ".", "_", "__" or any number of "-".
fn valid_component(c: &str) -> bool {
    let alnum = |b: u8| b.is_ascii_lowercase() || b.is_ascii_digit();
    let b = c.as_bytes();
    if b.is_empty() || !alnum(b[0]) || !alnum(b[b.len() - 1]) {
        return false;
    }
    let mut sep = String::new();
    for &x in b {
        if alnum(x) {
            if !(sep.is_empty() || sep == "." || sep == "_" || sep == "__" || sep.bytes().all(|s| s == b'-')) {
                return false;
            }
            sep.clear();
        } else if b"._-".contains(&x) {
            sep.push(x as char);
        } else {
            return false;
        }
    }
    true
}

/// Checks the signature over the catalog's exact bytes, then keeps what this panel can run:
/// a protocol it knows, an image by digest on ghcr.io, a panel new enough.
pub fn parse_catalog(data: &[u8], sig: &str, key: &VerifyingKey, panel: &str) -> Result<Vec<Entry>> {
    release::verify(data, sig, key, "catalog")?;
    let c: Catalog = serde_json::from_slice(data).context("the catalog is malformed")?;
    Ok(c.adapters
        .into_iter()
        .filter_map(|mut e| e.normalize().then_some(e))
        .filter(|e| valid_id(&e.id) && valid_image(&e.image) && valid_digest(&e.digest))
        .filter(|e| e.min_panel.is_empty() || panel == "dev" || !release::newer(&e.min_panel, panel))
        .collect())
}

pub fn catalog(panel: &str) -> Result<Vec<Entry>> {
    let data = net::get(CATALOG_URL, 1 << 20).context("download the marketplace catalog")?;
    let sig = net::get(&format!("{CATALOG_URL}.sig"), 4096).context("download the catalog's signature")?;
    let entries = parse_catalog(&data, &String::from_utf8_lossy(&sig), &release::key()?, panel)?;
    release::not_older(Path::new(CATALOG_SEEN), &data, "updated", "the marketplace's catalog")?;
    Ok(entries)
}

/// The newest catalog this server has accepted ("updated" of the signed index).
const CATALOG_SEEN: &str = "/opt/mikan/addons/catalog-updated";

/// The adapters as root keeps them: only entries that pass sound() survive.
pub fn load_state() -> Result<State> {
    load_from(Path::new(ROOT_STATE))
}

fn load_from(root_state: &Path) -> Result<State> {
    let raw = match fs::read(root_state) {
        Ok(b) => b,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => return Ok(State::default()),
        Err(e) => return Err(e.into()),
    };
    let mut s: State = serde_json::from_slice(&raw).context("the adapters' state is malformed")?;
    if !s.panel_api.is_empty() && !loopback(&s.panel_api) {
        eprintln!("mikan: the panel's API address {:?} in the state is not sound: ignored", s.panel_api);
        s.panel_api.clear();
    }
    if !s.panel_api_bridge.is_empty() && !bridge_api(&s.panel_api_bridge) {
        eprintln!("mikan: the panel's API address on the bridge {:?} in the state is not sound: ignored", s.panel_api_bridge);
        s.panel_api_bridge.clear();
    }
    for a in s.adapters.values_mut().filter(|a| a.protocol == 0) {
        // Written before protocol 2: a payment adapter, with no kind and no mode.
        a.protocol = PROTOCOL;
        for (field, own) in [(&mut a.kind, "payment"), (&mut a.mode, "http")] {
            if field.is_empty() {
                *field = own.into();
            }
        }
    }
    s.adapters.retain(|id, a| {
        let ok = sound(id, a);
        if !ok {
            eprintln!("mikan: the adapter {id:?} in the state is not sound: ignored");
        }
        ok
    });
    Ok(s)
}

/// root's copy first, then the panel's, which holds the tokens it calls adapters with.
fn save_state(s: &mut State) -> Result<()> {
    s.host_version = crate::version().to_owned();
    let data = serde_json::to_string_pretty(s)?;
    panelfs::write_root(Path::new(ROOT_STATE), data.as_bytes())?;
    let dir = panel_dir(true)?.context("the panel's directory for the adapters is not there")?;
    dir.write("state.json", data.as_bytes(), 0o600)
}

/// An unprivileged port, written as a number is (no sign, no leading zero).
fn port_ok(p: &str) -> bool {
    p.parse::<u16>().is_ok_and(|n| n >= 1024 && n.to_string() == p)
}

fn port_of(addr: &str) -> &str {
    addr.rsplit_once(':').map(|(_, p)| p).unwrap_or_default()
}

/// A loopback address on an unprivileged port.
fn loopback(addr: &str) -> bool {
    addr.strip_prefix("127.0.0.1:").is_some_and(port_ok)
}

/// A gateway the panel can listen on for the bridge: a plain IPv4 address of a network.
fn gateway_ok(ip: Ipv4Addr) -> bool {
    !(ip.is_unspecified() || ip.is_loopback() || ip.is_multicast() || ip.is_broadcast())
}

/// The panel's API on the bridge's gateway: <IPv4>:<port>.
fn bridge_api(addr: &str) -> bool {
    addr.rsplit_once(':').is_some_and(|(ip, p)| port_ok(p) && ip.parse::<Ipv4Addr>().is_ok_and(gateway_ok))
}

/// An external addon's address: http://127.0.0.1:<port> and nothing more, as a listen address.
fn external_listen(url: &str) -> Option<String> {
    let addr = url.strip_prefix("http://")?;
    let addr = addr.strip_suffix('/').unwrap_or(addr);
    loopback(addr).then(|| addr.to_owned())
}

fn plain_token(t: &str) -> bool {
    (16..=128).contains(&t.len()) && t.bytes().all(|b| b.is_ascii_alphanumeric())
}

/// Everything that goes into the compose file is checked here: an id, an image by digest (on
/// ghcr.io for the catalog's, docker's plain grammar for the admin's), a loopback listen
/// address, plain tokens and known words, so no value can carry YAML. A payment adapter
/// (protocol 1) carries nothing of protocol 2 and comes from the catalog only.
fn sound(id: &str, a: &Installed) -> bool {
    let version_ok = a.version.len() <= 32 && a.version.bytes().all(|b| b.is_ascii_alphanumeric() || b".-+".contains(&b));
    let protocol_ok = match a.protocol {
        PROTOCOL => plain_payment(&a.kind, &a.mode, &a.permissions, a.mem_mb) && a.panel_token.is_empty() && a.source.is_empty(),
        PROTOCOL_2 => shape_2_ok(&a.kind, &a.mode, &a.permissions, a.mem_mb) && plain_token(&a.panel_token),
        _ => false,
    };
    let source_ok = match a.source.as_str() {
        "" => valid_image(&a.image) && valid_digest(&a.digest),
        CUSTOM => valid_reference(&a.image) && valid_digest(&a.digest),
        EXTERNAL => a.image.is_empty() && a.digest.is_empty(),
        _ => false,
    };
    valid_id(id) && source_ok && loopback(&a.listen) && plain_token(&a.token) && version_ok && protocol_ok && a.status == "running"
}

/// The compose project of the adapters, hardened like the panel and with even less: no
/// capabilities at all, loopback only (the adapter refuses anything else). Compose never
/// pulls: the images are here already (fetch), so a missing one never sends it to a registry.
/// An addon of protocol 2 also gets the panel's API for addons; with no sound address for it
/// it is left out. A custom addon runs on the bridge, listens on all of its own interfaces
/// and is published on loopback only; an external one has no container. An addon runs by
/// image@digest unless `by_id` holds the ID of its loaded image (by addon id).
pub fn compose_yaml(
    adapters: &BTreeMap<String, Installed>,
    by_id: &BTreeMap<String, String>,
    panel_api: &str,
    panel_api_bridge: &str,
) -> String {
    let mut y = String::from("# Written by `mikan addon`: changes here are lost.\nname: mikan-addons\n\nservices:\n");
    let mut bridged = false;
    for (id, a) in adapters {
        let custom = a.source == CUSTOM;
        if a.source == EXTERNAL || (a.v2() && !loopback(panel_api)) || (custom && !bridge_api(panel_api_bridge)) {
            continue;
        }
        let image = by_id.get(id).cloned().unwrap_or_else(|| format!("{}@{}", a.image, a.digest));
        let mem = if a.mem_mb == 0 { DEFAULT_MEM_MB } else { a.mem_mb };
        let (network, listen, api) = if custom {
            bridged = true;
            let port = port_of(&a.listen);
            // The panel calls it on loopback as any other; a client of the panel serves nothing.
            let ports = if a.mode == "client" { String::new() } else { format!("    ports: [\"{}:{port}\"]\n", a.listen) };
            (format!("    networks: [{NETWORK}]\n{ports}"), format!("0.0.0.0:{port}"), panel_api_bridge)
        } else {
            ("    network_mode: host\n".to_owned(), a.listen.clone(), panel_api)
        };
        y.push_str(&format!(
            r#"  {id}:
    image: "{image}"
    pull_policy: never
{network}    restart: unless-stopped
    user: "65532:65532"
    cap_drop: [ALL]
    security_opt: ["no-new-privileges:true"]
    read_only: true
    tmpfs: ["/tmp:rw,size=16m"]
    mem_limit: {mem}m
    pids_limit: 64
    logging:
      driver: json-file
      options: {{max-size: "5m", max-file: "2"}}
    environment:
      MIKAN_ADAPTER_LISTEN: "{listen}"
      MIKAN_ADAPTER_TOKEN: "{token}"
"#,
            token = a.token
        ));
        if a.v2() {
            // The same token opens the Bot API proxy; the number before it means nothing
            // (frameworks want one), the bot's id comes from getMe.
            y.push_str(&format!(
                "      MIKAN_PANEL_URL: \"http://{api}/panel/v2\"\n      MIKAN_PANEL_TOKEN: \"{t}\"\n      MIKAN_BOT_API_URL: \"http://{api}\"\n      MIKAN_BOT_TOKEN: \"0:{t}\"\n",
                t = a.panel_token
            ));
        }
    }
    if bridged {
        y.push_str(&format!("\nnetworks:\n  {NETWORK}:\n    external: true\n"));
    }
    y
}

/// `docker`, whatever it answers.
fn docker_output(args: &[&str]) -> Result<Output> {
    Command::new("docker").args(args).stdin(Stdio::null()).output().with_context(|| format!("docker {}", args.join(" ")))
}

/// `docker`; fails naming the command and what it said on stderr.
fn docker(args: &[&str]) -> Result<Output> {
    docker::check(docker_output(args)?).with_context(|| format!("docker {}", args.join(" ")))
}

/// `docker compose` for the addons' project; what it wrote to stdout.
fn compose(args: &[&str]) -> Result<String> {
    let file = format!("{COMPOSE_DIR}/compose.yml");
    let all: Vec<&str> = ["compose", "-p", PROJECT, "-f", &file].into_iter().chain(args.iter().copied()).collect();
    Ok(String::from_utf8_lossy(&docker(&all)?.stdout).into_owned())
}

/// Runs what the state lists, and nothing else. Every image is fetched first; an addon whose
/// image cannot be had, or a custom one without the bridge, is left out while the rest run,
/// and the call fails naming it.
fn up(state: &mut State, say: &mut dyn FnMut(&str)) -> Result<()> {
    let dir = Path::new(COMPOSE_DIR);
    fs::create_dir_all(dir)?;
    fs::set_permissions(dir, fs::Permissions::from_mode(0o700))?;
    let mut problems = Vec::new();
    let mut running = BTreeMap::new();
    let mut by_id = BTreeMap::new();
    // An external addon has no container: nothing to fetch or run.
    for (id, a) in state.adapters.iter().filter(|(id, a)| a.source != EXTERNAL && sound(id, a)) {
        match fetch(a, say) {
            Ok(image_id) => {
                running.insert(id.clone(), a.clone());
                by_id.extend(image_id.map(|i| (id.clone(), i)));
            }
            Err(e) => problems.push(format!("{id}: {e:#}")),
        }
    }
    // The panel listens on the bridge only while a custom addon runs there.
    state.panel_api_bridge.clear();
    if running.values().any(|a| a.source == CUSTOM) {
        match bridge() {
            Ok(gateway) => state.panel_api_bridge = format!("{gateway}:{}", port_of(&state.panel_api)),
            Err(e) => {
                problems.push(format!("{e:#}"));
                running.retain(|_, a| a.source != CUSTOM);
            }
        }
    }
    // Before the addons start: they call the panel at once.
    sync_firewall(&state.panel_api_bridge, say);
    if running.is_empty() {
        if dir.join("compose.yml").exists() {
            compose(&["down", "--remove-orphans"])?;
            fs::remove_file(dir.join("compose.yml"))?;
        }
    } else {
        crate::envfile::write_private(
            &dir.join("compose.yml"),
            compose_yaml(&running, &by_id, &state.panel_api, &state.panel_api_bridge).as_bytes(),
        )?;
        compose(&["up", "-d", "--remove-orphans"])?;
    }
    if !problems.is_empty() {
        bail!("{}", problems.join("; "));
    }
    Ok(())
}

/// Makes sure the engine has the addon's image, since compose never pulls: there already,
/// loaded by `mikan addon load`, or pulled now. Gives the ID to run a loaded image by where
/// the engine keeps no digest for it (the classic image store); None runs image@digest.
fn fetch(a: &Installed, say: &mut dyn FnMut(&str)) -> Result<Option<String>> {
    let reference = format!("{}@{}", a.image, a.digest);
    if a.source == EXTERNAL || docker::image_present(&reference) {
        return Ok(None);
    }
    // Only the catalog's images are ever loaded from an archive and run by their ID.
    if a.source.is_empty()
        && let Some(id) = loaded(&a.digest)
        && docker::image_present(&id)
    {
        return Ok(Some(id));
    }
    say(&format!("Pulling {reference}"));
    docker::pull(&reference, |_| {}).map(|()| None)
}

/// The bridge of custom addons, made once: traffic between its containers is off, so one
/// addon cannot call another's port. Its gateway is where the panel serves them its API.
fn bridge() -> Result<Ipv4Addr> {
    let inspect = [
        "network",
        "inspect",
        "-f",
        r#"{{.Driver}} {{index .Options "com.docker.network.bridge.enable_icc"}} {{range .IPAM.Config}}{{.Gateway}} {{end}}"#,
        NETWORK,
    ];
    let mut out = docker_output(&inspect)?;
    if !out.status.success() {
        let name = format!("com.docker.network.bridge.name={NETWORK}");
        docker(&["network", "create", "--driver", "bridge", "-o", &name, "-o", "com.docker.network.bridge.enable_icc=false", NETWORK])
            .with_context(|| format!("create the network {NETWORK}"))?;
        out = docker(&inspect)?;
    }
    parse_network(&String::from_utf8_lossy(&out.stdout))
}

/// What `docker network inspect` says of mikan-addons: a bridge with no traffic between its
/// containers, and its IPv4 gateway. A network of that name made otherwise is not used.
fn parse_network(s: &str) -> Result<Ipv4Addr> {
    let mut words = s.split_whitespace();
    if words.next() != Some("bridge") || words.next() != Some("false") {
        bail!("the network {NETWORK} is not mikan's (a bridge without traffic between its containers): docker network rm {NETWORK}");
    }
    words
        .filter_map(|w| w.parse::<Ipv4Addr>().ok())
        .find(|ip| gateway_ok(*ip))
        .with_context(|| format!("the network {NETWORK} has no IPv4 gateway"))
}

/// The panel's API on the bridge as ufw sees it: the gateway and the port.
type Door = (Ipv4Addr, u16);

/// The door from the panel's address on the bridge as the state holds it; None if it is no
/// sound address.
pub fn door(panel_api_bridge: &str) -> Option<Door> {
    if !bridge_api(panel_api_bridge) {
        return None;
    }
    let (ip, port) = panel_api_bridge.rsplit_once(':')?;
    Some((ip.parse().ok()?, port.parse().ok()?))
}

/// The comment on our rule, for whoever reads `ufw show added`.
const RULE_COMMENT: &str = "mikan-addons";

/// The arguments of ufw for a rule that lets the bridge's containers reach the panel and
/// nothing else of the host. Without it the default `deny incoming` drops their traffic to the
/// gateway, where the panel listens for them. The values come from door(), so they are an
/// IPv4 address and a number; each goes as an argument of its own, never through a shell.
fn rule_args((ip, port): Door) -> Vec<String> {
    ["allow", "in", "on", NETWORK, "to", &ip.to_string(), "port", &port.to_string(), "proto", "tcp"].map(str::to_owned).to_vec()
}

fn allow_args(door: Door) -> Vec<String> {
    let mut args = rule_args(door);
    args.extend(["comment".to_owned(), RULE_COMMENT.to_owned()]);
    args
}

/// `--force` because ufw asks before it deletes; the comment is no part of what it matches.
fn delete_args(door: Door) -> Vec<String> {
    let mut args = vec!["--force".to_owned(), "delete".to_owned()];
    args.extend(rule_args(door));
    args
}

/// What an admin types to open the door by hand (mikan doctor).
pub fn allow_command(door: Door) -> String {
    format!("ufw {}", allow_args(door).join(" "))
}

/// The doors `ufw status` lists for the bridge: an allow rule from anywhere, into one IPv4
/// address and a TCP port, on mikan-addons. Nothing else is read, so what the admin wrote for
/// other interfaces is never touched, and a value that is no sound door is not taken.
pub fn parse_doors(status: &str) -> Vec<Door> {
    let on_bridge = |w: &[&str]| w.windows(2).any(|p| p[0] == "on" && p[1] == NETWORK);
    let mut doors = Vec::new();
    for line in status.lines() {
        let words: Vec<&str> = line.split_whitespace().collect();
        let Some(at) = words.iter().position(|w| *w == "ALLOW") else { continue };
        let (to, rest) = (&words[..at], &words[at + 1..]);
        // ufw writes the interface of an incoming rule with its destination.
        if to.len() < 2 || !(on_bridge(to) || on_bridge(rest)) {
            continue;
        }
        let rest = rest.strip_prefix(&["IN"]).unwrap_or(rest);
        if !rest.first().is_some_and(|f| f.eq_ignore_ascii_case("anywhere")) {
            continue;
        }
        let Some(port) = to[1].strip_suffix("/tcp") else { continue };
        if let Some(d) = door(&format!("{}:{port}", to[0])) {
            doors.push(d);
        }
    }
    doors
}

/// What to run so that the bridge has the door that is wanted (None: no custom addon) and no
/// other: an old one goes when the gateway or the port moved, the new one comes.
fn firewall_plan(have: &[Door], want: Option<Door>) -> Vec<Vec<String>> {
    let mut plan: Vec<Vec<String>> = Vec::new();
    for d in have.iter().filter(|d| Some(**d) != want) {
        let args = delete_args(*d);
        if !plan.contains(&args) {
            plan.push(args);
        }
    }
    if let Some(w) = want
        && !have.contains(&w)
    {
        plan.push(allow_args(w));
    }
    plan
}

/// Opens the panel's API on the bridge in ufw while a custom addon runs (panel_api_bridge is
/// set), closes it when none does, and replaces it when the gateway or the port moved. Does
/// nothing without an active ufw, or when the install keeps mikan out of it (MIKAN_UFW=0).
/// A rule that cannot be made is told, not fatal: the addons run and only cannot call the
/// panel, which doctor says.
fn sync_firewall(panel_api_bridge: &str, say: &mut dyn FnMut(&str)) {
    if crate::ops::Install::load().is_ok_and(|i| !i.ufw()) {
        return;
    }
    let Some(status) = crate::system::ufw_status() else { return };
    for args in firewall_plan(&parse_doors(&status), door(panel_api_bridge)) {
        let args: Vec<&str> = args.iter().map(String::as_str).collect();
        if let Err(e) = crate::system::run_quiet("ufw", &args) {
            say(&format!("The firewall rule for the addons' bridge was not changed: {e:#}"));
        }
    }
}

/// Waits for the addon to come up: one serving HTTP answers its info as itself (/v1/info for
/// a payment adapter, /v2/info for the rest); a client of the panel keeps running. An
/// external addon is not waited for: its process learns its tokens from the panel only now.
fn wait_started(id: &str, a: &Installed, limit: Duration) -> Result<()> {
    if a.source == EXTERNAL {
        return Ok(());
    }
    if a.v2() && a.mode == "client" {
        return wait_running(id, limit);
    }
    let agent: ureq::Agent = ureq::Agent::config_builder().timeout_global(Some(Duration::from_secs(3))).build().into();
    let (path, protocol) = if a.v2() { ("v2", PROTOCOL_2) } else { ("v1", PROTOCOL) };
    let url = format!("http://{}/{path}/info", a.listen);
    let start = Instant::now();
    let mut last = String::new();
    while start.elapsed() < limit {
        match agent.get(&url).header("authorization", &format!("Bearer {}", a.token)).call() {
            Ok(mut resp) => {
                let info: serde_json::Value =
                    resp.body_mut().read_json().with_context(|| format!("the addon's /{path}/info is malformed"))?;
                if info["id"] != id || info["protocol"] != protocol {
                    bail!("the addon answers as {} (protocol {})", info["id"], info["protocol"]);
                }
                return Ok(());
            }
            Err(e) => last = e.to_string(),
        }
        thread::sleep(Duration::from_millis(500));
    }
    bail!("the addon does not answer: {last}")
}

/// A client of the panel serves nothing to ask: it has to be running, and still be running a
/// few seconds later, so that one crashing at its start is caught.
fn wait_running(id: &str, limit: Duration) -> Result<()> {
    let start = Instant::now();
    let mut since: Option<Instant> = None;
    while start.elapsed() < limit {
        if running_services()?.iter().any(|s| s == id) {
            let first = *since.get_or_insert_with(Instant::now);
            if first.elapsed() >= Duration::from_secs(5) {
                return Ok(());
            }
        } else {
            since = None;
        }
        thread::sleep(Duration::from_millis(500));
    }
    bail!("the addon does not keep running")
}

fn running_services() -> Result<Vec<String>> {
    Ok(compose(&["ps", "--status", "running", "--services"])?.lines().map(str::to_owned).collect())
}

fn new_token() -> String {
    setup::token(48, b"abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")
}

/// A loopback address no other addon and not the panel's API holds.
fn free_listen(state: &State) -> String {
    loop {
        let listen = format!("127.0.0.1:{}", setup::free_port());
        if !taken(state, &listen, "") {
            return listen;
        }
    }
}

fn taken(state: &State, listen: &str, except: &str) -> bool {
    state.panel_api == listen || state.adapters.iter().any(|(id, a)| id != except && a.listen == listen)
}

/// "telegram 1.0.0", or the id alone for an addon of the admin's, which has no version here.
fn shown(id: &str, a: &Installed) -> String {
    if a.version.is_empty() { id.to_owned() } else { format!("{id} {}", a.version) }
}

/// Whether what runs is what is asked for: the same program, the same shape.
fn same_build(b: &Installed, n: &Installed) -> bool {
    b.source == n.source
        && b.image == n.image
        && b.digest == n.digest
        && b.kind == n.kind
        && b.mode == n.mode
        && b.mem_mb == n.mem_mb
        && b.permissions == n.permissions
        && (n.source != EXTERNAL || b.listen == n.listen)
}

/// Installs an adapter from the catalog, or updates it to the catalog's build; a build that
/// does not start leaves the one before it running.
pub fn install(id: &str, panel: &str, say: &mut dyn FnMut(&str)) -> Result<()> {
    if !valid_id(id) {
        bail!("no adapter {id:?}");
    }
    let _lock = lock::acquire(Wait::Block, say)?;
    let entry = catalog(panel)?.into_iter().find(|e| e.id == id).with_context(|| format!("the marketplace has no adapter {id}"))?;
    let next = Installed {
        version: entry.version,
        image: entry.image,
        digest: entry.digest,
        protocol: entry.protocol,
        kind: entry.kind,
        mode: entry.mode,
        mem_mb: entry.resources.mem_mb,
        ..Default::default()
    };
    put(id, next, say)
}

/// Installs what the admin named in the panel instead of the catalog: an image pinned by its
/// digest (custom) or a process of their own on a loopback port (external). Nothing signed
/// vouches for it, so all of it passes sound() before anything runs.
fn install_own(req: &Request, say: &mut dyn FnMut(&str)) -> Result<()> {
    let _lock = lock::acquire(Wait::Block, say)?;
    let mut next = Installed {
        source: req.source.clone(),
        protocol: PROTOCOL_2,
        kind: req.kind.clone(),
        mode: if req.mode.is_empty() { "http".into() } else { req.mode.clone() },
        mem_mb: req.mem_mb,
        permissions: req.permissions.clone(),
        ..Default::default()
    };
    match req.source.as_str() {
        CUSTOM => {
            next.image = req.image.clone();
            next.digest = req.digest.clone();
        }
        // A client of the panel serves nothing: it needs no address of its own.
        EXTERNAL if req.url.is_empty() && next.mode == "client" => {}
        EXTERNAL => {
            next.listen = external_listen(&req.url).with_context(|| format!("{:?} is not http://127.0.0.1:<port>", req.url))?;
        }
        other => bail!("unknown source {other:?}"),
    }
    put(&req.id, next, say)
}

/// Runs next as id. The port and the tokens stay across updates of the same program (the
/// panel reads them from the state); another source, or another image, gets its own.
fn put(id: &str, mut next: Installed, say: &mut dyn FnMut(&str)) -> Result<()> {
    let mut state = load_state()?;
    let before = state.adapters.get(id).cloned();
    if let Some(b) = &before
        && b.status == "running"
        && same_build(b, &next)
    {
        say(&format!("{} is installed already.", shown(id, b)));
        return Ok(());
    }
    let same = before.as_ref().filter(|b| b.source == next.source && (next.source != CUSTOM || b.image == next.image));
    if next.listen.is_empty() {
        next.listen = match same {
            Some(b) if b.source != EXTERNAL && !b.listen.is_empty() => b.listen.clone(),
            _ => free_listen(&state),
        };
    } else if taken(&state, &next.listen, id) {
        bail!("{} is taken by another addon or the panel's API", next.listen);
    }
    next.token = match same {
        Some(b) if !b.token.is_empty() => b.token.clone(),
        _ => new_token(),
    };
    if next.v2() {
        next.panel_token = match same {
            Some(b) if !b.panel_token.is_empty() => b.panel_token.clone(),
            _ => new_token(),
        };
        if state.panel_api.is_empty() {
            state.panel_api = free_listen(&state);
        }
    }
    next.status = "running".into();
    next.at = clock::rfc3339();
    if !sound(id, &next) {
        bail!("the request for {id} is not sound");
    }
    fetch(&next, say)?;
    state.adapters.insert(id.to_owned(), next.clone());
    let started = up(&mut state, say).and_then(|()| wait_started(id, &next, Duration::from_secs(30)));
    if let Err(e) = started {
        match before {
            Some(b) => {
                let was = if b.version.is_empty() { "the one before" } else { b.version.as_str() };
                say(&format!("{} did not start: going back to {was}", shown(id, &next)));
                state.adapters.insert(id.to_owned(), b);
            }
            None => {
                state.adapters.remove(id);
            }
        }
        let back = up(&mut state, say);
        save_state(&mut state)?;
        back?;
        return Err(e.context(format!("{} did not start", shown(id, &next))));
    }
    save_state(&mut state)?;
    say(&format!("{} runs on {}.", shown(id, &next), next.listen));
    Ok(())
}

/// Stops an adapter and forgets it; the panel keeps its settings and payments.
pub fn remove(id: &str, say: &mut dyn FnMut(&str)) -> Result<()> {
    let _lock = lock::acquire(Wait::Block, say)?;
    let mut state = load_state()?;
    if state.adapters.remove(id).is_none() {
        bail!("{id} is not installed");
    }
    let r = up(&mut state, say);
    save_state(&mut state)?;
    r?;
    say(&format!("{id} is removed."));
    Ok(())
}

/// Whether the panel asked for something. A queue that cannot be opened counts: apply
/// clears it.
pub fn pending() -> bool {
    match queue_dir() {
        Ok(Some(q)) => q.names().is_ok_and(|n| n.iter().any(|n| !n.starts_with('.'))),
        Ok(None) => false,
        Err(_) => true,
    }
}

/// The queue's requests oldest first, by the panel's number, and what is no request. A name
/// with a dot first is the panel's file in the making and is neither.
fn sort_queue(names: Vec<String>) -> (Vec<(String, String)>, Vec<String>) {
    let mut requests = Vec::new();
    let mut junk = Vec::new();
    for name in names {
        if name.starts_with('.') {
            continue;
        }
        match request_name(&name) {
            Some((seq, id)) => {
                let id = id.to_owned();
                requests.push((seq, name, id));
            }
            None => junk.push(name),
        }
    }
    requests.sort();
    (requests.into_iter().map(|(_, name, id)| (name, id)).collect(), junk)
}

/// <seq>-<id>.json: seq the panel's number (zero-padded), id the addon's.
fn request_name(name: &str) -> Option<(u64, &str)> {
    let (seq, id) = name.strip_suffix(".json")?.split_once('-')?;
    if seq.is_empty() || seq.len() > 19 || !seq.bytes().all(|b| b.is_ascii_digit()) || !valid_id(id) {
        return None;
    }
    Some((seq.parse().ok()?, id))
}

type Handler<'a> = &'a mut dyn FnMut(&Request, &mut dyn FnMut(&str)) -> Result<()>;

/// Hands the queue's requests to handle, oldest first, at most REQUESTS_PER_RUN. Each is
/// removed before it is handled, so one that fails is not tried again and again. Fails with
/// the first request that failed, after the rest were handled.
fn drain(queue: &Dir, say: &mut dyn FnMut(&str), handle: Handler) -> Result<()> {
    let mut results = Vec::new();
    for _ in 0..REQUESTS_PER_RUN {
        let (requests, junk) = sort_queue(queue.names()?);
        for name in junk {
            say(&format!("addons/{REQUESTS}/{name} is not a request, removed"));
            queue.discard(&name)?;
        }
        let Some((name, id)) = requests.into_iter().next() else { break };
        results.push(take(queue, &name, &id, say, &mut *handle));
    }
    results.into_iter().find(Result::is_err).unwrap_or(Ok(()))
}

/// Takes one request: removed first, whatever it is. It must be for the addon its name says.
fn take(queue: &Dir, name: &str, id: &str, say: &mut dyn FnMut(&str), handle: Handler) -> Result<()> {
    let data = queue.read(name, REQUEST_LIMIT);
    queue.discard(name)?;
    let data = match data {
        Ok(Some(d)) => d,
        Ok(None) => return Ok(()),
        Err(e) => {
            say(&format!("addons/{REQUESTS}/{name} is not a request, removed: {e:#}"));
            return Err(e);
        }
    };
    let req: Request = serde_json::from_slice(&data).with_context(|| format!("addons/{REQUESTS}/{name} is malformed"))?;
    if id != req.id {
        bail!("addons/{REQUESTS}/{name} asks for {:?}", req.id);
    }
    handle(&req, say)
}

/// The version of the panel installed here, whose catalog the addons come from; a node has
/// no addons.
pub fn panel_version() -> Result<String> {
    let install = crate::ops::Install::load()?;
    install.panel_only()?;
    Ok(install.version())
}

/// apply for the panel installed here.
pub fn apply_here(say: &mut dyn FnMut(&str)) -> Result<()> {
    apply(&panel_version()?, say)
}

/// Does what the panel asked for and tells it how each request went.
pub fn apply(panel: &str, say: &mut dyn FnMut(&str)) -> Result<()> {
    let _lock = lock::acquire(Wait::Block, say)?;
    let Some(dir) = panel_dir(false)? else { return Ok(()) };
    let queue = match queue_dir() {
        Ok(q) => q,
        Err(e) => {
            say(&format!("addons/{REQUESTS} is not a directory, removed: {e:#}"));
            dir.discard(REQUESTS)?;
            None
        }
    };
    let Some(queue) = queue else { return Ok(()) };
    // The id is the queued name's, which request_name checked.
    drain(&queue, say, &mut |req: &Request, say: &mut dyn FnMut(&str)| -> Result<()> {
        let result = match (req.action.as_str(), req.source.as_str()) {
            ("install", "") => install(&req.id, panel, say),
            ("install", _) => install_own(req, say),
            ("remove", _) => remove(&req.id, say),
            _ => Err(anyhow::anyhow!("unknown request {} {:?}", req.action, req.id)),
        };
        let mut state = load_state()?;
        state.request = Some(LastRequest {
            id: req.id.clone(),
            action: req.action.clone(),
            state: if result.is_ok() { "done" } else { "failed" }.into(),
            error: result.as_ref().err().map(|e| format!("{e:#}")).unwrap_or_default(),
            at: clock::rfc3339(),
        });
        save_state(&mut state)?;
        result
    })
}

/// The largest JSON taken out of an image archive (an index, a manifest).
const MEMBER_LIMIT: u64 = 4 << 20;

/// What an image archive holds, checked link by link from the digest its index.json names:
/// that blob hashes to it, and so does the manifest of this server's platform it leads to,
/// which names the config (the image's ID on the classic image store).
#[derive(Debug, PartialEq)]
struct Archived {
    digest: String,
    config: String,
}

fn archive_image(tar: &Path) -> Result<Archived> {
    let index: serde_json::Value =
        serde_json::from_slice(&tar_member(tar, "index.json")?).context("index.json in the archive is malformed")?;
    // `docker load` loads and names every image of the archive: one image, named only under
    // the marketplace's organization, so that it cannot put its own image under the name of
    // the panel's, the database's or another addon's.
    if index["manifests"].as_array().map_or(0, Vec::len) != 1 {
        bail!("the archive must hold exactly one image");
    }
    if !names_ours(&index["manifests"][0]["annotations"]) {
        bail!("the archive names its image outside {IMAGE_PREFIX}");
    }
    if let Ok(raw) = tar_member(tar, "manifest.json") {
        let docker: serde_json::Value = serde_json::from_slice(&raw).context("manifest.json in the archive is malformed")?;
        let entries = docker.as_array().map_or(&[][..], Vec::as_slice);
        let tags_ours = entries.iter().all(|e| {
            e["RepoTags"].as_array().is_none_or(|tags| tags.iter().all(|t| t.as_str().is_some_and(|t| t.starts_with(IMAGE_PREFIX))))
        });
        if entries.len() > 1 || !tags_ours {
            bail!("the archive must hold exactly one image, named only under {IMAGE_PREFIX}");
        }
    }
    let digest = index["manifests"][0]["digest"]
        .as_str()
        .filter(|d| valid_digest(d))
        .context("index.json in the archive names no image")?
        .to_owned();
    let top = blob(tar, &digest)?;
    let manifest = match top["manifests"].as_array() {
        // An index of platforms: the archive carries this server's only.
        Some(list) => {
            let arch = arch();
            let d = list
                .iter()
                .find(|m| m["platform"]["os"] == "linux" && m["platform"]["architecture"] == arch)
                .and_then(|m| m["digest"].as_str())
                .filter(|d| valid_digest(d))
                .with_context(|| format!("the archive has no image for linux/{arch}"))?;
            blob(tar, d)?
        }
        None => top,
    };
    let config =
        manifest["config"]["digest"].as_str().filter(|d| valid_digest(d)).context("the archive's manifest names no config")?.to_owned();
    Ok(Archived { digest, config })
}

/// A JSON blob of the archive, refused unless it hashes to its digest.
fn blob(tar: &Path, digest: &str) -> Result<serde_json::Value> {
    let hex = &digest["sha256:".len()..];
    let data = tar_member(tar, &format!("blobs/sha256/{hex}"))?;
    if release::sha256_hex(&data) != hex {
        bail!("{digest} in the archive does not match its content");
    }
    serde_json::from_slice(&data).with_context(|| format!("{digest} in the archive is not JSON"))
}

/// The names an archive's index gives its image, if any, are all under the marketplace's
/// organization (a bare tag, as `org.opencontainers.image.ref.name` may be, names nothing;
/// `postgres:18` would name an image).
fn names_ours(annotations: &serde_json::Value) -> bool {
    ["io.containerd.image.name", "org.opencontainers.image.ref.name"].iter().all(|k| match annotations[k].as_str() {
        None => true,
        Some(n) if !n.contains(['/', ':']) => *k == "org.opencontainers.image.ref.name",
        Some(n) => n.starts_with(IMAGE_PREFIX),
    })
}

/// One member of a tar (GNU tar reads a gzipped one too), at most MEMBER_LIMIT bytes.
fn tar_member(tar: &Path, name: &str) -> Result<Vec<u8>> {
    let mut child = Command::new("tar")
        .arg("-xOf")
        .arg(tar)
        .arg(name)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .context("tar")?;
    let mut data = Vec::new();
    child.stdout.take().context("tar has no output")?.take(MEMBER_LIMIT + 1).read_to_end(&mut data)?;
    if data.len() as u64 > MEMBER_LIMIT {
        let _ = child.kill();
        let _ = child.wait();
        bail!("{name} in the archive is larger than {MEMBER_LIMIT} bytes");
    }
    let out = child.wait_with_output()?;
    if !out.status.success() {
        bail!("no {name} in {}: {}", tar.display(), String::from_utf8_lossy(&out.stderr).trim());
    }
    Ok(data)
}

/// This server's platform as images name it.
fn arch() -> &'static str {
    match std::env::consts::ARCH {
        "x86_64" => "amd64",
        "aarch64" => "arm64",
        a => a,
    }
}

/// The catalog's addon an archive holds: the one pinned to the digest its index.json names.
fn pick(entries: Vec<Entry>, digest: &str, id: Option<&str>) -> Result<Entry> {
    entries.into_iter().find(|e| e.digest == digest && id.is_none_or(|id| e.id == id)).with_context(|| match id {
        Some(id) => format!("the archive holds {digest}: not the build of {id} the marketplace pins"),
        None => format!("the archive holds {digest}: no addon of the marketplace is pinned to it"),
    })
}

fn loaded(digest: &str) -> Option<String> {
    let m: BTreeMap<String, String> = serde_json::from_slice(&fs::read(LOADED).ok()?).ok()?;
    m.get(digest).filter(|id| valid_digest(id)).cloned()
}

fn remember_loaded(digest: &str, id: &str) -> Result<()> {
    let mut m: BTreeMap<String, String> = fs::read(LOADED).ok().and_then(|b| serde_json::from_slice(&b).ok()).unwrap_or_default();
    m.insert(digest.to_owned(), id.to_owned());
    panelfs::write_root(Path::new(LOADED), serde_json::to_string_pretty(&m)?.as_bytes())
}

/// `mikan addon load <tar>`: an addon's image from an archive (`docker save image@digest`;
/// the mikan release carries the official bot's) for a server that cannot reach the registry.
/// Taken only when the archive's index.json names the digest the signed catalog pins.
pub fn load(tar: &Path, id: Option<&str>, panel: &str, say: &mut dyn FnMut(&str)) -> Result<()> {
    if let Some(id) = id
        && !valid_id(id)
    {
        bail!("no addon {id:?}");
    }
    let _lock = lock::acquire(Wait::Block, say)?;
    let image = archive_image(tar)?;
    let entry = pick(catalog(panel)?, &image.digest, id)?;
    say(&format!("Loading {} {}", entry.id, entry.version));
    docker(&["load", "-i", &tar.to_string_lossy()])?;
    // The containerd image store knows a loaded image by the digest the archive names, the
    // classic one by its config only, and keeps no digest for it.
    let local = [&image.digest, &image.config]
        .into_iter()
        .find(|i| docker::image_present(i))
        .cloned()
        .context("docker load took the archive, but its image is not there under the digests the archive names")?;
    // A name keeps it from `docker image prune`; on the containerd store image@digest works then.
    let tag = format!("{}:loaded-{}", entry.image, &image.digest["sha256:".len()..][..12]);
    docker(&["tag", &local, &tag])?;
    remember_loaded(&image.digest, &local)?;
    say(&format!("{} {} is loaded: installed now, it does not need the registry.", entry.id, entry.version));
    Ok(())
}

/// `mikan addon list`: what runs here and what the marketplace offers.
pub fn list(panel: &str) -> Result<()> {
    let state = load_state()?;
    if state.adapters.is_empty() {
        println!("No addons installed.");
    }
    for (id, a) in &state.adapters {
        let source = if a.source.is_empty() { "official" } else { &a.source };
        println!("{id:<16} {:<10} {:<8} {source:<9} {}", a.version, a.status, a.listen);
    }
    match catalog(panel) {
        Ok(entries) => {
            println!("\nIn the marketplace:");
            for e in entries {
                let name = e.name.get("en").or_else(|| e.name.values().next()).cloned().unwrap_or_default();
                let mark = match state.adapters.get(&e.id) {
                    Some(a) if a.digest == e.digest => "installed",
                    Some(_) => "update",
                    None => "",
                };
                println!("{:<16} {:<10} {:<10} {name}", e.id, e.version, mark);
            }
        }
        Err(e) => println!("\nThe marketplace is out of reach: {e:#}"),
    }
    Ok(())
}

/// Stops every adapter (mikan uninstall, a restore); the state stays with the data.
pub fn down() {
    if Path::new(COMPOSE_DIR).join("compose.yml").exists() {
        let _ = compose(&["down", "--remove-orphans"]);
    }
    // No custom addon is left to call the panel there; a restore opens it again in resume.
    sync_firewall("", &mut crate::out);
}

/// The bridge goes with mikan (mikan uninstall), after the addons are down.
pub fn drop_network() {
    let _ = crate::system::succeeds("docker", &["network", "rm", NETWORK]);
}

/// Writes this command's version into the state the panel reads when it is not there yet:
/// the panel offers addons of protocol 2 only to a host that says it runs them, and a host
/// that never installed one would otherwise never say so.
pub fn touch(say: &mut dyn FnMut(&str)) {
    let run = || -> Result<()> {
        let _lock = lock::acquire(Wait::Block, &mut |_| {})?;
        let mut state = load_state()?;
        if state.host_version == crate::version() && panel_dir(false)?.is_some_and(|d| d.exists("state.json")) {
            return Ok(());
        }
        save_state(&mut state)
    };
    if let Err(e) = run() {
        say(&format!("Addons: the panel is not told this host's version: {e:#}"));
    }
}

/// Rebuild compose from validated state after restore; never run a compose file supplied
/// by a backup archive. Synchronize the panel's adapter tokens with root's state, and the
/// bridge's gateway, which a new server may have elsewhere.
pub fn resume(say: &mut dyn FnMut(&str)) -> Result<()> {
    let mut state = load_state()?;
    let started = up(&mut state, say);
    save_state(&mut state)?;
    started
}

#[cfg(test)]
mod tests {
    use super::*;

    // `docker load` names what the archive names: nothing outside the marketplace's organization.
    #[test]
    fn archive_names() {
        use serde_json::json;
        for ok in [
            json!({}),
            json!({"org.opencontainers.image.ref.name": "0.5.1.0-rc.6"}),
            json!({"io.containerd.image.name": "ghcr.io/getmikan/tool-telegram:0.5.1.0"}),
        ] {
            assert!(names_ours(&ok), "{ok}");
        }
        for bad in [
            json!({"io.containerd.image.name": "postgres:18-alpine"}),
            json!({"io.containerd.image.name": "ghcr.io/miroshka000/mikan:latest"}),
            json!({"org.opencontainers.image.ref.name": "postgres:18"}),
            json!({"org.opencontainers.image.ref.name": "docker.io/library/postgres"}),
        ] {
            assert!(!names_ours(&bad), "{bad}");
        }
    }
    use base64::Engine;
    use base64::engine::general_purpose::STANDARD;
    use ed25519_dalek::{Signer, SigningKey};

    fn entry(id: &str, protocol: u32, image: &str, digest: &str, min: &str) -> String {
        format!(
            r#"{{"id":"{id}","version":"1.0.0","protocol":{protocol},"image":"{image}","digest":"{digest}","min_panel":"{min}","name":{{"en":"X"}},"homepage":"h"}}"#
        )
    }

    /// A catalog of these entries and its signature.
    fn signed(signer: &SigningKey, entries: &[String]) -> (Vec<u8>, String) {
        let data = format!(r#"{{"version":1,"updated":"2026-10-11T00:00:00Z","adapters":[{}]}}"#, entries.join(",")).into_bytes();
        let sig = STANDARD.encode(signer.sign(&data).to_bytes());
        (data, sig)
    }

    #[test]
    fn signed_catalog_only() {
        let signer = SigningKey::from_bytes(&[9; 32]);
        let key = signer.verifying_key();
        let d = format!("sha256:{}", "c".repeat(64));
        let (data, sig) = signed(
            &signer,
            &[
                entry("yookassa", 1, "ghcr.io/getmikan/adapter-yookassa", &d, "0.4.3"),
                entry("future", 2, "ghcr.io/getmikan/adapter-future", &d, ""),
                entry("hub", 1, "docker.io/someone/adapter", &d, ""),
                entry("tagged", 1, "ghcr.io/getmikan/adapter-x", "latest", ""),
                entry("Bad", 1, "ghcr.io/getmikan/adapter-x", &d, ""),
                entry("newer", 1, "ghcr.io/getmikan/adapter-x", &d, "9.0.0"),
            ],
        );
        let got = parse_catalog(&data, &format!("{sig}\n"), &key, "0.4.3").unwrap();
        assert_eq!(got.iter().map(|e| e.id.as_str()).collect::<Vec<_>>(), ["yookassa"]);
        assert!(parse_catalog(&data, &sig, &key, "0.4.2").unwrap().is_empty());
        let mut tampered = data.clone();
        tampered[20] ^= 1;
        assert!(parse_catalog(&tampered, &sig, &key, "0.4.3").is_err());
        assert!(parse_catalog(&data, &sig, &SigningKey::from_bytes(&[8; 32]).verifying_key(), "0.4.3").is_err());
    }

    #[test]
    fn an_old_catalog_is_not_taken_again() {
        let d = tmpdir("catalog-seen");
        let seen = d.join("addons/catalog-updated");
        let cat = |u: &str| format!(r#"{{"version":1,"updated":"{u}","adapters":[]}}"#).into_bytes();
        let not_older = |doc: &[u8]| release::not_older(&seen, doc, "updated", "the catalog");
        not_older(&cat("2026-10-01T17:01:18Z")).unwrap();
        not_older(&cat("2026-10-01T17:01:18Z")).unwrap();
        not_older(&cat("2026-10-02T00:00:00Z")).unwrap();
        assert!(not_older(&cat("2026-10-01T17:01:18Z")).is_err(), "a rollback to a catalog of the day before");
        assert_eq!(fs::read_to_string(&seen).unwrap(), "2026-10-02T00:00:00Z");
        not_older(b"{}").unwrap();
        fs::remove_dir_all(&d).unwrap();
    }

    const STATUS: &str = "Status: active

To                         Action      From
--                         ------      ----
22/tcp                     ALLOW       Anywhere
172.18.0.1 21355/tcp on mikan-addons ALLOW       Anywhere
172.18.0.1 21355/tcp on mikan-addons ALLOW       Anywhere # mikan-addons
fe80::1 21355/tcp on mikan-addons ALLOW       Anywhere (v6)
172.18.0.1 80/udp on mikan-addons ALLOW       Anywhere
172.18.0.1 22/tcp on mikan-addons ALLOW       Anywhere
172.18.0.1 9000/tcp on mikan-addons DENY       Anywhere
172.18.0.1 9001/tcp on mikan-addons ALLOW OUT   Anywhere
172.18.0.1 9002/tcp on docker0 ALLOW       Anywhere
172.18.0.1 9003/tcp on mikan-addons ALLOW       203.0.113.5
10.0.0.1 9004/tcp ALLOW IN    Anywhere on mikan-addons
";

    #[test]
    fn the_door_is_the_state_address() {
        assert_eq!(door("172.18.0.1:21355"), Some((Ipv4Addr::new(172, 18, 0, 1), 21355)));
        for bad in [
            "",
            "172.18.0.1",
            "172.18.0.1:80",
            "172.18.0.1:021355",
            "0.0.0.0:21355",
            "127.0.0.1:21355",
            "fe80::1:21355",
            "x; ufw reset:21355",
        ] {
            assert_eq!(door(bad), None, "{bad}");
        }
    }

    #[test]
    fn the_rule_goes_as_separate_arguments() {
        let d = door("172.18.0.1:21355").unwrap();
        assert_eq!(
            allow_args(d),
            ["allow", "in", "on", "mikan-addons", "to", "172.18.0.1", "port", "21355", "proto", "tcp", "comment", "mikan-addons"]
        );
        assert_eq!(
            delete_args(d),
            ["--force", "delete", "allow", "in", "on", "mikan-addons", "to", "172.18.0.1", "port", "21355", "proto", "tcp"]
        );
        assert_eq!(allow_command(d), "ufw allow in on mikan-addons to 172.18.0.1 port 21355 proto tcp comment mikan-addons");
    }

    #[test]
    fn only_our_doors_are_read_from_ufw_status() {
        // allow rules from anywhere into an address and a TCP port on the bridge, nothing else:
        // not other interfaces, denials, outgoing rules, rules for one address, IPv6, UDP, or
        // a port that is not unprivileged
        assert_eq!(
            parse_doors(STATUS),
            [(Ipv4Addr::new(172, 18, 0, 1), 21355), (Ipv4Addr::new(172, 18, 0, 1), 21355), (Ipv4Addr::new(10, 0, 0, 1), 9004)]
        );
        assert_eq!(parse_doors("Status: inactive\n"), Vec::new());
    }

    #[test]
    fn the_firewall_follows_the_bridge() {
        let a = (Ipv4Addr::new(172, 18, 0, 1), 21355);
        let moved = (Ipv4Addr::new(172, 19, 0, 1), 21355);
        let rerouted = (Ipv4Addr::new(172, 18, 0, 1), 31000);
        // a custom addon starts: the door opens
        assert_eq!(firewall_plan(&[], Some(a)), [allow_args(a)]);
        // another run: nothing to do (idempotent)
        assert!(firewall_plan(&[a], Some(a)).is_empty());
        // the gateway or the port changed: the old rule goes, the new comes
        assert_eq!(firewall_plan(&[a], Some(moved)), [delete_args(a), allow_args(moved)]);
        assert_eq!(firewall_plan(&[a], Some(rerouted)), [delete_args(a), allow_args(rerouted)]);
        // the last custom addon is gone: the door closes, once however many copies there are
        assert_eq!(firewall_plan(&[a, a], None), [delete_args(a)]);
        assert!(firewall_plan(&[], None).is_empty());
        // a stale rule beside the right one is removed, the right one stays
        assert_eq!(firewall_plan(&[moved, a], Some(a)), [delete_args(moved)]);
    }

    #[test]
    fn ids() {
        for ok in ["yookassa", "a", "pay-2"] {
            assert!(valid_id(ok), "{ok}");
        }
        for bad in ["", "-x", "Yoo", "a/b", "a b", "../x", &"x".repeat(33)] {
            assert!(!valid_id(bad), "{bad}");
        }
    }

    // The panel (Go) reads the same state: the field names are the contract.
    #[test]
    fn state_matches_the_panel() {
        let mut s = State::default();
        s.adapters.insert(
            "yookassa".into(),
            Installed {
                version: "1.0.0".into(),
                image: "ghcr.io/getmikan/adapter-yookassa".into(),
                digest: "sha256:1".into(),
                listen: "127.0.0.1:41873".into(),
                token: "t".into(),
                protocol: PROTOCOL,
                kind: "payment".into(),
                mode: "http".into(),
                status: "running".into(),
                at: "2026-10-01T00:00:00Z".into(),
                ..Default::default()
            },
        );
        s.request = Some(LastRequest {
            id: "yookassa".into(),
            action: "install".into(),
            state: "done".into(),
            error: String::new(),
            at: "x".into(),
        });
        let v: serde_json::Value = serde_json::to_value(&s).unwrap();
        let a = &v["adapters"]["yookassa"];
        for k in ["version", "digest", "listen", "token", "protocol", "kind", "mode", "status", "at"] {
            assert!(a.get(k).is_some(), "{k}");
        }
        assert_eq!((a["protocol"].as_u64(), a["kind"].as_str(), a["mode"].as_str()), (Some(1), Some("payment"), Some("http")));
        assert_eq!(v["request"]["state"], "done");
        let go = fs::read_to_string("../internal/panel/addons/addons.go").unwrap();
        for tag in [
            "`json:\"listen\"`",
            "`json:\"token\"`",
            "`json:\"status\"`",
            "`json:\"adapters\"`",
            "`json:\"request,omitempty\"`",
            "`json:\"state\"`",
            "`json:\"protocol,omitempty\"`",
            "`json:\"kind,omitempty\"`",
            "`json:\"mode,omitempty\"`",
            "`json:\"panel_token,omitempty\"`",
            "`json:\"panel_api,omitempty\"`",
            "`json:\"host_version,omitempty\"`",
            "`json:\"panel_api_bridge,omitempty\"`",
            "`json:\"source,omitempty\"`",
            "`json:\"permissions,omitempty\"`",
        ] {
            assert!(go.contains(tag), "the panel has no {tag}");
        }
        assert!(go.contains(&format!("const CatalogURL = \"{CATALOG_URL}\"")), "the panel's catalog URL differs");
    }

    // A state written by a compromised panel cannot smuggle YAML into the compose file
    // root runs: every field that goes there is checked.
    #[test]
    fn only_sound_entries() {
        let good = Installed {
            version: "1.0.1".into(),
            image: "ghcr.io/getmikan/adapter-yookassa".into(),
            digest: format!("sha256:{}", "c".repeat(64)),
            listen: "127.0.0.1:45615".into(),
            token: "a".repeat(48),
            protocol: PROTOCOL,
            kind: "payment".into(),
            mode: "http".into(),
            status: "running".into(),
            ..Default::default()
        };
        assert!(sound("yookassa", &good));
        let bad = |f: &dyn Fn(&mut Installed)| {
            let mut a = good.clone();
            f(&mut a);
            a
        };
        let cases = [
            (
                "image",
                bad(&|a| {
                    a.image = "ghcr.io/x\"
    privileged: true
    x: \""
                        .into()
                }),
            ),
            ("image elsewhere", bad(&|a| a.image = "docker.io/library/alpine".into())),
            ("another organization", bad(&|a| a.image = "ghcr.io/someone/adapter-yookassa".into())),
            ("a way out of the organization", bad(&|a| a.image = "ghcr.io/getmikan/../someone/x".into())),
            (
                "digest",
                bad(&|a| {
                    a.digest = "sha256:abc
    volumes: [\"/:/host\"]"
                        .into()
                }),
            ),
            ("listen", bad(&|a| a.listen = "0.0.0.0:45615".into())),
            ("low port", bad(&|a| a.listen = "127.0.0.1:22".into())),
            (
                "token",
                bad(&|a| {
                    a.token = format!(
                        "{}\"
    privileged: true",
                        "a".repeat(40)
                    )
                }),
            ),
            ("short token", bad(&|a| a.token = "abc".into())),
            (
                "version",
                bad(&|a| {
                    a.version = "1
"
                    .into()
                }),
            ),
            ("status", bad(&|a| a.status = "hacked".into())),
        ];
        for (what, a) in cases {
            assert!(!sound("yookassa", &a), "{what} passed");
        }
        assert!(
            !sound(
                "x:
  privileged",
                &good
            ),
            "a service name with YAML passed"
        );
        let mut m = BTreeMap::new();
        m.insert(
            "evil".to_owned(),
            bad(&|a| {
                a.image = "ghcr.io/x
    privileged: true"
                    .into()
            }),
        );
        m.insert("yookassa".to_owned(), good.clone());
        let mut s = State { adapters: m, ..Default::default() };
        s.adapters.retain(|id, a| sound(id, a));
        assert_eq!(s.adapters.keys().collect::<Vec<_>>(), ["yookassa"]);
        assert!(!yaml(&s.adapters, "", "").contains("privileged"));
    }

    /// compose_yaml with every image run by image@digest.
    fn yaml(adapters: &BTreeMap<String, Installed>, panel_api: &str, panel_api_bridge: &str) -> String {
        compose_yaml(adapters, &BTreeMap::new(), panel_api, panel_api_bridge)
    }

    fn tmpdir(name: &str) -> std::path::PathBuf {
        let d = std::env::temp_dir().join(format!("addon-{name}-{}", std::process::id()));
        let _ = fs::remove_dir_all(&d);
        fs::create_dir_all(&d).unwrap();
        d
    }

    // A state written before protocol 2 has no protocol, kind or mode, and the fields since
    // dropped (image_id, error) are ignored. It is read as it was meant and written the new way.
    #[test]
    fn an_old_state_is_read_as_a_payment_adapter() {
        let d = tmpdir("old-state");
        let root_state = d.join("state.json");
        let digest = format!("sha256:{}", "c".repeat(64));
        let entry = |image: &str, status: &str, extra: &str| {
            format!(
                r#"{{"version":"1.0.0","image":"{image}","digest":"{digest}","listen":"127.0.0.1:45615","token":"{}","status":"{status}","at":"x"{extra}}}"#,
                "a".repeat(48)
            )
        };
        let state = format!(
            r#"{{"adapters":{{"yookassa":{},"loaded":{},"elsewhere":{},"failed":{}}}}}"#,
            entry("ghcr.io/getmikan/adapter-yookassa", "running", ""),
            entry("ghcr.io/getmikan/adapter-cryptobot", "running", &format!(r#","image_id":"sha256:{}","error":"x""#, "e".repeat(64))),
            entry("docker.io/library/alpine", "running", ""),
            entry("ghcr.io/getmikan/adapter-stars", "failed", "")
        );
        fs::write(&root_state, state).unwrap();
        let s = load_from(&root_state).unwrap();
        assert_eq!(s.adapters.keys().collect::<Vec<_>>(), ["loaded", "yookassa"]);
        for a in s.adapters.values() {
            assert_eq!((a.protocol, a.kind.as_str(), a.mode.as_str()), (PROTOCOL, "payment", "http"));
        }
        assert!(load_from(&d.join("none.json")).unwrap().adapters.is_empty(), "no state is no adapters");
        let written = serde_json::to_value(&s).unwrap();
        assert_eq!(written["adapters"]["yookassa"]["protocol"], 1);
        assert!(written["adapters"]["loaded"].get("image_id").is_none());
        fs::remove_dir_all(&d).unwrap();
    }

    #[test]
    fn compose_is_hardened() {
        let mut m = BTreeMap::new();
        m.insert(
            "yookassa".to_owned(),
            Installed {
                image: "ghcr.io/getmikan/adapter-yookassa".into(),
                digest: format!("sha256:{}", "c".repeat(64)),
                listen: "127.0.0.1:41873".into(),
                token: "tok".into(),
                status: "running".into(),
                ..Default::default()
            },
        );
        let y = yaml(&m, "", "");
        for want in [
            "name: mikan-addons",
            "  yookassa:\n",
            &format!("image: \"ghcr.io/getmikan/adapter-yookassa@sha256:{}\"", "c".repeat(64)),
            "user: \"65532:65532\"",
            "cap_drop: [ALL]",
            "read_only: true",
            "no-new-privileges:true",
            "MIKAN_ADAPTER_LISTEN: \"127.0.0.1:41873\"",
            "MIKAN_ADAPTER_TOKEN: \"tok\"",
            "pull_policy: never",
            "network_mode: host",
        ] {
            assert!(y.contains(want), "no {want} in\n{y}");
        }
        assert!(!y.contains("cap_add"), "an adapter needs no capability");
    }

    // Protocol 2 brings the other kinds, each with a mode, known permissions and sane memory;
    // a payment adapter stays protocol 1 and carries none of it. Same rules as the panel's
    // TestParseCatalogProtocol2.
    #[test]
    fn protocol_2_entries() {
        let signer = SigningKey::from_bytes(&[9; 32]);
        let d = format!("sha256:{}", "c".repeat(64));
        let e = |id: &str, protocol: u32, extra: &str| {
            format!(r#"{{"id":"{id}","version":"1.0.0","protocol":{protocol},"image":"ghcr.io/getmikan/{id}","digest":"{d}"{extra}}}"#)
        };
        let entries = [
            e("telegram", 2, r#","kind":"bot","mode":"http","permissions":["bot","kv"],"resources":{"mem_mb":128}"#),
            e("script", 2, r#","kind":"bot","mode":"client""#),
            e("page", 2, r#","kind":"ui""#),
            e("yookassa", 1, ""),
            e("no-kind", 2, ""),
            e("pay-v2", 2, r#","kind":"payment""#),
            e("odd-mode", 2, r#","kind":"bot","mode":"socket""#),
            e("root", 2, r#","kind":"bot","permissions":["admin"]"#),
            e("twice", 2, r#","kind":"bot","permissions":["kv","kv"]"#),
            e("greedy", 2, r#","kind":"bot","resources":{"mem_mb":4096}"#),
            e("tiny", 2, r#","kind":"bot","resources":{"mem_mb":8}"#),
            e("v1-bot", 1, r#","kind":"bot""#),
            e("v1-perms", 1, r#","permissions":["kv"]"#),
            e("v3", 3, r#","kind":"bot""#),
        ];
        let (data, sig) = signed(&signer, &entries);
        let got = parse_catalog(&data, &sig, &signer.verifying_key(), "dev").unwrap();
        assert_eq!(got.iter().map(|e| e.id.as_str()).collect::<Vec<_>>(), ["telegram", "script", "page", "yookassa"]);
        assert_eq!(got[2].mode, "http", "a protocol 2 entry without a mode serves HTTP");
        assert_eq!((got[3].kind.as_str(), got[3].mode.as_str()), ("payment", "http"));
    }

    fn bot() -> Installed {
        Installed {
            version: "1.0.0".into(),
            image: "ghcr.io/getmikan/telegram".into(),
            digest: format!("sha256:{}", "c".repeat(64)),
            listen: "127.0.0.1:45616".into(),
            token: "a".repeat(48),
            protocol: PROTOCOL_2,
            kind: "bot".into(),
            mode: "http".into(),
            panel_token: "b".repeat(48),
            mem_mb: 192,
            status: "running".into(),
            ..Default::default()
        }
    }

    fn bot_v1() -> Installed {
        Installed { protocol: PROTOCOL, kind: "payment".into(), mode: "http".into(), panel_token: String::new(), mem_mb: 0, ..bot() }
    }

    // A state the panel could have touched cannot turn a payment adapter into something it
    // is not, nor smuggle YAML through the fields of protocol 2.
    #[test]
    fn protocol_2_soundness() {
        assert!(sound("telegram", &bot()));
        assert!(sound("telegram", &bot_v1()));
        let cases = [
            ("no panel token", Installed { panel_token: String::new(), ..bot() }),
            ("panel token with YAML", Installed { panel_token: format!("{}\"\n    privileged: true", "b".repeat(40)), ..bot() }),
            ("kind", Installed { kind: "payment".into(), ..bot() }),
            ("unknown kind", Installed { kind: "bot\n    privileged: true".into(), ..bot() }),
            ("mode", Installed { mode: "socket".into(), ..bot() }),
            ("memory", Installed { mem_mb: 100_000, ..bot() }),
            ("protocol", Installed { protocol: 3, ..bot() }),
            ("a payment adapter with a panel token", Installed { panel_token: "b".repeat(48), ..bot_v1() }),
            ("a payment adapter with memory", Installed { mem_mb: 256, ..bot_v1() }),
        ];
        for (what, a) in cases {
            assert!(!sound("telegram", &a), "{what} passed");
        }
    }

    // An addon of protocol 2 gets the panel's API and its own memory; without a sound address
    // for the API it is not started at all.
    #[test]
    fn compose_of_protocol_2() {
        let mut m = BTreeMap::new();
        m.insert("telegram".to_owned(), bot());
        let y = yaml(&m, "127.0.0.1:41990", "");
        for want in [
            "  telegram:\n",
            "mem_limit: 192m",
            "MIKAN_PANEL_URL: \"http://127.0.0.1:41990/panel/v2\"",
            &format!("MIKAN_PANEL_TOKEN: \"{}\"", "b".repeat(48)),
            "MIKAN_BOT_API_URL: \"http://127.0.0.1:41990\"",
            &format!("MIKAN_BOT_TOKEN: \"0:{}\"", "b".repeat(48)),
            "network_mode: host",
            "read_only: true",
        ] {
            assert!(y.contains(want), "no {want} in\n{y}");
        }
        for api in ["", "0.0.0.0:41990", "127.0.0.1:80", "127.0.0.1:1\"\n    privileged: true"] {
            assert!(!yaml(&m, api, "").contains("telegram"), "started with the panel's API at {api:?}");
        }
    }

    // The panel's API address comes from root's state only when it is a loopback address.
    #[test]
    fn panel_api_address() {
        let d = tmpdir("panel-api");
        let root_state = d.join("state.json");
        fs::write(&root_state, r#"{"adapters":{},"panel_api":"0.0.0.0:41990"}"#).unwrap();
        assert_eq!(load_from(&root_state).unwrap().panel_api, "");
        fs::write(&root_state, r#"{"adapters":{},"panel_api":"127.0.0.1:41990"}"#).unwrap();
        assert_eq!(load_from(&root_state).unwrap().panel_api, "127.0.0.1:41990");
        fs::remove_dir_all(&d).unwrap();
    }

    // An image the admin names is any registry's, but in docker's plain grammar only: no tag,
    // no uppercase, nothing YAML could read as syntax.
    #[test]
    fn references_of_custom_images() {
        for ok in [
            "alpine",
            "someone/bot",
            "docker.io/library/alpine",
            "ghcr.io/someone/my-bot",
            "localhost:5000/bot",
            "registry.example.com:443/a/b_c/d__e/f.g",
            "a--b",
        ] {
            assert!(valid_reference(ok), "{ok}");
        }
        for bad in [
            "",
            "Alpine",
            "alpine:latest",
            "ghcr.io/someone/bot:1.0",
            "x/../y",
            "a//b",
            "/a",
            "a/",
            "-a",
            "a-",
            "a..b",
            "a___b",
            "a._b",
            "host:abc/x",
            "Host.io/x",
            "a b",
            "x\"\n    privileged: true",
            "x@sha256:aa",
            &"a".repeat(256),
        ] {
            assert!(!valid_reference(bad), "{bad:?}");
        }
    }

    fn custom() -> Installed {
        Installed {
            image: "ghcr.io/someone/my-bot".into(),
            digest: format!("sha256:{}", "d".repeat(64)),
            listen: "127.0.0.1:45617".into(),
            source: CUSTOM.into(),
            permissions: vec!["bot".into(), "kv".into()],
            version: String::new(),
            ..bot()
        }
    }

    fn external() -> Installed {
        Installed {
            image: String::new(),
            digest: String::new(),
            listen: "127.0.0.1:45618".into(),
            source: EXTERNAL.into(),
            version: String::new(),
            ..bot()
        }
    }

    // What the admin names in the panel is checked like the catalog's, and cannot pass for a
    // payment adapter or carry YAML through the new fields.
    #[test]
    fn custom_and_external_soundness() {
        assert!(sound("my-bot", &custom()));
        assert!(sound("mine", &external()));
        let cases = [
            ("a tag", Installed { image: "someone/bot:latest".into(), ..custom() }),
            ("YAML in the image", Installed { image: "x\"\n    privileged: true\n    y: \"".into(), ..custom() }),
            ("no digest", Installed { digest: String::new(), ..custom() }),
            ("a digest with YAML", Installed { digest: "sha256:abc\n    privileged: true".into(), ..custom() }),
            (
                "a custom payment adapter",
                Installed {
                    protocol: PROTOCOL,
                    kind: "payment".into(),
                    mode: "http".into(),
                    panel_token: String::new(),
                    mem_mb: 0,
                    permissions: vec![],
                    ..custom()
                },
            ),
            ("an unknown permission", Installed { permissions: vec!["root".into()], ..custom() }),
            ("a permission twice", Installed { permissions: vec!["kv".into(), "kv".into()], ..custom() }),
            ("an unknown source", Installed { source: "elsewhere".into(), ..custom() }),
            ("an external one with an image", Installed { image: "alpine".into(), ..external() }),
            ("an external one off loopback", Installed { listen: "0.0.0.0:45618".into(), ..external() }),
            ("a payment adapter with permissions", Installed { permissions: vec!["kv".into()], ..bot_v1() }),
        ];
        for (what, a) in cases {
            assert!(!sound("my-bot", &a), "{what} passed");
        }
    }

    // The text of one service of a compose file: its line and the lines indented below it.
    fn service<'a>(y: &'a str, id: &str) -> &'a str {
        let start = y.find(&format!("\n  {id}:\n")).unwrap_or_else(|| panic!("no {id} in\n{y}")) + 1;
        let rest = &y[start..];
        let end = rest.match_indices('\n').map(|(i, _)| i).find(|&i| !rest[i + 1..].starts_with("   ")).unwrap_or(rest.len());
        &rest[..end]
    }

    // A custom addon runs on the bridge, published on loopback only, and calls the panel at
    // the gateway; an external one has no service; every service runs only what is here.
    #[test]
    fn compose_of_custom_and_external() {
        let mut m = BTreeMap::new();
        m.insert("telegram".to_owned(), bot());
        m.insert("my-bot".to_owned(), custom());
        m.insert("my-client".to_owned(), Installed { mode: "client".into(), listen: "127.0.0.1:45619".into(), ..custom() });
        m.insert("mine".to_owned(), external());
        let y = yaml(&m, "127.0.0.1:41990", "172.18.0.1:41990");
        let mine = service(&y, "my-bot");
        for want in [
            &format!("image: \"ghcr.io/someone/my-bot@sha256:{}\"", "d".repeat(64)),
            "pull_policy: never",
            "networks: [mikan-addons]",
            "ports: [\"127.0.0.1:45617:45617\"]",
            "MIKAN_ADAPTER_LISTEN: \"0.0.0.0:45617\"",
            "MIKAN_PANEL_URL: \"http://172.18.0.1:41990/panel/v2\"",
            "cap_drop: [ALL]",
            "read_only: true",
        ] {
            assert!(mine.contains(want), "no {want} in\n{mine}");
        }
        assert!(!mine.contains("network_mode"), "a custom addon is not on the host's network:\n{mine}");
        let client = service(&y, "my-client");
        assert!(client.contains("networks: [mikan-addons]") && !client.contains("ports:"), "a client serves nothing:\n{client}");
        let official = service(&y, "telegram");
        assert!(official.contains("network_mode: host") && official.contains("http://127.0.0.1:41990/panel/v2"), "{official}");
        assert!(!y.contains("  mine:"), "an external addon has no container:\n{y}");
        assert_eq!(y.matches("pull_policy: never").count(), 3, "every service:\n{y}");
        assert!(y.ends_with("\nnetworks:\n  mikan-addons:\n    external: true\n"), "{y}");
        // no sound address on the bridge: the custom ones wait, the rest runs
        for bridge in ["", "127.0.0.1:41990", "0.0.0.0:41990", "172.18.0.1:80", "172.18.0.1:41990\"\n    privileged: true"] {
            let y = yaml(&m, "127.0.0.1:41990", bridge);
            assert!(!y.contains("my-bot") && !y.contains("networks:") && y.contains("  telegram:"), "{bridge:?}:\n{y}");
        }
        // a loaded image runs by its ID
        let loaded = BTreeMap::from([("telegram".to_owned(), format!("sha256:{}", "e".repeat(64)))]);
        let y = compose_yaml(&m, &loaded, "127.0.0.1:41990", "");
        assert!(service(&y, "telegram").contains(&format!("image: \"sha256:{}\"", "e".repeat(64))), "{y}");
    }

    #[test]
    fn addresses() {
        assert_eq!(external_listen("http://127.0.0.1:45618").as_deref(), Some("127.0.0.1:45618"));
        assert_eq!(external_listen("http://127.0.0.1:45618/").as_deref(), Some("127.0.0.1:45618"));
        for bad in [
            "",
            "127.0.0.1:45618",
            "https://127.0.0.1:45618",
            "http://localhost:45618",
            "http://0.0.0.0:45618",
            "http://127.0.0.1:22",
            "http://127.0.0.1:45618/path",
            "http://127.0.0.1:+45618",
            "http://127.0.0.1:045618",
            "http://127.0.0.1:45618\"\n    privileged: true",
        ] {
            assert_eq!(external_listen(bad), None, "{bad:?}");
        }
        assert!(bridge_api("172.18.0.1:41990"));
        for bad in ["", "172.18.0.1", "0.0.0.0:41990", "127.0.0.1:41990", "224.0.0.1:41990", "172.18.0.1:80", "[fd00::1]:41990", "x:41990"]
        {
            assert!(!bridge_api(bad), "{bad:?}");
        }
        assert!(!loopback("127.0.0.1:+8080") && !loopback("127.0.0.1:08080") && loopback("127.0.0.1:8080"));
    }

    // A network called mikan-addons is used only when it is the bridge mikan makes.
    #[test]
    fn the_bridge_network() {
        assert_eq!(parse_network("bridge false 172.18.0.1 \n").unwrap(), Ipv4Addr::new(172, 18, 0, 1));
        assert_eq!(parse_network("bridge false fd00::1 172.19.0.1 ").unwrap(), Ipv4Addr::new(172, 19, 0, 1));
        for bad in [
            "bridge true 172.18.0.1",
            "bridge <no value> 172.18.0.1",
            "macvlan false 172.18.0.1",
            "bridge false ",
            "bridge false 0.0.0.0",
            "",
        ] {
            assert!(parse_network(bad).is_err(), "{bad:?}");
        }
        let d = tmpdir("bridge-api");
        let root_state = d.join("state.json");
        fs::write(&root_state, r#"{"adapters":{},"panel_api":"127.0.0.1:41990","panel_api_bridge":"0.0.0.0:41990"}"#).unwrap();
        assert_eq!(load_from(&root_state).unwrap().panel_api_bridge, "");
        fs::write(&root_state, r#"{"adapters":{},"panel_api":"127.0.0.1:41990","panel_api_bridge":"172.18.0.1:41990"}"#).unwrap();
        assert_eq!(load_from(&root_state).unwrap().panel_api_bridge, "172.18.0.1:41990");
        fs::remove_dir_all(&d).unwrap();
    }

    #[test]
    fn queue_names() {
        assert_eq!(request_name("0000000001-telegram.json"), Some((1, "telegram")));
        assert_eq!(request_name("12-pay-2.json"), Some((12, "pay-2")));
        for bad in
            ["x-a.json", "1-A.json", "1-a.yaml", "-a.json", "1-.json", "1-a", "+1-a.json", "12345678901234567890-a.json", "1-../a.json"]
        {
            assert_eq!(request_name(bad), None, "{bad}");
        }
        let names = ["0000000010-c.json", "0000000002-b.json", "9-a.json", ".0000000011-d.json.123.tmp", "junk", "x-y.json"];
        let (requests, junk) = sort_queue(names.iter().map(|s| s.to_string()).collect());
        assert_eq!(
            requests,
            [("0000000002-b.json".to_owned(), "b".to_owned()), ("9-a.json".into(), "a".into()), ("0000000010-c.json".into(), "c".into())]
        );
        assert_eq!(junk, ["junk", "x-y.json"], "the panel's file in the making is not junk");
    }

    // The requests go oldest first; each is gone before it is handled, one that fails does not
    // hold the others, a request must be for the addon its name says, and what is no request
    // is removed.
    #[test]
    fn the_queue_is_taken_in_order() {
        let d = tmpdir("queue");
        fs::create_dir_all(d.join("addons/requests")).unwrap();
        let req = |id: &str| format!(r#"{{"action":"install","id":"{id}","at":"2026-10-11T00:00:00Z"}}"#);
        fs::write(d.join("addons/requests/0000000002-b.json"), req("b")).unwrap();
        fs::write(d.join("addons/requests/0000000001-a.json"), req("a")).unwrap();
        fs::write(d.join("addons/requests/0000000003-c.json"), req("other")).unwrap();
        fs::write(
            d.join("addons/requests/0000000004-mine.json"),
            r#"{"action":"install","id":"mine","source":"custom","image":"someone/bot","digest":"sha256:1","kind":"bot","mode":"client","permissions":["kv"],"mem_mb":64}"#,
        )
        .unwrap();
        fs::write(d.join("addons/requests/junk"), "x").unwrap();
        fs::create_dir(d.join("addons/requests/0000000005-dir.json")).unwrap();
        fs::write(d.join("addons/requests/.0000000006-e.json.1.tmp"), req("e")).unwrap();
        let queue = Dir::open(&d, "addons/requests", false).unwrap().unwrap();
        let mut seen = Vec::new();
        let mut said = Vec::new();
        let r = drain(&queue, &mut |l: &str| said.push(l.to_owned()), &mut |req: &Request, _: &mut dyn FnMut(&str)| {
            let queued = fs::read_dir(d.join("addons/requests"))
                .unwrap()
                .any(|e| e.unwrap().file_name().to_string_lossy().ends_with(&format!("-{}.json", req.id)));
            assert!(!queued, "{} is removed before it is handled", req.id);
            seen.push(format!("{} {} {}", req.action, req.id, req.source));
            if req.id == "a" { Err(anyhow::anyhow!("a failed")) } else { Ok(()) }
        });
        assert_eq!(seen, ["install a ", "install b ", "install mine custom"]);
        assert_eq!(format!("{:#}", r.unwrap_err()), "a failed", "the first failure, after the rest ran");
        let mut left = queue.names().unwrap();
        left.sort();
        assert_eq!(left, [".0000000006-e.json.1.tmp"], "everything else is taken or removed");
        assert!(said.iter().any(|l| l.contains("junk")), "{said:?}");
        fs::remove_dir_all(&d).unwrap();
    }

    // An archive as `docker save image@digest` writes it, built here: index.json names the
    // digest, the blobs hash to theirs.
    fn archive(d: &Path, platforms: &[&str], corrupt: bool) -> (std::path::PathBuf, String, String) {
        let root = d.join("oci");
        let _ = fs::remove_dir_all(&root);
        fs::create_dir_all(root.join("blobs/sha256")).unwrap();
        let put = |data: &str| {
            let sum = release::sha256_hex(data.as_bytes());
            fs::write(root.join(format!("blobs/sha256/{sum}")), if corrupt { format!("{data} ") } else { data.to_owned() }).unwrap();
            format!("sha256:{sum}")
        };
        let config = put(r#"{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":[]}}"#);
        let manifest = put(&format!(
            r#"{{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"{config}","size":1}},"layers":[]}}"#
        ));
        let top = if platforms.is_empty() {
            manifest
        } else {
            let list: Vec<String> = platforms
                .iter()
                .map(|a| format!(r#"{{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":"{manifest}","size":1,"platform":{{"architecture":"{a}","os":"linux"}}}}"#))
                .collect();
            put(&format!(r#"{{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{}]}}"#, list.join(",")))
        };
        fs::write(root.join("index.json"), format!(r#"{{"schemaVersion":2,"manifests":[{{"digest":"{top}","size":1}}]}}"#)).unwrap();
        fs::write(root.join("oci-layout"), r#"{"imageLayoutVersion":"1.0.0"}"#).unwrap();
        let tar = d.join("image.tar");
        let ok =
            Command::new("tar").arg("-cf").arg(&tar).arg("-C").arg(&root).args(["index.json", "oci-layout", "blobs"]).status().unwrap();
        assert!(ok.success());
        (tar, top, config)
    }

    // `mikan addon load` takes an archive only for the digest the signed catalog pins, and
    // only when every link from that digest to the config hashes right.
    #[test]
    fn an_archive_is_checked_against_the_catalog() {
        let d = tmpdir("archive");
        let (tar, digest, config) = archive(&d, &[], false);
        assert_eq!(archive_image(&tar).unwrap(), Archived { digest: digest.clone(), config: config.clone() });
        let (tar, index, config2) = archive(&d, &["s390x", arch()], false);
        assert_eq!(archive_image(&tar).unwrap(), Archived { digest: index.clone(), config: config2 }, "the manifest of this platform");
        let (tar, ..) = archive(&d, &["s390x"], false);
        assert!(archive_image(&tar).is_err(), "no image for this platform");
        let (tar, ..) = archive(&d, &[arch()], true);
        assert!(archive_image(&tar).unwrap_err().to_string().contains("does not match"), "a blob changed after its digest was taken");
        fs::write(d.join("not.tar"), "x").unwrap();
        assert!(archive_image(&d.join("not.tar")).is_err());

        let e = |id: &str, digest: &str| Entry {
            id: id.into(),
            version: "1.0.0".into(),
            protocol: 2,
            kind: "bot".into(),
            mode: "http".into(),
            permissions: vec![],
            resources: Resources::default(),
            image: format!("ghcr.io/getmikan/{id}"),
            digest: digest.into(),
            min_panel: String::new(),
            name: BTreeMap::new(),
        };
        let other = format!("sha256:{}", "c".repeat(64));
        let entries = || vec![e("other", &other), e("telegram", &index)];
        assert_eq!(pick(entries(), &index, None).unwrap().id, "telegram");
        assert_eq!(pick(entries(), &index, Some("telegram")).unwrap().id, "telegram");
        assert!(pick(entries(), &index, Some("other")).is_err(), "the archive is not that addon's pinned build");
        assert!(pick(entries(), &digest, None).is_err(), "a digest the catalog does not pin");
        fs::remove_dir_all(&d).unwrap();
    }
}
