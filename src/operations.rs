use std::{
    env,
    fs::{self, File, OpenOptions},
    io::{self, Read, Write},
    path::{Path, PathBuf},
    process::{self, Stdio},
    sync::{
        Arc, Mutex, OnceLock,
        atomic::{AtomicBool, Ordering},
    },
    thread,
    time::{Duration, Instant},
};

use reqwest::{
    Url,
    blocking::{Client, Response},
    redirect::Policy,
};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use tokio::{
    net::TcpListener,
    sync::{Semaphore, watch},
};

use crate::{
    App, Failure, RoomState, Server, VERSION, blobs,
    config::{Command, Config, truthy},
    db, run, tray,
};

static LOG: OnceLock<Mutex<File>> = OnceLock::new();
static INSTALLING: AtomicBool = AtomicBool::new(false);
static RESTART: AtomicBool = AtomicBool::new(false);
const MAX_DOWNLOAD: u64 = 80 * 1024 * 1024;

pub fn log(message: std::fmt::Arguments<'_>) {
    let line = format!("{} {message}\n", crate::now_millis());
    if env::var("COLLAB_DETACHED").as_deref() != Ok("1") || LOG.get().is_none() {
        let _ = io::stderr().write_all(line.as_bytes());
    }
    if let Some(file) = LOG.get() {
        let _ = file.lock().unwrap().write_all(line.as_bytes());
    }
}

#[derive(Serialize, Deserialize)]
struct Control {
    pid: u32,
    url: String,
    token: String,
}

struct ControlFile {
    path: PathBuf,
    _lock: File,
    token: String,
}

impl Drop for ControlFile {
    fn drop(&mut self) {
        if read_control(&self.path).is_ok_and(|current| current.token == self.token) {
            let _ = fs::remove_file(&self.path);
        }
    }
}

fn read_control(path: &Path) -> Result<Control, Failure> {
    Ok(serde_json::from_slice(&fs::read(path)?)?)
}

fn local_client() -> Result<Client, Failure> {
    Ok(Client::builder()
        .danger_accept_invalid_certs(true)
        .no_proxy()
        .redirect(Policy::none())
        .timeout(Duration::from_secs(2))
        .build()?)
}

fn healthy(client: &Client, url: &str) -> bool {
    client
        .get(format!("{url}/health"))
        .send()
        .is_ok_and(|response| response.status().is_success())
}

fn claim(config: &Config, control: &Control, lock: File) -> Result<ControlFile, Failure> {
    let path = config.data.join("server.json");
    let mut options = OpenOptions::new();
    options.write(true).create(true).truncate(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o600);
    }
    let mut file = options.open(&path)?;
    serde_json::to_writer(&mut file, control)?;
    file.sync_all()?;
    Ok(ControlFile {
        path,
        _lock: lock,
        token: control.token.clone(),
    })
}

fn stop(config: &Config) -> Result<(), Failure> {
    let path = config.data.join("server.json");
    let control = read_control(&path)?;
    let url = Url::parse(&control.url)?;
    if url.scheme() != "http" && url.scheme() != "https" {
        return Err("invalid control URL".into());
    }
    if !url.host_str().is_some_and(|host| {
        host.eq_ignore_ascii_case("localhost")
            || host
                .trim_matches(['[', ']'])
                .parse::<std::net::IpAddr>()
                .is_ok_and(|ip| ip.is_loopback())
    }) {
        return Err("the control endpoint must listen on loopback".into());
    }
    let client = local_client()?;
    client
        .post(format!("{}/control/shutdown", control.url))
        .header("X-Collab-Token", &control.token)
        .send()?
        .error_for_status()?;
    let deadline = Instant::now() + Duration::from_secs(10);
    while read_control(&path).is_ok_and(|current| current.token == control.token) {
        if Instant::now() >= deadline {
            return Err("server shutdown timed out".into());
        }
        thread::sleep(Duration::from_millis(100));
    }
    println!("Server stopped (PID {}).", control.pid);
    Ok(())
}

fn detach(config: &Config) -> Result<(), Failure> {
    fs::create_dir_all(&config.data)?;
    let path = config.log.as_ref().expect("detach log path");
    if let Some(parent) = path.parent().filter(|path| !path.as_os_str().is_empty()) {
        fs::create_dir_all(parent)?;
    }
    if path
        .metadata()
        .is_ok_and(|info| info.len() > 10 * 1024 * 1024)
    {
        let rotated = path.with_extension("log.old");
        if rotated.exists() {
            fs::remove_file(&rotated)?;
        }
        fs::rename(path, rotated)?;
    }
    let log = OpenOptions::new().create(true).append(true).open(path)?;
    let mut command = process::Command::new(env::current_exe()?);
    command
        .args(env::args_os().skip(1))
        .env("COLLAB_DETACHED", "1")
        .stdin(Stdio::null())
        .stdout(log.try_clone()?)
        .stderr(log);
    detach_process(&mut command);
    let mut child = command.spawn()?;
    let client = local_client()?;
    let deadline = Instant::now() + Duration::from_secs(15);
    loop {
        if let Some(status) = child.try_wait()? {
            return Err(format!(
                "detached server exited with {status}; see {}",
                path.display()
            )
            .into());
        }
        if let Ok(control) = read_control(&config.data.join("server.json")) {
            if control.pid == child.id() && healthy(&client, &control.url) {
                println!(
                    "Server started (PID {}). Log: {}",
                    child.id(),
                    path.display()
                );
                return Ok(());
            }
        }
        if Instant::now() >= deadline {
            child.kill()?;
            let _ = child.wait();
            return Err(format!("server startup timed out; see {}", path.display()).into());
        }
        thread::sleep(Duration::from_millis(100));
    }
}

#[cfg(windows)]
fn detach_process(command: &mut process::Command) {
    use std::os::windows::process::CommandExt;
    command.creation_flags(0x00000008 | 0x00000200);
}

#[cfg(unix)]
fn detach_process(command: &mut process::Command) {
    use std::os::unix::process::CommandExt;
    command.process_group(0);
}

pub async fn signal() {
    #[cfg(unix)]
    {
        use tokio::signal::unix::{SignalKind, signal};
        let mut term = signal(SignalKind::terminate()).expect("SIGTERM handler");
        let mut hup = signal(SignalKind::hangup()).expect("SIGHUP handler");
        tokio::select! { _ = tokio::signal::ctrl_c() => {}, _ = term.recv() => {}, _ = hup.recv() => {} }
    }
    #[cfg(windows)]
    {
        use tokio::signal::windows;
        let mut close = windows::ctrl_close().expect("console close handler");
        let mut brk = windows::ctrl_break().expect("console break handler");
        tokio::select! { _ = tokio::signal::ctrl_c() => {}, _ = close.recv() => {}, _ = brk.recv() => {} }
    }
}

pub fn entry(config: Config) -> Result<(), Failure> {
    match config.command {
        Command::Health => {
            let url = read_control(&config.data.join("server.json"))
                .ok()
                .map(|control| control.url)
                .unwrap_or_else(|| config.local_url(None));
            if healthy(&local_client()?, &url) {
                Ok(())
            } else {
                Err("server is not healthy".into())
            }
        }
        Command::Stop => stop(&config),
        Command::Detach => detach(&config),
        Command::CheckUpdate | Command::Update => {
            let release = fetch_release()?;
            if config.command == Command::Update && release.available {
                install(&release)?;
                println!(
                    "Installed {}. Restart the server to run it.",
                    release.version
                );
            } else if release.available {
                println!("Update {} available: {}", release.version, release.page);
            } else {
                println!("No newer Rust release available ({VERSION}).");
            }
            Ok(())
        }
        Command::Run => serve(config),
    }
}

fn serve(config: Config) -> Result<(), Failure> {
    fs::create_dir_all(&config.data)?;
    if let Some(path) = &config.log {
        if let Some(parent) = path.parent().filter(|path| !path.as_os_str().is_empty()) {
            fs::create_dir_all(parent)?;
        }
        let _ = LOG.set(Mutex::new(
            OpenOptions::new().create(true).append(true).open(path)?,
        ));
    }
    let lock = File::options()
        .read(true)
        .write(true)
        .create(true)
        .truncate(false)
        .open(config.data.join(".server.lock"))?;
    lock.try_lock()
        .map_err(|err| format!("data directory is already in use: {err}"))?;
    if let Ok(control) = read_control(&config.data.join("server.json")) {
        if healthy(&local_client()?, &control.url) {
            return Err(format!(
                "another server (PID {}) already uses this data directory",
                control.pid
            )
            .into());
        }
    }
    let runtime = tokio::runtime::Builder::new_multi_thread()
        .enable_all()
        .build()?;
    let listener = runtime.block_on(TcpListener::bind(&config.addr))?;
    let port = listener.local_addr()?.port();
    let control = Control {
        pid: process::id(),
        url: config.local_url(Some(port)),
        token: format!(
            "{}{}",
            uuid::Uuid::new_v4().simple(),
            uuid::Uuid::new_v4().simple()
        ),
    };
    let guard = claim(&config, &control, lock)?;
    let database = db::open(&config.data)?;
    let (stop, _) = watch::channel(false);
    let app = Arc::new(Server {
        config,
        state: Mutex::new(RoomState {
            db: database,
            clients: Default::default(),
        }),
        connections: Arc::new(Semaphore::new(256)),
        stop_token: control.token,
        stop,
        update: Mutex::new(None),
        plugin: Plugin::load(),
    });
    if let Err(err) = blobs::collect(&app, &app.state.lock().unwrap().db) {
        log(format_args!("blob cleanup: {err}"));
    }
    log(format_args!(
        "{}",
        translate("server.started", &[VERSION, &crate::PROTOCOL.to_string()])
    ));
    log(format_args!("Listening on {}", control.url));
    if !app.config.private {
        log(format_args!("{}", translate("server.noPassword", &[])));
    } else if app.config.tls.is_none() {
        log(format_args!("{}", translate("server.noTLS", &[])));
    }
    let tray = tray::start(app.clone(), port);
    let updater = start_update_thread(app.clone());
    let result = runtime.block_on(run(app.clone(), listener));
    app.stop.send_replace(true);
    drop(tray);
    if let Some(updater) = updater {
        updater.thread().unpark();
    }
    runtime.shutdown_timeout(Duration::from_secs(5));
    drop(app);
    drop(guard);
    log(format_args!("{}", translate("server.stopped", &[])));
    result?;
    if RESTART.load(Ordering::Acquire) {
        let mut command = process::Command::new(env::current_exe()?);
        command.args(env::args_os().skip(1));
        if env::var("COLLAB_DETACHED").as_deref() == Ok("1") {
            detach_process(&mut command);
        }
        command.spawn()?;
    }
    Ok(())
}

#[derive(Default, Deserialize)]
pub struct Plugin {
    #[serde(rename = "VersionName", default)]
    pub version: String,
    #[serde(rename = "MarketplaceURL", default)]
    pub marketplace: String,
}

impl Plugin {
    fn load() -> Self {
        if let (Some(version), Some(marketplace)) = (
            option_env!("COLLAB_PLUGIN_VERSION"),
            option_env!("COLLAB_PLUGIN_MARKETPLACE"),
        ) {
            return Self {
                version: version.into(),
                marketplace: marketplace.into(),
            };
        }
        let executable = env::current_exe().ok();
        let executable_dir = executable.as_deref().and_then(Path::parent);
        let files = [
            PathBuf::from("EditorCollaboration.uplugin"),
            PathBuf::from("../EditorCollaboration.uplugin"),
            executable_dir.unwrap_or(Path::new(".")).join("plugin.json"),
        ];
        files
            .into_iter()
            .filter_map(|path| fs::read(path).ok())
            .find_map(|bytes| serde_json::from_slice(&bytes).ok())
            .unwrap_or_default()
    }
}

#[derive(Clone)]
pub struct Release {
    pub version: String,
    pub page: String,
    pub available: bool,
    asset: Option<Url>,
    checksum: Option<Url>,
}

#[derive(Deserialize)]
struct GitHubRelease {
    tag_name: String,
    html_url: String,
    assets: Vec<GitHubAsset>,
}

#[derive(Deserialize)]
struct GitHubAsset {
    name: String,
    browser_download_url: String,
}

fn release_client() -> Result<Client, Failure> {
    Ok(Client::builder()
        .user_agent(format!("EditorCollaboration-Rust/{VERSION}"))
        .timeout(Duration::from_secs(30))
        .redirect(Policy::custom(|redirect| {
            if redirect.previous().len() >= 5 || !allowed_download(redirect.url()) {
                redirect.error("invalid release redirect")
            } else {
                redirect.follow()
            }
        }))
        .build()?)
}

fn allowed_download(url: &Url) -> bool {
    url.scheme() == "https"
        && url.username().is_empty()
        && url.password().is_none()
        && url.host_str().is_some_and(|host| {
            matches!(
                host,
                "github.com" | "api.github.com" | "codeload.github.com"
            ) || host.ends_with(".githubusercontent.com")
                || host.ends_with(".github.com")
        })
}

fn version_parts(raw: &str) -> Option<[u64; 3]> {
    let raw = raw
        .trim()
        .trim_start_matches(['v', 'V'])
        .split(['-', '+'])
        .next()?;
    let mut version = [0; 3];
    for (index, part) in raw.split('.').enumerate() {
        *version.get_mut(index)? = part.parse().ok()?;
    }
    Some(version)
}

fn asset_name() -> String {
    let os = match env::consts::OS {
        "macos" => "darwin",
        other => other,
    };
    let arch = match env::consts::ARCH {
        "x86_64" => "amd64",
        "aarch64" => "arm64",
        other => other,
    };
    let ext = if cfg!(windows) { ".exe" } else { "" };
    format!("collab-server-rust-{os}-{arch}{ext}")
}

fn fetch_release() -> Result<Release, Failure> {
    let repo = env::var("COLLAB_GITHUB_REPO")
        .ok()
        .filter(|repo| !repo.trim().is_empty())
        .unwrap_or_else(|| "400Interactive/EditorCollaboration_Server".into());
    let Some((owner, name)) = repo.trim().split_once('/') else {
        return Err("invalid GitHub repository".into());
    };
    if [owner, name].iter().any(|part| {
        part.is_empty()
            || !part
                .bytes()
                .all(|b| b.is_ascii_alphanumeric() || matches!(b, b'-' | b'_' | b'.'))
    }) {
        return Err("invalid GitHub repository".into());
    }
    let response = release_client()?
        .get(format!(
            "https://api.github.com/repos/{owner}/{name}/releases/latest"
        ))
        .send()?
        .error_for_status()?;
    let payload: GitHubRelease = serde_json::from_reader(response.take(1024 * 1024))?;
    let find = |name: &str| {
        payload
            .assets
            .iter()
            .find(|asset| asset.name == name)
            .and_then(|asset| Url::parse(&asset.browser_download_url).ok())
            .filter(allowed_download)
    };
    let asset = find(&asset_name());
    let checksum = find("SHA256SUMS");
    let newer = version_parts(&payload.tag_name)
        .is_some_and(|version| Some(version) > version_parts(VERSION));
    Ok(Release {
        version: payload.tag_name,
        page: payload.html_url,
        available: newer && asset.is_some() && checksum.is_some(),
        asset,
        checksum,
    })
}

fn download(client: &Client, url: &Url) -> Result<Response, Failure> {
    if !allowed_download(url) {
        return Err("invalid release download URL".into());
    }
    Ok(client.get(url.clone()).send()?.error_for_status()?)
}

fn install(release: &Release) -> Result<(), Failure> {
    if in_docker() {
        return Err("rebuild the Docker image to update".into());
    }
    let client = release_client()?;
    let asset = release
        .asset
        .as_ref()
        .ok_or("release has no Rust executable")?;
    let checksum = release
        .checksum
        .as_ref()
        .ok_or("release has no SHA256SUMS")?;
    let mut sums = String::new();
    download(&client, checksum)?
        .take(1024 * 1024)
        .read_to_string(&mut sums)?;
    let name = asset_name();
    let expected = sums
        .lines()
        .find_map(|line| {
            let mut fields = line.split_whitespace();
            let digest = fields.next()?;
            let file = fields.next()?.trim_start_matches('*');
            (file == name && digest.len() == 64 && digest.bytes().all(|b| b.is_ascii_hexdigit()))
                .then(|| digest.to_ascii_lowercase())
        })
        .ok_or("no checksum for the Rust executable")?;
    let destination = env::current_exe()?.canonicalize()?;
    let mut temp = tempfile::NamedTempFile::new_in(
        destination.parent().ok_or("missing executable directory")?,
    )?;
    if io::copy(
        &mut download(&client, asset)?.take(MAX_DOWNLOAD + 1),
        &mut temp,
    )? > MAX_DOWNLOAD
    {
        return Err("release executable exceeds size limit".into());
    }
    temp.flush()?;
    let mut hash = Sha256::new();
    io::copy(&mut temp.reopen()?, &mut hash)?;
    if hex::encode(hash.finalize()) != expected {
        return Err("SHA256 checksum does not match".into());
    }
    temp.as_file()
        .set_permissions(fs::metadata(&destination)?.permissions())?;
    temp.as_file().sync_all()?;
    #[cfg(windows)]
    {
        let previous = destination.with_extension("previous.exe");
        if previous.exists() {
            fs::remove_file(&previous)?;
        }
        fs::rename(&destination, &previous)?;
        if let Err(err) = temp.persist(&destination) {
            fs::rename(&previous, &destination)?;
            return Err(err.into());
        }
    }
    #[cfg(not(windows))]
    temp.persist(&destination)?;
    Ok(())
}

fn in_docker() -> bool {
    truthy(&env::var("COLLAB_DOCKER").unwrap_or_default()) || Path::new("/.dockerenv").exists()
}

pub fn install_offered(app: App) {
    if INSTALLING.swap(true, Ordering::AcqRel) {
        return;
    }
    thread::spawn(move || {
        let release = app.update.lock().unwrap().clone();
        if let Some(release) = release.filter(|release| release.available) {
            match install(&release) {
                Ok(()) => {
                    RESTART.store(true, Ordering::Release);
                    app.stop.send_replace(true);
                }
                Err(err) => log(format_args!("update failed: {err}")),
            }
        }
        INSTALLING.store(false, Ordering::Release);
    });
}

fn update_interval() -> Duration {
    let raw = env::var("COLLAB_UPDATE_INTERVAL").unwrap_or_default();
    let mut rest = raw.trim();
    let mut seconds = 0.0;
    while !rest.is_empty() {
        let Some(end) = rest.find(|ch: char| !(ch.is_ascii_digit() || ch == '.')) else {
            break;
        };
        let Ok(amount) = rest[..end].parse::<f64>() else {
            break;
        };
        rest = &rest[end..];
        let Some((unit, multiplier)) = [
            ("ms", 0.001),
            ("us", 0.000001),
            ("ns", 0.000000001),
            ("h", 3600.0),
            ("m", 60.0),
            ("s", 1.0),
        ]
        .into_iter()
        .find(|(unit, _)| rest.starts_with(unit)) else {
            break;
        };
        seconds += amount * multiplier;
        rest = &rest[unit.len()..];
    }
    if rest.is_empty() && seconds >= 60.0 {
        Duration::try_from_secs_f64(seconds).unwrap_or(Duration::from_secs(86400))
    } else {
        Duration::from_secs(86400)
    }
}

fn start_update_thread(app: App) -> Option<thread::JoinHandle<()>> {
    if matches!(
        env::var("COLLAB_UPDATE_CHECK")
            .unwrap_or_default()
            .trim()
            .to_ascii_lowercase()
            .as_str(),
        "0" | "false" | "no" | "off"
    ) {
        return None;
    }
    Some(thread::spawn(move || {
        let interval = update_interval();
        let mut announced = String::new();
        while !*app.stop.borrow() {
            match fetch_release() {
                Ok(release) => {
                    let available = release.available && release.version != announced;
                    if available {
                        announced.clone_from(&release.version);
                        log(format_args!(
                            "{}",
                            translate(
                                if in_docker() {
                                    "update.docker"
                                } else {
                                    "update.available"
                                },
                                &[&release.version]
                            )
                        ));
                    }
                    *app.update.lock().unwrap() = Some(release);
                    if available && app.config.auto_update && !in_docker() {
                        install_offered(app.clone());
                    }
                }
                Err(err) => log(format_args!("update check failed: {err}")),
            }
            if !*app.stop.borrow() {
                thread::park_timeout(interval);
            }
        }
    }))
}

pub fn translate(key: &str, args: &[&str]) -> String {
    static TRANSLATIONS: OnceLock<(
        std::collections::HashMap<String, String>,
        std::collections::HashMap<String, String>,
    )> = OnceLock::new();
    let (selected, default) = TRANSLATIONS.get_or_init(|| {
        let language = env::var("COLLAB_LANG")
            .ok()
            .filter(|lang| !lang.is_empty())
            .or_else(|| env::var("LC_ALL").ok().filter(|lang| !lang.is_empty()))
            .or_else(|| env::var("LANG").ok().filter(|lang| !lang.is_empty()))
            .unwrap_or_else(tray::language);
        let language = language
            .split(['.', '@'])
            .next()
            .unwrap_or_default()
            .replace('_', "-")
            .to_ascii_lowercase();
        let short = language.split('-').next().unwrap_or_default();
        let selected = match (language.as_str(), short) {
            ("pt-br", _) | (_, "pt") => include_str!("../lang/pt-BR.json"),
            ("es-419", _) | ("es-mx", _) => include_str!("../lang/es-419.json"),
            (_, "ar") => include_str!("../lang/ar.json"),
            (_, "de") => include_str!("../lang/de.json"),
            (_, "es") => include_str!("../lang/es.json"),
            (_, "fr") => include_str!("../lang/fr.json"),
            (_, "it") => include_str!("../lang/it.json"),
            (_, "ja") => include_str!("../lang/ja.json"),
            (_, "ko") => include_str!("../lang/ko.json"),
            (_, "pl") => include_str!("../lang/pl.json"),
            (_, "ru") => include_str!("../lang/ru.json"),
            (_, "tr") => include_str!("../lang/tr.json"),
            (_, "zh") => include_str!("../lang/zh-Hans.json"),
            _ => "{}",
        };
        (
            serde_json::from_str(selected).unwrap_or_default(),
            serde_json::from_str(include_str!("../lang/en.json")).expect("English translations"),
        )
    });
    let template = selected
        .get(key)
        .or_else(|| default.get(key))
        .map(String::as_str)
        .unwrap_or(key);
    let mut text = String::new();
    let mut args = args.iter();
    let mut chars = template.chars().peekable();
    while let Some(ch) = chars.next() {
        if ch == '%'
            && chars
                .peek()
                .is_some_and(|ch| matches!(ch, 's' | 'd' | 'v' | 'q'))
        {
            chars.next();
            text.push_str(args.next().copied().unwrap_or_default());
        } else {
            text.push(ch);
        }
    }
    text
}
