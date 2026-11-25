package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"agent-console/server/internal/provider"
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
	if err := reopened.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 9 {
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
	if session, err := reopened.BeginRun(ctx, "owner", "chat", "switch", "test", "stub", "default"); err != nil || session != "" {
		t.Fatalf("resume session leaked across provider switch: session=%q err=%v", session, err)
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
