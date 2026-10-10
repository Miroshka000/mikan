//! Docker and compose, through the docker command: what the server's admin would run by
//! hand, so nothing here needs the Docker socket's API.

use std::collections::BTreeMap;
use std::fs;
use std::io::{BufRead, BufReader, Read, Write};
use std::path::Path;
use std::process::{Child, Command, Output, Stdio};
use std::sync::mpsc;
use std::thread;
use std::time::{Duration, Instant};

use anyhow::{Context, Result, bail};
use serde::Deserialize;

use crate::DIR;
use crate::envfile::write_private;
use crate::system::output;

/// The database's image, pinned to the multi-arch index: compose.yaml and the pull before
/// an update name the same bytes, and a moved tag never starts another PostgreSQL.
macro_rules! postgres_image {
    () => {
        "postgres:18-alpine@sha256:77f585114c32fbca283dc835b0596f4e52b51b4c6662d7810b2f4084f60a1873"
    };
}
pub const POSTGRES_IMAGE: &str = postgres_image!();

/// The panel with its own node. Both run as an unprivileged user on the host network with
/// a read-only root; the panel reaches the node over a socket in a shared volume.
///
/// Each sees only its own data: the node parses traffic from the whole internet, and a
/// flaw in a protocol's parser must not hand over the panel's database, keys and the
/// files root reads (update/, addons/). The limits are far above what a busy node uses
/// (memory is cgroup-accounted; pids count threads, not connections): they stop a leak or
/// a flood from taking the host down with the containers.
///
/// PostgreSQL gets time to finish its shutdown checkpoint (a SIGKILL means crash recovery
/// on the next start) and only the capabilities its entrypoint uses to own its directories
/// and drop to its user (checked on Docker 28: initdb, a restart, an existing volume). The
/// panel waits up to about 30 s for its workers, so it gets 45 s; it reaches the socket
/// through a read-only mount (connecting needs no write access to the mount).
///
/// The panel answers Let's Encrypt on port 80, or where MIKAN_ACME_LISTEN says when a web
/// server holds that port and passes the challenge on (see acme.rs).
pub const PANEL_COMPOSE: &str = concat!(
    r#"name: mikan

x-hardening: &hardening
  image: ${MIKAN_IMAGE}
  network_mode: host
  restart: unless-stopped
  user: "65532:65532"
  cap_drop: [ALL]
  cap_add: [NET_BIND_SERVICE]
  security_opt: ["no-new-privileges:true"]
  read_only: true
  tmpfs: ["/tmp:rw,size=64m"]
  logging:
    driver: json-file
    options: {max-size: "10m", max-file: "3"}

services:
  node:
    <<: *hardening
    entrypoint: ["/usr/local/bin/mikan-node"]
    environment:
      MIKAN_DATA_DIR: /data/node
      MIKAN_NODE_SOCKET: /run/mikan/node.sock
    volumes: ["./data/node:/data/node", "run:/run/mikan"]
    pids_limit: 1024

  panel:
    <<: *hardening
    command: ["serve"]
    depends_on:
      node: {condition: service_started}
      postgres: {condition: service_healthy}
    environment:
      MIKAN_DATA_DIR: /data/panel
      MIKAN_NODE_SOCKET: /run/mikan/node.sock
      MIKAN_PANEL_LISTEN: 0.0.0.0:${PANEL_PORT}
      MIKAN_ACME_LISTEN: "${MIKAN_ACME_LISTEN:-:80}"
      MIKAN_DATABASE_URL: ${MIKAN_DATABASE_URL}
    volumes: ["./data/panel:/data/panel", "run:/run/mikan", "pg-run:/run/postgresql:ro"]
    mem_limit: 1g
    pids_limit: 512
    stop_grace_period: 45s
    healthcheck:
      test: ["CMD", "/usr/local/bin/mikan", "health"]
      interval: 30s
      timeout: 5s
      retries: 3

  postgres:
    image: "#,
    postgres_image!(),
    r#"
    network_mode: none
    restart: unless-stopped
    cap_drop: [ALL]
    cap_add: [CHOWN, DAC_OVERRIDE, FOWNER, SETGID, SETUID]
    security_opt: ["no-new-privileges:true"]
    environment:
      POSTGRES_USER: mikan
      POSTGRES_DB: mikan
      POSTGRES_PASSWORD: ${MIKAN_POSTGRES_PASSWORD}
      POSTGRES_INITDB_ARGS: --auth-local=scram-sha-256 --auth-host=scram-sha-256
    command: ["postgres", "-c", "listen_addresses=", "-c", "unix_socket_directories=/run/postgresql", "-c", "unix_socket_permissions=0777", "-c", "max_connections=50"]
    volumes: ["pg-data:/var/lib/postgresql", "pg-run:/run/postgresql"]
    shm_size: 256m
    mem_limit: 1g
    pids_limit: 256
    stop_grace_period: 120s
    healthcheck:
      test: ["CMD", "pg_isready", "-h", "/run/postgresql", "-U", "mikan", "-d", "mikan"]
      interval: 10s
      timeout: 5s
      start_period: 60s
      retries: 6
    logging:
      driver: json-file
      options: {max-size: "10m", max-file: "3"}

volumes:
  run: {}
  pg-run: {}
  pg-data: {}
"#
);

/// The compose project's volume that holds the database (compose prefixes the project).
pub const PG_VOLUME: &str = "mikan_pg-data";

/// A node of another panel, which drives it over its API port.
pub const NODE_COMPOSE: &str = r#"name: mikan

services:
  node:
    image: ${MIKAN_IMAGE}
    network_mode: host
    restart: unless-stopped
    user: "65532:65532"
    cap_drop: [ALL]
    cap_add: [NET_BIND_SERVICE]
    security_opt: ["no-new-privileges:true"]
    read_only: true
    tmpfs: ["/tmp:rw,size=64m"]
    logging:
      driver: json-file
      options: {max-size: "10m", max-file: "3"}
    entrypoint: ["/usr/local/bin/mikan-node"]
    environment:
      MIKAN_DATA_DIR: /data/node
      MIKAN_NODE_SOCKET: /run/mikan/node.sock
      MIKAN_NODE_JOIN: ${MIKAN_NODE_JOIN}
      MIKAN_ACME_LISTEN: "${MIKAN_ACME_LISTEN:-:80}"
    volumes: ["./data/node:/data/node", "run:/run/mikan"]
    pids_limit: 1024

volumes:
  run: {}
"#;

/// The compose file of this installer for a panel or a node.
pub fn compose_text(node: bool) -> &'static str {
    if node { NODE_COMPOSE } else { PANEL_COMPOSE }
}

/// Makes compose.yaml the one this installer writes: it has been written once, at install,
/// so a server installed by an older mikan keeps that one (the whole ./data mounted in both
/// containers, no limits) until this runs. Returns the text it replaced, for the caller to
/// put back if the new one does not start; the old file also stays as compose.yaml.old.
pub fn ensure_compose(root: &Path, node: bool) -> Result<Option<String>> {
    let path = root.join("compose.yaml");
    let want = compose_text(node);
    let have = fs::read_to_string(&path).ok();
    if have.as_deref() == Some(want) {
        return Ok(None);
    }
    if let Some(old) = &have {
        write_private(&root.join("compose.yaml.old"), old.as_bytes())?;
    }
    write_private(&path, want.as_bytes())?;
    Ok(Some(have.unwrap_or_default()))
}

/// Puts a compose.yaml back (the one ensure_compose replaced).
pub fn put_compose(root: &Path, text: &str) -> Result<()> {
    if text.is_empty() {
        return Ok(());
    }
    write_private(&root.join("compose.yaml"), text.as_bytes())
}

/// The Docker engine's version, None without Docker.
pub fn version() -> Option<String> {
    output("docker", &["version", "--format", "{{.Server.Version}}"]).map(|v| v.trim().to_owned()).filter(|v| !v.is_empty())
}

pub fn compose_ok() -> bool {
    output("docker", &["compose", "version"]).is_some()
}

/// Runs a command and hands each line of its output (stdout and stderr) to line.
pub fn stream(cmd: Command, line: impl FnMut(&str)) -> Result<()> {
    stream_with(cmd, None, None, line)
}

/// As stream, with text for the command's stdin and a limit on the silence: a command
/// that prints nothing for idle (a pull on a dead connection) is killed.
pub fn stream_with(mut cmd: Command, stdin: Option<Vec<u8>>, idle: Option<Duration>, mut line: impl FnMut(&str)) -> Result<()> {
    let name = format!("{cmd:?}");
    let input = if stdin.is_some() { Stdio::piped() } else { Stdio::null() };
    let mut child = cmd.stdin(input).stdout(Stdio::piped()).stderr(Stdio::piped()).spawn().with_context(|| format!("run {name}"))?;
    if let (Some(data), Some(mut pipe)) = (stdin, child.stdin.take()) {
        // From a thread: a command that does not read it yet must not stall this one.
        thread::spawn(move || {
            let _ = pipe.write_all(&data);
        });
    }
    let (tx, rx) = mpsc::channel::<String>();
    let readers = [forward(child.stdout.take().context("stdout")?, tx.clone()), forward(child.stderr.take().context("stderr")?, tx)];
    let mut tail = Vec::new();
    let mut silent = false;
    loop {
        let next = match idle {
            Some(limit) => rx.recv_timeout(limit),
            None => rx.recv().map_err(|_| mpsc::RecvTimeoutError::Disconnected),
        };
        match next {
            Ok(l) => {
                line(&l);
                tail.push(l);
                if tail.len() > 20 {
                    tail.remove(0);
                }
            }
            Err(mpsc::RecvTimeoutError::Timeout) => {
                silent = true;
                let _ = child.kill();
                break;
            }
            Err(mpsc::RecvTimeoutError::Disconnected) => break,
        }
    }
    // After a kill the grandchildren may still hold the pipes: do not wait for them.
    if !silent {
        for r in readers {
            let _ = r.join();
        }
    }
    let status = child.wait()?;
    if silent {
        bail!("{name} printed nothing for {} minutes and was stopped", idle.map_or(0, |d| d.as_secs() / 60));
    }
    if !status.success() {
        bail!("{name} failed ({status}):\n{}", tail.join("\n"));
    }
    Ok(())
}

/// Starts a command whose output lines come through the receiver; stopping the child is
/// the caller's.
pub fn follow(mut cmd: Command) -> Result<(Child, mpsc::Receiver<String>)> {
    let mut child = cmd.stdin(Stdio::null()).stdout(Stdio::piped()).stderr(Stdio::piped()).spawn()?;
    let (tx, rx) = mpsc::channel();
    forward(child.stdout.take().context("stdout")?, tx.clone());
    forward(child.stderr.take().context("stderr")?, tx);
    Ok((child, rx))
}

/// Sends the lines of a pipe to tx; bytes that are not UTF-8 do not stop it.
fn forward(r: impl Read + Send + 'static, tx: mpsc::Sender<String>) -> thread::JoinHandle<()> {
    thread::spawn(move || {
        let mut r = BufReader::new(r);
        let mut buf = Vec::new();
        while r.read_until(b'\n', &mut buf).is_ok_and(|n| n > 0) {
            let _ = tx.send(String::from_utf8_lossy(&buf).trim_end().to_owned());
            buf.clear();
        }
    })
}

/// What a distribution ships as Docker (Ubuntu's and Debian's docker.io, often without
/// compose v2) and what Docker CE replaces.
const DISTRO_PACKAGES: [&str; 7] =
    ["docker.io", "docker-compose", "docker-compose-v2", "docker-doc", "podman-docker", "containerd", "runc"];

/// Replaces a Docker that cannot run mikan (no compose v2) with Docker CE from
/// get.docker.com. The packages are removed, not purged: images, volumes and containers
/// stay in /var/lib/docker and come back with the new engine.
pub fn replace(mut line: impl FnMut(&str)) -> Result<()> {
    let snap = which("docker").is_some_and(|p| p.starts_with("/snap/"));
    if snap {
        // snap keeps a snapshot of its data on removal.
        let mut cmd = Command::new("snap");
        cmd.args(["remove", "docker"]);
        stream(cmd, &mut line).context("remove the snap docker")?;
    }
    let installed: Vec<&str> = DISTRO_PACKAGES.into_iter().filter(|p| package_installed(p)).collect();
    if !installed.is_empty() {
        let mut cmd = Command::new("apt-get");
        cmd.args(["remove", "-y"]).args(&installed).env("DEBIAN_FRONTEND", "noninteractive");
        stream(cmd, &mut line).context("remove the old Docker")?;
    }
    install(line)
}

fn package_installed(name: &str) -> bool {
    output("dpkg-query", &["-W", "-f=${Status}", name]).is_some_and(|s| s.contains("install ok installed"))
}

fn which(cmd: &str) -> Option<String> {
    output("sh", &["-c", &format!("command -v {cmd}")]).map(|s| s.trim().to_owned()).filter(|s| !s.is_empty())
}

/// Installs Docker with its official script, get.docker.com. The script goes to the shell
/// on its stdin, the way the documented `curl | sh` does, so there is no file in /tmp for
/// anyone to swap between the download and the run.
pub fn install(line: impl FnMut(&str)) -> Result<()> {
    let script = crate::net::get("https://get.docker.com", 1 << 20).context("download get.docker.com")?;
    let mut cmd = Command::new("sh");
    cmd.arg("-s").env("DEBIAN_FRONTEND", "noninteractive");
    stream_with(cmd, Some(script), Some(INSTALL_IDLE), line)?;
    let _ = Command::new("systemctl").args(["enable", "--now", "docker"]).stdout(Stdio::null()).stderr(Stdio::null()).status();
    if !compose_ok() {
        bail!("Docker is installed without compose v2");
    }
    Ok(())
}

/// Counts layers in `docker pull` output: its share of done layers is the progress.
#[derive(Default)]
pub struct PullProgress {
    layers: BTreeMap<String, bool>,
}

impl PullProgress {
    /// Takes one line of `docker pull`; returns the progress when it moved.
    pub fn feed(&mut self, line: &str) -> Option<f64> {
        let (id, state) = line.split_once(": ")?;
        if id.len() != 12 || !id.bytes().all(|b| b.is_ascii_hexdigit()) {
            return None;
        }
        let done = matches!(state.trim(), "Pull complete" | "Already exists");
        let entry = self.layers.entry(id.to_owned()).or_insert(false);
        *entry |= done;
        let finished = self.layers.values().filter(|d| **d).count();
        Some(finished as f64 / self.layers.len() as f64)
    }
}

/// A pull that prints nothing for this long has lost its connection (without a terminal
/// docker prints a line per layer, not a progress bar): without a limit it would hold the
/// update unit, and every update after it, for good.
const PULL_IDLE: Duration = Duration::from_secs(20 * 60);
const INSTALL_IDLE: Duration = Duration::from_secs(15 * 60);

pub fn pull(reference: &str, mut progress: impl FnMut(f64)) -> Result<()> {
    let mut p = PullProgress::default();
    let mut cmd = Command::new("docker");
    cmd.args(["pull", reference]);
    stream_with(cmd, None, Some(PULL_IDLE), |l| {
        if let Some(x) = p.feed(l) {
            progress(x);
        }
    })
}

/// Whether the engine has the image already (a pulled one, or one loaded from an archive).
pub fn image_present(reference: &str) -> bool {
    Command::new("docker")
        .args(["image", "inspect", reference])
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .status()
        .is_ok_and(|s| s.success())
}

/// Loads an image archive (docker save | gzip) and returns the image's name.
pub fn load(archive: &str) -> Result<String> {
    let out = Command::new("docker").args(["load", "-i", archive]).output()?;
    if !out.status.success() {
        bail!("docker load: {}", String::from_utf8_lossy(&out.stderr).trim());
    }
    String::from_utf8_lossy(&out.stdout)
        .lines()
        .filter_map(|l| l.strip_prefix("Loaded image: "))
        .next_back()
        .map(str::to_owned)
        .context("the archive has no image")
}

/// `docker compose` for /opt/mikan.
pub fn compose(args: &[&str]) -> Command {
    let mut cmd = Command::new("docker");
    cmd.args(["compose", "--project-directory", DIR]).args(args).current_dir(DIR);
    cmd
}

/// Runs a compose command and fails with its output.
pub fn compose_run(args: &[&str]) -> Result<Output> {
    let out = compose(args).stdin(Stdio::null()).output().with_context(|| format!("docker compose {}", args.join(" ")))?;
    if !out.status.success() {
        let msg = String::from_utf8_lossy(&out.stderr);
        bail!("docker compose {}: {}", args.first().unwrap_or(&""), msg.trim());
    }
    Ok(out)
}

/// `mikan admin …` in the running panel. stdin is fed to the command when given.
pub fn admin(args: &[&str], stdin: Option<&str>) -> Result<Output> {
    let mut full = vec!["exec", "-T", "panel", "mikan", "admin"];
    full.extend_from_slice(args);
    with_stdin(compose(&full), stdin)
}

/// `mikan admin …` in a one-off panel container, before the panel runs (bootstrap).
pub fn admin_once(args: &[&str], stdin: Option<&str>) -> Result<Output> {
    let mut full = vec!["run", "--rm", "--no-deps", "-T", "panel", "admin"];
    full.extend_from_slice(args);
    with_stdin(compose(&full), stdin)
}

/// The longest a database command may take: importing a large SQLite or dumping a large
/// database takes minutes, and one that hangs must not hold the update unit for good.
const DATABASE_LIMIT: Duration = Duration::from_secs(60 * 60);

/// Database administration while the panel is stopped: no bot, API or statistics writer.
pub fn database(args: &[&str]) -> Result<Output> {
    database_within(args, DATABASE_LIMIT)
}

/// A database command that outlives limit is stopped with its container (killing `compose
/// run` alone leaves the container running); PostgreSQL rolls its transaction back.
fn database_within(args: &[&str], limit: Duration) -> Result<Output> {
    let name = format!("mikan-database-{}-{}", std::process::id(), crate::clock::Utc::now().stamp());
    let mut full = vec!["run", "--rm", "--no-deps", "-T", "--name", name.as_str(), "panel", "database"];
    full.extend_from_slice(args);
    let what = format!("docker compose run panel database {}", args.join(" "));
    let child = compose(&full).stdin(Stdio::null()).stdout(Stdio::piped()).stderr(Stdio::piped()).spawn().with_context(|| what.clone())?;
    match wait_within(child, limit)? {
        Some(out) if out.status.success() => Ok(out),
        Some(out) => bail!("{what}: {}", String::from_utf8_lossy(&out.stderr).trim()),
        None => {
            let _ =
                Command::new("docker").args(["rm", "-f", &name]).stdin(Stdio::null()).stdout(Stdio::null()).stderr(Stdio::null()).status();
            bail!("{what} did not finish in {} minutes and was stopped", limit.as_secs() / 60)
        }
    }
}

/// Waits for a child with piped output for up to limit; None when it was killed for it.
fn wait_within(mut child: Child, limit: Duration) -> Result<Option<Output>> {
    let collect = |mut r: Box<dyn Read + Send>| {
        thread::spawn(move || {
            let mut buf = Vec::new();
            let _ = r.read_to_end(&mut buf);
            buf
        })
    };
    let stdout = collect(Box::new(child.stdout.take().context("stdout")?));
    let stderr = collect(Box::new(child.stderr.take().context("stderr")?));
    let start = Instant::now();
    loop {
        if let Some(status) = child.try_wait()? {
            let stdout = stdout.join().unwrap_or_default();
            let stderr = stderr.join().unwrap_or_default();
            return Ok(Some(Output { status, stdout, stderr }));
        }
        if start.elapsed() > limit {
            // Its children may still hold the pipes: the readers are not waited for.
            let _ = child.kill();
            let _ = child.wait();
            return Ok(None);
        }
        thread::sleep(Duration::from_millis(200));
    }
}

pub fn postgres_ready() -> Result<()> {
    compose_run(&["up", "-d", "--wait", "--wait-timeout", "120", "postgres"])?;
    Ok(())
}

/// Pulls the database's image unless the engine has it: before an update stops anything,
/// with the progress and the silence limit of every pull, not inside `compose up`.
pub fn pull_postgres(progress: impl FnMut(f64)) -> Result<()> {
    if image_present(POSTGRES_IMAGE) {
        return Ok(());
    }
    pull(POSTGRES_IMAGE, progress)
}

/// Where a volume of the engine keeps its files; None when there is no such volume.
pub fn volume_mountpoint(name: &str) -> Option<std::path::PathBuf> {
    output("docker", &["volume", "inspect", "--format", "{{.Mountpoint}}", name])
        .map(|s| s.trim().to_owned())
        .filter(|s| !s.is_empty())
        .map(std::path::PathBuf::from)
}

fn with_stdin(mut cmd: Command, stdin: Option<&str>) -> Result<Output> {
    let mut child = cmd.stdin(Stdio::piped()).stdout(Stdio::piped()).stderr(Stdio::piped()).spawn()?;
    if let Some(s) = stdin {
        child.stdin.take().context("stdin")?.write_all(s.as_bytes())?;
    }
    drop(child.stdin.take());
    Ok(child.wait_with_output()?)
}

/// Fails with what the command said on stderr.
pub fn check(out: Output) -> Result<Output> {
    if out.status.success() {
        return Ok(out);
    }
    let err = String::from_utf8_lossy(&out.stderr);
    let msg = err.lines().rev().find(|l| !l.trim().is_empty()).unwrap_or("failed").trim().trim_start_matches("mikan: ");
    bail!("{msg}")
}

/// The panel answers on its port (the container's own health check).
pub fn panel_healthy() -> bool {
    compose(&["exec", "-T", "panel", "mikan", "health"])
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .status()
        .is_ok_and(|s| s.success())
}

#[derive(Deserialize, Debug, Clone, Default)]
pub struct Service {
    #[serde(rename = "Service", default)]
    pub name: String,
    #[serde(rename = "State", default)]
    pub state: String,
    #[serde(rename = "Status", default)]
    pub status: String,
}

/// The containers of the compose project.
pub fn services() -> Result<Vec<Service>> {
    let out = compose_run(&["ps", "--all", "--format", "json"])?;
    parse_services(&String::from_utf8_lossy(&out.stdout))
}

/// Compose prints a JSON array (older versions) or one object per line.
fn parse_services(text: &str) -> Result<Vec<Service>> {
    let text = text.trim();
    if text.starts_with('[') {
        return Ok(serde_json::from_str(text)?);
    }
    text.lines().filter(|l| !l.trim().is_empty()).map(|l| Ok(serde_json::from_str(l)?)).collect()
}

#[derive(Deserialize, Debug, Clone, Default)]
pub struct Stats {
    #[serde(rename = "Name", default)]
    pub name: String,
    #[serde(rename = "CPUPerc", default)]
    pub cpu: String,
    #[serde(rename = "MemUsage", default)]
    pub mem: String,
}

/// CPU and memory of the running containers.
pub fn stats() -> Vec<Stats> {
    let Some(ids) = compose(&["ps", "-q"]).output().ok().map(|o| String::from_utf8_lossy(&o.stdout).into_owned()) else {
        return Vec::new();
    };
    let ids: Vec<&str> = ids.split_whitespace().collect();
    if ids.is_empty() {
        return Vec::new();
    }
    let mut args = vec!["stats", "--no-stream", "--format", "{{json .}}"];
    args.extend(ids);
    output("docker", &args).map(|o| o.lines().filter_map(|l| serde_json::from_str(l).ok()).collect()).unwrap_or_default()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn postgres_is_private_persistent_and_password_authenticated() {
        let pg = PANEL_COMPOSE.split("\n  postgres:").nth(1).unwrap().split("\nvolumes:").next().unwrap();
        assert!(pg.contains("network_mode: none"));
        assert!(!pg.contains("ports:") && !pg.contains("network_mode: host"));
        assert!(pg.contains("pg-data:/var/lib/postgresql"));
        assert!(pg.contains("--auth-local=scram-sha-256"));
        assert!(pg.contains("listen_addresses="));
        assert!(!NODE_COMPOSE.contains("postgres"), "standalone nodes must not create a database");
        // one pinned image for compose and the pull before an update
        assert!(pg.contains(&format!("image: {POSTGRES_IMAGE}\n")), "{pg}");
        assert!(
            POSTGRES_IMAGE.starts_with("postgres:18-alpine@sha256:") && POSTGRES_IMAGE.len() == "postgres:18-alpine@sha256:".len() + 64
        );
        assert!(pg.contains("cap_drop: [ALL]") && pg.contains("no-new-privileges:true"));
        assert!(pg.contains("stop_grace_period: 120s"), "a SIGKILL mid-checkpoint means crash recovery");
        let panel = PANEL_COMPOSE.split("\n  panel:").nth(1).unwrap().split("\n  postgres:").next().unwrap();
        assert!(panel.contains("pg-run:/run/postgresql:ro") && panel.contains("stop_grace_period: 45s"));
    }

    #[test]
    fn a_command_past_its_limit_is_stopped_and_one_within_it_answers() {
        let child = |script: &str| {
            Command::new("sh").args(["-c", script]).stdin(Stdio::null()).stdout(Stdio::piped()).stderr(Stdio::piped()).spawn().unwrap()
        };
        let started = Instant::now();
        assert!(wait_within(child("sleep 30"), Duration::from_millis(300)).unwrap().is_none());
        assert!(started.elapsed() < Duration::from_secs(10));
        let out = wait_within(child("echo out; echo err >&2; exit 3"), Duration::from_secs(10)).unwrap().unwrap();
        assert_eq!((out.status.code(), out.stdout.as_slice(), out.stderr.as_slice()), (Some(3), &b"out\n"[..], &b"err\n"[..]));
    }

    #[test]
    fn pull_progress() {
        let mut p = PullProgress::default();
        assert_eq!(p.feed("0.3.9: Pulling from miroshka000/mikan"), None);
        assert_eq!(p.feed("4f4fb700ef54: Pulling fs layer"), Some(0.0));
        assert_eq!(p.feed("a1b2c3d4e5f6: Already exists"), Some(0.5));
        assert_eq!(p.feed("4f4fb700ef54: Download complete"), Some(0.5));
        assert_eq!(p.feed("4f4fb700ef54: Pull complete"), Some(1.0));
        assert_eq!(p.feed("Digest: sha256:abc"), None);
    }

    // The node parses traffic from the internet: it must not see the panel's database and
    // keys, nor the panel the node's. The directories the programs use are the ones mounted.
    #[test]
    fn each_container_sees_only_its_own_data() {
        for (name, text) in [("panel", PANEL_COMPOSE), ("node", NODE_COMPOSE)] {
            assert!(!text.contains("./data:/data"), "{name}: all of ./data is mounted");
            assert!(text.contains("pids_limit"), "{name}: no limits");
        }
        let node = PANEL_COMPOSE.split("\n  node:").nth(1).unwrap().split("\n  panel:").next().unwrap();
        let panel = PANEL_COMPOSE.split("\n  panel:").nth(1).unwrap().split("\nvolumes:").next().unwrap();
        assert!(node.contains("./data/node:/data/node") && node.contains("MIKAN_DATA_DIR: /data/node"));
        assert!(!node.contains("data/panel"), "the node reaches the panel's data");
        assert!(panel.contains("./data/panel:/data/panel") && panel.contains("MIKAN_DATA_DIR: /data/panel"));
        assert!(!panel.contains("data/node"), "the panel reaches the node's data");
        // The panel's memory is bounded; a node's grows with its connections, and one killed
        // for it would drop every client, so it has none.
        assert!(panel.contains("mem_limit") && !node.contains("mem_limit") && !NODE_COMPOSE.contains("mem_limit"));
        assert!(NODE_COMPOSE.contains("./data/node:/data/node") && !NODE_COMPOSE.contains("data/panel"));
        // The node answers the CA for its own certificate, past a web server when told to.
        assert!(NODE_COMPOSE.contains("MIKAN_ACME_LISTEN: \"${MIKAN_ACME_LISTEN:-:80}\""));
    }

    fn tmpdir(name: &str) -> std::path::PathBuf {
        let d = std::env::temp_dir().join(format!("docker-{name}-{}", std::process::id()));
        let _ = fs::remove_dir_all(&d);
        fs::create_dir_all(&d).unwrap();
        d
    }

    // A server installed by 0.4.3 has the old compose.yaml: it is replaced once, the old
    // text is kept for the caller's rollback and for the admin.
    #[test]
    fn an_old_compose_is_replaced_once() {
        use std::os::unix::fs::PermissionsExt;
        let d = tmpdir("compose");
        let old = "name: mikan\nservices:\n  panel:\n    volumes: [\"./data:/data\"]\n";
        fs::write(d.join("compose.yaml"), old).unwrap();
        assert_eq!(ensure_compose(&d, false).unwrap().as_deref(), Some(old));
        assert_eq!(fs::read_to_string(d.join("compose.yaml")).unwrap(), PANEL_COMPOSE);
        assert_eq!(fs::read_to_string(d.join("compose.yaml.old")).unwrap(), old);
        assert_eq!(fs::metadata(d.join("compose.yaml")).unwrap().permissions().mode() & 0o777, 0o600);
        assert_eq!(ensure_compose(&d, false).unwrap(), None, "nothing to do the second time");
        // the rollback puts the old text back
        put_compose(&d, old).unwrap();
        assert_eq!(fs::read_to_string(d.join("compose.yaml")).unwrap(), old);
        // a node's compose is not a panel's
        assert!(ensure_compose(&d, true).unwrap().is_some());
        assert_eq!(fs::read_to_string(d.join("compose.yaml")).unwrap(), NODE_COMPOSE);
        fs::remove_dir_all(&d).unwrap();
    }

    #[test]
    fn a_silent_command_is_stopped_and_stdin_reaches_a_command() {
        let mut cmd = Command::new("sleep");
        cmd.arg("30");
        let started = std::time::Instant::now();
        let err = stream_with(cmd, None, Some(Duration::from_millis(300)), |_| {}).unwrap_err();
        assert!(format!("{err}").contains("printed nothing"), "{err}");
        assert!(started.elapsed() < Duration::from_secs(10));
        let mut cmd = Command::new("sh");
        cmd.args(["-c", "cat"]);
        let mut got = Vec::new();
        stream_with(cmd, Some(b"one\ntwo\n".to_vec()), Some(Duration::from_secs(10)), |l| got.push(l.to_owned())).unwrap();
        assert_eq!(got, ["one", "two"]);
    }

    #[test]
    fn compose_ps_formats() {
        let lines = "{\"Service\":\"node\",\"State\":\"running\",\"Health\":\"\",\"Status\":\"Up 2 hours\"}\n{\"Service\":\"panel\",\"State\":\"running\",\"Health\":\"healthy\",\"Status\":\"Up 2 hours (healthy)\"}\n";
        let s = parse_services(lines).unwrap();
        assert_eq!((s.len(), s[1].name.as_str(), s[1].status.as_str()), (2, "panel", "Up 2 hours (healthy)"));
        let array = "[{\"Service\":\"node\",\"State\":\"exited\",\"Status\":\"Exited (1)\"}]";
        assert_eq!(parse_services(array).unwrap()[0].state, "exited");
        assert!(parse_services("").unwrap().is_empty());
    }
}
