package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"agent-console/server/internal/provider"
)

var ErrDuplicateRun = errors.New("run ID already used in this chat")

type RunDenied string

func (e RunDenied) Error() string { return string(e) }

func checkRunAccess(ctx context.Context, tx *sql.Tx, userID, deviceID string, now time.Time) error {
	user, _, err := activeDevice(ctx, tx, "d.user_id=? AND d.id=?", now.UnixMilli(), userID, deviceID)
	switch {
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, ErrInactiveUser):
		return RunDenied("session_revoked")
	case err != nil:
		return err
	case user.Role == "pending":
		return RunDenied("account_pending")
	case user.Role == "admin":
		return nil
	case user.SubscriptionUntil > 0 && now.UnixMilli() > user.SubscriptionUntil:
		return RunDenied("subscription_expired")
	case user.PlanTier == "none":
		return RunDenied("no_active_plan")
	case user.PlanTier == "unlimited":
		return nil
	}

	usage, err := userUsage(ctx, tx, userID, now)
	if err != nil {
		return err
	}
	limit5h, limit7d := user.Limits()
	if usage.sevenDay >= limit7d {
		return RunDenied("weekly_limit_reached")
	}
	if !user.Bypass5hLimit && usage.fiveHour >= limit5h {
		return RunDenied("five_hour_limit_reached")
	}
	return nil
}

// BeginRun verifies write access and reserves the client message ID durably before calling the provider.
func (s *Store) BeginRun(ctx context.Context, userID, chatID, messageID, prompt, backend, model string) (string, error) {
	return s.beginRun(ctx, userID, "", chatID, messageID, prompt, backend, model)
}

// BeginRunForDevice checks current access and the available quota in one transaction.
func (s *Store) BeginRunForDevice(ctx context.Context, userID, deviceID, chatID, messageID, prompt, backend, model string) (string, error) {
	return s.beginRun(ctx, userID, deviceID, chatID, messageID, prompt, backend, model)
}

func (s *Store) beginRun(ctx context.Context, userID, deviceID, chatID, messageID, prompt, backend, model string) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	var previousBackend string
	var session sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT provider,provider_session_id FROM chats c WHERE `+chatWriteAccess,
		chatID, userID).Scan(&previousBackend, &session); err != nil {
		return "", err
	}
	if previousBackend != backend {
		session = sql.NullString{}
	}

	now := time.Now()
	if deviceID != "" {
		if err := checkRunAccess(ctx, tx, userID, deviceID, now); err != nil {
			return "", err
		}
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO chat_messages (id,chat_id,role,content,created_at,run_id,run_user_id,memory_private)
		VALUES (?,?,'user',?,?,?,?,NOT EXISTS(SELECT 1 FROM chat_shares WHERE chat_id=?)) ON CONFLICT(chat_id,role,run_id) DO NOTHING`,
		randomHex(16), chatID, prompt, now.UnixMilli(), messageID, userID, chatID)
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
		backend, model, session, now.UnixMilli(), prompt, chatID); err != nil {
		return "", err
	}
	return session.String, tx.Commit()
}

// FinishRun publishes a durable reply and resume session in a single transaction.
func (s *Store) FinishRun(ctx context.Context, userID, chatID, messageID, text, thinking, sessionID string, durationMs int64, usage *provider.Usage) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var prompt, backend, model string
	var memoryPrivate bool
	err = tx.QueryRowContext(ctx, `SELECT m.content,c.provider,c.model,m.memory_private FROM chat_messages m JOIN chats c ON c.id=m.chat_id
		WHERE m.chat_id=? AND m.run_id=? AND m.role='user' AND m.run_user_id=?`,
		chatID, messageID, userID).Scan(&prompt, &backend, &model, &memoryPrivate)
	if err != nil {
		return err
	}
	counts, cents := priceUsage(usage, model, prompt, text, thinking)
	now := time.Now().UnixMilli()
	if _, err := tx.ExecContext(ctx, `INSERT INTO usage_events
		(id,user_id,chat_id,run_id,provider,model,input_tokens,cached_input_tokens,output_tokens,cost_cents,created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`, randomHex(16), userID, chatID, messageID, backend, model,
		counts.InputTokens, counts.CachedInputTokens, counts.OutputTokens, cents, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO chat_messages (id,chat_id,role,content,thinking,created_at,run_id,duration_ms,memory_private)
		VALUES (?,?,'assistant',?,?,?,?,?,?)`, randomHex(16), chatID, text, thinking, now, messageID, durationMs, memoryPrivate); err != nil {
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

const chatWriteAccess = `c.id=?1 AND (c.user_id=?2 OR EXISTS(
	SELECT 1 FROM chat_shares WHERE chat_id=c.id AND user_id=?2 AND access='write'
))`

func (s *Store) CheckChatWrite(ctx context.Context, userID, chatID string) error {
	_, err := s.ChatOwner(ctx, userID, chatID)
	return err
}

func (s *Store) ChatOwner(ctx context.Context, userID, chatID string) (string, error) {
	var ownerID string
	err := s.db.QueryRowContext(ctx, "SELECT c.user_id FROM chats c WHERE "+chatWriteAccess, chatID, userID).Scan(&ownerID)
	return ownerID, err
}
