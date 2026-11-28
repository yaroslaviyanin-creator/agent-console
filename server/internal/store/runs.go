package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"time"

	"agent-console/server/internal/provider"
	"agent-console/server/internal/uploads"
)

var (
	ErrDuplicateRun        = errors.New("run ID already used in this chat")
	ErrProviderMismatch    = errors.New("chat provider mismatch")
	ErrProviderUnavailable = errors.New("provider is not available")
)

func isRunnableProvider(id string) bool {
	switch id {
	case "antigravity", "codex", "claude", "paid_api", "stub":
		return true
	default:
		return false
	}
}

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
	sessionID, _, err := s.beginRun(ctx, userID, "", chatID, messageID, prompt, backend, model, nil, 10, "")
	return sessionID, err
}

// BeginRunForDevice checks current access and the available quota in one transaction.
func (s *Store) BeginRunForDevice(ctx context.Context, userID, deviceID, chatID, messageID, prompt, backend, model string) (string, error) {
	sessionID, _, err := s.beginRun(ctx, userID, deviceID, chatID, messageID, prompt, backend, model, nil, 10, "")
	return sessionID, err
}

// BeginRunForDeviceWithAttachments checks access, quota, and atomically binds uploaded attachments to the user message.
func (s *Store) BeginRunForDeviceWithAttachments(ctx context.Context, userID, deviceID, chatID, messageID, prompt, backend, model string, attachmentIDs []string) (string, []Attachment, error) {
	return s.beginRun(ctx, userID, deviceID, chatID, messageID, prompt, backend, model, attachmentIDs, 10, "")
}

// BeginRunForDeviceWithAttachmentsLimit enforces a configurable maximum attachment count per message.
func (s *Store) BeginRunForDeviceWithAttachmentsLimit(ctx context.Context, userID, deviceID, chatID, messageID, prompt, backend, model string, attachmentIDs []string, maxAttachments int) (string, []Attachment, error) {
	return s.beginRun(ctx, userID, deviceID, chatID, messageID, prompt, backend, model, attachmentIDs, maxAttachments, "")
}

// BeginRunForDeviceWithAttachmentsReuse starts a run either inserting a new user message or reusing an existing one (regenerate/edit).
func (s *Store) BeginRunForDeviceWithAttachmentsReuse(ctx context.Context, userID, deviceID, chatID, messageID, prompt, backend, model string, attachmentIDs []string, maxAttachments int, reuseUserMessageID string) (string, []Attachment, error) {
	return s.beginRun(ctx, userID, deviceID, chatID, messageID, prompt, backend, model, attachmentIDs, maxAttachments, reuseUserMessageID)
}

func (s *Store) beginRun(ctx context.Context, userID, deviceID, chatID, messageID, prompt, backend, model string, attachmentIDs []string, maxAttachments int, reuseUserMessageID string) (string, []Attachment, error) {
	if !isRunnableProvider(backend) {
		return "", nil, ErrProviderUnavailable
	}
	if maxAttachments <= 0 {
		maxAttachments = 10
	}
	if len(attachmentIDs) > maxAttachments {
		return "", nil, ErrInvalidAttachments
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", nil, err
	}
	defer tx.Rollback()

	var previousBackend, sessionProvider string
	var session sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT provider, CASE WHEN session_provider='' THEN provider ELSE session_provider END, provider_session_id FROM chats c WHERE `+chatWriteAccess,
		chatID, userID).Scan(&previousBackend, &sessionProvider, &session); err != nil {
		return "", nil, err
	}

	now := time.Now()
	if deviceID != "" {
		if err := checkRunAccess(ctx, tx, userID, deviceID, now); err != nil {
			return "", nil, err
		}
	}

	var attached []Attachment
	var userMsgID string
	if reuseUserMessageID != "" {
		var existingRunID sql.NullString
		err := tx.QueryRowContext(ctx, `SELECT id, content, run_id FROM chat_messages WHERE id=? AND chat_id=? AND role='user'`,
			reuseUserMessageID, chatID).Scan(&userMsgID, &prompt, &existingRunID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return "", nil, sql.ErrNoRows
			}
			return "", nil, err
		}
		res, err := tx.ExecContext(ctx, `UPDATE chat_messages SET run_id=?, run_user_id=? WHERE id=? AND chat_id=? AND role='user'`,
			messageID, userID, userMsgID, chatID)
		if err != nil {
			return "", nil, err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return "", nil, ErrDuplicateRun
		}
		attRows, err := tx.QueryContext(ctx, `SELECT id,chat_id,message_id,user_id,original_name,mime_type,kind,size_bytes,sha256,storage_key,COALESCE(extracted_text,''),warning,created_at
			FROM chat_attachments WHERE chat_id=? AND message_id=? ORDER BY created_at,rowid`, chatID, userMsgID)
		if err != nil {
			return "", nil, err
		}
		for attRows.Next() {
			var att Attachment
			var mID sql.NullString
			if err := attRows.Scan(&att.ID, &att.ChatID, &mID, &att.UserID, &att.OriginalName, &att.MIMEType, &att.Kind, &att.SizeBytes, &att.SHA256, &att.StorageKey, &att.ExtractedText, &att.Warning, &att.CreatedAt); err != nil {
				attRows.Close()
				return "", nil, err
			}
			att.MessageID = mID.String
			att.URL = "/api/attachments/" + att.ID
			attached = append(attached, att)
		}
		if err := attRows.Err(); err != nil {
			attRows.Close()
			return "", nil, err
		}
		attRows.Close()
	} else {
		if len(attachmentIDs) > 0 {
			seen := make(map[string]bool, len(attachmentIDs))
			for _, attID := range attachmentIDs {
				if attID == "" || seen[attID] {
					return "", nil, ErrInvalidAttachments
				}
				seen[attID] = true
				var att Attachment
				var extracted sql.NullString
				err := tx.QueryRowContext(ctx, `SELECT id,chat_id,user_id,original_name,mime_type,kind,size_bytes,sha256,storage_key,extracted_text,warning,created_at
					FROM chat_attachments WHERE id=? AND chat_id=? AND user_id=? AND message_id IS NULL`,
					attID, chatID, userID).
					Scan(&att.ID, &att.ChatID, &att.UserID, &att.OriginalName, &att.MIMEType, &att.Kind, &att.SizeBytes, &att.SHA256, &att.StorageKey, &extracted, &att.Warning, &att.CreatedAt)
				if err != nil {
					if errors.Is(err, sql.ErrNoRows) {
						return "", nil, ErrInvalidAttachments
					}
					return "", nil, err
				}
				att.ExtractedText = extracted.String
				att.URL = "/api/attachments/" + att.ID
				attached = append(attached, att)
			}
		}

		userMsgID = randomHex(16)
		result, err := tx.ExecContext(ctx, `INSERT INTO chat_messages (id,chat_id,role,content,created_at,run_id,run_user_id,memory_private)
			VALUES (?,?,'user',?,?,?,?,NOT EXISTS(SELECT 1 FROM chat_shares WHERE chat_id=?)) ON CONFLICT(chat_id,role,run_id) DO NOTHING`,
			userMsgID, chatID, prompt, now.UnixMilli(), messageID, userID, chatID)
		if err != nil {
			return "", nil, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return "", nil, err
		}
		if count == 0 {
			return "", nil, ErrDuplicateRun
		}
		for i := range attached {
			res, err := tx.ExecContext(ctx, `UPDATE chat_attachments SET message_id=? WHERE id=? AND chat_id=? AND user_id=? AND message_id IS NULL`,
				userMsgID, attached[i].ID, chatID, userID)
			if err != nil {
				return "", nil, err
			}
			if n, _ := res.RowsAffected(); n != 1 {
				return "", nil, ErrInvalidAttachments
			}
			attached[i].MessageID = userMsgID
		}
	}

	titleSeed := prompt
	if titleSeed == "" && len(attached) > 0 {
		titleSeed = attached[0].OriginalName
	}
	if backend == previousBackend {
		if _, err := tx.ExecContext(ctx, `UPDATE chats SET model=?,updated_at=MAX(updated_at,?),
			title=CASE WHEN title='Новый чат' THEN substr(?,1,80) ELSE title END WHERE id=?`,
			model, now.UnixMilli(), titleSeed, chatID); err != nil {
			return "", nil, err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `UPDATE chats SET updated_at=MAX(updated_at,?),
			title=CASE WHEN title='Новый чат' THEN substr(?,1,80) ELSE title END WHERE id=?`,
			now.UnixMilli(), titleSeed, chatID); err != nil {
			return "", nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", nil, err
	}
	if backend != sessionProvider {
		return "", attached, nil
	}
	return session.String, attached, nil
}

// FinishRun publishes a durable reply and resume session in a single transaction.
func (s *Store) FinishRun(ctx context.Context, userID, chatID, messageID, text, thinking, sessionID string, durationMs int64, usage *provider.Usage) error {
	return s.FinishRunWithProvider(ctx, userID, chatID, messageID, "", "", text, thinking, sessionID, durationMs, usage)
}

// FinishRunWithProvider publishes a durable reply and updates the chat's active provider, model, and session binding.
func (s *Store) FinishRunWithProvider(ctx context.Context, userID, chatID, messageID, targetProvider, targetModel, text, thinking, sessionID string, durationMs int64, usage *provider.Usage) error {
	_, err := s.FinishRunWithProviderAndArtifacts(ctx, userID, chatID, messageID, targetProvider, targetModel, text, thinking, sessionID, durationMs, usage, nil)
	return err
}

// FinishRunWithProviderAndArtifacts publishes a durable reply, persists assistant-generated artifacts, and updates the chat binding atomically.
func (s *Store) FinishRunWithProviderAndArtifacts(ctx context.Context, userID, chatID, messageID, targetProvider, targetModel, text, thinking, sessionID string, durationMs int64, usage *provider.Usage, artifacts []uploads.ValidatedAttachment) ([]MessageAttachment, error) {
	if targetProvider != "" && !isRunnableProvider(targetProvider) {
		return nil, ErrProviderUnavailable
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var ownerID, prompt, backend, model string
	var memoryPrivate bool
	err = tx.QueryRowContext(ctx, `SELECT c.user_id,m.content,c.provider,c.model,m.memory_private FROM chat_messages m JOIN chats c ON c.id=m.chat_id
		WHERE m.chat_id=? AND m.run_id=? AND m.role='user' AND m.run_user_id=?`,
		chatID, messageID, userID).Scan(&ownerID, &prompt, &backend, &model, &memoryPrivate)
	if err != nil {
		return nil, err
	}
	if targetProvider != "" {
		backend = targetProvider
	}
	if targetModel != "" {
		model = targetModel
	}
	counts, cents := priceUsage(usage, model, prompt, text, thinking)
	now := time.Now().UnixMilli()
	if _, err := tx.ExecContext(ctx, `INSERT INTO usage_events
		(id,user_id,chat_id,run_id,provider,model,input_tokens,cached_input_tokens,output_tokens,reasoning_tokens,cost_cents,created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, randomHex(16), userID, chatID, messageID, backend, model,
		counts.InputTokens, counts.CachedInputTokens, counts.OutputTokens, counts.ReasoningTokens, cents, now); err != nil {
		return nil, err
	}
	assistantMsgID := randomHex(16)
	if _, err := tx.ExecContext(ctx, `INSERT INTO chat_messages (id,chat_id,role,content,thinking,created_at,run_id,duration_ms,memory_private)
		VALUES (?,?,'assistant',?,?,?,?,?,?)`, assistantMsgID, chatID, text, thinking, now, messageID, durationMs, memoryPrivate); err != nil {
		return nil, err
	}

	var saved []MessageAttachment
	var writtenPaths []string
	rollbackFiles := func() {
		for _, p := range writtenPaths {
			_ = os.Remove(p)
		}
	}
	for _, art := range artifacts {
		att, diskPath, err := s.createAssistantAttachmentTx(ctx, tx, chatID, ownerID, assistantMsgID, art, now)
		if err != nil {
			rollbackFiles()
			return nil, err
		}
		writtenPaths = append(writtenPaths, diskPath)
		saved = append(saved, att)
	}

	if _, err := tx.ExecContext(ctx, `UPDATE chats SET provider=?,model=?,session_provider=?,provider_session_id=NULLIF(?,''),updated_at=MAX(updated_at,?) WHERE id=?`,
		backend, model, backend, sessionID, now, chatID); err != nil {
		rollbackFiles()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		rollbackFiles()
		return nil, err
	}
	return saved, nil
}

// SaveRunWebSources persists SSRF-fetched URL metadata onto the user message row for this run.
func (s *Store) SaveRunWebSources(ctx context.Context, chatID, runID string, sources []WebSource) error {
	if len(sources) == 0 {
		return nil
	}
	encoded, err := json.Marshal(sources)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE chat_messages SET web_sources_json=? WHERE chat_id=? AND run_id=? AND role='user'`, string(encoded), chatID, runID)
	return err
}

// ChatPriorMessages loads ordered conversation messages in a chat before the specified run ID.
func (s *Store) ChatPriorMessages(ctx context.Context, chatID, excludeRunID string) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,role,content,created_at FROM chat_messages
		WHERE chat_id=?1 AND (?2='' OR COALESCE(run_id,'')!=?2)
		ORDER BY created_at,rowid`, chatID, excludeRunID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []Message
	for rows.Next() {
		var m Message
		m.ChatID = chatID
		if err := rows.Scan(&m.ID, &m.Role, &m.Content, &m.CreatedAt); err != nil {
			return nil, err
		}
		messages = append(messages, m)
	}
	return messages, rows.Err()
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

// ChatDispatchInfo verifies write access and returns the chat owner ID and stored provider.
func (s *Store) ChatDispatchInfo(ctx context.Context, userID, chatID string) (ownerID string, provider string, err error) {
	err = s.db.QueryRowContext(ctx, "SELECT c.user_id, c.provider FROM chats c WHERE "+chatWriteAccess, chatID, userID).Scan(&ownerID, &provider)
	return ownerID, provider, err
}
