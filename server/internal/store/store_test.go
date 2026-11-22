package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"

	"errors"
	"path/filepath"
	"testing"
)

func TestMigrationsAndHashedDeviceToken(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nested", "test db?.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	if err := db.CreateUser(ctx, "alice", []byte("fixture hash")); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateUser(ctx, "alice", []byte("another hash")); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("duplicate registration: %v", err)
	}
	user, passwordHash, err := db.Credentials(ctx, "alice")
	if err != nil || string(passwordHash) != "fixture hash" {
		t.Fatalf("credentials: %q, %v", passwordHash, err)
	}
	if _, _, err := db.IssueDevice(ctx, "unknown", "laptop"); err == nil {
		t.Fatal("foreign key constraint failed to reject invalid user")
	}
	device, token, err := db.IssueDevice(ctx, user.ID, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	var storedHash []byte
	if err := db.db.QueryRow("SELECT token_hash FROM devices WHERE id=?", device.ID).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	expected := sha256.Sum256([]byte(token))
	if bytes.Equal(storedHash, []byte(token)) || !bytes.Equal(storedHash, expected[:]) {
		t.Fatal("expected SHA-256 token hash in SQLite")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	currentUser, currentDevice, err := reopened.Authenticate(ctx, token)
	if err != nil || currentUser != user || currentDevice.ID != device.ID {
		t.Fatalf("auth after reopening DB failed: %v", err)
	}
}

func TestUpgradeAndResumeChat(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "v1.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	if _, err := legacy.Exec(initialSchema + "PRAGMA user_version=1;"); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`
		INSERT INTO users VALUES ('owner','alice','hash',0);
		INSERT INTO chats VALUES ('chat','owner','Title','codex','default',NULL,0,0);
		INSERT INTO chat_messages VALUES ('legacy','chat','assistant','old','',0);`); err != nil {
		t.Fatal(err)
	}

	first, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	const sessionID = "019abcde-0123-4567-89ab-0123456789ab"
	if session, err := first.BeginRun(ctx, "owner", "chat", "reply", "hello", "codex", "default"); err != nil || session != "" {
		t.Fatalf("new Codex run: session=%q err=%v", session, err)
	}
	if err := first.FinishRun(ctx, "chat", "reply", "answer", "summary", sessionID); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if session, err := reopened.BeginRun(ctx, "owner", "chat", "next", "follow-up", "codex", "default"); err != nil || session != sessionID {
		t.Fatalf("resume after restart: session=%q err=%v", session, err)
	}
	list, err := reopened.Messages(ctx, "owner", "chat")
	if err != nil || len(list) != 4 || list[0].ID != "legacy" || list[2].ID != "reply" || list[2].Thinking != "summary" {
		t.Fatalf("migrated history: %v, %v", list, err)
	}
	if session, err := reopened.BeginRun(ctx, "owner", "chat", "switch", "test", "stub", "default"); err != nil || session != "" {
		t.Fatalf("resume session leaked across provider switch: session=%q err=%v", session, err)
	}
}
