//! signtools-portal: Apple ID login and developer-portal provisioning for SignTools.
//!
//! A thin CLI over Impactor's `plume_core` (Apple's GSA/SRP login with 2FA, anisette, and the Xcode
//! developer services API). SignTools' web service runs it as a subprocess; every command prints JSON.
//!
//!   login                       interactive over stdin/stdout, one JSON object per line:
//!                               in  {"email": "...", "password": "..."}
//!                               out {"event": "2fa", "method": "device"|"sms", "phones": [...]}
//!                               in  {"code": "123456"} or {"sms": <phone id>}
//!                               out {"event": "done", "email": "...", "teams": [...]} | {"event": "error", ...}
//!   accounts                    saved accounts
//!   logout  --email E
//!   teams   --email E           teams of an account (an Apple ID can be in several, e.g. individual + organization)
//!   cert    --email E --team T --out identity.pem [--allow-reset]
//!                               reuse or create an "Apple Development" certificate whose private key is kept
//!                               in the data dir (like Impactor); with --allow-reset, revoke an old certificate
//!                               when the team has no room for a new one (Impactor asks before doing that)
//!   provision --email E --team T --bundle-id B --name N --entitlements ents.plist --out profile.mobileprovision
//!                               ensure an explicit App ID, enable the capabilities the app's entitlements ask
//!                               for (e.g. Increased Memory Limit), download its team provisioning profile
//!
//! Errors: {"ok": false, "error": "...", "code": <Apple result code, if any>} and exit code 1.

use std::io::{BufRead, Write};
use std::path::{Path, PathBuf};

use anyhow::{Context, Result, anyhow};
use clap::{Parser, Subcommand};
use plume_core::auth::{Account, TwoFactorAction, TwoFactorMethod, TwoFactorRequest};
use plume_core::developer::DeveloperSession;
use plume_core::{AnisetteConfiguration, CertificateIdentity};
use plume_store::AccountStore;
use serde::Deserialize;
use serde_json::{Value, json};

/// Machine name of the certificates this tool creates (Impactor uses "AltStore").
const MACHINE_NAME: &str = "SignTools";

#[derive(Parser)]
#[command(name = "signtools-portal", version, about)]
struct Cli {
    /// Directory for saved accounts, certificate keys and anisette state
    #[arg(long, env = "PORTAL_DATA", default_value = "portal-data")]
    data: PathBuf,
    #[command(subcommand)]
    command: Command,
}

#[derive(Subcommand)]
enum Command {
    Login,
    Accounts,
    Logout {
        #[arg(long)]
        email: String,
    },
    Teams {
        #[arg(long)]
        email: String,
    },
    Cert {
        #[arg(long)]
        email: String,
        #[arg(long)]
        team: String,
        #[arg(long)]
        out: PathBuf,
        #[arg(long)]
        allow_reset: bool,
    },
    Provision {
        #[arg(long)]
        email: String,
        #[arg(long)]
        team: String,
        #[arg(long)]
        bundle_id: String,
        #[arg(long)]
        name: String,
        /// plist with the entitlements the app requests; matching capabilities get enabled
        #[arg(long)]
        entitlements: Option<PathBuf>,
        #[arg(long)]
        out: PathBuf,
    },
}

fn emit(value: Value) {
    let mut out = std::io::stdout().lock();
    let _ = writeln!(out, "{value}");
    let _ = out.flush();
}

fn read_json_line<T: for<'de> Deserialize<'de>>() -> Result<T> {
    let mut line = String::new();
    if std::io::stdin().lock().read_line(&mut line)? == 0 {
        return Err(anyhow!("stdin closed"));
    }
    serde_json::from_str(line.trim()).context("invalid JSON on stdin")
}

fn anisette(data: &Path) -> AnisetteConfiguration {
    AnisetteConfiguration::default().set_configuration_path(data.join("anisette"))
}

async fn store(data: &Path) -> Result<AccountStore> {
    Ok(AccountStore::load(&Some(data.join("accounts.json"))).await?)
}

async fn session(data: &Path, email: &str) -> Result<DeveloperSession> {
    let store = store(data).await?;
    let account = store
        .get_account(email)
        .ok_or_else(|| anyhow!("no saved account {email}, log in first"))?;
    DeveloperSession::new(account.adsid().clone(), account.xcode_gs_token().clone(), anisette(data))
        .await
        .map_err(|e| anyhow!(e).context("restore session (log in again if it expired)"))
}

async fn teams_json(session: &DeveloperSession) -> Result<Value> {
    let teams = session.qh_list_teams().await?.teams;
    Ok(Value::Array(
        teams
            .into_iter()
            .map(|t| json!({"id": t.team_id, "name": t.name, "type": t._type, "status": t.status}))
            .collect(),
    ))
}

#[derive(Deserialize)]
struct Credentials {
    email: String,
    password: String,
}

#[derive(Deserialize)]
struct TwoFactorReply {
    code: Option<String>,
    sms: Option<u32>,
}

async fn login(data: &Path) -> Result<Value> {
    let creds: Credentials = read_json_line()?;
    let (email, password) = (creds.email.trim().to_string(), creds.password);

    let tfa = |req: TwoFactorRequest| -> std::result::Result<TwoFactorAction, String> {
        emit(json!({
            "event": "2fa",
            "method": match req.method { TwoFactorMethod::Device => "device", TwoFactorMethod::Sms => "sms" },
            "phones": req.trusted_phone_numbers.iter()
                .map(|p| json!({"id": p.id, "last_two_digits": p.last_two_digits}))
                .collect::<Vec<_>>(),
        }));
        let reply: TwoFactorReply = read_json_line().map_err(|e| e.to_string())?;
        if let Some(id) = reply.sms {
            return Ok(TwoFactorAction::SendSms(id));
        }
        Ok(TwoFactorAction::SubmitCode(reply.code.unwrap_or_default().trim().to_string()))
    };
    let credentials = || -> std::result::Result<(String, String), String> { Ok((email.clone(), password.clone())) };

    let account = Account::login(credentials, tfa, anisette(data)).await?;
    let mut store = store(data).await?;
    store.accounts_add_from_session(email.clone(), account).await?;

    let session = session(data, &email).await?;
    Ok(json!({"event": "done", "ok": true, "email": email, "teams": teams_json(&session).await?}))
}

async fn cert(data: &Path, email: &str, team: &String, out: &Path, allow_reset: bool) -> Result<Value> {
    let session = session(data, email).await?;
    let mut reset_requested = false;
    let mut on_reset = || {
        reset_requested = true;
        allow_reset
    };
    let identity = CertificateIdentity::new_with_session(
        &session,
        data.to_path_buf(),
        Some(MACHINE_NAME.to_string()),
        team,
        true,
        Some(&mut on_reset),
    )
    .await
    .map_err(|e| {
        if reset_requested && !allow_reset {
            anyhow!(
                "the team has no room for another development certificate; allow revoking an old one \
                 (\"allow certificate reset\") or revoke one yourself in the developer portal"
            )
        } else {
            anyhow!(e)
        }
    })?;
    let cert = identity.cert.as_ref().ok_or_else(|| anyhow!("no certificate returned"))?;
    let cert_pem = cert.encode_pem();
    // plume_core keeps the private key at <data>/keys/<team>/key.pem
    let key_pem = std::fs::read_to_string(data.join("keys").join(team).join("key.pem")).context("read private key")?;
    std::fs::write(out, format!("{}\n{}", key_pem.trim_end(), cert_pem))?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(out, std::fs::Permissions::from_mode(0o600))?;
    }
    Ok(json!({"ok": true, "new": identity.new, "serial": identity.serial_number}))
}

async fn provision(
    data: &Path,
    email: &str,
    team: &String,
    bundle_id: &String,
    name: &String,
    entitlements: Option<&Path>,
    out: &Path,
) -> Result<Value> {
    let session = session(data, email).await?;
    let app_id = session.qh_ensure_app_id(team, name, bundle_id).await?;

    let mut warnings = Vec::new();
    if let Some(path) = entitlements {
        let ents: plist::Dictionary = plist::from_file(path).context("read entitlements")?;
        if !ents.is_empty() {
            if let Err(e) = session.v1_request_capabilities_for_entitlements(team, bundle_id, &ents).await {
                // keep going: the profile simply won't grant what could not be enabled
                warnings.push(format!("enabling capabilities failed: {e}"));
            }
        }
    }

    let profile = session.qh_get_profile(team, &app_id.app_id_id).await?;
    let bytes: Vec<u8> = profile.provisioning_profile.encoded_profile.into();
    std::fs::write(out, bytes)?;
    Ok(json!({"ok": true, "app_id_id": app_id.app_id_id, "identifier": app_id.identifier, "warnings": warnings}))
}

async fn run(cli: Cli) -> Result<Value> {
    std::fs::create_dir_all(&cli.data)?;
    let data = cli.data.as_path();
    match cli.command {
        Command::Login => login(data).await,
        Command::Accounts => {
            let store = store(data).await?;
            let accounts: Vec<Value> = store
                .accounts()
                .values()
                .map(|a| json!({"email": a.email(), "first_name": a.first_name()}))
                .collect();
            Ok(json!({"ok": true, "accounts": accounts}))
        }
        Command::Logout { email } => {
            let mut store = store(data).await?;
            store.accounts_remove(&email).await?;
            Ok(json!({"ok": true}))
        }
        Command::Teams { email } => {
            let session = session(data, &email).await?;
            Ok(json!({"ok": true, "teams": teams_json(&session).await?}))
        }
        Command::Cert { email, team, out, allow_reset } => cert(data, &email, &team, &out, allow_reset).await,
        Command::Provision { email, team, bundle_id, name, entitlements, out } => {
            provision(data, &email, &team, &bundle_id, &name, entitlements.as_deref(), &out).await
        }
    }
}

fn error_json(err: &anyhow::Error) -> Value {
    let code = err.chain().find_map(|e| match e.downcast_ref::<plume_core::Error>() {
        Some(plume_core::Error::DeveloperApi { result_code, .. }) => Some(*result_code),
        _ => None,
    });
    json!({"ok": false, "event": "error", "error": format!("{err:#}"), "code": code})
}

#[tokio::main]
async fn main() {
    // logs go to stderr, JSON results to stdout
    env_logger::Builder::from_env(env_logger::Env::default().default_filter_or("warn"))
        .target(env_logger::Target::Stderr)
        .init();
    let _ = rustls::crypto::ring::default_provider().install_default();
    let cli = Cli::parse();
    match run(cli).await {
        Ok(v) => emit(v),
        Err(e) => {
            emit(error_json(&e));
            std::process::exit(1);
        }
    }
}
