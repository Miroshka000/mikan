//! Releases: the signed manifest every release publishes (internal/release in the panel's
//! code). The installer trusts an image and an installer binary only through a manifest
//! with a valid signature of the release key.

use std::collections::BTreeMap;

use anyhow::{Context, Result, bail};
use base64::Engine;
use base64::engine::general_purpose::STANDARD;
use ed25519_dalek::{Signature, VerifyingKey};
use serde::Deserialize;

/// The public half of the release signing key, the same as internal/release.PublicKey.
pub const PUBLIC_KEY: &str = "Z3wSIPBSaJxh5CsGO8eINI0aM0kyrQ46EcJSNeH85W8=";

pub const REPO: &str = "Miroshka000/mikan";

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

/// The newest release, verified.
pub fn latest() -> Result<Manifest> {
    let url = latest_url();
    let data = crate::net::get_opt(&url, 1 << 20).context("download the release manifest")?.context("no release is published yet")?;
    let sig = crate::net::get(&format!("{url}.sig"), 4096).context("download the manifest's signature")?;
    parse(&data, &String::from_utf8_lossy(&sig), &key()?)
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
    let hex = m.digest.strip_prefix("sha256:").unwrap_or("");
    if hex.len() != 64 || !hex.bytes().all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b)) || !valid_image(&m.image) {
        bail!("the manifest names a bad image {}@{}", m.image, m.digest);
    }
    for (arch, asset) in &m.installer {
        let sha = asset.sha256.as_str();
        if sha.len() != 64 || !sha.bytes().all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b)) || !asset.url.starts_with("https://") {
            bail!("the manifest has a bad installer for {arch}");
        }
    }
    Ok(m)
}

/// The release image lives in the project's own namespace on GitHub Packages; the value
/// goes into .env and the compose file, so its characters are the image name's only.
fn valid_image(image: &str) -> bool {
    image.strip_prefix("ghcr.io/miroshka000/").is_some_and(|r| {
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

    fn manifest(version: &str, image: &str) -> Vec<u8> {
        format!(
            r#"{{"version":"{version}","published":"2026-09-29T10:00:00Z","image":"{image}","digest":"sha256:{}","installer":{{"x86_64":{{"url":"https://github.com/Miroshka000/mikan/releases/download/v{version}/mikan-x86_64","sha256":"{}"}}}},"notes":{{"en":"- x"}}}}"#,
            "a".repeat(64),
            "b".repeat(64)
        )
        .into_bytes()
    }

    #[test]
    fn signed_manifests_only() {
        let signer = SigningKey::from_bytes(&[7; 32]);
        let key = signer.verifying_key();
        let data = manifest("0.3.9", "ghcr.io/miroshka000/mikan");
        let sig = STANDARD.encode(signer.sign(&data).to_bytes());
        let m = parse(&data, &format!("{sig}\n"), &key).unwrap();
        assert_eq!(m.reference(), format!("ghcr.io/miroshka000/mikan@sha256:{}", "a".repeat(64)));
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
        let text = String::from_utf8(manifest("0.4.4", "ghcr.io/miroshka000/mikan")).unwrap();
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
        assert!(accept(&manifest("0.4.4", "ghcr.io/miroshka000/mikan")));
        for bad in [
            "ghcr.io/someone-else/mikan",
            "ghcr.io/miroshka000/../x",
            "ghcr.io/miroshka000/Mi kan",
            "ghcr.io/miroshka000/",
            "ghcr.io/miroshka000/m$x",
        ] {
            assert!(!accept(&manifest("0.4.4", bad)), "{bad}");
        }
        let short_hash = String::from_utf8(manifest("0.4.4", "ghcr.io/miroshka000/mikan")).unwrap().replace(&"b".repeat(64), "bb");
        assert!(!accept(short_hash.as_bytes()));
        let plain_http = String::from_utf8(manifest("0.4.4", "ghcr.io/miroshka000/mikan")).unwrap().replace("https://", "http://");
        assert!(!accept(plain_http.as_bytes()));
    }

    // The installer and the panel trust the same key.
    #[test]
    fn key_matches_the_panel() {
        let go = std::fs::read_to_string("../internal/release/release.go").unwrap();
        assert!(go.contains(&format!("const PublicKey = \"{PUBLIC_KEY}\"")), "internal/release.PublicKey differs");
        assert!(go.contains(&format!("const Repo = \"{REPO}\"")), "internal/release.Repo differs");
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

    #[test]
    fn versions() {
        assert!(newer("0.4.4.1", "0.4.4"));
        assert!(!newer("0.4.4", "0.4.4.0"));
        assert!(newer("0.4.4.10", "0.4.4.9"));
        assert!(newer("0.4.5.0", "0.4.4.99"));
        assert!(newer("0.4.4.1", "0.4.4.1-rc.1"));
        assert!(newer("0.4.4.1-rc.10", "0.4.4.1-rc.9"));
        assert!(newer("0.4.4.1-99999999999999999999999", "0.4.4.1-9"));
        assert!(semver("0.4.4.1-rc..1").is_none());
        assert!(semver("0.4.4.18446744073709551616").is_none());
        assert!(semver("0.4.4.1.2").is_none());
        assert!(semver("0.4.4.").is_none());
        assert!(newer("0.3.10", "0.3.9"));
        assert!(newer("0.4.0", "0.3.99"));
        assert!(newer("1.0.0", "0.9.9"));
        assert!(!newer("0.3.9", "0.3.9"));
        assert!(newer("0.3.9", "0.3.9-rc.1"));
        assert!(!newer("0.3.9-rc.1", "0.3.9"));
        assert!(newer("0.3.9-rc.2", "0.3.9-rc.1"));
        assert!(newer("0.3.9", "dev"));
        assert!(!newer("dev", "0.3.9"));
        assert!(!newer("0.3", "0.2.9"));
    }
}
