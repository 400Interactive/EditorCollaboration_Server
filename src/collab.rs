use rusqlite::{Connection, params};
use serde_json::{Map, Value, json};

use crate::{
    Input, Peer, RoomState, SessionId, broadcast, db, error, new_id, notify, now_millis,
    valid_package,
};
use db::{Asset, Chat, Comment};

fn valid_anchor(anchor: &Value) -> bool {
    matches!(
        anchor.get("kind").and_then(Value::as_str),
        Some("map" | "graph" | "asset")
    ) && anchor.to_string().len() <= 4096
}

fn clean_text(text: &str) -> Option<&str> {
    let text = text.trim();
    (!text.is_empty() && text.chars().count() <= 2000).then_some(text)
}

fn message(peer: &Peer, text: &str) -> Chat {
    Chat {
        id: new_id(),
        author_id: peer.author.clone(),
        author: peer.name(),
        color: peer.color().into(),
        text: text.into(),
        at: now_millis(),
        edited_at: 0,
    }
}

fn authored(message: &Chat, peer: &Peer) -> bool {
    if message.author_id.is_empty() {
        message.author == peer.name()
    } else {
        message.author_id == peer.author
    }
}

fn online(state: &RoomState, room: &str, author: &str) -> bool {
    state
        .clients
        .values()
        .any(|peer| peer.room == room && peer.author == author)
}

fn save_asset(state: &RoomState, room: &str, asset: &Asset) -> rusqlite::Result<()> {
    let assets = db::assets(&state.db, room)?;
    let own = assets
        .iter()
        .find(|old| old.package == asset.package && old.author_id == asset.author_id);
    let other = assets.iter().find(|old| {
        old.package == asset.package
            && old.author_id != asset.author_id
            && old.author == asset.author
    });
    if other.is_some_and(|other| online(state, room, &other.author_id))
        || (own.is_none() && other.is_none() && assets.len() >= 4000)
    {
        return Ok(());
    }
    let tx = state.db.unchecked_transaction()?;
    db::put_asset(&tx, room, asset)?;
    tx.commit()?;
    if let Some(old) = own.filter(|old| old.author != asset.author) {
        broadcast(
            state,
            room,
            None,
            json!({"type":"assetCleared","package":asset.package,"author":old.author}),
        );
    }
    broadcast(
        state,
        room,
        None,
        json!({"type":"assetModified","record":asset}),
    );
    Ok(())
}

pub fn clear_working(state: &RoomState, peer: &Peer) -> rusqlite::Result<()> {
    let mut assets = db::assets(&state.db, &peer.room)?;
    assets.retain(|asset| asset.author_id == peer.author && asset.working);
    let tx = state.db.unchecked_transaction()?;
    tx.execute(
        "UPDATE assets SET working=0 WHERE room=? AND author_id=?",
        params![peer.room, peer.author],
    )?;
    tx.commit()?;
    for mut asset in assets {
        asset.working = false;
        broadcast(
            state,
            &peer.room,
            None,
            json!({"type":"assetModified","record":asset}),
        );
    }
    Ok(())
}

pub fn presence(
    state: &mut RoomState,
    id: SessionId,
    delta: Map<String, Value>,
) -> rusqlite::Result<()> {
    if delta.is_empty() {
        return Ok(());
    }
    let peer = state.clients.get_mut(&id).expect("registered peer");
    let old_name = peer.name();
    for (key, value) in &delta {
        if value.is_null() {
            peer.state.remove(key);
        } else {
            peer.state.insert(key.clone(), value.clone());
        }
    }
    let peer = &state.clients[&id];
    broadcast(
        state,
        &peer.room,
        Some(id),
        json!({"type":"presence","id":peer.id,"state":delta}),
    );
    let name = peer.name();
    if old_name != name {
        for mut asset in db::assets(&state.db, &peer.room)? {
            if asset.author_id == peer.author && asset.author != name {
                asset.author.clone_from(&name);
                save_asset(state, &peer.room, &asset)?;
            }
        }
    }
    Ok(())
}

pub fn handle(state: &RoomState, peer: &Peer, msg: &Input<'_>) -> rusqlite::Result<()> {
    let room = &peer.room;
    let db = &state.db;
    match msg.kind.as_str() {
        "chat" => {
            let Some(text) = clean_text(&msg.text) else {
                return Ok(());
            };
            let chat = message(peer, text);
            let tx = db.unchecked_transaction()?;
            db::insert_chat(&tx, room, &chat)?;
            db::trim_chat(&tx, room)?;
            tx.commit()?;
            broadcast(state, room, None, json!({"type":"chat","message":chat}));
        }
        "chat.history" if !msg.before.is_empty() => {
            let (messages, complete) = db::chat_page(db, room, &msg.before)?;
            notify(
                peer,
                json!({"type":"chatOlder","before":msg.before,"messages":messages,"complete":complete}),
            );
        }
        "comment.create" => {
            let Some(text) = clean_text(&msg.text).filter(|_| valid_anchor(&msg.anchor)) else {
                notify(peer, error("comment", "invalid", "Invalid comment."));
                return Ok(());
            };
            let count: i64 =
                db.query_row("SELECT COUNT(*) FROM comments WHERE room=?", [room], |r| {
                    r.get(0)
                })?;
            if count >= 2000 {
                notify(
                    peer,
                    error("comment", "full", "This room has too many comments."),
                );
                return Ok(());
            }
            let comment = Comment {
                id: new_id(),
                anchor: msg.anchor.clone(),
                resolved: false,
                created_at: now_millis(),
                messages: vec![message(peer, text)],
            };
            db::insert_comment(db, room, &comment)?;
            broadcast(
                state,
                room,
                None,
                json!({"type":"comment","comment":comment}),
            );
        }
        "comment.reply" | "comment.resolve" | "comment.move" | "comment.edit"
        | "comment.delete" => {
            edit_comment(state, peer, msg)?;
        }
        "ping" if valid_anchor(&msg.anchor) => {
            let seconds = if (10..=20).contains(&msg.seconds) {
                msg.seconds
            } else {
                15
            };
            broadcast(
                state,
                room,
                None,
                json!({"type":"ping","id":new_id(),"anchor":msg.anchor,
                "authorId":peer.author,"author":peer.name(),"color":peer.color(),"at":now_millis(),"seconds":seconds}),
            );
        }
        "asset.modify" => {
            let asset = if msg.asset.is_empty() {
                &msg.package
            } else {
                &msg.asset
            };
            if valid_package(&msg.package)
                && valid_package(asset)
                && msg.hash.len() <= 64
                && msg.hash.bytes().all(|byte| byte.is_ascii_hexdigit())
            {
                save_asset(
                    state,
                    room,
                    &Asset {
                        package: msg.package.clone(),
                        asset: asset.clone(),
                        hash: msg.hash.to_ascii_lowercase(),
                        dirty: msg.dirty,
                        working: msg.working,
                        author_id: peer.author.clone(),
                        author: peer.name(),
                        color: peer.color().into(),
                        at: now_millis(),
                    },
                )?;
            }
        }
        "asset.clear" if valid_package(&msg.package) => {
            let assets = db::assets(db, room)?;
            let name = peer.name();
            let mut candidates = assets.iter().filter(|asset| asset.package == msg.package);
            let record = candidates
                .clone()
                .find(|asset| asset.author_id == peer.author)
                .or_else(|| {
                    candidates.find(|asset| {
                        asset.author == name && !online(state, room, &asset.author_id)
                    })
                });
            if let Some(record) = record {
                db::delete_asset(db, room, record)?;
                broadcast(
                    state,
                    room,
                    None,
                    json!({"type":"assetCleared","package":msg.package,"author":record.author}),
                );
            }
        }
        _ => {}
    }
    Ok(())
}

fn edit_comment(state: &RoomState, peer: &Peer, msg: &Input<'_>) -> rusqlite::Result<()> {
    let room = &peer.room;
    let db: &Connection = &state.db;
    let text = clean_text(&msg.text);
    if msg.kind == "comment.edit" && text.is_none() {
        notify(peer, error("comment", "invalid", "Invalid comment."));
        return Ok(());
    }
    let Some(mut comment) = db::comment(db, room, &msg.id)? else {
        return Ok(());
    };
    match msg.kind.as_str() {
        "comment.reply" if comment.messages.len() < 500 => {
            let Some(text) = text else {
                return Ok(());
            };
            comment.messages.push(message(peer, text));
        }
        "comment.resolve" => {
            let Some(resolved) = msg.resolved else {
                return Ok(());
            };
            comment.resolved = resolved;
        }
        "comment.move" if valid_anchor(&msg.anchor) => comment.anchor = msg.anchor.clone(),
        "comment.edit" => {
            let Some(item) = comment
                .messages
                .iter_mut()
                .find(|item| item.id == msg.message_id)
            else {
                return Ok(());
            };
            if !authored(item, peer) {
                notify(
                    peer,
                    error(
                        "comment",
                        "notMessageAuthor",
                        "Only the author can edit this message.",
                    ),
                );
                return Ok(());
            }
            item.text = text.expect("validated text").into();
            item.edited_at = now_millis();
        }
        "comment.delete" => {
            if !comment
                .messages
                .first()
                .is_some_and(|message| authored(message, peer))
            {
                notify(
                    peer,
                    error(
                        "comment",
                        "notAuthor",
                        "Only the author can delete this comment.",
                    ),
                );
                return Ok(());
            }
            db.execute(
                "DELETE FROM comments WHERE room=? AND id=?",
                params![room, comment.id],
            )?;
            broadcast(
                state,
                room,
                None,
                json!({"type":"commentDeleted","id":comment.id}),
            );
            return Ok(());
        }
        _ => return Ok(()),
    }
    db::save_comment(db, room, &comment)?;
    broadcast(
        state,
        room,
        None,
        json!({"type":"comment","comment":comment}),
    );
    Ok(())
}
