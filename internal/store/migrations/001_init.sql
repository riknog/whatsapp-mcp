-- Schema v1 of data.db (docs/01-DESIGN.md §4).
-- Times are Unix seconds (INTEGER). Text columns default to '' so scans never see NULL.

CREATE TABLE kv (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE chats (
  jid             TEXT PRIMARY KEY,                 -- canonical (PN when known, else LID)
  ref             TEXT NOT NULL UNIQUE,             -- 'c_' + base32(HMAC(secret, jid))[:10]
  kind            TEXT NOT NULL CHECK (kind IN ('direct', 'group')),
  display_name    TEXT NOT NULL DEFAULT '',
  last_message_at INTEGER NOT NULL DEFAULT 0,
  agent_cursor    INTEGER NOT NULL DEFAULT 0,       -- messages.pk of the last message the agent consumed (only moves forward)
  owner_read_at   INTEGER NOT NULL DEFAULT 0,       -- last read-self from the owner's phone
  hidden          INTEGER NOT NULL DEFAULT 0 CHECK (hidden IN (0, 1))
);
CREATE INDEX chats_last_message_idx ON chats (last_message_at);

CREATE TABLE jid_aliases (
  alias_jid     TEXT PRIMARY KEY,                   -- LID
  canonical_jid TEXT NOT NULL                       -- PN
);

CREATE TABLE contacts (
  jid           TEXT PRIMARY KEY,
  full_name     TEXT NOT NULL DEFAULT '',
  first_name    TEXT NOT NULL DEFAULT '',
  push_name     TEXT NOT NULL DEFAULT '',
  business_name TEXT NOT NULL DEFAULT '',
  updated_at    INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE messages (
  pk         INTEGER PRIMARY KEY AUTOINCREMENT,    -- ingestion sequence; rowid alias, so it survives VACUUM and never reuses a value
  chat_jid   TEXT NOT NULL,
  id         TEXT NOT NULL,
  sender_jid TEXT NOT NULL DEFAULT '',
  from_me    INTEGER NOT NULL CHECK (from_me IN (0, 1)),
  ts         INTEGER NOT NULL,
  kind       TEXT NOT NULL,                         -- text|image|audio|video|document|sticker|location|contact|other
  text       TEXT NOT NULL DEFAULT '',
  caption    TEXT NOT NULL DEFAULT '',
  quoted_id  TEXT NOT NULL DEFAULT '',
  UNIQUE (chat_jid, id)                             -- target of ON CONFLICT DO NOTHING
);
CREATE INDEX messages_chat_ts_idx ON messages (chat_jid, ts);
CREATE INDEX messages_ts_idx ON messages (ts);

-- External-content FTS5 index keyed by messages.pk (stable across VACUUM), so the triggers
-- below must keep it in sync: insert adds, delete removes with the 'delete'
-- command (which needs the exact old values), update is delete + insert.
CREATE VIRTUAL TABLE messages_fts USING fts5(
  text, caption,
  content='messages',
  content_rowid='pk',
  tokenize='unicode61 remove_diacritics 2'
);

CREATE TRIGGER messages_fts_ai AFTER INSERT ON messages BEGIN
  INSERT INTO messages_fts (rowid, text, caption) VALUES (new.pk, new.text, new.caption);
END;

CREATE TRIGGER messages_fts_ad AFTER DELETE ON messages BEGIN
  INSERT INTO messages_fts (messages_fts, rowid, text, caption) VALUES ('delete', old.pk, old.text, old.caption);
END;

CREATE TRIGGER messages_fts_au AFTER UPDATE ON messages BEGIN
  INSERT INTO messages_fts (messages_fts, rowid, text, caption) VALUES ('delete', old.pk, old.text, old.caption);
  INSERT INTO messages_fts (rowid, text, caption) VALUES (new.pk, new.text, new.caption);
END;

CREATE TABLE shareable_contacts (
  jid      TEXT PRIMARY KEY,
  added_at INTEGER NOT NULL
);

CREATE TABLE labels (
  id      TEXT PRIMARY KEY,
  name    TEXT NOT NULL,
  color   INTEGER NOT NULL DEFAULT 0,
  deleted INTEGER NOT NULL DEFAULT 0 CHECK (deleted IN (0, 1)),
  source  TEXT NOT NULL CHECK (source IN ('whatsapp', 'local'))
);

CREATE TABLE chat_labels (
  chat_jid TEXT NOT NULL,
  label_id TEXT NOT NULL,
  PRIMARY KEY (chat_jid, label_id)
);
CREATE INDEX chat_labels_label_idx ON chat_labels (label_id);

CREATE TABLE send_queue (
  id             INTEGER PRIMARY KEY,
  chat_jid       TEXT NOT NULL,
  kind           TEXT NOT NULL CHECK (kind IN ('text', 'contact')),
  text           TEXT NOT NULL DEFAULT '',
  shared_jid     TEXT NOT NULL DEFAULT '',
  quoted_id      TEXT NOT NULL DEFAULT '',
  text_hash      TEXT NOT NULL DEFAULT '',          -- sha256 hex of normalized text (or of shared_jid for cards); added in T03
  status         TEXT NOT NULL CHECK (status IN ('queued', 'sending', 'sent', 'failed', 'expired', 'rejected')),
  enqueued_at    INTEGER NOT NULL,
  sent_at        INTEGER NOT NULL DEFAULT 0,
  wa_message_id  TEXT NOT NULL DEFAULT '',
  error          TEXT NOT NULL DEFAULT ''
);
CREATE INDEX send_queue_status_idx ON send_queue (status, enqueued_at);
CREATE INDEX send_queue_sent_idx ON send_queue (status, sent_at);
CREATE INDEX send_queue_dup_idx ON send_queue (chat_jid, text_hash, enqueued_at);

CREATE TABLE audit_log (
  id      INTEGER PRIMARY KEY,
  ts      INTEGER NOT NULL,
  action  TEXT NOT NULL,
  chat_ref TEXT NOT NULL DEFAULT '',
  detail  TEXT NOT NULL DEFAULT ''                  -- no content, no phone, no JID
);
CREATE INDEX audit_log_ts_idx ON audit_log (ts);
