package store

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"agent-console/server/internal/uploads"
)

const MaxUISettingsBytes = 8192

var hexColorRE = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

type UserProfile struct {
	DisplayName     string          `json:"displayName"`
	AvatarData      string          `json:"avatarData"`
	ProfileMD       string          `json:"profileMd"`
	MemoryMD        string          `json:"memoryMd"`
	MemoryEnabled   bool            `json:"memoryEnabled"`
	MemoryUpdatedAt int64           `json:"memoryUpdatedAt"`
	UISettings      json.RawMessage `json:"uiSettings"`
}

var ErrInvalidProfile = errors.New("invalid profile fields")
var ErrInvalidUISettings = errors.New("invalid uiSettings")

const profileColumns = "display_name,avatar_data,profile_md,memory_md,memory_enabled,memory_updated_at,ui_settings_json"

func readUserProfile(db rowQuerier, ctx context.Context, query string, args ...any) (UserProfile, error) {
	var p UserProfile
	var uiRaw string
	err := db.QueryRowContext(ctx, query, args...).Scan(&p.DisplayName, &p.AvatarData, &p.ProfileMD, &p.MemoryMD, &p.MemoryEnabled, &p.MemoryUpdatedAt, &uiRaw)
	if err == nil {
		trimmed := strings.TrimSpace(uiRaw)
		if trimmed == "" || !json.Valid([]byte(trimmed)) {
			p.UISettings = json.RawMessage("{}")
		} else {
			p.UISettings = json.RawMessage(trimmed)
		}
	}
	return p, err
}

func (s *Store) UserProfile(ctx context.Context, userID string) (UserProfile, error) {
	return readUserProfile(s.db, ctx, "SELECT "+profileColumns+" FROM users WHERE id=?", userID)
}

func validProfileText(value string) bool {
	return len(value) <= 4096 && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

// ValidateAndNormalizeUISettings validates an extensible uiSettings JSON object,
// enforcing size, allowed keys, enum values, and #RRGGBB hex colors.
func ValidateAndNormalizeUISettings(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || len(raw) > MaxUISettingsBytes {
		return "", ErrInvalidUISettings
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return "", ErrInvalidUISettings
	}
	normalized := make(map[string]any, len(obj))
	for k, v := range obj {
		switch k {
		case "theme":
			s, ok := v.(string)
			if !ok || (s != "dark" && s != "light" && s != "system") {
				return "", ErrInvalidUISettings
			}
			normalized[k] = s
		case "accent":
			s, ok := v.(string)
			switch s {
			case "mint", "ocean", "violet", "amber", "rose", "graphite", "custom":
			default:
				ok = false
			}
			if !ok {
				return "", ErrInvalidUISettings
			}
			normalized[k] = s
		case "gradientPalette":
			s, ok := v.(string)
			switch s {
			case "accent", "indigo", "ocean", "emerald", "sunset", "monochrome", "custom":
			default:
				ok = false
			}
			if !ok {
				return "", ErrInvalidUISettings
			}
			normalized[k] = s
		case "accentCustomColor", "gradientCustomColor", "gradientCustomColor2":
			s, ok := v.(string)
			if !ok {
				return "", ErrInvalidUISettings
			}
			trimmed := strings.TrimSpace(s)
			if !hexColorRE.MatchString(trimmed) {
				return "", ErrInvalidUISettings
			}
			normalized[k] = strings.ToLower(trimmed)
		case "ambientGradient":
			b, ok := v.(bool)
			if !ok {
				return "", ErrInvalidUISettings
			}
			normalized[k] = b
		default:
			return "", ErrInvalidUISettings
		}
	}
	out, err := json.Marshal(normalized)
	if err != nil || len(out) > MaxUISettingsBytes {
		return "", ErrInvalidUISettings
	}
	return string(out), nil
}

func (s *Store) UpdateUserProfile(ctx context.Context, userID string, displayName, profileMD, memoryMD *string, memoryEnabled *bool, avatarData ...*string) (UserProfile, error) {
	return s.UpdateUserProfileWithUI(ctx, userID, displayName, profileMD, memoryMD, memoryEnabled, nil, avatarData...)
}

func (s *Store) UpdateUserProfileWithUI(ctx context.Context, userID string, displayName, profileMD, memoryMD *string, memoryEnabled *bool, uiSettings *json.RawMessage, avatarData ...*string) (UserProfile, error) {
	var avatar *string
	if len(avatarData) > 0 && avatarData[0] != nil {
		cleanAvatar, err := uploads.SanitizeAvatarDataURL(*avatarData[0])
		if err != nil {
			return UserProfile{}, ErrInvalidProfile
		}
		avatar = &cleanAvatar
	}
	var uiJSON *string
	if uiSettings != nil {
		norm, err := ValidateAndNormalizeUISettings(*uiSettings)
		if err != nil {
			return UserProfile{}, err
		}
		uiJSON = &norm
	}
	if displayName != nil && (!utf8.ValidString(*displayName) || utf8.RuneCountInString(*displayName) > 80 ||
		strings.ContainsFunc(*displayName, func(r rune) bool { return unicode.IsControl(r) || r == '\u2028' || r == '\u2029' })) ||
		profileMD != nil && !validProfileText(*profileMD) || memoryMD != nil && !validProfileText(*memoryMD) {
		return UserProfile{}, ErrInvalidProfile
	}
	if displayName != nil {
		trimmed := strings.TrimSpace(*displayName)
		displayName = &trimmed
	}
	// A manual memory edit or setting change advances the cutoff so an in-flight
	// summary cannot restore cleared content or replay messages from before opt-in.
	return readUserProfile(s.db, ctx, `UPDATE users SET
  display_name=COALESCE(?2,display_name), profile_md=COALESCE(?3,profile_md),
  memory_md=COALESCE(?4,memory_md), memory_enabled=COALESCE(?5,memory_enabled),
  avatar_data=COALESCE(?7,avatar_data),
  ui_settings_json=COALESCE(?8,ui_settings_json),
  memory_updated_at=CASE WHEN ?4 IS NOT NULL OR (?5 IS NOT NULL AND ?5 != memory_enabled)
    THEN MAX(memory_updated_at+1,?6) ELSE memory_updated_at END
  WHERE id=?1 RETURNING `+profileColumns, userID, displayName, profileMD, memoryMD, memoryEnabled, time.Now().UnixMilli(), avatar, uiJSON)
}

func (s *Store) SaveUserMemory(ctx context.Context, userID, memoryMD string, updatedAt int64) error {
	return s.saveUserMemory(ctx, userID, memoryMD, updatedAt, nil, "[]")
}

// SaveUserMemoryIfCurrent discards a stale summary if the profile or its source
// chats changed while the provider was working.
func (s *Store) SaveUserMemoryIfCurrent(ctx context.Context, userID, memoryMD string, updatedAt, since int64, chatIDs []string) error {
	ids, err := json.Marshal(chatIDs)
	if err != nil {
		return err
	}
	return s.saveUserMemory(ctx, userID, memoryMD, updatedAt, &since, string(ids))
}

func (s *Store) saveUserMemory(ctx context.Context, userID, memoryMD string, updatedAt int64, since *int64, chatIDs string) error {
	if !validProfileText(memoryMD) {
		return ErrInvalidProfile
	}
	_, err := s.db.ExecContext(ctx, `UPDATE users SET memory_md=?2,memory_updated_at=?3
  WHERE id=?1 AND is_active=1 AND role IN ('user','admin') AND memory_enabled=1 AND memory_updated_at<?3
  AND (?4 IS NULL OR memory_updated_at=?4)
  AND NOT EXISTS (SELECT 1 FROM json_each(?5) src WHERE NOT EXISTS (
   SELECT 1 FROM chats c WHERE c.id=src.value AND c.user_id=?1
   AND NOT EXISTS(SELECT 1 FROM chat_shares WHERE chat_id=c.id)
  ))`, userID, memoryMD, updatedAt, since, chatIDs)
	return err
}

// Forked rows have an empty sender marker. Live and legacy messages use a
// sender ID or NULL, so copying a conversation does not copy it into memory.
const personalMemoryMessages = `NOT EXISTS(SELECT 1 FROM chat_shares WHERE chat_id=c.id)
 AND m.memory_private=1 AND (m.run_user_id IS NULL OR m.run_user_id != '')`

func (s *Store) UsersPendingMemory(ctx context.Context, minNewMessages int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT u.id FROM users u WHERE u.is_active=1
  AND u.role IN ('user','admin') AND u.memory_enabled=1 AND (
   SELECT COUNT(*) FROM chats c JOIN chat_messages m ON m.chat_id=c.id
   WHERE c.user_id=u.id AND m.created_at>u.memory_updated_at AND `+personalMemoryMessages+`
  )>=? ORDER BY u.id`, minNewMessages)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) RecentPersonalMessagesSince(ctx context.Context, userID string, since int64, limit int) ([]Message, int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,chat_id,role,content,created_at FROM (
  SELECT m.rowid AS message_order,m.id,m.chat_id,m.role,m.content,m.created_at
  FROM chats c JOIN chat_messages m ON m.chat_id=c.id
  WHERE c.user_id=?1 AND m.created_at>?2 AND `+personalMemoryMessages+`
  ORDER BY m.created_at DESC,m.rowid DESC LIMIT ?3
 ) ORDER BY created_at,message_order`, userID, since, min(max(limit, 0), 40))
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var messages []Message
	var newest int64
	for rows.Next() {
		var message Message
		if err := rows.Scan(&message.ID, &message.ChatID, &message.Role, &message.Content, &message.CreatedAt); err != nil {
			return nil, 0, err
		}
		messages = append(messages, message)
		newest = message.CreatedAt
	}
	return messages, newest, rows.Err()
}

func (s *Store) IsChatShared(ctx context.Context, chatID string) (bool, error) {
	var shared bool
	err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM chat_shares WHERE chat_id=?)", chatID).Scan(&shared)
	return shared, err
}

// ChatRunContext reads the saved session and sharing state from the same snapshot.
func (s *Store) ChatRunContext(ctx context.Context, chatID string) (string, bool, error) {
	sessionID, _, shared, err := s.ChatRunState(ctx, chatID)
	return sessionID, shared, err
}

// ChatRunState reads the saved session, stored provider, and sharing state from the same snapshot.
func (s *Store) ChatRunState(ctx context.Context, chatID string) (sessionID, provider string, shared bool, err error) {
	sessionID, provider, _, shared, err = s.ChatRunSessionState(ctx, chatID)
	return sessionID, provider, shared, err
}

// ChatRunSessionState reads the saved session, stored provider, session provider, and sharing state from the same snapshot.
func (s *Store) ChatRunSessionState(ctx context.Context, chatID string) (sessionID, provider, sessionProvider string, shared bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT COALESCE(provider_session_id,''), provider,
  CASE WHEN session_provider='' THEN provider ELSE session_provider END,
  EXISTS(SELECT 1 FROM chat_shares WHERE chat_id=c.id) FROM chats c WHERE id=?`, chatID).
		Scan(&sessionID, &provider, &sessionProvider, &shared)
	return sessionID, provider, sessionProvider, shared, err
}
