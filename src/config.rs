use std::{
    collections::{HashMap, HashSet},
    env, fs,
    net::IpAddr,
    path::PathBuf,
};

use sha2::{Digest, Sha256};
use subtle::ConstantTimeEq;

use crate::Failure;

#[derive(PartialEq, Eq)]
pub enum Command {
    Run,
    Health,
    Detach,
    Stop,
    CheckUpdate,
    Update,
}

pub struct Config {
    pub addr: String,
    pub data: PathBuf,
    pub log: Option<PathBuf>,
    pub tls: Option<(PathBuf, PathBuf)>,
    pub max_room_clients: usize,
    pub no_tray: bool,
    pub auto_update: bool,
    pub command: Command,
    pub private: bool,
    password: [u8; 32],
    room: String,
    passwords: HashMap<String, [u8; 32]>,
}

fn option(name: &str) -> String {
    env::var(name).unwrap_or_default()
}

pub fn truthy(value: &str) -> bool {
    matches!(
        value.trim().to_ascii_lowercase().as_str(),
        "1" | "true" | "yes" | "on"
    )
}

fn host_port(host: &str, port: &str) -> String {
    if host.contains(':') && !host.starts_with('[') {
        format!("[{host}]:{port}")
    } else {
        format!("{host}:{port}")
    }
}

impl Config {
    pub fn parse() -> Result<Self, Failure> {
        let port = option("COLLAB_PORT");
        let mut addr = host_port(
            option("COLLAB_HOST").trim(),
            if port.trim().is_empty() {
                "7878"
            } else {
                port.trim()
            },
        );
        let mut room = option("COLLAB_ROOM");
        let mut password = option("COLLAB_PASSWORD");
        let mut room_file = option("COLLAB_ROOM_PASSWORDS_FILE");
        let mut cert = option("COLLAB_TLS_CERT");
        let mut key = option("COLLAB_TLS_KEY");
        let mut data = option("COLLAB_DATA_DIR");
        if data.trim().is_empty() {
            data = "data".into();
        }
        let mut log = option("COLLAB_LOG_FILE");
        let mut max_room_clients = option("COLLAB_MAX_ROOM_CLIENTS")
            .parse::<usize>()
            .ok()
            .filter(|count| *count > 0)
            .unwrap_or(64);
        let mut no_tray = truthy(&option("COLLAB_NO_TRAY"));
        let mut auto_update = truthy(&option("COLLAB_AUTO_UPDATE"));
        let mut commands = Vec::new();
        let mut args = env::args().skip(1);
        while let Some(arg) = args.next() {
            let (flag, explicit) = arg
                .split_once('=')
                .map_or((arg.as_str(), None), |(a, b)| (a, Some(b)));
            let mut value = || {
                explicit
                    .map(str::to_owned)
                    .or_else(|| args.next())
                    .ok_or_else(|| format!("missing value for {flag}"))
            };
            let boolean = || explicit.is_none_or(truthy);
            match flag {
                "-addr" => addr = value()?,
                "-room" => room = value()?,
                "-password" => password = value()?,
                "-room-passwords" => room_file = value()?,
                "-tls-cert" => cert = value()?,
                "-tls-key" => key = value()?,
                "-data" => data = value()?,
                "-log" => log = value()?,
                "-max-room-clients" => max_room_clients = value()?.parse::<usize>()?.max(1),
                "-no-tray" => no_tray = boolean(),
                "-auto-update" => auto_update = boolean(),
                "-healthcheck" if boolean() => commands.push((2, Command::Health)),
                "-detach" if boolean() && option("COLLAB_DETACHED") != "1" => {
                    commands.push((4, Command::Detach))
                }
                "-stop" if boolean() => commands.push((3, Command::Stop)),
                "-check-update" if boolean() => commands.push((0, Command::CheckUpdate)),
                "-update" if boolean() => commands.push((1, Command::Update)),
                "-healthcheck" | "-detach" | "-stop" | "-check-update" | "-update" => {}
                "-h" | "--help" | "-help" => {
                    println!(
                        "Editor Collaboration server
  -addr HOST:PORT  -room NAME  -password PASSWORD  -room-passwords FILE
  -tls-cert FILE  -tls-key FILE  -max-room-clients N  -data DIR  -log FILE
  -no-tray  -auto-update  -detach  -stop  -healthcheck  -check-update  -update"
                    );
                    std::process::exit(0);
                }
                _ => return Err(format!("unknown argument {flag}").into()),
            }
        }
        if cert.is_empty() != key.is_empty() {
            return Err("both -tls-cert and -tls-key are required".into());
        }
        if addr.starts_with(':') {
            addr.insert_str(0, "0.0.0.0");
        }
        let command = commands
            .into_iter()
            .min_by_key(|(priority, _)| *priority)
            .map_or(Command::Run, |(_, command)| command);
        let passwords = if matches!(command, Command::Run | Command::Detach) {
            room_passwords(&room_file)?
        } else {
            HashMap::new()
        };
        let private = !password.is_empty() || !passwords.is_empty();
        if matches!(command, Command::Run | Command::Detach) {
            if room != room.trim() || room.chars().count() > 128 {
                return Err("Invalid COLLAB_ROOM or -room.".into());
            }
            if passwords.is_empty() && !password.is_empty() && room.is_empty() {
                return Err("Set COLLAB_ROOM (or -room) for a shared password, or use a room-passwords file.".into());
            }
            if !private && !loopback(host(&addr)) {
                return Err(
                    "Set a password or listen only on loopback (COLLAB_HOST=127.0.0.1).".into(),
                );
            }
        }
        let log = if !log.is_empty() {
            Some(log.into())
        } else if command == Command::Detach || option("COLLAB_DETACHED") == "1" {
            Some(PathBuf::from(&data).join("server.log"))
        } else {
            None
        };
        Ok(Self {
            addr,
            data: data.into(),
            log,
            tls: (!cert.is_empty()).then(|| (cert.into(), key.into())),
            max_room_clients,
            no_tray,
            auto_update,
            command,
            private,
            room,
            passwords,
            password: Sha256::digest(password.as_bytes()).into(),
        })
    }

    pub fn local_url(&self, actual_port: Option<u16>) -> String {
        let (hostname, port) = self.addr.rsplit_once(':').unwrap_or(("127.0.0.1", "7878"));
        let hostname = match hostname {
            "" | "0.0.0.0" => "127.0.0.1",
            "[::]" => "[::1]",
            other => other,
        };
        let port = actual_port.map_or_else(|| port.to_owned(), |port| port.to_string());
        let scheme = if self.tls.is_some() { "https" } else { "http" };
        format!("{scheme}://{hostname}:{port}")
    }

    pub fn authorize(&self, room: &str, password: &str) -> bool {
        let given: [u8; 32] = Sha256::digest(password.as_bytes()).into();
        if self.passwords.is_empty() {
            return bool::from(self.password.ct_eq(&given))
                && (self.room.is_empty() || self.room == room);
        }
        let expected = self.passwords.get(room);
        bool::from(expected.unwrap_or(&[0; 32]).ct_eq(&given)) && expected.is_some()
    }
}

fn host(addr: &str) -> &str {
    addr.rsplit_once(':')
        .map_or(addr, |(host, _)| host)
        .trim_matches(['[', ']'])
}

fn loopback(host: &str) -> bool {
    host.eq_ignore_ascii_case("localhost")
        || host.parse::<IpAddr>().is_ok_and(|ip| ip.is_loopback())
}

fn room_passwords(path: &str) -> Result<HashMap<String, [u8; 32]>, Failure> {
    if path.is_empty() {
        return Ok(HashMap::new());
    }
    let entries: HashMap<String, String> = serde_json::from_slice(&fs::read(path)?)?;
    if entries.is_empty() {
        return Err("expected a non-empty JSON object of room passwords".into());
    }
    let mut used = HashSet::new();
    let mut result = HashMap::new();
    for (room, password) in entries {
        let digest: [u8; 32] = Sha256::digest(password.as_bytes()).into();
        if room.is_empty()
            || room != room.trim()
            || room.chars().count() > 128
            || password.trim().is_empty()
            || !used.insert(digest)
        {
            return Err("room names must be valid and passwords non-empty and distinct".into());
        }
        result.insert(room, digest);
    }
    Ok(result)
}
