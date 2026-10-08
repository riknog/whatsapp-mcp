-- Schema v2: what read_media needs to download an audio or image message on
-- demand, and the text extracted from it.
-- The row belongs to its message (messages.pk): retention, purges and alias
-- merges remove or keep it with the message, through ON DELETE CASCADE.
-- The keys only decrypt that one file; they never leave the process.

CREATE TABLE media (
  pk              INTEGER PRIMARY KEY REFERENCES messages (pk) ON DELETE CASCADE,
  kind            TEXT NOT NULL CHECK (kind IN ('image', 'audio')),
  mimetype        TEXT NOT NULL DEFAULT '',
  direct_path     TEXT NOT NULL DEFAULT '',
  media_key       BLOB NOT NULL,
  file_sha256     BLOB NOT NULL,
  file_enc_sha256 BLOB NOT NULL,
  file_length     INTEGER NOT NULL DEFAULT 0,
  extracted       TEXT NOT NULL DEFAULT '',    -- transcription or OCR text, raw (redacted when shown)
  extracted_by    TEXT NOT NULL DEFAULT '',    -- 'transcription' | 'ocr' | ''
  extracted_at    INTEGER NOT NULL DEFAULT 0
);
