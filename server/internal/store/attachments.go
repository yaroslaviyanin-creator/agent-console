package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"agent-console/server/internal/uploads"
)

var (
	ErrForbidden          = errors.New("forbidden")
	ErrInvalidAttachments = errors.New("invalid or unavailable chat attachments")
	ErrAttachmentInUse    = errors.New("attachment is already bound to a message")
)

var safeStorageKeyRe = regexp.MustCompile(`^[a-f0-9]{32}$`)

type Attachment struct {
	ID            string `json:"id"`
	ChatID        string `json:"chatId"`
	MessageID     string `json:"messageId,omitempty"`
	UserID        string `json:"userId"`
	OriginalName  string `json:"name"`
	MIMEType      string `json:"mimeType"`
	Kind          string `json:"kind"`
	SizeBytes     int64  `json:"sizeBytes"`
	SHA256        string `json:"sha256,omitempty"`
	StorageKey    string `json:"-"`
	ExtractedText string `json:"-"`
	CreatedAt     int64  `json:"createdAt"`
	URL           string `json:"url"`
}

type MessageAttachment struct {
	ID        string `json:"id,omitempty"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	MimeType  string `json:"mimeType,omitempty"`
	SizeBytes int64  `json:"sizeBytes,omitempty"`
	URL       string `json:"url,omitempty"`
}

func (a Attachment) AsMessageAttachment() MessageAttachment {
	return MessageAttachment{
		ID:        a.ID,
		Kind:      a.Kind,
		Name:      a.OriginalName,
		MimeType:  a.MIMEType,
		SizeBytes: a.SizeBytes,
		URL:       a.URL,
	}
}

func (s *Store) attachmentPath(storageKey string) (string, error) {
	if s.uploadsDir == "" || !safeStorageKeyRe.MatchString(storageKey) {
		return "", errors.New("invalid attachment storage key")
	}
	return filepath.Join(s.uploadsDir, storageKey), nil
}

// CreateAttachment writes validated bytes to an isolated UUID path (mode 0600) and records metadata.
func (s *Store) CreateAttachment(ctx context.Context, chatID, userID string, v uploads.ValidatedAttachment) (Attachment, error) {
	id := randomHex(16)
	storageKey := randomHex(16)
	diskPath, err := s.attachmentPath(storageKey)
	if err != nil {
		return Attachment{}, err
	}
	if err := os.MkdirAll(s.uploadsDir, 0700); err != nil {
		return Attachment{}, err
	}
	if err := os.WriteFile(diskPath, v.Data, 0600); err != nil {
		return Attachment{}, err
	}

	sum := sha256.Sum256(v.Data)
	shaHex := hex.EncodeToString(sum[:])
	now := time.Now().UnixMilli()
	var extracted any
	if v.ExtractedText != "" {
		extracted = v.ExtractedText
	}

	_, err = s.db.ExecContext(ctx, `INSERT INTO chat_attachments
		(id,chat_id,message_id,user_id,original_name,mime_type,kind,size_bytes,sha256,storage_key,extracted_text,created_at)
		VALUES (?,?,NULL,?,?,?,?,?,?,?,?,?)`,
		id, chatID, userID, v.Filename, v.MIMEType, string(v.Kind), int64(len(v.Data)), shaHex, storageKey, extracted, now)
	if err != nil {
		_ = os.Remove(diskPath)
		return Attachment{}, err
	}

	return Attachment{
		ID:            id,
		ChatID:        chatID,
		UserID:        userID,
		OriginalName:  v.Filename,
		MIMEType:      v.MIMEType,
		Kind:          string(v.Kind),
		SizeBytes:     int64(len(v.Data)),
		SHA256:        shaHex,
		StorageKey:    storageKey,
		ExtractedText: v.ExtractedText,
		CreatedAt:     now,
		URL:           "/api/attachments/" + id,
	}, nil
}

// GetAttachment fetches an attachment only if userID owns the chat or has any share access.
func (s *Store) GetAttachment(ctx context.Context, userID, attachmentID string) (Attachment, error) {
	var att Attachment
	var messageID, extracted sql.NullString
	var hasAccess bool
	err := s.db.QueryRowContext(ctx, `SELECT a.id,a.chat_id,a.message_id,a.user_id,a.original_name,a.mime_type,a.kind,a.size_bytes,a.sha256,a.storage_key,a.extracted_text,a.created_at,
		(c.user_id=?2 OR EXISTS(SELECT 1 FROM chat_shares WHERE chat_id=c.id AND user_id=?2))
		FROM chat_attachments a JOIN chats c ON c.id=a.chat_id WHERE a.id=?1`, attachmentID, userID).
		Scan(&att.ID, &att.ChatID, &messageID, &att.UserID, &att.OriginalName, &att.MIMEType, &att.Kind, &att.SizeBytes, &att.SHA256, &att.StorageKey, &extracted, &att.CreatedAt, &hasAccess)
	if err != nil {
		return Attachment{}, err
	}
	if !hasAccess {
		return Attachment{}, ErrForbidden
	}
	att.MessageID = messageID.String
	att.ExtractedText = extracted.String
	att.URL = "/api/attachments/" + att.ID
	return att, nil
}

func (s *Store) ReadAttachmentBytes(storageKey string) ([]byte, error) {
	diskPath, err := s.attachmentPath(storageKey)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(diskPath)
}

// DeleteUnattachedAttachment removes an unattached upload authored by userID.
func (s *Store) DeleteUnattachedAttachment(ctx context.Context, userID, attachmentID string) error {
	var ownerID string
	var messageID sql.NullString
	var storageKey string
	err := s.db.QueryRowContext(ctx, `SELECT user_id, message_id, storage_key FROM chat_attachments WHERE id=?`, attachmentID).
		Scan(&ownerID, &messageID, &storageKey)
	if err != nil {
		return err
	}
	if ownerID != userID {
		return ErrForbidden
	}
	if messageID.Valid {
		return ErrAttachmentInUse
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM chat_attachments WHERE id=? AND user_id=? AND message_id IS NULL`, attachmentID, userID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	if diskPath, err := s.attachmentPath(storageKey); err == nil {
		_ = os.Remove(diskPath)
	}
	return nil
}

// DeleteChat deletes a chat owned by userID and removes all associated attachment files from disk.
func (s *Store) DeleteChat(ctx context.Context, userID, chatID string) error {
	rows, err := s.db.QueryContext(ctx, `SELECT a.storage_key FROM chat_attachments a JOIN chats c ON c.id=a.chat_id WHERE c.id=? AND c.user_id=?`, chatID, userID)
	if err != nil {
		return err
	}
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return err
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	var deletedID string
	if err := s.db.QueryRowContext(ctx, `DELETE FROM chats WHERE id=? AND user_id=? RETURNING id`, chatID, userID).Scan(&deletedID); err != nil {
		return err
	}
	for _, key := range keys {
		if diskPath, err := s.attachmentPath(key); err == nil {
			_ = os.Remove(diskPath)
		}
	}
	return nil
}

// CleanupOrphanAttachments deletes unattached uploads older than maxAge from DB and disk.
func (s *Store) CleanupOrphanAttachments(ctx context.Context, maxAge time.Duration) (int, error) {
	cutoff := time.Now().Add(-maxAge).UnixMilli()
	rows, err := s.db.QueryContext(ctx, `SELECT id, storage_key FROM chat_attachments WHERE message_id IS NULL AND created_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	type orphan struct {
		id, key string
	}
	var orphans []orphan
	for rows.Next() {
		var o orphan
		if err := rows.Scan(&o.id, &o.key); err != nil {
			rows.Close()
			return 0, err
		}
		orphans = append(orphans, o)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()

	removed := 0
	for _, o := range orphans {
		res, err := s.db.ExecContext(ctx, `DELETE FROM chat_attachments WHERE id=? AND message_id IS NULL`, o.id)
		if err != nil {
			return removed, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			removed++
			if diskPath, err := s.attachmentPath(o.key); err == nil {
				_ = os.Remove(diskPath)
			}
		}
	}
	return removed, nil
}

func (s *Store) messageAttachmentsForChat(ctx context.Context, chatID string) (map[string][]MessageAttachment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, message_id, kind, original_name, mime_type, size_bytes
		FROM chat_attachments WHERE chat_id=? AND message_id IS NOT NULL ORDER BY created_at, rowid`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string][]MessageAttachment)
	for rows.Next() {
		var msgID string
		var att MessageAttachment
		if err := rows.Scan(&att.ID, &msgID, &att.Kind, &att.Name, &att.MimeType, &att.SizeBytes); err != nil {
			return nil, err
		}
		att.URL = "/api/attachments/" + att.ID
		result[msgID] = append(result[msgID], att)
	}
	return result, rows.Err()
}

// PriorAttachmentsForChat returns attachments keyed by user message ID for provider handoff context.
func (s *Store) PriorAttachmentsForChat(ctx context.Context, chatID string) (map[string][]Attachment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, chat_id, message_id, user_id, original_name, mime_type, kind, size_bytes, sha256, storage_key, COALESCE(extracted_text,''), created_at
		FROM chat_attachments WHERE chat_id=? AND message_id IS NOT NULL ORDER BY created_at, rowid`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string][]Attachment)
	for rows.Next() {
		var att Attachment
		if err := rows.Scan(&att.ID, &att.ChatID, &att.MessageID, &att.UserID, &att.OriginalName, &att.MIMEType, &att.Kind, &att.SizeBytes, &att.SHA256, &att.StorageKey, &att.ExtractedText, &att.CreatedAt); err != nil {
			return nil, err
		}
		att.URL = "/api/attachments/" + att.ID
		result[att.MessageID] = append(result[att.MessageID], att)
	}
	return result, rows.Err()
}
