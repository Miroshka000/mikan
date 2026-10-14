//! Releases: the signed manifest every release publishes (internal/release in the panel's
//! code). The installer trusts an image and an installer binary only through a manifest
//! with a valid signature of the release key.

use std::collections::BTreeMap;
use std::path::Path;

use anyhow::{Context, Result, bail};
use base64::Engine;
use base64::engine::general_purpose::STANDARD;
use ed25519_dalek::{Signature, VerifyingKey};
use serde::Deserialize;
use sha2::{Digest, Sha256};

/// The public half of the release signing key, the same as internal/release.PublicKey.
/// This fork signs its own releases with its own key; a manifest signed with the upstream
/// key is refused here.
pub const PUBLIC_KEY: &str = "ayuo0yLe0M0N5QQPaxkjAA0ltZcXSG7LRANrgyFAhUg=";

pub const REPO: &str = "o0hhs/mikanaddons";

/// The project's own namespace on GitHub Packages: ghcr.io/<owner of REPO in lowercase>/,
/// as internal/release checks it.
const IMAGE_PREFIX: &str = "ghcr.io/o0hhs/";

/// Installs a node of an existing panel on a fresh server (internal/release.JoinCommand).
pub fn join_command(key: &str) -> String {
    format!("curl -fsSL https://github.com/{REPO}/releases/latest/download/install.sh | sudo bash -s -- --join {key}")
}

pub fn latest_url() -> String {
    format!("https://github.com/{REPO}/releases/latest/download/manifest.json")
}

/// The manifest is signed, so the signature already says who wrote every byte; unknown
/// fields are not refused, because a field a later release adds (a minimum version, a
/// channel) must not stop every installer already out there from updating.
#[derive(Deserialize, Debug, Clone)]
pub struct Manifest {
    pub version: String,
    #[serde(default)]
    pub min_installer: String,
    pub published: String,
    pub image: String,
    pub digest: String,
    pub installer: BTreeMap<String, Asset>,
    #[serde(default)]
    pub notes: BTreeMap<String, String>,
}

#[derive(Deserialize, Debug, Clone)]
pub struct Asset {
    pub url: String,
    pub sha256: String,
}

impl Manifest {
    /// The image pinned to the released digest.
    pub fn reference(&self) -> String {
        format!("{}@{}", self.image, self.digest)
    }

    /// This server's installer binary.
    pub fn installer(&self) -> Option<&Asset> {
        self.installer.get(std::env::consts::ARCH)
    }
}

/// The newest release of this server's channel (stable on a fresh server), verified: from
/// the release index, or GitHub's latest release when the index cannot be had.
pub fn latest() -> Result<Manifest> {
    Ok(find(None, crate::update::channel_here())?.manifest)
}

/// GitHub's latest release, verified: what every updater read before the index, and what
/// it falls back to.
fn latest_with(key: &VerifyingKey, fetch: Fetch<'_>) -> Result<Manifest> {
    let url = latest_url();
    let data = fetch(&url, 1 << 20).context("download the release manifest")?.context("no release is published yet")?;
    let sig = fetch(&format!("{url}.sig"), 4096).context("download the manifest's signature")?.context("the manifest has no signature")?;
    let manifest = parse(&data, &String::from_utf8_lossy(&sig), key)?;
    // Its own record, not the index's: the index is published again with every beta, so
    // the latest stable release is often older than the index seen.
    if let Some(seen) = latest_seen() {
        not_older(seen, &data, "published", "the latest release")?;
    }
    Ok(manifest)
}

/// Downloads a URL whole, at most limit bytes; None when it is not there (net::get_opt).
/// Tests hand in files of their own.
pub type Fetch<'a> = &'a dyn Fn(&str, u64) -> Result<Option<Vec<u8>>>;

/// The release index (internal/release/index.go has the whole story): every release an
/// updater may go to, each pointing to its own signed manifest, signed itself with the
/// release key. Its address does not depend on GitHub's "latest": it is an asset of the
/// pre-release tagged "updates", which never becomes the latest release.
///
/// The format only grows: unknown fields are ignored; an entry with an unparsable version,
/// an unknown channel or a missing field is skipped, never fatal; a format older readers
/// cannot read goes to a new file name, never a new meaning of index.json. "rollout" (a
/// share of servers that take a release) is reserved: an entry may carry it, and it is
/// ignored for now. testdata/index.json is run by the panel's tests too.
pub fn index_url() -> String {
    format!("https://github.com/{REPO}/releases/download/updates/index.json")
}

/// Which releases a server takes: stable ones, or the pre-releases (vX-rc.N) too.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Channel {
    Stable,
    Beta,
}

impl Channel {
    /// Only the exact words count: whatever else is written is no channel.
    pub fn parse(s: &str) -> Option<Channel> {
        match s {
            "stable" => Some(Channel::Stable),
            "beta" => Some(Channel::Beta),
            _ => None,
        }
    }
}

/// A release the index lists.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Entry {
    pub version: String,
    pub channel: String,
    pub manifest: String,
    /// The lowest installed version that updates to this release directly.
    pub from: String,
}

impl Entry {
    /// Whether a reader can use the entry; the others are skipped. A pre-release is never
    /// stable; a beta entry may be a release version (one tried on beta first).
    fn valid(&self) -> bool {
        let pre = matches!(semver(&self.version), Some((_, Some(_))));
        semver(&self.version).is_some()
            && Channel::parse(&self.channel).is_some_and(|c| c == Channel::Beta || !pre)
            && self.manifest.starts_with("https://")
            && semver(&self.from).is_some()
            && !newer(&self.from, &self.version)
    }
}

/// Reads an index whose signature was checked: the entries a reader cannot use are left
/// out. The keys are matched exactly and the last of a repeated key wins, as the panel
/// reads it.
pub fn decode_index(data: &[u8]) -> Result<Vec<Entry>> {
    let doc: serde_json::Value = serde_json::from_slice(data).context("the release index is malformed")?;
    let releases =
        doc.as_object().and_then(|o| o.get("releases")).and_then(|r| r.as_array()).context("the release index has no list of releases")?;
    let text = |v: &serde_json::Value, key: &str| v.get(key).and_then(|s| s.as_str()).unwrap_or_default().to_owned();
    Ok(releases
        .iter()
        .filter(|r| r.is_object())
        .map(|r| Entry { version: text(r, "version"), channel: text(r, "channel"), manifest: text(r, "manifest"), from: text(r, "from") })
        .filter(Entry::valid)
        .collect())
}

/// Checks the signature over the index's exact bytes, then reads it.
pub fn parse_index(data: &[u8], sig: &str, key: &VerifyingKey) -> Result<Vec<Entry>> {
    verify(data, sig, key, "release index")?;
    decode_index(data)
}

/// What a server of some version finds in the index.
#[derive(Debug, Default)]
pub struct Choice {
    /// The release to update to now: the highest one of the channels newer than the server
    /// that it updates to directly.
    pub target: Option<Entry>,
    /// The highest release of the channels, newer than the server or not. Newer than the
    /// target: the target is a hop on the way to it.
    pub newest: Option<Entry>,
}

/// Picks the release a server running current takes (internal/release.Choose): among the
/// entries of the allowed channels (stable, and beta too when beta) with a version newer
/// than current and from at or below it, the highest. A current that is not a release
/// version (a fresh server, "dev") has no from to meet. The first of equal versions wins.
pub fn choose(entries: &[Entry], current: &str, beta: bool) -> Choice {
    let known = semver(current).is_some();
    let mut c = Choice::default();
    for e in entries.iter().filter(|e| e.valid() && (beta || e.channel != "beta")) {
        if c.newest.as_ref().is_none_or(|n| newer(&e.version, &n.version)) {
            c.newest = Some(e.clone());
        }
        if !newer(&e.version, current) || (known && newer(&e.from, current)) {
            continue;
        }
        if c.target.as_ref().is_none_or(|t| newer(&e.version, &t.version)) {
            c.target = Some(e.clone());
        }
    }
    c
}

/// A refusal of the index itself (the requested release is not in it), not a failure to
/// get it: the latest release on GitHub does not answer instead.
#[derive(Debug)]
struct Refused(String);

impl std::fmt::Display for Refused {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(&self.0)
    }
}

impl std::error::Error for Refused {}

/// The release found for a server.
#[derive(Debug)]
pub struct Found {
    /// The release to update to now; when there is none, the newest one.
    pub manifest: Manifest,
    /// The newest release of the channel, when the update goes through manifest on the way
    /// to it or this version cannot reach it.
    pub newest: Option<String>,
    /// newest is out, but no release this version updates to leads there.
    pub unreachable: bool,
    /// Why the index was not used, when the answer is GitHub's latest release instead.
    pub fallback: Option<String>,
}

/// The release for a server of version current (None: a fresh server, which takes the
/// newest) on a channel: from the index, or GitHub's latest release when the index cannot
/// be had or believed, so a broken index never stops an update.
pub fn find(current: Option<&str>, channel: Channel) -> Result<Found> {
    find_with(current, channel, &key()?, &crate::net::get_opt)
}

pub fn find_with(current: Option<&str>, channel: Channel, key: &VerifyingKey, fetch: Fetch<'_>) -> Result<Found> {
    match from_index(current.unwrap_or_default(), channel, None, key, fetch) {
        Ok(found) => Ok(found),
        Err(e) => {
            let manifest = latest_with(key, fetch)?;
            Ok(Found { manifest, newest: None, unreachable: false, fallback: Some(format!("{e:#}")) })
        }
    }
}

/// The release a server running current takes on its way to the release upto, which the
/// panel asked for and which goes no further: the highest release of the index that is
/// not past upto and that current updates to directly (a hop, when upto is not
/// reachable at once; `newest` of the answer is upto then). upto must be a release of the
/// index in the channel (a pre-release makes the channel beta: the panel runs it): the
/// index says so, or the request is refused. Only when the index cannot be had or believed
/// does GitHub's latest release answer, and only if it is upto itself.
pub fn find_upto(current: &str, channel: Channel, upto: &str) -> Result<Found> {
    find_upto_with(current, channel, upto, &key()?, &crate::net::get_opt)
}

pub fn find_upto_with(current: &str, channel: Channel, upto: &str, key: &VerifyingKey, fetch: Fetch<'_>) -> Result<Found> {
    match from_index(current, channel, Some(upto), key, fetch) {
        Ok(found) => Ok(found),
        Err(e) if e.downcast_ref::<Refused>().is_some() => Err(e),
        Err(e) => {
            let why = format!("{e:#}");
            let manifest = latest_with(key, fetch).with_context(|| format!("the release index is unavailable ({why})"))?;
            if newer(&manifest.version, upto) || newer(upto, &manifest.version) {
                bail!(
                    "the release index is unavailable ({why}) and the latest release is mikan {}, not the mikan {upto} that was asked for",
                    manifest.version
                );
            }
            Ok(Found { manifest, newest: None, unreachable: false, fallback: Some(why) })
        }
    }
}

/// Whether a version is a pre-release (1.2.3-rc.1).
pub fn is_prerelease(v: &str) -> bool {
    matches!(semver(v), Some((_, Some(_))))
}

/// Whether v is a version of a release, as a request may name it: short, and nothing but a
/// version (no "v", no "dev", no address).
pub fn valid_version(v: &str) -> bool {
    v.len() <= 64 && semver(v).is_some()
}

fn from_index(current: &str, channel: Channel, upto: Option<&str>, key: &VerifyingKey, fetch: Fetch<'_>) -> Result<Found> {
    let url = index_url();
    let data = fetch(&url, 1 << 20).context("download the release index")?.context("no release index is published")?;
    let sig =
        fetch(&format!("{url}.sig"), 4096).context("download the index's signature")?.context("the release index has no signature")?;
    let mut entries = parse_index(&data, &String::from_utf8_lossy(&sig), key)?;
    if let Some(seen) = index_seen() {
        not_older(seen, &data, "published", "the release index")?;
    }
    let beta = channel == Channel::Beta;
    if let Some(upto) = upto {
        // Never a release the index does not list for the channel, and never past it.
        if !entries.iter().any(|e| (beta || e.channel != "beta") && !newer(&e.version, upto) && !newer(upto, &e.version)) {
            return Err(Refused(format!("mikan {upto} is not a release of this server's channel in the signed release index")).into());
        }
        entries.retain(|e| !newer(&e.version, upto));
    }
    let c = choose(&entries, current, beta);
    let newest = c.newest.context("the release index lists no release of the channel")?;
    let (entry, later, unreachable) = match c.target {
        Some(t) if newer(&newest.version, &t.version) => (t, Some(newest.version), false),
        Some(t) => (t, None, false),
        None if newer(&newest.version, current) => (newest.clone(), Some(newest.version), true),
        None => (newest, None, false),
    };
    let what = format!("the manifest of {}", entry.version);
    let data =
        fetch(&entry.manifest, 1 << 20).with_context(|| format!("download {what}"))?.with_context(|| format!("{what} is not there"))?;
    let sig = fetch(&format!("{}.sig", entry.manifest), 4096)
        .with_context(|| format!("download {what}'s signature"))?
        .with_context(|| format!("{what} has no signature"))?;
    let manifest = parse(&data, &String::from_utf8_lossy(&sig), key)?;
    if manifest.version != entry.version {
        bail!("the release index says {}, its manifest is of {}", entry.version, manifest.version);
    }
    Ok(Found { manifest, newest: later, unreachable, fallback: None })
}

/// The newest release index this server has accepted ("published"); tests keep none.
fn index_seen() -> Option<&'static Path> {
    if cfg!(test) { None } else { Some(Path::new("/opt/mikan/release/index-published")) }
}

/// The newest manifest of GitHub's latest release this server has accepted: a stale one
/// handed in when the index fails must not take the place of a newer one.
fn latest_seen() -> Option<&'static Path> {
    if cfg!(test) { None } else { Some(Path::new("/opt/mikan/release/latest-published")) }
}

/// A signed document stays validly signed for ever: an old copy served again (a cache, a
/// CDN, someone in the path) would hold a server on an old release, a security fix
/// hidden, or offer a vulnerable build as the current one. Its timestamp field (RFC 3339
/// in UTC) may only grow; the newest one seen is kept in seen, root's own file.
pub fn not_older(seen: &Path, doc: &[u8], field: &str, what: &str) -> Result<()> {
    let at = serde_json::from_slice::<serde_json::Value>(doc).ok().and_then(|v| v[field].as_str().map(str::to_owned));
    let Some(at) = at.filter(|u| !u.is_empty()) else { return Ok(()) };
    let before = std::fs::read_to_string(seen).unwrap_or_default();
    // RFC 3339 in UTC, always the same shape: comparing the text compares the time.
    if !before.is_empty() && at.as_str() < before.trim() {
        bail!("{what} is from {at}, older than the one seen on {}: it is not accepted", before.trim());
    }
    if at.as_str() > before.trim() {
        crate::panelfs::write_root(seen, at.as_bytes())?;
    }
    Ok(())
}

pub fn key() -> Result<VerifyingKey> {
    let raw: [u8; 32] = STANDARD.decode(PUBLIC_KEY)?.try_into().map_err(|_| anyhow::anyhow!("bad release key"))?;
    Ok(VerifyingKey::from_bytes(&raw)?)
}

/// Checks a signature of the release key over exact bytes (the manifest, the marketplace's
/// catalog); what names the file goes into the errors.
pub fn verify(data: &[u8], sig: &str, key: &VerifyingKey, what: &str) -> Result<()> {
    let raw = STANDARD.decode(sig.trim()).with_context(|| format!("the {what}'s signature is not base64"))?;
    let sig = Signature::from_slice(&raw).with_context(|| format!("the {what}'s signature is malformed"))?;
    key.verify_strict(data, &sig).with_context(|| format!("the {what}'s signature does not match the release key"))
}

/// Checks the signature over the manifest's exact bytes, then what it says.
pub fn parse(data: &[u8], sig: &str, key: &VerifyingKey) -> Result<Manifest> {
    verify(data, sig, key, "manifest")?;
    let m: Manifest = serde_json::from_slice(data).context("the manifest is malformed")?;
    if semver(&m.version).is_none() {
        bail!("the manifest has a bad version {:?}", m.version);
    }
    if !m.min_installer.is_empty() && semver(&m.min_installer).is_none() {
        bail!("the manifest has a bad minimum installer version");
    }
    if newer(&m.min_installer, &m.version) {
        bail!("the manifest requires an installer newer than its release");
    }
    if !is_sha256_hex(m.digest.strip_prefix("sha256:").unwrap_or("")) || !valid_image(&m.image) {
        bail!("the manifest names a bad image {}@{}", m.image, m.digest);
    }
    for (arch, asset) in &m.installer {
        if !is_sha256_hex(&asset.sha256) || !asset.url.starts_with("https://") {
            bail!("the manifest has a bad installer for {arch}");
        }
    }
    Ok(m)
}

/// A SHA-256 as 64 lowercase hex digits, the way digests are written.
pub fn is_sha256_hex(s: &str) -> bool {
    s.len() == 64 && s.bytes().all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
}

/// The SHA-256 of data as lowercase hex.
pub fn sha256_hex(data: &[u8]) -> String {
    Sha256::digest(data).iter().map(|x| format!("{x:02x}")).collect()
}

/// The release image lives in the project's own namespace on GitHub Packages; the value
/// goes into .env and the compose file, so its characters are the image name's only.
fn valid_image(image: &str) -> bool {
    image.strip_prefix(IMAGE_PREFIX).is_some_and(|r| {
        !r.is_empty() && !r.contains("..") && r.bytes().all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || b"._/-".contains(&b))
    })
}

type Version<'a> = ([u64; 4], Option<&'a str>);

fn semver(v: &str) -> Option<Version<'_>> {
    let (core, pre) = match v.split_once('-') {
        Some((c, p)) if !p.is_empty() => (c, Some(p)),
        Some(_) => return None,
        None => (v, None),
    };
    if let Some(p) = pre
        && p.split('.').any(|part| part.is_empty() || !part.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'-'))
    {
        return None;
    }
    let mut n = [0u64; 4];
    let mut parts = core.split('.');
    for x in &mut n[..3] {
        let p = parts.next()?;
        if p.is_empty() || !p.bytes().all(|b| b.is_ascii_digit()) {
            return None;
        }
        *x = p.parse().ok()?;
    }
    if let Some(p) = parts.next() {
        if p.is_empty() || !p.bytes().all(|b| b.is_ascii_digit()) {
            return None;
        }
        n[3] = p.parse().ok()?;
    }
    parts.next().is_none().then_some((n, pre))
}

/// Whether version a is later than b, as internal/release.Newer: a pre-release comes
/// before its release, and "dev" or anything unparsable is older than every release.
/// The last release whose panel moves a database from SQLite to PostgreSQL.
pub const LAST_SQLITE_MOVE: &str = "0.5.0.4";

/// Whether the panel of version can take a database from SQLite (a server of 0.4, or a
/// backup of one): 0.5.0.x can, 0.5.1.0 and later cannot. A version that is not a release's
/// (a local build) is let through: its panel refuses SQLite data itself.
pub fn moves_sqlite(version: &str) -> bool {
    semver(version).is_none() || newer("0.5.1.0-0", version)
}

pub fn newer(a: &str, b: &str) -> bool {
    match (semver(a), semver(b)) {
        (None, _) => false,
        (Some(_), None) => true,
        (Some((x, xp)), Some((y, yp))) => match x.cmp(&y) {
            std::cmp::Ordering::Equal => match (xp, yp) {
                (None, Some(_)) => true,
                (Some(p), Some(q)) => compare_prerelease(p, q).is_gt(),
                _ => false,
            },
            o => o.is_gt(),
        },
    }
}

fn compare_prerelease(a: &str, b: &str) -> std::cmp::Ordering {
    use std::cmp::Ordering;
    let mut x = a.split('.');
    let mut y = b.split('.');
    loop {
        let (p, q) = match (x.next(), y.next()) {
            (None, None) => return Ordering::Equal,
            (Some(_), None) => return Ordering::Greater,
            (None, Some(_)) => return Ordering::Less,
            (Some(p), Some(q)) => (p, q),
        };
        let pn = p.bytes().all(|b| b.is_ascii_digit());
        let qn = q.bytes().all(|b| b.is_ascii_digit());
        let order = match (pn, qn) {
            (true, false) => Ordering::Less,
            (false, true) => Ordering::Greater,
            (true, true) => {
                let p = p.trim_start_matches('0');
                let q = q.trim_start_matches('0');
                p.len().cmp(&q.len()).then_with(|| p.cmp(q))
            }
            _ => p.cmp(q),
        };
        if order != Ordering::Equal {
            return order;
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use ed25519_dalek::{Signer, SigningKey};

    // 0.5.0.x moves a database from SQLite, 0.5.1.0 and its pre-releases do not.
    #[test]
    fn which_releases_move_sqlite() {
        for v in ["0.4.5", "0.5.0.0", "0.5.0.4", "0.5.0.5-rc.4", "dev"] {
            assert!(moves_sqlite(v), "{v}");
        }
        for v in ["0.5.1.0", "0.5.1.0-rc.1", "0.5.1.1", "0.6.0"] {
            assert!(!moves_sqlite(v), "{v}");
        }
    }

    fn manifest(version: &str, image: &str) -> Vec<u8> {
        format!(
            r#"{{"version":"{version}","published":"2026-09-29T10:00:00Z","image":"{image}","digest":"sha256:{}","installer":{{"x86_64":{{"url":"https://github.com/o0hhs/mikanaddons/releases/download/v{version}/mikan-x86_64","sha256":"{}"}}}},"notes":{{"en":"- x"}}}}"#,
            "a".repeat(64),
            "b".repeat(64)
        )
        .into_bytes()
    }

    #[test]
    fn signed_manifests_only() {
        let signer = SigningKey::from_bytes(&[7; 32]);
        let key = signer.verifying_key();
        let data = manifest("0.3.9", "ghcr.io/o0hhs/mikan");
        let sig = STANDARD.encode(signer.sign(&data).to_bytes());
        let m = parse(&data, &format!("{sig}\n"), &key).unwrap();
        assert_eq!(m.reference(), format!("ghcr.io/o0hhs/mikan@sha256:{}", "a".repeat(64)));
        assert_eq!(m.installer["x86_64"].sha256, "b".repeat(64));

        let mut tampered = data.clone();
        tampered[14] = b'8';
        assert!(parse(&tampered, &sig, &key).is_err());
        let other = SigningKey::from_bytes(&[8; 32]).verifying_key();
        assert!(parse(&data, &sig, &other).is_err());
        assert!(parse(&data, "not base64!", &key).is_err());

        let docker_hub = manifest("0.3.9", "docker.io/someone/mikan");
        let sig = STANDARD.encode(signer.sign(&docker_hub).to_bytes());
        assert!(parse(&docker_hub, &sig, &key).is_err());
    }

    // A later release adds a field to the signed manifest (a minimum version, a channel):
    // the installers already out there must still read it.
    #[test]
    fn new_fields_do_not_break_old_installers() {
        let signer = SigningKey::from_bytes(&[7; 32]);
        let key = signer.verifying_key();
        let text = String::from_utf8(manifest("0.4.4", "ghcr.io/o0hhs/mikan")).unwrap();
        let extended = text.replacen("{\"version\"", "{\"min_installer\":\"0.4.4\",\"channel\":\"stable\",\"version\"", 1).replacen(
            "\"sha256\":",
            "\"size\":123,\"sha256\":",
            1,
        );
        assert_ne!(extended, text);
        let sig = STANDARD.encode(signer.sign(extended.as_bytes()).to_bytes());
        let m = parse(extended.as_bytes(), &sig, &key).expect("a manifest with new fields");
        assert_eq!(m.version, "0.4.4");
        assert!(m.installer.contains_key("x86_64"));
        assert_eq!(m.min_installer, "0.4.4");
        let malformed = extended.replace("\"min_installer\":\"0.4.4\"", "\"min_installer\":\"0.4.4.1.2\"");
        let sig = STANDARD.encode(signer.sign(malformed.as_bytes()).to_bytes());
        assert!(parse(malformed.as_bytes(), &sig, &key).is_err());
        let future = extended.replace("\"min_installer\":\"0.4.4\"", "\"min_installer\":\"0.4.5.0\"");
        let sig = STANDARD.encode(signer.sign(future.as_bytes()).to_bytes());
        assert!(parse(future.as_bytes(), &sig, &key).is_err());
    }

    #[test]
    fn only_the_projects_images_and_real_hashes() {
        let signer = SigningKey::from_bytes(&[7; 32]);
        let key = signer.verifying_key();
        let accept = |data: &[u8]| parse(data, &STANDARD.encode(signer.sign(data).to_bytes()), &key).is_ok();
        assert!(accept(&manifest("0.4.4", "ghcr.io/o0hhs/mikan")));
        for bad in [
            "ghcr.io/someone-else/mikan",
            "ghcr.io/o0hhs/../x",
            "ghcr.io/o0hhs/Mi kan",
            "ghcr.io/o0hhs/",
            "ghcr.io/o0hhs/m$x",
        ] {
            assert!(!accept(&manifest("0.4.4", bad)), "{bad}");
        }
        let short_hash = String::from_utf8(manifest("0.4.4", "ghcr.io/o0hhs/mikan")).unwrap().replace(&"b".repeat(64), "bb");
        assert!(!accept(short_hash.as_bytes()));
        let plain_http = String::from_utf8(manifest("0.4.4", "ghcr.io/o0hhs/mikan")).unwrap().replace("https://", "http://");
        assert!(!accept(plain_http.as_bytes()));
    }

    // The installer and the panel trust the same key.
    #[test]
    fn key_matches_the_panel() {
        let go = std::fs::read_to_string("../internal/release/release.go").unwrap();
        assert!(go.contains(&format!("const PublicKey = \"{PUBLIC_KEY}\"")), "internal/release.PublicKey differs");
        assert!(go.contains(&format!("const Repo = \"{REPO}\"")), "internal/release.Repo differs");
        let index = std::fs::read_to_string("../internal/release/index.go").unwrap();
        assert!(
            index.contains("const IndexURL = \"https://github.com/\" + Repo + \"/releases/download/updates/index.json\"")
                && index_url().ends_with("/releases/download/updates/index.json"),
            "internal/release.IndexURL differs"
        );
        let owner = REPO.split('/').next().unwrap().to_lowercase();
        assert_eq!(IMAGE_PREFIX, format!("ghcr.io/{owner}/"), "the image namespace is not the repository owner's");
        assert!(
            go.contains("/releases/latest/download/install.sh | sudo bash\"") && go.contains("\" -s -- --join \""),
            "internal/release.JoinCommand differs"
        );
        key().unwrap();
    }

    // install.sh checks the manifest's signature with this very key before it runs anything
    // it downloaded: its PEM must be the release key, and the script must still parse.
    #[test]
    fn install_sh_carries_the_release_key() {
        let sh = include_str!("../install.sh");
        let mut spki = vec![0x30, 0x2a, 0x30, 0x05, 0x06, 0x03, 0x2b, 0x65, 0x70, 0x03, 0x21, 0x00];
        spki.extend(STANDARD.decode(PUBLIC_KEY).unwrap());
        assert!(
            sh.contains(&format!("-----BEGIN PUBLIC KEY-----\n{}\n-----END PUBLIC KEY-----", STANDARD.encode(spki))),
            "install.sh has another key"
        );
        assert!(sh.contains("pkeyutl -verify") && sh.contains("-rawin"));
        // the signature check comes before the installer is downloaded, and it is not optional
        let verify = sh.find("pkeyutl -verify").unwrap();
        assert!(verify < sh.find("get \"$url\"").unwrap());
        assert!(sh.contains("nothing is installed"));
        let ok = std::process::Command::new("sh").arg("-n").arg(concat!(env!("CARGO_MANIFEST_DIR"), "/install.sh")).status().unwrap();
        assert!(ok.success(), "install.sh does not parse");
    }

    #[derive(Deserialize)]
    struct DecodeCase {
        name: String,
        doc: String,
        #[serde(default)]
        versions: Vec<String>,
        #[serde(default)]
        error: bool,
    }
    #[derive(Deserialize)]
    struct RawEntry {
        version: String,
        channel: String,
        manifest: String,
        from: String,
    }
    #[derive(Deserialize)]
    struct ChooseCase {
        current: String,
        beta: bool,
        target: String,
        newest: String,
        #[serde(default)]
        manifest: String,
    }
    #[derive(Deserialize)]
    struct ChooseTable {
        releases: Vec<RawEntry>,
        cases: Vec<ChooseCase>,
    }
    #[derive(Deserialize)]
    struct IndexTable {
        decode: Vec<DecodeCase>,
        choose: Vec<ChooseTable>,
    }

    fn index_table() -> IndexTable {
        let t: IndexTable = serde_json::from_str(include_str!("../../testdata/index.json")).unwrap();
        assert!(!t.decode.is_empty() && !t.choose.is_empty());
        t
    }

    // testdata/index.json is run by the panel's tests as well (internal/release): both sides
    // keep, skip and choose the same entries.
    #[test]
    fn index_decode_table() {
        for c in index_table().decode {
            match decode_index(c.doc.as_bytes()) {
                Ok(entries) => {
                    assert!(!c.error, "{}: decoded {entries:?}", c.name);
                    let got: Vec<&str> = entries.iter().map(|e| e.version.as_str()).collect();
                    assert_eq!(got, c.versions, "{}", c.name);
                }
                Err(e) => assert!(c.error, "{}: {e:#}", c.name),
            }
        }
    }

    #[test]
    fn index_choose_table() {
        for (i, t) in index_table().choose.into_iter().enumerate() {
            let entries: Vec<Entry> = t
                .releases
                .into_iter()
                .map(|r| Entry { version: r.version, channel: r.channel, manifest: r.manifest, from: r.from })
                .collect();
            for c in t.cases {
                let got = choose(&entries, &c.current, c.beta);
                let version = |e: &Option<Entry>| e.as_ref().map(|e| e.version.clone()).unwrap_or_default();
                assert_eq!(
                    (version(&got.target), version(&got.newest)),
                    (c.target.clone(), c.newest.clone()),
                    "table {i}, {:?} beta={}",
                    c.current,
                    c.beta
                );
                if !c.manifest.is_empty() {
                    assert_eq!(got.target.unwrap().manifest, c.manifest, "table {i}, {:?}", c.current);
                }
            }
        }
    }

    #[test]
    fn channels_are_exact_words() {
        assert_eq!(Channel::parse("stable"), Some(Channel::Stable));
        assert_eq!(Channel::parse("beta"), Some(Channel::Beta));
        for bad in ["", "Beta", "STABLE", " beta", "beta\n", "nightly", "beta\0"] {
            assert_eq!(Channel::parse(bad), None, "{bad:?}");
        }
    }

    /// A GitHub of signed files for find_with: the index, the releases' manifests and
    /// "latest".
    struct Files {
        signer: SigningKey,
        files: std::cell::RefCell<BTreeMap<String, Vec<u8>>>,
    }

    impl Files {
        fn new() -> Self {
            Files { signer: SigningKey::from_bytes(&[7; 32]), files: Default::default() }
        }
        fn put(&self, url: &str, data: Vec<u8>) {
            let sig = STANDARD.encode(self.signer.sign(&data).to_bytes()).into_bytes();
            self.files.borrow_mut().insert(format!("{url}.sig"), sig);
            self.files.borrow_mut().insert(url.to_owned(), data);
        }
        fn release(&self, version: &str) -> String {
            let url = format!("https://github.com/{REPO}/releases/download/v{version}/manifest.json");
            self.put(&url, manifest(version, "ghcr.io/o0hhs/mikan"));
            url
        }
        /// An index of "version channel from" entries.
        fn index(&self, entries: &[&str]) {
            let list: Vec<String> = entries
                .iter()
                .map(|e| {
                    let f: Vec<&str> = e.split(' ').collect();
                    let url = format!("https://github.com/{REPO}/releases/download/v{}/manifest.json", f[0]);
                    format!(r#"{{"version":"{}","channel":"{}","from":"{}","manifest":"{url}"}}"#, f[0], f[1], f[2])
                })
                .collect();
            self.put(&index_url(), format!(r#"{{"schema":1,"releases":[{}]}}"#, list.join(",")).into_bytes());
        }
        fn find(&self, current: Option<&str>, channel: Channel) -> Result<Found> {
            let fetch = |url: &str, _limit: u64| -> Result<Option<Vec<u8>>> { Ok(self.files.borrow().get(url).cloned()) };
            find_with(current, channel, &self.signer.verifying_key(), &fetch)
        }
    }

    #[test]
    fn find_takes_the_index_then_falls_back_to_latest() {
        let f = Files::new();
        for v in ["0.4.5", "0.5.0.0", "0.5.0.1", "0.5.0.2-rc.1", "0.6.0.0"] {
            f.release(v);
        }
        let latest = manifest("0.5.0.0", "ghcr.io/o0hhs/mikan");
        f.put(&latest_url(), latest);
        f.index(&[
            "0.4.5 stable 0.4.0",
            "0.5.0.0 stable 0.4.5",
            "0.5.0.1 stable 0.4.5",
            "0.5.0.2-rc.1 beta 0.5.0.0",
            "0.6.0.0 stable 0.5.0.1",
        ]);
        for (current, channel, version, newest, unreachable) in [
            (Some("0.4.5"), Channel::Stable, "0.5.0.1", Some("0.6.0.0"), false),
            (Some("0.5.0.0"), Channel::Beta, "0.5.0.2-rc.1", Some("0.6.0.0"), false),
            (Some("0.5.0.1"), Channel::Stable, "0.6.0.0", None, false),
            (Some("0.6.0.0"), Channel::Stable, "0.6.0.0", None, false),
            (Some("0.3.9"), Channel::Stable, "0.6.0.0", Some("0.6.0.0"), true),
            (None, Channel::Stable, "0.6.0.0", None, false),
        ] {
            let got = f.find(current, channel).unwrap();
            assert_eq!(got.manifest.version, version, "{current:?}");
            assert_eq!(got.newest.as_deref(), newest, "{current:?}");
            assert_eq!(got.unreachable, unreachable, "{current:?}");
            assert!(got.fallback.is_none(), "{current:?}: {:?}", got.fallback);
        }

        let fallback = |what: &str| {
            let got = f.find(Some("0.4.5"), Channel::Stable).unwrap();
            assert_eq!(got.manifest.version, "0.5.0.0", "{what}");
            assert!(got.fallback.is_some(), "{what}");
        };
        // Another key's index.
        let other = SigningKey::from_bytes(&[9; 32]);
        let data = f.files.borrow()[&index_url()].clone();
        f.files.borrow_mut().insert(format!("{}.sig", index_url()), STANDARD.encode(other.sign(&data).to_bytes()).into_bytes());
        fallback("another key's index");
        f.put(&index_url(), b"{not json".to_vec());
        fallback("a malformed index");
        f.index(&["0.5.0.1 nightly 0.4.5"]);
        fallback("no release of the channel");
        f.index(&["0.5.0.1 stable 0.4.5"]);
        f.put(
            &format!("https://github.com/{REPO}/releases/download/v0.5.0.1/manifest.json"),
            manifest("0.5.0.2", "ghcr.io/o0hhs/mikan"),
        );
        fallback("a manifest of another version");
        f.files.borrow_mut().clear();
        assert!(f.find(Some("0.4.5"), Channel::Stable).is_err(), "nothing at all");
    }

    // testdata/versions.json is run by the panel's tests as well (internal/release): both
    // sides order and refuse versions the same way.
    #[test]
    fn versions() {
        #[derive(Deserialize)]
        struct Pair {
            a: String,
            b: String,
            newer: bool,
        }
        #[derive(Deserialize)]
        struct Table {
            pairs: Vec<Pair>,
            valid: Vec<String>,
            invalid: Vec<String>,
        }
        let table: Table = serde_json::from_str(include_str!("../../testdata/versions.json")).unwrap();
        assert!(!table.pairs.is_empty() && !table.valid.is_empty() && !table.invalid.is_empty());
        for c in &table.pairs {
            assert_eq!(newer(&c.a, &c.b), c.newer, "newer({:?}, {:?})", c.a, c.b);
            assert!(!(c.newer && newer(&c.b, &c.a)), "newer({:?}, {:?}) and the other way round", c.a, c.b);
        }
        for v in &table.valid {
            assert!(semver(v).is_some(), "{v:?} refused");
        }
        for v in &table.invalid {
            assert!(semver(v).is_none(), "{v:?} accepted");
        }
    }
}
