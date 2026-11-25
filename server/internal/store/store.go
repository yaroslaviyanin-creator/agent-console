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
	"strings"
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
	Subscription
}

type Device struct {
	ID         string `json:"id"`
	UserID     string `json:"userId"`
	Name       string `json:"name"`
	LastSeenAt int64  `json:"lastSeenAt"`
	CreatedAt  int64  `json:"createdAt"`
}

type ChatGrant struct {
	UserID string `json:"userId"`
	Access string `json:"access"`
}

type Chat struct {
	OwnerID   string      `json:"ownerId,omitempty"`
	Access    string      `json:"access,omitempty"`
	Grants    []ChatGrant `json:"grants,omitempty"`
	ID        string      `json:"id"`
	Title     string      `json:"title"`
	Provider  string      `json:"provider"`
	Model     string      `json:"model"`
	CreatedAt int64       `json:"createdAt"`
	UpdatedAt int64       `json:"updatedAt"`
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
		p := filepath.ToSlash(absolute)
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		dsn = (&url.URL{Scheme: "file", Path: p}).String()
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
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var version int
	if err := conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version == 12 {
		return nil
	}
	// SQLite requires disabling foreign keys outside the transaction on this connection.
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	switch version {
	case 0:
		if _, err := tx.ExecContext(ctx, initialSchema+"\nPRAGMA user_version=12;"); err != nil {
			return err
		}
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
		fallthrough
	case 5:
		// Existing accounts had unrestricted access before plans were introduced.
		if _, err := tx.ExecContext(ctx, `CREATE TABLE users_v6 (
			id TEXT PRIMARY KEY,
			username TEXT NOT NULL UNIQUE,
			password_hash BLOB NOT NULL,
			created_at INTEGER NOT NULL,
			role TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('pending','user','admin')),
			is_active INTEGER NOT NULL DEFAULT 1 CHECK (is_active IN (0,1)),
			plan_tier TEXT NOT NULL DEFAULT 'none' CHECK (plan_tier IN ('none','pro','2x_pro','unlimited')),
			bypass_5h_limit INTEGER NOT NULL DEFAULT 0 CHECK (bypass_5h_limit IN (0,1)),
			subscription_until INTEGER NOT NULL DEFAULT 0 CHECK (subscription_until >= 0)
		);
		INSERT INTO users_v6 (id,username,password_hash,created_at,role,is_active,plan_tier)
			SELECT id,username,password_hash,created_at,role,is_active,'unlimited' FROM users;
		DROP TABLE users;
		ALTER TABLE users_v6 RENAME TO users;
		CREATE INDEX chat_messages_by_role_time ON chat_messages(chat_id,role,created_at);
		PRAGMA user_version=6;`); err != nil {
			return err
		}
		var table string
		switch err := tx.QueryRowContext(ctx, "PRAGMA foreign_key_check").Scan(&table, new(sql.NullInt64), new(string), new(int)); {
		case err == nil:
			return fmt.Errorf("foreign key violation in %s", table)
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		fallthrough
	case 6:
		if _, err := tx.ExecContext(ctx, `CREATE TABLE chat_shares (
			chat_id TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
			user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			access TEXT NOT NULL CHECK (access IN ('read','fork','write')),
			created_at INTEGER NOT NULL,
			PRIMARY KEY (chat_id,user_id)
		);
		CREATE INDEX chat_shares_by_user ON chat_shares(user_id);
		ALTER TABLE chat_messages ADD COLUMN run_user_id TEXT;
		CREATE INDEX chat_messages_by_sender ON chat_messages(run_user_id,created_at) WHERE role='user';
		PRAGMA user_version=7;`); err != nil {
			return err
		}
		fallthrough
	case 7:
		if _, err := tx.ExecContext(ctx, `CREATE TABLE usage_events (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			chat_id TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
			run_id TEXT NOT NULL,
			provider TEXT NOT NULL,
			model TEXT NOT NULL,
			input_tokens INTEGER NOT NULL DEFAULT 0,
			cached_input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			cost_cents INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL,
			UNIQUE (chat_id,run_id)
		);
		CREATE INDEX usage_events_by_user_time ON usage_events (user_id,created_at);
		PRAGMA user_version=8;`); err != nil {
			return err
		}
		fallthrough
	case 8:
		if _, err := tx.ExecContext(ctx, `ALTER TABLE users ADD COLUMN display_name TEXT NOT NULL DEFAULT '';
   ALTER TABLE users ADD COLUMN profile_md TEXT NOT NULL DEFAULT '';
   ALTER TABLE users ADD COLUMN memory_md TEXT NOT NULL DEFAULT '';
   ALTER TABLE users ADD COLUMN memory_enabled INTEGER NOT NULL DEFAULT 1 CHECK (memory_enabled IN (0,1));
   ALTER TABLE users ADD COLUMN memory_updated_at INTEGER NOT NULL DEFAULT 0;
   ALTER TABLE chat_messages ADD COLUMN memory_private INTEGER NOT NULL DEFAULT 1 CHECK (memory_private IN (0,1));
   UPDATE chat_messages SET memory_private=0 WHERE chat_id IN (SELECT chat_id FROM chat_shares);
   PRAGMA user_version=9;`); err != nil {
			return err
		}
		fallthrough
	case 9:
		if _, err := tx.ExecContext(ctx, `CREATE TABLE chats_v10 (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			title TEXT NOT NULL,
			provider TEXT NOT NULL CHECK (provider IN ('codex', 'stub', 'gemini')),
			model TEXT NOT NULL,
			provider_session_id TEXT,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		);
		INSERT INTO chats_v10 (id,user_id,title,provider,model,provider_session_id,created_at,updated_at)
			SELECT id,user_id,title,provider,model,provider_session_id,created_at,updated_at FROM chats;
		DROP TABLE chats;
		ALTER TABLE chats_v10 RENAME TO chats;
		CREATE INDEX chats_by_user ON chats (user_id, updated_at DESC);
		PRAGMA user_version=10;`); err != nil {
			return err
		}
		var table string
		switch err := tx.QueryRowContext(ctx, "PRAGMA foreign_key_check").Scan(&table, new(sql.NullInt64), new(string), new(int)); {
		case err == nil:
			return fmt.Errorf("foreign key violation in %s", table)
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		fallthrough
	case 10:
		if _, err := tx.ExecContext(ctx, `CREATE TABLE chats_v11 (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			title TEXT NOT NULL,
			provider TEXT NOT NULL CHECK (provider IN ('codex', 'stub', 'gemini', 'antigravity')),
			model TEXT NOT NULL,
			provider_session_id TEXT,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		);
		INSERT INTO chats_v11 (id,user_id,title,provider,model,provider_session_id,created_at,updated_at)
			SELECT id,user_id,title,provider,model,provider_session_id,created_at,updated_at FROM chats;
		DROP TABLE chats;
		ALTER TABLE chats_v11 RENAME TO chats;
		CREATE INDEX chats_by_user ON chats (user_id, updated_at DESC);
		PRAGMA user_version=11;`); err != nil {
			return err
		}
		var tableV11 string
		switch err := tx.QueryRowContext(ctx, "PRAGMA foreign_key_check").Scan(&tableV11, new(sql.NullInt64), new(string), new(int)); {
		case err == nil:
			return fmt.Errorf("foreign key violation in %s", tableV11)
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		fallthrough
	case 11:
		if _, err := tx.ExecContext(ctx, `CREATE TABLE chats_v12 (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			title TEXT NOT NULL,
			provider TEXT NOT NULL CHECK (provider IN ('antigravity', 'codex', 'claude', 'paid_api', 'stub')),
			model TEXT NOT NULL,
			provider_session_id TEXT,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		);
		INSERT INTO chats_v12 (id,user_id,title,provider,model,provider_session_id,created_at,updated_at)
			SELECT id,user_id,title,CASE WHEN provider='gemini' THEN 'antigravity' ELSE provider END,model,provider_session_id,created_at,updated_at FROM chats;
		DROP TABLE chats;
		ALTER TABLE chats_v12 RENAME TO chats;
		CREATE INDEX chats_by_user ON chats (user_id, updated_at DESC);
		PRAGMA user_version=12;`); err != nil {
			return err
		}
		var tableV12 string
		switch err := tx.QueryRowContext(ctx, "PRAGMA foreign_key_check").Scan(&tableV12, new(sql.NullInt64), new(string), new(int)); {
		case err == nil:
			return fmt.Errorf("foreign key violation in %s", tableV12)
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
	case 12:
	default:
		return fmt.Errorf("unsupported schema version %d", version)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "PRAGMA foreign_keys=ON")
	return err
}

var (
	ErrUsernameTaken = errors.New("username already exists")
	ErrInactiveUser  = errors.New("account is inactive")
)

func ValidRole(role string) bool { return role == "pending" || role == "user" || role == "admin" }

func (s *Store) CreateUser(ctx context.Context, username string, passwordHash []byte) error {
	_, err := s.CreateUserWithRole(ctx, username, passwordHash, "user")
	return err
}

func (s *Store) Credentials(ctx context.Context, username string) (User, []byte, error) {
	var user User
	var hash []byte
	err := s.db.QueryRowContext(ctx,
		"SELECT id, username, role, is_active, plan_tier, bypass_5h_limit, subscription_until, password_hash FROM users WHERE username=?", username).
		Scan(&user.ID, &user.Username, &user.Role, &user.IsActive, &user.PlanTier, &user.Bypass5hLimit, &user.SubscriptionUntil, &hash)
	return user, hash, err
}

func (s *Store) IssueDevice(ctx context.Context, userID, name string) (Device, string, error) {
	return s.IssueDeviceForLogin(ctx, userID, name, nil)
}

const deviceLifetime = 30 * 24 * time.Hour

// Match the verified hash at insert time so a concurrent password reset cannot mint a token.
func (s *Store) IssueDeviceForLogin(ctx context.Context, userID, name string, verifiedHash []byte) (Device, string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Device{}, "", err
	}
	defer tx.Rollback()
	device, token, err := issueDevice(ctx, tx, userID, name, verifiedHash)
	if err == nil {
		err = tx.Commit()
	}
	return device, token, err
}

func issueDevice(ctx context.Context, tx *sql.Tx, userID, name string, verifiedHash []byte) (Device, string, error) {
	token := randomHex(32)
	hash := sha256.Sum256([]byte(token))
	now := time.Now().UnixMilli()
	device := Device{ID: randomHex(16), UserID: userID, Name: name, LastSeenAt: now, CreatedAt: now}
	var expected any
	if verifiedHash != nil {
		expected = verifiedHash
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM devices WHERE expires_at > 0 AND expires_at <= ?", now); err != nil {
		return Device{}, "", err
	}
	err := tx.QueryRowContext(ctx, `INSERT INTO devices (id,user_id,name,token_hash,last_seen_at,created_at,expires_at)
  SELECT ?, id, ?, ?, ?, ?, ? FROM users WHERE id=? AND is_active=1 AND (? IS NULL OR password_hash=?)
		RETURNING id`, device.ID, name, hash[:], now, now, now+deviceLifetime.Milliseconds(), userID, expected, expected).Scan(&device.ID)
	if errors.Is(err, sql.ErrNoRows) {
		var active bool
		if lookup := tx.QueryRowContext(ctx, "SELECT is_active FROM users WHERE id=?", userID).Scan(&active); lookup == nil && !active {
			err = ErrInactiveUser
		}
	}
	return device, token, err
}

func (s *Store) Authenticate(ctx context.Context, token string) (User, Device, error) {
	hash := sha256.Sum256([]byte(token))
	now := time.Now().UnixMilli()
	user, device, err := activeDevice(ctx, s.db, "d.token_hash=?", now, hash[:])
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
		return activeDevice(ctx, s.db, "d.token_hash=?", now, hash[:])
	}
	return user, device, err
}

func (s *Store) AuthenticateDevice(ctx context.Context, userID, deviceID string) (User, Device, error) {
	return activeDevice(ctx, s.db, "d.user_id=? AND d.id=?", time.Now().UnixMilli(), userID, deviceID)
}

func activeDevice(ctx context.Context, db rowQuerier, predicate string, now int64, args ...any) (User, Device, error) {
	var user User
	var device Device
	args = append(args, now)
	err := db.QueryRowContext(ctx, `SELECT d.id,d.user_id,d.name,d.last_seen_at,d.created_at,
  u.username,u.role,u.is_active,u.plan_tier,u.bypass_5h_limit,u.subscription_until FROM devices d JOIN users u ON u.id=d.user_id
  WHERE `+predicate+` AND (d.expires_at=0 OR d.expires_at>?)`, args...).
		Scan(&device.ID, &device.UserID, &device.Name, &device.LastSeenAt, &device.CreatedAt, &user.Username, &user.Role, &user.IsActive, &user.PlanTier, &user.Bypass5hLimit, &user.SubscriptionUntil)
	user.ID = device.UserID
	if err == nil && !user.IsActive {
		err = ErrInactiveUser
	}
	return user, device, err
}

func (s *Store) CreateChat(ctx context.Context, userID, title, provider, model string) (Chat, error) {
	now := time.Now().UnixMilli()
	chat := Chat{ID: randomHex(16), OwnerID: userID, Access: "owner", Title: title, Provider: provider, Model: model, CreatedAt: now, UpdatedAt: now}
	return chat, insertChat(ctx, s.db, chat)
}

func insertChat(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, chat Chat) error {
	_, err := db.ExecContext(ctx, `INSERT INTO chats (id,user_id,title,provider,model,created_at,updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, chat.ID, chat.OwnerID, chat.Title, chat.Provider, chat.Model, chat.CreatedAt, chat.UpdatedAt)
	return err
}

func (s *Store) Chats(ctx context.Context, userID string) ([]Chat, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.id,c.title,c.provider,c.model,c.created_at,c.updated_at,c.user_id,
		COALESCE(membership.access,'owner'),g.user_id,g.access
		FROM chats c LEFT JOIN chat_shares membership ON membership.chat_id=c.id AND membership.user_id=?1
		LEFT JOIN chat_shares g ON g.chat_id=c.id AND c.user_id=?1
		WHERE c.user_id=?1 OR membership.user_id IS NOT NULL
		ORDER BY c.updated_at DESC,c.rowid DESC,g.created_at,g.user_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	chats := []Chat{}
	for rows.Next() {
		var c Chat
		var recipient, grant sql.NullString
		if err := rows.Scan(&c.ID, &c.Title, &c.Provider, &c.Model, &c.CreatedAt, &c.UpdatedAt, &c.OwnerID, &c.Access, &recipient, &grant); err != nil {
			return nil, err
		}
		if len(chats) == 0 || chats[len(chats)-1].ID != c.ID {
			chats = append(chats, c)
		}
		if recipient.Valid {
			last := &chats[len(chats)-1]
			last.Grants = append(last.Grants, ChatGrant{UserID: recipient.String, Access: grant.String})
		}
	}
	return chats, rows.Err()
}

func (s *Store) ChatMembers(ctx context.Context, chatID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT u.id FROM users u
		WHERE u.is_active=1 AND u.role IN ('user','admin') AND u.id IN (
			SELECT user_id FROM chats WHERE id=?1
			UNION ALL SELECT user_id FROM chat_shares WHERE chat_id=?1
		)`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	members := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		members = append(members, id)
	}
	return members, rows.Err()
}

func ValidShareAccess(access string) bool {
	return access == "read" || access == "fork" || access == "write"
}

var ErrInvalidShare = errors.New("invalid share recipient or access")

func (s *Store) SetChatShare(ctx context.Context, ownerID, chatID, targetUserID, access string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var wasShared bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM chat_shares WHERE chat_id=c.id) FROM chats c WHERE id=? AND user_id=?", chatID, ownerID).Scan(&wasShared); err != nil {
		return err
	}
	if ownerID == targetUserID || (access != "" && !ValidShareAccess(access)) {
		return ErrInvalidShare
	}
	if access == "" {
		_, err = tx.ExecContext(ctx, "DELETE FROM chat_shares WHERE chat_id=? AND user_id=?", chatID, targetUserID)
	} else {
		var eligible bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users
			WHERE id=? AND is_active=1 AND role IN ('user','admin'))`, targetUserID).Scan(&eligible); err != nil {
			return err
		}
		if !eligible {
			return ErrInvalidShare
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO chat_shares (chat_id,user_id,access,created_at) VALUES (?,?,?,?)
			ON CONFLICT(chat_id,user_id) DO UPDATE SET access=excluded.access`, chatID, targetUserID, access, time.Now().UnixMilli())
	}
	if err != nil {
		return err
	}
	// A session can contain developer instructions from earlier turns.
	if _, err := tx.ExecContext(ctx, `UPDATE chats SET provider_session_id=NULL WHERE id=?1
  AND ?2 != EXISTS(SELECT 1 FROM chat_shares WHERE chat_id=?1)`, chatID, wasShared); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ForkChat(ctx context.Context, userID, sourceChatID string) (Chat, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Chat{}, err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	c := Chat{ID: randomHex(16), OwnerID: userID, Access: "owner", CreatedAt: now, UpdatedAt: now}
	if err := tx.QueryRowContext(ctx, `SELECT title,provider,model FROM chats c WHERE c.id=?1 AND
		(c.user_id=?2 OR EXISTS(SELECT 1 FROM chat_shares WHERE chat_id=c.id AND user_id=?2 AND access='fork'))`,
		sourceChatID, userID).Scan(&c.Title, &c.Provider, &c.Model); err != nil {
		return Chat{}, err
	}
	c.Title += " · копия"
	if err := insertChat(ctx, tx, c); err != nil {
		return Chat{}, err
	}
	// Copied messages keep their run IDs for transcript pairing and do not count toward quotas.
	if _, err := tx.ExecContext(ctx, `INSERT INTO chat_messages (id,chat_id,role,content,thinking,created_at,run_id,duration_ms,run_user_id)
		SELECT lower(hex(randomblob(16))),?,role,content,thinking,created_at,run_id,duration_ms,''
		FROM chat_messages WHERE chat_id=? ORDER BY created_at,rowid`, c.ID, sourceChatID); err != nil {
		return Chat{}, err
	}
	return c, tx.Commit()
}

func (s *Store) Messages(ctx context.Context, userID, chatID string) ([]Message, error) {
	return s.messages(ctx, userID, chatID, true)
}

func (s *Store) messages(ctx context.Context, userID, chatID string, includeShared bool) ([]Message, error) {
	// A LEFT JOIN distinguishes an accessible empty chat from an inaccessible chat.
	rows, err := s.db.QueryContext(ctx, `SELECT CASE WHEN m.role='assistant' THEN COALESCE(m.run_id,m.id) ELSE m.id END, m.role,m.content,m.thinking,m.created_at,m.run_id,m.duration_ms
		FROM chats c LEFT JOIN chat_messages m ON m.chat_id=c.id
		WHERE c.id=?1 AND (c.user_id=?2 OR (?3 AND EXISTS(
			SELECT 1 FROM chat_shares WHERE chat_id=c.id AND user_id=?2
		))) ORDER BY m.created_at,m.rowid`, chatID, userID, includeShared)
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
