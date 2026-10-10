//! `mikan update`: to the latest release, or to an image the admin names, with a backup
//! first. A panel already on PostgreSQL goes back to its previous image when the new one
//! cannot migrate the database, or does not start on an unchanged schema; otherwise it
//! stays stopped with its data preserved. Nodes always go back. The daily timer, the
//! panel's Update button and the admin's shell all come through here, one at a time
//! (lock.rs).
//!
//! The panel and this command talk through data/panel/update, which the panel owns, so
//! everything read from there is small, plain-file data and everything written goes through
//! panelfs (no link followed, no directory trusted):
//!
//!   update/policy.json  the panel: {"auto": true} when automatic updates are on, and
//!                       "channel": "beta" when it takes pre-releases (only these exact
//!                       words count; anything else is stable)
//!   update/request      the panel: its Update button (a path unit starts `update --requested`)
//!   update/status.json  this: {"state": "running" | "ok" | "failed", "version", "from", "error", "at"}
//!
//! A node talks the same way through data/node/update, which its container owns, with
//! one difference: there is no policy (its .env says), and the request names the release,
//! {"version": "X"}, which the panel asked its node API for. The panel's own version is
//! what it asks, but the node's container is not trusted any more than the panel's: the
//! version is only a name, and the node goes to the signed release of that version in the
//! release index of its channel, never to an image or an address from the request. It
//! never goes back, and never past the version asked for (hop by hop, when that release
//! cannot be reached at once). The daily timer on a node does nothing unless its root-owned
//! .env says MIKAN_AUTO_UPDATE=1: nodes follow their panel.

use std::fs;
use std::path::Path;
use std::process::Command;
use std::time::Duration;

use anyhow::{Context, Result, bail};
use serde::Serialize;
use sha2::{Digest, Sha256};

use crate::envfile::EnvFile;
use crate::lock::{self, Wait};
use crate::ops::Install;
use crate::panelfs::{self, Dir};
use crate::release::Channel;
use crate::{DIR, addon, backup, clock, docker, host, net, release, setup, signals};

/// What `mikan update` was asked for.
#[derive(clap::Args, Clone, Debug, Default)]
pub struct UpdateArgs {
    /// An image (ghcr.io/…@sha256:…) or an image archive (.tar.gz) instead of the latest release
    pub target: Option<String>,
    /// Only when automatic updates are on (the daily timer runs this)
    #[arg(long)]
    pub auto: bool,
    /// The panel's Update button asked for it (a systemd path unit runs this)
    #[arg(long, hide = true)]
    pub requested: bool,
    /// Say what is out, change nothing
    #[arg(long)]
    pub check: bool,
}

/// The directory the panel's container, or a node's, owns for this (below /opt/mikan).
fn update_rel(node: bool) -> &'static str {
    if node { "data/node/update" } else { "data/panel/update" }
}

/// Where the panel and the host updater talk; the panel sees it as /data/panel/update. A
/// node's is /data/node/update.
fn update_dir(node: bool, create: bool) -> Result<Option<Dir>> {
    Dir::open(Path::new(DIR), update_rel(node), create)
}

/// How the update stands, as the panel shows it.
#[derive(Clone, Copy, Serialize, Debug, PartialEq, Eq)]
#[serde(rename_all = "lowercase")]
enum Phase {
    Running,
    Ok,
    Failed,
}

/// Tells the panel how the update went; a node's panel reads it through the node API.
fn report(install: &Install, phase: Phase, version: &str, from: &str, error: &str) {
    let body = serde_json::json!({"state": phase, "version": version, "from": from, "error": error, "at": clock::rfc3339()});
    let sent = update_dir(install.node, true).and_then(|d| {
        d.with_context(|| format!("{} is not there", update_rel(install.node)))?.write("status.json", body.to_string().as_bytes(), 0o644)
    });
    if let Err(e) = sent {
        eprintln!("mikan: cannot tell the panel how the update went: {e:#}");
    }
}

/// Whether the daily check may update: a node asks its .env, and only an explicit
/// MIKAN_AUTO_UPDATE=1 there lets it move ahead of its panel (it follows the panel
/// otherwise); a panel asks its policy file.
fn auto_on(install: &Install) -> bool {
    if install.node {
        return install.env.get("MIKAN_AUTO_UPDATE") == Some("1");
    }
    policy_on()
}

/// Whether the panel's settings turned automatic updates on, for the menu.
pub fn policy_on() -> bool {
    update_dir(false, false).ok().flatten().and_then(|d| d.read("policy.json", 4096).ok().flatten()).is_some_and(|b| policy_says_on(&b))
}

fn policy_says_on(data: &[u8]) -> bool {
    serde_json::from_slice::<serde_json::Value>(data).is_ok_and(|v| v["auto"] == true)
}

/// The channel of releases this server takes. A node has no panel on its host: its .env
/// says (MIKAN_UPDATE_CHANNEL), stable unless it says beta. A panel's comes from the
/// panel's policy file, which the panel writes and so is not trusted with more than the
/// two exact words: anything else there, or no file, is stable.
pub fn channel(install: &Install) -> Channel {
    if install.node {
        return install.env.get("MIKAN_UPDATE_CHANNEL").and_then(Channel::parse).unwrap_or(Channel::Stable);
    }
    update_dir(false, false)
        .ok()
        .flatten()
        .and_then(|d| d.read("policy.json", 4096).ok().flatten())
        .map_or(Channel::Stable, |b| policy_channel(&b))
}

fn policy_channel(data: &[u8]) -> Channel {
    serde_json::from_slice::<serde_json::Value>(data)
        .ok()
        .and_then(|v| v.get("channel").and_then(|c| c.as_str()).and_then(Channel::parse))
        .unwrap_or(Channel::Stable)
}

/// This server's channel; stable on a server mikan is not installed on yet.
pub fn channel_here() -> Channel {
    Install::load().map_or(Channel::Stable, |i| channel(&i))
}

/// How many releases one `mikan update` goes through at most, one after another.
const MAX_HOPS: u32 = 5;

/// Which hop of an update this command is (0 for the first): set by the update that
/// started it.
const HOP_ENV: &str = "MIKAN_UPDATE_HOP";

/// The release a hop chain asked for by the panel ends at: set by the update that started
/// it, so that no hop goes past it. A name, nothing else: the release is looked up in the
/// signed index again by each hop.
const UPTO_ENV: &str = "MIKAN_UPDATE_UPTO";

/// What this command was asked to stop at: the release the node's panel asked for, or the
/// one the hop that started this command was going to. Only a valid version counts.
fn upto_from_env() -> Option<String> {
    std::env::var(UPTO_ENV).ok().filter(|v| release::valid_version(v))
}

/// What comes after a hop to a release that is not the newest one.
#[derive(Debug, PartialEq, Eq)]
enum After {
    /// The newest release is here: nothing more.
    Done,
    /// The next hop, with its number.
    Next(u32),
    /// As many hops as one update makes: the rest waits for the next one.
    Stop,
}

/// What comes after hop number hop, when newest is still ahead (None: it is not).
fn after_hop(newest: Option<&str>, hop: u32) -> After {
    match newest {
        None => After::Done,
        Some(_) if hop + 1 >= MAX_HOPS => After::Stop,
        Some(_) => After::Next(hop + 1),
    }
}

fn hop_number() -> u32 {
    std::env::var(HOP_ENV).ok().and_then(|v| v.parse().ok()).unwrap_or(0)
}

/// Goes on to the next release after a hop. The command on the disk does it: the newer one
/// when this hop replaced it, so a chain survives each handover to a newer installer. It
/// chooses again from the version now running, under this command's lock, and tells the
/// panel how it went itself.
fn next_hop(newest: Option<&str>, daily: bool, at: &mut Attempt, say: &mut dyn FnMut(&str)) -> Result<()> {
    let hop = match after_hop(newest, hop_number()) {
        After::Done => return Ok(()),
        After::Stop => {
            let more = format!("mikan {} is out still: run mikan update again to go on.", newest.unwrap_or_default());
            say(&more);
            // The panel waits for the release it asked for: this is not the end it expects.
            if at.upto.is_some() {
                at.skipped = Some(more);
            }
            return Ok(());
        }
        After::Next(n) => n,
    };
    say(&format!("mikan {} runs; going on towards mikan {}.", at.version, newest.unwrap_or_default()));
    let mut child = Command::new(host::BIN);
    child.arg("update").env(lock::HELD_ENV, "1").env(HOP_ENV, hop.to_string());
    if let Some(upto) = &at.upto {
        child.env(UPTO_ENV, upto);
    }
    if daily {
        child.arg("--auto");
    }
    at.handed_off = true;
    if !child.status()?.success() {
        bail!("mikan {} runs, the update to the next release did not finish; retry mikan update", at.version);
    }
    Ok(())
}

/// What the panel, or a node's container, put at update/request.
#[derive(Debug, PartialEq, Eq)]
enum Asked {
    Nothing,
    /// The panel's Update button: the newest release.
    Update,
    /// A node's panel asked for this release, and no further.
    Version(String),
    /// The adapters ({"do": "addons"}), not an update.
    Addons,
    /// Not a request: a link, a directory, a FIFO, a file of a size nobody writes, or
    /// (for a node) one that does not name a release version.
    Garbage(String),
}

/// The largest request: what the panel writes is a dozen bytes, a node's a few dozen.
const REQUEST_LIMIT: u64 = 4096;

/// Takes the request: whatever it is, it goes first, so the unit that watches for it does
/// not fire again and again. node says whose request it is: a node's must name a release,
/// {"version": "X"}, the only thing taken from it, and is nothing else; the panel's is any
/// small file (what it says does not matter) except the adapters' {"do": "addons"}.
fn take_request(dir: &Dir, node: bool) -> Result<Asked> {
    if !dir.exists("request") {
        return Ok(Asked::Nothing);
    }
    let read = dir.read("request", REQUEST_LIMIT);
    dir.discard("request")?;
    Ok(match read {
        Ok(Some(data)) if node => match requested_version(&data) {
            Ok(v) => Asked::Version(v),
            Err(why) => Asked::Garbage(why),
        },
        Ok(Some(data)) => match serde_json::from_slice::<serde_json::Value>(&data) {
            Ok(v) if v["do"] == "addons" => Asked::Addons,
            _ => Asked::Update,
        },
        Ok(None) => Asked::Nothing,
        Err(e) => Asked::Garbage(format!("{e:#}")),
    })
}

/// The release version a node's request names: {"version": "X"} with X a version of a
/// release, nothing else of it is read.
fn requested_version(data: &[u8]) -> std::result::Result<String, String> {
    let v: serde_json::Value = serde_json::from_slice(data).map_err(|e| format!("not JSON ({e})"))?;
    let version = v.as_object().and_then(|o| o.get("version")).and_then(|x| x.as_str()).ok_or("no version in it")?;
    if !release::valid_version(version) {
        return Err("the version is not a release version".into());
    }
    Ok(version.to_owned())
}

/// update/ itself may be what the panel (or a node) replaced (a link, a file): the request
/// inside it would stay for ever and the path unit would start this command again and
/// again. The stand-in goes; the panel makes its directory again.
fn clear_update_dir(node: bool, say: &mut dyn FnMut(&str)) {
    let rel = update_rel(node);
    let Ok(Some(owner)) = Dir::open(Path::new(DIR), rel.trim_end_matches("/update"), false) else { return };
    if owner.exists("update") {
        match owner.discard("update") {
            Ok(()) => say(&format!("{rel} was not a directory: removed it")),
            Err(e) => say(&format!("cannot remove {rel}: {e:#}")),
        }
    }
}

/// Whether the status the panel reads is an ending (ok, failed): the upgraded command an
/// update handed over to has told it how it went, in its own words.
fn status_ended(node: bool) -> bool {
    update_dir(node, false)
        .ok()
        .flatten()
        .and_then(|d| d.read("status.json", 64 << 10).ok().flatten())
        .and_then(|b| serde_json::from_slice::<serde_json::Value>(&b).ok())
        .is_some_and(|v| v["state"] == "ok" || v["state"] == "failed")
}

/// What an update has told the panel so far.
#[derive(Default)]
struct Attempt {
    version: String,
    from: String,
    started: bool,
    /// The upgraded command finished the update and reported it.
    handed_off: bool,
    /// Why the daily check left the update for later, for the panel to show.
    skipped: Option<String>,
    /// The release a node's panel asked for, which the update goes to and no further.
    upto: Option<String>,
}

/// Updates to the latest release (or target), upgrading the installer first when needed.
/// A panel goes back only from PostgreSQL to PostgreSQL, when the migration fails or the
/// schema did not move; a node always can.
pub fn update(a: &UpdateArgs, say: &mut dyn FnMut(&str), progress: &mut dyn FnMut(f64)) -> Result<()> {
    // `--check` changes nothing, so it waits for no one.
    let _lock = if a.check {
        None
    } else {
        // The daily check comes again tomorrow; the button and the admin wait their turn.
        let wait = if a.auto && !a.requested { Wait::Skip } else { Wait::Block };
        match lock::acquire(wait, say)? {
            Some(g) => Some(g),
            None => {
                say("Another mikan operation is running: this automatic check skips its turn.");
                return Ok(());
            }
        }
    };
    let install = Install::load()?;
    let mut at = Attempt { upto: upto_from_env(), ..Attempt::default() };
    if a.requested {
        let dir = match update_dir(install.node, false) {
            Ok(d) => d,
            Err(e) => {
                say(&format!("{e:#}"));
                clear_update_dir(install.node, say);
                None
            }
        };
        let Some(dir) = dir else { return Ok(()) };
        match take_request(&dir, install.node)? {
            Asked::Nothing => return Ok(()),
            // The panel wakes this unit for its payment adapters too.
            Asked::Addons if !install.node => return panel_addons(say),
            Asked::Addons => {
                say("update/request is not a request, removed: adapters are the panel's");
                return Ok(());
            }
            Asked::Garbage(why) => {
                say(&format!("update/request is not a request, removed: {why}"));
                // A node's panel waits for an answer; the panel's own page needs none.
                if install.node {
                    report(&install, Phase::Failed, "", &install.version(), &format!("the update request was not valid: {why}"));
                }
                return Ok(());
            }
            Asked::Update => {}
            Asked::Version(v) => at.upto = Some(v),
        }
    }
    let r = update_to(a, say, progress, &mut at);
    // An adapter request that came while an update was asked for waits for it.
    if a.requested
        && addon::pending()
        && let Err(e) = addon::apply(&install.version(), say)
    {
        say(&format!("Payment adapters: {e:#}"));
    }
    // The admin pressed Update and waits for an answer, and an update that began must end
    // in one: ok, failed, never "running" for good. What the upgraded command said about
    // its own work stays; it is told here only when it ended before telling.
    if (a.requested || at.started) && !(at.handed_off && at.skipped.is_none() && status_ended(install.node)) {
        let now = Install::load().unwrap_or(install);
        match (&r, &at.skipped) {
            (Ok(()), None) => report(&now, Phase::Ok, &at.version, &at.from, ""),
            (Ok(()), Some(why)) => report(&now, Phase::Failed, &at.version, &at.from, why),
            (Err(e), _) => report(&now, Phase::Failed, &at.version, &at.from, &format!("{e:#}")),
        }
    }
    r
}

fn panel_addons(say: &mut dyn FnMut(&str)) -> Result<()> {
    let install = Install::load()?;
    install.panel_only()?;
    addon::apply(&install.version(), say)
}

fn update_to(a: &UpdateArgs, say: &mut dyn FnMut(&str), progress: &mut dyn FnMut(f64), at: &mut Attempt) -> Result<()> {
    let mut install = Install::load()?;
    let daily = a.auto && !a.requested;
    if daily && !auto_on(&install) {
        return Ok(());
    }
    let current = install.version();
    at.from.clone_from(&current);
    at.version.clone_from(&current);
    // What a node's panel asked for (or an earlier hop of that) is only ever a step up.
    let upto = a.target.is_none().then(|| at.upto.clone()).flatten();
    if let Some(upto) = &upto {
        match upto_step(&current, upto) {
            Step::Refuse(why) => bail!(why),
            Step::Already => {
                say(&format!("mikan {current} is the release asked for."));
                return Ok(());
            }
            Step::Go => {}
        }
    }
    let mut manifest = None;
    // The newest release, when this update is a hop on the way to it.
    let mut ahead = None;
    let (image, version) = match a.target.as_deref() {
        Some(t) if Path::new(t).is_file() => {
            say(&format!("Loading {t}"));
            let image = docker::load(t)?;
            let v = setup::image_version(&image)?;
            (image, v)
        }
        Some(t) => {
            say(&format!("Pulling {t}"));
            docker::pull(t, &mut *progress)?;
            (t.to_owned(), setup::image_version(t)?)
        }
        None => {
            let found = match &upto {
                Some(upto) => release::find_upto(&current, upto_channel(channel(&install), upto), upto)?,
                None => release::find(Some(&current), channel(&install))?,
            };
            if let Some(why) = &found.fallback {
                say(&format!("The release index is unavailable ({why}): the latest release on GitHub answers instead."));
            }
            let m = found.manifest;
            if found.unreachable {
                let newest = found.newest.unwrap_or_default();
                if a.check {
                    say(&format!(
                        "mikan {newest} is out, but mikan {current} cannot update to it directly and no release in between is listed."
                    ));
                    return Ok(());
                }
                bail!("mikan {newest} is out, but mikan {current} cannot update to it directly and no release in between is listed");
            }
            if already_current(&m.version, &current, a.check || install.node || docker::panel_healthy()) {
                say(&format!("mikan {current} is the latest release."));
                if !a.check && self_update(&m, say) {
                    follow_new_command(say);
                }
                return Ok(());
            }
            if a.check {
                match &found.newest {
                    Some(newest) => {
                        say(&format!("mikan {newest} is out, this server runs {current}: it updates through mikan {} first.", m.version))
                    }
                    None => say(&format!("mikan {} is out, this server runs {current}.", m.version)),
                }
                if let Some(notes) = m.notes.get("en") {
                    say(notes);
                }
                return Ok(());
            }
            ahead = found.newest;
            say(&format!("Updating mikan {current} → {}", m.version));
            at.version.clone_from(&m.version);
            at.started = true;
            report(&install, Phase::Running, &m.version, &current, "");
            let reference = m.reference();
            docker::pull(&reference, &mut *progress)?;
            // The release names its version: an image that says another is not the one
            // that was signed for.
            let v = setup::image_version(&reference)?;
            if v.trim_start_matches('v') != m.version {
                bail!("the image is mikan {v}, the signed release says {}", m.version);
            }
            let version = m.version.clone();
            manifest = Some(m);
            (reference, version)
        }
    };
    if a.check {
        say(&format!("{image} is mikan {version}; this server runs {current}."));
        return Ok(());
    }
    at.version.clone_from(&version);
    if !at.started {
        at.started = true;
        report(&install, Phase::Running, &version, &current, "");
    }

    if let Some(m) = &manifest {
        let upgrade = self_update(m, say);
        if upgrade {
            // The new installer must prepare infrastructure before the image starts.
            // The signed image was already verified above; hand off under our lock.
            let mut child = Command::new(host::BIN);
            child.args(["update", &image]).env(lock::HELD_ENV, "1");
            if daily {
                // so that it, too, leaves a move it has no room for to a later night
                child.arg("--auto");
            }
            at.handed_off = true;
            let status = child.status()?;
            if !status.success() {
                bail!("the upgraded installer could not finish the update; retry mikan update");
            }
            return next_hop(ahead.as_deref(), daily, at, say);
        }
        if !m.min_installer.is_empty() && release::newer(&m.min_installer, crate::version()) {
            bail!(
                "this release requires installer {}; its verified upgrade failed. Run the signed one-line installer again before updating",
                m.min_installer
            );
        }
    }

    let root = Path::new(DIR);
    let was_postgres = install.env.get("MIKAN_DATABASE_URL").is_some();
    // The move from SQLite cannot be undone once it imported: it starts only with room for
    // it. The daily check leaves it for a later night, the admin is told why.
    let moving = !install.node && !was_postgres;
    if moving && let Err(why) = room_to_move(root) {
        if daily {
            let why = format!("the move to PostgreSQL waits for room: {why}");
            say(&format!("No update tonight: {why}"));
            at.skipped = Some(why);
            return Ok(());
        }
        bail!("the move to PostgreSQL needs more room, nothing was changed: {why}");
    }
    if !install.node {
        // Pulled now, not by `compose up` with the panel stopped and no limit on silence.
        say("PostgreSQL 18 image");
        docker::pull_postgres(&mut *progress).context("the PostgreSQL image could not be pulled; nothing was changed")?;
    }

    // A backup that cannot be made stops the update before changing its image or data.
    let kind = if moving { backup::PRE_POSTGRES } else { backup::PRE_UPDATE };
    let gate = backup::backup_as(kind, say).context("the backup before the update failed; nothing was changed")?;

    // From here until the new version is up or the old one is back, nothing may cut it short.
    let _critical = signals::critical();
    let old_compose = fs::read_to_string(root.join("compose.yaml")).context("read the current compose file")?;
    let old_env = install.env.render();
    let saved = if install.node {
        gate
    } else {
        // The archive to go back to holds every payment and bot update accepted until the
        // old process shut down (SQLite with its WAL, PostgreSQL dumped now); the one above
        // proved a backup can be made while nothing had changed yet, and goes.
        docker::compose_run(&["stop", "panel"])?;
        match backup::archive_stopped(root, kind, true) {
            Ok(last) => {
                let _ = fs::remove_file(&gate);
                last
            }
            Err(e) => {
                docker::compose_run(&["up", "-d", "panel"])?;
                return Err(e.context("the data of the stopped panel could not be saved; the update stopped and the panel runs again"));
            }
        }
    };
    say(&format!("Backup: {}", saved.display()));
    let before = Before { env: &old_env, compose: &old_compose, saved: &saved, current: &current };

    // Until the database migrates, the configuration from before can come back as it was.
    let prepared = (|| -> Result<()> {
        if !install.node {
            setup::postgres_env(&mut install.env)?;
        }
        install.env.set("MIKAN_IMAGE", &image)?;
        install.env.set("MIKAN_VERSION", &version)?;
        install.env.save()?;
        docker::ensure_compose(root, install.node)?;
        if !install.node {
            docker::postgres_ready()?;
        }
        Ok(())
    })();
    if let Err(e) = prepared {
        if install.node {
            return Err(e);
        }
        return Err(match restore_previous(root, &install.env, &old_env, &old_compose, was_postgres) {
            Ok(()) => e.context(format!("the update could not prepare the database; mikan {current} runs again")),
            Err(back) => e.context(format!(
                "the update could not prepare the database, and mikan {current} did not start again ({back:#}); backup: {}",
                saved.display()
            )),
        });
    }
    let mut schema = None;
    if !install.node {
        match docker::database(&["migrate"]) {
            Ok(out) => schema = schema_change(&String::from_utf8_lossy(&out.stdout)),
            // On PostgreSQL already the previous version is tried on what the migration
            // left (each of its steps commits whole or not at all); one that refuses a
            // schema moved forward leaves the panel stopped.
            Err(e) if was_postgres => {
                say(&format!("mikan {version} could not migrate the database: going back to {current}"));
                return Err(roll_back(&mut install, &before, &format!("mikan {version} could not migrate the database: {e:#}")));
            }
            // A failed import may have committed before losing its response. Never fall
            // back to stale SQLite or restore a snapshot implicitly after this.
            Err(e) => {
                return Err(e.context(format!(
                    "database migration did not finish; panel stays stopped, original SQLite and PostgreSQL preserved. Run mikan update again; backup: {}",
                    saved.display()
                )));
            }
        }
    }
    if let Err(e) = start(&install, root) {
        // The previous image runs on what this update left only when that was PostgreSQL
        // already and the schema did not move: going back from SQLite would lose every
        // write since the import, and an older binary refuses a newer schema.
        if !install.node && !(was_postgres && schema.is_some_and(|(from, to)| from == to)) {
            let _ = docker::compose_run(&["stop", "panel"]);
            return Err(e.context(format!("PostgreSQL is preserved and the panel is stopped. No automatic database rollback is safe; retry mikan update after fixing startup. Backup: {}", saved.display())));
        }
        say(&format!("mikan {version} did not start: going back to {current}"));
        return Err(roll_back(&mut install, &before, &format!("mikan {version} did not start: {e:#}")));
    }
    seal_when_current(root, install.node, say);
    say(&format!("mikan {version} is running."));
    if let Some(m) = manifest
        && self_update(&m, say)
    {
        follow_new_command(say);
    } else if let Err(e) = host::install_units(!install.node) {
        say(&format!("No automatic updates: {e:#}"));
    }
    next_hop(ahead.as_deref(), daily, at, say)
}

/// What a request for the release upto means for a server running current.
#[derive(Debug, PartialEq, Eq)]
enum Step {
    /// An older release: never.
    Refuse(String),
    /// It runs already.
    Already,
    Go,
}

fn upto_step(current: &str, upto: &str) -> Step {
    if release::newer(current, upto) {
        Step::Refuse(format!("mikan {upto} is older than the mikan {current} this server runs: a server does not go back"))
    } else if release::newer(upto, current) {
        Step::Go
    } else {
        Step::Already
    }
}

/// The channel a request for upto is looked up in: the server's, and beta for a
/// pre-release, which the panel runs and the admin chose.
fn upto_channel(channel: Channel, upto: &str) -> Channel {
    if release::is_prerelease(upto) { Channel::Beta } else { channel }
}

/// Retry an unhealthy release with the same version, but never turn a stale signed
/// manifest into an automatic downgrade just because the current panel is unhealthy.
fn already_current(latest: &str, current: &str, healthy_or_check: bool) -> bool {
    release::newer(current, latest) || (!release::newer(latest, current) && healthy_or_check)
}

/// Starts the containers on the current .env and waits for them.
fn start(install: &Install, root: &Path) -> Result<()> {
    panelfs::own_dirs(root, !install.node)?;
    docker::compose_run(&["up", "-d"])?;
    setup::wait_ready(install.node_port(), Duration::from_secs(90))
}

/// The schema versions `mikan database migrate` reports: before and after it ran.
fn schema_change(out: &str) -> Option<(u64, u64)> {
    let line = out.lines().find_map(|l| l.trim().strip_prefix("PostgreSQL schema version: "))?;
    let (from, to) = line.split_once(" -> ")?;
    Some((from.trim().parse().ok()?, to.trim().parse().ok()?))
}

/// The .env from before the update, to be written once. A PostgreSQL password made
/// meanwhile stays: its volume may already be initialized with it, and another one on the
/// next attempt would lock the panel out of its own database.
fn previous_env(root: &Path, now: &EnvFile, old_env: &str) -> Result<EnvFile> {
    let mut env = EnvFile::from_text(root.join(".env"), old_env);
    if let Some(password) = now.get("MIKAN_POSTGRES_PASSWORD") {
        env.set("MIKAN_POSTGRES_PASSWORD", password)?;
    }
    Ok(env)
}

/// Puts the configuration from before the update back, starts it and waits for it.
fn restore_previous(root: &Path, now: &EnvFile, old_env: &str, old_compose: &str, was_postgres: bool) -> Result<()> {
    previous_env(root, now, old_env)?.save()?;
    if !was_postgres {
        // The compose file from before has no database service to stop it by later.
        let _ = docker::compose_run(&["stop", "postgres"]);
    }
    docker::put_compose(root, old_compose)?;
    docker::compose_run(&["up", "-d"])?;
    setup::wait_ready(None, Duration::from_secs(90))
}

/// The configuration from before comes back: a node's always, a panel's when its database
/// is one the previous version may run on. A panel that does not start on it stays
/// stopped. why says what failed.
fn roll_back(install: &mut Install, before: &Before, why: &str) -> anyhow::Error {
    let root = Path::new(DIR);
    let (current, saved) = (before.current, before.saved.display());
    let back = (|| -> Result<()> {
        install.env = previous_env(root, &install.env, before.env)?;
        install.env.save()?;
        docker::put_compose(root, before.compose)?;
        start(install, root)
    })();
    match back {
        Ok(()) => anyhow::anyhow!("{why}; mikan {current} runs again (backup: {saved})"),
        Err(e) => {
            let stopped = if install.node {
                ""
            } else {
                let _ = docker::compose_run(&["stop", "panel"]);
                ", so the panel is stopped (an older mikan refuses a schema a newer one moved forward)"
            };
            anyhow::anyhow!(
                "{why}; and mikan {current} does not start either ({e:#}){stopped}. The data from before the update are in {saved}: mikan restore {saved}"
            )
        }
    }
}

/// What an update goes back to.
struct Before<'a> {
    /// .env and compose.yaml as they were.
    env: &'a str,
    compose: &'a str,
    /// The archive of the data from before.
    saved: &'a Path,
    current: &'a str,
}

/// What the move from SQLite to PostgreSQL needs before anything stops: disk for the
/// archives, the dump and the new database (three times the panel's data, at least
/// 1 GiB) where mikan and Docker keep them, and memory for PostgreSQL beside the panel.
fn room_to_move(root: &Path) -> std::result::Result<(), String> {
    let data = crate::system::tree_bytes(&root.join("data/panel"));
    let docker_dir = Path::new("/var/lib/docker");
    let free = [root, docker_dir].into_iter().filter(|p| p.exists()).filter_map(crate::system::disk_free_bytes).min();
    move_fits(free, crate::system::mem_available_mb(), data)
}

fn move_fits(free: Option<u64>, mem_mb: Option<u64>, data: u64) -> std::result::Result<(), String> {
    let need = data.saturating_mul(3).max(1 << 30);
    if let Some(free) = free
        && free < need
    {
        return Err(format!("{} MB of free disk, {} MB needed (three times data/panel, at least 1 GB)", free >> 20, need >> 20));
    }
    if let Some(mb) = mem_mb
        && mb < 512
    {
        return Err(format!("{mb} MB of memory available, 512 MB needed for PostgreSQL beside the panel"));
    }
    Ok(())
}

/// data/ becomes root's once no container can mount all of it: with the current
/// compose.yaml. An older one (the migration failed) would be locked out of its data.
fn seal_when_current(root: &Path, node: bool, say: &mut dyn FnMut(&str)) {
    if fs::read_to_string(root.join("compose.yaml")).is_ok_and(|c| c == docker::compose_text(node))
        && let Err(e) = panelfs::seal(root)
    {
        say(&format!("data/ stays as it was: {e:#}"));
    }
}

/// Brings the server's own files to what this installer writes: the units, and
/// compose.yaml, whose change recreates the containers (they mount their own data only
/// and have limits since 0.4.4). A server stopped by its admin stays stopped.
pub fn converge(say: &mut dyn FnMut(&str)) -> Result<()> {
    let _lock = lock::acquire(Wait::Block, say)?;
    let install = Install::load()?;
    let root = Path::new(DIR);
    // The pool of ports the panel moves a blocked inbound to may have grown since the
    // install opened it, and a node installed before public node certificates lacks port 80:
    // ufw gets them again (a rule it has is left as it is).
    if install.ufw()
        && crate::system::ufw_active()
        && let Err(e) = host::update_rules().iter().try_for_each(|r| host::allow(r))
    {
        say(&format!("ufw did not take port 80 and the ports of the pool ({e:#}): open them yourself"));
    }
    if !install.node && install.env.get("MIKAN_DATABASE_URL").is_none() {
        // post-update must never activate the PostgreSQL compose over a live SQLite
        // installation. `update` is the only operation that performs the cutover.
        say("This SQLite installation needs mikan update before its compose file can change.");
        return Ok(());
    }
    // Where systemd is: a server without it has no units to bring up to date.
    if Path::new("/run/systemd/system").exists() && !host::units_current(!install.node) {
        match host::install_units(!install.node) {
            Ok(()) => say("The update units are the current ones."),
            Err(e) => say(&format!("No automatic updates: {e:#}")),
        }
    }
    let running = docker::services().is_ok_and(|s| s.iter().any(|s| s.state == "running"));
    let Some(old) = docker::ensure_compose(root, install.node)? else {
        if running {
            // compose.yaml is already the current one: data/ may be sealed.
            let _ = panelfs::seal(root);
        }
        return Ok(());
    };
    say("compose.yaml is the current one: each container mounts only its own data (the previous file is compose.yaml.old)");
    if !running {
        return Ok(());
    }
    let _critical = signals::critical();
    let started = start(&install, root);
    if let Err(e) = started {
        say(&format!("The containers do not start with the new compose.yaml: {e:#}; putting the previous one back"));
        docker::put_compose(root, &old)?;
        start(&install, root).context("the previous compose.yaml does not start either")?;
        bail!("the new compose.yaml did not start: {e:#}");
    }
    panelfs::seal(root)
}

/// After the command was replaced by a newer one, the newer one finishes the work with its
/// own idea of the units and compose.yaml: it runs under this command's lock.
fn follow_new_command(say: &mut dyn FnMut(&str)) {
    let done = Command::new(host::BIN).arg("post-update").env(lock::HELD_ENV, "1").status();
    if !done.is_ok_and(|s| s.success()) {
        say("The new mikan command could not finish setting up: run `mikan update` once more.");
    }
}

/// Replaces this command with the release's installer when it differs; true when it did.
fn self_update(m: &release::Manifest, say: &mut dyn FnMut(&str)) -> bool {
    let Some(asset) = m.installer() else { return false };
    // Never back to an older command: a release older than this one (a rollback of the
    // release, an old signed manifest served again, a build from a branch) is not an update.
    if release::newer(crate::version(), &m.version) {
        return false;
    }
    let sha = |b: &[u8]| Sha256::digest(b).iter().map(|x| format!("{x:02x}")).collect::<String>();
    if fs::read(host::BIN).map(|b| sha(&b)).ok().as_deref() == Some(asset.sha256.as_str()) {
        return false;
    }
    let result = net::get(&asset.url, 64 << 20).and_then(|data| {
        if sha(&data) != asset.sha256 {
            bail!("the downloaded installer does not match the release manifest");
        }
        host::replace_bin(&data, Some(&m.version))
    });
    match result {
        Ok(()) => {
            say(&format!("The mikan command is {} now too.", m.version));
            true
        }
        Err(e) => {
            say(&format!("The mikan command stays as it is: {e:#}"));
            false
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::os::unix::fs::symlink;

    #[test]
    fn schema_change_is_read_from_the_migrate_report() {
        let out = "PostgreSQL schema version: 1 -> 1\nSQLite import verified: 31 tables; original SQLite preserved\n";
        assert_eq!(schema_change(out), Some((1, 1)));
        assert_eq!(schema_change("PostgreSQL schema version: 1 -> 2\n"), Some((1, 2)));
        assert_eq!(schema_change("PostgreSQL schema is ready; no legacy SQLite database\n"), None);
        assert_eq!(schema_change("PostgreSQL schema version: one -> 2\n"), None);
    }

    // The .env goes back in one write, with the password a database volume may already
    // have been initialized with, and without the address of a database it never used.
    #[test]
    fn the_previous_env_keeps_a_new_database_password() {
        let old = "MIKAN_IMAGE=old-image\nMIKAN_VERSION=0.4.4\nPANEL_PORT=21355\n";
        let now = EnvFile::from_text(
            "x",
            "MIKAN_IMAGE=new-image\nMIKAN_VERSION=0.5.0.1\nPANEL_PORT=21355\nMIKAN_POSTGRES_PASSWORD=made\nMIKAN_DATABASE_URL=postgresql://mikan:made@localhost/mikan?host=/run/postgresql\n",
        );
        let env = previous_env(Path::new("/opt/mikan"), &now, old).unwrap();
        assert_eq!(env.render(), format!("{old}MIKAN_POSTGRES_PASSWORD=made\n"));
        let unchanged = previous_env(Path::new("/opt/mikan"), &EnvFile::from_text("x", ""), old).unwrap();
        assert_eq!(unchanged.render(), old);
    }

    #[test]
    fn the_move_to_postgres_needs_disk_and_memory() {
        const GIB: u64 = 1 << 30;
        assert!(move_fits(Some(2 * GIB), Some(1024), 100 << 20).is_ok());
        assert!(move_fits(None, None, 100 << 20).is_ok(), "what cannot be read does not stop it");
        let small = move_fits(Some(900 << 20), Some(1024), 1 << 20).unwrap_err();
        assert!(small.contains("1024 MB needed"), "at least 1 GiB: {small}");
        assert!(move_fits(Some(5 * GIB), Some(1024), 2 * GIB).unwrap_err().contains("6144 MB needed"), "three times the data");
        assert!(move_fits(Some(5 * GIB), Some(400), 1 << 20).unwrap_err().contains("400 MB of memory"));
    }

    #[test]
    fn failed_current_release_is_retryable_but_old_manifests_never_downgrade() {
        assert!(!already_current("0.5.0.1", "0.5.0.1", false));
        assert!(already_current("0.5.0.1", "0.5.0.1", true));
        assert!(already_current("0.5.0.0", "0.5.0.1", false));
        assert!(already_current("0.5.0.0", "0.5.0.1", true));
        assert!(!already_current("0.5.0.2", "0.5.0.1", true));
    }

    fn tmpdir(name: &str) -> std::path::PathBuf {
        let d = std::env::temp_dir().join(format!("update-{name}-{}", std::process::id()));
        let _ = fs::remove_dir_all(&d);
        fs::create_dir_all(&d).unwrap();
        d
    }

    fn open(d: &Path) -> Dir {
        Dir::open(d, ".", false).unwrap().unwrap()
    }

    // The panel's Update button writes {"at": …}; its adapters write {"do": "addons"}.
    #[test]
    fn the_panels_requests() {
        let d = tmpdir("requests");
        let dir = open(&d);
        assert_eq!(take_request(&dir, false).unwrap(), Asked::Nothing);
        for (body, want) in
            [(r#"{"at":"2026-10-01T00:00:00Z"}"#, Asked::Update), (r#"{"do":"addons"}"#, Asked::Addons), ("not json", Asked::Update)]
        {
            fs::write(d.join("request"), body).unwrap();
            assert_eq!(take_request(&dir, false).unwrap(), want, "{body}");
            assert!(!d.join("request").exists(), "the request must go at once");
        }
        fs::remove_dir_all(&d).unwrap();
    }

    // What the panel may plant instead of a request: it is removed and logged, never
    // followed, never waited for, and the next look finds nothing (so the path unit that
    // watches the file has nothing to fire on).
    #[test]
    fn a_planted_request_is_removed_not_obeyed() {
        planted_request(false);
    }

    // A node's container plants the same things in its own directory, and its request is
    // read the same way.
    #[test]
    fn a_planted_node_request_is_removed_not_obeyed() {
        planted_request(true);
    }

    fn planted_request(node: bool) {
        let d = tmpdir(if node { "planted-node" } else { "planted" });
        let dir = open(&d);
        let secret = d.join("secret");
        fs::write(&secret, "root's").unwrap();
        type Plant<'a> = Box<dyn Fn(&Path) + 'a>;
        let plant: [(&str, Plant<'_>); 5] = [
            ("a directory", Box::new(|p| fs::create_dir_all(p.join("inner")).unwrap())),
            ("a link", Box::new(|p| symlink(d.join("secret"), p).unwrap())),
            ("a link to /dev/zero", Box::new(|p| symlink("/dev/zero", p).unwrap())),
            (
                "a FIFO",
                Box::new(|p| {
                    rustix::fs::mknodat(rustix::fs::CWD, p, rustix::fs::FileType::Fifo, rustix::fs::Mode::from_raw_mode(0o600), 0).unwrap()
                }),
            ),
            ("a big file", Box::new(|p| fs::write(p, vec![b'x'; 1 << 20]).unwrap())),
        ];
        for (what, make) in plant {
            let p = d.join("request");
            make(&p);
            let got = take_request(&dir, node).unwrap();
            assert!(matches!(got, Asked::Garbage(_)), "{what}: {got:?}");
            assert!(fs::symlink_metadata(&p).is_err(), "{what} stayed");
            assert_eq!(take_request(&dir, node).unwrap(), Asked::Nothing, "{what}: it is looked at again");
        }
        assert_eq!(fs::read_to_string(&secret).unwrap(), "root's");
        fs::remove_dir_all(&d).unwrap();
    }

    // Reports are plain files for the panel, written through the directory handle: a link
    // at status.json is replaced, and the contract's field names are what the panel reads.
    #[test]
    fn the_report_is_written_for_the_panel_and_replaces_a_link() {
        let d = tmpdir("report");
        let target = d.join("etc-passwd");
        fs::write(&target, "root:x:0:0").unwrap();
        symlink(&target, d.join("status.json")).unwrap();
        let dir = open(&d);
        let body = serde_json::json!({"state": Phase::Failed, "version": "0.4.4", "from": "0.4.3", "error": "x", "at": clock::rfc3339()});
        dir.write("status.json", body.to_string().as_bytes(), 0o644).unwrap();
        assert_eq!(fs::read_to_string(&target).unwrap(), "root:x:0:0", "the link's target stays");
        let v: serde_json::Value = serde_json::from_slice(&fs::read(d.join("status.json")).unwrap()).unwrap();
        assert_eq!((v["state"].as_str(), v["from"].as_str()), (Some("failed"), Some("0.4.3")));
        for p in [Phase::Running, Phase::Ok] {
            assert!(serde_json::to_string(&p).unwrap().chars().all(|c| c == '"' || c.is_ascii_lowercase()));
        }
        // the panel's Go side reads these names
        let go = fs::read_to_string("../internal/panel/updates/updates.go").unwrap();
        for tag in ["`json:\"state\"", "`json:\"version\"`", "`json:\"from\"`", "`json:\"error\"`", "`json:\"at\""] {
            assert!(go.contains(tag), "the panel has no {tag}");
        }
        assert!(go.contains("enum:\"running,ok,failed\""));
        // and the node's, which carries the same file in its health
        let node = fs::read_to_string("../internal/nodeapi/types.go").unwrap();
        for tag in ["`json:\"state\"", "`json:\"version\"`", "`json:\"from\"`", "`json:\"error,omitempty\"`", "`json:\"at\""] {
            assert!(node.contains(tag), "the node has no {tag}");
        }
        assert!(node.contains("enum:\"running,ok,failed\""));
        fs::remove_dir_all(&d).unwrap();
    }

    // The node's request is {"version": "X"} with X a release version, and nothing else of
    // it is read; whatever else is in the file, it is gone at once.
    #[test]
    fn a_nodes_request_names_a_release_and_nothing_else() {
        let d = tmpdir("node-requests");
        let dir = open(&d);
        let ask = |body: &str| {
            fs::write(d.join("request"), body).unwrap();
            let got = take_request(&dir, true).unwrap();
            assert!(!d.join("request").exists(), "{body}: the request must go at once");
            got
        };
        assert_eq!(take_request(&dir, true).unwrap(), Asked::Nothing);
        for ok in ["0.5.0.2", "0.5.0.2-rc.1", "1.2.3", "0.4.5"] {
            assert_eq!(ask(&format!(r#"{{"version":"{ok}"}}"#)), Asked::Version(ok.into()));
        }
        assert_eq!(ask(r#"{"version":"0.5.0.2","image":"evil/image@sha256:0","url":"http://evil"}"#), Asked::Version("0.5.0.2".into()));
        let long = format!(r#"{{"version":"0.5.0.2-{}"}}"#, "x".repeat(80));
        for bad in [
            "",
            "not json",
            "[]",
            "null",
            r#""0.5.0.2""#,
            "{}",
            r#"{"version":null}"#,
            r#"{"version":5}"#,
            r#"{"version":["0.5.0.2"]}"#,
            r#"{"version":""}"#,
            r#"{"version":"dev"}"#,
            r#"{"version":"v0.5.0.2"}"#,
            r#"{"version":"latest"}"#,
            r#"{"version":"0.5.0.2 "}"#,
            r#"{"version":"0.5.0.2\n"}"#,
            r#"{"version":"0.5"}"#,
            r#"{"version":"0.5.0.2.1"}"#,
            r#"{"version":"0.5.0.2/../x"}"#,
            r#"{"version":"ghcr.io/x/y@sha256:aa"}"#,
            r#"{"at":"2026-10-04T00:00:00Z"}"#,
            r#"{"do":"addons"}"#,
            long.as_str(),
        ] {
            assert!(matches!(ask(bad), Asked::Garbage(_)), "{bad}");
        }
        let big = format!(r#"{{"version":"0.5.0.2","pad":"{}"}}"#, "x".repeat(8192));
        assert!(matches!(ask(&big), Asked::Garbage(_)), "larger than any request");
        fs::remove_dir_all(&d).unwrap();
    }

    // Each side talks in the directory its container owns; the directory above it is what a
    // stand-in for it is cleared from (clear_update_dir).
    #[test]
    fn a_nodes_directory_is_not_the_panels() {
        assert_eq!(update_rel(false), "data/panel/update");
        assert_eq!(update_rel(true), "data/node/update");
        assert_eq!(update_rel(true).trim_end_matches("/update"), "data/node");
        assert_eq!(update_rel(false).trim_end_matches("/update"), "data/panel");
    }

    // What a request for a release means, and what the daily timer of a node may do.
    #[test]
    fn a_server_never_goes_back_and_a_node_follows_its_panel() {
        assert_eq!(upto_step("0.5.0.1", "0.5.0.2"), Step::Go);
        assert_eq!(upto_step("0.4.5", "0.5.0.2"), Step::Go);
        assert_eq!(upto_step("0.5.0.2", "0.5.0.2"), Step::Already);
        assert_eq!(upto_step("0.4.5", "0.4.5.0"), Step::Already, "three numbers and four are one version");
        assert_eq!(upto_step("0.5.0.2-rc.1", "0.5.0.2"), Step::Go);
        for (current, upto) in [("0.5.0.3", "0.5.0.2"), ("0.6.0.0", "0.5.0.2"), ("0.5.0.2", "0.5.0.2-rc.1"), ("1.0.0", "0.9.9.9")] {
            assert!(matches!(upto_step(current, upto), Step::Refuse(_)), "{current} to {upto}");
        }
        // a pre-release asked for is looked up on beta, whatever the node's channel says
        assert_eq!(upto_channel(Channel::Stable, "0.5.0.3-rc.1"), Channel::Beta);
        assert_eq!(upto_channel(Channel::Stable, "0.5.0.3"), Channel::Stable);
        assert_eq!(upto_channel(Channel::Beta, "0.5.0.3"), Channel::Beta);

        let node = |env: &str| Install { env: EnvFile::from_text("x", &format!("MIKAN_MODE=node\n{env}")), node: true };
        assert!(!auto_on(&node("")), "a node follows its panel unless its .env says otherwise");
        assert!(!auto_on(&node("MIKAN_AUTO_UPDATE=0\n")));
        for not_one in ["true", "yes", "on", "2", " 1", "1 "] {
            assert!(!auto_on(&node(&format!("MIKAN_AUTO_UPDATE={not_one}\n"))), "{not_one:?}");
        }
        assert!(auto_on(&node("MIKAN_AUTO_UPDATE=1\n")));
    }

    // The chain a request makes: each hop chooses again from the version the last one left,
    // never past the release asked for (see hop_by_hop_to_the_newest for the same chain
    // without a limit).
    #[test]
    fn a_requested_chain_stops_at_the_release_asked_for() {
        let signer = ed25519_dalek::SigningKey::from_bytes(&[7; 32]);
        let entry = |v: &str, channel: &str, from: &str| {
            format!(
                r#"{{"version":"{v}","channel":"{channel}","from":"{from}","manifest":"https://github.com/{}/releases/download/v{v}/manifest.json"}}"#,
                release::REPO
            )
        };
        let releases = [
            ("0.4.5", "stable", "0.4.0"),
            ("0.5.0.0", "stable", "0.4.5"),
            ("0.5.0.1", "stable", "0.4.5"),
            ("0.5.0.2", "stable", "0.5.0.1"),
            ("0.5.0.3-rc.1", "beta", "0.5.0.2"),
            ("0.6.0.0", "stable", "0.5.0.2"),
        ];
        let list: Vec<String> = releases.iter().map(|(v, c, from)| entry(v, c, from)).collect();
        let versions: Vec<&str> = releases.iter().map(|(v, ..)| *v).collect();
        let files = signed_files(&signer, &versions, &format!(r#"{{"schema":1,"releases":[{}]}}"#, list.join(",")));
        let fetch = |url: &str, _: u64| -> Result<Option<Vec<u8>>> { Ok(files.get(url).cloned()) };
        let key = signer.verifying_key();
        // from a version, towards upto: what the hop is and what comes after it
        let hop = |current: &str, upto: &str, channel: Channel| {
            release::find_upto_with(current, upto_channel(channel, upto), upto, &key, &fetch)
                .map(|f| (f.manifest.version, f.newest, f.unreachable))
        };
        let step = |current: &str, upto: &str| hop(current, upto, Channel::Stable).unwrap();
        // directly, when it can be
        assert_eq!(step("0.5.0.1", "0.5.0.2"), ("0.5.0.2".into(), None, false));
        // the highest reachable one up to it, and on from there, one release at a time
        assert_eq!(step("0.4.5", "0.5.0.2"), ("0.5.0.1".into(), Some("0.5.0.2".into()), false));
        assert_eq!(step("0.5.0.1", "0.6.0.0"), ("0.5.0.2".into(), Some("0.6.0.0".into()), false));
        // never past it, though newer ones are out and reachable
        assert_eq!(step("0.5.0.1", "0.5.0.2").0, "0.5.0.2");
        assert_eq!(step("0.4.5", "0.5.0.0"), ("0.5.0.0".into(), None, false));
        assert_eq!(step("0.4.5", "0.5.0.1").0, "0.5.0.1");
        // a pre-release is asked for on beta whatever the node's channel is
        assert_eq!(step("0.5.0.2", "0.5.0.3-rc.1"), ("0.5.0.3-rc.1".into(), None, false));
        // not a release of the index, or of the channel: refused, and GitHub's latest is no way round it
        for (current, upto) in [("0.5.0.1", "0.5.0.9"), ("0.5.0.1", "0.4.4"), ("0.5.0.1", "0.5.0.3-rc.2"), ("0.5.0.1", "0.7.0.0")] {
            let err = hop(current, upto, Channel::Stable).unwrap_err();
            assert!(format!("{err:#}").contains("not a release of this server's channel"), "{current} to {upto}: {err:#}");
        }
        // one that no release in between leads to
        let (v, newest, unreachable) = step("0.3.9", "0.5.0.2");
        assert!(unreachable && newest.as_deref() == Some("0.5.0.2"), "{v} {newest:?}");
        // and the whole chain from 0.4.5 to 0.6.0.0, as the hops make it
        let mut current = "0.4.5".to_owned();
        let mut went = Vec::new();
        loop {
            let (version, newest, _) = step(&current, "0.6.0.0");
            went.push(version.clone());
            current = version;
            if newest.is_none() {
                break;
            }
        }
        assert_eq!(went, ["0.5.0.1", "0.5.0.2", "0.6.0.0"]);
    }

    // The index is gone: GitHub's latest release answers only when it is the release asked for.
    #[test]
    fn without_the_index_only_the_release_asked_for_will_do() {
        let signer = ed25519_dalek::SigningKey::from_bytes(&[7; 32]);
        let key = signer.verifying_key();
        let mut files = signed_files(&signer, &["0.5.0.2"], r#"{"schema":1,"releases":[]}"#);
        // no index at all, and a latest release
        files.retain(|url, _| !url.contains("/updates/"));
        let latest = release::latest_url();
        let manifest = files[&format!("https://github.com/{}/releases/download/v0.5.0.2/manifest.json", release::REPO)].clone();
        let sig = files[&format!("https://github.com/{}/releases/download/v0.5.0.2/manifest.json.sig", release::REPO)].clone();
        files.insert(latest.clone(), manifest);
        files.insert(format!("{latest}.sig"), sig);
        let fetch = |url: &str, _: u64| -> Result<Option<Vec<u8>>> { Ok(files.get(url).cloned()) };
        let got = release::find_upto_with("0.5.0.1", Channel::Stable, "0.5.0.2", &key, &fetch).unwrap();
        assert_eq!(got.manifest.version, "0.5.0.2");
        assert!(got.fallback.is_some() && got.newest.is_none());
        for upto in ["0.5.0.3", "0.5.0.1.1", "0.6.0.0"] {
            let err = release::find_upto_with("0.5.0.1", Channel::Stable, upto, &key, &fetch).unwrap_err();
            assert!(format!("{err:#}").contains("not the mikan"), "{upto}: {err:#}");
        }
        // an index that is there but says nothing of the release is a refusal, not a reason to look elsewhere
        let files = signed_files(&signer, &["0.5.0.2"], r#"{"schema":1,"releases":[]}"#);
        let fetch = |url: &str, _: u64| -> Result<Option<Vec<u8>>> { Ok(files.get(url).cloned()) };
        assert!(release::find_upto_with("0.5.0.1", Channel::Stable, "0.5.0.2", &key, &fetch).is_err());
    }

    // The panel writes the channel; only the two exact words get through, and nothing the
    // panel plants instead of the file (a link, a FIFO, a huge file) is followed or waited for.
    #[test]
    fn the_channel_from_the_panel_is_untrusted() {
        for (body, want) in [
            (r#"{"auto":true,"channel":"beta"}"#, Channel::Beta),
            (r#"{"auto":true,"channel":"stable"}"#, Channel::Stable),
            (r#"{"auto":true}"#, Channel::Stable),
            (r#"{"channel":"Beta"}"#, Channel::Stable),
            (r#"{"channel":"beta "}"#, Channel::Stable),
            (r#"{"channel":["beta"]}"#, Channel::Stable),
            (r#"{"channel":true}"#, Channel::Stable),
            (r#"{"channel":"nightly"}"#, Channel::Stable),
            (r#"{"channel":"stable","channel":"beta"}"#, Channel::Beta),
            ("not json", Channel::Stable),
            ("", Channel::Stable),
        ] {
            assert_eq!(policy_channel(body.as_bytes()), want, "{body}");
        }
        let d = tmpdir("channel");
        let dir = open(&d);
        let read = |dir: &Dir| dir.read("policy.json", 4096).ok().flatten().map_or(Channel::Stable, |b| policy_channel(&b));
        fs::write(d.join("beta.json"), r#"{"channel":"beta"}"#).unwrap();
        symlink(d.join("beta.json"), d.join("policy.json")).unwrap();
        assert_eq!(read(&dir), Channel::Stable, "a link is no policy");
        fs::remove_file(d.join("policy.json")).unwrap();
        fs::write(d.join("policy.json"), format!(r#"{{"channel":"beta","x":"{}"}}"#, "x".repeat(8192))).unwrap();
        assert_eq!(read(&dir), Channel::Stable, "a file larger than the panel writes");
        fs::remove_file(d.join("policy.json")).unwrap();
        rustix::fs::mknodat(rustix::fs::CWD, d.join("policy.json"), rustix::fs::FileType::Fifo, rustix::fs::Mode::from_raw_mode(0o600), 0)
            .unwrap();
        assert_eq!(read(&dir), Channel::Stable, "a FIFO is not waited for");
        fs::remove_file(d.join("policy.json")).unwrap();
        fs::write(d.join("policy.json"), r#"{"auto":false,"channel":"beta"}"#).unwrap();
        assert_eq!(read(&dir), Channel::Beta);
        fs::remove_dir_all(&d).unwrap();
    }

    // A node has no panel: its .env says, stable unless it says beta exactly.
    #[test]
    fn a_node_takes_its_channel_from_its_env() {
        let node = |env: &str| Install { env: EnvFile::from_text("x", &format!("MIKAN_MODE=node\n{env}")), node: true };
        assert_eq!(channel(&node("")), Channel::Stable);
        assert_eq!(channel(&node("MIKAN_UPDATE_CHANNEL=beta\n")), Channel::Beta);
        assert_eq!(channel(&node("MIKAN_UPDATE_CHANNEL=Beta\n")), Channel::Stable);
    }

    #[test]
    fn hops_are_bounded() {
        assert_eq!(after_hop(None, 0), After::Done);
        assert_eq!(after_hop(Some("0.6.0.0"), 0), After::Next(1));
        assert_eq!(after_hop(Some("0.6.0.0"), MAX_HOPS - 2), After::Next(MAX_HOPS - 1));
        assert_eq!(after_hop(Some("0.6.0.0"), MAX_HOPS - 1), After::Stop);
        assert_eq!(after_hop(None, MAX_HOPS + 3), After::Done);
    }

    /// A GitHub of signed files: the releases' manifests, the index and "latest".
    fn signed_files(signer: &ed25519_dalek::SigningKey, releases: &[&str], index: &str) -> std::collections::BTreeMap<String, Vec<u8>> {
        use base64::Engine;
        use ed25519_dalek::Signer;
        let mut files = std::collections::BTreeMap::new();
        let mut put = |url: String, data: Vec<u8>| {
            let sig = base64::engine::general_purpose::STANDARD.encode(signer.sign(&data).to_bytes());
            files.insert(format!("{url}.sig"), sig.into_bytes());
            files.insert(url, data);
        };
        for v in releases {
            let body = format!(
                r#"{{"version":"{v}","published":"2026-10-04T10:00:00Z","image":"ghcr.io/miroshka000/mikan","digest":"sha256:{}","installer":{{}}}}"#,
                "a".repeat(64)
            );
            put(format!("https://github.com/{}/releases/download/v{v}/manifest.json", release::REPO), body.into_bytes());
        }
        put(release::index_url(), index.as_bytes().to_vec());
        files
    }

    // The chain the hops make, each choosing again from the version the last one left
    // (as the command on the disk does after a handover): 0.4.4 goes through 0.4.5 and
    // 0.5.0.1 to 0.6.0.0, and a chain longer than MAX_HOPS stops for the next update.
    #[test]
    fn hop_by_hop_to_the_newest() {
        let signer = ed25519_dalek::SigningKey::from_bytes(&[7; 32]);
        let entry = |v: &str, from: &str| {
            format!(
                r#"{{"version":"{v}","channel":"stable","from":"{from}","manifest":"https://github.com/{}/releases/download/v{v}/manifest.json"}}"#,
                release::REPO
            )
        };
        let chain = |releases: &[(&str, &str)]| -> (Vec<String>, After) {
            let list: Vec<String> = releases.iter().map(|(v, from)| entry(v, from)).collect();
            let versions: Vec<&str> = releases.iter().map(|(v, _)| *v).collect();
            let files = signed_files(&signer, &versions, &format!(r#"{{"schema":1,"releases":[{}]}}"#, list.join(",")));
            let fetch = |url: &str, _: u64| -> Result<Option<Vec<u8>>> { Ok(files.get(url).cloned()) };
            let mut current = "0.4.4".to_owned();
            let mut went = Vec::new();
            for hop in 0.. {
                let found = release::find_with(Some(&current), Channel::Stable, &signer.verifying_key(), &fetch).unwrap();
                assert!(found.fallback.is_none(), "{:?}", found.fallback);
                if !release::newer(&found.manifest.version, &current) {
                    return (went, After::Done);
                }
                current.clone_from(&found.manifest.version);
                went.push(current.clone());
                match after_hop(found.newest.as_deref(), hop) {
                    After::Next(n) => assert_eq!(n, hop + 1),
                    other => return (went, other),
                }
            }
            unreachable!()
        };
        let (went, end) = chain(&[("0.4.5", "0.4.0"), ("0.5.0.0", "0.4.5"), ("0.5.0.1", "0.4.5"), ("0.6.0.0", "0.5.0.1")]);
        assert_eq!(went, ["0.4.5", "0.5.0.1", "0.6.0.0"]);
        assert_eq!(end, After::Done);
        let steps: Vec<String> = (0..8).map(|i| format!("0.4.{}", 5 + i)).collect();
        let long: Vec<(&str, &str)> =
            steps.iter().enumerate().map(|(i, v)| (v.as_str(), if i == 0 { "0.4.0" } else { steps[i - 1].as_str() })).collect();
        let (went, end) = chain(&long);
        assert_eq!(went.len() as u32, MAX_HOPS, "{went:?}");
        assert_eq!(end, After::Stop);
    }

    #[test]
    fn auto_update_only_follows_the_policy_file() {
        let d = tmpdir("policy");
        fs::write(d.join("policy.json"), r#"{"auto": true}"#).unwrap();
        let dir = open(&d);
        let on = |dir: &Dir| {
            dir.read("policy.json", 4096)
                .ok()
                .flatten()
                .and_then(|b| serde_json::from_slice::<serde_json::Value>(&b).ok())
                .is_some_and(|v| v["auto"] == true)
        };
        assert!(on(&dir));
        fs::remove_file(d.join("policy.json")).unwrap();
        symlink("/etc/hostname", d.join("policy.json")).unwrap();
        assert!(!on(&dir), "a link is no policy");
        fs::remove_dir_all(&d).unwrap();
    }
}
