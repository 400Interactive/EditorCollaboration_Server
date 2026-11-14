use std::{fs, path::Path, time::Duration};

use rusqlite::{Connection, OptionalExtension, Row, params};
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};

use crate::{Failure, Peer, now_millis};

#[derive(Serialize, Deserialize, Default)]
#[serde(default, rename_all = "camelCase")]
pub struct Chat {
    pub id: String,
    pub author_id: String,
    pub author: String,
    pub color: String,
    pub text: String,
    pub at: i64,
    #[serde(skip_serializing_if = "is_zero")]
    pub edited_at: i64,
}

fn is_zero(value: &i64) -> bool {
    *value == 0
}

#[derive(Serialize, Deserialize, Default)]
#[serde(default, rename_all = "camelCase")]
pub struct Comment {
    pub id: String,
    pub anchor: Value,
    pub resolved: bool,
    pub created_at: i64,
    #[serde(deserialize_with = "vec_or_null")]
    pub messages: Vec<Chat>,
}

fn vec_or_null<'de, D: serde::Deserializer<'de>, T: Deserialize<'de>>(
    deserializer: D,
) -> Result<Vec<T>, D::Error> {
    Ok(Option::<Vec<T>>::deserialize(deserializer)?.unwrap_or_default())
}

#[derive(Serialize, Deserialize, Default)]
#[serde(default, rename_all = "camelCase")]
pub struct Asset {
    pub package: String,
    pub asset: String,
    pub hash: String,
    pub dirty: bool,
    pub working: bool,
    pub author_id: String,
    pub author: String,
    pub color: String,
    pub at: i64,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct Package {
    pub package: String,
    pub hash: String,
    pub size: i64,
    pub base: String,
    pub author_id: String,
    pub author: String,
    pub color: String,
    pub seq: i64,
    pub at: i64,
    pub cutoff: i64,
    pub engine: String,
}

pub const PACKAGE_COLUMNS: &str =
    "package,hash,size,base,author_id,author,color,seq,at,cutoff,engine";
const ASSET_COLUMNS: &str = "package,asset,hash,dirty,working,author_id,author,color,at";

pub fn open(dir: &Path) -> Result<Connection, Failure> {
    let mut db = Connection::open(dir.join("collab.db"))?;
    db.busy_timeout(Duration::from_secs(10))?;
    db.execute_batch(
        "PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL; PRAGMA temp_store=MEMORY;",
    )?;
    db.execute_batch(include_str!("schema.sql"))?;
    let tx = db.transaction()?;
    for (table, column, definition) in [
        ("assets", "working", "INTEGER NOT NULL DEFAULT 0"),
        ("packages", "cutoff", "INTEGER NOT NULL DEFAULT 0"),
        ("packages", "engine", "TEXT NOT NULL DEFAULT ''"),
        ("blobs", "strong_hash", "TEXT NOT NULL DEFAULT ''"),
    ] {
        let exists: bool = tx.query_row(
            &format!("SELECT EXISTS(SELECT 1 FROM pragma_table_info('{table}') WHERE name=?1)"),
            [column],
            |row| row.get(0),
        )?;
        if !exists {
            tx.execute_batch(&format!(
                "ALTER TABLE {table} ADD COLUMN {column} {definition}"
            ))?;
        }
    }
    tx.execute_batch(
        "INSERT OR IGNORE INTO content_heads(room,target,seq,revision,cutoff,base,engine)
            SELECT p.room,p.package,COALESCE(MAX(o.seq),0),p.seq,COALESCE(MAX(o.seq),0),p.base,p.engine
            FROM packages p LEFT JOIN ops o ON o.room=p.room AND o.target=p.package GROUP BY p.room,p.package;
         INSERT OR IGNORE INTO content_heads(room,target,seq,revision,cutoff,base,engine)
            SELECT room,target,MAX(seq),0,MAX(seq),'','' FROM ops GROUP BY room,target;
         UPDATE packages SET cutoff=(SELECT h.cutoff FROM content_heads h
            WHERE h.room=packages.room AND h.target=packages.package) WHERE engine='';
         INSERT OR IGNORE INTO room_blobs(room,hash) SELECT room,hash FROM packages;"
    )?;
    tx.commit()?;
    import_legacy(&mut db, dir)?;
    Ok(db)
}

fn import_legacy(db: &mut Connection, dir: &Path) -> Result<(), Failure> {
    #[derive(Deserialize, Default)]
    #[serde(default)]
    struct Legacy {
        room: String,
        #[serde(deserialize_with = "vec_or_null")]
        comments: Vec<Option<Comment>>,
        #[serde(deserialize_with = "vec_or_null")]
        chat: Vec<Chat>,
        #[serde(deserialize_with = "vec_or_null")]
        assets: Vec<Asset>,
    }

    for file in fs::read_dir(dir)? {
        let file = file?;
        let name = file.file_name();
        let Some(name) = name
            .to_str()
            .filter(|name| name.starts_with("room-") && name.ends_with(".json"))
        else {
            continue;
        };
        let Ok(legacy) = serde_json::from_slice::<Legacy>(&fs::read(file.path())?) else {
            server_log!("Ignoring invalid legacy room file {name}");
            continue;
        };
        if legacy.room.trim().is_empty() {
            continue;
        }
        let tx = db.transaction()?;
        for chat in legacy.chat {
            insert_chat(&tx, &legacy.room, &chat)?;
        }
        for comment in legacy
            .comments
            .into_iter()
            .flatten()
            .filter(|comment| !comment.id.is_empty())
        {
            insert_comment(&tx, &legacy.room, &comment)?;
        }
        for asset in legacy.assets {
            put_asset(&tx, &legacy.room, &asset)?;
        }
        tx.commit()?;
        fs::rename(file.path(), dir.join(format!("{name}.imported")))?;
    }
    Ok(())
}

pub fn chat_page(db: &Connection, room: &str, before: &str) -> rusqlite::Result<(Vec<Chat>, bool)> {
    let mut statement = db.prepare(
        "SELECT id,author_id,author,color,text,at FROM chat WHERE room=?1
         AND (?2='' OR seq < (SELECT seq FROM chat WHERE id=?2 AND room=?1))
         ORDER BY seq DESC LIMIT 101",
    )?;
    let mut messages = statement
        .query_map(params![room, before], |row| {
            Ok(Chat {
                id: row.get(0)?,
                author_id: row.get(1)?,
                author: row.get(2)?,
                color: row.get(3)?,
                text: row.get(4)?,
                at: row.get(5)?,
                edited_at: 0,
            })
        })?
        .collect::<rusqlite::Result<Vec<_>>>()?;
    let complete = messages.len() <= 100;
    messages.truncate(100);
    messages.reverse();
    Ok((messages, complete))
}

pub fn insert_chat(db: &Connection, room: &str, chat: &Chat) -> rusqlite::Result<()> {
    db.execute(
        "INSERT OR IGNORE INTO chat(room,id,author_id,author,color,text,at) VALUES(?,?,?,?,?,?,?)",
        params![
            room,
            chat.id,
            chat.author_id,
            chat.author,
            chat.color,
            chat.text,
            chat.at
        ],
    )?;
    Ok(())
}

fn read_comment(row: &Row<'_>) -> rusqlite::Result<Comment> {
    let anchor: String = row.get(1)?;
    let messages: String = row.get(4)?;
    Ok(Comment {
        id: row.get(0)?,
        anchor: serde_json::from_str(&anchor).unwrap_or_default(),
        resolved: row.get(2)?,
        created_at: row.get(3)?,
        messages: serde_json::from_str(&messages).unwrap_or_default(),
    })
}

pub fn comment(db: &Connection, room: &str, id: &str) -> rusqlite::Result<Option<Comment>> {
    db.query_row(
        "SELECT id,anchor,resolved,created_at,messages FROM comments WHERE room=? AND id=?",
        params![room, id],
        read_comment,
    )
    .optional()
}

pub fn insert_comment(db: &Connection, room: &str, comment: &Comment) -> rusqlite::Result<()> {
    db.execute("INSERT OR IGNORE INTO comments(room,id,anchor,resolved,created_at,messages) VALUES(?,?,?,?,?,?)",
        params![room,comment.id,comment.anchor.to_string(),comment.resolved,comment.created_at,
                serde_json::to_string(&comment.messages).expect("serializable comments")])?;
    Ok(())
}

pub fn save_comment(db: &Connection, room: &str, comment: &Comment) -> rusqlite::Result<()> {
    db.execute(
        "UPDATE comments SET anchor=?,resolved=?,messages=? WHERE room=? AND id=?",
        params![
            comment.anchor.to_string(),
            comment.resolved,
            serde_json::to_string(&comment.messages).expect("serializable comments"),
            room,
            comment.id
        ],
    )?;
    Ok(())
}

pub fn assets(db: &Connection, room: &str) -> rusqlite::Result<Vec<Asset>> {
    db.prepare(&format!(
        "SELECT {ASSET_COLUMNS} FROM assets WHERE room=? ORDER BY at"
    ))?
    .query_map([room], |row| {
        Ok(Asset {
            package: row.get(0)?,
            asset: row.get(1)?,
            hash: row.get(2)?,
            dirty: row.get(3)?,
            working: row.get(4)?,
            author_id: row.get(5)?,
            author: row.get(6)?,
            color: row.get(7)?,
            at: row.get(8)?,
        })
    })?
    .collect()
}

pub fn delete_asset(db: &Connection, room: &str, asset: &Asset) -> rusqlite::Result<()> {
    db.execute("DELETE FROM assets WHERE room=? AND package=? AND (author=? OR (author_id=? AND author_id<>''))",
        params![room, asset.package, asset.author, asset.author_id])?;
    Ok(())
}

pub fn put_asset(db: &Connection, room: &str, asset: &Asset) -> rusqlite::Result<()> {
    delete_asset(db, room, asset)?;
    db.execute("INSERT INTO assets(room,package,author,asset,hash,dirty,working,author_id,color,at) VALUES(?,?,?,?,?,?,?,?,?,?)",
        params![room,asset.package,asset.author,asset.asset,asset.hash,asset.dirty,asset.working,
                asset.author_id,asset.color,asset.at])?;
    Ok(())
}

pub fn read_package(row: &Row<'_>) -> rusqlite::Result<Package> {
    Ok(Package {
        package: row.get(0)?,
        hash: row.get(1)?,
        size: row.get(2)?,
        base: row.get(3)?,
        author_id: row.get(4)?,
        author: row.get(5)?,
        color: row.get(6)?,
        seq: row.get(7)?,
        at: row.get(8)?,
        cutoff: row.get(9)?,
        engine: row.get(10)?,
    })
}

pub fn snapshot(db: &Connection, peer: &Peer) -> rusqlite::Result<[Value; 3]> {
    let room = &peer.room;
    db.execute(
        "DELETE FROM assets WHERE room=? AND at<?",
        params![room, now_millis() - 30 * 86400 * 1000],
    )?;
    db.execute(
        "DELETE FROM comments WHERE room=?1 AND seq NOT IN
                (SELECT seq FROM comments WHERE room=?1 ORDER BY seq DESC LIMIT 2000)",
        [room],
    )?;
    trim_chat(db, room)?;
    let (chat, complete) = chat_page(db, room, "")?;
    let comments = db
        .prepare(
            "SELECT id,anchor,resolved,created_at,messages FROM comments WHERE room=? ORDER BY seq",
        )?
        .query_map([room], read_comment)?
        .collect::<rusqlite::Result<Vec<_>>>()?;
    Ok([
        json!({"type":"chatHistory","messages":chat,"complete":complete}),
        json!({"type":"comments","comments":comments}),
        json!({"type":"assets","records":assets(db, room)?}),
    ])
}

pub fn trim_chat(db: &Connection, room: &str) -> rusqlite::Result<()> {
    db.execute(
        "DELETE FROM chat WHERE room=?1 AND seq NOT IN
                (SELECT seq FROM chat WHERE room=?1 ORDER BY seq DESC LIMIT 10000)",
        [room],
    )?;
    Ok(())
}
