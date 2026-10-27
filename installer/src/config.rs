//! The server's settings. The plain ones are in /opt/mikan/config.json; the secret ones
//! (the database's password and address, a node's join key) are files of their own in
//! /opt/mikan/secrets (0700), each readable by the container user that needs it (0400),
//! which compose mounts as /run/secrets/<name>. Nothing secret is in an environment variable
//! of a container: `docker inspect` and /proc/<pid>/environ show the files' paths only.
//!
//! Servers set up before kept everything in /opt/mikan/.env. While it is there it is what
//! the server runs on (an update's rollback to an older mikan puts it back), and `mikan
//! update` moves it here: the new files first, .env removed once the containers came up on
//! them (the update's archive keeps a copy).

use std::collections::BTreeMap;
use std::fs;
use std::io::Write;
use std::os::unix::fs::{DirBuilderExt, OpenOptionsExt, PermissionsExt};
use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

use anyhow::{Context, Result, bail};

/// The plain settings.
pub const FILE: &str = "config.json";
/// The secrets' directory.
pub const SECRETS: &str = "secrets";
/// The settings of servers set up before config.json.
pub const LEGACY: &str = ".env";

/// A secret: its key, its file in secrets/ and the user of the container that reads it.
pub struct Secret {
    pub key: &'static str,
    pub file: &'static str,
    owner: u32,
    /// Only a panel (or only a node) has it.
    panel: bool,
}

/// The containers' user (compose.yaml) and PostgreSQL's in its Alpine image.
const APP_UID: u32 = 65532;
const POSTGRES_UID: u32 = 70;

pub const SECRET_KEYS: [Secret; 3] = [
    Secret { key: "MIKAN_POSTGRES_PASSWORD", file: "postgres_password", owner: POSTGRES_UID, panel: true },
    Secret { key: "MIKAN_DATABASE_URL", file: "database_url", owner: APP_UID, panel: true },
    Secret { key: "MIKAN_NODE_JOIN", file: "node_join", owner: APP_UID, panel: false },
];

fn secret(key: &str) -> Option<&'static Secret> {
    SECRET_KEYS.iter().find(|s| s.key == key)
}

/// Whether mikan is installed in root: its settings are there, in either form.
pub fn installed(root: &Path) -> bool {
    root.join(FILE).exists() || root.join(LEGACY).exists()
}

pub struct Config {
    root: PathBuf,
    values: BTreeMap<String, String>,
    /// Read from .env, not yet moved.
    legacy: bool,
}

impl Config {
    /// The settings of the install in root: .env while it is there, else config.json and the
    /// secrets. A missing install is an io NotFound (Install::load tells it apart).
    pub fn load(root: impl AsRef<Path>) -> Result<Self> {
        let root = root.as_ref().to_path_buf();
        let legacy = root.join(LEGACY);
        match fs::read_to_string(&legacy) {
            Ok(text) => return Ok(Self { legacy: true, ..Self::from_env_text(&root, &text) }),
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
            Err(e) => return Err(e).with_context(|| format!("read {}", legacy.display())),
        }
        let path = root.join(FILE);
        let text = fs::read_to_string(&path).with_context(|| format!("read {}", path.display()))?;
        let mut values: BTreeMap<String, String> =
            serde_json::from_str(&text).with_context(|| format!("{} is not a JSON object of strings", path.display()))?;
        values.retain(|k, v| secret(k).is_none() && !v.is_empty());
        for s in &SECRET_KEYS {
            let file = root.join(SECRETS).join(s.file);
            match fs::read_to_string(&file) {
                Ok(v) if !v.trim().is_empty() => {
                    values.insert(s.key.to_owned(), v.trim().to_owned());
                }
                Ok(_) => {}
                Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
                Err(e) => return Err(e).with_context(|| format!("read {}", file.display())),
            }
        }
        Ok(Self { root, values, legacy: false })
    }

    pub fn new(root: impl AsRef<Path>) -> Self {
        Self { root: root.as_ref().to_path_buf(), values: BTreeMap::new(), legacy: false }
    }

    /// Settings as a .env of before writes them (an archive, a snapshot from before an
    /// update): KEY=VALUE lines, the last of a repeated key, quotes stripped.
    pub fn from_env_text(root: impl AsRef<Path>, text: &str) -> Self {
        let mut values = BTreeMap::new();
        for line in text.lines() {
            let Some((k, raw)) = line.split_once('=') else { continue };
            let k = k.trim();
            if k.is_empty() || k.starts_with('#') {
                continue;
            }
            let v = ['"', '\''].iter().find_map(|&q| raw.strip_prefix(q)?.strip_suffix(q)).unwrap_or(raw);
            if v.is_empty() {
                values.remove(k);
            } else {
                values.insert(k.to_owned(), v.to_owned());
            }
        }
        Self { root: root.as_ref().to_path_buf(), values, legacy: false }
    }

    /// Read from .env, which `mikan update` moves to config.json and secrets/.
    pub fn legacy(&self) -> bool {
        self.legacy
    }

    pub fn get(&self, key: &str) -> Option<&str> {
        self.values.get(key).map(String::as_str).filter(|v| !v.is_empty())
    }

    /// Sets key to value. The plain values go into compose.yaml as they are, so anything
    /// but image names, versions, ports and keys is refused; the database address also takes
    /// its query.
    pub fn set(&mut self, key: &str, value: &str) -> Result<()> {
        let fits = if key == DSN_KEY { plain_dsn(value) } else { plain(value) };
        if !fits {
            bail!("{key}: the value has characters that cannot go into compose.yaml ($, quotes, spaces, #)");
        }
        if value.is_empty() {
            self.values.remove(key);
        } else {
            self.values.insert(key.to_owned(), value.to_owned());
        }
        Ok(())
    }

    pub fn remove(&mut self, key: &str) {
        self.values.remove(key);
    }

    /// The values that are secret, for text that leaves the server to be cleaned of them:
    /// the secrets, and any other key whose name says it holds one.
    pub fn secret_values(&self) -> Vec<String> {
        const NAMES: [&str; 8] = ["KEY", "SECRET", "TOKEN", "PASS", "JOIN", "DATABASE_URL", "DSN", "CREDENTIAL"];
        self.values
            .iter()
            .filter(|(k, _)| {
                let k = k.to_ascii_uppercase();
                secret(&k).is_some() || NAMES.iter().any(|n| k.contains(n))
            })
            .map(|(_, v)| v.clone())
            .filter(|v| v.len() >= 6)
            .collect()
    }

    /// The settings as .env lines, sorted: what an older mikan reads (a rollback writes it
    /// back with the compose.yaml it goes with) and what an update's snapshot keeps.
    pub fn to_env_text(&self) -> String {
        self.values.iter().map(|(k, v)| format!("{k}={v}\n")).collect()
    }

    /// The value compose.yaml takes for ${key}, with def when unset ("${key:-def}").
    pub fn compose_value(&self, key: &str) -> Option<&str> {
        if secret(key).is_some() { None } else { self.get(key) }
    }

    /// Writes config.json and the secrets of this kind of server (a panel's or a node's):
    /// each secret a file even when unset, as compose wants every file it mounts. .env stays
    /// until retire_legacy.
    pub fn save(&self) -> Result<()> {
        let node = self.get("MIKAN_MODE") == Some("node");
        let plain: BTreeMap<&str, &str> =
            self.values.iter().filter(|(k, _)| secret(k).is_none()).map(|(k, v)| (k.as_str(), v.as_str())).collect();
        let mut json = serde_json::to_string_pretty(&plain)?;
        json.push('\n');
        let dir = self.root.join(SECRETS);
        match fs::DirBuilder::new().mode(0o700).create(&dir) {
            Ok(()) => {}
            Err(e) if e.kind() == std::io::ErrorKind::AlreadyExists => fs::set_permissions(&dir, fs::Permissions::from_mode(0o700))?,
            Err(e) => return Err(e).with_context(|| format!("create {}", dir.display())),
        }
        for s in SECRET_KEYS.iter() {
            let path = dir.join(s.file);
            if s.panel == node {
                let _ = fs::remove_file(&path);
                continue;
            }
            let value = self.get(s.key).unwrap_or_default();
            write_private(&path, value.as_bytes())?;
            // Only root can give the file to its reader: the installer is root, a test run by
            // anyone else keeps it.
            if rustix::process::geteuid().is_root() {
                std::os::unix::fs::chown(&path, Some(s.owner), Some(s.owner)).with_context(|| format!("chown {}", path.display()))?;
            }
            fs::set_permissions(&path, fs::Permissions::from_mode(0o400))?;
        }
        write_private(&self.root.join(FILE), json.as_bytes())
    }

    /// Removes .env once the containers run on config.json and secrets/.
    pub fn retire_legacy(&mut self) -> Result<()> {
        match fs::remove_file(self.root.join(LEGACY)) {
            Ok(()) => {}
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
            Err(e) => return Err(e).context("remove .env"),
        }
        self.legacy = false;
        Ok(())
    }

    /// The legacy .env this server runs on again (a rollback to a mikan that reads it).
    pub fn write_legacy(&self) -> Result<()> {
        write_private(&self.root.join(LEGACY), self.to_env_text().as_bytes())
    }
}

/// A value compose.yaml takes as it stands.
fn plain(value: &str) -> bool {
    value.bytes().all(|b| b.is_ascii_alphanumeric() || b"._:/@+=,-".contains(&b))
}

/// The PostgreSQL address of the panel, the one value that is a URL with a query.
const DSN_KEY: &str = "MIKAN_DATABASE_URL";

fn plain_dsn(value: &str) -> bool {
    value.bytes().all(|b| b.is_ascii_alphanumeric() || b"._:/@+=,-?&%".contains(&b))
}

/// Writes a file readable by root only, replacing it at once: a new file under its own
/// name (never a leftover one, never through a link), synced, then renamed over path.
pub fn write_private(path: &Path, data: &[u8]) -> Result<()> {
    let name = path.file_name().map(|n| n.to_string_lossy().into_owned()).unwrap_or_default();
    let nanos = SystemTime::now().duration_since(UNIX_EPOCH).map(|d| d.as_nanos()).unwrap_or(0);
    let tmp = path.with_file_name(format!(".{name}.{}.{nanos}.tmp", std::process::id()));
    let mut f = fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .mode(0o600)
        .custom_flags(libc::O_NOFOLLOW)
        .open(&tmp)
        .with_context(|| format!("create {}", tmp.display()))?;
    let written = f.write_all(data).and_then(|()| f.sync_all()).and_then(|()| fs::rename(&tmp, path));
    if let Err(e) = written {
        let _ = fs::remove_file(&tmp);
        return Err(e).with_context(|| format!("write {}", path.display()));
    }
    // The rename is only as durable as its directory.
    if let Some(dir) = path.parent().and_then(|d| fs::File::open(d).ok()) {
        let _ = dir.sync_all();
    }
    Ok(())
}

/// Whether name is the temporary file write_private makes for file, `.<file>.<pid>.<nanos>.tmp`:
/// a write cut short before its rename (a kill, a power cut) leaves it next to the file.
pub fn leftover_of(name: &str, file: &str) -> bool {
    let Some(rest) = name.strip_prefix('.').and_then(|n| n.strip_prefix(file)).and_then(|n| n.strip_suffix(".tmp")) else {
        return false;
    };
    let parts: Vec<&str> = rest.split('.').collect();
    matches!(parts[..], ["", pid, nanos] if [pid, nanos].iter().all(|p| !p.is_empty() && p.bytes().all(|b| b.is_ascii_digit())))
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::os::unix::fs::MetadataExt;

    fn dir(name: &str) -> PathBuf {
        let d = std::env::temp_dir().join(format!("config-{name}-{}", std::process::id()));
        let _ = fs::remove_dir_all(&d);
        fs::create_dir_all(&d).unwrap();
        d
    }

    // A panel's .env moves whole: the plain settings to config.json, the password and the
    // address to their own files of their readers, readable by them alone; .env goes last.
    #[test]
    fn a_legacy_env_moves_and_reads_back() {
        let d = dir("move");
        fs::write(
            d.join(LEGACY),
            "MIKAN_IMAGE=ghcr.io/x@sha256:ab\nPANEL_PORT=\"2053\"\nMIKAN_POSTGRES_PASSWORD=pw123456\nMIKAN_DATABASE_URL=postgresql://mikan:pw123456@localhost/mikan?host=/run/postgresql\n# note\n",
        )
        .unwrap();
        let mut c = Config::load(&d).unwrap();
        assert!(c.legacy());
        assert_eq!(c.get("PANEL_PORT"), Some("2053"));
        c.save().unwrap();
        let json = fs::read_to_string(d.join(FILE)).unwrap();
        assert!(json.contains("\"PANEL_PORT\": \"2053\"") && !json.contains("pw123456"), "{json}");
        let pw = d.join(SECRETS).join("postgres_password");
        let meta = fs::metadata(&pw).unwrap();
        assert_eq!(meta.mode() & 0o777, 0o400);
        if rustix::process::geteuid().is_root() {
            assert_eq!(meta.uid(), POSTGRES_UID);
            assert_eq!(fs::metadata(d.join(SECRETS).join("database_url")).unwrap().uid(), APP_UID);
        }
        assert_eq!(fs::metadata(d.join(SECRETS)).unwrap().mode() & 0o777, 0o700);
        assert!(!d.join(SECRETS).join("node_join").exists(), "a panel has no join key");
        c.retire_legacy().unwrap();
        assert!(!d.join(LEGACY).exists());
        let back = Config::load(&d).unwrap();
        assert!(!back.legacy());
        assert_eq!(back.get("MIKAN_POSTGRES_PASSWORD"), Some("pw123456"));
        assert_eq!(back.to_env_text(), c.to_env_text());
        assert!(back.secret_values().iter().any(|v| v.starts_with("postgresql://")));
        assert_eq!(back.compose_value("MIKAN_DATABASE_URL"), None, "a secret never goes into compose.yaml");
        fs::remove_dir_all(&d).unwrap();
    }

    // A node writes its join key, empty when unset, as compose mounts every file it names.
    #[test]
    fn a_node_has_its_join_key_file() {
        let d = dir("node");
        let mut c = Config::new(&d);
        c.set("MIKAN_MODE", "node").unwrap();
        c.save().unwrap();
        assert_eq!(fs::read_to_string(d.join(SECRETS).join("node_join")).unwrap(), "");
        c.set("MIKAN_NODE_JOIN", "mk1.abcdef").unwrap();
        c.save().unwrap();
        assert_eq!(Config::load(&d).unwrap().get("MIKAN_NODE_JOIN"), Some("mk1.abcdef"));
        assert!(!d.join(SECRETS).join("database_url").exists());
        fs::remove_dir_all(&d).unwrap();
    }

    #[test]
    fn values_go_into_compose_as_written() {
        let mut c = Config::new("x");
        for ok in ["ghcr.io/miroshka000/mikan@sha256:ab12", "0.4.4-rc.1+b2", "21355", ""] {
            c.set("K", ok).unwrap();
        }
        for bad in ["a$HOME", "${X}", "a b", "a #b", "\"q\"", "a\nb", "img?x=1"] {
            assert!(c.set("K", bad).is_err(), "{bad:?} was taken");
        }
        c.set("MIKAN_DATABASE_URL", "postgres://u:p%40ss@h/db?sslmode=require&connect_timeout=10").unwrap();
        let old = Config::from_env_text("x", "A=1\nB=\"2\"\nA=3\nE=\n");
        assert_eq!(old.to_env_text(), "A=3\nB=2\n");
    }

    #[test]
    fn nothing_installed_is_not_found() {
        let d = dir("none");
        let e = Config::load(&d).err().unwrap();
        assert!(e.root_cause().downcast_ref::<std::io::Error>().is_some_and(|io| io.kind() == std::io::ErrorKind::NotFound));
        fs::remove_dir_all(&d).unwrap();
    }

    #[test]
    fn private_files_are_private_and_leave_no_tmp() {
        use std::os::unix::fs::{PermissionsExt, symlink};
        let d = std::env::temp_dir().join(format!("config-file-{}", std::process::id()));
        let _ = fs::remove_dir_all(&d);
        fs::create_dir_all(&d).unwrap();
        let f = d.join(".env");
        write_private(&f, b"A=1\n").unwrap();
        write_private(&f, b"A=2\n").unwrap();
        assert_eq!(fs::read_to_string(&f).unwrap(), "A=2\n");
        assert_eq!(fs::metadata(&f).unwrap().permissions().mode() & 0o777, 0o600);
        // a link where the file goes is replaced, not written through
        let target = d.join("target");
        fs::write(&target, "keep").unwrap();
        fs::remove_file(&f).unwrap();
        symlink(&target, &f).unwrap();
        write_private(&f, b"A=3\n").unwrap();
        assert_eq!(fs::read_to_string(&target).unwrap(), "keep");
        assert_eq!(fs::read_to_string(&f).unwrap(), "A=3\n");
        assert_eq!(fs::read_dir(&d).unwrap().count(), 2, "no temporary file stays");
        fs::remove_dir_all(&d).unwrap();
    }

    #[test]
    fn a_leftover_is_told_by_its_name() {
        assert!(leftover_of(".node_join.4242.1761523200123456789.tmp", "node_join"));
        for other in [
            "node_join",
            ".node_join.tmp",
            ".node_join.1.tmp",
            ".node_join.1.2.3.tmp",
            ".node_join.x.2.tmp",
            ".node_join..2.tmp",
            ".node_join.1.2.tmp.x",
            ".database_url.1.2.tmp",
            ".node_joinx.1.2.tmp",
        ] {
            assert!(!leftover_of(other, "node_join"), "{other}");
        }
    }
}
