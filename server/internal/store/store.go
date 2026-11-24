package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var initialSchema string

type Store struct{ db *sql.DB }

type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	IsActive bool   `json:"isActive"`
}

type Device struct {
	ID         string `json:"id"`
	UserID     string `json:"userId"`
	Name       string `json:"name"`
	LastSeenAt int64  `json:"lastSeenAt"`
	CreatedAt  int64  `json:"createdAt"`
}

type Chat struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

type Message struct {
	Progress   string `json:"progress,omitempty"`
	DurationMs *int64 `json:"durationMs,omitempty"`
	RunID      string `json:"runId,omitempty"`
	ID         string `json:"id"`
	ChatID     string `json:"chatId"`
	Role       string `json:"role"`
	Content    string `json:"content"`
	Thinking   string `json:"thinking"`
	CreatedAt  int64  `json:"createdAt"`
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func Open(ctx context.Context, path string) (*Store, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, err
		}
		// SQLite reuses this file and its permissions when it opens the database.
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, err
		}
		if err := f.Close(); err != nil {
			return nil, err
		}
	}
	dsn := "file::memory:"
	if path != ":memory:" {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		dsn = (&url.URL{Scheme: "file", Path: filepath.ToSlash(absolute)}).String()
	}
	dsn += "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	if path != ":memory:" {
		dsn += "&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate SQLite: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	switch version {
	case 0:
		if _, err := tx.ExecContext(ctx, initialSchema); err != nil {
			return err
		}
		fallthrough
	case 1:
		if _, err := tx.ExecContext(ctx, `ALTER TABLE chat_messages ADD COLUMN run_id TEXT;
			CREATE UNIQUE INDEX chat_message_runs ON chat_messages (chat_id,role,run_id);
			PRAGMA user_version=2;`); err != nil {
			return err
		}
		fallthrough
	case 2:
		if _, err := tx.ExecContext(ctx, `ALTER TABLE chat_messages ADD COLUMN duration_ms INTEGER;
			PRAGMA user_version=3;`); err != nil {
			return err
		}
		fallthrough
	case 3:
		for _, column := range []struct{ name, definition string }{
			{"role", "TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('user', 'admin'))"},
			{"is_active", "INTEGER NOT NULL DEFAULT 1 CHECK (is_active IN (0, 1))"},
		} {
			var exists bool
			if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pragma_table_info('users') WHERE name=?)", column.name).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				if _, err := tx.ExecContext(ctx, "ALTER TABLE users ADD COLUMN "+column.name+" "+column.definition); err != nil {
					return err
				}
			}
		}
		if _, err := tx.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS devices_by_user ON devices(user_id); PRAGMA user_version=4;"); err != nil {
			return err
		}
		fallthrough
	case 4:
		var exists bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pragma_table_info('devices') WHERE name='expires_at')").Scan(&exists); err != nil {
			return err
		}
		if !exists {
			if _, err := tx.ExecContext(ctx, "ALTER TABLE devices ADD COLUMN expires_at INTEGER NOT NULL DEFAULT 0"); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "PRAGMA user_version=5"); err != nil {
			return err
		}
	case 5:
	default:
		return fmt.Errorf("unsupported schema version %d", version)
	}
	return tx.Commit()
}

var (
	ErrUsernameTaken = errors.New("username already exists")
	ErrInactiveUser  = errors.New("account is inactive")
)

func ValidRole(role string) bool { return role == "user" || role == "admin" }

func (s *Store) CreateUser(ctx context.Context, username string, passwordHash []byte) error {
	_, err := s.CreateUserWithRole(ctx, username, passwordHash, "user")
	return err
}

func (s *Store) Credentials(ctx context.Context, username string) (User, []byte, error) {
	var user User
	var hash []byte
	err := s.db.QueryRowContext(ctx,
		"SELECT id, username, role, is_active, password_hash FROM users WHERE username=?", username).
		Scan(&user.ID, &user.Username, &user.Role, &user.IsActive, &hash)
	return user, hash, err
}

func (s *Store) IssueDevice(ctx context.Context, userID, name string) (Device, string, error) {
	return s.IssueDeviceForLogin(ctx, userID, name, nil)
}

const deviceLifetime = 30 * 24 * time.Hour

// Match the verified hash at insert time so a concurrent password reset cannot mint a token.
func (s *Store) IssueDeviceForLogin(ctx context.Context, userID, name string, verifiedHash []byte) (Device, string, error) {
	token := randomHex(32)
	hash := sha256.Sum256([]byte(token))
	now := time.Now().UnixMilli()
	device := Device{ID: randomHex(16), UserID: userID, Name: name, LastSeenAt: now, CreatedAt: now}
	var expected any
	if verifiedHash != nil {
		expected = verifiedHash
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Device{}, "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM devices WHERE expires_at > 0 AND expires_at <= ?", now); err != nil {
		return Device{}, "", err
	}
	err = tx.QueryRowContext(ctx, `INSERT INTO devices (id,user_id,name,token_hash,last_seen_at,created_at,expires_at)
  SELECT ?, id, ?, ?, ?, ?, ? FROM users WHERE id=? AND is_active=1 AND (? IS NULL OR password_hash=?)
		RETURNING id`, device.ID, name, hash[:], now, now, now+deviceLifetime.Milliseconds(), userID, expected, expected).Scan(&device.ID)
	if errors.Is(err, sql.ErrNoRows) {
		var active bool
		if lookup := tx.QueryRowContext(ctx, "SELECT is_active FROM users WHERE id=?", userID).Scan(&active); lookup == nil && !active {
			err = ErrInactiveUser
		}
	}
	if err == nil {
		err = tx.Commit()
	}
	return device, token, err
}

func (s *Store) Authenticate(ctx context.Context, token string) (User, Device, error) {
	hash := sha256.Sum256([]byte(token))
	now := time.Now().UnixMilli()
	user, device, err := s.activeDevice(ctx, "d.token_hash=?", now, hash[:])
	cutoff := now - time.Minute.Milliseconds()
	if err != nil || device.LastSeenAt > cutoff {
		return user, device, err
	}
	err = s.db.QueryRowContext(ctx, `UPDATE devices SET last_seen_at=?, expires_at=?
  WHERE id=? AND last_seen_at <= ? AND (expires_at=0 OR expires_at>?)
   AND user_id IN (SELECT id FROM users WHERE id=? AND is_active=1)
  RETURNING last_seen_at`, now, now+deviceLifetime.Milliseconds(), device.ID, cutoff, now, user.ID).Scan(&device.LastSeenAt)
	if errors.Is(err, sql.ErrNoRows) {
		// A concurrent refresh or revocation can change the row after the initial read.
		return s.activeDevice(ctx, "d.token_hash=?", now, hash[:])
	}
	return user, device, err
}

func (s *Store) AuthenticateDevice(ctx context.Context, userID, deviceID string) (User, Device, error) {
	return s.activeDevice(ctx, "d.user_id=? AND d.id=?", time.Now().UnixMilli(), userID, deviceID)
}

func (s *Store) activeDevice(ctx context.Context, predicate string, now int64, args ...any) (User, Device, error) {
	var user User
	var device Device
	args = append(args, now)
	err := s.db.QueryRowContext(ctx, `SELECT d.id,d.user_id,d.name,d.last_seen_at,d.created_at,
  u.username,u.role,u.is_active FROM devices d JOIN users u ON u.id=d.user_id
  WHERE `+predicate+` AND (d.expires_at=0 OR d.expires_at>?)`, args...).
		Scan(&device.ID, &device.UserID, &device.Name, &device.LastSeenAt, &device.CreatedAt, &user.Username, &user.Role, &user.IsActive)
	user.ID = device.UserID
	if err == nil && !user.IsActive {
		err = ErrInactiveUser
	}
	return user, device, err
}

func (s *Store) CreateChat(ctx context.Context, userID, title, provider, model string) (Chat, error) {
	now := time.Now().UnixMilli()
	chat := Chat{ID: randomHex(16), Title: title, Provider: provider, Model: model, CreatedAt: now, UpdatedAt: now}
	_, err := s.db.ExecContext(ctx, `INSERT INTO chats (id,user_id,title,provider,model,created_at,updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, chat.ID, userID, title, provider, model, now, now)
	return chat, err
}

func (s *Store) Chats(ctx context.Context, userID string) ([]Chat, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,title,provider,model,created_at,updated_at
		FROM chats WHERE user_id=? ORDER BY updated_at DESC, rowid DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	chats := []Chat{}
	for rows.Next() {
		var c Chat
		if err := rows.Scan(&c.ID, &c.Title, &c.Provider, &c.Model, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		chats = append(chats, c)
	}
	return chats, rows.Err()
}

func (s *Store) Messages(ctx context.Context, userID, chatID string) ([]Message, error) {
	// A LEFT JOIN returns one row for an owned, empty chat and none for unknown or foreign chats.
	rows, err := s.db.QueryContext(ctx, `SELECT CASE WHEN m.role='assistant' THEN COALESCE(m.run_id,m.id) ELSE m.id END, m.role,m.content,m.thinking,m.created_at,m.run_id,m.duration_ms
		FROM chats c LEFT JOIN chat_messages m ON m.chat_id=c.id
		WHERE c.id=? AND c.user_id=? ORDER BY m.created_at, m.rowid`, chatID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	messages := []Message{}
	found := false
	for rows.Next() {
		found = true
		var id, role, content, thinking, runID sql.NullString
		var createdAt sql.NullInt64
		var durationMs *int64
		if err := rows.Scan(&id, &role, &content, &thinking, &createdAt, &runID, &durationMs); err != nil {
			return nil, err
		}
		if id.Valid {
			messages = append(messages, Message{ID: id.String, RunID: runID.String, ChatID: chatID, Role: role.String, Content: content.String, Thinking: thinking.String, CreatedAt: createdAt.Int64, DurationMs: durationMs})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !found {
		return nil, sql.ErrNoRows
	}
	return messages, nil
}
