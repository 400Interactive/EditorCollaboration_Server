macro_rules! server_log {
    ($($arg:tt)*) => { crate::operations::log(format_args!($($arg)*)) };
}

mod blobs;
mod collab;
mod config;
mod content;
mod db;
mod operations;
mod tray;

use std::{
    collections::{HashMap, HashSet},
    net::SocketAddr,
    sync::{Arc, Mutex},
    time::{Duration, SystemTime, UNIX_EPOCH},
};

use axum::{
    Json, Router,
    body::Bytes,
    extract::{
        ConnectInfo, State, WebSocketUpgrade,
        ws::{CloseFrame, Message, Utf8Bytes, WebSocket},
    },
    http::{HeaderMap, StatusCode},
    response::{IntoResponse, Response},
    routing::{get, post},
};
use futures_util::{SinkExt, StreamExt};
use rusqlite::Connection;
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value, json, value::RawValue};
use sha2::{Digest, Sha256};
use subtle::ConstantTimeEq;
use tokio::{
    net::TcpListener,
    sync::{Semaphore, mpsc, watch},
    time::{Instant, timeout},
};
use uuid::Uuid;

use config::Config;

const VERSION: &str = env!("CARGO_PKG_VERSION");
const PROTOCOL: u32 = 9;
const MAX_MESSAGE: usize = 64 * 1024;
const WRITE_TIMEOUT: Duration = Duration::from_secs(10);
type App = Arc<Server>;
type Failure = Box<dyn std::error::Error + Send + Sync>;
type HttpResult<T> = Result<T, StatusCode>;
type SessionId = Uuid;

struct Server {
    config: Config,
    state: Mutex<RoomState>,
    connections: Arc<Semaphore>,
    stop_token: String,
    stop: watch::Sender<bool>,
    update: Mutex<Option<operations::Release>>,
    plugin: operations::Plugin,
}

struct RoomState {
    db: Connection,
    clients: HashMap<SessionId, Peer>,
}

struct Peer {
    room: String,
    id: String,
    author: String,
    session: String,
    token: String,
    engine: String,
    commit: String,
    state: Map<String, Value>,
    send: mpsc::Sender<Utf8Bytes>,
    disconnect: watch::Sender<bool>,
}

impl Peer {
    fn name(&self) -> String {
        let name = self
            .state
            .get("name")
            .and_then(Value::as_str)
            .unwrap_or_default();
        if name.trim().is_empty() {
            "?".into()
        } else {
            name.chars().filter(|c| *c >= ' ').collect()
        }
    }

    fn color(&self) -> &str {
        self.state
            .get("color")
            .and_then(Value::as_str)
            .unwrap_or_default()
    }
}

#[derive(Deserialize, Default)]
#[serde(default, rename_all = "camelCase")]
struct Input<'a> {
    #[serde(rename = "type")]
    kind: String,
    protocol_version: u32,
    room: String,
    password: String,
    client_id: String,
    session: String,
    engine_version: String,
    commit: String,
    state: Map<String, Value>,
    target: String,
    package: String,
    base: String,
    hash: String,
    size: i64,
    since: i64,
    revision: i64,
    from_revision: i64,
    from_since: i64,
    operation_id: String,
    #[serde(rename = "kind")]
    op_kind: String,
    #[serde(borrow)]
    ops: Option<&'a RawValue>,
    graph: String,
    blob: String,
    from: String,
    text: String,
    before: String,
    anchor: Value,
    id: String,
    message_id: String,
    resolved: Option<bool>,
    seconds: i64,
    asset: String,
    dirty: bool,
    working: bool,
}

fn encode(event: impl Serialize) -> Utf8Bytes {
    serde_json::to_string(&event)
        .expect("serializable protocol event")
        .into()
}

fn enqueue(peer: &Peer, message: Utf8Bytes) {
    if let Err(mpsc::error::TrySendError::Full(_)) = peer.send.try_send(message) {
        peer.disconnect.send_replace(true);
    }
}

fn broadcast(state: &RoomState, room: &str, exclude: Option<SessionId>, message: Value) {
    let message = encode(message);
    for (id, peer) in &state.clients {
        if peer.room == room && Some(*id) != exclude {
            enqueue(peer, message.clone());
        }
    }
}

fn notify(peer: &Peer, event: Value) {
    enqueue(peer, encode(event));
}

fn error(code: &str, reason: &str, message: &str) -> Value {
    json!({"type":"error","code":code,"reason":reason,"message":message})
}

fn new_id() -> String {
    let id = Uuid::new_v4();
    hex::encode(&id.as_bytes()[..8])
}

fn author_id(client_id: &str) -> String {
    hex::encode(&Sha256::digest(format!("author:{client_id}"))[..16])
}

fn valid_key(key: &str) -> bool {
    (16..=64).contains(&key.len()) && key.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'-')
}

fn now_millis() -> i64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or_default()
        .as_millis() as i64
}

fn valid_package(path: &str) -> bool {
    path.starts_with('/') && path.len() <= 512 && !path.contains(['\r', '\n', '\0'])
}

fn clean_version(version: &str) -> String {
    let version = version.trim();
    if version.chars().count() > 64 || version.contains(['\r', '\n', '\0']) {
        return String::new();
    }
    version.into()
}

fn filter_state(mut state: Map<String, Value>) -> Map<String, Value> {
    state.retain(|key, value| {
        let allowed = matches!(
            key.as_str(),
            "name"
                | "color"
                | "map"
                | "cam"
                | "asset"
                | "graph"
                | "nodes"
                | "actors"
                | "active"
                | "pie"
                | "play"
                | "sequencer"
                | "branch"
                | "actorsMore"
                | "nodesMore"
                | "liveBp"
        );
        allowed
            && (!matches!(key.as_str(), "name" | "color")
                || value.is_null()
                || value
                    .as_str()
                    .is_some_and(|text| text.chars().count() <= 64))
            && value.to_string().len() <= 8192
    });
    state
}

fn notice(state: &RoomState, peer: &Peer) -> Option<Value> {
    for other in state
        .clients
        .values()
        .filter(|other| other.room == peer.room)
    {
        if peer.engine != other.engine {
            return Some(
                json!({"type":"notice","code":"engine","message":"Collaborators are not on the same engine version."}),
            );
        }
        if peer.commit != other.commit {
            return Some(
                json!({"type":"notice","code":"commit","message":"Collaborators are not on the same git commit."}),
            );
        }
    }
    None
}

struct Rejection(&'static str, String);

fn reject(code: &'static str, message: impl Into<String>) -> Rejection {
    Rejection(code, message.into())
}

fn register(app: &Server, id: SessionId, peer: Peer) -> Result<(), Rejection> {
    if *app.stop.borrow() {
        return Err(reject("full", "The server is shutting down."));
    }
    let mut state = app.state.lock().unwrap();
    let room = &peer.room;
    let stale = state
        .clients
        .iter()
        .find(|(_, other)| {
            other.room == *room && other.author == peer.author && other.session == peer.session
        })
        .map(|(id, _)| *id);
    let room_count = state
        .clients
        .values()
        .filter(|other| other.room == *room)
        .count();
    if room_count.saturating_sub(usize::from(stale.is_some())) >= app.config.max_room_clients {
        return Err(reject("full", "The room is full."));
    }
    if room_count == 0
        && state
            .clients
            .values()
            .map(|p| &p.room)
            .collect::<HashSet<_>>()
            .len()
            >= 64
    {
        return Err(reject("full", "The server is hosting too many rooms."));
    }
    let history = db::snapshot(&state.db, &peer).map_err(|err| {
        server_log!("load room {room}: {err}");
        reject("storage", "The server cannot read its database.")
    })?;
    let replaced = stale.and_then(|id| state.clients.remove(&id));
    if let Some(stale) = &replaced {
        stale.disconnect.send_replace(true);
        broadcast(&state, room, None, json!({"type":"leave","id":stale.id}));
    }
    let roster: Vec<_> = state
        .clients
        .values()
        .filter(|other| other.room == *room)
        .map(|other| json!({"id":other.id,"authorId":other.author,"state":other.state}))
        .collect();
    notify(
        &peer,
        json!({"type":"welcome","selfId":peer.id,"authorId":peer.author,
                       "protocolVersion":PROTOCOL,"blobToken":peer.token}),
    );
    notify(&peer, json!({"type":"roster","peers":roster}));
    for event in history {
        notify(&peer, event);
    }
    if let Some(warning) = notice(&state, &peer) {
        notify(&peer, warning.clone());
        broadcast(&state, room, None, warning);
        notify(&peer, json!({"type":"packages","records":[]}));
    } else {
        content::send_packages(&state.db, &peer).ok();
        if replaced.is_some_and(|stale| stale.engine != peer.engine || stale.commit != peer.commit)
        {
            for other in state.clients.values().filter(|other| other.room == *room) {
                notify(other, json!({"type":"notice","code":"compatible"}));
                content::send_packages(&state.db, other).ok();
            }
        }
    }
    broadcast(
        &state,
        room,
        None,
        json!({"type":"presence","id":peer.id,
                                       "authorId":peer.author,"state":peer.state}),
    );
    server_log!("{} + {} ({})", peer.room, peer.name(), peer.id);
    state.clients.insert(id, peer);
    Ok(())
}

async fn send_rejection(socket: &mut WebSocket, rejection: Rejection) {
    let _ = timeout(WRITE_TIMEOUT, async {
        socket
            .send(Message::Text(encode(
                json!({"type":"error","code":rejection.0,"message":rejection.1}),
            )))
            .await?;
        socket
            .send(Message::Close(Some(CloseFrame {
                code: 1008,
                reason: rejection.0.into(),
            })))
            .await
    })
    .await;
}

async fn upgrade(State(app): State<App>, ws: WebSocketUpgrade) -> Response {
    let Ok(permit) = app.connections.clone().try_acquire_owned() else {
        return StatusCode::SERVICE_UNAVAILABLE.into_response();
    };
    ws.max_message_size(MAX_MESSAGE)
        .max_frame_size(MAX_MESSAGE)
        .on_upgrade(move |socket| async move {
            let _permit = permit;
            session(socket, app).await;
        })
}

fn parse_message(frame: &Message) -> Option<Input<'_>> {
    match frame {
        Message::Text(text) => serde_json::from_str(text).ok(),
        Message::Binary(bytes) => serde_json::from_slice(bytes).ok(),
        _ => None,
    }
}

async fn session(mut socket: WebSocket, app: App) {
    let mut stop = app.stop.subscribe();
    let Ok(Some(Ok(first))) = timeout(Duration::from_secs(10), socket.recv()).await else {
        return;
    };
    let Some(hello) = parse_message(&first).filter(|msg| msg.kind == "hello") else {
        send_rejection(
            &mut socket,
            reject("protocol", "The first message must be 'hello'."),
        )
        .await;
        return;
    };
    let room = hello.room.trim();
    let rejection = if hello.protocol_version != PROTOCOL {
        Some(reject(
            "version",
            format!(
                "Protocol version {} is not supported (server: {PROTOCOL}). Update the plugin or the server.",
                hello.protocol_version
            ),
        ))
    } else if room.is_empty() || room.chars().count() > 128 {
        Some(reject("room", "Invalid room name."))
    } else if !app.config.authorize(room, &hello.password) {
        tokio::time::sleep(Duration::from_millis(500)).await;
        Some(reject("auth", "Wrong password."))
    } else if !valid_key(&hello.client_id) || !valid_key(&hello.session) {
        Some(reject(
            "protocol",
            "The hello message needs a clientId and a session.",
        ))
    } else {
        None
    };
    if let Some(rejection) = rejection {
        send_rejection(&mut socket, rejection).await;
        return;
    }

    let id = Uuid::new_v4();
    let (send, mut outgoing) = mpsc::channel(256);
    let (disconnect, mut disconnected) = watch::channel(false);
    let mut state = filter_state(hello.state);
    state.retain(|_, value| !value.is_null());
    let peer = Peer {
        room: room.into(),
        id: new_id(),
        author: author_id(&hello.client_id),
        session: hello.session,
        token: Uuid::new_v4().simple().to_string(),
        engine: clean_version(&hello.engine_version),
        commit: clean_version(&hello.commit),
        state,
        send,
        disconnect,
    };
    if let Err(rejection) = tokio::task::block_in_place(|| register(&app, id, peer)) {
        send_rejection(&mut socket, rejection).await;
        return;
    }

    let (mut writer, mut reader) = socket.split();
    let (pong, mut pongs) = watch::channel(Instant::now());
    let mut ping = tokio::time::interval_at(
        Instant::now() + Duration::from_secs(5),
        Duration::from_secs(5),
    );
    let write = async {
        loop {
            let message = tokio::select! {
                Some(text) = outgoing.recv() => Message::Text(text),
                _ = ping.tick() => {
                    if pongs.borrow_and_update().elapsed() >= Duration::from_secs(15) { break; }
                    Message::Ping(Bytes::new())
                },
                _ = disconnected.wait_for(|stop| *stop) => break,
                _ = stop.wait_for(|stop| *stop) => break,
            };
            if !matches!(
                timeout(WRITE_TIMEOUT, writer.send(message)).await,
                Ok(Ok(()))
            ) {
                break;
            }
        }
    };
    let read = async {
        let mut tokens = 60.0_f64;
        let mut last = Instant::now();
        while let Some(Ok(frame)) = reader.next().await {
            match frame {
                Message::Close(_) => break,
                Message::Pong(_) => {
                    pong.send_replace(Instant::now());
                }
                _ => {}
            }
            let Some(msg) = parse_message(&frame) else {
                continue;
            };
            tokens = (tokens + last.elapsed().as_secs_f64() * 30.0).min(60.0);
            last = Instant::now();
            if tokens < 1.0 {
                if msg.kind == "ops.preview" {
                    continue;
                }
                tokio::time::sleep(Duration::from_secs_f64((1.0 - tokens) / 30.0)).await;
                tokens = 1.0;
                last = Instant::now();
            }
            tokens -= 1.0;
            tokio::task::block_in_place(|| dispatch(&app, id, msg));
        }
    };
    tokio::select! { _ = write => {}, _ = read => {} }
    let _ = timeout(Duration::from_secs(1), writer.send(Message::Close(None))).await;
    tokio::task::block_in_place(|| leave(&app, id));
}

fn dispatch(app: &Server, id: SessionId, msg: Input<'_>) {
    let mut state = app.state.lock().unwrap();
    let Some(peer) = state.clients.get(&id) else {
        return;
    };
    let result = match msg.kind.as_str() {
        "presence" => collab::presence(&mut state, id, filter_state(msg.state)),
        "pkg.publish" | "pkg.delete" | "ops" | "ops.preview" | "ops.sync" | "graph.resync"
        | "graph.snapshot" => {
            if notice(&state, peer).is_some() {
                return;
            }
            content::handle(app, &state, id, peer, &msg)
        }
        _ => collab::handle(&state, peer, &msg),
    };
    if let Err(err) = result {
        server_log!("{}: {err}", msg.kind);
        if let Some(peer) = state.clients.get(&id) {
            notify(
                peer,
                error("storage", "", "The server could not complete the request."),
            );
        }
    }
}

fn leave(app: &Server, id: SessionId) {
    let mut state = app.state.lock().unwrap();
    let Some(peer) = state.clients.remove(&id) else {
        return;
    };
    let room = &peer.room;
    if !state
        .clients
        .values()
        .any(|p| p.room == *room && p.author == peer.author)
    {
        if let Err(err) = collab::clear_working(&state, &peer) {
            server_log!("clear working: {err}");
        }
    }
    broadcast(&state, room, None, json!({"type":"leave","id":peer.id}));
    let mut empty = true;
    for other in state.clients.values().filter(|p| p.room == *room) {
        empty = false;
        if (other.engine != peer.engine || other.commit != peer.commit)
            && notice(&state, other).is_none()
        {
            notify(other, json!({"type":"notice","code":"compatible"}));
            content::send_packages(&state.db, other).ok();
        }
    }
    if empty {
        if let Err(err) = blobs::collect(app, &state.db) {
            server_log!("blob cleanup: {err}");
        }
    }
    server_log!("{} - {} ({})", room, peer.name(), peer.id);
}

async fn health(State(app): State<App>) -> Json<Value> {
    let state = app.state.lock().unwrap();
    let rooms: HashSet<_> = state.clients.values().map(|peer| &peer.room).collect();
    let mut payload = json!({"status":"ok","version":VERSION,"protocolVersion":PROTOCOL,
                            "clients":state.clients.len(),"rooms":rooms.len()});
    if !app.plugin.version.is_empty() {
        payload["pluginVersion"] = json!(app.plugin.version);
    }
    if !app.plugin.marketplace.is_empty() {
        payload["marketplaceUrl"] = json!(app.plugin.marketplace);
    }
    if let Some(release) = app.update.lock().unwrap().as_ref() {
        payload["updateAvailable"] = json!(release.available);
        payload["latestVersion"] = json!(release.version);
        payload["releaseUrl"] = json!(release.page);
    }
    Json(payload)
}

async fn stop(
    State(app): State<App>,
    ConnectInfo(address): ConnectInfo<SocketAddr>,
    headers: HeaderMap,
) -> HttpResult<StatusCode> {
    if !address.ip().is_loopback() || headers.contains_key("x-forwarded-for") {
        return Err(StatusCode::NOT_FOUND);
    }
    let token = headers
        .get("x-collab-token")
        .map(|header| header.as_bytes())
        .unwrap_or_default();
    if !bool::from(token.ct_eq(app.stop_token.as_bytes())) {
        return Err(StatusCode::FORBIDDEN);
    }
    app.stop.send_replace(true);
    Ok(StatusCode::ACCEPTED)
}

async fn run(app: App, listener: TcpListener) -> Result<(), Failure> {
    let routes = Router::new()
        .route(
            "/",
            get(|| async {
                format!("Editor Collaboration server {VERSION} - WebSocket endpoint: /ws\n")
            }),
        )
        .route("/health", get(health))
        .route("/ws", get(upgrade))
        .route("/blob/{hash}", get(blobs::get).put(blobs::put))
        .route("/control/shutdown", post(stop))
        .with_state(app.clone())
        .into_make_service_with_connect_info::<SocketAddr>();
    let handle = axum_server::Handle::new();
    let shutdown = handle.clone();
    let stop_app = app.clone();
    let stop_task = tokio::spawn(async move {
        let mut stop = stop_app.stop.subscribe();
        tokio::select! {
            _ = stop.wait_for(|requested| *requested) => {},
            _ = operations::signal() => {},
        }
        stop_app.stop.send_replace(true);
        shutdown.graceful_shutdown(Some(Duration::from_secs(5)));
    });
    let result = if let Some((cert, key)) = &app.config.tls {
        let tls = axum_server::tls_rustls::RustlsConfig::from_pem_file(cert, key).await?;
        let mut server = axum_server::from_tcp_rustls(listener.into_std()?, tls)?.handle(handle);
        server
            .http_builder()
            .http1()
            .timer(hyper_util::rt::TokioTimer::new())
            .header_read_timeout(Duration::from_secs(10))
            .max_buf_size(64 * 1024);
        server.serve(routes).await
    } else {
        let mut server = axum_server::from_tcp(listener.into_std()?)?.handle(handle);
        server
            .http_builder()
            .http1()
            .timer(hyper_util::rt::TokioTimer::new())
            .header_read_timeout(Duration::from_secs(10))
            .max_buf_size(64 * 1024);
        server.serve(routes).await
    };
    stop_task.abort();
    result?;
    // Upgraded sockets are outside the HTTP server's graceful-shutdown bookkeeping.
    let deadline = Instant::now() + Duration::from_secs(5);
    while !app.state.lock().unwrap().clients.is_empty() && Instant::now() < deadline {
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
    Ok(())
}

fn main() -> Result<(), Failure> {
    let _ = rustls::crypto::ring::default_provider().install_default();
    let cfg = Config::parse()?;
    operations::entry(cfg)
}
