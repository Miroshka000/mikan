//! Backups and restore. A backup is a tar.gz of what mikan keeps in /opt/mikan: .env,
//! compose.yaml, a consistent copy of the panel's database, its certificates and the node's
//! data. It holds every secret of the server, so it is born private: the directory is 0700
//! and the archive is created 0600 before tar writes a byte into it.
//!
//! A restore does not trust the archive (it may be somebody else's file): the names and
//! types are checked, it is unpacked into a directory of its own, and only then, with the
//! containers stopped and the current data in a snapshot, does it replace anything.

use std::fs::{self, File, OpenOptions};
use std::io::ErrorKind;
use std::os::unix::fs::{DirBuilderExt, OpenOptionsExt, PermissionsExt};
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::time::Duration;

use anyhow::{Context, Result, bail};

use crate::clock::Utc;
use crate::envfile::EnvFile;
use crate::lock::{self, Wait};
use crate::ops::Install;
use crate::panelfs::{self, Dir};
use crate::{DIR, docker, setup, signals, system};

/// The kinds of archive mikan makes on its own; the admin's are "mikan".
pub const PRE_UPDATE: &str = "pre-update";
/// An update that moves SQLite to PostgreSQL: the archives with the original SQLite. A
/// failed move leaves .env on PostgreSQL, so its retries are "pre-update" and can never
/// rotate these away.
pub const PRE_POSTGRES: &str = "pre-postgres";
const PRE_RESTORE: &str = "pre-restore";

/// How many archives of a kind are kept; the admin's own are never removed.
fn keep(kind: &str) -> Option<usize> {
    match kind {
        PRE_UPDATE => Some(5),
        PRE_POSTGRES | PRE_RESTORE => Some(3),
        _ => None,
    }
}

/// Where the panel's database lies below /opt/mikan: a backup's PostgreSQL dump or
/// SQLite copy, a restore's dump, and the SQLite file itself with its journal.
const DUMP: &str = "data/panel/backup.dump";
const SQLITE_COPY: &str = "data/panel/backup.db";
const RESTORE_DUMP: &str = "data/panel/restore.dump";
const SQLITE: &str = "data/panel/mikan.db";
const SQLITE_WAL: &str = "data/panel/mikan.db-wal";
const SQLITE_SHM: &str = "data/panel/mikan.db-shm";

/// The name in data/panel of one of the paths above.
fn panel_name(rel: &str) -> &str {
    rel.rsplit_once('/').map_or(rel, |(_, n)| n)
}

/// A dump of PostgreSQL at DUMP; what an interrupted one left there goes first.
fn dump_postgres(panel: &Dir) -> Result<()> {
    docker::postgres_ready()?;
    panel.discard(panel_name(DUMP))?;
    docker::database(&["backup", &format!("/{DUMP}")])?;
    Ok(())
}

/// Room left for a dump and an archive beyond the estimate, which is generous anyway.
const MARGIN: u64 = 256 << 20;

/// A full disk half way through a dump or an archive is found out before it starts: the
/// data as they are plus the database's volume (a dump is smaller than its tables and
/// indexes) must fit with a margin.
fn ensure_room(root: &Path, postgres: bool) -> Result<()> {
    let Some(free) = system::disk_free_bytes(root) else { return Ok(()) };
    let db = if postgres { docker::volume_mountpoint(docker::PG_VOLUME).map_or(0, |p| system::tree_bytes(&p)) } else { 0 };
    let need = system::tree_bytes(&root.join("data")) + db + MARGIN;
    if free < need {
        bail!(
            "not enough free disk for a backup: {} MB free in {}, about {} MB needed; free some space (old archives are in {}) and try again",
            free >> 20,
            root.display(),
            need >> 20,
            root.join("backups").display()
        );
    }
    Ok(())
}

/// What a restore may put back, below /opt/mikan: the settings, and the data of the panel
/// and the node. Anything else in an archive is not a mikan backup.
const TOP: [&str; 2] = [".env", "compose.yaml"];

/// The backups' directory, private.
fn backups_dir(root: &Path) -> Result<PathBuf> {
    let dir = root.join("backups");
    match fs::DirBuilder::new().mode(0o700).create(&dir) {
        Ok(()) => {}
        Err(e) if e.kind() == ErrorKind::AlreadyExists => fs::set_permissions(&dir, fs::Permissions::from_mode(0o700))?,
        Err(e) => return Err(e).with_context(|| format!("create {}", dir.display())),
    }
    Ok(dir)
}

/// A new file kind-<time>.tar.gz in dir, for its owner only; never one that is there.
fn create(dir: &Path, kind: &str) -> Result<(File, PathBuf)> {
    let stamp = Utc::now().stamp();
    for n in 0..100 {
        let name = if n == 0 { format!("{kind}-{stamp}.tar.gz") } else { format!("{kind}-{stamp}-{n}.tar.gz") };
        let path = dir.join(name);
        match OpenOptions::new().write(true).create_new(true).mode(0o600).custom_flags(libc::O_NOFOLLOW).open(&path) {
            Ok(f) => return Ok((f, path)),
            Err(e) if e.kind() == ErrorKind::AlreadyExists => {}
            Err(e) => return Err(e).with_context(|| format!("create {}", path.display())),
        }
    }
    bail!("no free name for a backup in {}", dir.display())
}

/// How hard an archive is compressed: an archive made while the panel is stopped is
/// downtime, so it takes gzip's fastest level (a PostgreSQL dump is compressed already).
#[derive(Clone, Copy, PartialEq)]
enum Squeeze {
    Normal,
    Fast,
}

/// tar of members (paths below root) into a new private archive in dir.
fn pack(root: &Path, dir: &Path, kind: &str, members: &[String], squeeze: Squeeze) -> Result<PathBuf> {
    let (file, path) = create(dir, kind)?;
    // What the node's panel and the updater say to each other (data/node/update) is of the
    // moment: a restore must not bring back an old request, or a status that says an update
    // runs. The panel's own directory is not a member at all.
    let out = match squeeze {
        Squeeze::Normal => Command::new("tar")
            .args(["-czf", "-", "--exclude=data/node/update", "-C"])
            .arg(root)
            .args(members)
            .stdin(Stdio::null())
            .stdout(Stdio::from(file))
            .stderr(Stdio::piped())
            .output(),
        // tar into gzip's fastest level: any tar (BusyBox's too) and any gzip take this.
        Squeeze::Fast => fast_tar(root, members, file),
    };
    match out {
        // GNU tar exits 1 when a file changed while it was read (a node's counters): the
        // archive is whole, that one file is as of a moment ago.
        Ok(o)
            if o.status.success()
                || (o.status.code() == Some(1) && String::from_utf8_lossy(&o.stderr).contains("changed as we read it")) =>
        {
            Ok(path)
        }
        Ok(o) => {
            let _ = fs::remove_file(&path);
            bail!("tar failed ({}): {}", o.status, String::from_utf8_lossy(&o.stderr).trim())
        }
        Err(e) => {
            let _ = fs::remove_file(&path);
            Err(e).context("run tar")
        }
    }
}

/// `tar -c members | gzip -1 > file`: tar's own status and complaint, or gzip's when gzip
/// failed.
fn fast_tar(root: &Path, members: &[String], file: File) -> std::io::Result<std::process::Output> {
    let mut tar = Command::new("tar")
        .args(["-cf", "-", "--exclude=data/node/update", "-C"])
        .arg(root)
        .args(members)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()?;
    let piped = tar.stdout.take().expect("tar's stdout is piped");
    let gzip = Command::new("gzip").arg("-1").stdin(Stdio::from(piped)).stdout(Stdio::from(file)).stderr(Stdio::piped()).spawn();
    let out = tar.wait_with_output()?;
    let gz = gzip?.wait_with_output()?;
    if !gz.status.success() {
        return Ok(gz);
    }
    Ok(out)
}

/// Removes the oldest archives of a kind beyond what it keeps.
fn prune(dir: &Path, kind: &str) {
    let Some(keep) = keep(kind) else { return };
    let prefix = format!("{kind}-");
    let mut names: Vec<String> = fs::read_dir(dir)
        .map(|d| {
            d.flatten()
                .filter_map(|e| e.file_name().into_string().ok())
                .filter(|n| n.starts_with(&prefix) && n.ends_with(".tar.gz"))
                .collect()
        })
        .unwrap_or_default();
    names.sort();
    let excess = names.len().saturating_sub(keep);
    for name in names.into_iter().take(excess) {
        let _ = fs::remove_file(dir.join(name));
    }
}

fn exists(root: &Path, rel: &str) -> bool {
    fs::symlink_metadata(root.join(rel)).is_ok()
}

/// The backup the admin asks for.
pub fn backup(say: &mut dyn FnMut(&str)) -> Result<PathBuf> {
    backup_as("mikan", say)
}

/// A backup of the database (a consistent copy while the panel runs), certificates and
/// settings, in /opt/mikan/backups. kind names the file: an update's own are rotated.
pub fn backup_as(kind: &str, say: &mut dyn FnMut(&str)) -> Result<PathBuf> {
    let _lock = lock::acquire(Wait::Block, say)?;
    let install = Install::load()?;
    let root = Path::new(DIR);
    let dir = backups_dir(root)?;
    let postgres = !install.node && install.env.get("MIKAN_DATABASE_URL").is_some();
    ensure_room(root, postgres)?;
    let mut members: Vec<String> = TOP.iter().map(|s| (*s).to_owned()).collect();
    let panel = if install.node {
        None
    } else {
        let panel = Dir::open(root, "data/panel", false)?.context("data/panel is not there")?;
        // What an interrupted backup left would make the database copy fail.
        let copy = if postgres {
            dump_postgres(&panel)?;
            DUMP
        } else {
            panel.discard(panel_name(SQLITE_COPY))?;
            docker::check(docker::admin(&["backup", &format!("/{SQLITE_COPY}")], None)?)?;
            SQLITE_COPY
        };
        members.push(copy.into());
        if exists(root, "data/panel/tls") {
            members.push("data/panel/tls".into());
        }
        Some(panel)
    };
    if exists(root, "data/node") {
        members.push("data/node".into());
    }
    if exists(root, "addons/state.json") {
        members.push("addons/state.json".into());
    }
    if exists(root, "data/panel/addons") {
        members.push("data/panel/addons".into());
    }
    let packed = pack(root, &dir, kind, &members, Squeeze::Normal);
    if let Some(panel) = &panel {
        let _ = panel.discard(panel_name(SQLITE_COPY));
        let _ = panel.discard(panel_name(DUMP));
    }
    let file = packed?;
    prune(&dir, kind);
    Ok(file)
}

/// The archives in /opt/mikan/backups, the newest first.
pub fn backups() -> Vec<PathBuf> {
    let mut list: Vec<(std::time::SystemTime, PathBuf)> = fs::read_dir(Path::new(DIR).join("backups"))
        .map(|d| {
            d.flatten()
                .filter(|e| e.path().extension().is_some_and(|x| x == "gz"))
                .filter_map(|e| Some((e.metadata().ok()?.modified().ok()?, e.path())))
                .collect()
        })
        .unwrap_or_default();
    list.sort_by(|a, b| b.cmp(a));
    list.into_iter().map(|(_, p)| p).collect()
}

/// Whether an archive path is one a restore takes: relative, without "..", and one of the
/// settings files or inside the panel's or the node's data.
fn allowed_name(name: &str) -> bool {
    let n = name.strip_prefix("./").unwrap_or(name).trim_end_matches('/');
    if n.is_empty() || n.split('/').any(|c| c.is_empty() || c == ".." || c == ".") {
        return false;
    }
    TOP.contains(&n)
        || ["data", "data/panel", "data/node", "addons", "addons/state.json"].contains(&n)
        || n.starts_with("data/panel/")
        || n.starts_with("data/node/")
}

/// Judges the two listings of an archive (`tar -tzf`, names; `tar -tvzf`, with types).
/// Only plain files and directories pass: a link could lead the unpacking out of the
/// directory it is meant for, a device or a FIFO has no place in a backup.
fn check_listing(names: &str, verbose: &str) -> Result<()> {
    let names: Vec<&str> = names.lines().collect();
    if names.is_empty() {
        bail!("the archive is empty");
    }
    // A name with a line break would make the two listings disagree.
    if names.len() != verbose.lines().count() {
        bail!("the archive has file names with line breaks");
    }
    if let Some(l) = verbose.lines().find(|l| !matches!(l.chars().next(), Some('-' | 'd'))) {
        bail!("the archive has an entry that is not a plain file or directory: {}", l.trim());
    }
    if let Some(n) = names.iter().find(|n| !allowed_name(n)) {
        bail!("the archive has a path a mikan backup does not: {n}");
    }
    Ok(())
}

fn check_archive(file: &Path) -> Result<()> {
    let list = |flags: &str| -> Result<String> {
        let out = Command::new("tar").arg(flags).arg(file).stdin(Stdio::null()).output().context("run tar")?;
        if !out.status.success() {
            bail!("{} is not a readable tar.gz archive: {}", file.display(), String::from_utf8_lossy(&out.stderr).trim());
        }
        Ok(String::from_utf8_lossy(&out.stdout).into_owned())
    };
    check_listing(&list("-tzf")?, &list("-tvzf")?)
}

/// A directory of its own for unpacking, removed when done.
struct Stage(PathBuf);

impl Stage {
    fn new(root: &Path) -> Result<Self> {
        let nanos = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).map(|d| d.as_nanos()).unwrap_or(0);
        let path = root.join(format!(".restore-{}-{nanos}", std::process::id()));
        fs::DirBuilder::new().mode(0o700).create(&path).with_context(|| format!("create {}", path.display()))?;
        Ok(Self(path))
    }
}

impl Drop for Stage {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}

fn extract(file: &Path, into: &Path) -> Result<()> {
    let out = Command::new("tar")
        .args(["-xzf"])
        .arg(file)
        .arg("-C")
        .arg(into)
        .args(["--no-same-owner", "--no-same-permissions"])
        .stdin(Stdio::null())
        .output()
        .context("run tar")?;
    if !out.status.success() {
        bail!("tar could not unpack {}: {}", file.display(), String::from_utf8_lossy(&out.stderr).trim());
    }
    Ok(())
}

/// What was unpacked is plain files and directories, none with a setuid bit: checked
/// again on the disk, whatever the listing said.
fn verify_tree(dir: &Path, depth: usize) -> Result<()> {
    if depth > 32 {
        bail!("the archive is nested too deep");
    }
    for entry in fs::read_dir(dir)? {
        let entry = entry?;
        let meta = fs::symlink_metadata(entry.path())?;
        if meta.permissions().mode() & 0o7000 != 0 {
            bail!("{} has a setuid, setgid or sticky bit", entry.path().display());
        }
        if meta.is_dir() {
            verify_tree(&entry.path(), depth + 1)?;
        } else if !meta.is_file() {
            bail!("{} is not a plain file", entry.path().display());
        }
    }
    Ok(())
}

/// What an unpacked archive puts back: (where it is, where it goes below /opt/mikan).
fn plan(stage: &Path) -> Result<Vec<(PathBuf, &'static str)>> {
    let items = plan_items(stage);
    if !items.iter().any(|(_, r)| *r == ".env") {
        bail!("the archive has no .env: it is not a mikan backup");
    }
    Ok(items)
}

/// What an archive without .env puts back: the panel's PostgreSQL dump alone, as the panel
/// sends it to Telegram. The server's settings, certificates, adapters and node data stay
/// as they are.
fn plan_database(stage: &Path) -> Result<Vec<(PathBuf, &'static str)>> {
    let items: Vec<_> = plan_items(stage).into_iter().filter(|(_, r)| *r == RESTORE_DUMP).collect();
    if items.is_empty() {
        bail!("the archive has neither .env nor the panel's PostgreSQL dump: it is not a mikan backup");
    }
    Ok(items)
}

fn plan_items(stage: &Path) -> Vec<(PathBuf, &'static str)> {
    let mut items = Vec::new();
    for rel in [".env", "compose.yaml", "data/panel/tls", "data/node", "addons/state.json", "data/panel/addons"] {
        if fs::symlink_metadata(stage.join(rel)).is_ok() {
            items.push((stage.join(rel), rel));
        }
    }
    // The panel's database comes as the consistent copy a backup makes, or as the file a
    // snapshot took with the panel stopped.
    for db in [DUMP, SQLITE_COPY, SQLITE] {
        if fs::symlink_metadata(stage.join(db)).is_ok() {
            items.push((stage.join(db), if db == DUMP { RESTORE_DUMP } else { SQLITE }));
            if db == SQLITE && stage.join(SQLITE_WAL).is_file() {
                // Raw stopped snapshots can still contain committed transactions in
                // WAL (a forced shutdown). VACUUM backup.db has no accompanying WAL.
                items.push((stage.join(SQLITE_WAL), SQLITE_WAL));
            }
            break;
        }
    }
    items
}

/// Fits what a panel's archive puts back to this server: its database credentials and
/// image stay (POSTGRES_PASSWORD on an existing volume does not change the role's
/// password, and an image runs the database it was made for). A server that went back to
/// SQLite (an update that could not move it) keeps its compose file and takes only an
/// SQLite database: a PostgreSQL dump needs the move first.
fn fit_to_panel(items: &mut Vec<(PathBuf, &'static str)>, archived: &mut EnvFile, current: &EnvFile) -> Result<()> {
    for key in ["MIKAN_DATABASE_URL", "MIKAN_POSTGRES_PASSWORD", "MIKAN_IMAGE", "MIKAN_VERSION"] {
        match current.get(key) {
            Some(v) => archived.set(key, v)?,
            None => archived.remove(key),
        }
    }
    if current.get("MIKAN_DATABASE_URL").is_none() {
        items.retain(|(_, r)| *r != "compose.yaml");
        if items.iter().any(|(_, r)| *r == RESTORE_DUMP) {
            bail!("this server still runs SQLite and the backup holds a PostgreSQL database: run mikan update first, then restore");
        }
    }
    if !items.iter().any(|(_, r)| [SQLITE, RESTORE_DUMP].contains(r)) {
        bail!("the panel backup has no database");
    }
    // A backup of 0.4 holds SQLite alone: only a panel that still moves it can take it.
    let version = current.get("MIKAN_VERSION").unwrap_or_default();
    if current.get("MIKAN_DATABASE_URL").is_some()
        && !items.iter().any(|(_, r)| *r == RESTORE_DUMP)
        && !crate::release::moves_sqlite(version)
    {
        bail!(
            "this backup is of mikan 0.4 (SQLite), and mikan {version} no longer moves SQLite to PostgreSQL: restore it on a server of mikan {}",
            crate::release::LAST_SQLITE_MOVE
        );
    }
    Ok(())
}

/// Fits what a node's archive puts back to this server: the image stays the one this
/// server runs. The archive may be somebody else's file, and its MIKAN_IMAGE would run
/// whatever image it names, outside the signed releases.
fn fit_to_node(archived: &mut EnvFile, current: &EnvFile) -> Result<()> {
    for key in ["MIKAN_IMAGE", "MIKAN_VERSION"] {
        match current.get(key) {
            Some(v) => archived.set(key, v)?,
            None => archived.remove(key),
        }
    }
    Ok(())
}

/// Moves the items in, each current one aside first; when one step fails, everything that
/// moved goes back.
fn swap(root: &Path, items: &[(PathBuf, &str)]) -> Result<()> {
    let aside = root.join(format!(".restore-old-{}", std::process::id()));
    let _ = fs::remove_dir_all(&aside);
    fs::DirBuilder::new().mode(0o700).create(&aside)?;
    let mut parked: Vec<(PathBuf, PathBuf)> = Vec::new();
    let mut placed: Vec<PathBuf> = Vec::new();
    let result = (|| -> Result<()> {
        let mut away = |rel: &str| -> Result<()> {
            let to = root.join(rel);
            if fs::symlink_metadata(&to).is_ok() {
                let spot = aside.join(rel.replace('/', "__"));
                fs::rename(&to, &spot).with_context(|| format!("move {} aside", to.display()))?;
                parked.push((to, spot));
            }
            Ok(())
        };
        for (from, rel) in items {
            away(rel)?;
            if *rel == SQLITE {
                // The journal of the old database does not belong to the new one.
                away(SQLITE_WAL)?;
                away(SQLITE_SHM)?;
            }
            let to = root.join(rel);
            if let Some(parent) = to.parent() {
                fs::create_dir_all(parent)?;
            }
            fs::rename(from, &to).with_context(|| format!("put {} in place", to.display()))?;
            placed.push(to);
        }
        Ok(())
    })();
    if result.is_err() {
        for p in placed.iter().rev() {
            let _ = if fs::symlink_metadata(p).is_ok_and(|m| m.is_dir()) { fs::remove_dir_all(p) } else { fs::remove_file(p) };
        }
        for (orig, spot) in parked.iter().rev() {
            let _ = fs::rename(spot, orig);
        }
    }
    let _ = fs::remove_dir_all(&aside);
    result
}

/// The data as they are once the panel has stopped, in a kind archive: nothing it wrote
/// before the stop is missing. SQLite goes as its raw file with the WAL (a forced
/// shutdown leaves committed transactions there), PostgreSQL as a dump taken now; with
/// dump false only the files go (a database that cannot be dumped, `restore
/// --no-db-snapshot`).
pub fn archive_stopped(root: &Path, kind: &str, dump: bool) -> Result<PathBuf> {
    let dir = backups_dir(root)?;
    let postgres = EnvFile::load(root.join(".env"))?.get("MIKAN_DATABASE_URL").is_some();
    let pg = dump && postgres;
    ensure_room(root, pg)?;
    if pg {
        dump_postgres(&Dir::open(root, "data/panel", false)?.context("data/panel is missing")?)?;
    }
    let members: Vec<String> = [
        ".env",
        "compose.yaml",
        SQLITE,
        SQLITE_WAL,
        SQLITE_SHM,
        "data/panel/tls",
        "data/node",
        "addons/state.json",
        "data/panel/addons",
        DUMP,
    ]
    .iter()
    .filter(|m| exists(root, m))
    // A dump not taken now is a stale one, and so is the SQLite of a server on
    // PostgreSQL without its dump: a restore of this archive would take either for
    // the database.
    .filter(|m| match **m {
        DUMP => pg,
        SQLITE | SQLITE_WAL | SQLITE_SHM => pg || !postgres,
        _ => true,
    })
    .map(|m| (*m).to_owned())
    .collect();
    let packed = pack(root, &dir, kind, &members, Squeeze::Fast);
    if pg && let Some(panel) = Dir::open(root, "data/panel", false)? {
        panel.discard(panel_name(DUMP))?;
    }
    let file = packed?;
    prune(&dir, kind);
    Ok(file)
}

/// data/panel and data/node are directories, not links the panel put there: the swap
/// below renames paths inside them, and with the containers stopped this cannot change
/// between this look and the swap.
fn real_data_dirs(root: &Path) -> Result<()> {
    for part in ["data/panel", "data/node"] {
        Dir::open(root, part, false)?;
    }
    Ok(())
}

/// Replaces the data with a backup's. The archive is checked first and unpacked aside; the
/// current data go to a pre-restore archive before anything changes, and a restore that
/// fails half way puts them back. no_db_snapshot leaves the current PostgreSQL data out of
/// that archive: a database too broken to dump can still be replaced.
pub fn restore(file: &Path, no_db_snapshot: bool, say: &mut dyn FnMut(&str)) -> Result<()> {
    let _lock = lock::acquire(Wait::Block, say)?;
    let install = Install::load()?;
    let root = Path::new(DIR);
    let postgres = !install.node && install.env.get("MIKAN_DATABASE_URL").is_some();
    if !file.is_file() {
        bail!("no file {}", file.display());
    }
    if !install.node && Dir::open(root, "data/panel", false)?.is_none() {
        bail!("{} is missing", root.join("data/panel").display());
    }
    say("Checking the archive");
    check_archive(file)?;
    let stage = Stage::new(root)?;
    extract(file, &stage.0)?;
    verify_tree(&stage.0, 0)?;
    let items = if fs::symlink_metadata(stage.0.join(".env")).is_ok() {
        let mut items = plan(&stage.0)?;
        let mut archived = EnvFile::load(stage.0.join(".env"))?;
        if (archived.get("MIKAN_MODE") == Some("node")) != install.node {
            bail!(
                "the backup is a {}'s, this server is a {}",
                if install.node { "panel" } else { "node" },
                if install.node { "node" } else { "panel" }
            );
        }
        if install.node {
            fit_to_node(&mut archived, &install.env)?;
        } else {
            fit_to_panel(&mut items, &mut archived, &install.env)?;
        }
        archived.save()?;
        items
    } else {
        if !postgres {
            bail!(
                "the archive holds only a panel's PostgreSQL database; this server is a node or still runs SQLite (run mikan update first)"
            );
        }
        let items = plan_database(&stage.0)?;
        say("The archive holds only the panel's database: the settings, certificates, adapters and node data stay as they are");
        items
    };

    let _critical = signals::critical();
    say("Stopping mikan");
    crate::addon::down();
    docker::compose_run(&["stop"])?;
    let again = |say: &mut dyn FnMut(&str)| {
        if let Err(e) = docker::compose_run(&["up", "-d"]) {
            say(&format!("mikan did not start again: {e:#}"));
        }
        if !install.node
            && let Err(e) = crate::addon::resume(say)
        {
            say(&format!("Payment adapters did not start again: {e:#}"));
        }
    };
    // With the containers down nothing can swap a directory for a link any more: this is
    // the look that counts (the one before it only fails early).
    if let Err(e) = real_data_dirs(root) {
        again(say);
        return Err(e);
    }
    let dump = !(postgres && no_db_snapshot);
    if !dump {
        say("--no-db-snapshot: the current PostgreSQL data are NOT saved, only the files; if this restore fails, they are lost");
    }
    let snap = match archive_stopped(root, PRE_RESTORE, dump) {
        Ok(s) => s,
        Err(e) => {
            again(say);
            let hint = if dump && postgres {
                format!(
                    ". If PostgreSQL itself is broken and its current data are not needed, save only the files first: mikan restore --no-db-snapshot {}",
                    file.display()
                )
            } else {
                String::new()
            };
            return Err(e.context(format!("the current data could not be saved first, so nothing was replaced{hint}")));
        }
    };
    say(&format!("The current data are saved in {}", snap.display()));
    if let Err(e) = swap(root, &items) {
        again(say);
        return Err(e.context("the data were not replaced"));
    }
    let started = (|| -> Result<()> {
        if postgres {
            // The backup may carry the compose file of an older mikan: it gets the current one.
            docker::ensure_compose(root, false)?;
            panelfs::layout(root, true)?;
            docker::postgres_ready()?;
            let source = if items.iter().any(|(_, r)| *r == RESTORE_DUMP) { RESTORE_DUMP } else { SQLITE };
            docker::database(&["restore", &format!("/{source}")])?;
        } else if install.node {
            docker::ensure_compose(root, true)?;
            panelfs::layout(root, false)?;
        } else {
            // Back on SQLite with the compose file it went back to, which may mount all of
            // data/: it is not sealed, and the panel reads its database itself on start.
            panelfs::own_dirs(root, true)?;
        }
        docker::compose_run(&["up", "-d"])?;
        setup::wait_ready(Install::load()?.node_port(), Duration::from_secs(90))
    })();
    // A full copy of the database with its secrets: the archive keeps it, data/panel not.
    if items.iter().any(|(_, r)| *r == RESTORE_DUMP)
        && let Some(panel) = Dir::open(root, "data/panel", false).ok().flatten()
        && let Err(e) = panel.discard(panel_name(RESTORE_DUMP))
    {
        say(&format!("Remove {} by hand: {e:#}", root.join(RESTORE_DUMP).display()));
    }
    if let Err(e) = started {
        let _ = docker::compose_run(&["stop", "panel"]);
        return Err(e.context(format!(
            "the backup is in place but mikan does not start; the data before it are in {}: mikan restore {}",
            snap.display(),
            snap.display()
        )));
    }
    if !install.node {
        crate::addon::resume(say)?;
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::os::unix::fs::symlink;

    fn tmpdir(name: &str) -> PathBuf {
        let d = std::env::temp_dir().join(format!("backup-{name}-{}", std::process::id()));
        let _ = fs::remove_dir_all(&d);
        fs::create_dir_all(&d).unwrap();
        d
    }

    fn file(root: &Path, rel: &str, text: &str) {
        let p = root.join(rel);
        fs::create_dir_all(p.parent().unwrap()).unwrap();
        fs::write(p, text).unwrap();
    }

    // The secrets of the server are in the archive: nobody but root may see the file
    // while tar writes it, and the directory keeps the names private too.
    #[test]
    fn a_database_backup_puts_back_the_database_alone() {
        let d = tmpdir("plan-db");
        assert!(plan_database(&d).is_err(), "nothing in it");
        file(&d, "data/panel/backup.dump", "PGDMP");
        let items = plan_database(&d).unwrap();
        assert_eq!(items.iter().map(|(_, r)| *r).collect::<Vec<_>>(), ["data/panel/restore.dump"]);
        assert!(plan(&d).is_err(), "a database alone is not a full backup");
        // Whatever else came along is not taken without the full restore.
        file(&d, "data/node/state.json", "{}");
        file(&d, "data/panel/tls/panel.key", "k");
        assert_eq!(plan_database(&d).unwrap().len(), 1);
        // A SQLite copy alone is not what the panel sends.
        let e = tmpdir("plan-db-sqlite");
        file(&e, "data/panel/backup.db", "db");
        assert!(plan_database(&e).is_err());
        fs::remove_dir_all(&d).unwrap();
        fs::remove_dir_all(&e).unwrap();
    }

    #[test]
    fn a_backup_is_private_from_its_first_byte() {
        let root = tmpdir("private");
        file(&root, ".env", "MIKAN_IMAGE=x\n");
        file(&root, "compose.yaml", "name: mikan\n");
        file(&root, "data/panel/backup.db", "db");
        file(&root, "data/node/state.json", "{}");
        let dir = backups_dir(&root).unwrap();
        assert_eq!(fs::metadata(&dir).unwrap().permissions().mode() & 0o777, 0o700);
        // an old 0755 directory is made private too
        fs::set_permissions(&dir, fs::Permissions::from_mode(0o755)).unwrap();
        backups_dir(&root).unwrap();
        assert_eq!(fs::metadata(&dir).unwrap().permissions().mode() & 0o777, 0o700);
        let a = pack(
            &root,
            &dir,
            "mikan",
            &[".env".into(), "compose.yaml".into(), "data/panel/backup.db".into(), "data/node".into()],
            Squeeze::Normal,
        )
        .unwrap();
        let b = pack(&root, &dir, "mikan", &[".env".into()], Squeeze::Fast).unwrap();
        assert_ne!(a, b, "two backups of one second must not share a name");
        assert_eq!(fs::metadata(&a).unwrap().permissions().mode() & 0o777, 0o600);
        // the archive is what restore accepts, the fast one too
        check_archive(&a).unwrap();
        check_archive(&b).unwrap();
        // a failed tar leaves no file
        let before = fs::read_dir(&dir).unwrap().count();
        assert!(pack(&root, &dir, "mikan", &["no-such-file".into()], Squeeze::Normal).is_err());
        assert_eq!(fs::read_dir(&dir).unwrap().count(), before, "a broken archive stayed");
        fs::remove_dir_all(&root).unwrap();
    }

    // The node's talk with the updater is no part of a backup: the pre-update backup is made
    // while its status says "running", and a restore must not bring that back.
    #[test]
    fn a_backup_leaves_out_what_the_nodes_updater_and_panel_say() {
        let root = tmpdir("node-update");
        file(&root, ".env", "MIKAN_MODE=node\n");
        file(&root, "data/node/state.json", "{}");
        file(&root, "data/node/update/status.json", r#"{"state":"running"}"#);
        file(&root, "data/node/update/request", r#"{"version":"0.5.0.2"}"#);
        let dir = backups_dir(&root).unwrap();
        let a = pack(&root, &dir, "mikan", &[".env".into(), "data/node".into()], Squeeze::Normal).unwrap();
        let listing = Command::new("tar").arg("-tzf").arg(&a).output().unwrap();
        let names = String::from_utf8_lossy(&listing.stdout);
        assert!(names.contains("data/node/state.json"), "{names}");
        assert!(!names.contains("update"), "{names}");
        fs::remove_dir_all(&root).unwrap();
    }

    #[test]
    fn automatic_backups_are_rotated_and_the_admins_stay() {
        let dir = tmpdir("prune");
        for n in 1..=8 {
            fs::write(dir.join(format!("pre-update-2026100{n}-000000.tar.gz")), "x").unwrap();
        }
        fs::write(dir.join("mikan-20260101-000000.tar.gz"), "mine").unwrap();
        fs::write(dir.join("pre-restore-20260101-000000.tar.gz"), "snap").unwrap();
        // the original SQLite of the move to PostgreSQL, older than every retry after it
        fs::write(dir.join("pre-postgres-20260101-000000.tar.gz"), "sqlite").unwrap();
        prune(&dir, PRE_UPDATE);
        prune(&dir, "mikan");
        let mut left: Vec<String> = fs::read_dir(&dir).unwrap().map(|e| e.unwrap().file_name().into_string().unwrap()).collect();
        left.sort();
        assert_eq!(left.iter().filter(|n| n.starts_with("pre-update-")).count(), 5);
        assert!(left.contains(&"pre-update-20261008-000000.tar.gz".to_string()), "the newest stays");
        assert!(!left.contains(&"pre-update-20261001-000000.tar.gz".to_string()), "the oldest goes");
        for kept in ["mikan-20260101-000000.tar.gz", "pre-restore-20260101-000000.tar.gz", "pre-postgres-20260101-000000.tar.gz"] {
            assert!(left.contains(&kept.to_string()), "{kept}");
        }
        for n in 2..=5 {
            fs::write(dir.join(format!("pre-postgres-2026010{n}-000000.tar.gz")), "x").unwrap();
        }
        prune(&dir, PRE_POSTGRES);
        assert_eq!(
            fs::read_dir(&dir).unwrap().flatten().filter(|e| e.file_name().to_string_lossy().starts_with("pre-postgres-")).count(),
            3
        );
        assert!(dir.join("pre-postgres-20260105-000000.tar.gz").exists());
        fs::remove_dir_all(&dir).unwrap();
    }

    // A node's archive keeps this server's image: one it names would run outside the
    // signed releases.
    #[test]
    fn a_node_archive_keeps_the_image() {
        let mut archived = EnvFile::from_text("x", "MIKAN_MODE=node\nMIKAN_IMAGE=evil.example/x\nMIKAN_VERSION=9.9.9\nNODE_PORT=1\n");
        let current = EnvFile::from_text("x", "MIKAN_MODE=node\nMIKAN_IMAGE=ghcr.io/miroshka000/mikan@sha256:aa\nMIKAN_VERSION=0.5.0.5\n");
        fit_to_node(&mut archived, &current).unwrap();
        assert_eq!(archived.get("MIKAN_IMAGE"), current.get("MIKAN_IMAGE"));
        assert_eq!(archived.get("MIKAN_VERSION"), current.get("MIKAN_VERSION"));
        assert_eq!(archived.get("NODE_PORT"), Some("1"), "the settings are the archive's");
    }

    // A panel's archive keeps this server's database credentials and image; a server back
    // on SQLite keeps its compose file and refuses a PostgreSQL dump.
    #[test]
    fn an_archive_is_fitted_to_the_server_it_is_restored_on() {
        let d = tmpdir("fit");
        file(
            &d,
            ".env",
            "MIKAN_DATABASE_URL=postgresql://mikan:old@localhost/mikan?host=/run/postgresql\nMIKAN_POSTGRES_PASSWORD=old\nMIKAN_IMAGE=archived\nPANEL_PORT=1\n",
        );
        file(&d, "compose.yaml", "archived compose");
        file(&d, "data/panel/backup.dump", "pgdump");
        let current = EnvFile::from_text(
            "x",
            "MIKAN_IMAGE=live\nMIKAN_VERSION=0.5.0.1\nMIKAN_POSTGRES_PASSWORD=live\nMIKAN_DATABASE_URL=postgresql://mikan:live@localhost/mikan?host=/run/postgresql\n",
        );
        let mut items = plan(&d).unwrap();
        let mut archived = EnvFile::load(d.join(".env")).unwrap();
        fit_to_panel(&mut items, &mut archived, &current).unwrap();
        for key in ["MIKAN_IMAGE", "MIKAN_VERSION", "MIKAN_POSTGRES_PASSWORD", "MIKAN_DATABASE_URL"] {
            assert_eq!(archived.get(key), current.get(key), "{key}");
        }
        assert_eq!(archived.get("PANEL_PORT"), Some("1"), "the settings are the archive's");
        assert!(items.iter().any(|(_, r)| *r == "compose.yaml") && items.iter().any(|(_, r)| *r == RESTORE_DUMP));

        // back on SQLite, with the password of a volume an update initialized
        let sqlite = EnvFile::from_text("x", "MIKAN_IMAGE=old\nMIKAN_VERSION=0.4.4\nMIKAN_POSTGRES_PASSWORD=kept\n");
        let mut archived = EnvFile::load(d.join(".env")).unwrap();
        let err = fit_to_panel(&mut plan(&d).unwrap(), &mut archived, &sqlite).unwrap_err();
        assert!(format!("{err}").contains("mikan update first"), "{err}");
        fs::remove_file(d.join("data/panel/backup.dump")).unwrap();
        file(&d, "data/panel/backup.db", "sqlite copy");
        let mut items = plan(&d).unwrap();
        let mut archived = EnvFile::load(d.join(".env")).unwrap();
        fit_to_panel(&mut items, &mut archived, &sqlite).unwrap();
        assert_eq!((archived.get("MIKAN_DATABASE_URL"), archived.get("MIKAN_POSTGRES_PASSWORD")), (None, Some("kept")));
        assert_eq!(archived.get("MIKAN_IMAGE"), Some("old"));
        assert!(!items.iter().any(|(_, r)| *r == "compose.yaml"), "its own compose file stays");
        assert!(items.iter().any(|(_, r)| *r == SQLITE));
        // the same SQLite backup on a panel of 0.5.0.x moves; on 0.5.1.0 it is refused
        let mut archived = EnvFile::load(d.join(".env")).unwrap();
        fit_to_panel(&mut plan(&d).unwrap(), &mut archived, &current).unwrap();
        let newer = EnvFile::from_text(
            "x",
            "MIKAN_IMAGE=live\nMIKAN_VERSION=0.5.1.0\nMIKAN_POSTGRES_PASSWORD=live\nMIKAN_DATABASE_URL=postgresql://mikan:live@localhost/mikan?host=/run/postgresql\n",
        );
        let mut archived = EnvFile::load(d.join(".env")).unwrap();
        let err = fit_to_panel(&mut plan(&d).unwrap(), &mut archived, &newer).unwrap_err();
        assert!(format!("{err}").contains(crate::release::LAST_SQLITE_MOVE), "{err}");
        // no database at all
        fs::remove_file(d.join("data/panel/backup.db")).unwrap();
        assert!(fit_to_panel(&mut plan(&d).unwrap(), &mut EnvFile::load(d.join(".env")).unwrap(), &current).is_err());
        fs::remove_dir_all(&d).unwrap();
    }

    // Without the dump of a server on PostgreSQL, neither a stale dump nor the SQLite of
    // before the move goes in: a restore of the archive must not take them for the data.
    #[test]
    fn a_files_only_snapshot_holds_no_stale_database() {
        let root = tmpdir("no-db");
        server(&root);
        file(&root, ".env", "MIKAN_DATABASE_URL=postgresql://mikan:p@localhost/mikan?host=/run/postgresql\n");
        file(&root, DUMP, "interrupted dump");
        let snap = archive_stopped(&root, PRE_RESTORE, false).unwrap();
        let out = Command::new("tar").arg("-tzf").arg(&snap).output().unwrap();
        let names = String::from_utf8_lossy(&out.stdout);
        assert!(names.lines().any(|n| n == ".env") && names.lines().any(|n| n == "data/panel/tls/stale.key"), "{names}");
        assert!(!names.lines().any(|n| n.starts_with("data/panel/mikan.db") || n == DUMP), "{names}");
        fs::remove_dir_all(&root).unwrap();
    }

    #[test]
    fn only_mikan_backups_pass_the_listing() {
        let names = ".env\ncompose.yaml\ndata/\ndata/panel/\ndata/panel/backup.db\ndata/panel/tls/\ndata/panel/tls/panel.key\ndata/node/\ndata/node/state.json\n";
        let verbose: String = names
            .lines()
            .map(|n| format!("{} root/root 1 2026-10-01 00:00 {n}\n", if n.ends_with('/') { "drwx------" } else { "-rw-------" }))
            .collect();
        check_listing(names, &verbose).unwrap();
        let with = |n: &str, kind: &str| {
            let names = format!("{names}{n}\n");
            let verbose = format!("{verbose}{kind}rwxrwxrwx root/root 0 2026-10-01 00:00 {n}\n");
            check_listing(&names, &verbose)
        };
        assert!(with("data/panel/ok", "-").is_ok());
        for (n, why) in [
            ("/etc/passwd", "absolute"),
            ("../etc/passwd", "outside"),
            ("data/../../etc/cron.d/x", "a way out inside"),
            ("etc/passwd", "somewhere else"),
            ("data/unknown/state.json", "unknown data"),
            ("data/panel/../../x", "nested dots"),
            ("evil", "a stranger"),
        ] {
            assert!(with(n, "-").is_err(), "{why}: {n}");
        }
        for kind in ["l", "h", "c", "b", "p"] {
            assert!(with("data/panel/link", kind).is_err(), "type {kind} passed");
        }
        assert!(check_listing("", "").is_err(), "an empty archive");
        // a name with a line break makes the listings disagree: refused
        assert!(check_listing(".env\ndata/panel/a\n../../etc/x\n", "-rw 1 .env\n-rw 1 data/panel/a\n../../etc/x\n").is_err());
    }

    #[test]
    fn a_hostile_archive_is_refused_before_it_is_unpacked() {
        let d = tmpdir("hostile");
        let src = d.join("src");
        file(&src, ".env", "MIKAN_MODE=panel\n");
        fs::create_dir_all(src.join("data/panel")).unwrap();
        symlink("/etc", src.join("data/panel/tls")).unwrap();
        let tgz = d.join("evil.tar.gz");
        let ok = Command::new("tar").args(["-czf"]).arg(&tgz).arg("-C").arg(&src).args([".env", "data"]).status().unwrap().success();
        assert!(ok);
        let err = check_archive(&tgz).unwrap_err();
        assert!(format!("{err:#}").contains("not a plain file"), "{err:#}");
        // not an archive at all
        file(&d, "plain.tar.gz", "this is not gzip");
        assert!(check_archive(&d.join("plain.tar.gz")).is_err());
        fs::remove_dir_all(&d).unwrap();
    }

    #[test]
    fn an_unpacked_tree_is_plain_files_only() {
        let d = tmpdir("tree");
        file(&d, "a/b/c", "x");
        verify_tree(&d, 0).unwrap();
        symlink("/etc/passwd", d.join("a/link")).unwrap();
        assert!(verify_tree(&d, 0).is_err());
        fs::remove_file(d.join("a/link")).unwrap();
        fs::set_permissions(d.join("a/b/c"), fs::Permissions::from_mode(0o4755)).unwrap();
        assert!(verify_tree(&d, 0).is_err(), "setuid");
        fs::remove_dir_all(&d).unwrap();
    }

    #[test]
    fn what_a_backup_puts_back() {
        let d = tmpdir("plan");
        assert!(plan(&d).is_err(), "no .env, not a backup");
        file(&d, ".env", "MIKAN_MODE=node\n");
        file(&d, "data/node/state.json", "{}");
        let items = plan(&d).unwrap();
        assert_eq!(items.iter().map(|(_, r)| *r).collect::<Vec<_>>(), [".env", "data/node"]);
        file(&d, "data/panel/backup.db", "db");
        file(&d, "data/panel/tls/panel.key", "k");
        let items = plan(&d).unwrap();
        let to: Vec<&str> = items.iter().map(|(_, r)| *r).collect();
        assert!(to.contains(&"data/panel/mikan.db") && to.contains(&"data/panel/tls"), "{to:?}");
        fs::remove_dir_all(&d).unwrap();
    }

    #[test]
    fn postgres_archive_wins_over_preserved_sqlite_and_keeps_addons() {
        let d = tmpdir("postgres-plan");
        file(&d, ".env", "MIKAN_DATABASE_URL=postgresql://x\n");
        file(&d, "data/panel/backup.dump", "pgdump");
        file(&d, "data/panel/mikan.db", "old sqlite");
        file(&d, "addons/state.json", "{}");
        let items = plan(&d).unwrap();
        let to: Vec<&str> = items.iter().map(|(_, r)| *r).collect();
        assert!(to.contains(&"data/panel/restore.dump"));
        assert!(!to.contains(&"data/panel/mikan.db"));
        assert!(to.contains(&"addons/state.json"));
        fs::remove_dir_all(d).unwrap();
    }

    #[test]
    fn raw_sqlite_snapshots_restore_their_committed_wal() {
        let root = tmpdir("wal-swap");
        server(&root);
        let stage = root.join("stage");
        file(&stage, ".env", "RESTORED=1\n");
        file(&stage, "data/panel/mikan.db", "checkpointed pages");
        file(&stage, "data/panel/mikan.db-wal", "committed payment after checkpoint");
        let items = plan(&stage).unwrap();
        assert!(items.iter().any(|(_, to)| *to == "data/panel/mikan.db-wal"));
        swap(&root, &items).unwrap();
        assert_eq!(fs::read_to_string(root.join("data/panel/mikan.db-wal")).unwrap(), "committed payment after checkpoint");
        assert_eq!(fs::read_to_string(root.join("data/panel/mikan.db")).unwrap(), "checkpointed pages");
        fs::remove_dir_all(root).unwrap();
    }

    fn server(root: &Path) {
        file(root, ".env", "OLD=1\n");
        file(root, "compose.yaml", "old compose");
        file(root, "data/panel/mikan.db", "old db");
        file(root, "data/panel/mikan.db-wal", "old wal");
        file(root, "data/panel/tls/stale.key", "stale");
        file(root, "data/panel/update/status.json", "{}");
        file(root, "data/node/old", "old node");
    }

    #[test]
    fn the_swap_replaces_what_the_backup_has_and_keeps_the_rest() {
        let root = tmpdir("swap");
        server(&root);
        let stage = root.join("stage");
        file(&stage, ".env", "NEW=1\n");
        file(&stage, "data/panel/backup.db", "new db");
        file(&stage, "data/panel/tls/fresh.key", "fresh");
        file(&stage, "data/node/new", "new node");
        let items = plan(&stage).unwrap();
        swap(&root, &items).unwrap();
        assert_eq!(fs::read_to_string(root.join(".env")).unwrap(), "NEW=1\n");
        assert_eq!(fs::read_to_string(root.join("compose.yaml")).unwrap(), "old compose", "not in the backup: stays");
        assert_eq!(fs::read_to_string(root.join("data/panel/mikan.db")).unwrap(), "new db");
        assert!(!root.join("data/panel/mikan.db-wal").exists(), "the old database's journal is gone");
        assert!(root.join("data/panel/tls/fresh.key").exists() && !root.join("data/panel/tls/stale.key").exists(), "no stale certificate");
        assert!(root.join("data/node/new").exists() && !root.join("data/node/old").exists());
        assert!(root.join("data/panel/update/status.json").exists(), "what the backup does not hold stays");
        assert!(
            fs::read_dir(&root).unwrap().flatten().all(|e| !e.file_name().to_string_lossy().starts_with(".restore-old")),
            "the parked files are cleaned"
        );
        fs::remove_dir_all(&root).unwrap();
    }

    // The step that fails puts everything back: the server is what it was.
    #[test]
    fn a_swap_that_fails_half_way_puts_the_data_back() {
        let root = tmpdir("rollback");
        server(&root);
        let stage = root.join("stage");
        file(&stage, ".env", "NEW=1\n");
        file(&stage, "data/node/new", "new node");
        let mut items = plan(&stage).unwrap();
        items.push((stage.join("gone"), "data/panel/tls"));
        assert!(swap(&root, &items).is_err());
        assert_eq!(fs::read_to_string(root.join(".env")).unwrap(), "OLD=1\n");
        assert_eq!(fs::read_to_string(root.join("data/node/old")).unwrap(), "old node");
        assert!(!root.join("data/node/new").exists());
        assert_eq!(fs::read_to_string(root.join("data/panel/tls/stale.key")).unwrap(), "stale");
        assert_eq!(fs::read_to_string(root.join("data/panel/mikan.db")).unwrap(), "old db");
        fs::remove_dir_all(&root).unwrap();
    }

    #[test]
    fn a_snapshot_holds_the_current_data() {
        let root = tmpdir("snapshot");
        server(&root);
        let snap = archive_stopped(&root, PRE_RESTORE, true).unwrap();
        assert!(snap.starts_with(root.join("backups")));
        let out = Command::new("tar").arg("-tzf").arg(&snap).output().unwrap();
        let names = String::from_utf8_lossy(&out.stdout);
        for want in [".env", "compose.yaml", "data/panel/mikan.db", "data/panel/mikan.db-wal", "data/panel/tls/stale.key", "data/node/old"]
        {
            assert!(names.lines().any(|n| n == want), "{want} is not in {names}");
        }
        // a snapshot is a backup restore takes
        check_archive(&snap).unwrap();
        let stage = tmpdir("snapshot-stage");
        extract(&snap, &stage).unwrap();
        assert!(plan(&stage).unwrap().iter().any(|(_, r)| *r == "data/panel/mikan.db"));
        fs::remove_dir_all(&stage).unwrap();
        fs::remove_dir_all(&root).unwrap();
    }
}
