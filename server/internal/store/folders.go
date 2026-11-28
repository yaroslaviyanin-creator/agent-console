package store

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"agent-console/server/internal/uploads"
)

type Folder struct {
	ID              string `json:"id"`
	UserID          string `json:"userId,omitempty"`
	Name            string `json:"name"`
	Color           string `json:"color"`
	SortOrder       int    `json:"sortOrder"`
	SystemPrompt    string `json:"systemPrompt"`
	AttachmentCount int    `json:"attachmentCount"`
	CreatedAt       int64  `json:"createdAt"`
}

type SearchResult struct {
	ChatID    string `json:"chatId"`
	MessageID string `json:"messageId"`
	ChatTitle string `json:"chatTitle"`
	Role      string `json:"role"`
	Snippet   string `json:"snippet"`
	CreatedAt int64  `json:"createdAt"`
}

func (s *Store) ListFolders(ctx context.Context, userID string) ([]Folder, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT f.id, f.user_id, f.name, f.color, f.sort_order, f.system_prompt,
		(SELECT COUNT(*) FROM folder_attachments fa WHERE fa.folder_id = f.id),
		f.created_at
		FROM chat_folders f WHERE f.user_id=? ORDER BY f.sort_order ASC, f.created_at ASC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	folders := []Folder{}
	for rows.Next() {
		var f Folder
		if err := rows.Scan(&f.ID, &f.UserID, &f.Name, &f.Color, &f.SortOrder, &f.SystemPrompt, &f.AttachmentCount, &f.CreatedAt); err != nil {
			return nil, err
		}
		folders = append(folders, f)
	}
	return folders, rows.Err()
}

func (s *Store) GetFolder(ctx context.Context, userID, folderID string) (Folder, error) {
	var f Folder
	err := s.db.QueryRowContext(ctx, `SELECT f.id, f.user_id, f.name, f.color, f.sort_order, f.system_prompt,
		(SELECT COUNT(*) FROM folder_attachments fa WHERE fa.folder_id = f.id),
		f.created_at
		FROM chat_folders f WHERE f.id=? AND f.user_id=?`, folderID, userID).
		Scan(&f.ID, &f.UserID, &f.Name, &f.Color, &f.SortOrder, &f.SystemPrompt, &f.AttachmentCount, &f.CreatedAt)
	return f, err
}

func (s *Store) CreateFolder(ctx context.Context, userID, name, color, systemPrompt string) (Folder, error) {
	now := time.Now().UnixMilli()
	var maxOrder int
	_ = s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(sort_order), -1) FROM chat_folders WHERE user_id=?`, userID).Scan(&maxOrder)
	f := Folder{
		ID:           randomHex(16),
		UserID:       userID,
		Name:         name,
		Color:        color,
		SortOrder:    maxOrder + 1,
		SystemPrompt: systemPrompt,
		CreatedAt:    now,
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO chat_folders (id,user_id,name,color,sort_order,system_prompt,created_at) VALUES (?,?,?,?,?,?,?)`,
		f.ID, f.UserID, f.Name, f.Color, f.SortOrder, f.SystemPrompt, f.CreatedAt)
	if err != nil {
		return Folder{}, err
	}
	return f, nil
}

func (s *Store) UpdateFolder(ctx context.Context, userID, folderID string, name *string, color *string, sortOrder *int, systemPrompt *string) (Folder, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Folder{}, err
	}
	defer tx.Rollback()

	var f Folder
	err = tx.QueryRowContext(ctx, `SELECT f.id, f.user_id, f.name, f.color, f.sort_order, f.system_prompt,
		(SELECT COUNT(*) FROM folder_attachments fa WHERE fa.folder_id = f.id),
		f.created_at
		FROM chat_folders f WHERE f.id=? AND f.user_id=?`,
		folderID, userID).Scan(&f.ID, &f.UserID, &f.Name, &f.Color, &f.SortOrder, &f.SystemPrompt, &f.AttachmentCount, &f.CreatedAt)
	if err != nil {
		return Folder{}, err
	}
	if name != nil {
		f.Name = *name
	}
	if color != nil {
		f.Color = *color
	}
	if sortOrder != nil {
		f.SortOrder = *sortOrder
	}
	if systemPrompt != nil {
		f.SystemPrompt = *systemPrompt
	}
	if _, err := tx.ExecContext(ctx, `UPDATE chat_folders SET name=?, color=?, sort_order=?, system_prompt=? WHERE id=? AND user_id=?`,
		f.Name, f.Color, f.SortOrder, f.SystemPrompt, folderID, userID); err != nil {
		return Folder{}, err
	}
	if err := tx.Commit(); err != nil {
		return Folder{}, err
	}
	return f, nil
}

func (s *Store) DeleteFolder(ctx context.Context, userID, folderID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `SELECT storage_key FROM folder_attachments WHERE folder_id=? AND user_id=?`, folderID, userID)
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

	if _, err := tx.ExecContext(ctx, `UPDATE chats SET folder_id=NULL WHERE folder_id=? AND user_id=?`, folderID, userID); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM chat_folders WHERE id=? AND user_id=?`, folderID, userID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, key := range keys {
		if diskPath, err := s.attachmentPath(key); err == nil {
			_ = os.Remove(diskPath)
		}
	}
	return nil
}

func (s *Store) CountFolderAttachments(ctx context.Context, folderID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM folder_attachments WHERE folder_id=?`, folderID).Scan(&count)
	return count, err
}

func (s *Store) ListFolderAttachments(ctx context.Context, userID, folderID string) ([]Attachment, error) {
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM chat_folders WHERE id=? AND user_id=?)`, folderID, userID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, sql.ErrNoRows
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, folder_id, user_id, original_name, mime_type, kind, size_bytes, sha256, storage_key, COALESCE(extracted_text, ''), warning, created_at
		FROM folder_attachments WHERE folder_id=? AND user_id=? ORDER BY created_at ASC`, folderID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Attachment{}
	for rows.Next() {
		var a Attachment
		if err := rows.Scan(&a.ID, &a.FolderID, &a.UserID, &a.OriginalName, &a.MIMEType, &a.Kind, &a.SizeBytes, &a.SHA256, &a.StorageKey, &a.ExtractedText, &a.Warning, &a.CreatedAt); err != nil {
			return nil, err
		}
		a.URL = "/api/folders/" + folderID + "/attachments/" + a.ID
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) CreateFolderAttachment(ctx context.Context, folderID, userID string, v uploads.ValidatedAttachment) (Attachment, error) {
	var exists bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM chat_folders WHERE id=? AND user_id=?)`, folderID, userID).Scan(&exists); err != nil {
		return Attachment{}, err
	}
	if !exists {
		return Attachment{}, sql.ErrNoRows
	}
	id := randomHex(16)
	storageKey, diskPath, sizeBytes, shaHex, err := s.stageValidatedAttachment(v)
	if err != nil {
		return Attachment{}, err
	}
	now := time.Now().UnixMilli()
	var extracted any
	if v.ExtractedText != "" {
		extracted = v.ExtractedText
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO folder_attachments
		(id,folder_id,user_id,original_name,mime_type,kind,size_bytes,sha256,storage_key,extracted_text,warning,created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, folderID, userID, v.Filename, v.MIMEType, string(v.Kind), sizeBytes, shaHex, storageKey, extracted, v.Warning, now)
	if err != nil {
		_ = os.Remove(diskPath)
		return Attachment{}, err
	}
	return Attachment{
		ID:            id,
		FolderID:      folderID,
		UserID:        userID,
		OriginalName:  v.Filename,
		MIMEType:      v.MIMEType,
		Kind:          string(v.Kind),
		SizeBytes:     sizeBytes,
		SHA256:        shaHex,
		StorageKey:    storageKey,
		ExtractedText: v.ExtractedText,
		Warning:       v.Warning,
		CreatedAt:     now,
		URL:           "/api/folders/" + folderID + "/attachments/" + id,
	}, nil
}

func (s *Store) GetFolderAttachment(ctx context.Context, userID, folderID, attachmentID string) (Attachment, error) {
	var a Attachment
	var extracted sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT fa.id, fa.folder_id, fa.user_id, fa.original_name, fa.mime_type, fa.kind, fa.size_bytes, fa.sha256, fa.storage_key, fa.extracted_text, fa.warning, fa.created_at
		FROM folder_attachments fa
		JOIN chat_folders f ON f.id = fa.folder_id
		WHERE fa.id=? AND fa.folder_id=? AND f.user_id=?`, attachmentID, folderID, userID).
		Scan(&a.ID, &a.FolderID, &a.UserID, &a.OriginalName, &a.MIMEType, &a.Kind, &a.SizeBytes, &a.SHA256, &a.StorageKey, &extracted, &a.Warning, &a.CreatedAt)
	if err != nil {
		return Attachment{}, err
	}
	a.ExtractedText = extracted.String
	a.URL = "/api/folders/" + folderID + "/attachments/" + a.ID
	return a, nil
}

func (s *Store) DeleteFolderAttachment(ctx context.Context, userID, folderID, attachmentID string) error {
	var storageKey string
	err := s.db.QueryRowContext(ctx, `DELETE FROM folder_attachments WHERE id=? AND folder_id=? AND user_id=? RETURNING storage_key`,
		attachmentID, folderID, userID).Scan(&storageKey)
	if err != nil {
		return err
	}
	if diskPath, err := s.attachmentPath(storageKey); err == nil {
		_ = os.Remove(diskPath)
	}
	return nil
}

// GetChatProjectContext returns the folder and its attachments ONLY when userID owns both the chat and the folder.
func (s *Store) GetChatProjectContext(ctx context.Context, userID, chatID string) (Folder, []Attachment, error) {
	var folderID sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT folder_id FROM chats WHERE id=? AND user_id=?`, chatID, userID).Scan(&folderID)
	if err != nil {
		return Folder{}, nil, err
	}
	if !folderID.Valid || strings.TrimSpace(folderID.String) == "" {
		return Folder{}, nil, nil
	}
	folder, err := s.GetFolder(ctx, userID, folderID.String)
	if err != nil {
		if err == sql.ErrNoRows {
			return Folder{}, nil, nil
		}
		return Folder{}, nil, err
	}
	atts, err := s.ListFolderAttachments(ctx, userID, folder.ID)
	if err != nil {
		return Folder{}, nil, err
	}
	return folder, atts, nil
}

// SearchMessages searches accessible chats (owned or shared) by chat title and message text with Unicode case-folding.
func (s *Store) SearchMessages(ctx context.Context, userID, query string, limit int) ([]SearchResult, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return []SearchResult{}, nil
	}
	if limit <= 0 || limit > 30 {
		limit = 30
	}
	lowerQ := strings.ToLower(q)

	results := make([]SearchResult, 0, min(limit, 16))
	matchedChatTitle := make(map[string]bool)

	// 1. Search accessible message contents (newest first).
	rows, err := s.db.QueryContext(ctx, `SELECT c.id, c.title,
		CASE WHEN m.role='assistant' THEN COALESCE(NULLIF(m.run_id,''), m.id) ELSE m.id END,
		m.role, m.content, m.created_at
		FROM chat_messages m
		JOIN chats c ON c.id = m.chat_id
		WHERE c.user_id=?1 OR EXISTS(SELECT 1 FROM chat_shares WHERE chat_id=c.id AND user_id=?1)
		ORDER BY m.created_at DESC, m.rowid DESC`, userID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var chatID, chatTitle, msgID, role, content string
		var createdAt int64
		if err := rows.Scan(&chatID, &chatTitle, &msgID, &role, &content, &createdAt); err != nil {
			rows.Close()
			return nil, err
		}
		if strings.Contains(strings.ToLower(content), lowerQ) {
			results = append(results, SearchResult{
				ChatID:    chatID,
				MessageID: msgID,
				ChatTitle: chatTitle,
				Role:      role,
				Snippet:   buildSearchSnippet(content, lowerQ),
				CreatedAt: createdAt,
			})
			if len(results) >= limit {
				break
			}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	// 2. Include chat title matches if space remains (or prioritize title matches not yet present).
	if len(results) < limit {
		for _, r := range results {
			matchedChatTitle[r.ChatID] = true
		}
		chatRows, err := s.db.QueryContext(ctx, `SELECT c.id, c.title, c.updated_at
			FROM chats c
			WHERE (c.user_id=?1 OR EXISTS(SELECT 1 FROM chat_shares WHERE chat_id=c.id AND user_id=?1))
			ORDER BY c.updated_at DESC, c.rowid DESC`, userID)
		if err != nil {
			return nil, err
		}
		for chatRows.Next() {
			var chatID, chatTitle string
			var updatedAt int64
			if err := chatRows.Scan(&chatID, &chatTitle, &updatedAt); err != nil {
				chatRows.Close()
				return nil, err
			}
			if matchedChatTitle[chatID] {
				continue
			}
			if strings.Contains(strings.ToLower(chatTitle), lowerQ) {
				results = append(results, SearchResult{
					ChatID:    chatID,
					MessageID: "",
					ChatTitle: chatTitle,
					Role:      "title",
					Snippet:   chatTitle,
					CreatedAt: updatedAt,
				})
				if len(results) >= limit {
					break
				}
			}
		}
		if err := chatRows.Err(); err != nil {
			chatRows.Close()
			return nil, err
		}
		chatRows.Close()
	}

	return results, nil
}

func buildSearchSnippet(content, lowerQ string) string {
	clean := strings.Join(strings.Fields(strings.TrimSpace(content)), " ")
	if clean == "" {
		return ""
	}
	lowerClean := strings.ToLower(clean)
	byteIdx := strings.Index(lowerClean, lowerQ)
	if byteIdx < 0 {
		return clipSearchRunes(clean, 140)
	}
	// Map byte offset from lowerClean back to clean by counting runes before byteIdx.
	runeStart := utf8.RuneCountInString(lowerClean[:byteIdx])
	runes := []rune(clean)
	matchRunes := utf8.RuneCountInString(lowerQ)

	from := runeStart - 45
	prefix := ""
	if from > 0 {
		prefix = "…"
	} else {
		from = 0
	}
	to := runeStart + matchRunes + 75
	suffix := ""
	if to < len(runes) {
		suffix = "…"
	} else {
		to = len(runes)
	}
	return prefix + string(runes[from:to]) + suffix
}

func clipSearchRunes(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "…"
}
