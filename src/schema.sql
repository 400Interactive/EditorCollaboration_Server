CREATE TABLE IF NOT EXISTS chat (
	seq       INTEGER PRIMARY KEY,
	room      TEXT NOT NULL,
	id        TEXT NOT NULL UNIQUE,
	author_id TEXT NOT NULL,
	author    TEXT NOT NULL,
	color     TEXT NOT NULL,
	text      TEXT NOT NULL,
	at        INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS chat_by_room ON chat(room, seq);

CREATE TABLE IF NOT EXISTS comments (
	seq        INTEGER PRIMARY KEY,
	room       TEXT NOT NULL,
	id         TEXT NOT NULL UNIQUE,
	anchor     TEXT NOT NULL,
	resolved   INTEGER NOT NULL,
	created_at INTEGER NOT NULL,
	messages   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS comments_by_room ON comments(room, seq);

CREATE TABLE IF NOT EXISTS assets (
	room      TEXT NOT NULL,
	package   TEXT NOT NULL,
	author    TEXT NOT NULL,
	asset     TEXT NOT NULL,
	hash      TEXT NOT NULL,
	dirty     INTEGER NOT NULL,
	working   INTEGER NOT NULL DEFAULT 0,
	author_id TEXT NOT NULL,
	color     TEXT NOT NULL,
	at        INTEGER NOT NULL,
	PRIMARY KEY (room, package, author)
);

CREATE TABLE IF NOT EXISTS packages (
	room      TEXT NOT NULL,
	package   TEXT NOT NULL,
	hash      TEXT NOT NULL,
	size      INTEGER NOT NULL,
	base      TEXT NOT NULL,
	author_id TEXT NOT NULL,
	author    TEXT NOT NULL,
	color     TEXT NOT NULL,
	seq       INTEGER NOT NULL,
	at        INTEGER NOT NULL,
	cutoff    INTEGER NOT NULL DEFAULT 0,
	engine    TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (room, package)
);

CREATE TABLE IF NOT EXISTS ops (
	room      TEXT NOT NULL,
	target    TEXT NOT NULL,
	seq       INTEGER NOT NULL,
	kind      TEXT NOT NULL,
	author_id TEXT NOT NULL,
	ops       TEXT NOT NULL,
	at        INTEGER NOT NULL,
	PRIMARY KEY (room, target, seq)
);
CREATE INDEX IF NOT EXISTS ops_by_target ON ops(room, target, seq);

CREATE TABLE IF NOT EXISTS content_heads (
	room TEXT NOT NULL,
	target TEXT NOT NULL,
	seq INTEGER NOT NULL,
	revision INTEGER NOT NULL,
	cutoff INTEGER NOT NULL,
	base TEXT NOT NULL,
	engine TEXT NOT NULL,
	PRIMARY KEY(room, target)
);

CREATE TABLE IF NOT EXISTS op_receipts (
    room TEXT NOT NULL,
    author_id TEXT NOT NULL,
    operation_id TEXT NOT NULL,
    target TEXT NOT NULL,
    seq INTEGER NOT NULL,
    PRIMARY KEY(room, author_id, operation_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS op_receipts_by_sequence ON op_receipts(room,target,seq);

CREATE TABLE IF NOT EXISTS package_removals (
    room TEXT NOT NULL,
    package TEXT NOT NULL,
    author_id TEXT NOT NULL,
    renamed_to TEXT NOT NULL,
    PRIMARY KEY(room, package)
);

CREATE TABLE IF NOT EXISTS room_blobs (
	room TEXT NOT NULL,
	hash TEXT NOT NULL,
	PRIMARY KEY(room, hash)
);

CREATE TABLE IF NOT EXISTS blobs (
	hash TEXT PRIMARY KEY,
	size INTEGER NOT NULL,
	at   INTEGER NOT NULL
);
