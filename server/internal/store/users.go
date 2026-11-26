package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"time"
)

type Subscription struct {
	PlanTier          string `json:"planTier"`
	Bypass5hLimit     bool   `json:"bypass5hLimit"`
	SubscriptionUntil int64  `json:"subscriptionUntil"`
}

type SubscriptionUpdate struct {
	PlanTier          *string `json:"planTier"`
	Bypass5hLimit     *bool   `json:"bypass5hLimit"`
	SubscriptionUntil *int64  `json:"subscriptionUntil"`
}

func (s SubscriptionUpdate) Valid() bool {
	return (s.PlanTier == nil || ValidPlanTier(*s.PlanTier)) &&
		(s.SubscriptionUntil == nil || *s.SubscriptionUntil >= 0)
}

func ValidPlanTier(tier string) bool {
	switch tier {
	case "none", "pro", "2x_pro", "unlimited":
		return true
	}
	return false
}

type AdminUser struct {
	ResetsAt5h int64 `json:"resetsAt5h"`
	ResetsAt7d int64 `json:"resetsAt7d"`
	User
	DevicesCount int   `json:"devicesCount"`
	ChatsCount   int   `json:"chatsCount"`
	CreatedAt    int64 `json:"createdAt"`
	Usage5h      int64 `json:"usage5h"`
	Usage7d      int64 `json:"usage7d"`
	Limit5h      int64 `json:"limit5h"`
	Limit7d      int64 `json:"limit7d"`
}

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) Username(ctx context.Context, userID string) (string, error) {
	var name string
	err := s.db.QueryRowContext(ctx, "SELECT username FROM users WHERE id=?", userID).Scan(&name)
	return name, err
}

func (s *Store) CreateUserWithRole(ctx context.Context, username string, hash []byte, role string, subscriptions ...Subscription) (AdminUser, error) {
	subscription := Subscription{PlanTier: "none"}
	if len(subscriptions) > 0 {
		subscription = subscriptions[0]
	} else if role == "user" {
		// Preserve access for callers provisioning users outside the public API.
		subscription.PlanTier = "unlimited"
	}
	return createUser(ctx, s.db, username, hash, role, subscription)
}

func createUser(ctx context.Context, db rowQuerier, username string, hash []byte, role string, subscription Subscription) (AdminUser, error) {
	u := AdminUser{User: User{ID: randomHex(16), Username: username, Role: role, IsActive: true, Subscription: subscription}, CreatedAt: time.Now().UnixMilli()}
	u.Limit5h, u.Limit7d = u.Limits()
	err := db.QueryRowContext(ctx, `INSERT INTO users (id,username,password_hash,created_at,role,plan_tier,bypass_5h_limit,subscription_until)
		VALUES (?,?,?,?,?,?,?,?) ON CONFLICT(username) DO NOTHING RETURNING id`,
		u.ID, username, hash, u.CreatedAt, role, u.PlanTier, u.Bypass5hLimit, u.SubscriptionUntil).Scan(&u.ID)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrUsernameTaken
	}
	return u, err
}

func (s *Store) Register(ctx context.Context, username string, hash []byte, deviceName string) (User, Device, string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, Device{}, "", err
	}
	defer tx.Rollback()
	user, err := createUser(ctx, tx, username, hash, "pending", Subscription{PlanTier: "none"})
	if err != nil {
		return User{}, Device{}, "", err
	}
	device, token, err := issueDevice(ctx, tx, user.ID, deviceName, nil)
	if err == nil {
		err = tx.Commit()
	}
	return user.User, device, token, err
}

type AdminChat struct {
	Chat
	MessagesCount int `json:"messagesCount"`
}

func (s *Store) AdminUserChats(ctx context.Context, userID string) ([]AdminChat, error) {
	if err := s.db.QueryRowContext(ctx, "SELECT id FROM users WHERE id=?", userID).Scan(new(string)); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,title,provider,model,created_at,updated_at,
		(SELECT COUNT(*) FROM chat_messages WHERE chat_id=chats.id)
		FROM chats WHERE user_id=? ORDER BY updated_at DESC,rowid DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	chats := []AdminChat{}
	for rows.Next() {
		var c AdminChat
		if err := rows.Scan(&c.ID, &c.Title, &c.Provider, &c.Model, &c.CreatedAt, &c.UpdatedAt, &c.MessagesCount); err != nil {
			return nil, err
		}
		chats = append(chats, c)
	}
	return chats, rows.Err()
}

func (s *Store) AdminChatMessages(ctx context.Context, userID, chatID string) ([]Message, error) {
	return s.messages(ctx, userID, chatID, false)
}

func (s *Store) SetUserRole(ctx context.Context, username, role string) error {
	return s.db.QueryRowContext(ctx, "UPDATE users SET role=? WHERE username=? RETURNING id", role, username).Scan(new(string))
}

type DirectoryUser struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"displayName,omitempty"`
	AvatarData  string `json:"avatarData,omitempty"`
}

func (s *Store) DirectoryUsers(ctx context.Context) ([]DirectoryUser, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,username,display_name,avatar_data FROM users
		WHERE is_active=1 AND role IN ('user','admin') ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := []DirectoryUser{}
	for rows.Next() {
		var user DirectoryUser
		if err := rows.Scan(&user.ID, &user.Username, &user.DisplayName, &user.AvatarData); err != nil {
			return nil, err
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

func (s *Store) AdminUsers(ctx context.Context) ([]AdminUser, error) {
	now := time.Now()
	query := `SELECT id,username,role,is_active,created_at,plan_tier,bypass_5h_limit,subscription_until,
		(SELECT COUNT(*) FROM devices WHERE user_id=owner.id AND (expires_at=0 OR expires_at>?1)),
		(SELECT COUNT(*) FROM chats WHERE user_id=owner.id), ` + usageColumns(2, 3) + `
		FROM users owner ORDER BY username`
	rows, err := s.db.QueryContext(ctx, query, now.UnixMilli(), now.Add(-fiveHours).UnixMilli(), now.Add(-sevenDays).UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := []AdminUser{}
	for rows.Next() {
		var u AdminUser
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &u.IsActive, &u.CreatedAt, &u.PlanTier, &u.Bypass5hLimit, &u.SubscriptionUntil, &u.DevicesCount, &u.ChatsCount, &u.Usage5h, &u.Usage7d, &u.ResetsAt5h, &u.ResetsAt7d); err != nil {
			return nil, err
		}
		u.Limit5h, u.Limit7d = u.Limits()
		users = append(users, u)
	}
	return users, rows.Err()
}

func (s *Store) UpdateAdminUser(ctx context.Context, id string, role *string, active *bool, hash []byte, subscription SubscriptionUpdate) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var password any
	if hash != nil {
		password = hash
	}
	err = tx.QueryRowContext(ctx, `UPDATE users SET role=COALESCE(?,role),
		is_active=COALESCE(?,is_active), password_hash=COALESCE(?,password_hash),
		plan_tier=COALESCE(?,plan_tier), bypass_5h_limit=COALESCE(?,bypass_5h_limit),
		subscription_until=COALESCE(?,subscription_until) WHERE id=? RETURNING id`,
		role, active, password, subscription.PlanTier, subscription.Bypass5hLimit, subscription.SubscriptionUntil, id).Scan(new(string))
	if err != nil {
		return err
	}
	if hash != nil {
		if _, err := tx.ExecContext(ctx, "DELETE FROM devices WHERE user_id=?", id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) RevokeUserDevices(ctx context.Context, id string) error {
	if err := s.db.QueryRowContext(ctx, "SELECT id FROM users WHERE id=?", id).Scan(new(string)); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, "DELETE FROM devices WHERE user_id=?", id)
	return err
}

func (s *Store) UserDevices(ctx context.Context, userID string) ([]Device, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,last_seen_at,created_at FROM devices
  WHERE user_id=? AND (expires_at=0 OR expires_at>?) ORDER BY last_seen_at DESC,id`,
		userID, time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	devices := []Device{}
	for rows.Next() {
		d := Device{UserID: userID}
		if err := rows.Scan(&d.ID, &d.Name, &d.LastSeenAt, &d.CreatedAt); err != nil {
			return nil, err
		}
		devices = append(devices, d)
	}
	return devices, rows.Err()
}

func (s *Store) DeleteDeviceByID(ctx context.Context, userID, deviceID string) error {
	return s.db.QueryRowContext(ctx, "DELETE FROM devices WHERE id=? AND user_id=? RETURNING id", deviceID, userID).Scan(new(string))
}

func (s *Store) DeleteDeviceByToken(ctx context.Context, token string) error {
	hash := sha256.Sum256([]byte(token))
	_, err := s.db.ExecContext(ctx, "DELETE FROM devices WHERE token_hash=?", hash[:])
	return err
}
