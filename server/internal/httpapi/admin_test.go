package httpapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"agent-console/server/internal/config"
	"agent-console/server/internal/store"
	"agent-console/server/internal/uploads"
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
	request("GET", users, "", "", 404, nil)
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
	for _, method := range []string{"GET", "POST", "PATCH", "DELETE"} {
		request(method, "/api/admin", aliceToken, "", 404, nil)
		request(method, "/api/admin/users/123", aliceToken, "", 404, nil)
		request(method, users, aliceToken, "", 404, nil)
	}
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
	request("GET", chatListPath, aliceToken, "", 404, nil)
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
		User    store.User `json:"user"`
		Pending bool       `json:"pending"`
		Token   string     `json:"token"`
	}
	request("POST", "/api/auth/register", "",
		`{"username":"pending-user","password":"pending-pass","deviceName":"Browser"}`, 201, &pending)
	if pending.Token != "" || !pending.Pending {
		t.Fatalf("expected pending registration without token, got %+v", pending)
	}
	request("GET", quotaPath, pending.Token, "", 401, nil)
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
		User    store.User `json:"user"`
		Pending bool       `json:"pending"`
		Token   string     `json:"token"`
	}
	if err := json.Unmarshal(call("POST", register, "", body, 201, ""), &account); err != nil {
		t.Fatal(err)
	}
	if account.Token != "" || !account.Pending || account.User.Role != "pending" || account.User.PlanTier != "none" || !account.User.IsActive {
		t.Fatalf("incorrect pending account: %+v", account)
	}
	call("POST", register, "", body, 409, "username_taken")
	for range 3 {
		call("POST", register, "", `{}`, 400, "invalid_registration")
	}
	r429 := httptest.NewRequest(http.MethodPost, register, strings.NewReader(body))
	r429.Header.Set("Content-Type", "application/json")
	w429 := httptest.NewRecorder()
	api.ServeHTTP(w429, r429)
	if w429.Code != 429 || w429.Header().Get("Retry-After") == "" {
		t.Fatalf("expected 429 with Retry-After, got %d header=%q", w429.Code, w429.Header().Get("Retry-After"))
	}
}

func TestLogoutAllEndpoint(t *testing.T) {
	ctx := t.Context()
	db, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	hash, _ := bcrypt.GenerateFromPassword([]byte("pass12345"), bcrypt.MinCost)
	u, err := db.CreateUserWithRole(ctx, "bob", hash, "user")
	if err != nil {
		t.Fatal(err)
	}
	_, tok1, err := db.IssueDevice(ctx, u.ID, "Dev1")
	if err != nil {
		t.Fatal(err)
	}
	_, tok2, err := db.IssueDevice(ctx, u.ID, "Dev2")
	if err != nil {
		t.Fatal(err)
	}
	api := newHandler(ctx, db, config.Config{Provider: "stub"}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout-all", strings.NewReader(`{"includeCurrent":false}`))
	req.Header.Set("Authorization", "Bearer "+tok1)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	api.ServeHTTP(w, req)
	if w.Code != 204 {
		t.Fatalf("logout-all: got %d", w.Code)
	}

	// tok2 is invalidated immediately, tok1 remains valid.
	r2 := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	r2.Header.Set("Authorization", "Bearer "+tok2)
	w2 := httptest.NewRecorder()
	api.ServeHTTP(w2, r2)
	if w2.Code != 401 {
		t.Fatalf("expected revoked tok2 to get 401, got %d", w2.Code)
	}
	r1 := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	r1.Header.Set("Authorization", "Bearer "+tok1)
	w1 := httptest.NewRecorder()
	api.ServeHTTP(w1, r1)
	if w1.Code != 200 {
		t.Fatalf("expected current tok1 to stay 200, got %d", w1.Code)
	}
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
		if got := request("GET", token, "", 200); !reflect.DeepEqual(got, store.UserProfile{MemoryEnabled: true, UISettings: json.RawMessage("{}")}) {
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
	request("PATCH", user, `{"uiSettings":{"accentCustomColor":"not-hex"}}`, 400)
	if got := request("GET", user, "", 200); !reflect.DeepEqual(got, profile) {
		t.Fatalf("invalid patch modified profile")
	}
	if got := request("GET", admin, "", 200); !reflect.DeepEqual(got, store.UserProfile{MemoryEnabled: true, UISettings: json.RawMessage("{}")}) {
		t.Fatalf("admin received another user's profile")
	}
	uiUpdated := request("PATCH", user, `{"uiSettings":{"theme":"light","accent":"ocean"}}`, 200)
	if !strings.Contains(string(uiUpdated.UISettings), `"theme":"light"`) {
		t.Fatalf("expected uiSettings persisted, got %s", uiUpdated.UISettings)
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

func TestAdminDeleteUser(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	admin, err := db.CreateUserWithRole(ctx, "admin", []byte("hash"), "admin")
	if err != nil {
		t.Fatal(err)
	}
	_, adminToken, err := db.IssueDevice(ctx, admin.ID, "admin-dev")
	if err != nil {
		t.Fatal(err)
	}
	user, err := db.CreateUserWithRole(ctx, "alice", []byte("hash"), "user")
	if err != nil {
		t.Fatal(err)
	}
	_, userToken, err := db.IssueDevice(ctx, user.ID, "alice-dev")
	if err != nil {
		t.Fatal(err)
	}
	pending, err := db.CreateUserWithRole(ctx, "bob", []byte("hash"), "pending")
	if err != nil {
		t.Fatal(err)
	}

	chat, err := db.CreateChat(ctx, user.ID, "Alice Chat", "stub", "stub-model")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(db.UploadsDir(), 0700); err != nil {
		t.Fatal(err)
	}
	storageKey := "alice-file.txt"
	diskPath := filepath.Join(db.UploadsDir(), storageKey)
	if err := os.WriteFile(diskPath, []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	att, err := db.CreateAttachment(ctx, chat.ID, user.ID, uploads.ValidatedAttachment{
		Filename: "hello.txt",
		MIMEType: "text/plain",
		Kind:     uploads.KindText,
		Size:     5,
		Data:     []byte("hello"),
	})
	if err != nil {
		t.Fatal(err)
	}
	diskPath = filepath.Join(db.UploadsDir(), att.StorageKey)
	if _, err := os.Stat(diskPath); err != nil {
		t.Fatalf("expected attachment file %s to exist before delete: %v", diskPath, err)
	}

	handler, err := New(ctx, db, config.Config{Provider: "stub", CORSOrigin: "*"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	call := func(method, path, token string, want int) {
		t.Helper()
		r := httptest.NewRequest(method, path, nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s got %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
	}

	// Non-admin receives 404
	call("DELETE", "/api/admin/users/"+pending.ID, userToken, 404)

	// Self-delete returns 400
	call("DELETE", "/api/admin/users/"+admin.ID, adminToken, 400)

	// Last active admin cannot be deleted
	if err := db.DeleteAdminUser(ctx, admin.ID); !errors.Is(err, store.ErrLastAdmin) {
		t.Fatalf("DeleteAdminUser(last admin) got %v want ErrLastAdmin", err)
	}

	// Delete pending user -> 204
	call("DELETE", "/api/admin/users/"+pending.ID, adminToken, 204)
	call("DELETE", "/api/admin/users/"+pending.ID, adminToken, 404)

	// Delete regular user -> 204, cleans up chats, devices, and disk attachment
	call("DELETE", "/api/admin/users/"+user.ID, adminToken, 204)
	if _, err := os.Stat(diskPath); !os.IsNotExist(err) {
		t.Fatalf("expected attachment file %s to be deleted, err=%v", diskPath, err)
	}
	call("GET", "/api/chats", userToken, 401)
	if devices, err := db.UserDevices(ctx, user.ID); err != nil || len(devices) != 0 {
		t.Fatalf("expected deleted user's devices to be gone, got %+v (%v)", devices, err)
	}
	call("DELETE", "/api/admin/users/"+user.ID, adminToken, 404)
}
