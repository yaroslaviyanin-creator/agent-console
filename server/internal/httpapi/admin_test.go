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
	api := New(ctx, db, config.Config{Provider: "stub", CORSOrigin: "*"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
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
	const create = `{"username":"alice","password":"original-pass","role":"user"}`
	var alice store.AdminUser
	request("POST", users, adminToken, create, 201, &alice)
	request("POST", users, adminToken, create, 409, nil)
	const loginAlice = `{"username":"alice","password":"original-pass","deviceName":"Telegram"}`
	aliceToken, _ := login(loginAlice, 200)
	request("GET", users, aliceToken, "", 403, nil)
	if _, err := db.CreateChat(ctx, alice.ID, "Fixture", "stub", "default"); err != nil {
		t.Fatal(err)
	}
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
	var list []store.AdminUser
	request("GET", users, adminToken, "", 200, &list)
	if len(list) != 2 || list[0].ID != alice.ID || list[0].DevicesCount != 1 || list[0].ChatsCount != 1 || list[0].CreatedAt == 0 {
		t.Fatalf("incorrect users list: %+v", list)
	}
	request("PATCH", users+"/"+admin.ID, adminToken, `{"role":"user"}`, 400, nil)
	request("PATCH", users+"/"+admin.ID, adminToken, `{"isActive":false}`, 400, nil)
	path := users + "/" + alice.ID
	var ws struct{ Ticket string }
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
		return New(t.Context(), nil, config.Config{Provider: "stub", CORSOrigin: "*", TrustProxyHeaders: trust}, slog.New(slog.NewTextHandler(io.Discard, nil)))
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
