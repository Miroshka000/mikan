//! /opt/mikan/.env: the KEY=VALUE lines compose reads. Keys the installer does not know
//! and the order of lines stay as they are.

use std::fs;
use std::io::Write;
use std::os::unix::fs::OpenOptionsExt;
use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

use anyhow::{Context, Result, bail};

pub struct EnvFile {
    path: PathBuf,
    lines: Vec<String>,
}

impl EnvFile {
    pub fn load(path: impl AsRef<Path>) -> Result<Self> {
        let path = path.as_ref().to_path_buf();
        let text = fs::read_to_string(&path).with_context(|| format!("read {}", path.display()))?;
        Ok(Self::parse(path, &text))
    }

    pub fn new(path: impl AsRef<Path>) -> Self {
        Self::parse(path.as_ref().to_path_buf(), "")
    }

    /// The file at path as text kept from before (an update's copy of the old .env).
    pub fn from_text(path: impl AsRef<Path>, text: &str) -> Self {
        Self::parse(path.as_ref().to_path_buf(), text)
    }

    fn parse(path: PathBuf, text: &str) -> Self {
        Self { path, lines: text.lines().map(str::to_owned).collect() }
    }

    /// The value as compose reads it: the last of a repeated key, without quotes.
    pub fn get(&self, key: &str) -> Option<&str> {
        let raw = self.lines.iter().rev().find_map(|l| l.strip_prefix(key)?.strip_prefix('='))?;
        let v = ['"', '\''].iter().find_map(|&q| raw.strip_prefix(q)?.strip_suffix(q)).unwrap_or(raw);
        Some(v).filter(|v| !v.is_empty())
    }

    /// Sets key to value, in place when the key is there; a key repeated by a hand edit is
    /// left once, because compose takes the last and a change of the first would not show.
    pub fn set(&mut self, key: &str, value: &str) -> Result<()> {
        let fits = if key == DSN_KEY { plain_dsn(value) } else { plain(value) };
        if !fits {
            bail!("{key}: the value has characters that compose reads differently ($, quotes, spaces, #)");
        }
        let line = format!("{key}={value}");
        let at: Vec<usize> = self
            .lines
            .iter()
            .enumerate()
            .filter(|(_, l)| l.strip_prefix(key).is_some_and(|r| r.starts_with('=')))
            .map(|(i, _)| i)
            .collect();
        match at.split_last() {
            Some((&last, earlier)) => {
                self.lines[last] = line;
                for &i in earlier.iter().rev() {
                    self.lines.remove(i);
                }
            }
            None => self.lines.push(line),
        }
        Ok(())
    }

    /// Removes every line of key.
    pub fn remove(&mut self, key: &str) {
        self.lines.retain(|l| !l.strip_prefix(key).is_some_and(|r| r.starts_with('=')));
    }

    pub fn render(&self) -> String {
        let mut s = self.lines.join("\n");
        s.push('\n');
        s
    }

    /// Writes the file whole (a new file, then a rename), readable by root only: the join
    /// key of a node is in it.
    pub fn save(&self) -> Result<()> {
        write_private(&self.path, self.render().as_bytes())
    }
}

/// A value compose reads as it stands: it interpolates `$`, strips quotes and ends a line
/// at ` #`, so anything but plain image names, versions, ports and keys is refused.
fn plain(value: &str) -> bool {
    value.bytes().all(|b| b.is_ascii_alphanumeric() || b"._:/@+=,-".contains(&b))
}

/// The PostgreSQL address of the panel, the one value that is a URL with a query.
const DSN_KEY: &str = "MIKAN_DATABASE_URL";

/// A DSN also takes its query (`?host=…&sslmode=…`) and percent-escapes: compose gives
/// `?`, `&` and `%` no meaning, only the image names and keys of the rest never have them.
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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn keeps_unknown_lines_and_order() {
        let mut e = EnvFile::parse("x".into(), "MIKAN_IMAGE=mikan:0.3.8\n# note\nPANEL_PORT=21355\nMIKAN_UFW=1\n");
        assert_eq!(e.get("MIKAN_IMAGE"), Some("mikan:0.3.8"));
        assert_eq!(e.get("MIKAN"), None);
        e.set("MIKAN_IMAGE", "ghcr.io/o0hhs/mikan@sha256:abc").unwrap();
        e.set("MIKAN_VERSION", "0.3.9").unwrap();
        assert_eq!(e.render(), "MIKAN_IMAGE=ghcr.io/o0hhs/mikan@sha256:abc\n# note\nPANEL_PORT=21355\nMIKAN_UFW=1\nMIKAN_VERSION=0.3.9\n");
        assert!(e.set("X", "a\nb").is_err());
    }

    // Compose interpolates $, strips quotes and ends a line at " #": such a value would
    // reach a container as something else than what was written.
    #[test]
    fn values_compose_reads_as_written() {
        let mut e = EnvFile::parse("x".into(), "");
        for ok in ["ghcr.io/o0hhs/mikan@sha256:ab12", "0.4.4-rc.1+b2", "mikan1.AbC_-9", "21355", ""] {
            e.set("K", ok).unwrap();
        }
        for bad in ["a$HOME", "${X}", "a b", "a #b", "\"q\"", "'q'", "a`b", "a\\b", "a;b", "a\nb", "img?x=1", "a&b", "a%20b"] {
            assert!(e.set("K", bad).is_err(), "{bad:?} was taken");
        }
    }

    // The database address carries a query and escapes; nothing else needs them, and it
    // still refuses what compose would read differently.
    #[test]
    fn the_dsn_takes_its_query_and_nothing_compose_reinterprets() {
        let mut e = EnvFile::parse("x".into(), "");
        for ok in
            ["postgresql://mikan:Ab9@localhost/mikan?host=/run/postgresql", "postgres://u:p%40ss@h/db?sslmode=require&connect_timeout=10"]
        {
            e.set("MIKAN_DATABASE_URL", ok).unwrap();
            assert_eq!(e.get("MIKAN_DATABASE_URL"), Some(ok));
        }
        for bad in [
            "postgres://u:$P@h/db",
            "postgres://u:p@h/db #x",
            "postgres://u:p@h/db?a='b'",
            "postgres://u:p w@h/db",
            "postgres://h/db?a=1;b",
        ] {
            assert!(e.set("MIKAN_DATABASE_URL", bad).is_err(), "{bad:?} was taken");
        }
        assert!(e.set("MIKAN_IMAGE", "postgresql://h/db?host=/run").is_err(), "a query only in the DSN");
    }

    #[test]
    fn a_key_is_removed_and_text_is_parsed() {
        let mut e = EnvFile::from_text("x", "A=1\nMIKAN_DATABASE_URL=postgresql://h/db\nAB=2\nMIKAN_DATABASE_URL=again\n");
        e.remove("MIKAN_DATABASE_URL");
        e.remove("A");
        assert_eq!(e.render(), "AB=2\n");
    }

    // A key repeated by a hand edit: compose reads the last, so the update must see and
    // change what compose sees.
    #[test]
    fn repeated_keys_and_quotes() {
        let mut e = EnvFile::parse("x".into(), "MIKAN_IMAGE=old\nPANEL_PORT=\"2053\"\nMIKAN_IMAGE=newer\nQ='x'\nE=\n");
        assert_eq!(e.get("MIKAN_IMAGE"), Some("newer"));
        assert_eq!(e.get("PANEL_PORT"), Some("2053"));
        assert_eq!(e.get("Q"), Some("x"));
        assert_eq!(e.get("E"), None);
        e.set("MIKAN_IMAGE", "fresh").unwrap();
        assert_eq!(e.render(), "PANEL_PORT=\"2053\"\nMIKAN_IMAGE=fresh\nQ='x'\nE=\n");
    }

    #[test]
    fn private_files_are_private_and_leave_no_tmp() {
        use std::os::unix::fs::{PermissionsExt, symlink};
        let d = std::env::temp_dir().join(format!("envfile-{}", std::process::id()));
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
}
