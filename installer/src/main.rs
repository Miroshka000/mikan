//! mikan: installs the mikan VPN panel, or a node of one, and manages it from the
//! server's shell. Without a command it opens the installer on a fresh server and the
//! menu on an installed one; every menu action is a command too.

mod acme;
mod addon;
mod backup;
mod clock;
mod docker;
mod doctor;
mod envfile;
mod hello;
mod host;
mod lock;
mod net;
mod ops;
mod panelfs;
mod release;
mod setup;
mod signals;
mod sites;
mod system;
mod tui;
mod update;

use std::io::Write;
use std::net::IpAddr;
use std::path::PathBuf;
use std::process::ExitCode;

use clap::{Parser, Subcommand};

/// Where mikan lives on the server.
pub const DIR: &str = "/opt/mikan";

/// This installer's version: the release tag it was built from, "dev" otherwise.
pub fn version() -> &'static str {
    option_env!("MIKAN_VERSION").map(|v| v.trim_start_matches('v')).unwrap_or("dev")
}

#[derive(Parser)]
#[command(
    name = "mikan",
    version = version(),
    about = "mikan VPN panel: the installer and the server's menu",
    after_help = "Without a command: the installer on a fresh server, the menu on an installed one."
)]
struct Cli {
    #[command(subcommand)]
    cmd: Option<Cmd>,
}

#[derive(Subcommand)]
enum Cmd {
    /// Install the panel, or with --join a node of another panel
    Install(setup::Options),
    /// Containers, version, health and whether an update is out
    Status,
    /// Check this server: Docker, containers, ports, firewall, clock, DNS, access to GitHub and GHCR, disk and memory
    Doctor,
    /// Follow the logs (Ctrl+C stops)
    Logs {
        #[arg(value_parser = ["panel", "node"])]
        service: Option<String>,
    },
    /// Print the panel's link
    Url,
    /// Set a new admin password; all sessions end
    ResetPassword,
    /// Give the panel a new secret link
    ResetPath,
    /// Turn off the admin's 2FA (a lost phone)
    #[command(name = "disable-2fa")]
    Disable2fa,
    /// The panel's nodes: list, add, key, set
    #[command(disable_help_flag = true)]
    Node {
        #[arg(trailing_var_arg = true, allow_hyphen_values = true)]
        args: Vec<String>,
    },
    /// Inbounds: list, add, set; a new port is opened in ufw
    #[command(disable_help_flag = true)]
    Inbound {
        #[arg(trailing_var_arg = true, allow_hyphen_values = true)]
        args: Vec<String>,
    },
    /// Your own TLS certificate for the panel or a node (certbot, acme.sh, Caddy…)
    #[command(subcommand)]
    Cert(CertCmd),
    /// REALITY camouflage sites: scan, check, apply
    #[command(disable_help_flag = true)]
    Targets {
        #[arg(trailing_var_arg = true, allow_hyphen_values = true)]
        args: Vec<String>,
    },
    /// Back up the database, certificates and settings to /opt/mikan/backups
    Backup,
    /// Replace the data with a backup's
    Restore {
        file: PathBuf,
        /// Do not ask
        #[arg(long, short = 'y')]
        yes: bool,
        /// Save only the files before replacing them, not the current database: for a
        /// PostgreSQL too broken to dump. Its current data are lost if the restore fails
        #[arg(long)]
        no_db_snapshot: bool,
    },
    /// Update to the latest release, backing up and migrating the database safely
    Update(update::UpdateArgs),
    /// Payment adapters of the marketplace: list, install, remove
    #[command(subcommand)]
    Addon(AddonCmd),
    /// Node: take a new join key from the panel's Nodes page (asked for when not given;
    /// MIKAN_JOIN_KEY works too: the key holds the node's private key, keep it out of ps)
    Join {
        key: Option<String>,
        /// Open the node's API port in ufw for this address only, the panel's
        #[arg(long, value_name = "IP")]
        panel_ip: Option<IpAddr>,
    },
    /// The files of this release: compose.yaml and the update units (`update` runs it, and
    /// the new command runs it after replacing the old one)
    #[command(hide = true)]
    PostUpdate,
    /// Restart the containers
    Restart,
    /// Stop mikan and remove the command; the data stays in /opt/mikan
    Uninstall {
        /// Do not ask
        #[arg(long, short = 'y')]
        yes: bool,
    },
}

#[derive(Subcommand)]
enum AddonCmd {
    /// What runs here and what the marketplace offers
    List,
    /// Install an adapter, or update it to the marketplace's build
    Install { id: String },
    /// Stop and remove an adapter; the panel keeps its settings and payments
    Remove { id: String },
    /// Do what the panel asked for (the update request unit runs this)
    Apply,
}

#[derive(Subcommand)]
enum CertCmd {
    /// Install a certificate: the chain and its key. Fits a renewal hook:
    /// certbot ... --deploy-hook "mikan cert set --cert $RENEWED_LINEAGE/fullchain.pem --key $RENEWED_LINEAGE/privkey.pem"
    Set {
        /// The chain, leaf first (fullchain.pem)
        #[arg(long)]
        cert: PathBuf,
        /// The private key (privkey.pem)
        #[arg(long)]
        key: PathBuf,
        /// A node's own certificate instead of the panel's (mikan node list)
        #[arg(long)]
        node: Option<u32>,
    },
    /// Go back to Let's Encrypt (a node: to its self-signed certificate)
    Clear {
        #[arg(long)]
        node: Option<u32>,
    },
    /// What the own certificate is
    Show {
        #[arg(long)]
        node: Option<u32>,
    },
}

fn main() -> ExitCode {
    let cli = Cli::parse();
    let result = match cli.cmd {
        None => tui::start(),
        Some(Cmd::Install(o)) => setup::install(o),
        Some(Cmd::Status) => ops::status(),
        Some(Cmd::Doctor) => doctor::run(),
        Some(Cmd::Logs { service }) => ops::logs(service.as_deref()),
        Some(Cmd::Url) => ops::admin(&["url"]),
        Some(Cmd::ResetPassword) => ops::admin(&["reset-password"]),
        Some(Cmd::ResetPath) => ops::admin(&["reset-path"]),
        Some(Cmd::Disable2fa) => ops::admin(&["disable-2fa"]),
        Some(Cmd::Node { args }) => passthrough("node", &args),
        Some(Cmd::Targets { args }) => passthrough("targets", &args),
        Some(Cmd::Cert(c)) => match c {
            CertCmd::Set { cert, key, node } => ops::cert_set(&cert, &key, node),
            CertCmd::Clear { node } => ops::cert(&["clear"], node),
            CertCmd::Show { node } => ops::cert(&["show"], node),
        },
        Some(Cmd::Inbound { args }) => ops::inbound(&args),
        Some(Cmd::Backup) => backup::backup(&mut out).map(|f| out(&format!("Backup: {}", f.display()))),
        Some(Cmd::Restore { file, yes, no_db_snapshot }) => {
            let question = if no_db_snapshot {
                format!("Replace the current data with {}? The current database is NOT saved first.", file.display())
            } else {
                format!("Replace the current data with {}?", file.display())
            };
            if yes || ops::confirm(&question) {
                backup::restore(&file, no_db_snapshot, &mut out).map(|()| out(&format!("Restored from {}.", file.display())))
            } else {
                Ok(())
            }
        }
        Some(Cmd::Update(a)) => update::update(&a, &mut out, &mut progress_line()),
        Some(Cmd::Addon(c)) => addon_cmd(c),
        Some(Cmd::Join { key, panel_ip }) => ops::join_key(key).and_then(|k| ops::join(&k, panel_ip)),
        Some(Cmd::PostUpdate) => update::converge(&mut out),
        Some(Cmd::Restart) => ops::restart(),
        Some(Cmd::Uninstall { yes }) => {
            if yes || ops::confirm("Stop mikan and remove the mikan command? The data stays in /opt/mikan.") {
                ops::uninstall().map(|panel| {
                    if panel {
                        out(&format!(
                            "Done. The data and backups stay in {DIR}, the database in the Docker volume {}; remove them with: rm -rf {DIR} && docker volume rm {}",
                            docker::PG_VOLUME,
                            docker::PG_VOLUME
                        ))
                    } else {
                        out(&format!("Done. The data and backups stay in {DIR}; remove them with: rm -rf {DIR}"))
                    }
                })
            } else {
                Ok(())
            }
        }
    };
    match result {
        Ok(()) => ExitCode::SUCCESS,
        Err(e) => {
            let _ = writeln!(std::io::stderr(), "mikan: {e:#}");
            ExitCode::FAILURE
        }
    }
}

/// Prints a line and goes on when nobody reads it any more: an ssh session that dropped in
/// the middle of an update must not panic the update half way (println! would).
pub fn out(l: &str) {
    let _ = writeln!(std::io::stdout(), "{l}");
}

fn addon_cmd(c: AddonCmd) -> anyhow::Result<()> {
    let install = ops::Install::load()?;
    install.panel_only()?;
    let panel = install.version();
    let mut say = out;
    match c {
        AddonCmd::List => addon::list(&panel),
        AddonCmd::Install { id } => addon::install(&id, &panel, &mut say),
        AddonCmd::Remove { id } => addon::remove(&id, &mut say),
        AddonCmd::Apply => addon::apply(&panel, &mut say),
    }
}

fn passthrough(cmd: &str, args: &[String]) -> anyhow::Result<()> {
    let mut full = vec![cmd];
    full.extend(args.iter().map(String::as_str));
    ops::admin(&full)
}

/// Prints the pull's progress in quarters.
fn progress_line() -> impl FnMut(f64) {
    let mut last = -1;
    move |p| {
        let q = (p * 4.0) as i32;
        if q != last {
            last = q;
            out(&format!("  {}%", q * 25));
        }
    }
}
