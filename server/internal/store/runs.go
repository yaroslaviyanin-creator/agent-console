package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var ErrDuplicateRun = errors.New("run ID already used in this chat")

// BeginRun verifies ownership and reserves the client message ID durably before calling the provider.
func (s *Store) BeginRun(ctx context.Context, userID, chatID, messageID, prompt, backend, model string) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	var previousBackend string
	var session sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT provider,provider_session_id FROM chats WHERE id=? AND user_id=?`,
		chatID, userID).Scan(&previousBackend, &session); err != nil {
		return "", err
	}
	if previousBackend != backend {
		session = sql.NullString{}
	}

	now := time.Now().UnixMilli()
	result, err := tx.ExecContext(ctx, `INSERT INTO chat_messages (id,chat_id,role,content,created_at,run_id)
		VALUES (?,?,'user',?,?,?) ON CONFLICT(chat_id,role,run_id) DO NOTHING`,
		randomHex(16), chatID, prompt, now, messageID)
	if err != nil {
		return "", err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return "", err
	}
	if count == 0 {
		return "", ErrDuplicateRun
	}
	if _, err := tx.ExecContext(ctx, `UPDATE chats SET provider=?,model=?,provider_session_id=?,updated_at=MAX(updated_at,?),
        title=CASE WHEN title='Новый чат' THEN substr(?,1,80) ELSE title END WHERE id=?`,
		backend, model, session, now, prompt, chatID); err != nil {
		return "", err
	}
	return session.String, tx.Commit()
}

// FinishRun publishes a durable reply and resume session in a single transaction.
func (s *Store) FinishRun(ctx context.Context, chatID, messageID, text, thinking, sessionID string, durationMs int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	now := time.Now().UnixMilli()
	if _, err := tx.ExecContext(ctx, `INSERT INTO chat_messages (id,chat_id,role,content,thinking,created_at,run_id,duration_ms)
		VALUES (?,?,'assistant',?,?,?,?,?)`, randomHex(16), chatID, text, thinking, now, messageID, durationMs); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE chats SET provider_session_id=NULLIF(?,''),updated_at=MAX(updated_at,?) WHERE id=?`,
		sessionID, now, chatID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CheckChat(ctx context.Context, userID, chatID string) error {
	var id string
	return s.db.QueryRowContext(ctx, "SELECT id FROM chats WHERE id=? AND user_id=?", chatID, userID).Scan(&id)
}
