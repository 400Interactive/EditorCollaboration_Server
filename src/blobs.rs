use std::{
    collections::HashSet,
    fs as std_fs, io,
    path::PathBuf,
    time::{Duration, SystemTime},
};

use axum::{
    body::Body,
    extract::{Path, State},
    http::{HeaderMap, StatusCode, header},
    response::{IntoResponse, Response},
};
use futures_util::StreamExt;
use rusqlite::{Connection, OptionalExtension, params};
use sha2::{Digest, Sha256};
use tokio::{
    fs,
    io::{AsyncReadExt, AsyncSeekExt, AsyncWriteExt},
};
use tokio_util::io::ReaderStream;

use crate::{App, Failure, HttpResult, RoomState, Server, content, now_millis};

pub const MAX_BLOB: u64 = 256 * 1024 * 1024;
const TRANSFER_TIMEOUT: Duration = Duration::from_secs(600);
const BLOB_GRACE: Duration = Duration::from_secs(3600);

pub fn valid_hash(hash: &str) -> bool {
    hash.len() == 32
        && hash
            .bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
}

pub fn path(app: &Server, hash: &str) -> PathBuf {
    app.config.data.join("blobs").join(&hash[..2]).join(hash)
}

pub fn exists(app: &Server, db: &Connection, room: &str, hash: &str) -> rusqlite::Result<bool> {
    if !valid_hash(hash) {
        return Ok(false);
    }
    let granted: bool = db.query_row(
        "SELECT EXISTS(SELECT 1 FROM room_blobs WHERE room=? AND hash=?)",
        params![room, hash],
        |row| row.get(0),
    )?;
    Ok(granted && path(app, hash).is_file())
}

fn token(headers: &HeaderMap) -> HttpResult<&str> {
    headers
        .get(header::AUTHORIZATION)
        .and_then(|value| value.to_str().ok())
        .and_then(|value| value.strip_prefix("Bearer "))
        .ok_or(StatusCode::UNAUTHORIZED)
}

fn token_room<'a>(state: &'a RoomState, token: &str) -> HttpResult<&'a str> {
    state
        .clients
        .values()
        .find(|peer| peer.token == token)
        .map(|peer| peer.room.as_str())
        .ok_or(StatusCode::UNAUTHORIZED)
}

fn storage(error: impl std::fmt::Display) -> StatusCode {
    server_log!("blob storage: {error}");
    StatusCode::INTERNAL_SERVER_ERROR
}

pub async fn put(
    State(app): State<App>,
    Path(hash): Path<String>,
    headers: HeaderMap,
    body: Body,
) -> HttpResult<StatusCode> {
    tokio::time::timeout(
        TRANSFER_TIMEOUT,
        upload(app, hash.to_ascii_lowercase(), headers, body),
    )
    .await
    .map_err(|_| StatusCode::REQUEST_TIMEOUT)?
}

async fn upload(app: App, hash: String, headers: HeaderMap, body: Body) -> HttpResult<StatusCode> {
    let token = token(&headers)?;
    token_room(&app.state.lock().unwrap(), token)?;
    if !valid_hash(&hash) {
        return Err(StatusCode::BAD_REQUEST);
    }
    let destination = path(&app, &hash);
    let parent = destination.parent().unwrap();
    fs::create_dir_all(parent).await.map_err(storage)?;
    let (file, temporary) = tokio::task::block_in_place(|| tempfile::NamedTempFile::new_in(parent))
        .map_err(storage)?
        .into_parts();
    let mut file = fs::File::from_std(file);
    let mut chunks = body.into_data_stream();
    let mut digest = md5::Context::new();
    let mut strong = Sha256::new();
    let mut size = 0u64;
    while let Some(chunk) = chunks.next().await {
        let chunk = chunk.map_err(|_| StatusCode::BAD_REQUEST)?;
        size += chunk.len() as u64;
        if size > MAX_BLOB {
            return Err(StatusCode::PAYLOAD_TOO_LARGE);
        }
        digest.consume(&chunk);
        strong.update(&chunk);
        file.write_all(&chunk).await.map_err(storage)?;
    }
    if format!("{:x}", digest.compute()) != hash {
        return Err(StatusCode::BAD_REQUEST);
    }
    file.flush().await.map_err(storage)?;
    file.sync_all().await.map_err(storage)?;
    drop(file);
    let strong = hex::encode(strong.finalize());

    tokio::task::block_in_place(|| {
        let state = app.state.lock().unwrap();
        let room = token_room(&state, token)?;
        match destination.metadata() {
            Ok(info) if info.is_file() => {
                if info.len() != size
                    || existing_digest(&state.db, &hash, &destination).map_err(storage)? != strong
                {
                    return Err(StatusCode::CONFLICT);
                }
            }
            Err(err) if err.kind() == io::ErrorKind::NotFound => {
                temporary.persist_noclobber(&destination).map_err(storage)?;
            }
            Ok(_) => return Err(StatusCode::INTERNAL_SERVER_ERROR),
            Err(err) => return Err(storage(err)),
        }
        std_fs::File::options()
            .write(true)
            .open(&destination)
            .and_then(|file| file.set_modified(SystemTime::now()))
            .map_err(storage)?;
        let tx = state.db.unchecked_transaction().map_err(storage)?;
        tx.execute("INSERT INTO blobs(hash,size,at,strong_hash) VALUES(?,?,?,?)
            ON CONFLICT(hash) DO UPDATE SET size=excluded.size,at=excluded.at,strong_hash=excluded.strong_hash",
            params![hash,size,now_millis(),strong]).map_err(storage)?;
        tx.execute(
            "INSERT OR IGNORE INTO room_blobs(room,hash) VALUES(?,?)",
            params![room, hash],
        )
        .map_err(storage)?;
        tx.commit().map_err(storage)?;
        Ok(StatusCode::OK)
    })
}

fn existing_digest(db: &Connection, hash: &str, path: &std::path::Path) -> Result<String, Failure> {
    let cached: Option<String> = db
        .query_row(
            "SELECT strong_hash FROM blobs WHERE hash=?",
            [hash],
            |row| row.get(0),
        )
        .optional()?;
    if let Some(digest) = cached.filter(|digest| !digest.is_empty()) {
        return Ok(digest);
    }
    let mut digest = Sha256::new();
    io::copy(&mut std_fs::File::open(path)?, &mut digest)?;
    Ok(hex::encode(digest.finalize()))
}

pub async fn get(
    State(app): State<App>,
    Path(hash): Path<String>,
    headers: HeaderMap,
) -> HttpResult<Response> {
    let token = token(&headers)?;
    let hash = hash.to_ascii_lowercase();
    let (file, metadata) = tokio::task::block_in_place(|| {
        let state = app.state.lock().unwrap();
        let room = token_room(&state, token)?;
        if !valid_hash(&hash) {
            return Err(StatusCode::BAD_REQUEST);
        }
        if !exists(&app, &state.db, room, &hash).map_err(storage)? {
            return Err(StatusCode::NOT_FOUND);
        }
        let file = std_fs::File::open(path(&app, &hash)).map_err(|_| StatusCode::NOT_FOUND)?;
        let metadata = file.metadata().map_err(storage)?;
        Ok((file, metadata))
    })?;
    let size = metadata.len();
    let mut file = fs::File::from_std(file);
    file.set_max_buf_size(32 * 1024);
    let mut response = Response::builder()
        .header(header::ACCEPT_RANGES, "bytes")
        .header(header::CONTENT_TYPE, "application/octet-stream");
    let length = if let Some(range) = headers.get(header::RANGE) {
        let Some((start, end)) = range
            .to_str()
            .ok()
            .and_then(|range| byte_range(range, size))
        else {
            return Ok((
                StatusCode::RANGE_NOT_SATISFIABLE,
                [(header::CONTENT_RANGE, format!("bytes */{size}"))],
            )
                .into_response());
        };
        file.seek(io::SeekFrom::Start(start))
            .await
            .map_err(storage)?;
        response = response
            .status(StatusCode::PARTIAL_CONTENT)
            .header(header::CONTENT_RANGE, format!("bytes {start}-{end}/{size}"));
        end - start + 1
    } else {
        size
    };
    let stream = ReaderStream::with_capacity(file.take(length), 32 * 1024)
        .take_until(tokio::time::sleep(TRANSFER_TIMEOUT));
    response
        .header(header::CONTENT_LENGTH, length)
        .body(Body::from_stream(stream))
        .map_err(storage)
}

fn byte_range(value: &str, size: u64) -> Option<(u64, u64)> {
    if size == 0 {
        return None;
    }
    let (start, end) = value.strip_prefix("bytes=")?.split_once('-')?;
    if start.is_empty() {
        let length: u64 = end.parse().ok()?;
        return (length > 0).then(|| (size.saturating_sub(length), size - 1));
    }
    let start: u64 = start.parse().ok()?;
    let end = if end.is_empty() {
        size - 1
    } else {
        end.parse::<u64>().ok()?.min(size - 1)
    };
    (start <= end).then_some((start, end))
}

pub fn collect(app: &Server, db: &Connection) -> Result<(), Failure> {
    let root = app.config.data.join("blobs");
    let dirs = match std_fs::read_dir(&root) {
        Ok(dirs) => dirs,
        Err(err) if err.kind() == io::ErrorKind::NotFound => return Ok(()),
        Err(err) => return Err(err.into()),
    };
    let mut keep: HashSet<String> = db
        .prepare("SELECT hash FROM packages")?
        .query_map([], |row| row.get::<_, String>(0))?
        .collect::<rusqlite::Result<_>>()?;
    let mut query = db.prepare("SELECT ops FROM ops")?;
    let mut rows = query.query([])?;
    while let Some(row) = rows.next()? {
        keep.extend(content::references(&row.get::<_, String>(0)?));
    }
    drop(rows);
    drop(query);
    for dir in dirs {
        let dir = dir?;
        if !dir.file_type()?.is_dir() {
            continue;
        }
        for entry in std_fs::read_dir(dir.path())? {
            let entry = entry?;
            let Some(hash) = entry.file_name().to_str().map(str::to_ascii_lowercase) else {
                continue;
            };
            if !entry.file_type()?.is_file()
                || !valid_hash(&hash)
                || keep.contains(&hash)
                || entry.path() != path(app, &hash)
            {
                continue;
            }
            if entry.metadata()?.modified()?.elapsed().unwrap_or_default() < BLOB_GRACE {
                continue;
            }
            db.execute("DELETE FROM room_blobs WHERE hash=?", [&hash])?;
            std_fs::remove_file(entry.path())?;
            db.execute("DELETE FROM blobs WHERE hash=?", [&hash])?;
        }
    }
    Ok(())
}
