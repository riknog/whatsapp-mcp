package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Media kinds stored in the media table.
const (
	MediaImage = "image"
	MediaAudio = "audio"
)

// Sources of Media.ExtractedBy.
const (
	ExtractedByTranscription = "transcription"
	ExtractedByOCR           = "ocr"
)

// insertMedia stores the media row of m, if it has one, for the message row
// that already exists with m's chat and id. A row already present is kept.
// It runs inside the caller's transaction.
func insertMedia(ctx context.Context, tx *sql.Tx, m Message) error {
	md := m.Media
	if md == nil {
		return nil
	}
	if md.Kind != MediaImage && md.Kind != MediaAudio {
		return nil
	}
	if md.DirectPath == "" || len(md.MediaKey) == 0 {
		return nil // nothing to download with
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO media (pk, kind, mimetype, direct_path, media_key, file_sha256, file_enc_sha256, file_length)
		SELECT pk, ?, ?, ?, ?, ?, ?, ? FROM messages WHERE chat_jid = ? AND id = ?
		ON CONFLICT (pk) DO NOTHING`,
		md.Kind, md.Mimetype, md.DirectPath, md.MediaKey, nonNil(md.FileSHA256), nonNil(md.FileEncSHA256),
		md.FileLength, m.ChatJID, m.ID)
	if err != nil {
		return fmt.Errorf("store: gravar mídia: %w", err)
	}
	return nil
}

func nonNil(b []byte) []byte {
	if b == nil {
		return []byte{}
	}
	return b
}

// GetMedia returns a message of a visible chat and its media row. The media is
// nil when the message has none (not audio or image, or stored without keys).
// ErrNotFound if the chat or the message is unknown, ErrHidden if the chat is
// hidden. It runs in one read-only snapshot.
func (s *Store) GetMedia(ctx context.Context, chatJID, id string) (Message, *Media, error) {
	tx, err := s.beginRead(ctx)
	if err != nil {
		return Message{}, nil, err
	}
	defer func() { _ = tx.Rollback() }()

	if err := requireVisibleChat(ctx, tx, chatJID); err != nil {
		return Message{}, nil, err
	}
	var m Message
	var fromMe int
	var md Media
	var kind sql.NullString
	var length, at sql.NullInt64
	var mime, path, extracted, by sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT `+messageColumns+`,
		       md.kind, md.mimetype, md.direct_path, md.media_key, md.file_sha256, md.file_enc_sha256,
		       md.file_length, md.extracted, md.extracted_by, md.extracted_at
		FROM messages m LEFT JOIN media md ON md.pk = m.pk
		WHERE m.chat_jid = ? AND m.id = ?`, chatJID, id).
		Scan(&m.ChatJID, &m.ID, &m.SenderJID, &fromMe, &m.TS, &m.Kind, &m.Text, &m.Caption, &m.QuotedID,
			&kind, &mime, &path, &md.MediaKey, &md.FileSHA256, &md.FileEncSHA256,
			&length, &extracted, &by, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, nil, ErrNotFound
	}
	if err != nil {
		return Message{}, nil, fmt.Errorf("store: ler mídia: %w", err)
	}
	m.FromMe = fromMe == 1
	if !kind.Valid {
		return m, nil, nil
	}
	md.Kind, md.Mimetype, md.DirectPath = kind.String, mime.String, path.String
	md.FileLength, md.Extracted, md.ExtractedBy, md.ExtractedAt = length.Int64, extracted.String, by.String, at.Int64
	return m, &md, nil
}

// SetMediaExtracted stores the text read from a message's media. ErrNotFound
// if the message has no media row.
func (s *Store) SetMediaExtracted(ctx context.Context, chatJID, id, text, by string) error {
	res, err := s.w.ExecContext(ctx, `
		UPDATE media SET extracted = ?, extracted_by = ?, extracted_at = ?
		WHERE pk = (SELECT pk FROM messages WHERE chat_jid = ? AND id = ?)`,
		text, by, s.now(), chatJID, id)
	if err != nil {
		return fmt.Errorf("store: gravar texto da mídia: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: gravar texto da mídia: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
