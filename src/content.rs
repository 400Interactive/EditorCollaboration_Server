use rusqlite::{Connection, OptionalExtension, params};
use serde::Deserialize;
use serde_json::{Value, json, value::RawValue};

use crate::{
    Input, Peer, RoomState, Server, SessionId, blobs, broadcast, db, error, notify, now_millis,
    valid_key, valid_package,
};
use db::{PACKAGE_COLUMNS, Package};

const REMOVAL_SELECT: &str = "SELECT r.package,r.author_id,r.renamed_to,h.revision,h.cutoff
    FROM package_removals r JOIN content_heads h ON h.room=r.room AND h.target=r.package";

#[derive(serde::Serialize)]
#[serde(rename_all = "camelCase")]
struct Removal {
    package: String,
    author_id: String,
    to: String,
    revision: i64,
    cutoff: i64,
}

impl Removal {
    fn read(row: &rusqlite::Row<'_>) -> rusqlite::Result<Self> {
        Ok(Self {
            package: row.get(0)?,
            author_id: row.get(1)?,
            to: row.get(2)?,
            revision: row.get(3)?,
            cutoff: row.get(4)?,
        })
    }

    fn event(&self) -> Value {
        let mut value = json!(self);
        value["type"] = json!("pkgDeleted");
        value
    }
}

fn remove_package(
    tx: &Connection,
    peer: &Peer,
    target: &str,
    to: &str,
    mut head: Head,
) -> rusqlite::Result<Removal> {
    let room = &peer.room;
    tx.execute(
        "DELETE FROM packages WHERE room=? AND package=?",
        params![room, target],
    )?;
    tx.execute(
        "DELETE FROM ops WHERE room=? AND target=?",
        params![room, target],
    )?;
    head.revision += 1;
    head.cutoff = head.seq;
    head.save(tx, peer, target)?;
    tx.execute(
        "INSERT INTO package_removals(room,package,author_id,renamed_to)
        VALUES (?,?,?,?) ON CONFLICT(room,package) DO UPDATE SET
        author_id=excluded.author_id,renamed_to=excluded.renamed_to",
        params![room, target, peer.author, to],
    )?;
    Ok(Removal {
        package: target.into(),
        author_id: peer.author.clone(),
        to: to.into(),
        revision: head.revision,
        cutoff: head.cutoff,
    })
}

#[derive(Default)]
struct Head {
    seq: i64,
    revision: i64,
    cutoff: i64,
    base: String,
    engine: String,
}

impl Head {
    fn matches(&self, peer: &Peer) -> bool {
        self.base == peer.commit && self.engine == peer.engine
    }

    fn save(&self, tx: &Connection, peer: &Peer, target: &str) -> rusqlite::Result<()> {
        tx.execute("INSERT INTO content_heads(room,target,seq,revision,cutoff,base,engine) VALUES(?,?,?,?,?,?,?)
            ON CONFLICT(room,target) DO UPDATE SET seq=excluded.seq,revision=excluded.revision,
            cutoff=excluded.cutoff,base=excluded.base,engine=excluded.engine",
            params![peer.room,target,self.seq,self.revision,self.cutoff,peer.commit,peer.engine])?;
        Ok(())
    }
}

fn read_head(db: &Connection, room: &str, target: &str) -> rusqlite::Result<Option<Head>> {
    db.query_row(
        "SELECT seq,revision,cutoff,base,engine FROM content_heads WHERE room=? AND target=?",
        params![room, target],
        |row| {
            Ok(Head {
                seq: row.get(0)?,
                revision: row.get(1)?,
                cutoff: row.get(2)?,
                base: row.get(3)?,
                engine: row.get(4)?,
            })
        },
    )
    .optional()
}

#[derive(Deserialize)]
struct Operation {
    #[serde(default)]
    op: String,
    #[serde(default)]
    blob: String,
}

fn operations(raw: Option<&RawValue>) -> Option<Vec<Operation>> {
    let list: Vec<Operation> = serde_json::from_str(raw?.get()).ok()?;
    if list.is_empty()
        || list.len() > 64
        || list.iter().any(|op| {
            op.op.is_empty()
                || op.op.chars().count() > 64
                || (!op.blob.is_empty() && !blobs::valid_hash(&op.blob.to_ascii_lowercase()))
        })
    {
        return None;
    }
    Some(list)
}

fn blobs_exist(
    app: &Server,
    db: &Connection,
    room: &str,
    list: &[Operation],
) -> rusqlite::Result<bool> {
    for op in list.iter().filter(|op| !op.blob.is_empty()) {
        if !blobs::exists(app, db, room, &op.blob.to_ascii_lowercase())? {
            return Ok(false);
        }
    }
    Ok(true)
}

pub fn references(raw: &str) -> impl Iterator<Item = String> {
    #[derive(Deserialize)]
    struct Reference {
        #[serde(default)]
        blob: String,
    }
    serde_json::from_str::<Vec<Reference>>(raw)
        .unwrap_or_default()
        .into_iter()
        .map(|op| op.blob.to_ascii_lowercase())
        .filter(|hash| blobs::valid_hash(hash))
}

fn valid_graph(graph: &str) -> bool {
    !graph.is_empty() && graph.len() <= 1024 && !graph.contains(['\0', '\n', '\r'])
}

pub fn handle(
    app: &Server,
    state: &RoomState,
    id: SessionId,
    peer: &Peer,
    msg: &Input<'_>,
) -> rusqlite::Result<()> {
    let db = &state.db;
    let room = &peer.room;
    match msg.kind.as_str() {
        "pkg.publish" | "pkg.delete" | "ops" => commit(app, state, peer, msg)?,
        "ops.sync" if valid_package(&msg.target) && msg.since >= 0 => replay(db, peer, msg)?,
        "ops.preview" | "graph.resync" | "graph.snapshot" if valid_package(&msg.target) => {
            let current = read_head(db, room, &msg.target)?;
            let matches = current.as_ref().map_or(msg.revision == 0, |head| {
                head.revision == msg.revision && head.matches(peer)
            });
            if !matches {
                return Ok(());
            }
            match msg.kind.as_str() {
                "ops.preview" => {
                    let Some(list) = operations(msg.ops) else {
                        return Ok(());
                    };
                    if blobs_exist(app, db, room, &list)? {
                        broadcast(
                            state,
                            room,
                            Some(id),
                            json!({"type":"opsPreview","target":msg.target,
                            "authorId":peer.author,"ops":msg.ops,"revision":msg.revision}),
                        );
                    }
                }
                "graph.resync" if valid_graph(&msg.graph) => {
                    broadcast(
                        state,
                        room,
                        Some(id),
                        json!({"type":"graphResync","target":msg.target,
                        "graph":msg.graph,"authorId":peer.author,"revision":msg.revision}),
                    );
                }
                "graph.snapshot" if valid_graph(&msg.graph) && msg.text.len() <= 48 * 1024 => {
                    let hash = msg.blob.to_ascii_lowercase();
                    if msg.text.is_empty() && !blobs::valid_hash(&hash) {
                        return Ok(());
                    }
                    if !hash.is_empty() && !blobs::exists(app, db, room, &hash)? {
                        notify(
                            peer,
                            error("sync", "missingBlob", "The snapshot file was not uploaded."),
                        );
                        return Ok(());
                    }
                    let mut event = json!({"type":"graphSnapshot","target":msg.target,"graph":msg.graph,
                        "authorId":peer.author,"revision":msg.revision});
                    if !msg.text.is_empty() {
                        event["text"] = json!(msg.text);
                    }
                    if !hash.is_empty() {
                        event["blob"] = json!(hash);
                    }
                    broadcast(state, room, None, event);
                }
                _ => {}
            }
        }
        _ => {}
    }
    Ok(())
}

fn conflict(peer: &Peer, target: &str, operation_id: &str) {
    notify(
        peer,
        json!({"type":"contentConflict","target":target,"operationId":operation_id}),
    );
}

fn commit(app: &Server, state: &RoomState, peer: &Peer, msg: &Input<'_>) -> rusqlite::Result<()> {
    let room = &peer.room;
    let db = &state.db;
    let ops = msg.kind == "ops";
    let publish = msg.kind == "pkg.publish";
    let target = if ops { &msg.target } else { &msg.package };
    if !valid_package(target) {
        return Ok(());
    }
    if msg.base != peer.commit {
        conflict(peer, target, &msg.operation_id);
        return Ok(());
    }

    if !msg.operation_id.is_empty() {
        if !ops || !valid_key(&msg.operation_id) {
            return Ok(());
        }
        let previous: Option<(String, i64)> = db
            .query_row(
                "SELECT target,seq FROM op_receipts WHERE room=? AND author_id=? AND operation_id=?",
                params![room, peer.author, msg.operation_id],
                |row| Ok((row.get(0)?, row.get(1)?)),
            )
            .optional()?;
        if let Some((previous, seq)) = previous {
            if previous == *target {
                notify(
                    peer,
                    json!({"type":"opsAck","target":target,"operationId":msg.operation_id,"seq":seq}),
                );
            } else {
                conflict(peer, target, &msg.operation_id);
            }
            return Ok(());
        }
    }
    if publish && !msg.from.is_empty() && (!valid_package(&msg.from) || msg.from == *target) {
        return Ok(());
    }

    let hash = msg.hash.to_ascii_lowercase();
    if publish {
        if !(0..=blobs::MAX_BLOB as i64).contains(&msg.size)
            || !blobs::exists(app, db, room, &hash)?
        {
            notify(
                peer,
                error(
                    "sync",
                    "missingBlob",
                    "The published file was not uploaded to this room.",
                ),
            );
            return Ok(());
        }
        if !blobs::path(app, &hash)
            .metadata()
            .is_ok_and(|file| file.len() == msg.size as u64)
        {
            notify(
                peer,
                error(
                    "sync",
                    "missingBlob",
                    "The uploaded file size does not match the publication.",
                ),
            );
            return Ok(());
        }
    }
    if ops {
        let Some(list) = operations(msg.ops) else {
            return Ok(());
        };
        if !matches!(msg.op_kind.as_str(), "level" | "graph") {
            return Ok(());
        }
        if !blobs_exist(app, db, room, &list)? {
            notify(
                peer,
                error(
                    "sync",
                    "missingBlob",
                    "An operation references a file not uploaded to this room.",
                ),
            );
            return Ok(());
        }
    }

    let tx = rusqlite::Transaction::new_unchecked(db, rusqlite::TransactionBehavior::Immediate)?;
    let existing = read_head(&tx, room, target)?;
    let same_base = existing.as_ref().is_none_or(|head| head.matches(peer));
    let mut head = existing.unwrap_or_default();
    let expected = if publish && !same_base {
        0
    } else {
        head.revision
    };
    if msg.revision != expected
        || (!same_base && !publish)
        || (publish && same_base && (msg.since < head.cutoff || msg.since > head.seq))
    {
        conflict(peer, target, &msg.operation_id);
        return Ok(());
    }

    let at = now_millis();
    let mut removal = None;
    let event = if publish {
        if !msg.from.is_empty() {
            let source = read_head(&tx, room, &msg.from)?;
            if source.as_ref().map_or(0, |head| head.revision) != msg.from_revision
                || source.as_ref().map_or(0, |head| head.seq) != msg.from_since
                || source.as_ref().is_some_and(|head| !head.matches(peer))
            {
                conflict(peer, target, &msg.operation_id);
                return Ok(());
            }
            removal = Some(remove_package(
                &tx,
                peer,
                &msg.from,
                target,
                source.unwrap_or_default(),
            )?);
        }
        tx.execute(
            "DELETE FROM package_removals WHERE room=? AND package=?",
            params![room, target],
        )?;
        head.revision += 1;
        head.cutoff = if same_base { msg.since } else { head.seq };
        let record = Package {
            package: target.clone(),
            hash,
            size: msg.size,
            base: peer.commit.clone(),
            author_id: peer.author.clone(),
            author: peer.name(),
            color: peer.color().into(),
            seq: head.revision,
            at,
            cutoff: head.cutoff,
            engine: peer.engine.clone(),
        };
        tx.execute("INSERT INTO packages(room,package,hash,size,base,author_id,author,color,seq,at,cutoff,engine)
            VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(room,package) DO UPDATE SET hash=excluded.hash,
            size=excluded.size,base=excluded.base,author_id=excluded.author_id,author=excluded.author,
            color=excluded.color,seq=excluded.seq,at=excluded.at,cutoff=excluded.cutoff,engine=excluded.engine",
            params![room,target,record.hash,record.size,record.base,record.author_id,record.author,
                record.color,record.seq,record.at,record.cutoff,record.engine])?;
        tx.execute(
            "DELETE FROM ops WHERE room=? AND target=? AND seq<=?",
            params![room, target, head.cutoff],
        )?;
        json!({"type":"pkgPublished","record":record})
    } else if let Some(raw) = msg.ops.filter(|_| ops) {
        let deleted: bool = tx.query_row(
            "SELECT EXISTS(SELECT 1 FROM package_removals WHERE room=? AND package=?)",
            params![room, target],
            |row| row.get(0),
        )?;
        if deleted {
            conflict(peer, target, &msg.operation_id);
            return Ok(());
        }
        head.seq += 1;
        tx.execute(
            "INSERT INTO ops(room,target,seq,kind,author_id,ops,at) VALUES(?,?,?,?,?,?,?)",
            params![
                room,
                target,
                head.seq,
                msg.op_kind,
                peer.author,
                raw.get(),
                at
            ],
        )?;
        if !msg.operation_id.is_empty() {
            tx.execute(
                "INSERT INTO op_receipts(room,author_id,operation_id,target,seq) VALUES(?,?,?,?,?)",
                params![room, peer.author, msg.operation_id, target, head.seq],
            )?;
        }
        json!({"type":"opsApplied","target":target,"seq":head.seq,"revision":head.revision,
               "kind":msg.op_kind,"authorId":peer.author,"senderId":peer.id,"operationId":msg.operation_id,"ops":raw})
    } else {
        let removal = remove_package(&tx, peer, target, "", head)?;
        tx.commit()?;
        broadcast(state, room, None, removal.event());
        return Ok(());
    };
    head.save(&tx, peer, target)?;
    tx.commit()?;
    broadcast(state, room, None, event);
    if let Some(removal) = removal {
        broadcast(state, room, None, removal.event());
    }
    Ok(())
}

fn replay(db: &Connection, peer: &Peer, msg: &Input<'_>) -> rusqlite::Result<()> {
    let target = &msg.target;
    let room = &peer.room;
    let head = read_head(db, room, target)?;
    if head.as_ref().is_some_and(|head| !head.matches(peer)) {
        notify(
            peer,
            json!({"type":"contentBaseline","target":target,"incompatible":true}),
        );
        return Ok(());
    }
    let head = head.unwrap_or_default();
    let removal = db
        .query_row(
            &format!("{REMOVAL_SELECT} WHERE r.room=? AND r.package=?"),
            params![room, target],
            Removal::read,
        )
        .optional()?;
    if let Some(removal) = removal {
        notify(peer, removal.event());
        return Ok(());
    }
    if msg.revision != head.revision || msg.since < head.cutoff || msg.since > head.seq {
        let record = db
            .query_row(
                &format!("SELECT {PACKAGE_COLUMNS} FROM packages WHERE room=? AND package=?"),
                params![room, target],
                db::read_package,
            )
            .optional()?;
        notify(
            peer,
            match record {
                Some(record) => json!({"type":"pkgPublished","record":record}),
                None => {
                    json!({"type":"contentBaseline","target":target,"revision":head.revision,"cutoff":head.cutoff})
                }
            },
        );
        return Ok(());
    }
    let mut query = db.prepare(
        "SELECT o.seq,o.kind,o.author_id,o.ops,o.at,COALESCE(r.operation_id,'') FROM ops o
        LEFT JOIN op_receipts r ON r.room=o.room AND r.target=o.target AND r.seq=o.seq
        WHERE o.room=? AND o.target=? AND o.seq>? ORDER BY o.seq",
    )?;
    let mut rows = query.query(params![room, target, msg.since])?;
    let mut entries = Vec::new();
    let mut size = 0;
    let mut complete = true;
    while let Some(row) = rows.next()? {
        let raw: String = row.get(3)?;
        if !entries.is_empty() && size + raw.len() > 48 * 1024 {
            complete = false;
            break;
        }
        size += raw.len();
        let ops = RawValue::from_string(raw).map_err(|err| {
            rusqlite::Error::FromSqlConversionFailure(3, rusqlite::types::Type::Text, Box::new(err))
        })?;
        entries.push(
            json!({"seq":row.get::<_,i64>(0)?,"kind":row.get::<_,String>(1)?,
            "authorId":row.get::<_,String>(2)?,"ops":ops,"at":row.get::<_,i64>(4)?,"operationId":row.get::<_,String>(5)?}),
        );
    }
    notify(
        peer,
        json!({"type":"opsBatch","target":target,"since":msg.since,"revision":head.revision,
                       "entries":entries,"complete":complete}),
    );
    Ok(())
}

pub fn send_packages(db: &Connection, peer: &Peer) -> rusqlite::Result<()> {
    let records = db.prepare(&format!("SELECT {PACKAGE_COLUMNS} FROM packages WHERE room=? AND base=? AND engine=? ORDER BY at"))?
        .query_map(params![peer.room,peer.commit,peer.engine], db::read_package)?
        .collect::<rusqlite::Result<Vec<_>>>()?;
    let removals = db
        .prepare(&format!(
            "{REMOVAL_SELECT} WHERE r.room=? AND h.base=? AND h.engine=? ORDER BY r.package"
        ))?
        .query_map(params![peer.room, peer.commit, peer.engine], Removal::read)?
        .collect::<rusqlite::Result<Vec<_>>>()?;
    notify(
        peer,
        json!({"type":"packages","records":records,"removals":removals}),
    );
    Ok(())
}
