package httpapi

import (
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
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "admin.db"))
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
	var list []store.AdminUser
	request("GET", users, adminToken, "", 200, &list)
	if len(list) != 2 || list[0].ID != alice.ID || list[0].DevicesCount != 1 || list[0].ChatsCount != 1 || list[0].CreatedAt == 0 {
		t.Fatalf("incorrect users list: %+v", list)
	}
	request("PATCH", users+"/"+admin.ID, adminToken, `{"role":"user"}`, 400, nil)
	request("PATCH", users+"/"+admin.ID, adminToken, `{"isActive":false}`, 400, nil)
	path := users + "/" + alice.ID
	request("PATCH", path, adminToken, `{"isActive":false}`, 204, nil)
	login(loginAlice, 403)
	request("GET", "/api/auth/me", aliceToken, "", 403, nil)
	request("GET", "/ws?token="+aliceToken, "", "", 403, nil)
	request("PATCH", path, adminToken, `{"isActive":true,"role":"admin","newPassword":"changed-pass"}`, 204, nil)
	login(loginAlice, 401)
	request("GET", "/api/auth/me", aliceToken, "", 401, nil)
	newToken, updated := login(`{"username":"alice","password":"changed-pass","deviceName":"Telegram"}`, 200)
	if updated.Role != "admin" || !updated.IsActive {
		t.Fatalf("incorrect patched user: %+v", updated)
	}
	request("GET", users, newToken, "", 200, nil)
	request("DELETE", path+"/devices", adminToken, "", 204, nil)
	request("GET", "/api/auth/me", newToken, "", 401, nil)
	request("PATCH", users+"/missing", adminToken, `{"role":"user"}`, 404, nil)
	request("DELETE", users+"/missing/devices", adminToken, "", 404, nil)
}
