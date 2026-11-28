package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"agent-console/server/internal/provider"
	"agent-console/server/internal/uploads"
)

func TestMigrationsAndHashedDeviceToken(t *testing.T) {
	ctx := context.Background()
	fileName := "test db?.db"
	if runtime.GOOS == "windows" {
		fileName = "test db%3F.db"
	}
	path := filepath.Join(t.TempDir(), "nested", fileName)
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
	var version int
	if err := reopened.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 18 {
		t.Fatalf("schema version %d: %v", version, err)
	}
	if currentDevice.LastSeenAt != device.LastSeenAt {
		t.Fatal("recent device was refreshed")
	}
	before := time.Now().Add(-2 * time.Minute).UnixMilli()
	if _, err := reopened.db.Exec("UPDATE devices SET last_seen_at=? WHERE id=?", before, device.ID); err != nil {
		t.Fatal(err)
	}
	_, refreshed, err := reopened.Authenticate(ctx, token)
	if err != nil || refreshed.LastSeenAt <= before {
		t.Fatalf("stale device was not refreshed: %+v (%v)", refreshed, err)
	}
	var expiresAt int64
	if err := reopened.db.QueryRow("SELECT expires_at FROM devices WHERE id=?", device.ID).Scan(&expiresAt); err != nil || expiresAt != refreshed.LastSeenAt+deviceLifetime.Milliseconds() {
		t.Fatalf("incorrect refreshed expiry: %d (%v)", expiresAt, err)
	}
	if _, err := reopened.db.Exec("UPDATE devices SET expires_at=1 WHERE id=?", device.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := reopened.Authenticate(ctx, token); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expired token: %v", err)
	}
	if _, _, err := reopened.IssueDevice(ctx, user.ID, "new"); err != nil {
		t.Fatal(err)
	}
	if err := reopened.DeleteDeviceByID(ctx, user.ID, device.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expired device was not purged: %v", err)
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
	const legacyV1Schema = `
		CREATE TABLE users (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, password_hash BLOB NOT NULL, created_at INTEGER NOT NULL);
		CREATE TABLE devices (id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE, name TEXT NOT NULL, token_hash BLOB NOT NULL UNIQUE, last_seen_at INTEGER NOT NULL, created_at INTEGER NOT NULL);
		CREATE TABLE chats (id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE, title TEXT NOT NULL, provider TEXT NOT NULL CHECK (provider IN ('codex', 'stub')), model TEXT NOT NULL, provider_session_id TEXT, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
		CREATE TABLE chat_messages (id TEXT PRIMARY KEY, chat_id TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE, role TEXT NOT NULL CHECK (role IN ('user', 'assistant')), content TEXT NOT NULL, thinking TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL);
		CREATE INDEX chats_by_user ON chats (user_id, updated_at DESC);
		CREATE INDEX messages_by_chat ON chat_messages (chat_id, created_at);
		PRAGMA user_version=1;`
	if _, err := legacy.Exec(legacyV1Schema); err != nil {
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
	if err := first.FinishRun(ctx, "owner", "chat", "reply", "answer", "summary", sessionID, 276608, nil); err != nil {
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
	if list[0].DurationMs != nil || list[2].DurationMs == nil || *list[2].DurationMs != 276608 {
		t.Fatalf("unexpected durations after reopen: legacy=%v, reply=%v", list[0].DurationMs, list[2].DurationMs)
	}
	if session, err := reopened.BeginRun(ctx, "owner", "chat", "paid", "test", "paid_api", "deepseek"); !errors.Is(err, ErrProviderUnavailable) || session != "" {
		t.Fatalf("expected ErrProviderUnavailable for paid_api, got session=%q err=%v", session, err)
	}
	if session, err := reopened.BeginRun(ctx, "owner", "chat", "after-paid", "still codex", "codex", "default"); err != nil || session != sessionID {
		t.Fatalf("rejected paid_api wiped or changed session: session=%q err=%v", session, err)
	}
	if session, err := reopened.BeginRun(ctx, "owner", "chat", "switch", "to stub", "stub", "default"); err != nil || session != "" {
		t.Fatalf("expected empty session when switching provider, got session=%q err=%v", session, err)
	}
	if err := reopened.FinishRunWithProvider(ctx, "owner", "chat", "switch", "stub", "default", "stub reply", "", "stub-sess-1", 10, nil); err != nil {
		t.Fatal(err)
	}
	if sess, prov, sessProv, _, err := reopened.ChatRunSessionState(ctx, "chat"); err != nil || sess != "stub-sess-1" || prov != "stub" || sessProv != "stub" {
		t.Fatalf("unexpected post-switch state: sess=%q prov=%q sessProv=%q err=%v", sess, prov, sessProv, err)
	}
}

func TestSubscriptionQuotas(t *testing.T) {
	ctx := t.Context()
	db, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, device, token, err := db.Register(ctx, "alice", []byte("fixture"), "Browser")
	if err != nil {
		t.Fatal(err)
	}
	if current, _, err := db.Authenticate(ctx, token); err != nil || current != user || current.Role != "pending" || current.PlanTier != "none" {
		t.Fatalf("registered user: %+v (%v)", current, err)
	}
	chat, err := db.CreateChat(ctx, user.ID, "Quota", "stub", "default")
	if err != nil {
		t.Fatal(err)
	}
	start := func(id string, want error) {
		t.Helper()
		_, err := db.BeginRunForDevice(ctx, user.ID, device.ID, chat.ID, id, "prompt", "stub", "default")
		if !errors.Is(err, want) {
			t.Fatalf("start %s: %v, want %v", id, err, want)
		}
	}
	start("first", RunDenied("account_pending"))
	role, tier, until, bypass := "user", "none", time.Now().Add(-time.Minute).UnixMilli(), false
	configure := func() {
		t.Helper()
		err := db.UpdateAdminUser(ctx, user.ID, &role, nil, nil, SubscriptionUpdate{
			PlanTier: &tier, SubscriptionUntil: &until, Bypass5hLimit: &bypass,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	configure()
	start("first", RunDenied("subscription_expired"))
	until = 0
	configure()
	start("first", RunDenied("no_active_plan"))
	tier = "pro"
	configure()
	now := time.Now()
	seed := func(id string, cents int, when time.Time) {
		t.Helper()
		_, err := db.db.ExecContext(ctx, `INSERT INTO usage_events(id,user_id,chat_id,run_id,provider,model,cost_cents,created_at)
			VALUES (?,?,?,?,'stub','default',?,?)`, id, user.ID, chat.ID, id, cents, when.UnixMilli())
		if err != nil {
			t.Fatal(err)
		}
	}
	seed("recent", 1499, now)
	seed("week", 13499, now.Add(-24*time.Hour))
	seed("old", 100000, now.Add(-8*24*time.Hour))
	start("first", nil)
	start("unfinished", nil)
	finish := func(id string) {
		t.Helper()
		if err := db.FinishRun(ctx, user.ID, chat.ID, id, "response", "", "", 123, nil); err != nil {
			t.Fatal(err)
		}
	}
	finish("first")
	start("second", RunDenied("five_hour_limit_reached"))
	bypass = true
	configure()
	start("second", nil)
	finish("second")
	start("third", RunDenied("weekly_limit_reached"))
	if five, week, err := db.UserUsage(ctx, user.ID); err != nil || five != 1501 || week != 15000 {
		t.Fatalf("usage %d/%d: %v", five, week, err)
	}
	role = "admin"
	until = now.Add(-time.Hour).UnixMilli()
	configure()
	start("third", nil)
}

func TestChatSharingAndFork(t *testing.T) {
	ctx := t.Context()
	db, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	user := func(name, role string) string {
		t.Helper()
		u, err := db.CreateUserWithRole(ctx, name, []byte("hash"), role)
		if err != nil {
			t.Fatal(err)
		}
		return u.ID
	}
	alice, bob := user("alice", "user"), user("bob", "admin")
	pending, inactive := user("pending", "pending"), user("inactive", "user")
	if _, err := db.db.ExecContext(ctx, "UPDATE users SET is_active=0 WHERE id=?", inactive); err != nil {
		t.Fatal(err)
	}
	if members, err := db.DirectoryUsers(ctx); err != nil || len(members) != 2 || members[0].ID != alice || members[1].ID != bob {
		t.Fatalf("directory: %+v (%v)", members, err)
	}
	source, err := db.CreateChat(ctx, alice, "Shared", "stub", "default")
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{alice, pending, inactive} {
		if err := db.SetChatShare(ctx, alice, source.ID, target, "read"); !errors.Is(err, ErrInvalidShare) {
			t.Fatalf("invalid share: %v", err)
		}
	}
	if err := db.SetChatShare(ctx, bob, source.ID, pending, "read"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("non-owner share: %v", err)
	}
	if err := db.SetChatShare(ctx, alice, source.ID, bob, "read"); err != nil {
		t.Fatal(err)
	}
	if rooms, err := db.Chats(ctx, alice); err != nil || len(rooms) != 1 || len(rooms[0].Grants) != 1 || rooms[0].Grants[0].UserID != bob {
		t.Fatalf("owner grants: %+v (%v)", rooms, err)
	}
	if rooms, err := db.Chats(ctx, bob); err != nil || len(rooms) != 1 || rooms[0].Access != "read" || rooms[0].OwnerID != alice || len(rooms[0].Grants) != 0 {
		t.Fatalf("shared metadata: %+v (%v)", rooms, err)
	}
	if messages, err := db.Messages(ctx, bob, source.ID); err != nil || len(messages) != 0 {
		t.Fatalf("read empty shared chat: %+v (%v)", messages, err)
	}
	if err := db.CheckChatWrite(ctx, bob, source.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("read permission passed write check: %v", err)
	}
	if _, err := db.BeginRun(ctx, bob, source.ID, "denied", "prompt", "stub", "default"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("read permission allowed writing: %v", err)
	}
	if _, err := db.ForkChat(ctx, bob, source.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("read permission allowed forking: %v", err)
	}
	if err := db.SetChatShare(ctx, alice, source.ID, bob, "write"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.BeginRun(ctx, bob, source.ID, "run", "prompt", "stub", "default"); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishRun(ctx, bob, source.ID, "run", "reply", "thinking", "private-session", 123, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.SetChatShare(ctx, alice, source.ID, bob, "fork"); err != nil {
		t.Fatal(err)
	}
	fork, err := db.ForkChat(ctx, bob, source.ID)
	if err != nil || fork.ID == source.ID || fork.OwnerID != bob || fork.Access != "owner" || len(fork.Grants) != 0 {
		t.Fatalf("fork: %+v (%v)", fork, err)
	}
	messages, err := db.Messages(ctx, bob, fork.ID)
	if err != nil || len(messages) != 2 || messages[0].Content != "prompt" || messages[0].RunID != "run" ||
		messages[1].RunID != "run" || messages[1].Content != "reply" || messages[1].Thinking != "thinking" ||
		messages[1].DurationMs == nil || *messages[1].DurationMs != 123 {
		t.Fatalf("fork history: %+v (%v)", messages, err)
	}
	if count, _, err := db.UserUsage(ctx, bob); err != nil || count != 1 {
		t.Fatalf("writer usage with fork: %d (%v)", count, err)
	}
	if count, _, err := db.UserUsage(ctx, alice); err != nil || count != 0 {
		t.Fatalf("owner usage: %d (%v)", count, err)
	}
	if session, err := db.BeginRun(ctx, bob, fork.ID, "next", "follow-up", "stub", "default"); err != nil || session != "" {
		t.Fatalf("fork provider session: %q (%v)", session, err)
	}
}

func TestFinishRunRecordsTokenCostOnce(t *testing.T) {
	ctx := t.Context()
	db, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUserWithRole(ctx, "writer", []byte("fixture"), "user")
	if err != nil {
		t.Fatal(err)
	}
	chat, err := db.CreateChat(ctx, user.ID, "Quota", "codex", "default")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.BeginRun(ctx, user.ID, chat.ID, "run", "test prompt", "codex", "gpt-mini"); err != nil {
		t.Fatal(err)
	}
	usage := &provider.Usage{InputTokens: 1000000, CachedInputTokens: 500000, OutputTokens: 200000}
	finish := func() error { return db.FinishRun(ctx, user.ID, chat.ID, "run", "reply", "thinking", "", 1, usage) }
	if err := finish(); err != nil {
		t.Fatal(err)
	}
	if err := finish(); err == nil {
		t.Fatal("duplicate finish succeeded")
	}
	var counts provider.Usage
	var cents int64
	var backend, model string
	err = db.db.QueryRowContext(ctx, `SELECT input_tokens,cached_input_tokens,output_tokens,cost_cents,provider,model
		FROM usage_events WHERE user_id=? AND chat_id=? AND run_id='run'`, user.ID, chat.ID).
		Scan(&counts.InputTokens, &counts.CachedInputTokens, &counts.OutputTokens, &cents, &backend, &model)
	if err != nil || counts != *usage || cents != 155 || backend != "codex" || model != "gpt-mini" {
		t.Fatalf("stored usage: %+v, %d cents, %s/%s (%v)", counts, cents, backend, model, err)
	}
	if five, week, err := db.UserUsage(ctx, user.ID); err != nil || five != 155 || week != 155 {
		t.Fatalf("usage %d/%d: %v", five, week, err)
	}
	for _, tc := range []struct {
		model string
		usage *provider.Usage
		cents int64
	}{
		{"default", usage, 465},
		{"Claude-Opus", usage, 930},
		{"default", nil, 1},
		{"default", &provider.Usage{}, 0},
	} {
		got, cents := priceUsage(tc.usage, tc.model, "12345678", "1234", "5678")
		if cents != tc.cents || (tc.usage == nil && got != (provider.Usage{InputTokens: 2, OutputTokens: 2})) {
			t.Fatalf("%s usage: %+v, %d cents, want %d", tc.model, got, cents, tc.cents)
		}
	}
}

func TestProfileMigrationAndMemoryIsolation(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "v8.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	makeUser := func(name, role string) string {
		user, err := db.CreateUserWithRole(ctx, name, []byte("hash"), role)
		if err != nil {
			t.Fatal(err)
		}
		return user.ID
	}
	owner := makeUser("owner", "user")
	reader := makeUser("reader", "user")
	pending := makeUser("pending", "pending")
	disabled := makeUser("disabled", "user")
	makeChat := func(userID string, when int64) Chat {
		chat, err := db.CreateChat(ctx, userID, "test", "stub", "default")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.BeginRun(ctx, userID, chat.ID, "run", "question", "stub", "default"); err != nil {
			t.Fatal(err)
		}
		if err = db.FinishRun(ctx, userID, chat.ID, "run", "answer", "", "old-session", 0, nil); err != nil {
			t.Fatal(err)
		}
		if _, err = db.db.ExecContext(ctx, "UPDATE chat_messages SET created_at=?+rowid%2 WHERE chat_id=?", when, chat.ID); err != nil {
			t.Fatal(err)
		}
		return chat
	}
	personal := makeChat(owner, 20)
	shared := makeChat(owner, 30)
	makeChat(pending, 30)
	makeChat(disabled, 30)
	if err := db.SetChatShare(ctx, owner, shared.ID, reader, "fork"); err != nil {
		t.Fatal(err)
	}
	if session, sharedNow, err := db.ChatRunContext(ctx, shared.ID); session != "" || !sharedNow || err != nil {
		t.Fatalf("sharing retained private session: %q, %v, %v", session, sharedNow, err)
	}
	fork, err := db.ForkChat(ctx, reader, shared.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.db.ExecContext(ctx, `ALTER TABLE users DROP COLUMN display_name;
  ALTER TABLE users DROP COLUMN avatar_data;
  ALTER TABLE users DROP COLUMN profile_md;
  ALTER TABLE users DROP COLUMN memory_md;
  ALTER TABLE users DROP COLUMN memory_enabled;
  ALTER TABLE users DROP COLUMN memory_updated_at;
  ALTER TABLE chat_messages DROP COLUMN memory_private;
  PRAGMA user_version=8;`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	initial, err := db.UserProfile(ctx, owner)
	if err != nil || initial != (UserProfile{MemoryEnabled: true}) {
		t.Fatalf("migrated profile: %+v %v", initial, err)
	}
	name := strings.Repeat("Я", 80)
	preference := strings.Repeat("а", 2048)
	profile, err := db.UpdateUserProfile(ctx, owner, &name, &preference, nil, nil)
	if err != nil || profile.DisplayName != name || profile.ProfileMD != preference || !profile.MemoryEnabled {
		t.Fatalf("profile update error: %v", err)
	}
	validAvatar := "data:image/webp;base64,UklGRh4AAABXRUJQVlA4TBEAAAAvAAAAAAfQ//73v/+BiOh/AAA="
	if profile, err = db.UpdateUserProfile(ctx, owner, nil, nil, nil, nil, &validAvatar); err != nil ||
		(!strings.HasPrefix(profile.AvatarData, "data:image/jpeg;base64,") && !strings.HasPrefix(profile.AvatarData, "data:image/png;base64,")) {
		t.Fatalf("avatar update error: %v (avatar=%q)", err, profile.AvatarData)
	}
	invalidAvatar := "javascript:alert(1)"
	if _, err := db.UpdateUserProfile(ctx, owner, nil, nil, nil, nil, &invalidAvatar); !errors.Is(err, ErrInvalidProfile) {
		t.Fatalf("expected invalid avatar rejection, got: %v", err)
	}
	overlong := name + "!"
	tooManyBytes := preference + "!"
	for _, test := range []struct{ display, about, memory *string }{
		{display: &overlong}, {about: &tooManyBytes}, {memory: &tooManyBytes},
	} {
		if _, err := db.UpdateUserProfile(ctx, owner, test.display, test.about, test.memory, nil); !errors.Is(err, ErrInvalidProfile) {
			t.Fatalf("validation: %v", err)
		}
	}
	off := false
	if _, err = db.UpdateUserProfile(ctx, disabled, nil, nil, nil, &off); err != nil {
		t.Fatal(err)
	}
	eligible, err := db.UsersPendingMemory(ctx, 2)
	if err != nil || !slices.Equal(eligible, []string{owner}) {
		t.Fatalf("eligible: %v %v", eligible, err)
	}
	if eligible, err = db.UsersPendingMemory(ctx, 3); err != nil || len(eligible) != 0 {
		t.Fatalf("shared messages counted: %v %v", eligible, err)
	}
	for _, userID := range []string{reader, owner} {
		messages, newest, err := db.RecentPersonalMessagesSince(ctx, userID, 0, 40)
		if err != nil {
			t.Fatal(err)
		}
		if userID == reader {
			if len(messages) != 0 || newest != 0 {
				t.Fatalf("fork %s included in memory", fork.ID)
			}
		} else if len(messages) != 2 || messages[0].ChatID != personal.ID || messages[1].ChatID != personal.ID || messages[0].CreatedAt > messages[1].CreatedAt || newest != messages[1].CreatedAt {
			t.Fatalf("personal batch: %+v %d", messages, newest)
		}
	}
	if err = db.SaveUserMemory(ctx, owner, "- Test", 21); err != nil {
		t.Fatal(err)
	}
	if eligible, err = db.UsersPendingMemory(ctx, 2); err != nil || len(eligible) != 0 {
		t.Fatalf("watermark did not advance: %v %v", eligible, err)
	}
	if err = db.SetChatShare(ctx, owner, shared.ID, reader, ""); err != nil {
		t.Fatal(err)
	}
	if messages, _, err := db.RecentPersonalMessagesSince(ctx, owner, 21, 40); err != nil || len(messages) != 0 {
		t.Fatalf("historically shared messages included after revocation: %+v %v", messages, err)
	}
	empty := ""
	if _, err = db.UpdateUserProfile(ctx, owner, nil, nil, &empty, nil); err != nil {
		t.Fatal(err)
	}
	if err = db.SaveUserMemoryIfCurrent(ctx, owner, "- Stale", 22, 21, []string{personal.ID}); err != nil {
		t.Fatal(err)
	}
	if profile, err = db.UserProfile(ctx, owner); err != nil || profile.MemoryMD != "" || profile.MemoryUpdatedAt <= 21 {
		t.Fatalf("manual clear was overwritten: %+v %v", profile, err)
	}
}

func TestProviderCheckAndMigration(t *testing.T) {
	ctx := t.Context()
	db, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	user, err := db.CreateUserWithRole(ctx, "provider_user", []byte("hash"), "user")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"antigravity", "codex", "claude", "paid_api", "stub"} {
		if _, err := db.CreateChat(ctx, user.ID, "Chat "+p, p, "default"); err != nil {
			t.Fatalf("create %s chat failed: %v", p, err)
		}
	}
	if _, err := db.CreateChat(ctx, user.ID, "Legacy Gemini", "gemini", "gemini-3.8-flash"); err == nil {
		t.Fatalf("expected gemini provider to be rejected by CHECK constraint")
	}
}

func TestAntigravityChatAndMigration(t *testing.T) {
	ctx := t.Context()
	db, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	user, err := db.CreateUserWithRole(ctx, "agy_user", []byte("hash"), "user")
	if err != nil {
		t.Fatal(err)
	}
	chat, err := db.CreateChat(ctx, user.ID, "Antigravity Chat", "antigravity", "gemini-3.8-flash-high")
	if err != nil {
		t.Fatalf("create antigravity chat failed: %v", err)
	}
	if chat.Provider != "antigravity" {
		t.Fatalf("expected provider antigravity, got %q", chat.Provider)
	}
	_, err = db.BeginRun(ctx, user.ID, chat.ID, "msg1", "Hello Antigravity", "antigravity", "gemini-3.8-flash-high")
	if err != nil {
		t.Fatalf("begin run failed: %v", err)
	}
	if err := db.FinishRun(ctx, user.ID, chat.ID, "msg1", "Hi there!", "", "834a3b51-377b-4b32-b712-b0b6e81e8377", 100, nil); err != nil {
		t.Fatalf("finish run failed: %v", err)
	}
}

func TestBeginRunProviderMismatchSafetyAndConcurrency(t *testing.T) {
	ctx := t.Context()
	db, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	owner, err := db.CreateUserWithRole(ctx, "owner", []byte("hash"), "user")
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateUserWithRole(ctx, "other", []byte("hash"), "user")
	if err != nil {
		t.Fatal(err)
	}
	chat, err := db.CreateChat(ctx, owner.ID, "Codex Chat", "codex", "gpt-5")
	if err != nil {
		t.Fatal(err)
	}
	const wantSession = "019abcde-0123-4567-89ab-0123456789cd"
	if _, err := db.BeginRun(ctx, owner.ID, chat.ID, "m1", "hello", "codex", "gpt-5"); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishRun(ctx, owner.ID, chat.ID, "m1", "hi", "", wantSession, 50, nil); err != nil {
		t.Fatal(err)
	}

	// Unauthorized user gets sql.ErrNoRows before provider mismatch.
	if _, _, err := db.ChatDispatchInfo(ctx, other.ID, chat.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unauthorized ChatDispatchInfo = %v, want sql.ErrNoRows", err)
	}
	if _, err := db.BeginRun(ctx, other.ID, chat.ID, "m-unauth", "hack", "claude", "default"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unauthorized BeginRun = %v, want sql.ErrNoRows", err)
	}

	// Concurrent paid_api rejections and provider switches never leak codex session to another provider.
	const workers = 8
	errs := make(chan error, workers)
	for i := range workers {
		go func(idx int) {
			id := "concurrent-" + string(rune('a'+idx))
			switch idx % 3 {
			case 0:
				_, err := db.BeginRun(ctx, owner.ID, chat.ID, id, "bad", "paid_api", "deepseek")
				if !errors.Is(err, ErrProviderUnavailable) {
					errs <- err
					return
				}
			case 1:
				sess, err := db.BeginRun(ctx, owner.ID, chat.ID, id, "switch", "claude", "sonnet")
				if err != nil {
					errs <- err
					return
				}
				if sess != "" {
					errs <- errors.New("leaked codex session to claude: " + sess)
					return
				}
			default:
				sess, err := db.BeginRun(ctx, owner.ID, chat.ID, id, "good", "codex", "gpt-5")
				if err != nil {
					errs <- err
					return
				}
				if sess != wantSession {
					errs <- errors.New("lost session id: " + sess)
					return
				}
			}
			errs <- nil
		}(i)
	}
	for range workers {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent BeginRun invariant failed: %v", err)
		}
	}

	_, storedProvider, err := db.ChatDispatchInfo(ctx, owner.ID, chat.ID)
	if err != nil || storedProvider != "codex" {
		t.Fatalf("stored provider mutated before FinishRun: %q (%v)", storedProvider, err)
	}
	sess, gotProvider, sessProv, _, err := db.ChatRunSessionState(ctx, chat.ID)
	if err != nil || gotProvider != "codex" || sessProv != "codex" || sess != wantSession {
		t.Fatalf("ChatRunSessionState = (%q, %q, %q, %v), want (%q, codex, codex)", sess, gotProvider, sessProv, err, wantSession)
	}
}

func TestMigrationV14SanitizesStoredAvatars(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	u1, err := db.CreateUserWithRole(ctx, "dirty_svg", []byte("h"), "user")
	if err != nil {
		t.Fatal(err)
	}
	u2, err := db.CreateUserWithRole(ctx, "valid_webp", []byte("h"), "user")
	if err != nil {
		t.Fatal(err)
	}
	// Seed legacy un-sanitized avatars at v14.
	if _, err := db.db.ExecContext(ctx, `UPDATE users SET avatar_data='data:image/svg+xml;base64,PHN2Zz48L3N2Zz4=' WHERE id=?`, u1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `UPDATE users SET avatar_data='data:image/webp;base64,UklGRh4AAABXRUJQVlA4TBEAAAAvAAAAAAfQ//73v/+BiOh/AAA=' WHERE id=?`, u2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `PRAGMA user_version=14;`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	p1, err := reopened.UserProfile(ctx, u1.ID)
	if err != nil || p1.AvatarData != "" {
		t.Fatalf("expected dirty SVG avatar to be cleared on v15 migration, got %q (%v)", p1.AvatarData, err)
	}
	p2, err := reopened.UserProfile(ctx, u2.ID)
	if err != nil || (!strings.HasPrefix(p2.AvatarData, "data:image/jpeg;base64,") && !strings.HasPrefix(p2.AvatarData, "data:image/png;base64,")) {
		t.Fatalf("expected legacy webp avatar to be re-encoded on v15 migration, got %q (%v)", p2.AvatarData, err)
	}
}

func TestAttachmentStoreLifecycleAndCleanup(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "att.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	owner, err := db.CreateUserWithRole(ctx, "owner", []byte("h"), "user")
	if err != nil {
		t.Fatal(err)
	}
	stranger, err := db.CreateUserWithRole(ctx, "stranger", []byte("h"), "user")
	if err != nil {
		t.Fatal(err)
	}
	chat, err := db.CreateChat(ctx, owner.ID, "Files", "codex", "default")
	if err != nil {
		t.Fatal(err)
	}

	att, err := db.CreateAttachment(ctx, chat.ID, owner.ID, uploads.ValidatedAttachment{
		Filename:      "index.html",
		MIMEType:      "text/plain; charset=utf-8",
		Kind:          uploads.KindText,
		Size:          12,
		Data:          []byte("<h1>Hi</h1>\n"),
		ExtractedText: "<h1>Hi</h1>\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	diskPath := filepath.Join(db.UploadsDir(), att.StorageKey)
	if info, err := os.Stat(diskPath); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("unexpected file permissions/stat on %s: %v", diskPath, err)
	}

	if _, err := db.GetAttachment(ctx, stranger.ID, att.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected ErrForbidden for stranger, got %v", err)
	}
	if err := db.DeleteUnattachedAttachment(ctx, stranger.ID, att.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected ErrForbidden deleting stranger upload, got %v", err)
	}

	// Binding attachment to a message protects it from DeleteUnattachedAttachment.
	if _, attached, err := db.BeginRunForDeviceWithAttachments(ctx, owner.ID, "", chat.ID, "run-1", "Check file", "codex", "default", []string{att.ID}); err != nil || len(attached) != 1 {
		t.Fatalf("BeginRunForDeviceWithAttachments failed: %v (%d)", err, len(attached))
	}
	if err := db.DeleteUnattachedAttachment(ctx, owner.ID, att.ID); !errors.Is(err, ErrAttachmentInUse) {
		t.Fatalf("expected ErrAttachmentInUse, got %v", err)
	}
	msgs, err := db.Messages(ctx, owner.ID, chat.ID)
	if err != nil || len(msgs) != 1 || len(msgs[0].Attachments) != 1 || msgs[0].Attachments[0].Name != "index.html" {
		t.Fatalf("unexpected message attachments: %+v (%v)", msgs, err)
	}

	// Orphan cleanup removes unattached uploads older than maxAge.
	orphan, err := db.CreateAttachment(ctx, chat.ID, owner.ID, uploads.ValidatedAttachment{
		Filename: "old.txt", MIMEType: "text/plain; charset=utf-8", Kind: uploads.KindText, Size: 3, Data: []byte("old"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, "UPDATE chat_attachments SET created_at=? WHERE id=?", time.Now().Add(-3*time.Hour).UnixMilli(), orphan.ID); err != nil {
		t.Fatal(err)
	}
	removed, err := db.CleanupOrphanAttachments(ctx, 2*time.Hour)
	if err != nil || removed != 1 {
		t.Fatalf("CleanupOrphanAttachments removed=%d err=%v", removed, err)
	}
	if _, err := os.Stat(filepath.Join(db.UploadsDir(), orphan.StorageKey)); !os.IsNotExist(err) {
		t.Fatalf("expected orphan disk file removed, got %v", err)
	}

	// Deleting chat removes remaining bound files from disk.
	if err := db.DeleteChat(ctx, owner.ID, chat.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(diskPath); !os.IsNotExist(err) {
		t.Fatalf("expected bound attachment file removed on DeleteChat, got %v", err)
	}
}

func TestStoreMigrateV16ToV17Attachments(t *testing.T) {
	ctx := t.Context()
	dbPath := filepath.Join(t.TempDir(), "v16.db")
	db, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	// Downgrade chat_attachments to v16 schema and PRAGMA user_version=16 to test live upgrade.
	if _, err := db.db.ExecContext(ctx, `
		DROP TABLE chat_attachments;
		CREATE TABLE chat_attachments (
			id TEXT PRIMARY KEY,
			chat_id TEXT NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
			message_id TEXT REFERENCES chat_messages(id) ON DELETE CASCADE,
			user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			original_name TEXT NOT NULL,
			mime_type TEXT NOT NULL,
			kind TEXT NOT NULL CHECK (kind IN ('image', 'text', 'pdf', 'archive')),
			size_bytes INTEGER NOT NULL CHECK (size_bytes >= 0),
			sha256 TEXT NOT NULL,
			storage_key TEXT NOT NULL UNIQUE,
			extracted_text TEXT,
			created_at INTEGER NOT NULL
		);
		PRAGMA user_version=16;
	`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	reopened, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("Open after v16 failed: %v", err)
	}
	defer reopened.Close()

	var version int
	if err := reopened.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil || version != 18 {
		t.Fatalf("expected user_version=18, got %d (err=%v)", version, err)
	}
	owner, err := reopened.CreateUserWithRole(ctx, "u1", []byte("h"), "user")
	if err != nil {
		t.Fatal(err)
	}
	chat, err := reopened.CreateChat(ctx, owner.ID, "Docs", "codex", "default")
	if err != nil {
		t.Fatal(err)
	}
	att, err := reopened.CreateAttachment(ctx, chat.ID, owner.ID, uploads.ValidatedAttachment{
		Filename: "doc.docx", MIMEType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		Kind: uploads.KindDocument, Size: 3, Data: []byte("doc"), Warning: "warn",
	})
	if err != nil || att.Warning != "warn" {
		t.Fatalf("CreateAttachment after v17 migration failed: %+v err=%v", att, err)
	}
}
