package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
	"unicode/utf8"
)

type Folder struct {
	ID        string `json:"id"`
	UserID    string `json:"userId,omitempty"`
	Name      string `json:"name"`
	Color     string `json:"color"`
	SortOrder int    `json:"sortOrder"`
	CreatedAt int64  `json:"createdAt"`
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
	rows, err := s.db.QueryContext(ctx, `SELECT id,user_id,name,color,sort_order,created_at
		FROM chat_folders WHERE user_id=? ORDER BY sort_order ASC, created_at ASC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	folders := []Folder{}
	for rows.Next() {
		var f Folder
		if err := rows.Scan(&f.ID, &f.UserID, &f.Name, &f.Color, &f.SortOrder, &f.CreatedAt); err != nil {
			return nil, err
		}
		folders = append(folders, f)
	}
	return folders, rows.Err()
}

func (s *Store) CreateFolder(ctx context.Context, userID, name, color string) (Folder, error) {
	now := time.Now().UnixMilli()
	var maxOrder int
	_ = s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(sort_order), -1) FROM chat_folders WHERE user_id=?`, userID).Scan(&maxOrder)
	f := Folder{
		ID:        randomHex(16),
		UserID:    userID,
		Name:      name,
		Color:     color,
		SortOrder: maxOrder + 1,
		CreatedAt: now,
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO chat_folders (id,user_id,name,color,sort_order,created_at) VALUES (?,?,?,?,?,?)`,
		f.ID, f.UserID, f.Name, f.Color, f.SortOrder, f.CreatedAt)
	if err != nil {
		return Folder{}, err
	}
	return f, nil
}

func (s *Store) UpdateFolder(ctx context.Context, userID, folderID string, name *string, color *string, sortOrder *int) (Folder, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Folder{}, err
	}
	defer tx.Rollback()

	var f Folder
	err = tx.QueryRowContext(ctx, `SELECT id,user_id,name,color,sort_order,created_at FROM chat_folders WHERE id=? AND user_id=?`,
		folderID, userID).Scan(&f.ID, &f.UserID, &f.Name, &f.Color, &f.SortOrder, &f.CreatedAt)
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
	if _, err := tx.ExecContext(ctx, `UPDATE chat_folders SET name=?, color=?, sort_order=? WHERE id=? AND user_id=?`,
		f.Name, f.Color, f.SortOrder, folderID, userID); err != nil {
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
	return tx.Commit()
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
