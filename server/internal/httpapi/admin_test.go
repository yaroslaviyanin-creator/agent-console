package httpapi

import (
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-console/server/internal/config"
	"agent-console/server/internal/store"
	"golang.org/x/crypto/bcrypt"
)

func TestAdminUsersLifecycle(t *testing.T) {
	ctx := t.Context()
	dbPath := filepath.Join(t.TempDir(), "admin.db")
	db, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	hash, err := bcrypt.GenerateFromPassword([]byte("admin-pass"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := db.CreateUserWithRole(ctx, "root", hash, "admin")
	if err != nil {
		t.Fatal(err)
	}
	api, err := New(ctx, db, config.Config{Provider: "stub", CORSOrigin: "*"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, token, body string, want int, dst any) {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: got %d, want %d (%s)", method, path, w.Code, want, w.Body)
		}
		if dst != nil {
			if err := json.Unmarshal(w.Body.Bytes(), dst); err != nil {
				t.Fatal(err)
			}
		}
	}
	login := func(body string, status int) (string, store.User) {
		t.Helper()
		var result struct {
			Token string
			User  store.User
		}
		var dst any
		if status == http.StatusOK {
			dst = &result
		}
		request("POST", "/api/auth/login", "", body, status, dst)
		return result.Token, result.User
	}
	const users = "/api/admin/users"
	request("GET", users, "", "", 401, nil)
	adminToken, adminUser := login(`{"username":"root","password":"admin-pass","deviceName":"Mac"}`, 200)
	if adminUser.Role != "admin" || !adminUser.IsActive {
		t.Fatalf("incorrect login user: %+v", adminUser)
	}
	var current session
	request("GET", "/api/auth/me", adminToken, "", 200, &current)
	if current.User != adminUser {
		t.Fatalf("incorrect auth/me user: %+v", current.User)
	}
	const create = `{"username":"alice","password":"original-pass","role":"user","planTier":"2x_pro","bypass5hLimit":true,"subscriptionUntil":1800000000000}`
	var alice store.AdminUser
	request("POST", users, adminToken, create, 201, &alice)
	if alice.PlanTier != "2x_pro" || !alice.Bypass5hLimit || alice.SubscriptionUntil != 1800000000000 || alice.Limit5h != 3000 || alice.Limit7d != 30000 {
		t.Fatalf("created subscription: %+v", alice)
	}
	request("POST", users, adminToken, create, 409, nil)
	request("POST", users, adminToken, `{"username":"invalid","password":"original-pass","role":"user","planTier":"unknown"}`, 400, nil)
	const loginAlice = `{"username":"alice","password":"original-pass","deviceName":"Telegram"}`
	aliceToken, _ := login(loginAlice, 200)
	request("GET", users, aliceToken, "", 403, nil)
	chat, err := db.CreateChat(ctx, alice.ID, "Fixture", "stub", "default")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.BeginRun(ctx, alice.ID, chat.ID, "fixture", "hello", "stub", "default"); err != nil {
		t.Fatal(err)
	}
	chatListPath := users + "/" + alice.ID + "/chats"
	var chats []store.AdminChat
	request("GET", chatListPath, adminToken, "", 200, &chats)
	if len(chats) != 1 || chats[0].ID != chat.ID || chats[0].MessagesCount != 1 {
		t.Fatalf("incorrect admin chats: %+v", chats)
	}
	var messages []store.Message
	request("GET", chatListPath+"/"+chat.ID+"/messages", adminToken, "", 200, &messages)
	if len(messages) != 1 || messages[0].Content != "hello" {
		t.Fatalf("incorrect admin messages: %+v", messages)
	}
	request("GET", users+"/"+admin.ID+"/chats/"+chat.ID+"/messages", adminToken, "", 404, nil)
	request("GET", chatListPath, aliceToken, "", 403, nil)
	expired, _, err := db.IssueDevice(ctx, alice.ID, "Expired")
	if err != nil {
		t.Fatal(err)
	}
	fixtureDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer fixtureDB.Close()
	if _, err := fixtureDB.ExecContext(ctx, "UPDATE devices SET expires_at=1 WHERE id=?", expired.ID); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err := fixtureDB.ExecContext(ctx, `INSERT INTO chat_messages (id,chat_id,role,content,created_at)
		VALUES ('legacy-week',?,'user','within seven days',?), ('legacy-old',?,'user','older than seven days',?)`,
		chat.ID, now.Add(-24*time.Hour).UnixMilli(), chat.ID, now.Add(-8*24*time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := fixtureDB.ExecContext(ctx, `INSERT INTO usage_events(id,user_id,chat_id,run_id,provider,model,cost_cents,created_at)
		VALUES ('current',?,?,'current','stub','default',240,?), ('week',?,?,'week','stub','default',4560,?)`,
		alice.ID, chat.ID, now.UnixMilli(), alice.ID, chat.ID, now.Add(-24*time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	const quotaPath = "/api/quotas/current"
	request("GET", quotaPath, "", "", 401, nil)
	var quota store.Quota
	request("GET", quotaPath, adminToken, "", 200, &quota)
	if quota.FiveHour != (store.QuotaWindow{}) || quota.SevenDay != (store.QuotaWindow{}) {
		t.Fatalf("unexpected empty admin quota: %+v", quota)
	}
	request("GET", quotaPath, aliceToken, "", 200, &quota)
	if quota.Subscription != alice.Subscription ||
		quota.FiveHour != (store.QuotaWindow{UsedCents: 240, LimitCents: 3000, UsedPercent: 8, ResetsAt: now.Add(5 * time.Hour).UnixMilli()}) ||
		quota.SevenDay != (store.QuotaWindow{UsedCents: 4800, LimitCents: 30000, UsedPercent: 16, ResetsAt: now.Add(6 * 24 * time.Hour).UnixMilli()}) {
		t.Fatalf("unexpected user quota: %+v", quota)
	}
	var pending struct {
		session
		Token string
	}
	request("POST", "/api/auth/register", "",
		`{"username":"pending-user","password":"pending-pass","deviceName":"Browser"}`, 201, &pending)
	request("GET", quotaPath, pending.Token, "", 403, nil)
	var list []store.AdminUser
	request("GET", users, adminToken, "", 200, &list)
	if len(list) != 3 || list[0].ID != alice.ID || list[0].DevicesCount != 1 || list[0].ChatsCount != 1 || list[0].CreatedAt == 0 {
		t.Fatalf("incorrect users list: %+v", list)
	}
	want := map[string]struct {
		role                               string
		usage5h, usage7d, limit5h, limit7d int64
	}{
		admin.ID:        {role: "admin"},
		pending.User.ID: {role: "pending"},
		alice.ID:        {"user", 240, 4800, 3000, 30000},
	}
	for _, user := range list {
		expected, ok := want[user.ID]
		if !ok || user.Role != expected.role || user.Usage5h != expected.usage5h || user.Usage7d != expected.usage7d ||
			user.Limit5h != expected.limit5h || user.Limit7d != expected.limit7d {
			t.Fatalf("unexpected user %+v, want %+v", user, expected)
		}
		if user.ID == alice.ID && (user.ResetsAt5h != quota.FiveHour.ResetsAt || user.ResetsAt7d != quota.SevenDay.ResetsAt) {
			t.Fatalf("admin reset times: %+v", user)
		}
		delete(want, user.ID)
	}
	request("PATCH", users+"/"+admin.ID, adminToken, `{"role":"user"}`, 400, nil)
	request("PATCH", users+"/"+admin.ID, adminToken, `{"isActive":false}`, 400, nil)
	path := users + "/" + alice.ID
	var ws struct{ Ticket string }
	request("PATCH", path, adminToken, `{"planTier":"pro","bypass5hLimit":false,"subscriptionUntil":0}`, 204, nil)
	request("GET", "/api/auth/me", aliceToken, "", 200, &current)
	if current.User.PlanTier != "pro" || current.User.Bypass5hLimit || current.User.SubscriptionUntil != 0 {
		t.Fatalf("updated subscription: %+v", current.User)
	}
	request("PATCH", path, adminToken, `{"subscriptionUntil":-1}`, 400, nil)
	request("POST", "/api/ws/ticket", aliceToken, "", 200, &ws)
	request("PATCH", path, adminToken, `{"isActive":false}`, 204, nil)
	login(loginAlice, 403)
	request("GET", "/api/auth/me", aliceToken, "", 403, nil)
	request("GET", "/ws?ticket="+ws.Ticket, "", "", 403, nil)
	request("GET", "/ws?token="+adminToken, "", "", 401, nil)
	request("PATCH", path, adminToken, `{"isActive":true,"role":"admin","newPassword":"changed-pass"}`, 204, nil)
	login(loginAlice, 401)
	request("GET", "/api/auth/me", aliceToken, "", 401, nil)
	newToken, updated := login(`{"username":"alice","password":"changed-pass","deviceName":"Telegram"}`, 200)
	if updated.Role != "admin" || !updated.IsActive {
		t.Fatalf("incorrect patched user: %+v", updated)
	}
	request("GET", users, newToken, "", 200, nil)
	_, secondaryToken, err := db.IssueDevice(ctx, alice.ID, "Desktop")
	if err != nil {
		t.Fatal(err)
	}
	var devices []struct {
		store.Device
		IsCurrent bool
	}
	request("GET", "/api/devices", newToken, "", 200, &devices)
	if len(devices) != 2 || devices[0].IsCurrent == devices[1].IsCurrent {
		t.Fatalf("device list: %+v", devices)
	}
	var secondary session
	request("GET", "/api/auth/me", secondaryToken, "", 200, &secondary)
	for _, device := range devices {
		if device.IsCurrent == (device.ID == secondary.Device.ID) {
			t.Fatalf("unexpected current flag: %+v", device)
		}
	}
	devicePath := "/api/devices/" + secondary.Device.ID
	request("DELETE", devicePath, adminToken, "", 404, nil)
	request("DELETE", devicePath, newToken, "", 204, nil)
	request("GET", "/api/auth/me", secondaryToken, "", 401, nil)
	request("POST", "/api/auth/logout", newToken, "", 204, nil)
	request("GET", "/api/devices", newToken, "", 401, nil)
	_, newToken, err = db.IssueDevice(ctx, alice.ID, "Telegram")
	if err != nil {
		t.Fatal(err)
	}
	request("DELETE", path+"/devices", adminToken, "", 204, nil)
	request("GET", "/api/auth/me", newToken, "", 401, nil)
	request("PATCH", users+"/missing", adminToken, `{"role":"user"}`, 404, nil)
	request("DELETE", users+"/missing/devices", adminToken, "", 404, nil)
}

func TestLoginRateLimit(t *testing.T) {
	newAPI := func(t *testing.T, trust bool) http.Handler {
		api, err := New(t.Context(), nil, config.Config{Provider: "stub", CORSOrigin: "*", TrustProxyHeaders: trust}, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err != nil {
			t.Fatal(err)
		}
		return api
	}
	request := func(t *testing.T, api http.Handler, ip, username, forwarded, realIP string, want int) {
		t.Helper()
		// Invalid passwords count as attempts without exercising bcrypt or SQLite.
		r := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"username":"`+username+`","password":"","deviceName":"test"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Forwarded-For", forwarded)
		r.Header.Set("X-Real-IP", realIP)
		r.RemoteAddr = ip
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != want || want == 429 && !strings.Contains(w.Body.String(), `"rate_limited"`) {
			t.Fatalf("login: got %d, want %d (%s)", w.Code, want, w.Body)
		}
	}
	api := newAPI(t, false)
	for range 5 {
		request(t, api, "192.0.2.1:1000", "alice", "", "", 400)
		request(t, api, "192.0.2.1:1001", "bob", "", "", 400)
	}
	request(t, api, "192.0.2.2:1002", "alice", "", "", 429)
	request(t, api, "192.0.2.1:1002", "charlie", "", "", 429)
	request(t, api, "192.0.2.2:1002", "charlie", "", "", 400)

	for _, tc := range []struct {
		name, remote string
		trust        bool
		want         int
	}{
		{"trusted loopback", "127.0.0.1:1234", true, 400},
		{"proxy trust disabled", "127.0.0.1:1234", false, 429},
		{"non-loopback peer", "192.0.2.3:1234", true, 429},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newAPI(t, tc.trust)
			for range 10 {
				request(t, api, tc.remote, "", "198.51.100.3, 198.51.100.1, invalid", "", 400)
			}
			request(t, api, tc.remote, "", "198.51.100.4, 198.51.100.1", "", 429)
			request(t, api, tc.remote, "", "198.51.100.3, 198.51.100.2", "", tc.want)
			request(t, api, tc.remote, "", "invalid", "198.51.100.2", tc.want)
		})
	}
}

func TestPendingRegistration(t *testing.T) {
	ctx := t.Context()
	db, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	admin, err := db.CreateUserWithRole(ctx, "root", []byte("fixture"), "admin")
	if err != nil {
		t.Fatal(err)
	}
	_, adminToken, err := db.IssueDevice(ctx, admin.ID, "Admin")
	if err != nil {
		t.Fatal(err)
	}
	api, err := New(ctx, db, config.Config{Provider: "stub", CORSOrigin: "*"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, token, body string, status int, code string) []byte {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != status || code != "" && !strings.Contains(w.Body.String(), `"code":"`+code+`"`) {
			t.Fatalf("%s %s: got %d (%s), want %d %s", method, path, w.Code, w.Body, status, code)
		}
		return w.Body.Bytes()
	}
	const register = "/api/auth/register"
	const body = `{"username":"self","password":"test-password","deviceName":"Browser"}`
	var account struct {
		session
		Token string
	}
	if err := json.Unmarshal(call("POST", register, "", body, 201, ""), &account); err != nil {
		t.Fatal(err)
	}
	if account.Token == "" || account.Device.ID == "" || account.User.Role != "pending" || account.User.PlanTier != "none" || !account.User.IsActive {
		t.Fatalf("incorrect pending account: %+v", account.User)
	}
	for _, path := range []string{"/api/health", "/api/auth/me", "/api/devices"} {
		call("GET", path, account.Token, "", 200, "")
	}
	for _, path := range []string{"/api/chats", "/api/models", "/api/codex/chats", "/api/admin/users", "/ws"} {
		call("GET", path, account.Token, "", 403, "account_pending")
	}
	call("POST", "/api/ws/ticket", account.Token, "", 403, "account_pending")
	call("PATCH", "/api/admin/users/"+account.User.ID, adminToken, `{"role":"user","planTier":"pro"}`, 204, "")
	call("GET", "/api/chats", account.Token, "", 200, "")
	ticket := call("POST", "/api/ws/ticket", account.Token, "", 200, "")
	var ws struct{ Ticket string }
	if err := json.Unmarshal(ticket, &ws); err != nil {
		t.Fatal(err)
	}
	call("PATCH", "/api/admin/users/"+account.User.ID, adminToken, `{"role":"pending"}`, 204, "")
	call("GET", "/ws?ticket="+ws.Ticket, "", "", 403, "account_pending")
	call("POST", "/api/auth/logout", account.Token, "", 204, "")
	call("POST", register, "", body, 409, "username_taken")
	for range 3 {
		call("POST", register, "", `{}`, 400, "invalid_registration")
	}
	call("POST", register, "", body, 429, "rate_limited")
}

func TestProfileEndpoints(t *testing.T) {
	ctx := t.Context()
	db, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tokenFor := func(username, role string) string {
		user, err := db.CreateUserWithRole(ctx, username, []byte("hash"), role)
		if err != nil {
			t.Fatal(err)
		}
		_, token, err := db.IssueDevice(ctx, user.ID, "test")
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	user, admin, pending := tokenFor("user", "user"), tokenFor("admin", "admin"), tokenFor("pending", "pending")
	handler, err := New(ctx, db, config.Config{Provider: "stub", CORSOrigin: "*"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, token, body string, status int) store.UserProfile {
		t.Helper()
		r := httptest.NewRequest(method, "/api/profile", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s profile got %d want %d: %s", method, w.Code, status, w.Body.String())
		}
		var result store.UserProfile
		if status == 200 {
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
		}
		return result
	}
	request("GET", "", "", 401)
	request("GET", pending, "", 403)
	request("PATCH", pending, `{"displayName":"No"}`, 403)
	for _, token := range []string{user, admin} {
		if got := request("GET", token, "", 200); got != (store.UserProfile{MemoryEnabled: true}) {
			t.Fatalf("default: %+v", got)
		}
	}
	escaped := strings.Repeat(`\u000a`, 4096)
	body := `{"displayName":" Ярослав ","profileMd":"` + escaped + `","memoryMd":"` + escaped + `","memoryEnabled":false}`
	profile := request("PATCH", user, body, 200)
	if profile.DisplayName != "Ярослав" || len(profile.ProfileMD) != 4096 || len(profile.MemoryMD) != 4096 ||
		profile.MemoryEnabled || profile.MemoryUpdatedAt <= 0 {
		t.Fatalf("unexpected update")
	}
	request("PATCH", user, `{"profileMd":"`+strings.Repeat("я", 2049)+`"}`, 400)
	request("PATCH", user, `{"userId":"Other"}`, 400)
	if got := request("GET", user, "", 200); got != profile {
		t.Fatalf("invalid patch modified profile")
	}
	if got := request("GET", admin, "", 200); got != (store.UserProfile{MemoryEnabled: true}) {
		t.Fatalf("admin received another user's profile")
	}
	if got := request("PATCH", admin, `{"displayName":"Админ"}`, 200); got.DisplayName != "Админ" {
		t.Fatalf("admin patch: %+v", got)
	}
	if got := request("PATCH", user, `{"memoryMd":""}`, 200); got.MemoryMD != "" || got.ProfileMD != profile.ProfileMD || got.MemoryEnabled {
		t.Fatalf("clear changed other settings")
	}
}


func TestNewRejectsUnimplementedProviders(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := New(t.Context(), nil, config.Config{Provider: "stub", CORSOrigin: "*"}, logger); err != nil {
		t.Fatalf("local stub: %v", err)
	}
	for _, cli := range []string{"codex", "antigravity", "claude"} {
		cfg := config.Config{Provider: cli, CORSOrigin: "*"}
		if _, err := New(t.Context(), nil, cfg, logger); err == nil || !strings.Contains(err.Error(), "RUNNER_URL") {
			t.Fatalf("expected %s to require an isolated runner, got: %v", cli, err)
		}
		cfg.RunnerURL = "http://runner:8081"
		cfg.RunnerSharedSecret = strings.Repeat("x", 32)
		if _, err := New(t.Context(), nil, cfg, logger); err != nil {
			t.Fatalf("remote %s: %v", cli, err)
		}
	}
	if _, err := New(t.Context(), nil, config.Config{Provider: "paid_api", CORSOrigin: "*"}, logger); err == nil || !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("New(\"paid_api\") expected not implemented error, got: %v", err)
	}
	for _, unsupported := range []string{"", "unknown"} {
		if _, err := New(t.Context(), nil, config.Config{Provider: unsupported, CORSOrigin: "*"}, logger); err == nil {
			t.Fatalf("New(%q) expected error, got nil", unsupported)
		}
	}
}
