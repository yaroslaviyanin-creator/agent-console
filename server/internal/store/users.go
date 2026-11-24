package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"time"
)

type AdminUser struct {
	User
	DevicesCount int   `json:"devicesCount"`
	ChatsCount   int   `json:"chatsCount"`
	CreatedAt    int64 `json:"createdAt"`
}

func (s *Store) CreateUserWithRole(ctx context.Context, username string, hash []byte, role string) (AdminUser, error) {
	u := AdminUser{User: User{ID: randomHex(16), Username: username, Role: role, IsActive: true}, CreatedAt: time.Now().UnixMilli()}
	err := s.db.QueryRowContext(ctx, `INSERT INTO users (id, username, password_hash, created_at, role)
		VALUES (?,?,?,?,?) ON CONFLICT(username) DO NOTHING RETURNING id`,
		u.ID, username, hash, u.CreatedAt, role).Scan(&u.ID)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrUsernameTaken
	}
	return u, err
}

func (s *Store) SetUserRole(ctx context.Context, username, role string) error {
	return s.db.QueryRowContext(ctx, "UPDATE users SET role=? WHERE username=? RETURNING id", role, username).Scan(new(string))
}

func (s *Store) AdminUsers(ctx context.Context) ([]AdminUser, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, username, role, is_active, created_at,
		(SELECT COUNT(*) FROM devices WHERE user_id=users.id),
		(SELECT COUNT(*) FROM chats WHERE user_id=users.id)
		FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := []AdminUser{}
	for rows.Next() {
		var u AdminUser
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &u.IsActive, &u.CreatedAt, &u.DevicesCount, &u.ChatsCount); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

func (s *Store) UpdateAdminUser(ctx context.Context, id string, role *string, active *bool, hash []byte) error {
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
		is_active=COALESCE(?,is_active), password_hash=COALESCE(?,password_hash) WHERE id=? RETURNING id`,
		role, active, password, id).Scan(new(string))
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
