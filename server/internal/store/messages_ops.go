package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"time"
)

func (s *Store) Messages(ctx context.Context, userID, chatID string) ([]Message, error) {
	return s.messages(ctx, userID, chatID, true)
}

func (s *Store) checkOwnerOrForbidden(ctx context.Context, tx *sql.Tx, userID, chatID string) error {
	var ownerID string
	var hasShare bool
	err := tx.QueryRowContext(ctx, `SELECT c.user_id, EXISTS(SELECT 1 FROM chat_shares WHERE chat_id=c.id AND user_id=?2)
		FROM chats c WHERE c.id=?1`, chatID, userID).Scan(&ownerID, &hasShare)
	if err != nil {
		return err
	}
	if ownerID == userID {
		return nil
	}
	if hasShare {
		return ErrForbidden
	}
	return sql.ErrNoRows
}

func (s *Store) checkWriteOrForbidden(ctx context.Context, tx *sql.Tx, userID, chatID string) error {
	var ownerID string
	var shareAccess sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT c.user_id, (SELECT access FROM chat_shares WHERE chat_id=c.id AND user_id=?2)
		FROM chats c WHERE c.id=?1`, chatID, userID).Scan(&ownerID, &shareAccess)
	if err != nil {
		return err
	}
	if ownerID == userID || shareAccess.String == "write" {
		return nil
	}
	if shareAccess.Valid {
		return ErrForbidden
	}
	return sql.ErrNoRows
}

// UpdateChat updates title and/or pinned state for a chat owned by userID.
func (s *Store) UpdateChat(ctx context.Context, userID, chatID string, title *string, pinned *bool) (Chat, error) {
	return s.UpdateChatWithOptions(ctx, userID, chatID, title, pinned, false, nil, nil)
}

// UpdateChatWithOptions updates title, pinned, folderId, and/or archived state for a chat owned by userID.
func (s *Store) UpdateChatWithOptions(ctx context.Context, userID, chatID string, title *string, pinned *bool, folderSet bool, folderID *string, archived *bool) (Chat, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Chat{}, err
	}
	defer tx.Rollback()

	if err := s.checkOwnerOrForbidden(ctx, tx, userID, chatID); err != nil {
		return Chat{}, err
	}

	var c Chat
	var curFolder sql.NullString
	var curArchived sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT id,title,provider,model,pinned,folder_id,archived_at,created_at,updated_at,user_id FROM chats WHERE id=?`, chatID).
		Scan(&c.ID, &c.Title, &c.Provider, &c.Model, &c.Pinned, &curFolder, &curArchived, &c.CreatedAt, &c.UpdatedAt, &c.OwnerID); err != nil {
		return Chat{}, err
	}
	c.Access = "owner"
	if curFolder.Valid && curFolder.String != "" {
		v := curFolder.String
		c.FolderID = &v
	}
	if curArchived.Valid {
		v := curArchived.Int64
		c.ArchivedAt = &v
	}

	if title != nil {
		c.Title = *title
	}
	if pinned != nil {
		c.Pinned = *pinned
	}
	if folderSet {
		if folderID == nil || *folderID == "" {
			c.FolderID = nil
		} else {
			var ownedFolder string
			err := tx.QueryRowContext(ctx, `SELECT id FROM chat_folders WHERE id=? AND user_id=?`, *folderID, userID).Scan(&ownedFolder)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return Chat{}, ErrForbidden
				}
				return Chat{}, err
			}
			v := ownedFolder
			c.FolderID = &v
		}
	}
	if archived != nil {
		if *archived {
			if c.ArchivedAt == nil {
				now := time.Now().UnixMilli()
				c.ArchivedAt = &now
			}
		} else {
			c.ArchivedAt = nil
		}
	}

	var dbFolder any
	if c.FolderID != nil {
		dbFolder = *c.FolderID
	}
	var dbArchived any
	if c.ArchivedAt != nil {
		dbArchived = *c.ArchivedAt
	}

	if _, err := tx.ExecContext(ctx, `UPDATE chats SET title=?, pinned=?, folder_id=?, archived_at=? WHERE id=? AND user_id=?`,
		c.Title, c.Pinned, dbFolder, dbArchived, chatID, userID); err != nil {
		return Chat{}, err
	}
	if err := tx.Commit(); err != nil {
		return Chat{}, err
	}
	return c, nil
}

// ForkChatUpTo clones a chat (or prefix up to upToMessageID inclusive) and duplicates attachments safely on disk.
func (s *Store) ForkChatUpTo(ctx context.Context, userID, sourceChatID, upToMessageID string) (Chat, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Chat{}, err
	}
	defer tx.Rollback()

	now := time.Now().UnixMilli()
	c := Chat{ID: randomHex(16), OwnerID: userID, Access: "owner", CreatedAt: now, UpdatedAt: now}
	var sourceOwner string
	var shareAccess sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT title,provider,model,user_id,(SELECT access FROM chat_shares WHERE chat_id=c.id AND user_id=?2)
		FROM chats c WHERE c.id=?1`, sourceChatID, userID).Scan(&c.Title, &c.Provider, &c.Model, &sourceOwner, &shareAccess); err != nil {
		return Chat{}, err
	}
	if sourceOwner != userID {
		if !shareAccess.Valid {
			return Chat{}, sql.ErrNoRows
		}
		if shareAccess.String != "fork" {
			return Chat{}, sql.ErrNoRows
		}
	}

	var (
		hasCutoff       bool
		cutoffCreatedAt int64
		cutoffRowID     int64
	)
	if upToMessageID != "" {
		err := tx.QueryRowContext(ctx, `SELECT created_at, rowid FROM chat_messages
			WHERE chat_id=?1 AND (id=?2 OR run_id=?2)
			ORDER BY created_at DESC, rowid DESC LIMIT 1`, sourceChatID, upToMessageID).
			Scan(&cutoffCreatedAt, &cutoffRowID)
		if err != nil {
			return Chat{}, err
		}
		hasCutoff = true
	}

	c.Title += " · копия"
	if _, err := tx.ExecContext(ctx, `INSERT INTO chats (id,user_id,title,provider,session_provider,model,provider_session_id,pinned,created_at,updated_at)
		VALUES (?, ?, ?, ?, '', ?, NULL, 0, ?, ?)`, c.ID, c.OwnerID, c.Title, c.Provider, c.Model, c.CreatedAt, c.UpdatedAt); err != nil {
		return Chat{}, err
	}

	type srcMsg struct {
		oldID, role, content, thinking, webSources string
		createdAt                                  int64
		runID                                      sql.NullString
		durationMs                                 sql.NullInt64
	}
	var (
		rows *sql.Rows
	)
	if hasCutoff {
		rows, err = tx.QueryContext(ctx, `SELECT id,role,content,thinking,created_at,run_id,duration_ms,COALESCE(web_sources_json,'[]')
			FROM chat_messages WHERE chat_id=?1 AND (created_at < ?2 OR (created_at = ?2 AND rowid <= ?3))
			ORDER BY created_at,rowid`, sourceChatID, cutoffCreatedAt, cutoffRowID)
	} else {
		rows, err = tx.QueryContext(ctx, `SELECT id,role,content,thinking,created_at,run_id,duration_ms,COALESCE(web_sources_json,'[]')
			FROM chat_messages WHERE chat_id=?1 ORDER BY created_at,rowid`, sourceChatID)
	}
	if err != nil {
		return Chat{}, err
	}
	var msgs []srcMsg
	for rows.Next() {
		var m srcMsg
		if err := rows.Scan(&m.oldID, &m.role, &m.content, &m.thinking, &m.createdAt, &m.runID, &m.durationMs, &m.webSources); err != nil {
			rows.Close()
			return Chat{}, err
		}
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Chat{}, err
	}
	rows.Close()

	idMap := make(map[string]string, len(msgs))
	copiedRuns := make(map[string]bool)
	for _, m := range msgs {
		newMsgID := randomHex(16)
		idMap[m.oldID] = newMsgID
		if _, err := tx.ExecContext(ctx, `INSERT INTO chat_messages (id,chat_id,role,content,thinking,created_at,run_id,duration_ms,run_user_id,web_sources_json)
			VALUES (?,?,?,?,?,?,?,?,'',?)`,
			newMsgID, c.ID, m.role, m.content, m.thinking, m.createdAt, m.runID, m.durationMs, m.webSources); err != nil {
			return Chat{}, err
		}
		if m.role == "assistant" && m.runID.Valid && m.runID.String != "" && !copiedRuns[m.runID.String] {
			copiedRuns[m.runID.String] = true
			if _, err := tx.ExecContext(ctx, `INSERT INTO usage_events
				(id,user_id,chat_id,run_id,provider,model,input_tokens,cached_input_tokens,output_tokens,reasoning_tokens,cost_cents,created_at)
				SELECT ?,?, ?,run_id,provider,model,input_tokens,cached_input_tokens,output_tokens,reasoning_tokens,0,created_at
				FROM usage_events WHERE chat_id=? AND run_id=?`,
				randomHex(16), userID, c.ID, sourceChatID, m.runID.String); err != nil {
				return Chat{}, err
			}
		}
	}

	type srcAtt struct {
		oldMsgID, name, mimeType, kind, sha256, storageKey, warning, source string
		sizeBytes                                                           int64
		extractedText                                                       sql.NullString
		createdAt                                                           int64
	}
	attRows, err := tx.QueryContext(ctx, `SELECT message_id,original_name,mime_type,kind,size_bytes,sha256,storage_key,extracted_text,warning,source,created_at
		FROM chat_attachments WHERE chat_id=? AND message_id IS NOT NULL ORDER BY created_at,rowid`, sourceChatID)
	if err != nil {
		return Chat{}, err
	}
	var atts []srcAtt
	for attRows.Next() {
		var a srcAtt
		if err := attRows.Scan(&a.oldMsgID, &a.name, &a.mimeType, &a.kind, &a.sizeBytes, &a.sha256, &a.storageKey, &a.extractedText, &a.warning, &a.source, &a.createdAt); err != nil {
			attRows.Close()
			return Chat{}, err
		}
		if _, ok := idMap[a.oldMsgID]; ok {
			atts = append(atts, a)
		}
	}
	if err := attRows.Err(); err != nil {
		attRows.Close()
		return Chat{}, err
	}
	attRows.Close()

	var copiedPaths []string
	rollbackFiles := func() {
		for _, p := range copiedPaths {
			_ = os.Remove(p)
		}
	}
	if len(atts) > 0 && s.uploadsDir != "" {
		if err := os.MkdirAll(s.uploadsDir, 0700); err != nil {
			return Chat{}, err
		}
	}
	for _, a := range atts {
		newAttID := randomHex(16)
		newKey := randomHex(16)
		if s.uploadsDir != "" {
			srcPath, err := s.attachmentPath(a.storageKey)
			if err != nil {
				rollbackFiles()
				return Chat{}, err
			}
			dstPath, err := s.attachmentPath(newKey)
			if err != nil {
				rollbackFiles()
				return Chat{}, err
			}
			if err := copyFileSecure(srcPath, dstPath); err != nil {
				rollbackFiles()
				return Chat{}, err
			}
			copiedPaths = append(copiedPaths, dstPath)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO chat_attachments
			(id,chat_id,message_id,user_id,original_name,mime_type,kind,size_bytes,sha256,storage_key,extracted_text,warning,source,created_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			newAttID, c.ID, idMap[a.oldMsgID], userID, a.name, a.mimeType, a.kind, a.sizeBytes, a.sha256, newKey, a.extractedText, a.warning, a.source, a.createdAt); err != nil {
			rollbackFiles()
			return Chat{}, err
		}
	}

	if err := tx.Commit(); err != nil {
		rollbackFiles()
		return Chat{}, err
	}
	return c, nil
}

func collectDeletedAttachmentKeys(ctx context.Context, tx *sql.Tx, chatID string, afterCreatedAt, afterRowID int64) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT a.storage_key FROM chat_attachments a
		JOIN chat_messages m ON m.id=a.message_id
		WHERE a.chat_id=?1 AND (m.created_at > ?2 OR (m.created_at = ?2 AND m.rowid > ?3))`,
		chatID, afterCreatedAt, afterRowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

func (s *Store) removeStorageKeys(keys []string) {
	for _, key := range keys {
		if p, err := s.attachmentPath(key); err == nil {
			_ = os.Remove(p)
		}
	}
}

// DeleteMessage deletes a message (and its attachments if user message) and resets CLI session state.
func (s *Store) DeleteMessage(ctx context.Context, userID, chatID, messageID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := s.checkWriteOrForbidden(ctx, tx, userID, chatID); err != nil {
		return err
	}

	var msgID, role string
	var runID sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT id,role,run_id FROM chat_messages
		WHERE chat_id=?1 AND (id=?2 OR run_id=?2)
		ORDER BY CASE WHEN id=?2 THEN 0 ELSE 1 END, created_at DESC, rowid DESC LIMIT 1`,
		chatID, messageID).Scan(&msgID, &role, &runID)
	if err != nil {
		return err
	}

	var keysToDelete []string
	collectForQuery := func(query string, args ...any) error {
		attRows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		for attRows.Next() {
			var k string
			if err := attRows.Scan(&k); err != nil {
				attRows.Close()
				return err
			}
			keysToDelete = append(keysToDelete, k)
		}
		if err := attRows.Err(); err != nil {
			attRows.Close()
			return err
		}
		attRows.Close()
		return nil
	}

	if role == "user" {
		if runID.Valid && runID.String != "" {
			if err := collectForQuery(`SELECT a.storage_key FROM chat_attachments a
				JOIN chat_messages m ON m.id=a.message_id
				WHERE a.chat_id=? AND (m.id=? OR m.run_id=?)`, chatID, msgID, runID.String); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM chat_messages WHERE chat_id=? AND (id=? OR run_id=?)`, chatID, msgID, runID.String); err != nil {
				return err
			}
		} else {
			if err := collectForQuery(`SELECT storage_key FROM chat_attachments WHERE chat_id=? AND message_id=?`, chatID, msgID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM chat_messages WHERE chat_id=? AND id=?`, chatID, msgID); err != nil {
				return err
			}
		}
	} else {
		if err := collectForQuery(`SELECT storage_key FROM chat_attachments WHERE chat_id=? AND message_id=?`, chatID, msgID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM chat_messages WHERE chat_id=? AND id=?`, chatID, msgID); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, `UPDATE chats SET provider_session_id=NULL, session_provider='' WHERE id=?`, chatID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.removeStorageKeys(keysToDelete)
	return nil
}

// PrepareRegenerate trims the selected reply and all subsequent messages, keeping the paired user prompt & attachments.
func (s *Store) PrepareRegenerate(ctx context.Context, userID, chatID, messageID, targetProvider, targetModel string) (userMsgID string, prompt string, attachments []Attachment, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", nil, err
	}
	defer tx.Rollback()

	if err := s.checkWriteOrForbidden(ctx, tx, userID, chatID); err != nil {
		return "", "", nil, err
	}

	var targetID, role string
	var runID sql.NullString
	var createdAt, rowID int64
	err = tx.QueryRowContext(ctx, `SELECT id,role,run_id,created_at,rowid FROM chat_messages
		WHERE chat_id=?1 AND (id=?2 OR run_id=?2)
		ORDER BY CASE WHEN role='assistant' THEN 0 ELSE 1 END, created_at DESC, rowid DESC LIMIT 1`,
		chatID, messageID).Scan(&targetID, &role, &runID, &createdAt, &rowID)
	if err != nil {
		return "", "", nil, err
	}

	var userCreatedAt, userRowID int64
	if role == "user" {
		userMsgID = targetID
		if err := tx.QueryRowContext(ctx, `SELECT content,created_at,rowid FROM chat_messages WHERE id=?`, userMsgID).
			Scan(&prompt, &userCreatedAt, &userRowID); err != nil {
			return "", "", nil, err
		}
	} else {
		if runID.Valid && runID.String != "" {
			err = tx.QueryRowContext(ctx, `SELECT id,content,created_at,rowid FROM chat_messages
				WHERE chat_id=? AND run_id=? AND role='user' LIMIT 1`, chatID, runID.String).
				Scan(&userMsgID, &prompt, &userCreatedAt, &userRowID)
		} else {
			err = sql.ErrNoRows
		}
		if errors.Is(err, sql.ErrNoRows) {
			err = tx.QueryRowContext(ctx, `SELECT id,content,created_at,rowid FROM chat_messages
				WHERE chat_id=?1 AND role='user' AND (created_at < ?2 OR (created_at = ?2 AND rowid < ?3))
				ORDER BY created_at DESC, rowid DESC LIMIT 1`, chatID, createdAt, rowID).
				Scan(&userMsgID, &prompt, &userCreatedAt, &userRowID)
		}
		if err != nil {
			return "", "", nil, err
		}
	}

	keysToDelete, err := collectDeletedAttachmentKeys(ctx, tx, chatID, userCreatedAt, userRowID)
	if err != nil {
		return "", "", nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM chat_messages
		WHERE chat_id=?1 AND (created_at > ?2 OR (created_at = ?2 AND rowid > ?3))`,
		chatID, userCreatedAt, userRowID); err != nil {
		return "", "", nil, err
	}

	if targetProvider != "" && targetModel != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE chats SET provider=?, model=?, provider_session_id=NULL, session_provider='' WHERE id=?`,
			targetProvider, targetModel, chatID); err != nil {
			return "", "", nil, err
		}
	} else if targetProvider != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE chats SET provider=?, provider_session_id=NULL, session_provider='' WHERE id=?`,
			targetProvider, chatID); err != nil {
			return "", "", nil, err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `UPDATE chats SET provider_session_id=NULL, session_provider='' WHERE id=?`, chatID); err != nil {
			return "", "", nil, err
		}
	}

	attRows, err := tx.QueryContext(ctx, `SELECT id,chat_id,message_id,user_id,original_name,mime_type,kind,size_bytes,sha256,storage_key,COALESCE(extracted_text,''),warning,created_at
		FROM chat_attachments WHERE chat_id=? AND message_id=? ORDER BY created_at,rowid`, chatID, userMsgID)
	if err != nil {
		return "", "", nil, err
	}
	for attRows.Next() {
		var a Attachment
		var mID sql.NullString
		if err := attRows.Scan(&a.ID, &a.ChatID, &mID, &a.UserID, &a.OriginalName, &a.MIMEType, &a.Kind, &a.SizeBytes, &a.SHA256, &a.StorageKey, &a.ExtractedText, &a.Warning, &a.CreatedAt); err != nil {
			attRows.Close()
			return "", "", nil, err
		}
		a.MessageID = mID.String
		a.URL = "/api/attachments/" + a.ID
		attachments = append(attachments, a)
	}
	if err := attRows.Err(); err != nil {
		attRows.Close()
		return "", "", nil, err
	}
	attRows.Close()

	if err := tx.Commit(); err != nil {
		return "", "", nil, err
	}
	s.removeStorageKeys(keysToDelete)
	return userMsgID, prompt, attachments, nil
}

// PrepareEditMessage updates a user message's text, trims subsequent messages, and resets CLI session state.
func (s *Store) PrepareEditMessage(ctx context.Context, userID, chatID, messageID, newContent string) (userMsgID string, attachments []Attachment, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", nil, err
	}
	defer tx.Rollback()

	if err := s.checkWriteOrForbidden(ctx, tx, userID, chatID); err != nil {
		return "", nil, err
	}

	var userCreatedAt, userRowID int64
	err = tx.QueryRowContext(ctx, `SELECT id,created_at,rowid FROM chat_messages
		WHERE chat_id=?1 AND (id=?2 OR run_id=?2) AND role='user'
		ORDER BY CASE WHEN id=?2 THEN 0 ELSE 1 END LIMIT 1`,
		chatID, messageID).Scan(&userMsgID, &userCreatedAt, &userRowID)
	if err != nil {
		return "", nil, err
	}

	if _, err := tx.ExecContext(ctx, `UPDATE chat_messages SET content=? WHERE id=?`, newContent, userMsgID); err != nil {
		return "", nil, err
	}

	keysToDelete, err := collectDeletedAttachmentKeys(ctx, tx, chatID, userCreatedAt, userRowID)
	if err != nil {
		return "", nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM chat_messages
		WHERE chat_id=?1 AND (created_at > ?2 OR (created_at = ?2 AND rowid > ?3))`,
		chatID, userCreatedAt, userRowID); err != nil {
		return "", nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE chats SET provider_session_id=NULL, session_provider='' WHERE id=?`, chatID); err != nil {
		return "", nil, err
	}

	attRows, err := tx.QueryContext(ctx, `SELECT id,chat_id,message_id,user_id,original_name,mime_type,kind,size_bytes,sha256,storage_key,COALESCE(extracted_text,''),warning,created_at
		FROM chat_attachments WHERE chat_id=? AND message_id=? ORDER BY created_at,rowid`, chatID, userMsgID)
	if err != nil {
		return "", nil, err
	}
	for attRows.Next() {
		var a Attachment
		var mID sql.NullString
		if err := attRows.Scan(&a.ID, &a.ChatID, &mID, &a.UserID, &a.OriginalName, &a.MIMEType, &a.Kind, &a.SizeBytes, &a.SHA256, &a.StorageKey, &a.ExtractedText, &a.Warning, &a.CreatedAt); err != nil {
			attRows.Close()
			return "", nil, err
		}
		a.MessageID = mID.String
		a.URL = "/api/attachments/" + a.ID
		attachments = append(attachments, a)
	}
	if err := attRows.Err(); err != nil {
		attRows.Close()
		return "", nil, err
	}
	attRows.Close()

	if err := tx.Commit(); err != nil {
		return "", nil, err
	}
	s.removeStorageKeys(keysToDelete)
	return userMsgID, attachments, nil
}
