//! Files shared with the panel container. The container runs unprivileged and owns
//! data/panel: whatever lies there may be a link, a directory, a FIFO or a huge file
//! planted by a compromised panel, and any directory in the path may be swapped for a link
//! at any moment. root reaches such files only through a `Dir`: a directory opened below a
//! trusted root without following a single link, with every later step relative to that
//! open directory, so a swap after the open changes nothing.

use std::ffi::{CStr, CString};
use std::fs::{self, File};
use std::io::{Read, Write};
use std::os::fd::OwnedFd;
use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

use anyhow::{Context, Result, bail};
use rustix::fs::{AtFlags, CWD, FileType, Gid, Mode, OFlags, Uid};
use rustix::io::Errno;

/// The panel's user inside its container.
pub const PANEL_UID: u32 = 65532;

/// Most entries a planted directory is removed with; more is not the panel's doing.
const REMOVE_LIMIT: usize = 100_000;
const DEPTH_LIMIT: usize = 32;

const DIR_FLAGS: OFlags = OFlags::RDONLY.union(OFlags::DIRECTORY).union(OFlags::NOFOLLOW).union(OFlags::CLOEXEC);

/// A directory the panel may write to, opened without following links.
pub struct Dir {
    fd: OwnedFd,
    path: PathBuf,
}

/// A bare file name: one component, no path.
fn check_name(name: &str) -> Result<()> {
    if name.is_empty() || name == "." || name == ".." || name.contains(['/', '\0']) {
        bail!("{name:?} is not a file name");
    }
    Ok(())
}

fn errno(e: Errno, what: impl FnOnce() -> String) -> anyhow::Error {
    anyhow::Error::new(std::io::Error::from(e)).context(what())
}

fn panel_owner() -> (Option<Uid>, Option<Gid>) {
    (Some(Uid::from_raw(PANEL_UID)), Some(Gid::from_raw(PANEL_UID)))
}

/// Gives a file or directory to the panel's user. Only root can: the installer is root, and
/// anyone else (a test) keeps the file.
fn hand_over(fd: impl std::os::fd::AsFd) -> std::result::Result<(), Errno> {
    if !rustix::process::geteuid().is_root() {
        return Ok(());
    }
    let (uid, gid) = panel_owner();
    rustix::fs::fchown(fd, uid, gid)
}

impl Dir {
    /// Opens rel ("data/panel/update") below root. root is root's own path and may be a
    /// link; everything below it must be a real directory, a link there is refused. A
    /// missing last component is made (for the panel) with create_last, or answers None.
    pub fn open(root: &Path, rel: &str, create_last: bool) -> Result<Option<Dir>> {
        let base = fs::canonicalize(root).with_context(|| format!("resolve {}", root.display()))?;
        let mut fd = rustix::fs::openat(CWD, &base, DIR_FLAGS.difference(OFlags::NOFOLLOW), Mode::empty())
            .map_err(|e| errno(e, || format!("open {}", base.display())))?;
        let mut path = base;
        let parts: Vec<&str> = if rel.is_empty() || rel == "." { Vec::new() } else { rel.split('/').collect() };
        for (i, part) in parts.iter().enumerate() {
            check_name(part)?;
            path.push(part);
            let last = i + 1 == parts.len();
            fd = match rustix::fs::openat(&fd, *part, DIR_FLAGS, Mode::empty()) {
                Ok(next) => next,
                Err(Errno::NOENT) if last && create_last => {
                    match rustix::fs::mkdirat(&fd, *part, Mode::from_raw_mode(0o755)) {
                        Ok(()) | Err(Errno::EXIST) => {}
                        Err(e) => return Err(errno(e, || format!("create {}", path.display()))),
                    }
                    let next = rustix::fs::openat(&fd, *part, DIR_FLAGS, Mode::empty())
                        .map_err(|e| errno(e, || format!("open {}", path.display())))?;
                    hand_over(&next).map_err(|e| errno(e, || format!("chown {}", path.display())))?;
                    next
                }
                Err(Errno::NOENT) => return Ok(None),
                Err(Errno::LOOP | Errno::NOTDIR) => bail!("{} is a link or not a directory", path.display()),
                Err(e) => return Err(errno(e, || format!("open {}", path.display()))),
            };
        }
        Ok(Some(Dir { fd, path }))
    }

    /// Reads a small regular file; None when it is not there. A link, a directory, a FIFO
    /// and a file larger than limit are errors.
    pub fn read(&self, name: &str, limit: u64) -> Result<Option<Vec<u8>>> {
        check_name(name)?;
        let shown = || format!("{}", self.path.join(name).display());
        let fd =
            match rustix::fs::openat(&self.fd, name, OFlags::RDONLY | OFlags::NOFOLLOW | OFlags::NONBLOCK | OFlags::CLOEXEC, Mode::empty())
            {
                Ok(fd) => fd,
                Err(Errno::NOENT) => return Ok(None),
                Err(Errno::LOOP) => bail!("{} is a link", shown()),
                Err(e) => return Err(errno(e, || format!("open {}", shown()))),
            };
        let file = File::from(fd);
        if !file.metadata()?.is_file() {
            bail!("{} is not a regular file", shown());
        }
        let mut data = Vec::new();
        file.take(limit + 1).read_to_end(&mut data)?;
        if data.len() as u64 > limit {
            bail!("{} is larger than {limit} bytes", shown());
        }
        Ok(Some(data))
    }

    /// Whether anything, a link or a directory included, is called name.
    pub fn exists(&self, name: &str) -> bool {
        check_name(name).is_ok() && rustix::fs::statat(&self.fd, name, AtFlags::SYMLINK_NOFOLLOW).is_ok()
    }

    /// The names in this directory; one that is not UTF-8 is none the panel writes and is
    /// left out.
    pub fn names(&self) -> Result<Vec<String>> {
        Ok(children(&self.fd)?.into_iter().filter_map(|n| n.into_string().ok()).collect())
    }

    /// Writes name for the panel to read: a fresh file (never an existing one, never
    /// through a link) owned by the panel, then renamed over name, which replaces a link
    /// at name instead of following it.
    pub fn write(&self, name: &str, data: &[u8], mode: u32) -> Result<()> {
        check_name(name)?;
        let nanos = SystemTime::now().duration_since(UNIX_EPOCH).map(|d| d.as_nanos()).unwrap_or(0);
        let tmp = format!(".{name}.{}.{nanos}", std::process::id());
        let shown = || format!("{}", self.path.join(name).display());
        let fd = rustix::fs::openat(
            &self.fd,
            tmp.as_str(),
            OFlags::WRONLY | OFlags::CREATE | OFlags::EXCL | OFlags::NOFOLLOW | OFlags::CLOEXEC,
            Mode::from_raw_mode(mode),
        )
        .map_err(|e| errno(e, || format!("create {}", self.path.join(&tmp).display())))?;
        let written = (|| -> Result<()> {
            let mut file = File::from(fd);
            file.write_all(data)?;
            file.sync_all()?;
            hand_over(&file)?;
            rustix::fs::fchmod(&file, Mode::from_raw_mode(mode))?;
            rustix::fs::renameat(&self.fd, tmp.as_str(), &self.fd, name)?;
            Ok(())
        })();
        if written.is_err() {
            let _ = rustix::fs::unlinkat(&self.fd, tmp.as_str(), AtFlags::empty());
        }
        written.with_context(|| format!("write {}", shown()))
    }

    /// Removes what the panel left at name: a file, a link itself (never its target), or
    /// a directory planted in place of a file.
    pub fn discard(&self, name: &str) -> Result<()> {
        check_name(name)?;
        remove(&self.fd, &CString::new(name)?, 0, &mut 0).with_context(|| format!("remove {}", self.path.join(name).display()))
    }

    /// Hands every file below this directory to the panel, links themselves not their
    /// targets.
    pub fn own(&self) -> Result<()> {
        let (uid, gid) = panel_owner();
        rustix::fs::fchown(&self.fd, uid, gid)?;
        chown_below(&self.fd, 0).with_context(|| format!("chown {}", self.path.display()))
    }
}

fn remove(parent: &OwnedFd, name: &CStr, depth: usize, seen: &mut usize) -> Result<()> {
    let st = match rustix::fs::statat(parent, name, AtFlags::SYMLINK_NOFOLLOW) {
        Ok(st) => st,
        Err(Errno::NOENT) => return Ok(()),
        Err(e) => return Err(std::io::Error::from(e).into()),
    };
    if FileType::from_raw_mode(st.st_mode) != FileType::Directory {
        return rustix::fs::unlinkat(parent, name, AtFlags::empty()).or_else(ignore_gone).map_err(Into::into);
    }
    if depth >= DEPTH_LIMIT {
        bail!("directory nested deeper than {DEPTH_LIMIT} levels");
    }
    let dir = rustix::fs::openat(parent, name, DIR_FLAGS, Mode::empty()).map_err(std::io::Error::from)?;
    for entry in children(&dir)? {
        *seen += 1;
        if *seen > REMOVE_LIMIT {
            bail!("more than {REMOVE_LIMIT} entries");
        }
        remove(&dir, &entry, depth + 1, seen)?;
    }
    rustix::fs::unlinkat(parent, name, AtFlags::REMOVEDIR).or_else(ignore_gone).map_err(Into::into)
}

fn ignore_gone(e: Errno) -> std::result::Result<(), Errno> {
    if e == Errno::NOENT { Ok(()) } else { Err(e) }
}

/// The names in a directory, without "." and "..".
fn children(dir: &OwnedFd) -> Result<Vec<CString>> {
    let mut names = Vec::new();
    for entry in rustix::fs::Dir::read_from(dir).map_err(std::io::Error::from)? {
        let entry = entry.map_err(std::io::Error::from)?;
        let name = entry.file_name().to_owned();
        if name.as_bytes() != b"." && name.as_bytes() != b".." {
            names.push(name);
        }
    }
    Ok(names)
}

fn chown_below(dir: &OwnedFd, depth: usize) -> Result<()> {
    if depth >= DEPTH_LIMIT {
        bail!("directory nested deeper than {DEPTH_LIMIT} levels");
    }
    let (uid, gid) = panel_owner();
    for name in children(dir)? {
        let Ok(st) = rustix::fs::statat(dir, name.as_c_str(), AtFlags::SYMLINK_NOFOLLOW) else { continue };
        if FileType::from_raw_mode(st.st_mode) == FileType::Directory {
            if let Ok(sub) = rustix::fs::openat(dir, name.as_c_str(), DIR_FLAGS, Mode::empty()) {
                rustix::fs::fchown(&sub, uid, gid).map_err(std::io::Error::from)?;
                chown_below(&sub, depth + 1)?;
            }
        } else {
            rustix::fs::chownat(dir, name.as_c_str(), uid, gid, AtFlags::SYMLINK_NOFOLLOW).map_err(std::io::Error::from)?;
        }
    }
    Ok(())
}

/// What the containers see of /opt/mikan/data: only data/panel and data/node are theirs,
/// each mounted alone, made if missing and handed to the panel's user with all it holds.
pub fn own_dirs(root: &Path, panel: bool) -> Result<()> {
    let data = root.join("data");
    fs::create_dir_all(&data)?;
    let mut parts = vec!["node"];
    if panel {
        parts.push("panel");
    }
    for part in parts {
        let dir = Dir::open(&data, part, true)?.with_context(|| format!("no {}", data.join(part).display()))?;
        dir.own()?;
    }
    Ok(())
}

/// data itself is root's, 0700: the panel cannot rename its directory and leave a link in
/// its place. Only once no container mounts all of ./data any more (an older compose.yaml
/// does, and a root-owned data would lock its panel out).
pub fn seal(root: &Path) -> Result<()> {
    use std::os::unix::fs::PermissionsExt;
    let data = fs::canonicalize(root.join("data"))?;
    std::os::unix::fs::chown(&data, Some(0), Some(0))?;
    fs::set_permissions(&data, fs::Permissions::from_mode(0o700))?;
    Ok(())
}

/// own_dirs and seal, for a server whose compose.yaml is the current one.
pub fn layout(root: &Path, panel: bool) -> Result<()> {
    own_dirs(root, panel)?;
    seal(root)
}

/// A file only root reads: the panel cannot see or replace it.
pub fn write_root(path: &Path, data: &[u8]) -> Result<()> {
    use std::os::unix::fs::PermissionsExt;
    if let Some(dir) = path.parent() {
        fs::create_dir_all(dir)?;
        fs::set_permissions(dir, fs::Permissions::from_mode(0o700))?;
    }
    crate::envfile::write_private(path, data)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::os::unix::fs::{MetadataExt, symlink};

    fn tmpdir(name: &str) -> PathBuf {
        let d = std::env::temp_dir().join(format!("panelfs-{name}-{}", std::process::id()));
        let _ = fs::remove_dir_all(&d);
        fs::create_dir_all(&d).unwrap();
        d
    }

    fn open(root: &Path, rel: &str) -> Dir {
        Dir::open(root, rel, false).unwrap().unwrap()
    }

    // A link planted in the panel's directory is never followed: not on read, not on write.
    #[test]
    fn links_are_not_followed() {
        let d = tmpdir("links");
        let secret = d.join("secret");
        fs::write(&secret, "root only").unwrap();
        symlink(&secret, d.join("request")).unwrap();
        let dir = open(&d, ".");
        assert!(dir.read("request", 4096).is_err());
        symlink(&secret, d.join("status.json")).unwrap();
        dir.write("status.json", b"{}", 0o644).unwrap();
        assert_eq!(fs::read_to_string(&secret).unwrap(), "root only");
        assert!(!fs::symlink_metadata(d.join("status.json")).unwrap().file_type().is_symlink());
        assert_eq!(fs::read(d.join("status.json")).unwrap(), b"{}");
        // discard removes the link, not what it points to
        symlink(&secret, d.join("gone")).unwrap();
        dir.discard("gone").unwrap();
        assert!(fs::symlink_metadata(d.join("gone")).is_err());
        assert_eq!(fs::read_to_string(&secret).unwrap(), "root only");
        fs::remove_dir_all(&d).unwrap();
    }

    // Any directory of the path may be the link: data/panel renamed away and a link to
    // another directory put in its place.
    #[test]
    fn links_in_the_path_are_refused() {
        let d = tmpdir("path");
        let outside = tmpdir("path-outside");
        fs::create_dir_all(outside.join("update")).unwrap();
        fs::create_dir_all(d.join("data")).unwrap();
        symlink(&outside, d.join("data/panel")).unwrap();
        assert!(Dir::open(&d, "data/panel/update", true).is_err(), "an intermediate link");
        fs::remove_file(d.join("data/panel")).unwrap();
        fs::create_dir_all(d.join("data/panel")).unwrap();
        symlink(outside.join("update"), d.join("data/panel/update")).unwrap();
        assert!(Dir::open(&d, "data/panel/update", true).is_err(), "the last component a link");
        assert!(!outside.join("update/status.json").exists());
        // the root itself may be a link: it is the admin's, not the panel's
        let alias = d.with_file_name(format!("{}-alias", d.file_name().unwrap().to_string_lossy()));
        symlink(&d, &alias).unwrap();
        fs::remove_file(d.join("data/panel/update")).unwrap();
        assert!(Dir::open(&alias, "data/panel", false).unwrap().is_some());
        fs::remove_file(&alias).unwrap();
        fs::remove_dir_all(&d).unwrap();
        fs::remove_dir_all(&outside).unwrap();
    }

    // The directory is swapped for a link after it was opened: the open handle still
    // leads to the real one.
    #[test]
    fn a_swap_after_the_open_changes_nothing() {
        let d = tmpdir("swap");
        let outside = tmpdir("swap-outside");
        fs::create_dir_all(d.join("update")).unwrap();
        let dir = open(&d, "update");
        fs::rename(d.join("update"), d.join("update.real")).unwrap();
        symlink(&outside, d.join("update")).unwrap();
        dir.write("status.json", b"{}", 0o644).unwrap();
        assert!(d.join("update.real/status.json").exists());
        assert!(fs::read_dir(&outside).unwrap().next().is_none(), "nothing went through the link");
        fs::remove_dir_all(&d).unwrap();
        fs::remove_dir_all(&outside).unwrap();
    }

    #[test]
    fn sizes_and_types() {
        let d = tmpdir("sizes");
        let dir = open(&d, ".");
        assert!(dir.read("missing", 10).unwrap().is_none());
        fs::write(d.join("big"), vec![b'x'; 11]).unwrap();
        assert!(dir.read("big", 10).is_err());
        fs::write(d.join("ok"), b"0123456789").unwrap();
        assert_eq!(dir.read("ok", 10).unwrap().unwrap().len(), 10);
        fs::create_dir(d.join("dir")).unwrap();
        assert!(dir.read("dir", 10).is_err());
        let fifo = d.join("fifo");
        rustix::fs::mknodat(CWD, &fifo, FileType::Fifo, Mode::from_raw_mode(0o600), 0).unwrap();
        assert!(dir.read("fifo", 10).is_err(), "a FIFO without a writer must not block");
        symlink("/dev/zero", d.join("zero")).unwrap();
        assert!(dir.read("zero", 10).is_err());
        assert!(dir.read("a/b", 10).is_err());
        assert!(dir.read("..", 10).is_err());
        fs::remove_dir_all(&d).unwrap();
    }

    #[test]
    fn discards_whatever_the_panel_left() {
        let d = tmpdir("discard");
        let dir = open(&d, ".");
        fs::create_dir_all(d.join("request/deep/er")).unwrap();
        fs::write(d.join("request/deep/er/f"), "x").unwrap();
        symlink("/", d.join("request/up")).unwrap();
        dir.discard("request").unwrap();
        assert!(!dir.exists("request"));
        rustix::fs::mknodat(CWD, d.join("fifo"), FileType::Fifo, Mode::from_raw_mode(0o600), 0).unwrap();
        dir.discard("fifo").unwrap();
        assert!(!dir.exists("fifo"));
        dir.discard("never-there").unwrap();
        fs::remove_dir_all(&d).unwrap();
    }

    #[test]
    fn names_of_a_directory() {
        let d = tmpdir("names");
        fs::write(d.join("b"), "").unwrap();
        fs::create_dir(d.join("a")).unwrap();
        symlink("/", d.join("c")).unwrap();
        let mut names = open(&d, ".").names().unwrap();
        names.sort();
        assert_eq!(names, ["a", "b", "c"]);
        fs::remove_dir_all(&d).unwrap();
    }

    #[test]
    fn creates_the_last_component_only() {
        let d = tmpdir("create");
        assert!(Dir::open(&d, "data/panel/update", true).unwrap().is_none(), "data/panel must exist already");
        assert!(Dir::open(&d, "update", false).unwrap().is_none());
        // root is not the panel's uid here: the chown is the one thing a test cannot see
        let made = Dir::open(&d, "update", true);
        if system_is_root() {
            made.unwrap().unwrap().write("status.json", b"{}", 0o644).unwrap();
            assert_eq!(fs::metadata(d.join("update/status.json")).unwrap().uid(), PANEL_UID);
        }
        fs::remove_dir_all(&d).unwrap();
    }

    // data is root's and sealed; the two directories the containers mount are the panel's,
    // with everything in them, links as links. Needs root to chown, as the installer does.
    #[test]
    fn data_is_root_and_the_two_directories_are_the_panels() {
        use std::os::unix::fs::PermissionsExt;
        if !system_is_root() {
            return;
        }
        let d = tmpdir("layout");
        fs::create_dir_all(d.join("data/panel/tls")).unwrap();
        fs::write(d.join("data/panel/tls/k"), "k").unwrap();
        symlink("/etc/passwd", d.join("data/panel/link")).unwrap();
        fs::create_dir_all(d.join("data/node")).unwrap();
        own_dirs(&d, true).unwrap();
        assert_eq!(fs::metadata(d.join("data/panel/tls/k")).unwrap().uid(), PANEL_UID);
        assert_eq!(fs::symlink_metadata(d.join("data/panel/link")).unwrap().uid(), PANEL_UID, "the link itself");
        assert_ne!(fs::metadata("/etc/passwd").unwrap().uid(), PANEL_UID, "its target is not touched");
        // the old compose mounts all of data: until it is gone data stays the panel's
        assert_eq!(fs::metadata(d.join("data")).unwrap().uid(), 0);
        seal(&d).unwrap();
        let m = fs::metadata(d.join("data")).unwrap();
        assert_eq!((m.uid(), m.permissions().mode() & 0o777), (0, 0o700));
        // a link where data/panel should be is refused, not followed
        fs::remove_dir_all(d.join("data/panel")).unwrap();
        symlink("/etc", d.join("data/panel")).unwrap();
        assert!(own_dirs(&d, true).is_err());
        assert_ne!(fs::metadata("/etc").unwrap().uid(), PANEL_UID);
        fs::remove_dir_all(&d).unwrap();
    }

    fn system_is_root() -> bool {
        rustix::process::geteuid().is_root()
    }
}
